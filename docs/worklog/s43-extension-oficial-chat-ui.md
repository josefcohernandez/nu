# S43 — Extensión oficial `chat` (UI del harness sobre toolkit + agente; streaming markdown); CP-11 (chat.md)

Octava extensión de la Fase 8 y la cara visible de enu. **Lua puro sobre la API congelada**
(ADR-003): el core NO sabe lo que es un chat; es una extensión oficial sin privilegio que
consume la API pública del agente (agente.md), el toolkit de widgets (S42) y el bus de
eventos —una UI de terceros podría hacer lo mismo—. Plugin embebido
`internal/runtime/embedded/chat/` (`plugin.toml` name="chat", `requires=["toolkit","agent",
"providers","sessions"]` —el loader §14 los ordena topológicamente antes—; `init.lua` que
cablea + arranca en TTY; módulos `lua/chat/{init,transcript,input,statusline,commands,
permission}.lua`), INACTIVO por defecto (ADR-010), activable por `enu.toml`
`plugins.enabled=[...,"chat"]`, `source="builtin"`. Implementa chat.md y, junto a S42, cierra
la cuestión abierta nº3 (la API pública del toolkit, ahora consumida de verdad).

## Layout (chat.md §1)

Una `toolkit.app` (S42) cuya raíz es un `stack` con dos capas: (1) una `vbox` con las tres
bandas del chat —**transcript** (`toolkit.text{markdown=true}`, `flex=1`: ocupa el alto
sobrante, desplazable por viewport), **input multilínea** (`chat.input`, `pref_h=3`) y
**statusline** (`hbox` de dos `toolkit.label`, `pref_h=1`)—; y (2) una capa modal
(`modal_layer`, superpuesta) para el diálogo de permisos y los pickers (chat.md §1/§5). La app
crea su región a pantalla completa (`enu.ui.size()`), enruta el input al foco (el editor) y
repinta por nodos sucios (todo de S42, sin tocar el toolkit). El foco arranca en el editor.

## El flujo de streaming markdown (chat.md §2, EL CORAZÓN de S43)

El chat suscribe los eventos `agent:*` del bus del core (api.md §4), **filtrando por la sesión
activa** (G3: `payload.session == self.session.id`; la actividad de otras sesiones va al
contador de la statusline, no al transcript). El render del turno:

- `agent:delta` de tipo `text` → `transcript:append_delta(text)` acumula en el item del
  asistente EN CURSO y `Chat:_refresh_transcript()` vuelca el markdown acumulado al widget
  `toolkit.text` con `set_text`: el widget RE-RENDERIZA el markdown (streaming-safe, S23) y el
  Block CRECE incrementalmente. **Es exactamente el camino caliente de CP-9 (delta → markdown
  acumulado → blit), ahora dentro del chat** —lo pesado (medir/renderizar markdown) es
  primitiva Go en el widget; Lua solo orquesta el bucle de deltas (ADR-012)—.
- `agent:delta` de tipo `thinking` → bloque de razonamiento atenuado (cita markdown; el plegado
  interactivo de chat.md §2 es mejora posterior).
- `agent:message` → `transcript:seal_assistant(message)` SELLA el mensaje con el Message
  canónico del `done` (sustituye los deltas por el render final, providers.md §2.1).
- `agent:tool.start`/`tool.end` → bloques de tool (cabecera con nombre + estado).
- `agent:permission.asked` → diálogo modal (§5).
- `agent:error` → bloque de error.

**Modelo del transcript (`transcript.lua`).** Un único `toolkit.text{markdown=true}` con TODA
la conversación serializada a un string markdown (los "items" —user/assistant/tool/thinking/
error— se renderizan a fragmentos markdown y se concatenan). Es lo más simple que cumple el
criterio de hecho y reusa el viewport/scroll del toolkit tal cual; reconstruir la cadena por
delta es barato (concatenación Lua), lo caro es el render (Go). DESVIACIÓN: un widget-por-
mensaje (para los renderers enchufables de tool results de chat.md §2) es la evolución natural
sobre este mismo modelo (los items ya están separados); v1 los serializa. `chat.renderer` se
expone (registra el render) pero v1 usa el fallback de texto.

## Input multilínea (chat.md §3)

`chat.input` es un widget propio que EXTIENDE el contrato del `toolkit.input` (focusable +
`on_key` + caret) a multilínea —el toolkit deja el catálogo abierto y el multilínea es «la
extensión natural del mismo contrato» (S42)—. El texto se guarda como array de líneas y un
caret `(row,col)` en bytes (v1 ASCII/UTF-8 simple, como el toolkit.input; el editor por
graphemes es posterior). **enter SIN modificador NO lo consume el editor: lo DEJA PASAR**
(devuelve false) para que un keymap global del chat lo recoja como "enviar" (chat.md §3, el
mismo patrón del toolkit.input); **shift+enter / alt+enter** (según terminal, `ev.mods`,
api.md §9.3) insertan línea (lo consume). Flechas/home/end navegan (con salto en bordes);
`↑/↓` EN EL BORDE piden historial (callbacks `on_history_prev/next` que el chat conecta, sin
que el editor sepa de sesiones); pegado multilínea correcto (parte por `\n`). El cursor REAL
multilínea lo coloca el chat con `Region:cursor` sumando `caret_row` (el `toolkit.app` solo
coloca una fila; la colocación multilínea es del chat, no se toca el toolkit).

