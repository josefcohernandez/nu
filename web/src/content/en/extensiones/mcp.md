---
title: The mcp extension
description: Integrates MCP (Model Context Protocol) servers as agent tools — pure Lua over enu.proc and enu.json, with declarative configuration in mcp.toml and per-server permissions.
---

## What it does

`mcp` connects **MCP servers** (Model Context Protocol) to the agent: every
tool a server announces is registered as one more agent tool, just like a
file one. It's pure Lua over the public API — true to the core not knowing
what MCP is —: it launches the server as a subprocess with `enu.proc`, talks
JSON-RPC 2.0 to it over stdio encoding with `enu.json`, and registers the
tools with `agent.tool{...}`. Framing is **newline-delimited** (one line =
one JSON message terminated by `\n`), MCP's stdio transport.

A dedicated reader demultiplexes responses by their `id`, so several
`tools/call` can be in flight at once without mixing up. If the server dies
(EOF on its stdout), every pending request wakes up with an `EMCP` error:
nothing hangs forever.

## How it's activated

`plugin.toml` declares `requires = ["agent"]`, so activating `mcp` pulls in
the agent. Add it to `enu.toml`:

```toml
# ~/.config/enu/enu.toml
[plugins]
enabled = ["providers", "sessions", "agent", "mcp"]
```

Loading the extension **doesn't connect to any server**: launching one is an
act of the user. If an `mcp.toml` exists in the config directory, the
extension auto-connects its servers lazily (in a task, tolerating the
absence of the file, which is the normal case).

## Configuration

Servers are declared as **data** in `mcp.toml`, inside `enu.config.dir()`
(normally `~/.config/enu/`):

```toml
# ~/.config/enu/mcp.toml
[servers.github]
command = ["mcp-server-github"]   # server argv (no shell), required
cwd     = "/opt/project"          # optional
env     = ["GITHUB_TOKEN=..."]    # optional
```

Each `[servers.<name>]` entry launches a process; `<name>` is the prefix of
its tools. Without `mcp.toml` nothing connects. A server that fails to
connect gets logged and **doesn't block** the others.

### Tools and permissions

Every remote tool registers under the name `mcp__<server>__<tool>` — the MCP
ecosystem's namespacing convention —, which avoids clashes between servers
and keeps the permission pattern readable. Since these are **third-party**
tools, they register with `permissions.default = "ask"`: they require
explicit permission, never granted on their own like native read-only ones.
Enable them with a pattern in the agent's permission config:

```toml
allow = ["mcp__github__*"]
```

In headless mode without that `allow`, the agent's permission pipeline
**denies** them with an actionable error. If the server disconnects, its
tools stay re-registered with a handler that fails actionably (it warns the
model that reconnecting is needed) instead of breaking the loop.

## What it exposes

The public module is obtained with `require("mcp")`. Its errors use the
`EMCP` code (with the core's structured shape: `{ code, message, detail? }`).

| Signature | Effect |
|---|---|
| `mcp.connect(opts: table) -> Conn` **⏸** | Launches a server and completes the handshake (`initialize` → `initialized` → `tools/list` → registration). `opts`: `{ name, command, cwd?, env? }`. Reconnecting an already-connected `name` closes the previous connection. |
| `mcp.connect_configured() -> Conn[]` **⏸** | Launches every server from `mcp.toml`. |
| `mcp.get(name: string) -> Conn?` | The live connection of a server, or `nil`. |
| `mcp.servers() -> string[]` | The names of the connected servers. |

On a `Conn` handle:

| Signature | Effect |
|---|---|
| `Conn:call_tool(remote_name: string, args?: table) -> result` **⏸** | Invokes `tools/call` on the server (`remote_name` is the name without the prefix). |
| `Conn:list_tools() -> tool[]` **⏸** | Requests `tools/list`; each tool is `{ name, description?, inputSchema? }`. |
| `Conn:close()` | Kills the server cleanly and unregisters its tools. Idempotent. |

The process's lifetime follows that of the task that called `connect`: when
that task ends (or on `Conn:close()`), the server dies. For a long-lived
server, connect from a task that lives as long as the session.

```lua
local mcp = require("mcp")

enu.task.spawn(function()
  local conn = mcp.connect{ name = "github", command = { "mcp-server-github" } }
  -- From here on, the mcp__github__* tools are available to the agent.
  for _, t in ipairs(conn:list_tools()) do
    enu.log.info("MCP tool available: %s", t.name)
  end
end)
```

The client negotiates the MCP protocol version at `initialize` and
announces its data as `clientInfo` (`name = "enu"`); v1 covers text tools,
an MCP server's central use case.
