# The official agent extension: contract

Status: **draft for discussion**. Like providers and sessions, this is NOT
sacred core API: it's the public contract of the official `agent`
extension, versioned separately. Built entirely on top of [api.md](api.md),
[providers.md](providers.md) and [sesiones.md](sesiones.md) — if something
here can't be implemented with those three surfaces, it's them that are
incomplete (ADR-003).

## 1. Structural decision: engine without UI

The `agent` extension is a **headless engine**. It paints nothing: it runs
the loop, runs tools, emits events. The chat interface is **another**
official extension (`chat`) that consumes this contract just as any third
party could. Sought-after consequences:

- Free scripting/CI mode: `enu -e "script.lua"` can use the agent without
  an interactive terminal.
- Subagents can run in workers (without `ui`) with no special case.
- The official UI has no privileged access: the public API is enough, or
  it's incomplete.

## 2. Sessions and turn

```
agent.session(opts) ⏸ -> Session   -- suspending IO: writer lock and, with resume, replay (A-28)
  opts: { model: "provider/model", system?, cwd?, tools?: string[],
          skills?: string[], permissions?: Permissions, parent?,
          thinking?: { mode?: "off"|"adaptive"|"budget", budget?: integer },
          resume?: string }                          -- id: reopens instead of creating

Session:send(content: string|Block[]) ⏸ -> Message  -- runs the full turn
Session:retry() ⏸ -> Message                         -- re-runs the turn after an error (G43)
Session:cancel()                                     -- cancels the turn in progress
Session:fork(at?: integer, opts?: table) ⏸ -> Session -- forks and re-homes; copies the prefix (G39; sesiones.md §5)
Session:compact() ⏸                                  -- manual compaction
Session:set_model(model: string)                     -- hot swap (G19)
Session:set_thinking(thinking)                        -- hot reasoning change (ADR-016)
Session:close()                                      -- releases the writer lock (G39); synchronous on purpose: callable from enu.task.cleanup
Session.id / Session.usage -> { context_tokens, cost_usd, turns }
```

> **Implementation status.** ✅ Implemented `send/spawn/set_model/close` and
> also `cancel`, `fork`, `compact` and `clear_queue` ([pospuesto.md](../postponed/pospuesto.md)
> **P22**, resolved). The turn runs in a task **owned by the session** (the
> one `cancel` cancels); `send` waits for the result via a future, not the
> task, so canceling the turn doesn't cancel whoever called it (its `send`
> returns nil).
> ⏳ Pending construction (G39): `fork`'s `opts?` and its full inheritance
> rule — v1 inherits a partial list that loses `skills` and `thinking`.

**The turn** (`send`) is the heart of the contract:

1. Appends the user's message (a `message` entry in the transcript).
2. Assembles the canonical request (§7) and passes through `request.pre`
   hooks.
3. Calls the adapter (`stream`); re-emits the deltas on the bus
   (`agent:delta`) for whoever paints them.
4. On `done`: persists the message (with `usage` and model), emits
   `agent:message`.
5. If `stop_reason == "tool_calls"`: for each tool call, **in order**
   (parallel execution is postponed, [P12](../postponed/pospuesto.md)): permission
   pipeline (§5) → `tool.pre` hooks → handler → `tool.post` hooks →
   `tool_result`. Then, back to step 2.
6. Ends when the model stops without requesting tools, or upon exhausting
   `max_turns` (configurable; loop protection).

**Reentry (G4)**: `send` with a turn in flight **queues** the message; the
loop injects it when assembling the next request (between iterations,
never mid-stream). This lets the user correct the agent while it's working
("use pnpm, not npm"). All `send` calls consumed by the same turn resolve
with that turn's final message. `Session:cancel()` cancels the turn,
**not** drain the queue (draining it is a separate action:
`Session:clear_queue()`). *(✅ Implemented: [pospuesto.md](../postponed/pospuesto.md) **P23**.
The loop drains the queue at the start of each iteration; every `send` consumed
by a turn resolves with its final message.)*

