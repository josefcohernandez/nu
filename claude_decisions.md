# Decisiones y desviaciones de implementación

Este fichero recoge decisiones de implementación que no estaban especificadas al
detalle en los documentos de diseño y desviaciones puntuales del plan, una
entrada por sesión. No sustituye al flujo de diseño (`problemas.md`/`adr.md`):
recoge lo operativo que no llega a hallazgo `G##` pero que la sesión siguiente
debe poder reconstruir.

## S05 — `nu.task.sleep`/`defer`/`every` + `Timer:stop` (api.md §3)

### Semántica de quiescencia con timers activos (decisión clave)

`api.md` §3 no dice cómo interactúan los timers con el fin de `nu -e`. El
modelo de S04 hace que `EvalString` corra el chunk, suelte el token y llame a
`waitIdle()`, que bloquea hasta que el conjunto queda **quiescente**. Había que
decidir qué cuenta como "trabajo pendiente":

- **`defer(fn)` SÍ cuenta.** Es "el siguiente tick": su handler debe correr
  antes de que `EvalString` devuelva. Se contabiliza con un contador `pending`
  (incrementado al encolar, decrementado al ejecutar el disparo); `waitIdle`
  espera a `live == 0 && pending == 0`. Sin esto, un `defer` encolado por el
  chunk podría no llegar a correr nunca.

- **`every(ms, fn)` NO cuenta.** Un timer periódico no termina jamás; si contara
  para la quiescencia, `nu -e` no volvería nunca. Decisión: un `every` activo es
  **facilidad de fondo**, no trabajo de primer plano. El fin de `nu -e` lo
  determinan el chunk + sus tasks + sus `defer` encolados; cuando todo eso queda
  quiescente, `EvalString` vuelve aunque haya timers activos, y `Runtime.Close`
  los apaga (corta sus goroutines de ticker, sin fugas).

  Justificación: en un `nu` interactivo (S33+) el loop sigue vivo por la UI/los
  eventos de entrada, no por los timers; bajo `nu -e` (headless, sin UI) el fin
  natural es la quiescencia del primer plano. Un timer que debiera mantener vivo
  el proceso indica que el trabajo real está en una task (que sí cuenta), no en
  el timer. Esto es coherente con el criterio de hecho de S05 en el plan
  ("`every` dispara N veces y `stop` lo corta"): los tests anclan el runtime con
  una task mientras el timer tickea.

### Handlers síncronos sobre thread efímero (no sobre `host`)

`defer` y cada disparo de `every` ejecutan un handler **síncrono** (no ⏸, §3):
corren bajo el token, como el chunk y los handlers de eventos. Se ejecutan sobre
un **thread Lua dedicado por disparo** (`host.NewThread()`), no sobre la pila del
estado principal. Motivo: mientras `EvalString` está en `waitIdle`, la pila de
`host` aún custodia los valores de retorno del chunk; un `CallByParam` sobre
`host` podría interferir. Es la misma estrategia que las tasks (cada una sobre su
`co`). Coste: un thread por disparo, recogido por el GC de gopher-lua (no hay
`Close` por thread en la API, igual que para los `co` de las tasks).

### `stop` sin disparo tardío (carrera tick/token)

Un disparo de `every` puede quedar esperando el token justo cuando llega `stop`.
Para garantizar "tras `stop`, ni un tick más", el disparo usa
`runSyncHandlerCancelable`: mientras espera el token atiende también a `stopCh` y,
si se cerró, no ejecuta. `stopTimer`/`stopAllTimers` cierran `stopCh` de forma
idempotente (solo si el timer sigue rastreado), así que `Timer:stop()` doble no
entra en pánico.

### Convención de tests con `-race`

`go test -race` exige cgo; el resto del proyecto compila con `CGO_ENABLED=0`
(ADR-001). Por tanto: `CGO_ENABLED=0 go build ./...` para el binario, y
`CGO_ENABLED=1 go test -race -count=4 ./...` para la suite con detector de
carreras (igual criterio que dejó S04 en la bitácora). Los tests de timing usan
periodos cortos (1-5 ms) y esperas holgadas; `-count=4`/`-count=8` no produjeron
flaky.

### Sin hallazgos

