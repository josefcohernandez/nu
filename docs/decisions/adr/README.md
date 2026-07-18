---
title: "Registro de decisiones técnicas (ADR)"
type: "indice"
status: "vigente"
---
# Registro de decisiones técnicas (ADR)

Formato ligero: contexto → decisión → consecuencias. Una entrada por decisión,
numeradas, nunca se reescriben: si una decisión cambia, se añade una nueva que
la reemplaza (supersede).

Estados: **Aceptada** · **Propuesta** · **Abierta** (aún sin decisión) ·
**Reemplazada por ADR-NNN**.

---

## Índice

| ADR | Título | Estado | Fichero |
|---|---|---|---|
| ADR-001 | Go como lenguaje del core | Aceptada | [adr-001-go-como-lenguaje-del-core.md](adr-001-go-como-lenguaje-del-core.md) |
| ADR-002 | Lua (gopher-lua) como lenguaje de extensión | Aceptada | [adr-002-lua-gopher-lua-como-lenguaje.md](adr-002-lua-gopher-lua-como-lenguaje.md) |
| ADR-003 | Core mínimo: el agente y MCP son extensiones oficiales | Aceptada | [adr-003-core-minimo-el-agente.md](adr-003-core-minimo-el-agente.md) |
| ADR-004 | Modelo de concurrencia híbrido ("modelo del navegador") | Aceptada | [adr-004-modelo-de-concurrencia-hibrido-modelo.md](adr-004-modelo-de-concurrencia-hibrido-modelo.md) |
| ADR-005 | Providers de LLM: registro en TOML + adaptadores en Lua | Aceptada | [adr-005-providers-de-llm-registro.md](adr-005-providers-de-llm-registro.md) |
| ADR-006 | TUI: librería del kernel | Propuesta | [adr-006-tui-libreria-del-kernel.md](adr-006-tui-libreria-del-kernel.md) |
| ADR-007 | API de UI expuesta a Lua | Aceptada | [adr-007-api-de-ui-expuesta.md](adr-007-api-de-ui-expuesta.md) |
| ADR-008 | Granularidad de aislamiento: workers por tarea, estado principal compartido | Aceptada | [adr-008-granularidad-de-aislamiento-workers.md](adr-008-granularidad-de-aislamiento-workers.md) |
| ADR-009 | Convenciones de la API: namespace global, async por corrutinas, errores estructurados | Propuesta | [adr-009-convenciones-de-la-api-namespace.md](adr-009-convenciones-de-la-api-namespace.md) |
| ADR-010 | Extensiones oficiales: distribuidas con nu, no activas por defecto | Aceptada | [adr-010-extensiones-oficiales-distribuidas-con-nu.md](adr-010-extensiones-oficiales-distribuidas-con-nu.md) |
| ADR-011 | Realización del scheduler: goroutine-por-task + token de ejecución Lua | Reemplazada | [adr-011-realizacion-del-scheduler-goroutine.md](adr-011-realizacion-del-scheduler-goroutine.md) |
| ADR-012 | Resultado del spike de ADR-007: el toolkit se construye en Lua | Aceptada | [adr-012-resultado-del-spike-de-adr.md](adr-012-resultado-del-spike-de-adr.md) |
| ADR-013 | Integración continua y publicación de releases | Aceptada | [adr-013-integracion-continua-y-publicacion.md](adr-013-integracion-continua-y-publicacion.md) |
| ADR-014 | Licencia: Apache 2.0 | Aceptada | [adr-014-licencia-apache-2-0.md](adr-014-licencia-apache-2-0.md) |
| ADR-015 | Conjunto oficial de producto y onramp no interactivo | Aceptada | [adr-015-conjunto-oficial-de-producto.md](adr-015-conjunto-oficial-de-producto.md) |
| ADR-016 | Modelo canónico de `thinking` con `mode` y traducción por-modelo en el adaptador | Aceptada | [adr-016-modelo-canonico-de-thinking.md](adr-016-modelo-canonico-de-thinking.md) |
| ADR-017 | El onramp deja config de agente usable y el chat degrada con gracia | Aceptada | [adr-017-el-onramp-deja-config.md](adr-017-el-onramp-deja-config.md) |
| ADR-018 | Las extensiones oficiales son un PRODUCTO: el toolkit decora y la UI del harness se ve acabada | Aceptada | [adr-018-las-extensiones-oficiales-son.md](adr-018-las-extensiones-oficiales-son.md) |
| ADR-019 | La VM objetivo del kernel es PUC-Lua sobre wazero; gopher-lua queda en mantenimiento | Aceptada | [adr-019-la-vm-objetivo-del-kernel.md](adr-019-la-vm-objetivo-del-kernel.md) |
| ADR-020 | El puente ⏸ definitivo: tasks como corrutinas Lua nativas (reemplaza ADR-011 en la conmutación) | Aceptada | [adr-020-el-puente-definitivo-tasks.md](adr-020-el-puente-definitivo-tasks.md) |
| ADR-021 | Baseline completo y reproducible de lint antes de congelar v1 | Aceptada | [adr-021-baseline-completo-y-reproducible.md](adr-021-baseline-completo-y-reproducible.md) |
| ADR-022 | Renombrado total del proyecto y de la API: `nu` → `enu` | Aceptada | [adr-022-renombrado-total-del-proyecto.md](adr-022-renombrado-total-del-proyecto.md) |
| ADR-023 | Los permisos de `bash` se emparejan por subcomando con un tokenizador cerrado y fallan hacia `ask` | Aceptada | [adr-023-los-permisos-de-bash.md](adr-023-los-permisos-de-bash.md) |
| ADR-024 | Identidad de un worker: foto del plugin dueño en el spawn, inmutable | Aceptada | [adr-024-identidad-de-un-worker-foto.md](adr-024-identidad-de-un-worker-foto.md) |
| ADR-025 | Reposicionamiento: motor para construir coding harnesses; Pi como referencia; pre-1.0 con roturas por ADR; frente público en inglés | Aceptada | [adr-025-reposicionamiento-motor-de-harnesses.md](adr-025-reposicionamiento-motor-de-harnesses.md) |
| ADR-026 | El binario estrena subcomandos de gestión: `init`, `doctor`, `update`, `uninstall` (refina ADR-015/017) | Aceptada | [adr-026-subcomandos-de-gestion-del-binario.md](adr-026-subcomandos-de-gestion-del-binario.md) |
