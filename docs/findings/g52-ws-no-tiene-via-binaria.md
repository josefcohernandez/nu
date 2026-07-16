---
title: "`enu.ws` no tiene vía binaria: `Ws:send` siempre manda frame de texto y `Ws:recv` no distingue el tipo de frame"
type: "hallazgo"
id: "G52"
status: "resuelto"
date: "2026-07-14"
origin: "auditoría integral (hallazgo A-38)"
resolution: "Ws:send gana opts.binary y Ws:recv devuelve un segundo valor binary, dando vía binaria explícita sin romper llamantes existentes."
affected: ["api.md §8 / runtime/ws.go"]
---
# G52 · `enu.ws` no tiene vía binaria: `Ws:send` siempre manda frame de texto y `Ws:recv` no distingue el tipo de frame — `api.md` §8 / `runtime/ws.go` — **RESUELTO**

**Resolución** (2026-07-14; adición a [api.md](../contracts/api.md) §8, nivel de API 2→3).
`Ws:send(data, opts?)` gana `opts.binary?: boolean`: con él, el frame sale
binario (`MessageBinary`); sin él, el comportamiento actual (frame de texto)
se conserva intacto — compatible con todo llamante existente. Y `Ws:recv()`
devuelve un **segundo valor** `binary: boolean` que distingue el tipo del
frame entrante (los llamantes actuales, que solo toman el primero, no notan
nada: adición pura en Lua). Se descartó la autodetección (mandar binario
cuando `data` no sea UTF-8 válido): un cambio de tipo de frame dependiente
del *contenido* es magia frágil — el mismo programa mandaría frames de tipo
distinto según el payload, y un consumidor estricto al otro lado vería un
protocolo incoherente. El tipo de frame es semántica del protocolo y la
declara quien envía. Implementación: kernel (`ws.go` + wrapper), con tests
que citan A-38/G52.

**Problema.** `ws.go:148` cablea `websocket.MessageText` en todo `send`:
bytes no-UTF-8 → un servidor conforme cierra con 1007 (RFC 6455 §5.6 exige
UTF-8 en frames de texto), y `api.md` no restringía `data` a texto ni ofrecía
alternativa. En recepción, `recv` ya entregaba los bytes de cualquier frame
(descarta el `MessageType`), así que un binario entrante *funcionaba* pero era
indistinguible de texto: un proxy/echo fiel era inexpresable. Detectado en la
auditoría integral (A-38 del informe).

**Impacto.** Cualquier protocolo WS binario (o mixto) era inutilizable desde
`enu`: MCP sobre WS con payloads comprimidos, protocolos de LSP/DAP framing
binario, o un simple relay fiel.

**Opciones.** (a) `opts.binary` en `send` + segundo retorno en `recv`
(elegida: explícita, mínima, retrocompatible). (b) Autodetección por validez
UTF-8 del payload (descartada: tipo de frame dependiente del contenido).
(c) Modo por conexión en `ws.connect` (descartada: los protocolos mixtos
existen y obligaría a dos conexiones o a un modo "raw" igual de explícito).
