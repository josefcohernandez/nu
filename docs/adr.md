# Registro de decisiones técnicas (ADR)

Formato ligero: contexto → decisión → consecuencias. Una entrada por decisión,
numeradas, nunca se reescriben: si una decisión cambia, se añade una nueva que
la reemplaza (supersede).

Estados: **Aceptada** · **Propuesta** · **Abierta** (aún sin decisión) ·
**Reemplazada por ADR-NNN**.

---

## ADR-001 · Go como lenguaje del core

**Estado:** Aceptada · 2026-06

**Contexto.** El proyecto nace como reacción al dependency hell de JS/TS en los
harnesses actuales. Necesitamos: binario único sin runtime, cross-compile
trivial, buen soporte de concurrencia (streaming SSE, subprocesos, UI
concurrente) y velocidad de iteración alta mientras la API de extensiones está
en flujo. Candidatos evaluados: Go, Rust, Zig, C.

**Decisión.** Go, con `CGO_ENABLED=0`.

**Razonamiento.**
- Binario estático y cross-compile resuelven la distribución (la antítesis de
  npm).
- El trabajo real del harness (IO concurrente) es el punto fuerte de Go.
- Prior art directo: Crush (Charm) y la TUI original de OpenCode son Go.
- Rust (ratatui + mlua) fue el segundo candidato serio; se descarta por
  velocidad de iteración en fase de diseño, no por capacidad. Codex CLI
  (reescrito de TS a Rust) valida que ambos caminos funcionan.
- Zig/C descartados: meses de infraestructura que Go/Rust regalan.

**Consecuencias.** Renunciamos a LuaJIT embebido (requeriría cgo). El
rendimiento del scripting queda acotado por gopher-lua → refuerza ADR-004.

---

## ADR-002 · Lua (gopher-lua) como lenguaje de extensión

**Estado:** Aceptada · 2026-06

**Contexto.** La extensibilidad es el producto. Candidatos: Lua (gopher-lua o
LuaJIT/cgo), Starlark, Risor/Tengo, JS vía goja, WASM.

**Decisión.** Lua 5.1 embebido vía gopher-lua (Go puro).

**Razonamiento.**
- Lua está culturalmente probado como lenguaje de extensión (Neovim, wezterm,
  mpv, hammerspoon): la familiaridad del usuario es una feature.
- gopher-lua mantiene el binario estático sin cgo (coherente con ADR-001).
  LuaJIT daría rendimiento real pero rompe el cross-compile y el binario único.
- Starlark: paralelizable pero deliberadamente limitado (sin while ni
  recursión); incompatible con "Lua puede hacer cualquier cosa".
- goja (JS): mismo modelo monohilo, y reintroduce la cultura que evitamos.
- WASM: sandboxing y multi-lenguaje, pero DX de autoría muy inferior a 30
  líneas de Lua. Se reconsiderará solo si el sandboxing de terceros se vuelve
  requisito duro.

**Consecuencias.** Lua 5.1 (no 5.4). Rendimiento de intérprete: el trabajo
pesado debe vivir en primitivas Go (ADR-004). gopher-lua no es thread-safe →
condiciona todo el modelo de concurrencia.

---

## ADR-003 · Core mínimo: el agente y MCP son extensiones oficiales

**Estado:** Aceptada · 2026-06

**Contexto.** Dos modelos posibles: core-con-hooks (Neovim: el programa
principal en nativo, extensiones decoran) o kernel-runtime (Emacs/Textadept:
el programa entero escrito en el lenguaje de extensión sobre un kernel de
primitivas).

**Decisión.** Kernel-runtime. El core Go no contiene lógica de agente, MCP,
chat ni comandos: todo eso son extensiones Lua oficiales, embebidas en el
binario con `go:embed` pero sin ningún privilegio arquitectónico.

**Razonamiento.**
- "Lua puede hacer cualquier cosa" exige que las features oficiales sean
  construibles con la API pública; si no, la API está incompleta. Dogfooding
  estructural (como pi con sus propias features).
- El usuario radical no hace fork: sustituye extensiones.
- `go:embed` preserva la experiencia batteries-included.

**Consecuencias.** La superficie de primitivas del kernel crece (HTTP/SSE,
spawn con streams, UI completa): el core conceptualmente mínimo necesita una
stdlib grande. La estabilidad de la API core se vuelve crítica desde v1: los
breaking changes nos rompen a nosotros primero y al ecosistema después.

