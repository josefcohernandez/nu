---
title: "Los secretos del provider se heredan por defecto en el entorno de todo subproceso lanzado por la tool `bash`/`enu.proc`"
type: "hallazgo"
id: "G55"
status: "resuelto"
date: "2026-07-16"
origin: "auditoría de seguridad 2026-07-16 (SEC-04)"
resolution: "providers.secret_env_vars() lista los nombres de api_key_env, y bash/enu.proc excluyen esas variables del entorno del hijo por defecto."
affected: ["extensión agent / enu.proc §6"]
---
# G55 · Los secretos del provider se heredan por defecto en el entorno de todo subproceso lanzado por la tool `bash`/`enu.proc` — extensión `agent` / `enu.proc` §6 — **RESUELTO**

**Resolución** (2026-07-16; [providers.md](../contracts/providers.md) §4 +
[agente.md](../contracts/agente.md) §3 — el core queda **intacto**). Dos piezas, ambas en
las extensiones. (1) La extensión de providers gana
`providers.secret_env_vars() -> string[]`: los **nombres** —nunca los
valores— de las `api_key_env` de todos los providers declarados en
`providers.toml`, deduplicados; solo esa extensión sabe qué variables del
entorno son credenciales ("provider" es vocabulario de producto, ADR-003),
así que ella publica la lista y las demás la consumen. (2) La tool `bash` de
la extensión `agent` (y el lanzamiento de servidores MCP por `enu.proc`)
monta por defecto el entorno del hijo **sin** esas variables; el opt-in es
explícito y nominal — `inherit_secrets = ["VAR", ...]` bajo `[tools.bash]`
en el `agent.toml` del usuario, lista de nombres exactos sin comodín — y
**no** puede concederlo ni el `agent.toml` del proyecto (amplía: se ignora,
agente.md §11) ni los args de la tool (el modelo se autoconcedería el
secreto por inyección de prompt); para un servidor MCP, el opt-in es su
propia entrada de config con un `env` explícito. La mecánica es la que
`enu.proc` ya ofrece — `opts.env` **reemplaza** el entorno heredado por
llamada ([api.md](../contracts/api.md) §6; la semántica de reemplazo quedó fijada en S16
de [decisiones-implementacion.md](../worklog/README.md)), y
"heredado menos estas" lo cubre el idioma `env -u` del SO —: cambia el
**default de la extensión**, no el core.
Advertencia para plugins que lancen subprocesos en
[guia-plugins.md](../contracts/guia-plugins.md) §5. Descartado: recortar dentro de
`enu.proc` (el core no sabe qué es un provider, ADR-003 — sería
contaminarlo con vocabulario de producto) y el opt-in por argumento de la
invocación (quien propone los args es el modelo: papel mojado ante prompt
injection). Distinto de [P7](../postponed/pospuesto.md) —transcripts—, que sigue
pospuesto con nota cruzada. (Origen: SEC-04.)

**Problema.** Las variables de entorno que portan las API keys de los providers
(`api_key_env` y conocidas equivalentes) se propagan sin filtrar al entorno de
los subprocesos que arranca la tool `bash` (y `enu.proc` en general). Un comando
propuesto por el LLM —o un script de build hostil— puede leer la clave con un
simple `env`/`printenv`. Distinto de `P7`, que cubre la redacción de secretos en
los *transcripts*, no en el *entorno* heredado. Detectado en SEC-04 (2026-07-16).

**Impacto.** Exfiltración trivial de credenciales de LLM desde cualquier
subproceso, sin que el usuario haya concedido acceso a esos secretos.
