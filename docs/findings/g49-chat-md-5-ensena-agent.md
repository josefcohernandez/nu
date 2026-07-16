---
title: "`chat.md` §5 enseña `agent.permission.respond(id, \"once\")`, que la API real interpreta como DENEGACIÓN"
type: "hallazgo"
id: "G49"
status: "resuelto"
date: "2026-07-12"
origin: "auditoría integral 2026-07-12"
resolution: "El ejemplo de chat.md pasa a respond(id, true); 'una vez' y 'siempre' conceden igual, difieren solo en si se persiste."
affected: ["chat.md §5", "agente.md §5"]
---
# G49 · `chat.md` §5 enseña `agent.permission.respond(id, "once")`, que la API real interpreta como DENEGACIÓN — `chat.md` §5 / `agente.md` §5 — **RESUELTO**

**Resolución** (aplicada en [chat.md](chat.md) §5): el ejemplo pasa a `agent.permission.respond(id, true)` y la prosa aclara que **"permitir una vez" y "permitir siempre" conceden igual** (`granted = true`); difieren solo en si además se persiste el patrón (política de la sesión / `agent.toml` global vía `persist_allow`). El defecto era del documento: la API (`respond(id, granted)`, booleano — `p.future:set(granted == true)`) y la UI oficial ya eran correctas; un integrador tercero que siguiera el contrato al pie de la letra **denegaba creyendo conceder** (`"once" == true` es `false` en Lua). (A-31 del informe.)

**Problema/Impacto/Opciones.** Ver título y resolución: doc-fix sin alternativa razonable (cambiar la API para aceptar strings rompería a los llamantes booleanos existentes y añadiría un segundo vocabulario para lo mismo).
