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
// y su join de apagado.
type worker struct {
	inst       *Instance
	chans      *workerChannels
	terminated chan struct{}
	termOnce   sync.Once
}

// registerWorker registra un worker en el Pool y devuelve su id (el __wid del
// lado Lua). Sólo el Pool principal lleva este registro.
func (p *Pool) registerWorker(w *worker) int64 {
	p.workerMu.Lock()
	defer p.workerMu.Unlock()
	p.workerNext++
	id := p.workerNext
	p.workers[id] = w
	return id
}

func (p *Pool) lookupWorker(id int64) *worker {
	p.workerMu.Lock()
	defer p.workerMu.Unlock()
	return p.workers[id]
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
		id := inst.pool.registerWorker(w)
		return []any{id}, nil
	})

	// nu.worker._send(wid, msg) ⏸: encola msg en la cola hacia el worker; SUSPENDE
	// si está llena (backpressure). ECLOSED si el worker terminó.
	p.RegisterSuspending("worker._send", func(inst *Instance, args []any) ([]any, error) {
		w := inst.pool.lookupWorker(toI64(args[0]))
		if w == nil {
			return nil, &StructuredError{Code: "EINVAL", Message: "Worker:send: worker inválido"}
		}
		return sendOnChan(w.chans.toWorker, w.chans.done, args[1], "Worker:send")
	})

	// nu.worker._recv(wid) ⏸: saca un mensaje de la cola desde el worker; SUSPENDE
	// hasta que llegue. nil (fin de canal) si el worker terminó y no queda nada.
	p.RegisterSuspending("worker._recv", func(inst *Instance, args []any) ([]any, error) {
		w := inst.pool.lookupWorker(toI64(args[0]))
		if w == nil {
			return nil, &StructuredError{Code: "EINVAL", Message: "Worker:recv: worker inválido"}
		}
		return recvOnChan(w.chans.fromWorker, w.chans.done)
	})

	// nu.worker._terminate(wid): inmediato y seguro (estados aislados). Interrumpe
	// un bucle de CPU (cancela el ctx) y despierta cualquier send/recv (cierra done).
	p.Register("worker._terminate", func(inst *Instance, args []any) ([]any, error) {
		if w := inst.pool.lookupWorker(toI64(args[0])); w != nil {
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

	// Copia las primitivas CONCEDIDAS del registro del padre al del worker.
	parent := inst.pool.reg
	for id, name := range parent.names {
		if !workerGrants(name, caps, capsGiven) {
			continue
		}
		wp.reg.register(name, parent.fns[id], parent.suspending[id])
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

	w := &worker{inst: winst, chans: chans, terminated: make(chan struct{})}
	go w.run(source)
	return w, nil
}

// workerGrants decide si una primitiva `name` (p. ej. "fs.read") está concedida a
// un worker (G6). Nunca cruzan: nu.ui (un solo escritor de UI), nu.worker (no hay
// anidamiento) ni las primitivas internas de despacho de handles (las re-registra
// el propio Pool del worker). Sin caps → toda la API [W]. Con caps: concede el
// módulo entero ("fs") o la función exacta ("fs.read"), deny-by-default.
func workerGrants(name string, caps map[string]bool, capsGiven bool) bool {
	if hasPrefix(name, "ui.") || hasPrefix(name, "worker.") ||
		name == "__handle_call" || name == "__handle_call_s" {
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
func (w *worker) run(source string) {
	defer w.shutdown()
	boot := "nu.task.spawn(function()\n" + source + "\nend)"
	if _, lerr, err := w.inst.Eval(boot); err != nil || lerr != "" {
		return // error de carga: aislado (ADR-008); el worker termina
	}
	_ = w.inst.RunTasks(w.inst.ctx) // se detiene solo (idle) o por terminate (ctx cancelado)
}

// shutdown cierra los canales y el estado del worker, y notifica el join. Lo corre
// la goroutine dueña del worker (la única que toca su estado Lua).
func (w *worker) shutdown() {
	w.chans.closeDone()
	_ = w.inst.Close()
	close(w.terminated)
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
