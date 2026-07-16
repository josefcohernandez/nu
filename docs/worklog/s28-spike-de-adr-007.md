# S28 — SPIKE de ADR-007 (compositor + toolkit Lua mínimos; HITO DE VETO)

S28 no es una feature de la API: es el **hito de veto** que valida ADR-007
(toolkit de widgets en Lua) antes de comprometer la arquitectura de UI. El
resultado y las mediciones se registran formalmente en **ADR-012** (adr.md);
aquí van las decisiones de diseño del spike que no son de espec.

## Alcance: interno y desechable, NO la API pública §9

El spike construye una versión **mínima e interna** de lo que S29 expondrá como
`enu.ui` (celdas, regiones, blit, diff→ANSI), pero **no se cuelga del global
`enu`**: vive en `internal/runtime/spike_compositor.go` (la primitiva) y
`spike_shim.go` (el puente a Lua, registrado solo desde los tests vía
`registerSpikeShim`, NUNCA desde `registerNu`). Así el veto se mide sin congelar
nada ni ampliar la superficie sagrada (api.md intacto, APILevel sigue en 1). S29
reemplaza el spike por el compositor de producción; estos ficheros son
desechables. Si §9 no hubiera bastado para el toolkit habría sido un `G##`, pero
bastó.

## El compositor mínimo (decisiones de implementación)

- **Rejilla plana** (`[]scell` indexado por `y*w+x`, no `[][]scell`) por
  localidad de caché: el diff la recorre entera cada frame. Cada celda es
  `{rune (como string, para no partir un grapheme ancho/ZWJ), *style, width}`.
- **Doble buffer** (back = frame en composición, front = último emitido). El
  back se **reutiliza** entre frames (`clear` in situ, no realloc) para no
  presionar al GC en el camino caliente (un frame por token).
- **`blitBlock` = copia con viewport (G28).** Estampa el `*block` de S22 en la
  región en coordenadas locales `(ox, oy)` que pueden ser **negativas** (offset
  negativo = empezar más abajo/derecha en el Block = scroll); el exceso recorta
  por el borde de la región. Es copia celda a celda, nunca re-render (§9.1): el
  scroll cuesta una copia de la ventana, no recalcular el Block. Graphemes
  anchos (w=2) dejan la celda siguiente como continuación (`r=""`, `w=0`).
- **Diff → ANSI por runs.** Recorre por filas; donde una celda difiere del
  front arranca un *run* con un único move-cursor (`ESC[y;xH`, 1-based) y lo
  extiende mientras siga difiriendo; emite SGR (`ESC[...m`) solo al cambiar el
  estilo respecto a la celda anterior emitida (minimiza bytes). Colores
  literales (§9.2): `#rrggbb`→truecolor (`38;2;r;g;b`), índice→256 (`38;5;n`).
  La degradación fina con `caps().colors` es S29; el coste de construir la
  cadena es del mismo orden.
- **Coalescing:** `frame()` devuelve el nº de celdas cambiadas; 0 cambios = 0
  bytes emitidos (un frame idéntico no produce salida), realizando "la UI
  repinta por eventos, no a 60 fps" (ADR-007).

## El shim Lua mínimo (qué mide)

`__spike.composer/markdown/fuzzy_window` + métodos `Composer:region/begin/frame`
y `Region:blit/fill`. El "toolkit mínimo en Lua" que el veto evalúa **es el
script Lua del benchmark** que orquesta estas primitivas: por frame hace
`begin → fill/blit → frame` (~3 cruces Go↔Lua). `markdown` reusa
`renderMarkdownBlocks` (S23) y `fuzzy_window` reusa `fuzzyScore` (S27) +
construye el Block de la ventana visible (top N) — "el filtrado es primitiva Go,
Lua repinta lo visible".

## El umbral y la metodología del veto (honestidad)

- **Umbral pre-comprometido:** caso (a) streaming markdown 120×40 ≤ **8 ms/
  frame** (¼ del presupuesto de 30 fps, holgura para HTTP/SSE/parse y hardware
  lento); caso (b) picker 100k ≤ **50 ms/pulsación** (cota de "instantáneo").
- **Criterio de atribución (clave):** la pregunta de ADR-007 no es "¿es rápido
  el render?" sino "¿el *overhead de orquestar desde Lua* rompe la fluidez
  frente a Go?". Por eso el veto solo se ejecuta si un caso se sale del
  presupuesto **Y** la causa es el overhead de Lua (no la primitiva Go, que
  mover el toolkit a Go no arreglaría). Se mide Go-puro vs Lua-orquestado y se
  reporta el delta.
- **`-race` NO decide el veto.** El detector de carreras instrumenta cada acceso
  e infla los tiempos ~7× (verificado: caso (b) p99 pasa de ~52 ms a ~354 ms
  bajo `-race`): válido para CORRECCIÓN, inútil para un veto de RENDIMIENTO. Se
  detecta con build tags (`spike_race_{on,off}_test.go` → `spikeRaceEnabled`) y
  bajo `-race` `TestSpikeMeasureVeto` solo reporta números (veto "indeciso", no
  falla). El veredicto firme es la corrida sin `-race`.
- **Limitación headless declarada:** sin TTY el diff va a un buffer en memoria,
  no a un terminal. Se mide el **coste de cómputo** (compose+diff+encode + cruce
  Lua), no la latencia física del pty —que es idéntica se decida Lua o Go, así
  que no sesga la decisión—. Es justo lo que el veto pone en juego.

## El resultado y la observación del picker

**El veto NO se ejecuta:** el overhead de Lua es despreciable en ambos casos
(caso (a) ±decenas de µs; caso (b) dentro del ruido, Lua a veces más rápido)
porque todo el trabajo pesado es primitiva Go y Lua solo cruza ~3 veces por
frame. Toolkit en Lua (S42); Fase 8 sin reordenar; ADR-007 → Aceptada.

**Observación (no veto, no `G##`):** el p99 del caso (b) (~52–74 ms en Go puro)
roza/supera el presupuesto, pero el outlier es la pulsación de **1 carácter**
(casa ~todos los 100k) y el coste vive en `fuzzyScore` (primitiva Go que recorre
100k), **no** en el cruce a Lua. Si en producción molesta, el arreglo es de
`enu.search.fuzzy` (paralelizar el scoring, o umbral de longitud mínima de query
en el toolkit), no de la arquitectura de UI. Se anota en ADR-012 como nota de
optimización futura.

## Verificación

`spike_bench_test.go`: tests funcionales (`-race`) de viewport/scroll (G28),
recorte horizontal, coalescing + damage tracking, SGR, orquestación Lua; +
`TestSpikeMeasureVeto` (imprime p50/p99 de ambos workloads, Go vs Lua, y el
veredicto) + 3 benchmarks. `CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios;
`CGO_ENABLED=1 go test -race -timeout 120s ./internal/...` verde (no regresiona
S01–S27). **APILevel sigue en 1.** Puntero ▶ avanza a **S29** (compositor real).
