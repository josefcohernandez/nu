# The official chat extension: contract

Status: **draft for discussion**. Contract for the official `chat`
extension — the visible face of enu, what the user sees on startup. Like the
rest of the official extensions, **no privileges**: it consumes the public
agent API ([agente.md](agente.md)), the widget toolkit (official extension
on top of [api.md](api.md) §9) and the event bus. An alternative third-party
UI can do everything this one does.

## 1. Anatomy

```
┌──────────────────────────────────────────────┐
│ transcript (scroll)                          │
│   messages · tool blocks · thinking          │
│                                              │
├──────────────────────────────────────────────┤
│ input (multiline, history, completion)       │
├──────────────────────────────────────────────┤
│ statusline: model · context % · cost ·       │
│             cwd · permission mode            │
└──────────────────────────────────────────────┘
+ modal layers: permission dialog, pickers
```

One column, one visible session. Splits and multi-session view: postponed
([P14](../postponed/pospuesto.md)).

> ✅ **Product polish** ([ADR-018](../decisions/adr/README.md)). The column looks finished, not
> like a bare kernel: a **welcome screen** on startup (banner + model + cwd +
> shortcuts) instead of a blank screen; a **framed input** (`toolkit.box`, rounded
> border) with a `› ` prompt and visible placeholder; an **activity** row with an
> animated spinner while the turn runs; a **statusline as a bar** (background +
> colored segments); tool **cards** with their arguments; **framed and centered
> modals**. All of it built on the toolkit (its `box`/`spinner`/`richtext`
> widgets and the `Theme:markdown_opts` that colors the transcript, G22) — with
> no kernel privilege.

Multi-session (G3): `chat` renders only events whose `session` is the
active session; the activity of other sessions (subagents, background
sessions) is reflected as a discreet indicator in the statusline — including
a count of pending permissions, because an unanswered ask blocks its
session indefinitely.

## 2. Turn rendering (consuming `agent:*`)

| Event | Render |
|---|---|
| `agent:delta` | Streaming text into the message block in progress (markdown via `enu.text.markdown`, which is streaming-safe). |
| `agent:delta` (thinking) | Reasoning block **collapsed by default**, expandable; dimmed style. |
| `agent:tool.start/progress/end` | **Collapsible** tool block: header with name + summarized args; `progress` live; on completion, folded result if it's long. |
| `agent:message` | Seals the message (replaces the deltas with the final render). |
| `agent:error` | Error block with the structured code (`[code] message`) and, if `retryable`, a retry action: the `(/retry to retry)` hint over the `/retry` builtin → `Session:retry()` (G43). |
| `agent:retry` | Dimmed note "retrying (n/m) in Xs…" while the engine waits out the backoff (G42). |
| `agent:permission.asked` | Modal dialog (§5), FIFO-queued if one is already visible. |
| `agent:compact` | Visual mark of "history compacted above". |

> ✅ **Implemented** ([pospuesto.md](../postponed/pospuesto.md) **P27**). Chat also consumes
> `agent:tool.progress` (live progress under the tool in progress) and
> `agent:compact` (the "history compacted above" mark, already emitted by the
> agent with **P25**). With G42/G43 it also consumes `agent:retry` (the
> backoff note) and the full `agent:error` payload (`[code] message` + the
> `/retry` hint).

**Pluggable renderers**: a plugin can register the render for its tool's
result — `chat.renderer(tool_name, fn(result, width) -> Block)`. This way the
diff tool renders diffs with colors and the tests tool renders its table,
without `chat` knowing about them. Fallback: folded plain text.

## 3. Input

