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

## S11 — loader de plugins (api.md §14)

Superficie nueva exacta: `nu.plugin.current()`, `nu.plugin.list()`,
`nu.config.dir()` [W] y `nu.config.data_dir()` [W] (§14). El arranque canónico
(carga de plugins, `init.lua` del usuario, `core:ready`) lo dispara `Runtime.Boot`,
método Go interno, no superficie Lua. APILevel sigue en 1 (api.md ya describía §14;
no es una adición post-congelado). Sin hallazgo: el modelo de S04–S10 (token +
estado principal + bus de eventos) bastó.

### Dependencia TOML añadida

`github.com/BurntSushi/toml` (resuelto a v1.6.0 por `go get`; TOML **puro-Go**,
coherente con `CGO_ENABLED=0`/ADR-001). Se usa para parsear `plugin.toml` (campos
`name`, `version`, `requires?`) **internamente en el loader**: NO es la API Lua
`nu.toml` (eso es S18, que reusará esta misma librería para `nu.toml.encode/decode`).
`go mod tidy` deja go.mod/go.sum coherentes.

### Modelo del loader

- **Descubrimiento**: por cada directorio pasado con `WithPluginDir`, cada
  subdirectorio con `plugin.toml` es un plugin. La unicidad de nombre se valida en
  el descubrimiento (un `map[name]`); colisión = `EINVAL` accionable que nombra el
  plugin y **ambas rutas**. El nombre es la identidad (§14), lo que deja libres de
  colisión los namespaces de eventos por convención (G26).
- **Orden topológico**: DFS post-orden sobre el grafo de `requires` (el dependido
  antes que el dependiente). Visita determinista (nodos y `requires` ordenados por
  nombre) para que el arranque sea reproducible. Dos errores accionables: ciclo
  (coloreado blanco/gris/negro; un re-encuentro de gris reconstruye el tramo del
  ciclo `a -> b -> a`) y dependencia ausente (`requires` que no corresponde a
  ningún plugin descubierto). La validación del grafo es **total antes** de correr
  un solo `init.lua`: un grafo roto no deja el sistema medio-cargado.
- **Arranque canónico** (`Boot`, bajo el token, en el estado principal — como un
  chunk de `-e`, no como una task): rutas de require → por cada plugin en orden
  topológico {empuja owner, corre `init.lua`, emite `core:plugin.loaded`} →
  `init.lua` del usuario (`config.dir()/init.lua`) **el último** → `core:ready`
  **una vez**. Un `init.lua` que lanza queda **aislado** (ADR-008): se loguea, se
  emite `core:plugin.error`, y los demás plugins + el usuario siguen cargando;
  `Boot` solo devuelve error por un **grafo** inválido (colisión/ciclo/ausente), no
  por un fallo de runtime de un init.

### Rutas de `require`

El baseline (S01, sandbox.go §1.2) dejó `package`/`require` **cerrados**. El loader
abre `OpenPackage` una sola vez en `setupRequirePaths` y fija `package.path` a
**solo** los `lua/` de los plugins (`<dir>/lua/?.lua` y `<dir>/lua/?/init.lua`).
Deliberadamente NO incluye el `./?.lua` que gopher-lua trae por defecto: `require`
es para módulos de plugins, no un agujero para cargar ficheros arbitrarios del cwd
(respeta el sandbox). `cpath` vacío (sin librerías C, CGO_ENABLED=0). El loader usa
`L.LoadFile` para ejecutar los `init.lua` (es el único autorizado a tocar el disco
así, §1.2); `dofile`/`loadfile` siguen deshabilitados como globales.

### Pila de owner para `nu.plugin.current` y el log

`rt.owner` (string, S03) se sustituyó por `rt.ownerStack []*pluginInfo` +
`rt.currentOwner()` (tope de la pila, o `"user"` si vacía). El loader empuja el
contexto del plugin antes de su `init.lua` y lo saca al terminar (defer). Así,
DURANTE el `init.lua` de un plugin, `nu.plugin.current()` y el owner del log son ese
plugin; fuera (chunk de `-e`, `init.lua` del usuario, handlers) son `"user"`. La
pila se muta **solo bajo el token** (el arranque es síncrono) y se lee solo desde
código Lua (que también exige el token): sin candado ni carrera (`-race` verde).
`current()` nunca es `nil`: fuera de plugin devuelve `{name="user", version="",
dir=config.dir}`. Limitación conocida (no del alcance de S11): una task spawneada
por el init de un plugin corre **después** de que el owner se haya sacado, así que
verá owner "user" — el etiquetado fiable de handles por dueño es trabajo de S13
(reload), que se construye sobre esta pila.

### Frontera con S12/S13

- **S12** (activación por `nu.toml` + embebidas `go:embed`): el campo
  `pluginInfo.Source` (`"user"`/`"builtin"`) y `pluginInfo.Enabled` están previstos
  pero en S11 son siempre `"user"`/`true`. `WithSliceBudget`/`WithDataDir`/
  `WithConfigDir`/`WithPluginDir` son los ganchos que S12 cableará a `nu.toml`. El
  gancho de activación (qué plugins se cargan) vive en `loader.discover`/`Boot` sin
  adelantar su lógica.
- **S13** (`nu.plugin.reload`): se apoya en `ownerStack`/`currentOwner` para
  etiquetar handles por dueño (G2); la recarga en sí (vaciar caché de require,
  `core:plugin.unload`, re-correr init) NO se implementa en S11.

## S12 — activación de extensiones embebidas gobernada por `nu.toml` (api.md §14, ADR-010)

