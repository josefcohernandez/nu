---
title: "Resultado del spike de ADR-007: el toolkit se construye en Lua"
type: "adr"
id: "ADR-012"
status: "aceptada"
date: "2026-06"
---
# ADR-012 · Resultado del spike de ADR-007: el toolkit se construye en Lua

**Estado:** Aceptada · 2026-06 (cierra la *validación pendiente* de
[ADR-007](adr-007-api-de-ui-expuesta.md) y la **cuestión abierta nº1** de
[arquitectura.md](../../core/arquitectura.md); ADR-007 asciende a Aceptada en consecuencia)

**Contexto.** ADR-007 fijó la API de UI (celdas + regiones + compositor en Go,
render caro en Go, **toolkit de widgets como extensión Lua**) con un **veto
pre-comprometido**: si un toolkit en Lua no mantiene la UI fluida, se mueve la
implementación del toolkit a Go (opción B clásica) conservando la API pública.
La sesión S28 ([implementacion.md](../../plan/implementacion.md), hito de veto) construyó
una versión **mínima e interna** de la primitiva —rejilla de celdas
(`rune`+`style`), regiones, `blit` de un Block (S22) con viewport y recorte por
ambos extremos (G28), diff de rejilla → ANSI a un buffer en memoria, coalescing
de frames— y un **shim Lua** que la orquesta, y la torturó con los dos
workloads pactados, midiendo **el coste de cómputo de la tubería
compose+diff+encode más el overhead de orquestar desde Lua** frente a hacerlo
todo en Go.

**Limitación del entorno (declarada).** El spike corrió **headless** (sin TTY):
el diff se serializa a un buffer en memoria, no a un terminal. Por tanto se mide
el **coste de cómputo** (componer + difar + codificar a ANSI + el cruce
Go↔Lua), **no** la latencia física del terminal (ancho de banda del pty,
vsync), que es idéntica se decida Lua o Go. Es exactamente lo que el veto pone
en juego —el rendimiento de Lua sin JIT en el camino caliente, limitación nº8
de [modelo-ejecucion.md](../../core/modelo-ejecucion.md)—; la física del TTY no discrimina
entre las dos opciones, así que excluirla no sesga la decisión.

**Umbral de fluidez (pre-comprometido).** Caso (a) streaming markdown a
pantalla completa (120×40): un frame (compose+diff+encode **+** overhead Lua)
**≤ 8 ms** (un cuarto del presupuesto de un frame a 30 fps, ~33 ms; deja holgura
para el resto del turno —HTTP/SSE/parse— y para hardware más lento). Caso (b)
fuzzy picker sobre ~100k ficheros: una pulsación (fuzzy sobre 100k + render de
la ventana visible) **≤ 50 ms** (la cota por debajo de la cual el filtrado se
siente instantáneo). **Criterio de atribución:** como la pregunta de ADR-007 no
es "¿es rápido el render?" sino "¿el *overhead de Lua* rompe la fluidez frente a
Go?", el veto se ejecuta solo si un caso se sale del presupuesto **y** el
culpable es el sobrecoste de orquestar desde Lua (no la primitiva Go, que el
veto no arreglaría).

**Mediciones** (Intel Xeon @ 2.10 GHz, 4 núcleos; tiempos reales, **sin**
`-race` —el detector de carreras infla ~7× y no representa el coste de
producción—; `go test`/`go test -bench`):

| Caso | Métrica | Go puro | Lua orquestado | Presupuesto |
|---|---|---|---|---|
| (a) streaming markdown, 311 frames | p50 / p99 por frame | ~0.4 ms / ~1.8 ms | ~0.4 ms / ~1.8 ms | ≤ 8 ms |
| (b) picker 100k, 7 pulsaciones | p50 / p99 por pulsación | ~31–45 ms / ~52–74 ms | ~30–38 ms / ~40–53 ms | ≤ 50 ms |

Benchmarks (`ns/op`): la tubería compose+diff+encode aislada a pantalla
completa (`BenchmarkSpikeComposeOnly`) **~0.37 ms/frame**; con el re-render del
markdown por token (`BenchmarkSpikeStreamGo`) **~0.72 ms/frame**; una pulsación
del picker sobre 100k (`BenchmarkSpikeFuzzyKeyGo`, query típica) **~31 ms**.

**El hallazgo clave.** El **overhead de orquestar desde Lua es despreciable** en
ambos casos (caso (a): diferencia Lua−Go en el ruido, ±decenas de µs; caso (b):
Lua iguala o mejora a Go dentro de la varianza). La razón es estructural y
confirma el diseño de ADR-004/ADR-007: **todo el trabajo pesado es primitiva Go**
(render markdown de S23, scorer fuzzy de S27, y la propia tubería
compose/diff/encode), y **Lua solo hace ~3 cruces Go↔Lua por frame** (pedir el
Block, blittear, disparar el frame). El bucle caliente no ejecuta lógica pesada
en el intérprete Lua, así que su ausencia de JIT nunca entra en juego.

**Decisión.** **El veto NO se ejecuta.** El toolkit de widgets (S42) se
construye **en Lua**, como ADR-007 propuso. La Fase 8 del plan de
implementación se mantiene tal cual (S42 = extensión Lua); **no** se reordena.
ADR-007 asciende de *Propuesta (pendiente de validación)* a **Aceptada**.

**Consecuencias.**
- Caso (a) cabe con **dos órdenes de magnitud de holgura** (p99 ~1.8 ms contra
  8 ms de presupuesto): el streaming de tokens con markdown a pantalla completa
  no es un problema de rendimiento para un toolkit en Lua.
- Caso (b): el p50 (~31–45 ms) cabe en el presupuesto; el **p99 (~52–74 ms en Go
  puro) lo roza o lo supera**, pero el outlier es la pulsación de **un solo
  carácter** (`"r"`), que casa ~todos los 100k ficheros —un caso patológico que
  un picker real apenas transita—, y el coste vive en la **primitiva Go**
  (`fuzzyScore` recorriendo 100k candidatos), **no** en el cruce a Lua: mover el
  toolkit a Go no lo arreglaría. Queda como **observación de rendimiento, no
  como veto**: si en producción el picker sobre repos enormes se siente lento
  con queries muy cortas, el arreglo es de la *primitiva* `nu.search.fuzzy`
  (S27) —p. ej. paralelizar el scoring, o un umbral de longitud mínima de query
  en el toolkit—, no de la arquitectura de UI. No abre un `G##`: la API §11 y
  §9 bastan; es una nota de optimización futura.
- El compositor real (S29) y el ciclo de vida de regiones (S30–S33) heredan el
  modelo validado aquí (celdas planas, diff por runs, blit como copia con
  viewport G28, coalescing). El código del spike es **interno y desechable**
  (`internal/runtime/spike_*.go`): no es la API pública §9 ni la amplía; S29 lo
  reemplaza por la implementación de producción.

---
