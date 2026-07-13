package runtime

import (
	"strconv"
	"strings"

	"github.com/rivo/uniseg"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// `nu.text.markdown` — render completo de markdown a un `Block` (api.md §10,
// sesión S23, inventario 🔒). Es CPU puro: parsea un string ya en memoria y
// emite líneas de spans estilizados, sin esperar IO. Por eso es **[W] pero
// NINGUNA ⏸** (como `width`/`wrap`/`truncate` de S22 y los codecs de S18): no
// usa el puente `suspend` ni `requireTask`, corre síncrona en el estado
// principal (y en workers cuando lleguen, S34).
//
// LUA DECIDE, GO EJECUTA (ADR-004). El parseo de CommonMark lo hace Go
// (`github.com/yuin/goldmark`, puro-Go, `CGO_ENABLED=0` intacto): se construye
// el AST del documento y se recorre emitiendo spans en el `Block`. El
// word-wrap reusa `wrapText`/`splitWide` de S22 (text.go) y la anchura,
// `uniseg.StringWidth` (la misma de `text.width`). El theme resuelve `Style`
// con `parseStyle`/`normalizeColor` de S22 (ui.go). Ni una función pública de
// más: solo se cuelga `nu.text.markdown`.
//
// THEME (themable, §10). `opts.theme` lleva un `Style` por cada elemento
// (headings por nivel "h1".."h6", code, emphasis, strong, link, bullet,
// blockquote, rule). Los colores son **literales** (#rrggbb o índice 0-255; los
// nombres semánticos los resuelve el toolkit, no el core, G22), validados por
// `parseStyle`. Si no se pasa theme (o falta un elemento), se usa un default
// razonable (defaultTheme): negrita en headings/strong, itálica en emphasis,
// subrayado en links — sin color, para no imponer una paleta.
//
// ───────────────────────────────────────────────────────────────────────────
// STREAMING-SAFE Y EL INVARIANTE DE ESTABILIDAD (la lógica 🔒).
// ───────────────────────────────────────────────────────────────────────────
//
// El render debe aceptar entrada INCOMPLETA sin romper (un bloque de código
// ```...sin cerrar, una lista a medias, un *énfasis sin cerrar, un [enlace
// (sin cerrar): producir un Block válido, sin panic ni error. goldmark ya es
// tolerante (parsea hasta EOF: un fence sin cerrar lo trata como code block
// hasta el final del texto, un énfasis sin cerrar se queda como texto plano),
// así que la primera mitad sale gratis: NO dependemos de que el último bloque
// esté "cerrado".
//
// La parte difícil es la ESTABILIDAD al ir AÑADIENDO texto (simular streaming
// token a token): el prefijo ya renderizado no debe "saltar"/reflowear
// caóticamente. La estrategia es **render por bloques de nivel superior,
// independientes**: cada hijo directo del documento (un párrafo, un heading, un
// code block, una lista, un blockquote, un hr) se renderiza a SU propio conjunto
// de líneas, y el Block es la concatenación en orden. La clave de la estabilidad
// es que **markdown es estable por bloques al crecer por el final**: añadir
// texto al final del documento solo puede afectar al ÚLTIMO bloque de nivel
// superior (el que está "en construcción"); los bloques anteriores ya están
// delimitados por una línea en blanco (o un cambio de tipo) y su render no
// cambia. Las excepciones de CommonMark a esto (un "Setext heading" —subrayado
// `===`/`---` que reinterpreta el párrafo previo— o una "lazy continuation" que
// extiende un párrafo sin línea en blanco) NO rompen el Block (siguen siendo
// válidos), solo relajan el invariante en ese último bloque; por eso el
// invariante se enuncia "salvo el último bloque". Renderizar por bloques
// independientes (en vez de un layout global) es lo que evita que un fence
// abierto al final reflowee los párrafos de arriba.
//
// INVARIANTE EXACTO (lo que blinda el test 🔒 de crecimiento estable):
//
//	Sea R(s) las líneas del Block de markdown(s) descompuestas por bloques de
//	nivel superior: R(s) = [B_1, B_2, ..., B_m]. Para dos prefijos s_k y s_{k+1}
//	(s_k es prefijo de s_{k+1}, un token más), los bloques de R(s_k) salvo el
//	ÚLTIMO son un prefijo exacto de R(s_{k+1}): B_i(s_k) == B_i(s_{k+1}) para
//	todo i < m_k. Es decir, el contenido ya completo (los bloques anteriores al
//	que se está escribiendo) no cambia entre prefijos sucesivos; solo crece por
//	el final (el último bloque se reescribe y/o se añaden bloques nuevos).
//
// Se hace testeable emitiendo, junto al Block, los **límites de bloque**
// (renderMarkdownBlocks devuelve [][][]span: una rebanada por bloque de nivel
// superior). El test compara bloque a bloque entre prefijos y exige igualdad de
// todos menos el último del prefijo corto.
//
// TABLAS: NO se soportan en S23 (CommonMark base; las tablas son una extensión
// GFM). goldmark sin extensiones no las parsea, así que una tabla cae a un
// párrafo de texto plano (las celdas con `|`) — válido y estable, solo sin
// formato de tabla. Documentado en docs/decisiones-implementacion.md (S23); reabrible si una
// extensión las pide (P##).
//
// QUÉ REUSARÁ S24 (highlight): el render de un code block aquí aplica UN solo
// Style (theme.code) a cada línea; S24 (`nu.text.highlight`) sustituirá ese
// tramo plano por spans coloreados por token dentro del MISMO armazón de líneas
// (renderCodeBlock es el punto de extensión: hoy un span por línea, mañana N
// spans por línea según el lexer — por eso `lang` ya se extrae y se pasa).

// markdownTheme agrupa los estilos resueltos (literales) de cada elemento de
// markdown. Todos son punteros a `style` (nil = sin estilo, hereda lo de debajo
// al pintar). Se construye una vez por llamada desde `opts.theme` (con
// defaultTheme rellenando lo ausente).
type markdownTheme struct {
	heading    [6]*style // por nivel 1..6 (índice 0 = h1)
	code       *style    // code inline y code block
	emphasis   *style    // *italic*
	strong     *style    // **bold**
	link       *style    // [texto](url)
	bullet     *style    // el "- "/"1. " de las listas
	blockquote *style    // el "> " y su contenido
	rule       *style    // la línea de un --- (hr)
}

// defaultTheme construye el theme por defecto: atributos tipográficos, sin
// color (no imponemos paleta; el toolkit la añade vía opts.theme). Headings y
// strong en negrita, emphasis en itálica, link subrayado, blockquote en
// itálica. Es "razonable" (§10) y suficiente para que un render headless
// distinga los elementos por atributo.
func defaultTheme() markdownTheme {
	bold := &style{bold: true}
	italic := &style{italic: true}
	underline := &style{underline: true}
	var t markdownTheme
	for i := range t.heading {
		t.heading[i] = bold
	}
	t.code = nil // el code block/inline se distingue por estructura; sin color por defecto
	t.emphasis = italic
	t.strong = bold
	t.link = underline
	t.bullet = nil
	t.blockquote = italic
	t.rule = nil
	return t
}

// mdRenderer es el estado de un render: la fuente cruda (para extraer el texto de
// los segmentos del AST), el ancho de envoltura y el theme. Se crea uno por
// llamada; no se comparte entre goroutines (CPU puro, sin estado global).
type mdRenderer struct {
	src   []byte
	width int
	theme *markdownTheme
}

// renderMarkdownBlocks parsea `s` (CommonMark, tolerante a entrada incompleta) y
// devuelve sus líneas AGRUPADAS por bloque de nivel superior: una rebanada por
// hijo directo del documento. Esta partición es la base del invariante de
// estabilidad (ver cabecera): al crecer el texto por el final, solo el ÚLTIMO
// grupo cambia. El Block final es la concatenación de todos los grupos.
func renderMarkdownBlocks(s string, width int, theme *markdownTheme) [][][]span {
	src := []byte(s)
	doc := goldmark.DefaultParser().Parse(text.NewReader(src))
	r := &mdRenderer{src: src, width: width, theme: theme}

	var blocks [][][]span
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		blocks = append(blocks, r.renderTopBlock(child))
	}
	return blocks
}

