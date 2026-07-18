---
title: "El reintento con backoff prometido por agente.md §2 no existe en el motor"
type: "hallazgo"
id: "G42"
status: "resuelto"
date: "2026-07-16"
origin: "informe de arquitectura 2026-07-08 (H-1), enrutado el 2026-07-16"
resolution: "stream_with_retry en el motor: solo la apertura del stream se reintenta (max_retries, backoff exponencial), con notificación agent:retry."
affected: ["agente.md §2/§4/§10"]
---
# G42 · El reintento con backoff prometido por agente.md §2 no existe en el motor — `agente.md` §2/§4/§10 — **RESUELTO**

**Resolución** (aplicada en [agente.md](../contracts/agente.md) §2 —párrafo "Reintentos"—, §4 —evento `retry` en la lista de notificaciones— y §10 —`max_retries`/`retry_base_ms`—): el motor reintenta la **apertura del stream** (paso 3 del turno), y solo eso. Cuatro piezas:

1. Si `adapter.stream` lanza un error con `detail.retryable = true` (la marca que [providers.md](../contracts/providers.md) §3 obliga al adaptador a poner: 429, 5xx, cortes de red), el motor espera con backoff exponencial (`retry_base_ms · 2^(intento−1)`; por defecto 1 s → 2 s → 4 s) y reintenta, hasta `max_retries` reintentos (por defecto 3, configurable por `opts` y `agent.toml` con la precedencia estándar de §10). Agotados, el error propaga tal cual — con su `retryable` intacto, que G43 lleva hasta la UI para el reintento manual.
2. **Solo la apertura.** Un fallo a mitad de stream no se reintenta nunca: los deltas ya emitidos están pintados por la UI y reintentar duplicaría contenido. Es además la frontera natural — los adaptadores detectan el status HTTP al abrir el stream, así que los errores retryables nacen casi todos ahí; lo que muere a mitad de stream es otra clase de fallo y propaga como error del turno.
3. Cada espera se anuncia con la notificación `agent:retry { session, attempt, max_retries, delay_ms, code, message }` (§4), para que una UI no muestre siete segundos de nada. El backoff duerme con `enu.task.sleep`: es un punto de suspensión normal, así que un `Session:cancel` durante la espera aborta el turno como siempre (S08), sin caso especial.
4. El subagente en modo worker (§9) aplica la misma política — hereda `max_retries`/`retry_base_ms` del padre en su `init` — pero sin evento: los workers no tienen bus (ADR-004).

**Problema.** agente.md §2 prometía: *"Errores del adaptador con `retryable = true`: reintento con backoff exponencial y límite configurable — la política vive aquí, nunca en el adaptador"*. Los tres adaptadores cumplían su mitad (marcaban `retryable` en 429/5xx: `adapter_anthropic.lua`, `adapter_openai_compat.lua`, `adapter_gemini.lua`), pero el motor llamaba a `adapter.stream` sin ningún envoltorio de reintento (`agent/init.lua`, `subagent_worker.lua`): un `EPROVIDER` retryable propagaba igual que uno permanente hasta el `pcall` de `_turn_loop`, que cerraba el turno con `agent:error`. Un rate-limit pasajero — el pan de cada día contra APIs de LLM — mataba el turno entero. Aflorada en la auditoría de arquitectura post-M17 ([informe-arquitectura-2026-07-08.md](../audits/informe-arquitectura-2026-07-08.md), H-1); no estaba ni resuelta ni pospuesta: una grieta silenciosa entre contrato e implementación.

**Impacto.** Robustez de todo turno contra errores transitorios del proveedor (el caso más frecuente de fallo en producción); también los subagentes, que sin esto fallaban el job entero por un 429.
