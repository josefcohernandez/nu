package vmwasm

// Tests de M05: el marshaling de la frontera VM (C1) y el registro de host
// functions (C2). Blindan las propiedades de las que cuelga todo M09:
//   - round-trip de valores Go↔Lua por el wire (int/float/string/bool/nil/
//     tablas anidadas/NULL) sin pérdida;
//   - byte-seguridad (G11): bytes no-UTF-8 cruzan intactos en ambos sentidos;
//   - integer vs float (Lua 5.4 tiene los dos subtipos);
//   - cruce de errores estructurados (§1.4): un HostFn que falla se convierte en
//     error(...) capturable por pcall con code/message/detail idénticos (C4).

import (
	"strings"
	"testing"
)

// poolWith registra las primitivas de prueba y devuelve una instancia.
func poolWith(t *testing.T, reg func(p *Pool)) *Instance {
	t.Helper()
	p, err := NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	reg(p)
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// M05.1: el codec Go es simétrico consigo mismo (Encode∘Decode = id).
func TestWireRoundTripGo(t *testing.T) {
	vals := []any{
		nil, true, false, int64(42), int64(-7), 3.5,
		"texto", "", NullValue,
		[]any{int64(1), "dos", true},
		map[string]any{"a": int64(1), "b": "z"},
	}
	enc, err := Encode(vals)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(vals) {
		t.Fatalf("len: got %d want %d", len(got), len(vals))
	}
	// spot checks
	if got[3] != int64(42) || got[5] != 3.5 || got[6] != "texto" || got[8] != NullValue {
		t.Fatalf("valores escalares no round-tripean: %v", got)
	}
	arr := got[9].([]any)
	if arr[1] != "dos" {
		t.Fatalf("array: %v", arr)
	}
	m := got[10].(map[string]any)
	if m["b"] != "z" {
		t.Fatalf("map: %v", m)
	}
}

// M05.2: G11 — un string con bytes NO-UTF-8 cruza el codec Go intacto.
func TestWireBytesNoUTF8(t *testing.T) {
	raw := string([]byte{0xff, 0x00, 0xfe, 0x41, 0x80})
	enc, err := Encode([]any{raw})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].(string) != raw {
		t.Fatalf("bytes no-UTF-8 corrompidos: %x != %x", got[0].(string), raw)
	}
}

