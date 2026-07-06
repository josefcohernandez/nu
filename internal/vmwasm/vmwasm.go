// Package vmwasm es el backend de VM de nu basado en PUC-Lua oficial compilado
// a WebAssembly y ejecutado sobre wazero (Go puro, CGO_ENABLED=0). Materializa
// [ADR-019]; el plan por sesiones vive en docs/migracion-vm.md.
//
// Esta sesión (M02) provee el CIMIENTO: el blob nu.wasm embebido, un Pool que lo
// compila una vez e instancia N veces (aislamiento físico de memoria por
// instancia — la base de los workers de M12), el trampolín de desenrollado
// Snapshot/Restore (que M03 endurece) y la costura de dispatch host genérica
// (que M05 rellena con el marshaling y las primitivas nu.*). Todavía NO hay
// scheduler, ni nu.*, ni integración con el Runtime del kernel: eso es M04+.
package vmwasm

import (
	"context"
	_ "embed"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// nuWasm es el blob del intérprete: PUC-Lua 5.4.7 + el shim del kernel,
// reproducible con internal/vmwasm/build.sh (DM1: se comitea; un job de CI
// verifica su hash contra las fuentes).
//
//go:embed nu.wasm
var nuWasm []byte

// Dispatcher es la costura de llamada Lua→Go: recibe el id de una primitiva y
// sus argumentos ya copiados de la memoria wasm, y devuelve el resultado (bytes)
// o un error. M05 implementa el marshaling y el catálogo de ids encima. En M02
// el dispatcher por defecto rechaza todo (aún no hay primitivas).
type Dispatcher func(id int32, args []byte) ([]byte, error)

// Pool compila nu.wasm una sola vez y fabrica instancias que comparten el
// módulo compilado pero NO la memoria (cada Instance tiene su estado Lua).
type Pool struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
}

// NewPool prepara el runtime wazero, registra el módulo host "nu" (el trampolín
// y el dispatch) y compila el blob. El módulo host se registra una vez; las
// funciones enrutan a la Instance correcta por el valor del contexto (cada
// instancia llama a sus exports con su propio ctx).
func NewPool() (*Pool, error) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	if _, err := rt.NewHostModuleBuilder("nu").
		NewFunctionBuilder().WithFunc(hostTry).Export("host_try").
		NewFunctionBuilder().WithFunc(hostThrow).Export("host_throw").
		NewFunctionBuilder().WithFunc(hostDispatch).Export("host_dispatch").
		Instantiate(ctx); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("vmwasm: registrar módulo host: %w", err)
	}

	compiled, err := rt.CompileModule(ctx, nuWasm)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("vmwasm: compilar nu.wasm: %w", err)
	}
	return &Pool{rt: rt, compiled: compiled}, nil
}

// Close libera el runtime y todas sus instancias.
func (p *Pool) Close() error { return p.rt.Close(context.Background()) }

// instanceKey identifica a la Instance en el contexto para que las funciones
// host (compartidas por el módulo) enruten al estado correcto.
type instanceKey struct{}

func instanceOf(ctx context.Context) *Instance { return ctx.Value(instanceKey{}).(*Instance) }

// tryFrame guarda el checkpoint de un LUAI_TRY activo: el snapshot del motor
// wazero y el __stack_pointer (shadow stack de C, que el snapshot no cubre).
type tryFrame struct {
	snap experimental.Snapshot
	sp   uint64
}

// Instance es un estado Lua aislado (su propia memoria lineal). No es seguro
// para uso concurrente: como el estado principal de nu, un solo hilo lo maneja
// (el scheduler de M06 lo serializa; un worker de M12 tiene su propia Instance).
type Instance struct {
	pool *Pool
	mod  api.Module
	ctx  context.Context

	tries      []tryFrame // pila LIFO de LUAI_TRY activos
	dispatch   Dispatcher
	bufPtr     uint32
	bufCap     uint32
	callPfunc  api.Function
	evalFn     api.Function
	coSpawnFn  api.Function
	coResumeFn api.Function
	resultLen  api.Function

	mu sync.Mutex // sólo protege contra reentrada accidental en tests, no concurrencia real
}

