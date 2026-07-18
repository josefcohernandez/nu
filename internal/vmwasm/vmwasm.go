// Package vmwasm es el backend de VM de enu basado en PUC-Lua oficial compilado
// a WebAssembly y ejecutado sobre wazero (Go puro, CGO_ENABLED=0). Materializa
// [ADR-019]; el plan por sesiones vive en docs/archive/migracion-vm.md.
//
// Esta sesión (M02) provee el CIMIENTO: el blob enu.wasm embebido, un Pool que lo
// compila una vez e instancia N veces (aislamiento físico de memoria por
// instancia — la base de los workers de M12), el trampolín de desenrollado
// Snapshot/Restore (que M03 endurece) y la costura de dispatch host genérica
// (que M05 rellena con el marshaling y las primitivas enu.*). Todavía NO hay
// scheduler, ni enu.*, ni integración con el Runtime del kernel: eso es M04+.
package vmwasm

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// enuWasm es el blob del intérprete: PUC-Lua 5.4.7 + el shim del kernel,
// reproducible con internal/vmwasm/build.sh (DM1: se comitea; un job de CI
// verifica su hash contra las fuentes).
//
//go:embed enu.wasm
var enuWasm []byte

// Dispatcher es la costura de llamada Lua→Go: recibe el id de una primitiva y
// sus argumentos ya copiados de la memoria wasm, y devuelve el resultado (bytes)
// o un error. M05 implementa el marshaling y el catálogo de ids encima. En M02
// el dispatcher por defecto rechaza todo (aún no hay primitivas).
type Dispatcher func(id int32, args []byte) ([]byte, error)

// Pool compila enu.wasm una sola vez y fabrica instancias que comparten el
// módulo compilado pero NO la memoria (cada Instance tiene su estado Lua). El
// registro de primitivas (reg) también se comparte: las primitivas enu.* son las
// mismas para todas las instancias (el estado por-instancia lo lleva el *Instance
// que el HostFn recibe).
type Pool struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	reg      *hostRegistry
	ui       UIBackend // backend de compositor (M11); nil = headless (G20)

	isWorker bool              // true en el Pool de un worker (M12): preludio sin ui/events/spawn
	modules  map[string]string // fuentes de módulo por nombre para require (M13, DM5)

	// ownerSnapshot es el resolvedor del dueño VIGENTE del estado principal (G56,
	// ADR-024): lo fija el Runtime con SetOwnerSnapshot (típicamente rt.currentOwner).
	// enu.worker._spawn —HostFn SÍNCRONA, en la goroutine que conduce la VM, donde la
	// pila de dueños es coherente por construcción (ADR-004)— lo llama para tomar la
	// FOTO del dueño del worker en el momento del spawn. Sólo lo tiene el Pool
	// principal (un worker no anida workers, P11); nil en Pools sin Runtime (tests de
	// bajo nivel). No se lee jamás desde la goroutine de un worker.
	ownerSnapshot func() string
	apiVersion    int // nivel de enu.version.api que inyecta el preludio (M13, lo fija el Runtime)
	verMajor      int // enu.version.major/minor/patch (api.md §1); los fija el Runtime
	verMinor      int
	verPatch      int
	extraPreludio []preludioSnippet // snippets Lua que aporta el catálogo (M13b: wrappers finos)
	sliceBudget   time.Duration     // presupuesto por slice del watchdog (DM4); ≤0 lo desactiva (workers, G15)

	// Registro de workers vivos de este Pool (M12), para _send/_recv/_terminate y
	// para el apagado ordenado. Sólo lo usa el Pool principal.
	workerMu   sync.Mutex
	workers    map[int64]*worker
	workerNext int64
}

