---
title: "La superficie [W] prometida en `api.md` §16 no llega a los workers: los wrappers Lua de `extraPreludio` no cruzan"
type: "hallazgo"
id: "G45"
status: "resuelto"
date: "2026-07-13"
origin: "auditoría integral 2026-07-12"
resolution: "AddPreludioW etiqueta los wrappers Lua worker-safe y spawnWorker los copia solo si sus thunks pasan los caps del worker."
affected: ["api.md §16 / vmwasm/worker.go"]
---
# G45 · La superficie [W] prometida en `api.md` §16 no llega a los workers: los wrappers Lua de `extraPreludio` no cruzan — `api.md` §16 / `vmwasm/worker.go` — **RESUELTO**

**Resolución** (2026-07-13; opción (a), construida el mismo día — detalle en la
fila `G45 (kernel)` de la bitácora de [implementacion.md](../plan/implementacion.md)).
`AddPreludio` gana la variante **`AddPreludioW(snippet, needs...)`** que etiqueta
el fragmento como [W] y declara los **thunks que envuelve** (`needs`, p. ej.
`"re._compile"`); `spawnWorker` copia al preludio del worker los etiquetados
**cuyos `needs` pasan `workerGrants`** — la misma autoridad que poda los thunks
poda sus wrappers, de modo que "lo no concedido no existe" (api.md §14) vale
también en la capa Lua: un worker sin la cap `http` no tiene `enu.http` ni como
tabla, y la detección de superficie por existencia (la que blinda el aislamiento
de subagentes, agente.md §9) sigue siendo fiable. Los siete wrappers [W] cruzan
(`log`, `re.compile`, `text.*`, `proc.spawn`, `ws.connect`, `http.stream`,
`search.grep`); `fs.watch` queda solo-principal con la variante sin marca, como
exigía la nota del problema. La construcción **destapó una segunda capa de la
misma grieta**: los **métodos de handle** (`Re:match`, `Proc:read_line`,
`GrepIter:next`...) tampoco cruzaban — `registerHandleDispatch` arrancaba el
pool del worker con el mapa de métodos vacío, así que incluso con los wrappers
copiados todo handle era inservible; el mapa del padre se copia entero, sin
podar (lo inalcanzable es inerte: un método solo se despacha sobre un handle ya
creado por un thunk concedido de la propia instancia). **No toca `api.md`**
(APILevel intacto): §16 se cumple ahora tal como se lee. Blindaje 🔒:
`worker_g45_test.go` (paridad con la tabla de §16 desde dentro de un worker,
wrappers operativos punta a punta y poda por caps también de los wrappers).

**Problema.** `api.md` §16 declara disponibles en workers ([W]) `re`, `ws`, `search`, `log`, `proc`, `http` y `text` completos, pero buena parte de esa superficie no son thunks del catálogo sino **wrappers Lua** registrados con `Pool.AddPreludio` (`enu.log.*`, `enu.re.compile`, `enu.text.wrap/markdown/highlight/diff`, `enu.proc.spawn` y sus métodos, `enu.ws.connect`, `enu.http.stream`, `enu.search.grep`). `spawnWorker` (`vmwasm/worker.go:137-179`) copia los módulos y las primitivas del registro pero **nunca `extraPreludio`**: el preludio del worker corre sin esos wrappers y los módulos quedan ausentes (verificado empíricamente: los seis probados, `nil`). Los thunks host sí cruzan; falta exactamente la capa de wrappers. Nota: el wrapper de `enu.fs.watch` también vive en `extraPreludio` pero watch NO es [W] — la solución debe discriminar, no copiar en bloque.

**Impacto.** Todo plugin que siga §16 y mueva trabajo pesado a un worker (el caso de uso central de los workers: búsqueda, render, subagentes) revienta con `attempt to index a nil value` al tocar cualquiera de esos módulos. La promesa de la superficie sagrada está incumplida en el código.

**Opciones.** (a) **Marca worker-safe por preludio:** `AddPreludio` gana una variante/opción que etiqueta el fragmento como [W] (`log`, `re`, `text`, `proc`, `ws`, `http.stream`, `search.grep` sí; `fs.watch`, ui no), y `spawnWorker` copia los etiquetados — el gating de `caps` sigue haciéndolo `workerGrants` sobre los thunks subyacentes (un wrapper sin su thunk falla con el error de cap, coherente). (b) **Rebajar §16** quitando el marcador [W] a esos módulos — rompe la promesa de la espec y castra a los workers; descartable salvo urgencia. (c) **Mover los wrappers al preludio base** del Pool (compartido por principal y workers) — no discrimina fs.watch ni futuros wrappers solo-principal sin añadir de todos modos una marca, que es la opción (a). Recomendación: (a), con test de paridad que recorra la tabla de §16 dentro de un worker.
