package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// El scheduler es la quilla del kernel (api.md §1.3, §3; ADR-004, realizado por
// ADR-011). El modelo de concurrencia es "del navegador": estado Lua de un solo
// hilo lógico con async por await implícito. Sobre gopher-lua eso se realiza con
// **una goroutine por task + un único token de ejecución Lua** ("GIL"), no con
// yields de corrutina:
//
//   - Cada **task** corre en su propia goroutine, sobre su propio thread Lua
//     (`*lua.LState` hijo del principal, comparten globales `G`). El **token**
//     (un canal de capacidad 1) garantiza que **solo una goroutine toca Lua a la
//     vez** —el invariante single-threaded de ADR-004/008 sin memoria Lua
//     compartida en paralelo.
//   - Una primitiva **⏸** no cede una corrutina: **suelta el token**, hace el
//     trabajo bloqueante en una goroutine de fondo (que jamás toca Lua) y, al
//     terminar, **recupera el token** y retorna el valor con normalidad. Como no
//     hay yield, la pila Lua vive en la pila Go de la task: `pcall` y las tail
//     calls que envuelven a un ⏸ sobreviven nativas (api.md §1.4: los errores se
//     capturan con `pcall`, incluso alrededor de operaciones que suspenden).
//
// Por qué este modelo y no el puente de corrutinas que insinuaban los documentos:
// gopher-lua (semántica Lua 5.1) **no deja a una corrutina ceder a través de una
// frontera de llamada Go** —ni `pcall` ni una tail call hacia una función Go que
// hace yield—. Eso rompía el modelo de errores de §1.4. La decisión y su porqué
// están en ADR-011; la grieta, en problemas.md (hallazgo del puente ⏸).
//
// "Cero data races" (inventario 🔒 de S04) sale del token: el handoff es por
// canal (happens-before), y nada Lua se toca fuera de él. La superficie pública
// de S04 es `nu.task.spawn` + `Task:await` (§3); sleep/defer/every, future,
// all/race, cancel/cleanup y el watchdog llegan en S05–S09 sobre esta base.

// taskTypeName identifica la metatabla del handle `Task` en el registro de tipos
// de gopher-lua. El userdata que devuelve `spawn` la lleva, y de ahí cuelga
// `await`.
const taskTypeName = "nu.task.Task"

// task es una unidad de ejecución concurrente: una goroutine corriendo `fn` sobre
// el thread Lua `co`. Al terminar guarda su desenlace (`results` o `errValue`) y
// cierra `doneCh`, que es donde esperan los `await` pendientes.
type task struct {
	co   *lua.LState
	fn   *lua.LFunction
	args []lua.LValue

	doneCh  chan struct{} // se cierra (bajo token) al terminar la task
	done    bool          // espejo de doneCh legible bajo token sin select
	awaited bool          // alguien hizo (o está haciendo) await sobre ella

	results  []lua.LValue // valores de retorno si terminó bien
	errValue lua.LValue   // objeto lanzado si terminó con error; nil si no
	canceled bool         // se canceló (S08): no se entrega su resultado; `await` ve ECANCELED

	// cancelCh es el canal de **señal de cancelación**: lo cierra `cancelTask`
	// (una sola vez, `cancelOnce`) y los puntos de suspensión (`suspend`,
	// `Task:await`, `Future:await`) lo observan por `select` para abortar la task
	// en su siguiente ⏸. La cancelación es **cooperativa**: surte efecto en el
	// próximo punto de suspensión, no a media ejecución de Lua (eso, para CPU
	// pura, es el watchdog de S09).
	cancelCh   chan struct{}
	cancelOnce sync.Once

	// aborting se pone a true en `abort` —justo antes de lanzar el pánico
	// centinela— y es lo que hace el desenrollado **NO capturable** por `pcall`
	// (api.md §1.3, inventario 🔒 de S08). Las versiones envueltas de `pcall` y
	// `xpcall` (cancel.go) consultan este flag de la task en curso: si está
	// abortando, re-lanzan el centinela en vez de devolver `false, err` a Lua, de
	// modo que el aborto atraviesa cualquier `pcall`/`xpcall` del usuario hasta
	// `runTask`. Solo lo escribe la propia goroutine de la task (en `abort`) y lo
	// lee la misma goroutine (en el `pcall` envuelto), siempre bajo el token: sin
	// carrera. S09 reusará exactamente este camino con `reason = abortBudget`.
	aborting bool
	reason   abortReason // por qué se aborta: cancelación (S08) o presupuesto (S09)

	// cleanups es la **pila LIFO de liberadores** que registra `nu.task.cleanup`
	// (api.md §3, §1.3). Corren TODOS al terminar la task —éxito, error o
	// aborto— en orden inverso al de registro (el último registrado corre
	// primero). Solo la goroutine de la task la toca (registro y ejecución), bajo
	// el token: sin candado.
	cleanups []*lua.LFunction

	// --- Watchdog de slice (S09, api.md §1.3) ---
	//
	// ctxCancel cancela el `context.Context` que `co` vigila en cada instrucción
	// del intérprete (gopher-lua `mainLoopWithContext`): es el ÚNICO modo de
	// romper un slice de **CPU puro** que nunca suspende (`while true do end`), que
	// no tiene punto de chequeo cooperativo como `suspend`/`await`. Lo dispara el
	// watchdog desde su propia goroutine cuando el slice excede el presupuesto.
	// Una vez cancelado el contexto queda cancelado para siempre: como exceder el
	// presupuesto aborta la task ENTERA, no hay que re-armarlo entre slices.
	ctxCancel context.CancelFunc

	// budgetTimer es el temporizador del slice en curso (`time.AfterFunc`). Se
	// **arma** cuando la task toma el token para correr Lua (inicio de `runTask`,
	// y tras re-adquirir en `suspend`/`Task:await`/`Future:await`) y se **desarma**
	// justo antes de soltar el token (al suspender o terminar). Si dispara antes de
	// desarmarse, el slice excedió el presupuesto. Solo lo toca la goroutine de la
	// task (arm/disarm), bajo el token: sin candado sobre el campo en sí.
	budgetTimer *time.Timer

	// budgetExceeded lo pone a true el watchdog (en otra goroutine) al disparar,
	// **antes** de cancelar el contexto. La goroutine de la task lo lee al
	// detectar el error de contexto (en los wrappers de `pcall`/`xpcall` y en
	// `runTask`) para reconocer que el desenlace fue por presupuesto y no un error
	// Lua normal. Es `atomic.Bool` porque cruza goroutines sin el token (el
	// watchdog no lo tiene); el resto de campos de aborto (`aborting`/`reason`/
	// `canceled`) los sigue escribiendo SOLO la goroutine de la task bajo el token
	// —invariante de S08 intacto—, una vez ha leído este flag.
	budgetExceeded atomic.Bool
}

