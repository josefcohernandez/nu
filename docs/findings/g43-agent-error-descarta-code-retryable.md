---
title: "`agent:error` descarta el `code` y el `retryable` que chat.md promete pintar"
type: "hallazgo"
id: "G43"
status: "resuelto"
date: "2026-07-16"
origin: "informe de arquitectura 2026-07-08 (H-2), enrutado el 2026-07-16"
resolution: "agent:error lleva code/retryable completos; Session:retry y /retry en chat permiten el reintento manual del último turno fallido."
affected: ["agente.md §2/§4", "chat.md §2/§4"]
---
# G43 · `agent:error` descarta el `code` y el `retryable` que chat.md promete pintar — `agente.md` §2/§4 · `chat.md` §2/§4 — **RESUELTO**

**Resolución** (aplicada en [agente.md](../contracts/agente.md) §2 —`Session:retry()` en la firma y párrafo "Reintento manual"— y §4 —payload de la garantía de error visible—, y [chat.md](../contracts/chat.md) §2/§4): el mismo principio que G40 — el dato existía y se descartaba en la frontera; la prosa es presentación, no el portador. Dos piezas:

1. El payload de `agent:error` lleva el error estructurado completo: `{ session, message, code?, retryable?, detail? }` (cambio aditivo: nadie que leyera `message` se rompe). `retryable` se alza de `detail.retryable` como campo de primer nivel porque es la señal que toda UI necesita para decidir si ofrece reintento.
2. `Session:retry() ⏸ -> Message` entra en el contrato: re-ejecuta el turno sobre el historial vigente **sin anexar mensaje nuevo** — exactamente lo que necesita la acción de reintento tras un error (el mensaje del usuario ya está en el historial; un `send` lo duplicaría). Mismo contrato de espera que `send` (future del mensaje final); `EINVAL` con un turno en vuelo, la sesión cerrada o el historial vacío. Chat lo consume: el bloque de error pinta `[code] mensaje` y, si `retryable`, la pista `(/retry para reintentar)`; `/retry` entra en los builtins de chat.md §4.

**Problema.** chat.md §2 prometía: *"`agent:error` | Bloque de error con el código estructurado y, si `retryable`, acción de reintento"*. Pero `Session:_turn_loop` solo extraía `message` y `code` del error capturado (el `detail` — y con él `retryable` — se perdía), y el handler del chat solo usaba `p.message`: ni el dato llegaba, ni existía interacción alguna de reintento. Aflorada en la misma auditoría que G42; son gemelas — G42 sin G43 deja a la UI ciega cuando los reintentos automáticos se agotan.

**Impacto.** Toda UI que consuma `agent:error` (chat, orquestadores headless, telemetría): sin el `code` no se distingue un 401 accionable de un timeout, y sin `retryable` no hay acción de reintento que ofrecer.