// El runtime wazero y el módulo compilado se COMPARTEN a nivel de proceso: el
// blob enu.wasm se compila con el JIT UNA sola vez (páginas ejecutables mmap
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
		cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
		// Caché de compilación en disco (M15, veto de experiencia): el JIT de enu.wasm
		// —páginas ejecutables mmap— es el grueso del arranque en FRÍO (~230 ms). Cada
		// invocación del binario es un proceso nuevo, así que sin caché se recompila
		// siempre. Persistirla hace que a partir del 2º arranque se cargue el módulo YA
		// compilado (arranque en CALIENTE ~= gopher). wazero la indexa por hash del
		// contenido + su versión, así que es segura entre versiones y binarios. Si el
		// dir de caché no se puede preparar, se sigue sin caché (solo más lento).
		if cache := wasmCompilationCache(); cache != nil {
			cfg = cfg.WithCompilationCache(cache)
		}
		sharedRT = wazero.NewRuntimeWithConfig(ctx, cfg)
		wasi_snapshot_preview1.MustInstantiate(ctx, sharedRT)
		if _, err := sharedRT.NewHostModuleBuilder("enu").
			NewFunctionBuilder().WithFunc(hostTry).Export("host_try").
			NewFunctionBuilder().WithFunc(hostThrow).Export("host_throw").
			NewFunctionBuilder().WithFunc(hostDispatch).Export("host_dispatch").
			NewFunctionBuilder().WithFunc(hostOverBudget).Export("nu_over_budget").
			Instantiate(ctx); err != nil {
			sharedErr = fmt.Errorf("vmwasm: registrar módulo host: %w", err)
			return
		}
		sharedCompiled, sharedErr = sharedRT.CompileModule(ctx, enuWasm)
		if sharedErr != nil {
			sharedErr = fmt.Errorf("vmwasm: compilar enu.wasm: %w", sharedErr)
		}
	})
	return sharedRT, sharedCompiled, sharedErr
}

// wasmCompilationCache prepara la caché de compilación en disco (M15). Devuelve nil
// si el directorio de caché no se puede preparar —el arranque sigue, solo recompila—.
// Usa NU_WASM_CACHE si está fijada (los tests la aíslan a un tmpdir); si no, el dir de
// caché del usuario (`os.UserCacheDir()/enu/wasm`).
func wasmCompilationCache() wazero.CompilationCache {
	dir := os.Getenv("NU_WASM_CACHE")
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(base, "enu", "wasm")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	cache, err := wazero.NewCompilationCacheWithDir(dir)
	if err != nil {
		return nil
	}
	return cache
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
	p.registerGlobals()  // __pending_gname/gval de SetGlobalString (M13d)
	return p, nil
}

// registerGlobals expone las dos getters síncronas que SetGlobalString usa para
// pasar un global Lua desde Go sin interpolar código: __pending_gname devuelve el
// nombre y __pending_gval el valor de la ranura de un solo hueco de la Instance. La
// Eval de SetGlobalString hace `_G[enu.__pending_gname()] = enu.__pending_gval()`.
func (p *Pool) registerGlobals() {
	p.Register("__pending_gname", func(inst *Instance, _ []any) ([]any, error) {
		return []any{inst.pendingGName}, nil
	})
	p.Register("__pending_gval", func(inst *Instance, _ []any) ([]any, error) {
		return []any{inst.pendingGVal}, nil
	})
	p.Register("__pending_ename", func(inst *Instance, _ []any) ([]any, error) {
		return []any{inst.pendingEName}, nil
	})
	p.Register("__pending_epayload", func(inst *Instance, _ []any) ([]any, error) {
		return []any{inst.pendingEPayload}, nil
	})
}

// EmitEvent emite `name` con `payload` en el bus enu.events del estado principal, sin
// interpolar (ranura de un hueco + getters + una Eval). Es la vía por la que los
// eventos ui:* del core (que el driver de TTY observa: resize, focus, suspend/resume)
// llegan al bus wasm, paridad con rt.sched.emit del backend gopher. `payload` puede
// ser nil (evento sin datos). No suspende: emit es síncrono (G10).
func (inst *Instance) EmitEvent(name string, payload map[string]any) error {
	inst.slotMu.Lock()
	defer inst.slotMu.Unlock()
	inst.mu.Lock()
	inst.pendingEName, inst.pendingEPayload = name, payload
	inst.mu.Unlock()
	_, lerr, err := inst.Eval("enu.events.emit(enu.__pending_ename(), enu.__pending_epayload())")
	if err != nil {
		return err
	}
	if lerr != "" {
		return fmt.Errorf("vmwasm: EmitEvent(%q): %s", name, lerr)
	}
	return nil
}

