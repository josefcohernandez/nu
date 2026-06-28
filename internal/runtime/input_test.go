package runtime

// Tests de input de `nu.ui` §9.3 (S31, inventario 🔒). Blindan la LÓGICA PROPIA que
// el "Criterio de hecho" de la sesión nombra —"la pila enruta al handler superior;
// 'g g' resuelve con timeout; una imagen pegada llega como path, no bytes"— y los
// casos límite de la máquina de secuencias:
//
//   - **Pila de input**: el handler de ARRIBA recibe primero; un false/nil deja
//     pasar al de abajo; un true consume (los de abajo no lo ven). `pop`/`unmap`
//     quitan y el de abajo vuelve a recibir.
//   - **Secuencias con timeout**: "g g" dispara cuando llegan dos 'g' (sin timeout
//     entre medias); si pasa el timeout entre las dos 'g', NO dispara la secuencia
//     (y la primera 'g' se reinyecta como tecla suelta); una tecla que no continúa
//     la aborta; un keymap de un solo paso ("ctrl+k") dispara al instante.
//   - **Conflictos**: dos keymaps para la misma seq → el más reciente activo (el de
//     arriba) gana; `unmap` restaura el de abajo (la pila manda).
//   - **Paste de imagen (G30)**: un paste no-texto se entrega con `path` (fichero en
//     tmpdir, existe y contiene los bytes) y SIN `text`; un paste de texto llega con
//     `text`.
//   - **pcall por frontera**: un handler que lanza no rompe la pila ni tumba el
//     proceso (se trata como "no consumió").
//
// Estas pruebas son CAJA BLANCA (mismo paquete): inyectan eventos con `feedInput`
// y disparan el timeout de forma DETERMINISTA con `feedTimeout` —sin esperar al
// reloj, para que no sean flaky—. El timer real solo se ejercita en el camino vivo
// (`TestInputSequenceTimerLive`). El lado Lua de la superficie pública lo cubre el
// snippet de `ui_test.go`.

