---
title: "Matriz de smoke tests de instalación en sistemas limpios (Fase 9, ADR-025 Fase 1; refina ADR-013)"
type: "sesion"
id: "S48"
phase: 9
status: "cerrada"
---
# S48 — Matriz de smoke tests de instalación en sistemas limpios (Fase 9 — Producto)

**Qué es.** Tercera sesión de la Fase 9. DevOps del operador (ADR-013), no API ni
contrato: un workflow nuevo `.github/workflows/smoke-instalacion.yml` que prueba
la promesa de [ADR-025](../decisions/adr/adr-025-reposicionamiento-motor-de-harnesses.md)
—«corre en una Debian limpia o una máquina air-gapped»— metiendo el **mismo**
binario estático (CGO_ENABLED=0, ADR-001) en **contenedores mínimos sin
toolchain** y en macOS Intel+ARM. El smoke de `ci.yml` ya arranca el binario,
pero en runners de GitHub cargados de utilidades; éste demuestra que no hay
dependencia oculta (glibc, un `.so`) — alpine (musl) o una imagen slim lo
destaparían.

**Qué se entregó.** El workflow con cuatro jobs: `construir` (binario estático
linux/amd64 una vez → artefacto), `linux` (matriz `debian:stable-slim`,
`ubuntu:latest`, `fedora:latest`, `alpine:latest`, cada imagen vía `docker run`),
`macos` (matriz `macos-13` Intel + `macos-14` ARM, build nativo), y `matriz`
(consolida el resultado como dato: JSON + `$GITHUB_STEP_SUMMARY`). Humo en cada
plataforma: `enu -e 'return enu.version.api'` (arranca el runtime headless,
imprime el nivel de API) → `enu --default-config` (escribe el conjunto oficial,
sale 0) → segundo `enu -e` con el oficial activo.

**Decisiones de tubería (refinan la matriz de ADR-013; steps, no API).**
1. **Workflow separado, no en `ci.yml`.** Concern distinto (instalación en limpio)
   y más pesado (6 jobs de plataforma); mantenerlo aparte deja `ci.yml` legible.
2. **Filtro de rutas** (`**.go`, `go.mod/sum`, `install.sh`, embedded Lua, el
   propio workflow): una PR de solo-docs (como S46/S47) no puede romper el
   arranque, así que no gasta la matriz. La PR de esta sesión sí la dispara (toca
   el workflow), de modo que **el workflow se valida a sí mismo**.
3. **`docker run` como step, no `container:` del job.** Las imágenes slim/alpine
   no traen node y las actions de JS (`checkout`, `download-artifact`) fallarían
   dentro del contenedor; ejecutar docker como step en el runner lo esquiva.
4. **Build-once + artefacto.** Todos los contenedores prueban el MISMO binario —la
   portabilidad del mismo artefacto es justo lo que se afirma—. `chmod +x` tras
   descargar: `upload-artifact@v4` no preserva el bit de ejecución.
5. **`enu --version` NO se usa: el flag no existe** (los flags del binario son
   `-e`, `-p`, `--continue`, `--auto-permissions`, `--model`, `--default-config`;
   verificado en `main.go`). La fila de S48 lo mencionaba —inexactitud de
   planificación, no una grieta de API—; se corrigió la fila y el smoke usa
   `enu -e 'return enu.version.api'`, que además es una prueba de liveness más
   fuerte (arranca el runtime y el embed, no solo imprime un string).

**DoD y desviación de protocolo.** No se tocó Go: `go build ./...` verde (rc=0,
confirmado). **No hay BDD/TDD ni test unitario 🔒**: la lógica no es nuestra
(Go), es un workflow; su «test» es correr en verde, que **solo puede validarse en
CI** (necesita runners de GitHub, Docker, los contenedores y macOS). No se lanza
`/juicio`: por su política de coste, un workflow de CI es *glue* de DevOps, no
API/contrato/scheduler. La validación end-to-end la da **la propia PR de esta
sesión** (el filtro de rutas la dispara): si algún leg sale rojo, se arregla en la
rama antes del merge. El puntero avanza a S49 porque el entregable (el workflow)
está hecho; su verificación va en vuelo en la PR.
