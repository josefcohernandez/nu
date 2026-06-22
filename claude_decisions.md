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

## S18 — codecs `nu.json` / `nu.toml` / `nu.yaml` (api.md §12)

Tres pares `encode`/`decode` (`codecs.go`), **ninguno ⏸** (CPU puro: parsean o
serializan un string ya en memoria, no hay IO que esperar) y todos **[W]** (§16;
hoy en el estado principal, los workers son S34). "Lua decide, Go ejecuta"
(ADR-004): el parseo/serialización es Go (stdlib `encoding/json`, BurntSushi/toml
—la misma de S11—, `gopkg.in/yaml.v3`), en particular YAML, "demasiado
traicionero para Lua puro" (§12). **APILevel sigue en 1** (§12 ya estaba en
api.md; se implementa, no se amplía la superficie sagrada).

### El mapeo Lua↔Go (compartido por los tres formatos)

El puente es un valor intermedio Go (`interface{}` con
`map[string]interface{}`/`[]interface{}`/`float64`/`string`/`bool`/`nil`) que las
tres librerías saben serializar (`luaToGo`/`goToLua`):

- **`nil` (Lua) → null.** En `decode`, un null → `nil` PERDERÍA la clave en una
  tabla Lua (`t.k = nil` la borra), así que JSON usa el **sentinel `NULL`** (ver
  abajo). TOML/YAML mapean nil a `nil` Lua (`useNull=false`): null no se da en su
  forma típica de config, y quien necesite el round-trip de null usa JSON.
- **boolean → bool; number → float64.** Lua no distingue int de float; el lado Go
  emite entero si no hay parte fraccionaria. JSON `decode` usa `UseNumber` para no
  degradar enteros grandes a notación científica en el round-trip.
- **string → string, con UTF-8 ESTRICTO (G11):** ver abajo.
- **table → array vs objeto:** una tabla cuyas claves son **exactamente 1..n
  contiguas** (la convención de secuencia de Lua) → **array**; cualquier otra →
  **objeto** (claves a string, vía `luaKeyToString`: número 1.0 → "1"). La
  detección cuenta la longitud de la secuencia y el total de claves; solo es
  array si coinciden y hay al menos una. Claves no escalares (tabla, función) →
  `EINVAL`.

**Tabla vacía → objeto (`{}`)** (la decisión ambigua de §12). Una tabla vacía
podría ser `[]` o `{}`; se elige `{}` porque la inmensa mayoría de las
tablas-config de este proyecto son mapas y una lista vacía es el caso raro.
Documentado aquí y en la cabecera de `codecs.go`. Quien necesite `[]` exacto lo
trata como dato (un array no vacío sí se detecta sin ambigüedad).

### UTF-8 estricto (G11) — la mitad 🔒 de S18

`encoding/json` **reemplaza** los bytes UTF-8 inválidos por U+FFFD en silencio.
El contrato (§12) exige lo contrario: `encode` **lanza `EINVAL`** ante bytes
inválidos —sanear es una decisión visible de quien tiene el contexto (la tool),
nunca del codec—. Se detecta con `utf8.ValidString` en `luaToGo` (valores) **y en
las claves de objeto** (un string-clave inválido rompe el documento igual). Vale
para los tres formatos al codificar. También se rechaza un número no finito
(NaN/Inf), sin representación en JSON/TOML/YAML.

### Sentinel `nu.json.NULL` — la otra mitad 🔒

Un **userdata único** por Runtime (`rt.jsonNull`, creado una vez en
`registerCodecs`), reconocido por **identidad**. `decode` entrega el sentinel en
lugar de `null` (NO `nil`, que al asignarse a una tabla borra la clave: una ida y
vuelta perdería claves con valor null); `encode` lo reconoce y emite `null`. Es
el patrón canónico de "null que sobrevive el round-trip". El test contrasta
explícitamente con `{ a = nil, b = 1 }`, que SÍ pierde la clave `a` — justo lo que
el sentinel evita.

### Detalles de serialización

- **JSON `SetEscapeHTML(false):`** por defecto `encoding/json` escapa `<`/`>`/`&`
  (defensa para incrustar en HTML); en un codec de propósito general eso
  sorprende (un round-trip cambiaría el texto), así que se desactiva —quien
  incruste en HTML escapa él, coherente con que sanear es del consumidor (§12)—.
  Se recorta el `\n` final que `json.Encoder.Encode` añade.
- **`opts.pretty`** → `SetIndent("", "  ")` (dos espacios).
- **TOML raíz:** un documento TOML es un mapa; `encode` exige que la raíz sea un
  objeto (array/escalar → `EINVAL` accionable).
- **Errores de parseo** (`decode` de JSON/TOML/YAML inválido) → `EINVAL` con el
  texto de la librería (BurntSushi y yaml.v3 incluyen línea/columna).

### Deps añadidas

`gopkg.in/yaml.v3` v3.0.1 (puro-Go; `go get` + `go mod tidy`, hubo red). No toca
`CGO_ENABLED=0`. BurntSushi/toml ya estaba (S11); `encoding/json` es stdlib.

### CP-4 — adaptación `search.files` → `fs.list` (cierra Fase 3)

El texto de CP-4 ("una herramienta de verdad, solo con primitivas") menciona
`nu.search.files` para recorrer el repo, pero **esa primitiva es S27 (Fase 5) y
aún no existe**. Se sustituye por un **recorrido recursivo en Lua sobre
`nu.fs.list`** (disponible desde S14): enumerar el directorio + recurrir por los
subdirectorios (saltando `.git`, como haría el filtrado gitignore de
`search.files`). Es la sustitución más fiel —el mismo trabajo (enumerar el árbol)
con la primitiva que SÍ existe en la Fase 3—; `search.files` (recursión +
filtrado en Go) llega en S27/CP-6. El test (`cp4_test.go`) monta un repo git
temporal (un fichero comiteado + uno sin trackear + un subdir), recorre con
`fs.list` recursivo, lee con `fs.read`, lanza `git status --porcelain` con
`proc.run` (`opts.cwd`, sin shell), y emite un resumen con `json.encode` que luego
re-parsea con `json.decode` para validarlo (cierra el círculo del codec). **Sin
red ni UI, solo primitivas del core** → ejercita el corolario de completitud
(filosofía §2): no hizo falta ninguna primitiva nueva, así que **sin hallazgo
G##**. Si `git` no está, el test se salta (lo necesita para `git status`).

### Tests 🔒 (`codecs_test.go`, nombran G11)

UTF-8 estricto G11 (byte 0xff suelto, anidado, y como clave de objeto → `EINVAL`;
ASCII+multibyte+emoji round-trip exacto); sentinel NULL ida y vuelta (clave `a`
presente como `nu.json.NULL` distinta de nil, iterada con `pairs`; `encode` →
`"a":null`; round-trip; contraste con `nil` que pierde la clave); array vs objeto
y tabla vacía → `{}`; `pretty` indenta y es JSON válido; `decode` inválido →
`EINVAL`; `toml.decode` de un `plugin.toml` real (name/version/requires) +
round-trip + raíz no-objeto → `EINVAL`; YAML frontmatter de skill (claves, listas,
strings) + round-trip; codecs desde una task ([W]); no-serializable (función,
NaN, Inf) → `EINVAL`. `CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde. Binario
`nu -e` confirma de extremo a extremo encode/decode de los tres formatos, el
round-trip del sentinel NULL, `pretty` y el UTF-8 estricto (G11 → `EINVAL`).

**Sin hallazgos:** §12 bastó tal cual. **CP-4 verde → Fase 3 cerrada.**

## S19 — `nu.http.request` (api.md §8)

Primera sesión de la **Fase 4 (Red)**. Implementa **solo** `nu.http.request(opts)
-> {status, headers, body}` ⏸ (§8): una petición HTTP **buffereada** sobre el
puente `suspend` de S04 (ADR-011), el mismo patrón que `nu.fs`/`nu.proc` —el IO
(la petición) va en la goroutine de fondo, que **jamás toca Lua**; la respuesta o
el error cruzan a Lua solo en la `deliverFn`, bajo el token recuperado—. `stream`
(S20) y `ws` (S21) se quedan fuera a propósito. APILevel sigue en 1 (§8 ya estaba
en api.md). **Sin hallazgos G##:** §8 bastó tal cual.

### El status es DATO, no error (la semántica clave de §8)

Un 404 o un 500 devuelven `{status=404, ...}` **sin lanzar** —el código de estado
es información que el llamante decide cómo tratar (un adaptador de provider
distingue 429 de 500 para reintentar, ADR-005)—. Solo los fallos de **transporte**
lanzan: conexión rechazada / DNS / reset → `ENET`; expirar `timeout_ms` →
`ETIMEOUT`; `url` ausente/inválida y otros usos malos → `EINVAL`. Esto invierte el
default de muchos clientes HTTP (que lanzan por 4xx/5xx) y es deliberado: el
status pertenece a la lógica de la extensión, no al transporte.

### El modelo del cliente: reutilizable vs por-petición (la decisión de diseño)

**Un `*http.Client` reutilizable para el caso común, uno efímero por-petición para
los casos con TLS/proxy a medida.** El caso común (sin `opts.tls`, sin
`opts.proxy`, sin CA/proxy de `[net]`) reusa un único cliente cacheado en
`httpState` (creado perezosamente, candado para la carrera entre goroutines de
fondo): así se aprovecha el **pool de conexiones keep-alive** entre peticiones,
que es lo que hace eficiente hablar repetidamente con el mismo endpoint (el caso
del agente: muchas llamadas al mismo provider). Una petición que pide una CA
distinta, `insecure` o un proxy propio necesita su propio `tls.Config`/`Transport`,
así que construye un cliente **efímero solo para ella**; no se cachean los
efímeros (son la excepción, y cachearlos por combinación de opciones añadiría
complejidad sin beneficio claro en v1). El plazo NO va por `client.Timeout` sino
por un `context.WithTimeout` por petición: así `ctx.Err()` distingue limpiamente
el timeout (`ETIMEOUT`) del resto de fallos de transporte (`ENET`).

### TLS y proxy (G12)

`opts.tls = {ca_file?, insecure?}`: `ca_file` añade una CA corporativa **a la raíz
del sistema** (parte de `x509.SystemCertPool` y le añade el PEM —"añadir una CA",
no reemplazar la confianza—); `insecure=true` desactiva la verificación (entornos
de prueba, expuesto a sabiendas). `opts.proxy` fija un proxy por petición; sin él,
`http.ProxyFromEnvironment` respeta `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` del
entorno. **Defaults globales en `[net]` de `nu.toml`** (`ca_file`, `proxy`),
sobreescribibles por petición —la precedencia es: opción de la petición > `[net]` >
entorno/sistema—. La config `[net]` se lee en `New` (config_toml.go) y se pasa a
`httpState`; un `nu.toml` mal formado no la aplica (su error se aplaza a `Boot`,
como el resto de la config).

### Headers de respuesta con valores múltiples → unir por ", "

`http.Header` es nombre→[]valor (el modelo del protocolo permite headers
repetidos); el contrato (§8) pide una tabla nombre→valor. **Decisión: unir los
valores repetidos por `", "`** —la forma canónica de combinar headers según RFC
7230 §3.2.2, válida para casi todos—. La excepción notable es `Set-Cookie` (no se
parte/une por comas); un consumidor que necesite cookies crudas no tiene una API
buffereada que se lo dé bien (usará `stream` cuando llegue, S20, o no le sirve este
camino). Es predecible y reversible para el caso común (un solo valor pasa
intacto) y evita exponer arrays donde casi todo el código espera un string.

### Validación de `opts` (a `EINVAL`, antes de suspender)

`opts` no-tabla, `url` ausente/vacía, `timeout_ms` no positivo o de tipo
equivocado, `headers`/`tls` de tipo equivocado → `EINVAL` lanzado en el estado
principal (bajo el token), **antes** de suspender. La validación fina de la URL
(sintaxis, esquema) se delega a `http.NewRequestWithContext` en la goroutine de
fondo; un error suyo se rinde también como `EINVAL` (uso inválido). `timeout_ms`
ausente → un techo por defecto de 30 s (una petición de red sin plazo podría
colgar una task para siempre); un `0` explícito se trata como inválido (el contrato
no lo define como "infinito").

### Tests (`http_test.go`, herméticos con `net/http/httptest`)

Todos contra servidores **locales** (`httptest`), **sin red externa** → no flaky
por DNS ni endpoints remotos: 200 con body + headers de petición/respuesta
correctos; 404 y 500 **no lanzan** (status como dato); POST con body recibido en el
server; **fallo de transporte** (servidor cerrado → puerto cerrado) → `ENET`;
**timeout** (server que duerme >> `timeout_ms`, con un canal `release` que lo
desbloquea al terminar el test, sin goroutines colgadas) → `ETIMEOUT`; `url`
ausente/vacía/`opts` no-tabla → `EINVAL`; `timeout_ms` negativo/no-numérico →
`EINVAL`; `request` fuera de task → `EINVAL` (es ⏸); **TLS G12** contra
`httptest.NewTLSServer`: sin `insecure` falla (CA desconocida → `ENET`), con
`insecure=true` pasa, y con la CA del server como `ca_file` pasa **sin** `insecure`;
headers múltiples unidos por ", "; 5 peticiones concurrentes progresan en paralelo
(red anti-data-race del cliente reutilizable). `CGO_ENABLED=0 go build`/`go
vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test -race -timeout 120s -count=2
./internal/...` verde, sin flaky. Binario `nu -e` confirma de extremo a extremo:
GET → status=200, 404 → no lanza (status=404), puerto cerrado → `ENET`, `url`
vacía → `EINVAL`.

### Lo que reusan S20 (stream) y S21 (ws)

S20 reusará el modelo del cliente (reutilizable vs por-petición), el parseo de
`opts` TLS/proxy (G12) y el mapeo de errores de transporte (`classifyTransportError`,
`httpError` con su código del core ya decidido fuera del token) —cambiará que NO
bufferiza el body: devuelve un `Stream` al recibir cabeceras y expone `chunks()`/
`events()` (parser SSE, 🔒) con backpressure acotado (→ `EIO`)—. S21 (ws) se
construye sobre el mismo puente ⏸ pero con una librería de websockets; comparte el
parseo de `opts` y el mapeo `ENET`/`ETIMEOUT`.

**Sin hallazgos:** §8 bastó. Puntero ▶ avanza a **S20**.

## S20 — `nu.http.stream` + parser SSE (api.md §8, 🔒)

`nu.http.stream` es la respuesta HTTP en **streaming**, la otra cara de
`nu.http.request` (S19, buffereada). Devuelve un `Stream` **al recibir las
cabeceras** (`Stream.status`/`Stream.headers`), **sin leer el body**; el cuerpo se
itera trozo a trozo con `Stream:chunks()` (crudo) o `Stream:events()` (parser SSE
incorporado, la lógica 🔒). `stream.go` (handle + iteradores + apertura) y `sse.go`
(parser). Todo lo que S19 dejó listo se reusa tal cual: el parseo de `opts`
(`parseReqOpts`), el modelo de cliente reutilizable vs por-petición (`clientFor`,
con TLS/proxy de G12) y el mapeo de errores de transporte
(`classifyTransportError`/`httpError`, que ya deciden el código del core fuera del
token). Lo único que cambia es el consumo del body.

### El puente ⏸ y la goroutine de fondo (sin novedad de modelo)

`stream` suspende hasta las cabeceras; cada `next` de `chunks()`/`events()`
suspende hasta el siguiente trozo/evento. Una **sola** goroutine de fondo
(`readLoop`) lee el body a trozos y **jamás toca Lua**: empuja los bytes a una cola
interna y el consumidor los saca por el puente ⏸ (la `deliverFn` construye el
string/evento con el token recuperado). Es el mismo invariante de S04/S14/S16.

### El parser SSE incremental (la lógica 🔒)

La exigencia es que un evento puede llegar **partido entre varios trozos de red**
(TCP no respeta límites de evento ni de línea). El parser (`sseParser`) no asume
nada sobre los cortes: acumula bytes en un buffer, extrae solo las **líneas
completas** y guarda el resto para el próximo trozo. El caso delicado es un `\r` al
**final** del buffer: podría ser un `\r\n` partido entre chunks, así que se trata
como línea incompleta hasta saber qué le sigue (en EOF, `flush` lo cierra). Un
evento se **despacha en la línea en blanco**; un último evento sin su línea en
blanco final se despacha en EOF. Soporta los tres terminadores (`\n`/`\r\n`/`\r`),
`data:` múltiple **concatenado con `\n`** (sin `\n` final), `event:`/`id:`,
`retry:` y comentarios (`:` inicial) ignorados, y el espacio opcional tras los dos
puntos (se quita **uno**). `event`/`id` llevan flag `has*` para distinguir
"ausente" de "presente vacío" —no se inventa `event="message"`, que la espec deja
al consumidor—.

### El buffer acotado y el backpressure → `EIO`

El body se lee a una cola interna protegida por mutex+cond (NO el token: el
productor no toca Lua) con cuenta de **bytes pendientes** (`buffered`). Si un trozo
nuevo superaría `maxStreamBuffer` (8 MiB) porque Lua consume más lento de lo que el
servidor empuja, el stream **falla con `EIO`** en vez de crecer sin límite —es la
semántica de §8: el buffer tiene tope, desbordarlo es un error, no una espera
infinita ni una fuga—. Se eligió un tope por **bytes** (no por nº de trozos) porque
es lo que acota la memoria de forma predecible, y es determinista (no depende de
timing: con suficiente volumen siempre desborda).

### El idle timeout → `ETIMEOUT` (y por qué `timeout_ms` no cubre el body)

Un SSE puede quedarse **mudo para siempre** sin cerrar la conexión, así que un
plazo total cortaría un stream largo legítimo. Por eso `opts.timeout_ms` cubre
**solo hasta las cabeceras** (un `time.AfterFunc` que cancela el contexto si no
llegan a tiempo y se detiene al recibirlas), y el body lo protege
`opts.idle_timeout_ms?`: un `time.Timer` que se **re-arma con cada trozo** y, al
disparar, cancela el contexto —el `Read` mudo retorna y se rinde `ETIMEOUT`—. Se
distingue una cancelación por idle (`idleFired` → `ETIMEOUT`) de una por `close()`
del usuario (fin normal, no error).

### Close / cleanup / rastreo (la vida del stream)

`Stream:close()` cancela el contexto (desbloquea el `Read`), cierra el body, para
el idle-timer y despierta a los consumidores (que ven `ECLOSED`). Es **idempotente**
(`closeOnce`) y síncrono (no ⏸). El idioma de vida es el de §6:
`nu.task.cleanup(function() st:close() end)` —al cancelar/terminar la task, el
stream se cierra sin fuga de goroutines—. Como red de seguridad, `Runtime.Close`
cierra todos los streams vivos (`stopAllStreams`, rastreo en `scheduler.streams`,
gemelo de `procs`/`watchers`; un stream vivo **no** cuenta para la quiescencia).
**Decisión:** el `Stream` NO es un `ownedHandle` por dueño (como `Proc`): un stream
es de la **task que lo consume** (su vida es la del turno de IO), no del plugin, así
que se ata con `cleanup`, no con el registro de `reload`. Aun así se rastrea para
`Close`.

### `status` y `headers` como campos del userdata

El contrato pide `Stream.status`/`Stream.headers` como **campos** (no métodos) y
`Stream:chunks/events/close` como métodos. Se resuelve con un `__index` función que
devuelve `status`/`headers` directamente y delega el resto en la tabla de métodos.

### Tests 🔒

`sse_test.go` (parser puro, sin red ni token): tabla con data simple/multilínea,
sin espacio tras `:`, event+data+id, comentario ignorado, varios eventos, `\r\n` y
`\r`, evento sin event, data vacío, retry ignorado, id presente, último evento sin
línea en blanco final. **Cada caso se ejecuta con varias particiones del mismo
`raw`** (todo de una, **byte a byte**, de 2/3/7 bytes) → blinda eventos partidos
entre chunks. Más dos casos adversarios: un `\n\n` partido EXACTAMENTE entre trozos
y un `\r\n` partido entre trozos (el `\r` al final de un trozo, el `\n` al inicio
del siguiente: si se tratara el `\r` como terminador se vería una línea en blanco
espuria). `stream_test.go` (e2e con `httptest` + `http.Flusher`, herméticos): `events()`
{event,data,id}, evento emitido en N writes parseado como uno, `chunks()` crudo +
nil al fin, status 404 no lanza, **backpressure → `EIO`** (server vuelca ~12 MiB,
consumidor duerme 300 ms y desborda), **idle-timeout → `ETIMEOUT`** (body mudo >
`idle_timeout_ms`, con canal `release` que lo desbloquea al terminar el test, sin
goroutines colgadas), `close` idempotente, **close por cleanup al cancelar la
task** (mide `NumGoroutine` para descartar fuga), `stream` fuera de task →
`EINVAL`, `idle_timeout_ms` inválido → `EINVAL`. `CGO_ENABLED=0 go
build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test -race -timeout 120s
-count=2 ./internal/...` verde, sin flaky (no regresiona S01–S19). Binario `nu -e`
confirma e2e: status=200 + evento partido en varios writes parseado como uno
(`ping`/`hola mundo`) + evento sin `event` (data="fin").

**Sin hallazgos:** §8 bastó. Puntero ▶ avanza a **S21**.

## S21 — `nu.ws.connect` (api.md §8; cierra Fase 4 — Red, CP-5)

Websockets: `nu.ws.connect(url, opts?) -> Ws` ⏸, `Ws:send(data)` ⏸,
`Ws:recv() -> string?` ⏸ (**`nil` al cerrar**), `Ws:close()` (`ws.go`). Es el
complemento full-duplex de `nu.http.stream` (S20) y la última pieza de la Fase 4.

### La librería WebSocket: `github.com/coder/websocket`

Se eligió `github.com/coder/websocket` (la continuación de `nhooyr.io/websocket`)
frente a `github.com/gorilla/websocket`. Motivos:

- **Puro-Go y sin dependencias transitivas.** `go mod tidy` añade SOLO
  `coder/websocket` (no arrastra nada más), así que `CGO_ENABLED=0` sigue verde
  (ADR-001) y el binario estático no engorda con un árbol de deps.
- **API basada en `context.Context`.** `Dial(ctx, url, opts)`,
  `conn.Read(ctx)`, `conn.Write(ctx, typ, p)`: el contexto es justo lo que el
  puente ⏸ y la cancelación de tasks necesitan —cancelar el contexto desbloquea
  un `Read`/`Write` colgado, que es como `close()` aborta el IO de fondo—.
- **Serializa las escrituras por dentro.** `gorilla/websocket` obliga al
  llamante a un mutex propio para no intercalar escrituras; `coder/websocket` ya
  lo gestiona, lo que encaja con que `send` pueda correr desde la goroutine de
  fondo de su `suspend` sin coordinación extra por nuestra parte.

### El modelo de IO: NO hay goroutine permanente de lectura (a diferencia de S20)

El `Stream` de S20 necesita una goroutine de fondo permanente porque el body de
un SSE **llega aunque nadie lo pida** (hay que leerlo para no bloquear al
servidor y para aplicar backpressure). Un websocket es distinto: es
**petición-respuesta dirigida por el consumidor** —Lua llama `recv()` cuando
quiere el siguiente mensaje—. Así que cada `send`/`recv` hace su `Write`/`Read`
bloqueante **dentro de la goroutine de fondo de SU propio `suspend`** y no hay
ningún productor de fondo corriendo entre llamadas. Es el patrón de
`Proc:read_line`/`Proc:write` (S16), no el del `Stream`. Más simple y sin colas:
el único estado compartido entre la goroutine de fondo y `close()` es el flag
`closed` (bajo `mu`, no el token: el productor jamás toma el token).

### `recv() -> nil al cerrar`: distinguir cierre de error de transporte

El criterio de hecho de S21 es "recv tras cierre da nil". `recv()` devuelve el
mensaje, o **`nil` cuando la conexión se cierra**: ordenadamente (la otra punta
mandó un frame de cierre normal) o porque nosotros llamamos `Ws:close()`. La
distinción "cierre → nil (fin de stream)" vs "fallo real → lanza `ENET`" la hace
`websocket.CloseStatus(err)`: un cierre `StatusNormalClosure` (1000),
`StatusGoingAway` (1001) o `StatusNoStatusRcvd` (1005, la otra punta cortó sin
código) es fin de stream; cualquier otro error de lectura es transporte. Además,
si fuimos nosotros quienes cerramos (flag `closed`), el `Read` abortado por
nuestro `cancel` también es fin de stream, no error.

Detalle de robustez (lo descubrió un test): tras detectar un cierre ordenado,
`recv` marca el handle cerrado (llama a `close()`, idempotente). Sin esto, un
`recv()` posterior reintentaría un `conn.Read` sobre una conexión ya cerrada, que
devuelve un error **distinto** (no clasificable como cierre normal) y se rendiría
como `ENET` en vez de seguir dando `nil`. Con el flag puesto, todo `recv`
posterior corta en seco a `nil`.

### Connect: el `timeout_ms` cubre solo el handshake

Como en el `stream` de S20, el plazo del handshake no debe cortar la vida de la
conexión (un websocket es de larga duración). `dialWs` usa un `context.WithCancel`
para la conexión (sin plazo, lo cancela `close()`) y, encima, un
`context.WithTimeout(connCtx, timeout)` SOLO para `Dial`, que se desecha
(`dialCancel`) al volver. Un fallo del handshake → `ENET`; su timeout →
`ETIMEOUT`, distinguido por `dialCtx.Err()` vía `classifyTransportError` (reusado
de S19). `send` envía **texto** por defecto (`MessageText`: el provider habla JSON
sobre texto, ADR-005); `SetReadLimit(32 MiB)` acota un mensaje entrante gigante
(el default de la lib, 32 KiB, es poco para un turno grande de un provider).

### Close / cleanup / rastreo (la vida del websocket)

Idéntico al `Stream` de S20: `Ws:close()` es idempotente (`closeOnce`), marca
`closed`, manda el frame de cierre normal (best-effort) y cancela el contexto
(desbloquea cualquier IO colgado). El idioma de vida es
`nu.task.cleanup(function() w:close() end)`; la red de seguridad es
`Runtime.Close` → `stopAllWs` (rastreo en `scheduler.ws`, gemelo de
`scheduler.streams`). Un `Ws` vivo **no** cuenta para la quiescencia (la otra
punta puede no cerrar nunca) y NO es un `ownedHandle` por dueño (su vida es la
del turno de IO, se ata con `cleanup`, no con `reload`).

### Tests (`ws_test.go`, herméticos; CP-5 en `cp5_test.go`)

`nu.ws` NO está en el inventario 🔒 (es un wrapper sobre la lib + el puente ⏸),
pero su lógica propia se blinda igual con servidores **locales**
(`net/http/httptest` + `websocket.Accept`): eco round-trip (varios mensajes en
orden), recv → nil tras cierre del servidor (y siguientes recv siguen dando nil),
recv → nil tras `Ws:close()` local, `send` tras close → `ECLOSED`, puerto cerrado
→ `ENET`, handshake mudo (servidor TCP que no contesta) → `ETIMEOUT`, close
idempotente, **close por cleanup al cancelar la task** (sin fuga de goroutines,
medida con `NumGoroutine`), fuera de task → `EINVAL`, `url`/`opts`/`headers`/
`timeout_ms` malos → `EINVAL`.

**CP-5 (cierra Fase 4)** prueba las cuatro capacidades de red juntas: (a)
`http.request` trata un 404 como dato; (b) un SSE consumido con `Stream:events()`
**mientras otra task contadora avanza** —se comprueba `ticks > 0` mientras el SSE
se consume, demostrando que el event loop NO se bloquea (el puente ⏸ libera el
token)—; (c) un ws de eco round-trip; (d) un consumidor lento que desborda el
buffer → `EIO` (backpressure de S20).

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S20). Binario `nu -e` confirma e2e contra un servidor de eco local: `send`/
`recv` round-trip (`hola`/`mundo`), recv tras cerrar → `nil`, puerto cerrado →
`ENET`.

**Sin hallazgos:** §8 bastó. **Fase 4 (Red) cerrada — CP-5 verde.** Puntero ▶
avanza a **S22** (Fase 5 — Texto y búsqueda).

## S22 — `nu.text` (width/wrap/truncate) + tipo `Block` + `nu.ui.block`/`caps`/`Style` (api.md §10, §9.2, 🔒)

Abre la **Fase 5 (Texto y búsqueda)**. Sesión fundacional: el tipo `Block` que se
fija aquí es la moneda común que construyen y consumen S23 (markdown), S24
(highlight), S25 (diff) y S29 (blit/viewport), y `text.width` es la lógica 🔒
sobre la que descansa TODO el cálculo de layout (wrap, truncate, recorte de
viewport).

### La librería: `github.com/rivo/uniseg` (puro-Go)

La anchura en celdas no es número de bytes ni de runes ni de graphemes: hay que
contemplar **grapheme clusters** (una "é" puede ser base+combining = 2 runes, 1
celda), **east-asian wide** (CJK/hangul = 2 celdas) y **emoji con secuencias ZWJ**
(una familia 👨‍👩‍👧‍👦 son 4 emojis unidos por U+200D que el terminal pinta como
1 glifo = 1 grapheme de anchura 2). Reimplementar las tablas Unicode en Go sería
absurdo y frágil: se delega en `rivo/uniseg` (v0.4.7), puro-Go (CGO_ENABLED=0
intacto, sin deps transitivas), que expone `StringWidth(s)` (anchura monospace
total) y `FirstGraphemeClusterInString` (iteración cluster a cluster con su
anchura, para recortar sin partir un grapheme). Pasa de `// indirect` a directa
en `go.mod` tras `go mod tidy`. Alternativas descartadas: `go-runewidth`
(maneja east-asian pero no clusters/ZWJ correctamente para nuestro caso);
`x/text` (no da anchura de celda lista para usar).

