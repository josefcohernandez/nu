---
title: "El puente ⏸ definitivo: tasks como corrutinas Lua nativas (reemplaza ADR-011 en la conmutación)"
type: "adr"
id: "ADR-020"
status: "aceptada"
date: "2026-07"
supersedes: ["ADR-011"]
---
# ADR-020 · El puente ⏸ definitivo: tasks como corrutinas Lua nativas (reemplaza ADR-011 en la conmutación)

**Estado:** Aceptada · 2026-07 (diseña el puente ⏸ del backend wasm de [ADR-019]; **reemplaza a [ADR-011](adr-011-realizacion-del-scheduler-goroutine.md)** cuando wasm sea la VM por defecto —conmutación M16 de [migracion-vm.md](../../archive/migracion-vm.md)—; hasta entonces ambos coexisten tras el selector de backend, ADR-011 para gopher y este para wasm). No cambia la semántica observable de [api.md](../../contracts/api.md) §1.3 ni ninguna firma.

**Contexto.** ADR-011 realizó el scheduler *sin yields* —goroutine por task + un token de ejecución— porque gopher-lua (Lua 5.1 reimplementado) **no deja que una corrutina ceda a través de un `pcall`** (grieta [G31](../../findings/g31-el-puente-no-puede-ceder.md)). Fue un rodeo forzado: el "puente de corrutinas" que ADR-004 anticipó como el modelo natural no se pudo construir. El spike de ADR-019 demostró (test 🔒 `TestYieldATravesDePcall`, M02) que **el Lua oficial de PUC sobre wazero SÍ cede a través de `pcall`**: la grieta G31 no existe en la implementación de referencia. Por tanto el backend wasm puede —y debe— realizar el puente como ADR-004 quería.

**Decisión.** El puente ⏸ del backend wasm son **corrutinas Lua nativas**, con un bucle de scheduler en Go que las conduce:

1. **Una task es una corrutina Lua** (`lua_newthread` + `lua_resume`, expuesto por el shim: `nu_co_spawn`/`nu_co_resume`, M02). El estado principal sigue siendo single-threaded (ADR-004): las corrutinas se planifican **cooperativamente**, una corre a la vez, compartiendo la memoria de la instancia. Sin token/GIL: la corrutina cede de verdad.
2. **⏸ = `lua_yield` con una petición.** Una primitiva suspendente (o `nu.task.sleep`) cede con un *descriptor de trabajo* (wire de M05: tipo de trabajo + args). El bucle Go lee lo cedido, lanza el trabajo bloqueante en una **goroutine de fondo** (que jamás toca la VM), y cuando termina **reanuda** la corrutina con el resultado (`nu_co_resume`). El código Lua se escribe secuencial (await implícito), como manda §1.3.
3. **El bucle del scheduler** mantiene un conjunto de tasks listas y suspendidas; reanuda las listas hasta que ceden o terminan; entrega los resultados de las goroutines de fondo por un canal. Es el event loop de ADR-004, ahora sin el baile del token.
4. **`nu.task` completo** (spawn/sleep/await/all[G27]/race/every/defer/future/cleanup) se implementa sobre este bucle. La semántica observable es idéntica a la de gopher (los tests `scheduler_test`/`allrace`/`future`/`timers` son el contrato de paridad).

**El coste del yield (la luz ámbar del INFORME §4.1).** Cada `lua_resume` atraviesa `luaD_rawrunprotected` → `LUAI_TRY` → el trampolín, que toma un `Snapshot` de wazero (clona la pila del motor). Medido en el spike: 26 µs en frío, degradando hacia decenas-de-µs bajo presión de GC. Para el caso real —un yield por operación de IO que tarda milisegundos— es ruido. El **veto 2 de M15** (≤ 50 µs sostenido) lo audita con números en el scheduler real. **Palanca de mitigación reservada** si lo excede: aligerar el camino sin-error del trampolín (el `Snapshot` sólo hace falta si un `LUAI_THROW` va a ocurrir; un resume que no lanza lo desperdicia). No se optimiza preventivamente: se mide primero.

**Consecuencias.**

- **El kernel se simplifica** (censo C4): sin token, sin la goroutine-por-task, sin el `suspend` que suelta/re-adquiere el token. La cancelación y el watchdog (M07) se realizan sobre el aborto nativo de Lua + interrupción por época de wazero, sin el `installCancelPcall` (el envoltorio de `pcall`/`xpcall` de ADR-011 muere en M17).
- **ADR-011 queda reemplazada** en la conmutación (M16); su código gopher se retira con gopher-lua (M17). Como manda el flujo del proyecto, ADR-011 no se reescribe: se marca "Reemplazada por ADR-020" cuando la conmutación se cierre.
- **Coexistencia temporal**: mientras el selector ofrezca ambos backends (M04-M16), ADR-011 rige el camino gopher y este el wasm; los dos pasan la misma suite de conformidad (§3 del plan).
- Riesgo vigilado: el coste del yield (arriba) y la dependencia del trampolín en la API experimental de snapshots de wazero (gate-test 🔒 de M03).
