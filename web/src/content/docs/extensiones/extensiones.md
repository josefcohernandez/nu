---
title: Las extensiones oficiales
description: Índice de las extensiones que vienen embebidas en el binario — el harness (providers, sessions, agent, chat), las piezas de apoyo (mcp, toolkit, repl) y la guía para escribir las tuyas.
---

`nu` es un runtime desnudo: un kernel diminuto de primitivas más un intérprete
Lua. Todo lo demás —el agente, el chat, los providers de LLM, el puente con
MCP— son **extensiones Lua**, y las oficiales no son ninguna excepción: vienen
embebidas en el binario pero **inactivas por defecto** y **sin
privilegio de kernel**, de modo que una alternativa de terceros puede sustituir
cualquiera. Se activan por nombre en `plugins.enabled` de `nu.toml`; las
dependencias se resuelven solas en orden topológico (activar `chat` arrastra
`agent`, `providers`, `sessions` y `toolkit`).

## El harness

El conjunto de producto —lo que activa `nu --default-config`— es un coding
harness completo, repartido en cuatro contratos:

- **[providers](../providers.md)** — el registro de modelos (TOML) y los
  adaptadores de LLM (`anthropic`, `openai-compat` y `gemini`, embebidos). Los
  modelos se declaran como datos, no como código.
- **[sessions](../sesiones.md)** — la persistencia de conversaciones: JSONL
  append-only bajo `data_dir()/sessions/`.
- **[agent](../agente.md)** — el motor headless del agente: el turno, las tools,
  los permisos, los hooks, los subagentes y la compactación de contexto.
- **[chat](../chat.md)** — la UI de terminal: transcript, editor de entrada,
  comandos slash y statusline. Solo en TTY.

## Las piezas de apoyo

Tres extensiones más pequeñas que el harness usa por dentro —y que también sirven
por sí solas:

- **[mcp](mcp.md)** — integra servidores MCP (Model Context Protocol) como tools
  del agente: cada tool remota se registra igual que una propia. Lua puro sobre
  `nu.proc` y `nu.json`.
- **[toolkit](toolkit.md)** — el toolkit de widgets sobre `nu.ui` y `nu.text`:
  contenedores de layout, foco, composición entre plugins y themes. Es lo que el
  chat usa para pintar.
- **[repl](repl.md)** — un intérprete de Lua interactivo sobre la API pública,
  activable en solitario: el punto de partida del autor que no quiere el harness.

## Escribir la tuya

- **[Guía de autoría de plugins](../guia-plugins.md)** — la sabiduría práctica
  para construir una extensión propia sobre la API del core y los contratos de
  las oficiales, con su checklist.

## Experimental

- **mesh** (la malla de agentes) — specs Role+Job, claim por CAS de git, runner
  de jobs y torneo de forks. Está fuera del conjunto de producto y su contrato
  sigue en borrador, así que **no tiene aún documentación pública**; su diseño
  vive en [malla.md](../malla.md).

---

La extensión `example` viene embebida solo como **andamiaje de referencia** para
autores de plugins y pruebas del loader: no es producto y no entra en ningún
conjunto activable por defecto.