import (
	"os"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// feedKey inyecta un evento de tecla simple (sin modificadores) y devuelve si se
// consumió. Helper de los tests: presupone el token tomado.
func feedKey(in *inputState, key string) bool {
	return in.feedInput(inputEvent{typ: "key", key: key})
}

// withToken corre `fn` con el token tomado (el despacho de input es estado
// principal). Los tests inyectan eventos bajo el token, como haría el driver.
func withToken(rt *Runtime, fn func()) {
	rt.sched.acquire()
	defer rt.sched.release()
	fn()
}

// registerGoCounter instala una global Lua `mark(tag)` que incrementa un contador Go
// por tag (para que los handlers Lua registren que se dispararon, observable desde
// Go). Devuelve el mapa de contadores.
func registerGoCounter(h *harness) map[string]int {
	counts := map[string]int{}
	h.rt.L.SetGlobal("mark", h.rt.L.NewFunction(func(L *lua.LState) int {
		counts[L.CheckString(1)]++
		return 0
	}))
	return counts
}

// La pila de input: tres handlers apilados. El de arriba recibe primero; si devuelve
// false deja pasar; el que devuelve true consume y los de abajo no lo ven. Tras
// `pop` del de arriba, el siguiente vuelve a recibir.
func TestInputStackOrderAndConsume(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	// Apila tres handlers. El de abajo (A) consume todo; el de en medio (B) consume
	// solo "x"; el de arriba (C) no consume nada (deja pasar). Orden de apilado:
	// A, B, C → C arriba.
	h.eval(`
		nu.ui.on_input(function(ev) mark("A:"..ev.key); return true end)        -- abajo: consume todo
		nu.ui.on_input(function(ev) if ev.key == "x" then mark("B:x"); return true end; mark("B:miss:"..ev.key); return false end)
		nu.ui.on_input(function(ev) mark("C:"..ev.key); return false end)        -- arriba: nunca consume
	`)
	in := h.rt.ui.input

	// Tecla "y": C la ve (no consume), B la ve (no es "x", no consume), A la ve y
	// consume. Llega al fondo.
	withToken(h.rt, func() {
		if !feedKey(in, "y") {
			t.Fatal("'y' debería consumirse (A consume todo)")
		}
	})
	if counts["C:y"] != 1 || counts["B:miss:y"] != 1 || counts["A:y"] != 1 {
		t.Fatalf("'y' no recorrió la pila completa: %v", counts)
	}

	// Tecla "x": C la ve (no consume), B la consume → A NO la ve.
	withToken(h.rt, func() {
		if !feedKey(in, "x") {
			t.Fatal("'x' debería consumirse (B consume 'x')")
		}
	})
	if counts["C:x"] != 1 || counts["B:x"] != 1 {
		t.Fatalf("'x' debería verla C y consumirla B: %v", counts)
	}
	if counts["A:x"] != 0 {
		t.Fatalf("A NO debería ver 'x' (B la consumió): %v", counts)
	}
}

// pop quita el handler de arriba: el de abajo vuelve a recibir. Aquí el de arriba
// consume todo; tras pop, el de abajo (que no consumía nada) recibe.
func TestInputHandlePop(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	h.eval(`
		nu.ui.on_input(function(ev) mark("bottom:"..ev.key); return false end)
		top = nu.ui.on_input(function(ev) mark("top:"..ev.key); return true end)
	`)
	in := h.rt.ui.input

	withToken(h.rt, func() { feedKey(in, "a") })
	if counts["top:a"] != 1 || counts["bottom:a"] != 0 {
		t.Fatalf("antes de pop, top consume: %v", counts)
	}

	h.eval(`top:pop()`)
	withToken(h.rt, func() {
		if feedKey(in, "b") {
			t.Fatal("tras pop del top, nadie consume (bottom devuelve false)")
		}
	})
	if counts["bottom:b"] != 1 {
		t.Fatalf("tras pop, bottom debería recibir: %v", counts)
	}

	// pop idempotente: un segundo pop no peta.
	h.eval(`top:pop()`)
}

// Un keymap de un solo paso ("ctrl+k") dispara al instante (no hay secuencia que
// esperar). El handler crudo de abajo no ve la tecla (el keymap la consume).
func TestInputKeymapSinglePressInstant(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	h.eval(`
		nu.ui.on_input(function(ev) mark("raw:"..ev.key); return false end)
		nu.ui.keymap("ctrl+k", function() mark("ctrlk") end)
	`)
	in := h.rt.ui.input

	withToken(h.rt, func() {
		ev := inputEvent{typ: "key", key: "k", mods: modSet{ctrl: true}}
		if !in.feedInput(ev) {
			t.Fatal("ctrl+k debería consumirse (keymap de un paso)")
		}
	})
	if counts["ctrlk"] != 1 {
		t.Fatalf("ctrl+k debería disparar al instante: %v", counts)
	}
	if counts["raw:k"] != 0 {
		t.Fatalf("el handler crudo NO debería ver ctrl+k (lo consumió el keymap): %v", counts)
	}
}

// Un keymap CONSUME por defecto (disparar = atender): una fn que no devuelve nada
// (nil) se traga la tecla, y el handler crudo de abajo no la ve. Pero una fn que
// devuelve `false` EXPLÍCITO CEDE: el evento sigue bajando por la pila y lo recibe el
// handler de abajo (api.md §9.3, "azúcar sobre la pila; quien no consume deja pasar").
// Blinda el bug por el que el keymap consumía SIEMPRE ignorando el retorno —lo que
// dejaba colgado el chat: sus atajos `esc`/`enter` devuelven `false` con un modal
// abierto para que la tecla llegue al picker enfocado, y nunca llegaba—.
func TestInputKeymapDeclineFallsThrough(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	// Abajo, un handler crudo que marca y consume (simula el on_input de la app que
	// enruta al widget enfocado). Arriba, dos keymaps: "esc" CEDE (return false),
	// "enter" CONSUME (return true).
	h.eval(`
		nu.ui.on_input(function(ev) mark("raw:"..ev.key); return true end)
		nu.ui.keymap("esc", function() mark("km:esc"); return false end)
		nu.ui.keymap("enter", function() mark("km:enter"); return true end)
		nu.ui.keymap("tab", function() mark("km:tab") end)  -- sin retorno (nil): consume
	`)
	in := h.rt.ui.input

	withToken(h.rt, func() {
		// esc: el keymap dispara pero CEDE → el handler crudo de abajo SÍ la ve.
		if !feedKey(in, "esc") {
			t.Fatal("esc debería consumirse abajo (el keymap cedió, el crudo consume)")
		}
		// enter: el keymap dispara y CONSUME → el crudo NO la ve.
		if !feedKey(in, "enter") {
			t.Fatal("enter debería consumirse (el keymap la consume)")
		}
		// tab: sin retorno (nil) consume por defecto → el crudo NO la ve.
		if !feedKey(in, "tab") {
			t.Fatal("tab debería consumirse (un keymap sin retorno consume)")
		}
	})

	if counts["km:esc"] != 1 || counts["raw:esc"] != 1 {
		t.Fatalf("esc: el keymap dispara y CEDE al crudo: %v", counts)
	}
	if counts["km:enter"] != 1 || counts["raw:enter"] != 0 {
		t.Fatalf("enter: el keymap CONSUME, el crudo no la ve: %v", counts)
	}
	if counts["km:tab"] != 1 || counts["raw:tab"] != 0 {
		t.Fatalf("tab: un keymap nil CONSUME, el crudo no la ve: %v", counts)
	}
}

// Secuencia "g g": dos 'g' seguidas (sin timeout entre medias) disparan la fn. La
// primera 'g' se retiene (consume) esperando la segunda; la segunda completa.
func TestInputSequenceGGCompletes(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	h.eval(`nu.ui.keymap("g g", function() mark("gg") end)`)
	in := h.rt.ui.input

	withToken(h.rt, func() {
		if !feedKey(in, "g") {
			t.Fatal("la primera 'g' se retiene (consume) esperando la segunda")
		}
		if counts["gg"] != 0 {
			t.Fatal("una sola 'g' no debe disparar 'g g'")
		}
		if !feedKey(in, "g") {
			t.Fatal("la segunda 'g' completa la secuencia (consume)")
		}
	})
	if counts["gg"] != 1 {
		t.Fatalf("'g g' debería dispararse una vez: %v", counts)
	}
}

// Secuencia "g g" con TIMEOUT entre las dos 'g': la secuencia NO dispara, y la
// primera 'g' se reinyecta como tecla suelta al handler de abajo ("se resuelve lo
// que haya o pasa el input"). Determinista: el timeout se dispara con `feedTimeout`.
func TestInputSequenceGGTimeoutAborts(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	h.eval(`
		nu.ui.on_input(function(ev) mark("raw:"..ev.key); return false end)
		nu.ui.keymap("g g", function() mark("gg") end)
	`)
	in := h.rt.ui.input

	withToken(h.rt, func() {
		feedKey(in, "g") // primera 'g': retenida, secuencia pendiente
		in.feedTimeout() // pasa el timeout antes de la segunda 'g'
	})
	if counts["gg"] != 0 {
		t.Fatalf("con timeout entre las 'g', 'g g' NO debe disparar: %v", counts)
	}
	if counts["raw:g"] != 1 {
		t.Fatalf("la primera 'g' debe reinyectarse al handler crudo de abajo: %v", counts)
	}

	// Y una 'g' posterior arranca una secuencia nueva (el estado quedó limpio).
	withToken(h.rt, func() {
		feedKey(in, "g")
		feedKey(in, "g")
	})
	if counts["gg"] != 1 {
		t.Fatalf("tras el timeout, una 'g g' nueva debería disparar: %v", counts)
	}
}

// Una tecla que NO continúa la secuencia la aborta: "g" seguida de "x" no completa
// "g g"; la "g" se reinyecta y la "x" se procesa normal.
func TestInputSequenceAbortedByNonContinuation(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	h.eval(`
		nu.ui.on_input(function(ev) mark("raw:"..ev.key); return false end)
		nu.ui.keymap("g g", function() mark("gg") end)
	`)
	in := h.rt.ui.input

	withToken(h.rt, func() {
		feedKey(in, "g") // pendiente
		feedKey(in, "x") // no continúa "g g": aborta
	})
	if counts["gg"] != 0 {
		t.Fatalf("'g' + 'x' no debe disparar 'g g': %v", counts)
	}
	// La 'g' bufferizada se reinyecta y la 'x' se procesa: el handler crudo ve ambas.
	if counts["raw:g"] != 1 || counts["raw:x"] != 1 {
		t.Fatalf("'g' (reinyectada) y 'x' (normal) deben llegar al handler crudo: %v", counts)
	}
}

// Conflicto: dos keymaps para la misma seq ("x"). El más reciente activo (el de
// arriba) gana. Tras `unmap` del de arriba, el de abajo recupera.
func TestInputKeymapConflictStackWins(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	h.eval(`
		nu.ui.keymap("x", function() mark("low") end)        -- abajo
		topkm = nu.ui.keymap("x", function() mark("high") end) -- arriba (más reciente)
	`)
	in := h.rt.ui.input

	withToken(h.rt, func() { feedKey(in, "x") })
	if counts["high"] != 1 || counts["low"] != 0 {
		t.Fatalf("el keymap de arriba (más reciente) debe ganar: %v", counts)
	}

	h.eval(`topkm:unmap()`)
	withToken(h.rt, func() { feedKey(in, "x") })
	if counts["low"] != 1 {
		t.Fatalf("tras unmap del de arriba, el de abajo recupera: %v", counts)
	}
}

// pcall por frontera: un handler que LANZA no rompe la pila ni tumba el proceso. Se
// trata como "no consumió" → el de abajo sí recibe.
func TestInputHandlerErrorIsolated(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	h.eval(`
		nu.ui.on_input(function(ev) mark("bottom:"..ev.key); return true end)
		nu.ui.on_input(function(ev) error("boom") end)   -- arriba: lanza
	`)
	in := h.rt.ui.input

	withToken(h.rt, func() {
		if !feedKey(in, "z") {
			t.Fatal("el handler de abajo debería consumir (el de arriba lanzó, dejó pasar)")
		}
	})
	if counts["bottom:z"] != 1 {
		t.Fatalf("un handler que lanza no debe romper la pila: %v", counts)
	}
}

// Paste de imagen (G30): un paste no-texto se entrega con `path` (fichero en tmpdir,
// existe y contiene los bytes) y SIN `text`. Un paste de texto llega con `text`.
func TestInputPasteImageG30(t *testing.T) {
	h := newHarness(t)

	var gotPath, gotText string
	var hadText, hadPath bool
	h.rt.L.SetGlobal("capture", h.rt.L.NewFunction(func(L *lua.LState) int {
		ev := L.CheckTable(1)
		if p := ev.RawGetString("path"); p != lua.LNil {
			gotPath = p.String()
			hadPath = true
		}
		if tx := ev.RawGetString("text"); tx != lua.LNil {
			gotText = tx.String()
			hadText = true
		}
		return 0
	}))
	h.eval(`nu.ui.on_input(function(ev) capture(ev); return true end)`)
	in := h.rt.ui.input

	// Paste de IMAGEN: bytes binarios, pasteIsText=false. Debe llegar como `path`.
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff} // bytes "binarios" (PNG-ish)
	withToken(h.rt, func() {
		in.feedInput(inputEvent{typ: "paste", pasteBytes: imgBytes, pasteIsText: false})
	})
	if !hadPath {
		t.Fatal("un paste de imagen debe entregar `path`")
	}
	if hadText {
		t.Fatalf("un paste de imagen NO debe entregar `text` (G30): text=%q", gotText)
	}
	// El fichero existe y contiene exactamente los bytes pegados.
	data, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("el fichero del paste de imagen debe existir: %v", err)
	}
	if string(data) != string(imgBytes) {
		t.Fatalf("el fichero debe contener los bytes pegados: got %v", data)
	}

	// Paste de TEXTO: debe llegar como `text`, sin `path`.
	gotPath, gotText, hadText, hadPath = "", "", false, false
	withToken(h.rt, func() {
		in.feedInput(inputEvent{typ: "paste", text: "hola", pasteIsText: true})
	})
	if !hadText || gotText != "hola" {
		t.Fatalf("un paste de texto debe entregar text=hola: hadText=%v text=%q", hadText, gotText)
	}
	if hadPath {
		t.Fatalf("un paste de texto NO debe entregar `path`: path=%q", gotPath)
	}
}

