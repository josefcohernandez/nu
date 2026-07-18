---
title: Qué es enu
description: La idea de enu en una página — un kernel mínimo de Lua sobre el terminal donde todo, incluido el agente, es una extensión.
---

`enu` es **un runtime de Lua orientado a terminal cuya killer app es un coding
harness**: un único binario Go con un kernel mínimo donde todo lo demás —
incluido el propio agente— son extensiones Lua.

Dicho de otra forma: `enu` es un coding harness de CLI/TUI, pero esa frase
describe el *producto*. El *proyecto* es un **kernel diminuto y un sistema de
extensiones** donde el loop del agente, la UI de chat, el soporte de MCP y los
providers de LLM no tienen privilegio arquitectónico alguno: son Lua, como lo
que tú escribas.

## Las ideas que no debes perder de vista

1. **El core no sabe lo que es un agente.** El modelo no es Neovim (core grande
   + hooks), es Emacs/Textadept: un kernel que solo aporta primitivas (runtime,
   IO, red, UI de terminal) y un intérprete de Lua. El agente, el chat, los
   comandos slash: extensiones.
2. **Cero dependency hell.** Un único binario estático (`CGO_ENABLED=0`). Sin
   Node, sin npm, sin runtime que instalar. `curl | sh` y a trabajar.
3. **Lua decide, Go ejecuta.** Todo trabajo pesado y universal (búsqueda en
   repo, diff, markdown, highlighting, streaming HTTP) es una primitiva Go,
   paralela por dentro. Si una extensión quema CPU en Lua, falta una primitiva
   o el trabajo va a un worker.
4. **La API del core es sagrada.** Pequeña, aburrida, **crece solo por
   adición**. Romper una firma rompe el mundo.
5. **Batteries included, pero no enchufadas.** El binario trae las extensiones
   oficiales embebidas, pero ninguna se activa sola: `enu` recién instalado es un
   runtime desnudo, y el harness es una elección del usuario.

## Lo que enu no es

- **No es un editor.** No compite con Neovim: no hay buffers gigantes que
  mantener resaltados a cada tecla.
- **No es un framework de agentes de servidor.** Es una herramienta interactiva
  de terminal para personas.
- **No es un proyecto multi-lenguaje de plugins.** Lua embebido es la capa de
  extensión; los procesos externos (MCP y similares) son el escape hatch para
  todo lo demás.

## Cómo está organizado este manual

- **Empezando** (esta sección): instalación, tu primer script, tu primer
  agente y los conceptos que necesitas para no pelearte con el modelo de
  ejecución.
- **Referencia de la API**: una página por namespace de `enu.*`, con la firma,
  la semántica y ejemplos ejecutables de cada función.

:::tip
Si vienes a leer código de inmediato, salta a [Tu primer
script](/enu/docs/primer-script/). Si quieres entender *por qué* `enu` es
como es, sigue por [Conceptos clave](/enu/docs/conceptos/).
:::