### La ESTRUCTURA del tipo `Block` (crítica para S23–S29)

Un **Block** es un **handle opaco** (`*lua.LUserData` con metatabla
`nu.ui.Block`) cuyo `__index` solo expone `.width` y `.height` (números) a Lua;
el contenido es interno (no se expone como tabla mutable: el Block es opaco,
§9.2). Internamente (`block.go`):

- `type span struct { text string; st *style }` — un tramo de texto con estilo.
  `text` es UTF-8 crudo; `st` nil = sin estilo (hereda lo de debajo al pintar).
- `type style struct { fg, bg string; fgSet, bgSet bool; bold, italic,
  underline, reverse bool }` — el estilo de un span. Los colores se guardan
  **normalizados como string**: un literal `"#rrggbb"` (validado, a minúsculas) o
  el índice 0-255 como decimal-string (`"42"`). Guardar string (no un tipo color
  resuelto) preserva la intención literal hasta que el compositor (S29) lo degrade
  a `nu.ui.caps().colors` —el render decide, no el Block (§9.2, G22)—.
- `type block struct { lines [][]span; width, height int }` — una rebanada de
  líneas, cada línea una rebanada de spans. `width` = **máximo ancho de línea en
  celdas** (vía `uniseg.StringWidth`), `height` = nº de líneas. Ambos se calculan
  **una vez** en `newBlock` (único constructor) y se **cachean**: el Block es
  **inmutable** (wrap/markdown/diff devuelven uno nuevo, no mutan), y el
  compositor consultará `.width`/`.height` en cada blit, así que recalcular sería
  el coste cuadrático que ADR-007 evita.

**Por qué spans y no una rejilla de celdas ya resuelta:** el Block es una
*descripción* (texto lógico por tramos de estilo), no una *pintura*. La rejilla,
el recorte de viewport y el degradado de color son del compositor (S29); guardar
spans deja a S25/S23 construir líneas concatenando tramos sin pensar en celdas, y
mantiene "blit = copia, nunca re-render" (§9.1). Helpers que S23–S29 reusan:
`newBlock(lines)`, `pushBlock`, `checkBlock(L, idx)`, `lineWidth(spans)`.

### `nu.text.width/wrap/truncate` — CPU puro, [W], **ninguna ⏸**

`text` es [W] (§16) pero **ninguna suspende**: miden/reordenan un string ya en
memoria, no esperan IO (como los codecs de S18). Por eso NO usan el puente
`suspend` ni `requireTask` —corren síncronas en el estado principal (y en workers
con S34)—. [W] = "disponible en workers", no "suspende".

- `width(s)` → `uniseg.StringWidth(s)`. Vacío = 0.
- `wrap(s, width, opts?)` → Block. Word-wrap por palabras (espacios ASCII), con
  los `\n` de `s` como **límites duros** (un `\n\n` deja una línea en blanco). Una
  palabra **más ancha que `width`** se **parte por grapheme** (`splitWide`) en
  trozos ≤ `width` —partir es preferible a desbordar el viewport, que recortaría
  y perdería texto en silencio—. `width <= 0` → `EINVAL`. `opts.style` aplica un
  `Style` por defecto a cada span producido. El wrap colapsa el espaciado (un
  espacio entre palabras de una misma línea); preservar el espaciado exacto no es
  el contrato de un word-wrap.
- `truncate(s, width, opts?)` → string. Recorta a ≤ `width` celdas **por
  grapheme** (nunca parte un cluster/emoji). Si `s` cabe entero, se devuelve tal
  cual (sin elipsis). `opts.ellipsis` (p. ej. "…") se reserva su anchura del
  presupuesto; si la elipsis es **más ancha que `width`**, se cae a recorte simple
  sin elipsis (mejor texto a secas que nada). `width == 0` → "". `width < 0` →
  `EINVAL`.

### `nu.ui.block`/`caps`/`Style` y la NOTA DE FRONTERA (G20 es S32)

El contrato dice que sin TTY `nu.ui` **no existe** (G20). Pero ese gating es S32,
y S23–S31 necesitan `nu.ui.block`/`caps`/`Style` **ya** para construir e
inspeccionar Blocks en sus tests (markdown/highlight/diff producen Blocks; el
theme resuelve `Style`). Decisión: en S22 `nu.ui` se cuelga **siempre** (también
headless) con solo `block`/`caps`; S32 añadirá la condición de TTY por encima sin
tocar estas firmas. `nu.has("ui")` sigue en **false** hasta S32 (no se afirma una
capacidad que aún no se concede). Es deuda explícita, no contradicción de G20.

- `nu.ui.block(lines)` → Block. Cada línea es un **string** (un span sin estilo)
  o un **array de Spans** `{text, style?}`. Calcula `.width`/`.height` al
  construir. Una línea vacía `""` conserva su hueco (afecta a `.height`).
- `Style` (`parseStyle`/`normalizeColor`): colores **literales** —`"#rrggbb"` (6
  hex) o índice 0-255 (número o string numérica)—; un **nombre semántico**
  (`"accent"`) o un hex mal formado o un índice fuera de rango → `EINVAL` (los
  nombres son vocabulario del theme, G22).
- `nu.ui.caps()` → `{colors, kitty_keyboard, mouse, images}`. Sin terminal vivo
  que interrogar (eso es Fase 6), `colors` se estima por entorno
  (`COLORTERM=truecolor` → 16M; `TERM` con "256color" → 256; `TERM` vacío
  headless → 256 default razonable; `dumb` → 0; resto → 16) y los protocolos
  (kitty_keyboard/mouse/images) quedan en `false` (deny-by-default hasta la
  negociación de Fase 6, como `nu.has`).

### Tests 🔒 y verificación

`text_test.go`: **width** table-driven y NOMBRADO (vacío=0, ascii, ascii con
espacios, CJK wide=2, hangul=2, mezcla, emoji simple=2, **emoji ZWJ familia=2**,
é precompuesto=1, é combinante base+marca=1, combining suelto, varios emojis) +
vía Lua (incl. combining acute por bytes `\204\129`). **wrap** (vacío→[""], cabe,
envuelve por palabra, palabra justa, **palabra más larga que el ancho se parte**,
`\n` duro, línea en blanco entre párrafos, CJK por celdas) con invariante "ninguna
línea > width". **truncate** (cabe entero/justo, con/sin elipsis, width 0, elipsis
multi-celda, **no parte emoji**, emoji entero cuando cabe, **no parte grapheme
combinante**, elipsis más ancha que width→recorte simple) con invariante
"resultado ≤ width". **splitWide** (emoji×3 en width 2 → 3 trozos; emoji de 2
celdas en width 1 → trozo único sin partir). **ui.block** manual inspeccionado en
Go (width=máx línea, height=nº líneas, spans con estilo, línea en blanco
conservada), color índice normalizado, validaciones→EINVAL, **caps** (4 claves,
colors>0, protocolos false), **normalizeColor** (hex a minúsculas, índices,
rechazo de nombres G22). `TestTextNotSuspending` confirma que corren fuera de
task.

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S21). Binario `nu -e` confirma e2e: width (5/4/2/2 para ascii/CJK/emoji/ZWJ),
wrap (height/width), truncate con elipsis, ui.block (width 6/height 2), caps, y
G22 (`fg="accent"` → EINVAL).

**Sin hallazgos:** §10 y §9.2 bastaron. Puntero ▶ avanza a **S23**
(`nu.text.markdown`, 🔒).

## S23 — `nu.text.markdown` (render completo, streaming-safe, themable) (api.md §10, 🔒)

`nu.text.markdown(s, opts) -> Block` renderiza markdown completo a un `Block` de
ancho `opts.width`. Es **[W] pero NINGUNA ⏸** (CPU puro, como `width`/`wrap`/
`truncate` de S22 y los codecs de S18: parsea un string ya en memoria, no espera
IO; por eso no usa `suspend` ni `requireTask`). Vive en `markdown.go`; se cuelga
desde `registerText` (text.go) para mantener todo `nu.text` en un sitio.

### Librería: goldmark (puro-Go, CommonMark)

`github.com/yuin/goldmark` v1.7.8 — puro-Go, CommonMark, **sin deps transitivas**
que afecten a `CGO_ENABLED=0` (ADR-001). "Lua decide, Go ejecuta" (ADR-004): el
parseo del documento a AST lo hace goldmark (`goldmark.DefaultParser().Parse(
text.NewReader(src))`) y nosotros recorremos el AST emitiendo spans en el
`Block`. Se **reusa** todo lo que se pueda de S22: `wrapText`/`splitWide` (word-
wrap por palabras y partido por grapheme), `uniseg.StringWidth` (anchura en
celdas, la misma de `text.width`) y `parseStyle`/`normalizeColor` (theme con
colores literales, G22). Pasa de `// indirect` a directa tras `go mod tidy`.

### Modelo de theme

`opts.theme` es una tabla con un `Style` por elemento; claves: `h1`..`h6`,
`code`, `emphasis`, `strong`, `link`, `bullet`, `blockquote`, `rule`. Cada una es
opcional; lo ausente cae al `defaultTheme()`: bold en headings/strong, italic en
emphasis/blockquote, underline en link, **sin color** (no imponemos paleta; el
toolkit la añade vía `opts.theme`). Los colores son **literales** (`#rrggbb` o
índice 0-255), validados por `parseStyle`; un nombre semántico (`"accent"`) →
`EINVAL` que nombra el elemento (G22: los nombres son del theme del toolkit, no
del core). El estilo inline se compone con `combineStyle` (ORea los atributos
booleanos, los colores de `add` pisan a los de `base`): un `[link]` dentro de
*itálica* conserva la itálica y añade el subrayado.

### Elementos soportados (y tablas: NO)

Headings (reestilizados con el Style del nivel por encima de su énfasis interno),
párrafos con word-wrap conservando estilos inline (**bold**/*italic*/`code
inline`/[link]/autolink), code blocks fenced e indentados (una línea por línea de
código, **SIN envolver** —el código no reflowea; el compositor recorta—, un span
por línea con `theme.code`), listas `-`/`*`/`1.` (marcador + sangría colgante;
`Start` respetado en ordenadas), blockquotes (prefijo `> ` + contenido), reglas
`---` (línea de guiones), enlaces (texto con `theme.link`).

**Tablas: NO se soportan** en S23. Son una extensión GFM, no CommonMark base;
goldmark sin extensiones no las parsea, así que una tabla cae a un párrafo de
texto plano (las celdas con `|`) — válido y estable, solo sin formato de tabla.
Si una extensión las pide, se reabre como P## y se activa la extensión de
goldmark.

### Anchura de los contenedores (prefijo + contenido ≤ width)

Un blockquote (`> `, 2 celdas) o un ítem de lista (`- `/`1. `, N celdas) consume
ancho con su prefijo. Para que prefijo+contenido no exceda `opts.width`, el
contenido interno se renderiza a un ancho **reducido** (`renderChildrenWidth`
baja `r.width` temporalmente al rango del prefijo y lo restaura al volver). El
ancho reducido es fijo por contenedor (no depende del contenido posterior), así
que no compromete la estabilidad. El marcador de una lista ordenada puede crecer
(`9.`→`10.`), un reflow menor confinado al bloque de la lista (que es un único
bloque de nivel superior; el invariante solo protege los bloques *anteriores*).

### wrapSpans: word-wrap de spans estilizados (nuestro)

Generaliza el word-wrap de S22 a una secuencia de spans con estilo. `tokenizeSpans`
trocea en palabras recordando `sepBefore` (si venía tras un espacio en el
origen): **no se inventa un espacio donde el origen no lo tenía** —esto arregla
el bug de "code ." (un `code` inline pegado a un punto), "*no cierra" (un `*`
huérfano pegado a la palabra) y "[aqui](http" (un enlace sin cerrar)—. Tokens
pegados (sin separación) forman un **grupo atómico** que no se parte al envolver
(es una palabra visual); entre grupos va un espacio, y un grupo más ancho que
`width` se parte por grapheme con `splitWide` conservando el estilo de cada
token.

