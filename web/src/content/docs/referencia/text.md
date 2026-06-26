---
title: nu.text / nu.re — texto
description: Anchura en celdas, wrap, truncado, markdown, syntax highlighting, diff y regex RE2.
---

`nu.text` reúne las operaciones de render y procesado de texto —las
cuadráticas-en-pantalla, en Go— y `nu.re` el motor de regex. Ambos disponibles en
workers **[W]** y ninguno suspende.

Varias funciones devuelven un **Block** (un handle opaco de líneas estilizadas)
que se estampa con [`Region:blit`](/nu/referencia/ui/#superficie). Las que
devuelven valores planos (`width`, `truncate`) se prueban directamente con
`nu -e`.

## `nu.text.width` [W]

```
nu.text.width(s) -> integer
```

Anchura en **celdas** (no bytes ni runes): cuenta graphemes, caracteres
east-asian y emoji correctamente.

```sh
nu -e 'return nu.text.width("café"), nu.text.width("日本")'
```

```
4
4
```

(`café` ocupa 4 celdas; `日本` también: dos caracteres anchos.)

## `nu.text.truncate` [W]

```
nu.text.truncate(s, width, opts?) -> string
```

Trunca a `width` celdas, con elipsis opcional.

```sh
nu -e 'return nu.text.truncate("hola mundo", 7, { ellipsis = "…" })'
```

```
hola m…
```

## `nu.text.wrap` [W]

```
nu.text.wrap(s, width, opts?) -> Block
```

Word-wrap a `width` celdas. Devuelve un Block listo para `blit`. Con
`opts.style` —un [Style](../ui/) `{ fg?, bg?, bold?, italic?, underline?, reverse? }`—
cada línea del Block sale con ese estilo por defecto.

```lua
local block = nu.text.wrap("un párrafo largo que no cabe en una línea", 20)
-- region:blit(0, 0, block)

-- estilizado: cada línea en el color de acento, en negrita.
local aviso = nu.text.wrap("atención: esto es importante", 20, {
  style = { fg = "#ffcc00", bold = true },
})
```

## `nu.text.markdown` [W]

```
nu.text.markdown(s, opts) -> Block
```

Render **completo** de markdown a `opts.width`, themable. Acepta entrada
incompleta (**streaming-safe**): puedes re-renderizar a cada delta de un LLM sin
que se rompa con markdown a medio cerrar.

```lua
local block = nu.text.markdown("# Título\n\nUn **párrafo**.", { width = 80 })
```

## `nu.text.highlight` [W]

```
nu.text.highlight(code, lang, opts?) -> Block
```

Syntax highlighting de `code` para el lenguaje `lang`.

```lua
local block = nu.text.highlight("local x = 1\nreturn x", "lua")
```

## `nu.text.diff` [W]

```
nu.text.diff(a, b, opts?) -> { hunks, block? }
```

Diff estructurado entre `a` y `b`. Con `opts.render = true` devuelve además el
Block pintado.

```lua
local d = nu.text.diff(viejo, nuevo, { render = true })
for _, h in ipairs(d.hunks) do
  -- inspeccionar cada hunk
end
-- region:blit(0, 0, d.block)
```

## `nu.re` — regex RE2 [W]

```
nu.re.compile(pattern) -> Re
  Re:match(s) -> caps?            -- nil si no casa
  Re:find_all(s) -> ranges
  Re:replace(s, repl) -> string
```

Motor **RE2** (lineal, sin backtracking catastrófico). Compila una vez, reutiliza.

```sh
nu -e '
local re = nu.re.compile("(\\w+)@(\\w+)")
return nu.json.encode(re:match("usuario@dominio"))
'
```

```
["usuario@dominio","usuario","dominio"]
```

`match` devuelve la captura completa seguida de los grupos. Reemplazo:

```sh
nu -e 'return nu.re.compile("\\d+"):replace("a1b22c333", "#")'
```

```
a#b#c#
```

:::note[Aquí no hay "tokens"]
No existe estimación de tokens de LLM en este módulo: "token" es vocabulario de
producto, y la heurística (~4 bytes/token) es Lua puro que no justifica una
primitiva. Vive en la extensión de providers (`providers.approx_tokens`).
:::
