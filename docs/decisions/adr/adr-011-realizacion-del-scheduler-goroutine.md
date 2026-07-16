---
title: "Realización del scheduler: goroutine-por-task + token de ejecución Lua"
type: "adr"
id: "ADR-011"
status: "reemplazada"
date: "2026-06"
superseded_by: ["ADR-020"]
---
# ADR-011 · Realización del scheduler: goroutine-por-task + token de ejecución Lua

**Estado:** **Reemplazada por [ADR-020](#adr-020--el-puente--definitivo-tasks-como-corrutinas-lua-nativas-reemplaza-adr-011-en-la-conmutación)** · la conmutación **M16** hizo de wasm el backend por defecto y la retirada **M17** ([migracion-vm.md](archive/migracion-vm.md)) eliminó gopher-lua del `go.mod` y del binario, borrando el scheduler goroutine-por-task que este ADR realizaba; el puente ⏸ definitivo (tasks como corrutinas Lua nativas) lo describe ahora ADR-020. Como manda el flujo del proyecto, el cuerpo no se reescribe: queda como registro histórico de *cómo* se realizó ADR-004 sobre gopher-lua. · Originalmente Aceptada · 2026-06 (refinaba *cómo* se realiza ADR-004 sobre
gopher-lua; no cambiaba su semántica observable ni la API de [api.md](api.md))

**Contexto.** ADR-004 fijó el "modelo del navegador" (estado Lua principal
single-threaded, async por await implícito) y anticipó como mayor coste "el
puente de corrutinas" (event loop + coroutines-Lua ↔ goroutines). Al
implementar la quilla (S04) se descubrió una grieta del runtime
(problemas.md G31): **gopher-lua —semántica Lua 5.1— no permite que una
corrutina ceda (`yield`) a través de una frontera de llamada Go.** En
concreto, verificado contra gopher-lua v1.1.2:

1. `pcall(fn)` donde `fn` suspende: la corrutina **se aborta** en el `pcall`
   en vez de ceder. Pero [api.md](api.md) §1.4 promete que los errores
   estructurados "se capturan con `pcall`", y el pseudocódigo
   ([pseudocodigo.md](pseudocodigo.md) §§ tool runner, ramas paralelas)
   envuelve en `pcall` operaciones que hacen IO (⏸). El modelo de errores
   entero se apoyaba en algo que el runtime no soporta.
2. `return ⏸fn()` en posición de cola: el `OP_TAILCALL` elide el frame del
   llamante *antes* de que la función Go ceda, perdiendo la continuación; la
   task "termina" en vez de suspenderse.

Ambas tienen la misma raíz (el `yield` de corrutina no cruza fronteras Go) y
no se arreglan en la espec: la API es correcta, lo que falla es la *técnica*
de realización del puente.

**Decisión.** Realizar el scheduler **sin yields de corrutina**: una
**goroutine por task** + un **único token de ejecución Lua** ("GIL"):

1. Cada task corre en su propia goroutine, sobre su propio thread Lua
   (`*lua.LState` hijo del principal; comparten globales `G`).
2. Un token (canal de capacidad 1) garantiza que **solo una goroutine toca
   Lua a la vez** — el invariante single-threaded de ADR-004/008.
3. Una primitiva ⏸ no cede una corrutina: **suelta el token**, hace el
   trabajo bloqueante en una goroutine de fondo (que jamás toca Lua) y, al
   terminar, **recupera el token** y retorna con normalidad.

Como no hay yield, la pila Lua de la task vive en su pila Go: `pcall`, las
tail calls y el desenrollado de errores son los **nativos** de gopher-lua y
sobreviven a la suspensión. `Task:await` pasa a ser una función Go pura que
relanza el error de la task esperada con `L.Error` (capturable con `pcall`).

**Razonamiento.** Es la otra realización canónica del "modelo del navegador"
sobre un runtime Lua-en-Go (el patrón "giant lock" cooperativo). Mata las dos
grietas en la raíz en vez de parchearlas (trampolines Lua para la cola, y un
`pcall` rendido como sub-corrutina para el caso 1 — ambos más invasivos y aun
así frágiles). La semántica observable de ADR-004 se conserva intacta: Lua de
un hilo lógico, await implícito, cero data races (ahora por el token, con
handoff por canal = *happens-before*; validado con `-race`).

**Consecuencias.** El "event loop + cola de eventos" de ADR-004 se realiza
como token + goroutines, no como un bucle que reanuda corrutinas; la
descripción de S04 en [implementacion.md](implementacion.md) se lee con esa
lente. El coste por task sube de una corrutina a una goroutine (+ un thread
Lua) — barato en Go y aceptable para el volumen de tasks de un harness. La
detección de "estoy en una task" (para vetar ⏸ fuera de task, §1.3) es por
estado de ejecución: el chunk principal y los handlers síncronos corren sobre
el estado `host`; las tasks, sobre su propio thread. Las piezas que
presuponían un bucle central que reanuda corrutinas (timers de S05, despacho
de eventos de S10) se construyen sobre este modelo: un "tick del loop" es
trabajo que toma el token en el estado principal. El **watchdog** de S09
(presupuesto por slice) y el **desenrollado no capturable** de S08
(cancelación/`EBUDGET` que `pcall` no atrapa) se diseñan ya sabiendo que
`pcall` es el nativo de gopher-lua: el aborto no capturable necesitará un
mecanismo propio (un panic centinela que el kernel reconozca y no deje que el
`pcall` de usuario se trague), no el `yield` que aquí se descarta.

---
