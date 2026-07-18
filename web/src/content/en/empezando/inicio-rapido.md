---
title: Quick start
description: From zero to your first agent turn in three steps — activate the official set, declare a model, and launch a headless turn or the chat.
---

Freshly installed, `enu` is a **bare runtime**: the official extensions ship
embedded but **inactive by default** ([ADR-010](decisions/adr/adr-010-extensiones-oficiales-distribuidas-con-nu.md)) — the harness is a
choice you make, not a foregone conclusion. From zero to your first agent
turn in three steps:

```sh
# 1. Activate the official product set (agent, chat, providers, sessions,
#    mcp, toolkit, repl). Writes `plugins.enabled` to ~/.config/enu/enu.toml.
enu --default-config

# 2. Declare a model and export its key (see "Models and keys" below).
cat > ~/.config/enu/providers.toml <<'TOML'
[providers.anthropic]
adapter     = "anthropic"
base_url    = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"

[[providers.anthropic.models]]
id      = "claude-opus-4-8"
context = 200000
aliases = ["opus"]
TOML
export ANTHROPIC_API_KEY="sk-..."

# 3. Launch a headless turn...
enu -p 'Summarize what this repository does' --model anthropic/opus

#    ...or open the interactive chat (in a terminal with a TTY):
enu
```

Without step 1, `enu` starts up at the **bare runtime screen** (with a TTY) or
fails with an actionable error naming the exact line of `enu.toml` that's
missing (without a TTY). Nothing happens by magic: every step is explicit
and reversible.
