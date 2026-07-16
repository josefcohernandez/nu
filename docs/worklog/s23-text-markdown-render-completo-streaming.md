# S23 — `enu.text.markdown` (render completo, streaming-safe, themable) (api.md §10, 🔒)

`enu.text.markdown(s, opts) -> Block` renderiza markdown completo a un `Block` de
ancho `opts.width`. Es **[W] pero NINGUNA ⏸** (CPU puro, como `width`/`wrap`/
`truncate` de S22 y los codecs de S18: parsea un string ya en memoria, no espera
IO; por eso no usa `suspend` ni `requireTask`). Vive en `markdown.go`; se cuelga
desde `registerText` (text.go) para mantener todo `enu.text` en un sitio.

## Librería: goldmark (puro-Go, CommonMark)

`github.com/yuin/goldmark` v1.7.8 — puro-Go, CommonMark, **sin deps transitivas**
que afecten a `CGO_ENABLED=0` (ADR-001). "Lua decide, Go ejecuta" (ADR-004): el
parseo del documento a AST lo hace goldmark (`goldmark.DefaultParser().Parse(
text.NewReader(src))`) y nosotros recorremos el AST emitiendo spans en el
`Block`. Se **reusa** todo lo que se pueda de S22: `wrapText`/`splitWide` (word-
wrap por palabras y partido por grapheme), `uniseg.StringWidth` (anchura en
celdas, la misma de `text.width`) y `parseStyle`/`normalizeColor` (theme con
colores literales, G22). Pasa de `// indirect` a directa tras `go mod tidy`.

## Modelo de theme

`opts.theme` es una tabla con un `Style` por elemento; claves: `h1`..`h6`,
`code`, `emphasis`, `strong`, `link`, `bullet`, `blockquote`, `rule`. Cada una es
opcional; lo ausente cae al `defaultTheme()`: bold en headings/strong, italic en
emphasis/blockquote, underline en link, **sin color** (no imponemos paleta; el
toolkit la añade vía `opts.theme`). Los colores son **literales** (`#rrggbb` o
índice 0-255), validados por `parseStyle`; un nombre semántico (`"accent"`) →
`EINVAL` que nombra el elemento (G22: los nombres son del theme del toolkit, no
del core). El estilo inline se compone con `combineStyle` (ORea los atributos
booleanos, los colores de `add` pisan a los de `base`): un `[link]` dentro de
*itálica* conserva la itálica y añade el subrayado.

## Elementos soportados (y tablas: NO)

Headings (reestilizados con el Style del nivel por encima de su énfasis interno),
párrafos con word-wrap conservando estilos inline (**bold**/*italic*/`code
inline`/[link]/autolink), code blocks fenced e indentados (una línea por línea de
código, **SIN envolver** —el código no reflowea; el compositor recorta—, un span
por línea con `theme.code`), listas `-`/`*`/`1.` (marcador + sangría colgante;
`Start` respetado en ordenadas), blockquotes (prefijo `> ` + contenido), reglas
`---` (línea de guiones), enlaces (texto con `theme.link`).

**Tablas: NO se soportan** en S23. Son una extensión GFM, no CommonMark base;
goldmark sin extensiones no las parsea, así que una tabla cae a un párrafo de
texto plano (las celdas con `|`) — válido y estable, solo sin formato de tabla.
Si una extensión las pide, se reabre como P## y se activa la extensión de
goldmark.

## Anchura de los contenedores (prefijo + contenido ≤ width)

Un blockquote (`> `, 2 celdas) o un ítem de lista (`- `/`1. `, N celdas) consume
ancho con su prefijo. Para que prefijo+contenido no exceda `opts.width`, el
contenido interno se renderiza a un ancho **reducido** (`renderChildrenWidth`
baja `r.width` temporalmente al rango del prefijo y lo restaura al volver). El
ancho reducido es fijo por contenedor (no depende del contenido posterior), así
que no compromete la estabilidad. El marcador de una lista ordenada puede crecer
(`9.`→`10.`), un reflow menor confinado al bloque de la lista (que es un único
bloque de nivel superior; el invariante solo protege los bloques *anteriores*).

## wrapSpans: word-wrap de spans estilizados (nuestro)

