package vmwasm

// Workers como instancias wasm (migracion-vm.md M12, ADR-008/§13). El regalo de
// ADR-019: cada worker es una Instance wasm distinta, con su **memoria lineal
// físicamente aislada** — más fuerte que el `*LState` separado de gopher (que
// comparte el heap de Go). Un worker es un mini-runtime completo (G15): su propio
// scheduler (nu.task), sin watchdog (existe para quemar CPU), controlado por
// terminate() + caps. Su único canal con el mundo es la mensajería con el padre.
//
// La frontera de workers **solo cruza datos, nunca referencias** (ADR-008): los
// mensajes son valores JSON-ables COPIADOS. No cruzan closures, userdata ni
// Blocks. En wasm eso es natural: el mensaje se decodifica a un `any` Go neutro en
// el lado emisor y se re-codifica en el estado del receptor; ningún puntero a la
// memoria lineal de una instancia toca la otra.
//
// Alcance (como M09-M11): M12 entrega el MECANISMO (aislamiento, mensajería
// acotada, caps, exclusión recv/on_message, terminate) contra código fuente
// directo; la resolución de `module` por el loader (require) es la integración de
// M13. Desviación anotada: nu.task es intrínseco al runtime del worker (vive en el
// preludio), siempre presente; las caps filtran los módulos respaldados por
// primitivas (fs/proc/http/...), que es donde se prueba el mecanismo de G6.

import (
	"sync"
)

// workerQueueCap es la capacidad de las colas padre↔worker. Pequeña a propósito
// (paridad con el kernel): hace el backpressure OBSERVABLE. No es superficie de
// API (no se configura desde Lua en v1), es un detalle de transporte.
const workerQueueCap = 16

// workerChannels son las dos colas acotadas y la señal de fin de un worker.
type workerChannels struct {
	toWorker   chan any      // padre Worker:send → worker parent.recv
	fromWorker chan any      // worker parent.send → padre Worker:recv
	done       chan struct{} // se cierra al terminar (terminate o fin natural)
	doneOnce   sync.Once
}

func newWorkerChannels() *workerChannels {
	return &workerChannels{
		toWorker:   make(chan any, workerQueueCap),
		fromWorker: make(chan any, workerQueueCap),
		done:       make(chan struct{}),
	}
}

func (c *workerChannels) closeDone() { c.doneOnce.Do(func() { close(c.done) }) }

// worker es un worker vivo visto desde el padre: su Instance aislada, sus canales
// y su join de apagado. `id`/`parent` son el ancla al registro del Pool principal:
// permiten que shutdown() se retire a sí mismo del mapa (A-07).
type worker struct {
	inst       *Instance
	chans      *workerChannels
	terminated chan struct{}
	termOnce   sync.Once
	id         int64 // id en el registro del padre (0 hasta registrar)
	parent     *Pool // Pool principal que lleva el registro (nil hasta registrar)
}

// registerWorker registra un worker en el Pool y devuelve su id (el __wid del
// lado Lua). Sólo el Pool principal lleva este registro. Se llama ANTES de
// arrancar la goroutine del worker, para que shutdown() —que se retira del mapa
// (A-07)— nunca pueda correr sobre un worker aún sin id/parent.
func (p *Pool) registerWorker(w *worker) int64 {
	p.workerMu.Lock()
	defer p.workerMu.Unlock()
	p.workerNext++
	id := p.workerNext
	w.id = id
	w.parent = p
	p.workers[id] = w
	return id
}

// deregisterWorker retira un worker terminado del registro (A-07): evita el
// crecimiento monótono del mapa —y con él la retención de la struct y sus
// canales— en procesos de larga vida que spawneen workers periódicamente. Lo
// llaman reapIfDrained (fin sin mensajes pendientes) y el _recv que encuentra
// el fin de canal (zombie ya drenado). Idempotente (borrar una clave ausente
// es un no-op).
func (p *Pool) deregisterWorker(id int64) {
	p.workerMu.Lock()
	delete(p.workers, id)
	p.workerMu.Unlock()
}

