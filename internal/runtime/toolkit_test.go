package runtime

// Tests de la extensión oficial `toolkit` (S42, embebida en
// internal/runtime/embedded/toolkit). Es el **toolkit de widgets** de
// arquitectura.md §kernel/nota ui: Lua puro sobre la API pública congelada (Fase
// 8, ADR-003 / ADR-012 —el core NO sabe lo que es un widget—), construido sobre
// `enu.ui` (§9, S29/S30/S31) y `enu.text` (§10).
//
// Blindan el criterio de hecho de S42 ("Un layout con focus entre dos widgets
// compone sin colisión entre plugins") y el alcance del enunciado:
//
//   - ÁRBOL + DIRTY TRACKING: construir el árbol; cambiar UN widget recompone solo
//     ese nodo (los hermanos reusan su Block cacheado) — verificable contando
//     composiciones.
//   - LAYOUT con FOCUS entre DOS widgets: un vbox con dos inputs; el foco se mueve
//     y el input enrutado recibe las teclas (el otro no); compone a Blocks
//     inspeccionables (alturas/anchuras y, vía el compositor, contenido).
//   - THEME (G22): un nombre semántico ("accent") se resuelve a un literal en el
//     Block/Style; un nombre desconocido es EINVAL accionable.
//   - SIN COLISIÓN entre dos "plugins"/árboles: dos apps independientes, cada una
//     su región y su árbol, componen y enrutan lo suyo sin pisarse.
//
// La UI es headless en los tests (sin TTY, G20), así que el arnés fuerza `enu.ui`
// con `WithForceUI(true)` (como S29-S33). El Block es opaco a Lua (solo
// `.width`/`.height`, block.go): la inspección de CONTENIDO se hace en Go mirando
// la rejilla del compositor (igual que compositor_test.go), y la lógica del
// toolkit (árbol/dirty/focus) se inspecciona desde Lua sobre sus propias tablas.

import (
	"strconv"
	"strings"
	"testing"
)

// bootToolkit arranca un Runtime con la extensión `toolkit` activada por enu.toml,
// con `enu.ui` forzada (headless, G20) y un tamaño de pantalla conocido para que el
// layout sea determinista. Devuelve el harness ya con Boot hecho.
func bootToolkit(t *testing.T, w, h int) *harness {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"toolkit\"]\n")
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(true), WithUISize(w, h))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}
}

// TestToolkitCargaYActiva: la extensión carga (source="builtin") y expone su API
// pública (app, contenedores, hojas, theme).
func TestToolkitCargaYActiva(t *testing.T) {
	h := bootToolkit(t, 40, 10)
	if src := listSource(h, "toolkit"); src != "builtin" {
		t.Fatalf(`toolkit debía cargarse con source="builtin"; got %q`, src)
	}
	h.expectEval(`
		local tk = require("toolkit")
		assert(type(tk.app) == "function", "app")
		assert(type(tk.vbox) == "function", "vbox")
		assert(type(tk.hbox) == "function", "hbox")
		assert(type(tk.stack) == "function", "stack")
		assert(type(tk.label) == "function", "label")
		assert(type(tk.text) == "function", "text")
		assert(type(tk.input) == "function", "input")
		assert(type(tk.theme) == "table" and type(tk.theme.new) == "function", "theme")
		assert(type(tk.widget) == "table" and type(tk.widget.new) == "function", "widget")
		return "ok"`, "ok")
}

