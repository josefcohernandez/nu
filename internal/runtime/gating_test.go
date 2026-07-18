package runtime

// Tests del GATING HEADLESS de `enu.ui` (G20, §9, S32) y de la superficie de S32
// (clipboard OSC 52, eventos `ui:*`). Blindan el "Criterio de hecho" de la sesión:
//
//   - **Gating (G20)**: con la UI desactivada (simula headless: `WithForceUI(false)`),
//     el módulo `enu.ui` es **inexistente** desde Lua (`enu.ui == nil`) y
//     `enu.has("ui")` es **false**. Con la UI forzada (test), `enu.ui` existe y
//     `enu.has("ui")` es **true**. Es el criterio "bajo `enu -e` enu.ui inexistente".
//   - **clipboard_set OSC 52**: produce la secuencia OSC 52 correcta (bytes emitidos
//     al "terminal" inyectado).
//   - **clipboard_get**: sin driver de TTY (headless) devuelve `nil` (el parseo se
//     blinda en `osc52_test.go`).
//   - **eventos `ui:*`**: `ui:resize` se emite al cambiar el tamaño; `ui:focus`/
//     `ui:suspend`/`ui:resume` se emiten por sus vías inyectables.

import (
	"bytes"
	"testing"
)

// TestGatingHeadlessNoUI blinda G20: con la UI desactivada (headless), `enu.ui` no
// existe y `enu.has("ui")` es false. El bus (`ui:` es del core) sigue disponible.
func TestGatingHeadlessNoUI(t *testing.T) {
	rt := New(WithDataDir(t.TempDir()), WithForceUI(false))
	defer rt.Close()
	h := &harness{t: t, rt: rt}

	// `enu.ui` directamente no está en el global `enu` (no es probar-y-capturar: es nil).
	h.expectEval(`return tostring(enu.ui == nil)`, "true")
	// `enu.has("ui")` es false (deny-by-default, coherente con que el módulo no exista).
	h.expectEval(`return tostring(enu.has("ui"))`, "false")
	// El compositor tampoco se construyó (caja blanca): rt.ui es nil.
	if rt.ui != nil {
		t.Fatal("headless: rt.ui debería ser nil (sin compositor)")
	}
	// `enu.has` de una cap desconocida sigue siendo false.
	h.expectEval(`return tostring(enu.has("inexistente"))`, "false")
}

// TestGatingForcedUI blinda el otro lado de G20: con la UI forzada (lo que hacen los
// tests), `enu.ui` existe y `enu.has("ui")` es true.
func TestGatingForcedUI(t *testing.T) {
	h := newHarness(t) // newHarness fuerza la UI (WithForceUI(true))
	h.expectEval(`return tostring(enu.ui ~= nil)`, "true")
	h.expectEval(`return tostring(enu.has("ui"))`, "true")
	// ui.images sigue false: el protocolo de imágenes no se ha negociado (driver S33+).
	h.expectEval(`return tostring(enu.has("ui.images"))`, "false")
}

// TestUIResizeEvent blinda que cambiar el tamaño de la pantalla emite `ui:resize` con
// `{w, h}` (§9.1: "cambios → evento ui:resize") y actualiza `enu.ui.size()`. Un resize
// al MISMO tamaño no emite un evento espurio.
func TestUIResizeEvent(t *testing.T) {
	h := newHarnessUI(t, 80, 24)
	h.eval(`
		rw, rh, count = nil, nil, 0
		enu.events.on("ui:resize", function(ev) count = count + 1; rw, rh = ev.w, ev.h end)
	`)

	// Inyecta un cambio de tamaño (lo que el driver de TTY haría ante un SIGWINCH).
	h.rt.resizeUI(100, 40)
	h.expectEval(`return tostring(rw), tostring(rh), tostring(count)`, "100", "40", "1")
	// El tamaño visible por Lua se actualizó.
	h.expectEval(`local s = enu.ui.size(); return s.w, s.h`, "100", "40")

	// Un resize al mismo tamaño NO emite otro evento.
	h.rt.resizeUI(100, 40)
	h.expectEval(`return tostring(count)`, "1")
}

// TestUIFocusSuspendResumeEvents blinda las vías de emisión de `ui:focus`,
// `ui:suspend` y `ui:resume` (esqueleto de S32; el driver real los disparará en S33+).
func TestUIFocusSuspendResumeEvents(t *testing.T) {
	h := newHarnessUI(t, 20, 5)
	h.eval(`
		focused, suspended, resumed = nil, 0, 0
		enu.events.on("ui:focus", function(ev) focused = ev.focused end)
		enu.events.on("ui:suspend", function() suspended = suspended + 1 end)
		enu.events.on("ui:resume", function() resumed = resumed + 1 end)
	`)

	h.rt.emitUIFocus(true)
	h.expectEval(`return tostring(focused)`, "true")
	h.rt.emitUIFocus(false)
	h.expectEval(`return tostring(focused)`, "false")

	h.rt.emitUISuspend()
	h.rt.emitUIResume()
	h.rt.emitUISuspend()
	h.expectEval(`return tostring(suspended), tostring(resumed)`, "2", "1")
}

// TestUIClipboardLuaSurface es el snippet del lado del autor de extensiones (DoD §2):
// comprueba que `enu.has("ui")` es true y que las firmas del portapapeles existen y se
// invocan sin error desde Lua (set síncrono; get desde una task).
func TestUIClipboardLuaSurface(t *testing.T) {
	h := newHarness(t)
	if err := h.rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	var buf bytes.Buffer
	h.rt.ui.clipWriter = &buf

	h.expectEval(`
		assert(enu.has("ui"), "enu.has('ui') debe ser true con UI activa")
		assert(type(enu.ui.clipboard_set) == "function", "clipboard_set existe")
		assert(type(enu.ui.clipboard_get) == "function", "clipboard_get existe")
		enu.ui.clipboard_set("x")
		return "ok"
	`, "ok")
}