// QuitSignal devuelve el canal que se CIERRA al emitirse el primer core:shutdown en
// esta Instance (G58). Lo escucha el bucle del driver de TTY (driver.go) en su
// `select`, junto al canal de input, para despertar cuando el apagado nace de una
// task de fondo (`/quit`) en vez de un keymap síncrono. Sólo-lectura: quien lo cierra
// es SignalQuit.
func (inst *Instance) QuitSignal() <-chan struct{} { return inst.quitSignal }

// SignalQuit cierra `quitSignal` una sola vez (idempotente, G58). La invoca la
// primitiva interna __driver_notify_quit desde el handler de core:shutdown del
// driver; también es segura de llamar por más de una vía (una señal + una task que
// emiten a la vez) gracias a quitOnce.
func (inst *Instance) SignalQuit() { inst.quitOnce.Do(func() { close(inst.quitSignal) }) }

// WithLock ejecuta `fn` con el mutex de la Instance tomado —el mismo que serializa
// los Call a la VM (Eval/schedStep)—. Es la vía por la que el PINTOR (armPainter) y
// el driver de TTY leen el compositor de forma EXCLUYENTE con las host functions de
// enu.ui que lo mutan durante un Call: en gopher el token del scheduler serializa
// pintor y mutaciones; en wasm ese papel lo hace este mutex. `fn` no debe re-entrar
// la VM (no llamar a Eval), sólo tocar estado Go compartido (el compositor).
func (inst *Instance) WithLock(fn func()) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	fn()
}

// SetGlobalString fija el global Lua `name` al string `value` en el estado principal
// de la Instance, SIN interpolar `value` (ni `name`) en código Lua —lo que abriría
// una inyección con un valor que llevara comillas o saltos—. Es la contraparte wasm
// de rt.L.SetGlobal(name, LString(value)) del backend gopher: la usa el binario para
// pasar sus args CLI al driver Lua y el arnés de tests para inyectar constantes.
func (inst *Instance) SetGlobalString(name, value string) error {
	inst.slotMu.Lock()
	defer inst.slotMu.Unlock()
	inst.mu.Lock()
	inst.pendingGName, inst.pendingGVal = name, value
	inst.mu.Unlock()
	_, lerr, err := inst.Eval("_G[enu.__pending_gname()] = enu.__pending_gval()")
	if err != nil {
		return err
	}
	if lerr != "" {
		return fmt.Errorf("vmwasm: SetGlobalString(%q): %s", name, lerr)
	}
	return nil
}

// NewPool prepara el Pool PRINCIPAL: despacho de handles (M10) y el host de
// workers (M12: enu.worker.spawn y sus primitivas). El backend de UI se añade
// aparte con SetUIBackend (M11).
func NewPool() (*Pool, error) {
	p, err := newBarePool()
	if err != nil {
		return nil, err
	}
	p.registerHandleDispatch() // primitivas genéricas de despacho de métodos (M10)
	p.registerWorkerHost()     // enu.worker.spawn/_send/_recv/_terminate (M12)
	return p, nil
}

// SetAPIVersion fija el nivel de enu.version.api que el preludio expone. Lo llama
// el Runtime al construir el estado wasm (M13), con su APILevel. Debe llamarse
// antes de NewInstance (el preludio se arma con él).
func (p *Pool) SetAPIVersion(v int) { p.apiVersion = v }

// SetVersion fija major/minor/patch de enu.version (api.md §1), además del api que
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

