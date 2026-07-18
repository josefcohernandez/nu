# enu

[![CI](https://github.com/dbareagimeno/enu/actions/workflows/ci.yml/badge.svg)](https://github.com/dbareagimeno/enu/actions/workflows/ci.yml)
[![Licencia: Apache 2.0](https://img.shields.io/badge/licencia-Apache%202.0-blue.svg)](LICENSE)

[English](README.md) · **Español**

> **Un coding harness auto-extensible, en un único binario estático.**
> Sin Node. Sin npm. Sin Python.

enu trae de serie un agente de código, una UI de terminal, providers de modelos,
sesiones y soporte de MCP — pero ninguno vive en el core. Son **plugins Lua
construidos sobre la misma API pública que usas tú**. Sustituye una tool.
Rediseña la TUI. Reescribe el loop del agente. O quita el agente entero y usa
enu como runtime nativo para tu propia automatización de terminal.

<!-- TODO(S47): demo real aquí (GIF/asciinema de 30-45 s): chat → la tool pide
     permiso → el agente edita un archivo → un plugin Lua → `enu -p` headless. -->

[Instalar](#arranque-rápido) · [Escribir un plugin](#mostrar-no-contar) · [Cómo funciona](#cómo-funciona) · [Docs](docs/README.md)

---

## Arranque rápido

```sh
# 1. Un binario estático — detecta SO/arquitectura y verifica el checksum.
curl -fsSL https://raw.githubusercontent.com/dbareagimeno/enu/main/install.sh | sh

# 2. Activar el conjunto oficial (agente, chat, providers, sesiones, MCP…).
enu --default-config

# 3. Apúntalo a un modelo y en marcha — chat interactivo, o headless con `-p`.
export ANTHROPIC_API_KEY=sk-...
enu                              # TUI interactiva
enu -p 'Resume este repositorio' # un turno headless a stdout
```

Recién instalado, enu es un **runtime desnudo**: las extensiones oficiales
vienen embebidas pero **apagadas por defecto**, así que encenderlas es una
elección explícita y reversible. El onramp y la configuración completos, en la
[guía de inicio](docs/README.md).

---

## Por qué enu

### Despliégalo donde sea

Descargas un binario y lo ejecutas en tu portátil, en un contenedor o en CI. Sin
runtime del lenguaje anfitrión, sin gestor de paquetes, sin toolchain de plugins
que aprovisionar antes. El mismo binario corre en una Debian limpia o en una
máquina air-gapped.

### Reescríbelo todo

El agente oficial **no tiene API privada**. Remapea el chat, añade tools y
comandos slash, engancha el ciclo de vida, o reemplaza el loop del agente
entero — todo a través de la superficie pública `enu.*`. Si una feature oficial
no se puede construir como un plugin cualquiera, es un bug de la API, no una
excusa para un atajo.

### Automatiza sin UI

El motor del agente no pinta nada por diseño. `enu -p '…'` ejecuta un turno y
escribe el resultado a stdout, con códigos de salida estables — hecho para
scripts, pipes y CI, no una TUI encajada a la fuerza en un entorno headless.

---

## Mostrar, no contar

Un plugin es un directorio con un `plugin.toml` y un `init.lua`. Aquí tienes uno
completo que añade un comando `/review`:

```lua
-- ~/.config/enu/plugins/review/init.lua
local chat = require("chat")

chat.command{
  name = "review",
  description = "Revisa el diff actual de git",
  run = function()
    chat.prompt("Revisa el diff actual de git. Céntrate en la corrección.")
  end,
}
```

Lo guardas, recargas, listo — sin SDK, sin compilador, sin gestor de paquetes.

**Esto no es una API de extensión especial.** El chat oficial registra sus
propios comandos por la misma superficie `chat.command{}`. Todo lo que puede
hacer un plugin oficial, puede hacerlo el tuyo.

---

## Cómo funciona

```
┌─────────────────────────────────────────────┐
│ Userland Lua                                │
│   agent · chat · providers · sessions · mcp │
│   tus plugins                               │
├─────────────────────────────────────────────┤
│ API pública  enu.*                          │
├─────────────────────────────────────────────┤
│ kernel estático en Go                       │
│   fs · proc · http · search · ui · workers  │
└─────────────────────────────────────────────┘
     Los plugins oficiales no usan API privada.
```

Tres ideas lo sostienen (razonamiento completo en
[docs/core/filosofia.md](docs/core/filosofia.md)):

1. **El core no sabe lo que es un agente.** Emacs/Textadept, no Neovim: un
   kernel diminuto de primitivas más un intérprete de Lua. Agente, MCP, chat,
   comandos slash y providers son todos extensiones Lua — las oficiales
   incluidas, sin privilegio arquitectónico.
2. **Corolario de completitud.** Si una feature oficial no se puede construir
   con la API pública, la API está incompleta — el arreglo va en la API, nunca
   en un atajo privilegiado. Esto es lo que mantiene la superficie honesta.
3. **Lua decide, Go ejecuta.** El trabajo pesado (búsqueda, diff, markdown,
   highlighting, streaming HTTP) es una primitiva Go, paralela por dentro; Lua
   solo orquesta.

El conjunto oficial de producto — `providers`, `sessions`, `agent`, `mcp`,
`chat`, `toolkit`, `repl` — son todos plugins. Una alternativa de terceros puede
sustituir cualquiera.

---

## enu vs Pi

[Pi](https://github.com/earendil-works/pi) es lo más parecido a enu: un coding
harness hackeable y extensible. Es más maduro que enu en todo lo que da tener
usuarios — un ecosistema real de plugins, una UX pulida, un SDK estable. La
diferencia honesta es **cómo se distribuye cada uno**:

| | **enu** | Pi |
|---|---|---|
| Distribución | Un binario estático Go | Node / npm |
| Runtime requerido | Ninguno | Node |
| Lenguaje de extensión | Lua | TypeScript |
| Toolchain para un plugin básico | Ninguna | Ecosistema Node |
| Loop del agente reemplazable | Sí | Sí |
| API pública como frontera arquitectónica | Principio explícito (corolario de completitud) | Arquitectura extensible |
| Ecosistema de plugins | Inicial | Maduro |
| Headless / RPC | Headless hoy; RPC en el roadmap | Headless + RPC, maduro |
| Madurez | Pre-1.0 | Producción, comunidad amplia |

Pi es más maduro. enu es más fácil de desplegar como infraestructura — un
binario, sin runtime anfitrión, plugins que son solo ficheros Lua.

---

## Estado del proyecto

enu es **pre-1.0**. El kernel está construido y las extensiones oficiales corren
sobre él, pero la API sigue siendo experimental y puede cambiar (las roturas se
hacen a propósito, vía una decisión registrada, nunca por accidente). Soportado
en Linux y macOS (nativos), y en Windows a través de WSL2.

Señales que puedes comprobar en vez de creer:

- Los plugins oficiales corren **exclusivamente** por la API pública.
- Los tests de extremo a extremo ejercitan el **binario real compilado** (loop
  del agente, MCP, sesiones), más tests de TUI interactiva sobre un PTY.
- El race detector corre en CI, y las releases publican checksums firmados.

Es un buen momento para escribir un plugin, enchufar enu a CI o apuntarlo a un
modelo local — y para decirnos dónde chirría la API o el diseño.

---

## Documentación

El mapa completo está en [docs/README.md](docs/README.md). Elige tu camino:

- **Quiero usar enu** → [Empezar](docs/README.md) · [CLI y configuración](docs/contracts/agente.md)
- **Quiero escribir un plugin** → [Guía de plugins](docs/contracts/guia-plugins.md) · [API del core](docs/contracts/api.md)
- **Quiero entender el diseño** → [Filosofía](docs/core/filosofia.md) · [Arquitectura](docs/core/arquitectura.md) · [Decisiones (ADR)](docs/decisions/adr/README.md)
- **Quiero contribuir** → [CONTRIBUTING.md](CONTRIBUTING.md)

La fuente de diseño interna (contratos, ADR, hallazgos) está en español; la
documentación de cara al público, en inglés.

---

## Contribuir

Las aportaciones son bienvenidas — lee antes [CONTRIBUTING.md](CONTRIBUTING.md).
Como el proyecto es dirigido por el diseño, el mejor primer paso es
[docs/core/filosofia.md](docs/core/filosofia.md) y el
[índice de ADR](docs/decisions/adr/README.md): entiende el *porqué* antes de
proponer el *qué*.

El autor conserva la titularidad del proyecto, por lo que al incorporar código
de terceros puede pedir un acuerdo de contribución (CLA).

## Licencia

enu es software libre bajo la [Apache License 2.0](LICENSE) (permisiva, con
concesión de patentes). Eres libre de usarlo, estudiarlo, modificarlo y
distribuirlo, incluso comercialmente. Copyright de Diego Barea; ver
[NOTICE](NOTICE).
