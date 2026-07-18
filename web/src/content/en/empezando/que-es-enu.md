---
title: What is enu
description: enu's idea in one page — a minimal Lua kernel over the terminal where everything, including the agent, is an extension.
---

`enu` is **a terminal-oriented Lua runtime whose killer app is a coding
harness**: a single Go binary with a minimal kernel where everything else —
including the agent itself— are Lua extensions.

Put another way: `enu` is a CLI/TUI coding harness, but that phrase describes
the *product*. The *project* is a **tiny kernel and an extension system**
where the agent loop, the chat UI, MCP support, and LLM providers have no
architectural privilege whatsoever: they're Lua, just like whatever you write.

## The ideas you must never lose sight of

1. **The core doesn't know what an agent is.** The model isn't Neovim (large
   core + hooks), it's Emacs/Textadept: a kernel that only provides
   primitives (runtime, IO, network, terminal UI) and a Lua interpreter. The
   agent, the chat, slash commands: extensions.
2. **Zero dependency hell.** A single static binary (`CGO_ENABLED=0`). No
   Node, no npm, no runtime to install. `curl | sh` and you're working.
3. **"Lua decides, Go executes."** All heavy, universal work (repo search,
   diff, markdown, highlighting, HTTP streaming) is a Go primitive, parallel
   on the inside. If an extension burns CPU in Lua, a primitive is missing or
   the work should go to a worker.
4. **The core API is sacred.** Small, boring, **grows only by addition**.
   Breaking a signature breaks the world.
5. **Batteries included, but not plugged in.** The binary ships with the
   official extensions embedded, but none activates on its own: a freshly
   installed `enu` is a bare runtime, and the harness is a choice made by the
   user.

## What enu is not

- **It's not an editor.** It doesn't compete with Neovim: there are no giant
  buffers to keep highlighted on every keystroke.
- **It's not a server-side agent framework.** It's an interactive terminal
  tool for people.
- **It's not a multi-language plugin project.** Embedded Lua is the
  extension layer; external processes (MCP and the like) are the escape
  hatch for everything else.

## How this manual is organized

- **Getting started** (this section): installation, your first script, your
  first agent, and the concepts you need so you don't fight the execution
  model.
- **API reference**: one page per `enu.*` namespace, with the signature, the
  semantics, and runnable examples for each function.

:::tip
If you're here to read code right away, jump to [Your first
script](/enu/en/docs/primer-script/). If you want to understand *why* `enu`
is the way it is, continue with [Key concepts](/enu/en/docs/conceptos/).
:::