### STREAMING-SAFE y el invariante de estabilidad (la lógica 🔒)

**Entrada incompleta no rompe.** goldmark es tolerante: parsea hasta EOF (un
fence ```...sin cerrar es un code block hasta el final del texto, un `*énfasis`
sin cerrar cae a texto plano, un `[enlace](sin cerrar` queda como texto). No
dependemos de que el último bloque esté "cerrado"; el render produce siempre un
Block válido (height ≥ 1) sin panic ni error.

**Estrategia de estabilidad: render por bloques de nivel superior
INDEPENDIENTES.** `renderMarkdownBlocks` devuelve `[][][]span` (una rebanada por
hijo directo del documento); el Block es su concatenación. La clave: markdown es
estable por bloques al crecer por el final —añadir texto solo afecta al ÚLTIMO
bloque de nivel superior (el "en construcción"); los anteriores ya están
delimitados por una línea en blanco o un cambio de tipo—. Renderizar por bloques
(no un layout global) es lo que evita que un fence abierto al final reflowee los
párrafos de arriba.

**INVARIANTE EXACTO (lo que blinda el test 🔒):** sea `R(s) = [B_1, ..., B_m]` la
descomposición del render por bloques de nivel superior. Para `s_k` prefijo de
`s_{k+1}` (un token más), `B_i(s_k) == B_i(s_{k+1})` para todo `i < m_k - 1` (los
bloques ya completos, todos salvo el último del prefijo corto, no cambian; solo
crece por el final). Las excepciones de CommonMark (setext heading que
reinterpreta el párrafo previo con un subrayado `===`/`---`; lazy continuation
que extiende un párrafo) NO rompen el Block, solo relajan el ÚLTIMO bloque, por
eso el invariante lo excluye. El test emite los límites de bloque y compara
bloque a bloque salvo el último (0 violaciones sobre 4 docs por-rune y un troceo
por-token).

### Punto de extensión para S24 (highlight)

`renderCodeBlock` aplica hoy UN span (`theme.code`) por línea de código y ya
extrae `lang` con `languageOf`. S24 (`nu.text.highlight`) sustituirá ese span
plano por N spans coloreados por token según el lexer del lenguaje, manteniendo
el MISMO armazón (una entrada por línea del código) — por eso `lang` se pasa ya a
`renderCodeBlock` aunque hoy se ignore.

### Tests 🔒 y verificación

`markdown_test.go`: (1) **entrada incompleta no rompe** (tabla de ~20 casos:
fence/lista/ordered/italic/bold/code-inline/link/heading/quote/hr/setext a
medias, backtick/asterisco/corchete sueltos, mezcla caótica) + vía Lua; (2)
**crecimiento estable** por-rune sobre 4 docs y un troceo por-token, comparando
los bloques de nivel superior salvo el último (invariante exacto, 0 violaciones);
(3) render de cada elemento (heading por niveles, emphasis/strong/normal, code
block sin-wrap + `lang` extraído, listas viñeta/ordenada con `Start`/sangría
colgante, blockquote prefijo+estilo, hr a `width`, link subrayado, párrafo
wrap≤width, Block.width≤opts.width); (4) theme literal aplicado (inspección en
Go) + G22→`EINVAL` nombrando el elemento, `opts.width` obligatorio (7
caminos→`EINVAL`: sin opts, sin width, 0, negativo, no-entero, opts no-tabla,
width no-número), no-suspende fuera de task.

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S22). Binario `nu -e` confirma e2e: doc completo (height 5, width 23), fence
incompleto (height 1), theme `fg="accent"` → rechazado (G22).

**Sin hallazgos:** §10 bastó. Puntero ▶ avanza a **S24** (`nu.text.highlight`,
🔒).

## S24 — `nu.text.highlight` (syntax highlighting, degrada a texto plano) (api.md §10)

`nu.text.highlight(code, lang, opts?) -> Block` resalta un snippet a un `Block`
con un span por tramo coloreado según su tipo de token. **[W] pero NINGUNA ⏸**
(CPU puro como S22/S23/S18: tokeniza un string ya en memoria, no espera IO; ni
`suspend` ni `requireTask`). Vive en `highlight.go` y se cuelga de `nu.text`
junto a `width`/`wrap`/`truncate`/`markdown`. Ni una función pública de más.

### Librería: chroma, y la decisión de versión

Se usa `github.com/alecthomas/chroma/v2` (puro-Go, decenas de lexers y themes que
asignan colores `#rrggbb` por tipo de token — justo lo que el `Block` quiere
guardar como color LITERAL, G22). Es la opción canónica de highlighting en Go y
encaja con "Lua decide, Go ejecuta" (ADR-004): el léxico pesado va en Go.

**Desviación deliberada — se pinea v2.14.0, no la última (v2.27.0).** La última
de chroma declara `go >= 1.25` en su `go.mod`; añadirla **subiría la versión `go`
del módulo `nu` de 1.24.7 a 1.25** (un cambio de toolchain de todo el proyecto,
no una decisión que toque a S24). v2.14.0 mantiene `go 1.24.7` intacto y trae los
mismos lexers/themes necesarios. Trae `github.com/dlclark/regexp2` como dep
transitiva (puro-Go, `CGO_ENABLED=0` intacto). Si el módulo sube a Go ≥ 1.25 por
otro motivo, se puede actualizar chroma sin coste; el disparador es ese.

### El degradado a texto plano (la lógica propia)

Un `lang` **desconocido, vacío o nil** NO es un error: degrada a **texto plano**
—un `Block` sin estilo (un span por línea vía `splitLines` de S22), con el texto
EXACTO—. Es la red de seguridad del render de fences en streaming de S23: un
fence con un `lang` que no reconocemos (o sin `lang`) sigue dando un Block
legible en vez de romper. La señal es que `lexers.Get(lang)` devuelve `nil`
cuando no hay lexer para ese nombre (tras intentar también por extensión); `lang`
vacío ni se consulta. Un fallo de tokenización (no esperado con los lexers
embebidos) también cae a texto plano: highlight nunca rompe el render.

### El mapeo tokens→spans

Lexer encontrado → se envuelve en `chroma.Coalesce` (funde tokens adyacentes del
MISMO tipo: menos spans, mismo resultado, texto idéntico). Se tokeniza con
`EnsureLF=false` (no alterar el texto de origen: queremos reconstruir `code`
EXACTO desde los spans). Se agrupa por línea con `chroma.SplitTokensIntoLines`
(una línea de código → una línea de Block) — esa función deja el `\n` como sufijo
del token que cierra cada línea, que se recorta con `TrimSuffix` (el salto de
línea es estructura del Block, no texto del span). Cada tramo se emite como
`span{text, style}` con `tokenStyle(theme, tok.Type)`: el color de primer plano
(literal `#rrggbb` de `Colour.String()` si `IsSet()`, G22) y los atributos
bold/italic/underline (los trileanos `Yes` de Chroma); un token sin color ni
atributos → `st = nil` (sin estilo), para no inflar el Block. Chroma no expone
"reverse", así que ese atributo queda en false. Una línea de código en blanco
conserva su hueco (un span vacío sin estilo); código vacío → un Block de height 1.

### Theme: el nombre, no un mapeo a mano

`opts.theme` es un **string**: el nombre de un theme de Chroma (default
`"github"`, claro y legible). Un theme desconocido cae al fallback propio de
`styles.Get` (nunca nil, no rompe). **No** se acepta un mapeo de `Style` por tipo
de token a mano: los `TokenType` de Chroma son un vocabulario amplio (decenas de
subcategorías) y exponerlos filtraría el detalle de la librería a la API pública;
el nombre de theme es la única perilla, y un theme de Chroma ya da colores
literales coherentes con G22. La firma §10 es `highlight(code, lang, opts?)`;
`opts` solo lleva `theme?` por ahora — sin ampliar la superficie.

### Frontera: NO se toca markdown.go

S23 dejó `renderCodeBlock` como "punto de extensión" para que S24 sustituyera su
span plano por N spans coloreados. **Esa integración se deja para después**: S24
implementa `nu.text.highlight` standalone y NO modifica `markdown.go`, para no
arriesgar el invariante de estabilidad (streaming-safe) de S23. La integración
highlight-dentro-de-markdown es trabajo futuro reabrible (un `opts.highlight` o
similar en `markdown`), fuera del alcance de §10 de `highlight`.

### Tests (`highlight_test.go`)

Sobre el núcleo puro `highlightToBlock` (sin LState): Go → varios spans con
estilo y ≥2 colores `#rrggbb` distintos, `.height`=nº líneas; desconocido/""/lang
extraño → texto plano sin estilo + texto EXACTO; json/python/lua → spans
razonables (≥2 colores); línea en blanco conserva hueco; código vacío → height≥1;
theme desconocido → fallback. Invariante transversal "no se pierde texto":
concatenar los spans por línea reproduce `code`. Vía Lua (`buildBlock` de
text_test.go): Go height/estilo, desconocido→plano, `.height` legible, sin-opts y
`opts.theme` válidos; usos malos de la firma (lang no-string, opts no-tabla,
`opts.theme` no-string) → `EINVAL` (lang nil/"" NO es error: degrada).

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S23). Binario `nu -e` confirma e2e: go (height 3), desconocido → plano
(height 2), `opts.theme` no-string → `EINVAL`.

**Sin hallazgos:** §10 bastó. Puntero ▶ avanza a **S25** (`nu.text.diff`).

## S25 — `nu.text.diff` (hunks estructurados + render a Block) (api.md §10, 🔒)

`nu.text.diff(a, b, opts?) -> {hunks, block?}` compara `a` (viejo) y `b`
(nuevo) **línea a línea** y devuelve sus hunks (regiones de cambio) y,
opcionalmente, un `Block` pintado. **[W] pero NINGUNA ⏸** (CPU puro, como
`width`/`wrap`/`markdown`/`highlight` de S22–S24 y los codecs de S18): no usa el
puente `suspend` ni `requireTask`. Vive en `diff.go` y reusa los helpers de
Block de S22 (`newBlock`/`span`/`style`, `parseStyle`/`normalizeColor`).

### Algoritmo / librería: LCS line-based propio, SIN dependencia nueva

La tarea ofrecía usar `go-difflib` o `gotextdiff`. Se decide **no añadir
ninguna dependencia**: el diff line-based clásico (LCS por programación dinámica
→ backtrack → agrupado en hunks con contexto) es pequeño, su corrección en los
bordes es exactamente lo que el test 🔒 blinda, y mantenerlo propio evita atar
la forma de los hunks (la API pública del consumidor) a la de una librería
externa. `go.mod`/`go.sum` quedan **intactos** (cero deps nuevas, coherente con
"cero dependency hell", ADR-001/filosofía §6).

Las piezas (todas puras, sin LState, testeables directamente):

- `splitDiffLines(s)`: parte `s` por `\n` tratando el salto como **terminador,
  no separador** — `"a\n"` y `"a"` dan ambos `["a"]`; `""` da cero líneas;
  `"a\nb"` da `["a","b"]`. Así **"sin newline final"** no introduce diferencias
  espurias frente al mismo texto con newline final (caso de borde 🔒). El `\r`
  de un CRLF se conserva dentro de la línea (el diff es por contenido exacto;
  normalizar finales de línea es del consumidor).
- `lcsTable(a, b)`: longitudes de la subsecuencia común más larga por DP,
  rellenada de atrás hacia delante para que el backtrack avance en orden de
  fichero. O(n·m); suficiente para diffs de tamaño humano (Myers O(ND) reabrible
  si hiciera falta para ficheros enormes).
- `diffOps(a, b)`: backtrack que emite la secuencia `context`/`del`/`add`. El
  desempate (ante un cambio, `del` antes que `add`) es el del diff unificado:
  una línea modificada sale como su `del` seguido de su `add`.
- `groupHunks(ops)`: agrupa los cambios en hunks rodeándolos de a lo sumo
  **`diffContextLines` = 3** líneas de contexto a cada lado (el estándar de
  facto), y **funde** dos cambios separados por ≤ 2·contexto en un solo hunk
  (su contexto se solapa), como hace el diff unificado.

### La forma de los hunks (la API que consume el visor / toolkit)

Cada hunk: `{ old_start, old_count, new_start, new_count, lines = { {kind, text},
... } }`, con `kind` ∈ `"context"|"del"|"add"`. Los índices son **1-based**
(convención Lua). `old_start`/`new_start` apuntan a la primera línea (contexto o
cambio) del hunk en cada lado; `old_count`/`new_count` son cuántas líneas de ese
lado abarca el hunk (contexto+del para old, contexto+add para new). Cuando un
lado **no toca ninguna línea propia** (p. ej. `a` vacío → `b`: todo add; o `b`
vacío: todo del) su `*_start` y `*_count` son **0** — la convención del diff
unificado (0 = posición de inserción al principio). `a == b` → `hunks` vacío
(`#hunks == 0` distingue "sin cambios" sin ambigüedad).

### El render (`opts.render = true`)

`renderDiffBlock` pinta una cabecera `@@ -o,oc +n,nc @@` (estilo `header`,
negrita) por hunk y, debajo, una línea por operación con prefijo `+ `/`- `/`  `
y el estilo del tipo. El theme por defecto (`defaultDiffTheme`, G22): add
**verde** (índice ANSI `"2"` LITERAL), del **rojo** (`"1"`), contexto sin
estilo, header negrita. Colores **literales** (índice o `#rrggbb`), que el
compositor S29 degradará con `caps().colors` (el Block guarda literales, nunca
nombres semánticos). `opts.theme` (claves `add`/`del`/`context`/`header`) valida
cada `Style` con `parseStyle` (un nombre semántico como `"accent"` → `EINVAL`,
G22). Sin hunks → Block vacío válido (una línea en blanco, height 1: un Block
siempre tiene ≥1 línea, como en markdown/highlight).

### `diffContextLines` fijo en 3 (sin perilla en `opts`)

El nº de líneas de contexto NO se expone por `opts`: la firma §10 no lo
contempla y 3 es el estándar de facto del diff unificado. Añadir una perilla
sería ampliar la superficie pública sin necesidad (API sagrada, ADR-003);
reabrible si un consumidor concreto lo pide.

### Tests (`diff_test.go`, table-driven nombrando los bordes 🔒)

`TestComputeDiffEdges` blinda: inserción pura, borrado puro, cambio (del+add),
cambio en la **PRIMERA** línea, cambio en la **ÚLTIMA** línea, `a` vacío → todo
add (rangos old 0,0), `a` → `b` vacío todo del (new 0,0), ambos vacíos sin
hunks, `a == b` sin hunks, una sola línea, **sin newline final == con newline**,
sin-newline última línea cambiada, inserción al principio, append al final, dos
cambios lejanos → 2 hunks, dos cercanos → 1 hunk fusionado. Más
`TestSplitDiffLines` (terminador vs separador, CRLF),
`TestDiffLinesConsistentWithSources` (cada `context`/`del` casa con `a`,
`context`/`add` con `b`), `TestRenderDiffBlock` (prefijos +/-/␣, estilos
verde/rojo/neutro, height = cabecera + ops), `TestRenderDiffBlockEmpty`
(height 1), y vía Lua (`TestDiffLua` hunks inspeccionados/`.height`/sin
render→`block==nil`, `TestDiffLuaErrors` opts no-tabla / `opts.theme` nombre
semántico G22 / theme no-tabla → `EINVAL`, `TestDiffLuaTheme` colores literales
aplicados al render).

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S24). Binario `nu -e` confirma e2e: cambio en medio → 1 hunk
(context/context/del/add/context), block.height = 6, rangos old/new 1,4; `a==b`
→ 0 hunks; `a` vacío → old_count 0 / new_count 2; `opts` no-tabla → `EINVAL`.

### Corrección: umbral de fusión al boundary 2·contexto (off-by-one)

El umbral de fusión de `groupHunks` tenía un off-by-one: la condición era
`next - diffContextLines <= end`, que fundía huecos de contexto ≤ 5 pero
**separaba** un hueco de exactamente 6 (= 2·`diffContextLines`), contradiciendo
el propio comentario de la función y esta entrada ("funde dos cambios separados
por ≤ 2·contexto"). `git diff -U3` y `GNU diff -U3` funden hasta hueco 6 y
separan a partir de 7 (verificado). Cuando los dos bloques de contexto quedan
**adyacentes sin solaparse** (hueco = 6) deben seguir en un solo hunk. Fix
mínimo: `next - diffContextLines <= end + 1` (el `+1` cubre la adyacencia). Los
rangos del hunk fusionado abarcan ambos cambios + todo el contexto intermedio.
Tests de frontera añadidos a `TestComputeDiffEdges` nombrando el caso: hueco de
contexto = 5 → 1 hunk, = 6 → 1 hunk (el que fallaba, con rangos old/new 1,8
comprobados), = 7 → 2 hunks; coherente con `diff -U3`.

**Sin hallazgos:** §10 bastó (`diff(a, b, opts?)` se implementó tal cual; `opts`
usa `render?`/`theme?`). Puntero ▶ avanza a **S26** (`nu.re`).

## S26 — `nu.re` (RE2: compile/match/find_all/replace)

`nu.re` implementa la fila de §10 (`nu.re.compile(pattern) -> Re` + el handle
`Re` con `match`/`find_all`/`replace`) sobre el `regexp` de la stdlib de Go,
que **es RE2**. Tres decisiones de diseño propias —la forma de las capturas,
las unidades de los rangos y la sintaxis de `repl`— y una observación sobre la
elección de motor.

### Por qué RE2 (y sin dependencia nueva)

El `regexp` de la stdlib **es** una implementación de RE2: garantiza tiempo
**lineal** sobre el tamaño de la entrada (autómata, sin backtracking) a cambio
de no soportar **backreferences** ni lookaround. Para un harness es justo lo
que se quiere: un patrón venido de un agente o de la configuración NUNCA puede
colgar el runtime con un ReDoS (backtracking catastrófico). El precio se
documenta y se reporta: `compile("(a)\\1")` (backreference) → `EINVAL` con el
mensaje de `regexp.Compile` incrustado (la stdlib lo reporta como secuencia de
escape inválida), no un fallo silencioso. **Cero dependencias nuevas**
(`go.mod`/`go.sum` intactos, ADR-001). `*regexp.Regexp` es **seguro para uso
concurrente** (lo garantiza su doc), así que un mismo `Re` se casa desde varias
tasks sin candado (encaja con el modelo de concurrencia del navegador,
ADR-004). Es **[W] pero NINGUNA ⏸** (CPU puro: compila/casa un string en
memoria, sin IO; como `nu.text` y los codecs de S18): ni `suspend` ni
`requireTask`.

### Decisión: forma de `caps` en `Re:match` — array 1-based + grupos con nombre

`Re:match(s)` devuelve, ante coincidencia, una tabla con DOS vistas a la vez:

  - **Parte array, 1-based** (estilo Lua): `[1]` es la coincidencia COMPLETA
    (el grupo 0), `[2]` el primer grupo, `[3]` el segundo, etc. Así `caps[1]`
    es SIEMPRE el match entero, aunque el patrón no tenga grupos (un patrón sin
    grupos da `caps[1]` y nada más).
  - **Grupos con nombre** (`(?P<name>...)`, sintaxis de nombres de RE2/Go)
    ADEMÁS por su **clave string**: `caps.name`. Conviven con la parte array
    (un grupo con nombre aparece dos veces: por su índice posicional y por su
    nombre), lo que deja a Lua acceder como prefiera.

Alternativas descartadas: (a) solo grupos (sin el match 0 en `[1]`) —rompía el
caso "patrón sin grupos" y obligaba a un campo aparte para el match completo—;
(b) un campo `.groups` separado del `.full` —más verboso sin ganancia—. El
array 1-based con `[1]`=match completo es el convenio más natural en Lua y el
que menos sorprende. Sin coincidencia → `nil` (no lanza: no casar es un
resultado válido, no un error). Un grupo opcional que no participó (p. ej.
`(a)?` sin "a") → `""` (string vacío): `FindStringSubmatch` no distingue
"vacío" de "ausente" en su salida de strings, y un array Lua no admite `nil`
intermedio sin agujerearse.

### Decisión: unidades de `Re:find_all` — rangos de byte, 1-based, inclusivos

`Re:find_all(s)` devuelve TODAS las coincidencias (no solapadas, de izquierda a
derecha) como un array de rangos `{start, end}` con **offsets de BYTE, 1-based,
ambos inclusive** —el MISMO convenio que `string.find` de Lua—, de modo que
`s:sub(start, end)` reconstruye EXACTAMENTE cada coincidencia.

Se eligen **bytes** (no runes/caracteres) por dos razones que se refuerzan: (a)
`string.sub` de Lua indexa por byte, así que el rango es directamente
utilizable (componer con `s:sub` es el caso de uso obvio: localizar/resaltar);
(b) `FindAllStringIndex` de Go ya devuelve offsets de byte —convertir a runes
obligaría a recontar y rompería esa composición—. La conversión de convenios:
Go da `[inicio, fin)` 0-based con fin **exclusivo**; a Lua, `start = inicio+1`
(1-based) y `end = fin` (un fin exclusivo 0-based coincide numéricamente con el
último byte 1-based inclusive). Una coincidencia **vacía** (p. ej. `x*` sobre
"ab" casa el vacío en cada posición) da `end = start-1` (longitud cero),
coherente con que `s:sub(start, start-1)` es `""` en Lua.

Se devuelven **solo los rangos de la coincidencia completa**, no los de cada
grupo: es el caso común (resaltar/localizar dónde casa el patrón) y mantiene la
firma simple. Quien necesite las capturas de cada coincidencia las saca con
`match` sobre el tramo; si el patrón "rangos por grupo" se repitiera, sería una
adición futura (no se especula API; la superficie de §10 es sagrada).

### Decisión: sintaxis de `repl` en `Re:replace` — la de Go

`Re:replace(s, repl)` sustituye TODAS las coincidencias no solapadas y delega
en `Regexp.ReplaceAllString`, así que `repl` usa la **sintaxis de Go**: `$1`,
`$2`, ... refieren grupos por número; `${name}` por nombre; `$0` (o `${0}`) la
coincidencia completa; `$$` es un `$` literal. Un nombre no delimitado por
llaves se extiende hasta el último carácter alfanumérico (`$1x` busca el grupo
"1x", no el grupo 1 seguido de "x"): se recomienda `${1}x` —el mismo matiz que
documenta la stdlib—. Una referencia a un grupo inexistente se reemplaza por
vacío. No se inventa una sintaxis propia (`\1` u otra): reusar la de la
librería es menos superficie, menos sorpresas y documentación gratis. Sin
coincidencias → `s` intacto.

### Tests

Vía el arnés Lua (`re_test.go`), porque la forma de la tabla de capturas y de
los rangos solo es observable desde Lua: `TestReMatchPositional` (`(\d+)-(\d+)`
sobre "12-34" → `[1]`/`[2]`/`[3]`), `TestReMatchNamed` (por índice Y por
nombre), `TestReMatchNoGroups`, `TestReMatchNoMatch` (→nil), `TestReMatchEmpty
String` (`\d+` no casa el vacío, `\d*` sí con match vacío), `TestReMatchOptional
Group` (grupo ausente→""), `TestReFindAllRanges` (`s:sub` reconstruye cada match
+ offsets concretos), `TestReFindAllNone`, `TestReFindAllUTF8` (offsets de BYTE
coherentes con `string.sub` sobre texto multibyte), `TestReFindAllEmptyMatch`
(`end=start-1`), `TestReReplaceNumbered`/`Named`/`NoMatch`/`All`,
`TestReCompileBackreference` (`(a)\1`→`EINVAL` con mensaje, criterio de hecho),
`TestReCompileInvalidSyntax` (`(abc`→`EINVAL`), `TestReFromTask` (uso desde una
task; resultado expuesto por global tras `waitIdle`), `TestReTypeMismatch`
(self no-`Re`→`EINVAL`).

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S25). Binario `nu -e` confirma e2e: match con grupos (12-34/12/34), grupo
nombrado, no-match→nil, find_all que reconstruye por `s:sub`, replace
`${b}-${a}`, backreference→`EINVAL`.

