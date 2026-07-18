package runtime

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// `enu.text.highlight` — syntax highlighting de un snippet a un `Block` (api.md
// §10, sesión S24). Es CPU puro: tokeniza un string ya en memoria y emite líneas
// de spans coloreados, sin esperar IO. Por eso es **[W] pero NINGUNA ⏸** (como
// `width`/`wrap`/`truncate`/`markdown` de S22/S23 y los codecs de S18): no usa el
// puente `suspend` ni `requireTask`, corre síncrona en el estado principal (y en
// workers cuando lleguen, S34).
//
// LUA DECIDE, GO EJECUTA (ADR-004). El léxico de cada lenguaje lo hace Go
// (`github.com/alecthomas/chroma/v2`, puro-Go, muchos lexers, `CGO_ENABLED=0`
// intacto): se obtiene un lexer por nombre de lenguaje, se tokeniza el código y se
// agrupan los tokens por línea (`chroma.SplitTokensIntoLines`), emitiendo un span
// por tramo con el color que el theme le asigna a su tipo de token. Ni una función
// pública de más: solo se cuelga `enu.text.highlight`.
//
// ───────────────────────────────────────────────────────────────────────────
// EL DEGRADADO A TEXTO PLANO (la lógica propia a blindar).
// ───────────────────────────────────────────────────────────────────────────
//
// Un lenguaje **desconocido o vacío** NO es un error: degrada a **texto plano**
// —un Block con un span sin estilo por línea, conservando el texto EXACTO—. Es la
// red de seguridad del render de markdown en streaming (S23): un fence con un
// `lang` que no reconocemos (o sin `lang`) sigue produciendo un Block legible en
// vez de romper. `chroma.lexers.Get(lang)` devuelve `nil` cuando no hay un lexer
// para ese nombre (tras intentar también por extensión); ese `nil` es la señal de
// degradado. `lang` vacío ni se consulta: directo a texto plano.
//
// ───────────────────────────────────────────────────────────────────────────
// EL THEME Y LOS COLORES LITERALES (G22).
// ───────────────────────────────────────────────────────────────────────────
//
// El color de cada token sale de un **theme de Chroma** (un `*chroma.Style`), que
// asigna a cada tipo de token (keyword, string, comment, ...) un color
// **`#rrggbb`** concreto. Eso es coherente con G22: el `Block` guarda colores
// LITERALES (nunca nombres semánticos), y el compositor (S29) los degrada a lo que
// el terminal soporte con `caps().colors`. `opts.theme` (string) elige el theme
// por nombre de Chroma (p. ej. "github", "monokai"); ausente/desconocido → el
// theme por defecto de S24 (`defaultHighlightTheme`). NO se acepta un mapeo de
// `Style` a mano por tipo de token: los tipos de Chroma son un vocabulario amplio
// (decenas de subcategorías) y exponerlos sería filtrar el detalle de la librería
// a la API; el nombre de theme es la perilla, y un theme de Chroma ya da colores
// literales. (La firma §10 es `highlight(code, lang, opts?)`; `opts` solo lleva
// `theme?` por ahora — sin ampliar la superficie pública.)

// defaultHighlightTheme es el nombre del theme de Chroma usado cuando `opts.theme`
// no se pasa o no existe. "github" es un theme claro, legible y con colores
// `#rrggbb` bien diferenciados por tipo de token; cualquier otro válido sirve, la
// elección es estética. `styles.Get` cae a su propio fallback si el nombre no
// existe, así que nunca es nil.
const defaultHighlightTheme = "github"

