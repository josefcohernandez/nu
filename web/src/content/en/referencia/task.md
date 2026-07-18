---
title: enu.task — concurrency
description: enu's scheduler — tasks, sleep, all/race, timers, defer, futures, cancellation, and cleanup.
---

`enu.task` is the scheduler: cooperative coroutines over the main state's event
loop. It's where all async work lives. Review the model in
[Key concepts](/enu/en/docs/conceptos/#3-el-modelo-de-concurrencia-del-navegador)
if it's not yet clear to you.

The whole module is available in workers **[W]** (each worker is a
mini-runtime with its own scheduler).

## `enu.task.spawn` [W]

```
enu.task.spawn(fn, ...) -> Task
```

Launches a task (a coroutine managed by the scheduler). Extra arguments are
passed to `fn`. Returns a `Task` handle you can await or cancel.

It's the gateway to IO: a synchronous handler (input, events) that needs to
do IO launches a task with `spawn`.

```lua
local t = enu.task.spawn(function(name)
  return "hello, " .. name
end, "world")
```

## `Task:await` ⏸ [W]

```
Task:await() -> any
```

Waits for the result of another task. Suspends until it finishes.

```sh
enu -e '
enu.task.spawn(function()
  local t = enu.task.spawn(function() enu.task.sleep(10); return 42 end)
  local v = t:await()
  enu.fs.write(enu.fs.tmpdir().."/r.txt", tostring(v))  -- v == 42
end)
return "launched"
'
```

(Remember: `await` is ⏸, so it goes inside a task; in `enu -e` the chunk isn't
one, which is why we wrap it in `spawn`.)

## `enu.task.sleep` ⏸ [W]

```
enu.task.sleep(ms)
```

Suspends the current task for `ms` milliseconds, without blocking the loop.

```lua
enu.task.spawn(function()
  enu.task.sleep(500)
  -- half a second later
end)
```

## `enu.task.all` ⏸ [W]

```
enu.task.all(fns: Task[] | fn[]) -> any[]
```

Waits for **all** tasks (or functions, which are launched as tasks). If one
throws, it cancels the rest and re-throws. Results are returned **aligned
with the inputs** (`out[i]` corresponds to `fns[i]`), never in completion
order: that way you correlate result with input in a fan-out without carrying
the index by hand (it's `enu`'s `Promise.all`).

```sh
enu -e '
enu.task.spawn(function()
  local r = enu.task.all({
    function() return "a" end,
    function() return "b" end,
    function() return "c" end,
  })
  enu.fs.write(enu.fs.tmpdir().."/all.txt", enu.json.encode(r))  -- ["a","b","c"]
end)
return "ok"
'
```

## `enu.task.race` ⏸ [W]

```
enu.task.race(fns) -> (winner_index, result)
```

The first task to finish wins; the rest are canceled. Returns the **index**
of the winner and its result. The classic pattern: an operation with a
timeout.

```lua
enu.task.spawn(function()
  local i, res = enu.task.race({
    function() return enu.http.request{ url = "https://slow.example" } end,
    function() enu.task.sleep(2000); return "timeout" end,
  })
  if i == 2 then error({ code = "ETIMEOUT", message = "took too long" }) end
  return res
end)
```

## `enu.task.every` [W]

```
enu.task.every(ms, fn) -> Timer
  Timer:stop()
```

Periodic timer: runs `fn` (a **synchronous** handler) every `ms`. Returns a
`Timer` with `Timer:stop()`.

```lua
local timer = enu.task.every(1000, function()
  -- every second; synchronous: for IO, spawn a task in here
end)
-- ...
timer:stop()
```

## `enu.task.defer` [W]

```
enu.task.defer(fn)
```

Runs `fn` on the **next tick** of the loop. Useful for postponing work right
after the current frame.

```lua
enu.task.defer(function()
  -- runs once the current tick's work has drained
end)
```

## `enu.task.future` [W]

```
enu.task.future() -> Future
  Future:set(v)            -- synchronous, one-shot (calling it again throws EINVAL)
  Future:await() -> v  ⏸   -- several can wait; if already set, returns immediately
```

Single-use rendezvous: the building block for "one task waits for a value
another piece of code will produce" (dialogs, pickers, proxies) **without
polling**. `set` is synchronous (it can be called from an input or event
handler); `await` suspends.

```sh
enu -e '
local f = enu.task.future()
enu.task.spawn(function()
  local v = f:await()                       -- waits for the value
  enu.fs.write(enu.fs.tmpdir().."/fut.txt", v)
end)
enu.task.spawn(function()
  enu.task.sleep(10)
  f:set("resolved")                         -- another task produces it
end)
return "ok"
'
```

## `Task:cancel` [W]

```
Task:cancel()
```

**Cooperative** cancellation: aborts the task at its next suspension point,
**without going through `pcall`** (it's not a catchable error). Its
`cleanup`s run. You observe the result as `ECANCELED` if you `await`.

```lua
local t = enu.task.spawn(function()
  while true do enu.task.sleep(100) end   -- indefinite work
end)
t:cancel()  -- stops it at the next sleep
```

## `enu.task.cleanup` [W]

```
enu.task.cleanup(fn)
```

Registers a **synchronous** releaser on the current task's LIFO stack. All of
them run when the task ends — success, error, **or abort**
(cancellation/watchdog). It's this house's `defer`: the reliable way to close
processes, regions, or handlers no matter what happens.

```lua
enu.task.spawn(function()
  local proc = enu.proc.spawn({ "long-running" })
  enu.task.cleanup(function() proc:kill() end)  -- always gets killed
  -- ... even if this throws or the task is canceled, the process dies
end)
```

:::caution[Why cleanup and not pcall]
Cancellation and the watchdog unwind the stack **without** going through
`pcall` — otherwise any `pcall` in the ecosystem would swallow them and the
program would carry on as if nothing happened. That's why resource release
goes in `cleanup`, not in a `pcall`/`finally`.
:::