---

## ADR-004 · Modelo de concurrencia híbrido ("modelo del navegador")

**Estado:** Aceptada · 2026-06

**Contexto.** Un agente es inherentemente concurrente (stream de tokens, tool
calls paralelas, input de UI simultáneos). gopher-lua no es thread-safe. El
modelo Neovim (todo en un hilo) produce los cuelgues con trabajo pesado que
queremos evitar. Alternativas evaluadas: (1) estado único + event loop, (2)
actores puros con paso de mensajes por extensión, (3) extensiones como
subprocesos, (4) cambiar de runtime (Starlark/WASM).

**Decisión.** Híbrido de tres patas:
1. Estado Lua principal single-threaded con event loop y async por coroutines
   (patrón Node/libuv/`vim.uv`) para UI, hooks y orquestación.
2. Workers explícitos (`worker.spawn()`): estados Lua adicionales en
   goroutines propias, sin memoria compartida, paso de mensajes.
3. Primitivas Go paralelas por dentro para todo lo universalmente pesado
   (búsqueda, diff, parsing, highlighting, markdown).

Regla de oro: **Lua decide, Go ejecuta**.

**Razonamiento.**
- Un harness no es un editor: no mantiene buffers gigantes resaltados a cada
  tecla. Sus tareas pesadas son delegables a primitivas paralelas.
- El monohilo en el estado principal es una feature (determinismo, cero data
  races) para el 95% de los plugins; el 5% restante tiene workers opt-in.
- Subprocesos como modelo principal: latencia inaceptable para hooks de UI y
  reintroduce fricción de distribución (queda como Capa 2).
- Es el modelo ya validado por la plataforma web y por Luau (actores de
  Roblox).

**Consecuencias.** Hay que construir el equivalente a "luv para Go" (event
loop + puente de coroutines): el mayor coste de ingeniería inicial del core.
Markdown/highlighting entran al kernel como builtins por rendimiento, violando
conscientemente la pureza del kernel mínimo. Queda abierta la granularidad de
aislamiento (ADR-008).

---

## ADR-005 · Providers de LLM: registro en TOML + adaptadores en Lua

**Estado:** Aceptada · 2026-06

**Contexto.** Los providers difieren en protocolo (SSE, tool calls, system
prompts, thinking blocks): eso es código. Pero endpoints, claves, modelos y
límites son datos. ¿Dónde vive cada cosa?

**Decisión.** TOML declara el registro (datos); los adaptadores de protocolo
son extensiones Lua oficiales (código). El kernel solo aporta la primitiva
HTTP/SSE.

**Razonamiento.**
- Coherente con ADR-003: implementar protocolos en el core contradiría el
  kernel mínimo.
- Parsear SSE en Lua es viable: texto a velocidad de lectura humana.
- Añadir un provider raro (Ollama, vLLM, proxy corporativo) pasa a ser un
  fichero Lua, sin recompilar ni esperar release.
- La configuración del usuario común sigue siendo declarativa y simple (TOML).

**Consecuencias.** El cliente HTTP del kernel debe exponer streaming de
respuesta de primera clase desde v1.

---

## ADR-006 · TUI: librería del kernel

**Estado:** Propuesta · 2026-06

**Contexto.** Candidatos en Go: Bubble Tea + Lipgloss (+ glamour para
markdown) o tview. La elección está acoplada a ADR-007 (qué API de UI se
expone a Lua): el kernel podría incluso usar primitivas de terminal propias.

**Decisión (provisional).** Bubble Tea + Lipgloss como punto de partida, a
revisar cuando se cierre ADR-007.

**Consecuencias.** Ninguna irreversible mientras la API Lua de UI no exponga
conceptos de Bubble Tea directamente (no debería: la API pública es nuestra,
la librería es detalle de implementación).

---

## ADR-007 · API de UI expuesta a Lua

