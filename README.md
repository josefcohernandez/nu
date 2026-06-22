# nu

[![CI](https://github.com/dbareagimeno/nu/actions/workflows/ci.yml/badge.svg)](https://github.com/dbareagimeno/nu/actions/workflows/ci.yml)

> Un runtime de Lua orientado a terminal cuya killer app es un coding
> harness. Un binario Go, kernel mínimo, y todo lo demás — incluido el
> propio agente — extensiones Lua.

Estado: **kernel construido** (las 45 sesiones del [plan de
implementación](docs/implementacion.md) están cerradas; un binario Go estático,
sin CGO, con las extensiones oficiales embebidas). El método del proyecto sigue
siendo el mismo que lo hizo posible: el diseño se decide en `docs/` y la API se
valida escribiendo pseudocódigo contra ella antes de congelarla —esos documentos
*son* la espec, y el código la implementa, nunca al revés.

## Instalación

Cada release publica el binario estático para las plataformas objetivo. Descarga
el `.tar.gz` de tu sistema de la [última
release](https://github.com/dbareagimeno/nu/releases/latest), descomprímelo y
ponlo en el `PATH`:

```sh
# Ajusta VERSIÓN y la plataforma (linux/darwin × amd64/arm64).
tar -xzf nu-vVERSIÓN-linux-amd64.tar.gz
chmod +x nu
sudo mv nu /usr/local/bin/
nu -e 'return nu.version'   # comprueba la instalación (headless, sin TTY)
```

Verifica la integridad con el `checksums.txt` que acompaña a cada release
(`sha256sum -c checksums.txt`).

El binario no tiene dependencias dinámicas (`CGO_ENABLED=0`): corre tal cual en
cualquier distro o contenedor. En **Windows**, `nu` se usa vía **WSL2** con el
binario de `linux/amd64` (el Windows nativo está pospuesto, ver
[`docs/pospuesto.md`](docs/pospuesto.md) P18).

También puedes compilarlo desde el código con Go (la versión está en `go.mod`):

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o nu .
```

## Documentación

Orden de lectura sugerido:

1. [Filosofía](docs/filosofia.md) — principios y lo que nu no es
2. [Arquitectura](docs/arquitectura.md) — la forma del sistema (vista estática)
3. [Modelo de ejecución](docs/modelo-ejecucion.md) — concurrencia, comunicación y limitaciones (vista dinámica)
4. [API del core](docs/api.md) — la superficie sagrada v1
5. [ADR](docs/adr.md) — registro de decisiones y su razonamiento

Contratos de las extensiones oficiales:

- [Providers](docs/providers.md) — registro TOML y adaptadores de LLM
- [Agente](docs/agente.md) — el motor headless: turno, tools, permisos, subagentes
- [Sesiones](docs/sesiones.md) — persistencia JSONL append-only
- [Chat](docs/chat.md) — la UI oficial

Para autores de plugins:

- [Guía de plugins](docs/guia-plugins.md) — sabiduría práctica y checklist

Proceso y registro de trabajo:

- [Pseudocódigo de validación](docs/pseudocodigo.md) — las rondas que torturaron la API
- [Problemas abiertos](docs/problemas.md) — grietas que la v1 necesita cerradas
- [Pospuesto](docs/pospuesto.md) — lo que decidimos no decidir todavía, con su disparador
- [Plan de implementación](docs/implementacion.md) — la secuencia de construcción, una feature por sesión