// TestToolkitThemeSemanticoALiteral (G22): el theme resuelve un nombre semántico
// a un literal "#rrggbb"; el literal pasa por el core SIN error (un nombre
// semántico NO pasaría: lo blinda el subtest de error). Es el corazón de G22: el
// core solo entiende literales y el toolkit los produce.
func TestToolkitThemeSemanticoALiteral(t *testing.T) {
	h := bootToolkit(t, 40, 10)

	// El default mapea "accent" a un hex; theme:color lo resuelve.
	h.expectEval(`
		local tk = require("toolkit")
		local th = tk.theme.default
		local lit = th:color("accent")
		assert(lit == "#e0875f", "accent debía resolver a #e0875f, fue "..tostring(lit))
		-- es un literal que el core acepta:
		assert(tk.theme.is_literal_color(lit), "el resuelto debe ser literal")
		-- un Style con nombre semántico se convierte a uno con fg LITERAL:
		local st = th:style({ fg = "accent", bold = true })
		assert(st.fg == "#e0875f", "style.fg debía ser literal")
		assert(st.bold == true, "atributo bold conservado")
		-- ese Style YA es aceptable por el core (enu.ui.block no lanza con literales):
		local blk = enu.ui.block({ { { text = "hola", style = st } } })
		assert(blk.height == 1, "el Block se construyó")
		return "ok"`, "ok")

	// Un nombre desconocido es EINVAL accionable (un theme incompleto se nota).
	if se := h.evalErr(`return require("toolkit").theme.default:color("no-existe")`); se.Code != CodeEINVAL {
		t.Fatalf("color desconocido: code=%q, want EINVAL", se.Code)
	}

	// El core rechaza un nombre semántico crudo (la otra cara de G22): si el
	// toolkit NO resolviera, esto es lo que pasaría. Lo comprobamos para anclar que
	// la resolución es necesaria.
	if se := h.evalErr(`return enu.ui.block({ { { text = "x", style = { fg = "accent" } } } })`); se.Code != CodeEINVAL {
		t.Fatalf("enu.ui.block con nombre semántico debía ser EINVAL (G22), fue %q", se.Code)
	}

	// Un theme nuevo debe RESOLVER a literales: definirlo con un valor no-literal
	// (otro nombre) es EINVAL al construir (se ancla al theme, no estalla luego).
	if se := h.evalErr(`return require("toolkit").theme.new({ colors = { accent = "rojo" } })`); se.Code != CodeEINVAL {
		t.Fatalf("theme.new con color no-literal debía ser EINVAL, fue %q", se.Code)
	}
}

// TestToolkitArbolYDirty: construir el árbol y comprobar el DIRTY TRACKING. Se
// instala un contador Go que cuenta cada vez que un widget COMPONE su Block
// (recompose), inyectado en el render de los widgets vía un wrapper. Cambiar UN
// widget debe recomponer SOLO ese; el hermano reusa su caché.
func TestToolkitArbolYDirty(t *testing.T) {
	h := bootToolkit(t, 40, 10)

	// Contador de composiciones por etiqueta acumulado EN LUA (dual gopher/wasm): en vez
	// de un callback Go, un global `COMPOSES` que el wrapper de `compose` incrementa y que
	// el test lee con `h.eval` (idioma wasm-only del arnés).
	composes := func(tag string) int {
		s := strings.TrimSpace(h.eval(`return tostring(COMPOSES["` + tag + `"] or 0)`)[0])
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("contador de composiciones %q no numérico: %q (%v)", tag, s, err)
		}
		return n
	}

	// Montamos un vbox con dos labels. Envolvemos su `compose` para que avise al
	// contador Lua (sin tocar el toolkit: lo hacemos desde el test, decorando el
	// método de la instancia). Tras el primer paint, ambos se compusieron una vez.
	h.eval(`
		COMPOSES = {}
		local function compose_mark(tag) COMPOSES[tag] = (COMPOSES[tag] or 0) + 1 end
		local tk = require("toolkit")
		local root = tk.vbox{}
		A = tk.label{ text = "A" }
		B = tk.label{ text = "B" }
		-- decorar compose para contar (el wrapper llama al original):
		local function instrument(wdg, tag)
			local orig = wdg.compose
			wdg.compose = function(self, w, h)
				compose_mark(tag)
				return orig(self, w, h)
			end
		end
		instrument(A, "A")
		instrument(B, "B")
		root:add(A)
		root:add(B)
		APP = tk.app{ root = root, w = 40, h = 10 }
	`)

	// Primer pintado: cada label compuso al menos una vez.
	if composes("A") == 0 || composes("B") == 0 {
		t.Fatalf("tras montar, ambos labels debían componerse: A=%d B=%d", composes("A"), composes("B"))
	}
	a0, b0 := composes("A"), composes("B")

	// Cambiar SOLO A: recompone A, NO B (B reusa su caché).
	h.eval(`A:set_text("A2"); APP:paint()`)
	if composes("A") <= a0 {
		t.Fatalf("cambiar A debía recomponer A: antes=%d ahora=%d", a0, composes("A"))
	}
	if composes("B") != b0 {
		t.Fatalf("cambiar A NO debía recomponer B (dirty tracking): B antes=%d ahora=%d", b0, composes("B"))
	}

	// Un paint sin cambios no recompone NADA (todo el árbol está limpio).
	a1, b1 := composes("A"), composes("B")
	h.eval(`APP:paint()`)
	if composes("A") != a1 || composes("B") != b1 {
		t.Fatalf("paint sin cambios no debía recomponer nada: A %d->%d, B %d->%d",
			a1, composes("A"), b1, composes("B"))
	}
}