S12 monta lo que ADR-010 exige: las extensiones oficiales se distribuyen DENTRO
del binario (`go:embed`) pero están **INACTIVAS por defecto** —nu instalado es un
runtime desnudo; el harness se activa, no se presupone—. La activación la gobierna
`config.dir()/nu.toml`.

### `nu.toml` es config del core, no la API Lua `nu.toml`

`config.dir()/nu.toml` (config_toml.go) configura al PROPIO runtime: no se confunde
con `nu.toml` el codec (API Lua de S18). Ambos reusan la misma librería TOML pura-Go
añadida en S11 (BurntSushi), pero son cosas distintas. Campos v1 leídos:
`plugins.enabled` (lista de activación), `plugins.dirs` (rutas extra), y
`watchdog.slice_budget_ms`. Claves desconocidas se ignoran (forward-compat, igual
que `plugin.toml`). Un `nu.toml` AUSENTE es lo normal del runtime desnudo: no activa
nada y no es error.

### Parseo en `New`, error de config APLAZADO a `Boot`

`nu.toml` se parsea en `New` (ahí ya se conoce `config.dir()` y sus valores deben
estar listos antes de `Boot`: el budget del watchdog va al scheduler que `New`
construye, y la lista de activación al loader). Pero `New` **no devuelve error** (su
firma es sagrada, §17). Decisión: un `nu.toml` mal formado se guarda en
`loader.configErr` y lo devuelve `Boot` (cuya firma sí lo permite), **antes** de
tocar plugin alguno. Así el error de config no deja el arranque a medias y llega a
`main`/tests con el camino que ya existía para los errores de grafo.

### `slice_budget_ms` con `*int` y precedencia de la Option

`slice_budget_ms` es `*int` (no `int`) para distinguir "no especificado" (nil → rige
el default 100 ms o `WithSliceBudget`) de "especificado como 0" (0 → desactiva el
watchdog explícitamente, semántica de S09). Precedencia: **Option explícita
`WithSliceBudget` > nu.toml > default**. Se añadió `config.sliceBudgetSet` (lo pone
`WithSliceBudget`) para que un test que fija su budget no lo pise la config de disco.
`plugins.dirs` simplemente se **suma** a las rutas de `WithPluginDir`.

### Infraestructura `go:embed` y materialización a disco

`embed.go` embebe el árbol `internal/runtime/embedded/` con `//go:embed embedded`
(el directorio DEBE existir para que `embed` compile; por eso la STUB). El loader de
S11 carga plugins de DIRECTORIOS en disco (lee `plugin.toml` con `os.ReadFile`, corre
`init.lua` con `L.LoadFile`, añade `lua/` a las rutas de require). Decisión: para que
una embebida se cargue **exactamente igual** que un plugin de usuario (§14), se
EXTRAE su árbol del `embed.FS` a `<data_dir>/embedded/<name>` (`extractEmbedded`,
idempotente: sobrescribe, así un binario nuevo gana sobre lo extraído antes) y se
reusa el loader de S11. La alternativa —enseñar al loader a leer de un `fs.FS`—
duplicaría el descubrimiento por ganancia nula (el árbol es diminuto). Sin red
(ADR-010): todo sale del binario.

### La extensión STUB `example`

Las extensiones oficiales reales (agente, chat, providers, MCP, toolkit) son la
Fase 8 y aún no existen, pero el mecanismo de embebido + gating se prueba ya en S12.
Para ello el árbol embebido contiene una sola extensión STUB, `embedded/example/`
(`plugin.toml` + `init.lua` que deja la huella `_example_embedded_cargada=true`).
Existe SOLO para los tests del gating; cuando lleguen las oficiales reales se añaden
bajo `embedded/` sin tocar el mecanismo.

### Gating ADR-010 en `loader.discover`

Tras descubrir los plugins de disco (S11, sin cambios), por cada nombre de
`plugins.enabled`:
- si ya está como plugin de disco → el **dir de usuario SUSTITUYE** a la embebida del
  mismo nombre (§14): no se materializa la embebida, gana el de usuario
  (`source="user"`), no coexisten;
- si es una embebida del catálogo → se extrae y carga con `source="builtin"`;
- si no es ni una cosa ni otra → `EINVAL` **accionable** que nombra la extensión y la
  **línea `plugins.enabled` de `nu.toml`** que lo arregla (§14).

Decisión de alcance: los plugins de disco (`WithPluginDir`/`plugins.dirs`) siguen
cargándose como en S11, sin gating. ADR-010 habla de las **extensiones oficiales
embebidas** inactivas por defecto; los plugins explícitos del usuario, por
definición ya elegidos, se cargan. Esto además evita regresionar los tests de S11
(que arrancan sin `nu.toml`). Los casos de prueba de S12 (gating de embebidas,
sustitución por nombre, errores accionables) quedan todos cubiertos.

### Sin superficie Lua nueva

S12 es config/loader interno. `nu.plugin.list()` ya reflejaba `source`/`enabled`
desde S11; una embebida activada sale `{source="builtin", enabled=true}`. No se tocó
`api.md` (§14/ADR-010 bastaron); APILevel sigue en 1. Sin hallazgos.

### Frontera explícita con S33 (G21)

La **pantalla de runtime desnudo** (render TTY del catálogo de embebidas +
activar/salir, sin red) es UI: NO se hizo en S12. Es S30/S33. S12 dejó listos el
catálogo (`embeddedNames`) y la activación por `nu.toml`, que es lo que esa pantalla
consumirá.

