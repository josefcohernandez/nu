---
title: enu.worker — paralelismo
description: Workers opt-in — estados Lua aislados, paso de mensajes JSON-ables, capacidades y el canal con el padre.
---

`enu.worker` es el paralelismo de verdad: un estado Lua nuevo en su goroutine,
**sin memoria compartida**. La comunicación es por **paso de mensajes
JSON-ables** (copiados, no referencias). `enu.worker.spawn` es solo del estado
principal.

:::tip[¿Necesitas un worker?]
Casi nunca. "Lua decide, Go ejecuta": si estás quemando CPU en Lua, lo normal es
que falte una primitiva Go (búsqueda, diff, markdown ya lo son). Un worker es
para *tu* cómputo pesado en Lua puro que no quieres que congele el loop.
:::

## `enu.worker.spawn`

```
enu.worker.spawn(module: string, opts?) -> Worker
```

Levanta un estado Lua nuevo cargando `module` (resoluble por el loader). Dentro
**no existen** `enu.ui`, `enu.events` (el bus principal) ni workers anidados; sí el
resto de la API marcada **[W]**.

### Capacidades (`opts.caps`)

`opts.caps?: string[]` restringe la API del worker a lo enumerado, con **dos
granularidades**:

- `"fs"` concede el módulo entero.
- `"fs.read"` concede una función concreta.

Lo no concedido **no existe** dentro del estado (sandboxing por capacidades;
*deny-by-default* para superficie nueva: las funciones añadidas en el futuro
nunca quedan concedidas por listas antiguas). Sin `caps`, el worker recibe toda
la API [W].

```lua
-- Worker que solo puede leer ficheros y parsear JSON: nada más.
local w = enu.worker.spawn("mi-plugin/analizador", {
  caps = { "fs.read", "json" },
})
```

## Mensajes

```
Worker:send(msg) ⏸                  -- suspende si la cola está llena (backpressure)
Worker:recv() -> msg ⏸
Worker:on_message(fn) -> Sub        -- alternativa por callback (estado principal)
Worker:terminate()                  -- inmediato y seguro (estados aislados)
```

Los mensajes son **valores JSON-ables, copiados**: no cruzan tablas por
referencia, ni closures, ni userdata, ni Blocks. Un worker manda datos
**digeridos** y el estado principal renderiza.

`recv` y `on_message` son **excluyentes**: registrar uno con el otro pendiente
lanza `EINVAL` en el acto (nunca una prioridad silenciosa).

```lua
-- Estado principal
enu.task.spawn(function()
  local w = enu.worker.spawn("mi-plugin/worker")
  enu.task.cleanup(function() w:terminate() end)

  w:send({ tarea = "procesar", datos = { 1, 2, 3 } })
  local resultado = w:recv()        -- espera la respuesta digerida
  return resultado
end)
```

## Dentro del worker

El worker se comunica con el padre por un canal simétrico:

```
enu.worker.parent.send(msg) ⏸
enu.worker.parent.recv() -> msg ⏸
```

```lua
-- mi-plugin/worker.lua (el module cargado)
enu.task.spawn(function()
  while true do
    local msg = enu.worker.parent.recv()
    local total = 0
    for _, n in ipairs(msg.datos) do total = total + n end
    enu.worker.parent.send({ total = total })
  end
end)
```

Cada worker es un **mini-runtime completo**: scheduler propio, varias tasks,
timers y futures (todo `enu.task` [W]). **Sin watchdog**: los workers existen para
quemar CPU a gusto; el control es `terminate()` desde el padre, más las `caps`.
