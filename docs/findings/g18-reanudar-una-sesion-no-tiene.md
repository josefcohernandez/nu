---
title: "Reanudar una sesión no tiene API"
type: "hallazgo"
id: "G18"
status: "resuelto"
origin: "revisión de coherencia de la documentación completa"
resolution: "agent.session{resume=id} reabre una sesión existente con replay del transcript y adquisición del lock de escritor."
affected: ["agente.md §2"]
---
# G18 · Reanudar una sesión no tiene API — `agente.md` §2 — **RESUELTO**

**Resolución** (aplicada en [agente.md](../contracts/agente.md) §2 y
[chat.md](../contracts/chat.md) §4/§8): `agent.session{ resume = id }` — una sola
función, dos modos. Reabre con replay del transcript (sesiones.md §3) y
adquisición del lock de escritor (§6); el resto de `opts` es estado
efímero, no se persiste. `agent.resume()` aparte se descartó (firma
duplicada sin ganancia); reanudar-como-fork se descartó (bifurca el
historial en cada reanudación). El azúcar CLI (`enu --continue`) queda
deliberadamente fuera de los contratos: pertenece a la superficie CLI
(cuestión abierta 5 de [arquitectura.md](../core/arquitectura.md)).

**Problema.** `agent.session(opts)` solo crea sesiones nuevas (sus `opts`
no admiten id). Pero [chat.md](../contracts/chat.md) §8 (`enu --continue`, picker de
`/sessions`) presupone reanudación, y [sesiones.md](../contracts/sesiones.md) §7
describe el listado que la alimenta. Falta el punto de entrada.

**Impacto.** Contrato congelable; la feature está prometida en dos
documentos.

**Opciones.** (a) `agent.resume(id) -> Session` (replay de sesiones.md §3
+ lock de §6); (b) `agent.session{ resume = id, ... }` (una sola función,
dos modos); (c) reanudar = fork del último punto (unifica mecánica con §5
pero bifurca el historial en cada reanudación — probablemente
descartable).