El modelo de S04 (goroutine-por-task + token) bastó para S05 sin ampliar la API.
No se abrió ningún `G##`.

## S06 — `nu.task.future` (rendez-vous de un solo uso, api.md §3)

### Desviación de procedimiento: rama desde `origin/main`

Esta sesión se implementó partiendo de `origin/main`, donde el puntero ▶ ya
marcaba `S06` (S05 quedó mergeada). El ramaje local de trabajo estaba desfasado;
se creó `claude/s06-future` desde `origin/main` para arrancar sobre el estado
real. No hay desviación de *alcance*: S06 depende solo de S04 (cerrada), así que
el grafo de dependencias se respeta.

### Quiescencia: `set`/`await` NO tocan `live`/`pending` (decisión clave)

Un awaiter bloqueado en `Future:await` es una **task que ya está contada en
`live`** (se contó al hacer `spawn`); no termina hasta que su `await` retorna,
exactamente igual que una task bloqueada en `Task:await`. Por tanto los futures
no añaden contabilidad de quiescencia propia: reusan la de S04/S05 sin tocarla.
`set` tampoco mueve el conteo: resuelve y despierta, pero no crea ni destruye
trabajo de primer plano.

Consecuencia aceptada y coherente con el modelo: un `Future:await` sin un `set`
que lo resuelva cuelga `waitIdle` para siempre —es el mismo "deadlock de primer
plano" que una task esperando a otra que nunca acaba—. Detectarlo exigiría API
nueva (detección de deadlock) que api.md §3 no contempla; no es responsabilidad
del future inventarla.

### Despertar de múltiples awaiters con un único `set`

Se reusa el patrón de `Task:await`: un canal `resolvedCh` que `set` **cierra**
(bajo el token). El cierre de canal es un broadcast natural —todos los awaiters
bloqueados en `<-resolvedCh` despiertan a la vez— y aporta el happens-before que
hace visible el `value` (escrito bajo token antes del cierre) cuando cada awaiter
recupera el token. No hace falta candado propio en `resolved`/`value`: ambos se
tocan solo bajo el token (el token *es* el candado), y el único cruce entre
goroutines es el cierre del canal. Esto es lo que blinda el test `-race`.

### `set()` sin argumento resuelve con `nil`

Coherente con que un future pueda usarse como mera señal ("ya ocurrió") y no solo
como portador de valor. No es API nueva: `Future:set(v)` con `v` opcional cae en
el `LNil` que devuelve `L.Get(2)` cuando no se pasó argumento. `set()` con nil
sigue consumiendo el único uso (un segundo `set` da `EINVAL`): resolver con nil
es resolver.

### Sin hallazgos

El modelo de S04/S05 bastó para S06 sin ampliar la API. No se abrió ningún `G##`.

## S07 — `nu.task.all` / `nu.task.race` (api.md §3)

### La frontera S07/S08: substrato de cancelación interno (decisión clave)

`all`/`race` necesitan "cancelar el resto", pero la cancelación PÚBLICA es S08
(`Task:cancel()`, `nu.task.cleanup` con pila LIFO, `ECANCELED` observable en
`await`, y la garantía formal de que el desenrollado **no es capturable** por un
`pcall` de usuario). S07 implementa solo el **substrato interno mínimo** que esos
dos combinadores requieren, diseñado para que S08 lo **reutilice y extienda, no lo
reescriba**:

- **`cancelCh` + `canceled` por task** (`scheduler.go`). Cada task tiene un canal
  de señal que se cierra una sola vez (`cancelOnce`). `cancelTask(t)` lo cierra y
  marca `t.canceled`. Es el único punto de entrada del substrato; lo llaman
  `all`/`race` sobre las perdedoras.

- **Observación en los puntos de suspensión.** `suspend` (de donde cuelga todo ⏸:
  `sleep`, el `suspend_echo` de prueba, y los `all`/`race` mismos), `Task:await` y
  `Future:await` ahora hacen `select` también sobre `cancelCh`: si la task que se
  suspende es cancelada mientras espera —o ya estaba cancelada al llegar al ⏸—,
  aborta en ese punto. Es **cancelación cooperativa**: surte efecto en el siguiente
  ⏸, no a media ejecución de Lua (eso, para CPU pura, es el watchdog de S09).

