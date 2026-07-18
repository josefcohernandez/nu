---
title: enu.fs — filesystem
description: Lectura, escritura atómica, stat, listado, manipulación de ficheros y vigilancia del filesystem.
---

`enu.fs` es el acceso al filesystem. Casi todo es **⏸** (suspende: va dentro de
una task) y **[W]** (disponible en workers), salvo `enu.fs.watch`, que es solo del
estado principal.

## `enu.fs.read` ⏸ [W]

```
enu.fs.read(path) -> string
```

Lee el fichero entero como string. Lanza `ENOENT` si no existe.

```sh
enu -e '
enu.task.spawn(function()
  local txt = enu.fs.read("README.md")
  enu.fs.write(enu.fs.tmpdir().."/n.txt", tostring(#txt))  -- nº de bytes
end)
return "ok"
'
```

## `enu.fs.write` / `enu.fs.append` ⏸ [W]

```
enu.fs.write(path, data, opts?)
enu.fs.append(path, data)
```

Escritura **atómica** (vía fichero temporal + rename: nunca dejas un fichero a
medias). `opts.exclusive = true` crea **solo si no existe**, en una operación
indivisible (`O_EXCL`); si ya existe lanza `EEXIST`. Es la pieza para lockfiles.
`opts.mode` (entero de permisos, p. ej. `0600`) fija el modo de creación con un
chmod **no recortado por el umask** —para credenciales o un transcript que no
deben quedar legibles por otros—; es componible con `exclusive`. Sin `opts.mode`,
un fichero nuevo nace con el modo estándar recortado por el umask. `append` no
lleva opts y **preserva el modo del fichero existente**: para permisos fijos, crea
el fichero vacío con `write{ mode }` y luego haz `append`.

```lua
enu.task.spawn(function()
  enu.fs.write("salida.txt", "contenido\n")
  enu.fs.append("salida.txt", "otra línea\n")

  -- Fichero de credenciales: 0600, no legible por otros usuarios.
  enu.fs.write("token", secreto, { mode = tonumber("600", 8) })

  -- Lockfile: solo uno gana la creación.
  local ok = pcall(function()
    enu.fs.write("app.lock", enu.sys.pid()..":"..enu.sys.hostname(), { exclusive = true })
  end)
  if not ok then error({ code = "EEXIST", message = "ya hay un proceso" }) end
end)
```

## `enu.fs.stat` ⏸ [W]

```
enu.fs.stat(path) -> { size, mtime_ms, is_dir, mode }?
```

Metadatos del fichero, o **`nil` si no existe** (no lanza `ENOENT`: es la forma
idiomática de comprobar existencia).

```lua
enu.task.spawn(function()
  local st = enu.fs.stat("config.json")
  if st and not st.is_dir then
    -- existe y es un fichero
  end
end)
```

## `enu.fs.list` ⏸ [W]

```
enu.fs.list(dir) -> { name, is_dir }[]
```

Lista el directorio **sin recursión**. Para recursivo respetando `.gitignore`,
usa [`enu.search.files`](/enu/api/search/).

```sh
enu -e '
enu.task.spawn(function()
  local entradas = enu.fs.list("docs")
  enu.fs.write(enu.fs.tmpdir().."/c.txt", tostring(#entradas))
end)
return "ok"
'
```

## Manipulación ⏸ [W]

```
enu.fs.mkdir(path) ⏸ [W]
enu.fs.remove(path, opts?) ⏸ [W]  -- opts.recursive=true para dirs no vacíos
enu.fs.rename(from, to) ⏸ [W]
enu.fs.copy(from, to) ⏸ [W]
```

```lua
enu.task.spawn(function()
  enu.fs.mkdir("build")
  enu.fs.copy("plantilla.txt", "build/copia.txt")
  enu.fs.rename("build/copia.txt", "build/final.txt")
  enu.fs.remove("build", { recursive = true })
end)
```

## `enu.fs.tmpdir` ⏸ [W]

```
enu.fs.tmpdir() -> string
```

Directorio temporal **propio de la sesión** (se limpia con ella).

## `enu.fs.cwd` [W]

```
enu.fs.cwd() -> string
```

Directorio de trabajo, **inmutable** durante la sesión (los subprocesos pueden
recibir otro vía `opts.cwd`). Nota: no es ⏸, se puede llamar sin task.

```sh
enu -e 'return enu.fs.cwd() ~= nil'
```

```
true
```

## `enu.fs.watch`

```
enu.fs.watch(path, opts?, fn) -> Watcher
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
local w = enu.fs.watch("src", { recursive = true, gitignore = true }, function(events)
  for _, e in ipairs(events) do
    enu.log.info("%s: %s", e.kind, e.path)
  end
end)
-- ...
w:stop()
```
