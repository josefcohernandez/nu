---
title: enu.json / toml / yaml — codecs
description: Codificación y decodificación de JSON, TOML y YAML, con el sentinel NULL y el manejo estricto de UTF-8.
---

`enu.json`, `enu.toml` y `enu.yaml` son los codecs. Todos disponibles en workers
**[W]** y ninguno suspende: trabajan sobre strings en memoria.

## `enu.json` [W]

```
enu.json.encode(v, opts?) -> string   -- opts.pretty
enu.json.decode(s) -> v
```

```sh
enu -e 'return enu.json.encode({ a = 1, b = { 2, 3 } })'
```

```
{"a":1,"b":[2,3]}
```

Con formato:

```sh
enu -e 'return enu.json.encode({ a = 1 }, { pretty = true })'
```

```
{
  "a": 1
}
```

Decodificar:

```sh
enu -e 'local v = enu.json.decode("[10,20,30]"); return v[1] + v[2] + v[3]'
```

```
60
```

### `null` y el sentinel `NULL`

JSON `null` ↔ `enu.json.NULL` (un sentinel), para **no perder claves**: si
mapeara a `nil`, la clave desaparecería de la tabla Lua.

```lua
local v = enu.json.decode('{"x": null}')
-- v.x == enu.json.NULL  (la clave "x" existe; no se perdió)
if v.x == enu.json.NULL then -- ...
```

### Estricto con UTF-8

`encode` lanza `EINVAL` ante bytes inválidos: sanear es una decisión **visible**
de quien tiene el contexto (la tool), nunca del codec a tus espaldas.

```lua
local ok, err = pcall(function() return enu.json.encode({ s = bytes_crudos }) end)
if not ok and err.code == "EINVAL" then
  -- decide tú cómo sanear; el codec no lo hace solo
end
```

## `enu.toml` [W]

```
enu.toml.encode(v) -> string
enu.toml.decode(s) -> v
```

```sh
enu -e 'local v = enu.toml.decode("nombre = \"nu\"\nversion = 2"); return v.nombre, v.version'
```

```
nu
2
```

TOML es el formato de configuración de `enu` (`enu.toml`, `providers.toml`,
`plugin.toml`), así que este codec es el que usan los plugins para leer su propia
config.

## `enu.yaml` [W]

```
enu.yaml.encode(v) -> string
enu.yaml.decode(s) -> v
```

Necesario para metadatos del ecosistema existente (frontmatter de skills): YAML
es demasiado traicionero para parsearlo en Lua puro.

```sh
enu -e 'local v = enu.yaml.decode("nombre: nu\ntags:\n  - cli\n  - lua"); return v.nombre, #v.tags'
```

```
nu
2
```