- **Desenrollado por pánico centinela** (`abortSignal`, `scheduler.abort`). Al
  detectar la cancelación, `suspend`/`await` lanzan un `panic(abortSignal{t})` que
  desenrolla la pila Go de la goroutine de la task. `runTask` lo recibe a través
  del `CallByParam` (gopher-lua convierte cualquier pánico Go en error al cruzar su
  `PCall` interno) y, viendo `t.canceled`, descarta el desenlace: una task
  cancelada **no entrega `results` ni `errValue`** y no se loguea (la cancelación
  es deliberada).

- **`coToTask`** (`sync.Map` en el scheduler): mapea el thread Lua de cada task
  viva a su `*task`, para que `suspend` halle el `cancelCh` de quien se suspende.
  Se puebla/limpia en `runTask`.

**Lo que S07 deja a propósito MÍNIMO y S08 formalizará:**

1. **No capturable por `pcall` de usuario.** En S07 el pánico de aborto SÍ podría
   ser atrapado por un `pcall` de Lua que envolviera el punto de suspensión —es el
   mismo motivo de ADR-011: gopher-lua recupera todo pánico Go en su `PCall`
   interno—. Para S07 basta porque las perdedoras de `all`/`race` (y sus tests) no
   envuelven su ⏸ en `pcall`. La garantía formal "**no capturable**" (§1.3) es S08:
   requerirá su propio mecanismo (re-lanzar `abortSignal` tras cada frontera
   `pcall`, o marcar el thread como "abortando" para que el `pcall` de usuario no
   lo trague). El tipo `abortSignal` se dejó distinguible para que S08 lo reconozca
   y reinyecte.

2. **`nu.task.cleanup` (pila LIFO) durante el aborto.** No existe aún; S08 correrá
   los liberadores registrados durante el desenrollado.

3. **`ECANCELED` observable.** Una task cancelada hoy simplemente no entrega
   resultado; `await` sobre ella vería un desenlace vacío. S08 hará `ECANCELED`
   observable en `await` (§1.3), sin que ello capture la cancelación.

4. **`Task:cancel()` público.** S08 expondrá `cancelTask` como método del handle
   `Task`. S07 no añade superficie pública: las únicas firmas nuevas son `all` y
   `race` (§3, API sagrada).

5. **Propagar la cancelación al trabajo de fondo en curso.** En S07, una task
   cancelada durante un `sleep` deja correr el `time.After` de fondo hasta su fin
   (su `deliverFn` se descarta). S08/posteriores podrán pasar un `context` al
   trabajo de fondo para abortarlo de inmediato; aquí no hace falta.

### Fan-in concurrente: detectar el primer error sin orden (decisión)

`all` debe cancelar al resto **en cuanto** una task falla, no cuando le toque por
orden de array. Un primer intento esperando los `doneCh` en orden (`for i := range
tasks { <-t.doneCh }`) fallaba: una primera task lenta retrasaba ver el fallo de
una segunda rápida, y la lenta llegaba a completar antes de ser cancelada (lo
cazó `TestAllCancelsOthersOnError`). La solución es `waitAllOrFirstError`: una
goroutine efímera por task reporta su cierre a un canal común; el bucle devuelve
el índice del primer fallo en cuanto ocurre, o -1 si todas terminan bien. `race`
usa el simétrico `waitFirst` (primer cierre gana, sea por éxito o por error).

### Alineamiento G27: indexar por posición, no por terminación

El invariante 🔒 (G27) sale gratis de la estructura: `all` resuelve la lista a un
slice `tasks[]` en orden de la tabla (clave 1..n) y rellena `out[i+1]` con
`firstResult(tasks[i])`. El orden en que cierran los `doneCh` no toca el array de
salida: se indexa por posición. `race` devuelve el índice del ganador **+1**
(1-based, Lua). Tests con sleeps inversos (terminación 3,2,1 frente a entrada
1,2,3) blindan que no se cuela el orden de terminación.

### Entrada: handles, funciones o mezcla