**Sin hallazgos:** §10 bastó (`compile`/`match`/`find_all`/`replace` se
implementaron tal cual). Puntero ▶ avanza a **S27** (`nu.search.files`).

## S27 — `nu.search` (files/grep/fuzzy) (api.md §11, 🔒; cierra Fase 5 — CP-6)

Búsqueda a escala de repo: las tres primitivas de §11 en `search.go`. **[W]**
(§16; hoy estado principal, workers S34). **Ni una función pública de más**
(§11 ya estaba en api.md). **Sin hallazgos:** el puente ⏸ de S04, el patrón del
iterador del `Stream` de S20 y las librerías ya presentes (go-gitignore de S15,
`regexp`/RE2 de S26) bastaron.

### Sin dependencia nueva (`go.mod`/`go.sum` intactos)

`files`/`grep` reusan **go-gitignore** (S15) y **`regexp`/RE2** (S26). Para
`fuzzy` se evaluó añadir `github.com/sahilm/fuzzy`; **descartado**: el scorer de
un picker es un algoritmo conocido de ~50 líneas (subsecuencia con bonus, estilo
fzf simplificado), fácil de blindar y de hacer determinista; añadir una
dependencia por eso contradice "cero dependency hell" (filosofía §6) sin ganancia
(no es un parser traicionero como YAML, donde la lib sí se justificó en S18).

### `files` — recorrido y filtrado (`walkFiles`)

`filepath.WalkDir` desde `root`, en la goroutine de fondo del puente ⏸. Filtros,
en este orden por entrada: (1) `.git/` podado SIEMPRE (`SkipDir`); (2) ocultos
(nombre con `.` inicial) — sin `hidden` se podan los dirs y se omiten los ficheros,
con `hidden=true` se incluyen (salvo `.git/`); (3) `.gitignore` (cargado de
`root`, comprobado sobre la ruta RELATIVA a `root` como hace git) — lo ignorado se
poda (dir) u omite (fichero); (4) `glob` por nombre BASE del fichero. `max` corta
el `WalkDir` con un centinela (`errFilesMaxReached`) — no hay otra vía de parada
temprana en `WalkDir`. `root` inexistente → `ENOENT` (se comprueba con `os.Stat`
antes del walk, porque `WalkDir` no falla limpio si la raíz no existe).
**Decisión:** `gitignore` es SIEMPRE activo en `files` (no es opt-out como en
`watch`): un picker de ficheros sobre `node_modules/` es ruido puro; §11 no expone
una perilla para desactivarlo, así que no se inventa. El `.gitignore` mismo es un
fichero más (con `hidden=true` aparece; sus patrones no lo nombran, no se
autoexcluye).

### `grep` — el iterador paralelo (lo delicado, gemelo del `Stream` de S20)

El patrón es el del iterador de stream (S20) generalizado a N productores:

- **Enumeración primero, bajo el puente ⏸:** `walkFiles` (gitignore+glob) lista
  los ficheros candidatos en la goroutine de fondo, de modo que `root`
  inexistente → `ENOENT` al CREAR el iterador, no a mitad del consumo.
- **Pool acotado:** `grepWorkers` = `runtime.NumCPU()` acotado por el nº de
  ficheros (no lanzar 8 goroutines para 3 ficheros), suelo 1. Cada worker toma
  ficheros de un canal de trabajo y casa línea a línea (`bufio.Scanner`, buffer
  subido a 1 MiB para no abortar en líneas largas).
- **Canal SIN buffer (`results`):** backpressure natural — un worker bloquea al
  empujar un match hasta que el `next` lo saca. El `next` lee `<-results` dentro
  del `work` del puente ⏸ (fuera del token, JAMÁS toca Lua); la cuenta
  `emitted`/`max` y `it.close()` se tocan SOLO en la `deliverFn` (bajo el token).
- **EOF:** una goroutine cerradora hace `wg.Wait()` (todos los workers
  terminaron) y `close(results)`; el `next` distingue "fin" (`ok=false`) de
  "siguiente match".
- **Cancelación (S08):** al crear el iterador se registra un `nu.task.cleanup`
  (`registerGrepCleanup`, manipula la pila LIFO de la task bajo el token) que
  cierra el `context`. Cancelar/terminar la task → repartidor y workers paran
  (`ctx.Done`) y la cerradora cierra `results`, desbloqueando un `next` colgado.
  Sin esto, un `<-results` colgado tras un abort dejaría una goroutine de la task
  esperando para siempre. Red de seguridad `Runtime.Close`→`stopAllGreps`
  (rastreo `scheduler.greps`, gemelo de `streams`). `grepIter.close` idempotente
  (`closeOnce`).
- **`ranges` coherentes con S26:** byte 1-based inclusive (`FindAllStringIndex`
  +1 en el inicio, fin tal cual), de modo que `line:sub(start,end)` reconstruye
  el match — el mismo convenio que `nu.re.find_all`.
- **`opts.root` OBLIGATORIO** (¿dónde buscar?); `case` por string
  `"sensitive"|"insensitive"` (insensitive antepone `(?i)` a la regex). Ficheros
  ilegibles/binarios se saltan en silencio (como `grep -r`).

**Orden de entrega:** NO determinista entre ficheros (varios workers compiten por
el canal), pero dentro de un fichero las líneas salen en orden (un fichero lo
procesa un solo worker, de arriba abajo). §11 solo promete "según llegan", no un
orden global — el test de paralelismo verifica el TOTAL y la cuenta por fichero,
no el orden.

### `fuzzy` — síncrono, scorer propio, orden estable

**NO ⏸** (la primitiva caliente del picker, §11): CPU puro sobre datos en memoria,
como `nu.re`/codecs — no usa el puente `suspend`. `fuzzyScore`: subsecuencia
case-insensitive con base por carácter + bonus de contigüidad + bonus de inicio de
palabra (tras separador `/\_-. ` o cambio minúscula→mayúscula camelCase) + bonus
de primer carácter. `query` vacío casa todo (score 0: picker recién abierto).

**Estabilidad (inventario 🔒):** `sort.SliceStable` por score DESC comparando
**SOLO por score** (NUNCA por índice). Comparar por índice rompería la
estabilidad frente a un orden de entrada arbitrario; `SliceStable` ya conserva el
orden de entrada en los empates, que es justo lo que el contrato pide (un picker
con empates muestra los candidatos en su orden natural, no barajados). El test
estrella pasa 4 candidatos idénticos y exige el orden 1,2,3,4.

### Tests y verificación

`search_test.go` (herméticos en `t.TempDir()`): files (gitignore/hidden/glob/max/
errores), fuzzy (orden/estabilidad/scorer unitario/vacío/max/EINVAL), grep
(forma+ranges, glob/case/max, paralelo completo 50×3=150 sin pérdida/duplicado,
early-stop sin fuga de goroutines). `cp6_test.go` (CP-6, cierra Fase 5): markdown
+highlight+diff+grep+fuzzy+files juntos sobre un repo en disco, todo inspeccionado
sin pintar pantalla.

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S26). Binario `nu -e` confirma e2e. **APILevel sigue en 1.**

**CP-6 verde → `[x] Fase 5`.** Puntero ▶ avanza a **S28** (SPIKE ADR-007, abre
Fase 6).

## S28 — SPIKE de ADR-007 (compositor + toolkit Lua mínimos; HITO DE VETO)

S28 no es una feature de la API: es el **hito de veto** que valida ADR-007
(toolkit de widgets en Lua) antes de comprometer la arquitectura de UI. El
resultado y las mediciones se registran formalmente en **ADR-012** (adr.md);
aquí van las decisiones de diseño del spike que no son de espec.

### Alcance: interno y desechable, NO la API pública §9

El spike construye una versión **mínima e interna** de lo que S29 expondrá como
`nu.ui` (celdas, regiones, blit, diff→ANSI), pero **no se cuelga del global
`nu`**: vive en `internal/runtime/spike_compositor.go` (la primitiva) y
`spike_shim.go` (el puente a Lua, registrado solo desde los tests vía
`registerSpikeShim`, NUNCA desde `registerNu`). Así el veto se mide sin congelar
nada ni ampliar la superficie sagrada (api.md intacto, APILevel sigue en 1). S29
reemplaza el spike por el compositor de producción; estos ficheros son
desechables. Si §9 no hubiera bastado para el toolkit habría sido un `G##`, pero
bastó.

### El compositor mínimo (decisiones de implementación)

- **Rejilla plana** (`[]scell` indexado por `y*w+x`, no `[][]scell`) por
  localidad de caché: el diff la recorre entera cada frame. Cada celda es
  `{rune (como string, para no partir un grapheme ancho/ZWJ), *style, width}`.
- **Doble buffer** (back = frame en composición, front = último emitido). El
  back se **reutiliza** entre frames (`clear` in situ, no realloc) para no
  presionar al GC en el camino caliente (un frame por token).
- **`blitBlock` = copia con viewport (G28).** Estampa el `*block` de S22 en la
  región en coordenadas locales `(ox, oy)` que pueden ser **negativas** (offset
  negativo = empezar más abajo/derecha en el Block = scroll); el exceso recorta
  por el borde de la región. Es copia celda a celda, nunca re-render (§9.1): el
  scroll cuesta una copia de la ventana, no recalcular el Block. Graphemes
  anchos (w=2) dejan la celda siguiente como continuación (`r=""`, `w=0`).
- **Diff → ANSI por runs.** Recorre por filas; donde una celda difiere del
  front arranca un *run* con un único move-cursor (`ESC[y;xH`, 1-based) y lo
  extiende mientras siga difiriendo; emite SGR (`ESC[...m`) solo al cambiar el
  estilo respecto a la celda anterior emitida (minimiza bytes). Colores
  literales (§9.2): `#rrggbb`→truecolor (`38;2;r;g;b`), índice→256 (`38;5;n`).
  La degradación fina con `caps().colors` es S29; el coste de construir la
  cadena es del mismo orden.
- **Coalescing:** `frame()` devuelve el nº de celdas cambiadas; 0 cambios = 0
  bytes emitidos (un frame idéntico no produce salida), realizando "la UI
  repinta por eventos, no a 60 fps" (ADR-007).

### El shim Lua mínimo (qué mide)

`__spike.composer/markdown/fuzzy_window` + métodos `Composer:region/begin/frame`
y `Region:blit/fill`. El "toolkit mínimo en Lua" que el veto evalúa **es el
script Lua del benchmark** que orquesta estas primitivas: por frame hace
`begin → fill/blit → frame` (~3 cruces Go↔Lua). `markdown` reusa
`renderMarkdownBlocks` (S23) y `fuzzy_window` reusa `fuzzyScore` (S27) +
construye el Block de la ventana visible (top N) — "el filtrado es primitiva Go,
Lua repinta lo visible".

### El umbral y la metodología del veto (honestidad)

- **Umbral pre-comprometido:** caso (a) streaming markdown 120×40 ≤ **8 ms/
  frame** (¼ del presupuesto de 30 fps, holgura para HTTP/SSE/parse y hardware
  lento); caso (b) picker 100k ≤ **50 ms/pulsación** (cota de "instantáneo").
- **Criterio de atribución (clave):** la pregunta de ADR-007 no es "¿es rápido
  el render?" sino "¿el *overhead de orquestar desde Lua* rompe la fluidez
  frente a Go?". Por eso el veto solo se ejecuta si un caso se sale del
  presupuesto **Y** la causa es el overhead de Lua (no la primitiva Go, que
  mover el toolkit a Go no arreglaría). Se mide Go-puro vs Lua-orquestado y se
  reporta el delta.
- **`-race` NO decide el veto.** El detector de carreras instrumenta cada acceso
  e infla los tiempos ~7× (verificado: caso (b) p99 pasa de ~52 ms a ~354 ms
  bajo `-race`): válido para CORRECCIÓN, inútil para un veto de RENDIMIENTO. Se
  detecta con build tags (`spike_race_{on,off}_test.go` → `spikeRaceEnabled`) y
  bajo `-race` `TestSpikeMeasureVeto` solo reporta números (veto "indeciso", no
  falla). El veredicto firme es la corrida sin `-race`.
- **Limitación headless declarada:** sin TTY el diff va a un buffer en memoria,
  no a un terminal. Se mide el **coste de cómputo** (compose+diff+encode + cruce
  Lua), no la latencia física del pty —que es idéntica se decida Lua o Go, así
  que no sesga la decisión—. Es justo lo que el veto pone en juego.

### El resultado y la observación del picker

**El veto NO se ejecuta:** el overhead de Lua es despreciable en ambos casos
(caso (a) ±decenas de µs; caso (b) dentro del ruido, Lua a veces más rápido)
porque todo el trabajo pesado es primitiva Go y Lua solo cruza ~3 veces por
frame. Toolkit en Lua (S42); Fase 8 sin reordenar; ADR-007 → Aceptada.

**Observación (no veto, no `G##`):** el p99 del caso (b) (~52–74 ms en Go puro)
roza/supera el presupuesto, pero el outlier es la pulsación de **1 carácter**
(casa ~todos los 100k) y el coste vive en `fuzzyScore` (primitiva Go que recorre
100k), **no** en el cruce a Lua. Si en producción molesta, el arreglo es de
`nu.search.fuzzy` (paralelizar el scoring, o umbral de longitud mínima de query
en el toolkit), no de la arquitectura de UI. Se anota en ADR-012 como nota de
optimización futura.

### Verificación

`spike_bench_test.go`: tests funcionales (`-race`) de viewport/scroll (G28),
recorte horizontal, coalescing + damage tracking, SGR, orquestación Lua; +
`TestSpikeMeasureVeto` (imprime p50/p99 de ambos workloads, Go vs Lua, y el
veredicto) + 3 benchmarks. `CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios;
`CGO_ENABLED=1 go test -race -timeout 120s ./internal/...` verde (no regresiona
S01–S27). **APILevel sigue en 1.** Puntero ▶ avanza a **S29** (compositor real).

## S29 — `nu.ui` compositor real (§9.1)

- **Modelo de composición (una rejilla por región).** El compositor mantiene una rejilla de pantalla (back/front para el diff) y una lista de regiones; cada región tiene **su propia rejilla** de su tamaño lógico. `blit`/`fill`/`clear` escriben en la rejilla de la región (persisten entre frames, como una ventana). Cada pintado compone apilando las regiones por z-order sobre la rejilla de pantalla, recortando cada una al rectángulo visible. Separar contenido (rejilla de región) de presentación (composite) hace G1 y G28 triviales y correctas por construcción.
- **G1 (resize):** región fuera de pantalla no se toca; el composite la recorta; reaparece al crecer la pantalla (coords y rejilla intactas).
- **G28 (blit = copia, nunca re-render):** `blit` copia la ventana visible del Block (offset negativo recorta el inicio, exceso el final); otro offset = otra copia; nunca reconstruye el Block (scroll barato).
- **Coalescing:** los cambios se acumulan y se pintan como mucho cada ~30 ms (sin flush manual); el diff por runs emite solo lo cambiado; un frame idéntico emite 0 bytes. En headless/test se expone una vía interna (no pública) para forzar/inspeccionar el frame compuesto.
- **`size()` headless:** con TTY real se lee del terminal; sin TTY un default inyectable para los tests (el gating de "nu.ui no existe sin TTY", G20, es S32).
- **Spike de S28 eliminado:** `spike_compositor.go`/`spike_shim.go`/`spike_*_test.go` borrados; el modelo se promovió a `compositor.go` de producción. ADR-012 conserva las mediciones del veto.
- **Solo estado principal (ADR-008):** todas las mutaciones bajo el token Lua; sin candado propio. `Region` es `ownedHandle` (reload S13 la destruye).
- **Frontera:** S30 (ciclo de vida de Region), S31 (input), S32 (gating headless G20) NO adelantados. api.md intacto (APILevel 1).
- **Nota de proceso:** el subagente de implementación escribió y verificó el código pero se colgó antes de commitear y del rastro; el orquestador re-verificó (build/vet/gofmt/`go test -race` completos verdes, spike retirado, superficie §9.1 exacta) y completó el rastro + commit + push. Implementación y tests son obra del subagente; el cierre (rastro/commit) lo hizo el orquestador.

## S30 — ciclo de vida de `Region` (move/resize/raise/lower/show/hide/destroy/cursor) (api.md §9.1)

Sesión sin desviaciones de la espec: §9.1 bastó para las ocho firmas, que se
implementaron exactas sobre el `uiRegion`/`compositor` de S29 (no se amplió api.md,
`nu.version.api` sigue en 1; **sin hallazgos `G##`**). Decisiones de modelado
(donde §9.1 deja libertad) y su porqué:

- **raise/lower por reasignación de `z`, no por reordenar una lista.** `raise()`
  pone `z = max(z de las demás vivas)+1`; `lower()`, `min−1`. Alternativa
  descartada: mantener una lista ordenada y mover el elemento al final/principio.
  Se eligió la reasignación porque el criterio de apilado ya vive en un solo sitio
  —`regionLess` ordena por `(z, seq)` (S29)— y así un `composite` o un `blit`
  posteriores lo respetan sin estado adicional ni un segundo invariante que
  mantener. Conserva el orden relativo del resto: solo la región afectada salta al
  tope o al fondo. El `seq` de creación sigue desempatando z iguales (estabilidad).

- **resize conserva el contenido en la esquina superior izquierda.** §9.1 deja
  abierto qué pasa con el contenido al redimensionar; se decidió **conservar la
  intersección** (copiar la esquina (0,0) común al nuevo lienzo; lo que excede se
  descarta, lo nuevo es fondo) en vez de reiniciar el lienzo. Razón: coherencia con
  el modelo "la región es una ventana" de S29 —agrandar una ventana real no borra
  lo que ya mostraba—. `w/h<0` → `EINVAL`, igual que `nu.ui.region`.