// lookupWorker resuelve un worker por su id. Devuelve (w, known): `w` es el
// worker VIVO (nil si ya no está en el registro), y `known` indica si el id fue
// alguna vez un worker válido. Tras A-07 un worker terminado se retira del mapa,
// así que un id conocido (0 < id ≤ workerNext) que ya no está presente es un
// worker RETIRADO —send debe dar ECLOSED y recv nil, igual que cuando seguía en
// el mapa con `done` cerrado—; sólo un id fuera de ese rango (forjado/corrupto)
// es realmente inválido (EINVAL).
func (p *Pool) lookupWorker(id int64) (w *worker, known bool) {
	p.workerMu.Lock()
	defer p.workerMu.Unlock()
	if w, ok := p.workers[id]; ok {
		return w, true
	}
	return nil, id > 0 && id <= p.workerNext
}

// registerWorkerHost registra las primitivas del lado PADRE de los workers (M12).
// El preludioWorkerHost las envuelve como nu.worker.spawn y los métodos del
// handle Worker.
func (p *Pool) registerWorkerHost() {
	// nu.worker._spawn(source, opts?) -> wid. SÍNCRONA: crea la Instance del
	// worker (aislada), la arranca en su goroutine con su propio RunTasks, y
	// devuelve su id. `source` es código Lua (M13 lo cambia por resolución del
	// módulo vía el loader).
	p.Register("worker._spawn", func(inst *Instance, args []any) ([]any, error) {
		source, _ := args[0].(string)
		if source == "" {
			return nil, &StructuredError{Code: "EINVAL", Message: "nu.worker.spawn: module es obligatorio"}
		}
		var opts map[string]any
		if len(args) > 1 {
			opts, _ = args[1].(map[string]any)
		}
		caps, capsGiven, err := parseWorkerCaps(opts)
		if err != nil {
			return nil, err
		}
		w, err := inst.spawnWorker(source, caps, capsGiven)
		if err != nil {
			return nil, err
		}
		// Registrar ANTES de arrancar la goroutine (A-07): así w.id/w.parent ya
		// están fijados cuando run→shutdown se retire del mapa, aunque el worker
		// termine de inmediato (p. ej. un error de carga o un módulo trivial).
		id := inst.pool.registerWorker(w)
		go w.run(source)
		return []any{id}, nil
	})

	// nu.worker._send(wid, msg) ⏸: encola msg en la cola hacia el worker; SUSPENDE
	// si está llena (backpressure). ECLOSED si el worker terminó.
	p.RegisterSuspending("worker._send", func(inst *Instance, args []any) ([]any, error) {
		w, known := inst.pool.lookupWorker(toI64(args[0]))
		if w == nil {
			if known {
				// worker retirado tras terminar (A-07): misma semántica que con la
				// entrada aún en el mapa y `done` cerrado.
				return nil, &StructuredError{Code: "ECLOSED", Message: "Worker:send: el worker ha terminado"}
			}
			return nil, &StructuredError{Code: "EINVAL", Message: "Worker:send: worker inválido"}
		}
		return sendOnChan(w.chans.toWorker, w.chans.done, args[1], "Worker:send")
	})

	// nu.worker._recv(wid) ⏸: saca un mensaje de la cola desde el worker; SUSPENDE
	// hasta que llegue. nil (fin de canal) si el worker terminó y no queda nada.
	p.RegisterSuspending("worker._recv", func(inst *Instance, args []any) ([]any, error) {
		w, known := inst.pool.lookupWorker(toI64(args[0]))
		if w == nil {
			if known {
				// worker retirado tras terminar (A-07): fin de canal, igual que con
				// la entrada aún en el mapa, `done` cerrado y la cola drenada.
				return []any{nil}, nil
			}
			return nil, &StructuredError{Code: "EINVAL", Message: "Worker:recv: worker inválido"}
		}
		res, err := recvOnChan(w.chans.fromWorker, w.chans.done)
		if err == nil && len(res) == 1 && res[0] == nil {
			// fin de canal: la cola quedó drenada — reapear al zombie que shutdown
			// dejó por tener mensajes pendientes (A-07). Idempotente si ya se borró.
			inst.pool.deregisterWorker(toI64(args[0]))
		}
		return res, err
	})

	// nu.worker._terminate(wid): inmediato y seguro (estados aislados). Interrumpe
	// un bucle de CPU (cancela el ctx) y despierta cualquier send/recv (cierra done).
	p.Register("worker._terminate", func(inst *Instance, args []any) ([]any, error) {
		if w, _ := inst.pool.lookupWorker(toI64(args[0])); w != nil {
			w.terminate()
		}
		return nil, nil
	})
}

