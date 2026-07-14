---
title: nu.ui — terminal
description: Regiones, blocks, estilos, input por pila y el compositor de nu. Solo estado principal, solo con TTY.
---

`nu.ui` es la superficie de terminal: celdas, regiones y el compositor. El
compositor, el diffing y el pintado viven en Go; los cambios se coalescen y se
pinta como mucho cada ~30 ms. **No existe "flush" manual.**

:::caution[Headless: nu.ui no existe]
Sin TTY interactivo (`nu -e`, CI, salida redirigida) el módulo `nu.ui`
**directamente no existe** —el mismo modelo que las `caps` de los workers: la
superficie no concedida no está—. Detéctalo con
[`nu.has("ui")`](/nu/referencia/nu/#nuhas-w), **nunca** probando y capturando el
error. Por eso los ejemplos de esta página no son ejecutables con `nu -e`.
:::

Solo **estado principal** (no workers). Empieza siempre por:

```lua
if not nu.has("ui") then return end   -- degrada en headless
```

## Superficie

```
nu.ui.size() -> { w, h }            -- tamaño del terminal en celdas
nu.ui.region(opts) -> Region        -- opts: x, y, w, h, z?
```

Las **regiones** son la unidad de composición: rectángulos con z-order,
propiedad de quien los crea.

```
Region:blit(x, y, block: Block)     -- estampa un Block en coords locales
Region:fill(style?) / Region:clear()
Region:move(x, y) / Region:resize(w, h)
Region:raise() / Region:lower()
Region:show() / Region:hide() / Region:destroy()
Region:cursor(x, y | nil)           -- coloca (o oculta con nil) el cursor real
```

```lua
if not nu.has("ui") then return end
local sz = nu.ui.size()
local r = nu.ui.region{ x = 0, y = 0, w = sz.w, h = 3, z = 10 }
r:fill()
r:blit(0, 0, nu.ui.block({ "Hola desde una región" }))
```

### Resize y viewport

Una región total o parcialmente fuera de pantalla **se recorta sin error**
(jamás pinta fuera de límites) y sus coordenadas no se tocan: si la pantalla
crece, reaparece tal cual. Recolocarse es responsabilidad del dueño
(convención "tu región, tu `ui:resize`").

`Region:blit` **recorta por ambos extremos**: `x`/`y` pueden ser **negativos** y
recortan el borde inicial del bloque. `blit(0, -3, doc)` muestra `doc` desde su
cuarta fila: es un **viewport** sobre un Block mayor que la región, donde
*scroll = re-blit con otro offset*. Es **copia, nunca re-render**: scrollear
cuesta lo que copiar la ventana visible.

```lua
-- Scroll: re-blittear el mismo Block con distinto offset (sin recalcular nada).
local offset = 0
local function pintar() r:blit(0, -offset, doc) end
nu.ui.keymap("j", function() offset = offset + 1; pintar() end)
```

## Blocks y estilos

Un **Block** es un handle opaco de líneas estilizadas, producido por
[`nu.text.*`](/nu/referencia/text/) o construido a mano. Tiene anchura y altura.

```
nu.ui.block(lines: (string | Span[])[]) -> Block   -- Span = { text, style? }
nu.ui.caps() -> { colors, kitty_keyboard, mouse, images }
nu.ui.clipboard_set(s) / nu.ui.clipboard_get() -> string?  ⏸   -- OSC 52
```

Un `Style` es `{ fg?, bg?, bold?, italic?, underline?, reverse? }` con colores
**literales**: `"#rrggbb"` o índice 0-255 (el render los degrada a lo que el
terminal soporte). Los nombres semánticos (`"accent"`, `"error"`…) **no son del
core**: son vocabulario del theme del toolkit, que los resuelve a literales.

```lua
local linea = {
  { text = "ERROR ", style = { fg = "#ff5555", bold = true } },
  { text = "algo falló" },
}
local block = nu.ui.block({ "primera línea", linea })
```

## Input

Modelo de **pila**: el input fluye al handler superior; quien no consume, deja
pasar. El enrutado fino de focus es trabajo del toolkit, no del core.

```
nu.ui.on_input(fn) -> InputHandle    -- fn(ev) -> boolean (true = consumido)
  InputHandle:pop()
nu.ui.keymap(seq: string, fn, opts?) -> Keymap
  Keymap:unmap()
```

El evento `ev` es `{ type: "key"|"mouse"|"paste", key?, mods?, x?, y?, text?, path? }`.

```lua
local h = nu.ui.on_input(function(ev)
  if ev.type == "key" and ev.key == "q" then
    -- salir
    return true   -- consumido
  end
  return false     -- deja pasar al siguiente de la pila
end)
-- h:pop() para quitarlo
```

`keymap` es azúcar sobre la pila, con notación `"ctrl+k"`, `"alt+enter"` o
secuencias `"g g"`:

```lua
local km = nu.ui.keymap("ctrl+s", function() guardar() end)
-- km:unmap() para quitarlo
```

En conflictos, **la pila manda**: el registro más reciente activo gana (y el
`init.lua` del usuario se carga el último, así que tiene la última palabra).

:::note[Pegar una imagen]
Cuando el portapapeles trae contenido no-texto (una imagen), el core lo vuelca a
un fichero temporal de la sesión y entrega el evento `paste` con `path` (la ruta)
en vez de `text`. Así los bytes binarios nunca cruzan las fronteras de texto/JSON.
:::