// Camino VIVO del timer real: una secuencia "g g" con un timeout corto que vence sin
// la segunda 'g'. Comprueba que la goroutine del oneShot toma el token y aborta la
// secuencia (ejercita el timer real bajo -race). No es flaky: se espera por condición
// con un deadline holgado.
func TestInputSequenceTimerLive(t *testing.T) {
	h := newHarness(t)
	counts := registerGoCounter(h)

	// timeout_ms corto (20 ms) para que el test no tarde, pero holgado para no ser
	// flaky: el aborto solo necesita que la goroutine tome el token.
	h.eval(`
		nu.ui.on_input(function(ev) mark("raw:"..ev.key); return false end)
		nu.ui.keymap("g g", function() mark("gg") end, { timeout_ms = 20 })
	`)
	in := h.rt.ui.input

	withToken(h.rt, func() { feedKey(in, "g") }) // arranca la secuencia con timer real

	// Espera a que el timer real aborte la secuencia (reinyecta la 'g' al handler
	// crudo). Polling por condición, no sleep fijo.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.rt.sched.acquire()
		done := counts["raw:g"] == 1 && in.pendingHandler == nil
		h.rt.sched.release()
		if done {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if counts["gg"] != 0 {
		t.Fatalf("el timer real no debe disparar 'g g' sin la segunda 'g': %v", counts)
	}
	if counts["raw:g"] != 1 {
		t.Fatalf("el timer real debe abortar y reinyectar la 'g': %v", counts)
	}
}

// parseSeq/parseChord: notación de teclas. Casos válidos y errores accionables.
func TestParseSeq(t *testing.T) {
	ok := []struct {
		in    string
		steps []chord
	}{
		{"g", []chord{{key: "g"}}},
		{"g g", []chord{{key: "g"}, {key: "g"}}},
		{"ctrl+k", []chord{{key: "k", mods: modSet{ctrl: true}}}},
		{"alt+enter", []chord{{key: "enter", mods: modSet{alt: true}}}},
		{"shift+tab", []chord{{key: "tab", mods: modSet{shift: true}}}},
		{"ctrl+shift+x", []chord{{key: "x", mods: modSet{ctrl: true, shift: true}}}},
		{"ctrl+x ctrl+s", []chord{{key: "x", mods: modSet{ctrl: true}}, {key: "s", mods: modSet{ctrl: true}}}},
		// El orden de los modificadores no importa.
		{"shift+ctrl+x", []chord{{key: "x", mods: modSet{ctrl: true, shift: true}}}},
	}
	for _, c := range ok {
		got, err := parseSeq(c.in)
		if err != "" {
			t.Fatalf("parseSeq(%q) error inesperado: %s", c.in, err)
		}
		if len(got) != len(c.steps) {
			t.Fatalf("parseSeq(%q): %d pasos, want %d", c.in, len(got), len(c.steps))
		}
		for i := range got {
			if !chordEqual(got[i], c.steps[i]) {
				t.Fatalf("parseSeq(%q)[%d] = %+v, want %+v", c.in, i, got[i], c.steps[i])
			}
		}
	}

	bad := []string{"", "   ", "ctrl+", "ctrl", "+x", "ctrl+shift", "foo+x"}
	for _, b := range bad {
		if _, err := parseSeq(b); err == "" {
			t.Fatalf("parseSeq(%q) debería ser error", b)
		}
	}
}
