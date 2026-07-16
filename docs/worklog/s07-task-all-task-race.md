---
title: "`enu.task.all` / `enu.task.race` (api.md §3)"
type: "sesion"
id: "S07"
phase: 1
status: "cerrada"
---
# S07 — `enu.task.all` / `enu.task.race` (api.md §3)

## La frontera S07/S08: substrato de cancelación interno (decisión clave)

`all`/`race` necesitan "cancelar el resto", pero la cancelación PÚBLICA es S08
(`Task:cancel()`, `enu.task.cleanup` con pila LIFO, `ECANCELED` observable en
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

2. **`enu.task.cleanup` (pila LIFO) durante el aborto.** No existe aún; S08 correrá
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

## Fan-in concurrente: detectar el primer error sin orden (decisión)

`all` debe cancelar al resto **en cuanto** una task falla, no cuando le toque por
orden de array. Un primer intento esperando los `doneCh` en orden (`for i := range
tasks { <-t.doneCh }`) fallaba: una primera task lenta retrasaba ver el fallo de
una segunda rápida, y la lenta llegaba a completar antes de ser cancelada (lo
cazó `TestAllCancelsOthersOnError`). La solución es `waitAllOrFirstError`: una
goroutine efímera por task reporta su cierre a un canal común; el bucle devuelve
el índice del primer fallo en cuanto ocurre, o -1 si todas terminan bien. `race`
usa el simétrico `waitFirst` (primer cierre gana, sea por éxito o por error).

## Alineamiento G27: indexar por posición, no por terminación

El invariante 🔒 (G27) sale gratis de la estructura: `all` resuelve la lista a un
slice `tasks[]` en orden de la tabla (clave 1..n) y rellena `out[i+1]` con
`firstResult(tasks[i])`. El orden en que cierran los `doneCh` no toca el array de
salida: se indexa por posición. `race` devuelve el índice del ganador **+1**
(1-based, Lua). Tests con sleeps inversos (terminación 3,2,1 frente a entrada
1,2,3) blindan que no se cuela el orden de terminación.

## Entrada: handles, funciones o mezcla

§3 dice `Task[]|fn[]`. Se interpreta de la forma más permisiva y coherente con la
prosa ("handles ya creados O funciones"): cada elemento del array puede ser una
función (se le hace `spawn`) o un handle `Task` (se adjunta), y pueden mezclarse.
Un valor de otro tipo, o un array vacío, es `EINVAL` con mensaje que nombra la
posición. Cada task entrega su **primer** valor de retorno (§3: el array de `all`
y el `result` de `race` son de un valor por entrada, no multivalor).

## Sin hallazgos

El modelo de S04/S06 más el substrato de cancelación interno bastaron para S07 sin
ampliar la API ni tocar `api.md` §3. La frontera S07/S08 es **orden de
implementación**, no un `G##`: se resolvió con el substrato mínimo descrito arriba.
