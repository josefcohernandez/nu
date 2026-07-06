package vmwasm

// Userdata como handles opacos (migracion-vm.md M10, categoría C5 del censo).
// api.md §1.5: los handles del core (Task, Proc, Region, Block, Stream, Ws...)
// son "userdata opacos con métodos". Como los valores no cruzan la frontera por
// referencia (C1), un handle es un ENTERO opaco en el lado Lua (índice en una
// tabla Go de objetos vivos) y sus métodos se despachan por una host function
// genérica. Es el mismo modelo mental que el `handles.go` del kernel (registro
// por dueño), aquí adaptado a la frontera wasm.
//
// El ciclo de vida ("quien crea, mata" + red del GC, api.md §6) se conserva: una
// primitiva `close`/`cancel`/`destroy` libera el handle (freeHandle); un índice
// ya liberado o inválido produce un error estructurado accionable.

import (
	"fmt"
	"sync"
)

// handleEntry es un objeto vivo detrás de un handle: su tipo (para despachar
// métodos) y el valor Go real (un *os.Process, una *Region, etc.).
type handleEntry struct {
	typ string
	val any
}

// handleTable es la tabla de objetos vivos de una Instance. Bajo mutex porque un
// HostFn suspendente (goroutine de fondo) podría, en teoría, liberar un handle
// mientras el hilo principal asigna otro — aunque el contrato de
// RegisterSuspending lo desaconseja, el mutex lo blinda barato.
type handleTable struct {
	mu   sync.Mutex
	objs map[uint32]*handleEntry
	next uint32
}

func newHandleTable() *handleTable {
	return &handleTable{objs: make(map[uint32]*handleEntry), next: 1}
}

// AllocHandle registra un objeto vivo y devuelve su handle opaco. Lo usa una
// primitiva del kernel que crea un recurso (proc.spawn, ui.region...).
func (inst *Instance) AllocHandle(typ string, val any) Handle {
	ht := inst.handles
	ht.mu.Lock()
	defer ht.mu.Unlock()
	id := ht.next
	ht.next++
	ht.objs[id] = &handleEntry{typ: typ, val: val}
	return Handle(id)
}

// GetHandle resuelve un handle a su objeto; ok=false si fue liberado o es inválido.
func (inst *Instance) GetHandle(h Handle) (string, any, bool) {
	ht := inst.handles
	ht.mu.Lock()
	defer ht.mu.Unlock()
	e, ok := ht.objs[uint32(h)]
	if !ok {
		return "", nil, false
	}
	return e.typ, e.val, true
}

// FreeHandle libera un handle (close/cancel/destroy). Idempotente.
func (inst *Instance) FreeHandle(h Handle) {
	ht := inst.handles
	ht.mu.Lock()
	defer ht.mu.Unlock()
	delete(ht.objs, uint32(h))
}

// HandleMethod es un método de un tipo de handle (censo C5): recibe el valor Go
// real y los args, devuelve valores o error. Como las primitivas, un método
// puede ser síncrono o suspendente (RegisterHandleMethod / ...Suspending).
type HandleMethod func(inst *Instance, val any, args []any) ([]any, error)

// registerHandleDispatch instala las dos primitivas genéricas de despacho de
// métodos de handle (una síncrona, una suspendente) que el preludio usa desde la
// metatable. Se llama una vez al preparar el Pool.
func (p *Pool) registerHandleDispatch() {
	p.reg.methods = make(map[string]HandleMethod)
	// __handle_call(handleid, "Tipo.metodo", ...args): despacho SÍNCRONO.
	p.Register("__handle_call", handleCallFn(false))
	// __handle_call_s(...): despacho SUSPENDENTE (Proc:wait, Ws:recv...).
	p.RegisterSuspending("__handle_call_s", handleCallFn(true))
}

// handleCallFn construye el HostFn de despacho. Los métodos suspendentes NO deben
// tocar la Instance salvo por el valor (contrato de RegisterSuspending); el
// síncrono sí puede (p. ej. liberar el handle).
func handleCallFn(bool) HostFn {
	return func(inst *Instance, args []any) ([]any, error) {
		if len(args) < 2 {
			return nil, &StructuredError{Code: "EINVAL", Message: "handle_call: faltan (handle, método)"}
		}
		hid, ok := toU32(args[0])
		if !ok {
			return nil, &StructuredError{Code: "EINVAL", Message: "handle_call: handle inválido"}
		}
		method, _ := args[1].(string)
		typ, val, live := inst.GetHandle(Handle(hid))
		if !live {
			return nil, &StructuredError{Code: "ECLOSED", Message: fmt.Sprintf("handle %d liberado o inválido", hid)}
		}
		key := typ + "." + method
		fn, ok := inst.pool.reg.methods[key]
		if !ok {
			return nil, &StructuredError{Code: "EINVAL", Message: "método desconocido: " + key}
		}
		// Expone el handle en despacho para que un método liberador (destroy/close/
		// cancel) pueda soltar su propio handle con inst.FreeHandle(inst.dispatchHandle).
		// Sólo es válido en el despacho SÍNCRONO (hilo principal); los métodos
		// suspendentes no deben tocar la Instance (contrato de RegisterSuspending).
		inst.dispatchHandle = Handle(hid)
		return fn(inst, val, args[2:])
	}
}

// RegisterHandleMethod registra un método SÍNCRONO de un tipo de handle.
func (p *Pool) RegisterHandleMethod(typ, method string, fn HandleMethod) {
	if p.reg.methods == nil {
		p.reg.methods = make(map[string]HandleMethod)
	}
	p.reg.methods[typ+"."+method] = fn
}

func toU32(v any) (uint32, bool) {
	switch x := v.(type) {
	case int64:
		return uint32(x), true
	case float64:
		return uint32(x), true
	case Handle:
		return uint32(x), true
	default:
		return 0, false
	}
}