// abortReason distingue por qué se desenrolla una task de forma no capturable
// (api.md §1.3, §1.4). Ambos motivos comparten el mismo mecanismo de pánico
// centinela + re-lanzado a través de `pcall`; difieren en el código que `await`
// observa y en si el desenlace fue deliberado (cancelación: no se loguea) o un
// exceso de presupuesto (S09: se loguea y emite `core:plugin.misbehaved`).
type abortReason int

const (
	abortNone   abortReason = iota
	abortCancel             // `Task:cancel()` o cancelación de combinador (S07/S08): ECANCELED
	abortBudget             // watchdog por slice excedido (S09): EBUDGET
)

// abortSignal es el valor del **pánico centinela** con que una task abortada
// (por `cancel`, S08; por watchdog, S09) desenrolla su pila Go en su siguiente
// punto de suspensión (o slice). Lo lanza `abort`; `runTask` lo recupera y NO lo
// confunde con un error Lua: la task queda marcada `canceled`, sin `results` ni
// `errValue`.
//
// El desenrollado **NO es capturable por `pcall`** (api.md §1.3): el pánico es
// un valor Go, no un `error()` de Lua, pero gopher-lua recupera todo pánico Go
// en su `PCall` interno (el mismo motivo de ADR-011), así que por sí solo un
// `pcall` de usuario lo atraparía. La inmunidad la dan las versiones envueltas
// de `pcall`/`xpcall` (cancel.go), que consultan `task.aborting` y re-lanzan el
// centinela en vez de devolverlo a Lua. Llevar `t` en el centinela permite a
// esos envoltorios reconstruirlo idéntico al re-lanzar, sin depender de la
// representación que gopher-lua le diera al cruzar `PCall`.
type abortSignal struct{ t *task }

// deliverFn construye, **ya con el token recuperado** (es seguro tocar Lua), los
// valores que una primitiva ⏸ devuelve a su task tras completar el trabajo de
// fondo. La goroutine de fondo la *fabrica* capturando datos Go; no la invoca.
type deliverFn func(L *lua.LState) []lua.LValue

// scheduler administra el token Lua y la contabilidad de tasks vivas para saber
// cuándo el conjunto quedó quiescente (lo necesita `EvalString`).
type scheduler struct {
	rt   *Runtime
	host *lua.LState // estado principal: el chunk de `nu -e` y los handlers síncronos corren aquí

	gil chan struct{} // token de ejecución Lua (cap 1); el que lo tiene, corre Lua

	mu      sync.Mutex // protege `live`/`pending`/`timers` y respalda `cond`
	cond    *sync.Cond // señala "no queda trabajo de primer plano" (live+pending == 0)
	live    int        // tasks vivas (lanzadas y no terminadas)
	pending int        // handlers `defer` encolados y aún no ejecutados (S05)

	// timers son los `every` activos. Se rastrean para poder cortarlos todos al
	// cerrar el runtime (`Close`), de modo que no quede ninguna goroutine de
	// ticker colgada tras el fin del proceso. `defer` no se rastrea: es de un
	// solo disparo y se autolimpia.
	timers map[*luaTimer]struct{}

	// watchers son los `nu.fs.watch` activos (S15). Se rastrean por la misma razón
	// que los `timers`: cortarlos todos en `Close` para no dejar goroutines de
	// fondo ni watchers del SO colgados. Su goroutine vigila fsnotify + debounce;
	// como un `every`, un watcher activo **no** cuenta para la quiescencia.
	watchers map[*luaWatcher]struct{}

	// procs son los subprocesos vivos de `nu.proc.spawn` (S16). Se rastrean para
	// **matarlos todos en `Close`** (`stopAllProcs`): un subproceso que un script
	// dejó corriendo no debe sobrevivir al proceso de `nu` —es la última red de
	// seguridad de la vida del proceso (§6), tras el `cleanup` de quien lo creó y el
	// finalizer del GC—. Como `watch`/`every`, un proceso vivo **no** cuenta para la
	// quiescencia: la vía de fin de vida es `cleanup`/`kill`, no esperar a que el
	// proceso muera (que podría no morir nunca). El mapa lo tocan `spawn` (al crear)
	// y `wait` no lo desregistra a propósito —`Close` mata por idempotencia (`killed`)
	// aunque el proceso ya haya salido—.
	procs map[*luaProc]struct{}

	// streams son los `nu.http.stream` vivos (S20). Se rastrean por la misma razón
	// que `procs`/`watchers`: cerrarlos todos en `Close` (`stopAllStreams`) para no
	// dejar goroutines de fondo (la que lee el body) ni conexiones colgadas tras el
	// fin del proceso. Como un proceso o un watcher, un stream vivo **no** cuenta
	// para la quiescencia: su fin de vida es `close`/`cleanup`, no esperar a que el
	// servidor termine (un SSE puede no terminar nunca). El mapa lo tocan
	// `trackStream` (al recibir cabeceras) y `untrackStream` (`Stream:close`).
	streams map[*httpStream]struct{}

	// ws son los `nu.ws.connect` vivos (S21). Se rastrean por la misma razón que
	// `streams`/`procs`/`watchers`: cerrarlos todos en `Close` (`stopAllWs`) para no
	// dejar conexiones ni goroutines de IO colgadas tras el fin del proceso. Como un
	// stream, un websocket vivo **no** cuenta para la quiescencia: su fin de vida es
	// `close`/`cleanup`, no esperar a que la otra punta cierre (puede no cerrar
	// nunca). El mapa lo tocan `trackWs` (al conectar) y `untrackWs` (`Ws:close`).
	ws map[*luaWs]struct{}

	// greps son los iteradores de `nu.search.grep` vivos (S27). Se rastrean por la
	// misma razón que `streams`/`procs`: cancelarlos todos en `Close` (`stopAllGreps`)
	// para no dejar el pool de goroutines de fondo (las que recorren el árbol y casan
	// el patrón) colgado tras el fin del proceso. Como un stream, un grep vivo **no**
	// cuenta para la quiescencia: su fin de vida es alcanzar `max`/EOF o el `cleanup`
	// de la task, no esperar a que el pool termine. El mapa lo tocan `trackGrep` (al
	// crear el iterador) y `untrackGrep` (`grepIter.close`).
	greps map[*grepIter]struct{}

	// coToTask mapea el thread Lua de cada task viva (su `co`) a su `*task`. Lo
	// usa `suspend` para observar el `cancelCh` de la task que se suspende (S07):
	// quien suspende es la goroutine de esa task, que corre sobre `co == L`. Se
	// puebla en `runTask` y se limpia al terminar. `sync.Map` porque se escribe
	// desde la goroutine de cada task y se lee desde `suspend` sin el token.
	coToTask sync.Map // *lua.LState -> *task

	// budget es el **presupuesto por slice** del watchdog (api.md §1.3): el tiempo
	// máximo que una task puede correr Lua de forma continua sin suspender. Por
	// defecto 100 ms; configurable vía `WithSliceBudget` (el gancho que S11/S12
	// cablearán a `nu.toml`). Inmutable tras construir el runtime, así que se lee
	// sin candado desde las goroutines de las tasks.
	budget time.Duration

	// events es el bus `nu.events` (S10, §4): suscripciones y cola de emisiones
	// anidadas. Vive aquí porque comparte el token y `host` con el resto del
	// scheduler —todo el bus corre en el estado principal bajo el token, sin
	// candado propio (events.go)—. Lo inicializa `registerEvents`.
	events *eventBus

	// ownerHandles es el **registro de handles por dueño** (S13, §14, inventario
	// 🔒): asocia cada plugin (por nombre de owner) a los handles persistentes que
	// registró —suscripciones de eventos (`on`/`once`), timers (`every`)—. Es lo
	// que permite a `nu.plugin.reload` soltarlos todos sin dejar huérfanos (G2).
	// Lo alimentan `on`/`once`/`every` (al crear) y `Sub:cancel`/`Timer:stop` (al
	// soltar a mano); `reload` lo recorre. Vive bajo el token, como `events`, sin
	// candado. Diseñado para que futuras primitivas con handle persistente (S15
	// watchers, S16 procs, S29+ input/regiones) se enchufen implementando
	// `ownedHandle` (handles.go).
	ownerHandles map[string][]ownedHandle
}

