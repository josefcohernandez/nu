---
title: "El release publica una imagen de contenedor multi-arch (linux/amd64+arm64) en GHCR como canal de ejecución para hosts sin binario nativo (complementa ADR-027)"
type: "adr"
id: "ADR-028"
status: "aceptada"
date: "2026-07-19"
---
# ADR-028 · Imagen de contenedor publicada: el canal de ejecución para hosts sin binario nativo

**Estado:** Aceptada · 2026-07-19 (**complementa —no reemplaza—
[ADR-027](adr-027-sin-binario-de-mac-intel.md)**; materializa el flujo Docker
aportado por la contribución externa [PR #108](https://github.com/dbareagimeno/enu/pull/108))

**Contexto.** [ADR-025](adr-025-reposicionamiento-motor-de-harnesses.md)
posiciona enu como un motor pensado para desplegarse como infraestructura, y su
razonamiento ya nombra a los **contenedores** como uno de los entornos objetivo
(«contenedores, CI, air-gapped» — todos Linux). [ADR-027](adr-027-sin-binario-de-mac-intel.md)
retiró el binario nativo `darwin/amd64`: el macOS **publicado** es Apple Silicon
(`darwin/arm64`), y la vía de escape documentada para quien siga en un Mac Intel
es «Linux/WSL2, o compila desde fuente». Su disparador de reapertura era
«demanda real y sostenida de usuarios de Mac Intel que no puedan usar Linux/WSL2
ni compilar desde fuente».

Una contribución externa (PR #108) aportó un flujo Docker completo
—build/test/dev por bind mount y una imagen `runtime` autocontenida—. Docker en
un host Intel corre la imagen **`linux/amd64` dentro de la VM de Docker**: es
exactamente la vía de escape que ADR-027 ya asumía, materializada, **sin ningún
binario `darwin`**. Lo que faltaba para que «ejecutar enu sin binario nativo»
fuese real y no un «hazlo tú»: la PR compila en local pero **no publica** ninguna
imagen versionada que un usuario pueda `docker run` sin más.

**Decisión.** El release **publica, además de los tarballs, una imagen OCI
multi-arch (`linux/amd64` + `linux/arm64`) en GHCR** (`ghcr.io/dbareagimeno/enu`),
etiquetada por versión (y `latest` para releases no pre-release). La imagen
envuelve el **mismo binario estático** (`CGO_ENABLED=0`) que los tarballs, en el
`target: runtime` del Dockerfile.

Esa imagen es el **canal de ejecución soportado para hosts sin binario nativo**
—señaladamente **Mac Intel**, vía Docker Desktop (`linux/amd64` en la VM)— y, en
general, para desplegar enu en contenedor/CI. **No se reintroduce `darwin/amd64`:**
ADR-027 se mantiene íntegra; `install.sh` sigue rechazando el binario nativo
Intel, pero ahora ofrece la imagen como remedio. Con esto, el **disparador de
ADR-027 queda atendido por el canal contenedor**, no por un binario nuevo — de
ahí que esta decisión *complemente* y no *reemplace* a ADR-027.

El **flujo Docker de desarrollo** (Compose + Makefile: `build`/`test`/`blob`/`dev`
en local por bind mount) se incorpora al repo bajo `docker/` como conveniencia de
DX. No es API ni parte de la espec: como CI y el propio release, es **DevOps del
operador** ([ADR-013](adr-013-integracion-continua-y-publicacion.md)); reproduce
los pasos ya existentes (`CGO_ENABLED=0 go build`, el filtro de `internal/vmwasm`
de `ci.yml`, `internal/vmwasm/build.sh`), no inventa un build nuevo.

Publicar una imagen era, además, un **disparador explícito de ADR-013** para
reabrir su punto 5 (empaquetado *a mano* vs GoReleaser). Este ADR lo **atiende y
lo reafirma**: la imagen se construye con `buildx` dentro del propio `release.yml`,
a mano y sin GoReleaser — el alcance sigue siendo pequeño y en un solo workflow, la
misma razón que sostuvo la elección original. ADR-013 no se reescribe (inmutable);
esta línea cierra su disparador.

**Razonamiento.**
- **Coste/beneficio.** La imagen **reutiliza el target `linux` ya soportado y
  probado** (smoke + `-race` en CI): cero target de compilación nuevo — justo el
  coste (un runner escaso, un asset más que firmar) que ADR-027 rechazó pagar por
  Intel. GHCR se publica con el `GITHUB_TOKEN` (`packages: write`), sin secretos
  extra.
- **Coherencia con ADR-025/027.** Los entornos objetivo del motor ya eran
  contenedores/Linux; publicar la imagen **formaliza un canal que el diseño ya
  daba por supuesto**, en lugar de abrir una plataforma nueva.
- **Multi-arch bien hecho.** El `builder` del Dockerfile se ancla al
  `BUILDPLATFORM` y **cross-compila** al `TARGETOS/TARGETARCH` (posible porque el
  binario es `CGO_ENABLED=0`): así publicar `linux/arm64` desde un runner `amd64`
  no emula todo el `go build` bajo QEMU (lento y frágil), solo el `apt-get` de la
  etapa `runtime`.
- **Por qué `debian-slim` y no `scratch`/distroless.** enu es un coding harness
  cuya gracia es **spawnear procesos** (`enu.proc`: git, bash, herramientas del
  agente): necesita un userland real, no solo el binario estático. La imagen trae
  `ca-certificates`, `git` y `bash`; las herramientas del **proyecto del usuario**
  (node, python, compiladores…) son *bring your own* — se montan con el workspace
  o se derivan en una imagen propia; no se infla la base.
- **Por qué un ADR y no un retoque.** La decisión ripplea por `release.yml`,
  `install.sh`, `docs/ops/release.md` y `docs/core/arquitectura.md`, y toca el
  disparador de ADR-027: es multi-documento y de producto (cómo se **distribuye y
  ejecuta** enu), justo lo que un ADR registra.

**Consecuencias.**
- `docker/`: nuevo directorio con `Dockerfile` (+ `Dockerfile.dockerignore`),
  `docker-compose.yml` y `Makefile` — el flujo de build/test/dev/run local.
  Autoría original: PR #108 (José Fco), re-aterrizada sobre `develop` y conformada
  al renombrado `nu`→`enu` ([ADR-022](adr-022-renombrado-total-del-proyecto.md)).
- `release.yml`: nuevo job **`imagen`** (`needs: verificar-version`) que hace
  `buildx` multi-arch y `push` a GHCR con `packages: write`; `latest` solo si el
  tag no es pre-release. Independiente del job `release` (la imagen va al
  registro, no a los assets de la Release).
- `install.sh`: el rechazo de `darwin/amd64` añade el remedio `docker run
  ghcr.io/dbareagimeno/enu`.
- `docs/ops/release.md`: la verificación añade la imagen (pull del manifiesto
  multi-arch + smoke `-e`), y una nota operativa: la **primera** publicación crea
  el paquete GHCR como privado — hacerlo público una vez.
- `docs/core/arquitectura.md` / G9: se añade el **contenedor como vía de
  ejecución soportada** para hosts sin binario nativo; el alcance de binarios
  *publicados* no cambia.
- **Relación con ADR-013:** su punto 5 fijaba «si se añaden imágenes Docker, se
  reabre esta elección [a mano vs GoReleaser]». Este ADR **atiende ese disparador
  y reafirma el empaquetado a mano** (`buildx` en `release.yml`, sin GoReleaser).
  ADR-013 no se reescribe (inmutable); el bucle queda cerrado aquí.
- **Relación con ADR-027:** la complementa; ADR-027 sigue **Aceptada** y su
  decisión (no publicar binario `darwin/amd64`) intacta. Su disparador se
  considera **atendido** por este canal.
- **Disparador de revisión de ESTA decisión:** si sostener la publicación
  multi-arch se vuelve costoso o frágil (p. ej. la variante `arm64` bajo QEMU),
  reconsiderar reducir a `linux/amd64` o mover a runners `arm` nativos.
