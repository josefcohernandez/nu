---
title: "`enu.text` (width/wrap/truncate) + tipo `Block` + `enu.ui.block`/`caps`/`Style` (api.md §10, §9.2, 🔒)"
type: "sesion"
id: "S22"
phase: 5
status: "cerrada"
---
# S22 — `enu.text` (width/wrap/truncate) + tipo `Block` + `enu.ui.block`/`caps`/`Style` (api.md §10, §9.2, 🔒)

Abre la **Fase 5 (Texto y búsqueda)**. Sesión fundacional: el tipo `Block` que se
fija aquí es la moneda común que construyen y consumen S23 (markdown), S24
(highlight), S25 (diff) y S29 (blit/viewport), y `text.width` es la lógica 🔒
sobre la que descansa TODO el cálculo de layout (wrap, truncate, recorte de
viewport).

## La librería: `github.com/rivo/uniseg` (puro-Go)

La anchura en celdas no es número de bytes ni de runes ni de graphemes: hay que
contemplar **grapheme clusters** (una "é" puede ser base+combining = 2 runes, 1
celda), **east-asian wide** (CJK/hangul = 2 celdas) y **emoji con secuencias ZWJ**
(una familia 👨‍👩‍👧‍👦 son 4 emojis unidos por U+200D que el terminal pinta como
1 glifo = 1 grapheme de anchura 2). Reimplementar las tablas Unicode en Go sería
absurdo y frágil: se delega en `rivo/uniseg` (v0.4.7), puro-Go (CGO_ENABLED=0
intacto, sin deps transitivas), que expone `StringWidth(s)` (anchura monospace
total) y `FirstGraphemeClusterInString` (iteración cluster a cluster con su
anchura, para recortar sin partir un grapheme). Pasa de `// indirect` a directa
en `go.mod` tras `go mod tidy`. Alternativas descartadas: `go-runewidth`
(maneja east-asian pero no clusters/ZWJ correctamente para nuestro caso);
`x/text` (no da anchura de celda lista para usar).

## La ESTRUCTURA del tipo `Block` (crítica para S23–S29)

Un **Block** es un **handle opaco** (`*lua.LUserData` con metatabla
`enu.ui.Block`) cuyo `__index` solo expone `.width` y `.height` (números) a Lua;
el contenido es interno (no se expone como tabla mutable: el Block es opaco,
§9.2). Internamente (`block.go`):

- `type span struct { text string; st *style }` — un tramo de texto con estilo.
  `text` es UTF-8 crudo; `st` nil = sin estilo (hereda lo de debajo al pintar).
- `type style struct { fg, bg string; fgSet, bgSet bool; bold, italic,
  underline, reverse bool }` — el estilo de un span. Los colores se guardan
  **normalizados como string**: un literal `"#rrggbb"` (validado, a minúsculas) o
  el índice 0-255 como decimal-string (`"42"`). Guardar string (no un tipo color
  resuelto) preserva la intención literal hasta que el compositor (S29) lo degrade
  a `enu.ui.caps().colors` —el render decide, no el Block (§9.2, G22)—.
- `type block struct { lines [][]span; width, height int }` — una rebanada de
  líneas, cada línea una rebanada de spans. `width` = **máximo ancho de línea en
  celdas** (vía `uniseg.StringWidth`), `height` = nº de líneas. Ambos se calculan
  **una vez** en `newBlock` (único constructor) y se **cachean**: el Block es
  **inmutable** (wrap/markdown/diff devuelven uno nuevo, no mutan), y el
  compositor consultará `.width`/`.height` en cada blit, así que recalcular sería
  el coste cuadrático que ADR-007 evita.

**Por qué spans y no una rejilla de celdas ya resuelta:** el Block es una
*descripción* (texto lógico por tramos de estilo), no una *pintura*. La rejilla,
el recorte de viewport y el degradado de color son del compositor (S29); guardar
spans deja a S25/S23 construir líneas concatenando tramos sin pensar en celdas, y
mantiene "blit = copia, nunca re-render" (§9.1). Helpers que S23–S29 reusan:
`newBlock(lines)`, `pushBlock`, `checkBlock(L, idx)`, `lineWidth(spans)`.

## `enu.text.width/wrap/truncate` — CPU puro, [W], **ninguna ⏸**

`text` es [W] (§16) pero **ninguna suspende**: miden/reordenan un string ya en
memoria, no esperan IO (como los codecs de S18). Por eso NO usan el puente
`suspend` ni `requireTask` —corren síncronas en el estado principal (y en workers
con S34)—. [W] = "disponible en workers", no "suspende".

