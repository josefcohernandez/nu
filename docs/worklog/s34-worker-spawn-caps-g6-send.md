# S34 — `enu.worker.spawn` + caps (G6) + send/recv con colas acotadas (api.md §13, 🔒; abre Fase 7)

Primera sesión de la Fase 7 (Workers). Implementa el **paralelismo opt-in** (ADR-008):
`enu.worker.spawn` levanta un estado Lua NUEVO y aislado en su propia goroutine, con su
propio scheduler, comunicado con el padre por colas acotadas de mensajes JSON-ables
copiados. El filtrado de `caps` (G6) y el backpressure de las colas son la lógica 🔒.

## El mini-runtime del worker (G15): reuso del scheduler SIN watchdog

Un worker es un `*Runtime` "recortado" (`newWorkerRuntime`, worker_registry.go): mismo
motor que el principal —estado Lua sandboxeado (`applySandbox`) + scheduler propio
(`newScheduler`)— pero con `isWorker=true` y, sobre todo, **presupuesto de slice 0** →
`armWatchdog` es no-op (G15: los workers existen para quemar CPU; el control es
`terminate()` y las `caps`, no el watchdog). Decisión clave: **no se reimplementa nada del
event loop**; el worker REUSA la maquinaria de S04 (token/`suspend`/`runTask`/`waitIdle`).
El módulo del worker corre **como una task** (no como chunk sobre `host`): un chunk no
podría suspender (`requireTask` exige `L != rt.L`), y el patrón natural del worker es un
bucle de `enu.worker.parent.recv()` (⏸). Por eso `run` hace `s.spawn(require(module))` y
luego `waitIdle`. Un worker es headless siempre (`uiActive=false`, `ui=nil`): nada de
`enu.ui`.

## El filtrado de caps (G6): deny-by-default, dos granularidades

`registerWorkerNu` registra TODA la superficie [W] en el `enu` del worker reusando las
mismas `registerXxx` del principal (un solo punto de verdad), y DESPUÉS **poda el árbol**
por `caps` (`pruneByCaps`). Registrar-y-podar es más simple y robusto que registrar función
a función. Tres granularidades de decisión por módulo `M`:
- `caps["M"]` (p. ej. `"fs"`) → módulo entero conservado.
- algún `caps["M.fn"]` (p. ej. `"fs.read"`) → solo esas funciones; si ninguna existe, M se
  elimina entero.
- ni una ni otra → M eliminado (deny-by-default).

Sin `caps` (`capsGiven=false`) → toda la API [W]. Con `caps={}` (vacío) → casi nada. Lo no
concedido **NO EXISTE** (es `nil`), el mismo modelo que el gating de `enu.ui` (G20): no se
"lanza EACCES", la superficie simplemente no está, así que un plugin no puede ni nombrarla.
**Deny-by-default para superficie nueva**: una función añadida luego a un módulo NO queda
concedida por una `caps` antigua de granularidad de función (solo lo enumerado sobrevive);
`"M"` entero sí concede lo futuro de M, por diseño. NO son [W] (nunca llegan al worker):
`enu.ui`, `enu.events`, `enu.fs.watch`, `enu.worker.spawn` (sin anidar) ni `enu.plugin` (§16).
`enu.version`/`enu.has` y `enu.worker.parent` van SIEMPRE (no son superficie recortable: la
detección de capacidades y el canal con el padre). `enu.config.dir`/`data_dir` son [W] (§14).

## Las colas acotadas / backpressure (§13)

Dos canales Go acotados (`workerQueueCap=16`) por worker: `toWorker` (padre→worker) y
`fromWorker` (worker→padre), más un `done` que se cierra al terminar. `Worker:send` (padre)
y `enu.worker.parent.send` (worker) son ⏸: encolan SUSPENDIENDO si la cola está llena
(backpressure, §13/§8) —el envío real ocurre FUERA del token, en la goroutine de fondo del
puente `suspend`, así otra task del MISMO estado progresa mientras el send espera hueco—.
A diferencia de los streams de §8 (que fallan con `EIO` al desbordar), el send del worker
**suspende** (la cola es un punto de rendez-vous con ritmo, no un buffer que rebosa). `recv`
suspende hasta que llega un mensaje. Una punta cerrada (`done`): `send` → `ECLOSED`; `recv`
→ `nil` (fin de canal, coherente con `Ws:recv`), drenando primero lo que quedara encolado.

