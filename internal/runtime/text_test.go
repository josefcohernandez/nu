package runtime

import (
	"strconv"
	"testing"

	"github.com/rivo/uniseg"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

// Tests de `enu.text` (S22, inventario 🔒). El corazón es `text.width`: la anchura
// en CELDAS de terminal (graphemes, east-asian, emoji ZWJ) es la base de TODO el
// layout (wrap, truncate, blit, viewport), así que sus casos límite se blindan
// table-driven y NOMBRADOS. wrap/truncate llevan además sus propios casos de borde
// (palabra más larga que el ancho, recorte sin partir grapheme), y los snippets
// Lua ejercitan las firmas desde el lado del autor de extensiones.

// TestTextWidth blinda la lógica 🔒 de `text.width`: anchura en celdas con
// graphemes, east-asian wide y emoji (incl. secuencias ZWJ). Cada caso lleva
// nombre porque es la regresión más cara de toda la Fase 5/6.
func TestTextWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"vacío", "", 0},
		{"ascii", "hello", 5},
		{"ascii con espacios", "a b c", 5},
		{"cjk wide (chino)", "你好", 4},                   // 2 ideogramas × 2 celdas
		{"hangul wide", "한", 2},                         // 1 sílaba × 2 celdas
		{"mezcla ascii+cjk", "a你b", 4},                  // 1 + 2 + 1
		{"emoji simple", "😀", 2},                        // 1 grapheme, 2 celdas
		{"emoji ZWJ familia", "👨‍👩‍👧‍👦", 2},             // 1 grapheme (ZWJ une 4), 2 celdas
		{"é precompuesto", "é", 1},                      // U+00E9, 1 celda
		{"é combinante (base+marca)", "é", 1},          // e + combining acute = 1 grapheme, 1 celda
		{"combining suelto cuenta 0 extra", "café́", 4}, // "caf" + (e+acute) = 4 celdas
		{"texto con varios emojis", "a😀b", 4},           // 1 + 2 + 1
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := uniseg.StringWidth(c.in); got != c.want {
				t.Fatalf("width(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestTextWidthViaLua comprueba que la primitiva `enu.text.width` expone la misma
// anchura desde Lua (el camino real del autor de extensiones), incluida la
// familia ZWJ y el vacío.
func TestTextWidthViaLua(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`return enu.text.width("hello")`, "5")
	h.expectEval(`return enu.text.width("")`, "0")
	h.expectEval(`return enu.text.width("你好")`, "4")
	h.expectEval(`return enu.text.width("😀")`, "2")
	h.expectEval(`return enu.text.width("👨‍👩‍👧‍👦")`, "2")
	// "e" + combining acute (U+0301, UTF-8 0xCC 0x81) = 1 grapheme, 1 celda.
	h.expectEval("return enu.text.width(\"e\\204\\129\")", "1")
}

// TestWrapText blinda el word-wrap puro (`wrapText`): respeto de los límites de
// palabra, `.height` correcto, partición de palabras más largas que el ancho y los
// `\n` explícitos como límites duros.
func TestWrapText(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"vacío da una línea en blanco", "", 10, []string{""}},
		{"cabe en una línea", "hola mundo", 20, []string{"hola mundo"}},
		{"envuelve por palabra", "hola mundo cruel", 10, []string{"hola mundo", "cruel"}},
		{"palabra justa al ancho", "abcde fghij", 5, []string{"abcde", "fghij"}},
		{"palabra más larga que el ancho se parte", "abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"palabra larga seguida de corta", "abcdefgh xy", 4, []string{"abcd", "efgh", "xy"}},
		{"newline explícito es límite duro", "ab\ncd", 10, []string{"ab", "cd"}},
		{"línea en blanco entre párrafos", "a\n\nb", 10, []string{"a", "", "b"}},
		{"cjk se parte por celdas", "你好世界", 4, []string{"你好", "世界"}},
		// Palabra de anchura 0 (zero-width space suelto): el centinela de "línea
		// vacía" debe ser cur=="", no curW==0, o la siguiente palabra la pisa y
		// se pierde contenido (lo cazó FuzzWrapText con "\x04 0").
		{"palabra de anchura cero no se pierde", "\u200b x", 10, []string{"\u200b x"}},
		// Misma familia en splitWide: un ZWJ hu\u00e9rfano (anchura 0) dejaba curW en 0
		// y el siguiente cluster ancho se pegaba a su trozo, excediendo width con
		// 2 graphemes (lo caz\u00f3 FuzzWrapText con "\u200d\u5b610" y width=1).
		{"zwj hu\u00e9rfano no se pega al cluster ancho", "\u200d\u5b610", 1, []string{"\u200d", "\u5b61", "0"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := wrapText(c.in, c.width)
			if len(got) != len(c.want) {
				t.Fatalf("wrapText(%q,%d) = %q (%d líneas), want %q (%d)", c.in, c.width, got, len(got), c.want, len(c.want))
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("wrapText(%q,%d) línea %d = %q, want %q", c.in, c.width, i, got[i], c.want[i])
				}
			}
			// Invariante 🔒: ninguna línea excede `width` celdas — salvo la excepción
			// documentada de splitWide: un grapheme AISLADO más ancho que width
			// (partirlo es imposible sin romperlo).
			for i, ln := range got {
				if w := uniseg.StringWidth(ln); w > c.width && uniseg.GraphemeClusterCount(ln) > 1 {
					t.Fatalf("wrapText(%q,%d) línea %d %q tiene %d celdas > width", c.in, c.width, i, ln, w)
				}
			}
		})
	}
}

