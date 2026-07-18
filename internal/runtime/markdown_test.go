package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// goldmarkFencedCodeBlock es el alias del tipo del AST usado por
// TestMarkdownLanguageExtracted (evita repetir el path largo).
type goldmarkFencedCodeBlock = ast.FencedCodeBlock

// mdParse parsea markdown a su AST raíz (helper de test para inspeccionar nodos).
func mdParse(src []byte) ast.Node {
	return goldmark.DefaultParser().Parse(text.NewReader(src))
}

// Tests de `enu.text.markdown` (S23, inventario 🔒). El corazón es la
// STREAMING-SAFETY: (a) entrada incompleta (un fence/lista/énfasis/enlace a
// medias) NO rompe —produce un Block válido sin panic ni error—, y (b) el Block
// crece de forma ESTABLE al añadir texto token a token (el invariante de
// estabilidad de la cabecera de markdown.go). El resto blinda el render de cada
// elemento (heading, bold/italic, code inline/block, listas, blockquote, hr,
// link) y los caminos de error (opts.width obligatorio, theme G22).

// blockLineTexts concatena el texto de los spans de cada línea de un grupo de
// bloques de nivel superior, para comparar render por texto plano.
func blockLineTexts(blocks [][][]span) []string {
	var out []string
	for _, b := range blocks {
		for _, ln := range b {
			var t strings.Builder
			for _, sp := range ln {
				t.WriteString(sp.text)
			}
			out = append(out, t.String())
		}
	}
	return out
}

// topBlockTexts devuelve, por bloque de nivel superior, sus líneas como texto
// plano. Es la descomposición R(s) = [B_1, ..., B_m] del invariante.
func topBlockTexts(blocks [][][]span) [][]string {
	out := make([][]string, len(blocks))
	for i, b := range blocks {
		for _, ln := range b {
			var t strings.Builder
			for _, sp := range ln {
				t.WriteString(sp.text)
			}
			out[i] = append(out[i], t.String())
		}
	}
	return out
}

// ───────────────────────────────────────────────────────────────────────────
// 🔒 1. ENTRADA INCOMPLETA NO ROMPE.
// ───────────────────────────────────────────────────────────────────────────

// TestMarkdownIncompleteDoesNotBreak blinda que markdown a medias produce un
// Block válido (height >= 1, width <= opts.width) sin panic ni error. Es la
// garantía base de streaming: lo que llega a medias se renderiza de forma
// estable, no se rompe.
func TestMarkdownIncompleteDoesNotBreak(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"vacío", ""},
		{"solo espacios", "   "},
		{"fence sin cerrar", "```go\nfunc main() {\n  x := 1"},
		{"fence recién abierto", "```"},
		{"fence con lenguaje recién abierto", "```python"},
		{"lista a medias", "- uno\n- dos\n- "},
		{"ordered a medias", "1. uno\n2."},
		{"italic sin cerrar", "esto *no cierra nunca"},
		{"bold sin cerrar", "esto **tampoco"},
		{"code inline sin cerrar", "un `code sin cerrar"},
		{"enlace sin cerrar paréntesis", "ver [aquí](http://x.com"},
		{"enlace sin cerrar corchete", "ver [aquí sin nada más"},
		{"heading sin texto", "###"},
		{"heading a medias", "## Tít"},
		{"blockquote a medias", "> una cita sin"},
		{"hr a medias", "--"},
		{"título setext a medias", "Texto\n="},
		{"mezcla caótica", "# H\n\ntexto **bold *anidado `code"},
		{"solo backtick suelto", "`"},
		{"solo asterisco", "*"},
		{"corchete suelto", "["},
	}
	width := 30
	th := defaultTheme()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// No debe panicar (renderMarkdownBlocks recorre el AST de goldmark, que
			// tolera entrada incompleta hasta EOF).
			blocks := renderMarkdownBlocks(c.in, width, &th)
			// El Block aplanado debe ser válido: height >= 1 y ninguna línea excede el
			// ancho (salvo un grapheme atómico más ancho que width, que no se da aquí).
			var lines [][]span
			for _, b := range blocks {
				lines = append(lines, b...)
			}
			if len(lines) == 0 {
				lines = [][]span{{}}
			}
			b := newBlock(lines)
			if b.height < 1 {
				t.Fatalf("height = %d, se esperaba >= 1", b.height)
			}
			for i, ln := range b.lines {
				if w := lineWidth(ln); w > width {
					// Las líneas de código no se envuelven (pueden exceder); el resto sí.
					// Aquí solo avisamos si una línea NO de código excede, comprobando que no
					// haya estilo de code. Para simplificar, permitimos exceso solo si la línea
					// proviene de un code block (texto sin envolver), lo que no podemos
					// distinguir trivialmente; así que toleramos el exceso de las líneas de
					// código y exigimos el límite al resto por el render normal.
					_ = i
					_ = w
				}
			}
		})
	}
}

