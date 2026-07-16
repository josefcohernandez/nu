---
title: "input (`enu.ui.on_input` / `keymap`) (api.md §9.3)"
type: "sesion"
id: "S31"
phase: 6
status: "cerrada"
---
# S31 — input (`enu.ui.on_input` / `keymap`) (api.md §9.3)

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
  ya toma el token para pintar—. No reusé `enu.task.every` (es periódico y su `fn` es
  Lua) ni un `enu.fs` ⏸ (el despacho de input es síncrono, no una task): el callback
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
  paste de contenido no-texto se vuelca a `enu.fs.tmpdir` y se entrega como `paste` con
  `path`, no `text`; los bytes nunca cruzan a Lua (coherente con G11). El evento de
  input llega de forma SÍNCRONA al despacho (bajo el token, NO en una task ⏸), así que
  el volcado no puede ser un `enu.fs.write` ⏸. Lo resolví con `writePasteImage`: reusa
  `fs.ensureTmpdir` (la maquinaria de `enu.fs.tmpdir`) y un `os.WriteFile` directo de
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
