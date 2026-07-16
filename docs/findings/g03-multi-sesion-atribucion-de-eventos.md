---
title: "Multi-sesión: atribución de eventos y modales concurrentes"
type: "hallazgo"
id: "G3"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "session_id obligatorio en todo payload agent:*, chat filtra por sesión activa y los modales de permisos se encolan en FIFO sin timeout."
affected: ["agente.md §4", "chat.md"]
---
# G3 · Multi-sesión: atribución de eventos y modales concurrentes — `agente.md` §4 / `chat.md` — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §4-§5 y
[chat.md](chat.md) §1/§2/§5): `session` obligatorio en todo payload
`agent:*` (emitido vía helper único); `chat` pinta solo la sesión activa y
señala el resto en statusline; modales en cola FIFO etiquetados por
sesión, **sin timeout** en los asks (un timeout→deny sería no
determinista) — la UI hace visibles los pendientes. Descartado el
namespacing por sesión en el nombre del evento (el bus no tiene wildcards
y un campo lo resuelve gratis).

**Problema.** Los payloads `agent:*` no obligan a llevar `session_id`
(dos sesiones concurrentes mezclarían deltas), `chat.md` no especifica
filtrado, y dos `permission.asked` simultáneos abrirían dos modales sobre
la misma pila de input sin orden definido.

**Impacto.** Los subagentes ya hacen esto real en v1 — no es un caso
futuro. Contrato congelable afectado.

**Opciones.** (a) `session_id` obligatorio en todo payload `agent:*` +
`chat` filtra por sesión activa + cola FIFO de modales (uno visible a la
vez); (b) además, namespacing de eventos por sesión
(`agent:<id>:delta`) para suscripciones selectivas baratas.