// preludioSnippet es un fragmento Lua del catálogo con su marca de disponibilidad
// (G45): workerSafe distingue los wrappers [W] —que spawnWorker copia al preludio
// de cada worker— de los solo-principal (p. ej. enu.fs.watch, que depende de
// enu.events, inexistente en un worker). needs enumera los thunks que el wrapper
// envuelve: el snippet solo cruza si alguno está concedido por caps, de modo que
// la superficie no concedida NO EXISTE dentro del worker (deny-by-default, §14)
// también en su capa Lua, no solo en los thunks.
type preludioSnippet struct {
	src        string
	workerSafe bool
	needs      []string
}

// AddPreludio añade un snippet Lua que se ejecuta al final del preludio, cuando la
// tabla `enu` ya está montada. Es el punto de extensión con el que un módulo del
// catálogo (M13b) aporta un wrapper fino en Lua —p. ej. enu.re.compile, que ensambla
// la tabla mixta de capturas que el wire no puede cruzar de una pieza—. Debe
// llamarse antes de NewInstance. Esta variante registra el snippet como
// SOLO-PRINCIPAL: no cruza a los workers.
func (p *Pool) AddPreludio(snippet string) {
	p.extraPreludio = append(p.extraPreludio, preludioSnippet{src: snippet})
}

