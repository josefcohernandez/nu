---
title: "Watchdog de slice (api.md §1.3)"
type: "sesion"
id: "S09"
phase: 1
status: "cerrada"
---
# S09 — Watchdog de slice (api.md §1.3)

## Interrumpir un slice de CPU puro: `LState.SetContext` (decisión clave, hito de veto)

El gancho técnico que S08 dejó pendiente —cortar un slice de **CPU puro** que
nunca suspende (`while true do end`), sin punto de chequeo cooperativo— se
resuelve con `LState.SetContext` de gopher-lua v1.1.2, **el mecanismo soportado**
(no un hack de debug hook frágil). Verificado en su fuente: con un contexto
puesto, el thread corre `mainLoopWithContext` (`vm.go`), que en CADA instrucción
del intérprete comprueba `ctx.Done()` y, si está cancelado, lanza un error Lua
(`L.RaiseError(ctx.Err())` → `*ApiError` con mensaje "context canceled") que rompe
el bucle. `spawn` dota a cada thread de task de un `context.Context` **propio**
(raíz `Background`, no hijo del de `host`: aislar una task no afecta a otras,
ADR-008) vía `SetContext`. **No hay hallazgo ni veto:** el mecanismo existe y es
integrable con el desenrollado no capturable de S08.

## El watchdog corre en su propia goroutine, sin el token (decisión clave)

La clave del "sin congelar el loop" (CP-2). El temporizador del slice es un
`time.AfterFunc(budget)` que se **arma** cuando la task toma el token para correr
Lua (inicio de `runTask`; re-adquisición tras cada ⏸ en
`suspend`/`Task:await`/`Future:await`) y se **desarma** justo antes de soltarlo.
Si dispara, su callback corre en la goroutine del timer —que **no tiene el
token**—, por eso puede cortar a una task que lo monopoliza mientras otras tasks
y timers esperan: tras el corte, la víctima desenrolla hasta `runTask`, suelta el
token, y el resto progresa. El presupuesto es 100 ms por defecto, configurable con
`WithSliceBudget` (`Option` del Runtime; gancho que S11/S12 cablearán a
`enu.toml`); `<=0` desactiva el watchdog.

## Cada slice se mide aparte: arm/disarm en cada ⏸ (decisión)

Un ⏸ cierra el slice (desarma) y, al re-adquirir el token, abre uno nuevo (arma).
Así un bucle de CPU intercalado con suspensiones no acumula tiempo entre slices:
cada tramo continuo tiene su propio presupuesto. De ahí "sin falsos positivos":
trabajo normal que cede a menudo (sleeps, IO) nunca dispara el watchdog aunque su
tiempo TOTAL exceda el presupuesto.

## Reparto de escritura entre watchdog y task: invariante de S08 intacto (decisión clave)

S08 documentó que `aborting`/`reason`/`canceled` los escribe SOLO la goroutine de
la task bajo el token. S09 lo respeta pese a que el watchdog vive en otra
goroutine: el watchdog solo toca un **flag atómico** `budgetExceeded` (`atomic.Bool`,
cruza goroutines) y cancela el `ctx` (seguro concurrentemente). La **goroutine de
la task** es quien "reclama" el corte (`claimBudgetAbort`): al detectar
`budgetExceeded`, pone `aborting`/`reason=abortBudget`/`canceled` ella misma, bajo
el token. El reclamo ocurre en dos sitios: en `reraiseIfAborting` (cuando un
`pcall` de usuario capturó el ctx-error → re-lanza `abortSignal` para colarlo no
capturable) y en `runTask` tras el `CallByParam` (cuando el ctx-error llegó sin
`pcall` envolvente). En `suspend`/`*await` también se reclama antes de bloquear
(un slice anterior justo en el límite). Idempotente.

## `EBUDGET` vs `ECANCELED` en `await`: se distingue por `reason` (decisión)

`Task:await` de una task abortada por watchdog observa **`EBUDGET`**; de una
cancelada, **`ECANCELED`**. Ambos son *observación* de OTRA task —el awaiter SÍ
los captura con `pcall` y sobrevive—, no el aborto del propio awaiter. La
distinción es por `t.reason` (`abortBudget` vs `abortCancel`); se comprueba antes
que `errValue` (una task abortada nunca tiene `errValue`).

## `core:plugin.misbehaved` por gancho interno (decisión; lo cablea S10)

El bus `enu.events` es S10 (aún no existe). La emisión se hace por un **gancho
interno** `rt.emitMisbehaved(owner, reason)` que hoy loguea best-effort (como el
resto de fallos de task). **S10 lo cableará** a
`enu.events.emit("core:plugin.misbehaved", {plugin = owner, reason = ...})` sin
tocar el watchdog (el punto de llamada ya es único). NO se inventó superficie
pública: §1.3 dice que el watchdog es transparente.

## Alcance: solo el slice de una task (decisión)

Los handlers síncronos (`defer`/`every`) y los `cleanup` corren sobre threads
efímeros de `host`, que no tiene contexto, así que el watchdog no los vigila. El
alcance de S09 (api.md §1.3) es el **slice de una task**; vigilar handlers
síncronos sería otra pieza, fuera de esta sesión. Coherente con que `Close`
cancela el `ctx` de cada task al terminar (evita la fuga de `context.WithCancel`).

## Sin superficie pública nueva, sin hallazgo, sin veto

S09 no añade nada a `api.md` (el watchdog es transparente; `WithSliceBudget` es
`Option` Go, no API Lua). gopher-lua v1.1.2 SÍ permite interrumpir un slice de CPU
puro de forma limpia e integrable con el desenrollado de S08. El hito de veto
S08+S09 queda validado a favor de ADR-008 (aislamiento por tarea). CP-2 verde
cierra la Fase 1.
