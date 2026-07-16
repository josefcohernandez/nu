# S25 — `enu.text.diff` (hunks estructurados + render a Block) (api.md §10, 🔒)

`enu.text.diff(a, b, opts?) -> {hunks, block?}` compara `a` (viejo) y `b`
(nuevo) **línea a línea** y devuelve sus hunks (regiones de cambio) y,
opcionalmente, un `Block` pintado. **[W] pero NINGUNA ⏸** (CPU puro, como
`width`/`wrap`/`markdown`/`highlight` de S22–S24 y los codecs de S18): no usa el
puente `suspend` ni `requireTask`. Vive en `diff.go` y reusa los helpers de
Block de S22 (`newBlock`/`span`/`style`, `parseStyle`/`normalizeColor`).

## Algoritmo / librería: LCS line-based propio, SIN dependencia nueva

La tarea ofrecía usar `go-difflib` o `gotextdiff`. Se decide **no añadir
ninguna dependencia**: el diff line-based clásico (LCS por programación dinámica
→ backtrack → agrupado en hunks con contexto) es pequeño, su corrección en los
bordes es exactamente lo que el test 🔒 blinda, y mantenerlo propio evita atar
la forma de los hunks (la API pública del consumidor) a la de una librería
externa. `go.mod`/`go.sum` quedan **intactos** (cero deps nuevas, coherente con
"cero dependency hell", ADR-001/filosofía §6).

Las piezas (todas puras, sin LState, testeables directamente):

- `splitDiffLines(s)`: parte `s` por `\n` tratando el salto como **terminador,
  no separador** — `"a\n"` y `"a"` dan ambos `["a"]`; `""` da cero líneas;
  `"a\nb"` da `["a","b"]`. Así **"sin newline final"** no introduce diferencias
  espurias frente al mismo texto con newline final (caso de borde 🔒). El `\r`
  de un CRLF se conserva dentro de la línea (el diff es por contenido exacto;
  normalizar finales de línea es del consumidor).
- `lcsTable(a, b)`: longitudes de la subsecuencia común más larga por DP,
  rellenada de atrás hacia delante para que el backtrack avance en orden de
  fichero. O(n·m); suficiente para diffs de tamaño humano (Myers O(ND) reabrible
  si hiciera falta para ficheros enormes).
- `diffOps(a, b)`: backtrack que emite la secuencia `context`/`del`/`add`. El
  desempate (ante un cambio, `del` antes que `add`) es el del diff unificado:
  una línea modificada sale como su `del` seguido de su `add`.
- `groupHunks(ops)`: agrupa los cambios en hunks rodeándolos de a lo sumo
  **`diffContextLines` = 3** líneas de contexto a cada lado (el estándar de
  facto), y **funde** dos cambios separados por ≤ 2·contexto en un solo hunk
  (su contexto se solapa), como hace el diff unificado.

## La forma de los hunks (la API que consume el visor / toolkit)

Cada hunk: `{ old_start, old_count, new_start, new_count, lines = { {kind, text},
... } }`, con `kind` ∈ `"context"|"del"|"add"`. Los índices son **1-based**
(convención Lua). `old_start`/`new_start` apuntan a la primera línea (contexto o
cambio) del hunk en cada lado; `old_count`/`new_count` son cuántas líneas de ese
lado abarca el hunk (contexto+del para old, contexto+add para new). Cuando un
lado **no toca ninguna línea propia** (p. ej. `a` vacío → `b`: todo add; o `b`
vacío: todo del) su `*_start` y `*_count` son **0** — la convención del diff
unificado (0 = posición de inserción al principio). `a == b` → `hunks` vacío
(`#hunks == 0` distingue "sin cambios" sin ambigüedad).

## El render (`opts.render = true`)