// AddPreludioW es AddPreludio con marca [W] (G45): el snippet además cruza al
// preludio de cada worker, porque envuelve superficie que api.md §16 declara
// disponible en workers. La marca es por-snippet y no en bloque porque conviven
// wrappers [W] (log, re, text, proc, ws, http.stream, search.grep) con wrappers
// solo-principal (fs.watch). `needs` son los nombres de los thunks que el wrapper
// llama (p. ej. "re._compile"): el snippet cruza solo si caps concede alguno —la
// misma autoridad (workerGrants) que poda los thunks poda sus wrappers, y un
// módulo no concedido no existe en el worker tampoco como tabla Lua (§14). Sin
// needs, el snippet cruza siempre.
func (p *Pool) AddPreludioW(snippet string, needs ...string) {
	p.extraPreludio = append(p.extraPreludio, preludioSnippet{src: snippet, workerSafe: true, needs: needs})
}

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
// para uso concurrente: como el estado principal de enu, un solo hilo lo maneja
// (el scheduler de M06 lo serializa; un worker de M12 tiene su propia Instance).
type Instance struct {
	pool   *Pool
	mod    api.Module
	ctx    context.Context
	cancel context.CancelFunc // cancela el ctx del Call (worker:terminate, M12)

	tries       []tryFrame     // pila LIFO de LUAI_TRY activos
	pfuncPool   []api.Function // nu_call_pfunc, UNA por nivel de anidamiento (ver hostTry)
	dispatch    Dispatcher
	bufPtr      uint32
	bufCap      uint32
	evalFn      api.Function
	coSpawnFn   api.Function
	coResumeFn  api.Function
	resultLen   api.Function
	schedStepFn api.Function // nu_sched_step, perezoso (M06)
	handles     *handleTable // tabla de objetos vivos tras los handles (M10, C5)

	dispatchHandle Handle           // handle en despacho síncrono (M11: self-free)
	pendingInput   []map[string]any // cola de eventos de input crudos (M11, FeedInput)
	workerChans    *workerChannels  // canales con el padre, si esta Instance es un worker (M12)

	// workerOwner es la FOTO del plugin dueño tomada en el spawn (G56, ADR-024):
	// la identidad con que las primitivas [W] atribuidas por dueño (enu.log, enu.proc)
	// corren DENTRO de este worker, inmutable durante toda su vida. Vacío en el estado
	// principal (no es un worker). La captura enu.worker._spawn en el estado principal
	// —donde el ownerStack es coherente— y viaja COPIADA aquí, como los mensajes: nunca
	// se lee el ownerStack del padre desde la goroutine del worker (elimina el data
	// race de SEC-05 por diseño). Sólo lo toca NewInstance→spawnWorker (fijación) y el
	// accesor WorkerOwner (lectura): inmutable tras el spawn, sin carrera.
	workerOwner string

	// pendingGName/pendingGVal es la ranura de un solo hueco por la que SetGlobalString
	// (M13d) pasa un global Lua desde Go SIN interpolar código: se fija bajo mu y una
	// Eval lo aplica con `_G[enu.__pending_gname()] = enu.__pending_gval()`. Es la vía
	// por la que el BINARIO pasa sus args CLI al driver Lua sobre wasm (paridad con
	// rt.L.SetGlobal en gopher). Single-goroutine (ADR-004): sin carrera entre el set y
	// la Eval que lo lee (mismo patrón que pendingInput/FeedInput).
	pendingGName string
	pendingGVal  string

	// pendingEName/pendingEPayload es la ranura por la que EmitEvent inyecta un evento
	// del core (ui:resize, ui:focus…) en el bus wasm desde Go, sin interpolar: se fija
	// bajo mu y una Eval hace `enu.events.emit(enu.__pending_ename(), enu.__pending_epayload())`.
	// Es la paridad wasm de rt.sched.emit para los eventos ui:* que nacen en el driver.
	pendingEName    string
	pendingEPayload map[string]any

	// Watchdog por slice (DM4). `sliceBudget` (copiado del Pool en NewInstance) es
	// el presupuesto; `taskDeadline` lo fija `__reset_budget` ANTES de reanudar cada
	// task, y lo lee `nu_over_budget` (el import del count-hook). Ambos los toca sólo
	// el goroutine que conduce el Call —síncrono, sin goroutine de fondo—: race-free.
	sliceBudget  time.Duration
	taskDeadline time.Time

	// mu serializa TODA entrada a la VM en producción: los Call del bucle de
	// RunTasks (schedStep), los Eval de EmitEvent que llegan desde goroutines de
	// fondo (watchers de fs, señales), FeedInput del driver de TTY y el acceso del
	// pintor al compositor vía WithLock. Es el sustituto wasm del token del
	// scheduler (ADR-004): quitarlo, o añadir un camino de entrada que no lo tome,
	// reintroduce un data race sobre la memoria del módulo.
	mu sync.Mutex

	// slotMu serializa el par "fijar la ranura + Eval" de SetGlobalString/EmitEvent:
	// como ambos sueltan `mu` entre el set y la Eval (la Eval la re-toma), dos
	// llamadas concurrentes (p. ej. la entrega de dos watchers de enu.fs.watch desde
	// goroutines distintas) podrían pisarse la ranura pendiente. slotMu hace atómico
	// ese par sin bloquear el `mu` que conduce la VM.
	slotMu sync.Mutex

	// Estado del BOMBEO del scheduler (G44). Vive en la Instance —no en cada
	// invocación de RunTasks— para que el trabajo de fondo (los `every`) SOBREVIVA
	// a la quiescencia de primer plano: su petición en vuelo sigue viva, su
	// resultado espera en `pumpCh` (buffer 64) y la siguiente invocación (u otro
	// `PumpTasks`) lo reanuda — pausa, no muerte. `pumpKick` es el timbre (buffer
	// 1, "queda pulsado hasta que alguien lo mira"): `Eval`/`CoSpawn` lo tocan
	// para que el trabajo encolado desde fuera del bucle (EmitEvent, FeedInput,
	// un handler) despierte al `select` sin esperar al vencimiento casual del IO
	// en vuelo. `pumpActive` (CAS) impide dos bucles simultáneos sobre el mismo
	// estado. Los campos sin sincronización propia (inject/outstanding/cancels)
	// los toca SOLO la goroutine que ganó el CAS: el propio CAS ordena las
	// invocaciones sucesivas desde goroutines distintas.
	pumpCh          chan asyncResult
	pumpKick        chan struct{}
	pumpInject      []any
	pumpOutstanding int
	pumpReqCancels  map[int64]context.CancelFunc
	pumpOrphans     []context.CancelFunc
	pumpActive      atomic.Bool

	// quitSignal se CIERRA la primera vez que algo emite core:shutdown en el bus de
	// esta Instance (G58). Lo dispara la primitiva interna __driver_notify_quit
	// —que el handler de core:shutdown del driver de TTY (driver.go) invoca— y lo
	// escucha el `select` de ese driver JUNTO al canal de input: así un
	// core:shutdown nacido en una task de fondo (`/quit`, que corre en
	// enu.task.spawn) despierta el bucle sin depender de que llegue más teclado. El
	// cierre es idempotente (quitOnce); se firma bajo la goroutine que conduce la VM
	// (bajo `mu`), pero el canal lo lee otra goroutine —cerrar un canal es seguro de
	// observar desde fuera, y sync.Once ordena el único cierre—.
	quitSignal chan struct{}
	quitOnce   sync.Once
}