## La copia de mensajes JSON-ables (aislamiento ADR-008)

NADA de Lua cruza entre estados. `Worker:send` convierte el valor Lua a su representación Go
neutra con `luaToGo` (el codec de §12/S18) BAJO EL TOKEN DEL EMISOR, ANTES de suspender —lo
que valida que sea JSON-able y rechaza closures/userdata/threads/Blocks con `EINVAL`—; el
valor Go neutro (no un LValue) es lo único que cruza el canal entre goroutines; el receptor
lo reconstruye con `goToLua` bajo SU token. Así una tabla se COPIA (mutarla tras enviarla no
afecta al otro lado) y los dos estados Lua nunca comparten memoria. `useNull=false`: los
mensajes son valores JSON-ables corrientes, no documentos JSON (el sentinel `enu.json.NULL`
es userdata por-estado y no podría cruzar de todas formas). De aquí sale "cero data races"
con DOS schedulers: cada `*lua.LState` solo lo toca su goroutine bajo su token; el cruce es
copia + happens-before por canal.

## `terminate` inmediato y sin fuga (arreglo del review de S34)

`Worker:terminate()` debe ser **inmediato y seguro** (§13). El primer corte de S34 solo
cerraba `done`, que únicamente observan `send`/`recv` en las colas: una task del worker
suspendida en `enu.task.sleep`/`http`/`proc`/`await`/... NO se despertaba, así que
`driveUntilDone`→`waitIdle` bloqueaba hasta la quiescencia y un `sleep(60000)` colgaba ~60 s.
Resultado: tras `terminate()`+`Close()` la goroutine del worker seguía viva (FUGA); como el
worker comparte el `log`/`data_dir` del padre y su `Close` no cierra el log (`isWorker`), la
goroutine fugada tocaba el dataDir mientras el test lo borraba → fallos intermitentes
"directory not empty" bajo `-race -count`. Ese era el bloqueante del review.

**La corrección, reusando el substrato de cancelación de S07/S08:** `terminate()` ahora, ANTES
de cerrar `done`, llama a `scheduler.cancelAllTasks()` sobre el scheduler DEL WORKER. Eso:

1. Cierra un nuevo canal `cancelAll` del scheduler (idempotente, `cancelAllOnce`). `suspend` y
   `taskAwait` lo observan en su `select` **en paralelo al `cancelCh` por task**: cualquier task
   suspendida en CUALQUIER ⏸ despierta AQUÍ y aborta por el mismo camino (`abort` → `cleanup`)
   que una cancelación individual. Es la cancelación cooperativa de S07/S08 disparada sobre
   TODAS las tasks vivas a la vez, en vez de una por su handle. En el estado PRINCIPAL
   `cancelAll` nunca se cierra (su fin de vida es `Close`, que corta los recursos de fondo, no
   las tasks Lua); solo un worker lo cierra.
2. Cancela el `context` de cada task viva (iterando `coToTask`): es lo único que rompe un slice
   de **CPU pura** que nunca suspende (un worker no tiene watchdog, G15). Así tampoco un
   `while true do end` deja la goroutine del worker colgada.

Con (1)+(2) `waitIdle` alcanza la quiescencia de inmediato; la goroutine del worker
(`driveUntilDone`+`shutdown`) cierra su `*lua.LState`, marca `terminated` y muere. SIGUE
esperando a la quiescencia antes de cerrar el estado —cerrarlo a media-task sería carrera—,
pero ahora la quiescencia llega al acto, no al vencer el `sleep`. `terminate()` desde Lua NO
bloquea (es "inmediato"). La seguridad thread: `cancelAllTasks` la llama la goroutine del
PADRE (sin el token del worker), pero solo cierra un canal y llama a `context.CancelFunc`
(seguros desde cualquier goroutine); no toca Lua ni los campos de aborto de las tasks
(`aborting`/`reason`/`canceled`), que las propias goroutines siguen escribiendo bajo su token
al despertar —invariante de S08 intacto—.

