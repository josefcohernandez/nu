package runtime

// Catálogo de nu.fs sobre el backend wasm (M13b, §5). Contraparte de fs.go: las
// mismas primitivas (read/write/append/stat/list/mkdir/remove/rename/copy/tmpdir/
// cwd) reusando las MISMAS funciones Go VM-agnósticas (writeAtomic/writeExclusive/
// copyFile/fsState.ensureTmpdir) y las mismas constantes de permisos. Todas ⏸
// salvo cwd (consulta pura). El HostFn suspendente corre en una goroutine de fondo
// (contrato de RegisterSuspending): no toca la Instance, pero sí el `os` y el
// `rt.fs` (mutex-safe) — igual que la deliverFn del backend gopher hace el IO fuera
// del token.
//
// La guardia requireTask del backend gopher aquí la impone el propio mecanismo: un
// thunk ⏸ hace coroutine.yield, que fuera de una task (coroutine) ya falla.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	ignore "github.com/sabhiram/go-gitignore"

	"github.com/dbareagimeno/nu/internal/vmwasm"
)

// mapFsErrorWasm traduce un error del SO al error estructurado del core (§1.4),
// mismo mapeo que mapFsError del backend gopher.
func mapFsErrorWasm(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &vmwasm.StructuredError{Code: "ENOENT", Message: err.Error()}
	case errors.Is(err, os.ErrExist):
		return &vmwasm.StructuredError{Code: "EEXIST", Message: err.Error()}
	case errors.Is(err, os.ErrPermission):
		return &vmwasm.StructuredError{Code: "EACCES", Message: err.Error()}
	default:
		return &vmwasm.StructuredError{Code: "EIO", Message: err.Error()}
	}
}

func registerFsWasm(p *vmwasm.Pool, rt *Runtime) {
	// nu.fs.read(path) -> string ⏸
	p.RegisterSuspending("fs.read", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		path, _ := args[0].(string)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}
		return []any{string(data)}, nil
	})

	// nu.fs.write(path, data, opts?) ⏸ — escritura atómica; opts.exclusive = O_EXCL.
	p.RegisterSuspending("fs.write", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		path, _ := args[0].(string)
		data := []byte(argString(args, 1))
		exclusive := false
		if opts, ok := arg(args, 2).(map[string]any); ok {
			exclusive, _ = opts["exclusive"].(bool)
		}
		var err error
		if exclusive {
			err = writeExclusive(path, data)
		} else {
			err = writeAtomic(path, data)
		}
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}
		return nil, nil
	})

	// nu.fs.append(path, data) ⏸
	p.RegisterSuspending("fs.append", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		path, _ := args[0].(string)
		data := []byte(argString(args, 1))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fsFilePerm)
		if err == nil {
			_, err = f.Write(data)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}
		return nil, nil
	})

	// nu.fs.stat(path) -> {size, mtime_ms, is_dir, mode}? ⏸ — inexistente → nil.
	p.RegisterSuspending("fs.stat", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		path, _ := args[0].(string)
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return []any{nil}, nil // inexistente → nil, NO lanza (§5)
			}
			return nil, mapFsErrorWasm(err)
		}
		return []any{map[string]any{
			"size":     info.Size(),
			"mtime_ms": info.ModTime().UnixMilli(),
			"is_dir":   info.IsDir(),
			"mode":     int64(info.Mode().Perm()),
		}}, nil
	})

	// nu.fs.list(dir) -> {name, is_dir}[] ⏸ — inexistente → ENOENT.
	p.RegisterSuspending("fs.list", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		dir, _ := args[0].(string)
		des, err := os.ReadDir(dir)
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}
		arr := make([]any, len(des))
		for i, de := range des {
			arr[i] = map[string]any{"name": de.Name(), "is_dir": de.IsDir()}
		}
		return []any{arr}, nil
	})

	// nu.fs.mkdir(path) ⏸ — MkdirAll (mkdir -p), idempotente.
	p.RegisterSuspending("fs.mkdir", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		path, _ := args[0].(string)
		if err := os.MkdirAll(path, fsDirPerm); err != nil {
			return nil, mapFsErrorWasm(err)
		}
		return nil, nil
	})

	// nu.fs.remove(path, opts?) ⏸ — inexistente → no-op; dir no vacío exige recursive.
	p.RegisterSuspending("fs.remove", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		path, _ := args[0].(string)
		recursive := false
		if opts, ok := arg(args, 1).(map[string]any); ok {
			recursive, _ = opts["recursive"].(bool)
		}
		var err error
		if recursive {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
			if errors.Is(err, os.ErrNotExist) {
				err = nil // idempotente
			}
		}
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}
		return nil, nil
	})

	// nu.fs.rename(from, to) ⏸
	p.RegisterSuspending("fs.rename", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		if err := os.Rename(argString(args, 0), argString(args, 1)); err != nil {
			return nil, mapFsErrorWasm(err)
		}
		return nil, nil
	})

	// nu.fs.copy(from, to) ⏸ — copia en streaming.
	p.RegisterSuspending("fs.copy", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		if err := copyFile(argString(args, 0), argString(args, 1)); err != nil {
			return nil, mapFsErrorWasm(err)
		}
		return nil, nil
	})

	// nu.fs.tmpdir() -> string ⏸ — el scratch de la sesión (compartido, rt.fs).
	p.RegisterSuspending("fs.tmpdir", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		dir, err := rt.fs.ensureTmpdir()
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}
		return []any{dir}, nil
	})

	// nu.fs.cwd() -> string — la ÚNICA de fs que NO es ⏸ (consulta pura).
	p.Register("fs.cwd", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		dir, err := os.Getwd()
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}
		return []any{dir}, nil
	})

	// nu.fs.watch (§5, §16, S15): observador del FS con lotes+debounce. Se registra
	// aparte para no ensuciar el bloque de primitivas ⏸ (paridad con registerWatch,
	// que registerFs llama para mantener la superficie de nu.fs junta).
	registerWatchWasm(p, rt)
}