// TestToolkitLayoutFocusDosWidgets (CRITERIO DE HECHO de S42): un layout (vbox)
// con DOS widgets focusables (inputs) y FOCUS entre ellos. El foco se mueve y el
// input enrutado recibe las teclas; el otro NO. Compone a Blocks (inspeccionable
// vía el compositor). Verifica el enrutado del input al widget enfocado (api.md
// §9.3) y el movimiento de foco (focus_next).
func TestToolkitLayoutFocusDosWidgets(t *testing.T) {
	h := bootToolkit(t, 30, 6)

	h.eval(`
		local tk = require("toolkit")
		local root = tk.vbox{}
		TOP = tk.input{ id = "top" }
		BOT = tk.input{ id = "bot" }
		root:add(TOP)  -- ocupa la mitad de arriba
		root:add(BOT)  -- la mitad de abajo
		-- ambos flexibles a partes iguales para que ocupen alto:
		TOP.flex = 1
		BOT.flex = 1
		APP = tk.app{ root = root, w = 30, h = 6 }
	`)

	// El layout dio área a ambos (cada uno ~3 filas de alto, ancho 30).
	h.expectEval(`return tostring(TOP.w), tostring(TOP.h)`, "30", "3")
	h.expectEval(`return tostring(BOT.w), tostring(BOT.h)`, "30", "3")

	// El foco arranca en el primer focusable (TOP).
	h.expectEval(`return tostring(APP.focused == TOP)`, "true")

	// Enrutamos teclas a la app por su handler (lo que hace el on_input del core,
	// api.md §9.3). Escribimos "hi" en TOP.
	h.eval(`
		APP:handle_key({ type = "key", key = "h" })
		APP:handle_key({ type = "key", key = "i" })
	`)
	h.expectEval(`return TOP:value()`, "hi")
	h.expectEval(`return BOT:value()`, "")

	// Movemos el foco al siguiente widget (BOT) con la navegación por defecto (tab),
	// y escribimos: ahora recibe BOT, no TOP.
	h.eval(`APP:handle_key({ type = "key", key = "tab" })`)
	h.expectEval(`return tostring(APP.focused == BOT)`, "true")
	h.eval(`
		APP:handle_key({ type = "key", key = "y" })
		APP:handle_key({ type = "key", key = "o" })
	`)
	h.expectEval(`return BOT:value()`, "yo")
	h.expectEval(`return TOP:value()`, "hi") // TOP intacto

	// focus_prev vuelve a TOP (envuelve correctamente).
	h.eval(`APP:focus_prev()`)
	h.expectEval(`return tostring(APP.focused == TOP)`, "true")

	// El árbol COMPUSO a Blocks: lo verificamos inspeccionando la rejilla del
	// compositor (el contenido real pintado). Forzamos un paint y miramos que la
	// región tiene el texto de los inputs. El input enfocado pinta con un caret '|'.
	h.eval(`APP:paint()`)
	// Composición del compositor: la fila 0 (TOP, enfocado) debe contener "hi" y un
	// caret; la fila 3 (BOT) debe contener "yo".
	withToken(h.rt, func() {
		row0 := composeRow(h.rt.ui.comp, 0)
		if !containsStr(row0, "hi") {
			t.Fatalf("la fila 0 debía mostrar el texto de TOP (\"hi\"...), fue %q", row0)
		}
		row3 := composeRow(h.rt.ui.comp, 3)
		if !containsStr(row3, "yo") {
			t.Fatalf("la fila 3 debía mostrar el texto de BOT (\"yo\"), fue %q", row3)
		}
	})
}

