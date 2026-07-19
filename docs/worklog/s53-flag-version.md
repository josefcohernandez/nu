---
title: "Flag `--version`/`-V` (Fase 10 — Convenciones CLI)"
type: "sesion"
id: "S53"
phase: 10
status: "cerrada"
---
# S53 — Flag `--version`/`-V` (Fase 10 — Convenciones CLI)

**Qué es.** El afijo de versión más convencional de cualquier binario, que a
`enu` le faltaba (lo notó el operador). Primera entrada de la **Fase 10**
(Convenciones CLI, post-adquisición): superficie del binario (`main.go`), NO
`api.md` (que queda intacto; `enu.version.api` no se mueve). Depende de S45.

**Qué se entregó.**
- `main.go`: campo `version` en `cliOptions`; flags `--version` y `-V` (alias);
  y el corto-circuito tras `flag.Parse()` — `if opts.version { return
  runVersion(os.Stdout) }`, **antes** de construir el runtime. `runVersion(out
  io.Writer) int` es el núcleo testeable (imprime + sale 0, sin config/TTY).
- `doctor.go`: se extrajo `versionString()` como **fuente única** del formato
  `enu <M>.<m>.<p> · API <n> (<os>/<arch>)`; `checkBinaryVersion` (el check
  `binary.version` de `enu doctor`) pasa a llamarla en vez de duplicar el
  `fmt.Sprintf`.
- `main_version_test.go`: formato (regex + constantes reales), `runVersion`
  (salida exacta + exit 0), y **`TestVersionMatchesDoctorCheck`** (el flag y el
  check de doctor comparten fuente: no pueden divergir).

**Decisión de diseño (frontera de ADR-026, resuelta en el alta).** El juez de
filosofía objetó (O1) que la gramática de ADR-026 pone la introspección del
binario del lado subcomando (`enu version`), no flag. Se resolvió por decisión
del operador con el camino (b) del propio juez: `--version`/`-V` es un
**meta-flag de introspección universal y sin efectos**, la categoría de
`--help`/`-h` —que ningún CLI modela como subcomando—, distinta de los **verbos
de acción** de gestión (`init`/`doctor`/`update`/`uninstall`, que tienen efectos
o leen config). La frontera de ADR-026 gobierna esos verbos, no la categoría
meta. La justificación quedó **citada en
[arquitectura.md](../core/arquitectura.md) §5** (spec-first, en el alta). El
diagnóstico RICO de versión sigue en `enu doctor` (`binary.version`);
`--version` es el afijo mínimo.

**Sin fila 🔒.** Wrapper fino que formatea constantes de compilación: no es
lógica propia no trivial, no tiene fallo silencioso ni de borde, no blinda un
`G##`. Test unitario basta (política de tests).

**DoD.** `CGO_ENABLED=0 go build ./...` verde; `gofmt`/`go vet` limpios; `go
test -race -shuffle=on ./` verde, sin regresiones (los demás flags intactos).
**Conformidad con la espec verificada empíricamente** contra el binario real:
`enu --version` y `enu -V` imprimen `enu 0.2.0 · API 5 (linux/amd64)` y salen 0,
**incluso en un binario desnudo con `XDG_CONFIG_HOME` inexistente** (confirma que
no arranca el runtime ni lee config). **Juicio clean-room: sin panel** — por la
política de coste de `/juicio` esto es *glue* (un flag que imprime constantes +
una extracción de helper con test que la blinda); el *diseño* ya pasó por el
juez de filosofía en el alta.

**Cierre.** Fase 10 con su primera (y por ahora única) sesión cerrada; el
puntero vuelve a `—`. La Fase 10 queda como pista viva: futuras convenciones de
CLI que el uso revele entran aquí por `/planificar-sesion`.