// renderTopBlock renderiza un bloque (hijo directo del documento o cualquier
// bloque contenedor) a sus líneas. Despacha por tipo de nodo. El armazón de
// líneas que produce es estable: cada tipo emite un número de líneas determinado
// por su contenido, sin depender de los bloques vecinos.
func (r *mdRenderer) renderTopBlock(n ast.Node) [][]span {
	switch node := n.(type) {
	case *ast.Heading:
		return r.renderHeading(node)
	case *ast.Paragraph:
		return r.renderParagraph(node)
	case *ast.TextBlock:
		// Un TextBlock es el contenido de un list item "tight" (sin envoltura en
		// párrafo): se renderiza como un párrafo.
		return r.renderParagraph(node)
	case *ast.FencedCodeBlock:
		return r.renderCodeBlock(node.Lines(), languageOf(node, r.src))
	case *ast.CodeBlock:
		return r.renderCodeBlock(node.Lines(), "")
	case *ast.Blockquote:
		return r.renderBlockquote(node)
	case *ast.List:
		return r.renderList(node)
	case *ast.ThematicBreak:
		return r.renderRule()
	default:
		// HTMLBlock u otros: emite su texto crudo línea a línea sin estilo, para no
		// perder contenido ni romper (robustez ante lo no contemplado).
		if linesNode, ok := n.(interface{ Lines() *text.Segments }); ok {
			return r.renderCodeBlock(linesNode.Lines(), "")
		}
		return [][]span{{}}
	}
}