// TestWrapProducesBlock comprueba el criterio de hecho del plan: `wrap` produce un
// Block con `.width <= width` y `.height` correcto, inspeccionable desde Lua.
func TestWrapProducesBlock(t *testing.T) {
	h := newHarness(t)
	// "hola mundo" = 10 celdas (cabe justo), "cruel" en la siguiente → 2 líneas.
	h.expectEval(`
		local b = enu.text.wrap("hola mundo cruel", 10)
		return b.height
	`, "2")
	// Con ancho 8, "hola" + "mundo" no caben juntas → 3 líneas.
	h.expectEval(`
		local b = enu.text.wrap("hola mundo cruel", 8)
		return b.height
	`, "3")
	h.expectEval(`
		local b = enu.text.wrap("hola mundo cruel", 10)
		return tostring(b.width <= 10)
	`, "true")
	// El Block es opaco: solo .width/.height son legibles, el contenido no.
	h.expectEval(`
		local b = enu.text.wrap("hola", 10)
		return tostring(b.lines)
	`, "nil")
}

// TestWrapWidthInvalid: un `width <= 0` no tiene sentido para envolver → EINVAL.
func TestWrapWidthInvalid(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`return enu.text.wrap("hola", 0)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("width=0: code = %q, want EINVAL", se.Code)
	}
}

// TestTruncateText blinda el recorte puro (`truncateText`): corta a `width` celdas
// exactas, con elipsis opcional, **sin partir un grapheme/emoji** por la mitad.
func TestTruncateText(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		width    int
		ellipsis string
		want     string
	}{
		{"cabe entero, sin elipsis", "hola", 10, "…", "hola"},
		{"cabe justo", "hola", 4, "…", "hola"},
		{"recorta con elipsis", "hola mundo", 6, "…", "hola …"},
		{"recorta sin elipsis", "hola mundo", 6, "", "hola m"},
		{"width 0 da vacío", "hola", 0, "…", ""},
		{"elipsis ascii multi-celda", "abcdef", 5, "..", "abc.."},
		{"no parte emoji (cabría medio)", "ab😀cd", 3, "", "ab"},     // 😀 son 2 celdas; en width 3 tras "ab" no cabe
		{"emoji entero cuando cabe", "ab😀cd", 4, "", "ab😀"},         // a,b,😀 = 1+1+2 = 4
		{"no parte grapheme combinante", "caféxy", 4, "", "café"}, // "caf"+(e+acute)=4 celdas
		{"elipsis más ancha que width cae a recorte simple", "abcdef", 2, "...", "ab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncateText(c.in, c.width, c.ellipsis)
			if got != c.want {
				t.Fatalf("truncateText(%q,%d,%q) = %q, want %q", c.in, c.width, c.ellipsis, got, c.want)
			}
			// Invariante 🔒: el resultado nunca excede `width` celdas.
			if w := uniseg.StringWidth(got); w > c.width {
				t.Fatalf("truncateText(%q,%d,%q) = %q tiene %d celdas > width", c.in, c.width, c.ellipsis, got, w)
			}
		})
	}
}

// TestTruncateViaLua ejercita la firma `enu.text.truncate` desde Lua, con y sin
// `opts.ellipsis`.
func TestTruncateViaLua(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`return enu.text.truncate("hola mundo", 6, {ellipsis="…"})`, "hola …")
	h.expectEval(`return enu.text.truncate("hola mundo", 6)`, "hola m")
	h.expectEval(`return enu.text.truncate("hola", 10)`, "hola")
	se := h.evalErr(`return enu.text.truncate("hola", -1)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("width negativo: code = %q, want EINVAL", se.Code)
	}
}