// newScheduler prepara el scheduler con el token libre (sembrado en el canal) y
// el presupuesto de slice del watchdog (S09).
func newScheduler(rt *Runtime, budget time.Duration) *scheduler {
	s := &scheduler{
		rt:       rt,
		host:     rt.L,
		gil:      make(chan struct{}, 1),
		timers:   make(map[*luaTimer]struct{}),
		watchers: make(map[*luaWatcher]struct{}),
		procs:    make(map[*luaProc]struct{}),
		streams:  make(map[*httpStream]struct{}),
		ws:       make(map[*luaWs]struct{}),
		greps:    make(map[*grepIter]struct{}),
		budget:   budget,
	}
	s.cond = sync.NewCond(&s.mu)
	s.gil <- struct{}{} // token disponible: el primero que lo pida corre Lua
	return s
}

// acquire toma el token Lua (bloquea hasta que esté libre). Tras volver, esta
// goroutine es la única autorizada a tocar Lua. release lo devuelve.
func (s *scheduler) acquire() { <-s.gil }
func (s *scheduler) release() { s.gil <- struct{}{} }

// register cuelga `nu.task` (con `spawn`) del global `nu` e instala la metatabla
// del tipo `Task` con su método `await`. `await` es una función Go pura: como
// nunca hay yield, puede relanzar el error de la task esperada con `L.Error`
// (capturable con `pcall`), sin envoltorios.
func (s *scheduler) register(nu *lua.LTable) {
	L := s.host

	mt := L.NewTypeMetatable(taskTypeName)
	index := L.NewTable()
	index.RawSetString("await", L.NewFunction(s.taskAwait))
	// Cancelación pública (S08): `Task:cancel()` (§3, §1.3). Cuelga del mismo
	// `__index` que `await`.
	index.RawSetString("cancel", L.NewFunction(s.taskCancel))
	L.SetField(mt, "__index", index)

	taskTbl := L.NewTable()
	taskTbl.RawSetString("spawn", L.NewFunction(s.taskSpawn))
	// `nu.task.cleanup(fn)` (S08): registra un liberador en la pila LIFO de la
	// task actual (§3, §1.3).
	taskTbl.RawSetString("cleanup", L.NewFunction(s.taskCleanup))
	nu.RawSetString("task", taskTbl)

	// Timers y diferidos (S05): `sleep`/`defer`/`every` + el tipo `Timer`,
	// colgados de la misma tabla `nu.task` (timers.go).
	s.registerTimers(nu, taskTbl)

	// Futures (S06): `nu.task.future` + el tipo `Future` con `set`/`await`,
	// colgados de la misma tabla `nu.task` (future.go).
	s.registerFuture(taskTbl)

	// Combinadores (S07): `nu.task.all` y `nu.task.race`, colgados de la misma
	// tabla `nu.task` (allrace.go).
	s.registerAllRace(taskTbl)
}

// waitIdle bloquea hasta que no quede **trabajo de primer plano**: ninguna task
// viva (`live`) ni ningún `defer` encolado sin ejecutar (`pending`, S05). Lo usa
// `EvalString` tras correr el chunk: las tasks que el chunk lanzó —y los `defer`
// que encoló— corren mientras el hilo principal espera (con el token soltado). El
// patrón mutex+cond evita el wakeup perdido: el chequeo y la espera son atómicos.
//
// Los timers periódicos (`every`) **no** entran en esta cuenta a propósito: un
// timer nunca termina, así que esperar a que pare colgaría `nu -e` para siempre.
// Son facilidad de fondo; `Close` los apaga (ver claude_decisions.md, S05).
func (s *scheduler) waitIdle() {
	s.mu.Lock()
	for s.live > 0 || s.pending > 0 {
		s.cond.Wait()
	}
	s.mu.Unlock()
}

// addPending/donePending contabilizan los `defer` encolados para la quiescencia:
// un `defer` pendiente mantiene el runtime no-quiescente hasta que su handler
// corre (es "el siguiente tick", no trabajo de fondo). Al llegar a cero se
// despierta a `waitIdle`, igual que al terminar la última task.
func (s *scheduler) addPending() {
	s.mu.Lock()
	s.pending++
	s.mu.Unlock()
}

