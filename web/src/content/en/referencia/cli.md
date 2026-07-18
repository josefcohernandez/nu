---
title: The CLI
description: The enu binary's flags, headless modes and exit codes.
---

This page documents the **command-line surface** of the `enu` binary.
It is not sacred `enu.*` API (that's the Lua surface): it's the interface of the
executable. It lives in the binary because the core doesn't know what an agent is —the CLI
orchestrates the extensions through the public API, just like an `init.lua` would—.

## Modes

```
enu                       Canonical boot. With a TTY and no active plugins,
                         paints the bare runtime screen (G21).
enu --default-config      Activates the official product set without a TTY: writes
                         plugins.enabled to enu.toml and exits (with -p/-e, it activates
                         it only for that process, without touching disk).
enu -e '<lua>'            Evaluates a headless Lua chunk and prints its returns.
enu -p '<prompt>'         Runs a headless agent turn and prints the assistant's
                         final text to stdout.
```

### `enu` (no arguments)

Normal boot. With an interactive TTY and **no active plugins**, it paints the
**bare runtime screen**: a fixed render with the version and API level,
the config and plugin paths, the catalog of embedded extensions and the
actions (activate the official set / individual extensions / exit). Without a TTY, there's no
screen: it prints usage. The no-TTY equivalent is `enu --default-config`.

### `enu --default-config`

The **no-TTY onramp** for having enu *batteries-included* in CI, Docker or scripts,
where the bare runtime screen doesn't exist. Activates the **official product
set** —the seven embedded extensions (`providers`, `sessions`, `agent`,
`mcp`, `chat`, `repl`, `toolkit`), all but the testing scaffolding
`example`—. It has **two modes** depending on how you combine it:

- **Alone** (`enu --default-config`): **writes** `plugins.enabled` to
  `config.dir()/enu.toml` and exits. Preserves the rest of the file (other keys,
  `[watchdog]`, …), is **atomic** (doesn't leave a half-written `enu.toml`) and
  **idempotent** (repeating it changes nothing). If the existing `enu.toml` is
  malformed, it **does not overwrite it**: exits with an actionable error.
- **Combined with a headless action** (`--default-config -p '…'` or
  `--default-config -e '…'`): **doesn't touch disk**. Activates the set only for that
  process and runs the action. This is the immutable-container case: running with everything
  active without rewriting config on every boot.

```sh
# Set up the machine once and for all (persistent):
enu --default-config
enu -p 'resume este repo'        # agent already active

# Docker / immutable CI (ephemeral, no FS touched):
enu --default-config -p 'resume este repo'
```

No network in either mode: the extensions ship inside the binary itself. It's CLI
surface, not sacred `enu.*` API (it adds nothing to the API nor bumps `enu.version.api`).

### `enu -e '<lua>'`

Evaluates the Lua chunk **without a TTY** (headless) and prints each return value on its
own line. The chunk runs on the **main state** (it is not a task): it can
`enu.task.spawn` but not use ⏸ functions directly. See [Your first
script](/enu/en/docs/primer-script/).

```sh
enu -e 'return enu.version.api'
```

```
2
```

### `enu -p '<prompt>'`

Runs a **headless agent turn** with the given prompt and prints the assistant's
final text. Runs as a task (the turn's ⏸ functions and its tools
work without a TTY). Requires the `providers`, `sessions` and `agent` extensions
active. See [Your first agent](/enu/en/docs/primer-agente/).

#### `-p` modifiers

| Flag | Effect |
|---|---|
| `--continue` / `-c` | Resumes the project's (cwd) **latest** session before sending the prompt. |
| `--auto-permissions` | Agent permissions in `"auto"` mode: grants sensitive tools (without it they're denied in headless). The risk is chosen, not inherited. |
| `--model 'prov/modelo'` | Selects the turn's model/provider (overrides the one in `agent.toml`). |

```sh
enu -p 'añade tests al módulo nuevo' --continue --auto-permissions --model anthropic/opus
```

## Exit codes

Headless modes exit with a consistent code for CI and scripts:

| Code | Meaning |
|---|---|
| **0** | Success. |
| **1** | Execution error: the `-e` chunk, the agent turn or the provider threw, or boot failed (invalid plugin graph, broken `enu.toml`). |
| **2** | Usage error: incompatible flags or a required argument missing. |
| **3** | Permission denied in headless: a sensitive tool was denied for lack of `--auto-permissions`. **Different** code from 1 so a script can distinguish "the model couldn't act due to permissions" from an execution failure. |

`enu --default-config` (persistent mode) exits with **0** after writing, or with **1**
if it couldn't write `enu.toml` (e.g. the existing file is malformed and isn't
overwritten, or an I/O error): the stderr message is actionable.

```sh
# Distinguish a permissions deny from a real failure.
enu -p 'borra los temporales'
case $? in
  0) echo "hecho" ;;
  3) echo "necesita --auto-permissions" ;;
  *) echo "error" ;;
esac
```

:::note[Windows]
`enu` is used on Windows via **WSL2** with the `linux/amd64` binary. Native
Windows support is postponed.
:::
