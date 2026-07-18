package runtime

// Tests de M13b: enu.re sobre wasm (§10). Paridad con re.go: compile→EINVAL en
// patrón inválido, match (parte array 1-based + grupos con nombre), find_all
// (rangos de byte 1-based), replace. Ejercita handles (C5) con un tipo real (Re)
// y el punto de extensión AddPreludio (el wrapper Lua de match).

import (
	"testing"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

func wasmReInst(t *testing.T) *vmwasm.Instance {
	t.Helper()
	p, err := vmwasm.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	registerReWasm(p)
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// M13b.re.1: match con parte array 1-based ([1]=completo, [2..]=grupos).
func TestReWasmMatchArray(t *testing.T) {
	inst := wasmReInst(t)
	out := evalWasm(t, inst, `
		local re = enu.re.compile("([0-9]+)-([a-z]+)")
		local c = re:match("42-abc")
		return c[1] .. "|" .. c[2] .. "|" .. c[3]`)
	if out != "42-abc|42|abc" {
		t.Fatalf("match array: got %q", out)
	}
}

// M13b.re.2: grupos con nombre conviven con la parte array (caps.name).
func TestReWasmMatchNombrado(t *testing.T) {
	inst := wasmReInst(t)
	out := evalWasm(t, inst, `
		local re = enu.re.compile("(?P<anio>[0-9][0-9][0-9][0-9])-(?P<mes>[0-9][0-9])")
		local c = re:match("2026-07")
		return c.anio .. "/" .. c.mes .. " (array: " .. c[1] .. ")"`)
	if out != "2026/07 (array: 2026-07)" {
		t.Fatalf("match nombrado: got %q", out)
	}
}

// M13b.re.3: sin coincidencia → nil (no lanza).
func TestReWasmMatchSinCoincidencia(t *testing.T) {
	inst := wasmReInst(t)
	out := evalWasm(t, inst, `
		local re = enu.re.compile("xyz")
		return tostring(re:match("abc"))`)
	if out != "nil" {
		t.Fatalf("sin coincidencia: got %q", out)
	}
}

// M13b.re.4: find_all → rangos de byte 1-based inclusive (s:sub reconstruye).
func TestReWasmFindAll(t *testing.T) {
	inst := wasmReInst(t)
	out := evalWasm(t, inst, `
		local re = enu.re.compile("a+")
		local s = "baaxay"
		local rs = re:find_all(s)
		-- dos coincidencias: "aa" (2..3) y "a" (5..5)
		local parts = {}
		for _, r in ipairs(rs) do parts[#parts+1] = s:sub(r[1], r[2]) end
		return #rs .. ":" .. table.concat(parts, ",")`)
	if out != "2:aa,a" {
		t.Fatalf("find_all: got %q", out)
	}
}

// M13b.re.5: replace con referencias a grupos ($1, ${name}).
func TestReWasmReplace(t *testing.T) {
	inst := wasmReInst(t)
	out := evalWasm(t, inst, `
		local re = enu.re.compile("([a-z]+)@([a-z]+)")
		return re:replace("user@host", "$2.$1")`)
	if out != "host.user" {
		t.Fatalf("replace: got %q", out)
	}
}

// M13b.re.6: un patrón inválido → EINVAL accionable en compile.
func TestReWasmCompileInvalido(t *testing.T) {
	inst := wasmReInst(t)
	out := evalWasm(t, inst, `
		local ok, e = pcall(function() return enu.re.compile("(sin cerrar") end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:EINVAL" {
		t.Fatalf("compile inválido: got %q", out)
	}
}
