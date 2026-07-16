# S35 — `Worker:on_message` (excluyente con `recv`, G8) + tasks/timers/futures dentro del worker + `terminate` (api.md §13, 🔒; cierra Fase 7 — CP-8)

S35 cierra la Fase 7. La feature de superficie es `Worker:on_message`; el resto del
alcance (tasks/timers/futures dentro del worker, `terminate` robusto) lo dejó S34 ya
implementado y S35 lo BLINDA por test. Ni una función pública de más: §13 ya estaba en
`api.md`, `APILevel` sigue en 1. Sin hallazgos `G##`.

## Modelo de `on_message`: drenador de fondo + entrega en el estado principal

`Worker:on_message(fn) -> Sub` es la ALTERNATIVA por CALLBACK en el ESTADO PRINCIPAL a
`Worker:recv`. La pregunta de diseño era "cómo se drena la cola worker→padre y se llama
`fn(msg)` en el estado principal sin fugas". La respuesta, coherente con el modelo y sin
pieza nueva, espeja el ticker de `enu.task.every` (timers.go):

- Un **drenador en goroutine de fondo** (`luaWorker.drainOnMessage`) hace
  `select` sobre `fromWorker`/`done`/`stopCh`. Por cada mensaje toma el token del padre
  (`acquire`/`release`), comprueba `sub.live` y llama `fn(msg)` sobre un thread efímero
  del estado principal bajo `pcall` (`scheduler.callOnMessage`), reconstruyendo el valor
  con `goToLua` BAJO EL TOKEN —igual que `Worker:recv`—. Ningún `LValue` cruza goroutines
  (aislamiento ADR-008); el valor Go neutro ya venía copiado del lado del worker.
- El drenador NO es una task ni cuenta para la quiescencia del padre (es facilidad de
  fondo, como `every`): su fin de vida es `Sub:cancel` (`stopCh`), `terminate` (`done`) o
  `Close`. **Consecuencia para los tests:** la entrega es ASÍNCRONA respecto a `eval`
  (que solo espera a las tasks de primer plano), así que los tests de entrega SONDEAN
  (`pollEval`) hasta que llegan los mensajes.
- **Drenar lo encolado antes de salir por `done`** (igual que `recvOnBoundedChan`): el
  `select` de Go, con `done` y `fromWorker` ambos listos, elige al azar; por eso, al
  despertar por `done`, se intenta un saque NO bloqueante de `fromWorker` —si la cola
  está vacía, se acabó el emisor—. Sin esto, un worker que envía N mensajes y termina
  perdía los que quedaran tras el primero (lo destapó `TestWorkerOnMessageDelivery`:
  entregaba 1 de 5). `Sub:cancel`, en cambio, NO drena: es un corte explícito.
- Un `fn` que lanza queda aislado en el log (best-effort, ADR-008) y el drenado SIGUE con
  el próximo mensaje (`TestWorkerOnMessageHandlerThrows`).

`on_message` es **handle por dueño** (`workerSub` implementa `ownedHandle`, S13): `reload`
lo suelta vía `releaseOwnerHandles`; un `cancel` a mano hace `untrack` para no dejarlo
colgando en `ownerHandles`. Solo estado principal (no [W]: en el worker no hay `Worker`).

El `Sub` de `on_message` es un handle PROPIO (`workerSubTypeName`, lleva `*workerSub`), NO
el `Sub` de `enu.events` (lleva `*subscriber`): mismo método público `:cancel()`, distinto
tipo. Se eligió un tipo nuevo en vez de reusar el de eventos porque `subCancel` valida el
tipo concreto del userdata (`*subscriber`) y mezclar tipos sería frágil.

## Exclusividad G8 (lo 🔒): rechazo explícito EN EL ACTO, nunca prioridad silenciosa

`on_message` y `recv` sobre el MISMO worker son EXCLUYENTES. Mecánica, toda bajo el token
del padre (que la serializa, sin candado):

- `Worker:recv` lleva un contador `recvPending` en el `luaWorker`: `++` BAJO EL TOKEN
  antes de suspender, `defer --` al re-adquirir. El `defer` corre incluso si `terminate`
  aborta la task suspendida (el `abort` de `suspend` panica con el token re-adquirido, y
  los `defer` de Go se ejecutan al desenrollar): así `recvPending` nunca queda inflado.
- `on_message` guarda `onMsg` (la `Sub` activa).
- Registrar `on_message` con `recvPending > 0` → `EINVAL` en el acto; hacer `recv` con
  `onMsg != nil` → `EINVAL` en el acto; un SEGUNDO `on_message` con uno activo → `EINVAL`
  (un único consumidor lógico del canal). NUNCA se elige uno y se ignora el otro.
- `Sub:cancel` pone `onMsg = nil` (en `release`): libera el worker para volver a `recv`.

## tasks/timers/futures dentro del worker (G15) y `terminate`: blindados, nada que arreglar

El mini-runtime del worker (scheduler propio de S34, reuso de S04) ya soporta todo
`enu.task` [W]. S35 lo BLINDA por test (`TestWorkerInternalTasksTimersFutures`): un worker
corre varias tasks (`spawn`/`await`), un `future` (`set`/`await`), `sleep` y un `every`
periódico, SIN watchdog. No hizo falta tocar el scheduler del worker. `terminate` quedó
inmediato y seguro en S34 (`cancelAllTasks` + `Close` espera la goroutine);
`TestWorkerTerminateDoesNotAffectParent` confirma idempotencia y que el padre sigue.

## CP-8 (cierra la Fase 7)

`TestCP8WorkerIndexesRepo`: un worker con `caps={"fs.read","search"}` indexa un repo de
prueba (recorre con `enu.search.files`, lee con `enu.fs.read`) y devuelve un digesto al
principal vía `send`/`recv`; DENTRO del worker `enu.fs.write` y `enu.ui` NO existen
(deny-by-default, G6, comprobado con `assert` desde el propio worker); un segundo worker
`terminate`-ado a mitad no afecta al padre. El backpressure de `send` al llenar la cola
acotada lo cubre `TestWorkerBackpressure` (S34), coherente con CP-5; no se duplica.

## Resultado

`CGO_ENABLED=0 go build ./...` y `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde, y
`-race -count=5 -run Worker`/`-run 'CP8|OnMessage'` verdes, **sin data races ni flaky**
(la entrega de `on_message` es timing-sensible —drenador de fondo + token— y aguantó el
sondeo bajo `-race`). No regresiona S01–S34. Cierra la Fase 7 (Workers); arranca la Fase
8. Puntero ▶ avanza a **S36**.
