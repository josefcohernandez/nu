---
title: Your first script
description: Run Lua with enu -e, understand the main state versus tasks, and handle structured errors.
---

`enu` is, above all, a Lua runtime. The fastest way to try it is `enu -e`,
which **evaluates a Lua chunk with no TTY (headless) and prints its return
values**.

## Hello, enu

```sh
enu -e 'return "hello, " .. "enu"'
```

```
hello, enu
```

Every value you `return` gets printed on its own line:

```sh
enu -e 'return 1, 2, 3'
```

```
1
2
3
```

Tables get printed as Lua's `tostring` (`table: 0x...`), which isn't very
useful. To view them, encode them with
[`enu.json`](/enu/en/api/codecs/):

```sh
enu -e 'return enu.json.encode(enu.version)'
```

```
{"api":2,"major":0,"minor":1,"patch":0}
```

:::caution[`print` doesn't go to the screen]
In `enu`, `print` is an alias for `enu.log.info`: it writes to the log file,
**never to the screen** (the UI belongs to extensions, not the core). For
something to show up on stdout with `enu -e`, `return` it.
:::

## The main state and tasks

This is the distinction you need to internalize from the start.

The `enu -e` chunk runs in the **main state** (single-threaded, with an event
loop). The main state **is not a task**, so it **cannot call suspending
functions** (the ones marked with ⏸: almost all IO —`enu.fs.read`,
`enu.http.request`, `enu.proc.run`…). If you try:

```sh
enu -e 'return enu.fs.read("README.md")'
```

```
error: EINVAL: enu.fs.read can only be called inside a task
```

To do IO, launch a **task** with
[`enu.task.spawn`](/enu/en/api/task/). Inside a task, ⏸ functions are
written in sequential style (with implicit *await*): no callbacks, no
promises.

```sh
enu -e '
enu.task.spawn(function()
  local texto = enu.fs.read("README.md")   -- ⏸ this is fine here
  enu.fs.write(enu.fs.tmpdir() .. "/copia.md", texto)
end)
return "launched"
'
```

`enu -e` waits for **all** tasks spawned by the chunk to finish before
exiting, so the effect (the copied file) has already happened by the time
the process ends. What you *cannot* do is return the result of the task as
the chunk's value: the chunk's `return` is evaluated before the task runs.
To move a value from one task to another, use
[`enu.task.future`](/enu/en/api/task/).

:::tip[Why this separation?]
It's the "browser" concurrency model: a deterministic main thread where
blocking IO is forbidden (it would freeze the event loop), plus cooperative
tasks for asynchronous work. We explain it in depth in [Key
concepts](/enu/en/docs/conceptos/).
:::

## Structured errors

Core functions don't return `(value, err)`: they **throw** structured tables
with `code`, `message`, and an optional `detail?`. They're caught with
`pcall`:

```sh
enu -e '
local ok, err = pcall(function() return enu.json.decode("{roto") end)
return ok, enu.json.encode(err)
'
```

```
false
{"code":"EINVAL","message":"...","detail":...}
```

The `code` is stable and part of the contract (`ENOENT`, `EEXIST`,
`EACCES`, `ETIMEOUT`, `EINVAL`…). It's what you branch on in your logic, not
the `message`. See
[Conventions](/enu/en/api/convenciones/#errors) for the complete list.

## Exit code

`enu -e` exits with **0** if the chunk didn't throw, and with **1** if it did
(an uncaught error). Useful in scripts and CI:

```sh
enu -e 'assert(enu.version.api >= 2)' && echo "API sufficient"
```

## Next step

You now know how to run Lua and why IO lives in tasks. If you want to see
the harness in action, continue with [Your first
agent](/enu/en/docs/primer-agente/). If you'd rather get the full mental
model, go to [Key concepts](/enu/en/docs/conceptos/).
