---
title: "`enu.text.highlight` (syntax highlighting, degrada a texto plano) (api.md §10)"
type: "sesion"
id: "S24"
phase: 5
status: "cerrada"
---
# S24 — `enu.text.highlight` (syntax highlighting, degrada a texto plano) (api.md §10)

`enu.text.highlight(code, lang, opts?) -> Block` resalta un snippet a un `Block`
con un span por tramo coloreado según su tipo de token. **[W] pero NINGUNA ⏸**
(CPU puro como S22/S23/S18: tokeniza un string ya en memoria, no espera IO; ni
`suspend` ni `requireTask`). Vive en `highlight.go` y se cuelga de `enu.text`
junto a `width`/`wrap`/`truncate`/`markdown`. Ni una función pública de más.

## Librería: chroma, y la decisión de versión

Se usa `github.com/alecthomas/chroma/v2` (puro-Go, decenas de lexers y themes que
asignan colores `#rrggbb` por tipo de token — justo lo que el `Block` quiere
guardar como color LITERAL, G22). Es la opción canónica de highlighting en Go y
encaja con "Lua decide, Go ejecuta" (ADR-004): el léxico pesado va en Go.

**Desviación deliberada — se pinea v2.14.0, no la última (v2.27.0).** La última
de chroma declara `go >= 1.25` en su `go.mod`; añadirla **subiría la versión `go`
del módulo `enu` de 1.24.7 a 1.25** (un cambio de toolchain de todo el proyecto,
no una decisión que toque a S24). v2.14.0 mantiene `go 1.24.7` intacto y trae los
mismos lexers/themes necesarios. Trae `github.com/dlclark/regexp2` como dep
transitiva (puro-Go, `CGO_ENABLED=0` intacto). Si el módulo sube a Go ≥ 1.25 por
otro motivo, se puede actualizar chroma sin coste; el disparador es ese.

## El degradado a texto plano (la lógica propia)

Un `lang` **desconocido, vacío o nil** NO es un error: degrada a **texto plano**
—un `Block` sin estilo (un span por línea vía `splitLines` de S22), con el texto
EXACTO—. Es la red de seguridad del render de fences en streaming de S23: un
fence con un `lang` que no reconocemos (o sin `lang`) sigue dando un Block
legible en vez de romper. La señal es que `lexers.Get(lang)` devuelve `nil`
cuando no hay lexer para ese nombre (tras intentar también por extensión); `lang`
vacío ni se consulta. Un fallo de tokenización (no esperado con los lexers
embebidos) también cae a texto plano: highlight nunca rompe el render.

## El mapeo tokens→spans

Lexer encontrado → se envuelve en `chroma.Coalesce` (funde tokens adyacentes del
MISMO tipo: menos spans, mismo resultado, texto idéntico). Se tokeniza con
`EnsureLF=false` (no alterar el texto de origen: queremos reconstruir `code`
EXACTO desde los spans). Se agrupa por línea con `chroma.SplitTokensIntoLines`
(una línea de código → una línea de Block) — esa función deja el `\n` como sufijo
del token que cierra cada línea, que se recorta con `TrimSuffix` (el salto de
línea es estructura del Block, no texto del span). Cada tramo se emite como
`span{text, style}` con `tokenStyle(theme, tok.Type)`: el color de primer plano
(literal `#rrggbb` de `Colour.String()` si `IsSet()`, G22) y los atributos
bold/italic/underline (los trileanos `Yes` de Chroma); un token sin color ni
atributos → `st = nil` (sin estilo), para no inflar el Block. Chroma no expone
"reverse", así que ese atributo queda en false. Una línea de código en blanco
conserva su hueco (un span vacío sin estilo); código vacío → un Block de height 1.

## Theme: el nombre, no un mapeo a mano

`opts.theme` es un **string**: el nombre de un theme de Chroma (default
`"github"`, claro y legible). Un theme desconocido cae al fallback propio de
`styles.Get` (nunca nil, no rompe). **No** se acepta un mapeo de `Style` por tipo
de token a mano: los `TokenType` de Chroma son un vocabulario amplio (decenas de
subcategorías) y exponerlos filtraría el detalle de la librería a la API pública;
el nombre de theme es la única perilla, y un theme de Chroma ya da colores
literales coherentes con G22. La firma §10 es `highlight(code, lang, opts?)`;
`opts` solo lleva `theme?` por ahora — sin ampliar la superficie.

## Frontera: NO se toca markdown.go

S23 dejó `renderCodeBlock` como "punto de extensión" para que S24 sustituyera su
span plano por N spans coloreados. **Esa integración se deja para después**: S24
implementa `enu.text.highlight` standalone y NO modifica `markdown.go`, para no
arriesgar el invariante de estabilidad (streaming-safe) de S23. La integración
highlight-dentro-de-markdown es trabajo futuro reabrible (un `opts.highlight` o
similar en `markdown`), fuera del alcance de §10 de `highlight`.

## Tests (`highlight_test.go`)

Sobre el núcleo puro `highlightToBlock` (sin LState): Go → varios spans con
estilo y ≥2 colores `#rrggbb` distintos, `.height`=nº líneas; desconocido/""/lang
extraño → texto plano sin estilo + texto EXACTO; json/python/lua → spans
razonables (≥2 colores); línea en blanco conserva hueco; código vacío → height≥1;
theme desconocido → fallback. Invariante transversal "no se pierde texto":
concatenar los spans por línea reproduce `code`. Vía Lua (`buildBlock` de
text_test.go): Go height/estilo, desconocido→plano, `.height` legible, sin-opts y
`opts.theme` válidos; usos malos de la firma (lang no-string, opts no-tabla,
`opts.theme` no-string) → `EINVAL` (lang nil/"" NO es error: degrada).

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S23). Binario `enu -e` confirma e2e: go (height 3), desconocido → plano
(height 2), `opts.theme` no-string → `EINVAL`.

**Sin hallazgos:** §10 bastó. Puntero ▶ avanza a **S25** (`enu.text.diff`).
