---
title: La extensión toolkit
description: El toolkit de widgets sobre enu.ui y enu.text — contenedores de layout, foco, composición entre plugins y un sistema de themes con nombres semánticos de color.
---

## Qué hace

El core expone una API de UI deliberadamente de bajo nivel (celdas, regiones y un
compositor; `enu.ui`, ADR-007). El `toolkit` es la extensión Lua oficial que aporta
lo de alto nivel encima de `enu.ui` y `enu.text`:

- **slots** — contenedores de layout (`vbox`/`hbox`/`stack`) que colocan a sus
  hijos;
- **foco** — un widget enfocado al que se enruta el input;
- **composición entre plugins** — cada app tiene su propio árbol, región y foco,
  sin colisionar con las de otros plugins;
- **themes** — los nombres semánticos de color (`accent`, `error`, `dim`…) se
  resuelven aquí a literales, no en el core.

Es Lua puro sobre la API pública, sin privilegio de kernel: el trabajo pesado
(medir texto, componer markdown, recortar el viewport, pintar) es primitiva Go,
así que orquestar la UI desde Lua es fluido. Se versiona **aparte** de
la API sagrada del core. El [chat](../chat.md) y el [repl](repl.md) lo usan para
pintar.

## Cómo se activa

El `plugin.toml` no declara `requires`. En `enu.toml`:

```toml
# ~/.config/enu/enu.toml
[plugins]
enabled = ["toolkit"]
```

El toolkit **no existe sin `enu.ui`**: en headless (sin TTY) las funciones que
pintan no se pueden usar. Cargar la extensión solo expone la maquinaria (las
funciones puras de árbol, layout y theme); montar una app de verdad exige
comprobar `enu.has("ui")` antes. Activar `chat` arrastra el toolkit por
dependencia.

## Configuración

El toolkit no tiene fichero de configuración. El theme se construye o se ajusta
por código y se pasa a la app.

## Qué expone

El módulo público se obtiene con `require("toolkit")`.

### La raíz: `toolkit.app`

`toolkit.app(opts) -> App` monta una app sobre una región del compositor:
gestiona el foco, enruta el input (apila un `on_input` que entrega la tecla al
widget enfocado) y repinta por nodos sucios. `opts`: `region` (una `Region` ya
creada) **o** `x/y/w/h/z?` para que la app cree la suya; `root?` (el contenedor
raíz, por defecto un `vbox`); `theme?`; `manage_input?` (por defecto `true`).
Sin `enu.ui` es `EINVAL` accionable.

Métodos de `App`: `relayout()`, `resize(w, h)`, `set_focus(w)`, `focus_next()` /
`focus_prev()`, `handle_key(ev) -> boolean`, `paint()` y `close()`.

### Contenedores (slots)

- `toolkit.vbox(opts) -> contenedor` — apila los hijos en vertical.
- `toolkit.hbox(opts) -> contenedor` — los coloca lado a lado en horizontal.
- `toolkit.stack(opts) -> contenedor` — superpone a todos los hijos en la misma
  área (la base de las capas modales; el último insertado queda encima).

`opts` común: `id?`, `pad?` (padding: número, `{v, h}` o `{t, r, b, l}`),
`gap?`, `align?` (eje cruzado: `stretch` por defecto, `start`/`center`/`end`),
`justify?` (eje principal: `start`/`center`/`end`/`between`). Se añaden hijos con
`contenedor:add(child)`. Cada hijo declara cómo ocupa el eje principal con
`flex` (número ≥ 0; si > 0, recibe una parte del espacio sobrante proporcional a
su `flex`) o un tamaño fijo (`pref_h` en un `vbox`, `pref_w` en un `hbox`).

### Widgets hoja

- `toolkit.label{text?, style?, id?, pref_h?} -> Label` — una línea de texto
  estilizado. Métodos: `set_text(s)`, `set_style(spec)`.
- `toolkit.text{text?, markdown?, id?} -> Text` — un bloque multilínea con
  scroll por viewport (markdown streaming-safe si `markdown = true`). Métodos:
  `set_text(s)`, `scroll_to(line)`, `content_height(w)`.
- `toolkit.input{value?, placeholder?, id?} -> Input` — un editor de una línea,
  focusable (consume caracteres, backspace, flechas, home/end). Métodos:
  `value()`, `set_value(s)`, `on_key(ev)`, `caret_col()`.
- `toolkit.box{child?, title?, border?, pad?, bg?, ...} -> Box` — un marco
  (borde + título opcional + padding) alrededor de un hijo. `border`: `"rounded"`
  (por defecto), `"square"` o `"none"`. Método: `set_title(s)`.
- `toolkit.spinner{label?, frames?, interval?, color?, id?} -> Spinner` — un
  indicador de actividad animado (`interval` en ms, por defecto 80). Nace parado;
  métodos: `start()`, `stop()`, `set_label(s)`.
- `toolkit.richtext{spans?, align?, fill_bg?, id?} -> RichText` — una línea de
  varios spans estilizados. `align`: `"left"` (por defecto) o `"right"`. Método:
  `set_spans(list)`.

### Themes

Un `Theme` es una tabla de nombres semánticos → literales; resuelve el color a
literal justo antes de componer el Block, de modo que el core nunca ve un nombre
y un cambio de theme repinta la UI sin tocar los widgets.

- `toolkit.theme.new{name?, colors} -> Theme` — construye un theme. `colors` debe
  mapear cada nombre a un **literal** (`"#rrggbb"` o índice `0-255`); un valor
  que no lo sea es `EINVAL` al construir.
- `toolkit.theme.default -> Theme` — la paleta curada por defecto (la identidad
  visual del harness).

Métodos de `Theme`: `color(name) -> literal`, `style(spec) -> Style` (convierte
los `fg`/`bg` semánticos de un spec a literales), `with(overrides) -> Theme`
(deriva un theme con algunos colores sustituidos) y `markdown_opts()` (la tabla
de estilos por elemento que `enu.text.markdown` acepta).

La paleta por defecto define, entre otros, `fg`, `bg`, `dim`, `secondary`,
`accent`, `error`, `warn`, `success`, `info`, `bg_surface`, `overlay`, `border`,
`border_focus`, `selection`, `role_user`, `role_assistant`, `heading`, `strong`,
`link`, `code` y los colores de diff (`diff_add`/`diff_del`/`diff_context`). Un
nombre desconocido es un error accionable, no un default silencioso.

### El nodo base

`toolkit.widget` es el nodo del árbol; `toolkit.widget.new{...} -> Widget` (y
`toolkit.widget.derive()`) permiten construir tipos de widget nuevos sobre el
mismo contrato de árbol, foco y dirty tracking.

## Eventos

El toolkit emite `toolkit:focus` (payload `{ app, widget }`) cuando el foco de
widget cambia, en el namespace del propio plugin. El namespace `ui:` está
reservado al core, que emite su propio `ui:focus` con otra semántica.

## Ejemplo

```lua
local toolkit = require("toolkit")

if enu.has("ui") then
  local column = toolkit.vbox{ id = "root" }
  local output = toolkit.text{ markdown = true }
  output.flex = 1
  local input = toolkit.input{ placeholder = "escribe algo…" }

  column:add(output)
  column:add(input)

  local app = toolkit.app{ root = column }
  app:set_focus(input)
end
```
