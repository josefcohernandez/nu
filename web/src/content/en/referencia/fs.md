---
title: enu.fs — filesystem
description: Reading, atomic writing, stat, listing, file manipulation, and filesystem watching.
---

`enu.fs` is filesystem access. Almost everything is **⏸** (suspending: it goes
inside a task) and **[W]** (available in workers), except `enu.fs.watch`, which
belongs only to the main state.

## `enu.fs.read` ⏸ [W]

```
enu.fs.read(path) -> string
```

Reads the whole file as a string. Throws `ENOENT` if it doesn't exist.

```sh
enu -e '
enu.task.spawn(function()
  local txt = enu.fs.read("README.md")
  enu.fs.write(enu.fs.tmpdir().."/n.txt", tostring(#txt))  -- number of bytes
end)
return "ok"
'
```

## `enu.fs.write` / `enu.fs.append` ⏸ [W]

```
enu.fs.write(path, data, opts?)
enu.fs.append(path, data)
```

**Atomic** write (via temp file + rename: you never leave a half-written
file). `opts.exclusive = true` creates **only if it doesn't exist**, in a
single indivisible operation (`O_EXCL`); if it already exists it throws
`EEXIST`. It's the building block for lockfiles. `opts.mode` (a permission
integer, e.g. `0600`) sets the creation mode with a chmod that is **not trimmed
by the umask** —for credentials or a transcript that must not be world-readable—;
it composes with `exclusive`. Without `opts.mode`, a new file is created with the
standard mode trimmed by the umask. `append` takes no opts and **preserves the
existing file's mode**: for fixed permissions, create the file empty with
`write{ mode }` and then `append`.

```lua
enu.task.spawn(function()
  enu.fs.write("output.txt", "content\n")
  enu.fs.append("output.txt", "another line\n")

  -- Credentials file: 0600, not readable by other users.
  enu.fs.write("token", secret, { mode = tonumber("600", 8) })

  -- Lockfile: only one wins the creation.
  local ok = pcall(function()
    enu.fs.write("app.lock", enu.sys.pid()..":"..enu.sys.hostname(), { exclusive = true })
  end)
  if not ok then error({ code = "EEXIST", message = "a process is already running" }) end
end)
```

## `enu.fs.stat` ⏸ [W]

```
enu.fs.stat(path) -> { size, mtime_ms, is_dir, mode }?
```

File metadata, or **`nil` if it doesn't exist** (it doesn't throw `ENOENT`:
that's the idiomatic way to check existence).

```lua
enu.task.spawn(function()
  local st = enu.fs.stat("config.json")
  if st and not st.is_dir then
    -- it exists and is a file
  end
end)
```

## `enu.fs.list` ⏸ [W]

```
enu.fs.list(dir) -> { name, is_dir }[]
```

Lists the directory **non-recursively**. For recursive listing that respects
`.gitignore`, use [`enu.search.files`](/enu/en/api/search/).

```sh
enu -e '
enu.task.spawn(function()
  local entries = enu.fs.list("docs")
  enu.fs.write(enu.fs.tmpdir().."/c.txt", tostring(#entries))
end)
return "ok"
'
```

## Manipulation ⏸ [W]

```
enu.fs.mkdir(path) ⏸ [W]
enu.fs.remove(path, opts?) ⏸ [W]  -- opts.recursive=true for non-empty dirs
enu.fs.rename(from, to) ⏸ [W]
enu.fs.copy(from, to) ⏸ [W]
```

```lua
enu.task.spawn(function()
  enu.fs.mkdir("build")
  enu.fs.copy("template.txt", "build/copy.txt")
  enu.fs.rename("build/copy.txt", "build/final.txt")
  enu.fs.remove("build", { recursive = true })
end)
```

## `enu.fs.tmpdir` ⏸ [W]

```
enu.fs.tmpdir() -> string
```

Temporary directory **scoped to the session** (cleaned up along with it).

## `enu.fs.cwd` [W]

```
enu.fs.cwd() -> string
```

Working directory, **immutable** during the session (subprocesses can receive
a different one via `opts.cwd`). Note: it's not ⏸, it can be called without a
task.

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

Watches for filesystem changes. **Main state only**. `opts`:

- `recursive?` — watches subdirectories.
- `gitignore = true` — ignores what git ignores (watching `node_modules/` is
  noise).
- `debounce_ms = 50`.

Delivers **in batches**: `fn(events[])` with `{ path, kind }` where `kind` is
`"create"`, `"modify"`, or `"remove"`. A `git checkout` that touches thousands
of files arrives as **a single batch**. The handler is synchronous.
`Watcher:stop()` stops it.

```lua
local w = enu.fs.watch("src", { recursive = true, gitignore = true }, function(events)
  for _, e in ipairs(events) do
    enu.log.info("%s: %s", e.kind, e.path)
  end
end)
-- ...
w:stop()
```