// M05.2b: bytes corruptos con un recuento gigante NO revientan la memoria. Un
// u32 de recuento arbitrario (p. ej. de un dispatcher que pasó basura o de un
// frame truncado) pediría `make([]any, ~1e9)` → OOM. `count()` lo acota contra
// los bytes restantes y devuelve error. Blinda el arreglo del OOM de M10 (la
// costura del dispatcher pasaba bytes crudos a Decode).
func TestWireRecuentoCorrupto(t *testing.T) {
	// "hola": el primer u32 LE es ~1.6e9, muy por encima de los 0 bytes que quedan.
	if _, err := Decode([]byte("hola")); err == nil {
		t.Fatal("Decode aceptó un recuento gigante en vez de rechazarlo (riesgo de OOM)")
	}
	// un array anidado con recuento imposible dentro de una lista válida.
	corrupto := []byte{
		1, 0, 0, 0, // lista de 1 valor
		wArray,                 // el valor es un array...
		0xff, 0xff, 0xff, 0xff, // ...de 4294967295 elementos: imposible
	}
	if _, err := Decode(corrupto); err == nil {
		t.Fatal("Decode aceptó un array con recuento imposible (riesgo de OOM)")
	}
	// un buffer válido de verdad NO se rechaza por la guardia (no hay falso positivo).
	enc, err := Encode([]any{[]any{int64(1), int64(2), int64(3)}, map[string]any{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(enc); err != nil {
		t.Fatalf("la guardia rechazó datos válidos (falso positivo): %v", err)
	}
}

// M05.3: round-trip Go→Lua→Go a través de una primitiva `echo` registrada, que
// devuelve sus args tal cual. Verifica el marshaling REAL cruzando la frontera
// wasm en ambos sentidos.
func TestEchoPrimitiva(t *testing.T) {
	inst := poolWith(t, func(p *Pool) {
		p.Register("test.echo", func(inst *Instance, args []any) ([]any, error) {
			return args, nil // devuelve lo que reciba
		})
	})
	// Lua llama enu.test.echo con varios valores y comprueba que vuelven idénticos.
	out, lerr, err := inst.Eval(`
		local a, b, c, d = enu.test.echo(42, "hola", true, nil)
		return tostring(a) .. "|" .. tostring(b) .. "|" .. tostring(c) .. "|" .. tostring(d)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "42|hola|true|nil" {
		t.Fatalf("echo: got %q", out)
	}
}

// M05.4: integer vs float sobreviven el viaje (Lua 5.4 distingue math.type).
func TestEchoIntVsFloat(t *testing.T) {
	inst := poolWith(t, func(p *Pool) {
		p.Register("test.echo", func(inst *Instance, args []any) ([]any, error) {
			// verifica los tipos Go que llegan
			return []any{args[0], args[1]}, nil
		})
	})
	out, lerr, err := inst.Eval(`
		local i, f = enu.test.echo(7, 7.0)
		return math.type(i) .. "," .. math.type(f)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "integer,float" {
		t.Fatalf("int/float: got %q", out)
	}
}

// M05.5: una tabla anidada (array + map) cruza y vuelve estructuralmente igual.
func TestEchoTablaAnidada(t *testing.T) {
	inst := poolWith(t, func(p *Pool) {
		p.Register("test.echo", func(inst *Instance, args []any) ([]any, error) {
			return args, nil
		})
	})
	out, lerr, err := inst.Eval(`
		local t = enu.test.echo({ nombre = "nu", tags = { "a", "b" }, n = 3 })
		return t.nombre .. ":" .. t.tags[2] .. ":" .. tostring(t.n)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "nu:b:3" {
		t.Fatalf("tabla anidada: got %q", out)
	}
}

// M05.6: G11 en la frontera REAL — bytes no-UTF-8 desde Go a Lua y de vuelta.
func TestEchoBytesNoUTF8(t *testing.T) {
	inst := poolWith(t, func(p *Pool) {
		p.Register("test.raw", func(inst *Instance, args []any) ([]any, error) {
			return []any{string([]byte{0xff, 0x00, 0x80})}, nil
		})
		p.Register("test.len", func(inst *Instance, args []any) ([]any, error) {
			return []any{int64(len(args[0].(string)))}, nil
		})
	})
	// Lua recibe los 3 bytes crudos y los devuelve; Go cuenta 3.
	out, lerr, err := inst.Eval(`
		local raw = enu.test.raw()
		return tostring(#raw) .. ":" .. tostring(enu.test.len(raw))`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "3:3" {
		t.Fatalf("bytes no-UTF-8 en la frontera: got %q", out)
	}
}

// M05.7: el sentinel NULL (enu.json.NULL) cruza como sí mismo, distinto de nil.
func TestEchoNullSentinel(t *testing.T) {
	inst := poolWith(t, func(p *Pool) {
		p.Register("test.echo", func(inst *Instance, args []any) ([]any, error) {
			// el NULL llega como NullValue, NO como nil
			isNull := len(args) > 0 && args[0] == NullValue
			return []any{isNull}, nil
		})
	})
	out, lerr, err := inst.Eval(`return tostring(enu.test.echo(enu.json.NULL))`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "true" {
		t.Fatalf("NULL sentinel no cruzó como tal: got %q", out)
	}
}

// M05.8: un error estructurado de un HostFn se convierte en error(...) que un
// pcall de Lua captura con code/message idénticos (C4, paridad §1.4).
func TestEchoErrorEstructurado(t *testing.T) {
	inst := poolWith(t, func(p *Pool) {
		p.Register("test.falla", func(inst *Instance, args []any) ([]any, error) {
			return nil, &StructuredError{Code: "ENOENT", Message: "no existe", Detail: "x.txt"}
		})
	})
	out, lerr, err := inst.Eval(`
		local ok, e = pcall(function() return enu.test.falla() end)
		return tostring(ok) .. ":" .. tostring(e.code) .. ":" .. tostring(e.message) .. ":" .. tostring(e.detail)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "false:ENOENT:no existe:x.txt" {
		t.Fatalf("error estructurado no cruzó fielmente: got %q", out)
	}
}

// M05.9: un error NO estructurado (genérico) se envuelve con code EIO.
func TestEchoErrorGenerico(t *testing.T) {
	inst := poolWith(t, func(p *Pool) {
		p.Register("test.crash", func(inst *Instance, args []any) ([]any, error) {
			return nil, errStr("algo falló")
		})
	})
	out, lerr, err := inst.Eval(`
		local ok, e = pcall(function() return enu.test.crash() end)
		return tostring(ok) .. ":" .. tostring(e.code) .. ":" .. tostring(e.message)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if !strings.HasPrefix(out, "false:EIO:") {
		t.Fatalf("error genérico: got %q", out)
	}
}
