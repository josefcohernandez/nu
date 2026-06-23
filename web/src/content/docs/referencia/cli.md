---
title: La CLI
description: Los flags del binario nu, los modos headless y los códigos de salida.
---

Esta página documenta la **superficie de línea de comandos** del binario `nu`.
No es API sagrada `nu.*` (eso es la superficie Lua): es la interfaz del
ejecutable. Vive en el binario porque el core no sabe lo que es un agente —el CLI
orquesta las extensiones por la API pública, igual que haría un `init.lua`—.

## Modos

```
nu                       Arranque canónico. Con TTY y ningún plugin activo,
                         pinta la pantalla de runtime desnudo (G21).
nu --default-config      Activa el conjunto oficial de producto sin TTY: escribe
                         plugins.enabled en nu.toml y sale (con -p/-e, lo activa
                         solo para ese proceso, sin tocar disco).
nu -e '<lua>'            Evalúa un chunk Lua headless e imprime sus retornos.
nu -p '<prompt>'         Ejecuta un turno de agente headless e imprime el texto
                         final del asistente a stdout.
```

### `nu` (sin argumentos)

Arranque normal. Con un TTY interactivo y **ningún plugin activo**, pinta la
**pantalla de runtime desnudo**: un render fijo con la versión y el nivel de API,
las rutas de config y plugins, el catálogo de extensiones embebidas y las
acciones (activar el conjunto oficial / extensiones sueltas / salir). Sin TTY, no
hay pantalla: imprime el uso. El equivalente sin TTY es `nu --default-config`.

### `nu --default-config`

El **onramp sin TTY** para tener nu *batteries-included* en CI, Docker o scripts,
donde la pantalla de runtime desnudo no existe. Activa el **conjunto oficial de
producto** —las siete extensiones embebidas (`providers`, `sessions`, `agent`,
`mcp`, `chat`, `repl`, `toolkit`), todas menos el andamiaje de pruebas
`example`—. Tiene **dos modos** según lo combines:

- **Solo** (`nu --default-config`): **escribe** `plugins.enabled` en
  `config.dir()/nu.toml` y sale. Preserva el resto del fichero (otras claves,
  `[watchdog]`, …), es **atómico** (no deja un `nu.toml` a medias) e
  **idempotente** (repetirlo no cambia nada). Si el `nu.toml` existente está mal
  formado, **no lo sobrescribe**: sale con error accionable.
- **Combinado con una acción headless** (`--default-config -p '…'` o
  `--default-config -e '…'`): **no toca disco**. Activa el conjunto solo para ese
  proceso y ejecuta la acción. Es el caso del contenedor inmutable: correr con todo
  activo sin reescribir config en cada arranque.

```sh
# Deja la máquina lista de una vez (persistente):
nu --default-config
nu -p 'resume este repo'        # ya con el agente activo

# Docker / CI inmutable (efímero, sin tocar el FS):
nu --default-config -p 'resume este repo'
```

Sin red en ambos modos: las extensiones salen del propio binario. Es superficie
CLI, no API sagrada `nu.*` (no añade nada a la API ni mueve `nu.version.api`).

### `nu -e '<lua>'`

Evalúa el chunk Lua **sin TTY** (headless) e imprime cada valor de retorno en su
propia línea. El chunk corre en el **estado principal** (no es una task): puede
`nu.task.spawn` pero no usar funciones ⏸ directamente. Ver [Tu primer
script](/nu/empezando/primer-script/).

```sh
nu -e 'return nu.version.api'
```

```
2
```

### `nu -p '<prompt>'`

Ejecuta un **turno de agente headless** con el prompt dado e imprime el texto
final del asistente. Corre como task (las funciones ⏸ del turno y sus tools
funcionan sin TTY). Requiere las extensiones `providers`, `sessions` y `agent`
activas. Ver [Tu primer agente](/nu/empezando/primer-agente/).

#### Modificadores de `-p`

| Flag | Efecto |
|---|---|
| `--continue` / `-c` | Reanuda la **última** sesión del proyecto (cwd) antes de enviar el prompt. |
| `--auto-permissions` | Permisos del agente en modo `"auto"`: concede las tools sensibles (sin él se deniegan en headless). El riesgo se elige, no se hereda. |
| `--model 'prov/modelo'` | Selecciona el modelo/provider del turno (anula el de `agent.toml`). |

```sh
nu -p 'añade tests al módulo nuevo' --continue --auto-permissions --model anthropic/opus
```

## Códigos de salida

Los modos headless salen con un código coherente para CI y scripts:

| Código | Significado |
|---|---|
| **0** | Éxito. |
| **1** | Error de ejecución: el chunk de `-e`, el turno del agente o el provider lanzaron, o el arranque falló (grafo de plugins inválido, `nu.toml` roto). |
| **2** | Error de uso: flags incompatibles o un argumento requerido ausente. |
| **3** | Permiso denegado en headless: una tool sensible se denegó por falta de `--auto-permissions`. Código **distinto** del 1 para que un script distinga "el modelo no pudo actuar por permisos" de un fallo de ejecución. |

`nu --default-config` (modo persistente) sale con **0** tras escribir, o con **1**
si no pudo escribir `nu.toml` (p. ej. el fichero existente está mal formado y no se
sobrescribe, o un error de E/S): el mensaje a stderr es accionable.

```sh
# Distinguir un deny de permisos de un fallo real.
nu -p 'borra los temporales'
case $? in
  0) echo "hecho" ;;
  3) echo "necesita --auto-permissions" ;;
  *) echo "error" ;;
esac
```

:::note[Windows]
`nu` se usa en Windows vía **WSL2** con el binario de `linux/amd64`. El soporte
nativo de Windows está pospuesto.
:::
