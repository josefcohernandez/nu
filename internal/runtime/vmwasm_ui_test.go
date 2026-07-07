package runtime

// Tests de M13c: el compositor REAL enchufado a la frontera wasm (§9). A diferencia
// de internal/vmwasm/ui_test.go (que probó el MECANISMO con un backend de grabación),
// aquí el backend es el `*compositor` de producción (compositor.go): las primitivas
// nu.ui.* mutan la rejilla real, y los efectos se observan inspeccionando el
// compositor Go (composeRow/paint), como los tests de caja blanca del compositor
// (compositor_test.go, mismo paquete). También se cubre que un Block de nu.text.*
// (wrap) se blittea igual que uno de nu.ui.block (round-trip del handle "Block").

import (
	"strings"
	"testing"

	"github.com/dbareagimeno/nu/internal/vmwasm"
)

// wasmUIInst crea una Instance wasm con el compositor real `comp` enchufado como
// backend de UI y el catálogo nu.text registrado (para el blit de un Block envuelto).
// El portapapeles va con io nil (test desnudo, sin TTY): clipboard es no-op / nil.
func wasmUIInst(t *testing.T, comp *compositor) *vmwasm.Instance {
	t.Helper()
	p, err := vmwasm.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.SetUIBackend(newCompositorBackend(comp, nil, nil))
	registerTextWasm(p, &Runtime{})
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// M13c.1: nu.ui.size()/nu.ui.caps() reales salen del compositor y de detectColors.
func TestUIWasmSizeCaps(t *testing.T) {
	comp := newCompositor(80, 24)
	inst := wasmUIInst(t, comp)
	out := evalWasm(t, inst, `
		local s = nu.ui.size()
		local c = nu.ui.caps()
		return s.w .. "x" .. s.h .. ":" .. type(c.colors) .. ":" .. tostring(c.mouse)`)
	if out != "80x24:number:false" {
		t.Fatalf("size/caps reales: got %q (esperado 80x24:number:false)", out)
	}
}

// M13c.2: nu.ui.region + region:blit(nu.ui.block(...)) pinta la celda real; se observa
// componiendo el compositor (la fila 0 lleva el glifo blitteado).
func TestUIWasmRegionBlit(t *testing.T) {
	comp := newCompositor(80, 24)
	inst := wasmUIInst(t, comp)
	evalWasm(t, inst, `
		local r = nu.ui.region({ x = 0, y = 0, w = 10, h = 3 })
		r:blit(0, 0, nu.ui.block({ "hola" }))
		return "ok"`)
	if len(comp.regions) != 1 {
		t.Fatalf("se esperaba 1 región en el compositor real, hay %d", len(comp.regions))
	}
	if got := composeRow(comp, 0); got != "hola" {
		t.Fatalf("blit real: fila compuesta 0 = %q, want %q", got, "hola")
	}
}

// M13c.3: el ciclo de vida de la región (move/resize/raise/hide/show/destroy) no
// panica, muta el compositor real, y destroy la descuelga del compositor. Tras
// destroy, un método sobre la región muerta da EINVAL "ya destruida" —PARIDAD con el
// backend gopher (ui.go, checkRegion/regionDestroy), no ECLOSED del handle crudo—: el
// envoltorio Lua de nu.ui.region (host.go, preludioInput) lleva la aliveness y hace
// destroy idempotente, de modo que la Region muerta sigue siendo un handle válido que
// responde el error de uso accionable, igual que una región gopher con alive=false.
func TestUIWasmRegionLifecycle(t *testing.T) {
	comp := newCompositor(80, 24)
	inst := wasmUIInst(t, comp)
	out := evalWasm(t, inst, `
		local r = nu.ui.region({ x = 1, y = 1, w = 10, h = 5 })
		r:move(3, 4)
		r:resize(20, 8)
		r:raise()
		r:lower()
		r:hide()
		r:show()
		r:destroy()
		r:destroy()   -- idempotente (§9.1): destruir dos veces es inocuo
		local ok, e = pcall(function() return r:move(0, 0) end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:EINVAL" {
		t.Fatalf("ciclo de vida: método tras destroy debía dar EINVAL (paridad gopher), got %q", out)
	}
	if len(comp.regions) != 0 {
		t.Fatalf("destroy no descolgó la región del compositor: quedan %d", len(comp.regions))
	}
}

// M13c.4: nu.ui.region:cursor coloca el cursor real del compositor (último gana, §9.1)
// y produce la secuencia de posicionado esperada en el frame.
func TestUIWasmCursor(t *testing.T) {
	comp := newCompositor(80, 24)
	inst := wasmUIInst(t, comp)
	evalWasm(t, inst, `
		local a = nu.ui.region({ x = 0, y = 0, w = 5, h = 5 })
		local b = nu.ui.region({ x = 10, y = 2, w = 5, h = 5 })
		a:cursor(1, 1)
		b:cursor(2, 3)   -- gana esta: cursor a pantalla (10+2, 2+3) = col 13, fila 6 (1-based: 12;13H)
		return "ok"`)
	comp.paint()
	// La región b (x=10,y=2) con cursor local (2,3) → pantalla (12,5) → 1-based "6;13H".
	if enc := comp.encoded(); !strings.Contains(enc, "\x1b[6;13H\x1b[?25h") {
		t.Fatalf("el frame no colocó el cursor de la última región: %q", enc)
	}
}

// M13c.5: un Block de nu.text.wrap se blittea en una región igual que uno de
// nu.ui.block (round-trip del handle "Block", C5): la fila compuesta lleva el texto
// envuelto y el Block tiene .width<=5 y .height>1.
func TestUIWasmBlitWrap(t *testing.T) {
	comp := newCompositor(80, 24)
	inst := wasmUIInst(t, comp)
	out := evalWasm(t, inst, `
		local blk = nu.text.wrap("hola mundo", 5)
		local r = nu.ui.region({ x = 0, y = 0, w = 5, h = 5 })
		r:blit(0, 0, blk)
		return tostring(blk.width <= 5) .. ":" .. tostring(blk.height > 1)`)
	if out != "true:true" {
		t.Fatalf("dims del Block de wrap: got %q (esperado true:true)", out)
	}
	if got := composeRow(comp, 0); got != "hola" {
		t.Fatalf("blit del wrap: fila 0 = %q, want %q", got, "hola")
	}
	if got := composeRow(comp, 1); got != "mundo" {
		t.Fatalf("blit del wrap: fila 1 = %q, want %q", got, "mundo")
	}
}

// M13c.6 (G28): region:blit con offset NEGATIVO recorta el borde inicial sin panic;
// la ventana visible arranca en una fila posterior del Block.
func TestUIWasmBlitOffsetNegativoG28(t *testing.T) {
	comp := newCompositor(80, 24)
	inst := wasmUIInst(t, comp)
	evalWasm(t, inst, `
		local b = nu.ui.block({ "L0", "L1", "L2" })
		local r = nu.ui.region({ x = 0, y = 0, w = 10, h = 3 })
		r:blit(0, -1, b)   -- oy negativo: la fila 0 muestra la 2ª línea del Block (G28)
		return "ok"`)
	if got := composeRow(comp, 0); got != "L1" {
		t.Fatalf("G28 blit(0,-1): fila 0 = %q, want %q", got, "L1")
	}
	if got := composeRow(comp, 1); got != "L2" {
		t.Fatalf("G28 blit(0,-1): fila 1 = %q, want %q", got, "L2")
	}
}

// M13c.7: la tubería REAL de pintado (composite + diff + encode) produce el ANSI del
// frame con el glifo blitteado — el compositor de producción, no una grabación.
func TestUIWasmPaintANSI(t *testing.T) {
	comp := newCompositor(20, 3)
	inst := wasmUIInst(t, comp)
	evalWasm(t, inst, `
		local r = nu.ui.region({ x = 0, y = 0, w = 10, h = 1 })
		r:blit(0, 0, nu.ui.block({ "hi" }))
		return "ok"`)
	if n := comp.paint(); n == 0 {
		t.Fatal("paint no emitió celdas cambiadas tras el blit")
	}
	if enc := comp.encoded(); !strings.Contains(enc, "hi") {
		t.Fatalf("el frame ANSI no contiene el glifo pintado: %q", enc)
	}
}

// M13c.8: nu.ui.region:fill con un estilo real tiñe el lienzo (el compositor pinta el
// SGR del color); un fg literal cruza y llega al frame.
func TestUIWasmFillStyle(t *testing.T) {
	comp := newCompositor(4, 1)
	inst := wasmUIInst(t, comp)
	evalWasm(t, inst, `
		local r = nu.ui.region({ x = 0, y = 0, w = 4, h = 1 })
		r:fill({ fg = "#ff0000" })
		return "ok"`)
	comp.paint()
	// #ff0000 → truecolor 38;2;255;0;0 en el SGR del frame.
	if enc := comp.encoded(); !strings.Contains(enc, "38;2;255;0;0") {
		t.Fatalf("fill no aplicó el color literal al frame: %q", enc)
	}
}
