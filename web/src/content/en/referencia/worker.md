---
title: enu.worker — parallelism
description: Opt-in workers — isolated Lua states, JSON-able message passing, capabilities, and the channel with the parent.
---

`enu.worker` is real parallelism: a new Lua state in its own goroutine,
**with no shared memory**. Communication happens via **JSON-able message
passing** (copied, not by reference). `enu.worker.spawn` belongs only to the
main state.

:::tip[Do you need a worker?]
Almost never. "Lua decides, Go executes": if you're burning CPU in Lua, the
normal explanation is that a Go primitive is missing (search, diff, markdown
already are ones). A worker is for *your* heavy computation in pure Lua that
you don't want to freeze the loop.
:::

## `enu.worker.spawn`

```
enu.worker.spawn(module: string, opts?) -> Worker
```

Spins up a new Lua state loading `module` (resolvable by the loader). Inside
it, `enu.ui`, `enu.events` (the main bus), and nested workers **don't exist**;
the rest of the API marked **[W]** does.

### Capabilities (`opts.caps`)

`opts.caps?: string[]` restricts the worker's API to what's listed, with
**two granularities**:

- `"fs"` grants the whole module.
- `"fs.read"` grants a specific function.

Whatever isn't granted **doesn't exist** inside the state (capability-based
sandboxing; *deny-by-default* for new surface: functions added in the future
are never granted by old lists). Without `caps`, the worker receives the
whole [W] API.

```lua
-- A worker that can only read files and parse JSON: nothing else.
local w = enu.worker.spawn("my-plugin/analyzer", {
  caps = { "fs.read", "json" },
})
```

## Messages

```
Worker:send(msg) ⏸                  -- suspends if the queue is full (backpressure)
Worker:recv() -> msg ⏸
Worker:on_message(fn) -> Sub        -- callback alternative (main state)
Worker:terminate()                  -- immediate and safe (isolated states)
```

Messages are **JSON-able values, copied**: tables never cross by reference,
nor do closures, userdata, or Blocks. A worker sends **digested** data and
the main state renders it.

`recv` and `on_message` are **mutually exclusive**: registering one while the
other is pending throws `EINVAL` on the spot (never a silent priority).

```lua
-- Main state
enu.task.spawn(function()
  local w = enu.worker.spawn("my-plugin/worker")
  enu.task.cleanup(function() w:terminate() end)

  w:send({ task = "process", data = { 1, 2, 3 } })
  local result = w:recv()        -- waits for the digested response
  return result
end)
```

## Inside the worker

The worker talks to the parent over a symmetric channel:

```
enu.worker.parent.send(msg) ⏸
enu.worker.parent.recv() -> msg ⏸
```

```lua
-- my-plugin/worker.lua (the loaded module)
enu.task.spawn(function()
  while true do
    local msg = enu.worker.parent.recv()
    local total = 0
    for _, n in ipairs(msg.data) do total = total + n end
    enu.worker.parent.send({ total = total })
  end
end)
```

Each worker is a **complete mini-runtime**: its own scheduler, several tasks,
timers, and futures (all of `enu.task` [W]). **No watchdog**: workers exist to
burn CPU freely; control is `terminate()` from the parent, plus `caps`.
