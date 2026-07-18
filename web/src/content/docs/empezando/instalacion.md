---
title: Instalación
description: Instala el binario estático de enu desde una release o compílalo con Go.
---

`enu` es **un único binario estático** sin dependencias dinámicas
(`CGO_ENABLED=0`): corre tal cual en cualquier distro o contenedor. No hay que
instalar Node, npm ni ningún runtime.

## Instalación rápida (`curl | sh`)

El camino de una línea: el script detecta tu sistema (linux/darwin × amd64/arm64),
descarga el binario de la última release, **verifica el checksum** y lo instala en
tu `PATH`.

```sh
curl -fsSL https://raw.githubusercontent.com/dbareagimeno/enu/main/install.sh | sh
```

Por defecto instala en `~/.local/bin` (o `/usr/local/bin` si tienes permiso); puedes
forzar el destino con `ENU_INSTALL_DIR`:

```sh
curl -fsSL https://raw.githubusercontent.com/dbareagimeno/enu/main/install.sh | ENU_INSTALL_DIR=/usr/local/bin sh
```

¿Prefieres revisarlo antes de ejecutarlo? Descárgalo, léelo y córrelo a mano —es un
script POSIX corto y sin magia. Si no quieres el script, sigue con el método manual.

## Desde una release (recomendado)

Cada release publica el binario para las plataformas objetivo
(linux/darwin × amd64/arm64). Descarga el `.tar.gz` de tu sistema de la
[última release](https://github.com/dbareagimeno/enu/releases/latest),
descomprímelo y ponlo en el `PATH`:

```sh
# Ajusta VERSIÓN y la plataforma.
tar -xzf enu-vVERSIÓN-linux-amd64.tar.gz
chmod +x enu
sudo mv enu /usr/local/bin/

enu -e 'return enu.version'   # comprueba la instalación (headless, sin TTY)
```

Verifica la integridad con el `checksums.txt` que acompaña a cada release:

```sh
sha256sum -c checksums.txt
```

## Compilar desde el código

Necesitas Go (la versión mínima está en `go.mod`):

```sh
git clone https://github.com/dbareagimeno/enu
cd enu
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o enu .
```

## Windows

En Windows, `enu` se usa vía **WSL2** con el binario de `linux/amd64`. El soporte
nativo de Windows está pospuesto.

## Comprobar que funciona

```sh
enu -e 'return enu.version'
```

Deberías ver una tabla con `major`, `minor`, `patch` y `api` (el nivel de la
API del core). Si lo ves, ya tienes un runtime de Lua funcionando.

:::note[Runtime desnudo]
`enu` recién instalado **no trae ninguna extensión activa**: arrancarlo con TTY
te muestra una pantalla del runtime con sus capacidades y la opción de activar
el conjunto oficial (el agente, el chat…) con una tecla, sin red. Sin TTY (CI,
Docker, scripts), el equivalente de un comando es `enu --default-config`, que
escribe esa activación en tu `enu.toml`. Esto es deliberado —ver [Conceptos
clave](/enu/docs/conceptos/)—. Para scripting headless con `enu -e` no
necesitas activar nada.
:::

## Siguiente paso

Ya puedes ejecutar Lua. Sigue con [Tu primer
script](/enu/docs/primer-script/).
