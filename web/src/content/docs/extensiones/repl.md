---
title: La extensión repl
description: Un intérprete de Lua interactivo sobre la API pública — activable en solitario, con evaluación estructurada, modo multilínea y uso headless para scripts y tests.
---

## Qué hace

`repl` es un **REPL de Lua** sobre la API pública del core: `enu` con solo el
`repl` activo es un intérprete Lua interactivo con acceso a `enu.*`. Es la prueba
de que el runtime sirve para más que el agente. Compila y ejecuta código del
usuario con `load` —la base de PUC-Lua 5.4, que compila un string en memoria sin
IO bloqueante y que el sandbox del core deja disponible a propósito—: no hizo
falta ninguna primitiva nueva para construirlo.

Sigue la semántica clásica del REPL de Lua:

- Una **expresión** suelta se evalúa y muestra su valor sin escribir `return`
  (`1 + 1` imprime `2`; `enu.version.api` imprime su nivel).
- Una **sentencia** (`x = 5`, un `for`) se ejecuta y no imprime nada.
- Un bloque **incompleto** (una función o `do` sin cerrar) no es un error: pide
  otra línea, con el prompt de continuación `..` (modo multilínea).
- Un error de usuario se captura sin tumbar el REPL. Los errores estructurados
  del core se muestran como `code: message` (p. ej. `ENOENT: no such file`).
- Las cadenas se entrecomillan al imprimirlas (`"hola"` se distingue de un
  identificador); el resto va por `tostring`.

## Cómo se activa

El `plugin.toml` **no declara `requires`**: el REPL se activa en solitario, sin
arrastrar el harness. En `enu.toml`:

```toml
# ~/.config/enu/enu.toml
[plugins]
enabled = ["repl"]
```

- **En TTY**, arranca la UI interactiva al evento `core:ready` (el último del
  arranque). Sales con `ctrl+d` o `/q` (`/quit`, `/exit`).
- **Cede al chat.** Si el conjunto oficial está activo (chat *y* repl), es el
  chat quien posee la pantalla: el REPL no monta UI y queda como módulo
  accesible por `require("repl")`. Así `enu` con el conjunto oficial abre solo el
  chat; `enu` con solo `repl` abre el REPL.
- **En headless** (`enu -e`, CI: sin `enu.ui`) no monta ninguna UI; el módulo
  queda accesible para `repl.eval` y scripts.

La UI interactiva usa el [toolkit](toolkit.md) como dependencia **blanda**
(`require` perezoso): se necesita para pintar, no para evaluar. Si el toolkit no
está activo, `repl.start` devuelve un `EINVAL` accionable, pero `repl.eval`
sigue funcionando sin pantalla.

## Configuración

El REPL no tiene fichero de configuración propio. Lo único ajustable es el theme
de su UI, que se pasa por código a `repl.start{ theme = ... }`.

## Qué expone

El módulo público se obtiene con `require("repl")`:

| Firma | Efecto |
|---|---|
| `repl.eval(src: string) -> tabla` | Evalúa una línea de Lua **de forma síncrona** y devuelve un resultado estructurado. La lógica pura, probada headless. |
| `repl.eval_in_task(src: string, cb: función)` | Evalúa `src` **dentro de una task** y entrega el resultado a `cb`. Es la vía cuando el código de usuario llama a funciones suspendientes del core (`enu.fs.read`, `enu.http.request`…), que solo corren dentro de una task. |
| `repl.start(opts?: tabla) -> Repl` | Monta la UI interactiva (solo TTY). `opts.theme` opcional. `EINVAL` en headless o sin toolkit. |
| `repl.banner() -> string` | El banner de bienvenida (versión, nivel de API y cómo salir). |

El resultado de `repl.eval` (y el que recibe el `cb` de `eval_in_task`) es una
tabla:

```lua
-- éxito de una expresión:
{ ok = true,  values = { 2 }, n = 1, display = "2" }
-- sentencia sin retorno:
{ ok = true,  values = {}, n = 0, display = "" }
-- error de ejecución o de sintaxis:
{ ok = false, error = <err>, display = "ENOENT: ..." }
-- entrada incompleta (pedir otra línea):
{ ok = false, incomplete = true, error = <msg>, display = "" }
```

`n` es explícito (no `#values`) para preservar los `nil` intercalados de un
retorno múltiple.

## Uso headless

Como `repl.eval` es síncrono y no necesita TTY, sirve para evaluar Lua sin
pantalla —en un test, un script o un pipe. Para expresiones y llamadas a la API
**no** suspendiente basta `eval`; para código que suspende, `eval_in_task`:

```lua
local repl = require("repl")

local r = repl.eval("enu.version.api")
print(r.display)                       --> el nivel de API

repl.eval_in_task("enu.fs.read('README.md')", function(res)
  if res.ok then print(#res.values[1] .. " bytes") end
end)
```
