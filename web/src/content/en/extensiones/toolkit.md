---
title: The toolkit extension
description: The widget toolkit over enu.ui and enu.text — layout containers, focus, composition across plugins, and a theme system with semantic color names.
---

## What it does

The core exposes a deliberately low-level UI API (cells, regions, and a
compositor; `enu.ui`, ADR-007). The `toolkit` is the official Lua extension
that provides the high-level layer on top of `enu.ui` and `enu.text`:

- **slots** — layout containers (`vbox`/`hbox`/`stack`) that place their
  children;
- **focus** — a focused widget that input is routed to;
- **composition across plugins** — each app has its own tree, region, and
  focus, without colliding with those of other plugins;
- **themes** — semantic color names (`accent`, `error`, `dim`…) are resolved
  here into literals, not in the core.

It's pure Lua over the public API, with no kernel privilege: the heavy
lifting (measuring text, composing markdown, clipping the viewport,
painting) is a Go primitive, so orchestrating the UI from Lua is smooth. It's
versioned **separately** from the core's sacred API. [chat](../chat.md) and
[repl](repl.md) use it to paint.

## How it's activated

`plugin.toml` declares no `requires`. In `enu.toml`:

```toml
# ~/.config/enu/enu.toml
[plugins]
enabled = ["toolkit"]
```

The toolkit **doesn't exist without `enu.ui`**: in headless mode (no TTY) the
painting functions can't be used. Loading the extension only exposes the
machinery (the pure tree, layout, and theme functions); mounting a real app
requires checking `enu.has("ui")` first. Activating `chat` pulls in the
toolkit by dependency.

## Configuration

The toolkit has no config file. The theme is built or adjusted in code and
passed to the app.

## What it exposes

The public module is obtained with `require("toolkit")`.

### The root: `toolkit.app`

`toolkit.app(opts) -> App` mounts an app over a region of the compositor:
manages focus, routes input (pushes an `on_input` that delivers the key to
the focused widget), and repaints by dirty nodes. `opts`: `region` (an
already-created `Region`) **or** `x/y/w/h/z?` so the app creates its own;
`root?` (the root container, a `vbox` by default); `theme?`;
`manage_input?` (`true` by default). Without `enu.ui` it's an actionable
`EINVAL`.

`App` methods: `relayout()`, `resize(w, h)`, `set_focus(w)`, `focus_next()` /
`focus_prev()`, `handle_key(ev) -> boolean`, `paint()`, and `close()`.

### Containers (slots)

- `toolkit.vbox(opts) -> container` — stacks children vertically.
- `toolkit.hbox(opts) -> container` — places them side by side horizontally.
- `toolkit.stack(opts) -> container` — overlays all children in the same
  area (the basis of modal layers; the last one inserted stays on top).

Common `opts`: `id?`, `pad?` (padding: a number, `{v, h}`, or `{t, r, b, l}`),
`gap?`, `align?` (cross axis: `stretch` by default, `start`/`center`/`end`),
`justify?` (main axis: `start`/`center`/`end`/`between`). Children are added
with `container:add(child)`. Each child declares how it occupies the main
axis with `flex` (a number ≥ 0; if > 0, it gets a share of the leftover
space proportional to its `flex`) or a fixed size (`pref_h` in a `vbox`,
`pref_w` in an `hbox`).

### Leaf widgets

- `toolkit.label{text?, style?, id?, pref_h?} -> Label` — a line of
  styled text. Methods: `set_text(s)`, `set_style(spec)`.
- `toolkit.text{text?, markdown?, id?} -> Text` — a multi-line block with
  viewport scrolling (streaming-safe markdown if `markdown = true`).
  Methods: `set_text(s)`, `scroll_to(line)`, `content_height(w)`.
- `toolkit.input{value?, placeholder?, id?} -> Input` — a single-line
  editor, focusable (consumes characters, backspace, arrows, home/end).
  Methods: `value()`, `set_value(s)`, `on_key(ev)`, `caret_col()`.
- `toolkit.box{child?, title?, border?, pad?, bg?, ...} -> Box` — a frame
  (border + optional title + padding) around a child. `border`: `"rounded"`
  (default), `"square"`, or `"none"`. Method: `set_title(s)`.
- `toolkit.spinner{label?, frames?, interval?, color?, id?} -> Spinner` — an
  animated activity indicator (`interval` in ms, 80 by default). Starts
  stopped; methods: `start()`, `stop()`, `set_label(s)`.
- `toolkit.richtext{spans?, align?, fill_bg?, id?} -> RichText` — a line of
  several styled spans. `align`: `"left"` (default) or `"right"`. Method:
  `set_spans(list)`.

### Themes

A `Theme` is a table of semantic names → literals; it resolves the color to
a literal right before composing the Block, so the core never sees a name
and a theme change repaints the UI without touching the widgets.

- `toolkit.theme.new{name?, colors} -> Theme` — builds a theme. `colors`
  must map each name to a **literal** (`"#rrggbb"` or a `0-255` index); a
  value that isn't one is `EINVAL` when constructing.
- `toolkit.theme.default -> Theme` — the harness's curated default palette
  (its visual identity).

`Theme` methods: `color(name) -> literal`, `style(spec) -> Style` (converts
a spec's semantic `fg`/`bg` into literals), `with(overrides) -> Theme`
(derives a theme with some colors replaced), and `markdown_opts()` (the
per-element style table that `enu.text.markdown` accepts).

The default palette defines, among others, `fg`, `bg`, `dim`, `secondary`,
`accent`, `error`, `warn`, `success`, `info`, `bg_surface`, `overlay`,
`border`, `border_focus`, `selection`, `role_user`, `role_assistant`,
`heading`, `strong`, `link`, `code`, and the diff colors
(`diff_add`/`diff_del`/`diff_context`). An unknown name is an actionable
error, not a silent default.

### The base node

`toolkit.widget` is the tree node; `toolkit.widget.new{...} -> Widget` (and
`toolkit.widget.derive()`) let you build new widget types on the same tree,
focus, and dirty-tracking contract.

## Events

The toolkit emits `toolkit:focus` (payload `{ app, widget }`) when widget
focus changes, in the plugin's own namespace. The `ui:` namespace is
reserved for the core, which emits its own `ui:focus` with different
semantics.

## Example

```lua
local toolkit = require("toolkit")

if enu.has("ui") then
  local column = toolkit.vbox{ id = "root" }
  local output = toolkit.text{ markdown = true }
  output.flex = 1
  local input = toolkit.input{ placeholder = "type something…" }

  column:add(output)
  column:add(input)

  local app = toolkit.app{ root = column }
  app:set_focus(input)
end
```
