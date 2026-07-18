---
title: "El chat no se cierra hasta la siguiente tecla: `/quit` despacha `core:shutdown` desde una task, pero el driver solo lo sondea al llegar más input"
type: "hallazgo"
id: "G58"
status: "resuelto"
date: "2026-07-18"
origin: "suite e2e de plugins oficiales (e2e/chat_test.go, helper quitViaSlashCommand)"
affected: ["chat.md §8", "driver (driver.go)"]
---
# G58 · El chat no se cierra hasta la siguiente tecla: `/quit` despacha `core:shutdown` desde una task, pero el driver solo lo sondea al llegar más input — `chat.md` §8 / driver

**Problema.** [chat.md](../contracts/chat.md) §8 promete que «`Chat:quit` (y
`ctrl+c`) emiten `core:shutdown`: salir del chat **apaga el runtime**» — la
frase que sostiene que el conjunto oficial abre una única TUI y que salir de
ella no deja al usuario colgado en una capa inferior. Para los keymaps
síncronos (`ctrl+c`, `esc`, `q` del arranque degradado) eso se cumple al
instante. Pero `/quit` no es un keymap: el editor lo somete como cualquier
mensaje (`enter` → `Chat:submit`) y el despacho del comando corre dentro de
`enu.task.spawn` — una task asíncrona que, al terminar, emite `core:shutdown`
y enciende el flag `__driver_quit`. El bucle del driver (`drive()`,
`driver.go`) solo **sondea** ese flag dentro de `feed()` — tras procesar un
lote de input o al vencer el timeout de una secuencia ESC pendiente —, y en el
tramo entre pulsaciones está bloqueado en un `select { case <-chunks }` sin
timeout. Si nadie toca el teclado tras `/quit`, ese `select` nunca se
reevalúa: el flag está encendido pero nadie lo mira hasta que llega el
siguiente trozo de teclado (una tecla cualquiera, o el timeout de una
secuencia ESC pendiente). Posible misma raíz compartida con el apagado por
`core:shutdown` en general: los `enu.task.cleanup` no corren por esa vía, así
que el `.jsonl.lock` de la sesión (sesiones.md §6) tampoco se borra en este
camino de apagado — sin confirmar si es el mismo bug o uno contiguo.

**Impacto.** Tras escribir `/quit` y pulsar enter, el proceso de `enu` queda
vivo indefinidamente si el usuario no vuelve a tocar el teclado (piénsese en
un `/quit` disparado por automatización, o en el usuario que suelta el
terminal esperando que el proceso muera solo) — contradice literalmente «salir
del chat apaga el runtime». En un terminal interactivo normal el efecto es
casi invisible (cualquier tecla, incluida la siguiente pulsación accidental,
lo destraba), lo que probablemente explica que nadie lo hubiera notado antes
de que la suite e2e lo cronometrara con un timeout duro. `e2e/chat_test.go`
(helper `quitViaSlashCommand`) lo sortea con **nudges de `esc`** cada 150ms
hasta que el proceso muere, y lo documenta en comentario junto al mecanismo
exacto — sin trampa que quede satisfecha antes de que el driver se arregle de
verdad: si `/quit` empezara a apagar sin nudge, el test seguiría en verde
(el primer `select` con `<-exited` gana igual).

**Opciones exploradas.**
- **(a) Un canal/señal de apagado en el `select` del driver.** Añadir un caso
  `case <-quitSignal:` junto a `<-chunks` en el `select` bloqueante, para que
  `core:shutdown` interrumpa la espera de teclado en vez de depender de que
  llegue más input. Es el arreglo más directo a la causa raíz descrita arriba.
- **(b) Timeout periódico en el `select`.** Envolver la espera de `<-chunks`
  en un `select` con un `time.After` corto que reevalúe el flag
  `__driver_quit` sin necesitar una señal dedicada; más barato de escribir,
  pero introduce latencia (hasta el periodo del timeout) y un tick de CPU
  constante mientras el chat está inactivo, lo que ADR-004 (event loop, no
  polling) desaconsejaría sin justificarlo.
