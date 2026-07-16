# S19 — `enu.http.request` (api.md §8)

Primera sesión de la **Fase 4 (Red)**. Implementa **solo** `enu.http.request(opts)
-> {status, headers, body}` ⏸ (§8): una petición HTTP **buffereada** sobre el
puente `suspend` de S04 (ADR-011), el mismo patrón que `enu.fs`/`enu.proc` —el IO
(la petición) va en la goroutine de fondo, que **jamás toca Lua**; la respuesta o
el error cruzan a Lua solo en la `deliverFn`, bajo el token recuperado—. `stream`
(S20) y `ws` (S21) se quedan fuera a propósito. APILevel sigue en 1 (§8 ya estaba
en api.md). **Sin hallazgos G##:** §8 bastó tal cual.

## El status es DATO, no error (la semántica clave de §8)

Un 404 o un 500 devuelven `{status=404, ...}` **sin lanzar** —el código de estado
es información que el llamante decide cómo tratar (un adaptador de provider
distingue 429 de 500 para reintentar, ADR-005)—. Solo los fallos de **transporte**
lanzan: conexión rechazada / DNS / reset → `ENET`; expirar `timeout_ms` →
`ETIMEOUT`; `url` ausente/inválida y otros usos malos → `EINVAL`. Esto invierte el
default de muchos clientes HTTP (que lanzan por 4xx/5xx) y es deliberado: el
status pertenece a la lógica de la extensión, no al transporte.

## El modelo del cliente: reutilizable vs por-petición (la decisión de diseño)

**Un `*http.Client` reutilizable para el caso común, uno efímero por-petición para
los casos con TLS/proxy a medida.** El caso común (sin `opts.tls`, sin
`opts.proxy`, sin CA/proxy de `[net]`) reusa un único cliente cacheado en
`httpState` (creado perezosamente, candado para la carrera entre goroutines de
fondo): así se aprovecha el **pool de conexiones keep-alive** entre peticiones,
que es lo que hace eficiente hablar repetidamente con el mismo endpoint (el caso
del agente: muchas llamadas al mismo provider). Una petición que pide una CA
distinta, `insecure` o un proxy propio necesita su propio `tls.Config`/`Transport`,
así que construye un cliente **efímero solo para ella**; no se cachean los
efímeros (son la excepción, y cachearlos por combinación de opciones añadiría
complejidad sin beneficio claro en v1). El plazo NO va por `client.Timeout` sino
por un `context.WithTimeout` por petición: así `ctx.Err()` distingue limpiamente
el timeout (`ETIMEOUT`) del resto de fallos de transporte (`ENET`).

## TLS y proxy (G12)

`opts.tls = {ca_file?, insecure?}`: `ca_file` añade una CA corporativa **a la raíz
del sistema** (parte de `x509.SystemCertPool` y le añade el PEM —"añadir una CA",
no reemplazar la confianza—); `insecure=true` desactiva la verificación (entornos
de prueba, expuesto a sabiendas). `opts.proxy` fija un proxy por petición; sin él,
`http.ProxyFromEnvironment` respeta `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` del
entorno. **Defaults globales en `[net]` de `enu.toml`** (`ca_file`, `proxy`),
sobreescribibles por petición —la precedencia es: opción de la petición > `[net]` >
entorno/sistema—. La config `[net]` se lee en `New` (config_toml.go) y se pasa a
`httpState`; un `enu.toml` mal formado no la aplica (su error se aplaza a `Boot`,
como el resto de la config).

## Headers de respuesta con valores múltiples → unir por ", "

`http.Header` es nombre→[]valor (el modelo del protocolo permite headers
repetidos); el contrato (§8) pide una tabla nombre→valor. **Decisión: unir los
valores repetidos por `", "`** —la forma canónica de combinar headers según RFC
7230 §3.2.2, válida para casi todos—. La excepción notable es `Set-Cookie` (no se
parte/une por comas); un consumidor que necesite cookies crudas no tiene una API
buffereada que se lo dé bien (usará `stream` cuando llegue, S20, o no le sirve este
camino). Es predecible y reversible para el caso común (un solo valor pasa
intacto) y evita exponer arrays donde casi todo el código espera un string.

## Validación de `opts` (a `EINVAL`, antes de suspender)

`opts` no-tabla, `url` ausente/vacía, `timeout_ms` no positivo o de tipo
equivocado, `headers`/`tls` de tipo equivocado → `EINVAL` lanzado en el estado
principal (bajo el token), **antes** de suspender. La validación fina de la URL
(sintaxis, esquema) se delega a `http.NewRequestWithContext` en la goroutine de
fondo; un error suyo se rinde también como `EINVAL` (uso inválido). `timeout_ms`
ausente → un techo por defecto de 30 s (una petición de red sin plazo podría
colgar una task para siempre); un `0` explícito se trata como inválido (el contrato
no lo define como "infinito").

## Tests (`http_test.go`, herméticos con `net/http/httptest`)

Todos contra servidores **locales** (`httptest`), **sin red externa** → no flaky
por DNS ni endpoints remotos: 200 con body + headers de petición/respuesta
correctos; 404 y 500 **no lanzan** (status como dato); POST con body recibido en el
server; **fallo de transporte** (servidor cerrado → puerto cerrado) → `ENET`;
**timeout** (server que duerme >> `timeout_ms`, con un canal `release` que lo
desbloquea al terminar el test, sin goroutines colgadas) → `ETIMEOUT`; `url`
ausente/vacía/`opts` no-tabla → `EINVAL`; `timeout_ms` negativo/no-numérico →
`EINVAL`; `request` fuera de task → `EINVAL` (es ⏸); **TLS G12** contra
`httptest.NewTLSServer`: sin `insecure` falla (CA desconocida → `ENET`), con
`insecure=true` pasa, y con la CA del server como `ca_file` pasa **sin** `insecure`;
headers múltiples unidos por ", "; 5 peticiones concurrentes progresan en paralelo
(red anti-data-race del cliente reutilizable). `CGO_ENABLED=0 go build`/`go
vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test -race -timeout 120s -count=2
./internal/...` verde, sin flaky. Binario `enu -e` confirma de extremo a extremo:
GET → status=200, 404 → no lanza (status=404), puerto cerrado → `ENET`, `url`
vacía → `EINVAL`.

## Lo que reusan S20 (stream) y S21 (ws)

S20 reusará el modelo del cliente (reutilizable vs por-petición), el parseo de
`opts` TLS/proxy (G12) y el mapeo de errores de transporte (`classifyTransportError`,
`httpError` con su código del core ya decidido fuera del token) —cambiará que NO
bufferiza el body: devuelve un `Stream` al recibir cabeceras y expone `chunks()`/
`events()` (parser SSE, 🔒) con backpressure acotado (→ `EIO`)—. S21 (ws) se
construye sobre el mismo puente ⏸ pero con una librería de websockets; comparte el
parseo de `opts` y el mapeo `ENET`/`ETIMEOUT`.

**Sin hallazgos:** §8 bastó. Puntero ▶ avanza a **S20**.
