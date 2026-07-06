package runtime

// Tests de M13b/M13c: nu.text sobre wasm (§10). width/truncate (M13b, no producen
// Block) y wrap/markdown/highlight/diff (M13c, producen Blocks como handle con
// .width/.height). El render fino de cada uno ya está blindado en los tests gopher
// (markdown_test/highlight_test/diff_test); aquí un smoke de dimensiones y la paridad
// de la superficie (firmas, EINVAL, forma de diff).

import (
	"testing"

	"github.com/dbareagimeno/nu/internal/vmwasm"
)

func wasmTextInst(t *testing.T) *vmwasm.Instance {
	t.Helper()
	p, err := vmwasm.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	registerTextWasm(p, &Runtime{})
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// M13b.text.1: width cuenta celdas (ASCII 1, east-asian wide 2).
func TestTextWasmWidth(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		return tostring(nu.text.width("abc")) .. ":" .. tostring(nu.text.width("日本"))`)
	// "abc" = 3 celdas; "日本" = 2 caracteres wide = 4 celdas.
	if out != "3:4" {
		t.Fatalf("width: got %q (esperado 3:4)", out)
	}
}

// M13b.text.2: truncate recorta a width celdas con elipsis; si cabe, sin tocar.
func TestTextWasmTruncate(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		local cabe = nu.text.truncate("hola", 10)
		local corta = nu.text.truncate("hola mundo", 6, { ellipsis = "…" })
		return cabe .. "|" .. corta .. "|" .. tostring(nu.text.width(corta) <= 6)`)
	// "hola" cabe entero; "hola mundo" recortado a <=6 celdas con elipsis.
	if got := out; got[:5] != "hola|" || got[len(got)-4:] != "true" {
		t.Fatalf("truncate: got %q", out)
	}
}

// M13b.text.3: truncate con width negativo → EINVAL.
func TestTextWasmTruncateInvalido(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		local ok, e = pcall(function() return nu.text.truncate("x", -1) end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:EINVAL" {
		t.Fatalf("truncate inválido: got %q", out)
	}
}

// M13c.text.4: wrap produce un Block con .width<=width y .height>1 (word-wrap real).
func TestTextWasmWrap(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		local b = nu.text.wrap("hola mundo largo del todo", 6)
		return tostring(b.width <= 6) .. ":" .. tostring(b.height > 1)`)
	if out != "true:true" {
		t.Fatalf("wrap: got %q (esperado true:true)", out)
	}
}

// M13c.text.5: wrap con width no positivo → EINVAL.
func TestTextWasmWrapInvalido(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		local ok, e = pcall(function() return nu.text.wrap("x", 0) end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:EINVAL" {
		t.Fatalf("wrap inválido: got %q", out)
	}
}

// M13c.text.6: markdown produce un Block coherente (ancho <= opts.width, varias líneas).
func TestTextWasmMarkdown(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		local b = nu.text.markdown("# Título\n\nun párrafo con varias palabras sueltas", { width = 12 })
		return tostring(b.width <= 12) .. ":" .. tostring(b.height >= 2)`)
	if out != "true:true" {
		t.Fatalf("markdown: got %q (esperado true:true)", out)
	}
}

// M13c.text.7: markdown sin opts.width → EINVAL (obligatorio, §10).
func TestTextWasmMarkdownInvalido(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		local ok, e = pcall(function() return nu.text.markdown("x", {}) end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:EINVAL" {
		t.Fatalf("markdown sin width: got %q", out)
	}
}

// M13c.text.8: highlight produce un Block (una línea del código = una del Block); un
// lenguaje desconocido degrada a texto plano SIN error (§10).
func TestTextWasmHighlight(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		local b = nu.text.highlight("local x = 1\nreturn x", "lua")
		local plano = nu.text.highlight("línea1\nlínea2", "desconocido-xyz")
		return b.height .. ":" .. plano.height`)
	if out != "2:2" {
		t.Fatalf("highlight: got %q (esperado 2:2)", out)
	}
}

// M13c.text.9: diff estructurado — hunks con la forma de §10 y, con opts.render, un
// Block pintado.
func TestTextWasmDiff(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		local r = nu.text.diff("a\nb\nc", "a\nB\nc", { render = true })
		local first = r.hunks[1]
		return #r.hunks .. ":" .. tostring(r.block ~= nil) .. ":" ..
		       tostring(r.block.height > 0) .. ":" .. first.lines[1].kind`)
	if out != "1:true:true:context" {
		t.Fatalf("diff: got %q (esperado 1:true:true:context)", out)
	}
}

// M13c.text.10: diff de dos textos iguales → sin hunks y, sin render, sin Block.
func TestTextWasmDiffSinCambios(t *testing.T) {
	inst := wasmTextInst(t)
	out := evalWasm(t, inst, `
		local r = nu.text.diff("x", "x")
		return #r.hunks .. ":" .. tostring(r.block)`)
	if out != "0:nil" {
		t.Fatalf("diff sin cambios: got %q (esperado 0:nil)", out)
	}
}