// TestSplitWide blinda el caso de borde de `splitWide`: una palabra más ancha que
// el ancho se parte por grapheme sin romper clusters, incluso un emoji aislado más
// ancho que el width (patológico: ocupa su propio trozo aunque exceda).
func TestSplitWide(t *testing.T) {
	got := splitWide("😀😀😀", 2)
	if len(got) != 3 {
		t.Fatalf("splitWide(emoji×3, 2) = %q, want 3 trozos", got)
	}
	for _, p := range got {
		if uniseg.StringWidth(p) != 2 {
			t.Fatalf("trozo %q no mide 2 celdas", p)
		}
	}
	// Emoji de 2 celdas con width 1: no se puede partir, ocupa su trozo entero.
	got = splitWide("😀", 1)
	if len(got) != 1 || got[0] != "😀" {
		t.Fatalf("splitWide(emoji, 1) = %q, want [😀]", got)
	}
}

// TestTextNotSuspending confirma que las primitivas de `text` NO son ⏸: corren
// fuera de una task (en el chunk de `-e`) sin lanzar EINVAL "solo dentro de una
// task" —son CPU puro, como los codecs—.
func TestTextNotSuspending(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`return enu.text.width("abc")`, "3")            // fuera de task: OK
	h.expectEval(`return enu.text.truncate("abcdef", 3)`, "abc") // fuera de task: OK
}

// --- enu.ui.block / Style / caps (S22, §9.2) ----------------------------------

// TestUIBlockManual inspecciona un Block construido a mano: `.width` = máximo
// ancho de línea en celdas, `.height` = nº de líneas, y los spans con su estilo
// (inspeccionados en Go, ya que el Block es opaco para Lua).
func TestUIBlockManual(t *testing.T) {
	h := newHarness(t)

	// Construye un Block manual y recupera el `*block` interno para inspeccionarlo.
	b := buildBlock(t, h, `
		return enu.ui.block({
			"hola",
			{ {text="ab", style={fg="#ff0000", bold=true}}, {text="你好"} },
			"",
		})
	`)

	if b.height != 3 {
		t.Fatalf("height = %d, want 3", b.height)
	}
	// Línea 0 "hola" = 4 celdas; línea 1 "ab"+"你好" = 2+4 = 6 celdas; línea 2 "" = 0.
	// El máximo es 6.
	if b.width != 6 {
		t.Fatalf("width = %d, want 6 (máx ancho de línea en celdas)", b.width)
	}

	// Línea 0: un span sin estilo con "hola".
	if len(b.lines[0]) != 1 || b.lines[0][0].text != "hola" || b.lines[0][0].st != nil {
		t.Fatalf("línea 0 inesperada: %+v", b.lines[0])
	}
	// Línea 1: dos spans; el primero estilizado (#ff0000, bold), el segundo sin estilo.
	if len(b.lines[1]) != 2 {
		t.Fatalf("línea 1 debe tener 2 spans, tiene %d", len(b.lines[1]))
	}
	sp0 := b.lines[1][0]
	if sp0.text != "ab" || sp0.st == nil || sp0.st.fg != "#ff0000" || !sp0.st.bold {
		t.Fatalf("span 0 de línea 1 inesperado: %+v (st=%+v)", sp0, sp0.st)
	}
	if b.lines[1][1].st != nil {
		t.Fatalf("span 1 de línea 1 no debía llevar estilo")
	}
	// Línea 2: una línea en blanco conserva su hueco (un span con texto "").
	if len(b.lines[2]) != 1 || b.lines[2][0].text != "" {
		t.Fatalf("línea 2 (en blanco) inesperada: %+v", b.lines[2])
	}
}

// TestUIBlockColorIndex comprueba que un índice 0-255 (número o string numérica)
// es un color válido y se normaliza a string decimal.
func TestUIBlockColorIndex(t *testing.T) {
	h := newHarness(t)
	b := buildBlock(t, h, `
		return enu.ui.block({
			{ {text="x", style={fg=42, bg="200"}} },
		})
	`)
	st := b.lines[0][0].st
	if st == nil || st.fg != "42" || st.bg != "200" {
		t.Fatalf("color índice mal normalizado: %+v", st)
	}
}