- **hide conserva lienzo y coordenadas; show la devuelve tal cual.** `hide` no
  destruye nada: conmuta un flag `visible` que `composite` consulta para saltarse la
  región. Es el simétrico barato de `show`; ambos idempotentes. Una región oculta
  que llevaba el cursor lo **suelta** (no puede tener el cursor algo que no se ve).

- **destroy: `untrack` + `release`, idempotente, métodos posteriores fallan limpio.**
  `destroy` desregistra del registro de handles por dueño (S13, no fuga) y luego
  `release` (descuelga del compositor, suelta el cursor si era suyo, marca `alive=
  false`). Es **idempotente** (segunda llamada no-op). Tras destruir, los demás
  métodos lanzan `EINVAL` "ya destruida" vía `checkRegion` —error de uso accionable,
  no no-op silencioso—. Matiz: `destroy` valida el tipo a mano (no `checkRegion`)
  para que su propia idempotencia no lance sobre la región ya muerta; la asimetría
  es deliberada (una Region muerta es el caso esperado de la idempotencia; pasar algo
  que no es Region es un error de tipo que sí debe lanzar).

- **cursor: propiedad única, "la última gana", soltar en hide/destroy.** El
  compositor lleva la dueña del cursor (`cursorOwner` + coords LOCALES + flag
  `cursorOff`). `Region:cursor(x,y)` reclama el cursor y **desbanca a la dueña
  anterior** (su `cursor()` previo se pierde, como pide §9.1: "solo una región puede
  tenerlo; la última llamada gana"). `cursor(nil)`/`cursor()` lo oculta (la región
  sigue siendo dueña, apagada). `hide`/`destroy`/reload de la dueña sueltan el cursor
  (`dropCursorIf`); destruir/ocultar OTRA región no lo toca. El frame lo emite en
  `paint` (`encodeCursor`): posiciona+muestra (`ESC[y;xH`+`ESC[?25h`, coords de
  pantalla = local + origen, 1-based) o oculta (`ESC[?25l`) si no hay dueña, está
  apagado o **cae fuera de pantalla** (G1: el cursor nunca se posiciona fuera de
  límites). Es **damage-tracked** (`lastCursor`): un frame que no cambia el cursor no
  reemite su secuencia, de modo que un frame totalmente sin cambios sigue emitiendo
  0 bytes y NO rompe el coalescing de S29 (esto se validó re-ejecutando los tests de
  S29 `TestCoalescingSingleFrame`/`TestDiffEmitsOnlyChanged`).

- **Firma de `cursor`.** `cursor(nil)` o `cursor()` ocultan; `cursor(x, y)` exige
  los dos enteros (`L.CheckInt(2)`/`(3)`): la firma de §9.1 es `(x, y | nil)`, no
  `(x)`, así que un solo entero suelto es un error de uso.

- **Solo estado principal (ADR-008), síncrono (no ⏸, no [W]).** Como `blit`/`fill`/
  `clear` de S29: mutan el compositor bajo el token, sin candado propio. Verificado
  con `CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` (verde, sin
  data races, incluida la goroutine viva del painter `TestUIPainterLive`).

## S31 — input (`nu.ui.on_input` / `keymap`) (api.md §9.3)

Sesión 🔒 (pila de input + resolución de secuencias con timeout). Sin hallazgos
`G##`: §9.3 bastó para implementar las dos firmas exactas; la lógica de
secuencias/timeout y el volcado G30 son del core, como pide el contrato. No se amplió
api.md (APILevel sigue en 1). Decisiones:

- **`keymap` como AZÚCAR sobre la misma pila, no un registro global.** §9.3 dice
  "azúcar sobre la pila" y "conflictos: la pila manda". Lo modelé literal: un keymap
  es un `inputHandler` más en la misma pila que `on_input` (con `maps []*seqMap` en
  vez de `raw`). Así el orden de resolución de conflictos es UNO (la pila: el de
  arriba gana), sin una tabla de prioridades aparte. Descartado un registro global
  `seq -> fn`: habría duplicado el criterio de orden y contradicho "la pila manda".

- **Resolución de secuencias = buffer pendiente + generación + `oneShot`.** El estado
  pendiente (`pendingBuf`/`pendingHandler`/`pendingTimeout`) vive en `inputState`
  (estado principal, bajo el token). El timeout es un timer de UN disparo nuevo
  (`oneShot` en `timers.go`): un `time.Timer` en Go puro cuya goroutine, al vencer,
  **toma el token** y ejecuta el callback —el mismo patrón que el painter de S29, que
  ya toma el token para pintar—. No reusé `nu.task.every` (es periódico y su `fn` es
  Lua) ni un `nu.fs` ⏸ (el despacho de input es síncrono, no una task): el callback
  del timeout es Go puro y corre una vez. **El contador de generación (`pendingGen`)**
  es la pieza anti-carrera: un timer que ya disparó pero cuya secuencia se resolvió/
  re-armó antes de que tomara el token comprueba `pendingGen != gen` y no hace nada.
  Así un `stop` best-effort basta (no hace falta esperar a la goroutine).

- **Abortar una secuencia = reinyectar lo bufferizado POR DEBAJO del keymap dueño.**
  §9.3: "si pasa el timeout o llega algo que no continúa, se resuelve lo que haya (o
  pasa el input)". Lo interpreté como: la(s) tecla(s) retenida(s) por el keymap se
  re-despachan como eventos sueltos, pero SOLO por debajo del handler que las retuvo
  (`dispatchFrom(ev, idx-1)`) —si las reinyectara por toda la pila, el propio keymap
  volvería a retener la primera tecla y entraría en bucle—. La tecla actual (la que
  abortó) se procesa después desde el tope, normal.

- **Tests deterministas: `feedInput` + `feedTimeout` (vías internas, no públicas).**
  El entorno es headless (sin TTY): no hay lector de bytes. Construí el pipeline para
  inyectar eventos sintéticos (`feedInput`) y disparar el timeout de forma SÍNCRONA
  (`feedTimeout`), de modo que el test de "entre las dos g pasa el timeout → no
  dispara" no dependa del reloj (no flaky). El timer real solo se ejercita en
  `TestInputSequenceTimerLive` (con `timeout_ms=20` y polling por condición bajo el
  token). **Qué es driver vs lógica probada:** el DRIVER (raw mode + parseo ANSI a
  eventos, lector de TTY) es de S32+ (CP-7 manual); lo 🔒 (pila + secuencias con
  timeout + G30) se blinda aquí con eventos inyectados.

- **G30 (paste de imagen): volcado SÍNCRONO con write directo de Go.** §9.3/G30: un
  paste de contenido no-texto se vuelca a `nu.fs.tmpdir` y se entrega como `paste` con
  `path`, no `text`; los bytes nunca cruzan a Lua (coherente con G11). El evento de
  input llega de forma SÍNCRONA al despacho (bajo el token, NO en una task ⏸), así que
  el volcado no puede ser un `nu.fs.write` ⏸. Lo resolví con `writePasteImage`: reusa
  `fs.ensureTmpdir` (la maquinaria de `nu.fs.tmpdir`) y un `os.WriteFile` directo de
  Go (`paste-N.bin`, 0600). El coste es una escritura de unos KB/MB de una imagen
  pegada, despreciable frente a la latencia humana de pegar. Si el volcado falla, el
  evento se entrega como un paste inerte (sin `text` ni `path`) y queda constancia en
  el log: mejor un paste vacío que perder el invariante de no cruzar bytes binarios.

- **`materializePaste` antes de despachar; `eventTable` elige `path` xor `text`.** La
  conversión bytes→fichero ocurre en `feedInput`, una sola vez, antes de que cualquier
  handler vea el evento. La tabla Lua del evento (`eventTable`) pone `path` si el
  evento lo trae (imagen) o `text` si no (texto), nunca ambos.

- **Solo estado principal (ADR-008).** `inputState` vive en `uiState`, bajo el token;
  el `oneShot` toma el token al disparar. Verificado con `CGO_ENABLED=1 go test -race
  -timeout 120s -count=2 ./internal/...` (verde, sin data races, sin flaky).

## S32 — resto de `nu.ui`: clipboard OSC 52 + eventos `ui:*` + gating headless G20 (api.md §9.2, §9, §4, §2)

### GATING HEADLESS (G20, la decisión central)

§9/G20: sin TTY interactivo (`nu -e`, CI, salida redirigida) el módulo `nu.ui`
**directamente NO EXISTE**, y la detección es `nu.has("ui")` (nunca probar-y-capturar),
el mismo modelo que las caps de los workers ("la superficie no concedida no está").
Lo implementé así:

- **`registerUI` se llama solo si `rt.uiActive`** (en `registerNu`). En headless ni se
  cuelga `nu.ui` del global ni se construye el compositor (`rt.ui = nil` vía
  `maybeUIState`); `armPainter`/`stopPainter`/`Close` ya toleraban `rt.ui == nil`.
- **`uiActive` lo decide `New`:** `WithForceUI(active)` manda (precedencia, `forceUISet`);
  sin ella, `detectTTY()`. Así el binario `nu` (que llama `runtime.New()` sin Options)
  aplica el gating real, y los tests fuerzan la UI.
- **`detectTTY()` exige stdout Y stdin TTY** (`golang.org/x/term.IsTerminal`): una UI a
  pantalla completa escribe el render (stdout) y lee teclas (stdin); si cualquiera está
  redirigida no hay superficie viable. `x/term` es puro-Go (sin CGO, ADR-001).
- **`nu.has` pasa a per-runtime** (`rt.caps()`), no un mapa global: `"ui"` depende del
  runtime concreto (uiActive). `"ui.images"`/`"net.tcp"` siguen false (deny-by-default;
  el protocolo de imágenes lo negociará el driver de S33+).

### Activación forzada para test (NO romper S22–S31)

Los tests corren headless (sin TTY), así que con el gating real `nu.ui` no existiría y
toda la suite de UI de S22–S31 (block/region/input) fallaría. La vía: la Option
**`WithForceUI(true)`**, que el arnés base (`newHarness`) y los harness de UI
(`newHarnessUI`/`newHarnessBudget`) activan. Ajusté también las pocas pruebas que
construyen `New(...)` a mano (`ui_test.go`) para añadirla. Ningún test se borró: el
gating real (por TTY) sigue cubierto por `TestGatingHeadlessNoUI` (que construye el
runtime con `WithForceUI(false)` para observar el comportamiento headless).

### Clipboard OSC 52 (`osc52.go`): driver vs lógica probada

§9.2: `clipboard_set`/`clipboard_get` "vía OSC 52 cuando el terminal lo soporta".

- **`set` NO ⏸**, **`get` ⏸**: `set` escribe unos bytes y el terminal no responde; `get`
  envía la consulta y **espera** la respuesta (de ahí ⏸, sobre el puente `suspend` de
  S04: suelta el token, lee en la goroutine de fondo que jamás toca Lua).
- **Por qué OSC 52 y no un portapapeles nativo:** "cero dependency hell" (ADR-001)
  descarta enlazar X11/Wayland/AppKit; OSC 52 es in-band, funciona por SSH y no añade
  dependencias de sistema. Su límite (el terminal debe soportar la lectura, muchos la
  desactivan) se modela honestamente: `get` devuelve `nil`, no un portapapeles vacío.
- **DRIVER vs lógica probada (como S31):** la salida es `uiState.clipWriter`
  (`os.Stdout` en producción, un buffer en test) y la respuesta llega de
  `uiState.clipReader` (el flujo del TTY que provee el DRIVER de S33+; nil en headless
  → `get` resuelve a `nil`). La lógica propia y arriesgada —codificar `set` (base64) y
  **parsear** la respuesta (`parseOSC52Reply`: terminador BEL/ST, selector ignorado,
  ruido tolerado, base64/vacío/`?`-rebotado→nil)— se blinda por unidad con bytes
  sintéticos (`osc52_test.go`). El ida y vuelta real con un TTY vivo es del driver.
- **`set` no lanza ante un fallo de escritura al TTY:** copiar al portapapeles es
  accesorio; un error va al log best-effort, no tumba al llamante.

### Eventos `ui:*` (`ui_events.go`): emisión cableada, fuente en el driver

§4: el core emite `ui:resize`/`ui:focus`/`ui:suspend`/`ui:resume`; §9.1: cambios de
tamaño → `ui:resize`. La FUENTE real (SIGWINCH, secuencias de foco, SIGTSTP) es el
DRIVER de TTY (S33+, CP-7 manual). S32 cabla la EMISIÓN por `nu.events` y deja las vías:

- **`resizeUI(w,h)`** redimensiona el compositor (recorta regiones, G1) y emite
  `ui:resize {w,h}` —**solo si el tamaño cambió** (no un evento espurio)—.
- **`emitUIFocus(b)`** → `ui:focus {focused}`; **`emitUISuspend`/`emitUIResume`** →
  `ui:suspend`/`ui:resume` (sin payload).
- `ui:` es namespace reservado al core (§4). La emisión presupone el token (estado
  principal, ADR-008): el driver encolará el evento del SO al loop, como el painter
  toma el token para pintar. Todas son no-op si no hay UI (`rt.ui == nil`).

### Dependencia: `golang.org/x/term`

`x/term v0.13.0` (directa, puro-Go). La última (v0.44.0) exige go >= 1.25; la pineé a
v0.13.0 para no bumpear el toolchain del repo (go 1.24.7) y reusar el x/sys v0.13.0 ya
presente. `go mod tidy` coherente; `CGO_ENABLED=0 go build ./...` verde.

### Sin ampliar la API

NO se tocó `api.md` ni `nu.version.api` (APILevel sigue en 1): las firmas
`clipboard_set`/`clipboard_get`, los eventos `ui:*` y el gating ya estaban
especificados. `nu.has` per-runtime es implementación, no cambia la firma `nu.has(cap)
-> boolean`. Sin hallazgos `G##`. `CGO_ENABLED=1 go test -race -timeout 120s -count=2
./internal/...` verde, sin data races.

## S33 — pantalla de runtime desnudo (api.md §14, G21; cierra Fase 6, CP-7 manual)

Cuando nu arranca con un TTY interactivo y NINGÚN plugin activo, el kernel pinta —ANTES
de correr Lua de producto— una pantalla FIJA hecha solo de sus capacidades: versión +
nivel de API (`nu.version`), rutas de config y de plugins (`config.dir`, `pluginDirs`),
catálogo de extensiones embebidas DISPONIBLES (`embeddedNames`, embed.go) y las acciones
(activar el conjunto oficial / activar sueltas / salir). Render FIJO (Block sobre el
compositor de S29), pre-Lua, sin widgets ni lógica de producto (filosofía §2: el kernel
habla de lo suyo). NO amplía `api.md` (G21 ya estaba en §14; APILevel sigue en 1); ni
una función de superficie Lua nueva. Sin hallazgos `G##`. Todo en `bare_screen.go` +
`bare_screen_test.go`; cableado mínimo en `main.go`.

### Condición y dónde se cablea

La pantalla se muestra SSI `rt.uiActive` (TTY interactivo, o `WithForceUI` en test) Y no
hay plugins activos (`loader.hasActivePlugins`: `len(enabled) > 0` o algún subdir con
`plugin.toml` en los dirs de plugins —comprobación LIGERA, sin materializar embebidas ni
validar el grafo, que es trabajo del `Boot` real—). Decidí cablearlo en **`main`**, no
dentro de `Boot`: `main` (sin `-e`) consulta `rt.BareScreenActive()` y, si procede,
pinta y vuelca las líneas; si no, sigue el arranque canónico de siempre. Así NINGÚN test
de S01–S32 que llama `rt.Boot()` directamente cambia de comportamiento (Boot sigue
cargando plugins + init del usuario + `core:ready`): la pantalla es una decisión del
binario, que es donde vive el TTY. Sin TTY (`nu` sin `-e` en CI) → `BareScreenActive`
es false → se imprime el uso (arranca desnudo), confirmado con el binario.

### Acciones: activar → escribir nu.toml → continuar Boot (sin red)

`activateAndBoot(names)` escribe `names` en `plugins.enabled` de `config.dir()/nu.toml`,
relee la config en el loader, resetea `booted` y llama a **`rt.Boot()`** (no `ldr.Boot()`,
para armar también el painter del compositor: tras activar, la UI de las extensiones debe
repintarse). `ActivateOfficial()` = `activateAndBoot(embeddedNames())`; activar suelta =
`activateAndBoot([]string{"repl"})`. Sin red: las embebidas salen del binario (ADR-010,
reusa `extractEmbedded`/`discover` de S12). La elección con el TECLADO la cablea el driver
de TTY (S33+/CP-7 manual); la lógica queda invocable por una vía interna testeable.

### Escritura de nu.toml: preservar el resto del fichero, atómica

`writeEnabledPlugins` lee el `nu.toml` existente a un **`map[string]any`** genérico (NO a
`runtimeConfig`, que perdería las claves que el core ignora por forward-compat), fija
`plugins.enabled` conservando el resto de `[plugins]` y de las demás secciones/claves
desconocidas, y reescribe TODO con BurntSushi de forma **atómica** reusando `writeAtomic`
de S14 (temporal en el mismo dir + `rename`: no deja un `nu.toml` a medias). Un `nu.toml`
MAL FORMADO **no se sobrescribe a ciegas** (perdería config del usuario): devuelve `EINVAL`
accionable y deja el fichero intacto. Un fichero ausente se crea (primer arranque); el
`config.dir` se crea si falta.

### CP-7 (cierra Fase 6) — MANUAL con TTY, NO ejecutable en CI headless

CP-7 es una prueba de humo **manual con TTY** (arrancar sin plugins → ver la pantalla;
activar el conjunto oficial; un plugin pinta markdown en streaming y responde a un keymap;
redimensionar; pegar imagen → path). En este entorno **HEADLESS no hay TTY**, así que NO
se pudo ejecutar la parte interactiva (limitación del entorno). Lo que SÍ queda cubierto
por tests automáticos (`bare_screen_test.go`):

- **Condición** TTY+sin-plugins (con UI y sin plugins → activa; sin UI → no; con embebida
  activada → no; con plugin de disco → no).
- **Contenido / render FIJO a buffer**: el modelo y la **rejilla del compositor** (`back`)
  contienen versión+API, rutas (config y dir de plugins), el catálogo de embebidas
  (`example`) y las acciones; el frame ANSI emitido no es vacío.
- **Activar conjunto oficial → nu.toml → Boot**: escribe `plugins.enabled` con el catálogo,
  y el Boot que continúa carga la embebida con `source="builtin"` y corre su init (sin red).
- **Activar suelta** (`example`): escribe solo esa.
- **Preservar config**: escribir `enabled` conserva `dirs`, `watchdog` y claves/secciones
  ajenas; el resultado es un `nu.toml` válido.
- **nu.toml mal formado** no se sobrescribe (EINVAL, fichero intacto).
- **No regresión**: `nu -e` headless sigue funcionando; `nu` sin `-e` sin TTY imprime el uso.

QUEDA PENDIENTE de un humano con TTY (CP-7 manual): la interacción de TECLADO para elegir
una acción, el streaming visible token a token, y el resize/paste VISIBLES. El render de la
pantalla, la condición y la cadena activar→nu.toml→Boot están automatizadas; el tablero
marca `[x] Fase 6` con esta nota.

