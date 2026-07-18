---
title: enu.sys — environment and clock
description: Platform, environment variables, wall and monotonic clocks, hostname and pid.
---

`enu.sys` exposes the process environment and clocks. Everything available in workers
**[W]** and nothing suspends: they are local queries.

## `enu.sys.platform` [W]

```
enu.sys.platform() -> "linux" | "darwin" | "windows"
```

```sh
enu -e 'return enu.sys.platform()'
```

```
linux
```

## `enu.sys.env` / `enu.sys.setenv` [W]

```
enu.sys.env(name) -> string?
enu.sys.setenv(name, value)
```

Reads and sets environment variables. `setenv` affects **only future subprocesses**
(it doesn't rewrite the environment of the `enu` process already running).

```lua
local home = enu.sys.env("HOME")
enu.sys.setenv("MI_FLAG", "1")   -- later enu.proc.run calls will see it
```

## `enu.sys.now_ms` / `enu.sys.mono_ms` [W]

```
enu.sys.now_ms() -> number   -- wall clock (epoch ms)
enu.sys.mono_ms() -> number  -- monotonic clock
```

Use `now_ms` for timestamps; use `mono_ms` to **measure durations** (it doesn't jump
with clock adjustments).

```sh
enu -e '
local t0 = enu.sys.mono_ms()
local s = 0; for i=1,1000 do s = s + i end
return (enu.sys.mono_ms() - t0) >= 0
'
```

```
true
```

## `enu.sys.hostname` [W]

```
enu.sys.hostname() -> string
```

Machine name. Together with `pid` it forms the **writer identity** for the
session locks.

## `enu.sys.pid` [W]

```
enu.sys.pid() -> integer
```

Pid of the `enu` process **itself**. Don't confuse it with
[`enu.proc.alive(pid)`](/enu/en/api/proc/#enuprocalive-w), which validates *other*
pids: `pid()` is your own.

```sh
enu -e 'return enu.sys.pid() > 0'
```

```
true
```

```lua
-- Writer identity for a lockfile.
local quien = enu.sys.hostname() .. ":" .. enu.sys.pid()
```

:::note[API level]
`enu.sys.pid()` was the first addition to the frozen API (it bumped `enu.version.api`
from 1 to 2). A good reminder that the surface **grows only by addition**.
:::
