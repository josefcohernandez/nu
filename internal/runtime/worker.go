package runtime

import (
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// `nu.worker` — paralelismo opt-in (api.md §13, ADR-008). Un worker es un estado
// Lua **nuevo y aislado** corriendo en su propia goroutine, con su propio
// scheduler: un mini-runtime completo (scheduler propio, tasks, timers, futures —
// todo `nu.task` [W]) **sin watchdog** (G15: los workers existen para quemar CPU a
// gusto; el control es `terminate()` desde el principal más las `caps`).
//
// El aislamiento es total (ADR-008): NADA de memoria Lua se comparte entre el
// estado principal y el del worker. La comunicación es por **paso de mensajes
// JSON-ables COPIADOS** (luaToGo en un estado → interface{} Go neutro → goToLua en
// el otro): tablas, números, strings, booleanos y nil cruzan; closures, userdata,
// threads y Blocks NO (un intento lanza `EINVAL`, reusando el codec de §12). Las
// colas son canales Go **acotados**: `send` SUSPENDE cuando la cola está llena
// (backpressure, §13/§8), nunca falla con `EIO` como los streams. Cada estado Lua
// solo lo toca su propia goroutine bajo su propio token; el cruce de mensajes es
// por los canales (copia + happens-before), de donde sale "cero data races" entre
// los dos schedulers.
//
// ALCANCE de S34 (lo que vive aquí): `nu.worker.spawn` (solo en el estado
// principal), el filtrado de `caps` (G6, dos granularidades, deny-by-default),
// `Worker:send`/`Worker:recv` (colas acotadas), `nu.worker.parent.send/recv`
// (dentro del worker) y un `Worker:terminate` que corta el worker para que los
// tests no dejen goroutines colgadas. FRONTERA con S35: `Worker:on_message`
// (excluyente con `recv`, G8), el `terminate` robusto/definitivo y la prueba a
// fondo de varias tasks/timers/futures DENTRO del worker son S35; aquí el
// mini-runtime ya las soporta (reusa el scheduler), pero su validación exhaustiva
// es la sesión siguiente.

// workerTypeName identifica la metatabla del handle `Worker` que devuelve
// `nu.worker.spawn` (en el estado principal).
const workerTypeName = "nu.worker.Worker"

// workerQueueCap es la capacidad de las colas acotadas worker↔padre. Pequeña a
// propósito: el backpressure (G de §13) debe ser observable —un productor que
// adelanta al consumidor SUSPENDE pronto, en vez de acumular una cola ilimitada
// que escondería el desacople de ritmos—. No es superficie de la API (no se
// configura desde Lua en v1); es un detalle del transporte.
const workerQueueCap = 16

// workerChannels son las dos colas acotadas que conectan el estado principal con
// el del worker. `toWorker` lleva los mensajes de `Worker:send` (padre) a
// `nu.worker.parent.recv` (worker); `fromWorker`, los de `nu.worker.parent.send`
// (worker) a `Worker:recv` (padre). Llevan el **valor Go neutro ya copiado**
// (interface{} de luaToGo): la copia ocurre en el lado emisor, bajo su token, y la
// reconstrucción (goToLua) en el receptor, bajo el suyo —ningún LValue cruza la
// frontera de goroutines, que es lo que blinda el aislamiento—.
//
// `done` se cierra al terminar el worker (`terminate`, o el fin natural de su
// módulo): interrumpe cualquier `send`/`recv` bloqueado en las colas para que no
// quede una goroutine de fondo colgada esperando a una punta que ya no existe.
type workerChannels struct {
	toWorker   chan interface{}
	fromWorker chan interface{}
	done       chan struct{}
}

// luaWorker es el handle `Worker` que vive en el estado PRINCIPAL: las colas
// compartidas con el worker, el sub-Runtime del worker y el corte idempotente.
type luaWorker struct {
	chans *workerChannels

	// wrt es el Runtime del worker (su `*lua.LState` y su scheduler propios). Lo
	// toca SOLO la goroutine del worker (su `runWorker`); el estado principal nunca
	// lee su Lua —solo cierra `done` para cortarlo—. Guardarlo aquí permite a
	// `terminate`/`Close` cerrarlo y soltar sus recursos.
	wrt *Runtime

	terminateOnce sync.Once
	terminated    chan struct{} // se cierra al terminar (espejo legible)
}

// registerWorker cuelga `nu.worker` del global `nu` en el estado PRINCIPAL (§13,
// §16: `worker.spawn` NO es [W] —no hay workers anidados—). Instala además la
// metatabla del tipo `Worker` con `send`/`recv`/`terminate`. Solo se llama desde
// `registerNu` (estado principal); el worker recibe su propia tabla `nu.worker`
// (solo `parent`) vía `registerWorkerParent`.
func (rt *Runtime) registerWorker(nu *lua.LTable) {
	L := rt.L

	mt := L.NewTypeMetatable(workerTypeName)
	methods := L.NewTable()
	methods.RawSetString("send", L.NewFunction(rt.workerSend))
	methods.RawSetString("recv", L.NewFunction(rt.workerRecv))
	methods.RawSetString("terminate", L.NewFunction(rt.workerTerminate))
	L.SetField(mt, "__index", methods)

	workerT := L.NewTable()
	workerT.RawSetString("spawn", L.NewFunction(rt.workerSpawn))
	nu.RawSetString("worker", workerT)
}

// checkWorker recupera el `*luaWorker` del userdata `self`. Lanza `EINVAL` si no
// es un handle de `Worker`.
func checkWorker(L *lua.LState) *luaWorker {
	ud := L.CheckUserData(1)
	w, ok := ud.Value.(*luaWorker)
	if !ok {
		raiseError(L, CodeEINVAL, "Worker: se esperaba un handle de Worker", lua.LNil)
		return nil
	}
	return w
}

// ── nu.worker.spawn ───────────────────────────────────────────────────────────

// workerSpawn implementa `nu.worker.spawn(module: string, opts?) -> Worker` (§13).
// Levanta un estado Lua NUEVO en su propia goroutine, con su propio scheduler
// (mini-runtime SIN watchdog), cargando `module` (resoluble por las rutas de
// `require` del loader del padre). `opts.caps?: string[]` restringe la API del
// worker (G6, deny-by-default). No suspende: la creación es síncrona y devuelve el
// handle al acto; el módulo del worker corre en paralelo en su goroutine.
//
// No es ⏸ y NO está disponible en workers (§16: sin anidar): se registra solo en
// el estado principal, así que dentro de un worker `nu.worker.spawn` no existe.
func (rt *Runtime) workerSpawn(L *lua.LState) int {
	module := L.CheckString(1)
	if module == "" {
		raiseError(L, CodeEINVAL, "nu.worker.spawn: module es obligatorio (string no vacío)", lua.LNil)
		return 0
	}

	caps, capsGiven, ok := parseWorkerCaps(L, L.Get(2))
	if !ok {
		return 0 // parseWorkerCaps ya lanzó EINVAL
	}

	chans := &workerChannels{
		toWorker:   make(chan interface{}, workerQueueCap),
		fromWorker: make(chan interface{}, workerQueueCap),
		done:       make(chan struct{}),
	}

	// Las rutas de `require` del worker se siembran de los plugins que el loader del
	// padre conoce: así un `require("mi_modulo")` dentro del worker resuelve el mismo
	// `lua/` que en el principal (§13: "las rutas de require de plugins están
	// disponibles dentro del worker"). Se calculan bajo el token del padre (tocan el
	// loader), antes de arrancar la goroutine del worker.
	patterns := rt.ldr.workerRequirePatterns()

	wrt := newWorkerRuntime(rt, chans, caps, capsGiven)

	w := &luaWorker{
		chans:      chans,
		wrt:        wrt,
		terminated: make(chan struct{}),
	}

	// La goroutine del worker corre TODO su Lua bajo su propio token: el estado
	// principal nunca lo toca. Al terminar (módulo completo o `terminate`), cierra
	// `done` y libera el sub-Runtime.
	go w.run(module, patterns)

	// Rastrea el worker para cortarlo en `Runtime.Close` (red de seguridad, como
	// procs/streams/ws): ningún worker debe sobrevivir al proceso del padre.
	rt.sched.trackWorker(w)

	ud := L.NewUserData()
	ud.Value = w
	L.SetMetatable(ud, L.GetTypeMetatable(workerTypeName))
	L.Push(ud)
	return 1
}

// parseWorkerCaps extrae `opts.caps?` (§13): un array de strings (nombres de
// capacidad, granularidad de módulo `"fs"` o de función `"fs.read"`). Devuelve
// `(caps, capsGiven, ok)`: `capsGiven` distingue "sin `caps`" (toda la API [W]) de
// "`caps={}` vacío" (deny-by-default: casi nada). Valida bajo el token del padre y
// lanza `EINVAL` ante un uso malo, antes de crear nada.
func parseWorkerCaps(L *lua.LState, optsV lua.LValue) (caps map[string]bool, capsGiven, ok bool) {
	switch opts := optsV.(type) {
	case *lua.LNilType, nil:
		return nil, false, true // sin opts: toda la API [W]
	case *lua.LTable:
		capsV := opts.RawGetString("caps")
		switch cv := capsV.(type) {
		case *lua.LNilType, nil:
			return nil, false, true // opts sin `caps`: toda la API [W]
		case *lua.LTable:
			caps = make(map[string]bool)
			bad := false
			cv.ForEach(func(_, v lua.LValue) {
				s, sok := v.(lua.LString)
				if !sok || string(s) == "" {
					bad = true
					return
				}
				caps[string(s)] = true
			})
			if bad {
				raiseError(L, CodeEINVAL,
					"nu.worker.spawn: opts.caps debe ser un array de nombres de capacidad (strings no vacíos)", lua.LNil)
				return nil, false, false
			}
			return caps, true, true
		default:
			raiseError(L, CodeEINVAL, "nu.worker.spawn: opts.caps debe ser un array de strings", lua.LNil)
			return nil, false, false
		}
	default:
		raiseError(L, CodeEINVAL, "nu.worker.spawn: opts debe ser una tabla", lua.LNil)
		return nil, false, false
	}
}

// ── Worker:send / Worker:recv (lado del padre, §13) ──────────────────────────

// workerSend implementa `Worker:send(msg)` ⏸ (§13): encola `msg` (valor JSON-able,
// COPIADO) hacia el worker. La COPIA (luaToGo) ocurre AQUÍ, bajo el token del
// padre —antes de suspender—: convierte el valor Lua a su representación Go neutra
// (validando que sea JSON-able; una función/userdata/Block → `EINVAL`). Luego
// SUSPENDE para encolarlo en la cola acotada: si está llena, la suspensión dura
// hasta que el worker consuma (backpressure). Un worker terminado → `ECLOSED`.
func (rt *Runtime) workerSend(L *lua.LState) int {
	if !rt.requireTask(L, "Worker:send") {
		return 0
	}
	w := checkWorker(L)
	if w == nil {
		return 0
	}
	msg := L.CheckAny(2)
	// COPIA bajo el token del padre: el valor Lua → Go neutro. `useNull=false`: los
	// mensajes son valores JSON-ables corrientes, no documentos JSON (el sentinel
	// `nu.json.NULL` es userdata por-estado y no podría cruzar de todas formas; un
	// nil de mensaje va como nil Lua en el otro extremo). Rechaza lo no serializable
	// (closure/userdata/thread/Block) con `EINVAL` ANTES de suspender.
	goMsg := rt.luaToGo(L, msg, "worker")

	sendOnBoundedChan(L, rt.sched, w.chans.toWorker, w.chans.done, goMsg, "Worker:send")
	return 0
}

// workerRecv implementa `Worker:recv() -> msg` ⏸ (§13): recibe del worker (cola
// worker→padre). SUSPENDE hasta que haya un mensaje. Reconstruye el valor (goToLua)
// AQUÍ, bajo el token del padre. Un worker terminado SIN mensajes pendientes →
// `nil` (fin del canal, coherente con §8: una punta cerrada da `nil`, no lanza).
func (rt *Runtime) workerRecv(L *lua.LState) int {
	if !rt.requireTask(L, "Worker:recv") {
		return 0
	}
	w := checkWorker(L)
	if w == nil {
		return 0
	}
	return recvOnBoundedChan(L, rt.sched, w.chans.fromWorker, w.chans.done)
}

// workerTerminate implementa `Worker:terminate()` (§13): corta el worker de forma
// **inmediata y segura** —cancela todas sus tasks vivas (incluidas las suspendidas en
// un `sleep`/`http`/`recv` largo) y cierra sus colas, así que el worker queda
// quiescente al acto sin esperar a que venza un ⏸—. Es **idempotente**: terminar dos
// veces es inocuo. No suspende. (S35 añade `on_message`; el corte ya es definitivo.)
func (rt *Runtime) workerTerminate(L *lua.LState) int {
	w := checkWorker(L)
	if w == nil {
		return 0
	}
	w.terminate()
	return 0
}

// terminate corta el worker de forma **inmediata y segura** (§13). Es idempotente
// (`terminateOnce`) y no suspende. Dos señales, en este orden:
//
//  1. **cancela todas las tasks vivas del scheduler del worker** (`cancelAllTasks`):
//     cierra el `cancelAll` del scheduler del worker, que despierta a cualquier task
//     suspendida en un ⏸ —`nu.task.sleep`, `http`, `proc`, `await`, `parent.recv`...,
//     no solo `send`/`recv` en las colas— por el `select` de `suspend`/`taskAwait`,
//     y cancela el contexto de cada una (rompe también un slice de CPU puro, que el
//     worker no tiene watchdog para cortar, G15). Esto es lo que hace `terminate`
//     *inmediato*: sin ello un `sleep(60000)` colgaría al worker ~60 s, y su
//     goroutine (que comparte el `log`/`data_dir` del padre) seguiría viva tocando el
//     dataDir mientras un test lo borra (la fuga que destapó el review).
//  2. **cierra `done`**: interrumpe cualquier `send`/`recv` bloqueado en las colas
//     (ambas puntas, `ECLOSED`/fin de canal) y, al verlo el bucle del worker, lo
//     lleva a soltar su estado.
//
// Con (1)+(2) el `waitIdle()` de `driveUntilDone` alcanza la quiescencia al acto, la
// goroutine del worker corre `shutdown` (cierra su `*lua.LState`) y cierra
// `terminated`. El cierre efectivo del Lua del worker lo hace SU goroutine (dueña de
// su estado): el padre NUNCA lo toca (aislamiento). `terminate` no espera a
// `terminated` —es "inmediato" desde Lua—; quien debe garantizar que la goroutine ya
// no toca el dataDir/log (el `Close`/`stopAllWorkers` del padre) usa `wait()`.
func (w *luaWorker) terminate() {
	w.terminateOnce.Do(func() {
		// Cancela las tasks del worker ANTES de cerrar `done`: así las suspendidas en
		// un ⏸ que no observa `done` (sleep/http/...) también despiertan y el worker
		// queda quiescente de inmediato. El scheduler del worker solo lo tocan las
		// goroutines de sus tasks bajo su token; `cancelAllTasks` solo cierra un canal
		// y cancela contextos (seguro desde la goroutine del padre).
		w.wrt.sched.cancelAllTasks()
		close(w.chans.done)
	})
}

// wait bloquea hasta que la goroutine del worker haya terminado de cerrar su estado
// (`shutdown` cerró `terminated`). Lo usa el `Close`/`stopAllWorkers` del PADRE para
// no devolver el control —ni dejar que el test borre el `data_dir`— mientras la
// goroutine del worker aún vive y podría tocar el `log`/`data_dir` compartidos. NO se
// llama desde la goroutine del propio worker (se autobloquearía): solo desde el
// padre, tras `terminate`. Seguro de llamar varias veces (un canal cerrado se lee sin
// bloquear) y sobre un worker que terminó solo (su `shutdown` ya cerró `terminated`).
func (w *luaWorker) wait() {
	<-w.terminated
}

// ── nu.worker.parent.send / recv (lado del worker, §13) ──────────────────────

// registerWorkerParent cuelga `nu.worker.parent` del global `nu` DENTRO del estado
// del worker (§13): el otro extremo de las colas. `send`/`recv` son ⏸ sobre el
// puente `suspend` del scheduler **del worker**. NO existe `nu.worker.spawn` aquí
// (sin anidar, §16): el worker solo tiene `nu.worker.parent`.
func (wrt *Runtime) registerWorkerParent(nu *lua.LTable, chans *workerChannels) {
	L := wrt.L
	workerT := L.NewTable()
	parent := L.NewTable()
	parent.RawSetString("send", L.NewFunction(func(L *lua.LState) int {
		if !wrt.requireTask(L, "nu.worker.parent.send") {
			return 0
		}
		msg := L.CheckAny(1)
		goMsg := wrt.luaToGo(L, msg, "worker")
		// El worker envía por `fromWorker`; la cola del padre→worker (`toWorker`) es
		// para recibir. Misma cola acotada → backpressure simétrico.
		sendOnBoundedChan(L, wrt.sched, chans.fromWorker, chans.done, goMsg, "nu.worker.parent.send")
		return 0
	}))
	parent.RawSetString("recv", L.NewFunction(func(L *lua.LState) int {
		if !wrt.requireTask(L, "nu.worker.parent.recv") {
			return 0
		}
		return recvOnBoundedChan(L, wrt.sched, chans.toWorker, chans.done)
	}))
	workerT.RawSetString("parent", parent)
	nu.RawSetString("worker", workerT)
}

// ── transporte: send/recv sobre una cola acotada con backpressure ────────────

// sendOnBoundedChan encola `goMsg` en `out` (una cola acotada) SUSPENDIENDO si está
// llena (backpressure, §13/§8). El envío real ocurre FUERA del token, en la
// goroutine de fondo del puente `suspend` —así otra task del mismo estado progresa
// mientras este `send` espera hueco—. `done` cerrado (worker terminado) → `ECLOSED`
// (la otra punta ya no consumirá; no tiene sentido esperar para siempre). `goMsg`
// es un valor Go ya copiado: no toca Lua en la goroutine de fondo (aislamiento).
func sendOnBoundedChan(L *lua.LState, s *scheduler, out chan interface{}, done chan struct{}, goMsg interface{}, who string) {
	s.suspend(L, func() deliverFn {
		select {
		case out <- goMsg:
			return func(L *lua.LState) []lua.LValue { return nil }
		case <-done:
			return func(L *lua.LState) []lua.LValue {
				raiseError(L, CodeECLOSED, who+": el worker ha terminado", lua.LNil)
				return nil
			}
		}
	})
}

// recvOnBoundedChan recibe el siguiente mensaje de `in` (cola acotada)
// SUSPENDIENDO hasta que llegue uno. Reconstruye el valor (goToLua) bajo el token,
// en la `deliverFn`. Si `done` se cierra (worker terminado) Y la cola está vacía →
// `nil` (fin del canal, no error): una punta cerrada rinde fin de stream, coherente
// con `Ws:recv`/§8.
//
// MENSAJES YA ENCOLADOS AL CERRARSE `done`: se entregan ANTES del fin de canal. El
// `select` de Go, cuando `in` y `done` están ambos listos, elige al azar; por eso,
// si despierta por la rama `done`, se hace un intento NO bloqueante de sacar uno de
// `in` (un mensaje que el emisor dejó justo antes de terminar). Como cada `recv`
// devuelve a lo sumo UN mensaje, llamadas sucesivas vacían la cola una a una: el
// primer `recv` tras `done` que encuentre la cola vacía es el que rinde `nil`. Así
// el consumidor drena todo lo encolado y solo entonces ve el fin del canal.
func recvOnBoundedChan(L *lua.LState, s *scheduler, in chan interface{}, done chan struct{}) int {
	rt := s.rt
	vals := s.suspend(L, func() deliverFn {
		select {
		case goMsg := <-in:
			return func(L *lua.LState) []lua.LValue {
				return []lua.LValue{rt.goToLua(L, goMsg, false)}
			}
		case <-done:
			// Worker terminado: si aún quedan mensajes encolados (`in` no vacía),
			// entrega el siguiente; solo cuando la cola está vacía declara fin de canal.
			select {
			case goMsg := <-in:
				return func(L *lua.LState) []lua.LValue {
					return []lua.LValue{rt.goToLua(L, goMsg, false)}
				}
			default:
				return func(L *lua.LState) []lua.LValue {
					return []lua.LValue{lua.LNil} // fin del canal
				}
			}
		}
	})
	return pushAll(L, vals)
}

// ── ciclo de vida de la goroutine del worker ─────────────────────────────────

// run es el cuerpo de la goroutine del worker (la creó `workerSpawn`). Configura
// las rutas de `require`, carga y corre `module` COMO UNA TASK (su top-level puede
// llamar funciones ⏸ —`nu.worker.parent.recv` en bucle es el patrón natural—),
// conduce el event loop del worker hasta la quiescencia y, al terminar, cierra
// `done` y libera el estado.
//
// El módulo corre como task, no como chunk: un chunk sobre `host` no podría
// suspender (`requireTask` exige `L != rt.L`), y el worker vive precisamente para
// recibir/enviar (⏸). `spawn` + `waitIdle` reusa exactamente la maquinaria de S04.
func (w *luaWorker) run(module string, patterns []string) {
	wrt := w.wrt
	s := wrt.sched

	// Configura `package.path` del worker con los `lua/` de los plugins del padre,
	// bajo el token del worker (toca su Lua). Si no hay plugins, no se abre `require`
	// (el módulo tendría que ser uno embebido/preload; en v1 el módulo siempre es de
	// un plugin, así que sin plugins el require fallará con un error accionable de
	// Lua, que la task del worker captura sin tumbar nada).
	s.acquire()
	setupWorkerRequirePaths(wrt.L, patterns)
	// El módulo como task: `require(module)` corre su top-level dentro de la task, así
	// que puede usar ⏸ (parent.recv/send). Un error en el módulo NO tumba el proceso
	// (ADR-008): la task lo aísla (runTask lo loguea best-effort).
	bootstrap, err := wrt.L.LoadString("return require(...)")
	if err != nil {
		// No debería ocurrir (chunk constante); si pasa, no hay módulo que correr.
		s.release()
		w.shutdown()
		return
	}
	s.spawn(bootstrap, []lua.LValue{lua.LString(module)})
	s.release()

	// Conduce el event loop hasta que no quede trabajo de primer plano (la task del
	// módulo terminó y no dejó tasks/defers pendientes), o hasta que `terminate`
	// cierre `done`. Se vigilan ambas: un worker que corre un bucle infinito de
	// `parent.recv` nunca queda quiescente —lo corta `terminate`—.
	w.driveUntilDone()
	w.shutdown()
}

// driveUntilDone bloquea (en la goroutine del worker) hasta que su scheduler quede
// quiescente: ninguna task viva ni `defer` pendiente (`waitIdle`). Es el event loop
// del worker, exactamente como `EvalString` conduce el del principal: mientras esta
// goroutine espera en `waitIdle` con el token soltado, las tasks del worker corren
// (la del módulo y las que lance), y `waitIdle` despierta cuando la última termina.
//
// CÓMO LLEGA LA QUIESCENCIA CON `terminate`. Un worker que corre un bucle infinito de
// `parent.recv` (o un `nu.task.sleep` largo) nunca quedaría quiescente por sí solo.
// `terminate()` lo fuerza: cancela TODAS las tasks vivas del worker
// (`cancelAllTasks`) —las suspendidas en cualquier ⏸ despiertan por el `cancelAll`
// del scheduler y abortan; los slices de CPU se rompen por contexto— y cierra `done`
// —las bloqueadas en las colas reciben `ECLOSED`/fin de canal—. Así toda task viva
// termina y `waitIdle` retorna enseguida tras `terminate`, sin esperar a que venza un
// `sleep`. (Antes del arreglo del review, `terminate` solo cerraba `done`, que no
// despierta a un `sleep`/`http`: el worker colgaba hasta vencer y su goroutine fugaba.)
//
// CLAVE (cero data races): SIEMPRE se espera a la quiescencia antes de devolver el
// control a `shutdown`, que cerrará el `*lua.LState` del worker. Cerrar el estado
// mientras la goroutine de una task aún desenrolla Lua sería una carrera; por eso no
// se cierra hasta que `waitIdle` confirma que no queda ninguna task viva.
func (w *luaWorker) driveUntilDone() {
	w.wrt.sched.waitIdle()
}

// shutdown cierra el worker: marca `done` (idempotente, para que el padre vea fin de
// canal aunque el worker terminara solo), cierra su `*lua.LState` y libera sus
// recursos (timers, etc.) vía `Runtime.Close`, y marca `terminated`. Lo toca SOLO
// la goroutine del worker: el estado principal nunca cierra el Lua del worker.
func (w *luaWorker) shutdown() {
	// Si el worker terminó solo (no por `terminate`), cierra `done` igualmente: el
	// padre debe ver fin de canal en su próximo `recv`, y un `send` posterior →
	// `ECLOSED`. Idempotente con `terminate`.
	w.terminate()
	// Cierra el estado del worker y corta su mini-runtime (timers/procs/etc.): nada
	// del worker debe sobrevivir. Lo hace la goroutine del worker (dueña de su Lua).
	w.wrt.Close()
	close(w.terminated)
}

// setupWorkerRequirePaths abre `package`/`require` en el estado del worker y fija
// `package.path` a los `lua/` de los plugins del padre (los mismos patrones que el
// loader del principal). Si no hay patrones, no abre `require`: un worker sin
// plugins no tiene módulos que cargar (su `require` fallará con un error de Lua
// accionable, aislado por la task). Espeja `loader.setupRequirePaths`, pero sobre
// el L del worker.
func setupWorkerRequirePaths(L *lua.LState, patterns []string) {
	if len(patterns) == 0 {
		return
	}
	L.Push(L.NewFunction(lua.OpenPackage))
	L.Push(lua.LString(lua.LoadLibName))
	L.Call(1, 0)
	pkg, ok := L.GetGlobal("package").(*lua.LTable)
	if !ok {
		return
	}
	pkg.RawSetString("path", lua.LString(strings.Join(patterns, ";")))
	pkg.RawSetString("cpath", lua.LString(""))
}