// TestToolkitSinColisionEntreArboles (criterio de hecho, "sin colisión entre
// plugins"): DOS apps independientes (simulando dos plugins), cada una su región
// y su árbol. Cada una compone lo suyo en su zona y enruta su input a su foco; no
// se pisan. Verificamos por el compositor (zonas disjuntas) y por el enrutado
// (cada app a su input).
func TestToolkitSinColisionEntreArboles(t *testing.T) {
	h := bootToolkit(t, 40, 6)

	h.eval(`
		local tk = require("toolkit")
		-- "plugin 1": una app en la mitad superior (filas 0..2).
		R1 = tk.vbox{}
		I1 = tk.input{ id = "i1" }
		I1.flex = 1
		R1:add(I1)
		APP1 = tk.app{ root = R1, x = 0, y = 0, w = 40, h = 3 }
		-- "plugin 2": una app en la mitad inferior (filas 3..5).
		R2 = tk.vbox{}
		I2 = tk.input{ id = "i2" }
		I2.flex = 1
		R2:add(I2)
		APP2 = tk.app{ root = R2, x = 0, y = 3, w = 40, h = 3 }
	`)

	// Cada app tiene su propio foco, independiente.
	h.expectEval(`return tostring(APP1.focused == I1)`, "true")
	h.expectEval(`return tostring(APP2.focused == I2)`, "true")

	// Enrutar a APP1 escribe en I1; a APP2 escribe en I2. No se cruzan.
	h.eval(`
		APP1:handle_key({ type = "key", key = "a" })
		APP1:handle_key({ type = "key", key = "1" })
		APP2:handle_key({ type = "key", key = "b" })
		APP2:handle_key({ type = "key", key = "2" })
	`)
	h.expectEval(`return I1:value()`, "a1")
	h.expectEval(`return I2:value()`, "b2")

	// Composición: cada app pinta en SU zona. APP1 en la fila 0, APP2 en la fila 3,
	// sin solaparse (cada región es disjunta).
	h.eval(`APP1:paint(); APP2:paint()`)
	withToken(h.rt, func() {
		row0 := composeRow(h.rt.ui.comp, 0)
		row3 := composeRow(h.rt.ui.comp, 3)
		if !containsStr(row0, "a1") {
			t.Fatalf("APP1 (fila 0) debía mostrar \"a1\", fue %q", row0)
		}
		if !containsStr(row3, "b2") {
			t.Fatalf("APP2 (fila 3) debía mostrar \"b2\", fue %q", row3)
		}
		// La zona de APP1 NO contiene lo de APP2 y viceversa (sin colisión).
		if containsStr(row0, "b2") {
			t.Fatalf("la zona de APP1 no debía contener lo de APP2: %q", row0)
		}
		if containsStr(row3, "a1") {
			t.Fatalf("la zona de APP2 no debía contener lo de APP1: %q", row3)
		}
	})
}

// TestToolkitInputDejaPasarNoConsumido: el contrato de la pila de input (api.md
// §9.3): lo que el widget enfocado NO consume, la app lo deja pasar (devuelve
// false), para que un keymap/handler de abajo lo recoja. El input de una línea no
// consume "enter" (lo gestiona la app: enviar): handle_key devuelve false.
func TestToolkitInputDejaPasarNoConsumido(t *testing.T) {
	h := bootToolkit(t, 20, 4)
	h.eval(`
		local tk = require("toolkit")
		local root = tk.vbox{}
		IN = tk.input{}
		IN.flex = 1
		root:add(IN)
		APP = tk.app{ root = root, w = 20, h = 4, manage_input = false }
	`)
	// "a" se consume (es texto editable).
	h.expectEval(`return tostring(APP:handle_key({ type = "key", key = "a" }))`, "true")
	// "enter" NO lo consume el editor de una línea: la app lo deja pasar.
	h.expectEval(`return tostring(APP:handle_key({ type = "key", key = "enter" }))`, "false")
	h.expectEval(`return IN:value()`, "a")
}

