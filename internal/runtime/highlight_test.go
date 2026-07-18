package runtime

import (
	"strings"
	"testing"
)

// Tests de `enu.text.highlight` (S24). La lógica propia a blindar es doble: (1) el
// **degradado a texto plano** ante un lenguaje desconocido o vacío (un Block sin
// estilo, SIN error, texto exacto) —la red de seguridad del render de markdown en
// streaming— y (2) el **mapeo de tokens a spans** (un snippet de un lenguaje
// conocido produce varios spans con estilo distinto del texto normal, una línea
// del código por línea del Block). Un invariante transversal: el texto
// reconstruido concatenando los spans por línea reproduce el `code` original (no
// se pierde ni se añade contenido).

// blockText reconstruye el texto de un Block uniendo los spans de cada línea con
// '\n' entre líneas. Es el invariante de "no se pierde texto": debe coincidir con
// el `code` original (salvo el '\n' final, que el Block representa como estructura
// de líneas, no como texto).
func blockText(b *block) string {
	var lines []string
	for _, ln := range b.lines {
		var sb strings.Builder
		for _, sp := range ln {
			sb.WriteString(sp.text)
		}
		lines = append(lines, sb.String())
	}
	return strings.Join(lines, "\n")
}

// countStyledSpans cuenta los spans con estilo y recoge los colores de primer
// plano distintos de un Block. Es la medida de "se resaltó": un highlight real
// produce varios spans con color y al menos dos colores distintos (keyword vs
// string vs texto).
func countStyledSpans(b *block) (styled int, distinctFg map[string]bool) {
	distinctFg = map[string]bool{}
	for _, ln := range b.lines {
		for _, sp := range ln {
			if sp.st != nil {
				styled++
				if sp.st.fgSet {
					distinctFg[sp.st.fg] = true
				}
			}
		}
	}
	return styled, distinctFg
}

// TestHighlightGoProducesStyledSpans: un snippet de Go produce un Block con varios
// spans coloreados (keywords/strings/etc. con colores DISTINTOS), preservando las
// líneas (.height == nº de líneas del código).
func TestHighlightGoProducesStyledSpans(t *testing.T) {
	code := "package main\n" +
		"\n" +
		"func main() {\n" +
		"\tmsg := \"hola\"\n" +
		"\t_ = msg\n" +
		"}\n"
	b := highlightToBlock(code, "go", defaultHighlightTheme)

	// El código tiene 6 líneas (la última termina en '\n', que NO crea una línea
	// fantasma: SplitTokensIntoLines descarta el token final vacío).
	if b.height != 6 {
		t.Fatalf("height = %d, want 6 (una línea de Block por línea de código)", b.height)
	}

	styled, fgs := countStyledSpans(b)
	if styled < 3 {
		t.Fatalf("spans con estilo = %d, want >= 3 (keywords/strings/identificadores resaltados)", styled)
	}
	if len(fgs) < 2 {
		t.Fatalf("colores de primer plano distintos = %d, want >= 2 (los tokens no son todos del mismo color)", len(fgs))
	}
	// Los colores son literales #rrggbb (G22): nunca un nombre semántico.
	for fg := range fgs {
		if !strings.HasPrefix(fg, "#") || len(fg) != 7 {
			t.Fatalf("color de token %q no es un literal #rrggbb (G22)", fg)
		}
	}
	// No se pierde texto: el Block reconstruye el código (sin el '\n' final).
	if got, want := blockText(b), strings.TrimSuffix(code, "\n"); got != want {
		t.Fatalf("texto reconstruido != código\n got: %q\nwant: %q", got, want)
	}
}

// TestHighlightUnknownLangDegradesToPlain: un lenguaje desconocido (o vacío)
// degrada a texto plano —un Block SIN estilo, SIN error, con el texto EXACTO—. Es
// la lógica de seguridad a blindar.
func TestHighlightUnknownLangDegradesToPlain(t *testing.T) {
	code := "esto no es código de ningún lenguaje\nsegunda línea\n"
	for _, lang := range []string{"no-existe-lang", "", "este-lenguaje-no-existe-42"} {
		b := highlightToBlock(code, lang, defaultHighlightTheme)

		// Texto plano = ningún span con estilo.
		styled, _ := countStyledSpans(b)
		if styled != 0 {
			t.Fatalf("lang=%q: spans con estilo = %d, want 0 (texto plano)", lang, styled)
		}
		// Texto exacto conservado (incluida la línea por el '\n' final → splitLines
		// produce un segmento final vacío, que es una línea en blanco legítima).
		if got, want := blockText(b), code; got != want {
			t.Fatalf("lang=%q: texto plano != código\n got: %q\nwant: %q", lang, got, want)
		}
	}
}

// TestHighlightCommonLanguages: json, python y lua producen spans razonables
// (varios con estilo, varios colores) y conservan el texto. Cubre que el
// resaltado no es exclusivo de Go.
func TestHighlightCommonLanguages(t *testing.T) {
	cases := []struct {
		lang string
		code string
	}{
		{"json", "{\n  \"name\": \"nu\",\n  \"n\": 42,\n  \"ok\": true\n}\n"},
		{"python", "def saluda(nombre):\n    return \"hola \" + nombre\n"},
		{"lua", "local function f(x)\n  return x + 1\nend\n"},
	}
	for _, c := range cases {
		b := highlightToBlock(c.code, c.lang, defaultHighlightTheme)
		styled, fgs := countStyledSpans(b)
		if styled < 2 {
			t.Fatalf("%s: spans con estilo = %d, want >= 2", c.lang, styled)
		}
		if len(fgs) < 2 {
			t.Fatalf("%s: colores distintos = %d, want >= 2", c.lang, len(fgs))
		}
		if got, want := blockText(b), strings.TrimSuffix(c.code, "\n"); got != want {
			t.Fatalf("%s: texto reconstruido != código\n got: %q\nwant: %q", c.lang, got, want)
		}
	}
}