**Resumption (G18, G46)**: `opts.resume = <id>` reopens an existing session
instead of creating one: replay of the transcript ([sesiones.md](sesiones.md)
§3) and acquisition of the writer lock (§6, with its conflict flow — fork,
read-only, or force). The replay reconstructs the history **and reapplies
the agent's `event` entries** — the session continues *where it left off*,
not where it started — with explicit precedence (G46): **resume opts >
transcript `event` > `agent.toml`**. The `opts` remain ephemeral process
state — they aren't persisted or rewriting history (G18) —, but they only
override the transcript *when given*: a `resume` without `model` is
governed by the last recorded `set_model` (last-wins, sesiones.md §3), not
by the default. For repeatable ones (`set_model`, `set_thinking`) the last
entry wins; the cumulative ones (`allow`/`deny`, §5) are **reapplied in
order** on top of the base policy and no opts overrides them — hot
permissions are a safety lever; losing them on resume would be surprising.
If the recorded model no longer resolves (the provider disappeared),
resuming fails with `EPROVIDER` on open — better than on the first turn —;
the escape hatch is an explicit `opts.model`, which takes precedence. The
id comes from the session listing (sesiones.md §7).

**Model change (G19)**: `Session:set_model("provider/model")` validates
against the provider registry, writes an `event` entry to the transcript
([sesiones.md](sesiones.md) §3) and applies from the next request onward;
with a turn in flight, when assembling the next iteration (as with
reentry), never mid-stream.

**Fork and close (G39)**: `Session:fork(at?, opts?)` forks the history into
a **self-contained** new session — the prefix is **copied** to the child's
transcript and `meta.parent = { id, entry = at }` remains as a navigational
link ([sesiones.md](sesiones.md) §5). `at` indexes the **current message
history** (by default, the end; after a compaction, the current history
starts at the summary). The child **inherits all of the parent's ephemeral
opts** (model, cwd, system, permissions, skills, thinking, max_turns,
tools...) except those `opts` overrides, following `spawn`'s rule (§9,
§11): permissions **only trim, never expand**. The `opts` are ephemeral as
in `resume` (G18): they aren't persisted or rewriting history. It's the
piece for *fork-as-replication* (pseudocode, round 8): K variants sharing
the exact prefix, each re-homed in its worktree via `opts.cwd`.
`Session:close()` releases the writer lock ([sesiones.md](sesiones.md) §6)
and marks the session closed (idempotent; subsequent methods fail with an
actionable error). House rule: whoever opens sessions closes them
(`enu.task.cleanup`); GC as a non-deterministic safety net, same as
[api.md](api.md) §6's `Proc`.

