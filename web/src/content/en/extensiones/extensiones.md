---
title: The official extensions
description: Index of the extensions embedded in the binary — the harness (providers, sessions, agent, chat), the supporting pieces (mcp, toolkit, repl), and the guide for writing your own.
---

`enu` is a bare runtime: a tiny kernel of primitives plus a Lua interpreter.
Everything else — the agent, the chat, the LLM providers, the bridge with
MCP — are **Lua extensions**, and the official ones are no exception: they
come embedded in the binary but **inactive by default** and **without
kernel privilege**, so a third-party alternative can replace any of them.
They're activated by name in `plugins.enabled` in `enu.toml`; dependencies
resolve themselves in topological order (activating `chat` pulls in
`agent`, `providers`, `sessions`, and `toolkit`).

## The harness

The product set — what `enu --default-config` activates — is a complete
coding harness, split across four contracts:

- **[providers](../providers.md)** — the model registry (TOML) and the LLM
  adapters (`anthropic`, `openai-compat`, and `gemini`, embedded). Models are
  declared as data, not as code.
- **[sessions](../sesiones.md)** — conversation persistence: append-only
  JSONL under `data_dir()/sessions/`.
- **[agent](../agente.md)** — the agent's headless engine: the turn, the
  tools, permissions, hooks, subagents, and context compaction.
- **[chat](../chat.md)** — the terminal UI: transcript, input editor, slash
  commands, and statusline. TTY only.

## The supporting pieces

Three smaller extensions the harness uses internally — and that also work
standalone:

- **[mcp](mcp.md)** — integrates MCP (Model Context Protocol) servers as
  agent tools: each remote tool registers just like a native one. Pure Lua
  over `enu.proc` and `enu.json`.
- **[toolkit](toolkit.md)** — the widget toolkit over `enu.ui` and `enu.text`:
  layout containers, focus, composition across plugins, and themes. It's
  what chat uses to paint.
- **[repl](repl.md)** — an interactive Lua interpreter over the public API,
  activatable standalone: the starting point for an author who doesn't want
  the harness.

## Writing your own

- **[Plugin authoring guide](../guia-plugins.md)** — the practical wisdom
  for building your own extension on top of the core API and the contracts
  of the official ones, with its checklist.

## Experimental

- **mesh** (the agent mesh) — Role+Job specs, claim by git CAS, job runner,
  and fork tournament. It's outside the product set and its contract is
  still in draft, so it **has no public documentation yet**; its design
  lives in [malla.md](../contracts/malla.md).

---

The `example` extension is embedded only as **reference scaffolding** for
plugin authors and loader tests: it isn't product, and it doesn't belong to
any set activatable by default.