§3 dice `Task[]|fn[]`. Se interpreta de la forma más permisiva y coherente con la
prosa ("handles ya creados O funciones"): cada elemento del array puede ser una
función (se le hace `spawn`) o un handle `Task` (se adjunta), y pueden mezclarse.
Un valor de otro tipo, o un array vacío, es `EINVAL` con mensaje que nombra la
posición. Cada task entrega su **primer** valor de retorno (§3: el array de `all`
y el `result` de `race` son de un valor por entrada, no multivalor).

### Sin hallazgos

El modelo de S04/S06 más el substrato de cancelación interno bastaron para S07 sin
ampliar la API ni tocar `api.md` §3. La frontera S07/S08 es **orden de
implementación**, no un `G##`: se resolvió con el substrato mínimo descrito arriba.

## S08 — Cancelación pública: `Task:cancel` + `nu.task.cleanup` + desenrollado no capturable (api.md §1.3, §3)

S08 está en el inventario 🔒 y es un **hito de veto** (valida ADR-008). El punto
difícil —y el que podía vetar el plan— era hacer el aborto **no capturable por
`pcall`** sobre gopher-lua, que recupera todo pánico Go en su `pcall` nativo. La
técnica conocida (envolver `pcall`/`xpcall`) funcionó limpia; **no hubo
hallazgo/veto**.

### La técnica del no-capturable: wrapper de `pcall`/`xpcall` (decisión clave)

gopher-lua implementa `pcall`/`xpcall` en Go (`basePCall`/`baseXPCall`) sobre
`LState.PCall`, cuyo `defer/recover()` captura **cualquier** pánico Go y lo
entrega a Lua como `false, err`. Por eso en S07 el `abortSignal` (un pánico Go)
SÍ era capturable por un `pcall` de usuario que envolviera el ⏸. Para blindarlo
(§1.3), `cancel.go` **reemplaza los globales `pcall` y `xpcall`** (que el baseline
de S01 abre nativos; los sustituye `installCancelPcall`, llamado por `registerNu`
tras `applySandbox`) por versiones Go que:

1. Reproducen `basePCall`/`baseXPCall` (incluida la comprobación "es llamable" y
   el multi-retorno en éxito), delegando en `LState.PCall`.
2. Ante un error capturado, consultan el flag **`task.aborting`** de la task en
   curso (la que corre sobre el `LState` actual, vía `coToTask`). Si está
   abortando, **re-lanzan** `abortSignal{t}` en vez de devolver `false, err`. Así
   el aborto se cuela por cada frontera `pcall`/`xpcall` —anidadas incluidas—
   hasta el `CallByParam` de `runTask`, único que lo recupera legítimamente.

`scheduler.abort` pone `t.aborting = true` **justo antes** de lanzar el centinela;
`runCleanups` lo baja antes de correr los liberadores (así un `pcall` dentro de un
cleanup vuelve a capturar con normalidad).

### Por qué `task.aborting` y no el valor del pánico (decisión)

Al cruzar `LState.PCall`, un pánico que no sea `*lua.ApiError` se convierte en un
`*ApiError` con su mensaje vía `fmt.Sprint` —se pierde el tipo Go `abortSignal`—.
Detectar el aborto por el valor recuperado sería frágil (dependería de la
representación textual). En cambio `aborting` es un flag de la propia task,
escrito y leído por su única goroutine **bajo el token**: detección robusta e
independiente de cómo gopher-lua represente el pánico. Sale gratis el re-lanzado
idéntico (reconstruimos `abortSignal{t}` desde la task). S09 reusará exactamente
este camino poniendo `reason = abortBudget`.

### `xpcall`: el `errfn` del usuario NO ve el aborto (decisión)

El `xpcall` nativo correría su message handler (`errfn`) **dentro** de
`LState.PCall`, es decir, sobre el aborto. Eso filtraría el aborto al código del
usuario (§1.3 lo prohíbe). La versión envuelta pasa `nil` como manejador al
`PCall` nativo y aplica `errfn` **nosotros, solo si el error NO es un aborto**.
Coste aceptado: el `errfn` corre tras desenrollar (no antes, como en Lua de
verdad), pero gopher-lua no expone traceback rico al handler, así que no se pierde
nada observable.

### Semántica de `ECANCELED` en `await` (decisión clave)