**Reasoning control ([ADR-016](../decisions/adr/adr-016-modelo-canonico-de-thinking.md))**:
`opts.thinking` (or `agent.toml`'s `[thinking]` default, §10) fixes the
reasoning mode each canonical request will carry (`thinking`,
providers.md §2.1); `Session:set_thinking(mode|table)` changes it hot (same
flow as `set_model`: from the next request onward). The session only
chooses the **mode** (`"off"`/`"adaptive"`/`"budget"`); the **dialect**
each model understands is resolved by the adapter using the `thinking`
data from `providers.toml` (a model with dialect `"none"` ignores the
request). A `request.pre` hook can fine-tune `thinking` per turn.

**Retries (G42)**: if **opening** the stream (step 3) throws an error with
`detail.retryable = true` (the adapter's mark, [providers.md](providers.md)
§3: 429, 5xx, network drops), the engine waits with exponential backoff
(`retry_base_ms · 2^(attempt−1)`; 1 s → 2 s → 4 s by default) and retries up
to `max_retries` times (§10) — the policy lives here, never in the adapter.
Each wait is announced as `agent:retry` (§4) and sleeps at a normal
suspension point: a `cancel` during the backoff aborts the turn as always. A
failure **mid-stream** is never retried — the deltas already emitted are
painted and retrying would duplicate content; it propagates as a turn error.
Once retries are exhausted, the error propagates with its `retryable`
intact: the UI can offer the manual retry. The worker-mode subagent (§9)
applies the same policy (it inherits `max_retries`/`retry_base_ms` in its
`init`), without the event — workers have no bus.

**Manual retry (G43)**: `Session:retry()` re-runs the turn over the current
history **without appending a new message** — the path for a UI's retry
action after an `agent:error` (the user's message is already in the history;
a `send` would duplicate it). Same waiting contract as `send` (future of the
final message); `EINVAL` with a turn in flight, the session closed, or an
empty history.

## 3. Tools

```
agent.tool{
  name, description,
  schema: table,                  -- JSON Schema of the args
  handler: function(args, ctx) ⏸ -> string|Block[]|table,
  permissions?: { default = "ask"|"allow"|"deny" },
}
```

- The handler runs as a task: it can suspend (fs, proc, http...) without
  blocking anything. Thrown errors → `tool_result` with `is_error = true`
  (the model sees the error; the loop doesn't break).
- `ctx = { session, cwd, progress(text), ask(question) ⏸ }`. `progress`
  emits `agent:tool.progress` (the UI paints it live); `ask` triggers the
  §5 flow.
- The basic tools (file read/write/edit, bash, grep, glob...) are brought
  by the extension itself, registered with this very function —
  dogfooding.
- MCP fits here without a special case: the `mcp` extension registers each
  remote tool with `agent.tool{...}` and its handler speaks JSON-RPC via
  `enu.proc`.

## 4. Hooks

Two mechanisms, deliberately separate:

**Notifications** (fire-and-forget, core bus `enu.events`, namespace
`agent:`): `session.start`, `session.end`, `turn.start`, `turn.end`,
`delta`, `message`, `tool.start`, `tool.progress`, `tool.end`, `compact`,
`error`, `retry` (G42: `{ attempt, max_retries, delay_ms, code, message }`,
one per backoff wait), `permission.asked`, `permission.denied` (G40, §5). For
painting, logging, observing. *(The `compact` event will only be emitted once
automatic compaction exists: [pospuesto.md](../postponed/pospuesto.md) (P25).)* The
`agent:` namespace is not reserved by the core (the core doesn't know
about agents, ADR-003): it's the `agent` plugin's namespace, protected by
plugin-name uniqueness like any other (G26, [api.md](api.md) §4).

**Guaranteed visible error.** Any turn failure —the adapter/provider throws
(e.g. HTTP 401 from a missing or invalid API key, network down), a
`request.pre` hook vetoes, or `max_turns` is exhausted— is ALWAYS emitted
as `agent:error` (with the full structured error: `message` and, if it
carries them, `code`, `retryable` and `detail` — G43) before
closing the turn. The turn's body runs under `pcall`, so an error never
silently kills the task: the UI paints it and `Session:send` returns (it
doesn't hang). The only exception is a `Session:cancel` (S08 abort, not
catchable by `pcall`): it isn't an error, so it closes the turn as
**canceled** (`turn.end { canceled = true }`) without emitting
`agent:error`.

**Mandatory attribution (G3)**: every `agent:*` payload carries `session`
(the id of the emitting session; subagents emit with their own — their
`meta.parent` links to the parent). The extension emits through a single
helper, so the field is set in one place only. Filtering and presenting is
each UI's decision.

**Middleware** (can modify or veto; the extension's own registry, not the
bus):

```
agent.hook(point, fn, opts?: {priority}) -> Hook ; Hook:remove()

fn(payload, ctx) ->
    nil                  -- no opinion; chain continues
  | modified_payload     -- replaces and continues
  | { deny = "reason" }  -- cuts the chain; the operation is rejected
```

v1 points: `request.pre` (mutate the canonical request: inject context,
trim), `tool.pre` (veto/rewrite args), `tool.post` (rewrite result),
`permission` (§5), `compact` (§8). Order: ascending `priority`, then
registration order. **The first deny wins** and is reported to the model
as a rejection (in `tool.pre`) or to the caller as an error.

## 5. Permissions

```
Permissions = {
  mode  = "ask" | "auto",        -- "ask" by default
  allow = { "edit", "bash:git *", ... },   -- tool[:argument] patterns
  deny  = { "bash:rm *", ... },
}
```

Pipeline for each tool call: `deny` (cuts) → `allow` (grants) →
`permission` hooks (can grant/deny programmatically) → if nobody decides
and `mode = "ask"`: `agent:permission.asked` is emitted and the turn waits
for a response (`agent.permission.respond(id, ...)` — the `chat` extension
paints the dialog). **In headless mode — there's no `enu.ui`; the test is
`enu.has("ui")` ([api.md](api.md) §9, G20) — with no response there's no
grant: default deny**, with three amortizers that remove almost all the
friction:

1. **Read-only tools are registered with `default = "allow"`**
   (read, grep, glob...): they never ask for permission, not even
   headless. The deny only bites the ones that mutate (write, bash,
   network).
2. **The denial error is actionable**: it names the exact pattern to add
   ("denied `bash:npm install`; add `allow = [\"bash:npm *\"]`"). The
   friction is paid once and with the solution in hand.
3. **Auto mode exists but is explicit and loud** (a flag like
   `--auto-permissions`, for sandboxes and disposable containers): the
   risk is chosen, not inherited.

Reason for the default: headless (CI, scripts) is exactly the unsupervised
context and the most exposed to prompt injection; a declared allowlist
also documents what the script can do, auditable at a glance.

**Denial travels as data (G40).** The actionable prose is *presentation*,
not the carrier (consistent with the structured errors of
[api.md](api.md) §1.4): every denial produces, exactly once, a structured
object

```
{ id, tool, args?,
  source = "deny" | "hook" | "default" | "headless" | "user",
  pattern?,      -- the deny-list pattern that bit (source = "deny")
  suggested? }   -- the exact allow that would fix the denial
```

(`source = "user"` is the rejection in the interactive dialog: "every
denial" includes the human one) with two destinations for two different
consumers: it's emitted as
`agent:permission.denied` (**live** observers — drivers, telemetry,
UIs — with G3's attribution), and it also goes into the denied
`tool_result`'s `meta`, under the `denied` key ([providers.md](providers.md)
§2.2), which [sesiones.md](sesiones.md) §3 persists intact — the denial
**travels with the transcript**, and a controller reading the session
after the fact (even on another machine) extracts it without parsing
prose. It's the piece for the asynchronous escalation loop validated in
pseudocode round 8 (scenario 36): denial → policy amendment by a human →
re-run. Amortizer 2's actionable text doesn't change: it's still what the
model sees and the human reads. And what scenario 36 found ambiguous is
now specified: **`tool.end` is also emitted for denied calls** (every
`tool.start` has its `tool.end`), with `is_error = true` — it's the
*generic* failure channel; `permission.denied` is the *specific*
permissions one.

Ask concurrency (G3): several sessions can have pending asks at once; each
one waits on its `future` **without timeout** (a timeout→deny would
introduce surprise, non-deterministic denials). The UI is responsible for
making pending ones visible.

This is the *soft* layer (facing the model). The *hard* layer for
untrusted code is workers with `caps` ([api.md](api.md) §13): a subagent in
a worker without `proc` doesn't run processes, no matter who says
otherwise.

## 6. Skills

> ✅ **Implemented** ([pospuesto.md](../postponed/pospuesto.md) **P24**). Assembly
> discovers skills, injects their index and exposes `agent.skills.list(cwd)`;
> the full content is loaded by the internal `skill` tool on demand. The
> repo's content goes through the TOFU gate (§11.2, `agent.trust`).

Compatible with the existing ecosystem's format: a directory with
`SKILL.md` (YAML frontmatter: `name`, `description` — via `enu.yaml`).

- Discovery: `config.dir()/skills/` (user) + `<repo>/.nu/skills/`
  (project). `agent.skills.list() -> SkillInfo[]`. The repo's content is
  subject to §11's trust model.
- Two-phase injection (context economy): the system prompt carries only
  the **index** (name + description); the full content is loaded on
  demand via the internal `skill` tool that the model invokes.
- Per session/subagent: `opts.skills = { "review", "deploy" }` limits the
  visible index.

## 7. System prompt

Assembled from ordered pieces: extension base → skills index → project
context file (`enu.md` at the repo root, if it exists) → `opts.system`. The
`request.pre` hooks can touch up the result. Every piece is replaceable
via configuration — there's no inaccessible magic prompt.

> ✅ **Implemented** ([pospuesto.md](../postponed/pospuesto.md) **P24**). The assembly is
> `base → skills index → enu.md (after TOFU) → opts.system`. Discovery is
> captured when the session opens; whether the repo's content is included
> is decided by trust at each assembly.

## 8. Compaction

> ✅ **Implemented** ([pospuesto.md](../postponed/pospuesto.md) **P25**). Compaction fires
> when the threshold is exceeded (default 80% of `context`) at the **turn
> boundary** (not between iterations, so as not to break
> tool_call↔tool_result pairing), and emits `agent:compact`.
> `Session:compact()` is the manual route; the `compact` hook customizes
> or prevents the summary.

- Automatic trigger: when `usage.input_tokens` exceeds the configurable
  threshold (default: 80% of the model's `context`, data from
  providers.toml). Source of truth: the provider's `usage`, never a local
  count (decision closed in providers.md §5).
- Default strategy: LLM summary of the old prefix (configurable model, by
  default the session's) → `compact` entry in the transcript
  (sesiones.md §3) → the replay for the model starts from the summary.
- Fully customizable with the `compact` hook: it receives the conversation
  and returns the summary message (or deny to prevent compaction).
- `providers.approx_tokens()` available for prior estimates ("does this
  file fit?") before having `usage` ([providers.md](providers.md) §4,
  G23).

## 9. Subagents

```
Session:spawn(opts) -> Sub
  opts: agent.session's + { worker? = false, caps?: string[] }

Sub:run(prompt) ⏸ -> Digest    -- the subagent's full turn(s)
  Digest = { text, message, stop_reason, usage, turns }   -- digested summary, not the stream
Sub:cancel()
```

- Own transcript as a child session (`meta.parent`, sesiones.md §7).
- By default runs as a task in the main state (shares the registered
  tools; cheap).
- `worker = true`: the **loop** runs in a worker (real parallelism, `caps`
  trimmable), but the **tool handlers execute in the main state via a
  message proxy** — args and results are JSON-able by contract, so they
  cross the boundary without friction. Implications:
  - A single tool registry: none is duplicated "in a worker version".
  - Security stays centralized: permissions, `tool.pre/post` hooks and
    dialogs run in the main state; the worker can't bypass the pipeline
    because execution never happens on its side. Two fences for two
    risks: inherited *permissions* limit which tools the subagent uses;
    *caps* limit what its Lua code does directly.
  - Proxy latency is irrelevant (microseconds vs. the LLM's seconds).
  - Honest limit: subagent streams run in real parallel (that's where the
    gain is), but their tool calls interleave as tasks in the main state.
    For IO tools it doesn't matter (they suspend and overlap); a tool
    with heavy Lua CPU would get in the main state's way — the watchdog
    will flag it, and its proper place is the Go primitives or a worker.
- **Known limitation (G16)**: nothing coordinates two parallel subagents
  writing the same file — last-write-wins. Deliberate: a lock in the
  official tools would be false security (bash and third-party tools
  write without going through it). The working remedy is **splitting
  territory** among subagents via prompt, as reference harnesses do.
- Permissions: the subagent inherits the parent's, **trimmed** by its
  `opts.permissions` (never expanded); `caps` applies the hard version.
  To avoid writing lists of functions by hand, the extension offers named
  packages as **plain, inspectable Lua tables**
  (`agent.caps.FS_RO = { "fs.read", "fs.stat", ... }`): the vocabulary
  lives here (iterable, replaceable), the mechanism in the core (G6).

## 10. Configuration

`config.dir()/agent.toml`: default model, `max_turns`,
`max_retries`/`retry_base_ms` (stream-opening retries, G42), compaction
threshold and model, **default reasoning** (`[thinking]` with `mode` and
`budget`, ADR-016), session retention policy ([P10](../postponed/pospuesto.md)), global
permissions. Precedence is the standard one: defaults < global <
project (`<repo>/.nu/agent.toml`) < session (`opts`) — with §11's
security exception: project permissions only trim.

The extension coins its own structured error code, **`EAGENT`** (shaped
per api.md §1.4, as providers.md §3 coins `EPROVIDER`): the engine's own
errors —a malformed `agent.toml`, `max_turns` exhausted without the model
finishing, a subagent whose channel dies— are thrown as `{ code =
"EAGENT", message, detail? }`, catchable with `pcall` (G48). API *usage*
errors remain `EINVAL`, and provider ones, `EPROVIDER`.

The `model` field (`"provider/model"`) is **mandatory** to open a session:
`agent.session` fails with an actionable `EINVAL` if it's not in `opts`
nor in `agent.toml`. That's why the `nu --default-config` onramp leaves an
**active** `agent.toml` template with a default `model` (`anthropic/opus`)
and its matching `providers.toml` ([ADR-017](../decisions/adr/README.md), [G35](../findings/README.md)):
the first startup already comes with a model configured (only the API key
needs exporting from the environment). Templates are written only if the
files don't already exist; they never overwrite user config.

## 11. Trust model for repo content (G14)

The repo is not the user: its config was written by a third party. Two
rules, without sandbox or constant dialogs:

1. **The repo only trims permissions, never expands them.** The `deny`
   entries from `<repo>/.nu/agent.toml` are always honored; its `allow`
   and its `mode` are **ignored** — if the user wants them, they copy
   them to their global config or grant them in-session. Zero friction,
   closes the "cloning and opening executes the repo's will" vector.
2. **One-keystroke TOFU for content that reaches the model.** The first
   time enu opens in a repo with `.enu/skills/` or `enu.md`, a single
   question ("this repo brings skills/context, use them? — remembered
   per repo", persisted in `data_dir`). Without an affirmative answer
   (including headless), that content isn't injected. It's the same
   `:trust` / `vim.secure.read()` pattern Neovim uses against the
   classic `exrc` attack.

MCP servers' tool descriptions aren't covered here: installing an MCP
server is a conscious act by the user — their responsibility, like
installing a plugin.

<!-- enu:interno -->

## 12. Relationship to what's postponed

Parallel tool calls ([P12](../postponed/pospuesto.md)), nested workers for subagents
([P11](../postponed/pospuesto.md)) and session retention ([P10](../postponed/pospuesto.md)) have
entries in the postponed register with their trigger.

<!-- /enu:interno -->
