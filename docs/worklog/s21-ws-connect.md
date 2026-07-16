# S21 — `enu.ws.connect` (api.md §8; cierra Fase 4 — Red, CP-5)

Websockets: `enu.ws.connect(url, opts?) -> Ws` ⏸, `Ws:send(data)` ⏸,
`Ws:recv() -> string?` ⏸ (**`nil` al cerrar**), `Ws:close()` (`ws.go`). Es el
complemento full-duplex de `enu.http.stream` (S20) y la última pieza de la Fase 4.

## La librería WebSocket: `github.com/coder/websocket`

Se eligió `github.com/coder/websocket` (la continuación de `nhooyr.io/websocket`)
frente a `github.com/gorilla/websocket`. Motivos:

- **Puro-Go y sin dependencias transitivas.** `go mod tidy` añade SOLO
  `coder/websocket` (no arrastra nada más), así que `CGO_ENABLED=0` sigue verde
  (ADR-001) y el binario estático no engorda con un árbol de deps.
- **API basada en `context.Context`.** `Dial(ctx, url, opts)`,
  `conn.Read(ctx)`, `conn.Write(ctx, typ, p)`: el contexto es justo lo que el
  puente ⏸ y la cancelación de tasks necesitan —cancelar el contexto desbloquea
  un `Read`/`Write` colgado, que es como `close()` aborta el IO de fondo—.
- **Serializa las escrituras por dentro.** `gorilla/websocket` obliga al
  llamante a un mutex propio para no intercalar escrituras; `coder/websocket` ya
  lo gestiona, lo que encaja con que `send` pueda correr desde la goroutine de
  fondo de su `suspend` sin coordinación extra por nuestra parte.

## El modelo de IO: NO hay goroutine permanente de lectura (a diferencia de S20)

El `Stream` de S20 necesita una goroutine de fondo permanente porque el body de
un SSE **llega aunque nadie lo pida** (hay que leerlo para no bloquear al
servidor y para aplicar backpressure). Un websocket es distinto: es
**petición-respuesta dirigida por el consumidor** —Lua llama `recv()` cuando
quiere el siguiente mensaje—. Así que cada `send`/`recv` hace su `Write`/`Read`
bloqueante **dentro de la goroutine de fondo de SU propio `suspend`** y no hay
ningún productor de fondo corriendo entre llamadas. Es el patrón de
`Proc:read_line`/`Proc:write` (S16), no el del `Stream`. Más simple y sin colas:
el único estado compartido entre la goroutine de fondo y `close()` es el flag
`closed` (bajo `mu`, no el token: el productor jamás toma el token).

## `recv() -> nil al cerrar`: distinguir cierre de error de transporte

El criterio de hecho de S21 es "recv tras cierre da nil". `recv()` devuelve el
mensaje, o **`nil` cuando la conexión se cierra**: ordenadamente (la otra punta
mandó un frame de cierre normal) o porque nosotros llamamos `Ws:close()`. La
distinción "cierre → nil (fin de stream)" vs "fallo real → lanza `ENET`" la hace
`websocket.CloseStatus(err)`: un cierre `StatusNormalClosure` (1000),
`StatusGoingAway` (1001) o `StatusNoStatusRcvd` (1005, la otra punta cortó sin
código) es fin de stream; cualquier otro error de lectura es transporte. Además,
si fuimos nosotros quienes cerramos (flag `closed`), el `Read` abortado por
nuestro `cancel` también es fin de stream, no error.

Detalle de robustez (lo descubrió un test): tras detectar un cierre ordenado,
`recv` marca el handle cerrado (llama a `close()`, idempotente). Sin esto, un
`recv()` posterior reintentaría un `conn.Read` sobre una conexión ya cerrada, que
devuelve un error **distinto** (no clasificable como cierre normal) y se rendiría
como `ENET` en vez de seguir dando `nil`. Con el flag puesto, todo `recv`
posterior corta en seco a `nil`.

## Connect: el `timeout_ms` cubre solo el handshake

Como en el `stream` de S20, el plazo del handshake no debe cortar la vida de la
conexión (un websocket es de larga duración). `dialWs` usa un `context.WithCancel`
para la conexión (sin plazo, lo cancela `close()`) y, encima, un
`context.WithTimeout(connCtx, timeout)` SOLO para `Dial`, que se desecha
(`dialCancel`) al volver. Un fallo del handshake → `ENET`; su timeout →
`ETIMEOUT`, distinguido por `dialCtx.Err()` vía `classifyTransportError` (reusado
de S19). `send` envía **texto** por defecto (`MessageText`: el provider habla JSON
sobre texto, ADR-005); `SetReadLimit(32 MiB)` acota un mensaje entrante gigante
(el default de la lib, 32 KiB, es poco para un turno grande de un provider).

## Close / cleanup / rastreo (la vida del websocket)

Idéntico al `Stream` de S20: `Ws:close()` es idempotente (`closeOnce`), marca
`closed`, manda el frame de cierre normal (best-effort) y cancela el contexto
(desbloquea cualquier IO colgado). El idioma de vida es
`enu.task.cleanup(function() w:close() end)`; la red de seguridad es
`Runtime.Close` → `stopAllWs` (rastreo en `scheduler.ws`, gemelo de
`scheduler.streams`). Un `Ws` vivo **no** cuenta para la quiescencia (la otra
punta puede no cerrar nunca) y NO es un `ownedHandle` por dueño (su vida es la
del turno de IO, se ata con `cleanup`, no con `reload`).

## Tests (`ws_test.go`, herméticos; CP-5 en `cp5_test.go`)

`enu.ws` NO está en el inventario 🔒 (es un wrapper sobre la lib + el puente ⏸),
pero su lógica propia se blinda igual con servidores **locales**
(`net/http/httptest` + `websocket.Accept`): eco round-trip (varios mensajes en
orden), recv → nil tras cierre del servidor (y siguientes recv siguen dando nil),
recv → nil tras `Ws:close()` local, `send` tras close → `ECLOSED`, puerto cerrado
→ `ENET`, handshake mudo (servidor TCP que no contesta) → `ETIMEOUT`, close
idempotente, **close por cleanup al cancelar la task** (sin fuga de goroutines,
medida con `NumGoroutine`), fuera de task → `EINVAL`, `url`/`opts`/`headers`/
`timeout_ms` malos → `EINVAL`.

**CP-5 (cierra Fase 4)** prueba las cuatro capacidades de red juntas: (a)
`http.request` trata un 404 como dato; (b) un SSE consumido con `Stream:events()`
**mientras otra task contadora avanza** —se comprueba `ticks > 0` mientras el SSE
se consume, demostrando que el event loop NO se bloquea (el puente ⏸ libera el
token)—; (c) un ws de eco round-trip; (d) un consumidor lento que desborda el
buffer → `EIO` (backpressure de S20).

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S20). Binario `enu -e` confirma e2e contra un servidor de eco local: `send`/
`recv` round-trip (`hola`/`mundo`), recv tras cerrar → `nil`, puerto cerrado →
`ENET`.

**Sin hallazgos:** §8 bastó. **Fase 4 (Red) cerrada — CP-5 verde.** Puntero ▶
avanza a **S22** (Fase 5 — Texto y búsqueda).
