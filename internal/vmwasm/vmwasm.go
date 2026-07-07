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
	"time"

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
// módulo compilado pero NO la memoria (cada Instance tiene su estado Lua). El
// registro de primitivas (reg) también se comparte: las primitivas nu.* son las
// mismas para todas las instancias (el estado por-instancia lo lleva el *Instance
// que el HostFn recibe).
type Pool struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	reg      *hostRegistry
	ui       UIBackend // backend de compositor (M11); nil = headless (G20)

	isWorker      bool              // true en el Pool de un worker (M12): preludio sin ui/events/spawn
	modules       map[string]string // fuentes de módulo por nombre para require (M13, DM5)
	apiVersion    int               // nivel de nu.version.api que inyecta el preludio (M13, lo fija el Runtime)
	verMajor      int               // nu.version.major/minor/patch (api.md §1); los fija el Runtime
	verMinor      int
	verPatch      int
	extraPreludio []string          // snippets Lua que aporta el catálogo (M13b: wrappers finos)
	sliceBudget   time.Duration     // presupuesto por slice del watchdog (DM4); ≤0 lo desactiva (workers, G15)

	// Registro de workers vivos de este Pool (M12), para _send/_recv/_terminate y
	// para el apagado ordenado. Sólo lo usa el Pool principal.
	workerMu   sync.Mutex
	workers    map[int64]*worker
	workerNext int64
}

// El runtime wazero y el módulo compilado se COMPARTEN a nivel de proceso: el
// blob nu.wasm se compila con el JIT UNA sola vez (páginas ejecutables mmap
// caras), no por Pool. Es lo que el doc del Pool promete ("compila una vez") y
// lo que evita el OOM de crear N runtimes JIT (p. ej. en la suite de tests).
// Distintos Pools comparten runtime+módulo pero tienen su propio registro de
// primitivas; las funciones host enrutan a la Instance correcta por el ctx, y de
// ahí a su Pool y su registro. El motor COMPILADOR es obligatorio: el trampolín
// (M03) depende de Snapshot/Restore, que el intérprete NO reproduce igual
// (hallazgo M10) — un dato de contención más para el veto de M15.
var (
	sharedOnce     sync.Once
	sharedRT       wazero.Runtime
	sharedCompiled wazero.CompiledModule
	sharedErr      error
)

func sharedRuntime() (wazero.Runtime, wazero.CompiledModule, error) {
	sharedOnce.Do(func() {
		ctx := context.Background()
		// WithCloseOnContextDone: wazero comprueba la cancelación del ctx del Call
		// e interrumpe un Call en vuelo cuando el ctx se cancela. Es lo que permite
		// que worker:terminate corte un bucle de CPU puro (`while true do end`) sin
		// watchdog (M12) y la base del watchdog por época (DM4). El trampolín
		// Snapshot/Restore sigue funcionando con este config (comprobado). Cerrar el
		// módulo NO basta: no interrumpe un Call ya en curso.
		sharedRT = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
		wasi_snapshot_preview1.MustInstantiate(ctx, sharedRT)
		if _, err := sharedRT.NewHostModuleBuilder("nu").
			NewFunctionBuilder().WithFunc(hostTry).Export("host_try").
			NewFunctionBuilder().WithFunc(hostThrow).Export("host_throw").
			NewFunctionBuilder().WithFunc(hostDispatch).Export("host_dispatch").
			NewFunctionBuilder().WithFunc(hostOverBudget).Export("nu_over_budget").
			Instantiate(ctx); err != nil {
			sharedErr = fmt.Errorf("vmwasm: registrar módulo host: %w", err)
			return
		}
		sharedCompiled, sharedErr = sharedRT.CompileModule(ctx, nuWasm)
		if sharedErr != nil {
			sharedErr = fmt.Errorf("vmwasm: compilar nu.wasm: %w", sharedErr)
		}
	})
	return sharedRT, sharedCompiled, sharedErr
}

