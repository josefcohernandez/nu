package runtime

import (
	"sync"
	"syscall"
	"time"
)

// El scheduler de tasks del kernel corre dentro del backend wasm (ADR-020,
// realizado en internal/vmwasm: task = corrutina Lua nativa, ⏸ = yield). Del lado
// de internal/runtime sobrevive esta pieza fina que el camino wasm reutiliza:
//
//   - el **token** de ejecución (`gil`, un canal de capacidad 1): serializa el
//     pintado del compositor (armPainter/paintLocked, ui.go) y el driver de TTY
//     con el código Go que lee el compositor bajo el token. En wasm la VM la
//     gobierna el mutex de la Instance (`rt.wasm.WithLock`); el token sigue
//     serializando el pintor con los tests/driver que leen el compositor.
//   - el **presupuesto por slice** del watchdog (`budget`), que `buildWasmState`
//     pasa al Pool wasm (`SetSliceBudget`, DM4).
//   - el **rastreo de recursos de fondo con vida propia** (subprocesos, streams
//     HTTP, websockets, iteradores de grep): las primitivas nu.proc/http.stream/
//     ws/search construyen su objeto Go VM-agnóstico (proc.go/stream.go/ws.go/
//     search.go, reusado por los HostFn de vmwasm) y lo registran aquí para que
//     `Runtime.Close` los corte a todos (red de seguridad tras el cleanup/GC).
//   - el **registro de handles por dueño** (`ownerHandles`, handles.go): los
//     recursos persistentes se etiquetan con su dueño para el reload (G2).
//
// (Antes de M17 aquí vivía además el scheduler de gopher-lua —una goroutine por
// task + este token como GIL—; ADR-020 lo reemplazó por el scheduler por
// corrutinas de wasm y M17 retiró el código gopher.)
type scheduler struct {
	rt *Runtime

	gil chan struct{} // token de ejecución (cap 1); el que lo tiene, pinta/lee el compositor

	// budget es el **presupuesto por slice** del watchdog (api.md §1.3): el tiempo
	// máximo que una task puede correr Lua de forma continua sin suspender. Por
	// defecto 100 ms; configurable vía `WithSliceBudget`. Inmutable tras construir el
	// runtime; `buildWasmState` lo traslada al Pool wasm (DM4).
	budget time.Duration

	mu sync.Mutex // protege los mapas de recursos de fondo y `ownerHandles`

	// procs/streams/ws/greps son los recursos de fondo vivos de nu.proc.spawn (S16),
	// nu.http.stream (S20), nu.ws.connect (S21) y nu.search.grep (S27). Se rastrean
	// para cortarlos TODOS en `Runtime.Close` (stopAll*): ninguna goroutine de fondo
	// ni descriptor debe sobrevivir al proceso (red de seguridad, tras el cleanup de
	// quien los creó). Los alimentan las primitivas (trackProc/trackStream/...) y los
	// soltados a mano (untrackStream/untrackWs/untrackGrep).
	procs    map[*luaProc]struct{}
	streams  map[*httpStream]struct{}
	ws       map[*luaWs]struct{}
	greps    map[*grepIter]struct{}
	watchers map[*wasmWatcher]struct{}

	// ownerHandles es el registro de handles por dueño (S13, handles.go): asocia cada
	// plugin (por nombre de owner) a los handles persistentes que registró, para que
	// un reload los suelte sin dejar huérfanos (G2). Lo alimentan `track`/`untrack`.
	ownerHandles map[string][]ownedHandle
}

// newScheduler prepara el scheduler con el token libre (sembrado en el canal), el
// presupuesto de slice del watchdog y los mapas de recursos de fondo vacíos.
func newScheduler(rt *Runtime, budget time.Duration) *scheduler {
	s := &scheduler{
		rt:       rt,
		gil:      make(chan struct{}, 1),
		budget:   budget,
		procs:    make(map[*luaProc]struct{}),
		streams:  make(map[*httpStream]struct{}),
		ws:       make(map[*luaWs]struct{}),
		greps:    make(map[*grepIter]struct{}),
		watchers: make(map[*wasmWatcher]struct{}),
	}
	s.gil <- struct{}{} // token disponible: el primero que lo pida pinta/lee el compositor
	return s
}

// acquire toma el token (bloquea hasta que esté libre). Tras volver, esta goroutine
// es la única autorizada a leer/mutar el compositor bajo el token. release lo devuelve.
func (s *scheduler) acquire() { <-s.gil }
func (s *scheduler) release() { s.gil <- struct{}{} }

