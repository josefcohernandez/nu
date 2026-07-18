---
title: enu.json / toml / yaml — codecs
description: Encoding and decoding of JSON, TOML and YAML, with the NULL sentinel and strict UTF-8 handling.
---

`enu.json`, `enu.toml` and `enu.yaml` are the codecs. All available in workers
**[W]** and none suspends: they work on in-memory strings.

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

With formatting:

```sh
enu -e 'return enu.json.encode({ a = 1 }, { pretty = true })'
```

```
{
  "a": 1
}
```

Decoding:

```sh
enu -e 'local v = enu.json.decode("[10,20,30]"); return v[1] + v[2] + v[3]'
```

```
60
```

### `null` and the `NULL` sentinel

JSON `null` ↔ `enu.json.NULL` (a sentinel), so as **not to lose keys**: if
it mapped to `nil`, the key would disappear from the Lua table.

```lua
local v = enu.json.decode('{"x": null}')
-- v.x == enu.json.NULL  (the key "x" exists; it wasn't lost)
if v.x == enu.json.NULL then -- ...
```

### Strict about UTF-8

`encode` throws `EINVAL` on invalid bytes: sanitizing is a **visible** decision
of whoever has the context (the tool), never something the codec does behind your back.

```lua
local ok, err = pcall(function() return enu.json.encode({ s = bytes_crudos }) end)
if not ok and err.code == "EINVAL" then
  -- you decide how to sanitize; the codec doesn't do it on its own
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

TOML is `enu`'s configuration format (`enu.toml`, `providers.toml`,
`plugin.toml`), so this codec is the one plugins use to read their own
config.

## `enu.yaml` [W]

```
enu.yaml.encode(v) -> string
enu.yaml.decode(s) -> v
```

Needed for metadata from the existing ecosystem (skills frontmatter): YAML
is too treacherous to parse in pure Lua.

```sh
enu -e 'local v = enu.yaml.decode("nombre: nu\ntags:\n  - cli\n  - lua"); return v.nombre, #v.tags'
```

```
nu
2
```