// newBarePool crea un Pool sobre el runtime compartido, sin más primitivas que
// las que registre el llamante. Base común del Pool principal y del de un worker.
func newBarePool() (*Pool, error) {
	rt, compiled, err := sharedRuntime()
	if err != nil {
		return nil, err
	}
	p := &Pool{rt: rt, compiled: compiled, reg: newHostRegistry(), workers: make(map[int64]*worker), modules: make(map[string]string)}
	p.registerLoader()   // require curado (M13): presente en todo Pool, también en workers
	p.registerWatchdog() // __reset_budget del watchdog (DM4): presente en todo Pool
	return p, nil
}

// NewPool prepara el Pool PRINCIPAL: despacho de handles (M10) y el host de
// workers (M12: nu.worker.spawn y sus primitivas). El backend de UI se añade
// aparte con SetUIBackend (M11).
func NewPool() (*Pool, error) {
	p, err := newBarePool()
	if err != nil {
		return nil, err
	}
	p.registerHandleDispatch() // primitivas genéricas de despacho de métodos (M10)
	p.registerWorkerHost()     // nu.worker.spawn/_send/_recv/_terminate (M12)
	return p, nil
}

// SetAPIVersion fija el nivel de nu.version.api que el preludio expone. Lo llama
// el Runtime al construir el estado wasm (M13), con su APILevel. Debe llamarse
// antes de NewInstance (el preludio se arma con él).
func (p *Pool) SetAPIVersion(v int) { p.apiVersion = v }

// SetVersion fija major/minor/patch de nu.version (api.md §1), además del api que
// pone SetAPIVersion. Lo llama el Runtime con sus constantes de versión. Debe
// llamarse antes de NewInstance.
func (p *Pool) SetVersion(major, minor, patch int) {
	p.verMajor, p.verMinor, p.verPatch = major, minor, patch
}

// SetSliceBudget fija el presupuesto por slice del watchdog (DM4, api.md §1.3):
// el tiempo máximo que una task puede correr Lua de forma continua sin ceder
// antes de que el count-hook la aborte con EBUDGET (no capturable). Lo llama el
// Runtime con su `sliceBudget` (100 ms por defecto). Un valor ≤0 DESACTIVA el
// watchdog —igual que en gopher— y es lo que usan los workers (G15: un worker es
// un mini-runtime cuyo trabajo es quemar CPU). Debe llamarse antes de NewInstance.
func (p *Pool) SetSliceBudget(d time.Duration) { p.sliceBudget = d }

// AddPreludio añade un snippet Lua que se ejecuta al final del preludio, cuando la
// tabla `nu` ya está montada. Es el punto de extensión con el que un módulo del
// catálogo (M13b) aporta un wrapper fino en Lua —p. ej. nu.re.compile, que ensambla
// la tabla mixta de capturas que el wire no puede cruzar de una pieza—. Debe
// llamarse antes de NewInstance.
func (p *Pool) AddPreludio(snippet string) { p.extraPreludio = append(p.extraPreludio, snippet) }

