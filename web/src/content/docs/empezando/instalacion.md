---
title: Instalación
description: Instala el binario estático de nu desde una release o compílalo con Go.
---

`nu` es **un único binario estático** sin dependencias dinámicas
(`CGO_ENABLED=0`): corre tal cual en cualquier distro o contenedor. No hay que
instalar Node, npm ni ningún runtime.

## Instalación rápida (`curl | sh`)

El camino de una línea: el script detecta tu sistema (linux/darwin × amd64/arm64),
descarga el binario de la última release, **verifica el checksum** y lo instala en
tu `PATH`.

```sh
curl -fsSL https://raw.githubusercontent.com/dbareagimeno/nu/main/install.sh | sh
```

Por defecto instala en `~/.local/bin` (o `/usr/local/bin` si tienes permiso); puedes
forzar el destino con `NU_INSTALL_DIR`:

```sh
curl -fsSL https://raw.githubusercontent.com/dbareagimeno/nu/main/install.sh | NU_INSTALL_DIR=/usr/local/bin sh
```

¿Prefieres revisarlo antes de ejecutarlo? Descárgalo, léelo y córrelo a mano —es un
script POSIX corto y sin magia. Si no quieres el script, sigue con el método manual.

## Desde una release (recomendado)

Cada release publica el binario para las plataformas objetivo
(linux/darwin × amd64/arm64). Descarga el `.tar.gz` de tu sistema de la
[última release](https://github.com/dbareagimeno/nu/releases/latest),
descomprímelo y ponlo en el `PATH`:

```sh
# Ajusta VERSIÓN y la plataforma.
tar -xzf nu-vVERSIÓN-linux-amd64.tar.gz
chmod +x nu
sudo mv nu /usr/local/bin/

nu -e 'return nu.version'   # comprueba la instalación (headless, sin TTY)
```

Verifica la integridad con el `checksums.txt` que acompaña a cada release:

```sh
sha256sum -c checksums.txt
```

## Compilar desde el código

Necesitas Go (la versión mínima está en `go.mod`):

```sh
git clone https://github.com/dbareagimeno/nu
cd nu
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o nu .
```

## Windows

En Windows, `nu` se usa vía **WSL2** con el binario de `linux/amd64`. El soporte
nativo de Windows está pospuesto.

## Comprobar que funciona

```sh
nu -e 'return nu.version'
```

Deberías ver una tabla con `major`, `minor`, `patch` y `api` (el nivel de la
API del core). Si lo ves, ya tienes un runtime de Lua funcionando.

:::note[Runtime desnudo]
`nu` recién instalado **no trae ninguna extensión activa**: arrancarlo con TTY
te muestra una pantalla del runtime con sus capacidades y la opción de activar
el conjunto oficial (el agente, el chat…) con una tecla, sin red. Sin TTY (CI,
Docker, scripts), el equivalente de un comando es `nu --default-config`, que
escribe esa activación en tu `nu.toml`. Esto es deliberado —ver [Conceptos
clave](/nu/empezando/conceptos/)—. Para scripting headless con `nu -e` no
necesitas activar nada.
:::

## Siguiente paso

Ya puedes ejecutar Lua. Sigue con [Tu primer
script](/nu/empezando/primer-script/).
