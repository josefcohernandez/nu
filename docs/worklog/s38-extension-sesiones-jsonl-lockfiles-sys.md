# S38 — Extensión sesiones (JSONL, lockfiles) + enu.sys.pid (G32, APILevel 1→2)

**Hallazgo G32 (corolario de completitud).** El lockfile de `sesiones.md §6` graba la
identidad del escritor `{pid, hostname, started}` con el pid del proceso PROPIO, pero la
API pública no lo exponía: `enu.sys` daba platform/env/setenv/now_ms/mono_ms/hostname y
`enu.proc.alive(pid)` valida pids AJENOS, no el propio. Es el cabo suelto de G17 (que cerró
`fs.write{exclusive}`, `proc.alive` y `sys.hostname` pero olvidó el pid propio). La extensión
oficial `sessions` era inconstruible sin esto → hallazgo, no atajo.

**Resolución (flujo de diseño: docs primero, luego código).** Adición pura a la superficie
sagrada: `enu.sys.pid() -> integer` [W] (no ⏸; consulta local sin IO, como `hostname`/`now_ms`),
wrapper de `os.Getpid()`. Por ser la PRIMERA adición tras el congelado, `enu.version.api` sube
de 1 a 2 (api.md §17/§2; `APILevel` en `enu.go`). G32 RESUELTO en `problemas.md`; api.md §7
y §16 actualizados; `sesiones.md §6` usa `enu.sys.pid()`. Es una adición estricta: no cambia
ninguna firma existente (ADR-003).

**Decisiones de la extensión `sessions`.**
- **Id de sesión**: timestamp ms (hex de ancho fijo, ordena lexicográficamente = temporal)
  + sufijo aleatorio. El PRNG se siembra UNA vez con `now_ms` + `pid` (sin semilla, gopher-lua
  daría la misma secuencia entre arranques → dos procesos en el mismo ms colisionarían en el
  sufijo; el pid los separa).
- **Lockfile** (§6, G5): `<sesión>.jsonl.lock` con `fs.write{exclusive}`; contenido
  `{pid=enu.sys.pid(), hostname, started}`. Conflicto resuelto por inspección: mismo hostname +
  pid muerto (`proc.alive`=false) → huérfano, reclamado en silencio; pid vivo → ESESSION busy;
  otro hostname → ESESSION foreign (no verificable a distancia). Liberado por `enu.task.cleanup`.
- **read_only** no toma lock (varios lectores concurrentes). Código de error `ESESSION` (forma
  ADR-009, acuñado por la extensión).
- **replay** descarta la última línea si está truncada (crash a mitad de append): JSONL es
  append-only y `fs.append` escribe una línea completa, así que solo la última puede partirse.

**Nota de proceso.** El subagente de implementación dejó el código y los docs escritos y la
suite verde, pero se detuvo antes del `git commit`. Se verificó `go build`/`go vet`/`gofmt` y
`go test -race -timeout 120s -count=2 ./internal/...` (todo verde, APILevel 2) y se commiteó/
pusheó tras completar la fila de bitácora de S38, esta entrada y la semilla del PRNG.