## Diálogo de permisos (chat.md §5)

Ante `agent:permission.asked` (de la sesión activa) se abre un modal (`chat.permission`, un
widget focusable en el `stack`) con la tool y sus **args completos** (sin truncar lo peligroso,
chat.md §5) y responde con `agent.permission.respond(id, granted)` —la decisión que el turno
espera (agente.md §5, future sin timeout, G3)—. Teclas: `a`/`y` = permitir una vez (true),
`d`/`n`/`esc` = denegar (false). **Cola FIFO, un modal visible** (chat.md §1/§5): varios asks
se encolan; al responder uno se muestra el siguiente. DESVIACIÓN: «permitir SIEMPRE» (añadir
el patrón a la política de la sesión / `agent.toml` global del usuario, chat.md §5) y la
edición del patrón propuesto antes de aceptar requieren escribir `agent.toml` y un editor de
patrón; el agente (S39) no expone aún edición de política en caliente, así que v1 ofrece
una-vez/denegar, MUESTRA el patrón sugerido, y documenta «siempre» como mejora. El contrato del
modal (un widget focusable que responde al ask) queda ejercido.

## Comandos slash (chat.md §4) y statusline (chat.md §6)

`chat.command{name, description, args?, complete?, handler ⏸}` registra comandos; `/` al inicio
del input los despacha (`commands.dispatch`); un comando desconocido se "maneja" mostrando un
error (no se envía al modelo). Builtins (dogfooding, con la misma `chat.command`): `/help`,
`/quit`, `/clear`, `/model [ref]` (con arg: `Session:set_model`, G19; sin arg: lista
`providers.list()`), `/sessions` (lista `sessions.list`), `/compact`. DESVIACIÓN: `/model`/
`/sessions`/`/fork` con PICKER difuso interactivo (chat.md §4) aceptan el argumento por TEXTO
en v1; el picker visual es una capa modal (`toolkit.stack`) — mejora documentada. `/permissions`
y el `/fork` completo dependen de superficie del agente posterior (agente.md §8 deja el disparo
de compactación para después). La **statusline** (`chat.statusline.add{id,side,priority,render}`)
trae segmentos por defecto (modelo · contexto % desde `Session.usage` · coste · cwd · modo de
permisos · contador de asks pendientes de otras sesiones, G3), extensibles por terceros.

## Theming (chat.md §7, G22)

chat NO hardcodea un solo color: el render final del transcript lo hace `enu.text.markdown`
(themable, api.md §10) y los widgets (input/statusline/diálogo) resuelven sus estilos
SEMÁNTICOS (`accent`/`error`/`dim`/`warn`) contra el theme de la app (`toolkit.theme`, G22), que
los convierte a literales al componer. La tabla de atajos por defecto `chat.keys` es pública y
remapeable (chat.md §7).

## Arranque (chat.md §8, G20)