// arg / argString: acceso seguro a args de un HostFn.
func arg(args []any, i int) any {
	if i >= len(args) {
		return nil
	}
	return args[i]
}

func argString(args []any, i int) string {
	s, _ := arg(args, i).(string)
	return s
}

// --- nu.fs.watch sobre el backend wasm (S15, §5, §16, inventario 🔒 G7) ---
//
// Contraparte de watch.go. El RETO es que en gopher la goroutine de fondo del
// watcher ENTREGA el lote llamando directamente a la LFunction bajo el token
// (deliverBatch). En wasm una función Lua no cruza la frontera (C1): no puede
// guardarse Go-side ni invocarse desde una goroutine. La entrega, sin embargo,
// sigue siendo PUSH desde Go —igual que gopher—: la goroutine de run() vuelca el
// lote por `inst.EmitEvent`, que toma el mutex de la Instance (el "token" de esta
// casa) y corre `nu.events.emit` en el estado principal. El wrapper Lua (AddPreludio)
// suscribe un handler por evento de nombre único (derivado del id del handle) que
// desenvuelve el lote y llama al `fn` del usuario. Así NO hace falta una task Lua
// viva bombeando `_recv` —que, al ser de primer plano y suspendida, colgaría la
// quiescencia de RunTasks (liveFg nunca bajaría a 0), y al marcarla de fondo se
// quedaría huérfana al retornar el primer RunTasks—: la entrega es independiente del
// scheduler, como el deliverBatch de gopher.
//
// Toda la LÓGICA NUESTRA a blindar (G7) es idéntica a watch.go: batching+debounce
// trailing, filtrado gitignore/.git, y recursión reconstruida al ver dirs nuevos.
// Reusa los mismos `watchEvent`/`watchKind*` del paquete; sólo cambia el destino de
// la entrega (EmitEvent en vez de deliverBatch) y que no hay LFunction guardada.

