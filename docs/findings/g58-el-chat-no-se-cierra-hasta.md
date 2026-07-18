---
title: "El chat no se cierra hasta la siguiente tecla: `/quit` despacha `core:shutdown` desde una task, pero el driver solo lo sondea al llegar más input"
type: "hallazgo"
id: "G58"
status: "abierto"
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

**Opciones a explorar** (no se decide en esta entrada; el arreglo queda
pospuesto — ver contexto de esta ronda):
- **(a) Un canal/señal de apagado en el `select` del driver.** Añadir un caso
  `case <-shutdownCh:` junto a `<-chunks` en el `select` bloqueante, para que
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

**Disparador de reapertura.** Cuando se toque el bucle del driver del chat
(`driver.go`, `drive()`/`feed()`) o su camino de apagado por
`core:shutdown`.
