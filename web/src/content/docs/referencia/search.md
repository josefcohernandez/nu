---
title: nu.search — búsqueda
description: Listado recursivo, grep paralelo a escala de repo y matching difuso para pickers.
---

`nu.search` es la búsqueda a escala de repositorio: la encarnación de "Lua
decide, Go ejecuta" —cada función es una primitiva Go, paralela por dentro—.
Disponible en workers **[W]**.

## `nu.search.files` ⏸ [W]

```
nu.search.files(root, opts?) -> string[]
```

Listado **recursivo** respetando `.gitignore`. `opts`: `glob`, `hidden`, `max`.

```sh
nu -e '
nu.task.spawn(function()
  local md = nu.search.files(".", { glob = "*.md", max = 3 })
  nu.fs.write(nu.fs.tmpdir().."/f.txt", nu.json.encode(md))
end)
return "ok"
'
```

```lua
nu.task.spawn(function()
  -- todos los ficheros Lua del repo, incluyendo ocultos
  local luas = nu.search.files(".", { glob = "**/*.lua", hidden = true })
  return #luas
end)
```

## `nu.search.grep` ⏸ [W]

```
nu.search.grep(pattern, opts) -> iterator
```

Búsqueda de contenido, **paralela por dentro**. Devuelve un **iterador** que
emite `{ path, line_no, line, ranges }` según llegan los resultados (no espera a
tenerlos todos). `opts`: `root`, `glob`, `case`, `max`.

```lua
nu.task.spawn(function()
  local n = 0
  for hit in nu.search.grep("TODO", { root = ".", glob = "*.go" }) do
    n = n + 1
    nu.log.info("%s:%d  %s", hit.path, hit.line_no, hit.line)
    -- hit.ranges marca dónde casó dentro de la línea
  end
  return n
end)
```

## `nu.search.fuzzy` [W]

```
nu.search.fuzzy(query, candidates: string[], opts?) -> { index, score }[]
```

Matching **difuso** ordenado, para pickers. **Síncrono y acotado** (es la
primitiva caliente del picker, se llama a cada tecla): no es ⏸. Devuelve los
índices (1-based, sobre `candidates`) con su score, de mejor a peor.

```sh
nu -e 'return nu.json.encode(nu.search.fuzzy("ab", { "axb", "ba", "cab" }))'
```

```
[{"index":1,"score":10},{"index":3,"score":6}]
```

El candidato `"ba"` no casa "ab" en orden, así que se descarta; `"axb"` (índice
1) puntúa más que `"cab"` (índice 3).

```lua
-- Picker incremental: re-filtrar a cada tecla.
local function filtrar(query, items)
  local res = {}
  for _, m in ipairs(nu.search.fuzzy(query, items)) do
    res[#res + 1] = items[m.index]
  end
  return res
end
```