## S13 — `nu.plugin.reload` (best-effort, G2) (api.md §14)

### Registro de handles por dueño: general, no parche events+timers (decisión clave)

`api.md` §14 dice que `reload` "suelta todos los handles del plugin (el core los
etiqueta por dueño vía `plugin.current()`)". La realización podía ser un agregado
ad-hoc (recorrer `eventBus.subs` filtrando por dueño + recorrer `scheduler.timers`
filtrando por dueño). Se rechazó: el conjunto de handles persistentes crecerá
(S15 watchers, S16 procs, S29+ input/regiones de UI) y un reload que enumere casos
especiales se pudriría. Decisión: **un único registro** `scheduler.ownerHandles`
(`map[ownerName][]ownedHandle`) e interfaz `ownedHandle{ release(); owner() }`.
Cada primitiva que entrega un handle persistente lo etiqueta con `currentOwner()`
(S11) al crear y llama `track`; al soltarlo a mano (`Sub:cancel`/`Timer:stop`)
llama `untrack`. `reload` itera la lista del dueño y llama `release()` sin conocer
los tipos. Añadir una primitiva nueva = implementar `ownedHandle` + `track`/
`untrack`; reload la recoge gratis. Consistente con "el core no sabe de producto"
(filosofía §1) y con la API sagrada (no se añade firma: la superficie nueva es
solo `nu.plugin.reload`, ya en §14).

### `untrack` en el camino manual, no en `stopTimer`/release (sin doble limpieza)

`luaTimer.release()` llama `stopTimer` (corta la goroutine). Pero el desregistro
del `ownerHandles` NO va en `release()` ni en `stopTimer`: va en `timerStop`/
`subCancel` —el camino **manual**—. Razón: `releaseOwnerHandles` (la vía de reload)
ya borra la entrada del dueño del mapa antes de iterar; si `release()` también
tocara el registro, sería doble limpieza (y, peor, mutar el mapa a media
iteración). Así el registro tiene un solo dueño de la mutación por camino: reload
borra en bloque; cancel/stop a mano quitan uno. Ambos idempotentes (un handle que
ya no está no da error). Sin fuga y sin carrera (todo bajo el token).

### Fix: la auto-cancelación de un `once` también desregistra (sin fuga)

Una revisión encontró un camino de desregistro que faltaba: cuando un
`nu.events.once` se **dispara**, `dispatch` (events.go) lo auto-cancela
(`sub.live = false`) y `purge` lo saca de `eventBus.subs`, pero NO pasaba por el
camino manual (`subCancel`), así que el handle muerto quedaba para siempre en
`ownerHandles[owner]`. Para un dueño de vida larga (p. ej. "user") que use `once`
repetidamente, el mapa crecía sin cota —fuga que viola el invariante 🔒 de S13
("sin fuga en el registro")—. Arreglo mínimo: tras marcar el `once` muerto en
`dispatch`, llamar `s.untrack(sub)` (corre bajo el token, en el despacho del
estado principal; `untrack` es idempotente, así que un `reload` que ya vaciara la
lista no se ve afectado). Es la misma desregistración que ya hacía `subCancel` en
el camino manual, ahora también en la auto-cancelación. Cubierto por
`TestReloadOnceAutoCancelSinFuga` y `TestReloadOnceDisparadoAntesDeReload`.

### Caché de require: enumerar el `lua/` del plugin, no adivinar por package.loaded

`package.path` es compartido por TODOS los plugins (S11), así que un módulo
`foo` en `package.loaded` podría venir del `lua/` de cualquiera. Para vaciar SOLO
la caché del plugin que se recarga, `clearRequireCache` **enumera los ficheros
`.lua` bajo `<dir>/lua/` de ESE plugin**, los traduce a nombres de módulo
(`foo.lua`→`foo`, `foo/init.lua`→`foo`, `bar/baz.lua`→`bar.baz`, siguiendo los
patrones de `setupRequirePaths`) y los pone a `nil` en `package.loaded`. No se
purgan módulos de otros plugins aunque el nombre coincida —el reload es del
plugin, no del espacio global de módulos—. Es best-effort (G2): si dos plugins
exportan un módulo con el mismo nombre, el que gane `package.path` es asunto del
loader, no del reload.

### `reload` es ⏸ aunque hoy todo es síncrono bajo el token

§14 marca `reload` como ⏸. Hoy todos sus pasos son trabajo del estado principal
bajo el token (emit síncrono, soltar handles, re-correr el init con `L.LoadFile`),
sin IO de fondo. Se respeta el marcador igualmente: (a) reserva que leer el init
pueda volverse ⏸ real en el futuro sin cambiar la firma; (b) homogeneidad —una
herramienta de desarrollo se invoca desde una task como el resto de async—. La
detección es la de §1.3 (`L == host` → `EINVAL`). El `init.lua` del usuario
(dueño "user") no es recargable por esta vía: re-correrlo sería re-arrancar, fuera
del alcance de G2 (que es "recargar un plugin").

## S14 — `nu.fs` (api.md §5)

S14 es 🔒. La superficie de §5 se implementó **sin tocar `api.md`** (no hubo
hallazgo): el puente `suspend` de S04 (ADR-011) bastó para todas las primitivas.
Las decisiones de implementación —ninguna amplía la API, todas concretan
semánticas que §5 deja a criterio del kernel— quedan aquí.

### El patrón ⏸ de IO sobre `suspend` (la plantilla de S15/S16 y la Fase 4)

