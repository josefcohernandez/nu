---
title: nu.events — bus de eventos
description: El bus de eventos genérico de nu — on, once, emit, y la semántica de despacho síncrono.
---

`nu.events` es un bus de eventos genérico. El core no sabe lo que es un agente:
este bus es donde las extensiones definen sus propios hooks. Solo está disponible
en el **estado principal** (no en workers).

## Convención de nombres

Los nombres son `"namespace:evento"`, en **dos niveles**. El core reserva solo lo
suyo —`core:` y `ui:`—; cualquier otro namespace es de un plugin por convención
(namespace = nombre del plugin). Como el loader garantiza que el nombre de un
plugin es único, dos extensiones no colisionan. Las oficiales no tienen
privilegio: `agent:` es el namespace del plugin `agent`, igual que `mi-plugin:`
es el tuyo.

## `nu.events.on`

```
nu.events.on(name, fn) -> Sub
  Sub:cancel()
```

Suscribe `fn` al evento `name`. Los handlers son **síncronos**, corren en orden
de registro y cada uno bajo `pcall` (un handler que lanza no tumba a los demás).
Devuelve un `Sub` con `Sub:cancel()`.

```lua
local sub = nu.events.on("mi-plugin:guardado", function(payload)
  -- reaccionar; síncrono, así que para IO: nu.task.spawn(...)
  nu.log.info("guardado: %s", payload.path)
end)
-- ...
sub:cancel()
```

## `nu.events.once`

```
nu.events.once(name, fn) -> Sub
```

Como `on`, pero se dispara **una sola vez** y se cancela sola.

```lua
nu.events.once("core:ready", function()
  nu.log.info("runtime listo")
end)
```

## `nu.events.emit`

```
nu.events.emit(name, payload?)
```

Despacha el evento de forma **síncrona** en el estado principal. El `payload` es
opcional (una tabla cualquiera).

```sh
nu -e '
local visto
nu.events.on("demo:hola", function(p) visto = p.quien end)
nu.events.emit("demo:hola", { quien = "nu" })
return visto
'
```

```
nu
```

## Semántica de despacho

Reglas finas, importantes cuando un handler modifica suscripciones:

- Cada `emit` corre sobre la **foto** de suscriptores tomada al emitir.
- **Cancelar** surte efecto inmediato: si aún no te tocó, ya no corres.
- Los **suscritos durante** un despacho solo ven eventos futuros.
- Los `emit` **anidados se encolan** y se despachan al terminar el actual
  (anchura, no profundidad): sin recursión ni desbordes. Un ping-pong infinito
  entre plugins se vuelve un bucle plano que el watchdog corta.

## Eventos que emite el core

`core:ready`, `core:shutdown`, `core:plugin.loaded`, `core:plugin.unload`,
`core:plugin.error`, `core:plugin.misbehaved`, `ui:resize`, `ui:focus`,
`ui:suspend`/`ui:resume`.

:::note[Para hooks de producto, mira la extensión]
Eventos como `agent:tool.start` o `agent:message` no son del core: los emite la
extensión `agent`. Su catálogo vive en el contrato del agente, no aquí.
:::