- **Framed** multiline editor ([ADR-018](../decisions/adr/README.md)): lives in a `toolkit.box`
  (rounded border, focus highlight) with a `› ` prompt; the box **grows and
  shrinks** with content (up to a maximum). The **placeholder** (usage hints)
  is visible even when the editor has focus (previously it hid right at
  startup). `enter` sends, `shift+enter` (or `alt+enter` depending on the
  terminal, via `enu.ui.caps`) inserts a line. History with `↑/↓` at the edge
  of the editor. `esc` cancels the turn in progress (`Session:cancel()`);
  while it runs, an **activity** row with a spinner signals it ("Thinking…/
  Running <tool>… · esc to interrupt").
- **`@` mentions**: opens a fuzzy picker of repo files
  (`enu.search.files` + `enu.search.fuzzy`); the mention injects the path and
  the agent decides whether to read it (content is not blindly embedded).
  *(✅ Implemented: [pospuesto.md](../postponed/pospuesto.md) **P26**, via `chat.picker`.)*
- **`/` at the start**: command autocompletion (§4) — `tab` opens the
  command picker. *(✅ Implemented: **P29**.)*
- Correct multiline paste (`paste` event from `enu.ui`).

## 4. Slash commands

First-class extension point:

```
chat.command{
  name, description,
  args?: string,                 -- usage help, e.g. "<model>"
  complete?: fn(prefix) -> string[],
  handler: fn(args, ctx) ⏸,
}
```

Builtins (registered with this same function — dogfooding):
`/model` (picker from `providers.list()`, applies `Session:set_model`),
`/sessions` (picker from the listing in [sesiones.md](sesiones.md) §7,
resumes via `agent.session{ resume = id }`), `/fork`, `/compact`,
`/permissions` (view and edit the session's policy), `/think` (view and
change reasoning, ADR-016), `/retry` (re-runs the turn after an error,
`Session:retry`, G43), `/help`, `/quit`.

> ✅ **Implemented** ([pospuesto.md](../postponed/pospuesto.md) **P28**). Besides
> `/model`, `/sessions`, `/compact`, `/clear`, `/help`, `/quit`, chat ships
> `/fork` (forks with `Session:fork` and continues on the branch via
> `Chat:switch_session`), `/permissions` (view and edit the policy:
> `allow|deny <pattern>`, `mode ask|auto`), `/think`
> (`off|adaptive|budget <N>`, via `Session:set_thinking`, ADR-016) and
> `/retry` (re-runs the failed turn via `Session:retry`, G43).

## 5. Permission dialog

On `agent:permission.asked`: a modal with the tool, the full args (without
truncating anything dangerous: the entire command, the entire path) and
options:

- **Allow once** → `agent.permission.respond(id, true)`. The second
  argument is a **boolean** (`true` grants, `false`/`nil` denies, G49):
  "once" and "always" grant equally; they differ only in whether the
  pattern is also persisted (below).
- **Always allow** → adds the pattern to the *session's* policy; with a
  modifier, persists to the **global user** config (`agent.toml`) — never to
  the project's `agent.toml`: its `allow` entries are ignored by the trust
  model ([agente.md](agente.md) §11). The proposed pattern is shown and is
  editable before accepting (generalizing `bash:npm install` to
  `bash:npm *` is a human decision, not the UI's).
  *(✅ Implemented: [pospuesto.md](../postponed/pospuesto.md) **P29**. Key `s` = always
  (session), `g` = always (global, persists to `agent.toml`). Inline editing
  of the pattern before accepting remains as minor polish; v1 uses the
  suggested pattern.)*
- **Deny** (with an optional note, which reaches the model as a rejection).

While the modal is open, the turn waits (that's how the agent pipeline is
designed); `esc` = deny. With several sessions asking at once: **FIFO queue,
one visible modal**, labeled with its originating session; the rest wait in
the queue (and are signaled in the statusline).

## 6. Statusline

It's rendered as a **bar** ([ADR-018](../decisions/adr/README.md)): a continuous background
(`bg_surface`) and, on top of it, the segments as **colored spans** from the
theme (G22) — not gray concatenated text. Each segment returns
`{ text, style }` (a semantic color name) or `""` to hide itself; chat
separates them with a dimmed `·` and right-aligns the right side. Default
segments: active model · context fill (% from `Session.usage`, which turns
`warn` colored near the compaction threshold) · accumulated session cost ·
reasoning (🧠, only if active; ADR-016) · cwd (**abbreviated**, `~`/last two
segments) · permission mode (`auto` highlighted). Extensible:

```
chat.statusline.add{ id, side: "left"|"right", priority, render: fn(ctx) -> Span[] }
```

## 7. Keymaps and theming

- Default shortcuts registered with `enu.ui.keymap`, all remappable by
  the user in their `init.lua` (the defaults table is public:
  `chat.keys`).
- **Semantic-only** colors from the toolkit theme (`accent`,
  `error`, `dim`...): `chat` doesn't hardcode a single color. Themes are
  toolkit plugins, not `chat`'s.

## 8. Startup and interaction with the rest

- `chat` only activates in an interactive TTY — the test is `enu.has("ui")`
  ([api.md](api.md) §9, G20); in headless it isn't even loaded (the
  engine/UI separation in [agente.md](agente.md) §1 is what allows it).
- **Welcome screen** ([ADR-018](../decisions/adr/README.md)). While the conversation is empty,
  the transcript shows a greeting (identifies the harness, the active
  **model** and **cwd**, and recalls the shortcuts) instead of a blank
  screen; on the first message the conversation replaces it. The quality of
  the degraded startup screen (below) stops being the exception.
- **Chat owns the screen and shutting it down powers off the binary**
  ([G36](../findings/g36-el-conjunto-oficial-de-producto.md)). The official set (ADR-015) also activates
  `repl`, but `repl` **yields**: it only auto-mounts its UI if chat isn't
  active (it checks this with `enu.plugin.list`, without depending on chat).
  And `Chat:quit` (and `ctrl+c`) emit `core:shutdown`: quitting chat
  **powers off the runtime** instead of leaving the user in a lower layer
  (the REPL/interpreter). This way `enu` with the official set opens **one**
  single TUI; with only `repl` active (G21), it opens the REPL.
- Creates the initial session (`agent.session`) with the resolved config
  (defaults < global < project), or resumes an existing one
  (`agent.session{ resume = id }`, fed by the `/sessions` picker).
- **Degraded startup ([ADR-017](../decisions/adr/README.md), [G35](../findings/README.md)).** If the
  initial session **can't be built due to missing or broken config** —
  `agent.session` throws `EINVAL` (no model), `EPROVIDER` (model/provider
  not resolvable in `providers.toml`) or `EAGENT`/`EPROVIDER` (malformed
  TOML) —, `chat.start` **doesn't die to the log**: it mounts a **minimal,
  actionable, exitable UI** that explains how to configure (`agent.toml`,
  `providers.toml`, the environment API key) and lets you exit (`esc`/`q`/
  `ctrl+c` → `core:shutdown`). An **unexpected** failure (not a config one)
  propagates as usual. The `enu --default-config` onramp leaves active
  templates that avoid this path on first startup (ADR-017); a missing
  **API key** doesn't reach here (`providers.resolve` doesn't fail without
  one): the error surfaces in-transcript as `agent:error` on the first turn.
- Doesn't touch `enu.fs` or `enu.proc` for agent logic: if `chat` needs
  something from the agent's domain that the public API doesn't provide,
  the agent's public API is incomplete — same rule as always.

## 9. Extension points (summary)

| Point | Function |
|---|---|
| Slash commands | `chat.command{}` |
| Tool result renderers | `chat.renderer(tool, fn)` |
| Statusline segments | `chat.statusline.add{}` |
| Shortcuts | `enu.ui.keymap` + `chat.keys` table |
| Appearance | toolkit themes (semantic) |

<!-- enu:interno -->

## 10. Postponed

Splits / multi-session view ([P14](../postponed/pospuesto.md)), search within the
transcript ([P15](../postponed/pospuesto.md)), vim mode for the input editor
([P16](../postponed/pospuesto.md)), image rendering in the transcript
([P6](../postponed/pospuesto.md)).

<!-- /enu:interno -->