// trackProc registra un subproceso vivo de `nu.proc.spawn` (S16) para poder matarlo
// al cerrar el runtime.
func (s *scheduler) trackProc(p *luaProc) {
	s.mu.Lock()
	s.procs[p] = struct{}{}
	s.mu.Unlock()
}

// untrackProc deja de rastrear un subproceso ya recogido (reap temprano: salió y
// sus dos streams se agotaron y cerraron, proc.go maybeReap). Idempotente.
func (s *scheduler) untrackProc(p *luaProc) {
	s.mu.Lock()
	delete(s.procs, p)
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
// el runtime.
func (s *scheduler) trackStream(st *httpStream) {
	s.mu.Lock()
	s.streams[st] = struct{}{}
	s.mu.Unlock()
}

// untrackStream deja de rastrear un stream cerrado (`Stream:close`). Idempotente.
func (s *scheduler) untrackStream(st *httpStream) {
	s.mu.Lock()
	delete(s.streams, st)
	s.mu.Unlock()
}

// stopAllStreams cierra todos los streams vivos. Lo llama `Runtime.Close`: ninguna
// conexión ni goroutine de lectura de body debe sobrevivir al proceso de `nu`.
// `httpStream.close` es idempotente (`closeOnce`). Se copia el conjunto antes de
// iterar porque `close` llama a `untrackStream`, que toca el mapa bajo el mismo candado.
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
// runtime.
func (s *scheduler) trackWs(w *luaWs) {
	s.mu.Lock()
	s.ws[w] = struct{}{}
	s.mu.Unlock()
}

// untrackWs deja de rastrear un websocket cerrado (`Ws:close`). Idempotente.
func (s *scheduler) untrackWs(w *luaWs) {
	s.mu.Lock()
	delete(s.ws, w)
	s.mu.Unlock()
}

// stopAllWs cierra todos los websockets vivos. Lo llama `Runtime.Close`: ninguna
// conexión ni goroutine de IO debe sobrevivir al proceso de `nu`. `luaWs.close` es
// idempotente (`closeOnce`). Se copia el conjunto antes de iterar porque `close` llama
// a `untrackWs`, que toca el mapa bajo el mismo candado.
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

// trackWatcher registra un `nu.fs.watch` vivo (S15) para poder cortarlo al cerrar
// el runtime: su goroutine de fondo y el watcher del SO (fd de inotify/kqueue) no
// deben sobrevivir al proceso.
func (s *scheduler) trackWatcher(w *wasmWatcher) {
	s.mu.Lock()
	s.watchers[w] = struct{}{}
	s.mu.Unlock()
}

// untrackWatcher deja de rastrear un watcher parado (`Watcher:stop`). Idempotente.
func (s *scheduler) untrackWatcher(w *wasmWatcher) {
	s.mu.Lock()
	delete(s.watchers, w)
	s.mu.Unlock()
}

// stopAllWatchers corta todos los watchers vivos. Lo llama `Runtime.Close`.
// `wasmWatcher.stop` es idempotente (`stopOnce`). Se copia el conjunto antes de
// iterar porque `stop` llama a `untrackWatcher`, que toca el mapa bajo el mismo
// candado.
func (s *scheduler) stopAllWatchers() {
	s.mu.Lock()
	ws := make([]*wasmWatcher, 0, len(s.watchers))
	for w := range s.watchers {
		ws = append(ws, w)
	}
	s.watchers = make(map[*wasmWatcher]struct{})
	s.mu.Unlock()
	for _, w := range ws {
		w.stop()
	}
}

// trackGrep registra un iterador de `nu.search.grep` vivo (S27) para poder
// cancelarlo al cerrar el runtime.
func (s *scheduler) trackGrep(it *grepIter) {
	s.mu.Lock()
	s.greps[it] = struct{}{}
	s.mu.Unlock()
}

// untrackGrep deja de rastrear un grep cerrado (`grepIter.close`). Idempotente.
func (s *scheduler) untrackGrep(it *grepIter) {
	s.mu.Lock()
	delete(s.greps, it)
	s.mu.Unlock()
}

// stopAllGreps cancela todos los greps vivos. Lo llama `Runtime.Close`: ningún pool
// de goroutines de fondo debe sobrevivir al proceso de `nu`. `grepIter.close` es
// idempotente (`closeOnce`). Se copia el conjunto antes de iterar porque `close` llama
// a `untrackGrep`, que toca el mapa bajo el mismo candado.
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
