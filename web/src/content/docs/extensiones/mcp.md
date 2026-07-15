---
title: La extensión mcp
description: Integra servidores MCP (Model Context Protocol) como tools del agente — Lua puro sobre nu.proc y nu.json, con configuración declarativa en mcp.toml y permisos por servidor.
---

## Qué hace

`mcp` conecta **servidores MCP** (Model Context Protocol) al agente: cada tool
que un servidor anuncia queda registrada como una tool más del agente, igual que
una de fichero. Es Lua puro sobre la API pública —fiel a que el core no sabe qué
es MCP—: lanza el servidor como subproceso con `nu.proc`, le habla
JSON-RPC 2.0 por stdio codificando con `nu.json`, y registra las tools con
`agent.tool{...}`. El framing es **newline-delimited** (una línea = un mensaje
JSON terminado en `\n`), el transporte stdio de MCP.

Un lector dedicado demultiplexa las respuestas por su `id`, de modo que puede
haber varios `tools/call` en vuelo sin mezclarse. Si el servidor muere (EOF en
su stdout), todos los requests pendientes despiertan con un error `EMCP`: nadie
cuelga para siempre.

## Cómo se activa

El `plugin.toml` declara `requires = ["agent"]`, así que activar `mcp` arrastra
el agente. Añádelo a `nu.toml`:

```toml
# ~/.config/nu/nu.toml
[plugins]
enabled = ["providers", "sessions", "agent", "mcp"]
```

Cargar la extensión **no conecta a ningún servidor**: lanzar uno es un acto del
usuario. Si existe un `mcp.toml` en el directorio de config, la extensión
auto-conecta sus servidores de forma perezosa (en una task, tolerando la
ausencia del fichero, que es lo normal).

## Configuración

Los servidores se declaran como **datos** en `mcp.toml`, dentro de
`nu.config.dir()` (normalmente `~/.config/nu/`):

```toml
# ~/.config/nu/mcp.toml
[servers.github]
command = ["mcp-server-github"]   # argv del servidor (sin shell), requerido
cwd     = "/opt/proyecto"          # opcional
env     = ["GITHUB_TOKEN=..."]     # opcional
```

Cada entrada `[servers.<nombre>]` lanza un proceso; `<nombre>` es el prefijo de
sus tools. Sin `mcp.toml` no se conecta nada. Un servidor que falla al conectar
se registra en el log y **no impide** a los demás.

### Tools y permisos

Cada tool remota se registra con el nombre `mcp__<servidor>__<tool>` —la
convención de namespacing del ecosistema MCP—, lo que evita choques entre
servidores y hace el patrón de permiso legible. Como son tools de **terceros**,
se registran con `permissions.default = "ask"`: requieren permiso explícito,
nunca se conceden solas como las de solo lectura propias. Habilítalas con un
patrón en la config de permisos del agente:

```toml
allow = ["mcp__github__*"]
```

En headless sin ese `allow`, el pipeline de permisos del agente las **deniega**
con un error accionable. Si el servidor se desconecta, sus tools quedan
re-registradas con un handler que falla de forma accionable (avisa al modelo de
que hay que reconectar) en vez de romper el loop.

## Qué expone

El módulo público se obtiene con `require("mcp")`. Sus errores usan el código
`EMCP` (con la forma estructurada del core: `{ code, message, detail? }`).

| Firma | Efecto |
|---|---|
| `mcp.connect(opts: tabla) -> Conn` **⏸** | Lanza un servidor y completa el handshake (`initialize` → `initialized` → `tools/list` → registro). `opts`: `{ name, command, cwd?, env? }`. Reconectar un `name` ya conectado cierra la conexión anterior. |
| `mcp.connect_configured() -> Conn[]` **⏸** | Lanza todos los servidores de `mcp.toml`. |
| `mcp.get(name: string) -> Conn?` | La conexión viva de un servidor, o `nil`. |
| `mcp.servers() -> string[]` | Los nombres de los servidores conectados. |

Sobre un handle `Conn`:

| Firma | Efecto |
|---|---|
| `Conn:call_tool(remote_name: string, args?: tabla) -> result` **⏸** | Invoca `tools/call` en el servidor (`remote_name` es el nombre sin prefijo). |
| `Conn:list_tools() -> tool[]` **⏸** | Pide `tools/list`; cada tool es `{ name, description?, inputSchema? }`. |
| `Conn:close()` | Mata el servidor limpiamente y desregistra sus tools. Idempotente. |

La vida del proceso sigue la de la task que llamó a `connect`: al terminar esa
task (o al `Conn:close()`), el servidor muere. Para un servidor de larga vida se
conecta desde una task que vive lo que la sesión.

```lua
local mcp = require("mcp")

nu.task.spawn(function()
  local conn = mcp.connect{ name = "github", command = { "mcp-server-github" } }
  -- A partir de aquí, las tools mcp__github__* están disponibles para el agente.
  for _, t in ipairs(conn:list_tools()) do
    nu.log.info("tool MCP disponible: %s", t.name)
  end
end)
```

El cliente negocia la versión del protocolo MCP en `initialize` y anuncia sus
datos como `clientInfo` (`name = "nu"`); v1 cubre las tools de texto, el caso
central de un servidor MCP.
