---
title: "Filosofía de enu"
description: "Principios fundacionales y lo que enu no es: el porqué del proyecto."
type: "contrato"
layer: "core"
web: "wiki"
status: "vigente"
---
# Filosofía de enu

> *Un runtime de Lua orientado a terminal cuya killer app es un coding harness.*

`enu` es un coding harness de CLI/TUI. Pero esa frase describe el producto, no el
proyecto. El proyecto es un **kernel mínimo y un sistema de extensiones donde
todo lo demás — incluido el propio agente — es una extensión**.

## Principios

### 1. Cero dependency hell

Un único binario estático. Sin Node, sin npm, sin toolchain, sin runtime que
instalar. `curl | sh` y a trabajar. La instalación y la actualización son
operaciones triviales en cualquier plataforma.

Esto es una reacción directa al estado de los harnesses actuales (pi.dev,
Claude Code, etc.), construidos sobre el ecosistema JS/TS. No criticamos sus
ideas — pi es una inspiración directa — sino su base material.

### 2. El core no sabe lo que es un agente

El modelo no es Neovim (core grande + hooks), es **Emacs/Textadept**: un kernel
diminuto que solo aporta primitivas (runtime, IO, red, UI de terminal) y un
intérprete de Lua. El loop del agente, el soporte de MCP, los comandos slash,
la UI de chat: **todo son extensiones Lua**, incluidas las oficiales.

La formulación general del principio: **el kernel solo conoce sus propias
capacidades** — primitivas, loader, sus extensiones embebidas. El agente es
solo el ejemplo más visible de lo que no es asunto suyo. La vara para
cualquier caso dudoso es esta: si algo se puede describir enteramente con el
vocabulario del kernel (plugins, rutas, versiones), es del kernel; si
necesita vocabulario de producto (agente, chat, tools), es de una extensión.

Corolario: si una feature oficial no se puede construir con la API pública de
extensiones, la API está incompleta. Dogfooding estructural, como hace pi con
sus propias features.

### 3. Lua puede hacer CUALQUIER COSA

No hay "zona privilegiada" reservada al core más allá de las primitivas. Una
extensión puede redefinir la UI entera, reemplazar el loop del agente,
interceptar cualquier evento. El usuario que quiere un harness distinto al
oficial no hace fork: escribe Lua.

La única excepción deliberada son los **providers de LLM**, que se declaran
como datos (TOML), no como código — ver ADR-005.

### 4. Lua decide, Go ejecuta

Lua es el orquestador, nunca el caballo de tiro. Todo trabajo universalmente
pesado (búsqueda en repo, diff, parsing, highlighting, render de markdown,
streaming HTTP) es una primitiva Go, paralela por dentro. Si una extensión está
quemando CPU en Lua, no es un problema de threading: es la señal de que esa
operación debería ser una primitiva.

### 5. Batteries included, pero no enchufadas (ADR-010)

El binario distribuye las extensiones oficiales embebidas (`go:embed`),
pero **ninguna se activa sola**: enu instalado es un runtime desnudo, y el
harness es una elección del usuario, no un hecho consumado. Enchufarlas es
trivial pero **explícito**: con TTY, el primer arranque ofrece activar el
conjunto oficial de una tecla; sin TTY (CI, Docker, scripts), el flag
`enu --default-config` hace lo mismo de un comando (ADR-015). En ambos casos,
sin red — todo sale del binario — y a partir de ahí el agente funciona.
Mismo modelo mental que Neovim: el programa no trae plugins activados. Y
como siempre: esas extensiones no tienen ningún privilegio — se leen, se
sustituyen, se apagan.

### 6. La API del core es sagrada

Si todo se construye sobre las primitivas, romperlas rompe el mundo. La API v1
debe ser deliberadamente pequeña y aburrida, y crecer solo por adición.

## Inspiraciones

| Proyecto | Qué tomamos |
|---|---|
| **pi.dev** | El concepto de harness mínimo y extensible; sus features como extensiones de su propia API |
| **Neovim** | Lua como lenguaje de extensión cultural y probado; el ecosistema de plugins que se construyen unos sobre otros |
| **Emacs / Textadept** | Kernel mínimo + el programa entero escrito en el lenguaje de extensión |
| **El navegador / Luau** | El modelo de concurrencia: hilo principal determinista + workers explícitos + primitivas nativas paralelas |

## Lo que enu no es

- No es un editor. No competimos con Neovim: no hay buffers gigantes que
  mantener resaltados a cada tecla.
- No es un framework de agentes para producción/servidor. Es una herramienta
  interactiva de terminal para personas.
- No es un proyecto de "soporte multi-lenguaje de plugins". Lua embebido es la
  capa de extensión; los procesos externos (MCP y similares) son el escape
  hatch para todo lo demás.