// spawnWorker construye la Instance aislada de un worker, con su registro de
// primitivas filtrado por caps, y la arranca en su goroutine.
func (inst *Instance) spawnWorker(source string, caps map[string]bool, capsGiven bool) (*worker, error) {
	wp, err := newBarePool()
	if err != nil {
		return nil, err
	}
	wp.isWorker = true
	wp.registerHandleDispatch() // los handles funcionan dentro del worker
	// Las rutas de require del loader están disponibles en el worker (api.md §13):
	// comparte los módulos del padre.
	for name, src := range inst.pool.modules {
		wp.modules[name] = src
	}

	// Copia las primitivas CONCEDIDAS del registro del padre al del worker.
	parent := inst.pool.reg
	for id, name := range parent.names {
		if !workerGrants(name, caps, capsGiven) {
			continue
		}
		wp.reg.register(name, parent.fns[id], parent.suspending[id])
	}

	// Los MÉTODOS de handle también cruzan (G45): la superficie [W] incluye los
	// métodos de los handles que sus thunks producen (Proc:wait, Re:match,
	// GrepIter:next...), y registerHandleDispatch arranca el pool del worker con
	// el mapa vacío. Se copia entero, sin podar por caps: es inerte lo inalcanzable
	// (un método solo se despacha sobre un handle YA creado por un thunk concedido
	// de la PROPIA instancia — un worker sin `ui` jamás tendrá un handle Region).
	for key, fn := range parent.methods {
		wp.reg.methods[key] = fn
	}

	// Los wrappers Lua [W] del catálogo cruzan al worker (G45): buena parte de la
	// superficie de api.md §16 (nu.log.*, nu.re.compile, nu.proc.spawn, nu.text.*,
	// nu.ws.connect, nu.http.stream, nu.search.grep) no son thunks del registro
	// sino snippets de extraPreludio; sin esta copia el worker arranca con esos
	// módulos a nil. Cruzan SOLO los marcados worker-safe (nu.fs.watch no: su
	// entrega depende de nu.events, que en un worker no existe) y SOLO si caps
	// concede alguno de los thunks que envuelven (needs): así "lo no concedido no
	// existe" (§14) vale también para la capa de wrappers —un worker sin la cap
	// `http` no tiene nu.http ni como tabla—, con workerGrants como única
	// autoridad de poda.
	for _, s := range inst.pool.extraPreludio {
		if !s.workerSafe {
			continue
		}
		cruza := len(s.needs) == 0
		for _, need := range s.needs {
			if workerGrants(need, caps, capsGiven) {
				cruza = true
				break
			}
		}
		if cruza {
			wp.extraPreludio = append(wp.extraPreludio, s)
		}
	}

	chans := newWorkerChannels()

	// Primitivas del lado WORKER del canal con el padre (siempre presentes; el
	// canal no es una capacidad podable).
	wp.RegisterSuspending("worker.parent._send", func(winst *Instance, args []any) ([]any, error) {
		return sendOnChan(winst.workerChans.fromWorker, winst.workerChans.done, args[0], "parent.send")
	})
	wp.RegisterSuspending("worker.parent._recv", func(winst *Instance, args []any) ([]any, error) {
		return recvOnChan(winst.workerChans.toWorker, winst.workerChans.done)
	})

	winst, err := wp.NewInstance()
	if err != nil {
		return nil, &StructuredError{Code: "EIO", Message: "no se pudo crear el worker: " + err.Error()}
	}
	winst.workerChans = chans

	// La goroutine NO se arranca aquí: el llamador (worker._spawn) registra primero
	// el worker en el Pool (fija id/parent) y sólo después hace `go w.run(source)`,
	// para que shutdown() pueda retirarse siempre del registro (A-07).
	w := &worker{inst: winst, chans: chans, terminated: make(chan struct{})}
	return w, nil
}

