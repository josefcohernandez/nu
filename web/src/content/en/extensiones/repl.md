---
title: The repl extension
description: An interactive Lua interpreter over the public API — activatable standalone, with structured evaluation, multi-line mode, and headless use for scripts and tests.
---

## What it does

`repl` is a **Lua REPL** over the core's public API: `enu` with only `repl`
active is an interactive Lua interpreter with access to `enu.*`. It's proof
that the runtime is good for more than the agent. It compiles and runs user
code with `load` — the basis of PUC-Lua 5.4, which compiles a string in
memory without blocking IO and which the core's sandbox leaves available on
purpose —: no new primitive was needed to build it.

It follows the classic Lua REPL semantics:

- A loose **expression** is evaluated and shows its value without writing
  `return` (`1 + 1` prints `2`; `enu.version.api` prints its level).
- A **statement** (`x = 5`, a `for`) is executed and prints nothing.
- An **incomplete** block (an unclosed function or `do`) isn't an error: it
  asks for another line, with the `..` continuation prompt (multi-line
  mode).
- A user error is caught without bringing down the REPL. The core's
  structured errors are shown as `code: message` (e.g. `ENOENT: no such
  file`).
- Strings are quoted when printed (`"hello"` is distinguished from an
  identifier); everything else goes through `tostring`.

## How it's activated

`plugin.toml` **declares no `requires`**: the REPL activates standalone,
without pulling in the harness. In `enu.toml`:

```toml
# ~/.config/enu/enu.toml
[plugins]
enabled = ["repl"]
```

- **On TTY**, it starts the interactive UI on the `core:ready` event (the
  last one of startup). You exit with `ctrl+d` or `/q` (`/quit`, `/exit`).
- **Yields to chat.** If the official set is active (chat *and* repl), chat
  is the one who owns the screen: the REPL mounts no UI and stays as a
  module accessible via `require("repl")`. So `enu` with the official set
  opens only chat; `enu` with only `repl` opens the REPL.
- **In headless mode** (`enu -e`, CI: no `enu.ui`) it mounts no UI; the module
  stays accessible for `repl.eval` and scripts.

The interactive UI uses the [toolkit](toolkit.md) as a **soft** dependency
(lazy `require`): it's needed to paint, not to evaluate. If the toolkit isn't
active, `repl.start` returns an actionable `EINVAL`, but `repl.eval` keeps
working without a screen.

## Configuration

The REPL has no config file of its own. The only adjustable thing is its
UI's theme, passed in code to `repl.start{ theme = ... }`.

## What it exposes

The public module is obtained with `require("repl")`:

| Signature | Effect |
|---|---|
| `repl.eval(src: string) -> table` | Evaluates a line of Lua **synchronously** and returns a structured result. The pure logic, tested headless. |
| `repl.eval_in_task(src: string, cb: function)` | Evaluates `src` **inside a task** and delivers the result to `cb`. This is the path when user code calls the core's suspending functions (`enu.fs.read`, `enu.http.request`…), which only run inside a task. |
| `repl.start(opts?: table) -> Repl` | Mounts the interactive UI (TTY only). `opts.theme` optional. `EINVAL` in headless mode or without toolkit. |
| `repl.banner() -> string` | The welcome banner (version, API level, and how to exit). |

The result of `repl.eval` (and what `eval_in_task`'s `cb` receives) is a
table:

```lua
-- success of an expression:
{ ok = true,  values = { 2 }, n = 1, display = "2" }
-- statement with no return:
{ ok = true,  values = {}, n = 0, display = "" }
-- execution or syntax error:
{ ok = false, error = <err>, display = "ENOENT: ..." }
-- incomplete input (ask for another line):
{ ok = false, incomplete = true, error = <msg>, display = "" }
```

`n` is explicit (not `#values`) to preserve the interleaved `nil`s of a
multiple return.

## Headless use

Since `repl.eval` is synchronous and needs no TTY, it's useful for
evaluating Lua without a screen — in a test, a script, or a pipe. For
expressions and calls to the **non**-suspending API, `eval` is enough; for
code that suspends, `eval_in_task`:

```lua
local repl = require("repl")

local r = repl.eval("enu.version.api")
print(r.display)                       --> the API level

repl.eval_in_task("enu.fs.read('README.md')", function(res)
  if res.ok then print(#res.values[1] .. " bytes") end
end)
```