// renderHeading renderiza un heading (#..######) a líneas envueltas a `width`,
// con el Style del nivel (theme.heading[level-1]). El texto inline del heading
// (que puede llevar **bold**/`code`/etc.) se aplana a texto plano y se reestiliza
// con el estilo del heading —un heading se ve como heading por encima de su
// énfasis interno—.
func (r *mdRenderer) renderHeading(n *ast.Heading) [][]span {
	level := n.Level
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	st := r.theme.heading[level-1]
	return r.wrapStyled(r.inlineText(n), st)
}

// renderParagraph renderiza un párrafo a líneas envueltas a `width`. A diferencia
// del heading, conserva los estilos inline (**bold**, *italic*, `code`, [link]):
// recolecta los spans estilizados del párrafo, los envuelve respetando las
// fronteras de estilo y devuelve las líneas.
func (r *mdRenderer) renderParagraph(n ast.Node) [][]span {
	return r.wrapSpans(r.inlineSpans(n))
}

// renderCodeBlock renderiza un bloque de código (fenced o indentado) a una línea
// por línea de código, con el Style theme.code. Las líneas de código NO se
// envuelven (el código no se reflowea: una línea larga se deja larga, el
// compositor recorta) ni se reestiliza por contenido (el highlighting es S24).
//
// PUNTO DE EXTENSIÓN PARA S24: hoy cada línea es UN span (texto crudo + code
// style). S24 (`nu.text.highlight`) reemplazará ese span único por N spans
// coloreados según el lexer del lenguaje, manteniendo el MISMO armazón (una
// entrada por línea del código) — por eso `lang` ya se extrae y se pasa aquí.
func (r *mdRenderer) renderCodeBlock(segs *text.Segments, lang string) [][]span {
	_ = lang // reservado para S24 (highlight por lenguaje)
	var lines [][]span
	for i := 0; i < segs.Len(); i++ {
		seg := segs.At(i)
		// El valor del segmento incluye el '\n' final de la línea; se recorta para que
		// no contamine la anchura ni meta una línea fantasma.
		raw := strings.TrimRight(string(seg.Value(r.src)), "\n")
		lines = append(lines, []span{{text: raw, st: r.theme.code}})
	}
	if len(lines) == 0 {
		// Un fence recién abierto (```sin contenido aún) es un bloque de código vacío:
		// una línea en blanco con el estilo de code, para que el bloque exista de forma
		// estable mientras se escribe dentro.
		lines = [][]span{{{text: "", st: r.theme.code}}}
	}
	return lines
}

