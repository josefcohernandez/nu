# enu

[![CI](https://github.com/dbareagimeno/enu/actions/workflows/ci.yml/badge.svg)](https://github.com/dbareagimeno/enu/actions/workflows/ci.yml)
[![Licencia: Apache 2.0](https://img.shields.io/badge/licencia-Apache%202.0-blue.svg)](LICENSE)

> **enu** (*e/nu — Extensible Native Userland*) es un agente de código para
> terminal que cabe en **un solo binario** y donde **todo — hasta el propio
> agente — es una extensión Lua que puedes reescribir**.

<!-- TODO(R-05): sustituir por una demo real (GIF/asciinema de 20-40 s):
     chat → la tool pide permiso → el agente edita un archivo → un plugin Lua →
     `enu -p` headless. Colócala aquí, arriba del todo. -->

---

## En 30 segundos

Los agentes de código de hoy son excelentes, pero llegan atados a su base
material: un runtime de Node o Python que instalar y mantener, y un núcleo
cerrado que solo dejas configurar por los bordes. Si quieres cambiar cómo
funciona el agente de verdad —el loop, las tools, la UI— haces un fork o te
aguantas.

**enu invierte eso.** Es un runtime de Lua orientado a terminal que *resulta*
que trae un agente de código de serie. El binario es un **kernel diminuto** de
primitivas rápidas (ficheros, procesos, HTTP, búsqueda, UI de terminal,
concurrencia) sobre un intérprete de Lua. El agente, el chat, los providers de
LLM, el soporte de MCP: **todos son extensiones Lua**, sin ningún privilegio de
kernel — las mismas que podrías escribir tú.

Qué te llevas de ahí:

- **Un solo binario estático, cero dependency hell.** Sin Node, sin npm, sin
  Python, sin toolchain. `curl | sh` y a trabajar; corre igual en tu portátil,
  en un contenedor o en CI.
- **El agente es tuyo, no una caja negra.** Remapea el chat, añade tools y
  comandos slash, engancha hooks o **reemplaza el loop del agente entero** con
  la misma API pública que usa la versión oficial. Si algo oficial no se puede
  construir con esa API, se considera un bug de la API.
- **Modo headless de primera clase.** El motor del agente no pinta nada por
  diseño: `enu -p '...'` ejecuta un turno y escribe el resultado a stdout. Apto
  para scripts, pipes y CI sin ceremonia.
- **Trae tu propio modelo.** Los providers se declaran como datos (TOML), no
  como código: Anthropic, cualquier endpoint compatible con OpenAI (OpenAI,
  Groq, OpenRouter, vLLM, Ollama…) y Gemini van embebidos; el resto es un plugin.

---

## Un vistazo

```sh
# 1. Instalar (binario estático; detecta SO/arquitectura y verifica el checksum).
curl -fsSL https://raw.githubusercontent.com/dbareagimeno/enu/main/install.sh | sh

# 2. Activar el conjunto oficial (agente, chat, providers, sesiones, MCP…).
#    Viene embebido pero apagado: enchufarlo es una elección tuya, explícita.
enu --default-config

# 3. Declarar un modelo y su clave, y lanzar un turno headless.
export ANTHROPIC_API_KEY="sk-..."
enu -p 'Resume qué hace este repositorio' --model anthropic/opus

#    …o, en un terminal interactivo, abrir el chat:
enu
```

Recién instalado, enu es un **runtime desnudo**: las extensiones oficiales
vienen embebidas pero **inactivas por defecto**, así que el harness es una
elección explícita y reversible, no un hecho consumado. El detalle completo del
onramp, la configuración y los modos del binario está en la
[**guía de inicio**](docs/README.md).

---

## En qué se diferencia (honestamente)

enu no compite en madurez con los agentes establecidos; compite en **forma**.
Esta tabla es deliberadamente honesta en ambas direcciones:

| | **enu** | Claude Code | Aider | Cursor |
|---|---|---|---|---|
| **Base material** | Un binario estático Go, sin runtime | CLI sobre Node/TS | CLI sobre Python (pip) | App de escritorio (Electron) |
| **Extensibilidad** | **Todo es Lua**: el agente, el chat y las tools se reescriben con la API pública | Config + hooks alrededor de un núcleo fijo | Config + scripting | Ajustes + extensiones del editor |
| **Modelo mental** | Runtime que trae un agente; el core **no sabe qué es un agente** | Agente cerrado con puntos de extensión | Agente de pair-programming en CLI | IDE con IA integrada |
| **Providers** | Datos en TOML; trae tu propio endpoint | Anthropic | Muchos | Integrados |
| **Headless / CI** | De primera clase (`enu -p`, códigos de salida estables) | Sí | Sí | No (es un editor) |
| **Madurez** | **Temprana / pre-1.0** — ver abajo | Producto maduro y muy usado | Maduro y muy usado | Producto comercial pulido |
| **Ecosistema de plugins** | Aún no existe | — | — | Grande (extensiones de editor) |

**Dónde enu está peor, y hay que decirlo:** es un proyecto **joven y liderado
por el diseño**. El kernel está implementado (las 45 sesiones del plan de
construcción están cerradas en la rama de desarrollo), pero **no hay todavía una
release estable que demuestre el onramp completo de punta a punta contra un
modelo vivo en una máquina limpia**, la release publicada va por detrás del
código, no hay ecosistema de plugins de terceros, y no hay integración con
editores. Si buscas una herramienta probada para tu trabajo diario **hoy**,
Claude Code, Aider o Cursor son la elección responsable. enu es para quien le
atrae la **idea** —un agente que es del todo suyo, en un binario que no arrastra
un runtime— y quiere verla crecer (o darle forma).

---

## Cómo funciona, por dentro

La tesis, en cuatro ideas (detalle en [docs/filosofia.md](docs/core/filosofia.md)):

1. **El core no sabe lo que es un agente.** Modelo Emacs/Textadept, no Neovim:
   un kernel diminuto de primitivas + un intérprete de Lua. El agente, MCP, el
   chat, los comandos slash y los providers son extensiones Lua, incluidas las
   oficiales — sin privilegio arquitectónico.
2. **Corolario de completitud.** Si una feature oficial no se puede construir con
   la API pública, la API está incompleta y el arreglo va en la API, nunca en un
   atajo privilegiado.
3. **Lua decide, Go ejecuta.** Todo el trabajo pesado (búsqueda, diff, markdown,
   highlighting, streaming HTTP) es una primitiva Go paralela por dentro; Lua
   solo orquesta.
4. **Batteries included, pero no enchufadas.** Las extensiones oficiales viajan
   embebidas (`go:embed`) pero apagadas: enchufarlas es trivial pero explícito.

Las extensiones oficiales del conjunto de producto: `providers` (registro de
modelos y adaptadores de LLM), `sessions` (persistencia JSONL append-only),
`agent` (el motor headless: turno, tools, permisos, hooks, subagentes), `chat`
(la UI de terminal), `mcp` (puente con servidores MCP), `toolkit` (widgets sobre
`enu.ui`) y `repl` (un intérprete de Lua sobre la API pública, activable sin el
harness). Ninguna tiene privilegio de kernel: una alternativa de terceros puede
sustituir cualquiera.

---

## Escribir un plugin

Un plugin es un directorio con un `plugin.toml` (`name`, `version`, `requires?`)
y un `init.lua`. Tiene a su disposición la **API del core** `enu.*` (fs, proc,
http, ws, search, text, re, ui, events, task, workers, codecs — pequeña y
estable) y los contratos de las extensiones oficiales para colgarse de ellas:
`agent.tool{}`, `agent.hook(...)`, `chat.command{}`,
`providers.register_adapter(...)`.

Empieza por la [**guía de plugins**](docs/contracts/guia-plugins.md) y por
[`examples/`](examples/), que trae una TUI funcional escrita en Lua puro sobre la
API del core:

```sh
XDG_CONFIG_HOME=examples enu   # una TUI de demostración (regiones, teclado, resize)
```

---

## Estado del proyecto

enu es **pre-1.0** y liderado por el diseño: los documentos en
[`docs/`](docs/) **son** la especificación y el código los implementa, no al
revés. La API se valida escribiendo pseudocódigo contra ella antes de
congelarla, y el kernel se construye en sesiones incrementales (una feature por
sesión) con tests y checkpoints. Ese método es deliberado, pero también implica
que hay superficie por endurecer: trátalo como una **versión inicial**.

El feedback es exactamente lo que este proyecto busca ahora — sobre la tesis,
sobre la API, o sobre dónde chirría.

---

## Documentación

El mapa completo por capas está en [docs/README.md](docs/README.md). Orden de
lectura sugerido:

1. [Filosofía](docs/core/filosofia.md) — los principios y lo que enu **no** es
2. [Arquitectura](docs/core/arquitectura.md) — la forma del sistema (vista estática)
3. [Modelo de ejecución](docs/core/modelo-ejecucion.md) — concurrencia y límites (vista dinámica)
4. [API del core](docs/contracts/api.md) — la superficie sagrada v1
5. [ADR](docs/decisions/adr/README.md) — el registro de decisiones y su razonamiento

**Contratos de las extensiones oficiales:**
[Providers](docs/contracts/providers.md) ·
[Agente](docs/contracts/agente.md) ·
[Sesiones](docs/contracts/sesiones.md) ·
[Chat](docs/contracts/chat.md)

**Para autores de plugins:** [Guía de plugins](docs/contracts/guia-plugins.md)

---

## Contribuir

Las aportaciones son bienvenidas; lee [CONTRIBUTING.md](CONTRIBUTING.md) antes de
enviar un Pull Request. Como el proyecto tiene un método de trabajo explícito, el
mejor primer paso es leer [docs/filosofia.md](docs/core/filosofia.md) y
[docs/adr.md](docs/decisions/adr/README.md) para entender el *porqué* antes de proponer el *qué*.

El autor conserva la titularidad del proyecto, por lo que al incorporar código de
terceros puede pedir una cesión de derechos o un acuerdo de contribución (CLA).

## Licencia

enu es software libre bajo la [Apache License 2.0](LICENSE) (permisiva, con
concesión de patentes). Eres libre de usarlo, estudiarlo, modificarlo y
distribuirlo, incluso comercialmente. Copyright de Diego Barea; ver
[NOTICE](NOTICE).