// NewInstance instancia el módulo compilado (memoria fresca), cablea el
// contexto (snapshotter + puntero a la Instance) y crea el estado Lua con las
// libs del baseline. El dispatcher arranca en el que rechaza todo; SetDispatcher
// lo sustituye (M05).
func (p *Pool) NewInstance() (*Instance, error) {
	inst := &Instance{pool: p, dispatch: rejectDispatcher}
	// ctx con el snapshotter (lo exige el trampolín) y la propia instancia.
	base := experimental.WithSnapshotter(context.Background())
	inst.ctx = context.WithValue(base, instanceKey{}, inst)

	mod, err := p.rt.InstantiateModule(inst.ctx, p.compiled,
		wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("vmwasm: instanciar: %w", err)
	}
	inst.mod = mod
	inst.callPfunc = mod.ExportedFunction("nu_call_pfunc")
	inst.evalFn = mod.ExportedFunction("nu_eval")
	inst.coSpawnFn = mod.ExportedFunction("nu_co_spawn")
	inst.coResumeFn = mod.ExportedFunction("nu_co_resume")
	inst.resultLen = mod.ExportedFunction("nu_result_len")

	r, err := mod.ExportedFunction("nu_buf").Call(inst.ctx)
	if err != nil {
		return nil, err
	}
	inst.bufPtr = uint32(r[0])
	if r, err = mod.ExportedFunction("nu_buf_cap").Call(inst.ctx); err != nil {
		return nil, err
	}
	inst.bufCap = uint32(r[0])

	if r, err = mod.ExportedFunction("nu_new").Call(inst.ctx); err != nil || r[0] != 0 {
		return nil, fmt.Errorf("vmwasm: nu_new: r=%v err=%w", r, err)
	}
	return inst, nil
}

// SetDispatcher instala el manejador de primitivas host (M05).
func (inst *Instance) SetDispatcher(d Dispatcher) { inst.dispatch = d }

// Close destruye el estado (su memoria muere con el módulo).
func (inst *Instance) Close() error { return inst.mod.Close(context.Background()) }

// writeBuf copia datos al buffer compartido; devuelve error si no cabe.
func (inst *Instance) writeBuf(data []byte) error {
	if uint32(len(data)) > inst.bufCap-1 {
		return fmt.Errorf("vmwasm: payload de %d bytes excede el buffer (%d)", len(data), inst.bufCap)
	}
	if !inst.mod.Memory().Write(inst.bufPtr, data) {
		return fmt.Errorf("vmwasm: escritura fuera de la memoria wasm")
	}
	return nil
}

// readResult lee el buffer hasta la longitud que reportó el último export.
func (inst *Instance) readResult() string {
	r, err := inst.resultLen.Call(inst.ctx)
	if err != nil || r[0] == 0 {
		return ""
	}
	b, _ := inst.mod.Memory().Read(inst.bufPtr, uint32(r[0]))
	return string(b)
}

// Eval carga y corre un chunk protegido. Devuelve (resultado, errorLua, errorGo):
// errorLua != "" es un error de Lua capturado (el chunk lanzó); errorGo != nil
// es un fallo duro del motor (trap wasm). Sólo uno es no-cero.
func (inst *Instance) Eval(chunk string) (string, string, error) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if err := inst.writeBuf([]byte(chunk)); err != nil {
		return "", "", err
	}
	r, err := inst.evalFn.Call(inst.ctx, uint64(len(chunk)))
	if err != nil {
		return "", "", err
	}
	if r[0] != 0 {
		return "", inst.readResult(), nil
	}
	return inst.readResult(), "", nil
}

// CoStatus es el resultado de reanudar una corrutina.
type CoStatus int

const (
	CoDone  CoStatus = iota // terminó
	CoYield                 // suspendida (⏸)
	CoError                 // lanzó
)

// CoSpawn crea una corrutina desde `chunk`. Devuelve su ref (>0) para reanudarla.
func (inst *Instance) CoSpawn(chunk string) (int32, error) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if err := inst.writeBuf([]byte(chunk)); err != nil {
		return 0, err
	}
	r, err := inst.coSpawnFn.Call(inst.ctx, uint64(len(chunk)))
	if err != nil {
		return 0, err
	}
	if int32(r[0]) < 0 {
		return 0, fmt.Errorf("vmwasm: la corrutina no compila: %s", inst.readResult())
	}
	return int32(r[0]), nil
}