**Estado:** Aceptada · 2026-06 (la *validación pendiente por spike* la cerró el
spike de S28 sin ejecutar el veto: [ADR-012](#adr-012--resultado-del-spike-de-adr-007-el-toolkit-se-construye-en-lua))

**Contexto.** Si la UI de chat es una extensión (ADR-003), la API de UI debe
ser lo bastante rica para construirla entera desde Lua. Opciones evaluadas:
(A) buffers y ventanas estilo Neovim, (B) árbol de widgets retenido en el
core, (C) superficie de celdas inmediata. Análisis:

- **A (buffers)**: modelo conocido por la audiencia y buena composición, pero
  la UI de un harness no es texto plano — mapear chat, tool calls colapsables
  y diffs a buffers es la misma contorsión (extmarks, virtual text) que sufren
  los chats-en-Neovim de los que huimos. Descartada.
- **B (widgets en core)**: el mejor encaje con la UI de un harness y el mejor
  rendimiento con gopher-lua (Lua muta nodos, Go hace layout/diff/render),
  pero el mayor riesgo del proyecto: congelar mal un framework de GUI dentro
  de la API sagrada del core, y la opción más opinionada (tensión con "Lua
  puede hacer cualquier cosa").
- **C (celdas)**: API de core mínima y trivial de congelar, máxima coherencia
  filosófica, pero el peor rendimiento (Lua dentro del bucle de render, sin
  JIT) y sin composición entre plugins de serie.

**Decisión.** Síntesis B+C, en serie: cada opción neutraliza el peor defecto
de la otra.

1. **Primitiva del core: celdas + regiones + compositor en Go.** No solo "pon
   un carácter en (x,y)": regiones con z-order, blit de bloques pre-rendidos y
   damage tracking. El compositor, el diffing y el pintado viven en Go.
2. **El render caro es primitiva Go** (módulo `text`): markdown → líneas
   estilizadas, wrapping, medición de anchos. Lua coloca bloques, no celdas,
   en los caminos calientes.
3. **El toolkit de widgets es una extensión Lua oficial**, internamente
   retenida (mantiene su árbol, solo recalcula nodos sucios). Aporta slots,
   focus y composición entre plugins. Se versiona aparte del core: puede
   iterar y romperse antes de su 1.0 sin tocar la API sagrada.
4. **Coalescing en el core**: los cambios se agrupan y se repinta como mucho
   cada ~30 ms (la UI repinta por eventos, no a 60 fps).

Es el patrón de ADR-003 aplicado por segunda vez: el core no sabe lo que es
un widget; si el toolkit no se puede construir bien sobre las celdas, la
primitiva está incompleta.

**Validación pendiente (criterio de veto pre-comprometido).** Spike: primitiva
de celdas/regiones + compositor + toolkit Lua mínimo (contenedor, texto,
input, lista), torturado con dos casos: (a) streaming de tokens a pantalla
completa con markdown, (b) fuzzy picker sobre ~100k ficheros (filtrado como
primitiva Go, Lua solo repinta lo visible). Si el toolkit Lua no mantiene
ambos fluidos, **fallback**: mover la implementación del toolkit a Go (opción
B clásica) *conservando la misma API pública* de cara a las extensiones — el
diseño de la API del toolkit no se tira. Al pasar el spike, esta ADR asciende
a Aceptada.

**Consecuencias.** El éxito del ecosistema depende de que el toolkit oficial
sea bueno desde el día uno (las extensiones heredarán su calidad). La API v1
congelada es solo la pequeña (celdas/regiones/input/text). UIs alternativas
(incluso una de buffers estilo Neovim) pueden coexistir como extensiones que
compiten con el toolkit oficial. Refuerza ADR-006: la librería TUI de Go queda
como detalle de implementación del compositor.

---

## ADR-008 · Granularidad de aislamiento: workers por tarea, estado principal compartido

**Estado:** Aceptada · 2026-06

**Contexto.** Con ADR-004 decidido, queda la pregunta fina: ¿el aislamiento es
opt-in por tarea (todas las extensiones comparten el estado principal y lanzan
workers efímeros cuando lo necesitan) o por plugin (cada extensión vive
permanentemente en su propio actor)? Afecta a: composabilidad entre plugins
(que se requieran unos a otros), contención de fallos, latencia de hooks
síncronos de UI y complejidad de la API.

**Decisión.** Per-task: todos los plugins comparten el estado principal por
defecto; el aislamiento es opt-in por tarea vía `worker.spawn()`. Con tres
reglas:

1. **Los workers no tienen acceso al módulo `ui`.** La pantalla solo se pinta
   desde el estado principal (como los Web Workers respecto al DOM). El worker
   devuelve resultados por mensaje y el estado principal actualiza la UI.
2. **Watchdog con cancelación.** Cada handler en el estado principal tiene un
   presupuesto de tiempo; si lo excede, el core lo aborta vía cancelación por
   contexto de gopher-lua y marca el plugin como sospechoso/deshabilitable.
3. **`pcall` en cada frontera de hook.** Un error en un plugin nunca tumba el
   event loop ni afecta a los demás plugins.

Los mensajes entre worker y estado principal son **copias** (las tablas Lua no
cruzan estados): un worker debe devolver resultados digeridos, no datos crudos
masivos.

**Razonamiento.**
- La composabilidad es el ingrediente secreto del ecosistema Neovim: plugins
  que se `require` entre sí, librerías-plugin (plenary), extensiones de
  extensiones (telescope). Con actores aislados, "usar otro plugin" sería RPC
  asíncrono con serialización — no se pueden pasar closures por un channel — y
  ese ecosistema no puede nacer.
- Los hooks síncronos (keymaps, render) necesitan respuesta inmediata; con
  actores serían round-trips bloqueantes con riesgo de deadlock, o todo hook
  se volvería async.
- Actores por plugin: N estados = N stdlibs en memoria, copias en cada
  frontera, API más difícil para el plugin de 20 líneas.
- El watchdog + pcall cubren la mayor parte del hueco de robustez: contención
  de errores y de bucles infinitos (más de lo que Neovim ofrece de serie).

**Consecuencias.** Riesgos aceptados conscientemente: un memory leak en un
plugin infla el proceso entero, y el watchdog no protege de la "muerte por mil
cortes" (muchos handlers lentos pero bajo presupuesto). Los actores por plugin
quedan como posible evolución futura (p. ej. `isolated = true` en el manifest
para plugins no confiables), pero no en v1: dos modos de ejecución duplican la
semántica de cada hook. La regla workers-sin-UI simplifica ADR-007: solo el
estado principal pinta, así que el modelo de UI no necesita ser thread-safe ni
multiplexar autores concurrentes.

---

## ADR-009 · Convenciones de la API: namespace global, async por corrutinas, errores estructurados

**Estado:** Propuesta · 2026-06 (se acepta al congelar [api.md](api.md))

**Contexto.** Antes de escribir código se define formalmente la API v1
([api.md](api.md)). Tres decisiones transversales necesitan registro propio.

**Decisión.**

1. **Namespace global `nu`** con submódulos (`nu.fs`, `nu.ui`, ...), como el
   global `vim` de Neovim; `require` queda para módulos de plugins. La stdlib
   bloqueante de Lua (`io`, `os.execute`, ...) se deshabilita: todo IO pasa
   por las primitivas async del core o congelaría el event loop.
2. **Async por funciones suspendientes**: dentro de una task (corrutina del
   scheduler), las primitivas de IO se llaman en estilo secuencial y
   suspenden hasta completarse (await implícito, patrón cosockets de
   OpenResty). Los handlers síncronos (input, eventos) no pueden suspender:
   lanzan tasks. Sin callbacks anidados ni promesas explícitas en la API.
3. **Errores estructurados lanzados** (`error({code, message, detail})`,
   capturables con `pcall`) en lugar del estilo `res, err`. Códigos
   reservados (`ENOENT`, `ETIMEOUT`, `ECANCELED`, `EBUDGET`, ...). Razón:
   los errores que se lanzan componen a través de capas de extensiones y no
   se ignoran en silencio; `res, err` se pierde al primer descuido.

**Consecuencias.** La DX de plugin trivial es código secuencial sin
conceptos async visibles. Deshabilitar `io`/`os` rompe compatibilidad con
librerías Lua puras que los usen (asumido: el ecosistema objetivo escribe
contra `nu.*`). El puente corrutinas↔goroutines del scheduler es la pieza
central del kernel (coherente con ADR-004).

---

## ADR-010 · Extensiones oficiales: distribuidas con nu, no activas por defecto

**Estado:** Aceptada · 2026-06 (modifica una consecuencia de ADR-003 y el
principio 5 de la filosofía)

**Contexto.** ADR-003 decidió embeber las extensiones oficiales
(`go:embed`) "preservando la experiencia batteries-included", lo que
implicaba activarlas por defecto. Al resolver G6 (paquetes de caps como
tablas de la extensión del agente) se reabrió la pregunta y se decidió un
modelo más austero.

**Decisión.** Las extensiones oficiales (agente, chat, providers, MCP,
toolkit, paquetes `agent.caps.*`) **no se activan por defecto**: se
distribuyen con nu, pero las activa quien las quiere. Activación explícita
y trivial (config o primer arranque, una tecla). Distribución: siguen
embebidas en el binario — inactivas — para no romper la promesa "un
binario, offline" (activar no requiere red).

**Razonamiento.** Coherencia radical con "el core no sabe lo que es un
agente": tampoco lo presupone. nu instalado es un runtime desnudo; el
harness es una elección del usuario, no un hecho consumado. Mismo modelo
mental que Neovim (el editor no trae plugins activados) — el público
objetivo lo espera así.

**Consecuencias.** El primer arranque debe ofrecer la activación del
conjunto oficial (sin eso, la primera experiencia sería una pantalla
vacía); el "agente funcionando en el primer minuto" pasa de automático a
"a una tecla de distancia". La filosofía §5 se reescribe. `nu.toml` pasa
de `plugins.disabled` a gobernar la activación (`plugins.enabled` o
equivalente — detalle del loader).

---

## ADR-011 · Realización del scheduler: goroutine-por-task + token de ejecución Lua

**Estado:** Aceptada · 2026-06 (refina *cómo* se realiza ADR-004 sobre
gopher-lua; no cambia su semántica observable ni la API de [api.md](api.md))

**Contexto.** ADR-004 fijó el "modelo del navegador" (estado Lua principal
single-threaded, async por await implícito) y anticipó como mayor coste "el
puente de corrutinas" (event loop + coroutines-Lua ↔ goroutines). Al
implementar la quilla (S04) se descubrió una grieta del runtime
(problemas.md G31): **gopher-lua —semántica Lua 5.1— no permite que una
corrutina ceda (`yield`) a través de una frontera de llamada Go.** En
concreto, verificado contra gopher-lua v1.1.2:

1. `pcall(fn)` donde `fn` suspende: la corrutina **se aborta** en el `pcall`
   en vez de ceder. Pero [api.md](api.md) §1.4 promete que los errores
   estructurados "se capturan con `pcall`", y el pseudocódigo
   ([pseudocodigo.md](pseudocodigo.md) §§ tool runner, ramas paralelas)
   envuelve en `pcall` operaciones que hacen IO (⏸). El modelo de errores
   entero se apoyaba en algo que el runtime no soporta.
2. `return ⏸fn()` en posición de cola: el `OP_TAILCALL` elide el frame del
   llamante *antes* de que la función Go ceda, perdiendo la continuación; la
   task "termina" en vez de suspenderse.

Ambas tienen la misma raíz (el `yield` de corrutina no cruza fronteras Go) y
no se arreglan en la espec: la API es correcta, lo que falla es la *técnica*
de realización del puente.

**Decisión.** Realizar el scheduler **sin yields de corrutina**: una
**goroutine por task** + un **único token de ejecución Lua** ("GIL"):

1. Cada task corre en su propia goroutine, sobre su propio thread Lua
   (`*lua.LState` hijo del principal; comparten globales `G`).
2. Un token (canal de capacidad 1) garantiza que **solo una goroutine toca
   Lua a la vez** — el invariante single-threaded de ADR-004/008.
3. Una primitiva ⏸ no cede una corrutina: **suelta el token**, hace el
   trabajo bloqueante en una goroutine de fondo (que jamás toca Lua) y, al
   terminar, **recupera el token** y retorna con normalidad.

Como no hay yield, la pila Lua de la task vive en su pila Go: `pcall`, las
tail calls y el desenrollado de errores son los **nativos** de gopher-lua y
sobreviven a la suspensión. `Task:await` pasa a ser una función Go pura que
relanza el error de la task esperada con `L.Error` (capturable con `pcall`).

**Razonamiento.** Es la otra realización canónica del "modelo del navegador"
sobre un runtime Lua-en-Go (el patrón "giant lock" cooperativo). Mata las dos
grietas en la raíz en vez de parchearlas (trampolines Lua para la cola, y un
`pcall` rendido como sub-corrutina para el caso 1 — ambos más invasivos y aun
así frágiles). La semántica observable de ADR-004 se conserva intacta: Lua de
un hilo lógico, await implícito, cero data races (ahora por el token, con
handoff por canal = *happens-before*; validado con `-race`).

**Consecuencias.** El "event loop + cola de eventos" de ADR-004 se realiza
como token + goroutines, no como un bucle que reanuda corrutinas; la
descripción de S04 en [implementacion.md](implementacion.md) se lee con esa
lente. El coste por task sube de una corrutina a una goroutine (+ un thread
Lua) — barato en Go y aceptable para el volumen de tasks de un harness. La
detección de "estoy en una task" (para vetar ⏸ fuera de task, §1.3) es por
estado de ejecución: el chunk principal y los handlers síncronos corren sobre
el estado `host`; las tasks, sobre su propio thread. Las piezas que
presuponían un bucle central que reanuda corrutinas (timers de S05, despacho
de eventos de S10) se construyen sobre este modelo: un "tick del loop" es
trabajo que toma el token en el estado principal. El **watchdog** de S09
(presupuesto por slice) y el **desenrollado no capturable** de S08
(cancelación/`EBUDGET` que `pcall` no atrapa) se diseñan ya sabiendo que
`pcall` es el nativo de gopher-lua: el aborto no capturable necesitará un
mecanismo propio (un panic centinela que el kernel reconozca y no deje que el
`pcall` de usuario se trague), no el `yield` que aquí se descarta.

---

## ADR-012 · Resultado del spike de ADR-007: el toolkit se construye en Lua

**Estado:** Aceptada · 2026-06 (cierra la *validación pendiente* de
[ADR-007](#adr-007--api-de-ui-expuesta-a-lua) y la **cuestión abierta nº1** de
[arquitectura.md](arquitectura.md); ADR-007 asciende a Aceptada en consecuencia)

**Contexto.** ADR-007 fijó la API de UI (celdas + regiones + compositor en Go,
render caro en Go, **toolkit de widgets como extensión Lua**) con un **veto
pre-comprometido**: si un toolkit en Lua no mantiene la UI fluida, se mueve la
implementación del toolkit a Go (opción B clásica) conservando la API pública.
La sesión S28 ([implementacion.md](implementacion.md), hito de veto) construyó
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
de [modelo-ejecucion.md](modelo-ejecucion.md)—; la física del TTY no discrimina
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

## ADR-013 · Integración continua y publicación de releases

**Estado:** Aceptada · 2026-06

**Contexto.** Cerradas las 45 sesiones del [plan de
implementación](implementacion.md), el kernel y las extensiones oficiales son
código real (un binario Go más `internal/runtime`). Hasta ahora la disciplina de
calidad vivía solo en el protocolo de [CLAUDE.md](../CLAUDE.md) —"toda sesión
deja `go build ./...` verde", el inventario 🔒 de tests obligatorios— y se
ejercía a mano en cada sesión. No había integración continua, ni linting
configurado, ni mecanismo para distribuir el binario. Esta decisión registra el
**cómo se valida y se publica `nu`**. Es DevOps del operador: la implementación
(los `.github/workflows/*.yml`) NO es parte de la API sagrada ([api.md](api.md))
ni de los contratos de extensión; este ADR captura las *decisiones*, no los
*steps* del YAML. Encaja donde ya viven ADR-001 (Go, `CGO_ENABLED=0`) y ADR-010
(extensiones embebidas inactivas), que describen la distribución sin haber fijado
su tubería.

**Decisión.**

1. **CI** (`.github/workflows/ci.yml`) en cada PR y push a `main`: formato
   (`gofmt`), `go vet`, módulos limpios (`go mod verify` + `tidy` sin diff),
   `golangci-lint` (conjunto mínimo, ver punto 5), `go build ./...`, build del
   binario estático con las flags de release, un **smoke test headless**
   (`nu -e 'return nu.version.api'`, sin secretos) y `go test -race` sobre una
   **matriz `ubuntu` + `macos`** (las dos plataformas objetivo v1). `-race`
   siempre: el inventario 🔒 incluye tests de concurrencia (S07–S10) que solo
   destapan data races bajo el detector. Sin matriz de versiones de Go: `nu` se
   distribuye como binario, no como librería que terceros compilan; la versión
   que importa es la de `go.mod`, leída con `go-version-file`.

2. **Releases** (`.github/workflows/release.yml`) al pushear un tag `vX.Y.Z`:
   cross-compila a **`linux/amd64`, `linux/arm64`, `darwin/amd64`,
   `darwin/arm64`**, empaqueta un `tar.gz` por plataforma más un `checksums.txt`
   (SHA256), y crea la GitHub Release con notas autogeneradas. **No** se publica
   Windows nativo: está pospuesto ([pospuesto.md](pospuesto.md) P18) y Windows va
   por WSL2, que usa el binario `linux/amd64`; un `.exe` daría falsa señal de
   soporte.

3. **Versionado — estrategia "constantes como fuente de verdad".** La versión
   vive en las constantes de `internal/runtime/nu.go` (`VersionMajor/Minor/Patch`,
   expuestas como `nu.version`). El release **no inyecta** la versión por
   `-ldflags -X`: la **verifica** contra el tag en un job-gate y aborta si
   divergen. El gate lee la versión **ejecutando el runtime**
   (`go run . -e '…nu.version…'`), no con un `grep` del fichero: usa la misma
   lógica de composición (`registerNu`) que el binario real, así que valida
   exactamente lo que verá el usuario, sin fragilidad ante el orden de las
   constantes.

4. **Contrato de build reproducible.** Todos los binarios se compilan con
   `CGO_ENABLED=0` (estático, ADR-001), `-trimpath` (sin rutas de la máquina de
   CI → reproducible) y `-ldflags "-s -w"` (sin tabla de símbolos ni DWARF →
   binario más pequeño; ~12 MB).

5. **Herramientas: lo mínimo.** Los workflows invocan `go` directamente y crean
   la release con una action estándar (`softprops/action-gh-release`); **no** se
   adopta GoReleaser. `golangci-lint` se incluye con un conjunto deliberadamente
   pequeño (`govet`, `errcheck`, `staticcheck`, `ineffassign`, `unused`) y
   `only-new-issues: true`, para no bloquear por deuda preexistente.

**Razonamiento.**
- **Estrategia A vs inyección por `-ldflags`.** Inyectar crearía dos fuentes de
  verdad (la constante Lua y la variable de `main`) que habría que mantener
  sincronizadas, y obligaría a meter una variable mutable en `main` y un flag
  `--version` por una razón puramente de empaquetado. La estrategia elegida tiene
  **una sola fuente de verdad**, no muta código en build (lo publicado es
  bit-a-bit lo del repo, reforzando `-trimpath`) y es coherente con "Lua decide,
  Go ejecuta": `nu.version` ya es la verdad observable; el packaging deriva de
  ella. Las constantes **no** son parte de la superficie sagrada (viven en
  `internal/runtime`, no en `api.md`): el gate las *lee*, no las amplía, así que
  no roza el protocolo de §4.
- **A mano vs GoReleaser.** El alcance es pequeño y estable (4 targets, 1
  binario, sin paquetes nativos ni brew tap ni Docker). GoReleaser metería una
  herramienta externa con su propia versión, config y "magia" —justo lo que la
  [filosofía §6](filosofia.md) ("cero dependency hell") evita en el producto y
  conviene evitar también en su tubería—. El workflow a mano cabe en YAML legible
  y no añade nada que mantener. Si en el futuro se añaden Homebrew tap, paquetes
  nativos o imágenes Docker, se reabre esta elección.

**Consecuencias.**
- El protocolo de [CLAUDE.md](../CLAUDE.md) ("build verde", inventario 🔒) deja
  de depender solo de la diligencia manual: la CI lo exige en cada PR. El
  `tidy`-check materializa "cero dependency hell" como gate automático.
- **Publicar implica subir la versión a mano antes del tag.** El flujo es: editar
  las constantes en `nu.go`, commit, tag `vX.Y.Z`, push. Si el tag no coincide,
  el release falla en el gate con un mensaje accionable y no publica nada. Es una
  fricción deliberada (una verificación, no un automatismo que adivine).
- **macOS en la matriz cuesta más minutos** que Linux. Para un repo de un solo
  desarrollador y bajo volumen de PRs el coste absoluto es pequeño y se acepta a
  cambio de cubrir el segundo OS objetivo; si el gasto importara, la palanca es
  dejar macOS solo en `push: main`. Para *compilar* los binarios darwin del
  release **no** hace falta runner macOS (el cross-compile de Go corre en Linux);
  macOS en CI es solo para *ejecutar* los tests nativamente.
- **Licencia:** resuelta en [ADR-014](#adr-014--licencia-apache-20) (Apache 2.0).
  Los `tar.gz` del release incluyen el binario; el `LICENSE` y el `NOTICE` viven
  en la raíz del repo.
- **Pendiente del dueño del proyecto, fuera de este ADR:** un flag `--version` en
  el CLI sería un nice-to-have de producto (toca la superficie CLI de S45), no un
  requisito de esta tubería; firmar binarios (cosign/GPG), brew tap y Docker
  quedan como mejoras futuras que reabrirían el punto 5.

---

## ADR-014 · Licencia: Apache 2.0

**Estado:** Aceptada · 2026-06

**Contexto.** El kernel ya es código real y se va a distribuir (ADR-013), pero el
repo no tenía licencia: sin ella, legalmente nadie puede usar ni redistribuir
`nu`. El autor quiere dos cosas a la vez, en apariencia en tensión: (1) que sea
**open source de verdad**, para aportar a la comunidad y maximizar adopción, y
(2) conservar la opción de **comercializarlo o venderlo** en el futuro si el
proyecto despega (el patrón de productos como pi/pdf.ai, donde el dueño pudo
vender). La clave —y la razón de que no haya contradicción— es que el poder de
vender/relicenciar **no nace de la licencia, sino de la titularidad del
copyright**: quien posee el 100% del código puede siempre, además de publicarlo
con una licencia abierta (que es no exclusiva), ofrecer una licencia propietaria
o ceder el proyecto entero. El riesgo a esa titularidad no es la licencia
elegida, sino **aceptar código de terceros sin cesión de derechos**.

Sobre la autoría: el único autor de `nu` es **Diego Barea**. La identidad
`Candela1011 <candelabr72@gmail.com>` que aparece en el historial de git no es un
segundo autor: es el `git config` que quedó en el ordenador prestado; no hay
co-titularidad. Se fijó la identidad del repo a nombre del autor para que el
rastro de autoría sea coherente.

**Decisión.** **Apache License 2.0**, copyright de Diego Barea. Se añaden a la
raíz: `LICENSE` (texto íntegro de Apache 2.0), `NOTICE` (atribución que la
licencia recomienda) y `CONTRIBUTING.md`. Las aportaciones externas se gestionan
**caso por caso, sin CLA formal por ahora**, pero `CONTRIBUTING.md` **reserva
expresamente** el derecho del mantenedor a pedir cesión de derechos o un acuerdo
de contribución antes de fusionar código de terceros. Así la titularidad se
mantiene unificada y la opción de comercializar sigue viva, sin imponer todavía
la fricción de un CLA.

**Razonamiento.**
- **Por qué permisiva y no copyleft (AGPL/GPL).** El objetivo es adopción amplia
  y "dar a la comunidad". Una AGPL volvería `nu` copyleft viral (quien lo corra
  modificado como servicio debe publicar sus cambios), lo que **reduce** la
  adopción y se usa cuando se quiere *forzar* compradores comerciales de forma
  continua —no es el caso—. Para la meta "vendible algún día" basta con la
  titularidad; una permisiva no se la quita.
- **Por qué Apache 2.0 y no MIT.** Ambas son permisivas y ambas preservan el
  derecho a vender. Apache 2.0 añade una **concesión explícita de patentes**
  (protege al autor y a los usuarios si esto se vuelve un negocio) y una cláusula
  de contribución (§5) que encaja con un futuro CLA. El coste es un `LICENSE` más
  largo y un `NOTICE`; merece la pena para un producto con ambición comercial.
- **Por qué sin CLA todavía.** Hoy el autor posee el 100% y puede vender sin
  pedir permiso a nadie; un CLA solo hace falta cuando entra código ajeno. Montar
  el CLA ahora sería fricción prematura. La cláusula de `CONTRIBUTING.md` evita
  el riesgo real (que alguien asuma que su PR entra con su copyright intacto)
  manteniéndolo barato.

**Consecuencias.**
- `nu` es libre para usar, estudiar, modificar y distribuir (incluso
  comercialmente) bajo Apache 2.0; la CI y el release ya pueden publicar con una
  licencia válida.
- El autor conserva la titularidad y, por tanto, la capacidad de ofrecer una
  versión propietaria o vender el proyecto. **Disparador de reapertura:** si el
  volumen de contribuciones externas crece, formalizar un CLA (texto + bot tipo
  CLA-assistant) para no tener que negociar cesiones una a una; el marco ya está
  anunciado en `CONTRIBUTING.md`.
- Si en el futuro se crea una entidad/empresa para comercializar `nu`, se
  actualiza el nombre del copyright; no requiere cambiar de licencia.
- No se añaden cabeceras de licencia por fichero `.go` (el `LICENSE` en la raíz
  basta para Apache 2.0 en un módulo de un solo titular); si algún día se acepta
  código de terceros, se revisará por si conviene marcar autoría por fichero.
