# S16 — `enu.proc` (api.md §6)

## La causa del cuelgue del intento previo, y su arreglo (lo central de esta sesión)

El intento previo de S16 escribió `proc.go`/`proc_test.go` correctos pero **se
colgó corriendo los tests** y nunca commiteó. La causa NO estaba en `enu.proc` sino
en una **grieta del desenrollado de cancelación de S08** que el idioma canónico de
§6 (`spawn` + `enu.task.cleanup(function() p:kill() end)`) fue el primero en
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

## Sin shell implícita (§6): decisión de seguridad estructural

`argv` es un **array**: `exec.Command(argv[0], argv[1:]...)` pasa los argumentos al
SO sin interpretación. Nadie invoca `/bin/sh`, así que `run(["echo","$HOME"])`
imprime el literal `$HOME`. Quien quiera shell la pone explícita
(`["sh","-c","..."]`). La inyección por shell no existe si no hay shell.

## Modelo de vida del proceso (la lógica 🔒, §6)

La vía **principal** es matarlo por `enu.task.cleanup` en quien lo crea: al terminar
la task —éxito, error o cancelación (S08)— el proceso muere con ella. Dos **redes
de seguridad**, no la vía principal: (1) el **finalizer del GC**
(`runtime.SetFinalizer`) mata un `Proc` que se quedó sin referencias en Lua —**no
determinista**, no se confía en ello—; (2) `Runtime.Close`→`stopAllProcs` mata
todos los vivos al cerrar la sesión (scheduler `procs`/`trackProc`, gemelo de
`watchers`/`timers`). Como `every`/`watch`, **un `Proc` vivo no cuenta para la
quiescencia**: esperar a que un subproceso muera para que `enu -e` retorne lo
colgaría. `*luaProc` implementa `ownedHandle` (S13): `reload` mata los procesos del
plugin que se recarga.

## Reparto de candados: `kill` con candado propio, nunca durante IO

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

## Pipes manuales (`os.Pipe`), no `cmd.StdoutPipe`

`cmd.StdoutPipe`/`StderrPipe` cierran el extremo de **lectura** en cuanto
`cmd.Wait` ve salir al proceso (os/exec lo documenta), lo que perdería datos si
reapeamos en cuanto el proceso muere. Con pipes propios el extremo de lectura es
**nuestro**: `Wait` no lo toca; lo cerramos al derribar el `Proc`. Así reaping
(`go p.wait()`, que recoge el zombi sin el cual `alive` lo reportaría vivo para
siempre) y streaming quedan **desacoplados**. Para stdin sí vale `StdinPipe` (es de
escritura; `close_stdin` lo cierra a mano, señalando EOF).

## `alive` (G17): existencia, no identidad

`enu.proc.alive(pid)` usa la "señal 0" (`kill(pid, 0)`): sin error o `EPERM` (existe
pero de otro usuario) → vivo; `ESRCH` → muerto; `pid <= 0` → no vivo. Informa de
**existencia, no de identidad**: un pid reciclado por el SO da `true` aunque sea
otro proceso. Es deliberado —para detectar locks de sesión huérfanos (sesiones.md
§6) basta saber si "alguien" tiene ese pid; la identidad la da el contenido del
lock (hostname, §7), no esta llamada—. No es ⏸ (consulta inmediata).

## `run`: el código de salida es dato, no error

Un `code != 0` **no lanza** (un `grep` sin coincidencias sale con 1 y eso es
información, como el `status` de `enu.http`). Lo que sí lanza: arranque fallido
(`ENOENT`/`EACCES`/`EIO`) o `timeout_ms` excedido (mata con SIGKILL, **drena** el
`Wait` del proceso muerto para no fugar su goroutine/pipes, y lanza `ETIMEOUT`).
`env` presente (aunque vacío) **reemplaza** el entorno heredado; ausente lo hereda.
