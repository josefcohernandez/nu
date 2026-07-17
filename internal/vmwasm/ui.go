package vmwasm

// Binding de UI de la frontera VM (migracion-vm.md M11, categorías C2/C5 del
// censo). api.md §9-§10: el compositor, el diffing y el pintado viven en Go
// (ADR-007/008) y son **VM-agnósticos** (el censo M01 lo confirma: compositor.go
// y bare_screen.go no tocan ningún tipo de la VM). Lo que esta sesión construye
// es el BINDING wasm: exponer esas operaciones Go como primitivas host, envolver
// Region/Block como handles opacos (C5, el mecanismo de M10) y manejar la pila de
// input —cuyos handlers son funciones Lua— como un preludio (preludioInput), el
// mismo patrón que el bus de eventos (M08).
//
// Decisión anotada (M11): en el backend gopher los callbacks de input se
// guardan Go-side como `*lua.LFunction` (input.go). En wasm eso es imposible: Go
// no sostiene referencias a funciones del estado Lua a través de la frontera. Por
// eso la **pila de input y la resolución de secuencias viven en el preludio Lua**
// (como enu.events), con Go inyectando eventos crudos. Es coherente con "la
// resolución de secuencias es del core" (api.md §9.3): el preludio se compila en
// el binario, ES el core. El compositor real (compositor.go, reusable tal cual)
// se enchufa a la interfaz `UIBackend` en la integración con el Runtime (M13);
// aquí un backend de grabación prueba el mecanismo, igual que M09 probó el
// mecanismo de primitivas y M10 el de handles antes de cablear el catálogo real.

import (
	"fmt"
)

// UIBackend abstrae el compositor para el binding wasm. El compositor real
// (compositor.go, VM-agnóstico) lo implementa vía un adaptador en M13; los tests
// usan un backend de grabación. Region y Block cruzan la frontera como handles
// (C5): sus objetos Go reales (RegionObj/BlockObj) viven en la tabla de handles
// de la Instance y sus métodos se despachan por el mecanismo de M10.
type UIBackend interface {
	// Size devuelve el tamaño del terminal en celdas (enu.ui.size).
	Size() (w, h int)
	// Caps devuelve las capacidades del terminal (enu.ui.caps).
	Caps() map[string]any
	// NewBlock construye un bloque pre-renderizado a partir de las líneas
	// (enu.ui.block). Un error es de validación (líneas mal formadas).
	NewBlock(lines []any) (BlockObj, error)
	// NewRegion crea una región de composición con z-order (enu.ui.region).
	NewRegion(x, y, w, h, z int) RegionObj
	// Clipboard vía OSC 52 (enu.ui.clipboard_*). Get es la única op ⏸ del módulo.
	ClipboardSet(s string)
	ClipboardGet() (string, bool)
}

// BlockObj es un bloque opaco: sólo expone sus dimensiones (api.md §9.2: un Block
// tiene .width y .height y ningún método — es copia inmutable de líneas).
type BlockObj interface {
	Dims() (w, h int)
}

// RegionObj es una región opaca con los métodos de api.md §9.1. Todos síncronos
// (el pintado se coalesce en Go; no hay flush manual). Blit recibe el BlockObj ya
// resuelto (el binding traduce el handle del Block a su objeto).
type RegionObj interface {
	Blit(x, y int, b BlockObj)
	Fill(style map[string]any)
	Clear()
	Move(x, y int)
	Resize(w, h int)
	Raise()
	Lower()
	Show()
	Hide()
	Destroy()
	Cursor(x, y int, show bool)
}

// SetUIBackend instala el backend de UI del Pool. Debe llamarse antes de
// NewInstance (el preludio se arma con el catálogo completo, y la presencia de UI
// decide si `enu.ui` existe — headless G20). Registra las primitivas enu.ui.* y sus
// métodos de handle.
func (p *Pool) SetUIBackend(b UIBackend) {
	p.ui = b
	p.registerUIPrimitives()
}

// HasUI indica si el Pool tiene UI (para el preludio: `enu.has("ui")`, headless G20).
func (p *Pool) HasUI() bool { return p.ui != nil }

// SetOwnerSnapshot instala el resolvedor del dueño vigente del estado principal
// (G56, ADR-024). Lo llama el Runtime con su currentOwner. enu.worker._spawn lo
// invoca en la goroutine de la VM —donde la pila de dueños es coherente— para
// tomar la FOTO del dueño con que arranca cada worker. Sólo tiene sentido en el
// Pool principal.
func (p *Pool) SetOwnerSnapshot(fn func() string) { p.ownerSnapshot = fn }