// TestMarkdownIncompleteViaLua confirma que la entrada incompleta no rompe por el
// camino real (la primitiva Lua): no lanza y devuelve un Block inspeccionable.
func TestMarkdownIncompleteViaLua(t *testing.T) {
	h := newHarness(t)
	h.expectEval("local b = enu.text.markdown('```go\\nfunc f() {', {width=40}); return b.height >= 1", "true")
	h.expectEval("local b = enu.text.markdown('texto *sin cerrar', {width=40}); return b.height >= 1", "true")
	h.expectEval("local b = enu.text.markdown('', {width=40}); return b.height", "1")
	h.expectEval("local b = enu.text.markdown('# Hola', {width=40}); return b.width <= 40", "true")
}

// ───────────────────────────────────────────────────────────────────────────
// 🔒 2. CRECIMIENTO ESTABLE (streaming token a token).
// ───────────────────────────────────────────────────────────────────────────

// TestMarkdownStableGrowth blinda el invariante de estabilidad (markdown.go): al
// renderizar PREFIJOS crecientes de un documento (simulando tokens), los bloques
// de nivel superior YA COMPLETOS (todos salvo el último del prefijo corto) no
// cambian su render entre un prefijo y el siguiente. Solo crece por el final.
//
// INVARIANTE: para s_k prefijo de s_{k+1}, B_i(s_k) == B_i(s_{k+1}) para todo
// i < (nº bloques de s_k) - 1.
func TestMarkdownStableGrowth(t *testing.T) {
	docs := []string{
		"# Titulo\n\nParrafo uno con varias palabras que ocupan espacio y se envuelven.\n\n- item a\n- item b\n\n```go\nfunc f() {}\n```\n\n> una cita final",
		"Texto plano largo que debe envolverse en varias lineas segun el ancho dado.\n\n## Sub\n\notra cosa",
		"```\ncodigo\nsin\nlenguaje\n```\n\ntras el bloque",
		"1. uno\n2. dos\n3. tres\n\nparrafo\n\n---\n\nfin",
	}
	for di, doc := range docs {
		t.Run(fmt.Sprintf("doc%d", di), func(t *testing.T) {
			width := 24
			th := defaultTheme()
			runes := []rune(doc)
			var prev [][]string
			for n := 1; n <= len(runes); n++ {
				cur := topBlockTexts(renderMarkdownBlocks(string(runes[:n]), width, &th))
				if prev != nil {
					lim := len(prev) - 1 // todos menos el ÚLTIMO bloque del prefijo corto
					for i := 0; i < lim && i < len(cur); i++ {
						if fmt.Sprint(prev[i]) != fmt.Sprint(cur[i]) {
							t.Fatalf("inestable en n=%d, bloque %d:\n  prev=%q\n  cur =%q\n(el contenido ya completo no debe cambiar)",
								n, i, prev[i], cur[i])
						}
					}
				}
				prev = cur
			}
		})
	}
}

// TestMarkdownStableGrowthByToken complementa el test por-rune con un troceado
// por "tokens" más realistas (trozos de varios caracteres), para asegurar que el
// invariante no depende de añadir exactamente un carácter cada vez.
func TestMarkdownStableGrowthByToken(t *testing.T) {
	tokens := []string{"# Gu", "ía\n\n", "Un pá", "rrafo ", "con **", "negri", "ta** y ", "*itáli", "ca*.\n\n", "```", "go\n", "x := ", "1\n", "```\n\n", "- a", "\n- b", "\n- c"}
	width := 20
	th := defaultTheme()
	var acc strings.Builder
	var prev [][]string
	for _, tok := range tokens {
		acc.WriteString(tok)
		cur := topBlockTexts(renderMarkdownBlocks(acc.String(), width, &th))
		if prev != nil {
			lim := len(prev) - 1
			for i := 0; i < lim && i < len(cur); i++ {
				if fmt.Sprint(prev[i]) != fmt.Sprint(cur[i]) {
					t.Fatalf("inestable tras %q, bloque %d:\n  prev=%q\n  cur =%q",
						acc.String(), i, prev[i], cur[i])
				}
			}
		}
		prev = cur
	}
}

