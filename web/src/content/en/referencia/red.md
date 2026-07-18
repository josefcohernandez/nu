---
title: enu.http / enu.ws — network
description: Buffered HTTP requests, response streaming (SSE as a first-class citizen), and websockets.
---

`enu.http` and `enu.ws` are the network. Available in workers **[W]**.
**Response streaming is first-class** (ADR-005): provider adapters live in
Lua and consume SSE with these primitives.

:::note[Examples with network access]
The calls on this page talk to external services: they can't run in an
environment without network access. The code is correct; its output depends
on the server.
:::

## `enu.http.request` ⏸ [W]

```
enu.http.request(opts) -> { status, headers, body }
```

Request with a **buffered** response. `opts`: `url`, `method?`, `headers?`,
`body?`, `timeout_ms?`, `tls?`, `proxy?` (see [TLS and proxy](#tls-and-proxy)),
`max_redirects?` (see [Redirects](#redirects)).
**Doesn't throw for status ≥ 400** (the status is just data); it throws
`ENET`/`ETIMEOUT` for transport failures.

```lua
enu.task.spawn(function()
  local res = enu.http.request{
    url = "https://api.example.com/items",
    method = "POST",
    headers = { ["content-type"] = "application/json" },
    body = enu.json.encode({ name = "nu" }),
    timeout_ms = 10000,
  }
  if res.status >= 400 then
    error({ code = "EHTTP", message = "server failure", detail = res.status })
  end
  return enu.json.decode(res.body)
end)
```

## `enu.http.stream` ⏸ [W]

```
enu.http.stream(opts) -> Stream
```

Returns **as soon as headers are received** (`Stream.status`,
`Stream.headers`), before the body. `opts.timeout_ms` covers up to the
headers; `opts.idle_timeout_ms?` throws `ETIMEOUT` if N ms pass without
receiving bytes (an SSE can go silent forever). It also accepts
`opts.max_redirects?` (see [Redirects](#redirects)).

```
Stream.status / Stream.headers
Stream:chunks() -> iterator  ⏸ [W]   -- raw body chunks as they arrive
Stream:events() -> iterator  ⏸ [W]   -- SSE parser: iterates { event?, data, id? }
Stream:close() [W]                   -- aborts the connection
```

Consuming an SSE (the pattern used by LLM providers):

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
    -- process the delta (e.g. re-emit it on the bus)
  end
  s:close()
end)
```

:::tip[Backpressure]
Streams are buffered in Go while Lua consumes at its own pace. The buffer has
a limit; exceeding it fails the stream with `EIO`. Consume without
accumulating.
:::

### TLS and proxy

`request` and `stream` accept `opts.tls = { ca_file?, insecure? }` (a
corporate CA per request) and `opts.proxy = "http://host:port"` (a specific
proxy for that request). The `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`
environment variables are respected by default. Global defaults live in the
`[net]` section of `enu.toml`, overridable per request with those two
options.

### Redirects

`request` and `stream` accept `opts.max_redirects?: number`: the budget of
redirects the client follows automatically. The default is **10**; with `0`
none is followed. When the budget runs out **no error is thrown**: the last
`3xx` response is delivered as data (with its `location` in `headers`),
consistent with "the status is just data". If you need to observe or validate
the chain hop by hop, pass `0` and follow it by hand — validating only the
initial URL is defeated by a `302` towards an internal destination.

On every **cross-host** hop — a change of host (name and port) relative to
the initial URL, or a scheme downgrade from `https` to `http` — the client
**strips every header you set in `opts.headers`** before resending the
request, on top of the ones already stripped across domains (`Authorization`,
`Cookie`), and doesn't restore them even if the chain returns to the initial
host. A different destination is a different party: it doesn't inherit your
credentials (`x-api-key` and friends) unless you decide so.

```lua
enu.task.spawn(function()
  -- Fetching a third-party URL: don't follow redirects blindly.
  local res = enu.http.request{ url = external_url, max_redirects = 0 }
  if res.status >= 300 and res.status < 400 then
    local target = res.headers.location
    -- validate `target` before deciding whether to follow
  end
end)
```

## `enu.ws.connect` ⏸ [W]

```
enu.ws.connect(url, opts?) -> Ws
  Ws:send(data, opts?)  ⏸                          -- opts.binary? = true sends a binary frame
  Ws:recv() -> data: string?, binary: boolean  ⏸   -- data = nil on close
  Ws:close()
```

Websocket client. **Text** frames require valid UTF-8 (enforced by the
protocol: a compliant server closes with 1007 if not); for arbitrary bytes
use `opts.binary = true` in `send`. The second value of `recv` tells the
incoming frame's type.

```lua
enu.task.spawn(function()
  local ws = enu.ws.connect("wss://example.com/socket")
  enu.task.cleanup(function() ws:close() end)

  ws:send(enu.json.encode({ type = "hello" }))
  while true do
    local msg = ws:recv()
    if msg == nil then break end   -- closed
    -- process msg
  end
end)
```

:::note[Reserved for the future]
`enu.net.tcp` (raw sockets) is reserved but **not v1**.
:::
