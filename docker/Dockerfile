# syntax=docker/dockerfile:1.7
#
# Build de `enu` en Docker. Referencia de lo que hace cada etapa: README.md
# ("Deploy it anywhere" / Quickstart) y CI (.github/workflows/ci.yml +
# release.yml) — este Dockerfile reproduce esos mismos pasos, no inventa un
# flujo nuevo.
#
# Puntos clave del build real que esto respeta:
#   - `go build` con CGO_ENABLED=0: el binario final es estático, sin libc.
#   - El blob internal/vmwasm/enu.wasm (PUC-Lua 5.4 compilado a wasm) YA está
#     comiteado en el repo: compilar `enu` normalmente NO necesita clang ni
#     wasi-sdk, solo el toolchain de Go (etapa `builder`).
#   - Regenerar ese blob (cuando cambia el shim en internal/vmwasm/shim/) SÍ
#     necesita curl/git/tar para que internal/vmwasm/build.sh descargue el
#     wasi-sdk oficial pineado por sha256 y clone Lua pineado por commit
#     (etapa `blob-builder`); build.sh trae su propio clang, no usa el de la
#     distro (por eso el blob es reproducible byte a byte).

ARG GO_VERSION=1.26
ARG DEBIAN_CODENAME=bookworm

# ---------------------------------------------------------------------------
# base-go: solo el toolchain de Go. Base común de "builder" (imagen
# autocontenida) y de los servicios de docker-compose que montan el repo por
# bind mount y compilan en caliente (make build / make shell).
# ---------------------------------------------------------------------------
FROM golang:${GO_VERSION}-${DEBIAN_CODENAME} AS base-go
WORKDIR /src
ENV CGO_ENABLED=0

# ---------------------------------------------------------------------------
# builder: compila el binario enu con el código COPIADO dentro de la imagen
# (sin bind mount) — la vía que usa "runtime" para producir una imagen final
# reproducible con un solo `docker build .`, sin depender de docker-compose.
#
# Multi-arch: la etapa se ancla al BUILDPLATFORM (corre NATIVA en el runner) y
# CROSS-COMPILA al TARGETOS/TARGETARCH que pide buildx (GOOS/GOARCH). Como el
# binario es CGO_ENABLED=0 (estático), cross-compilar es un simple cambio de
# variables — evita emular TODO el `go build` bajo QEMU al publicar linux/arm64
# desde un runner amd64 (release.yml, job "imagen"). En un `docker build`
# normal de una sola plataforma, TARGET* toma por defecto la del host, así que
# `make image` sigue produciendo una imagen del arch local.
# ---------------------------------------------------------------------------
FROM --platform=${BUILDPLATFORM} base-go AS builder
ARG TARGETOS
ARG TARGETARCH

# go.mod/go.sum por separado del resto: cambian mucho menos que el código, así
# que esta capa (la descarga de módulos) casi siempre viene de caché.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w" -o /out/enu .

# ---------------------------------------------------------------------------
# test: go test -race sobre el árbol, excluyendo internal/vmwasm — igual que
# el job "test" de ci.yml: su suite JIT-compila módulos wazero (páginas
# ejecutables mmap) y satura -race con OOM; tiene su propio job (vmblob) sin
# -race a secas + un subconjunto acotado con -race dirigido. -race necesita
# cgo, así que aquí se reactiva (la imagen golang trae gcc); el build de
# producción sigue siendo CGO_ENABLED=0 a propósito. Etapa opcional
# (`docker build --target test`); el flujo habitual es `make test`.
# ---------------------------------------------------------------------------
FROM builder AS test
ENV CGO_ENABLED=1
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go test -race -shuffle=on $(go list ./... | grep -v /internal/vmwasm)

# ---------------------------------------------------------------------------
# blob-builder: toolchain para regenerar internal/vmwasm/enu.wasm a mano
# (internal/vmwasm/build.sh). Solo necesita curl/git/tar/ca-certificates:
# build.sh descarga el wasi-sdk oficial (clang + sysroot propios, pineado por
# sha256 por plataforma) y clona PUC-Lua 5.4.7 pineado por tag Y commit — no
# toca el clang de la distro, así el blob sale reproducible byte a byte desde
# cualquier host (motivo documentado en internal/vmwasm/build.sh).
#
# El código NO se copia aquí: llega por bind mount (servicio `blob` de
# docker-compose), para que build.sh escriba el enu.wasm regenerado
# directamente sobre el repo del host — así es como se comitea en el flujo
# real, no sobre una capa de imagen desechable.
# ---------------------------------------------------------------------------
FROM base-go AS blob-builder
RUN apt-get update && apt-get install -y --no-install-recommends \
      curl git ca-certificates tar \
    && rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------------------------
# runtime: imagen final para EJECUTAR enu, no para desarrollarlo. Debian slim
# en vez de distroless/scratch a propósito: `enu` es un coding harness cuya
# gracia es spawnear procesos (git, bash, herramientas del agente vía
# enu.proc) — necesita un userland real, no solo el binario estático. Las
# herramientas del PROYECTO del usuario (node, python, compiladores…) no van
# aquí: se montan con el workspace o se derivan en una imagen propia.
# ---------------------------------------------------------------------------
FROM debian:${DEBIAN_CODENAME}-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git bash \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/enu /usr/local/bin/enu

WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/enu"]