// TestUIBlockInvalid blinda las validaciones de `enu.ui.block`/`Style`: línea de
// tipo erróneo, span sin `text`, color hex malo, nombre semántico (G22) → EINVAL.
func TestUIBlockInvalid(t *testing.T) {
	h := newHarness(t)
	cases := []string{
		`return enu.ui.block({ 42 })`,                                   // línea numérica
		`return enu.ui.block({ { {style={fg="#fff"}} } })`,              // span sin text
		`return enu.ui.block({ { {text="x", style={fg="#xyzxyz"}} } })`, // hex inválido
		`return enu.ui.block({ { {text="x", style={fg="accent"}} } })`,  // nombre semántico (G22)
		`return enu.ui.block({ { {text="x", style={fg=999}} } })`,       // índice fuera de rango
	}
	for i, code := range cases {
		se := h.evalErr(code)
		if se.Code != CodeEINVAL {
			t.Fatalf("caso %d: code = %q, want EINVAL\n%s", i, se.Code, code)
		}
	}
}

// TestUICaps comprueba la forma de `enu.ui.caps()`: las cuatro claves presentes,
// `colors` un número > 0 (default razonable en headless) y los protocolos en false
// (deny-by-default hasta la negociación de Fase 6).
func TestUICaps(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local c = enu.ui.caps()
		return tostring(c.colors > 0)
			.. "," .. tostring(c.kitty_keyboard)
			.. "," .. tostring(c.mouse)
			.. "," .. tostring(c.images)
	`, "true,false,false,false")
}

// TestNormalizeColor blinda el parseo de colores de `Style` (§9.2, G22): hex
// válido (normalizado a minúsculas), índices en rango, y rechazo de nombres
// semánticos y formatos inválidos. Ejercita `normalizeColorWasm` (vmwasm_ui.go),
// el parser del binding de UI, con los tipos que cruzan el wire.
func TestNormalizeColor(t *testing.T) {
	ok := []struct {
		in   any
		want string
	}{
		{"#FF00aa", "#ff00aa"},
		{"#000000", "#000000"},
		{int64(0), "0"},
		{int64(255), "255"},
		{"128", "128"},
		{float64(42), "42"},
	}
	for _, c := range ok {
		got, err := normalizeColorWasm(c.in)
		if err != nil || got != c.want {
			t.Fatalf("normalizeColorWasm(%v) = (%q,%v), want (%q,nil)", c.in, got, err, c.want)
		}
	}
	bad := []any{
		"accent",     // nombre semántico (G22)
		"#fff",       // hex corto
		"#gggggg",    // hex no-hex
		int64(256),   // fuera de rango
		int64(-1),    // negativo
		true,         // tipo no soportado
		float64(1.5), // índice no entero
	}
	for _, c := range bad {
		if _, err := normalizeColorWasm(c); err == nil {
			t.Fatalf("normalizeColorWasm(%v) debió fallar", c)
		}
	}
}

// buildBlock corre un snippet que devuelve un Block y recupera el `*block` Go
// interno para inspeccionarlo (el Block es opaco para Lua, pero el test corre en
// el mismo paquete). El snippet corre sobre la Instance wasm: el Block es un
// handle cuyo objeto Go —el `*block` real— se resuelve por la tabla de handles
// de la Instance (GetHandle) a partir de su `__id`.
func buildBlock(t *testing.T, h *harness, code string) *block {
	t.Helper()
	if _, luaErr, goErr := h.rt.wasm.Eval("__blk = (function()\n" + code + "\nend)()"); goErr != nil {
		t.Fatalf("el snippet de Block falló (motor): %v\n%s", goErr, code)
	} else if luaErr != "" {
		t.Fatalf("el snippet de Block falló: %s\n%s", luaErr, code)
	}
	idStr, luaErr, goErr := h.rt.wasm.Eval("return tostring(__blk.__id)")
	if goErr != nil || luaErr != "" {
		t.Fatalf("no se pudo leer __blk.__id: %v %s", goErr, luaErr)
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		t.Fatalf("el snippet no devolvió un handle de Block (__id = %q)", idStr)
	}
	typ, val, ok := h.rt.wasm.GetHandle(vmwasm.Handle(id))
	if !ok || typ != "Block" {
		t.Fatalf("el handle %d no es un Block vivo (typ=%q ok=%v)", id, typ, ok)
	}
	b, ok := val.(*block)
	if !ok {
		t.Fatalf("el objeto del handle no es un *block: %T", val)
	}
	return b
}

// sanity: asegura que el constructor newBlock calcula dimensiones coherentes
// directamente (sin pasar por Lua), por si una sesión futura construye Blocks en Go.
func TestNewBlockDimensions(t *testing.T) {
	b := newBlock([][]span{
		{{text: "你好"}},              // 4 celdas
		{{text: "ab"}, {text: "c"}}, // 3 celdas
	})
	if b.width != 4 || b.height != 2 {
		t.Fatalf("newBlock dims = (%d,%d), want (4,2)", b.width, b.height)
	}
}