// NewInstance instancia el módulo compilado (memoria fresca), cablea el
// contexto (snapshotter + puntero a la Instance) y crea el estado Lua con las
// libs del baseline. El dispatcher arranca en el que rechaza todo; SetDispatcher
// lo sustituye (M05).
func (p *Pool) NewInstance() (*Instance, error) {
	inst := &Instance{pool: p, handles: newHandleTable(), sliceBudget: p.sliceBudget,
		pumpCh:         make(chan asyncResult, 64),
		pumpKick:       make(chan struct{}, 1),
		pumpReqCancels: make(map[int64]context.CancelFunc),
		quitSignal:     make(chan struct{}),
	}
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

	// Preludio: construye la tabla `enu` (thunks sobre las primitivas registradas) y
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

// kickPump toca el timbre del bombeo (G44): señala al select de runTaskLoop que
// puede haber trabajo nuevo en la cola de ready (una task spawneada por un
// handler, un Future resuelto). No bloqueante y con buffer 1: el timbre queda
// pulsado hasta que el bucle lo mira, y los toques redundantes se funden. Un
// toque espurio solo cuesta un paso vacío del scheduler.
func (inst *Instance) kickPump() {
	select {
	case inst.pumpKick <- struct{}{}:
	default:
	}
}

// Eval carga y corre un chunk protegido. Devuelve (resultado, errorLua, errorGo):
// errorLua != "" es un error de Lua capturado (el chunk lanzó); errorGo != nil
// es un fallo duro del motor (trap wasm). Sólo uno es no-cero.
//
// Al terminar toca el timbre del bombeo (G44): un chunk puede haber encolado
// trabajo (EmitEvent y FeedInput entran por aquí; sus handlers hacen
// enu.task.spawn), y sin el toque ese trabajo esperaría al azar del IO en vuelo.
func (inst *Instance) Eval(chunk string) (string, string, error) {
	defer inst.kickPump()
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
// Toca el timbre del bombeo al salir (G44), como Eval.
func (inst *Instance) CoSpawn(chunk string) (int32, error) {
	defer inst.kickPump()
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

// --- funciones host (módulo "enu", compartidas; enrutan por ctx) --------------

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
	// api.Function no es reentrante (lección del spike; M03 lo blinda con
	// TestTrampolinNoReentrancia): un mismo objeto Function NO puede tener dos Call
	// vivos a la vez en la pila. Pero eso sólo pasa entre pcalls ANIDADOS, no entre
	// HERMANOS. Antes se pedía una Function FRESCA por llamada —cada
	// `ExportedFunction` crea un callEngine nuevo: en el perfil de asignaciones del
	// turno de agente eso era el ~31% de TODA la memoria (callEngine.init), a la par
	// del Snapshot—. En su lugar mantenemos UNA Function por NIVEL de anidamiento
	// (indexada por `depth`): los pcalls hermanos (mismo nivel, secuenciales) reusan
	// su callEngine —jamás hay dos Call vivos en el mismo slot— y sólo se crea una
	// Function nueva al bajar a un nivel nunca antes alcanzado. La no-reentrancia se
	// respeta (nivel N y N+1 usan slots distintos) y las asignaciones caen a O(max
	// profundidad) en vez de O(nº de pcalls). M15: palanca de rendimiento del veto 2.
	if depth > len(inst.pfuncPool) {
		inst.pfuncPool = append(inst.pfuncPool, inst.mod.ExportedFunction("nu_call_pfunc"))
	}
	_, callErr := inst.pfuncPool[depth-1].
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
