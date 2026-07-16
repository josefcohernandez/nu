---
title: "`enu.http.stream` + parser SSE (api.md §8, 🔒)"
type: "sesion"
id: "S20"
phase: 4
status: "cerrada"
---
# S20 — `enu.http.stream` + parser SSE (api.md §8, 🔒)

`enu.http.stream` es la respuesta HTTP en **streaming**, la otra cara de
`enu.http.request` (S19, buffereada). Devuelve un `Stream` **al recibir las
cabeceras** (`Stream.status`/`Stream.headers`), **sin leer el body**; el cuerpo se
itera trozo a trozo con `Stream:chunks()` (crudo) o `Stream:events()` (parser SSE
incorporado, la lógica 🔒). `stream.go` (handle + iteradores + apertura) y `sse.go`
(parser). Todo lo que S19 dejó listo se reusa tal cual: el parseo de `opts`
(`parseReqOpts`), el modelo de cliente reutilizable vs por-petición (`clientFor`,
con TLS/proxy de G12) y el mapeo de errores de transporte
(`classifyTransportError`/`httpError`, que ya deciden el código del core fuera del
token). Lo único que cambia es el consumo del body.

## El puente ⏸ y la goroutine de fondo (sin novedad de modelo)

`stream` suspende hasta las cabeceras; cada `next` de `chunks()`/`events()`
suspende hasta el siguiente trozo/evento. Una **sola** goroutine de fondo
(`readLoop`) lee el body a trozos y **jamás toca Lua**: empuja los bytes a una cola
interna y el consumidor los saca por el puente ⏸ (la `deliverFn` construye el
string/evento con el token recuperado). Es el mismo invariante de S04/S14/S16.

## El parser SSE incremental (la lógica 🔒)

La exigencia es que un evento puede llegar **partido entre varios trozos de red**
(TCP no respeta límites de evento ni de línea). El parser (`sseParser`) no asume
nada sobre los cortes: acumula bytes en un buffer, extrae solo las **líneas
completas** y guarda el resto para el próximo trozo. El caso delicado es un `\r` al
**final** del buffer: podría ser un `\r\n` partido entre chunks, así que se trata
como línea incompleta hasta saber qué le sigue (en EOF, `flush` lo cierra). Un
evento se **despacha en la línea en blanco**; un último evento sin su línea en
blanco final se despacha en EOF. Soporta los tres terminadores (`\n`/`\r\n`/`\r`),
`data:` múltiple **concatenado con `\n`** (sin `\n` final), `event:`/`id:`,
`retry:` y comentarios (`:` inicial) ignorados, y el espacio opcional tras los dos
puntos (se quita **uno**). `event`/`id` llevan flag `has*` para distinguir
"ausente" de "presente vacío" —no se inventa `event="message"`, que la espec deja
al consumidor—.

## El buffer acotado y el backpressure → `EIO`

El body se lee a una cola interna protegida por mutex+cond (NO el token: el
productor no toca Lua) con cuenta de **bytes pendientes** (`buffered`). Si un trozo
nuevo superaría `maxStreamBuffer` (8 MiB) porque Lua consume más lento de lo que el
servidor empuja, el stream **falla con `EIO`** en vez de crecer sin límite —es la
semántica de §8: el buffer tiene tope, desbordarlo es un error, no una espera
infinita ni una fuga—. Se eligió un tope por **bytes** (no por nº de trozos) porque
es lo que acota la memoria de forma predecible, y es determinista (no depende de
timing: con suficiente volumen siempre desborda).

## El idle timeout → `ETIMEOUT` (y por qué `timeout_ms` no cubre el body)

Un SSE puede quedarse **mudo para siempre** sin cerrar la conexión, así que un
plazo total cortaría un stream largo legítimo. Por eso `opts.timeout_ms` cubre
**solo hasta las cabeceras** (un `time.AfterFunc` que cancela el contexto si no
llegan a tiempo y se detiene al recibirlas), y el body lo protege
`opts.idle_timeout_ms?`: un `time.Timer` que se **re-arma con cada trozo** y, al
disparar, cancela el contexto —el `Read` mudo retorna y se rinde `ETIMEOUT`—. Se
distingue una cancelación por idle (`idleFired` → `ETIMEOUT`) de una por `close()`
del usuario (fin normal, no error).

## Close / cleanup / rastreo (la vida del stream)

`Stream:close()` cancela el contexto (desbloquea el `Read`), cierra el body, para
el idle-timer y despierta a los consumidores (que ven `ECLOSED`). Es **idempotente**
(`closeOnce`) y síncrono (no ⏸). El idioma de vida es el de §6:
`enu.task.cleanup(function() st:close() end)` —al cancelar/terminar la task, el
stream se cierra sin fuga de goroutines—. Como red de seguridad, `Runtime.Close`
cierra todos los streams vivos (`stopAllStreams`, rastreo en `scheduler.streams`,
gemelo de `procs`/`watchers`; un stream vivo **no** cuenta para la quiescencia).
**Decisión:** el `Stream` NO es un `ownedHandle` por dueño (como `Proc`): un stream
es de la **task que lo consume** (su vida es la del turno de IO), no del plugin, así
que se ata con `cleanup`, no con el registro de `reload`. Aun así se rastrea para
`Close`.

## `status` y `headers` como campos del userdata

El contrato pide `Stream.status`/`Stream.headers` como **campos** (no métodos) y
`Stream:chunks/events/close` como métodos. Se resuelve con un `__index` función que
devuelve `status`/`headers` directamente y delega el resto en la tabla de métodos.

## Tests 🔒

`sse_test.go` (parser puro, sin red ni token): tabla con data simple/multilínea,
sin espacio tras `:`, event+data+id, comentario ignorado, varios eventos, `\r\n` y
`\r`, evento sin event, data vacío, retry ignorado, id presente, último evento sin
línea en blanco final. **Cada caso se ejecuta con varias particiones del mismo
`raw`** (todo de una, **byte a byte**, de 2/3/7 bytes) → blinda eventos partidos
entre chunks. Más dos casos adversarios: un `\n\n` partido EXACTAMENTE entre trozos
y un `\r\n` partido entre trozos (el `\r` al final de un trozo, el `\n` al inicio
del siguiente: si se tratara el `\r` como terminador se vería una línea en blanco
espuria). `stream_test.go` (e2e con `httptest` + `http.Flusher`, herméticos): `events()`
{event,data,id}, evento emitido en N writes parseado como uno, `chunks()` crudo +
nil al fin, status 404 no lanza, **backpressure → `EIO`** (server vuelca ~12 MiB,
consumidor duerme 300 ms y desborda), **idle-timeout → `ETIMEOUT`** (body mudo >
`idle_timeout_ms`, con canal `release` que lo desbloquea al terminar el test, sin
goroutines colgadas), `close` idempotente, **close por cleanup al cancelar la
task** (mide `NumGoroutine` para descartar fuga), `stream` fuera de task →
`EINVAL`, `idle_timeout_ms` inválido → `EINVAL`. `CGO_ENABLED=0 go
build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test -race -timeout 120s
-count=2 ./internal/...` verde, sin flaky (no regresiona S01–S19). Binario `enu -e`
confirma e2e: status=200 + evento partido en varios writes parseado como uno
(`ping`/`hola mundo`) + evento sin `event` (data="fin").

**Sin hallazgos:** §8 bastó. Puntero ▶ avanza a **S21**.
