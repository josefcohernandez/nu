# nu Philosophy

> *A terminal-oriented Lua runtime whose killer app is a coding harness.*

`enu` is a CLI/TUI coding harness. But that sentence describes the product, not
the project. The project is a **minimal kernel and an extension system where
everything else — including the agent itself — is an extension**.

## Principles

### 1. Zero dependency hell

A single static binary. No Node, no npm, no toolchain, no runtime to
install. `curl | sh` and get to work. Installation and updates are trivial
operations on any platform.

This is a direct reaction to the state of current harnesses (pi.dev,
Claude Code, etc.), built on the JS/TS ecosystem. We don't criticize their
ideas — pi is a direct inspiration — but their material foundation.

### 2. The core doesn't know what an agent is

The model is not Neovim (large core + hooks), it's **Emacs/Textadept**: a
tiny kernel that only provides primitives (runtime, IO, network, terminal UI)
and a Lua interpreter. The agent loop, MCP support, slash commands, the chat
UI: **everything is a Lua extension**, official ones included.

The general formulation of the principle: **the kernel only knows its own
capabilities** — primitives, loader, its embedded extensions. The agent is
just the most visible example of what isn't its business. The bar for any
doubtful case is this: if something can be fully described with the
kernel's vocabulary (plugins, paths, versions), it belongs to the kernel; if
it needs product vocabulary (agent, chat, tools), it belongs to an
extension.

Corollary: if an official feature can't be built with the public extension
API, the API is incomplete. Structural dogfooding, the way pi does with its
own features.

### 3. Lua can do ANYTHING

There's no "privileged zone" reserved for the core beyond the primitives. An
extension can redefine the entire UI, replace the agent loop, intercept any
event. The user who wants a harness different from the official one doesn't
fork: they write Lua.

The only deliberate exception is **LLM providers**, which are declared as
data (TOML), not as code — see ADR-005.

### 4. Lua decides, Go executes

Lua is the orchestrator, never the workhorse. All universally heavy work
(repo search, diff, parsing, highlighting, markdown rendering, HTTP
streaming) is a Go primitive, parallel on the inside. If an extension is
burning CPU in Lua, that's not a threading problem: it's the signal that
that operation should be a primitive.

### 5. Batteries included, but not plugged in (ADR-010)

The binary ships the official extensions embedded (`go:embed`), but **none
activates on its own**: an installed `enu` is a bare runtime, and the harness
is a user's choice, not a fait accompli. Plugging them in is trivial but
**explicit**: with a TTY, the first launch offers to activate the official
set with one keystroke; without a TTY (CI, Docker, scripts), the
`nu --default-config` flag does the same in one command (ADR-015). In both
cases, with no network — everything comes from the binary — and from there
the agent works. Same mental model as Neovim: the program doesn't ship with
plugins enabled. And as always: those extensions have no privilege
whatsoever — they get read, replaced, turned off.

### 6. The core API is sacred

If everything is built on the primitives, breaking them breaks the world.
The v1 API must be deliberately small and boring, and grow only by
addition.

## Inspirations

| Project | What we take |
|---|---|
| **pi.dev** | The concept of a minimal, extensible harness; its features as extensions of its own API |
| **Neovim** | Lua as a culturally proven extension language; the ecosystem of plugins that build on one another |
| **Emacs / Textadept** | Minimal kernel + the entire program written in the extension language |
| **The browser / Luau** | The concurrency model: deterministic main thread + explicit workers + parallel native primitives |

## What nu is not

- It's not an editor. We don't compete with Neovim: there are no giant
  buffers to keep highlighted on every keystroke.
- It's not an agent framework for production/server use. It's an
  interactive terminal tool for people.
- It's not a "multi-language plugin support" project. Embedded Lua is the
  extension layer; external processes (MCP and similar) are the escape
  hatch for everything else.
