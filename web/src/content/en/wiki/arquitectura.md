# Architecture of enu

Status: foundational draft. This describes the shape of the system, not a
closed specification. Decisions and their reasoning live in
[adr.md](../decisions/adr/README.md); the formal definition of the core's v1 API, in
[api.md](api.md); the dynamic view (communication, orchestration and
limitations, with diagrams), in [modelo-ejecucion.md](modelo-ejecucion.md).
Contracts of the official extensions: [providers.md](providers.md),
[sesiones.md](sesiones.md), [agente.md](agente.md), [chat.md](chat.md).
Practical conventions for authors: [guia-plugins.md](guia-plugins.md). What's
been postponed, with its reopening trigger: [pospuesto.md](../postponed/pospuesto.md).
Cracks pending resolution before freezing: [problemas.md](../findings/README.md).

## Overview

```
┌─────────────────────────────────────────────────────────┐
│                  User extensions (Lua)                  │
├─────────────────────────────────────────────────────────┤
│           Official extensions (Lua, go:embed)            │
│   agent · MCP · chat UI · commands · providers          │
├─────────────────────────────────────────────────────────┤
│                     Core API (v1)                        │
├─────────────────────────────────────────────────────────┤
│                  Kernel (Go, single binary)               │
│  scheduler · IO · network · terminal UI · text · codecs  │
└─────────────────────────────────────────────────────────┘
```

The kernel is a **runtime**: a stdlib of primitives plus an event loop. It
contains no agent logic, no MCP logic, no chat logic. The smaller the core is
conceptually, the more complete its surface of primitives has to be: pure Lua
can't do TLS or paint a terminal, so the kernel gives it that.

## The kernel: inventory of primitives

| Module | Responsibility |
|---|---|
| **scheduler** | Event loop, timers, ⏸ task-Lua ↔ goroutine bridge (implemented with tasks as native Lua coroutines on wazero, ADR-020, after gopher was retired in M17; the previous goroutine-per-task + Lua token implementation from ADR-011 was replaced), workers |
| **io** | Filesystem, process spawning with streams, environment, parallel tree search (`files`/`grep`, G51) |
| **net** | HTTP/HTTPS client with streaming response (SSE), TCP/websocket |
| **ui** | Cells + regions + compositor (z-order, block blit, damage tracking, coalescing ~30 ms), input events, keymaps |
| **text** | UTF-8/graphemes, regex, markdown rendering, syntax highlighting |
| **data** | JSON, TOML and YAML codecs (G51) |
| **loader** | `require`, plugin paths, embedded extensions |

Notes:

- **text** includes markdown and highlighting as builtins even though it
  violates the purity of the minimal kernel: in interpreted Lua they'd be
  painfully slow. It's the same concession Neovim makes by embedding
  tree-sitter (ADR-004, the "Lua decides, Go executes" rule).
- The **ui** API is deliberately low-level (ADR-007): the core exposes
  cells/regions and a compositor; the **widget toolkit is an official
  Lua extension** (retained internally: dirty tree + nodes) that provides
  slots, focus, composition across plugins, **decoration** (box/border,
  padding, spinner, multi-span text — [ADR-018](../decisions/adr/README.md)) and the theme
  system — semantic color names are resolved here, not in the core (G22),
  and the theme wires its palette to markdown rendering
  (`Theme:markdown_opts`) —, and is versioned separately from the sacred
  API.
  Lua places pre-rendered blocks from `text`, not loose cells, on hot
  paths. It's the ADR-003 pattern applied to the UI: the core doesn't know
  what a widget is.

## Concurrency model: the browser model

Three legs (ADR-004):

1. **Single-threaded main Lua state.** UI, keymaps, hooks and
   orchestration. The single thread here is a *feature*: deterministic
   event ordering and zero data races for 95% of plugins. IO never blocks:
   Go goroutines do the work and publish results into the queue the Lua
   loop consumes; from the extension author's viewpoint everything is
   async via coroutines (`await`-style).
2. **Explicit workers.** A primitive like `worker.spawn()` raises another
   Lua state in another goroutine, with no shared memory, communicated by
   message passing. Real parallelism, opt-in, for the extension that needs
   to chew through data. Workers **have no access to the `ui` module**: the
   screen is only painted from the main state (like Web Workers with
   respect to the DOM). Messages are copies — a worker returns digested
   results, not raw bulk data. Optionally, a worker can be born with a
   trimmed API (`opts.caps`): modules not granted don't exist inside the
   state — capability-based sandboxing for subagents and untrusted code.
3. **Parallel Go primitives underneath.** `core.search()` and friends
   saturate every core without Lua ever noticing. Raw performance never
   depends on the interpreter's speed.

Technical constraint that drives the design: the embedded Lua interpreter
**is not thread-safe** (neither today's PUC-Lua instance on wazero nor the
legacy gopher-lua were); a Lua state can only be touched from one goroutine.
The pattern is the same as Node/libuv/`vim.uv`, already validated.

