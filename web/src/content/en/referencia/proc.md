---
title: enu.proc — subprocesses
description: Running and controlling subprocesses — run with buffers, spawn with streams, and detecting live processes.
---

`enu.proc` launches subprocesses. Available in workers **[W]**. **No implicit
shell**: `argv` is an array of strings; whoever wants a shell invokes it
explicitly (`{"sh", "-c", "..."}`).

## `enu.proc.run` ⏸ [W]

```
enu.proc.run(argv: string[], opts?) -> { code, stdout, stderr }
```

Buffered convenience: runs, waits, and returns the full output. `opts`:
`cwd`, `env`, `stdin`, `timeout_ms`.

```sh
enu -e '
enu.task.spawn(function()
  local r = enu.proc.run({ "echo", "hello" })
  enu.fs.write(enu.fs.tmpdir().."/o.txt", enu.json.encode(r))
  -- r == { code = 0, stdout = "hello\n", stderr = "" }
end)
return "ok"
'
```

With standard input and a directory:

```lua
enu.task.spawn(function()
  local r = enu.proc.run({ "grep", "TODO" }, {
    cwd = "/project",
    stdin = "line1\nTODO: something\nline3\n",
    timeout_ms = 5000,
  })
  return r.stdout
end)
```

## `enu.proc.spawn` [W]

```
enu.proc.spawn(argv, opts?) -> Proc
```

Fine-grained control with streams (for long-running or interactive
processes). Returns a `Proc`:

```
Proc:write(data) ⏸ [W]                                  -- writes to stdin
Proc:close_stdin() [W]
Proc:read_line(which: "stdout"|"stderr") -> string? ⏸ [W]  -- nil at EOF
Proc:read(which, n?) -> string? ⏸ [W]                   -- raw read
Proc:wait() -> { code } ⏸ [W]
Proc:kill(signal?) [W]                                  -- TERM by default
```

```lua
enu.task.spawn(function()
  local p = enu.proc.spawn({ "cat" })
  enu.task.cleanup(function() p:kill() end)   -- safety net

  p:write("one line\n")
  p:close_stdin()

  local line = p:read_line("stdout")         -- "one line"
  local res = p:wait()                        -- { code = 0 }
end)
```

:::caution[Process lifetime]
The rule is to kill the process explicitly via
[`enu.task.cleanup`](/enu/en/api/task/#enutaskcleanup-w) in whoever creates
it. As a safety net, a `Proc` with no references ends up killed by the GC,
but that's **non-deterministic**: don't rely on it.
:::

## `enu.proc.alive` [W]

```
enu.proc.alive(pid: integer) -> boolean
```

Is there a live process with that `pid` on this machine? It reports
**existence, not identity**: a recycled pid gives `true`. Useful for
detecting orphaned locks (combine it with
[`enu.sys.pid`](/enu/en/api/sys/) and `enu.sys.hostname`).

```lua
-- Is the lock's owner still alive?
if not enu.proc.alive(lock_pid) then
  -- orphaned lock: it can be reclaimed
end
```
