---
title: enu.ui — terminal
description: Regions, blocks, styles, stack-based input, and enu's compositor. Main state only, TTY only.
---

`enu.ui` is the terminal surface: cells, regions, and the compositor. The
compositor, diffing, and painting live in Go; changes are coalesced and
painted at most every ~30 ms. **There is no manual "flush".**

:::caution[Headless: enu.ui doesn't exist]
Without an interactive TTY (`enu -e`, CI, redirected output) the `enu.ui`
module **simply doesn't exist** — the same model as worker `caps`: a surface
that wasn't granted isn't there. Detect it with
[`enu.has("ui")`](/enu/en/api/enu/#enuhas-w), **never** by probing and
catching the error. That's why the examples on this page aren't runnable
with `enu -e`.
:::

**Main state only** (not workers). Always start with:

```lua
if not enu.has("ui") then return end   -- degrade in headless mode
```

## Surface

```
enu.ui.size() -> { w, h }            -- terminal size in cells
enu.ui.region(opts) -> Region        -- opts: x, y, w, h, z?
```

**Regions** are the composition unit: rectangles with z-order, owned by
whoever creates them.

```
Region:blit(x, y, block: Block)     -- stamps a Block at local coords
Region:fill(style?) / Region:clear()
Region:move(x, y) / Region:resize(w, h)
Region:raise() / Region:lower()
Region:show() / Region:hide() / Region:destroy()
Region:cursor(x, y | nil)           -- places (or hides with nil) the real cursor
```

```lua
if not enu.has("ui") then return end
local sz = enu.ui.size()
local r = enu.ui.region{ x = 0, y = 0, w = sz.w, h = 3, z = 10 }
r:fill()
r:blit(0, 0, enu.ui.block({ "Hello from a region" }))
```

### Resize and viewport

A region that's fully or partially off-screen **is clipped without error**
(it never paints out of bounds) and its coordinates aren't touched: if the
screen grows, it reappears as is. Repositioning is the owner's responsibility
(the "your region, your `ui:resize`" convention).

`Region:blit` **clips on both ends**: `x`/`y` can be **negative** and clip
the block's starting edge. `blit(0, -3, doc)` shows `doc` starting from its
fourth row: it's a **viewport** over a Block larger than the region, where
*scrolling = re-blitting with a different offset*. It's **a copy, never a
re-render**: scrolling costs exactly as much as copying the visible window.

```lua
-- Scroll: re-blit the same Block with a different offset (nothing recomputed).
local offset = 0
local function paint() r:blit(0, -offset, doc) end
enu.ui.keymap("j", function() offset = offset + 1; paint() end)
```

## Blocks and styles

A **Block** is an opaque handle to styled lines, produced by
[`enu.text.*`](/enu/en/api/text/) or built by hand. It has a width and a
height.

```
enu.ui.block(lines: (string | Span[])[]) -> Block   -- Span = { text, style? }
enu.ui.caps() -> { colors, kitty_keyboard, mouse, images }
enu.ui.clipboard_set(s) / enu.ui.clipboard_get() -> string?  ⏸   -- OSC 52
```

A `Style` is `{ fg?, bg?, bold?, italic?, underline?, reverse? }` with
**literal** colors: `"#rrggbb"` or a 0-255 index (the renderer degrades them
to whatever the terminal supports). Semantic names (`"accent"`, `"error"`…)
are **not part of the core**: they're vocabulary of the toolkit's theme,
which resolves them to literals.

```lua
local line = {
  { text = "ERROR ", style = { fg = "#ff5555", bold = true } },
  { text = "something failed" },
}
local block = enu.ui.block({ "first line", line })
```

## Input

**Stack** model: input flows to the topmost handler; whoever doesn't consume
it lets it pass through. Fine-grained focus routing is the toolkit's job,
not the core's.

```
enu.ui.on_input(fn) -> InputHandle    -- fn(ev) -> boolean (true = consumed)
  InputHandle:pop()
enu.ui.keymap(seq: string, fn, opts?) -> Keymap
  Keymap:unmap()
```

The `ev` event is `{ type: "key"|"mouse"|"paste", key?, mods?, x?, y?, text?, path? }`.

```lua
local h = enu.ui.on_input(function(ev)
  if ev.type == "key" and ev.key == "q" then
    -- quit
    return true   -- consumed
  end
  return false     -- let it pass to the next one on the stack
end)
-- h:pop() to remove it
```

`keymap` is sugar over the stack, with notation like `"ctrl+k"`,
`"alt+enter"`, or sequences like `"g g"`:

```lua
local km = enu.ui.keymap("ctrl+s", function() save() end)
-- km:unmap() to remove it
```

On conflicts, **the stack rules**: the most recently registered active entry
wins (and the user's `init.lua` loads last, so it has the final word).

:::note[Pasting an image]
When the clipboard carries non-text content (an image), the core dumps it to
a temporary file in the session and delivers the `paste` event with `path`
(the file path) instead of `text`. That way binary bytes never cross the
text/JSON boundaries.
:::