Toda primitiva ⏸ de `fs` tiene la misma forma:

```
vals := rt.sched.suspend(L, func() deliverFn {
    // GOROUTINE DE FONDO: IO bloqueante en Go, fuera del token, JAMÁS toca Lua.
    res, err := os.AlgoBloqueante(...)
    return func(L *lua.LState) []lua.LValue {
        // YA con el token recuperado: aquí SÍ es seguro tocar Lua.
        if err != nil { mapFsError(L, err); return nil }
        return []lua.LValue{ /* valores Go → LValue */ }
    }
})
return pushAll(L, vals)
```

La regla que blinda el invariante 🔒 "cero data races" de S04: la goroutine de
fondo captura **solo datos Go** (un `path` string, los bytes leídos, el error
crudo) y **no construye ni toca ningún `LValue`**; el error del SO se guarda tal
cual y se traduce a la tabla §1.4 **dentro de la `deliverFn`**, que corre con el
token recuperado —porque `raiseError`/`L.NewTable` tocan Lua—. Mientras la
goroutine de fondo trabaja, la task está bloqueada sin el token, así que el loop
no se congela (otras tasks progresan). **S15 (`fs.watch`), S16 (`nu.proc`) y toda
la red (Fase 4) reusan esta plantilla literalmente**; por eso se documenta como
patrón y no como detalle de `fs`.

Guardia común `requireTask(L, nombre)`: las ⏸ exigen estar en una task (`L != host`,
como `cleanup`/`await`/`reload`); fuera → `EINVAL` accionable. `cwd` es la **única
excepción**: no es ⏸ (consulta pura), así que NO lleva guardia y funciona también
en el chunk de `-e`.

### Mapeo de errores del SO → códigos §1.4 (`mapFsError`)

Un único punto traduce el errno: `errors.Is(err, os.ErrNotExist)` → `ENOENT`,
`os.ErrExist` → `EEXIST`, `os.ErrPermission` → `EACCES`, cualquier otro → `EIO`.
Se usa `errors.Is` (no comparación directa) porque la stdlib envuelve los errnos
en `*os.PathError`; `errors.Is` los desenvuelve. `EINVAL` lo emiten los guardias
de uso (fuera de task), no `mapFsError`. El mensaje conserva el texto del error de
Go (la ruta incluida) como pista accionable; nunca se traga el error.

### Escritura atómica: temporal en el MISMO dir + rename

`write` normal escribe a `.nu-fs-*.tmp` **en el directorio destino** (no en `/tmp`)
y hace `os.Rename`. El temporal va al mismo dir para que el rename sea
**same-filesystem** y por tanto atómico —un rename entre sistemas de ficheros
distintos no es atómico (y `os.Rename` ni funciona)—. Garantía: un lector
concurrente ve el contenido viejo o el nuevo **entero**, jamás un fichero a medias.
Un `defer` borra el temporal si se retorna por error antes del rename (no deja
residuo, blindado por test); tras un rename con éxito el temporal ya no existe con
ese nombre, así que el `Remove` diferido es un no-op. Se hace `Chmod` 0644 al
temporal porque `os.CreateTemp` lo crea 0600 y un `write` debe producir un fichero
con permisos normales.

### G17 — `write{exclusive=true}` es `O_EXCL`, sin temporal+rename

La rama exclusiva NO usa temporal+rename: el rename **sobreescribiría** un fichero
existente, rompiendo la exclusión. Se usa `O_WRONLY|O_CREATE|O_EXCL`, que es la
primitiva del SO que crea **solo si no existe** en una operación indivisible y
falla con `os.ErrExist` (→ `EEXIST`) si ya existe. Es la pieza de los lockfiles de
sesiones (sesiones.md §6): la creación del lock debe ser atómica y fallar si otro
proceso ya lo tiene. `append` usa `O_APPEND` (no es atómico como `write` —un append
es incremental por naturaleza, para logs/JSONL—; el `O_APPEND` del SO garantiza que
cada escritura va al final).

### `stat` de inexistente → `nil`, no lanza (la asimetría con `read`/`list`)

`stat` es la consulta "¿existe y qué es?", no una lectura que falla: un fichero
inexistente devuelve **`nil` sin lanzar** (§5). Cualquier OTRO error (permiso sobre
un componente del path, IO) sí se lanza. Contrasta deliberadamente con `read` y
`list`, que sobre un inexistente **sí** lanzan `ENOENT` —leer/listar lo que no
existe es un fallo, no una respuesta válida—. `mtime_ms` se da en milisegundos
(`ModTime().UnixMilli()`, coherente con §1.5: los tiempos del core son en ms);
`mode` son los bits de permiso Unix (`Mode().Perm()`).

### `mkdir` crea padres (`MkdirAll`)

`mkdir` usa `os.MkdirAll`: crea los **padres que falten** y es **idempotente** si
el directorio ya existe. Es el comportamiento esperado de una herramienta de
terminal (`mkdir -p`): nadie quiere encadenar mkdirs para crear `a/b/c` ni que
falle porque el directorio ya estaba. Si el path existe pero es un **fichero**,
`MkdirAll` falla (no se sobreescribe un fichero por un directorio). La alternativa
(`os.Mkdir`, un solo nivel, falla si ya existe) se descartó por ergonomía: §5 no
exige un nivel, y un plugin que quiera crear una jerarquía no debería tener que
recorrerla a mano.

### `remove`: recursive obligatorio para dir no vacío, inexistente = no-op