Isolation is **per task, not per plugin** (ADR-008): all plugins coexist in
the main state — which lets them `require` each other and compose, as in
Neovim — and robustness is obtained with two core guards:

- **Watchdog**: every handler has a time budget in the main state; if it
  exceeds it, it's aborted via context cancellation and the plugin is
  marked suspect.
- **`pcall` at every hook boundary**: an error in a plugin never brings
  down the event loop or the other plugins.

## Extension layers

- **Layer 1 — Embedded Lua.** The universal mechanism: lifecycle hooks,
  commands, UI, keybindings, and also the agent itself and the LLM
  protocol adapters. v1 distribution: `~/.config/enu/plugins/` + git clone;
  no package manager of its own for now.
- **Layer 2 — External processes.** Heavy tools or tools in other
  languages via subprocess (JSON-RPC/stdio). MCP lives here,
  **implemented as an official Lua extension** on top of the `io.spawn` +
  codecs primitives: the core doesn't know what MCP is.

## LLM providers

Data/code split (ADR-005):

- **TOML** declares the registry: endpoints, API keys, models, context
  limits. Configuration, not programming.
- **Protocol adapters in Lua** (official extensions) implement each
  dialect (Anthropic, OpenAI, Gemini, Ollama...): SSE format, tool calls,
  system prompts, thinking blocks. Parsing SSE in Lua is viable: it's text
  at human-reading speed.

Adding an exotic provider (vLLM, corporate proxy) is a Lua file, not a
recompile. The adapter contract and the registry format are in
[providers.md](providers.md).

## Distribution

- Static Go binary, `CGO_ENABLED=0`, cross-compiled to every platform. v1
  support: native Linux and macOS; on Windows, **WSL2** (G9) — this way
  the POSIX contract holds in full without a conditional spec. Native
  Windows: [P18](../postponed/pospuesto.md).
- Official extensions embedded with `go:embed` but **inactive by
  default** (ADR-010): explicit activation (bare runtime screen with a
  TTY — api.md §14 —, the `enu --default-config` flag without a TTY, or a
  hand-written `enu.toml`), no network; overridable by the user from
  their config directory. The **official product set** is the embedded
  extensions minus the `example` scaffolding and the `mesh` (ADR-015;
  [malla.md](../contracts/malla.md) §1.4): besides the
  harness (agent, chat, providers, MCP, toolkit), an **`repl`** —a Lua
  REPL over the public API, standalone-activatable, the starting point for
  extension authors who don't want the harness (G21)—. With a TTY, **a
  single primary UI owns the screen**: the repl **yields to chat** (it
  only auto-mounts its UI if chat isn't active, via `enu.plugin.list`), so
  `enu` with the official set opens a single TUI and not chat *and* the
  REPL overlapping ([G36](../findings/g36-el-conjunto-oficial-de-producto.md), [ADR-018](../decisions/adr/README.md)). The
  **`mesh`** ([malla.md](../contracts/malla.md), born from pseudocode round 8) ships
  embedded but activates explicitly: it's the agent-mesh orchestration
  tool, not the default harness.

## Persistence

Agent sessions are saved as append-only JSONL under
`data_dir()/sessions/`, reusing the canonical message model; it's a
public convention readable by other extensions, not a core primitive.
Full contract in [sesiones.md](sesiones.md). The rest of the extensions
write under `data_dir()/plugins/<name>/`.

<!-- enu:interno -->

## Open questions

1. ~~**ADR-007 validation spike**: cells/regions + compositor + minimal
   Lua toolkit, tortured with (a) token streaming with markdown to a full
   screen and (b) fuzzy picker over ~100k files. Pre-committed veto
   criterion: if it isn't smooth, the toolkit gets implemented in Go
   keeping the same public API.~~ **RESOLVED** by the S28 spike
   ([ADR-012](../decisions/adr/adr-012-resultado-del-spike-de-adr.md)):
   the overhead of orchestrating from Lua turned out negligible (the heavy
   work is a Go primitive), so **the veto did NOT fire** and the toolkit
   is built in Lua. ADR-007 was promoted to Accepted.
2. **Fine-grained watchdog policy**: the base budget is already fixed
   (100 ms, configurable in `enu.toml` — api.md §1.3); what's left is the
   fine print: whether it's configurable per plugin and the
   disabling/user-notification flow after `core:plugin.misbehaved`.
3. **Design of the official toolkit's public API** (widget vocabulary,
   layout, slots, focus): it's not sacred core API, but the ecosystem will
   inherit its quality.
