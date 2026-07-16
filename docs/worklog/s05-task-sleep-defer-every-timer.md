---
title: "`enu.task.sleep`/`defer`/`every` + `Timer:stop` (api.md §3)"
type: "sesion"
id: "S05"
phase: 1
status: "cerrada"
---
# S05 — `enu.task.sleep`/`defer`/`every` + `Timer:stop` (api.md §3)

## Semántica de quiescencia con timers activos (decisión clave)

`api.md` §3 no dice cómo interactúan los timers con el fin de `enu -e`. El
modelo de S04 hace que `EvalString` corra el chunk, suelte el token y llame a
`waitIdle()`, que bloquea hasta que el conjunto queda **quiescente**. Había que
decidir qué cuenta como "trabajo pendiente":

- **`defer(fn)` SÍ cuenta.** Es "el siguiente tick": su handler debe correr
  antes de que `EvalString` devuelva. Se contabiliza con un contador `pending`
  (incrementado al encolar, decrementado al ejecutar el disparo); `waitIdle`
  espera a `live == 0 && pending == 0`. Sin esto, un `defer` encolado por el
  chunk podría no llegar a correr nunca.

- **`every(ms, fn)` NO cuenta.** Un timer periódico no termina jamás; si contara
  para la quiescencia, `enu -e` no volvería nunca. Decisión: un `every` activo es
  **facilidad de fondo**, no trabajo de primer plano. El fin de `enu -e` lo
  determinan el chunk + sus tasks + sus `defer` encolados; cuando todo eso queda
  quiescente, `EvalString` vuelve aunque haya timers activos, y `Runtime.Close`
  los apaga (corta sus goroutines de ticker, sin fugas).

  Justificación: en un `enu` interactivo (S33+) el loop sigue vivo por la UI/los
  eventos de entrada, no por los timers; bajo `enu -e` (headless, sin UI) el fin
  natural es la quiescencia del primer plano. Un timer que debiera mantener vivo
  el proceso indica que el trabajo real está en una task (que sí cuenta), no en
  el timer. Esto es coherente con el criterio de hecho de S05 en el plan
  ("`every` dispara N veces y `stop` lo corta"): los tests anclan el runtime con
  una task mientras el timer tickea.

## Handlers síncronos sobre thread efímero (no sobre `host`)

`defer` y cada disparo de `every` ejecutan un handler **síncrono** (no ⏸, §3):
corren bajo el token, como el chunk y los handlers de eventos. Se ejecutan sobre
un **thread Lua dedicado por disparo** (`host.NewThread()`), no sobre la pila del
estado principal. Motivo: mientras `EvalString` está en `waitIdle`, la pila de
`host` aún custodia los valores de retorno del chunk; un `CallByParam` sobre
`host` podría interferir. Es la misma estrategia que las tasks (cada una sobre su
`co`). Coste: un thread por disparo, recogido por el GC de gopher-lua (no hay
`Close` por thread en la API, igual que para los `co` de las tasks).

## `stop` sin disparo tardío (carrera tick/token)

Un disparo de `every` puede quedar esperando el token justo cuando llega `stop`.
Para garantizar "tras `stop`, ni un tick más", el disparo usa
`runSyncHandlerCancelable`: mientras espera el token atiende también a `stopCh` y,
si se cerró, no ejecuta. `stopTimer`/`stopAllTimers` cierran `stopCh` de forma
idempotente (solo si el timer sigue rastreado), así que `Timer:stop()` doble no
entra en pánico.

## Convención de tests con `-race`

`go test -race` exige cgo; el resto del proyecto compila con `CGO_ENABLED=0`
(ADR-001). Por tanto: `CGO_ENABLED=0 go build ./...` para el binario, y
`CGO_ENABLED=1 go test -race -count=4 ./...` para la suite con detector de
carreras (igual criterio que dejó S04 en la bitácora). Los tests de timing usan
periodos cortos (1-5 ms) y esperas holgadas; `-count=4`/`-count=8` no produjeron
flaky.

## Sin hallazgos

El modelo de S04 (goroutine-por-task + token) bastó para S05 sin ampliar la API.
No se abrió ningún `G##`.
