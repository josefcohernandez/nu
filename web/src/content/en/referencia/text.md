---
title: enu.text / enu.re — text
description: Cell width, wrap, truncation, markdown, syntax highlighting, diff and RE2 regex.
---

`enu.text` gathers text rendering and processing operations —the
quadratic-on-screen ones, in Go— and `enu.re` the regex engine. Both available in
workers **[W]** and neither suspends.

Several functions return a **Block** (an opaque handle of styled lines)
that gets stamped with [`Region:blit`](/enu/en/api/ui/#surface). The ones
that return plain values (`width`, `truncate`) are tested directly with
`enu -e`.

## `enu.text.width` [W]

```
enu.text.width(s) -> integer
```

Width in **cells** (not bytes nor runes): correctly counts graphemes,
east-asian characters and emoji.

```sh
enu -e 'return enu.text.width("café"), enu.text.width("日本")'
```

```
4
4
```

(`café` takes 4 cells; `日本` also: two wide characters.)

## `enu.text.truncate` [W]

```
enu.text.truncate(s, width, opts?) -> string
```

Truncates to `width` cells, with an optional ellipsis.

```sh
enu -e 'return enu.text.truncate("hola mundo", 7, { ellipsis = "…" })'
```

```
hola m…
```

## `enu.text.wrap` [W]

```
enu.text.wrap(s, width, opts?) -> Block
```

Word-wrap to `width` cells. Returns a Block ready for `blit`. With
`opts.style` —a [Style](../ui/) `{ fg?, bg?, bold?, italic?, underline?, reverse? }`—
every line of the Block comes out with that default style.

```lua
local block = enu.text.wrap("un párrafo largo que no cabe en una línea", 20)
-- region:blit(0, 0, block)

-- styled: each line in the accent color, bold.
local aviso = enu.text.wrap("atención: esto es importante", 20, {
  style = { fg = "#ffcc00", bold = true },
})
```

## `enu.text.markdown` [W]

```
enu.text.markdown(s, opts) -> Block
```

**Full** markdown render at `opts.width`, themable. Accepts incomplete
input (**streaming-safe**): you can re-render on every delta of an LLM without
it breaking on half-closed markdown.

```lua
local block = enu.text.markdown("# Título\n\nUn **párrafo**.", { width = 80 })
```

## `enu.text.highlight` [W]

```
enu.text.highlight(code, lang, opts?) -> Block
```

Syntax highlighting of `code` for language `lang`.

```lua
local block = enu.text.highlight("local x = 1\nreturn x", "lua")
```

## `enu.text.diff` [W]

```
enu.text.diff(a, b, opts?) -> { hunks, block? }
```

Structured diff between `a` and `b`. With `opts.render = true` also returns the
painted Block.

```lua
local d = enu.text.diff(viejo, nuevo, { render = true })
for _, h in ipairs(d.hunks) do
  -- inspect each hunk
end
-- region:blit(0, 0, d.block)
```

## `enu.re` — RE2 regex [W]

```
enu.re.compile(pattern) -> Re
  Re:match(s) -> caps?            -- nil if it doesn't match
  Re:find_all(s) -> ranges
  Re:replace(s, repl) -> string
```

**RE2** engine (linear, no catastrophic backtracking). Compile once, reuse.

```sh
enu -e '
local re = enu.re.compile("(\\w+)@(\\w+)")
return enu.json.encode(re:match("usuario@dominio"))
'
```

```
["usuario@dominio","usuario","dominio"]
```

`match` returns the full capture followed by the groups. Replace:

```sh
enu -e 'return enu.re.compile("\\d+"):replace("a1b22c333", "#")'
```

```
a#b#c#
```

:::note[No "tokens" here]
There is no LLM token estimation in this module: "token" is product
vocabulary, and the heuristic (~4 bytes/token) is pure Lua that doesn't justify a
primitive. It lives in the providers extension (`providers.approx_tokens`).
:::