// TestToolkitVboxLayoutReparte: el reparto del vbox (slots) con tamaños fijos y
// flexibles. Un hijo con alto fijo se respeta; el flexible se queda el sobrante.
// Es la lógica de layout (nuestra, no del core): merece verificación directa.
func TestToolkitVboxLayoutReparte(t *testing.T) {
	h := bootToolkit(t, 20, 10)
	h.eval(`
		local tk = require("toolkit")
		local root = tk.vbox{}
		HEAD = tk.label{ text = "head" }   -- alto fijo 1
		HEAD.pref_h = 1
		BODY = tk.text{ text = "body" }     -- flexible: se queda el resto
		BODY.flex = 1
		FOOT = tk.label{ text = "foot" }   -- alto fijo 2
		FOOT.pref_h = 2
		root:add(HEAD); root:add(BODY); root:add(FOOT)
		APP = tk.app{ root = root, w = 20, h = 10 }
	`)
	// HEAD=1, FOOT=2 fijos; BODY se queda 10-1-2 = 7. Todos ancho 20.
	h.expectEval(`return tostring(HEAD.h)`, "1")
	h.expectEval(`return tostring(FOOT.h)`, "2")
	h.expectEval(`return tostring(BODY.h)`, "7")
	h.expectEval(`return tostring(HEAD.y), tostring(BODY.y), tostring(FOOT.y)`, "0", "1", "8")
	h.expectEval(`return tostring(BODY.w)`, "20")
}

// Un hijo añadido a un contenedor DESPUÉS del primer layout (con el árbol ya pintado
// y "limpio") recibe geometría real tras `app:relayout()`. Blinda el bug por el que
// un `App:relayout()` se fiaba del flag `dirty` del contenedor —que el paint SÍNCRONO
// disparado por `add()` ya había borrado—, dejando al hijo nuevo en 0×0: invisible y,
// si era un modal/picker focusable, atrapando el foco con el chat colgado. Es
// exactamente la capa modal del chat: un `stack` con un `vbox` centrado vacío al que
// se le añade el panel al vuelo.
func TestToolkitRelayoutAfterDynamicAdd(t *testing.T) {
	h := bootToolkit(t, 80, 24)
	h.eval(`
		local tk = require("toolkit")
		local root = tk.stack{}
		root:add(tk.label{ text = "fondo" })          -- la "columna" de fondo
		LAYER = tk.vbox{ justify = "center", align = "center" }  -- capa modal, vacía
		root:add(LAYER)
		APP = tk.app{ root = root, w = 80, h = 24 }    -- primer layout + paint: árbol limpio
		-- al vuelo, como open_modal: panel con tamaño preferido, añadido a la capa ya limpia.
		PANEL = tk.box{ id = "panel", child = tk.label{ text = "modal" }, border = "rounded" }
		PANEL.pref_w = 40
		PANEL.pref_h = 6
		LAYER:add(PANEL)
		APP:relayout()
	`)
	// El panel recibe su tamaño preferido (40×6) y queda CENTRADO en 80×24:
	// x = (80-40)/2 = 20, y = (24-6)/2 = 9. Sin el fix se quedaba en 0×0.
	h.expectEval(`return tostring(PANEL.w), tostring(PANEL.h)`, "40", "6")
	h.expectEval(`return tostring(PANEL.x), tostring(PANEL.y)`, "20", "9")
}