`CGO_ENABLED=0 go build ./...` y `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde, sin data races
(no regresiona S01–S32).

## S34 — `nu.worker.spawn` + caps (G6) + send/recv con colas acotadas (api.md §13, 🔒; abre Fase 7)

Primera sesión de la Fase 7 (Workers). Implementa el **paralelismo opt-in** (ADR-008):
`nu.worker.spawn` levanta un estado Lua NUEVO y aislado en su propia goroutine, con su
propio scheduler, comunicado con el padre por colas acotadas de mensajes JSON-ables
copiados. El filtrado de `caps` (G6) y el backpressure de las colas son la lógica 🔒.

### El mini-runtime del worker (G15): reuso del scheduler SIN watchdog

Un worker es un `*Runtime` "recortado" (`newWorkerRuntime`, worker_registry.go): mismo
motor que el principal —estado Lua sandboxeado (`applySandbox`) + scheduler propio
(`newScheduler`)— pero con `isWorker=true` y, sobre todo, **presupuesto de slice 0** →
`armWatchdog` es no-op (G15: los workers existen para quemar CPU; el control es
`terminate()` y las `caps`, no el watchdog). Decisión clave: **no se reimplementa nada del
event loop**; el worker REUSA la maquinaria de S04 (token/`suspend`/`runTask`/`waitIdle`).
El módulo del worker corre **como una task** (no como chunk sobre `host`): un chunk no
podría suspender (`requireTask` exige `L != rt.L`), y el patrón natural del worker es un
bucle de `nu.worker.parent.recv()` (⏸). Por eso `run` hace `s.spawn(require(module))` y
luego `waitIdle`. Un worker es headless siempre (`uiActive=false`, `ui=nil`): nada de
`nu.ui`.

### El filtrado de caps (G6): deny-by-default, dos granularidades

`registerWorkerNu` registra TODA la superficie [W] en el `nu` del worker reusando las
mismas `registerXxx` del principal (un solo punto de verdad), y DESPUÉS **poda el árbol**
por `caps` (`pruneByCaps`). Registrar-y-podar es más simple y robusto que registrar función
a función. Tres granularidades de decisión por módulo `M`:
- `caps["M"]` (p. ej. `"fs"`) → módulo entero conservado.
- algún `caps["M.fn"]` (p. ej. `"fs.read"`) → solo esas funciones; si ninguna existe, M se
  elimina entero.
- ni una ni otra → M eliminado (deny-by-default).

Sin `caps` (`capsGiven=false`) → toda la API [W]. Con `caps={}` (vacío) → casi nada. Lo no
concedido **NO EXISTE** (es `nil`), el mismo modelo que el gating de `nu.ui` (G20): no se
"lanza EACCES", la superficie simplemente no está, así que un plugin no puede ni nombrarla.
**Deny-by-default para superficie nueva**: una función añadida luego a un módulo NO queda
concedida por una `caps` antigua de granularidad de función (solo lo enumerado sobrevive);
`"M"` entero sí concede lo futuro de M, por diseño. NO son [W] (nunca llegan al worker):
`nu.ui`, `nu.events`, `nu.fs.watch`, `nu.worker.spawn` (sin anidar) ni `nu.plugin` (§16).
`nu.version`/`nu.has` y `nu.worker.parent` van SIEMPRE (no son superficie recortable: la
detección de capacidades y el canal con el padre). `nu.config.dir`/`data_dir` son [W] (§14).

### Las colas acotadas / backpressure (§13)

Dos canales Go acotados (`workerQueueCap=16`) por worker: `toWorker` (padre→worker) y
`fromWorker` (worker→padre), más un `done` que se cierra al terminar. `Worker:send` (padre)
y `nu.worker.parent.send` (worker) son ⏸: encolan SUSPENDIENDO si la cola está llena
(backpressure, §13/§8) —el envío real ocurre FUERA del token, en la goroutine de fondo del
puente `suspend`, así otra task del MISMO estado progresa mientras el send espera hueco—.
A diferencia de los streams de §8 (que fallan con `EIO` al desbordar), el send del worker
**suspende** (la cola es un punto de rendez-vous con ritmo, no un buffer que rebosa). `recv`
suspende hasta que llega un mensaje. Una punta cerrada (`done`): `send` → `ECLOSED`; `recv`
→ `nil` (fin de canal, coherente con `Ws:recv`), drenando primero lo que quedara encolado.

### La copia de mensajes JSON-ables (aislamiento ADR-008)

NADA de Lua cruza entre estados. `Worker:send` convierte el valor Lua a su representación Go
neutra con `luaToGo` (el codec de §12/S18) BAJO EL TOKEN DEL EMISOR, ANTES de suspender —lo
que valida que sea JSON-able y rechaza closures/userdata/threads/Blocks con `EINVAL`—; el
valor Go neutro (no un LValue) es lo único que cruza el canal entre goroutines; el receptor
lo reconstruye con `goToLua` bajo SU token. Así una tabla se COPIA (mutarla tras enviarla no
afecta al otro lado) y los dos estados Lua nunca comparten memoria. `useNull=false`: los
mensajes son valores JSON-ables corrientes, no documentos JSON (el sentinel `nu.json.NULL`
es userdata por-estado y no podría cruzar de todas formas). De aquí sale "cero data races"
con DOS schedulers: cada `*lua.LState` solo lo toca su goroutine bajo su token; el cruce es
copia + happens-before por canal.

### `terminate` inmediato y sin fuga (arreglo del review de S34)

`Worker:terminate()` debe ser **inmediato y seguro** (§13). El primer corte de S34 solo
cerraba `done`, que únicamente observan `send`/`recv` en las colas: una task del worker
suspendida en `nu.task.sleep`/`http`/`proc`/`await`/... NO se despertaba, así que
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

### Sin ampliar la API ni hallazgos

`§13` bastó EXACTA: ni una función pública de más; `APILevel` sigue en 1 (§13 ya estaba en
api.md). Sin hallazgos `G##`: la división Go/Lua y el puente `suspend` de S04 dieron todo.

### Tests 🔒 (worker_test.go), nombrando G6/G15

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
  suspendido en `nu.task.sleep(60000)` es cortado al acto por `terminate()` —`terminate`+`Close`
  completan MUY por debajo del sleep— y `runtime.NumGoroutine()` tras `Close` vuelve al nivel
  previo al spawn (la goroutine del worker terminó, no quedó colgada tocando el `data_dir`/`log`).
  (`TestWorkerTerminateInterruptsCPULoop`): un worker en bucle de CPU pura (`while true do end`,
  sin punto de suspensión) también se corta —cancelación del `context`—, sin colgar `terminate`+`Close`.

`CGO_ENABLED=0 go build ./...`, `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=4 ./internal/...` y `-count=8 -run Worker`
verdes, **sin data races, sin flaky ni fallos de cleanup ("directory not empty")** (es el test
de races más exigente hasta ahora: DOS goroutines de scheduler en paralelo). No regresiona
S01–S33. Puntero ▶ sigue en **S35**.

## S35 — `Worker:on_message` (excluyente con `recv`, G8) + tasks/timers/futures dentro del worker + `terminate` (api.md §13, 🔒; cierra Fase 7 — CP-8)

S35 cierra la Fase 7. La feature de superficie es `Worker:on_message`; el resto del
alcance (tasks/timers/futures dentro del worker, `terminate` robusto) lo dejó S34 ya
implementado y S35 lo BLINDA por test. Ni una función pública de más: §13 ya estaba en
`api.md`, `APILevel` sigue en 1. Sin hallazgos `G##`.

### Modelo de `on_message`: drenador de fondo + entrega en el estado principal

`Worker:on_message(fn) -> Sub` es la ALTERNATIVA por CALLBACK en el ESTADO PRINCIPAL a
`Worker:recv`. La pregunta de diseño era "cómo se drena la cola worker→padre y se llama
`fn(msg)` en el estado principal sin fugas". La respuesta, coherente con el modelo y sin
pieza nueva, espeja el ticker de `nu.task.every` (timers.go):

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
el `Sub` de `nu.events` (lleva `*subscriber`): mismo método público `:cancel()`, distinto
tipo. Se eligió un tipo nuevo en vez de reusar el de eventos porque `subCancel` valida el
tipo concreto del userdata (`*subscriber`) y mezclar tipos sería frágil.

### Exclusividad G8 (lo 🔒): rechazo explícito EN EL ACTO, nunca prioridad silenciosa

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

### tasks/timers/futures dentro del worker (G15) y `terminate`: blindados, nada que arreglar

El mini-runtime del worker (scheduler propio de S34, reuso de S04) ya soporta todo
`nu.task` [W]. S35 lo BLINDA por test (`TestWorkerInternalTasksTimersFutures`): un worker
corre varias tasks (`spawn`/`await`), un `future` (`set`/`await`), `sleep` y un `every`
periódico, SIN watchdog. No hizo falta tocar el scheduler del worker. `terminate` quedó
inmediato y seguro en S34 (`cancelAllTasks` + `Close` espera la goroutine);
`TestWorkerTerminateDoesNotAffectParent` confirma idempotencia y que el padre sigue.

### CP-8 (cierra la Fase 7)

`TestCP8WorkerIndexesRepo`: un worker con `caps={"fs.read","search"}` indexa un repo de
prueba (recorre con `nu.search.files`, lee con `nu.fs.read`) y devuelve un digesto al
principal vía `send`/`recv`; DENTRO del worker `nu.fs.write` y `nu.ui` NO existen
(deny-by-default, G6, comprobado con `assert` desde el propio worker); un segundo worker
`terminate`-ado a mitad no afecta al padre. El backpressure de `send` al llenar la cola
acotada lo cubre `TestWorkerBackpressure` (S34), coherente con CP-5; no se duplica.

### Resultado

