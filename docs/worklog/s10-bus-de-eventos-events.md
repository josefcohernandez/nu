# S10 — bus de eventos `enu.events` (api.md §4)

## La cola de emits: drenado plano (decisión clave de G10)

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

## El watchdog y el ping-pong infinito (el matiz no obvio)

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

## Emitir misbehaved desde la goroutine de la task (seguridad de hilo)

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

## Sin superficie de más, sin hallazgo

Superficie nueva = exactamente `enu.events.on/once/emit` + `Sub:cancel` (§4). El
bus es **solo estado principal** (no [W]); en un worker no existe (S34). El modelo
de S04–S09 (token + watchdog + desenrollado no capturable) bastó: la vigilancia
del watchdog en el drenado reusa `claimBudgetAbort`/`abort`, no inventa nada.
APILevel sigue en 1 (api.md ya describía §4; no es una adición post-congelado).