Borrar un fichero o un directorio **vacío** funciona sin más. Un directorio **no
vacío** exige `opts.recursive=true` —sin él, `os.Remove` falla y se rinde como
`EIO`—: es la salvaguarda contra un `rm -rf` accidental; borrar un árbol entero
debe ser explícito. Con `recursive=true` se usa `os.RemoveAll`. **Inexistente es
no-op** (no lanza `ENOENT`): borrar lo que ya no está deja el sistema en el estado
deseado (el recurso no existe), que es justo lo que pedía la llamada —semántica
idempotente, coherente con `mkdir`—. `RemoveAll` ya es no-op sobre inexistente; en
la rama no recursiva se traga el `ErrNotExist` explícitamente.

### `copy` solo ficheros, en streaming

`copy` usa `io.Copy` (streaming, sin cargar un fichero grande entero en RAM) y
cubre **solo ficheros**: copiar un directorio recursivamente es trabajo de más alto
nivel (Lua sobre `list`+`copy`), no una primitiva del core —el core da el ladrillo,
la composición es del autor de extensiones—. Abre el origen primero para que su
inexistencia/permiso sea el error que el usuario espera ver, y entonces crea el
destino (`O_TRUNC`: sobreescribe).

### `tmpdir` propio de la sesión, perezoso y reutilizado; `cwd` inmutable

`tmpdir` crea **un** directorio temporal por sesión (`os.MkdirTemp` bajo
`os.TempDir()`), **perezosamente** la primera vez y **reutilizado** después
(cacheado en `rt.fs.tmpdir`). La creación corre en la goroutine de fondo (es IO),
así que el campo lo protege un candado en `fsState` —dos `tmpdir` concurrentes no
deben crear dos directorios ni correr una carrera sobre el campo, y el candado no
depende del token (la goroutine de fondo no lo tiene)—. `Runtime.Close` lo borra
recursivamente (`closeTmpdir`): el scratch no sobrevive al proceso. `cwd` es la
única función NO ⏸ de `fs`: una consulta pura (`os.Getwd`), [W], **inmutable**
durante la sesión —no hay `chdir`, porque cambiar el cwd del proceso sería un
efecto global que rompería el aislamiento por tarea (ADR-008); un subproceso que
quiera otro dir lo recibe por `opts.cwd` (§6), sin tocar el cwd del proceso—.

### No se usa el `io`/`os` de Lua

Todo el IO es Go puro (`os`/`io` de la stdlib de Go). El baseline del sandbox (S01,
§1.2) dejó fuera `io` y recortó `os` en Lua a propósito; `nu.fs` es la superficie
**controlada** de IO que los reemplaza, con errores estructurados, ⏸ sobre el loop
y mapeo de códigos. Un plugin nunca toca el sistema de ficheros por la puerta de
atrás del `os` de Lua.

## S15 — `nu.fs.watch` (api.md §5, §16)

### `watch` NO es ⏸ y es solo estado principal (§16)

A diferencia del resto de `nu.fs` (todo ⏸), `watch` **no suspende**: arma el
observador y devuelve el `Watcher` en el acto. Y es **solo estado principal**
(§16): el handler es **síncrono** (como `every`/`on`), corre en el loop del estado
principal; el bus de entrega (token + thread efímero) vive ahí. Por eso `watch` no
es "esperar un resultado" sino "registrar un observador que dispara luego", que es
justo lo que NO encaja en ⏸.

**Corrección (retirado el guard host-only):** "solo estado principal" (§16) significa
**"no en workers"** —donde `fs.watch` ni siquiera se registra (S34)—, **no** "no en
tasks". Las tasks corren en el event loop del estado principal y comparten el `nu`
global, así que `watch` es invocable indistintamente desde el chunk, un handler
síncrono, el `init.lua` **o desde dentro de una task**, exactamente igual que sus
hermanos `every`/`on` (que tampoco distinguen host de task): registra el `Watcher`
síncronamente y devuelve sin suspender. Se eliminó el guard `if L != rt.L { EINVAL }`
de `fsWatch` —era una desviación de §16— y el test cómplice `TestWatchOutsideMainState`
se reescribió como `TestWatchFromTaskWorks` (verifica que un `watch` arrancado dentro
de una task funciona y entrega al menos un lote tras un cambio de fichero). El bloqueo
en workers ya lo garantiza que `fs.watch` no se registre en su LState (S34), sin
necesidad de guard alguno.

### El debounce + batching es lógica NUESTRA, no de fsnotify (G7)

fsnotify reenvía cada evento del SO uno a uno. El **coalescing en lotes** lo
hacemos nosotros: la goroutine de fondo acumula los eventos en un buffer y arma (o
re-arma) un `time.Timer` de `debounce_ms`; cuando pasa ese tiempo **sin nuevos
eventos**, vuelca TODO el buffer como **un solo** `fn(events[])`. El debounce es
**trailing y coalescente** (cada evento reinicia el reloj), así que una ráfaga
continua —un `git checkout` que toca miles de ficheros— se sigue agrupando y sale
como UN lote, no como N llamadas (criterio de hecho de S15, G7). `debounce_ms`
default 50 (§5); negativo → `EINVAL`. El reset del timer usa el patrón estándar
(`Stop` + drenar `C` si ya disparó) para no dejar un disparo viejo en el canal.

### Filtrado gitignore (G7): al añadir Y al filtrar eventos