- **(c) Investigar primero si el `.jsonl.lock` comparte la causa.** Antes de
  tocar el driver, confirmar si el lock no se borra por la misma razón (el
  `core:shutdown` no da paso a los `cleanup` LIFO) o por una distinta —
  determina si el arreglo es uno o dos.

> ✅ **RESUELTO (2026-07-18) — opción (a).** Se elige la señal por canal, no el
> polling: ADR-004 fija «event loop, no polling», así que la opción (b) (tick de
> `time.After` constante con el chat inactivo) queda descartada por el mismo
> motivo por el que se plantea. El arreglo es interno del driver/host Go; **no
> añade ni cambia ninguna firma pública de `enu.*`** (la superficie sagrada de
> `api.md` queda intacta, `enu.version.api` no se toca).
>
> **Mecanismo.** La `Instance` wasm expone un canal `quitSignal` que se cierra
> —una sola vez, con `sync.Once`— la primera vez que se emite `core:shutdown` en
> su bus (`internal/vmwasm/vmwasm.go`: campo `quitSignal`/`quitOnce`, métodos
> `QuitSignal() <-chan struct{}` y `SignalQuit()`). El cierre lo dispara una
> **primitiva interna** `__driver_notify_quit` (registrada en
> `internal/runtime/driver.go` vía `registerDriverWasm`, cableada en
> `registerWasmCatalog`), que el handler de `core:shutdown` del driver invoca
> además de encender el flag `__driver_quit` ya existente. Es plomería del
> kernel de doble guion bajo, como `enu.__handle_call`: no aparece en `api.md`.
> El bucle `drive()` añade `case <-quitSignal:` a su `select`, de modo que un
> `core:shutdown` nacido en una task de fondo (`/quit`, que corre en
> `enu.task.spawn`) despierta la espera de teclado en el acto; el camino
> síncrono (ctrl+c, y el sondeo del flag en `feed`) sigue igual. Carreras: la
> primitiva firma bajo la goroutine que conduce la VM y el driver sólo **lee** un
> canal cerrado desde otra goroutine —seguro de observar—, con `sync.Once`
> ordenando el único cierre. Verificado con `-race`.
>
> **Ficheros.** `internal/vmwasm/vmwasm.go` (canal + métodos + init en
> `NewInstance`); `internal/runtime/driver.go` (`registerDriverWasm`, handler que
> llama a la primitiva, caso del `select`); `internal/runtime/runtime.go`
> (`registerWasmCatalog` cablea `registerDriverWasm`);
> `internal/runtime/driver_test.go` (`TestDriverQuitFromBackgroundTaskG58`, que
> reproduce el mecanismo de `/quit`: una tecla spawnea una task que emite
> `core:shutdown` y el bucle debe apagarse **sin más input**; falla si se
> neutraliza el arreglo); `e2e/chat_test.go` (`quitViaSlashCommand`: se eliminan
> los «nudges» de esc y su comentario, y el helper pasa a **exigir** que el
> proceso muera tras `/quit`+enter sin más teclado —blinda que G58 no regrese—).
>
> **Sobre el `.jsonl.lock` (opción (c), refutada como causa compartida).** No es
> el mismo bug: el lock **no** se orfana en el apagado por falta de `cleanup`
> LIFO, sino en el **ARRANQUE**, porque el `cleanup` de la task efímera que abre
> la sesión no puede ⏸ en su vía de liberación. Es un bug **distinto y contiguo**
> que se registra aparte como
> [G60](g60-el-lock-de-sesion-nace-huerfano.md) (abierto, en discusión). El arreglo de esta entrada es
> **solo** el despertar del driver; no toca el ciclo de vida del lock ni
> `enu.task.cleanup` ni las extensiones sessions/chat.

**Disparador de reapertura.** — (resuelto). Si al tocar el bucle del driver
(`driver.go`, `drive()`/`feed()`) o su camino de apagado reapareciera el
síntoma, reabrir citando esta resolución.
