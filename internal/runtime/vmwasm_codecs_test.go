package runtime

// Tests de M13b: los codecs enu.json/toml/yaml sobre el backend wasm. Blindan la
// paridad con §12 (round-trip, G11 UTF-8 estricto, NULL, tabla vacía → objeto,
// array-de-tablas TOML) y la mejora de Lua 5.4 (integer vs float preservados).

import (
	"testing"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

// wasmCodecInst crea una Instance wasm con el catálogo de codecs registrado.
func wasmCodecInst(t *testing.T) *vmwasm.Instance {
	t.Helper()
	p, err := vmwasm.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	registerCodecsWasm(p)
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

func evalWasm(t *testing.T, inst *vmwasm.Instance, chunk string) string {
	t.Helper()
	out, lerr, err := inst.Eval(chunk)
	if err != nil || lerr != "" {
		t.Fatalf("eval %q: lerr=%q err=%v", chunk, lerr, err)
	}
	return out
}

// M13b.1: JSON round-trip de una estructura anidada (encode∘decode preserva).
func TestCodecsWasmJSONRoundTrip(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `
		local orig = { nombre = "nu", tags = { "a", "b" }, activo = true, meta = { n = 3 } }
		local s = enu.json.encode(orig)
		local back = enu.json.decode(s)
		return back.nombre .. ":" .. back.tags[2] .. ":" .. tostring(back.activo) .. ":" .. tostring(back.meta.n)`)
	if out != "nu:b:true:3" {
		t.Fatalf("round-trip JSON: got %q", out)
	}
}

// M13b.2: integer vs float SOBREVIVEN el round-trip (mejora de Lua 5.4 sobre gopher).
func TestCodecsWasmJSONIntVsFloat(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `
		local back = enu.json.decode('{ "i": 42, "f": 3.5 }')
		return math.type(back.i) .. "," .. math.type(back.f)`)
	if out != "integer,float" {
		t.Fatalf("int/float en JSON: got %q (esperado integer,float)", out)
	}
}

// M13b.3: el sentinel NULL sobrevive el round-trip (null → enu.json.NULL → null),
// sin perder la clave (a diferencia de nil, que la borraría).
func TestCodecsWasmJSONNull(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `
		local back = enu.json.decode('{ "a": null, "b": 1 }')
		local esNull = (back.a == enu.json.NULL)
		local s = enu.json.encode(back)
		-- el null sobrevive: re-encodear vuelve a poner "a":null
		return tostring(esNull) .. ":" .. tostring(s:find('"a":null') ~= nil)`)
	if out != "true:true" {
		t.Fatalf("NULL round-trip: got %q", out)
	}
}

// M13b.4: G11 — encode de un string con bytes UTF-8 inválidos → EINVAL (no U+FFFD).
func TestCodecsWasmJSONUTF8Estricto(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `
		local malo = string.char(0xff, 0xfe)
		local ok, e = pcall(function() return enu.json.encode({ x = malo }) end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:EINVAL" {
		t.Fatalf("UTF-8 estricto: got %q", out)
	}
}

// M13b.5: una tabla VACÍA encodea como objeto {} (§12), no como [].
func TestCodecsWasmJSONTablaVacia(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `return enu.json.encode({})`)
	if out != "{}" {
		t.Fatalf("tabla vacía: got %q (esperado {})", out)
	}
}

// M13b.6: NaN/Inf → EINVAL (no representable en JSON).
func TestCodecsWasmJSONNoFinito(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `
		local ok, e = pcall(function() return enu.json.encode({ x = 1/0 }) end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:EINVAL" {
		t.Fatalf("no finito: got %q", out)
	}
}

// M13b.7: TOML round-trip, incluido un array-de-tablas ([[x]]) — el formato de
// providers.toml. La raíz debe ser un objeto.
func TestCodecsWasmTOML(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `
		local doc = [=[
title = "demo"
[[server]]
host = "a"
[[server]]
host = "b"
]=]
		local t = enu.toml.decode(doc)
		local s = enu.toml.encode(t)
		local back = enu.toml.decode(s)
		return back.title .. ":" .. back.server[1].host .. ":" .. back.server[2].host`)
	if out != "demo:a:b" {
		t.Fatalf("TOML array-de-tablas: got %q", out)
	}
}

// M13b.8: TOML encode con raíz no-objeto → EINVAL.
func TestCodecsWasmTOMLRaizInvalida(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `
		local ok, e = pcall(function() return enu.toml.encode({ 1, 2, 3 }) end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:EINVAL" {
		t.Fatalf("TOML raíz array: got %q", out)
	}
}

// M13b.9: YAML round-trip (frontmatter de skills: mapa con listas y strings).
func TestCodecsWasmYAML(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `
		local doc = "name: skill-x\ntags:\n  - alpha\n  - beta\n"
		local y = enu.yaml.decode(doc)
		return y.name .. ":" .. y.tags[1] .. ":" .. y.tags[2]`)
	if out != "skill-x:alpha:beta" {
		t.Fatalf("YAML: got %q", out)
	}
}

// M13b.10: JSON inválido → EINVAL accionable.
func TestCodecsWasmJSONInvalido(t *testing.T) {
	inst := wasmCodecInst(t)
	out := evalWasm(t, inst, `
		local ok, e = pcall(function() return enu.json.decode('{ roto') end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:EINVAL" {
		t.Fatalf("JSON inválido: got %q", out)
	}
}
