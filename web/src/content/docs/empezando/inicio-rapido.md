---
title: Inicio rápido
description: De cero a tu primer turno de agente en tres pasos — activa el conjunto oficial, declara un modelo y lanza un turno headless o el chat.
---

Recién instalado, `enu` es un **runtime desnudo**: las extensiones oficiales
vienen embebidas pero **inactivas por defecto** ([ADR-010](decisions/adr/adr-010-extensiones-oficiales-distribuidas-con-nu.md)) — el
harness es una elección tuya, no un hecho consumado. De cero a tu primer turno
de agente en tres pasos:

```sh
# 1. Activa el conjunto oficial de producto (agent, chat, providers, sessions,
#    mcp, toolkit, repl). Escribe `plugins.enabled` en ~/.config/enu/enu.toml.
enu --default-config

# 2. Declara un modelo y exporta su clave (ver «Modelos y claves» más abajo).
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

# 3. Lanza un turno headless...
enu -p 'Resume qué hace este repositorio' --model anthropic/opus

#    ...o abre el chat interactivo (en un terminal con TTY):
enu
```

Sin el paso 1, `enu` arranca en la **pantalla de runtime desnudo** (con TTY) o
falla con un error accionable que nombra la línea exacta de `enu.toml` que falta
(sin TTY). Nada ocurre por arte de magia: cada paso es explícito y reversible.