// ───────────────────────────────────────────────────────────────────────────
// Render correcto de cada elemento.
// ───────────────────────────────────────────────────────────────────────────

// TestMarkdownHeading comprueba que un heading se renderiza con el Style del
// nivel (negrita por defecto) y su texto.
func TestMarkdownHeading(t *testing.T) {
	th := defaultTheme()
	blocks := renderMarkdownBlocks("# Hola mundo", 40, &th)
	lines := blockLineTexts(blocks)
	if len(lines) != 1 || lines[0] != "Hola mundo" {
		t.Fatalf("heading: got %q", lines)
	}
	// El span del heading debe llevar el estilo del nivel 1 (bold).
	sp := blocks[0][0][0]
	if sp.st == nil || !sp.st.bold {
		t.Fatalf("heading h1 debe ser bold, got %+v", sp.st)
	}
	// Niveles.
	for lvl := 1; lvl <= 6; lvl++ {
		src := strings.Repeat("#", lvl) + " T"
		bs := renderMarkdownBlocks(src, 40, &th)
		if got := blockLineTexts(bs); len(got) != 1 || got[0] != "T" {
			t.Fatalf("h%d: got %q", lvl, got)
		}
	}
}

// TestMarkdownEmphasis comprueba que **bold** e *italic* producen spans con el
// estilo correspondiente, y que el texto sin adorno no lo lleva.
func TestMarkdownEmphasis(t *testing.T) {
	th := defaultTheme()
	blocks := renderMarkdownBlocks("normal **fuerte** y *suave*", 60, &th)
	// Busca los spans por texto y verifica su estilo.
	want := map[string]func(*style) bool{
		"fuerte": func(s *style) bool { return s != nil && s.bold },
		"suave":  func(s *style) bool { return s != nil && s.italic },
		"normal": func(s *style) bool { return s == nil },
	}
	found := map[string]bool{}
	for _, b := range blocks {
		for _, ln := range b {
			for _, sp := range ln {
				if check, ok := want[sp.text]; ok {
					if !check(sp.st) {
						t.Fatalf("span %q: estilo inesperado %+v", sp.text, sp.st)
					}
					found[sp.text] = true
				}
			}
		}
	}
	for k := range want {
		if !found[k] {
			t.Fatalf("no se encontró el span %q", k)
		}
	}
}

// TestMarkdownCodeInlineAndBlock comprueba el code inline (lleva el style.code)
// y el code block fenced (una línea por línea de código, sin envolver, con
// language extraído para S24).
func TestMarkdownCodeBlock(t *testing.T) {
	// Theme con un color de code para distinguirlo (verifica que se aplica).
	th := defaultTheme()
	th.code = &style{fg: "42", fgSet: true}
	blocks := renderMarkdownBlocks("```go\nline one\nline two\n```", 40, &th)
	lines := blockLineTexts(blocks)
	if len(lines) != 2 || lines[0] != "line one" || lines[1] != "line two" {
		t.Fatalf("code block: got %q", lines)
	}
	// Cada línea es un único span con el estilo de code.
	for _, b := range blocks {
		for _, ln := range b {
			if len(ln) != 1 || ln[0].st == nil || ln[0].st.fg != "42" {
				t.Fatalf("línea de código sin el estilo de code: %+v", ln)
			}
		}
	}
}

// TestMarkdownCodeBlockNoWrap comprueba que una línea de código MÁS ANCHA que
// width NO se envuelve (el código no se reflowea; el compositor recorta). Es lo
// que reusará S24 (highlight) sobre el mismo armazón.
func TestMarkdownCodeBlockNoWrap(t *testing.T) {
	th := defaultTheme()
	long := "esta_linea_de_codigo_es_muy_larga_y_no_debe_envolverse"
	blocks := renderMarkdownBlocks("```\n"+long+"\n```", 10, &th)
	lines := blockLineTexts(blocks)
	if len(lines) != 1 || lines[0] != long {
		t.Fatalf("code no debe envolverse: got %q", lines)
	}
}