// workerGrants decide si una primitiva `name` (p. ej. "fs.read") está concedida a
// un worker (G6). Nunca cruzan: nu.ui (un solo escritor de UI), nu.worker (no hay
// anidamiento) ni las primitivas internas de despacho de handles (las re-registra
// el propio Pool del worker). Sin caps → toda la API [W]. Con caps: concede el
// módulo entero ("fs") o la función exacta ("fs.read"), deny-by-default.
func workerGrants(name string, caps map[string]bool, capsGiven bool) bool {
	if hasPrefix(name, "ui.") || hasPrefix(name, "worker.") || hasPrefix(name, "loader.") ||
		hasPrefix(name, "plugin.") ||
		name == "__handle_call" || name == "__handle_call_s" || name == "__reset_budget" ||
		name == "__pending_gname" || name == "__pending_gval" ||
		name == "__pending_ename" || name == "__pending_epayload" {
		// ui/worker no cruzan; plugin.* es SOLO estado principal (api.md §13/§16: el
		// worker no tiene ciclo de vida de plugins) y, peor, sus HostFns cierran
		// sobre el runtime PRINCIPAL: un plugin.reload desde la goroutine del worker
		// re-entraría la VM principal en paralelo con su bucle. loader._source,
		// __reset_budget (watchdog, DM4) y __pending_gname/gval (SetGlobalString,
		// M13d) ya los registra el propio Pool del worker (registerLoader/
		// registerWatchdog/registerGlobals en newBarePool); las primitivas de
		// despacho de handles las re-registra registerHandleDispatch. Copiarlas aquí
		// sería una doble-registración (panic).
		return false
	}
	if !capsGiven {
		return true // sin caps: toda la API [W]
	}
	module := name
	for i := 0; i < len(name); i++ {
		if name[i] == '.' {
			module = name[:i]
			break
		}
	}
	return caps[name] || caps[module]
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// parseWorkerCaps extrae opts.caps (§13): un array de strings. Devuelve el set,
// capsGiven (distingue "sin caps" —toda la API— de "caps={}" —deny casi todo—) y
// un error EINVAL ante un uso malo.
func parseWorkerCaps(opts map[string]any) (map[string]bool, bool, error) {
	if opts == nil {
		return nil, false, nil // sin opts: toda la API [W]
	}
	capsV, ok := opts["caps"]
	if !ok {
		return nil, false, nil // opts sin caps: toda la API [W]
	}
	arr, ok := capsV.([]any)
	if !ok {
		return nil, false, &StructuredError{Code: "EINVAL", Message: "nu.worker.spawn: opts.caps debe ser un array de strings"}
	}
	caps := make(map[string]bool)
	for _, e := range arr {
		s, ok := e.(string)
		if !ok || s == "" {
			return nil, false, &StructuredError{Code: "EINVAL", Message: "nu.worker.spawn: opts.caps debe ser un array de nombres de capacidad (strings no vacíos)"}
		}
		caps[s] = true
	}
	return caps, true, nil // capsGiven aunque el array esté vacío (deny-by-default)
}

// run es la goroutine del worker: corre el módulo como una task (para que su
// top-level pueda llamar a ⏸ como parent.recv) y conduce su scheduler hasta que
// no queda nada vivo o lo interrumpe terminate.
func (w *worker) run(module string) {
	defer w.shutdown()
	// El argumento de nu.worker.spawn es un NOMBRE DE MÓDULO (api.md §13), no código
	// fuente: el worker lo resuelve con `require` (contra el registro de módulos que
	// heredó del padre), corriendo su top-level DENTRO de una task para que pueda usar
	// ⏸ (parent.recv/send). Paridad con el backend gopher (`return require(module)`).
	// El nombre se pasa por un global fijado sin interpolar (SetGlobalString), así un
	// nombre con caracteres raros no puede inyectar código.
	if err := w.inst.SetGlobalString("__worker_module", module); err != nil {
		return
	}
	// require(nombre) si es un módulo registrado (el caso de producción/contrato); si
	// NO existe (ENOENT), se trata el argumento como CÓDIGO FUENTE inline y se corre con
	// `load` —lo usan pruebas de bajo nivel que pasan el cuerpo del worker directamente—.
	// Cualquier otro error de require (p. ej. compilación del módulo) se propaga.
	boot := `nu.task.spawn(function()
  local name = __worker_module
  local ok, mod = pcall(require, name)
  if ok then return mod end
  if type(mod) == "table" and mod.code == "ENOENT" then
    local fn, err = load(name, "worker")
    if not fn then error(err) end
    return fn()
  end
  error(mod)
end)`
	if _, lerr, err := w.inst.Eval(boot); err != nil || lerr != "" {
		return // error de carga: aislado (ADR-008); el worker termina
	}
	_ = w.inst.RunTasks(w.inst.ctx) // se detiene solo (idle) o por terminate (ctx cancelado)
}

// shutdown cierra los canales y el estado del worker, se retira del registro del
// padre (A-07) y notifica el join. Lo corre la goroutine dueña del worker (la
// única que toca su estado Lua), sea cual sea la vía de fin —natural, terminate()
// o StopWorkers—. La retirada respeta la promesa de recvOnChan («un mensaje
// encolado justo antes de terminar aún se entrega»): si la cola hacia el padre
// está vacía, la entrada se borra aquí mismo; si quedan mensajes bufferizados,
// la entrada sobrevive como zombie y la reapea el _recv que encuentre el fin de
// canal (retirar aquí perdería la cola: el recv sobre un id retirado da nil
// inmediato). El chequeo va ANTES de cerrar `terminated`: cuando wait() (y con
// él StopWorkers) retorna, un worker sin mensajes pendientes ya no está en el
// registro.
func (w *worker) shutdown() {
	w.chans.closeDone()
	_ = w.inst.Close()
	if w.parent != nil {
		w.parent.reapIfDrained(w)
	}
	close(w.terminated)
}

// reapIfDrained borra la entrada del registro si el worker terminó sin mensajes
// pendientes hacia el padre; con mensajes bufferizados la deja (zombie) para que
// el consumidor los drene — el _recv que dé fin de canal hará la retirada. El
// len() es estable: cuando shutdown corre, la goroutine del worker ya salió de
// RunTasks y nadie más escribe en fromWorker.
func (p *Pool) reapIfDrained(w *worker) {
	p.workerMu.Lock()
	defer p.workerMu.Unlock()
	if len(w.chans.fromWorker) == 0 {
		delete(p.workers, w.id)
	}
}

// terminate detiene el worker: cierra done (despierta send/recv bloqueados) y
// cancela su ctx (interrumpe un bucle de CPU en vuelo y hace que RunTasks retorne).
// Idempotente.
func (w *worker) terminate() {
	w.termOnce.Do(func() {
		w.chans.closeDone()
		w.inst.cancel()
	})
}

// wait bloquea hasta que el worker ha terminado del todo (para el apagado limpio).
func (w *worker) wait() { <-w.terminated }

// sendOnChan implementa send con backpressure: bloquea hasta que hay hueco o hasta
// que el worker termina (ECLOSED). Corre en una goroutine de fondo del scheduler
// (contrato de RegisterSuspending), así que bloquear aquí no para el bucle.
func sendOnChan(out chan any, done chan struct{}, msg any, who string) ([]any, error) {
	if err := rejectNonJSONable(msg); err != nil {
		return nil, err
	}
	select {
	case out <- msg:
		return nil, nil
	case <-done:
		return nil, &StructuredError{Code: "ECLOSED", Message: who + ": el worker ha terminado"}
	}
}

// recvOnChan implementa recv: bloquea hasta un mensaje; si done se cierra y no
// queda nada, devuelve nil (fin de canal, NO error). Drena antes de cerrar: un
// mensaje encolado justo antes de terminar aún se entrega.
func recvOnChan(in chan any, done chan struct{}) ([]any, error) {
	select {
	case m := <-in:
		return []any{m}, nil
	case <-done:
		select {
		case m := <-in:
			return []any{m}, nil
		default:
			return []any{nil}, nil // fin de canal
		}
	}
}

// rejectNonJSONable es la defensa Go: un handle (userdata/Block) no puede cruzar a
// un worker (§13). Las funciones/threads ni siquiera llegan aquí (el codec Lua las
// rechaza al serializar); el chequeo fino y accionable se hace en Lua antes de
// ceder (__check_msg). Aquí sólo se blinda el handle.
func rejectNonJSONable(v any) error {
	switch x := v.(type) {
	case Handle:
		return &StructuredError{Code: "EINVAL", Message: "worker: un handle (userdata/Block) no es serializable a un worker"}
	case []any:
		for _, e := range x {
			if err := rejectNonJSONable(e); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, e := range x {
			if err := rejectNonJSONable(e); err != nil {
				return err
			}
		}
	}
	return nil
}

// StopWorkers termina y espera a todos los workers vivos del Pool (apagado
// ordenado, sin goroutines colgadas). Lo llama el Runtime en Close (M13).
func (p *Pool) StopWorkers() {
	p.workerMu.Lock()
	ws := make([]*worker, 0, len(p.workers))
	for _, w := range p.workers {
		ws = append(ws, w)
	}
	p.workerMu.Unlock()
	for _, w := range ws {
		w.terminate()
	}
	for _, w := range ws {
		w.wait()
	}
}

func toI64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}
