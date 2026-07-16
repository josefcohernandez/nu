---
title: "Cambio de modelo a mitad de sesión sin API"
type: "hallazgo"
id: "G19"
status: "resuelto"
origin: "revisión de coherencia de la documentación completa"
resolution: "Session:set_model valida contra el registro de providers, persiste un evento en el transcript y aplica desde el siguiente request."
affected: ["agente.md §2", "chat.md §4"]
---
# G19 · Cambio de modelo a mitad de sesión sin API — `agente.md` §2 / `chat.md` §4 — **RESUELTO**

**Resolución** (aplicada en [agente.md](../contracts/agente.md) §2 y
[chat.md](../contracts/chat.md) §4): `Session:set_model("proveedor/modelo")` — valida
contra el registro de providers, escribe la entrada `event` en el
transcript (sesiones.md §3) y aplica desde el siguiente request; con un
turno en vuelo, al ensamblar la siguiente iteración (como la cola de G4),
nunca a mitad de un stream. `Session.model` mutable descartado (sin punto
claro de validación ni de registro en el transcript); fork-por-modelo
descartado (fragmenta sesiones para una operación cotidiana).

**Problema.** `/model` existe en `chat` (picker desde `providers.list()`)
y [sesiones.md](../contracts/sesiones.md) §3 pone "cambio de modelo a mitad de sesión"
como ejemplo canónico de entrada `event`, pero `Session` no expone ninguna
forma de cambiarlo: `opts.model` solo existe en la creación.

**Impacto.** Feature básica de UX, presupuesta por dos contratos.

**Opciones.** (a) `Session:set_model("proveedor/modelo")`: valida contra
el registro, escribe la entrada `event` y aplica desde el siguiente
request; (b) `Session.model` mutable (menos explícito, sin punto claro de
validación); (c) sin cambio en caliente: `/model` hace fork con el modelo
nuevo (consistente con append-only, pero fragmenta sesiones).