// highlightToBlock es el núcleo puro (sin Lua): tokeniza `code` con el lexer de
// `lang` y construye el Block, o degrada a texto plano si el lenguaje es
// desconocido/vacío. Separado de `textHighlight` para poder testearlo en Go sin
// montar un `LState`.
func highlightToBlock(code, lang, themeName string) *block {
	lexer := lookupLexer(lang)
	if lexer == nil {
		// Lenguaje desconocido o vacío: degrada a texto plano. Un span sin estilo por
		// línea, conservando el texto exacto (incluidas las líneas en blanco).
		return newBlock(plainLines(code))
	}

	style := styles.Get(themeName) // nunca nil: cae a su propio fallback si no existe

	// `EnsureLF` normaliza CRLF→LF dentro del lexer; lo desactivamos para no alterar
	// el texto de origen (queremos reconstruir `code` EXACTO desde los spans). El
	// recorte del '\n' lo hacemos nosotros por línea (SplitTokensIntoLines deja el
	// '\n' al final del último token de cada línea).
	it, err := lexer.Tokenise(&chroma.TokeniseOptions{State: "root", EnsureLF: false}, code)
	if err != nil {
		// Un fallo de tokenización (no debería darse con los lexers embebidos) degrada
		// a texto plano en vez de propagar: highlight nunca rompe el render.
		return newBlock(plainLines(code))
	}

	tokenLines := chroma.SplitTokensIntoLines(it.Tokens())
	if len(tokenLines) == 0 {
		// Código vacío: un Block con una línea en blanco (height >= 1), como el resto
		// de productores de Block.
		return newBlock([][]span{{{text: ""}}})
	}

	lines := make([][]span, 0, len(tokenLines))
	for _, toks := range tokenLines {
		spans := make([]span, 0, len(toks))
		for _, tok := range toks {
			// SplitTokensIntoLines deja el '\n' como sufijo del token que cierra la línea;
			// se recorta para que no contamine la anchura ni meta una línea fantasma (el
			// salto de línea es estructural del Block, no texto del span).
			txt := strings.TrimSuffix(tok.Value, "\n")
			if txt == "" {
				continue
			}
			spans = append(spans, span{text: txt, st: tokenStyle(style, tok.Type)})
		}
		if len(spans) == 0 {
			// Una línea en blanco del código conserva su hueco (afecta a .height); un span
			// vacío sin estilo basta.
			spans = []span{{text: ""}}
		}
		lines = append(lines, spans)
	}
	return newBlock(lines)
}

// lookupLexer obtiene el lexer de Chroma para `lang`, o nil si el lenguaje es
// vacío o desconocido (la señal de degradado a texto plano). Un lexer encontrado
// se envuelve en `chroma.Coalesce`, que funde tokens adyacentes del MISMO tipo en
// uno solo —menos spans, mismo resultado visual, y el texto reconstruido es
// idéntico—.
func lookupLexer(lang string) chroma.Lexer {
	if lang == "" {
		return nil
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		return nil
	}
	return chroma.Coalesce(lexer)
}

// plainLines convierte `code` en líneas de un span sin estilo cada una,
// conservando el texto EXACTO (las líneas en blanco mantienen su hueco). Reusa
// `splitLines` de S22 (text.go), que parte por '\n' preservando los segmentos
// vacíos. Es el cuerpo del degradado a texto plano.
func plainLines(code string) [][]span {
	textLines := splitLines(code)
	lines := make([][]span, len(textLines))
	for i, ln := range textLines {
		lines[i] = []span{{text: ln}}
	}
	return lines
}

// tokenStyle traduce el `StyleEntry` que el theme asigna al tipo de token a un
// `*style` del Block: el color de primer plano (literal `#rrggbb`, G22) y los
// atributos bold/italic/underline. Un token sin color ni atributos → nil (sin
// estilo, hereda lo de debajo al pintar), para no inflar el Block con estilos
// vacíos. Chroma no expone "reverse", así que ese atributo queda en false.
func tokenStyle(s *chroma.Style, t chroma.TokenType) *style {
	e := s.Get(t)
	out := &style{
		bold:      e.Bold == chroma.Yes,
		italic:    e.Italic == chroma.Yes,
		underline: e.Underline == chroma.Yes,
	}
	if e.Colour.IsSet() {
		// Colour.String() da "#rrggbb" en minúsculas, justo la forma normalizada que el
		// Block espera para un color literal (G22).
		out.fg, out.fgSet = e.Colour.String(), true
	}
	if !out.fgSet && !out.bold && !out.italic && !out.underline {
		return nil
	}
	return out
}
