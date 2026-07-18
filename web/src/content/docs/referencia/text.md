---
title: enu.text / enu.re — texto
description: Anchura en celdas, wrap, truncado, markdown, syntax highlighting, diff y regex RE2.
---

`enu.text` reúne las operaciones de render y procesado de texto —las
cuadráticas-en-pantalla, en Go— y `enu.re` el motor de regex. Ambos disponibles en
workers **[W]** y ninguno suspende.

Varias funciones devuelven un **Block** (un handle opaco de líneas estilizadas)
que se estampa con [`Region:blit`](/enu/api/ui/#superficie). Las que
devuelven valores planos (`width`, `truncate`) se prueban directamente con
`enu -e`.

## `enu.text.width` [W]

```
enu.text.width(s) -> integer
```

Anchura en **celdas** (no bytes ni runes): cuenta graphemes, caracteres
east-asian y emoji correctamente.

```sh
enu -e 'return enu.text.width("café"), enu.text.width("日本")'
```

```
4
4
```

(`café` ocupa 4 celdas; `日本` también: dos caracteres anchos.)

## `enu.text.truncate` [W]

```
enu.text.truncate(s, width, opts?) -> string
```

Trunca a `width` celdas, con elipsis opcional.

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

Word-wrap a `width` celdas. Devuelve un Block listo para `blit`. Con
`opts.style` —un [Style](../ui/) `{ fg?, bg?, bold?, italic?, underline?, reverse? }`—
cada línea del Block sale con ese estilo por defecto.

```lua
local block = enu.text.wrap("un párrafo largo que no cabe en una línea", 20)
-- region:blit(0, 0, block)

-- estilizado: cada línea en el color de acento, en negrita.
local aviso = enu.text.wrap("atención: esto es importante", 20, {
  style = { fg = "#ffcc00", bold = true },
})
```

## `enu.text.markdown` [W]

```
enu.text.markdown(s, opts) -> Block
```

Render **completo** de markdown a `opts.width`, themable. Acepta entrada
incompleta (**streaming-safe**): puedes re-renderizar a cada delta de un LLM sin
que se rompa con markdown a medio cerrar.

```lua
local block = enu.text.markdown("# Título\n\nUn **párrafo**.", { width = 80 })
```

## `enu.text.highlight` [W]

```
enu.text.highlight(code, lang, opts?) -> Block
```

Syntax highlighting de `code` para el lenguaje `lang`.

```lua
local block = enu.text.highlight("local x = 1\nreturn x", "lua")
```

## `enu.text.diff` [W]

```
enu.text.diff(a, b, opts?) -> { hunks, block? }
```

Diff estructurado entre `a` y `b`. Con `opts.render = true` devuelve además el
Block pintado.

```lua
local d = enu.text.diff(viejo, nuevo, { render = true })
for _, h in ipairs(d.hunks) do
  -- inspeccionar cada hunk
end
-- region:blit(0, 0, d.block)
```

## `enu.re` — regex RE2 [W]

```
enu.re.compile(pattern) -> Re
  Re:match(s) -> caps?            -- nil si no casa
  Re:find_all(s) -> ranges
  Re:replace(s, repl) -> string
```

Motor **RE2** (lineal, sin backtracking catastrófico). Compila una vez, reutiliza.

```sh
enu -e '
local re = enu.re.compile("(\\w+)@(\\w+)")
return enu.json.encode(re:match("usuario@dominio"))
'
```

```
["usuario@dominio","usuario","dominio"]
```

`match` devuelve la captura completa seguida de los grupos. Reemplazo:

```sh
enu -e 'return enu.re.compile("\\d+"):replace("a1b22c333", "#")'
```

```
a#b#c#
```

:::note[Aquí no hay "tokens"]
No existe estimación de tokens de LLM en este módulo: "token" es vocabulario de
producto, y la heurística (~4 bytes/token) es Lua puro que no justifica una
primitiva. Vive en la extensión de providers (`providers.approx_tokens`).
:::