// renderChildrenWidth renderiza los hijos de `n` con un ancho de envoltura
// REDUCIDO a `w` (>= 1), restaurando el ancho original al volver. Lo usan los
// contenedores (blockquote, list) para que el contenido interno deje hueco a su
// prefijo ("> " / "- ") y la suma prefijo+contenido no exceda `r.width`. El ancho
// reducido es fijo por contenedor (no depende del contenido posterior), así que
// no compromete la estabilidad de los bloques de nivel superior.
func (r *mdRenderer) renderChildrenWidth(n ast.Node, w int) [][]span {
	if w < 1 {
		w = 1
	}
	saved := r.width
	r.width = w
	defer func() { r.width = saved }()
	var inner [][]span
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		inner = append(inner, r.renderTopBlock(child)...)
	}
	return inner
}

// renderBlockquote renderiza un blockquote (>): renderiza su contenido interno
// (recursivo: párrafos, listas, otro quote) a un ancho reducido por el prefijo
// "> " y prefija cada línea con "> " usando el Style theme.blockquote, que
// también baña el contenido que no traiga estilo propio. La sangría "> " es
// estable: añadir texto dentro del quote no la mueve.
func (r *mdRenderer) renderBlockquote(n *ast.Blockquote) [][]span {
	const prefixW = 2 // "> "
	inner := r.renderChildrenWidth(n, r.width-prefixW)
	if len(inner) == 0 {
		inner = [][]span{{}}
	}
	prefix := span{text: "> ", st: r.theme.blockquote}
	out := make([][]span, 0, len(inner))
	for _, ln := range inner {
		row := make([]span, 0, len(ln)+1)
		row = append(row, prefix)
		for _, sp := range ln {
			if sp.st == nil {
				sp.st = r.theme.blockquote
			}
			row = append(row, sp)
		}
		out = append(out, row)
	}
	return out
}

// renderList renderiza una lista (- / * / 1.) a líneas con su marcador y la
// continuación sangrada. Cada ítem se renderiza recursivamente (puede llevar
// párrafos, sublistas) y se prefija: la primera línea con el marcador ("- " o
// "1. "), las demás con espacios de la misma anchura (sangría colgante estable).
func (r *mdRenderer) renderList(n *ast.List) [][]span {
	ordered := n.IsOrdered()
	number := n.Start
	if ordered && number == 0 {
		number = 1
	}
	var out [][]span
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		var marker string
		if ordered {
			marker = strconv.Itoa(number) + ". "
			number++
		} else {
			marker = "- "
		}
		markerW := len([]rune(marker))
		indent := strings.Repeat(" ", markerW)

		// El contenido del ítem se envuelve a un ancho reducido por el marcador, para
		// que marcador+contenido no exceda `r.width`.
		inner := r.renderChildrenWidth(item, r.width-markerW)
		if len(inner) == 0 {
			inner = [][]span{{}}
		}
		for i, ln := range inner {
			row := make([]span, 0, len(ln)+1)
			if i == 0 {
				row = append(row, span{text: marker, st: r.theme.bullet})
			} else {
				row = append(row, span{text: indent, st: r.theme.bullet})
			}
			row = append(row, ln...)
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		out = [][]span{{}}
	}
	return out
}

// renderRule renderiza una regla horizontal (---) a una línea de guiones del
// ancho `width`, con el Style theme.rule.
func (r *mdRenderer) renderRule() [][]span {
	return [][]span{{{text: strings.Repeat("-", r.width), st: r.theme.rule}}}
}

// ───────────────────────────────────────────────────────────────────────────
// Inline: aplanado de texto y recolección de spans estilizados.
// ───────────────────────────────────────────────────────────────────────────

// inlineText aplana el contenido inline de un nodo (heading) a texto PLANO, sin
// estilos. Lo usa el heading (que reestiliza todo con el estilo del nivel).
// Concatena el texto de los nodos hoja, insertando un espacio en los saltos de
// línea suaves (un heading de varias líneas se une en un flujo que se reenvuelve).
func (r *mdRenderer) inlineText(n ast.Node) string {
	var b strings.Builder
	r.collectText(n, &b)
	return b.String()
}

func (r *mdRenderer) collectText(n ast.Node, b *strings.Builder) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Text:
			b.Write(node.Value(r.src))
			if node.SoftLineBreak() || node.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(node.Value)
		case *ast.CodeSpan:
			b.WriteString(codeSpanText(node, r.src))
		case *ast.AutoLink:
			b.Write(node.URL(r.src))
		default:
			r.collectText(c, b)
		}
	}
}