`gitignore = true` (default §5) parsea el `.gitignore` de la raíz observada
(`github.com/sabhiram/go-gitignore`, puro-Go: `CompileIgnoreFile` + `MatchesPath`).
El filtrado ocurre en **dos sitios**: (1) al **añadir** subdirectorios en el modo
recursivo, se saltan los ignorados (no se VIGILA `node_modules/`: gastaría
descriptores y daría ruido); (2) al **clasificar** cada evento, un path ignorado se
descarta antes de entrar al buffer —ni llega al handler ni cuenta para el debounce—.
Un `.gitignore` ausente no es error (no se ignora nada por esa vía). El `.git/`
interno se ignora **siempre** (ruido universal de un repo), comprobando cualquier
componente `.git` en la ruta. Decisión de librería: go-gitignore es simple,
puro-Go y correcto para el criterio de hecho (basename, glob `*.log`, dir `build/`);
parsear `.gitignore` a mano sería reinventarlo peor.

### Alcance de `recursive`

fsnotify NO recursa: vigila directorios concretos. Con `recursive = true` se
**camina el subárbol** al arrancar (`filepath.WalkDir`) añadiendo cada subdirectorio
no ignorado (`SkipDir` sobre los ignorados, para no descender en `node_modules/`); y
un directorio **creado al vuelo** se añade al watcher al ver su evento `create` (si
es dir y no está ignorado), de modo que los cambios bajo él también se reporten. El
alcance documentado: la recursión se **reconstruye observando creaciones de
directorio**; un fichero suelto (`path` no es dir) se vigila a través de su
directorio padre, filtrando en `classify` los eventos que no le conciernen. Errores
al caminar una entrada concreta son best-effort (se salta, no rompe el watch).

### Entrega bajo el token; quiescencia como `every`; integración con el registro de handles

La goroutine de fondo **jamás toca Lua**: filtra y acumula datos Go; para entregar
el lote llama a `deliverBatch`, que **toma el token** (como `runSyncHandler` de
timers.go) y corre el handler en un thread efímero del estado principal bajo `pcall`
por frontera (ADR-008) —cero data races: los paths cruzan como `string` copiadas y
el handler se invoca con el token tomado—. Un `Watcher` activo **no** cuenta para la
quiescencia (no toca `pending`), igual que un `every`: un watcher nunca termina y
colgaría `nu -e`. `Watcher` implementa `ownedHandle` (handles.go, S13): `watch` lo
etiqueta con `currentOwner()` y lo `track`-a; `Watcher:stop()` lo `untrack`-a (sin
fuga en el registro) y `nu.plugin.reload` lo suelta vía `release()` —"reload no deja
handlers huérfanos" (G2)—. `stop` (y `Runtime.Close` vía `stopAllWatchers`) corta la
goroutine (`stopCh`, idempotente con `stopOnce`) y cierra el watcher del SO (`fsw.
Close`, libera descriptores), sin fuga de goroutines. `deliverBatch` atiende a
`stopCh` mientras espera el token: tras `stop`, ningún lote más (contrato de `stop`).

### Deps añadidas (puras-Go, `CGO_ENABLED=0` intacto)

`github.com/fsnotify/fsnotify` (filewatching pura-Go; su único indirecto es
`golang.org/x/sys`, también puro-Go) y `github.com/sabhiram/go-gitignore` (parseo de
`.gitignore`). Ninguna usa cgo: el binario estático (ADR-001) sigue compilando con
`CGO_ENABLED=0`.

## S16 — `nu.proc` (api.md §6)

### La causa del cuelgue del intento previo, y su arreglo (lo central de esta sesión)

El intento previo de S16 escribió `proc.go`/`proc_test.go` correctos pero **se
colgó corriendo los tests** y nunca commiteó. La causa NO estaba en `nu.proc` sino
en una **grieta del desenrollado de cancelación de S08** que el idioma canónico de
§6 (`spawn` + `nu.task.cleanup(function() p:kill() end)`) fue el primero en
destapar.

El mecanismo: un `cleanup` casi siempre **captura un local de la task por upvalue**
—aquí, `proc`—. Mientras la task corre, ese upvalue está **abierto**: apunta a un
slot del registro del thread `co` de la task, no a una copia. En un retorno normal,
gopher-lua **cierra** los upvalues (copia el valor dentro del `Upvalue`) al salir
del scope. Pero nuestro aborto de cancelación (S07/S08) es un **pánico Go**
(`abortSignal`) que desenrolla la pila Go **sin** ejecutar ese cierre de Lua, y el
`PCall` que recupera el pánico en `runTask` **resetea el registro de `co`** (pone
esos slots a `nil`). Resultado: cuando `runCleanups` ejecuta luego `p:kill()`, su
upvalue lee un slot ya `nil` → el `kill` opera sobre `nil` y, atrapado por el
`pcall` por frontera de cada cleanup (ADR-008), se traga al log sin matar nada. El
subproceso (`sleep 30`) queda **vivo**, y el test que espera su muerte se cuelga
(en el intento previo, sin `-timeout`, un cuelgue duro; con la suite reescrita
determinista, un fallo de `waitDead` a los 5 s).

**Arreglo (scheduler.go, `abort`):** **antes** de lanzar el `panic(abortSignal)`,
cerramos los upvalues abiertos de `co` con `closeOpenUpvalues(t.co)`. gopher-lua no
expone `closeAllUpvalues` directamente, pero su `LState.Error(lv, level)` con un
valor **no-string** ejecuta `closeAllUpvalues()` antes de panicar (verificado en su
fuente, `_state.go`): aprovechamos ese efecto pasando una tabla centinela y
envolviendo la llamada en un `recover()` que se traga su pánico —ese pánico es solo
el **vehículo** del cierre; el aborto real lo lleva el `panic(abortSignal)`
posterior—. Así los valores capturados sobreviven al reseteo del registro y los
`cleanup` los ven intactos. `runCleanups` ya corría sobre threads efímeros de
`host` (no sobre `co`), de modo que los upvalues ya cerrados son justo lo que esos
cleanups necesitan.