`chat.start(opts?) ⏸ -> Chat` exige `enu.ui` (TTY interactivo): en headless (sin UI,
`enu.has("ui")`=false, G20) es **EINVAL accionable** (nombra "headless"/"ui"; chat.md §8 — para
uso headless está el agente directo). Crea/reanuda la sesión (`agent.session`, con `resume` →
G18: refleja el historial repoblado en el transcript). El `init.lua` solo arranca el chat en
TTY (suscribe `core:ready` y monta la app en una task, porque `start` suspende); en headless
deja el módulo accesible por `require("chat")` sin tocar `enu.ui`. Suscribe `ui:resize` ("tu
región, tu ui:resize", api.md §9.1): rehace el layout al cambiar el tamaño.

## NO amplía api.md (corolario de completitud satisfecho)

`enu.events.on`/`emit` (§4), `enu.ui` (§9, vía el toolkit), `enu.text.markdown` (§10, vía el
toolkit), `enu.task.spawn`/`sleep`/`future` (§4), `enu.has("ui")` (§2/§9), `enu.ui.keymap` (§9.3),
`enu.json` (§12, en el diálogo) + los módulos `toolkit`/`agent`/`providers`/`sessions` bastaron
EXACTOS para el chat. **APILevel sigue en 2; ni una función pública del core de más. Sin
hallazgos `G##`.** El chat usa los códigos del core (`EINVAL`) para sus errores de uso; no
acuña código propio (no le hizo falta).

## CP-11 (dogfooding) — ADAPTACIÓN POR LIMITACIÓN DEL ENTORNO

CP-11 (implementacion.md) pide «una sesión de chat real de extremo a extremo contra un
**provider REAL**». **En este entorno CI NO hay red ni credenciales**, así que el provider real
NO es ejecutable headless. ADAPTACIÓN (como CP-9): `TestCP11ChatStreamingE2E` ejercita la sesión
de chat de EXTREMO A EXTREMO contra un **SSE GRABADO** del adaptador `anthropic` (servido por un
`httptest` local, reusando el `recordedSSE` de CP-9 + un segundo SSE grabado `recordedFinalSSE`
para la 2ª vuelta del turno). El test: el usuario "envía" (`chat.input:set_value` + `Chat:submit`)
→ el agente corre el TURNO COMPLETO (`Session:send`, dos vueltas: la 1ª pide la tool
`get_weather` registrada con default allow, la 2ª responde texto final tras el tool_result) → el
`agent:delta` streaming se pinta con markdown en el transcript → **el Block compuesto del
transcript CRECE con el texto en streaming** (alturas no-decrecientes, final > inicial) → el
contenido REAL llega a la pantalla compuesta (verificado en la rejilla del compositor). Es la
PRIMERA vez que el camino **chat → agente → stream → markdown → toolkit** corre junto (CP-9 lo
corrió FUERA del chat).

**Qué SÍ cubre el test automático:** el camino chat→agente→stream→markdown→toolkit de punta a
punta, con tool call real intercalada, persistencia de la sesión (no_store=false), y el render
incremental verificado tanto en el modelo del transcript como en la rejilla del compositor.
**Qué NO (limitación del entorno, queda para un humano con credenciales):** el provider REAL
(red + credenciales Anthropic). El camino es idéntico —solo cambia que el SSE viene de la red en
vez de un `httptest`—; el adaptador `anthropic` ya está probado contra red simulada en S37.

**Fase 8 NO se cierra aún**: faltan S44 (repl) y S45 (CLI). El tablero queda con la Fase 8
abierta; el puntero avanza a S44.

## Tests (`internal/runtime/chat_test.go`, arnés de S12 con las cinco extensiones por `enu.toml`, `WithForceUI(true)`+`WithUISize`)

Carga+activa (builtin, superficie del módulo); `chat.start` headless → EINVAL accionable (G20,
WithForceUI(false)); LAYOUT (los tres widgets con área, foco en el editor); INPUT MULTILÍNEA
(enter deja pasar, shift/alt+enter nueva línea, backspace une líneas, caret row/col); STREAMING
MARKDOWN (deltas de texto crecen el transcript —el criterio de hecho—, message sella, contenido
en la rejilla del compositor); filtrado por sesión (G3: delta de otra sesión no toca el
transcript); diálogo de permisos (modal responde con `agent.permission.respond`); comando slash
(/help, desconocido); statusline (segmentos por defecto con el modelo/permisos); **CP-11 e2e**
(turno de dos vueltas con tool contra SSE grabado; el transcript crece y se ve en pantalla).
`CGO_ENABLED=0 go build`/`go vet ./...` verdes; `gofmt -l` limpio; `CGO_ENABLED=1 go test -race
-timeout 120s -count=2 ./internal/...` verde (~61 s); no regresiona S01–S42.

## Desviaciones (resumen)

1. **Transcript = un único `toolkit.text` markdown** (no un widget-por-mensaje): lo mínimo
   coherente; los renderers enchufables de tool results (chat.md §2) son evolución sobre los
   items ya separados. `chat.renderer` se expone con fallback de texto.
2. **Pickers difusos (chat.md §3 menciones `@`, §4 `/model`/`/sessions`) por texto en v1**: el
   picker visual (capa modal `toolkit.stack` + `enu.search.fuzzy`) es mejora posterior; el
   contrato (comandos, statusline, capa modal) queda ejercido.
3. **«Permitir siempre» del diálogo de permisos (chat.md §5) → solo una-vez/denegar en v1**:
   persistir el patrón a `agent.toml` / editar el patrón requiere superficie del agente que S39
   no expone; se muestra el patrón sugerido.
4. **Cursor multilínea colocado por el chat** (no por el `toolkit.app`, que solo coloca una
   fila): uso correcto de `Region:cursor`; no toca el toolkit.
5. **CP-11 contra SSE grabado, no provider real** (limitación del entorno CI: sin red ni
   credenciales): documentado arriba; el camino automático cubre todo salvo la red real.

Ninguna desviación toca el core ni amplía api.md.

**Lo que reusará S44/S45.** S44 (repl): el toolkit (`toolkit.app`/`input`/`text`) para su UI
interactiva si la tiene, y el patrón de arranque en TTY (`enu.has("ui")`, suscripción a
`core:ready`); el repl puede activarse SOLO (sin el harness), igual que el chat se activa solo.
S45 (CLI): el azúcar `--continue` se apoya en `agent.session{resume}` (G18) que el chat ya
consume; el chat es el consumidor interactivo del agente que el CLI complementa en modo headless
(`enu -e`, `--auto-permissions`). El contrato de eventos `agent:*` que el chat consume es estable
para cualquier otra UI/observador.

**Nota de proceso.** Tras código + tests + docs (puntero a S44, tablero, bitácora, esta
entrada) + build/vet/gofmt/race-count=2 verdes, se commitea y pushea SIN demora.