// inlineSpans recolecta el contenido inline de un nodo como spans ESTILIZADOS,
// resolviendo el estilo de cada tramo según el contexto (énfasis, strong, code,
// link). Es lo que da el render rico de un párrafo. El estilo activo se acumula
// por anidamiento (un *texto `code`* combina itálica y code en el código).
func (r *mdRenderer) inlineSpans(n ast.Node) []span {
	var out []span
	r.collectSpans(n, nil, &out)
	return out
}

// collectSpans recorre el inline aplicando `cur` (el estilo heredado del
// contexto). Cada tipo de nodo combina su estilo con `cur` antes de bajar a sus
// hijos o emitir su texto. Un salto de línea suave/duro se emite como un espacio
// (el wrap posterior decide dónde cortar).
func (r *mdRenderer) collectSpans(n ast.Node, cur *style, out *[]span) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Text:
			txt := string(node.Value(r.src))
			if node.SoftLineBreak() || node.HardLineBreak() {
				txt += " "
			}
			if txt != "" {
				*out = append(*out, span{text: txt, st: cur})
			}
		case *ast.String:
			if len(node.Value) > 0 {
				*out = append(*out, span{text: string(node.Value), st: cur})
			}
		case *ast.CodeSpan:
			*out = append(*out, span{text: codeSpanText(node, r.src), st: combineStyle(cur, r.theme.code)})
		case *ast.Emphasis:
			st := r.theme.emphasis
			if node.Level >= 2 {
				st = r.theme.strong
			}
			r.collectSpans(c, combineStyle(cur, st), out)
		case *ast.Link:
			r.collectSpans(c, combineStyle(cur, r.theme.link), out)
		case *ast.AutoLink:
			*out = append(*out, span{text: string(node.URL(r.src)), st: combineStyle(cur, r.theme.link)})
		default:
			r.collectSpans(c, cur, out)
		}
	}
}

// codeSpanText extrae el texto de un `code inline` (`...`): sus hijos son nodos
// Text con los segmentos del contenido entre las comillas.
func codeSpanText(n *ast.CodeSpan, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			b.Write(t.Value(src))
		}
	}
	return b.String()
}

// languageOf extrae el lenguaje de un fenced code block (```go → "go"), o "" si
// el fence no declara lenguaje. Lo consumirá S24 (highlight); aquí solo se pasa a
// renderCodeBlock para fijar la firma del punto de extensión.
func languageOf(n *ast.FencedCodeBlock, src []byte) string {
	lang := n.Language(src)
	if lang == nil {
		return ""
	}
	return string(lang)
}

// ───────────────────────────────────────────────────────────────────────────
// Estilos y wrap de spans.
// ───────────────────────────────────────────────────────────────────────────

// combineStyle funde `base` (el estilo heredado del contexto) con `add` (el del
// elemento actual): los atributos booleanos se ORean y los colores de `add`
// pisan a los de `base` (un link dentro de itálica conserva la itálica y añade el
// subrayado del link). nil en ambos → nil (sin estilo).
func combineStyle(base, add *style) *style {
	if base == nil && add == nil {
		return nil
	}
	res := &style{}
	if base != nil {
		*res = *base
	}
	if add != nil {
		if add.fgSet {
			res.fg, res.fgSet = add.fg, true
		}
		if add.bgSet {
			res.bg, res.bgSet = add.bg, true
		}
		res.bold = res.bold || add.bold
		res.italic = res.italic || add.italic
		res.underline = res.underline || add.underline
		res.reverse = res.reverse || add.reverse
	}
	return res
}

// wrapStyled envuelve un string plano a `width` celdas (reusando `wrapText` de
// S22) y aplica `st` a cada línea producida. Lo usan los headings.
func (r *mdRenderer) wrapStyled(s string, st *style) [][]span {
	textLines := wrapText(s, r.width)
	out := make([][]span, len(textLines))
	for i, ln := range textLines {
		out[i] = []span{{text: ln, st: st}}
	}
	return out
}