**Por qué NO es un hallazgo `G##`:** no cambia ninguna firma ni semántica de
`api.md` —el contrato de §3/§1.3 siempre dijo que un `cleanup` corre al cancelar y
que el idioma es `cleanup(function() proc:kill() end)`; lo que estaba roto era un
**invariante interno** del desenrollado de S08 (un cleanup debe ver los valores que
capturó). Es corrección de implementación, no de espec; por eso se arregla en el
código sin pasar por `problemas.md`. Verificado quirúrgicamente: deshabilitar
`closeOpenUpvalues` hace fallar **solo** `TestSpawnKilledByCleanupOnCancel`, y
re-habilitarlo deja **toda** la suite de S08 (cancel/cleanup/watchdog) verde —el
cierre de upvalues no altera ninguna otra semántica de cancelación—.

### Sin shell implícita (§6): decisión de seguridad estructural

`argv` es un **array**: `exec.Command(argv[0], argv[1:]...)` pasa los argumentos al
SO sin interpretación. Nadie invoca `/bin/sh`, así que `run(["echo","$HOME"])`
imprime el literal `$HOME`. Quien quiera shell la pone explícita
(`["sh","-c","..."]`). La inyección por shell no existe si no hay shell.

### Modelo de vida del proceso (la lógica 🔒, §6)

La vía **principal** es matarlo por `nu.task.cleanup` en quien lo crea: al terminar
la task —éxito, error o cancelación (S08)— el proceso muere con ella. Dos **redes
de seguridad**, no la vía principal: (1) el **finalizer del GC**
(`runtime.SetFinalizer`) mata un `Proc` que se quedó sin referencias en Lua —**no
determinista**, no se confía en ello—; (2) `Runtime.Close`→`stopAllProcs` mata
todos los vivos al cerrar la sesión (scheduler `procs`/`trackProc`, gemelo de
`watchers`/`timers`). Como `every`/`watch`, **un `Proc` vivo no cuenta para la
quiescencia**: esperar a que un subproceso muera para que `nu -e` retorne lo
colgaría. `*luaProc` implementa `ownedHandle` (S13): `reload` mata los procesos del
plugin que se recarga.

### Reparto de candados: `kill` con candado propio, nunca durante IO

El IO de un `Proc` (write/read/wait) **bloquea** en goroutines de fondo (sin
token); `kill` debe poder **interrumpir** ese IO —el patrón de vida es "el cleanup
mata el proceso colgado para que su `read`/`wait` pendiente se desbloquee"—. Si
`kill` y el IO compartieran candado, matar a un proceso del que se está leyendo se
**deadlockearía** (el lock lo tendría el read bloqueado; kill esperaría un lock que
solo se suelta cuando el proceso muera, que es lo que kill intenta). Por eso `kill`
usa `killMu` **propio**, jamás tomado durante una operación bloqueante: cerrar/
señalar el proceso es lo que **desbloquea** el read/wait. `wait` se memoiza con
`sync.Once` + un `chan` (no un `Mutex`) precisamente para no sostener candado
durante la espera bloqueante.

### Pipes manuales (`os.Pipe`), no `cmd.StdoutPipe`

`cmd.StdoutPipe`/`StderrPipe` cierran el extremo de **lectura** en cuanto
`cmd.Wait` ve salir al proceso (os/exec lo documenta), lo que perdería datos si
reapeamos en cuanto el proceso muere. Con pipes propios el extremo de lectura es
**nuestro**: `Wait` no lo toca; lo cerramos al derribar el `Proc`. Así reaping
(`go p.wait()`, que recoge el zombi sin el cual `alive` lo reportaría vivo para
siempre) y streaming quedan **desacoplados**. Para stdin sí vale `StdinPipe` (es de
escritura; `close_stdin` lo cierra a mano, señalando EOF).

### `alive` (G17): existencia, no identidad

`nu.proc.alive(pid)` usa la "señal 0" (`kill(pid, 0)`): sin error o `EPERM` (existe
pero de otro usuario) → vivo; `ESRCH` → muerto; `pid <= 0` → no vivo. Informa de
**existencia, no de identidad**: un pid reciclado por el SO da `true` aunque sea
otro proceso. Es deliberado —para detectar locks de sesión huérfanos (sesiones.md
§6) basta saber si "alguien" tiene ese pid; la identidad la da el contenido del
lock (hostname, §7), no esta llamada—. No es ⏸ (consulta inmediata).

### `run`: el código de salida es dato, no error

Un `code != 0` **no lanza** (un `grep` sin coincidencias sale con 1 y eso es
información, como el `status` de `nu.http`). Lo que sí lanza: arranque fallido
(`ENOENT`/`EACCES`/`EIO`) o `timeout_ms` excedido (mata con SIGKILL, **drena** el
`Wait` del proceso muerto para no fugar su goroutine/pipes, y lanza `ETIMEOUT`).
`env` presente (aunque vacío) **reemplaza** el entorno heredado; ausente lo hereda.

## S17 — `nu.sys` (api.md §7)

