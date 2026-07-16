# S08 — Cancelación pública: `Task:cancel` + `enu.task.cleanup` + desenrollado no capturable (api.md §1.3, §3)

S08 está en el inventario 🔒 y es un **hito de veto** (valida ADR-008). El punto
difícil —y el que podía vetar el plan— era hacer el aborto **no capturable por
`pcall`** sobre gopher-lua, que recupera todo pánico Go en su `pcall` nativo. La
técnica conocida (envolver `pcall`/`xpcall`) funcionó limpia; **no hubo
hallazgo/veto**.

## La técnica del no-capturable: wrapper de `pcall`/`xpcall` (decisión clave)

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

## Por qué `task.aborting` y no el valor del pánico (decisión)

Al cruzar `LState.PCall`, un pánico que no sea `*lua.ApiError` se convierte en un
`*ApiError` con su mensaje vía `fmt.Sprint` —se pierde el tipo Go `abortSignal`—.
Detectar el aborto por el valor recuperado sería frágil (dependería de la
representación textual). En cambio `aborting` es un flag de la propia task,
escrito y leído por su única goroutine **bajo el token**: detección robusta e
independiente de cómo gopher-lua represente el pánico. Sale gratis el re-lanzado
idéntico (reconstruimos `abortSignal{t}` desde la task). S09 reusará exactamente
este camino poniendo `reason = abortBudget`.

## `xpcall`: el `errfn` del usuario NO ve el aborto (decisión)

El `xpcall` nativo correría su message handler (`errfn`) **dentro** de
`LState.PCall`, es decir, sobre el aborto. Eso filtraría el aborto al código del
usuario (§1.3 lo prohíbe). La versión envuelta pasa `nil` como manejador al
`PCall` nativo y aplica `errfn` **nosotros, solo si el error NO es un aborto**.
Coste aceptado: el `errfn` corre tras desenrollar (no antes, como en Lua de
verdad), pero gopher-lua no expone traceback rico al handler, así que no se pierde
nada observable.

## Semántica de `ECANCELED` en `await` (decisión clave)

`Task:await` de una task **cancelada** entrega `ECANCELED` (estructurado), que el
awaiter **SÍ puede capturar con `pcall`**. Es coherente con §1.3 porque es
**observación de la cancelación de OTRA task**, no el aborto del propio awaiter:
si cancelaran al awaiter mismo, su desenrollado sería inmune; pero *observar* que
una task que esperaba fue cancelada es un error normal y capturable. El awaiter
sigue vivo tras el `pcall` (corre el código de después). Implementación:
`taskAwait` comprueba `t.canceled` (antes que `t.errValue`, que una task cancelada
nunca tiene) y lanza `ECANCELED` con `raiseError`.

## `Task:cancel` sobre una task ya terminada es no-op (decisión clave)

Cancelar una task que **ya cerró su desenlace** NO debe convertir retroactivamente
su resultado en `ECANCELED` —terminó bien (o con error) antes de la cancelación, y
eso es lo que su `await` debe seguir entregando—. `cancelTask` chequea `t.done` y
retorna sin tocar `canceled`. Es seguro leer `t.done` ahí porque todas las
llamadas (`Task:cancel`, `all`/`race`) corren **bajo el token**, igual que el
`t.done = true` de `runTask`. `Task:cancel` **no suspende** (es síncrona desde
fuera, §3); cancelar dos veces es idempotente (`cancelOnce`); cancelarse a sí
misma es legal (surte efecto en el siguiente ⏸ propio).

## Pila LIFO de `cleanup`: corre en los TRES finales (decisión)

`task.cleanups []*lua.LFunction`; `enu.task.cleanup(fn)` apila (fuera de task →
`EINVAL`, no hay task a la que atar el liberador). `runCleanups` (en `runTask`,
con el token tomado, tras el `CallByParam`) corre TODOS en orden inverso al de
registro —semántica `defer`— pase lo que pase: éxito, error o aborto. Cada
liberador corre sobre un **thread efímero** (como las tasks y los handlers de S05)
bajo `pcall` por frontera (ADR-008): un cleanup que lanza queda en el log
(best-effort; evento formal en S10) y no impide que corran los demás ni tumba el
proceso.

## Substrato S07 reutilizado, no reescrito

Siguen intactos `cancelCh`/`cancelTask`/`abortSignal`/`coToTask` y los `select`
sobre `cancelCh` en `suspend`/`Task:await`/`Future:await`. S08 **añade**: el flag
`aborting`, el `abortReason` (`abortCancel` vs `abortBudget`, este último para S09),
la pila `cleanups`, los métodos públicos `Task:cancel`/`enu.task.cleanup`, el
`ECANCELED` en `await`, y los wrappers de `pcall`/`xpcall`. Superficie pública
nueva = SOLO `Task:cancel` y `enu.task.cleanup` (API sagrada, §3).

## Sin hallazgos ni veto

gopher-lua v1.1.2 **sí permite** un desenrollado no capturable limpio vía el
wrapper de `pcall`/`xpcall`. No se rompieron los errores normales de §1.4 (siguen
capturables, multi-retorno incluido). No se amplió `api.md`. No se abrió ningún
`G##`. El hito de veto S08 queda validado a favor de ADR-008.

## Qué hereda S09 (watchdog)

S09 reusa el **mismo desenrollado no capturable**: cortará el slice de CPU puro
excedido lanzando el mismo `abortSignal` —pero desde el watchdog, no desde un
punto de suspensión— con `reason = abortBudget`. Los wrappers de `pcall`/`xpcall`
ya lo harán no capturable (consultan `aborting`, agnóstico al `reason`);
`runCleanups` ya corre en el aborto sea cual sea el motivo; `await` distinguirá el
motivo para observar `EBUDGET` en vez de `ECANCELED`, y S09 emitirá
`core:plugin.misbehaved` (verificable tras S10). El gancho técnico que falta es
**interrumpir un slice Lua que no suspende** (un bucle de CPU puro): eso es trabajo
propio de S09 (hook de instrucciones / `LState` con límite), no de S08.
