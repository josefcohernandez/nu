# Plugin development guide

Status: living — grows with every lesson learned. This is not a contract:
it's the practical wisdom for writing plugins that work well within enu's
execution model ([modelo-ejecucion.md](modelo-ejecucion.md)). The exact
signatures are in [api.md](api.md) and the extension contracts are in
[agente.md](agente.md) / [chat.md](chat.md) / [providers.md](providers.md).

## 1. On load, a module only declares; the work happens when it's called

Loading means executing the top-level lines. If your setup touches
something that only exists in the main state (`enu.ui`, `enu.events`), your
module will blow up on `require` in any worker — even if the worker only
wanted to use some other, innocent function from the same module.

```lua
-- BAD: runs on load; explodes in workers
local bar = enu.ui.region{ x = 0, y = 0, w = 40, h = 1 }

-- GOOD: lazy; only fails whoever calls notify() where they shouldn't
local bar = nil
function M.notify(text)
  bar = bar or enu.ui.region{ x = 0, y = 0, w = 40, h = 1 }
  bar:blit(0, 0, enu.ui.block({ text }))
end
```

## 2. Only data crosses between states, never live state

Each worker loads **its own copy** of the modules: module variables aren't
shared with the main state. If a worker needs a value from the main state,
send it in the message. Only JSON-able values cross the boundary — never
functions, userdata, or Blocks. A worker returns *digested* results ("the
20 lines with errors"), not massive raw data; the main state renders.

## 3. Never block the loop

- ⏸ functions (IO) can only be called inside tasks. A synchronous handler
  (input, event, timer) that needs IO **spawns a task**:
  `enu.task.spawn(function() ... end)`.
- Heavy CPU work in Lua? Your tool is a worker — never the main state. The
  watchdog aborts slices that exceed their budget (~100 ms) and flags your
  plugin as suspicious.
- Work proportional to the screen or the repo? Don't do it in Lua: there's
  already a Go primitive (`enu.text.*`, `enu.search.*`). If there isn't one,
  it's probably a gap in the core — report it before reimplementing it
  slowly.
- To wait for a value that other code will produce (a dialog, a picker, a
  response), use `enu.task.future()` — never polling with `task.sleep`.
- **Every resource you create, register it with `enu.task.cleanup`**
  (killing the process, destroying the region, popping the input
  handler). Cleanups always run — success, error, or cancellation; it's
  the only way not to leave garbage behind when the user hits `esc` mid-way
  through your code.
- If you're hand-writing `caps` lists, mind the practical pairs:
  `proc.spawn` without `proc.kill` = processes you can't kill. The official
  bundles (`agent.caps.*`) already cure these together — print them to see
  exactly what they grant.
- Long-lived processes (an MCP server, a watcher): start them lazily
  (first use or `core:ready`), never on module load (§1), and kill them in
  `cleanup` and in `core:shutdown`.

## 4. Errors: throw structured, assume pcall at the boundaries

```lua
error({ code = "EINVAL", message = "empty filter", detail = { arg = "filter" } })
```

- The core wraps every hook in `pcall`: your error doesn't take anyone else
  down, but it gets logged against your plugin.
- In tool handlers, throwing is correct: the loop converts it into a
  `tool_result` with `is_error = true` and the model sees it. Don't return
  "successful" error strings.

## 5. Tools: the model is your user

- Args and result must be JSON-able (this also gets you the worker proxy
  for free). `description` and `schema` are the model's UX: write them as
  documentation, not as a formality.
- If your tool only reads, register it with `permissions = { default =
  "allow" }`; if it mutates (write, execute, network), leave it as `"ask"`.
  Don't self-grant `allow` on mutating tools: the permissions dialog is the
  user's trust in the whole ecosystem.
- Long or slow output: emit `ctx.progress(...)` — the UI paints it live.
- **Sanitize binary data at the source** (G11): if your tool can produce
  non-UTF-8 bytes (process output, arbitrary files), replace them visibly
  (`[binary output: 48KB omitted]`) before returning. The JSON codec is
  strict and will throw `EINVAL` downstream — far from your code and your
  context.
- **Redirects under control when facing third-party URLs** (G54): if your
  tool fetches URLs proposed by the model or coming from outside (a fetcher,
  a websearch), set `max_redirects = 0` and validate the destination of
  **every** hop before following it by hand — validating only the initial
  URL is defeated by a `302` towards the inside of the network
  (`169.254.169.254`). The cross-host header stripping (api.md §8) protects
  your credentials by default, but validating the *destination* is on you:
  the core doesn't know which hosts are legitimate for your tool.

## 6. UI: blocks, not cells; and clean up on exit

- Request Blocks from `enu.text.*` (markdown, wrap, highlight) and place
  them with `Region:blit`. If you're writing cell by cell on a hot path,
  you're doing the compositor's job — and slowly.
- Use the official toolkit unless you have a reason not to; if you go to
  raw `enu.ui`, you're responsible for your region: `input:pop()` and
  `Region:destroy()` too on error paths (wrap in `pcall` and clean up).
- No hardcoded colors: request colors from the toolkit's theme (`accent`,
  `error`, `dim`...) when building your Blocks — the toolkit resolves them
  to literals, because the core only accepts literals (G22). A plugin that
  hardcodes `#ff0000` breaks every theme but the author's. And if you cache
  Blocks or use theme colors over raw `enu.ui`, re-render on the toolkit's
  theme-change event — same treatment as `ui:resize`: your region, your
  repaint.