// WorkerOwner devuelve la foto del plugin dueño de esta Instance si es un worker
// (G56, ADR-024): `owner` es la identidad capturada en el spawn y `fromWorker`
// indica que la Instance corre en un Pool de worker. En el estado principal
// devuelve ("", false), señal de que el llamador debe resolver el dueño vigente
// (currentOwner). Lectura pura de campos inmutables tras el spawn: sin carrera.
func (inst *Instance) WorkerOwner() (owner string, fromWorker bool) {
	return inst.workerOwner, inst.pool.isWorker
}

// registerUIPrimitives registra el catálogo enu.ui.* como host functions y los
// métodos de Region como métodos de handle (M10). Se llama desde SetUIBackend.
func (p *Pool) registerUIPrimitives() {
	// enu.ui.size() -> {w, h}
	p.Register("ui.size", func(inst *Instance, args []any) ([]any, error) {
		w, h := inst.pool.ui.Size()
		return []any{map[string]any{"w": int64(w), "h": int64(h)}}, nil
	})
	// enu.ui.caps() -> {colors, kitty_keyboard, mouse, images}
	p.Register("ui.caps", func(inst *Instance, args []any) ([]any, error) {
		return []any{inst.pool.ui.Caps()}, nil
	})
	// enu.ui._block(lines) -> {id, width, height}. El wrapper Lua enu.ui.block
	// (preludioInput) envuelve el id como handle con .width/.height (api.md §9.2).
	p.Register("ui._block", func(inst *Instance, args []any) ([]any, error) {
		var lines []any
		if len(args) > 0 {
			lines, _ = args[0].([]any)
		}
		b, err := inst.pool.ui.NewBlock(lines)
		if err != nil {
			return nil, &StructuredError{Code: "EINVAL", Message: err.Error()}
		}
		w, h := b.Dims()
		id := inst.AllocHandle("Block", b)
		return []any{map[string]any{"id": int64(id), "width": int64(w), "height": int64(h)}}, nil
	})
	// enu.ui.region(opts) -> Region (handle). opts: {x, y, w, h, z?}.
	p.Register("ui.region", func(inst *Instance, args []any) ([]any, error) {
		opts, _ := args[0].(map[string]any)
		x := optInt(opts, "x")
		y := optInt(opts, "y")
		w := optInt(opts, "w")
		h := optInt(opts, "h")
		z := optInt(opts, "z")
		r := inst.pool.ui.NewRegion(x, y, w, h, z)
		return []any{inst.AllocHandle("Region", r)}, nil
	})
	// enu.ui.clipboard_set(s)
	p.Register("ui.clipboard_set", func(inst *Instance, args []any) ([]any, error) {
		s, _ := args[0].(string)
		inst.pool.ui.ClipboardSet(s)
		return nil, nil
	})
	// enu.ui._next_input() -> ev: saca el evento de input más antiguo de la cola que
	// FeedInput llena (la puerta Go del input). nil si no hay ninguno. NO toma
	// inst.mu: corre DENTRO de Eval (mismo goroutine, que ya la tiene); el modelo
	// es single-thread (el estado principal) así que no hay carrera sobre la cola.
	p.Register("ui._next_input", func(inst *Instance, args []any) ([]any, error) {
		if len(inst.pendingInput) == 0 {
			return []any{nil}, nil
		}
		ev := inst.pendingInput[0]
		inst.pendingInput = inst.pendingInput[1:]
		return []any{ev}, nil
	})
	// enu.ui.clipboard_get() -> string?  — la única ⏸ del módulo (lee la respuesta
	// OSC 52 del terminal, bloqueante).
	p.RegisterSuspending("ui.clipboard_get", func(inst *Instance, args []any) ([]any, error) {
		s, ok := inst.pool.ui.ClipboardGet()
		if !ok {
			return []any{nil}, nil
		}
		return []any{s}, nil
	})

	// Métodos de Region (handle "Region", C5). Cada uno resuelve su RegionObj y
	// delega en el compositor. Blit resuelve además el handle del Block.
	reg := func(name string, fn func(r RegionObj, inst *Instance, args []any) ([]any, error)) {
		p.RegisterHandleMethod("Region", name, func(inst *Instance, val any, args []any) ([]any, error) {
			r, ok := val.(RegionObj)
			if !ok {
				return nil, &StructuredError{Code: "EINVAL", Message: "handle no es una Region"}
			}
			return fn(r, inst, args)
		})
	}
	reg("blit", func(r RegionObj, inst *Instance, args []any) ([]any, error) {
		x := argInt(args, 0)
		y := argInt(args, 1)
		b, err := inst.resolveBlock(args, 2)
		if err != nil {
			return nil, err
		}
		r.Blit(x, y, b)
		return nil, nil
	})
	reg("fill", func(r RegionObj, inst *Instance, args []any) ([]any, error) {
		var style map[string]any
		if len(args) > 0 {
			style, _ = args[0].(map[string]any)
		}
		r.Fill(style)
		return nil, nil
	})
	reg("clear", func(r RegionObj, inst *Instance, args []any) ([]any, error) { r.Clear(); return nil, nil })
	reg("move", func(r RegionObj, inst *Instance, args []any) ([]any, error) {
		r.Move(argInt(args, 0), argInt(args, 1))
		return nil, nil
	})
	reg("resize", func(r RegionObj, inst *Instance, args []any) ([]any, error) {
		r.Resize(argInt(args, 0), argInt(args, 1))
		return nil, nil
	})
	reg("raise", func(r RegionObj, inst *Instance, args []any) ([]any, error) { r.Raise(); return nil, nil })
	reg("lower", func(r RegionObj, inst *Instance, args []any) ([]any, error) { r.Lower(); return nil, nil })
	reg("show", func(r RegionObj, inst *Instance, args []any) ([]any, error) { r.Show(); return nil, nil })
	reg("hide", func(r RegionObj, inst *Instance, args []any) ([]any, error) { r.Hide(); return nil, nil })
	reg("destroy", func(r RegionObj, inst *Instance, args []any) ([]any, error) {
		// "quien crea, mata" (api.md §6): destruye el recurso en el compositor y
		// libera su handle, de modo que reusar la Region da ECLOSED (M10). El id lo
		// expone el despachador síncrono en inst.dispatchHandle.
		r.Destroy()
		inst.FreeHandle(inst.dispatchHandle)
		return nil, nil
	})
	reg("cursor", func(r RegionObj, inst *Instance, args []any) ([]any, error) {
		if len(args) == 0 || args[0] == nil {
			r.Cursor(0, 0, false) // ocultar
			return nil, nil
		}
		r.Cursor(argInt(args, 0), argInt(args, 1), true)
		return nil, nil
	})
}

