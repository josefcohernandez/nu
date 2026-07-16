---
title: "El replay de `resume` ignora las entradas `event`: los cambios en caliente persistidos se pierden al reanudar"
type: "hallazgo"
id: "G46"
status: "resuelto"
date: "2026-07-13"
origin: "auditoría integral 2026-07-12"
resolution: "El resume aplica la precedencia opts > event del transcript > agent.toml, reaplicando set_model/set_thinking y allow/deny en orden."
affected: ["sesiones.md §3", "agente.md §2 (tensión G18", "G19)"]
---
# G46 · El replay de `resume` ignora las entradas `event`: los cambios en caliente persistidos se pierden al reanudar — `sesiones.md` §3 / `agente.md` §2 (tensión G18/G19) — **RESUELTO**

**Resolución** (2026-07-13; opción (a) **más la (c)** — la recomendación
completa del registro—, construida el mismo día en la extensión `agent`,
fila `G46 (extensión)` de la bitácora de [implementacion.md](implementacion.md)).
La tensión G18/G19 se cierra declarando la **precedencia explícita** en
[agente.md](agente.md) §2: **opts del resume > `event` del transcript >
`agent.toml`** — los `opts` siguen siendo efímeros (G18) pero solo pisan al
transcript *cuando se dan*; cuando callan, rige lo grabado. El replay de
`agent.session{resume=...}` reaplica los `event` del agente: los repetibles
(`set_model`, `set_thinking`) con last-wins (la regla de sesiones.md §3, cuyo
ejemplo canónico deja de ser letra muerta), y los acumulativos (`allow`/`deny`)
**reaplicados en orden** sobre la política base, con la semántica de caliente
(idempotentes) y sin re-persistir — perder una palanca de seguridad al reanudar
sorprende, así que ningún opts los pisa. Los `event` se releen del transcript
**entero**, no desde el último `compact` (la compactación resume mensajes, no
configuración; anotado en sesiones.md §3). Si el modelo grabado ya no resuelve,
reanudar falla con `EPROVIDER` al abrir — mejor que en el primer turno—; el
escape es un `opts.model` explícito, que tiene precedencia. Sin cambios de
kernel ni de `api.md`. Blindaje: `agent_g46_test.go` (precedencia en ambos
sentidos, last-wins con cambios repetidos, allow/deny reaplicados sin duplicar).

**Problema.** `Session:set_model`/`set_thinking`/`allow`/`deny` persisten entradas `event` en el transcript, y `sesiones.md` §3 define para ellas una regla de replay explícita ("para datos repetibles… la última gana", con el cambio de modelo como ejemplo canónico). Pero el replay de `agent.session{resume=...}` (`agent/init.lua`) solo reconstruye `message` y `compact`: las `event` se reciben del store y se descartan. Una sesión que cambió de modelo en caliente vuelve al modelo de `opts`/`agent.toml` al reanudarse, sin aviso. La grieta tiene una tensión de espec previa: G18 declaró los `opts` **efímeros** (se reaplican en cada resume), lo que para el caso del modelo choca frontalmente con el last-wins de sesiones.md §3 — hay que decidir la precedencia, no solo implementar.

**Impacto.** Reanudar miente: la sesión no continúa "donde estaba" en lo que a modelo/razonamiento se refiere. Para `allow`/`deny` (acumulativos, no cubiertos por la regla last-wins) el replay tampoco los reaplica, aunque su semántica de resume ni siquiera está especificada.

**Opciones.** (a) Precedencia explícita `opts de resume > event del transcript > agent.toml`: el replay aplica los `event` repetibles (`set_model`, `set_thinking`) salvo que el `resume` traiga la opción explícita — resuelve la tensión G18/G19 declarando que los opts son efímeros *cuando se dan*, y el transcript manda cuando callan. (b) Rebajar sesiones.md §3: las `event` son solo registro auditable y el resume no las aplica — honesto con el código actual, pero convierte el ejemplo canónico de la espec en letra muerta. (c) La (a) más semántica definida para los acumulativos: el replay re-aplica también `allow`/`deny` en orden. Recomendación: (a) ahora, y decidir (c) en la misma resolución (los `allow`/`deny` en caliente son una palanca de seguridad; perderlos al reanudar sorprende). Toca `agente.md` §2, `sesiones.md` §3 y el replay de la extensión.