**`Close`/`stopAllWorkers` del padre ESPERAN la goroutine del worker.** Tras `terminate`, el
padre llama `w.wait()` (← `terminated`): no devuelve el control hasta que la goroutine del
worker cerró su `*lua.LState`. Sin esa espera quedaba la fuga/carrera de cleanup descrita. Se
dispara `terminate` a TODOS los workers primero y se espera después (apagado en paralelo). El
estado principal NUNCA toca el Lua del worker: la goroutine del worker es la dueña de su Lua y
la que llama a `wrt.Close()`. El log es COMPARTIDO con el padre: `Runtime.Close` NO lo cierra
cuando `isWorker`.

**FRONTERA con S35** (NO en S34): `Worker:on_message` (excluyente con `recv`, G8 → `EINVAL` en
el acto) y la prueba a fondo de varias tasks/timers/futures DENTRO del worker. El mini-runtime
ya las SOPORTA (reusa el scheduler), pero su validación exhaustiva es S35.

## Sin ampliar la API ni hallazgos

`§13` bastó EXACTA: ni una función pública de más; `APILevel` sigue en 1 (§13 ya estaba en
api.md). Sin hallazgos `G##`: la división Go/Lua y el puente `suspend` de S04 dieron todo.

## Tests 🔒 (worker_test.go), nombrando G6/G15

- **caps G6** (`TestWorkerCapsTwoGranularities`): el worker INSPECCIONA su propia API y
  reporta al padre. Cuatro casos: sin caps (toda la API [W]); `caps={"fs"}` (todo fs, no
  http); `caps={"fs.read"}` (fs.read SÍ, fs.write NO, http NO); `caps={}` (casi nada). En
  todos: `ui`/`events`/`worker.spawn` ausentes (§16), `version`/`worker.parent` presentes.
- **backpressure** (`TestWorkerBackpressure`): un worker que no consume llena la cola; el
  productor SUSPENDE (no completa los 1000 envíos) y una task testigo del padre PROGRESA
  (el loop no se congela).
- **copia** (`TestWorkerMessageCopied`): el padre muta su tabla tras enviarla; el worker ve
  el valor del envío (7), no la mutación (999).
- **no-serializable** (`TestWorkerSendNonSerializable`): enviar una función → `EINVAL`.
- **round-trip** (`TestWorkerRoundTrip`): padre send → worker parent.recv → worker
  parent.send → padre recv (eco con transformación).
- **sin watchdog G15** (`TestWorkerNoWatchdog` funcional: un cómputo de CPU largo COMPLETA;
  `TestWorkerSchedulerHasNoWatchdog` estructural: `wrt.sched.budget<=0` aunque el padre
  tenga watchdog — no depende de temporización, robusto bajo `-race`).
- validación de args (`TestWorkerSpawnValidation`), `requireTask` (`TestWorkerSendRecvRequireTask`),
  `recv` tras `terminate` → `nil` (`TestWorkerRecvAfterTerminate`).
- **`terminate` inmediato y sin fuga (review)** (`TestWorkerTerminateInterruptsSleep`): un worker
  suspendido en `enu.task.sleep(60000)` es cortado al acto por `terminate()` —`terminate`+`Close`
  completan MUY por debajo del sleep— y `runtime.NumGoroutine()` tras `Close` vuelve al nivel
  previo al spawn (la goroutine del worker terminó, no quedó colgada tocando el `data_dir`/`log`).
  (`TestWorkerTerminateInterruptsCPULoop`): un worker en bucle de CPU pura (`while true do end`,
  sin punto de suspensión) también se corta —cancelación del `context`—, sin colgar `terminate`+`Close`.

`CGO_ENABLED=0 go build ./...`, `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=4 ./internal/...` y `-count=8 -run Worker`
verdes, **sin data races, sin flaky ni fallos de cleanup ("directory not empty")** (es el test
de races más exigente hasta ahora: DOS goroutines de scheduler en paralelo). No regresiona
S01–S33. Puntero ▶ sigue en **S35**.