// wasmWatcher es el objeto Go detrás del handle `Watcher` en wasm. Su goroutine
// corre el bucle fsnotify+debounce; `stopCh` la corta (idempotente vía stopOnce).
type wasmWatcher struct {
	fsw       *fsnotify.Watcher
	recursive bool
	debounce  time.Duration
	gi        *ignore.GitIgnore

	// evname es el nombre único del evento por el que este watcher entrega sus
	// lotes al bus del estado principal (derivado del id del handle: el wrapper Lua
	// suscribe el mismo nombre). Interno del kernel → namespace `core:` (§4).
	evname string

	stopCh   chan struct{}
	stopOnce sync.Once

	// ownerName/sched: ciclo de vida más allá del `stop` explícito. El watcher se
	// etiqueta con el dueño vigente al crearse (S13): `nu.plugin.reload` corta los
	// watchers de ESE plugin (vía release, como a sus procesos), y `Runtime.Close`
	// corta todos los vivos (stopAllWatchers) — sin esto, goroutine y fd de
	// fsnotify sobrevivían a ambos. `sched` es nil en los tests mínimos.
	ownerName string
	sched     *scheduler
}

// wasmWatcher implementa ownedHandle (S13): un reload del plugin dueño lo para
// igual que mata a sus procesos. release NO toca el registro por dueño (eso lo
// orquesta releaseOwnerHandles); stop sí se desregistra del mapa de vivos.
func (w *wasmWatcher) release()      { w.stop() }
func (w *wasmWatcher) owner() string { return w.ownerName }

