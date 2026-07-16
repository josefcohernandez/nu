---
title: "`enu.plugin.reload` (best-effort, G2) (api.md §14)"
type: "sesion"
id: "S13"
phase: 2
status: "cerrada"
---
# S13 — `enu.plugin.reload` (best-effort, G2) (api.md §14)

## Registro de handles por dueño: general, no parche events+timers (decisión clave)

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
solo `enu.plugin.reload`, ya en §14).

## `untrack` en el camino manual, no en `stopTimer`/release (sin doble limpieza)

`luaTimer.release()` llama `stopTimer` (corta la goroutine). Pero el desregistro
del `ownerHandles` NO va en `release()` ni en `stopTimer`: va en `timerStop`/
`subCancel` —el camino **manual**—. Razón: `releaseOwnerHandles` (la vía de reload)
ya borra la entrada del dueño del mapa antes de iterar; si `release()` también
tocara el registro, sería doble limpieza (y, peor, mutar el mapa a media
iteración). Así el registro tiene un solo dueño de la mutación por camino: reload
borra en bloque; cancel/stop a mano quitan uno. Ambos idempotentes (un handle que
ya no está no da error). Sin fuga y sin carrera (todo bajo el token).

## Fix: la auto-cancelación de un `once` también desregistra (sin fuga)

Una revisión encontró un camino de desregistro que faltaba: cuando un
`enu.events.once` se **dispara**, `dispatch` (events.go) lo auto-cancela
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

## Caché de require: enumerar el `lua/` del plugin, no adivinar por package.loaded

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

## `reload` es ⏸ aunque hoy todo es síncrono bajo el token

§14 marca `reload` como ⏸. Hoy todos sus pasos son trabajo del estado principal
bajo el token (emit síncrono, soltar handles, re-correr el init con `L.LoadFile`),
sin IO de fondo. Se respeta el marcador igualmente: (a) reserva que leer el init
pueda volverse ⏸ real en el futuro sin cambiar la firma; (b) homogeneidad —una
herramienta de desarrollo se invoca desde una task como el resto de async—. La
detección es la de §1.3 (`L == host` → `EINVAL`). El `init.lua` del usuario
(dueño "user") no es recargable por esta vía: re-correrlo sería re-arrancar, fuera
del alcance de G2 (que es "recargar un plugin").
