---
title: nu.fs — filesystem
description: Lectura, escritura atómica, stat, listado, manipulación de ficheros y vigilancia del filesystem.
---

`nu.fs` es el acceso al filesystem. Casi todo es **⏸** (suspende: va dentro de
una task) y **[W]** (disponible en workers), salvo `nu.fs.watch`, que es solo del
estado principal.

## `nu.fs.read` ⏸ [W]

```
nu.fs.read(path) -> string
```

Lee el fichero entero como string. Lanza `ENOENT` si no existe.

```sh
nu -e '
nu.task.spawn(function()
  local txt = nu.fs.read("README.md")
  nu.fs.write(nu.fs.tmpdir().."/n.txt", tostring(#txt))  -- nº de bytes
end)
return "ok"
'
```

## `nu.fs.write` / `nu.fs.append` ⏸ [W]

```
nu.fs.write(path, data, opts?)
nu.fs.append(path, data)
```

Escritura **atómica** (vía fichero temporal + rename: nunca dejas un fichero a
medias). `opts.exclusive = true` crea **solo si no existe**, en una operación
indivisible (`O_EXCL`); si ya existe lanza `EEXIST`. Es la pieza para lockfiles.

```lua
nu.task.spawn(function()
  nu.fs.write("salida.txt", "contenido\n")
  nu.fs.append("salida.txt", "otra línea\n")

  -- Lockfile: solo uno gana la creación.
  local ok = pcall(function()
    nu.fs.write("app.lock", nu.sys.pid()..":"..nu.sys.hostname(), { exclusive = true })
  end)
  if not ok then error({ code = "EEXIST", message = "ya hay un proceso" }) end
end)
```

## `nu.fs.stat` ⏸ [W]

```
nu.fs.stat(path) -> { size, mtime_ms, is_dir, mode }?
```

Metadatos del fichero, o **`nil` si no existe** (no lanza `ENOENT`: es la forma
idiomática de comprobar existencia).

```lua
nu.task.spawn(function()
  local st = nu.fs.stat("config.json")
  if st and not st.is_dir then
    -- existe y es un fichero
  end
end)
```

## `nu.fs.list` ⏸ [W]

```
nu.fs.list(dir) -> { name, is_dir }[]
```

Lista el directorio **sin recursión**. Para recursivo respetando `.gitignore`,
usa [`nu.search.files`](/nu/referencia/search/).

```sh
nu -e '
nu.task.spawn(function()
  local entradas = nu.fs.list("docs")
  nu.fs.write(nu.fs.tmpdir().."/c.txt", tostring(#entradas))
end)
return "ok"
'
```

## Manipulación ⏸ [W]

```
nu.fs.mkdir(path) ⏸ [W]
nu.fs.remove(path, opts?) ⏸ [W]  -- opts.recursive=true para dirs no vacíos
nu.fs.rename(from, to) ⏸ [W]
nu.fs.copy(from, to) ⏸ [W]
```

```lua
nu.task.spawn(function()
  nu.fs.mkdir("build")
  nu.fs.copy("plantilla.txt", "build/copia.txt")
  nu.fs.rename("build/copia.txt", "build/final.txt")
  nu.fs.remove("build", { recursive = true })
end)
```

## `nu.fs.tmpdir` ⏸ [W]

```
nu.fs.tmpdir() -> string
```

Directorio temporal **propio de la sesión** (se limpia con ella).

## `nu.fs.cwd` [W]

```
nu.fs.cwd() -> string
```

Directorio de trabajo, **inmutable** durante la sesión (los subprocesos pueden
recibir otro vía `opts.cwd`). Nota: no es ⏸, se puede llamar sin task.

```sh
nu -e 'return nu.fs.cwd() ~= nil'
```

```
true
```

## `nu.fs.watch`

```
nu.fs.watch(path, opts?, fn) -> Watcher
  Watcher:stop()
```

Vigila cambios en el filesystem. Solo **estado principal**. `opts`:

- `recursive?` — vigila subdirectorios.
- `gitignore = true` — ignora lo que git ignora (vigilar `node_modules/` es
  ruido).
- `debounce_ms = 50`.

Entrega **en lotes**: `fn(events[])` con `{ path, kind }` donde `kind` es
`"create"`, `"modify"` o `"remove"`. Un `git checkout` que toca miles de ficheros
llega como **un solo lote**. El handler es síncrono. `Watcher:stop()` para.

```lua
local w = nu.fs.watch("src", { recursive = true, gitignore = true }, function(events)
  for _, e in ipairs(events) do
    nu.log.info("%s: %s", e.kind, e.path)
  end
end)
-- ...
w:stop()
```