Generaliza el word-wrap de S22 a una secuencia de spans con estilo. `tokenizeSpans`
trocea en palabras recordando `sepBefore` (si venía tras un espacio en el
origen): **no se inventa un espacio donde el origen no lo tenía** —esto arregla
el bug de "code ." (un `code` inline pegado a un punto), "*no cierra" (un `*`
huérfano pegado a la palabra) y "[aqui](http" (un enlace sin cerrar)—. Tokens
pegados (sin separación) forman un **grupo atómico** que no se parte al envolver
(es una palabra visual); entre grupos va un espacio, y un grupo más ancho que
`width` se parte por grapheme con `splitWide` conservando el estilo de cada
token.

## STREAMING-SAFE y el invariante de estabilidad (la lógica 🔒)

**Entrada incompleta no rompe.** goldmark es tolerante: parsea hasta EOF (un
fence ```...sin cerrar es un code block hasta el final del texto, un `*énfasis`
sin cerrar cae a texto plano, un `[enlace](sin cerrar` queda como texto). No
dependemos de que el último bloque esté "cerrado"; el render produce siempre un
Block válido (height ≥ 1) sin panic ni error.

**Estrategia de estabilidad: render por bloques de nivel superior
INDEPENDIENTES.** `renderMarkdownBlocks` devuelve `[][][]span` (una rebanada por
hijo directo del documento); el Block es su concatenación. La clave: markdown es
estable por bloques al crecer por el final —añadir texto solo afecta al ÚLTIMO
bloque de nivel superior (el "en construcción"); los anteriores ya están
delimitados por una línea en blanco o un cambio de tipo—. Renderizar por bloques
(no un layout global) es lo que evita que un fence abierto al final reflowee los
párrafos de arriba.

**INVARIANTE EXACTO (lo que blinda el test 🔒):** sea `R(s) = [B_1, ..., B_m]` la
descomposición del render por bloques de nivel superior. Para `s_k` prefijo de
`s_{k+1}` (un token más), `B_i(s_k) == B_i(s_{k+1})` para todo `i < m_k - 1` (los
bloques ya completos, todos salvo el último del prefijo corto, no cambian; solo
crece por el final). Las excepciones de CommonMark (setext heading que
reinterpreta el párrafo previo con un subrayado `===`/`---`; lazy continuation
que extiende un párrafo) NO rompen el Block, solo relajan el ÚLTIMO bloque, por
eso el invariante lo excluye. El test emite los límites de bloque y compara
bloque a bloque salvo el último (0 violaciones sobre 4 docs por-rune y un troceo
por-token).

## Punto de extensión para S24 (highlight)

`renderCodeBlock` aplica hoy UN span (`theme.code`) por línea de código y ya
extrae `lang` con `languageOf`. S24 (`enu.text.highlight`) sustituirá ese span
plano por N spans coloreados por token según el lexer del lenguaje, manteniendo
el MISMO armazón (una entrada por línea del código) — por eso `lang` se pasa ya a
`renderCodeBlock` aunque hoy se ignore.

## Tests 🔒 y verificación

`markdown_test.go`: (1) **entrada incompleta no rompe** (tabla de ~20 casos:
fence/lista/ordered/italic/bold/code-inline/link/heading/quote/hr/setext a
medias, backtick/asterisco/corchete sueltos, mezcla caótica) + vía Lua; (2)
**crecimiento estable** por-rune sobre 4 docs y un troceo por-token, comparando
los bloques de nivel superior salvo el último (invariante exacto, 0 violaciones);
(3) render de cada elemento (heading por niveles, emphasis/strong/normal, code
block sin-wrap + `lang` extraído, listas viñeta/ordenada con `Start`/sangría
colgante, blockquote prefijo+estilo, hr a `width`, link subrayado, párrafo
wrap≤width, Block.width≤opts.width); (4) theme literal aplicado (inspección en
Go) + G22→`EINVAL` nombrando el elemento, `opts.width` obligatorio (7
caminos→`EINVAL`: sin opts, sin width, 0, negativo, no-entero, opts no-tabla,
width no-número), no-suspende fuera de task.

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S22). Binario `enu -e` confirma e2e: doc completo (height 5, width 23), fence
incompleto (height 1), theme `fg="accent"` → rechazado (G22).

**Sin hallazgos:** §10 bastó. Puntero ▶ avanza a **S24** (`enu.text.highlight`,
🔒).