4. ~~**MCP extension contract**: cited throughout the documentation
   (ADR-003, [agente.md](agente.md) §3, layer 2) but without its own
   document — configuration format (which servers, how they're declared),
   process lifecycle, tool mapping and their trust.~~ **RESOLVED** by the
   S41 implementation (`mcp` extension, [implementacion.md](../plan/implementacion.md)).
   The contract was fixed while building it —pure Lua on top of the public
   API, without touching the core (the completeness corollary satisfied)—:
   - **Configuration** (data/code split, ADR-005): servers are DECLARED in
     `mcp.toml` (`enu.config.dir()`), format
     `[servers.<name>] command = [...] cwd? env?`. Absent → nothing
     connects. They can also be connected by hand with `mcp.connect{ name,
     command, cwd?, env? } ⏸ -> Conn`.
   - **Process lifecycle**: the server is launched with `enu.proc.spawn`,
     lives as long as its `Conn` exists, and is killed cleanly
     (`Proc:kill` registered in `enu.task.cleanup` + idempotent
     `Conn:close()`, [api.md](api.md) §6). A server that dies (EOF on
     stdout) wakes up every pending request with `EMCP` (nobody hangs).
     The dialogue is JSON-RPC 2.0 over stdio with **line framing** (one
     line = one JSON message), demultiplexed by `id` with a dedicated
     reader task.
   - **Tool mapping and trust**: each tool the server announces
     (`tools/list`) is registered with `agent.tool{...}` ([agente.md](agente.md)
     §3) under the prefix `mcp__<server>__<tool>`; its handler makes a
     `tools/call` over JSON-RPC. **Trust** —these are THIRD-PARTY tools—
     is governed by the agent's permission pipeline ([agente.md](agente.md)
     §5): they're registered with `permissions.default = "ask"`, so they
     require explicit permission (`allow = {"mcp__<server>__*"}`) and in
     headless mode without it they're DENIED with an actionable error.
     There's no special case: an MCP tool goes through the same fence as
     any other.
5. ~~**CLI surface**: `enu -e` and `--auto-permissions` appear in the
   contracts without a specification of their own (flags, subcommands,
   headless behavior, exit codes). The resume sugar (a `--continue` over
   `agent.session{ resume }`) will be decided here: G18 deliberately left
   it out of the contracts.~~ **RESOLVED** by the S45 implementation
   ([implementacion.md](../plan/implementacion.md)). The CLI surface lives in the
   **binary** (`main.go`), NOT in the sacred `enu.*` API (api.md): it's the
   command-line interface of the executable, and the core still doesn't
   know what an agent is (ADR-003) — the CLI orchestrates the extensions
   (`agent`, `sessions`) through the public API, as a user's `init.lua`
   could. What was fixed:
   - **Flags**: `enu -e '<lua>'` (evaluates a headless Lua chunk and prints
     its returns, since S01); `enu -p '<prompt>'` (runs a **headless agent
     turn** — agente.md §1, "free scripting/CI mode" — and prints the
     assistant's final text to stdout); `--auto-permissions` (agent
     permissions in `"auto"` mode, agente.md §5 amortizer 3 — without it,
     in headless mode sensitive tools are DENIED); `--model
     'prov/model'` (overrides the default model from `agent.toml`);
     `--continue`/`-c` (resume sugar, below); `--default-config`
     (activates the **official product set** without a TTY —the onramp
     that G21's bare screen didn't cover—: standalone, writes
     `plugins.enabled` in `enu.toml` —and active templates of
     `agent.toml`/`providers.toml` if they don't exist, to leave the
     harness usable, ADR-017/G35— and exits; with `-p`/`-e`, it activates
     it only for that process without touching disk. ADR-015, G33).
   - **Headless / exit codes**: `enu -e` and agent mode run WITHOUT a TTY
     (G20) with exit codes consistent for CI/scripts — **0** success;
     **1** execution error (the chunk, the turn, or the provider threw, or
     startup failed); **2** invalid usage (flags/arguments); **3**
     permission denied in headless mode (a sensitive tool was denied for
     lack of `--auto-permissions`, agente.md §5 — a DIFFERENT code so a
     script can tell "the model couldn't act because of permissions" from
     an execution failure).
   - **`--continue` (G18)**: resumes the project's (cwd) MOST RECENT
     session before sending the prompt — `sessions.list(cwd)` (ids sort
     lexicographically = temporally, sesiones.md §2/§7) picks the last
     one, which is passed as `resume` to `agent.session{...}`. It's the
     `--continue` that G18 deliberately left out of the contracts for
     belonging to this surface.
   - **Startup** (S33): no args and with a TTY → normal startup (bare
     runtime screen if there are no plugins, G21); no args and without a
     TTY → usage (code 2); `enu -e`/`-p`/`--continue` → headless mode.
     `--default-config` alone (no headless action) writes the product set
     to `enu.toml` —plus `agent.toml`/`providers.toml` templates if
     missing (ADR-017/G35)— and exits (G33): the onramp without a TTY that
     the bare screen didn't give.
   The headless executor for the suspending modes (the agent turn is ⏸)
   is `Runtime.EvalTaskString` (runs a Lua chunk as a TASK to completion):
   a Go interface of the binary, NOT sacred Lua surface (like
   `EvalString`/`RenderBareScreen`); api.md stayed UNCHANGED (completeness
   corollary satisfied: the public API + the extensions were enough,
   without a `G##` finding).

<!-- /enu:interno -->