`renderDiffBlock` pinta una cabecera `@@ -o,oc +n,nc @@` (estilo `header`,
negrita) por hunk y, debajo, una línea por operación con prefijo `+ `/`- `/`  `
y el estilo del tipo. El theme por defecto (`defaultDiffTheme`, G22): add
**verde** (índice ANSI `"2"` LITERAL), del **rojo** (`"1"`), contexto sin
estilo, header negrita. Colores **literales** (índice o `#rrggbb`), que el
compositor S29 degradará con `caps().colors` (el Block guarda literales, nunca
nombres semánticos). `opts.theme` (claves `add`/`del`/`context`/`header`) valida
cada `Style` con `parseStyle` (un nombre semántico como `"accent"` → `EINVAL`,
G22). Sin hunks → Block vacío válido (una línea en blanco, height 1: un Block
siempre tiene ≥1 línea, como en markdown/highlight).

## `diffContextLines` fijo en 3 (sin perilla en `opts`)

El nº de líneas de contexto NO se expone por `opts`: la firma §10 no lo
contempla y 3 es el estándar de facto del diff unificado. Añadir una perilla
sería ampliar la superficie pública sin necesidad (API sagrada, ADR-003);
reabrible si un consumidor concreto lo pide.

## Tests (`diff_test.go`, table-driven nombrando los bordes 🔒)

`TestComputeDiffEdges` blinda: inserción pura, borrado puro, cambio (del+add),
cambio en la **PRIMERA** línea, cambio en la **ÚLTIMA** línea, `a` vacío → todo
add (rangos old 0,0), `a` → `b` vacío todo del (new 0,0), ambos vacíos sin
hunks, `a == b` sin hunks, una sola línea, **sin newline final == con newline**,
sin-newline última línea cambiada, inserción al principio, append al final, dos
cambios lejanos → 2 hunks, dos cercanos → 1 hunk fusionado. Más
`TestSplitDiffLines` (terminador vs separador, CRLF),
`TestDiffLinesConsistentWithSources` (cada `context`/`del` casa con `a`,
`context`/`add` con `b`), `TestRenderDiffBlock` (prefijos +/-/␣, estilos
verde/rojo/neutro, height = cabecera + ops), `TestRenderDiffBlockEmpty`
(height 1), y vía Lua (`TestDiffLua` hunks inspeccionados/`.height`/sin
render→`block==nil`, `TestDiffLuaErrors` opts no-tabla / `opts.theme` nombre
semántico G22 / theme no-tabla → `EINVAL`, `TestDiffLuaTheme` colores literales
aplicados al render).

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S24). Binario `enu -e` confirma e2e: cambio en medio → 1 hunk
(context/context/del/add/context), block.height = 6, rangos old/new 1,4; `a==b`
→ 0 hunks; `a` vacío → old_count 0 / new_count 2; `opts` no-tabla → `EINVAL`.

## Corrección: umbral de fusión al boundary 2·contexto (off-by-one)

El umbral de fusión de `groupHunks` tenía un off-by-one: la condición era
`next - diffContextLines <= end`, que fundía huecos de contexto ≤ 5 pero
**separaba** un hueco de exactamente 6 (= 2·`diffContextLines`), contradiciendo
el propio comentario de la función y esta entrada ("funde dos cambios separados
por ≤ 2·contexto"). `git diff -U3` y `GNU diff -U3` funden hasta hueco 6 y
separan a partir de 7 (verificado). Cuando los dos bloques de contexto quedan
**adyacentes sin solaparse** (hueco = 6) deben seguir en un solo hunk. Fix
mínimo: `next - diffContextLines <= end + 1` (el `+1` cubre la adyacencia). Los
rangos del hunk fusionado abarcan ambos cambios + todo el contexto intermedio.
Tests de frontera añadidos a `TestComputeDiffEdges` nombrando el caso: hueco de
contexto = 5 → 1 hunk, = 6 → 1 hunk (el que fallaba, con rangos old/new 1,8
comprobados), = 7 → 2 hunks; coherente con `diff -U3`.

**Sin hallazgos:** §10 bastó (`diff(a, b, opts?)` se implementó tal cual; `opts`
usa `render?`/`theme?`). Puntero ▶ avanza a **S26** (`enu.re`).
