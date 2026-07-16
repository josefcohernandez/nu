---
title: "`EAGENT` se usa en `chat.md`/`adr.md` (y en la extensión) pero `agente.md` nunca lo acuña"
type: "hallazgo"
id: "G48"
status: "resuelto"
date: "2026-07-12"
origin: "auditoría integral 2026-07-12"
resolution: "La extensión agent acuña formalmente EAGENT como su código de error estructurado, coherente con lo que ya lanzaba."
affected: ["agente.md §10"]
---
# G48 · `EAGENT` se usa en `chat.md`/`adr.md` (y en la extensión) pero `agente.md` nunca lo acuña — `agente.md` §10 — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §10): la extensión `agent` **acuña formalmente `EAGENT`** como su código de error estructurado (forma de api.md §1.4, mismo patrón con que providers.md §3 acuña `EPROVIDER`): errores propios del motor —`agent.toml` mal formado, `max_turns` agotado, subagente roto— viajan como `{ code = "EAGENT", message, detail? }`. La implementación ya lo lanzaba (`eagent()` en `agent/init.lua`, `subagent_worker.lua`); `chat.md` §8 y ADR-017, que ya lo citaban, quedan correctos sin tocarse. El defecto era la acuñación ausente en el contrato normativo.

**Problema.** `chat.md` §8 y ADR-017 enumeran `EAGENT` entre los errores que `agent.session` puede lanzar, pero el contrato que define `agent.session` (agente.md) solo mencionaba `EINVAL` en todo el documento: un lector del contrato no podía saber que ese código existe ni qué significa. (A-29 del informe.)

**Impacto.** Quien maneje errores del agente por `code` programaba contra un código indocumentado.

**Opciones.** (a) Acuñarlo en agente.md §10 (elegida: la implementación y dos documentos ya lo daban por existente). (b) Retirarlo de chat.md/adr.md y colapsar a EINVAL — empobrece el diagnóstico y reescribe un ADR, contra la disciplina.
