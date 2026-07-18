package runtime

// `enu.fs.watch` — observador del sistema de ficheros (api.md §5, §16, sesión S15,
// inventario 🔒). Vigila un `path` (fichero o directorio) y, cuando cambia, llama
// a un handler **síncrono** con un **lote** de eventos. A diferencia del resto de
// `enu.fs` (todo ⏸), `watch` **NO es ⏸ y es solo estado principal** (§16: no [W],
// no se registra en workers): no suspende la task que lo arranca —devuelve un
// `Watcher` y sigue—; los cambios llegan después, como los disparos de un `every`
// (S05), por el camino del handler síncrono sobre el token.
//
// LAS TRES PIEZAS DE LÓGICA NUESTRA (las que el inventario 🔒 manda blindar, G7):
//
//  1. ENTREGA EN LOTES + DEBOUNCE (G7). El SO entrega los cambios uno a uno
//     (fsnotify reenvía cada evento inotify/kqueue/ReadDirectoryChanges). Un `git
//     checkout` que toca miles de ficheros generaría miles de eventos; entregarlos
//     uno a uno ahogaría al handler. En su lugar, la goroutine de fondo **acumula**
//     los eventos en un buffer y arranca (o re-arma) un temporizador de
//     `debounce_ms`; cuando pasa ese tiempo **sin nuevos eventos**, vuelca TODO el
//     buffer como **un solo** `fn(events[])`. Así una ráfaga llega como un lote y
//     no como N llamadas —el batching/coalescing es lógica NUESTRA, no de fsnotify—.
//     El debounce es "trailing": el lote sale tras la calma, de modo que una ráfaga
//     continua se sigue agrupando (cada evento re-arma el reloj).
//
//  2. FILTRADO GITIGNORE (G7). Vigilar `node_modules/`, `.git/`, `target/`… es
//     ruido: lo que git ignora rara vez interesa a una herramienta de código. Con
//     `gitignore = true` (default), se parsea el `.gitignore` de la raíz observada
//     y **cada** evento cuyo path coincida con un patrón ignorado se descarta antes
//     de entrar al buffer —nunca llega al handler ni cuenta para el debounce—. El
//     `.git/` interno se ignora siempre (no aparece en `.gitignore` pero es ruido
//     universal de un repo).
//
//  3. RECURSIVO (alcance). fsnotify NO recursa por sí mismo (vigila directorios
//     concretos, no subárboles). Con `recursive = true` se **camina el árbol** al
//     arrancar y se añade cada subdirectorio (saltando los ignorados, para no
//     vigilar `node_modules/`); y un directorio **creado al vuelo** se añade al
//     watcher al verlo, de modo que los cambios bajo él también se reporten. El
//     alcance documentado: la recursión se reconstruye observando creaciones de
//     directorio; borrados de directorio los limpia el SO al desaparecer el watch.
//
// HILO Y CONCURRENCIA. La goroutine de fondo **jamás toca Lua**: recibe eventos
// del SO, filtra y acumula (datos Go puros), y para entregar el lote llama a
// `deliverBatch`, que **toma el token** (como `runSyncHandler` de los timers) y
// corre el handler en un thread efímero del estado principal bajo `pcall` por
// frontera (ADR-008). Es el mismo invariante que `every`: el trabajo de fondo va
// sin token; el código Lua, con token, en el estado principal. Cero data races
// (los paths cruzan como `string`, copiados; el handler se invoca bajo el token).
//
// QUIESCENCIA. Un `Watcher` activo **no** cuenta como trabajo de primer plano (no
// toca `pending`), igual que un `every`: un watcher nunca "termina", y haría que
// `enu -e` no volviera jamás. `Watcher:stop()` (o `Runtime.Close`) corta su
// goroutine y cierra el watcher del SO, sin fuga.

// watchKindCreate/Modify/Remove son los `kind` de cada evento del lote (§5):
// `{path, kind}`. fsnotify reporta operaciones por máscara de bits (`Op`); las
// mapeamos a estos tres `kind` estables del contrato.
const (
	watchKindCreate = "create"
	watchKindModify = "modify"
	watchKindRemove = "remove"
)

// watchEvent es un evento ya filtrado, listo para el lote: el path absoluto y su
// `kind`. Es un dato Go puro (sin Lua): cruza a la `deliverBatch` que lo convierte
// en la tabla `{path, kind}` bajo el token.
type watchEvent struct {
	path string
	kind string
}
