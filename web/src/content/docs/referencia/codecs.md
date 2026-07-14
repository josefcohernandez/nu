---
title: nu.json / toml / yaml — codecs
description: Codificación y decodificación de JSON, TOML y YAML, con el sentinel NULL y el manejo estricto de UTF-8.
---

`nu.json`, `nu.toml` y `nu.yaml` son los codecs. Todos disponibles en workers
**[W]** y ninguno suspende: trabajan sobre strings en memoria.

## `nu.json` [W]

```
nu.json.encode(v, opts?) -> string   -- opts.pretty
nu.json.decode(s) -> v
```

```sh
nu -e 'return nu.json.encode({ a = 1, b = { 2, 3 } })'
```

```
{"a":1,"b":[2,3]}
```

Con formato:

```sh
nu -e 'return nu.json.encode({ a = 1 }, { pretty = true })'
```

```
{
  "a": 1
}
```

Decodificar:

```sh
nu -e 'local v = nu.json.decode("[10,20,30]"); return v[1] + v[2] + v[3]'
```

```
60
```

### `null` y el sentinel `NULL`

JSON `null` ↔ `nu.json.NULL` (un sentinel), para **no perder claves**: si
mapeara a `nil`, la clave desaparecería de la tabla Lua.

```lua
local v = nu.json.decode('{"x": null}')
-- v.x == nu.json.NULL  (la clave "x" existe; no se perdió)
if v.x == nu.json.NULL then -- ...
```

### Estricto con UTF-8

`encode` lanza `EINVAL` ante bytes inválidos: sanear es una decisión **visible**
de quien tiene el contexto (la tool), nunca del codec a tus espaldas.

```lua
local ok, err = pcall(function() return nu.json.encode({ s = bytes_crudos }) end)
if not ok and err.code == "EINVAL" then
  -- decide tú cómo sanear; el codec no lo hace solo
end
```

## `nu.toml` [W]

```
nu.toml.encode(v) -> string
nu.toml.decode(s) -> v
```

```sh
nu -e 'local v = nu.toml.decode("nombre = \"nu\"\nversion = 2"); return v.nombre, v.version'
```

```
nu
2
```

TOML es el formato de configuración de `nu` (`nu.toml`, `providers.toml`,
`plugin.toml`), así que este codec es el que usan los plugins para leer su propia
config.

## `nu.yaml` [W]

```
nu.yaml.encode(v) -> string
nu.yaml.decode(s) -> v
```

Necesario para metadatos del ecosistema existente (frontmatter de skills): YAML
es demasiado traicionero para parsearlo en Lua puro.

```sh
nu -e 'local v = nu.yaml.decode("nombre: nu\ntags:\n  - cli\n  - lua"); return v.nombre, #v.tags'
```

```
nu
2
```
