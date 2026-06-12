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

**Estado:** Propuesta · 2026-06 (pendiente de validación por spike)

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