// CoResume reanuda la corrutina `ref` con un string opcional (nil = sin arg).
// Devuelve el estado y el payload (lo yieldeado, el resultado final o el error).
func (inst *Instance) CoResume(ref int32, arg *string) (CoStatus, string, error) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	alen := int64(-1)
	if arg != nil {
		if err := inst.writeBuf([]byte(*arg)); err != nil {
			return CoError, "", err
		}
		alen = int64(len(*arg))
	}
	r, err := inst.coResumeFn.Call(inst.ctx, uint64(uint32(ref)), uint64(uint32(int32(alen))))
	if err != nil {
		return CoError, "", err
	}
	return CoStatus(r[0]), inst.readResult(), nil
}

// --- funciones host (módulo "nu", compartidas; enrutan por ctx) --------------

// hostTry implementa LUAI_TRY: toma un snapshot, corre el cuerpo protegido
// re-entrando en wasm; un LUAI_THROW vuelve aquí como Restore (retorna 1). Un
// error del Call que no sea un throw es un trap real y se propaga.
func hostTry(ctx context.Context, L, f, ud int32) int32 {
	inst := instanceOf(ctx)
	sp := inst.mod.ExportedGlobal("__stack_pointer")
	inst.tries = append(inst.tries, tryFrame{
		snap: experimental.GetSnapshotter(ctx).Snapshot(),
		sp:   sp.Get(),
	})
	depth := len(inst.tries)
	// Función FRESCA por llamada: api.Function no es reentrante (lección del
	// spike; M03 lo blinda con un test).
	_, callErr := inst.mod.ExportedFunction("nu_call_pfunc").
		Call(ctx, uint64(uint32(L)), uint64(uint32(f)), uint64(uint32(ud)))
	if callErr != nil {
		panic(callErr)
	}
	if len(inst.tries) != depth {
		panic(fmt.Sprintf("vmwasm: trampolín desbalanceado (%d != %d)", len(inst.tries), depth))
	}
	inst.tries = inst.tries[:depth-1]
	return 0
}

// hostThrow implementa LUAI_THROW: restaura el shadow-stack y hace Restore del
// try más interno (no retorna; reescribe el estado de ejecución del motor).
func hostThrow(ctx context.Context) {
	inst := instanceOf(ctx)
	if len(inst.tries) == 0 {
		panic("vmwasm: LUAI_THROW sin LUAI_TRY activo")
	}
	top := inst.tries[len(inst.tries)-1]
	inst.tries = inst.tries[:len(inst.tries)-1]
	inst.mod.ExportedGlobal("__stack_pointer").(api.MutableGlobal).Set(top.sp)
	top.snap.Restore([]uint64{1})
}

// hostDispatch implementa la costura Lua→Go: lee `len` bytes de args del buffer,
// llama al dispatcher de la instancia, y escribe el resultado de vuelta.
// Devuelve la longitud del resultado (>=0) o -1 en error (el mensaje va al buffer).
func hostDispatch(ctx context.Context, id, length int32) int32 {
	inst := instanceOf(ctx)
	args, ok := inst.mod.Memory().Read(inst.bufPtr, uint32(length))
	if !ok {
		return -1
	}
	// copia defensiva: el dispatcher puede tardar y el buffer es reutilizable
	argsCopy := make([]byte, len(args))
	copy(argsCopy, args)
	out, err := inst.dispatch(id, argsCopy)
	if err != nil {
		msg := []byte(err.Error())
		_ = inst.writeBuf(msg)
		// escribe la longitud del mensaje vía el mismo buffer no es posible aquí;
		// el protocolo de error detallado lo fija M05. En M02, error = -1 + msg en BUF.
		return -1
	}
	if err := inst.writeBuf(out); err != nil {
		return -1
	}
	return int32(len(out))
}

// rejectDispatcher es el dispatcher por defecto: no hay primitivas hasta M05.
func rejectDispatcher(id int32, args []byte) ([]byte, error) {
	return nil, fmt.Errorf("vmwasm: sin dispatcher de primitivas (id=%d); se instala en M05", id)
}
