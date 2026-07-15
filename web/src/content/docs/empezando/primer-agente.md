---
title: Tu primer agente
description: Activa las extensiones oficiales, configura un provider de LLM y ejecuta un turno de agente headless con nu -p.
---

El coding harness es la *killer app* de `nu`, pero —fiel al principio de que el
core no sabe lo que es un agente— el agente es una **extensión**. Esta página te
lleva de un runtime desnudo a un turno de agente funcionando.

:::note[Requiere red y una API key]
A diferencia del resto del manual, este flujo habla con un LLM real: necesita
conexión y una clave de API. Los comandos son correctos, pero su salida depende
de tu provider.
:::

## 1. Activa las extensiones oficiales

Las extensiones oficiales vienen embebidas en el binario pero **inactivas por
defecto**. El agente necesita tres: `providers`, `sessions` y `agent`.
Actívalas en `nu.toml`, dentro de `nu.config.dir()` (normalmente
`~/.config/nu/`):

```toml
# ~/.config/nu/nu.toml
[plugins]
enabled = ["providers", "sessions", "agent"]
```

Si lanzas el agente sin activarlas, el error es accionable: te nombra
exactamente esta línea de `nu.toml`.

## 2. Declara un provider

Los providers de LLM se declaran como **datos** (TOML), no como código.
Edita `providers.toml` en el mismo directorio de config:

```toml
# ~/.config/nu/providers.toml
[providers.anthropic]
adapter     = "anthropic"
base_url    = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"        # nunca la clave en el fichero

[[providers.anthropic.models]]
id      = "claude-opus-4-8"
context = 200000
aliases = ["opus"]
```

La clave **nunca** va en el fichero: se lee del entorno (`api_key_env`).

```sh
export ANTHROPIC_API_KEY="sk-..."
```

Un modelo se nombra `"proveedor/id-o-alias"`: `"anthropic/opus"`.

## 3. Un turno headless con `nu -p`

`nu -p '<prompt>'` ejecuta **un turno de agente headless** y escribe el texto
final del asistente a stdout. Es el modo scripting/CI: el motor del agente es
headless por diseño, así que no necesita terminal interactivo.

```sh
nu -p 'resume el README de este proyecto en tres líneas'
```

Selecciona el modelo con `--model` (anula el de `agent.toml`):

```sh
nu -p '¿qué hace este repo?' --model anthropic/opus
```

### Permisos en headless

Las tools sensibles (escribir ficheros, ejecutar comandos, red) **se deniegan
en headless** salvo que lo autorices: no hay UI que pregunte. Para concederlas
en una ejecución no interactiva, usa `--auto-permissions` (el riesgo se elige,
no se hereda):

```sh
nu -p 'crea un archivo CHANGELOG.md inicial' --auto-permissions
```

Si una tool se deniega por falta de permiso, `nu` sale con **código 3**
(distinto del 1 de un error de ejecución) para que un script distinga "el modelo
no pudo actuar por permisos" de un fallo real.

### Continuar la última sesión

`--continue` (o `-c`) reanuda la sesión más reciente del proyecto (el cwd) antes
de enviar el prompt:

```sh
nu -p 'y ahora añade tests' --continue
```

## 4. Lo mismo desde Lua

`nu -p` es azúcar sobre la API pública de la extensión `agent`. Esto es,
esencialmente, lo que hace por dentro —y lo que escribirías tú en un `init.lua`
o un script—:

```lua
local agent = require("agent")

nu.task.spawn(function()
  local s = agent.session{ model = "anthropic/opus", cwd = nu.fs.cwd() }
  local final = s:send("resume el README en tres líneas")  -- ⏸ ejecuta el turno
  s:close()

  -- El Message final concatena sus bloques de texto.
  local text = ""
  for _, b in ipairs(final.content) do
    if b.type == "text" then text = text .. b.text end
  end
  nu.fs.write(nu.fs.tmpdir() .. "/respuesta.txt", text)
end)
```

Que el CLI use exactamente la misma API pública que tú es el principio en
acción: la UI oficial no tiene acceso privilegiado. Si algo del agente no se
pudiera construir con la API pública, sería la API la que está incompleta.

## Siguiente paso

Ya tienes el harness funcionando. A partir de aquí, la [referencia de la
API](/nu/referencia/convenciones/) documenta cada primitiva del core sobre la
que se construye todo esto.