// TestHighlightPreservesBlankLines: una línea en blanco en medio del código
// conserva su hueco (afecta a .height) y no se pierde texto.
func TestHighlightPreservesBlankLines(t *testing.T) {
	code := "x := 1\n\ny := 2\n" // línea en blanco en medio
	b := highlightToBlock(code, "go", defaultHighlightTheme)
	if b.height != 3 {
		t.Fatalf("height = %d, want 3 (la línea en blanco cuenta)", b.height)
	}
	if got, want := blockText(b), strings.TrimSuffix(code, "\n"); got != want {
		t.Fatalf("texto != código\n got: %q\nwant: %q", got, want)
	}
}

// TestHighlightEmptyCode: código vacío produce un Block válido de height 1 (una
// línea en blanco), no un panic ni un Block de height 0.
func TestHighlightEmptyCode(t *testing.T) {
	for _, lang := range []string{"go", "", "no-existe-lang"} {
		b := highlightToBlock("", lang, defaultHighlightTheme)
		if b.height < 1 {
			t.Fatalf("lang=%q: height = %d, want >= 1", lang, b.height)
		}
	}
}

// TestHighlightUnknownThemeFallsBack: un theme desconocido no rompe (styles.Get
// cae a su propio fallback), sigue resaltando un lenguaje conocido.
func TestHighlightUnknownThemeFallsBack(t *testing.T) {
	b := highlightToBlock("func main() {}\n", "go", "este-theme-no-existe")
	styled, _ := countStyledSpans(b)
	if styled < 1 {
		t.Fatalf("con theme desconocido no se resaltó nada (debió caer al fallback de chroma)")
	}
}

// ───────────────────────────────────────────────────────────────────────────
// Vía Lua: el snippet del autor de extensiones (Definition of Done §2).
// ───────────────────────────────────────────────────────────────────────────

// TestHighlightViaLua resalta un snippet desde Lua e inspecciona el Block
// resultante (height legible, es un handle de Block opaco). Reusa buildBlock de
// text_test.go (inspecciona el `*block` interno en Go).
func TestHighlightViaLua(t *testing.T) {
	h := newHarness(t)

	// Un snippet de Go: el Block resultante tiene la altura esperada y es opaco
	// (.width/.height legibles, contenido no).
	b := buildBlock(t, h, `return enu.text.highlight("func main() {\n  x := 1\n}\n", "go")`)
	if b.height != 3 {
		t.Fatalf("highlight Lua (go): height = %d, want 3", b.height)
	}
	styled, _ := countStyledSpans(b)
	if styled < 2 {
		t.Fatalf("highlight Lua (go): spans con estilo = %d, want >= 2", styled)
	}

	// Lenguaje desconocido desde Lua → texto plano, SIN lanzar.
	bp := buildBlock(t, h, `return enu.text.highlight("texto cualquiera\notra", "no-existe-lang")`)
	if s, _ := countStyledSpans(bp); s != 0 {
		t.Fatalf("highlight Lua (desconocido): spans con estilo = %d, want 0", s)
	}
	if got := blockText(bp); got != "texto cualquiera\notra" {
		t.Fatalf("highlight Lua (desconocido): texto = %q", got)
	}

	// .height legible desde Lua (el Block es opaco pero expone dimensiones).
	got := h.eval(`local blk = enu.text.highlight("a := 1\nb := 2\n", "go"); return tostring(blk.height)`)
	if len(got) != 1 || got[0] != "2" {
		t.Fatalf("blk.height desde Lua = %v, want [2]", got)
	}

	// Sin opts y con opts.theme por nombre: ambos válidos, no lanzan.
	h.eval(`assert(enu.text.highlight("local x = 1", "lua")); return true`)
	h.eval(`assert(enu.text.highlight("local x = 1", "lua", { theme = "monokai" })); return true`)
}

// TestHighlightViaLuaErrors: los usos malos de la firma → EINVAL (lang no-string,
// opts no-tabla, opts.theme no-string). El código (arg 1) y el degradado a texto
// plano NO son errores; solo el mal uso de los tipos de la firma lo es.
func TestHighlightViaLuaErrors(t *testing.T) {
	h := newHarness(t)
	bad := []string{
		`enu.text.highlight("code", 42)`,                   // lang no-string
		`enu.text.highlight("code", "go", 7)`,              // opts no-tabla
		`enu.text.highlight("code", "go", { theme = 99 })`, // theme no-string
	}
	for _, code := range bad {
		full := `local ok, err = pcall(function() ` + code + ` end)
assert(not ok, "el uso malo debió fallar")
assert(err.code == "EINVAL", "code esperado EINVAL, got " .. tostring(err.code))
return true`
		h.eval(full)
	}

	// lang ausente (nil) NO es error: degrada a texto plano (como "" ).
	h.eval(`assert(enu.text.highlight("texto suelto")); return true`)
}