// TestToolkitTextScrollViewport: un `text` desplazable (scroll>0) bajo otro
// widget pinta su contenido recortado a SU banda, sin sangrar sobre el de arriba
// (api.md §9.1: una región por viewport). Verifica el viewport del scroll: con
// scroll=2 la primera línea visible del text es su 3ª línea, y el label de arriba
// sigue intacto.
func TestToolkitTextScrollViewport(t *testing.T) {
	h := bootToolkit(t, 12, 4)
	h.eval(`
		local tk = require("toolkit")
		local root = tk.vbox{}
		HEAD = tk.label{ text = "HEAD" }   -- fila 0
		HEAD.pref_h = 1
		DOC = tk.text{ text = "L0\nL1\nL2\nL3\nL4" }  -- filas 1..3 (alto 3)
		DOC.flex = 1
		root:add(HEAD); root:add(DOC)
		APP = tk.app{ root = root, w = 12, h = 4 }
		DOC:scroll_to(2)   -- primera línea visible = L2
		APP:paint()
	`)
	// DOC ocupa filas 1..3 (alto 3). Con scroll=2 muestra L2,L3,L4.
	h.expectEval(`return tostring(DOC.y), tostring(DOC.h)`, "1", "3")
	withToken(h.rt, func() {
		row0 := composeRow(h.rt.ui.comp, 0)
		if !containsStr(row0, "HEAD") {
			t.Fatalf("fila 0 debía mantener HEAD intacto (sin sangrado del scroll), fue %q", row0)
		}
		// fila 1 = primera del viewport = L2 (no L0): el scroll recorta limpio.
		row1 := composeRow(h.rt.ui.comp, 1)
		if !containsStr(row1, "L2") {
			t.Fatalf("fila 1 (viewport con scroll=2) debía mostrar L2, fue %q", row1)
		}
		// L0/L1 no deben aparecer en ninguna fila visible (quedaron por encima del
		// viewport).
		for y := 0; y < 4; y++ {
			r := composeRow(h.rt.ui.comp, y)
			if containsStr(r, "L0") || containsStr(r, "L1") {
				t.Fatalf("L0/L1 no debían verse con scroll=2; fila %d = %q", y, r)
			}
		}
	})
}

// TestToolkitTextDesbordeSinScroll: un `text` MÁS ALTO que su banda, con scroll==0,
// encima de otro widget (un label) NO debe derramar sus filas de más sobre el de
// abajo. El `text` compone su Block COMPLETO (puede exceder su banda `h`,
// widgets.lua); el recorte del core es por REGIÓN, no por banda, así que blittear
// directo sobre la región COMPARTIDA de la app sangraría sobre el widget inferior.
// La app debe recortarlo a su banda (región-viewport dedicada), igual que para el
// scroll. Es la otra cara de TestToolkitTextScrollViewport (allí el riesgo es
// sangrar hacia ARRIBA con scroll; aquí, hacia ABAJO con desborde sin scroll).
func TestToolkitTextDesbordeSinScroll(t *testing.T) {
	h := bootToolkit(t, 12, 4)
	h.eval(`
		local tk = require("toolkit")
		local root = tk.vbox{}
		-- DOC arriba (flexible: se queda la banda 0..2, alto 3) con 6 líneas: DESBORDA
		-- su banda en 3 filas, SIN scroll (scroll==0).
		DOC = tk.text{ text = "D0\nD1\nD2\nD3\nD4\nD5" }
		DOC.flex = 1
		-- FOOT abajo (fila 3): un label de alto fijo 1. Es el widget que NO debe pisarse.
		FOOT = tk.label{ text = "FOOT" }
		FOOT.pref_h = 1
		root:add(DOC); root:add(FOOT)
		APP = tk.app{ root = root, w = 12, h = 4 }
		APP:paint()
	`)
	// DOC ocupa filas 0..2 (alto 3); FOOT la fila 3. DOC tiene 6 líneas (desborda).
	h.expectEval(`return tostring(DOC.y), tostring(DOC.h)`, "0", "3")
	h.expectEval(`return tostring(FOOT.y), tostring(FOOT.h)`, "3", "1")
	withToken(h.rt, func() {
		// La fila 3 (FOOT) debe seguir mostrando "FOOT": el desborde de DOC (D3/D4/D5)
		// NO la sobrescribe (recorte a banda). Es lo que se rompía sin el arreglo.
		row3 := composeRow(h.rt.ui.comp, 3)
		if !containsStr(row3, "FOOT") {
			t.Fatalf("la fila 3 debía conservar FOOT (sin sangrado del desborde de DOC), fue %q", row3)
		}
		// Las filas de DOC que caben en su banda SÍ se ven (la 1ª es D0, scroll==0).
		row0 := composeRow(h.rt.ui.comp, 0)
		if !containsStr(row0, "D0") {
			t.Fatalf("la fila 0 debía mostrar la 1ª línea de DOC (D0), fue %q", row0)
		}
		// Las filas que DESBORDAN (D3/D4/D5) no aparecen en NINGUNA fila visible: se
		// recortaron a la banda, no derramaron sobre FOOT ni más allá.
		for y := 0; y < 4; y++ {
			r := composeRow(h.rt.ui.comp, y)
			if containsStr(r, "D3") || containsStr(r, "D4") || containsStr(r, "D5") {
				t.Fatalf("las líneas de desborde (D3/D4/D5) no debían verse; fila %d = %q", y, r)
			}
		}
	})
}