// registerWatchWasm cuelga `nu.fs._watch` (la primitiva síncrona que arma el
// watcher y devuelve el handle) y el método `Watcher:stop`, e instala el wrapper
// `nu.fs.watch` (AddPreludio) que envuelve el handle con la suscripción de entrega.
func registerWatchWasm(p *vmwasm.Pool, rt *Runtime) {
	// nu.fs._watch(path, opts?) -> Watcher. SÍNCRONA (no ⏸): arma el observador y
	// devuelve el handle en el acto, como fsWatch en gopher. Un path inexistente →
	// ENOENT (fsnotify no vigila lo que no existe); debounce_ms negativo → EINVAL.
	p.Register("fs._watch", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		path := argString(args, 0)

		// Defaults de §5: recursive=false, gitignore=true, debounce_ms=50.
		recursive := false
		gitignore := true
		debounceMs := 50.0
		if opts, ok := arg(args, 1).(map[string]any); ok {
			if v, ok := opts["recursive"].(bool); ok {
				recursive = v
			}
			// gitignore default true: sólo un false explícito lo desactiva.
			if v, present := opts["gitignore"]; present && v != nil {
				if b, ok := v.(bool); ok {
					gitignore = b
				}
			}
			if v, ok := httpNum(opts["debounce_ms"]); ok {
				debounceMs = v
			}
		}
		if debounceMs < 0 {
			return nil, &vmwasm.StructuredError{Code: CodeEINVAL, Message: "nu.fs.watch: debounce_ms no puede ser negativo"}
		}

		// El path debe existir para observarlo (§5): inexistente → ENOENT por el mapeo.
		root, err := filepath.Abs(path)
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}

		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, mapFsErrorWasm(err)
		}

		// Carga el .gitignore de la raíz observada (o del dir del fichero, si `path` es
		// un fichero): sus patrones son relativos a él. Ausente → no ignora nada.
		var gi *ignore.GitIgnore
		ignoreRoot := root
		if !info.IsDir() {
			ignoreRoot = filepath.Dir(root)
		}
		if gitignore {
			if g, gerr := ignore.CompileIgnoreFile(filepath.Join(ignoreRoot, ".gitignore")); gerr == nil {
				gi = g
			}
		}

		w := &wasmWatcher{
			fsw:       fsw,
			recursive: recursive,
			debounce:  time.Duration(debounceMs) * time.Millisecond,
			gi:        gi,
			stopCh:    make(chan struct{}),
			ownerName: rt.currentOwner(),
			sched:     rt.sched,
		}
		if err := w.addTree(root, info.IsDir()); err != nil {
			_ = fsw.Close()
			return nil, mapFsErrorWasm(err)
		}

		h := inst.AllocHandle("Watcher", w)
		w.evname = fmt.Sprintf("core:__fs_watch.%d", uint32(h))
		if rt.sched != nil {
			rt.sched.trackWatcher(w) // para Runtime.Close (stopAllWatchers)
			rt.sched.track(w)        // para nu.plugin.reload (registro por dueño, G2)
		}
		go w.run(inst)
		return []any{h}, nil
	})

	// Watcher:stop() — corta el watcher (goroutine + watcher del SO), idempotente.
	p.RegisterHandleMethod("Watcher", "stop", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		val.(*wasmWatcher).stop()
		return nil, nil
	})

	// Wrapper Lua: nu.fs.watch(path, opts?, fn) -> Watcher. Acepta la forma ergonómica
	// watch(path, fn). Suscribe un handler al evento de entrega del watcher (nombre
	// único derivado del id del handle) que desenvuelve el lote y llama a `fn(events)`;
	// Watcher:stop cancela la suscripción y corta la entrega Go. El handler corre bajo
	// el pcall del bus (ADR-008), igual que en gopher.
	p.AddPreludio(`
nu.fs = nu.fs or {}
function nu.fs.watch(path, opts, fn)
  if fn == nil and type(opts) == "function" then
    fn = opts
    opts = nil
  end
  if type(fn) ~= "function" then
    error({ code = "EINVAL", message = "nu.fs.watch: se requiere una funcion handler" })
  end
  local w = nu.fs._watch(path, opts)                 -- handle {__id}; ENOENT/EINVAL si procede
  local sub = nu.events.on("core:__fs_watch." .. w.__id, function(p) fn(p.events) end)
  w.stop = function(self)
    sub:cancel()
    return __hcall(self.__id, "stop")
  end
  return w
end`)
}

// run es el bucle de fondo del watcher wasm: recibe eventos de fsnotify, los filtra
// (gitignore/.git), los acumula y los entrega EN LOTES tras `debounce` de calma (G7).
// Idéntico a watch.go salvo el destino de la entrega: aquí `deliver` toma el mutex de
// la Instance (EmitEvent) en vez del token de gopher. Jamás toca Lua fuera de deliver.
func (w *wasmWatcher) run(inst *vmwasm.Instance) {
	var (
		buf    []watchEvent
		timer  *time.Timer
		fireCh <-chan time.Time
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-w.stopCh:
			return

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			we, keep := w.classify(ev)
			if !keep {
				continue
			}
			// Dir recién creado bajo un watch recursivo: empieza a vigilarlo (alcance
			// documentado de recursive), como en watch.go.
			if w.recursive && we.kind == watchKindCreate {
				if fi, serr := os.Stat(we.path); serr == nil && fi.IsDir() && !w.isIgnoredDir(we.path) {
					_ = w.fsw.Add(we.path)
				}
			}
			buf = append(buf, we)
			// (Re)arma el debounce trailing: el lote sale tras `debounce` de calma.
			if timer == nil {
				timer = time.NewTimer(w.debounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(w.debounce)
			}
			fireCh = timer.C

		case <-fireCh:
			batch := buf
			buf = nil
			fireCh = nil
			w.deliver(inst, batch)

		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// Un error del backend (p. ej. overflow de la cola del SO) no tumba el
			// watcher ni el proceso (best-effort, ADR-008); se drena y se descarta.
		}
	}
}