`CGO_ENABLED=0 go build ./...` y `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde, y
`-race -count=5 -run Worker`/`-run 'CP8|OnMessage'` verdes, **sin data races ni flaky**
(la entrega de `on_message` es timing-sensible —drenador de fondo + token— y aguantó el
sondeo bajo `-race`). No regresiona S01–S34. Cierra la Fase 7 (Workers); arranca la Fase
8. Puntero ▶ avanza a **S36**.

## S36 — extensión oficial `providers` (registro TOML + contrato del adaptador + `approx_tokens`) (providers.md)

Primera extensión de la **Fase 8**: ya no se toca el kernel, se escribe **Lua sobre la
API pública congelada** (ADR-003, sin privilegio de kernel; el core no sabe lo que es un
provider). El contrato es [providers.md](docs/providers.md), no api.md.

### Estructura de la extensión (plugin embebido)

Plugin embebido bajo `internal/runtime/embedded/providers/`, materializado y cargado por
el loader como cualquier plugin (mecanismo de S12, ADR-010: INACTIVO por defecto):

- `plugin.toml` — `name = "providers"`, `version = "0.1.0"`. Sin `requires` (no depende de
  otro plugin; depende de primitivas del core, no de extensiones).
- `init.lua` — solo CABLEA: `require("providers")` y registra los adaptadores oficiales.
  En S36 solo el `stub`; el `anthropic` real (S37) se añadirá aquí igual:
  `providers.register_adapter("anthropic", ...)`.
- `lua/providers/init.lua` — el módulo público (`require("providers")`): lector del
  registro, `resolve`/`list`/`register_adapter`/`approx_tokens`/`reload`.
- `lua/providers/adapter_stub.lua` — adaptador STUB que materializa y prueba el contrato §3
  contra una petición simulada (sin red).

**Por qué módulo `require`-able y no namespace `nu.providers`:** el core reserva el
namespace `nu`; las extensiones exponen su API por `require(<nombre-plugin>)` (api.md §14,
convención namespace = nombre del plugin). El agente y la UI consumirán
`require("providers")`. El namespace de eventos de la extensión sería `providers:` (no se
emiten eventos en S36, pero queda reservado por convención).

### Decisiones de interpretación de providers.md

1. **`providers.toml` se lee perezosamente y por eso `resolve`/`list` SUSPENDEN (⏸).**
   providers.md §1 dice "vive en `nu.config.dir()`" pero no fija *cuándo* se lee. Se lee con
   `nu.fs.read` (⏸, api.md §5) la primera vez que alguien resuelve o lista, y se cachea;
   `reload()` invalida la caché. Consecuencia heredada de la API: como `nu.fs.read`
   suspende, `resolve`/`list` solo corren dentro de una task — que es exactamente el
   contexto del loop del agente. Es coherente con el resto de la API (IO = ⏸) y no requiere
   primitiva nueva.

2. **`providers.toml` ausente = registro vacío, no error.** Un nu recién instalado sin
   modelos configurados debe arrancar limpio; `list()` devuelve `[]`. Se distingue `ENOENT`
   (ausente → vacío) de un fallo de IO real (`EACCES`/`EIO`, se propaga) por el `code` del
   error estructurado de `nu.fs.read`.

3. **Errores del registro = `EPROVIDER` accionable** (providers.md §3 acuña `EPROVIDER`;
   CLAUDE.md/api.md §1.4: las extensiones acuñan los suyos con la misma forma). TOML mal
   formado, provider sin `adapter`, modelo sin `id`, modelo/ref inexistente, adaptador no
   registrado → `EPROVIDER` con `detail` y mensaje que nombra el provider/ref. La validación
   de *argumentos* del propio API (`approx_tokens(123)`, `resolve("")`) usa `EINVAL` (es un
   error del llamante, no del provider).

4. **`api_key` SIEMPRE del entorno** (providers.md §1: "nunca la clave en el fichero"). Se
   lee con `nu.sys.env(prov.api_key_env)`. Si el provider no declara `api_key_env` (p. ej.
   Ollama local), la `config` va sin `api_key` y no es error: el adaptador decide si la
   necesita.

5. **Resolución de adaptador por nombre con `require`** (providers.md §1/§4: el TOML puede
   declarar `adapter = "mi-plugin/corp-gateway"`). `get_adapter` mira primero el registro
   vivo (oficiales + `register_adapter`) y, si no está, intenta `require(name)` (resoluble
   contra las rutas `lua/` de los plugins, api.md §14) y valida su forma. Cachea el
   resultado. `register_adapter` con un nombre ya registrado SUSTITUYE (un plugin puede
   pisar un adaptador oficial a propósito); no es error.

6. **`approx_tokens` cuenta BYTES, `ceil(#s/4)`** (providers.md §4, G23). `#s` en Lua es
   longitud en bytes, que es lo que mejor aproxima la tokenización BPE sobre texto mixto y
   lo que hacía el core. `ceil` (aritmética entera `floor((n+3)/4)`) para no infraestimar;
   cadena vacía = 0. Es heurística, no exactitud — para eso está el `count_tokens?` del
   adaptador.

7. **El STUB declara `caps.tools = false` a propósito** para poder ejercitar la
   "degradación declarada" (§3 obligación 5: request con tools + adaptador sin soporte →
   `EINVAL`, no simulación silenciosa). Su `stream` devuelve un iterador Lua (función que da
   un `Event` por llamada y `nil` al agotarse) — el mismo protocolo que `Stream:events()`
   (api.md §8) que el adaptador real de S37 envolverá. Emite `text`,`text`,`usage`,`done`,
   con el `done` cargando el `Message` canónico ensamblado (§2.3): el agente no re-ensambla
   deltas.

### Hallazgo (corolario de completitud) — RESUELTO sin tocar api.md

`nu.toml.decode` **estringaba los array-de-tablas** (`[[providers.x.models]]`), el formato
CENTRAL de `providers.toml`. Causa: BurntSushi/toml, al decodificar un array-de-tablas en
`map[string]interface{}`, entrega el tipo concreto `[]map[string]interface{}` (no el
`[]interface{}` "abierto"); el puente `goToLua` (codecs.go, S18) solo contemplaba
`[]interface{}` y `map[string]interface{}` y caía al `default` de stringificación
(`models` salía como el string `"[map[id:big-1]]"`). Sin esto la extensión era
**inconstruible** sobre la API pública (filosofía §2).

**No es un hueco de api.md**: la firma documentada `nu.toml.decode -> v` ya prometía
convertir el documento a una tabla Lua —incluidos sus array-de-tablas—; era un bug de la
*implementación* del codec, no de la espec. El arreglo es por tanto MÍNIMO y en el codec,
no en api.md (que queda INTACTO): un fallback por reflexión en `goToLua` para slices y maps
de cualquier tipo concreto (cubre también `[]string`, `map[string]string`, etc., robusto
ante cualquier librería), con claves ordenadas para determinismo. Blindado por
`TestTOMLDecodeArrayDeTablas` (codecs_test.go) nombrando el caso. Es la única línea de Go
del kernel tocada, justificada por "lo mínimo imprescindible para que la extensión
funcione".

### Resultado

`CGO_ENABLED=0 go build ./...` y `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde. Tests en
`providers_test.go` (registro TOML, stub contra petición simulada, approx_tokens) +
`TestTOMLDecodeArrayDeTablas`. No regresiona S01–S35. Puntero ▶ avanza a **S37** (adaptador
`anthropic` real, depende de S20 y S36).

---

## S37 — Adaptador Anthropic (primer dialecto real); CP-9

Implementé el adaptador `anthropic` (providers.md §3) como módulo
`internal/runtime/embedded/providers/lua/providers/adapter_anthropic.lua`, registrado desde
el `init.lua` de providers con `register_adapter("anthropic", ...)`. El alcance, los DoD y
el contrato (providers.md §2/§3) no dejaban casi margen; estas son las decisiones de
interpretación que tomé donde el contrato no era literal.

### Cómo traduzco el dialecto Anthropic al canónico

**Request canónico → Messages API.** `to_wire` mapea bloque a bloque: `text`→text,
`image`→`image {source: base64}`, `thinking`→thinking, `tool_call {id,name,args}`→`tool_use
{id,name,input}`, `tool_result {id,content,is_error?}`→`tool_result {tool_use_id,...}`.
`system` va al campo `system` de Anthropic; `tools {name,description,schema}` a
`{name,description,input_schema}`; `thinking {budget}`→`{type="enabled",budget_tokens}`.
`max_tokens` es OBLIGATORIO en Anthropic: si el canónico no lo trae, cae al `max_output`
del ModelInfo resuelto y, en último término, a 4096. La clave va en `x-api-key` (NO
`Authorization: Bearer`) más `anthropic-version: 2023-06-01`.

**Round-trip de `meta` (§2.2 regla meta / §3 obl. 4).** El `meta` opaco de cada bloque se
funde con `pairs` en el bloque del wire. Para `thinking`, la `signature` viaja en `meta`
(la pone el adaptador al ensamblar el bloque desde `signature_delta`) y se reinyecta tal
cual en turnos siguientes; para otros, cubre `cache_control`/ids internos sin contaminar el
modelo canónico.

**SSE → stream canónico (§2.3).** Una **máquina de estados por ÍNDICE de bloque** sobre
`Stream:events()` (S20). El caso que obliga a estado es el **input de tool_use**: llega
troceado en `input_json_delta.partial_json`; lo acumulo como texto y lo decodifico con
`nu.json.decode` AL CERRAR el bloque (`content_block_stop`/`message_stop`). Un JSON de args
mal formado no aborta el stream: el bloque canónico queda con `args = {}` (el agente lo ve;
el adaptador no inventa, §3 obl. 3). `stop_reason` mapeado: `tool_use`→"tool_calls",
`max_tokens`→"max_tokens", `refusal`→"refusal", el resto→"end". `ping` se ignora
(keep-alive). El iterador devuelve un Event por llamada y CIERRA con `done {stop_reason,
message}` con el Message ensamblado (§2.1): el agente no re-ensambla deltas.

**Decisiones puntuales:**
- **Doble `usage`.** Emito un `usage` temprano en `message_start` (input_tokens/cache_read,
  útil para el llenado de contexto de la UI) y otro final en `message_delta` (output_tokens).
  Ambos son válidos por §2.3 (el evento `usage` no es único); la secuencia de tipos lo
  refleja.
- **Errores: dos vías a EPROVIDER (§3 obl. 2).** Status HTTP ≥ 400 (dato, api.md §8 no
  lanza) → `EPROVIDER` con `detail.status`/`provider_code`/`retryable`, leyendo el cuerpo
  JSON de error con `chunks()` (en error HTTP Anthropic manda JSON, no SSE); 429 y 5xx
  retryables. Evento `error` en mitad del SSE → `EPROVIDER` con `provider_code` y
  `retryable` (overloaded_error/api_error retryables). Marcar `retryable` es la única
  inteligencia de fallos que pide el contrato; el reintento es del loop del agente.
- **`count_tokens?`.** Anthropic tiene endpoint exacto (`/v1/messages/count_tokens`), pero
  uso la heurística `approx_tokens` de S36 sobre system + bloques de texto/thinking: sin
  red, suficiente para la estimación PREVIA (providers.md §5: la fuente de verdad del
  llenado es el `usage` del propio turno). El endpoint exacto queda como mejora futura.

### CP-9 (camino caliente, hito de veto de perf)

Como no hay red, GRABÉ un SSE de Anthropic realista (`recordedSSE` en
`providers_anthropic_test.go`: message_start con usage, ping, thinking con signature, texto
markdown en 3 deltas, tool_use con input troceado en dos `input_json_delta`, message_delta
con usage/stop, message_stop) y lo sirvo desde un `httptest` local con flush por línea
(patrón de los tests de S20). `TestCP9CaminoCaliente` corre el camino caliente COMPLETO:
una vuelta de conversación → el adaptador consume el SSE vía `nu.http.stream` → emite el
stream canónico → por cada delta de texto recompongo el markdown acumulado con
`nu.text.markdown` (streaming-safe, S23) y lo blitteo a una región (`Region:blit`, S29).

**Decisión de verificación del render:** el Block es OPACO (api.md §9.2: solo `.width`/
`.height`, no su contenido). Para confirmar que el render final corresponde al markdown
COMPLETO sin acceder a su interior, comparo su altura con un render fresco del texto entero
(coinciden si el streaming acumuló bien) y compruebo que es multilínea (encabezado +
cuerpo). El contenido textual ya se valida aparte (el Message ensamblado del `done`).

**Necesidad de `WithForceUI(true)`:** `bootWithToml` no fuerza la UI, así que en el entorno
headless de test `nu.ui` no existe (gating G20). Para ejercitar el blit del camino caliente,
`bootAnthropic` arranca el runtime con `WithForceUI(true)` (mismo recurso que `newHarness`,
S32); el gating REAL por TTY sigue aplicando al binario.

**Fluidez observada (medición):** todo el trabajo pesado del camino caliente —parseo SSE,
decode JSON, render markdown, blit— son primitivas Go; Lua solo orquesta el bucle de deltas
(reuso del aprendizaje S28/ADR-012). La suite `Anthropic|CP9` completa en ~0.06 s y
`-race -count=2 ./internal/...` entera en ~50 s sin data races. El camino caliente en Lua es
**aceptable**: no hay CPU ardiendo en Lua, el veto de perf (limitación nº8 de
modelo-ejecucion.md) no se dispara.

### Hallazgo

Ninguno. La API pública bastó exacta (`nu.http.stream`+`Stream:events()` §8, `nu.json` §12,
`nu.text.markdown` §10, `nu.ui.region`/`Region:blit` §9). api.md INTACTO; APILevel sigue en
1. Puntero ▶ avanza a **S38** (extensión sesiones; depende de S14, S16).

## S38 — Extensión sesiones (JSONL, lockfiles) + nu.sys.pid (G32, APILevel 1→2)

**Hallazgo G32 (corolario de completitud).** El lockfile de `sesiones.md §6` graba la
identidad del escritor `{pid, hostname, started}` con el pid del proceso PROPIO, pero la
API pública no lo exponía: `nu.sys` daba platform/env/setenv/now_ms/mono_ms/hostname y
`nu.proc.alive(pid)` valida pids AJENOS, no el propio. Es el cabo suelto de G17 (que cerró
`fs.write{exclusive}`, `proc.alive` y `sys.hostname` pero olvidó el pid propio). La extensión
oficial `sessions` era inconstruible sin esto → hallazgo, no atajo.

**Resolución (flujo de diseño: docs primero, luego código).** Adición pura a la superficie
sagrada: `nu.sys.pid() -> integer` [W] (no ⏸; consulta local sin IO, como `hostname`/`now_ms`),
wrapper de `os.Getpid()`. Por ser la PRIMERA adición tras el congelado, `nu.version.api` sube
de 1 a 2 (api.md §17/§2; `APILevel` en `nu.go`). G32 RESUELTO en `problemas.md`; api.md §7
y §16 actualizados; `sesiones.md §6` usa `nu.sys.pid()`. Es una adición estricta: no cambia
ninguna firma existente (ADR-003).

**Decisiones de la extensión `sessions`.**
- **Id de sesión**: timestamp ms (hex de ancho fijo, ordena lexicográficamente = temporal)
  + sufijo aleatorio. El PRNG se siembra UNA vez con `now_ms` + `pid` (sin semilla, gopher-lua
  daría la misma secuencia entre arranques → dos procesos en el mismo ms colisionarían en el
  sufijo; el pid los separa).
- **Lockfile** (§6, G5): `<sesión>.jsonl.lock` con `fs.write{exclusive}`; contenido
  `{pid=nu.sys.pid(), hostname, started}`. Conflicto resuelto por inspección: mismo hostname +
  pid muerto (`proc.alive`=false) → huérfano, reclamado en silencio; pid vivo → ESESSION busy;
  otro hostname → ESESSION foreign (no verificable a distancia). Liberado por `nu.task.cleanup`.
- **read_only** no toma lock (varios lectores concurrentes). Código de error `ESESSION` (forma
  ADR-009, acuñado por la extensión).
- **replay** descarta la última línea si está truncada (crash a mitad de append): JSONL es
  append-only y `fs.append` escribe una línea completa, así que solo la última puede partirse.

**Nota de proceso.** El subagente de implementación dejó el código y los docs escritos y la
suite verde, pero se detuvo antes del `git commit`. Se verificó `go build`/`go vet`/`gofmt` y
`go test -race -timeout 120s -count=2 ./internal/...` (todo verde, APILevel 2) y se commiteó/
pusheó tras completar la fila de bitácora de S38, esta entrada y la semilla del PRNG.

## S39 — Extensión oficial `agent` (motor headless: turno, tools, permisos, hooks, eventos `agent:*`); CP-10 (agente.md)

Cuarto eslabón de la Fase 8: el **motor headless** del harness, Lua puro sobre la API pública
congelada (ADR-003) y sobre las extensiones `providers` (S36/S37) y `sessions` (S38). Plugin
embebido `internal/runtime/embedded/agent/` (`plugin.toml` name="agent", `requires=["providers",
"sessions"]` —el loader §14 los ordena topológicamente antes—; `init.lua` que cablea + módulo
`lua/agent/init.lua` + `lua/agent/tools_fs.lua`). INACTIVO por defecto (ADR-010), activable por
`nu.toml` `plugins.enabled=["providers","sessions","agent"]` (las tres explícitas: `requires`
solo ordena, NO auto-descubre/activa — el loader exige que la dependencia esté en el conjunto
descubierto, que para embebidas es lo nombrado en `enabled`).

**NO amplía api.md** (corolario de completitud satisfecho): `nu.events` (§4), `nu.task.future`/
`spawn` (§3), `nu.has("ui")` (§9, G20), `nu.fs`/`nu.toml`/`nu.config.dir` y los módulos
`providers`/`sessions` bastaron exactos. APILevel sigue en 2. **Sin hallazgos `G##`.** Código de
error de la extensión: `EAGENT` (forma ADR-009).

**El TURNO (`Session:send`, agente.md §2), el corazón:** anexa el mensaje de usuario al historial
(y al transcript si persiste), `resolve` el modelo (providers), y entra en un bucle: ensambla el
request canónico (§7: system por piezas base+`opts.system`; messages = historial; tools =
ToolDefs registradas) → hooks `request.pre` (pueden mutar/vetar) → `adapter.stream(req, config)`
→ **consume el iterador de Events** (providers.md §2.3: `for ev in iter`), re-emitiendo cada delta
en el bus como `agent:delta` y guardando el `done`. El agente NO re-ensambla deltas: usa el
`Message` completo del `done` (§2.3). Persiste el mensaje del assistant con `usage`/`model`
(sesiones.md §3). Si `stop_reason == "tool_calls"`: ejecuta cada `tool_call` del mensaje EN ORDEN
(P12: paralelo pospuesto), anexa los `tool_result` como un mensaje rol `user` (providers.md §2.2)
y **vuelve a pedir**. Termina cuando el modelo para sin tools, o al agotar `max_turns` (EAGENT
accionable, protección de loops, §10).

**Registro de TOOLS (`agent.tool`, agente.md §3):** `{name, description, schema, handler, permissions?}`.
UN único registro de proceso (§9). `M.tools()` enumera las ToolDef. `run_tool` ejecuta una tool
call: permisos → `tool.pre` → handler (bajo pcall) → `tool.post` → `tool_result`. Cualquier fallo
(permiso denegado, handler que lanza, veto de hook, tool desconocida) NO rompe el loop: produce un
`tool_result` con `is_error=true` y texto accionable que el modelo VE (§3). El handler recibe
`ctx = {session, cwd, progress(text), ask(question)}`. Tools de fichero básicas (dogfooding §3):
`read_file` (default="allow", nunca pide permiso ni headless, §5 amortiguador 1) y `write_file`
(default "ask" → DENY en headless: es la que CP-10 deniega).

**PERMISOS (agente.md §5), pipeline por tool call:** (1) default="allow" concede directo; (2)
`deny` de la política corta; (3) `allow` concede; (4) hooks `permission` (deny / `{grant=true}`);
(5) nadie decidió → si tool default="deny", denegado; si `mode="auto"`, concedido (explícito y
ruidoso, amortiguador 3); si `mode="ask"` Y `nu.has("ui")` → emite `agent:permission.asked` y
ESPERA un `future` sin timeout (G3), respondible con `agent.permission.respond(id, granted)`; si
`mode="ask"` SIN UI (HEADLESS, G20) → **DEFAULT DENY** con error ACCIONABLE (amortiguador 2:
nombra la tool, el patrón `allow` a añadir, y menciona `--auto-permissions`). Patrones
`tool[:argumento]` con comodín `*` (glob → patrón Lua); `arg_text` heurístico
(command/cmd/path/file).

**HOOKS-MIDDLEWARE (`agent.hook`, agente.md §4): registro PROPIO, NO el bus.** Puntos v1:
`request.pre`/`tool.pre`/`tool.post`/`permission`/`compact`. `fn(payload, ctx)` → nil (no opina)
| payload sustituto (sigue) | `{deny="razón"}` (corta, el PRIMER deny gana). Orden: priority
ascendente, luego registro. Cada hook bajo `pcall` (frontera robusta, ADR-008): uno que lanza se
loguea y se ignora. `Hook:remove()` lo desactiva. `agent._reset_hooks()` (helper de tests, no
contractual) limpia el registro entre casos.

**Eventos `agent:*` (agente.md §4, notificaciones por `nu.events`):** session.start/end,
turn.start/end, delta, message, tool.start/progress/end, permission.asked, error. **Atribución
obligatoria (G3):** un único helper `emit(session_id, name, payload)` pone `payload.session`
SIEMPRE — imposible olvidarlo. `agent:` es el namespace del plugin (no reserva del core, ADR-003).

**Persistencia (sesiones.md):** `agent.session{...}` crea/reanuda vía `sessions.open` (hereda lock
de escritor, §6) salvo `no_store=true` (sesiones in-memory de test). Cada mensaje (user, assistant
con usage/model, tool_results) se persiste con `Session:append_message`. **Reanudación (G18):**
`opts.resume=<id>` hace replay del transcript y repuebla el historial en memoria (la política de
replay para el LLM —desde el último `compact`— vive aquí). `Session:set_model` (G19) valida contra
providers y escribe una entrada `event` (sesiones.md §3).

**Decisiones / desviaciones.**
- **`requires` no auto-activa**: el `nu.toml` de test enumera las tres extensiones; `requires`
  solo da el orden de carga (verificado leyendo `loader.go`: `topoSort` opera sobre lo descubierto,
  y una embebida solo se descubre si `enabled` la nombra). Documentado para S43/S45.
- **System prompt (§7) parcial en S39**: solo base + `opts.system`. El índice de skills (§6),
  `nu.md` del repo y el TOFU/confianza (§11) son trabajo posterior (no en el alcance de S39).
- **Compactación (§8)** no implementada en S39 (el hook `compact` existe en el registro de puntos,
  pero el disparo automático y la estrategia por defecto son trabajo posterior). No bloquea CP-10.
- **`ask` del handler (ctx.ask)**: en headless sin UI devuelve `false` (coherente con §5 default
  deny); con UI usa el mismo flujo de `future` que los permisos.
- **Resultado del handler** normalizado a `content: Block[]`: string→bloque texto; tabla con
  `type`→un bloque; tabla sin `type`→se asume Block[].

**Adaptador de prueba (`toolstub`)**: el stub oficial declara `tools=false` (degradación
declarada, §3), así que los tests registran desde Lua un adaptador `toolstub` con `tools=true`
que en la 1ª vuelta emite una tool call y en la 2ª (cuando el ÚLTIMO mensaje trae un tool_result)
responde texto y para. Mirar solo el último mensaje (no todo el historial) lo hace correcto al
REANUDAR (una sesión reanudada ya contiene tool_results de turnos previos) — sutileza que costó
un ciclo de depuración en CP-10.

**🔎 CP-10 verde (agente headless mínimo, usable):** `TestCP10AgenteHeadless` arranca el runtime
HEADLESS (`WithForceUI(false)`, `nu.has("ui")`=false), ejecuta un turno con la tool de fichero
real `read_file` (lee un fichero de disco con `nu.fs`, se concede por ser solo lectura, su
contenido se realimenta y el done final cierra), PERSISTE la sesión en JSONL (se verifica que el
fichero bajo `data_dir/sessions/` contiene `meta`, los `message`, el nombre de la tool, el
contenido leído y la respuesta final), y luego REANUDA la sesión (replay repuebla el historial) y
pide `write_file` → permiso DENEGADO accionable (nombra "headless"/"write_file"/"allow"), el turno
NO se rompe, y el fichero NO se crea. Todo SIN una sola línea de UI (G20).

**Tests** (`agent_test.go`, arnés de S12 con las tres extensiones por `nu.toml`): carga+activa;
turno completo (tool llamada, resultado realimentado, done final, historial de 4 mensajes);
permiso denegado headless → tool_result is_error accionable; permiso concedido por `allow`; hooks
tool.pre/post (reescriben args/resultado) y veto por `{deny}`; eventos `agent:*` emitidos con
`session` (G3); CP-10 (persistencia + reanudación headless con tool de fichero + permiso denegado).
`CGO_ENABLED=0 go build`/`go vet ./...` verdes; `gofmt -l` limpio; `CGO_ENABLED=1 go test -race
-timeout 120s -count=2 ./internal/...` verde (~54 s); no regresiona S01–S38.

**Lo que reusará S40 (subagentes):** `agent.caps.*` (paquetes de caps con nombre, §9, ya
definidos como tablas inspeccionables), el registro único de tools (los handlers corren en el
principal vía proxy, §9), los permisos/hooks centralizados (el worker no los esquiva), y
`opts.parent` (sesión hija con `meta.parent`). **Lo que reusará S43 (chat):** los eventos
`agent:delta`/`agent:message`/`agent:permission.asked` (para pintar streaming y diálogos),
`agent.permission.respond` (responder el ask del usuario), y `agent.session`/`Session:send` como
contrato consumido igual que un tercero.

**Nota de proceso.** Tras dejar el código, los tests, los docs (puntero, bitácora, esta entrada)
y verificar build/vet/gofmt/race-count=2 verdes, se commiteó y pusheó SIN demora (lección de S38).

## S40 — Subagentes del agente (workers + caps recortadas + digesto al padre) (agente.md §9)

### Qué pedía la sesión

S40 amplía la extensión `agent` (S39) con SUBAGENTES (agente.md §9): un agente que corre AISLADO
y devuelve al padre un RESULTADO DIGERIDO. El contrato: `Session:spawn(opts) -> Sub`,
`Sub:run(prompt) ⏸ -> digest`, `Sub:cancel()`, con dos modos (`worker=false`: task en el
principal compartiendo tools; `worker=true`: loop en un `nu.worker` con `caps` recortadas y los
handlers de tools ejecutados en el principal vía proxy de mensajes), más los paquetes de caps con
nombre `agent.caps.*`. Todo Lua sobre la API congelada (ADR-003): `nu.worker` §13 + `nu.task` +
el módulo `providers`. **NO amplía api.md** (APILevel sigue en 2; ni una función pública nueva).

### Arquitectura elegida (dos módulos)

- `lua/agent/subagent.lua`: el handle `Sub`, los dos modos y el PROXY de tools del lado del
  PADRE. Se cablea sobre el módulo `agent` ya construido con `subagent.attach(M)` (inyección para
  evitar require circular), exponiendo `M._subagent.spawn` (usado por `Session:spawn`).
- `lua/agent/subagent_worker.lua`: el LOOP del subagente que corre DENTRO del worker. Es el
  `module` que `nu.worker.spawn("agent.subagent_worker", {caps=...})` carga.

### Decisiones de interpretación de agente.md §9

1. **El digesto** (agente.md §9 dice "resultado digerido, no el stream crudo") se materializa como
   `{ text, message, stop_reason, usage, turns }`: `text` es el texto plano del mensaje final
   (atajo que el padre integra como tool_result/mensaje), `message` el Message canónico completo
   (JSON-able), `usage` el del proveedor del último turno. JSON-able a propósito: cruza la frontera
   del worker sin Blocks/closures (api.md §13).

2. **El proxy de tools** (modo worker). El worker NO ejecuta handlers: por cada tool_call manda
   `{kind="tool_call", id, name, args}` al padre por `nu.worker.parent.send` y espera
   `{kind="tool_result", result}`. El padre corre la tool con `M.run_tool_proxy(proxy_session,
   call)` = el mismo `run_tool` del turno (permisos → hooks → handler → tool_result). Así la
   seguridad queda centralizada (el worker no puede esquivar el pipeline porque la ejecución nunca
   ocurre en su lado) y hay UN solo registro de tools. La `proxy_session` es una `agent.session`
   hija real (aporta permisos heredados-recortados, cwd, y el transcript hijo si persiste).

3. **DOS VALLAS** (agente.md §9, literal): las *caps* limitan qué hace el código Lua del worker
   (G6, sandbox del core); los *permisos* (heredados del padre, recortados por `opts.permissions`,
   nunca ampliados) limitan qué tools usa —y como las tools corren en el padre, su pipeline de
   permisos §5 es la valla efectiva—.

4. **Caps por defecto de un subagente-worker: solo-lectura.** `FS_RO` (fs.read/stat/list/cwd) +
   `SEARCH` + los MÍNIMOS DEL LOOP (`task`/`json`/`toml`/`config.dir`/`log`/`fs.read`). Razón de
   los mínimos: el worker debe poder orquestar (task), serializar el digesto/los mensajes del
   proxy (json) y RESOLVER el modelo —`providers.resolve` lee `providers.toml` del disco con
   `nu.fs.read`+`nu.toml.decode` desde `nu.config.dir`—. Sin ellos el worker no podría ni correr
   el turno ni devolver nada. `normalize_caps` siempre los añade a una lista de usuario, sin
   ampliar la superficie de fs/net que el usuario eligió.

### Desviación: `opts.adapter_modules` (opt de la extensión, NO del core)

agente.md §9 lista `opts` = los de `agent.session` + `{ worker?, caps? }`. He añadido un opt
EXTRA de la extensión (no del core): `opts.adapter_modules` (lista de NOMBRES de módulo de
adaptador require-ables que el worker registra antes de resolver). **Por qué es necesario:** el
`init.lua` de `providers` —que registra los adaptadores oficiales imperativamente— NO corre dentro
de un worker (un worker solo ejecuta `require(module)`, sin ciclo de vida de plugins, api.md §13).
Así el registro vivo de adaptadores arranca VACÍO en el worker; el bootstrap lo rellena
requiriendo los módulos nombrados (los oficiales SON require-ables: `providers.adapter_anthropic`).
Es re-ejecutar lo que haría init.lua, sin privilegio de kernel. Tiene defecto sensato
(`{ "providers.adapter_anthropic" }`), así que el caso normal no lo necesita; los tests lo usan
para inyectar un stub require-able. Es una adición a los opts de UNA extensión, no a `api.md`.

### Por qué NO hizo falta ampliar api.md

El subagente-worker se expresa enteramente con la API pública: `nu.worker.spawn` con `caps`
(api.md §13, G6) para el aislamiento DURO; `Worker:send`/`recv` + `nu.worker.parent.send`/`recv`
para el protocolo init/tool_call/tool_result/done (mensajes JSON-ables copiados); `nu.task` para
el loop; el módulo `providers` (resolve + register_adapter) y el módulo `agent` (run_tool_proxy,
caps). El corolario de completitud se satisface: una feature oficial construida sin atajo de
kernel. APILevel sigue en 2.

### El subagente-worker es HEADLESS por construcción

Dentro del worker NO existen `nu.events` (bus principal) ni `nu.ui` (api.md §16). El loop del
subagente-worker, por tanto, DESCARTA los deltas del stream (no hay a quién emitirlos) y solo
emite el DIGESTO al padre. Coherente con agente.md §9: el padre recibe datos digeridos, no el
stream crudo. En modo task (worker=false) sí se emiten los `agent:*` (corre en el principal).

### Tests (`subagent_test.go`)

Arnés con providers+sessions+agent + un plugin de usuario que aporta dos módulos require-ables:
`wstub` (adaptador stub que decide su comportamiento mirando el REQUEST, no globales del principal
—que no cruzan al worker—) y `wprobe` (módulo de worker que reporta qué API existe dentro).
Casos: superficie de `Session:spawn`/`Sub`; modo task (digesto con texto+usage del último turno);
modo worker e2e (turno aislado con `wstub` → digesto integrado por el padre); AISLAMIENTO DESDE
DENTRO (`wprobe` con las caps por defecto: `fs.write`/`http`/`ui`/`events` NO existen,
`fs.read`/`task`/`json`/`toml` SÍ — la verificación directa del criterio "API recortada");
PROXY de tools (una tool cuyo handler marca una global del PRINCIPAL: si cambió, corrió en el
padre); caps mal formadas → EINVAL; los paquetes `agent.caps.*` sin `fs.write`.

`CGO_ENABLED=0 go build`/`go vet ./...` verdes; `gofmt -l` limpio; `CGO_ENABLED=1 go test -race
-timeout 120s -count=2 ./internal/...` verde (~53 s); no regresiona S01–S39. Sin hallazgos `G##`.

### Lo que reusará S41/S43

- **S41 (MCP):** el registro único de tools + `run_tool_proxy`/los permisos centralizados (las
  tools de un servidor MCP se registran con `agent.tool` igual que las de fichero, y el subagente
  las usa por el mismo proxy); el patrón de mensajería worker↔padre.
- **S43 (chat):** `Session:spawn`/`Sub:run`/el digesto como contrato consumido igual que un
  tercero, y (en modo task) los eventos `agent:*` del subagente.

**Nota de proceso.** Tras código + tests + docs (puntero a S41, bitácora, esta entrada) +
build/vet/gofmt/race-count=2 verdes, se commitea y pushea SIN demora (lección de S38/S39).

## S41 — Extensión oficial `mcp` (capa 2: cliente JSON-RPC/stdio; mapeo de tools + confianza) (arquitectura.md §capa 2, cierra cuestión abierta nº4)

Sexto eslabón de la Fase 8. **Lua puro sobre la API pública congelada** (ADR-003, sin privilegio
de kernel — el core NO sabe lo que es MCP). Implementa la **capa 2** de arquitectura.md
("procesos externos vía subproceso, JSON-RPC/stdio; MCP vive aquí como extensión oficial Lua sobre
`io.spawn` + codecs") y **cierra la cuestión abierta nº4** de arquitectura.md (el contrato de la
extensión MCP: configuración, ciclo de vida, mapeo de tools y confianza).

Plugin embebido nuevo `internal/runtime/embedded/mcp/`: `plugin.toml` (name="mcp",
`requires=["agent"]`), `init.lua` (cablea + auto-conexión perezosa de `mcp.toml` en una task) y el
módulo `lua/mcp/init.lua`. INACTIVO por defecto (ADR-010); activable por `nu.toml`
`plugins.enabled=[..., "mcp"]`, `source="builtin"`. El `embed.FS` lo descubre solo (cualquier
subdirectorio de `embedded/` con `plugin.toml`), sin tocar el mecanismo de S12.

### El cliente JSON-RPC 2.0 sobre stdio (`Conn`)

`mcp.connect{ name, command, cwd?, env? } ⏸ -> Conn` lanza el servidor con `nu.proc.spawn` (S16) y
le habla por stdin (requests JSON con `nu.json.encode` + `Proc:write`), leyendo responses de
stdout línea a línea (`Proc:read_line` + `nu.json.decode`). Demultiplexado: una **task lectora
dedicada** (`dispatch_loop`) lee stdout y reparte cada response a su request pendiente por `id`
(cada `request` registra un `nu.task.future` que el lector resuelve), permitiendo varios requests
en vuelo sin mezclar respuestas. Las notificaciones del servidor (sin id) se ignoran en v1.

### Decisiones de la extensión (no tocan el core; cierran nº4)

1. **Framing newline-delimited.** Una línea = un mensaje JSON terminado en `\n`. Es el framing del
   transporte stdio de MCP en su forma simple. La alternativa **Content-Length** (cabeceras estilo
   LSP) se descartó para v1: añade complejidad de parseo sin beneficio para el harness, y el
   transporte por líneas compone exactamente con `Proc:read_line` (api.md §6) sin buffering extra.
   Se documenta en el módulo; si un servidor exigiera Content-Length, sería una iteración futura
   (el cliente lee/escribe en un único punto, fácil de extender).
2. **Prefijo `mcp__<servidor>__<tool>`.** Las tools MCP se registran en el agente con este nombre.
   Es la convención de namespacing del ecosistema MCP: evita choques entre servidores y entre una
   tool MCP y una propia, y hace legible el patrón de permiso (`allow = {"mcp__github__*"}`).
3. **Confianza = `permissions.default = "ask"`.** Las tools MCP son de TERCEROS; se registran con
   default "ask" (agente.md §5), nunca el "allow" de las de solo lectura propias. Así requieren
   permiso EXPLÍCITO y en headless sin `allow` el pipeline de §5 las DENIEGA con error accionable.
   No hay caso especial en el agente: una tool MCP pasa por la misma valla (permisos → hooks →
   handler) que cualquier otra. Coherente con agente.md §3 ("MCP encaja aquí sin caso especial").
4. **`mcp.toml` como formato de configuración** (división datos/código, ADR-005):
   `[servers.<nombre>] command = [...] cwd? env?`. Ausente → no se conecta nada (lo normal).
   `mcp.connect_configured` los lanza desde una task; un servidor que falla no impide a los demás.

### Ciclo de vida del proceso (api.md §6)

El servidor se lanza, vive mientras la `Conn` exista, y se mata limpiamente: `Proc:kill` registrado
en `nu.task.cleanup` (muere al terminar la task dueña) y `Conn:close()` explícito e idempotente.
Un servidor que MUERE (EOF en stdout) hace que `dispatch_loop` marque la conexión caída y despierte
a TODOS los requests pendientes con `EMCP` (nadie cuelga para siempre). Al cerrar, las tools del
servidor se re-registran con un handler que falla accionable (la extensión `agent` no expone un
des-registro público — un re-registro SUSTITUYE, agente.md §3 — y dejar tools que invoquen una
conexión muerta sería peor: el error vuelve como tool_result is_error que el modelo ve).

### Mapeo de resultados

El resultado de `tools/call` de MCP (`{ content = [{type="text",text},...], isError? }`) se traduce
al formato del handler del agente (string | Block[]): se concatenan los bloques de texto; un
`isError = true` se propaga lanzando `EMCP` (el loop lo vuelve tool_result is_error). Imágenes y
otros tipos de bloque quedan para una iteración posterior (v1 cubre texto, el caso central).

### NO amplía api.md (corolario de completitud satisfecho)

`nu.proc` §6 (spawn/write/read_line/kill) + `nu.json` §12 + `nu.task` §4 (spawn/future/cleanup) +
`nu.fs`/`nu.toml`/`nu.config.dir` + el módulo `agent` (`agent.tool`, `agent.tools`) bastaron
EXACTOS para construir MCP. APILevel sigue en **2**; ni una función pública del core de más. Error
de la extensión: `EMCP` (forma ADR-009). Sin hallazgos `G##`.

### Tests (`mcp_test.go`)

El servidor MCP de prueba es un **mini-programa Go** (fuente embebida en el test) que se compila a
un binario temporal con `go build` (sin red, sin dependencias externas más allá de Go, garantizado
en el entorno — la opción más robusta sugerida por el enunciado). Habla JSON-RPC/stdio: responde a
`initialize`, `notifications/initialized`, `tools/list` (anuncia `echo` y `boom`) y `tools/call`
(las ejecuta; `boom` devuelve `isError=true`). Casos: carga+activa (builtin); connect + handshake +
tools/list + registro con prefijo y confianza; **CICLO COMPLETO** (el adaptador de prueba pide
`mcp__srv__echo`, el handler hace `tools/call`, "eco: hola MCP" se realimenta al modelo); confianza
headless (tool MCP sin allow → DENY accionable que nombra "headless"/tool/"allow"); `isError` del
servidor propagado a tool_result is_error; ciclo de vida (pid vivo tras connect, muerto tras
`close()`, vía `pidAlive`/`waitDead` de proc_test).

**Nota anti-race:** registrar globales Go (`SetGlobal`) DESPUÉS de Boot es una carrera con el
scheduler (el auto-connect de mcp ya corre); el test de ciclo de vida instala sus helpers
(`__publish_pid`, `__mcp_pid`) ANTES de Boot (`bootMCPWith(preBoot)`). El resto de tests no tocan
globales tras Boot.

`CGO_ENABLED=0 go build`/`go vet ./...` verdes; `gofmt -l` limpio; `CGO_ENABLED=1 go test -race
-timeout 120s -count=2 ./internal/...` verde (~54 s), sin flaky; no regresiona S01–S40.

### Lo que reusará S43 (chat)

`require("mcp")` (`mcp.connect`/`mcp.servers`/`mcp.get`) como cualquier extensión de tercero, y las
tools MCP ya registradas en el agente que el chat lista/invoca por el pipeline de permisos de §5
igual que las propias (la UI pinta el permiso de una tool MCP como el de cualquier otra).

**Nota de proceso.** Tras código + tests + docs (puntero a S42, bitácora, cierre de arquitectura
nº4, esta entrada) + build/vet/gofmt/race-count=2 verdes, se commitea y pushea SIN demora.

## S42 — Toolkit de widgets (árbol+dirty, slots, focus, themes G22) (arquitectura.md §kernel/nota ui)

Séptima extensión de la Fase 8. **Lua puro sobre la API congelada** (ADR-003 / ADR-012):
el core NO sabe lo que es un widget; el toolkit es una extensión oficial sin privilegio.
Plugin embebido `internal/runtime/embedded/toolkit/` (`plugin.toml` name="toolkit", sin
`requires`) con módulos `lua/toolkit/{init,theme,widget,layout,widgets,app}.lua`. Implementa
la nota de arquitectura.md §kernel sobre `ui` (el toolkit «retenida por dentro: árbol +
nodos sucios … aporta slots, focus, composición entre plugins y el sistema de themes») y,
junto a S43 que lo consume, cierra la cuestión abierta nº3 (la API pública del toolkit).

### El modelo (lo que arquitectura.md dejaba abierto, fijado aquí)

`arquitectura.md` nombra los ingredientes (árbol, nodos sucios, slots, focus, themes) pero
no el catálogo de widgets ni el modelo de layout exacto. Se implementa un **conjunto mínimo
coherente**, suficiente para el criterio de hecho de S42 y para que S43 (chat) construya su
anatomía (chat.md §1: columna transcript/input/statusline + capas modales).

- **Árbol retenido** (`toolkit.widget`): cada nodo conoce `parent`/`children`, su área local
  `(x,y,w,h)` —que le ASIGNA el layout del padre, una hoja no decide dónde va—, y
  `compose(w,h) -> Block` (lo único específico de cada tipo; el resto —árbol, dirty, focus—
  es común). `derive()` fabrica metatablas que heredan del Widget base para los tipos
  concretos sin duplicar maquinaria.

- **Dirty tracking** (decisión clave, el porqué es ADR-007: no recomponer todo cada frame).
  Cada nodo cachea su último `Block` (`_block`) y un flag `dirty`. `mark_dirty()` ensucia
  SOLO ese nodo (invalida su caché) y AVISA hacia arriba a la app (`_notify` →
  `app:_request_paint`), **sin ensuciar a los hermanos ni a los ancestros** (sus Blocks
  siguen válidos; lo que cambió es un descendiente que la app re-blittea). `render()`
  recompone únicamente si el nodo está sucio o si su TAMAÑO cambió respecto al caché. Sutileza
  importante: **mover sin redimensionar (solo `x/y`) NO recompone** —el contenido es el mismo,
  solo cambia dónde se blittea—; solo un cambio de `w/h` invalida el Block. Ese es el ahorro
  real: no RECOMPONER (medir texto, render markdown) que es lo caro; el blit es copia barata
  (api.md §9.1). Verificado instrumentando `compose` en el test (contar recomposiciones).

- **Slots/layout** (`toolkit.layout`): tres contenedores que NO pintan ellos mismos, COLOCAN
  a sus hijos repartiendo su área. `vbox`/`hbox` reparten un eje; un hijo declara cómo ocupa
  el eje principal con `flex` (>0: parte proporcional del sobrante) o tamaño fijo
  (`pref_h`/`pref_w`); un hijo sin flex ni tamaño fijo ocupa 0 (decisión explícita: quien no
  dice cuánto ocupa no acapara). El **último flexible** se queda el remanente del *slack*
  (espacio sobrante tras los fijos), no `main - pos` —el bug inicial: con `main - pos` un
  flexible intermedio robaba el hueco de los fijos posteriores; se corrigió a "slack restante
  del último flexible", que respeta a los fijos que vengan después—. `stack` superpone a todos
  los hijos en la misma área (orden de inserción = z lógico): la base de las capas modales.

- **Focus** (`toolkit.app`): la app raíz mantiene UN widget enfocado, recoge los focusables en
  PREORDEN (orden natural de tabulación), los cicla con `focus_next`/`focus_prev` (envuelve por
  los extremos) y enruta el input al ENFOCADO. `handle_key(ev)` entrega al `widget:on_key`; lo
  que el widget no consume, la app lo DEJA PASAR (devuelve false), respetando la pila del core
  (api.md §9.3: «quien no consume, deja pasar»), de modo que un keymap de capa superior puede
  recogerlo. `tab`/`shift+tab` mueven el foco por defecto. La app coloca el cursor REAL en el
  input enfocado con `Region:cursor`. Emite `toolkit:focus {app,widget}` al cambiar el foco —en
  el namespace del PLUGIN (`toolkit`), NO `ui:focus`: `ui:` es reserva del core (api.md §4), que
  ya emite su propio `ui:focus {focused}` con OTRA semántica (el foco del TERMINAL, ui_events.go);
  pisarlo rompería a sus suscriptores. El foco de WIDGET es vocabulario del toolkit (§9.3).

- **Themes (G22)** (`toolkit.theme`): EL punto de G22. El core solo entiende colores literales
  (`#rrggbb`/0-255); los nombres semánticos (`accent`/`error`/`dim`…) son vocabulario del
  theme, que los RESUELVE a literales antes de construir el Block/Style. `theme:color(name)`
  (literal→intacto; nombre→literal; desconocido→EINVAL accionable: un theme incompleto se nota,
  no degrada en silencio); `theme:style(spec)` convierte `fg`/`bg` semánticos a literales,
  copiando los atributos. `theme.new{colors}` VALIDA que la paleta sean literales (un theme
  que mapeara "accent" a otro nombre fallaría más tarde dentro de `nu.ui.block`; validarlo al
  construir lo ancla al theme). Se replica `is_literal_color` en Lua (misma forma que
  `normalizeColor` del core) para distinguir "ya es literal" de "es un nombre a resolver" SIN
  intentar construir un Block y capturar el error. `theme.default` trae una paleta mínima con
  los nombres que chat.md §7 exige.

- **Sin colisión entre plugins** (criterio de hecho): cada `toolkit.app` es INDEPENDIENTE —su
  propia `Region` (z-order propio, api.md §9.1), su propio árbol, su propio foco, su propio
  `on_input` en la pila—. Dos plugins que montan cada uno su app componen en regiones distintas
  y el input fluye por la pila (quien consume gana; quien no, deja pasar al de abajo, que puede
  ser otra app). No hay estado global compartido entre apps: toda la retención vive en la
  instancia.

### Widgets base implementados

- **label**: una línea de texto estilizado (statusline, cabeceras). No focusable. `pref_h=1`
  por defecto (un label ocupa su renglón, no 0). Compone con `nu.ui.block` + `theme:style`.
- **text**: bloque multilínea de markdown (`nu.text.markdown`, streaming-safe) o word-wrap
  (`nu.text.wrap`), con SCROLL por viewport. Compone el Block COMPLETO; el scroll es un offset
  (`scroll_to` solo pide repintado, no ensucia: "scroll = re-blit con otro offset", api.md §9.1).
- **input**: editor de UNA línea, FOCUSABLE. `on_key` consume caracteres imprimibles, backspace,
  flechas, home/end y mantiene un caret (en bytes; el editor rico/multilínea es la extensión
  natural posterior, chat.md §3). enter/tab los DEJA PASAR (los gestiona la app: enviar/cambiar
  foco). `caret_col()` da la columna del cursor real.

### Decisión de implementación: recorte a la banda por región-viewport (scroll Y desborde)

El recorte del core es por REGIÓN, no por banda de widget (api.md §9.1: la región es el viewport,
`blit(0,-3,doc)` recorta el borde inicial pero clipa al borde de la REGIÓN). Como la región de la
app abarca el árbol ENTERO, blittear ahí el Block de un `text` lo recorta a la región, no a su
banda — y el `text` compone su Block COMPLETO (puede exceder su banda `h`, widgets.lua). Eso
SANGRA en DOS casos:
  * **scroll** (`scroll>0`, offset negativo): el `text` empezaría por una fila posterior,
    derramando sobre el widget de ARRIBA;
  * **desborde** (Block más alto que la banda, incluso con `scroll==0`): el `text` escribiría
    filas de más sobre el widget de ABAJO.
El modelo correcto del core es **una región por viewport**: por eso un `text` que está desplazado
**o** que desborda su banda obtiene su PROPIA región hija (creada al vuelo, `z = app.z + 1`,
propiedad de la app, destruida en `App:close()`), recortada a su banda; ahí el offset recorta
limpio por AMBOS extremos (G28) y nada sale de la banda. Los widgets que CABEN en su banda y no
están desplazados se blittean directos en la región de la app (vía rápida: ni región hija ni z
extra para un label/input/text corto). Si un `text` que desbordaba vuelve a caber, se OCULTA su
región-viewport (su contenido viejo, a `z+1`, no debe seguir tapando lo que pinta la app; se
re-muestra si vuelve a hacer falta). El gate es `oy ~= 0 or blk.height > node.h`. Es uso correcto
de la primitiva, no una ampliación del core. (La revisión de S42 detectó el sangrado del desborde
sin scroll: el gate original solo cubría `scroll~=0`.)

### Render síncrono en `_request_paint` (simplicidad + tests deterministas)

`_request_paint` pinta de forma SÍNCRONA (`paint()`). En una app viva el compositor del core ya
coalesce los blits y pinta como mucho cada ~30 ms (api.md §9), así que blittear de más es barato
(es copia, no re-render); la ganancia del dirty tracking es no RECOMPONER los Blocks (lo caro),
no evitar el blit. Pintar síncrono mantiene el código simple y deja a los tests ver el resultado
al instante (inspeccionando la rejilla del compositor tras `APP:paint()`).

### NO amplía api.md (corolario de completitud satisfecho)

El toolkit se construyó EXACTAMENTE sobre la API §9 (`nu.ui.region`/`blit`/`fill`/`clear`/
`cursor`/`size`, `nu.ui.block`/`Style`, `nu.ui.on_input`) + §10 (`nu.text.markdown`/`wrap`/
`truncate`/`width`) + §4 (`nu.events.emit`, con su evento propio `toolkit:focus` en el namespace
del plugin) + §2 (`nu.has`). Ni una función pública de más; APILevel sigue en 2. Sin hallazgos `G##`. Confirma que la API de UI de bajo nivel
(ADR-007) basta para un toolkit de alto nivel en Lua (ADR-012: el veto de ADR-007 no se ejecutó).

### Tests y resultado

`toolkit_test.go` (arnés de S12 con `WithForceUI(true)`+`WithUISize` —el toolkit es UI, en
headless `nu.ui` no existe, G20—; el Block es opaco a Lua, así que el CONTENIDO se inspecciona en
Go mirando la rejilla del compositor, igual que `compositor_test.go`, y la lógica del toolkit se
inspecciona desde Lua sobre sus propias tablas): carga+activa (builtin); theme G22; dirty
tracking; layout+focus entre dos widgets (criterio de hecho); sin colisión entre dos árboles
(criterio de hecho); input no consumido se deja pasar; reparto del vbox; scroll-viewport;
**desborde sin scroll** (un `text` más alto que su banda, con `scroll==0`, encima de un label: el
recorte a banda evita que derrame sobre el de abajo); `app`
sin `nu.ui`→EINVAL. `CGO_ENABLED=0 go build`/`go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde (~55 s; el toolkit
estable bajo `-race -count=4`). Nota: `TestMCPToolServerError` (S41) es un flake conocido bajo la
suite completa con `-race -count=2` (compila y lanza un proceso externo; bajo contención de
CPU/IO del conjunto su handshake JSON-RPC ocasionalmente excede el timing); pasa aislado y en
re-ejecuciones de la suite completa; es ORTOGONAL a S42 (el toolkit es Lua sobre `nu.ui`/`nu.text`,
no toca proc/red). No regresiona S01–S41.

### Nota de revisión de S42 (dos arreglos antes de aprobar)

La revisión encontró dos defectos, ambos arreglados (el commit de S42 se enmendó):

1. **[Bloqueante] Colisión de evento con el core.** `App:set_focus` emitía `ui:focus` con payload
   `{app,widget}`, pisando el `ui:focus {focused}` que el core emite para el foco del TERMINAL
   (ui_events.go, blindado en `gating_test.go`): cualquier suscriptor del `ui:focus` del core se
   rompía (su `ev.focused` desaparecía). `ui:` es reserva del core (api.md §4) y el foco de
   WIDGET es vocabulario del toolkit (§9.3), así que el evento se RENOMBRÓ a **`toolkit:focus`**
   (namespace = nombre del plugin). Se ajustó la prosa en `app.lua`, `init.lua`, la bitácora
   (implementacion.md) y esta entrada. El `ui:focus` del core sigue intacto (su test sigue verde,
   no depende del toolkit).
2. **[Menor] Sangrado del `text` sin scroll.** El `paint()` solo usaba la región-viewport
   recortada cuando `scroll~=0`; con `scroll==0` un `text` más alto que su banda se blitteaba
   directo sobre la región compartida y derramaba filas sobre el widget de ABAJO (el recorte del
   core es por REGIÓN, no por banda). Se amplió el gate a `oy ~= 0 or blk.height > node.h`: todo
   `text` que desborde su banda o esté desplazado pinta por su región-viewport recortada a la
   banda (un `text` que vuelve a caber oculta su viewport para no dejar restos). Test nuevo:
   `TestToolkitTextDesbordeSinScroll` (un `text` de 6 líneas en una banda de 3 sobre un label →
   el label NO se sobrescribe). Detalle en "recorte a la banda por región-viewport".

**Nota de proceso.** Tras código + tests + docs (puntero a S43, bitácora, esta entrada) +
build/vet/gofmt/race-count=2 verdes, se commitea y pushea SIN demora.