// TestToolkitFocusEventNamespace blinda la corrección de la colisión de evento: el
// toolkit emite `toolkit:focus` (namespace del PLUGIN) al cambiar el foco de
// widget, NO `ui:focus` —reservado al core (api.md §4), que lo emite con OTRA
// semántica (el foco del TERMINAL, payload `{focused}`)—. Comprueba que un cambio
// de foco dispara `toolkit:focus {app,widget}` y NO toca el `ui:focus` del core.
func TestToolkitFocusEventNamespace(t *testing.T) {
	h := bootToolkit(t, 20, 6)
	h.eval(`
		local tk = require("toolkit")
		tk_focus_count, tk_focus_widget = 0, nil
		ui_focus_count = 0
		-- Suscriptor del evento del toolkit (lo que un plugin haría para reaccionar al
		-- foco de widget) y del evento del core (para probar que NO se dispara aquí).
		enu.events.on("toolkit:focus", function(ev)
			tk_focus_count = tk_focus_count + 1
			tk_focus_widget = ev.widget
		end)
		enu.events.on("ui:focus", function(ev) ui_focus_count = ui_focus_count + 1 end)

		local root = tk.vbox{}
		A = tk.input{ id = "a" }; A.flex = 1
		B = tk.input{ id = "b" }; B.flex = 1
		root:add(A); root:add(B)
		APP = tk.app{ root = root, w = 20, h = 6, manage_input = false }
	`)
	// Montar la app enfoca el primer focusable (A): un primer toolkit:focus a A.
	h.expectEval(`return tostring(tk_focus_count >= 1), tostring(tk_focus_widget == A)`, "true", "true")
	// Mover el foco a B emite OTRO toolkit:focus, ahora con widget == B.
	h.eval(`APP:focus_next()`)
	h.expectEval(`return tostring(APP.focused == B), tostring(tk_focus_widget == B)`, "true", "true")
	// El cambio de foco de WIDGET NO emite el `ui:focus` del core (es del terminal):
	// su contador sigue a 0. (Así no se pisa a sus suscriptores, ui_events.go.)
	h.expectEval(`return tostring(ui_focus_count)`, "0")
}

// TestToolkitAppRequiresUI: montar una app sin `enu.ui` (headless real, G20) es
// EINVAL accionable (chat.md §8: el consumidor comprueba enu.has("ui") antes). Se
// usa un runtime con WithForceUI(false): enu.ui no existe.
func TestToolkitAppRequiresUI(t *testing.T) {
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"toolkit\"]\n")
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	h := &harness{t: t, rt: rt}
	// La extensión SÍ carga (su módulo es Lua puro); montar una app es lo que falla
	// sin UI.
	h.expectEval(`return type(require("toolkit").app)`, "function")
	se := h.evalErr(`return require("toolkit").app{ w = 10, h = 5 }`)
	if se.Code != CodeEINVAL {
		t.Fatalf("app sin enu.ui: code=%q, want EINVAL", se.Code)
	}
}

// containsStr es un `strings.Contains` local (evita importar strings solo para
// esto en un fichero ya cargado de helpers).
func containsStr(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
