# enu

[![CI](https://github.com/dbareagimeno/enu/actions/workflows/ci.yml/badge.svg)](https://github.com/dbareagimeno/enu/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

**English** · [Español](README.es.md)

> **A self-extensible coding harness shipped as a single static binary.**
> No Node. No npm. No Python.

enu ships a coding agent, a terminal UI, model providers, sessions and MCP
support — but none of them live in the core. They are **Lua plugins built on
the same public API available to you**. Replace a tool. Redesign the TUI.
Rewrite the agent loop. Or drop the agent entirely and use enu as a native
runtime for your own terminal automation.

<!-- TODO(S47): demo real aquí (GIF/asciinema de 30-45 s): chat → la tool pide
     permiso → el agente edita un archivo → un plugin Lua → `enu -p` headless. -->

[Install](#quickstart) · [Write a plugin](#show-dont-tell) · [How it works](#how-it-works) · [Docs](docs/README.md)

---

## Quickstart

```sh
# 1. One static binary — detects OS/arch and verifies the checksum.
curl -fsSL https://raw.githubusercontent.com/dbareagimeno/enu/main/install.sh | sh

# 2. Enable the official set (agent, chat, providers, sessions, MCP…).
enu --default-config

# 3. Point it at a model and go — interactive chat, or headless with `-p`.
export ANTHROPIC_API_KEY=sk-...
enu                              # interactive TUI
enu -p 'Summarize this repo'     # one headless turn to stdout
```

A freshly installed enu is a **bare runtime**: the official extensions are
embedded but **off by default**, so turning them on is an explicit, reversible
choice. Full onramp and configuration in the [getting-started guide](docs/README.md).

---

## Why enu

### Deploy it anywhere

Download one binary and run it on your laptop, in a container, or in CI. No
host-language runtime, no package manager, no plugin toolchain to provision
first. The same binary runs on a clean Debian box or an air-gapped machine.

Prefer a container? Pull the published multi-arch image — also the supported way
to run enu on hosts without a native binary, such as **Intel Macs** (they run the
`linux/amd64` image in Docker Desktop's VM; there is no native `darwin/amd64`
build):

```sh
# Runs the bare runtime — no config or API key needed:
docker run --rm ghcr.io/dbareagimeno/enu -e 'return enu.version.api'
```

The [`docker/`](docker/) directory has a Compose + Makefile workflow to build,
test, and run enu without installing Go locally.

### Rewrite everything

The official agent has **no private API**. Remap the chat, add tools and slash
commands, hook the lifecycle, or replace the agent loop wholesale — all through
the public `enu.*` surface. If an official feature can't be built as an ordinary
plugin, that's a bug in the API, not a reason for a shortcut.

### Automate without a UI

The agent engine draws nothing by design. `enu -p '…'` runs a turn and writes
the result to stdout, with stable exit codes — built for scripts, pipes and CI,
not a TUI bolted onto a headless environment.

---

## Show, don't tell

A plugin is a directory with a `plugin.toml` and an `init.lua`. Here is a
complete one that adds a `/review` command:

```lua
-- ~/.config/enu/plugins/review/init.lua
local chat = require("chat")

chat.command{
  name = "review",
  description = "Review the current git diff",
  run = function()
    chat.prompt("Review the current git diff. Focus on correctness.")
  end,
}
```

Save it, reload, done — no SDK, no compiler, no package manager.

**This is not a special extension API.** The official chat registers its own
commands through the exact same `chat.command{}` surface. Everything an official
plugin can do, yours can too.

---

## How it works

```
┌─────────────────────────────────────────────┐
│ Lua userland                                │
│   agent · chat · providers · sessions · mcp │
│   your plugins                              │
├─────────────────────────────────────────────┤
│ public  enu.*  API                          │
├─────────────────────────────────────────────┤
│ static Go kernel                            │
│   fs · proc · http · search · ui · workers  │
└─────────────────────────────────────────────┘
        Official plugins use no private APIs.
```

Three ideas hold it together (full rationale in
[docs/core/filosofia.md](docs/core/filosofia.md)):

1. **The core doesn't know what an agent is.** Emacs/Textadept, not Neovim: a
   tiny kernel of primitives plus a Lua interpreter. Agent, MCP, chat, slash
   commands and providers are all Lua extensions — the official ones included,
   with no architectural privilege.
2. **Completeness corollary.** If an official feature can't be built on the
   public API, the API is incomplete — the fix goes into the API, never into a
   privileged shortcut. This is what keeps the surface honest.
3. **Lua decides, Go executes.** The heavy lifting (search, diff, markdown,
   highlighting, HTTP streaming) is a Go primitive, parallel underneath; Lua
   only orchestrates.

The official product set — `providers`, `sessions`, `agent`, `mcp`, `chat`,
`toolkit`, `repl` — are all plugins. A third-party alternative can replace any
of them.

---

## enu vs Pi

[Pi](https://github.com/earendil-works/pi) is the closest thing to enu: a
hackable, extensible coding harness. It is more mature than enu in every way
that comes from having users — a real plugin ecosystem, a polished UX, a stable
SDK. The honest difference is **how each one ships**:

| | **enu** | Pi |
|---|---|---|
| Distribution | Single static Go binary | Node / npm |
| Runtime required | None | Node |
| Extension language | Lua | TypeScript |
| Toolchain for a basic plugin | None | Node ecosystem |
| Replaceable agent loop | Yes | Yes |
| Public API as an architectural boundary | Explicit principle (completeness corollary) | Extensible architecture |
| Plugin ecosystem | Early | Mature |
| Headless / RPC | Headless today; RPC on the roadmap | Headless + RPC, mature |
| Maturity | Pre-1.0 | Production, broad community |

Pi is more mature. enu is easier to deploy as infrastructure — one binary, no
host runtime, plugins that are just Lua files.

---

## Project status

enu is **pre-1.0**. The kernel is built and the official extensions run on top
of it, but the API is still experimental and may change (breaking changes are
made deliberately, via a recorded decision, never by accident). Supported on
Linux and macOS (native), and on Windows through WSL2.

Signals you can check rather than take on faith:

- Official plugins run **exclusively** through the public API.
- End-to-end tests exercise the **real compiled binary** (agent loop, MCP,
  sessions), plus interactive TUI tests over a PTY.
- The race detector runs in CI, and releases ship signed checksums.

This is a great time to build a plugin, wire enu into CI, or point it at a local
model — and to tell us where the API or the design creaks.

---

## Documentation

The full map lives in [docs/README.md](docs/README.md). Pick your path:

- **I want to use enu** → [Getting started](docs/README.md) · [CLI & configuration](docs/contracts/agente.md)
- **I want to build a plugin** → [Plugin guide](docs/contracts/guia-plugins.md) · [Core API](docs/contracts/api.md)
- **I want to understand the design** → [Philosophy](docs/core/filosofia.md) · [Architecture](docs/core/arquitectura.md) · [Decisions (ADR)](docs/decisions/adr/README.md)
- **I want to contribute** → [CONTRIBUTING.md](CONTRIBUTING.md)

The internal design source (contracts, ADRs, findings) is written in Spanish;
the public-facing docs are in English.

---

## Contributing

Contributions are welcome — read [CONTRIBUTING.md](CONTRIBUTING.md) first.
Because the project is design-led, the best first step is
[docs/core/filosofia.md](docs/core/filosofia.md) and the
[ADR index](docs/decisions/adr/README.md): understand the *why* before proposing
the *what*.

The author retains ownership of the project, so incorporating third-party code
may require a contribution agreement (CLA).

## License

enu is free software under the [Apache License 2.0](LICENSE) (permissive, with a
patent grant). Use it, study it, modify it and distribute it, including
commercially. Copyright Diego Barea; see [NOTICE](NOTICE).