// Close libera las instancias del Pool. El runtime wazero es compartido a nivel
// de proceso y no se cierra aquí (vive lo que el proceso); las instancias se
// cierran individualmente con Instance.Close.
func (p *Pool) Close() error { return nil }

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
	pool   *Pool
	mod    api.Module
	ctx    context.Context
	cancel context.CancelFunc // cancela el ctx del Call (worker:terminate, M12)

	tries       []tryFrame // pila LIFO de LUAI_TRY activos
	dispatch    Dispatcher
	bufPtr      uint32
	bufCap      uint32
	callPfunc   api.Function
	evalFn      api.Function
	coSpawnFn   api.Function
	coResumeFn  api.Function
	resultLen   api.Function
	schedStepFn api.Function // nu_sched_step, perezoso (M06)
	handles     *handleTable // tabla de objetos vivos tras los handles (M10, C5)

	dispatchHandle Handle           // handle en despacho síncrono (M11: self-free)
	pendingInput   []map[string]any // cola de eventos de input crudos (M11, FeedInput)
	workerChans    *workerChannels  // canales con el padre, si esta Instance es un worker (M12)

	// Watchdog por slice (DM4). `sliceBudget` (copiado del Pool en NewInstance) es
	// el presupuesto; `taskDeadline` lo fija `__reset_budget` ANTES de reanudar cada
	// task, y lo lee `nu_over_budget` (el import del count-hook). Ambos los toca sólo
	// el goroutine que conduce el Call —síncrono, sin goroutine de fondo—: race-free.
	sliceBudget  time.Duration
	taskDeadline time.Time

	mu sync.Mutex // sólo protege contra reentrada accidental en tests, no concurrencia real
}

// NewInstance instancia el módulo compilado (memoria fresca), cablea el
// contexto (snapshotter + puntero a la Instance) y crea el estado Lua con las
// libs del baseline. El dispatcher arranca en el que rechaza todo; SetDispatcher
// lo sustituye (M05).
func (p *Pool) NewInstance() (*Instance, error) {
	inst := &Instance{pool: p, handles: newHandleTable(), sliceBudget: p.sliceBudget}
	// ctx con el snapshotter (lo exige el trampolín), cancelable (worker:terminate,
	// M12) y con la propia instancia.
	base := experimental.WithSnapshotter(context.Background())
	cctx, cancel := context.WithCancel(base)
	inst.cancel = cancel
	inst.ctx = context.WithValue(cctx, instanceKey{}, inst)

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

	// El dispatcher real es el registro de primitivas (M05): resuelve id→HostFn,
	// (de)serializa el wire y cruza errores estructurados. SetDispatcher aún puede
	// sustituirlo (lo usa algún test de la costura).
	inst.dispatch = inst.dispatchPrimitive

	// Preludio: construye la tabla `nu` (thunks sobre las primitivas registradas) y
	// el codec de wire en Lua. Se corre una vez, con el catálogo ya completo.
	if _, lerr, err := inst.Eval(p.preludio()); err != nil || lerr != "" {
		return nil, fmt.Errorf("vmwasm: preludio: lerr=%q err=%w", lerr, err)
	}
	return inst, nil
}

// SetDispatcher instala el manejador de primitivas host (M05).
func (inst *Instance) SetDispatcher(d Dispatcher) { inst.dispatch = d }

// Close destruye el estado (su memoria muere con el módulo) y cancela su ctx.
func (inst *Instance) Close() error {
	if inst.cancel != nil {
		inst.cancel()
	}
	return inst.mod.Close(context.Background())
}

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

// hostOverBudget implementa el import `nu_over_budget` que el count-hook del
// watchdog (DM4, nu_shim.c) invoca cada WD_COUNT instrucciones de una task.
// Devuelve 1 si el slice en curso rebasó su deadline —el hook cede entonces (0
// valores) y el scheduler Lua aborta la task con EBUDGET no capturable—, o 0 si
// aún tiene presupuesto. Un `sliceBudget <= 0` desactiva el watchdog (siempre 0),
// como en gopher y como en los workers (G15).
//
// RACE-FREE: `taskDeadline` lo fija `__reset_budget` en el MISMO goroutine que
// conduce el Call, ANTES de reanudar la task; este import se invoca síncronamente
// DENTRO de ese Call (el hook corre en el hilo Lua, no en una goroutine de fondo).
// Ninguna goroutine de `performRequest` toca estos campos ni la memoria wasm.
func hostOverBudget(ctx context.Context) int32 {
	inst := instanceOf(ctx)
	if inst.sliceBudget <= 0 {
		return 0 // watchdog desactivado
	}
	if time.Now().After(inst.taskDeadline) {
		return 1
	}
	return 0
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