`Task:await` de una task **cancelada** entrega `ECANCELED` (estructurado), que el
awaiter **SÍ puede capturar con `pcall`**. Es coherente con §1.3 porque es
**observación de la cancelación de OTRA task**, no el aborto del propio awaiter:
si cancelaran al awaiter mismo, su desenrollado sería inmune; pero *observar* que
una task que esperaba fue cancelada es un error normal y capturable. El awaiter
sigue vivo tras el `pcall` (corre el código de después). Implementación:
`taskAwait` comprueba `t.canceled` (antes que `t.errValue`, que una task cancelada
nunca tiene) y lanza `ECANCELED` con `raiseError`.

### `Task:cancel` sobre una task ya terminada es no-op (decisión clave)

Cancelar una task que **ya cerró su desenlace** NO debe convertir retroactivamente
su resultado en `ECANCELED` —terminó bien (o con error) antes de la cancelación, y
eso es lo que su `await` debe seguir entregando—. `cancelTask` chequea `t.done` y
retorna sin tocar `canceled`. Es seguro leer `t.done` ahí porque todas las
llamadas (`Task:cancel`, `all`/`race`) corren **bajo el token**, igual que el
`t.done = true` de `runTask`. `Task:cancel` **no suspende** (es síncrona desde
fuera, §3); cancelar dos veces es idempotente (`cancelOnce`); cancelarse a sí
misma es legal (surte efecto en el siguiente ⏸ propio).

### Pila LIFO de `cleanup`: corre en los TRES finales (decisión)

`task.cleanups []*lua.LFunction`; `nu.task.cleanup(fn)` apila (fuera de task →
`EINVAL`, no hay task a la que atar el liberador). `runCleanups` (en `runTask`,
con el token tomado, tras el `CallByParam`) corre TODOS en orden inverso al de
registro —semántica `defer`— pase lo que pase: éxito, error o aborto. Cada
liberador corre sobre un **thread efímero** (como las tasks y los handlers de S05)
bajo `pcall` por frontera (ADR-008): un cleanup que lanza queda en el log
(best-effort; evento formal en S10) y no impide que corran los demás ni tumba el
proceso.

### Substrato S07 reutilizado, no reescrito

Siguen intactos `cancelCh`/`cancelTask`/`abortSignal`/`coToTask` y los `select`
sobre `cancelCh` en `suspend`/`Task:await`/`Future:await`. S08 **añade**: el flag
`aborting`, el `abortReason` (`abortCancel` vs `abortBudget`, este último para S09),
la pila `cleanups`, los métodos públicos `Task:cancel`/`nu.task.cleanup`, el
`ECANCELED` en `await`, y los wrappers de `pcall`/`xpcall`. Superficie pública
nueva = SOLO `Task:cancel` y `nu.task.cleanup` (API sagrada, §3).

### Sin hallazgos ni veto

gopher-lua v1.1.2 **sí permite** un desenrollado no capturable limpio vía el
wrapper de `pcall`/`xpcall`. No se rompieron los errores normales de §1.4 (siguen
capturables, multi-retorno incluido). No se amplió `api.md`. No se abrió ningún
`G##`. El hito de veto S08 queda validado a favor de ADR-008.

### Qué hereda S09 (watchdog)

S09 reusa el **mismo desenrollado no capturable**: cortará el slice de CPU puro
excedido lanzando el mismo `abortSignal` —pero desde el watchdog, no desde un
punto de suspensión— con `reason = abortBudget`. Los wrappers de `pcall`/`xpcall`
ya lo harán no capturable (consultan `aborting`, agnóstico al `reason`);
`runCleanups` ya corre en el aborto sea cual sea el motivo; `await` distinguirá el
motivo para observar `EBUDGET` en vez de `ECANCELED`, y S09 emitirá
`core:plugin.misbehaved` (verificable tras S10). El gancho técnico que falta es
**interrumpir un slice Lua que no suspende** (un bucle de CPU puro): eso es trabajo
propio de S09 (hook de instrucciones / `LState` con límite), no de S08.

## S09 — Watchdog de slice (api.md §1.3)

### Interrumpir un slice de CPU puro: `LState.SetContext` (decisión clave, hito de veto)

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

### El watchdog corre en su propia goroutine, sin el token (decisión clave)