// wordToken es una "palabra" del flujo inline: su texto, su estilo y si en el
// origen venía precedida de espacio (`sepBefore`). El estilo cambia entre tokens
// adyacentes sin espacio (un `code` pegado a un punto: dos tokens, sepBefore
// false en el segundo) y por eso el wrap no puede inventar un espacio donde no lo
// había —de ahí el flag, que evita el bug de "code ." con espacio espurio—.
type wordToken struct {
	text      string
	st        *style
	w         int
	sepBefore bool
}

// tokenizeSpans aplana una secuencia de spans estilizados a tokens de palabra,
// recordando para cada uno si venía tras un espacio. Un tramo de varios espacios
// cuenta como una sola separación (wrap canónico, como `splitWords` de S22). El
// primer token de todo tiene sepBefore=false (no abre con espacio).
func tokenizeSpans(spans []span) []wordToken {
	var out []wordToken
	pendingSep := false
	first := true
	for _, sp := range spans {
		s := sp.text
		start := -1
		for i := 0; i < len(s); i++ {
			if s[i] == ' ' {
				if start >= 0 {
					out = append(out, wordToken{text: s[start:i], st: sp.st, w: uniseg.StringWidth(s[start:i]), sepBefore: pendingSep && !first})
					first = false
					start = -1
				}
				pendingSep = true
			} else if start < 0 {
				start = i
			}
		}
		if start >= 0 {
			out = append(out, wordToken{text: s[start:], st: sp.st, w: uniseg.StringWidth(s[start:]), sepBefore: pendingSep && !first})
			first = false
			pendingSep = false
		}
	}
	return out
}

// wrapSpans envuelve una secuencia de spans estilizados a `width` celdas,
// respetando las fronteras de estilo: parte por palabras (como `wrapParagraph`
// de S22), conservando el estilo de cada tramo y SIN inventar espacios donde el
// origen no los tenía (`sepBefore`). Una palabra mantiene el estilo de su span;
// una palabra sola más ancha que `width` se parte por grapheme (reusa
// `splitWide`). Es el word-wrap de S22 generalizado a spans estilizados.
//
// AGRUPACIÓN POR PEGADO: tokens consecutivos sin espacio entre ellos
// (`sepBefore=false`, p. ej. `code`+`.` o un `*` huérfano pegado a la palabra
// siguiente) forman un GRUPO atómico que no se separa al envolver —se rompería
// la palabra visual—. El wrap opera sobre grupos; dentro de un grupo los spans
// van pegados, entre grupos va un espacio.
func (r *mdRenderer) wrapSpans(spans []span) [][]span {
	tokens := tokenizeSpans(spans)
	if len(tokens) == 0 {
		return [][]span{{}}
	}

	// Agrupa por pegado: cada grupo es una rebanada de tokens contiguos sin espacio.
	type group struct {
		toks []wordToken
		w    int
	}
	var groups []group
	for _, tk := range tokens {
		if len(groups) > 0 && !tk.sepBefore {
			g := &groups[len(groups)-1]
			g.toks = append(g.toks, tk)
			g.w += tk.w
		} else {
			groups = append(groups, group{toks: []wordToken{tk}, w: tk.w})
		}
	}

	var lines [][]span
	var cur []span
	curW := 0
	flush := func() {
		if cur != nil {
			lines = append(lines, cur)
			cur = nil
			curW = 0
		}
	}
	appendGroup := func(g group) {
		if curW > 0 {
			cur = append(cur, span{text: " ", st: g.toks[0].st})
			curW++
		}
		for _, tk := range g.toks {
			cur = append(cur, span{text: tk.text, st: tk.st})
		}
		curW += g.w
	}
	for _, g := range groups {
		if g.w > r.width {
			// El grupo entero no cabe: cierra la línea en curso y pártelo por grapheme,
			// span a span (conservando el estilo de cada token al partir).
			flush()
			for _, tk := range g.toks {
				for _, part := range splitWide(tk.text, r.width) {
					lines = append(lines, []span{{text: part, st: tk.st}})
				}
			}
			continue
		}
		if curW == 0 || curW+1+g.w <= r.width {
			appendGroup(g)
			continue
		}
		flush()
		appendGroup(g)
	}
	flush()
	if len(lines) == 0 {
		lines = [][]span{{}}
	}
	return lines
}
