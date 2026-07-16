---
title: "Adaptador Anthropic (primer dialecto real); CP-9"
type: "sesion"
id: "S37"
phase: 8
status: "cerrada"
---
# S37 — Adaptador Anthropic (primer dialecto real); CP-9

Implementé el adaptador `anthropic` (providers.md §3) como módulo
`internal/runtime/embedded/providers/lua/providers/adapter_anthropic.lua`, registrado desde
el `init.lua` de providers con `register_adapter("anthropic", ...)`. El alcance, los DoD y
el contrato (providers.md §2/§3) no dejaban casi margen; estas son las decisiones de
interpretación que tomé donde el contrato no era literal.

## Cómo traduzco el dialecto Anthropic al canónico

**Request canónico → Messages API.** `to_wire` mapea bloque a bloque: `text`→text,
`image`→`image {source: base64}`, `thinking`→thinking, `tool_call {id,name,args}`→`tool_use
{id,name,input}`, `tool_result {id,content,is_error?}`→`tool_result {tool_use_id,...}`.
`system` va al campo `system` de Anthropic; `tools {name,description,schema}` a
`{name,description,input_schema}`; `thinking {budget}`→`{type="enabled",budget_tokens}`.
`max_tokens` es OBLIGATORIO en Anthropic: si el canónico no lo trae, cae al `max_output`
del ModelInfo resuelto y, en último término, a 4096. La clave va en `x-api-key` (NO
`Authorization: Bearer`) más `anthropic-version: 2023-06-01`.

**Round-trip de `meta` (§2.2 regla meta / §3 obl. 4).** El `meta` opaco de cada bloque se
funde con `pairs` en el bloque del wire. Para `thinking`, la `signature` viaja en `meta`
(la pone el adaptador al ensamblar el bloque desde `signature_delta`) y se reinyecta tal
cual en turnos siguientes; para otros, cubre `cache_control`/ids internos sin contaminar el
modelo canónico.

**SSE → stream canónico (§2.3).** Una **máquina de estados por ÍNDICE de bloque** sobre
`Stream:events()` (S20). El caso que obliga a estado es el **input de tool_use**: llega
troceado en `input_json_delta.partial_json`; lo acumulo como texto y lo decodifico con
`enu.json.decode` AL CERRAR el bloque (`content_block_stop`/`message_stop`). Un JSON de args
mal formado no aborta el stream: el bloque canónico queda con `args = {}` (el agente lo ve;
el adaptador no inventa, §3 obl. 3). `stop_reason` mapeado: `tool_use`→"tool_calls",
`max_tokens`→"max_tokens", `refusal`→"refusal", el resto→"end". `ping` se ignora
(keep-alive). El iterador devuelve un Event por llamada y CIERRA con `done {stop_reason,
message}` con el Message ensamblado (§2.1): el agente no re-ensambla deltas.

**Decisiones puntuales:**
- **Doble `usage`.** Emito un `usage` temprano en `message_start` (input_tokens/cache_read,
  útil para el llenado de contexto de la UI) y otro final en `message_delta` (output_tokens).
  Ambos son válidos por §2.3 (el evento `usage` no es único); la secuencia de tipos lo
  refleja.
- **Errores: dos vías a EPROVIDER (§3 obl. 2).** Status HTTP ≥ 400 (dato, api.md §8 no
  lanza) → `EPROVIDER` con `detail.status`/`provider_code`/`retryable`, leyendo el cuerpo
  JSON de error con `chunks()` (en error HTTP Anthropic manda JSON, no SSE); 429 y 5xx
  retryables. Evento `error` en mitad del SSE → `EPROVIDER` con `provider_code` y
  `retryable` (overloaded_error/api_error retryables). Marcar `retryable` es la única
  inteligencia de fallos que pide el contrato; el reintento es del loop del agente.
- **`count_tokens?`.** Anthropic tiene endpoint exacto (`/v1/messages/count_tokens`), pero
  uso la heurística `approx_tokens` de S36 sobre system + bloques de texto/thinking: sin
  red, suficiente para la estimación PREVIA (providers.md §5: la fuente de verdad del
  llenado es el `usage` del propio turno). El endpoint exacto queda como mejora futura.

## CP-9 (camino caliente, hito de veto de perf)

Como no hay red, GRABÉ un SSE de Anthropic realista (`recordedSSE` en
`providers_anthropic_test.go`: message_start con usage, ping, thinking con signature, texto
markdown en 3 deltas, tool_use con input troceado en dos `input_json_delta`, message_delta
con usage/stop, message_stop) y lo sirvo desde un `httptest` local con flush por línea
(patrón de los tests de S20). `TestCP9CaminoCaliente` corre el camino caliente COMPLETO:
una vuelta de conversación → el adaptador consume el SSE vía `enu.http.stream` → emite el
stream canónico → por cada delta de texto recompongo el markdown acumulado con
`enu.text.markdown` (streaming-safe, S23) y lo blitteo a una región (`Region:blit`, S29).

**Decisión de verificación del render:** el Block es OPACO (api.md §9.2: solo `.width`/
`.height`, no su contenido). Para confirmar que el render final corresponde al markdown
COMPLETO sin acceder a su interior, comparo su altura con un render fresco del texto entero
(coinciden si el streaming acumuló bien) y compruebo que es multilínea (encabezado +
cuerpo). El contenido textual ya se valida aparte (el Message ensamblado del `done`).

**Necesidad de `WithForceUI(true)`:** `bootWithToml` no fuerza la UI, así que en el entorno
headless de test `enu.ui` no existe (gating G20). Para ejercitar el blit del camino caliente,
`bootAnthropic` arranca el runtime con `WithForceUI(true)` (mismo recurso que `newHarness`,
S32); el gating REAL por TTY sigue aplicando al binario.

**Fluidez observada (medición):** todo el trabajo pesado del camino caliente —parseo SSE,
decode JSON, render markdown, blit— son primitivas Go; Lua solo orquesta el bucle de deltas
(reuso del aprendizaje S28/ADR-012). La suite `Anthropic|CP9` completa en ~0.06 s y
`-race -count=2 ./internal/...` entera en ~50 s sin data races. El camino caliente en Lua es
**aceptable**: no hay CPU ardiendo en Lua, el veto de perf (limitación nº8 de
modelo-ejecucion.md) no se dispara.

## Hallazgo

Ninguno. La API pública bastó exacta (`enu.http.stream`+`Stream:events()` §8, `enu.json` §12,
`enu.text.markdown` §10, `enu.ui.region`/`Region:blit` §9). api.md INTACTO; APILevel sigue en
1. Puntero ▶ avanza a **S38** (extensión sesiones; depende de S14, S16).