// deliver vuelca un lote al estado principal por el bus de eventos. EmitEvent toma
// el mutex de la Instance (serializado con schedStep) y corre `nu.events.emit` en el
// estado principal: es el análogo del deliverBatch de gopher (que toma el token).
// Atiende a stopCh antes de emitir: parado entre acumular y entregar → no entrega.
func (w *wasmWatcher) deliver(inst *vmwasm.Instance, batch []watchEvent) {
	if len(batch) == 0 {
		return
	}
	select {
	case <-w.stopCh:
		return
	default:
	}
	events := make([]any, len(batch))
	for i, e := range batch {
		events[i] = map[string]any{"path": e.path, "kind": e.kind}
	}
	// Error de EmitEvent (trap del motor, imposible en la práctica) → best-effort.
	_ = inst.EmitEvent(w.evname, map[string]any{"events": events})
}

// stop corta el watcher: cierra stopCh (la goroutine de run retorna) y el watcher del
// SO (sus goroutines mueren), y se desregistra del mapa de vivos del scheduler.
// Idempotente (stopOnce): parar dos veces es inocuo.
func (w *wasmWatcher) stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		_ = w.fsw.Close()
		if w.sched != nil {
			w.sched.untrackWatcher(w)
		}
	})
}

// addTree añade `root` al watcher del SO; recursivo y dir, camina el subárbol
// añadiendo cada subdir no ignorado. Gemelo de (*luaWatcher).addTree de watch.go.
func (w *wasmWatcher) addTree(root string, isDir bool) error {
	if !isDir {
		// Un fichero suelto: se vigila su directorio y `classify` filtra lo ajeno.
		return w.fsw.Add(filepath.Dir(root))
	}
	if err := w.fsw.Add(root); err != nil {
		return err
	}
	if !w.recursive {
		return nil
	}
	return filepath.WalkDir(root, func(pth string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // entrada ilegible: se salta, no rompe el watch
		}
		if !d.IsDir() || pth == root {
			return nil
		}
		if w.isIgnoredDir(pth) {
			return filepath.SkipDir // no vigiles el subárbol ignorado (node_modules/...)
		}
		_ = w.fsw.Add(pth) // best-effort
		return nil
	})
}

// isIgnoredDir: el .git/ interno (ruido universal) o lo que .gitignore ignora.
func (w *wasmWatcher) isIgnoredDir(pth string) bool {
	if filepath.Base(pth) == ".git" {
		return true
	}
	return w.matchesIgnore(pth)
}

// matchesIgnore consulta el .gitignore cargado (sin matcher, nada se ignora).
func (w *wasmWatcher) matchesIgnore(pth string) bool {
	if w.gi == nil {
		return false
	}
	return w.gi.MatchesPath(pth)
}

// classify filtra y traduce un evento fsnotify a un watchEvent del contrato (§5).
// keep=false descarta (gitignore/.git). Gemelo de (*luaWatcher).classify.
func (w *wasmWatcher) classify(ev fsnotify.Event) (watchEvent, bool) {
	if w.pathIgnored(ev.Name) {
		return watchEvent{}, false
	}
	var kind string
	switch {
	case ev.Op&fsnotify.Create != 0:
		kind = watchKindCreate
	case ev.Op&fsnotify.Remove != 0, ev.Op&fsnotify.Rename != 0:
		kind = watchKindRemove
	case ev.Op&fsnotify.Write != 0, ev.Op&fsnotify.Chmod != 0:
		kind = watchKindModify
	default:
		return watchEvent{}, false
	}
	return watchEvent{path: ev.Name, kind: kind}, true
}

// pathIgnored: bajo un .git/ o ignorado por .gitignore (G7). Gemelo de watch.go.
func (w *wasmWatcher) pathIgnored(pth string) bool {
	for cur := pth; ; {
		if filepath.Base(cur) == ".git" {
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return w.matchesIgnore(pth)
}
