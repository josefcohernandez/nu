# S15 — `enu.fs.watch` (api.md §5, §16)

## `watch` NO es ⏸ y es solo estado principal (§16)

A diferencia del resto de `enu.fs` (todo ⏸), `watch` **no suspende**: arma el
observador y devuelve el `Watcher` en el acto. Y es **solo estado principal**
(§16): el handler es **síncrono** (como `every`/`on`), corre en el loop del estado
principal; el bus de entrega (token + thread efímero) vive ahí. Por eso `watch` no
es "esperar un resultado" sino "registrar un observador que dispara luego", que es
justo lo que NO encaja en ⏸.

**Corrección (retirado el guard host-only):** "solo estado principal" (§16) significa
**"no en workers"** —donde `fs.watch` ni siquiera se registra (S34)—, **no** "no en
tasks". Las tasks corren en el event loop del estado principal y comparten el `enu`
global, así que `watch` es invocable indistintamente desde el chunk, un handler
síncrono, el `init.lua` **o desde dentro de una task**, exactamente igual que sus
hermanos `every`/`on` (que tampoco distinguen host de task): registra el `Watcher`
síncronamente y devuelve sin suspender. Se eliminó el guard `if L != rt.L { EINVAL }`
de `fsWatch` —era una desviación de §16— y el test cómplice `TestWatchOutsideMainState`
se reescribió como `TestWatchFromTaskWorks` (verifica que un `watch` arrancado dentro
de una task funciona y entrega al menos un lote tras un cambio de fichero). El bloqueo
en workers ya lo garantiza que `fs.watch` no se registre en su LState (S34), sin
necesidad de guard alguno.

## El debounce + batching es lógica NUESTRA, no de fsnotify (G7)

fsnotify reenvía cada evento del SO uno a uno. El **coalescing en lotes** lo
hacemos nosotros: la goroutine de fondo acumula los eventos en un buffer y arma (o
re-arma) un `time.Timer` de `debounce_ms`; cuando pasa ese tiempo **sin nuevos
eventos**, vuelca TODO el buffer como **un solo** `fn(events[])`. El debounce es
**trailing y coalescente** (cada evento reinicia el reloj), así que una ráfaga
continua —un `git checkout` que toca miles de ficheros— se sigue agrupando y sale
como UN lote, no como N llamadas (criterio de hecho de S15, G7). `debounce_ms`
default 50 (§5); negativo → `EINVAL`. El reset del timer usa el patrón estándar
(`Stop` + drenar `C` si ya disparó) para no dejar un disparo viejo en el canal.

## Filtrado gitignore (G7): al añadir Y al filtrar eventos

`gitignore = true` (default §5) parsea el `.gitignore` de la raíz observada
(`github.com/sabhiram/go-gitignore`, puro-Go: `CompileIgnoreFile` + `MatchesPath`).
El filtrado ocurre en **dos sitios**: (1) al **añadir** subdirectorios en el modo
recursivo, se saltan los ignorados (no se VIGILA `node_modules/`: gastaría
descriptores y daría ruido); (2) al **clasificar** cada evento, un path ignorado se
descarta antes de entrar al buffer —ni llega al handler ni cuenta para el debounce—.
Un `.gitignore` ausente no es error (no se ignora nada por esa vía). El `.git/`
interno se ignora **siempre** (ruido universal de un repo), comprobando cualquier
componente `.git` en la ruta. Decisión de librería: go-gitignore es simple,
puro-Go y correcto para el criterio de hecho (basename, glob `*.log`, dir `build/`);
parsear `.gitignore` a mano sería reinventarlo peor.

## Alcance de `recursive`

fsnotify NO recursa: vigila directorios concretos. Con `recursive = true` se
**camina el subárbol** al arrancar (`filepath.WalkDir`) añadiendo cada subdirectorio
no ignorado (`SkipDir` sobre los ignorados, para no descender en `node_modules/`); y
un directorio **creado al vuelo** se añade al watcher al ver su evento `create` (si
es dir y no está ignorado), de modo que los cambios bajo él también se reporten. El
alcance documentado: la recursión se **reconstruye observando creaciones de
directorio**; un fichero suelto (`path` no es dir) se vigila a través de su
directorio padre, filtrando en `classify` los eventos que no le conciernen. Errores
al caminar una entrada concreta son best-effort (se salta, no rompe el watch).

## Entrega bajo el token; quiescencia como `every`; integración con el registro de handles

La goroutine de fondo **jamás toca Lua**: filtra y acumula datos Go; para entregar
el lote llama a `deliverBatch`, que **toma el token** (como `runSyncHandler` de
timers.go) y corre el handler en un thread efímero del estado principal bajo `pcall`
por frontera (ADR-008) —cero data races: los paths cruzan como `string` copiadas y
el handler se invoca con el token tomado—. Un `Watcher` activo **no** cuenta para la
quiescencia (no toca `pending`), igual que un `every`: un watcher nunca termina y
colgaría `enu -e`. `Watcher` implementa `ownedHandle` (handles.go, S13): `watch` lo
etiqueta con `currentOwner()` y lo `track`-a; `Watcher:stop()` lo `untrack`-a (sin
fuga en el registro) y `enu.plugin.reload` lo suelta vía `release()` —"reload no deja
handlers huérfanos" (G2)—. `stop` (y `Runtime.Close` vía `stopAllWatchers`) corta la
goroutine (`stopCh`, idempotente con `stopOnce`) y cierra el watcher del SO (`fsw.
Close`, libera descriptores), sin fuga de goroutines. `deliverBatch` atiende a
`stopCh` mientras espera el token: tras `stop`, ningún lote más (contrato de `stop`).

## Deps añadidas (puras-Go, `CGO_ENABLED=0` intacto)

`github.com/fsnotify/fsnotify` (filewatching pura-Go; su único indirecto es
`golang.org/x/sys`, también puro-Go) y `github.com/sabhiram/go-gitignore` (parseo de
`.gitignore`). Ninguna usa cgo: el binario estático (ADR-001) sigue compilando con
`CGO_ENABLED=0`.