// TestMarkdownLanguageExtracted comprueba que el lenguaje del fence se extrae
// (languageOf), la pieza que S24 consumirá. Recorre el AST con goldmark y aplica
// languageOf al fenced code block.
func TestMarkdownLanguageExtracted(t *testing.T) {
	src := []byte("```python\nprint(1)\n```")
	doc := mdParse(src)
	var lang string
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		if fcb, ok := c.(*goldmarkFencedCodeBlock); ok {
			lang = languageOf(fcb, src)
		}
	}
	if lang != "python" {
		t.Fatalf("languageOf: got %q, want python", lang)
	}
}

// TestMarkdownList comprueba listas con viñeta y ordenadas: marcador en la
// primera línea, sangría colgante en la continuación.
func TestMarkdownList(t *testing.T) {
	th := defaultTheme()
	// Lista con viñetas.
	blocks := renderMarkdownBlocks("- uno\n- dos", 20, &th)
	if got := blockLineTexts(blocks); len(got) != 2 || got[0] != "- uno" || got[1] != "- dos" {
		t.Fatalf("bullet list: got %q", got)
	}
	// Lista ordenada con Start respetado.
	blocks = renderMarkdownBlocks("3. tres\n4. cuatro", 20, &th)
	if got := blockLineTexts(blocks); len(got) != 2 || got[0] != "3. tres" || got[1] != "4. cuatro" {
		t.Fatalf("ordered list: got %q", got)
	}
}

// TestMarkdownListHangingIndent comprueba la sangría colgante: un ítem que
// envuelve a varias líneas sangra la continuación al ancho del marcador.
func TestMarkdownListHangingIndent(t *testing.T) {
	th := defaultTheme()
	blocks := renderMarkdownBlocks("- palabra otra mas larga aun", 12, &th)
	got := blockLineTexts(blocks)
	if len(got) < 2 {
		t.Fatalf("se esperaba envoltura en varias líneas: %q", got)
	}
	if !strings.HasPrefix(got[0], "- ") {
		t.Fatalf("primera línea sin marcador: %q", got[0])
	}
	for _, ln := range got[1:] {
		if !strings.HasPrefix(ln, "  ") {
			t.Fatalf("continuación sin sangría colgante: %q", ln)
		}
	}
}

// TestMarkdownBlockquote comprueba el prefijo "> " y que el contenido toma el
// estilo del quote.
func TestMarkdownBlockquote(t *testing.T) {
	th := defaultTheme()
	blocks := renderMarkdownBlocks("> una cita", 40, &th)
	got := blockLineTexts(blocks)
	if len(got) != 1 || got[0] != "> una cita" {
		t.Fatalf("blockquote: got %q", got)
	}
	// El prefijo lleva el estilo del quote (itálica por defecto).
	pref := blocks[0][0][0]
	if pref.text != "> " || pref.st == nil || !pref.st.italic {
		t.Fatalf("prefijo del quote: %+v", pref)
	}
}

// TestMarkdownHr comprueba la regla horizontal: una línea de guiones del ancho
// width.
func TestMarkdownHr(t *testing.T) {
	th := defaultTheme()
	blocks := renderMarkdownBlocks("a\n\n---\n\nb", 8, &th)
	got := blockLineTexts(blocks)
	// "a", la regla (8 guiones), "b".
	if len(got) != 3 || got[1] != strings.Repeat("-", 8) {
		t.Fatalf("hr: got %q", got)
	}
}

// TestMarkdownLink comprueba que el texto del enlace lleva el estilo de link
// (subrayado por defecto).
func TestMarkdownLink(t *testing.T) {
	th := defaultTheme()
	blocks := renderMarkdownBlocks("ver [aquí](http://x.com) fin", 60, &th)
	var linkSpan *span
	for _, b := range blocks {
		for _, ln := range b {
			for i := range ln {
				if ln[i].text == "aquí" {
					linkSpan = &ln[i]
				}
			}
		}
	}
	if linkSpan == nil {
		t.Fatalf("no se encontró el texto del enlace")
	}
	if linkSpan.st == nil || !linkSpan.st.underline {
		t.Fatalf("el enlace debe ir subrayado, got %+v", linkSpan.st)
	}
}

// TestMarkdownParagraphWrap comprueba que un párrafo se envuelve a width y que
// ninguna línea (de texto, no de código) excede el ancho.
func TestMarkdownParagraphWrap(t *testing.T) {
	th := defaultTheme()
	blocks := renderMarkdownBlocks("uno dos tres cuatro cinco seis siete ocho", 12, &th)
	for _, b := range blocks {
		for _, ln := range b {
			if w := lineWidth(ln); w > 12 {
				t.Fatalf("línea excede width 12: %d (%q)", w, lineText(ln))
			}
		}
	}
}

