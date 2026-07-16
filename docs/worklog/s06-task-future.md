# S06 — `enu.task.future` (rendez-vous de un solo uso, api.md §3)

## Desviación de procedimiento: rama desde `origin/main`

Esta sesión se implementó partiendo de `origin/main`, donde el puntero ▶ ya
marcaba `S06` (S05 quedó mergeada). El ramaje local de trabajo estaba desfasado;
se creó `claude/s06-future` desde `origin/main` para arrancar sobre el estado
real. No hay desviación de *alcance*: S06 depende solo de S04 (cerrada), así que
el grafo de dependencias se respeta.

## Quiescencia: `set`/`await` NO tocan `live`/`pending` (decisión clave)

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

## Despertar de múltiples awaiters con un único `set`

Se reusa el patrón de `Task:await`: un canal `resolvedCh` que `set` **cierra**
(bajo el token). El cierre de canal es un broadcast natural —todos los awaiters
bloqueados en `<-resolvedCh` despiertan a la vez— y aporta el happens-before que
hace visible el `value` (escrito bajo token antes del cierre) cuando cada awaiter
recupera el token. No hace falta candado propio en `resolved`/`value`: ambos se
tocan solo bajo el token (el token *es* el candado), y el único cruce entre
goroutines es el cierre del canal. Esto es lo que blinda el test `-race`.

## `set()` sin argumento resuelve con `nil`

Coherente con que un future pueda usarse como mera señal ("ya ocurrió") y no solo
como portador de valor. No es API nueva: `Future:set(v)` con `v` opcional cae en
el `LNil` que devuelve `L.Get(2)` cuando no se pasó argumento. `set()` con nil
sigue consumiendo el único uso (un segundo `set` da `EINVAL`): resolver con nil
es resolver.

## Sin hallazgos

El modelo de S04/S05 bastó para S06 sin ampliar la API. No se abrió ningún `G##`.