Entorno y reloj. Wrappers finos sobre la stdlib (`platform`/`now_ms`/`mono_ms`/
`hostname`); la única lógica propia es el **overlay de `setenv`** y su precedencia
al lanzar subprocesos. Ninguna función ⏸ (son consultas/registros inmediatos);
todas [W] (§16, hoy en el estado principal: los workers son S34). Sin hallazgos:
§7 y la `procOpts` que dejó S16 bastaron; APILevel sigue en 1 (§7 ya estaba).

**`setenv` NO muta el entorno del proceso `nu` actual (decisión central, §7).**
Nada de `os.Setenv`: mutar el entorno global del proceso es un efecto compartido
que (a) rompería el aislamiento por tarea (ADR-008) —se vería desde TODO el
código, no solo desde quien lo pidió— y (b) contradiría el contrato ("afecta solo
a subprocesos futuros"). En su lugar, `setenv` escribe en un **overlay** del
Runtime (`sysState.envOver map[string]string`) que `nu.proc` aplica al construir
el entorno del hijo. El criterio de hecho ("`setenv` se ve en un subproceso
lanzado después, no en el actual") se cumple por construcción: el `nu` actual
nunca cambia su entorno; lo único que ve la variable es el hijo.

**Candado, no token, para el overlay.** `setenv` escribe el mapa en el estado
principal bajo el token, pero lo **leen las goroutines de fondo de `nu.proc`**
(que montan el entorno del hijo SIN el token, fuera del puente ⏸). Por eso el
overlay lleva su propio `sync.Mutex` —es lo que evita la data race que `-race`
cazaría—, no el token. Para no compartir el mapa vivo con esas goroutines,
`envOverlay()` devuelve una **copia** (coste despreciable: pocas entradas).

**Foto del overlay tomada en la entrada de `run`/`spawn`.** Ambos hacen
`opts.envOver = rt.sys.envOverlay()` justo tras `parseProcArgs`, en el estado
principal bajo el token. Así se fija de forma determinista qué `setenv` ve cada
subproceso: los que ocurrieron ANTES de la llamada (no los de después). Como la
llamada Lua happens-before la goroutine de fondo, cualquier `setenv` previo es
visible.

**Precedencia del entorno del hijo (la integración S16↔S17, `mergedEnv`).** De
menor a mayor: **entorno heredado del SO < overlay de `setenv` < `opts.env`
explícito de la llamada**. Razonamiento:

- El overlay **pisa lo heredado**: esa es la razón de ser de `setenv` (cambiar lo
  que el hijo ve respecto al entorno del proceso).
- `opts.env` explícito es **control total por llamada** (§6, ya decidido en S16):
  lo más local manda. Quien pasa `env` en ESA invocación decide esas claves por
  encima del overlay —p. ej. para AISLAR un subproceso de un `setenv` previo—. Y,
  coherente con S16, `opts.env` **reemplaza** el entorno heredado (parte de
  `opts.env`, no de `os.Environ`); con `opts.env` presente el overlay **no se
  aplica** (la capa explícita es la ganadora completa).
- Alternativa descartada: layar `opts.env` *encima* de (SO + overlay) en vez de
  reemplazar. Se descartó por coherencia con S16 (`opts.env` ya significaba
  "control total / reemplaza heredado"); cambiarlo habría sido una regresión
  silenciosa de esa semántica.

Detalle de implementación: `opts.env != nil` (aun siendo `[]string{}`) marca
"explícito" —`parseProcArgs` pone `[]string{}` no-nil cuando hay tabla `env`—. Sin
overlay ni `opts.env`, `mergedEnv` devuelve `nil` (el caso común: `exec.Cmd`
hereda `os.Environ()` tal cual, sin coste de copia). `splitEnv` parte "K=V" por
el PRIMER `=` (un valor puede contener `=`). Se mantiene **una sola entrada por
clave** (índice clave→posición) para un entorno limpio y determinista.

**`platform`** devuelve `runtime.GOOS` crudo: para los SO soportados es
"linux"/"darwin"/"windows" (lo que enumera §7); en cualquier otro, el literal de
`GOOS` —más honesto que inventar un valor del enum—. **`now_ms`** es el reloj de
pared (`time.Now().UnixMilli`, puede saltar hacia atrás). **`mono_ms`** es
monotónico desde `monoOrigin` (fijado al cargar el paquete, `time.Since`): origen
arbitrario, solo las diferencias entre lecturas son duración fiable.
**`hostname`** es `os.Hostname`; un fallo del SO (raro) → `EIO` en vez de
inventar un nombre.

**Tests (`sys_test.go`).** El overlay y su precedencia (lógica propia) llevan
test Go: `mergedEnv` table-driven (overlay pisa el SO / añade clave nueva
conservando lo heredado / `opts.env` gana al overlay / `opts.env` reemplaza lo
heredado / `opts.env={}` → entorno vacío) y `splitEnv`. El **criterio de hecho**
va de extremo a extremo por el puente ⏸ real: una task hace
`setenv("NU_TEST_X","42")` + `proc.run(["printenv","NU_TEST_X"])` y un future
publica el desenlace; otra task lo espera y assert-a stdout=="42\n"/code==0
(`printenv` es coreutils y se invoca SIN shell, así que también ejercita la
ausencia de shell de S16). El "no en el actual" se comprueba en Go con
`os.LookupEnv` (sigue vacío tras el snippet). El resto (`platform`/`env`/
`now_ms`/`mono_ms` no-decreciente/`hostname`/uso desde una task) con snippet Lua,
como pide la política para glue sobre la stdlib. `CGO_ENABLED=1 go test -race
-timeout 120s -count=2 ./internal/...` verde.