La clave del "sin congelar el loop" (CP-2). El temporizador del slice es un
`time.AfterFunc(budget)` que se **arma** cuando la task toma el token para correr
Lua (inicio de `runTask`; re-adquisición tras cada ⏸ en
`suspend`/`Task:await`/`Future:await`) y se **desarma** justo antes de soltarlo.
Si dispara, su callback corre en la goroutine del timer —que **no tiene el
token**—, por eso puede cortar a una task que lo monopoliza mientras otras tasks
y timers esperan: tras el corte, la víctima desenrolla hasta `runTask`, suelta el
token, y el resto progresa. El presupuesto es 100 ms por defecto, configurable con
`WithSliceBudget` (`Option` del Runtime; gancho que S11/S12 cablearán a
`nu.toml`); `<=0` desactiva el watchdog.

### Cada slice se mide aparte: arm/disarm en cada ⏸ (decisión)

Un ⏸ cierra el slice (desarma) y, al re-adquirir el token, abre uno nuevo (arma).
Así un bucle de CPU intercalado con suspensiones no acumula tiempo entre slices:
cada tramo continuo tiene su propio presupuesto. De ahí "sin falsos positivos":
trabajo normal que cede a menudo (sleeps, IO) nunca dispara el watchdog aunque su
tiempo TOTAL exceda el presupuesto.

### Reparto de escritura entre watchdog y task: invariante de S08 intacto (decisión clave)

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

### `EBUDGET` vs `ECANCELED` en `await`: se distingue por `reason` (decisión)

`Task:await` de una task abortada por watchdog observa **`EBUDGET`**; de una
cancelada, **`ECANCELED`**. Ambos son *observación* de OTRA task —el awaiter SÍ
los captura con `pcall` y sobrevive—, no el aborto del propio awaiter. La
distinción es por `t.reason` (`abortBudget` vs `abortCancel`); se comprueba antes
que `errValue` (una task abortada nunca tiene `errValue`).

### `core:plugin.misbehaved` por gancho interno (decisión; lo cablea S10)

El bus `nu.events` es S10 (aún no existe). La emisión se hace por un **gancho
interno** `rt.emitMisbehaved(owner, reason)` que hoy loguea best-effort (como el
resto de fallos de task). **S10 lo cableará** a
`nu.events.emit("core:plugin.misbehaved", {plugin = owner, reason = ...})` sin
tocar el watchdog (el punto de llamada ya es único). NO se inventó superficie
pública: §1.3 dice que el watchdog es transparente.

### Alcance: solo el slice de una task (decisión)

Los handlers síncronos (`defer`/`every`) y los `cleanup` corren sobre threads
efímeros de `host`, que no tiene contexto, así que el watchdog no los vigila. El
alcance de S09 (api.md §1.3) es el **slice de una task**; vigilar handlers
síncronos sería otra pieza, fuera de esta sesión. Coherente con que `Close`
cancela el `ctx` de cada task al terminar (evita la fuga de `context.WithCancel`).

### Sin superficie pública nueva, sin hallazgo, sin veto

S09 no añade nada a `api.md` (el watchdog es transparente; `WithSliceBudget` es
`Option` Go, no API Lua). gopher-lua v1.1.2 SÍ permite interrumpir un slice de CPU
puro de forma limpia e integrable con el desenrollado de S08. El hito de veto
S08+S09 queda validado a favor de ADR-008 (aislamiento por tarea). CP-2 verde
cierra la Fase 1.

## S10 — bus de eventos `nu.events` (api.md §4)

### La cola de emits: drenado plano (decisión clave de G10)

`api.md` §4 exige que un `emit` anidado (lanzado por un handler) se **encole** y
se despache al terminar el actual —anchura, no recursión— de modo que un
ping-pong infinito sea "un bucle plano que el watchdog corta", nunca una
recursión que desborde la pila Go. La realización (`events.go`, `scheduler.emit`):

- El bus lleva una `queue []pendingEmit` y un flag `draining`. `emit` SIEMPRE
  encola; si ya hay un drenado en curso (`draining == true`), solo encola y
  vuelve. El **frame raíz** del `emit` (el primero, que puso `draining`) drena la
  cola en un bucle plano `for len(queue) > 0 { dispatch(...) }`. Así un handler
  que re-emite no anida una llamada a `dispatch` en la pila Go: deja un trabajo en
  la cola que el bucle raíz recoge tras terminar el despacho en curso.
