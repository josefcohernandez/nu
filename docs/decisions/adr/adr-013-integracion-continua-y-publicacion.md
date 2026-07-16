---
title: "Integración continua y publicación de releases"
type: "adr"
id: "ADR-013"
status: "aceptada"
date: "2026-06"
---
# ADR-013 · Integración continua y publicación de releases

**Estado:** Aceptada · 2026-06 · **Refinada por [ADR-021](#adr-021--baseline-completo-y-reproducible-de-lint-antes-de-congelar-v1)** (el modo transitorio `only-new-issues` se retira al quedar la deuda en cero; el resto de la decisión permanece vigente)

**Contexto.** Cerradas las 45 sesiones del [plan de
implementación](implementacion.md), el kernel y las extensiones oficiales son
código real (un binario Go más `internal/runtime`). Hasta ahora la disciplina de
calidad vivía solo en el protocolo de [CLAUDE.md](../CLAUDE.md) —"toda sesión
deja `go build ./...` verde", el inventario 🔒 de tests obligatorios— y se
ejercía a mano en cada sesión. No había integración continua, ni linting
configurado, ni mecanismo para distribuir el binario. Esta decisión registra el
**cómo se valida y se publica `nu`**. Es DevOps del operador: la implementación
(los `.github/workflows/*.yml`) NO es parte de la API sagrada ([api.md](api.md))
ni de los contratos de extensión; este ADR captura las *decisiones*, no los
*steps* del YAML. Encaja donde ya viven ADR-001 (Go, `CGO_ENABLED=0`) y ADR-010
(extensiones embebidas inactivas), que describen la distribución sin haber fijado
su tubería.

**Decisión.**

1. **CI** (`.github/workflows/ci.yml`) en cada PR y push a `main`: formato
   (`gofmt`), `go vet`, módulos limpios (`go mod verify` + `tidy` sin diff),
   `golangci-lint` (conjunto mínimo, ver punto 5), `go build ./...`, build del
   binario estático con las flags de release, un **smoke test headless**
   (`nu -e 'return nu.version.api'`, sin secretos) y `go test -race` sobre una
   **matriz `ubuntu` + `macos`** (las dos plataformas objetivo v1). `-race`
   siempre: el inventario 🔒 incluye tests de concurrencia (S07–S10) que solo
   destapan data races bajo el detector. Sin matriz de versiones de Go: `nu` se
   distribuye como binario, no como librería que terceros compilan; la versión
   que importa es la de `go.mod`, leída con `go-version-file`.

2. **Releases** (`.github/workflows/release.yml`) al pushear un tag `vX.Y.Z`:
   cross-compila a **`linux/amd64`, `linux/arm64`, `darwin/amd64`,
   `darwin/arm64`**, empaqueta un `tar.gz` por plataforma más un `checksums.txt`
   (SHA256), y crea la GitHub Release con notas autogeneradas. **No** se publica
   Windows nativo: está pospuesto ([pospuesto.md](pospuesto.md) P18) y Windows va
   por WSL2, que usa el binario `linux/amd64`; un `.exe` daría falsa señal de
   soporte.

3. **Versionado — estrategia "constantes como fuente de verdad".** La versión
   vive en las constantes de `internal/runtime/enu.go` (`VersionMajor/Minor/Patch`,
   expuestas como `nu.version`). El release **no inyecta** la versión por
   `-ldflags -X`: la **verifica** contra el tag en un job-gate y aborta si
   divergen. El gate lee la versión **ejecutando el runtime**
   (`go run . -e '…nu.version…'`), no con un `grep` del fichero: usa la misma
   lógica de composición (`registerNu`) que el binario real, así que valida
   exactamente lo que verá el usuario, sin fragilidad ante el orden de las
   constantes.

4. **Contrato de build reproducible.** Todos los binarios se compilan con
   `CGO_ENABLED=0` (estático, ADR-001), `-trimpath` (sin rutas de la máquina de
   CI → reproducible) y `-ldflags "-s -w"` (sin tabla de símbolos ni DWARF →
   binario más pequeño; ~12 MB).

5. **Herramientas: lo mínimo.** Los workflows invocan `go` directamente y crean
   la release con una action estándar (`softprops/action-gh-release`); **no** se
   adopta GoReleaser. `golangci-lint` se incluye con un conjunto deliberadamente
   pequeño (`govet`, `errcheck`, `staticcheck`, `ineffassign`, `unused`) y
   `only-new-issues: true`, para no bloquear por deuda preexistente.

**Razonamiento.**
- **Estrategia A vs inyección por `-ldflags`.** Inyectar crearía dos fuentes de
  verdad (la constante Lua y la variable de `main`) que habría que mantener
  sincronizadas, y obligaría a meter una variable mutable en `main` y un flag
  `--version` por una razón puramente de empaquetado. La estrategia elegida tiene
  **una sola fuente de verdad**, no muta código en build (lo publicado es
  bit-a-bit lo del repo, reforzando `-trimpath`) y es coherente con "Lua decide,
  Go ejecuta": `nu.version` ya es la verdad observable; el packaging deriva de
  ella. Las constantes **no** son parte de la superficie sagrada (viven en
  `internal/runtime`, no en `api.md`): el gate las *lee*, no las amplía, así que
  no roza el protocolo de §4.
- **A mano vs GoReleaser.** El alcance es pequeño y estable (4 targets, 1
  binario, sin paquetes nativos ni brew tap ni Docker). GoReleaser metería una
  herramienta externa con su propia versión, config y "magia" —justo lo que la
  [filosofía §6](filosofia.md) ("cero dependency hell") evita en el producto y
  conviene evitar también en su tubería—. El workflow a mano cabe en YAML legible
  y no añade nada que mantener. Si en el futuro se añaden Homebrew tap, paquetes
  nativos o imágenes Docker, se reabre esta elección.

**Consecuencias.**
- El protocolo de [CLAUDE.md](../CLAUDE.md) ("build verde", inventario 🔒) deja
  de depender solo de la diligencia manual: la CI lo exige en cada PR. El
  `tidy`-check materializa "cero dependency hell" como gate automático.
- **Publicar implica subir la versión a mano antes del tag.** El flujo es: editar
  las constantes en `enu.go`, commit, tag `vX.Y.Z`, push. Si el tag no coincide,
  el release falla en el gate con un mensaje accionable y no publica nada. Es una
  fricción deliberada (una verificación, no un automatismo que adivine).
- **macOS en la matriz cuesta más minutos** que Linux. Para un repo de un solo
  desarrollador y bajo volumen de PRs el coste absoluto es pequeño y se acepta a
  cambio de cubrir el segundo OS objetivo; si el gasto importara, la palanca es
  dejar macOS solo en `push: main`. Para *compilar* los binarios darwin del
  release **no** hace falta runner macOS (el cross-compile de Go corre en Linux);
  macOS en CI es solo para *ejecutar* los tests nativamente.
- **Licencia:** resuelta en [ADR-014](#adr-014--licencia-apache-20) (Apache 2.0).
  Los `tar.gz` del release incluyen el binario; el `LICENSE` y el `NOTICE` viven
  en la raíz del repo.
- **Pendiente del dueño del proyecto, fuera de este ADR:** un flag `--version` en
  el CLI sería un nice-to-have de producto (toca la superficie CLI de S45), no un
  requisito de esta tubería; firmar binarios (cosign/GPG), brew tap y Docker
  quedan como mejoras futuras que reabrirían el punto 5.

---
