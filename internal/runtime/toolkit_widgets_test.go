package runtime

// Tests de los widgets de DECORACIÓN del toolkit (ADR-018): box (marco), richtext
// (línea multi-span) y spinner (animado). Lua puro sobre la API congelada; el render
// se inspecciona en la rejilla del compositor (como toolkit_test.go/chat_test.go).
// El test de la caja en x=1 BLINDA además G37 (el offset X de blitBlock): un marco
// con padding coloca su borde izquierdo en x>0, justo lo que el bug rompía.

import "testing"

// (bootToolkit vive en toolkit_test.go: arranca un runtime con el toolkit activado.)

// TestToolkitBoxFrameG37: una caja con título dentro de un vbox con padding pinta su
// marco COMPLETO —esquinas, lados y título— con el borde izquierdo en x>0. Antes de
// G37, la columna 0 del Block en x>0 se perdía (el borde izquierdo desaparecía).
func TestToolkitBoxFrameG37(t *testing.T) {
	h := bootToolkit(t, 20, 4)
	h.eval(`
		local tk = require("toolkit")
		local col = tk.vbox({ pad = 1 })
		local b = tk.box({ title = "T", border = "rounded" })
		b.pref_h = 3
		col:add(b)
		APP = tk.app({ root = col, w = 20, h = 5 })
	`)
	withToken(h.rt, func() {
		c := h.rt.ui.comp
		c.composite()
		// fila 1 (la caja arranca en y=1 por el pad): "╭─ T ──…──╮" desde la columna 1.
		top := gridRow(c.back, 1)
		if top == "" || []rune(top)[1] != '╭' {
			t.Fatalf("borde superior sin esquina izquierda en x=1 (G37): %q", top)
		}
		// fila 2: lados "│ … │"; la primera celda no-espacio en x=1 debe ser '│'.
		mid := []rune(gridRow(c.back, 2))
		if len(mid) < 2 || mid[1] != '│' {
			t.Fatalf("lado izquierdo del marco perdido en x=1 (G37): %q", string(mid))
		}
	})
}

// TestToolkitRichtext: una línea de varios spans con alineación a la derecha pinta el
// texto pegado al borde derecho (lo que la statusline necesita).
func TestToolkitRichtext(t *testing.T) {
	h := bootToolkit(t, 20, 1)
	h.eval(`
		local tk = require("toolkit")
		local r = tk.richtext({ align = "right", spans = {
			{ text = "ab", style = { fg = "accent" } },
			{ text = "cd", style = { fg = "dim" } },
		}})
		r.flex = 1
		local col = tk.vbox({})
		col:add(r)
		APP = tk.app({ root = col, w = 20, h = 1 })
	`)
	withToken(h.rt, func() {
		c := h.rt.ui.comp
		c.composite()
		row := gridRow(c.back, 0)
		if row != "                abcd" {
			t.Fatalf("richtext align=right: %q", row)
		}
	})
}

// TestToolkitSpinner: el spinner arranca PARADO; start() lo pone en marcha
// (idempotente) y stop() lo detiene sin fugar el timer. Comprobamos el ciclo de
// vida del timer interno (no la animación visual, que necesita ticks reales).
func TestToolkitSpinner(t *testing.T) {
	h := bootToolkit(t, 20, 1)
	h.expectEval(`
		local tk = require("toolkit")
		local s = tk.spinner({ label = "wait" })
		local before = s._timer == nil          -- nace parado
		s:start(); s:start()                     -- idempotente
		local running = s._timer ~= nil
		s:stop()
		local stopped = s._timer == nil
		return tostring(before and running and stopped)
	`, "true")
}
