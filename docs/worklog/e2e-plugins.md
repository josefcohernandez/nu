---
title: "Suite e2e de los plugins oficiales contra el binario real (paquete e2e/)"
type: "sesion"
id: "E2E"
status: "cerrada"
date: "2026-07-17"
---
# E2E — Suite end-to-end de los 8 plugins oficiales contra el binario real

Sesión post-plan (rama `claude/e2e-plugins`). Hasta hoy ningún test lanzaba
`./enu` como proceso: toda la suite era in-process (harness de `Eval`,
`runWith`) y el único arranque real era el smoke de CI. CP-11 quedó
deliberadamente adaptado a SSE grabado (worklog S43). Esta sesión añade el
paquete `e2e/`: 33 tests que ejercitan el binario compilado, sus flags y exit
codes (0/1/2/3), los ficheros que deja en disco y el TTY real.

**Arquitectura del arnés** (`e2e/harness_test.go` y compañía): `TestMain`
compila `enu` una vez (`CGO_ENABLED=0`, como CI); `Workspace` aísla
HOME/XDG en dirs temporales; `FakeProvider` es un `httptest.Server` que habla
el protocolo Messages/SSE de anthropic — el adaptador REAL del plugin
providers corre contra él vía `base_url`, con cola FIFO programable
(`PushText`/`PushToolUse`) e introspección del wire (`Requests()`); el PTY se
abre a mano sobre `/dev/ptmx` con `golang.org/x/sys` (que pasa a dependencia
directa; **cero dependencias nuevas**), con `Expect`/`ExpectRe` por contenido
sobre la salida ANSI cruda. Escotilla `E2E_NO_PTY=1` para runners sin
`/dev/ptmx` (los de GitHub sí lo tienen; la suite interactiva se ejerce de
verdad en CI).

**Decisiones de CI.** Sin job nuevo: el job `test` ya barre `./e2e/` vía
`go list ./...` con `-race -shuffle=on` en Linux y macOS; se documenta la
inclusión y el coste en el propio YAML.

**Hallazgos de producto** (caracterizados por tests que fallarán pidiendo
restaurar el comportamiento bueno cuando se arreglen; candidatos a `G##` o a
arreglo directo):

1. **repl: el proceso queda colgado al salir.** `Repl:quit()` cierra la UI
   pero no emite `core:shutdown` (chat sí lo emite), y el bucle del driver
   solo sale ante ese evento. Tras ctrl+d o `/q` el proceso del SO no muere.
2. **chat: `/quit` no despierta al driver.** El apagado se emite desde una
   task pero `drive()` está bloqueado en el `select` de chunks sin timeout y
   no lo ve hasta la siguiente pulsación. Posible misma raíz: el
   `.jsonl.lock` no se borra en el apagado por `core:shutdown` (los
   `enu.task.cleanup` no corren en esa vía).
3. **mcp: el auto-connect de `mcp.toml` es inutilizable en headless `-p`.**
   La task del auto-connect es efímera: su `cleanup` cierra la conexión y
   re-registra las tools como stubs "desconectado" antes del turno. Además,
   `env = [...]` (array) de `mcp.toml` no llega al subproceso
   (`enu.proc.spawn` solo interpreta `env` como tabla `{K=V}`).

**Otras notas para el que venga detrás**: los errores no capturados dentro de
una task NO abortan el proceso ni tocan stderr/exit code (ADR-008 — se
verifican volcando el resultado a disco desde la propia task); en macOS
`t.TempDir()` cuelga de `/var` (symlink a `/private/var`) y hay que comparar
contra el cwd resuelto; la aserción de permisos 0600 del lock fija umask; tras
un SIGKILL hay que cosechar el zombie (`cmd.Wait()`) antes de comprobar pids.
