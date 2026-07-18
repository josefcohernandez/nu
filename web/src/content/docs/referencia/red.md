---
title: enu.http / enu.ws — red
description: Peticiones HTTP buffereadas, streaming de respuesta (SSE de primera clase) y websockets.
---

`enu.http` y `enu.ws` son la red. Disponibles en workers **[W]**. El **streaming de
respuesta es de primera clase** (ADR-005): los adaptadores de providers viven en
Lua y consumen SSE con estas primitivas.

:::note[Ejemplos con red]
Las llamadas de esta página hablan con servicios externos: no se pueden ejecutar
en un entorno sin red. El código es correcto; su salida depende del servidor.
:::

## `enu.http.request` ⏸ [W]

```
enu.http.request(opts) -> { status, headers, body }
```

Petición con respuesta **buffereada**. `opts`: `url`, `method?`, `headers?`,
`body?`, `timeout_ms?`, `tls?`, `proxy?` (ver [TLS y proxy](#tls-y-proxy)),
`max_redirects?` (ver [Redirects](#redirects)).
**No lanza por status ≥ 400** (el status es un dato); lanza `ENET`/`ETIMEOUT`
por fallos de transporte.

```lua
enu.task.spawn(function()
  local res = enu.http.request{
    url = "https://api.example.com/items",
    method = "POST",
    headers = { ["content-type"] = "application/json" },
    body = enu.json.encode({ nombre = "nu" }),
    timeout_ms = 10000,
  }
  if res.status >= 400 then
    error({ code = "EHTTP", message = "fallo del servidor", detail = res.status })
  end
  return enu.json.decode(res.body)
end)
```

## `enu.http.stream` ⏸ [W]

```
enu.http.stream(opts) -> Stream
```

Devuelve **al recibir las cabeceras** (`Stream.status`, `Stream.headers`), antes
del body. `opts.timeout_ms` cubre hasta las cabeceras; `opts.idle_timeout_ms?`
lanza `ETIMEOUT` si pasan N ms sin recibir bytes (un SSE puede quedarse mudo para
siempre). Acepta también `opts.max_redirects?` (ver [Redirects](#redirects)).

```
Stream.status / Stream.headers
Stream:chunks() -> iterator  ⏸ [W]   -- trozos crudos del body según llegan
Stream:events() -> iterator  ⏸ [W]   -- parser SSE: itera { event?, data, id? }
Stream:close() [W]                   -- aborta la conexión
```

Consumir un SSE (el patrón de los providers de LLM):

```lua
enu.task.spawn(function()
  local s = enu.http.stream{
    url = "https://api.example.com/v1/stream",
    headers = { authorization = "Bearer ..." },
    idle_timeout_ms = 30000,
  }
  for ev in s:events() do
    if ev.data == "[DONE]" then break end
    local delta = enu.json.decode(ev.data)
    -- procesar el delta (p. ej. re-emitirlo en el bus)
  end
  s:close()
end)
```

:::tip[Backpressure]
Los streams se bufferizan en Go mientras Lua consume a su ritmo. El buffer tiene
límite; al excederlo el stream falla con `EIO`. Consume sin acumular.
:::

### TLS y proxy

`request` y `stream` aceptan `opts.tls = { ca_file?, insecure? }` (CA corporativa
por petición) y `opts.proxy = "http://host:puerto"` (proxy concreto para esa
petición). Las variables `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` del entorno se
respetan por defecto. Los defaults globales viven en la sección `[net]` de
`enu.toml`, sobreescribibles por petición con esas dos opciones.

### Redirects

`request` y `stream` aceptan `opts.max_redirects?: number`: el presupuesto de
redirecciones que el cliente sigue automáticamente. El default es **10**; con
`0` no se sigue ninguna. Agotado el presupuesto **no se lanza error**: se
entrega la última respuesta `3xx` como dato (con su `location` en `headers`),
coherente con "el status es dato". Quien necesite observar o validar la cadena
salto a salto pone `0` y la sigue a mano — validar solo la URL inicial se
evade con un `302` hacia un destino interno.

En cada salto **cross-host** — cambio de host (nombre y puerto) respecto de la
URL inicial, o degradación de esquema `https` → `http` — el cliente **recorta
todas las cabeceras que pusiste en `opts.headers`** antes de reenviar la
petición, además de las que ya se recortan entre dominios (`Authorization`,
`Cookie`), y no las restaura aunque la cadena regrese al host inicial. Un
destino distinto es un interlocutor distinto: no hereda tus credenciales
(`x-api-key` y compañía) sin que tú lo decidas.

```lua
enu.task.spawn(function()
  -- Fetch de una URL de terceros: no seguir redirects a ciegas.
  local res = enu.http.request{ url = url_externa, max_redirects = 0 }
  if res.status >= 300 and res.status < 400 then
    local destino = res.headers.location
    -- validar `destino` antes de decidir si se sigue
  end
end)
```

## `enu.ws.connect` ⏸ [W]

```
enu.ws.connect(url, opts?) -> Ws
  Ws:send(data, opts?)  ⏸                          -- opts.binary? = true manda frame binario
  Ws:recv() -> data: string?, binary: boolean  ⏸   -- data = nil al cerrar
  Ws:close()
```

Websocket cliente. Los frames de **texto** exigen UTF-8 válido (lo impone el
protocolo: un servidor conforme cierra con 1007 si no); para bytes arbitrarios
usa `opts.binary = true` en `send`. El segundo valor de `recv` distingue el tipo
del frame entrante.

```lua
enu.task.spawn(function()
  local ws = enu.ws.connect("wss://example.com/socket")
  enu.task.cleanup(function() ws:close() end)

  ws:send(enu.json.encode({ tipo = "hola" }))
  while true do
    local msg = ws:recv()
    if msg == nil then break end   -- cerrado
    -- procesar msg
  end
end)
```

:::note[Reservado para futuro]
`enu.net.tcp` (sockets crudos) está reservado pero **no es v1**.
:::