func (s *scheduler) donePending() {
	s.mu.Lock()
	s.pending--
	if s.live == 0 && s.pending == 0 {
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

// trackTimer registra un `every` activo para poder cortarlo al cerrar el runtime.
func (s *scheduler) trackTimer(t *luaTimer) {
	s.mu.Lock()
	s.timers[t] = struct{}{}
	s.mu.Unlock()
}

// stopTimer corta un `every` (vía `Timer:stop` o el cierre del runtime) y lo
// deja de rastrear. Es **idempotente**: cerrar `stopCh` dos veces entraría en
// pánico, así que el cierre solo ocurre la primera vez (cuando el timer sigue en
// el mapa). Tras esto, la goroutine del ticker ve `stopCh` cerrado y retorna.
func (s *scheduler) stopTimer(t *luaTimer) {
	s.mu.Lock()
	_, live := s.timers[t]
	if live {
		delete(s.timers, t)
		close(t.stopCh)
	}
	s.mu.Unlock()
}

// stopAllTimers corta todos los `every` activos. Lo llama `Runtime.Close` para no
// dejar goroutines de ticker colgadas al terminar el proceso.
func (s *scheduler) stopAllTimers() {
	s.mu.Lock()
	for t := range s.timers {
		close(t.stopCh)
		delete(s.timers, t)
	}
	s.mu.Unlock()
}

// trackWatcher registra un `nu.fs.watch` activo (S15) para poder cortarlo al
// cerrar el runtime. Mismo patrón que `trackTimer`.
func (s *scheduler) trackWatcher(w *luaWatcher) {
	s.mu.Lock()
	s.watchers[w] = struct{}{}
	s.mu.Unlock()
}

// stopWatcher corta un watcher (vía `Watcher:stop`, el cierre del runtime o un
// `reload`) y lo deja de rastrear. Es **idempotente**: cerrar `stopCh` dos veces
// entraría en pánico, así que el cierre solo ocurre la primera vez (cuando el
// watcher sigue en el mapa). Tras esto, su goroutine `run` ve `stopCh` cerrado y
// retorna; el watcher del SO se cierra (libera sus descriptores) sin dejar
// goroutines colgadas. El `Close` de fsnotify cierra sus canales `Events`/`Errors`,
// que la goroutine ya no leerá tras retornar.
func (s *scheduler) stopWatcher(w *luaWatcher) {
	s.mu.Lock()
	_, live := s.watchers[w]
	if live {
		delete(s.watchers, w)
	}
	s.mu.Unlock()
	if !live {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
	_ = w.fsw.Close()
}

// stopAllWatchers corta todos los `nu.fs.watch` activos. Lo llama `Runtime.Close`
// para no dejar goroutines de fondo ni watchers del SO colgados al terminar.
func (s *scheduler) stopAllWatchers() {
	s.mu.Lock()
	ws := make([]*luaWatcher, 0, len(s.watchers))
	for w := range s.watchers {
		ws = append(ws, w)
	}
	s.watchers = make(map[*luaWatcher]struct{})
	s.mu.Unlock()
	for _, w := range ws {
		w.stopOnce.Do(func() { close(w.stopCh) })
		_ = w.fsw.Close()
	}
}

// trackProc registra un subproceso vivo de `nu.proc.spawn` (S16) para poder matarlo
// al cerrar el runtime. Mismo patrón que `trackTimer`/`trackWatcher`.
func (s *scheduler) trackProc(p *luaProc) {
	s.mu.Lock()
	s.procs[p] = struct{}{}
	s.mu.Unlock()
}

// stopAllProcs mata todos los subprocesos vivos. Lo llama `Runtime.Close`: ningún
// subproceso de la sesión debe sobrevivir al proceso de `nu`. `killSignal` es
// idempotente (`killed`), así que matar uno que ya salió por su cuenta es inocuo —se
// usa SIGKILL para que ninguno lo ignore, como red de seguridad final—.
func (s *scheduler) stopAllProcs() {
	s.mu.Lock()
	ps := make([]*luaProc, 0, len(s.procs))
	for p := range s.procs {
		ps = append(ps, p)
	}
	s.procs = make(map[*luaProc]struct{})
	s.mu.Unlock()
	for _, p := range ps {
		p.killSignal(syscall.SIGKILL)
		p.closeReadPipes()
	}
}

// trackStream registra un `nu.http.stream` vivo (S20) para poder cerrarlo al cerrar
// el runtime. Mismo patrón que `trackProc`. Se llama al recibir las cabeceras (en
// la `deliverFn` de `nu.http.stream`, bajo el token).
func (s *scheduler) trackStream(st *httpStream) {
	s.mu.Lock()
	s.streams[st] = struct{}{}
	s.mu.Unlock()
}

// untrackStream deja de rastrear un stream cerrado (`Stream:close`). Idempotente:
// quitar uno que ya no está es inocuo. Lo llama `httpStream.close`, que ya es
// idempotente por su `closeOnce`.
func (s *scheduler) untrackStream(st *httpStream) {
	s.mu.Lock()
	delete(s.streams, st)
	s.mu.Unlock()
}

// stopAllStreams cierra todos los streams vivos. Lo llama `Runtime.Close`: ninguna
// conexión ni goroutine de lectura de body debe sobrevivir al proceso de `nu`.
// `httpStream.close` es idempotente (`closeOnce`), así que cerrar uno ya cerrado es
// inocuo. Se copia el conjunto antes de iterar porque `close` llama a
// `untrackStream`, que toca el mapa bajo el mismo candado.
func (s *scheduler) stopAllStreams() {
	s.mu.Lock()
	sts := make([]*httpStream, 0, len(s.streams))
	for st := range s.streams {
		sts = append(sts, st)
	}
	s.streams = make(map[*httpStream]struct{})
	s.mu.Unlock()
	for _, st := range sts {
		st.close()
	}
}

// trackWs registra un `nu.ws.connect` vivo (S21) para poder cerrarlo al cerrar el
// runtime. Mismo patrón que `trackStream`. Se llama al conectar (en la `deliverFn`
// de `nu.ws.connect`, bajo el token).
func (s *scheduler) trackWs(w *luaWs) {
	s.mu.Lock()
	s.ws[w] = struct{}{}
	s.mu.Unlock()
}

// untrackWs deja de rastrear un websocket cerrado (`Ws:close`). Idempotente: quitar
// uno que ya no está es inocuo. Lo llama `luaWs.close`, que ya es idempotente por su
// `closeOnce`.
func (s *scheduler) untrackWs(w *luaWs) {
	s.mu.Lock()
	delete(s.ws, w)
	s.mu.Unlock()
}

// stopAllWs cierra todos los websockets vivos. Lo llama `Runtime.Close`: ninguna
// conexión ni goroutine de IO debe sobrevivir al proceso de `nu`. `luaWs.close` es
// idempotente (`closeOnce`), así que cerrar uno ya cerrado es inocuo. Se copia el
// conjunto antes de iterar porque `close` llama a `untrackWs`, que toca el mapa bajo
// el mismo candado.
func (s *scheduler) stopAllWs() {
	s.mu.Lock()
	ws := make([]*luaWs, 0, len(s.ws))
	for w := range s.ws {
		ws = append(ws, w)
	}
	s.ws = make(map[*luaWs]struct{})
	s.mu.Unlock()
	for _, w := range ws {
		w.close()
	}
}

// trackGrep registra un iterador de `nu.search.grep` vivo (S27) para poder
// cancelarlo al cerrar el runtime. Mismo patrón que `trackStream`. Se llama al
// crear el iterador (bajo el token, en `searchGrep`).
func (s *scheduler) trackGrep(it *grepIter) {
	s.mu.Lock()
	s.greps[it] = struct{}{}
	s.mu.Unlock()
}

// untrackGrep deja de rastrear un grep cerrado (`grepIter.close`). Idempotente:
// quitar uno que ya no está es inocuo. Lo llama `grepIter.close`, que ya es
// idempotente por su `closeOnce`.
func (s *scheduler) untrackGrep(it *grepIter) {
	s.mu.Lock()
	delete(s.greps, it)
	s.mu.Unlock()
}

// stopAllGreps cancela todos los greps vivos. Lo llama `Runtime.Close`: ningún
// pool de goroutines de fondo (recorrido del árbol + casado del patrón) debe
// sobrevivir al proceso de `nu`. `grepIter.close` es idempotente (`closeOnce`),
// así que cancelar uno ya cerrado es inocuo. Se copia el conjunto antes de iterar
// porque `close` llama a `untrackGrep`, que toca el mapa bajo el mismo candado.
func (s *scheduler) stopAllGreps() {
	s.mu.Lock()
	its := make([]*grepIter, 0, len(s.greps))
	for it := range s.greps {
		its = append(its, it)
	}
	s.greps = make(map[*grepIter]struct{})
	s.mu.Unlock()
	for _, it := range its {
		it.close()
	}
}

// spawn crea una task y lanza su goroutine. El arranque no es síncrono: la nueva
// goroutine compite por el token, así que solo corre cuando quien llamó a
// `spawn` lo suelta (al suspenderse o al terminar). Uniforme tanto si `spawn` se
// llama desde el chunk principal como desde dentro de otra task.
func (s *scheduler) spawn(fn *lua.LFunction, args []lua.LValue) *task {
	co, _ := s.host.NewThread()

	// Watchdog (S09): dota al thread de la task de un contexto cancelable que el
	// intérprete vigila en cada instrucción (`mainLoopWithContext`). Es lo que
	// permite romper un slice de CPU puro que no suspende; el watchdog lo cancela
	// al exceder el presupuesto. Contexto **propio por task** (raíz
	// `Background`), no hijo del de `host`, para que cancelar a una no afecte a
	// otras —el aislamiento es por tarea (ADR-008)—.
	ctx, cancel := context.WithCancel(context.Background())
	co.SetContext(ctx)

	t := &task{
		co:        co,
		fn:        fn,
		args:      args,
		doneCh:    make(chan struct{}),
		cancelCh:  make(chan struct{}),
		ctxCancel: cancel,
	}
	s.mu.Lock()
	s.live++
	s.mu.Unlock()
	go s.runTask(t)
	return t
}

// runTask es el cuerpo de la goroutine de una task: toma el token, corre `fn`
// protegida (un error Lua no tumba el proceso, ADR-008), guarda el desenlace,
// despierta a quien la espera (cerrando `doneCh`) y suelta el token. El
// decremento de `live` va fuera del token, bajo `mu`, para la quiescencia.
func (s *scheduler) runTask(t *task) {
	s.acquire()

	s.coToTask.Store(t.co, t) // para que `suspend` halle el `cancelCh` de esta task

	// Watchdog (S09): arma el presupuesto del **primer slice** —desde que la task
	// toma el token hasta su primer ⏸—; `suspend`/`await` lo re-arman en cada slice
	// posterior. El desarmado del último slice lo hace el propio `CallByParam` al
	// volver (vía `disarmWatchdog` más abajo) o el punto de suspensión que cedió.
	s.armWatchdog(t)
	err := t.co.CallByParam(lua.P{Fn: t.fn, NRet: lua.MultRet, Protect: true}, t.args...)
	s.disarmWatchdog(t)

	s.coToTask.Delete(t.co)

	// El watchdog pudo disparar **sin** que ningún `pcall` de usuario reconvirtiera
	// el error de contexto en aborto (caso de un bucle de CPU puro sin `pcall`
	// envolvente): entonces `err` trae el "context canceled" de gopher-lua pero
	// `t.canceled` sigue false. Aquí, ya bajo el token, la goroutine de la task
	// reconoce el exceso de presupuesto y lo equipara a un aborto por
	// `abortBudget` —descarta el desenlace y, abajo, emite `core:plugin.misbehaved`.
	// (Si SÍ hubo un `pcall` de usuario, los wrappers ya re-lanzaron `abortSignal`
	// y `t.canceled` llega true; este claim es idempotente.)
	budget := s.claimBudgetAbort(t)

	switch {
	case t.canceled:
		// La task fue abortada (por `Task:cancel`, S08, o por un combinador, S07):
		// `suspend`/`await` lanzaron el pánico centinela en su último ⏸ y, gracias
		// a los `pcall`/`xpcall` envueltos (cancel.go), atravesó cualquier `pcall`
		// de usuario hasta este `CallByParam`, que lo devolvió como error. No es un
		// desenlace observable directamente —ni `results` ni `errValue`—: una task
		// cancelada no entrega valor; lo que ve quien la espera es `ECANCELED`
		// (`taskAwait`), que es *observación*, no captura del aborto (§1.3). Tampoco
		// se loguea: la cancelación es deliberada, no un fallo. `err` se ignora.
	case err == nil:
		if n := t.co.GetTop(); n > 0 {
			t.results = make([]lua.LValue, n)
			for i := 1; i <= n; i++ {
				t.results[i-1] = t.co.Get(i)
			}
		}
	default:
		t.errValue = raisedValue(err)
	}
	t.co.SetTop(0)

	// Pila LIFO de `nu.task.cleanup` (§3, §1.3): corre TODOS los liberadores —pase
	// lo que pase con la task: éxito, error o aborto—, en orden inverso al de
	// registro. Va con el token aún tomado (estamos dentro de `runTask`, antes de
	// `release`), porque cada liberador es código Lua síncrono. Ya no estamos
	// desenrollando (el `CallByParam` retornó), así que `t.aborting` se apaga: un
	// `pcall` *dentro* de un cleanup vuelve a comportarse normal, y un cleanup que
	// suspendiera o se cancelara no relanza el centinela de esta task.
	s.runCleanups(t)

	t.done = true
	close(t.doneCh)

	// Watchdog (S09): si la task se abortó por **exceder el presupuesto** de un
	// slice, emite `core:plugin.misbehaved` (api.md §1.3, §4). A diferencia de la
	// cancelación —deliberada, silenciosa—, un slice excedido es un mal
	// comportamiento del plugin que el resto del sistema debe poder observar.
	// La emisión va por un **gancho interno** (`rt.emitMisbehaved`): el bus
	// `nu.events` es S10; hasta entonces el gancho solo loguea best-effort (como el
	// resto de errores de task). S10 lo cableará a
	// `nu.events.emit("core:plugin.misbehaved", ...)`.
	if budget {
		s.rt.emitMisbehaved(s.rt.currentOwner(), "una task excedió el presupuesto de slice del watchdog (EBUDGET)")
	}

	// Error fire-and-forget: si la task lanzó y nadie la espera, déjalo en el
	// log (best-effort de S04; el evento `core:plugin.error` llega en S10).
	// `awaited` ya es true si un `await` se registró antes de terminar, así que
	// el caso esperado-y-fallido no genera ruido.
	if t.errValue != nil && !t.awaited {
		_ = s.rt.log.write(levelError, s.rt.currentOwner(),
			"una task terminó con error y nadie hizo await: "+errString(t.errValue))
	}

	// Watchdog (S09): libera los recursos del contexto del thread de la task. Si la
	// mató el watchdog ya estará cancelado; si terminó normal, cancelarlo aquí
	// evita la fuga que `context.WithCancel` provoca cuando su cancel nunca se
	// llama. Tras esto el thread `co` queda para el GC de gopher-lua, como siempre.
	if t.ctxCancel != nil {
		t.ctxCancel()
	}

	s.release()

	s.mu.Lock()
	s.live--
	if s.live == 0 {
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

// suspend es **el puente ⏸** del modelo (ADR-011): suelta el token, corre `work`
// en una goroutine de fondo y, al terminar, recupera el token y devuelve los
// valores que `work` dejó listos. Toda primitiva ⏸ (la de prueba de S04; sleep,
// fs.read, http... en adelante) se construye sobre esto.
//
// Aislamiento (de donde sale "cero data races"): `work` corre fuera del token y
// **no debe tocar Lua**; devuelve una `deliverFn` que sí corre con el token
// recuperado y construye allí los valores. Mientras la goroutine de fondo
// trabaja, la goroutine de la task está bloqueada en `<-ch` sin el token, así que
// otras tasks progresan —el loop no se congela.
func (s *scheduler) suspend(L *lua.LState, work func() deliverFn) []lua.LValue {
	// Substrato de cancelación (S07): si esta task ya está cancelada al llegar al
	// punto de suspensión, aborta sin siquiera lanzar el trabajo de fondo. Si se
	// cancela *mientras* está suspendida, el `select` de abajo la despierta por
	// `cancelCh` en vez de por el resultado.
	t, hasTask := s.taskOf(L)

	// Watchdog (S09): este ⏸ **cierra el slice** en curso —la task va a soltar el
	// token—, así que se desarma su temporizador. Antes, si el watchdog ya disparó
	// (un slice anterior justo en el límite), se reclama el aborto por presupuesto
	// igual que la cancelación: el ⏸ es el punto natural donde el aborto cooperativo
	// surte efecto. Y si la task fue cancelada (S07/S08), aborta también aquí.
	if hasTask {
		s.disarmWatchdog(t)
		if s.claimBudgetAbort(t) {
			s.abort(t)
		}
		if isClosed(t.cancelCh) {
			s.abort(t)
		}
	}

	ch := make(chan deliverFn, 1)
	go func() { ch <- work() }()
	s.release()

	var d deliverFn
	if hasTask {
		select {
		case d = <-ch:
		case <-t.cancelCh:
			// Cancelada mientras esperaba: recupera el token y desenrolla. El
			// trabajo de fondo seguirá hasta su fin (su `deliverFn` se descarta);
			// para `sleep` es solo un `time.After`. S08 podrá propagar la
			// cancelación al trabajo en curso vía `context`.
			s.acquire()
			s.abort(t)
		}
	} else {
		d = <-ch // sin task (no debería ocurrir en ⏸ reales): comportamiento de S04
	}

	s.acquire()
	// Watchdog (S09): re-adquirido el token, arranca un **slice nuevo** —el código
	// Lua que corre tras el ⏸ tiene su propio presupuesto—. Así un bucle de CPU
	// puro intercalado con suspensiones no acumula tiempo entre slices: cada tramo
	// continuo se mide aparte (sin falsos positivos para trabajo normal que cede a
	// menudo).
	if hasTask {
		s.armWatchdog(t)
	}
	return d(L)
}

// taskOf devuelve la task cuya goroutine corre sobre el thread `L`, si la hay.
// El chunk principal y los handlers síncronos corren sobre `host` (sin task).
func (s *scheduler) taskOf(L *lua.LState) (*task, bool) {
	v, ok := s.coToTask.Load(L)
	if !ok {
		return nil, false
	}
	return v.(*task), true
}

// cancelTask es el **punto de entrada de la cancelación**. Lo llaman
// `Task:cancel()` (S08, público) y `all`/`race` (S07) para abortar tasks. Marca
// la task como cancelada (`reason = abortCancel`) y cierra su `cancelCh` (una
// sola vez, `cancelOnce`); la task abortará en su siguiente punto de suspensión
// (`suspend`/`await`).
//
// **No-op si la task ya terminó:** cancelar una task que ya cerró su desenlace no
// debe convertir retroactivamente su resultado en `ECANCELED` —terminó bien (o
// con error) ANTES de la cancelación, y eso es lo que su `await` debe seguir
// entregando—. Por eso, si `t.done`, no se toca `canceled`. Todas las llamadas
// (`Task:cancel`, `all`/`race`) corren **bajo el token**, igual que el `t.done`
// de `runTask`, así que leerlo aquí es seguro sin candado.
func (s *scheduler) cancelTask(t *task) {
	if t.done {
		return // ya resolvió: la cancelación no reescribe su desenlace
	}
	t.cancelOnce.Do(func() {
		t.canceled = true
		t.reason = abortCancel
		close(t.cancelCh)
	})
}

// abort desenrolla la pila Go de la task lanzando el pánico centinela. Lo llaman
// `suspend`/`Task:await`/`Future:await` cuando detectan `cancelCh` cerrado (y, en
// S09, el watchdog en su slice). Marca `t.aborting` **antes** de lanzar: ese flag
// es lo que las versiones envueltas de `pcall`/`xpcall` (cancel.go) consultan
// para re-lanzar el centinela en vez de devolverlo a Lua —así el aborto **no es
// capturable** (§1.3)—. `reason` (cancelación o presupuesto) la fijó ya quien
// cerró el canal (`cancelTask`); si por lo que fuera no estuviera, se asume
// cancelación.
//
// El pánico sube por la pila Go de la goroutine de la task, atravesando los
// `pcall` de usuario (re-lanzado por los envoltorios) hasta el `CallByParam` de
// `runTask`, que lo recupera; `runTask` ve `t.canceled`, descarta el desenlace y
// corre la pila de `cleanup`.
//
// CIERRE DE UPVALUES ANTES DE DESENROLLAR (la corrección que S16 destapó). Un
// `cleanup` registrado por la task casi siempre **captura locales por upvalue** —el
// idioma canónico de §6 es `nu.task.cleanup(function() proc:kill() end)`, donde
// `proc` es un local del cuerpo de la task—. Mientras la task corre, esos upvalues
// están **abiertos**: apuntan a slots del registro de `co`. En un retorno normal,
// gopher-lua los **cierra** (copia el valor dentro del `Upvalue`) al salir del
// scope; pero nuestro aborto es un **pánico Go** que NO ejecuta ese cierre, y el
// `PCall` que lo recupera **resetea el registro de `co`** (pone esos slots a `nil`).
// Resultado: cuando luego `runCleanups` ejecuta el liberador, su upvalue lee un slot
// ya `nil` → *nil pointer* al usar `proc`. Por eso, **antes** de panicar, cerramos
// todos los upvalues abiertos de `co` con `closeOpenUpvalues` (vía el camino público
// `Error` de gopher-lua, que hace `closeAllUpvalues`): así los valores capturados
// sobreviven al reseteo del registro y los `cleanup` los ven intactos. Sin esto, el
// criterio de hecho de S16 ("un spawn se mata por cleanup al cancelar la task")
// fallaría con un nil deref. (Ver claude_decisions.md, S16.)
func (s *scheduler) abort(t *task) {
	t.aborting = true
	if t.reason == abortNone {
		t.reason = abortCancel
	}
	s.closeOpenUpvalues(t.co)
	panic(abortSignal{t: t})
}

// abortUpvalueSentinel es el valor (una tabla, no un string) con que forzamos a
// gopher-lua a cerrar los upvalues abiertos del thread que se aborta. `LState.Error`
// con un valor **no-string** ejecuta `closeAllUpvalues` antes de panicar (es el mismo
// cierre que un `error{...}` normal dispara); aprovechamos ese efecto sin depender de
// API interna. El pánico que `Error` lanza lo capturamos de inmediato (recover) y lo
// descartamos: el aborto real lo lleva el `abortSignal` que lanzamos justo después.
var abortUpvalueSentinel = &struct{ tag string }{tag: "nu.abort.close-upvalues"}

// closeOpenUpvalues cierra los upvalues abiertos del thread `co` (los que capturan
// locales de los frames que el aborto va a desenrollar), de modo que los valores
// capturados sobrevivan al reseteo del registro que hace el `PCall` al recuperar el
// pánico (ver `abort`). Lo hace por el único camino público que gopher-lua ofrece
// para ello: `co.Error(tabla)` —que internamente llama a `closeAllUpvalues`— envuelto
// en un `recover` que se traga su pánico (no es el aborto; el aborto lo lleva el
// `panic(abortSignal)` posterior). No toca el registro de tasks ni el token; solo
// fuerza el cierre de upvalues sobre `co`, que es la goroutine que corre `abort`.
func (s *scheduler) closeOpenUpvalues(co *lua.LState) {
	defer func() { _ = recover() }() // el pánico de Error es solo el vehículo del cierre; se descarta
	tbl := co.NewTable()
	tbl.RawSetString("__nu_abort", lua.LString(abortUpvalueSentinel.tag))
	co.Error(tbl, 0) // cierra closeAllUpvalues y panica; el recover de arriba lo absorbe
}

// runCleanups ejecuta la pila LIFO de liberadores registrados con
// `nu.task.cleanup` (§3, §1.3). **Presupone el token tomado** (lo llama
// `runTask`). Corre en orden inverso al de registro —el último registrado, el
// primero en correr, semántica `defer`— y SIEMPRE: éxito, error o aborto de la
// task. Cada liberador es código Lua **síncrono** sobre un thread efímero, bajo
// `pcall` por frontera (ADR-008): un error en un cleanup no impide que corran los
// demás ni tumba el proceso, queda en el log (best-effort; evento formal en S10).
//
// `t.aborting` se baja al entrar: ya no se desenrolla el cuerpo de la task, así
// que un `pcall` dentro de un cleanup vuelve a capturar con normalidad.
func (s *scheduler) runCleanups(t *task) {
	t.aborting = false
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		fn := t.cleanups[i]
		co, _ := s.host.NewThread()
		if err := co.CallByParam(lua.P{Fn: fn, NRet: 0, Protect: true}); err != nil {
			_ = s.rt.log.write(levelError, s.rt.currentOwner(),
				"un liberador de nu.task.cleanup lanzó: "+errString(raisedValue(err)))
		}
	}
	t.cleanups = nil
}

// isClosed comprueba sin bloquear si un canal-señal (cerrado para difundir) ya
// fue cerrado. Solo válido para canales que únicamente se cierran (nunca se les
// envía), como `cancelCh`.
func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// taskSpawn implementa `nu.task.spawn(fn, ...) -> Task` (§3): lanza una task con
// `fn` y los argumentos extra, y devuelve su handle.
func (s *scheduler) taskSpawn(L *lua.LState) int {
	fn := L.CheckFunction(1)
	top := L.GetTop()
	args := make([]lua.LValue, 0, top-1)
	for i := 2; i <= top; i++ {
		args = append(args, L.Get(i))
	}
	t := s.spawn(fn, args)

	ud := L.NewUserData()
	ud.Value = t
	L.SetMetatable(ud, L.GetTypeMetatable(taskTypeName))
	L.Push(ud)
	return 1
}

// taskAwait implementa `Task:await() -> any` (§3): espera el resultado de otra
// task. Si ya terminó, devuelve su desenlace en el acto; si no, suelta el token
// y bloquea hasta que la esperada cierre su `doneCh`, entonces recupera el token.
// Devuelve los valores de la task esperada, o **relanza** su error (nativo, vía
// `L.Error`; capturable con `pcall`, no es cancelación, §1.3).
//
// **`ECANCELED` solo observable (S08, §1.3):** si la task esperada fue
// **cancelada** (terminó por aborto, sin `results` ni `errValue`), `await` lanza
// un `ECANCELED` estructurado. Es la *observación* de la cancelación de OTRA
// task: el awaiter SÍ puede capturarlo con `pcall` —no es el aborto del propio
// awaiter (eso, si lo cancelaran a él, no sería capturable)—. Así "una task
// cancelada, al ser esperada, hace que `await` entregue ECANCELED" sin que ello
// rompa la inmunidad del desenrollado.
//
// `await` es ⏸: llamarla fuera de una task es un error (§1.3) → `EINVAL`. La
// detección es por estado de ejecución: el chunk principal y los handlers
// síncronos corren sobre `host`; las tasks, sobre su propio `co`.
func (s *scheduler) taskAwait(L *lua.LState) int {
	if L == s.host {
		raiseError(L, CodeEINVAL, "Task:await solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	ud := L.CheckUserData(1)
	t, ok := ud.Value.(*task)
	if !ok {
		raiseError(L, CodeEINVAL, "Task:await espera un handle de Task", lua.LNil)
		return 0
	}
	if t.co == L {
		raiseError(L, CodeEINVAL, "una task no puede esperarse a sí misma", lua.LNil)
		return 0
	}

	t.awaited = true
	// Substrato de cancelación (S07): si la task que espera (`self`) es cancelada
	// mientras está bloqueada en este `await`, debe abortar igual que en `suspend`.
	self, hasSelf := s.taskOf(L)
	// Watchdog (S09): si el watchdog ya disparó para el awaiter, aborta aquí (este
	// `await` es un punto de suspensión). El claim convierte el corte en aborto por
	// presupuesto antes de bloquear.
	if hasSelf && s.claimBudgetAbort(self) {
		s.abort(self)
	}
	if hasSelf && isClosed(self.cancelCh) {
		s.abort(self)
	}
	if !t.done {
		// El awaiter va a soltar el token: su slice termina, desarma el watchdog;
		// al re-adquirir abajo arranca uno nuevo.
		if hasSelf {
			s.disarmWatchdog(self)
		}
		s.release()
		if hasSelf {
			select {
			case <-t.doneCh:
				s.acquire()
				s.armWatchdog(self)
			case <-self.cancelCh:
				s.acquire()
				s.abort(self)
			}
		} else {
			<-t.doneCh
			s.acquire()
		}
	}

	// Observación del aborto de la task esperada (§1.3): si fue abortada (cancelada
	// o por watchdog), no entregó valor; el awaiter lo *observa* como un error
	// capturable. Se comprueba antes que `errValue` porque una task abortada nunca
	// tiene `errValue` (su desenlace se descartó), pero el orden deja la intención
	// clara. El **código** distingue el motivo (§1.4): `EBUDGET` si la mató el
	// watchdog por exceder un slice (S09), `ECANCELED` si fue cancelación (S08).
	// Ambos son *observación* de OTRA task —el awaiter SÍ los captura—, no el
	// aborto del propio awaiter (que, si lo abortaran a él, sería inmune).
	if t.canceled {
		if t.reason == abortBudget {
			raiseError(L, CodeEBUDGET, "la task esperada excedió el presupuesto de slice (watchdog)", lua.LNil)
			return 0
		}
		raiseError(L, CodeECANCELED, "la task esperada fue cancelada", lua.LNil)
		return 0
	}
	if t.errValue != nil {
		L.Error(t.errValue, 1) // relanza el mismo objeto; no retorna (hace panic)
		return 0
	}
	for _, r := range t.results {
		L.Push(r)
	}
	return len(t.results)
}

// taskCancel implementa `Task:cancel()` (§3, §1.3): cancelación cooperativa de
// otra task. **No suspende** (es síncrona desde fuera): solo cierra la señal de
// cancelación; la task abortará en su siguiente punto de suspensión (o slice),
// de forma **no capturable**, y correrán sus `cleanup`. Puede llamarse desde el
// chunk, una task o un handler síncrono. Cancelar una task ya terminada o ya
// cancelada es un no-op inocuo (`cancelOnce`). Cancelarse a sí misma es legal:
// el aborto surte efecto en el siguiente ⏸ de la propia task.
func (s *scheduler) taskCancel(L *lua.LState) int {
	ud := L.CheckUserData(1)
	t, ok := ud.Value.(*task)
	if !ok {
		raiseError(L, CodeEINVAL, "Task:cancel espera un handle de Task", lua.LNil)
		return 0
	}
	s.cancelTask(t)
	return 0
}

// taskCleanup implementa `nu.task.cleanup(fn)` [W] (§3, §1.3): registra `fn` en
// la **pila LIFO** de liberadores de la task actual. Todos corren al terminar la
// task —éxito, error o aborto—, en orden inverso al de registro (`runCleanups`).
// Es el `defer` de esta casa: cerrar procesos, regiones, handlers de input.
//
// **Fuera de una task → `EINVAL`**: `cleanup` no tiene sentido en el chunk
// principal ni en un handler síncrono (no hay task a la que atar el liberador).
// La detección es la misma que el resto: el código de task corre sobre su propio
// `co`; el chunk y los handlers, sobre `host` (o un thread efímero sin task
// registrada en `coToTask`). Corre bajo el token, así que tocar `t.cleanups` no
// necesita candado.
func (s *scheduler) taskCleanup(L *lua.LState) int {
	fn := L.CheckFunction(1)
	t, hasTask := s.taskOf(L)
	if !hasTask {
		raiseError(L, CodeEINVAL, "nu.task.cleanup solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	t.cleanups = append(t.cleanups, fn)
	return 0
}

// suspendEcho es la **primitiva ⏸ interna de prueba** del puente (S04). No es
// superficie pública: las pruebas la registran como global para ejercitar la
// suspensión de extremo a extremo —una task la llama, suelta el token, una
// goroutine de fondo acarrea el valor y, al volver, la task lo recibe—. Acepta
// string o number (datos triviales de copiar a Go sin tocar Lua desde la
// goroutine). Es el modelo en miniatura de toda primitiva ⏸ real.
func (s *scheduler) suspendEcho(L *lua.LState) int {
	if L == s.host {
		raiseError(L, CodeEINVAL, "suspend_echo solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	v := L.Get(1)
	var (
		str   string
		num   float64
		isNum bool
	)
	switch x := v.(type) {
	case lua.LString:
		str = string(x)
	case lua.LNumber:
		num = float64(x)
		isNum = true
	default:
		raiseError(L, CodeEINVAL, "suspend_echo espera string o number", lua.LNil)
		return 0
	}

	vals := s.suspend(L, func() deliverFn {
		// Goroutine de fondo: captura solo datos Go (str/num), nunca el LState.
		return func(L *lua.LState) []lua.LValue {
			if isNum {
				return []lua.LValue{lua.LNumber(num)}
			}
			return []lua.LValue{lua.LString(str)}
		}
	})
	for _, v := range vals {
		L.Push(v)
	}
	return len(vals)
}

// raisedValue extrae el objeto lanzado de un error de `CallByParam`/`PCall`.
// gopher-lua lo envuelve en `*lua.ApiError.Object` (la tabla estructurada de
// §1.4 cuando vino de `raiseError`/`error{...}`); si no, cae al texto del error.
func raisedValue(err error) lua.LValue {
	if ae, ok := err.(*lua.ApiError); ok {
		if ae.Object != nil && ae.Object != lua.LNil {
			return ae.Object
		}
	}
	return lua.LString(err.Error())
}

// errString rinde un error de task a texto para el log: si es la tabla
// estructurada (§1.4), saca `code: message`; si no, su representación por
// defecto.
func errString(v lua.LValue) string {
	if tbl, ok := v.(*lua.LTable); ok {
		code, _ := tbl.RawGetString("code").(lua.LString)
		msg, _ := tbl.RawGetString("message").(lua.LString)
		if code != "" || msg != "" {
			return string(code) + ": " + string(msg)
		}
	}
	return v.String()
}