- `width(s)` → `uniseg.StringWidth(s)`. Vacío = 0.
- `wrap(s, width, opts?)` → Block. Word-wrap por palabras (espacios ASCII), con
  los `\n` de `s` como **límites duros** (un `\n\n` deja una línea en blanco). Una
  palabra **más ancha que `width`** se **parte por grapheme** (`splitWide`) en
  trozos ≤ `width` —partir es preferible a desbordar el viewport, que recortaría
  y perdería texto en silencio—. `width <= 0` → `EINVAL`. `opts.style` aplica un
  `Style` por defecto a cada span producido. El wrap colapsa el espaciado (un
  espacio entre palabras de una misma línea); preservar el espaciado exacto no es
  el contrato de un word-wrap.
- `truncate(s, width, opts?)` → string. Recorta a ≤ `width` celdas **por
  grapheme** (nunca parte un cluster/emoji). Si `s` cabe entero, se devuelve tal
  cual (sin elipsis). `opts.ellipsis` (p. ej. "…") se reserva su anchura del
  presupuesto; si la elipsis es **más ancha que `width`**, se cae a recorte simple
  sin elipsis (mejor texto a secas que nada). `width == 0` → "". `width < 0` →
  `EINVAL`.

## `enu.ui.block`/`caps`/`Style` y la NOTA DE FRONTERA (G20 es S32)

El contrato dice que sin TTY `enu.ui` **no existe** (G20). Pero ese gating es S32,
y S23–S31 necesitan `enu.ui.block`/`caps`/`Style` **ya** para construir e
inspeccionar Blocks en sus tests (markdown/highlight/diff producen Blocks; el
theme resuelve `Style`). Decisión: en S22 `enu.ui` se cuelga **siempre** (también
headless) con solo `block`/`caps`; S32 añadirá la condición de TTY por encima sin
tocar estas firmas. `enu.has("ui")` sigue en **false** hasta S32 (no se afirma una
capacidad que aún no se concede). Es deuda explícita, no contradicción de G20.

- `enu.ui.block(lines)` → Block. Cada línea es un **string** (un span sin estilo)
  o un **array de Spans** `{text, style?}`. Calcula `.width`/`.height` al
  construir. Una línea vacía `""` conserva su hueco (afecta a `.height`).
- `Style` (`parseStyle`/`normalizeColor`): colores **literales** —`"#rrggbb"` (6
  hex) o índice 0-255 (número o string numérica)—; un **nombre semántico**
  (`"accent"`) o un hex mal formado o un índice fuera de rango → `EINVAL` (los
  nombres son vocabulario del theme, G22).
- `enu.ui.caps()` → `{colors, kitty_keyboard, mouse, images}`. Sin terminal vivo
  que interrogar (eso es Fase 6), `colors` se estima por entorno
  (`COLORTERM=truecolor` → 16M; `TERM` con "256color" → 256; `TERM` vacío
  headless → 256 default razonable; `dumb` → 0; resto → 16) y los protocolos
  (kitty_keyboard/mouse/images) quedan en `false` (deny-by-default hasta la
  negociación de Fase 6, como `enu.has`).

## Tests 🔒 y verificación

`text_test.go`: **width** table-driven y NOMBRADO (vacío=0, ascii, ascii con
espacios, CJK wide=2, hangul=2, mezcla, emoji simple=2, **emoji ZWJ familia=2**,
é precompuesto=1, é combinante base+marca=1, combining suelto, varios emojis) +
vía Lua (incl. combining acute por bytes `\204\129`). **wrap** (vacío→[""], cabe,
envuelve por palabra, palabra justa, **palabra más larga que el ancho se parte**,
`\n` duro, línea en blanco entre párrafos, CJK por celdas) con invariante "ninguna
línea > width". **truncate** (cabe entero/justo, con/sin elipsis, width 0, elipsis
multi-celda, **no parte emoji**, emoji entero cuando cabe, **no parte grapheme
combinante**, elipsis más ancha que width→recorte simple) con invariante
"resultado ≤ width". **splitWide** (emoji×3 en width 2 → 3 trozos; emoji de 2
celdas en width 1 → trozo único sin partir). **ui.block** manual inspeccionado en
Go (width=máx línea, height=nº líneas, spans con estilo, línea en blanco
conservada), color índice normalizado, validaciones→EINVAL, **caps** (4 claves,
colors>0, protocolos false), **normalizeColor** (hex a minúsculas, índices,
rechazo de nombres G22). `TestTextNotSuspending` confirma que corren fuera de
task.

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S21). Binario `enu -e` confirma e2e: width (5/4/2/2 para ascii/CJK/emoji/ZWJ),
wrap (height/width), truncate con elipsis, ui.block (width 6/height 2), caps, y
G22 (`fg="accent"` → EINVAL).

**Sin hallazgos:** §10 y §9.2 bastaron. Puntero ▶ avanza a **S23**
(`enu.text.markdown`, 🔒).