// resolveBlock traduce el argumento args[i] (un handle de Block) a su BlockObj.
func (inst *Instance) resolveBlock(args []any, i int) (BlockObj, error) {
	if i >= len(args) {
		return nil, &StructuredError{Code: "EINVAL", Message: "blit: falta el Block"}
	}
	h, ok := toU32(args[i])
	if !ok {
		return nil, &StructuredError{Code: "EINVAL", Message: "blit: el argumento no es un Block"}
	}
	typ, val, live := inst.GetHandle(Handle(h))
	if !live || typ != "Block" {
		return nil, &StructuredError{Code: "EINVAL", Message: "blit: Block inválido o liberado"}
	}
	b, ok := val.(BlockObj)
	if !ok {
		return nil, &StructuredError{Code: "EINVAL", Message: "blit: handle no es un Block"}
	}
	return b, nil
}

// --- helpers de parseo de args ------------------------------------------------

func optInt(m map[string]any, k string) int {
	if m == nil {
		return 0
	}
	switch v := m[k].(type) {
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func argInt(args []any, i int) int {
	if i >= len(args) {
		return 0
	}
	switch v := args[i].(type) {
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// --- entrada de input desde el driver (M13 la cablea al tty real) -------------

// FeedInput inyecta un evento de input crudo y lo despacha SÍNCRONAMENTE por la
// pila del preludio (como enu.events.emit). Devuelve si algún handler lo consumió.
// El driver real (tty → inputEvent) lo llama en M13; aquí es la puerta Go del
// mecanismo. ev: {type, key?, mods?, x?, y?, text?, path?}.
func (inst *Instance) FeedInput(ev map[string]any) (bool, error) {
	inst.mu.Lock()
	inst.pendingInput = append(inst.pendingInput, ev)
	inst.mu.Unlock()
	out, lerr, err := inst.Eval(`return tostring(__ui_dispatch_input(enu.ui._next_input()))`)
	if err != nil {
		return false, err
	}
	if lerr != "" {
		return false, fmt.Errorf("vmwasm: FeedInput: %s", lerr)
	}
	return out == "true", nil
}
