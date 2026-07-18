package vmwasm

// Tests de M10: userdata como handles opacos (C5). Blindan el mecanismo: una
// primitiva crea un handle (AllocHandle), Lua lo recibe como opaco y llama sus
// métodos (h:metodo()), que se despachan al objeto Go; el ciclo de vida
// (liberar → ECLOSED al reusar) y la identidad se conservan.

import (
	"testing"
)

// counter es el objeto Go de ejemplo detrás de un handle.
type counter struct{ n int64 }

// registerCounter registra una primitiva `test.counter()` que crea un handle de
// tipo "Counter", y sus métodos inc/get/close.
func registerCounter(p *Pool) {
	p.Register("test.counter", func(inst *Instance, args []any) ([]any, error) {
		h := inst.AllocHandle("Counter", &counter{})
		return []any{h}, nil
	})
	p.RegisterHandleMethod("Counter", "inc", func(inst *Instance, val any, args []any) ([]any, error) {
		c := val.(*counter)
		by := int64(1)
		if len(args) > 0 {
			if b, ok := args[0].(int64); ok {
				by = b
			}
		}
		c.n += by
		return []any{c.n}, nil
	})
	p.RegisterHandleMethod("Counter", "get", func(inst *Instance, val any, args []any) ([]any, error) {
		return []any{val.(*counter).n}, nil
	})
	p.RegisterHandleMethod("Counter", "close", func(inst *Instance, val any, args []any) ([]any, error) {
		// la primitiva close libera el handle (quien crea, mata; api.md §6)
		return nil, nil
	})
}

// M10.1: crear un handle y llamar sus métodos — el objeto Go recuerda su estado.
func TestHandleMetodos(t *testing.T) {
	inst := poolWith(t, registerCounter)
	out, lerr, err := inst.Eval(`
		local c = enu.test.counter()
		c:inc()
		c:inc(5)
		return tostring(c:get())`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "6" {
		t.Fatalf("got %q (esperado 6 = 1+5)", out)
	}
}

// M10.2: dos handles son independientes (identidad opaca distinta).
func TestHandleIdentidad(t *testing.T) {
	inst := poolWith(t, registerCounter)
	out, lerr, err := inst.Eval(`
		local a = enu.test.counter()
		local b = enu.test.counter()
		a:inc(); a:inc()
		b:inc()
		return tostring(a:get()) .. "," .. tostring(b:get())`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "2,1" {
		t.Fatalf("got %q (los handles no son independientes)", out)
	}
}

// M10.3: un handle liberado da ECLOSED accionable al reusarse (ciclo de vida).
func TestHandleLiberado(t *testing.T) {
	// una primitiva que libera explícitamente
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	registerCounter(p)
	p.RegisterHandleMethod("Counter", "destroy", func(inst *Instance, val any, args []any) ([]any, error) {
		return nil, nil
	})
	// re-registramos close para que libere de verdad: necesitamos el id, que el
	// dispatcher ya resolvió; usamos un método que llama FreeHandle sobre el handle
	// actual — pero el método recibe val, no el id. Para el test, exponemos una
	// primitiva liberadora que toma el handle.
	p.Register("test.free", func(inst *Instance, args []any) ([]any, error) {
		if h, ok := toU32(args[0]); ok {
			inst.FreeHandle(Handle(h))
		}
		return nil, nil
	})
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inst.Close() })

	out, lerr, err := inst.Eval(`
		local c = enu.test.counter()
		enu.test.free(c.__id)
		local ok, e = pcall(function() return c:get() end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "false:ECLOSED" {
		t.Fatalf("got %q (un handle liberado debe dar ECLOSED)", out)
	}
}

// M10.4: un handle cruza de ida y vuelta por una primitiva sin perder identidad
// (el wire lo lleva como índice, C5).
func TestHandleRoundTrip(t *testing.T) {
	inst := poolWith(t, func(p *Pool) {
		registerCounter(p)
		// echo que devuelve el handle recibido
		p.Register("test.echo", func(inst *Instance, args []any) ([]any, error) {
			return args, nil
		})
	})
	out, lerr, err := inst.Eval(`
		local c = enu.test.counter()
		c:inc(9)
		local c2 = enu.test.echo(c)   -- cruza a Go y vuelve
		return tostring(c2:get())`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "9" {
		t.Fatalf("got %q (el handle perdió identidad en el round-trip)", out)
	}
}