- Modal input: your handler returns `true` (consume) while it's active, and
  gets popped as soon as you're done. Don't leave orphaned handlers on the
  stack.
- **Your region, your `ui:resize`**: if you create regions by hand,
  subscribe and reposition (the core only guarantees error-free clipping —
  your picker centered for 120 columns will show clipped at 60 until you
  move it yourself). With the toolkit, relayout is automatic.
- **Scroll = cache the Block, move the offset** (G28): for a transcript
  with scroll, build the Block once and `blit(0, -scroll, doc)` with a
  different `scroll` per tick — `blit` with an offset is a copy, not a
  re-render. The antipattern is rebuilding the Block (re-rendering the
  markdown) on every scroll: that *is* expensive. Bound `scroll` with
  `doc.height` and the region's height. For huge histories, virtualize
  (render only what's visible) — that's your/the toolkit's job, the core
  doesn't retain your content.
- **Mouse hit-testing is yours** (G29): the mouse event arrives in screen
  coordinates; you set your region's `x,y,w,h` and applied your `scroll`,
  so the screen→content mapping (which block/line was clicked) is yours to
  resolve by subtracting origin and offset — same split as relayout: what
  depends on your layout is yours, not the core's. With the toolkit,
  routing clicks to widgets is automatic.
- Streaming content: re-render the in-progress message **once per paint
  tick** (repainting is already coalesced to ~30 ms), not per delta — Go
  rendering is cheap; what kills you is doing it a thousand times a second.

## 7. Living together in the ecosystem

- **Storage**: only under `enu.config.data_dir()/plugins/<your-name>/`.
  Sessions (`sessions/`) are read, not written — they belong to the agent.
  Credentials and tokens: in your own directory, `0600` —`enu.fs.write(path,
  data, { mode = 0600 })` sets the mode with a chmod not trimmed by the umask
  ([api.md](api.md) §5, G57)—, and never in the user's repo or in tool results
  (they'd end up in the transcript).
- **Your own events**: namespace = your plugin name
  (`"my-plugin:thing.happened"`). Since the loader guarantees your name is
  unique, nobody can step on it. Only `core:` and `ui:` are reserved by the
  core (they're its own surfaces); `agent:` is **not** a core reservation,
  it's the namespace of the official `agent` plugin just like yours is
  yours (G26) — no privilege: you can't call yourself `agent`, nor can the
  agent appropriate your name.
- **Be a library**: reusable code goes in your plugin's `lua/` — others
  will be able to `require("your-plugin.module")`. That's how Neovim's
  ecosystem got built, and that's how we want enu's.
- **Hooks**: register with the minimal `priority` needed and return `nil`
  when you have no opinion. A hook that modifies payloads it doesn't
  understand breaks the plugins that come after it in the chain.
- Don't monopolize: configurable keymaps (expose your defaults table, the
  way `chat.keys` does), regions with just the right `z`, and no capturing
  global input "just in case."

## 8. Compatibility

- **The interpreter is Lua 5.4** (official PUC-Lua compiled to WebAssembly
  over wazero; see [api.md](api.md) §1.2). If you're coming from Lua 5.1
  —or from scripts written for the old gopher-lua backend—, the standard
  library changed: `loadstring(s)` was absorbed into `load(s)` (accepts the
  string directly), `unpack` is now `table.unpack`, `setfenv`/`getfenv` are
  gone (the environment is the lexical upvalue `_ENV`), and
  `table.getn`/`string.gfind`/`math.mod`/`math.log10` are gone too. In
  exchange you get real integers (integer division `//`, integer `%`),
  native bitwise operators (`&`, `|`, `~`, `<<`, `>>`), and `goto`. Don't
  detect the version by hand: just write Lua 5.4.
- Detect capabilities with `enu.has()` and `enu.ui.caps()`, never by checking
  versions.
- Declare dependencies on other plugins in `plugin.toml` (`requires`) — the
  topological load order depends on it.
- If your module might end up in a worker (logic libraries), don't
  reference main-state-only modules either on load or in functions a
  worker would call. Trick: split `your-plugin/logic.lua` (worker-safe)
  from `your-plugin/ui.lua`.

## 9. Checklist before publishing

- [ ] `require` of all my modules works in a clean state (no effects on
      load).
- [ ] No synchronous handler does IO or heavy CPU work.
- [ ] Structured errors; no "successful" strings with errors inside.
- [ ] Mutating tools with `default = "ask"`; descriptive schemas.
- [ ] Regions and input handlers cleaned up on errors too.
- [ ] Only semantic colors; remappable keymaps.
- [ ] I only write to my own directory; my events carry my namespace.
- [ ] Lua 5.4 API (no 5.1 `loadstring`/`unpack`/`setfenv`/`getfenv`).