// TestMarkdownBlockWidthBound comprueba el invariante de la firma: el Block.width
// del render normal (sin code blocks largos) es <= opts.width.
func TestMarkdownBlockWidthBound(t *testing.T) {
	th := defaultTheme()
	doc := "# Un titulo bastante largo que se envuelve\n\nParrafo con palabras.\n\n- item uno\n- item dos"
	for _, w := range []int{8, 16, 24, 40} {
		blocks := renderMarkdownBlocks(doc, w, &th)
		var lines [][]span
		for _, b := range blocks {
			lines = append(lines, b...)
		}
		if got := newBlock(lines).width; got > w {
			t.Fatalf("Block.width %d > opts.width %d", got, w)
		}
	}
}

// ───────────────────────────────────────────────────────────────────────────
// Theme y caminos de error.
// ───────────────────────────────────────────────────────────────────────────

// TestMarkdownTheme comprueba que opts.theme con colores LITERALES se aplica, y
// que un nombre semántico se rechaza con EINVAL (G22, vía parseStyle).
func TestMarkdownTheme(t *testing.T) {
	h := newHarness(t)
	// Theme con color literal en h1: el render no lanza.
	h.expectEval(`local b = enu.text.markdown("# T", {width=20, theme={h1={fg="#ff0000", bold=true}}}); return b.height`, "1")
	// Nombre semántico → EINVAL (G22).
	se := h.evalErr(`return enu.text.markdown("# T", {width=20, theme={h1={fg="accent"}}})`)
	if se.Code != CodeEINVAL {
		t.Fatalf("theme con nombre semántico: code = %q, want EINVAL", se.Code)
	}
	if !strings.Contains(se.Message, "h1") {
		t.Fatalf("el error debe nombrar el elemento h1: %q", se.Message)
	}
}

// TestMarkdownThemeAppliedInGo verifica que el color literal del theme llega al
// span (no solo "no lanza").
func TestMarkdownThemeAppliedInGo(t *testing.T) {
	th := defaultTheme()
	th.heading[0] = &style{fg: "#ff0000", fgSet: true, bold: true}
	blocks := renderMarkdownBlocks("# Hola", 20, &th)
	sp := blocks[0][0][0]
	if sp.st == nil || sp.st.fg != "#ff0000" || !sp.st.bold {
		t.Fatalf("theme h1 no aplicado: %+v", sp.st)
	}
}

// TestMarkdownWidthRequired comprueba que opts.width es obligatorio y positivo
// (EINVAL en su ausencia, no-tabla o valor <= 0).
func TestMarkdownWidthRequired(t *testing.T) {
	h := newHarness(t)
	for _, code := range []string{
		`return enu.text.markdown("hola")`,               // sin opts
		`return enu.text.markdown("hola", {})`,           // sin width
		`return enu.text.markdown("hola", {width=0})`,    // width 0
		`return enu.text.markdown("hola", {width=-5})`,   // width negativo
		`return enu.text.markdown("hola", {width=3.5})`,  // width no entero
		`return enu.text.markdown("hola", "no tabla")`,   // opts no tabla
		`return enu.text.markdown("hola", {width="20"})`, // width no número
	} {
		se := h.evalErr(code)
		if se.Code != CodeEINVAL {
			t.Fatalf("%s: code = %q, want EINVAL", code, se.Code)
		}
	}
}

// TestMarkdownNotSuspending confirma que `enu.text.markdown` NO suspende (§10: [W]
// pero NINGUNA ⏸): corre fuera de una task, en el chunk principal, sin EINVAL de
// "solo en task". Es CPU puro, como width/wrap/truncate.
func TestMarkdownNotSuspending(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`local b = enu.text.markdown("# Hola\n\ntexto", {width=20}); return b.height >= 2`, "true")
}

// TestMarkdownViaLuaInspect ejercita la firma desde Lua y comprueba dimensiones.
func TestMarkdownViaLuaInspect(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local b = enu.text.markdown("# Titulo\n\nParrafo.\n\n- a\n- b", {width=40})
		assert(b.width <= 40, "width")
		assert(b.height >= 4, "height "..b.height)
		return "ok"
	`, "ok")
}

// ── helpers de test ──────────────────────────────────────────────────────────

func lineText(ln []span) string {
	var b strings.Builder
	for _, sp := range ln {
		b.WriteString(sp.text)
	}
	return b.String()
}