- Consecuencia: el orden es **anchura** (BFS), no profundidad. Un handler de "a"
  que emite "b" produce `a:start, a:end, b`, no `a:start, b, a:end`. Tests lo
  fijan.

### El watchdog y el ping-pong infinito (el matiz no obvio)

El requisito "el watchdog corta el ping-pong infinito" tenía una trampa de
implementación que **se verificó con un test antes de darla por hecha**: el
mecanismo de corte de S09 es cancelar el `context.Context` del thread `co` de la
task, que el intérprete vigila en cada instrucción (`mainLoopWithContext`). Pero
los handlers de eventos NO corren sobre el `co` de la task: corren sobre threads
**efímeros de `host`** (como `defer`/`every`), que no llevan contexto. Durante un
ping-pong, la task no ejecuta Lua sobre su `co` —orquesta handlers en Go—, así que
cancelar su ctx no rompe nada y el bucle de drenado seguiría para siempre.
Comprobado: un ping-pong infinito desde una task colgaba 5 s sin cortarse.

Solución, coherente con S09 y **sin pieza ni API nueva**: el bucle de drenado,
cuando el `emit` raíz se lanzó dentro de una task (`taskOf(L)`), comprueba
cooperativamente `claimBudgetAbort(t)` **entre handlers** (en el borde de cada
iteración del bucle). Cuando el watchdog dispara (pone `budgetExceeded`), el
siguiente borde lo reclama y llama a `abort(t)` —el mismo centinela no capturable
de S08/S09—, que desenrolla hasta `runTask`: `EBUDGET` no capturable +
`core:plugin.misbehaved`. Esto ataja el caso que `api.md` §4 nombra (el rebote
entre handlers); un ÚNICO handler con `while true do end` en su interior sigue
FUERA (thread efímero sin ctx, exactamente como `defer`/`every` que S09 dejó
fuera). El límite del watchdog es el mismo que en S09; el borde cooperativo solo
extiende el corte al orquestador del bus, que sí corre en la goroutine de la task.

Detalle de robustez: como `abort` desenrolla por panic a media cola, un `defer` en
`emit` restaura `draining = false`/`queue = nil` al salir; sin él, el bus quedaría
permanentemente atascado (todo `emit` futuro vería `draining == true` y solo
encolaría). El panic sigue subiendo hacia `runTask`, que lo recupera.

### Emitir misbehaved desde la goroutine de la task (seguridad de hilo)

`rt.emitMisbehaved` (el gancho de S09) ahora emite `core:plugin.misbehaved` por
el bus real. Lo llama `runTask` desde la goroutine de la task —sobre el thread
`co`, no sobre `host`— pero **con el token tomado** (antes de `release`). La
pregunta era si es seguro emitir hacia el bus del estado principal desde ahí. Sí,
y se emite **directamente** (síncrono), sin re-encolar a otra goroutine: el bus
toca `host` (la tabla del payload, los threads efímeros de los handlers), no `co`;
y lo que protege esos accesos es el **token**, no qué goroutine/thread los hace.
Ya tenemos el invariante que el bus necesita (token + estado principal), así que
re-encolar sería complejidad sin beneficio. Se pasa `rt.L` (host) como thread
llamante de `emit`, no `co`: la emisión de misbehaved es un solo evento (no un
drenado de task que deba vigilar su propio watchdog —la task que lo motivó ya está
abortada—), así que no se engancha al borde cooperativo del watchdog del drenado.

### Sin superficie de más, sin hallazgo

Superficie nueva = exactamente `nu.events.on/once/emit` + `Sub:cancel` (§4). El
bus es **solo estado principal** (no [W]); en un worker no existe (S34). El modelo
de S04–S09 (token + watchdog + desenrollado no capturable) bastó: la vigilancia
del watchdog en el drenado reusa `claimBudgetAbort`/`abort`, no inventa nada.
APILevel sigue en 1 (api.md ya describía §4; no es una adición post-congelado).
