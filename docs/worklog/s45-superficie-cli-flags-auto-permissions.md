---
title: "Superficie CLI (flags, --auto-permissions, --continue/G18, códigos de salida); cierra la Fase 8 y el plan (arquitectura nº5)"
type: "sesion"
id: "S45"
phase: 8
status: "cerrada"
---
# S45 — Superficie CLI (flags, --auto-permissions, --continue/G18, códigos de salida); cierra la Fase 8 y el plan (arquitectura nº5)

**Qué es.** El ÚLTIMO eslabón del plan: la superficie de línea de comandos del binario `enu`.
Cierra la cuestión abierta nº5 de [arquitectura.md](../core/arquitectura.md), la Fase 8 y el plan
entero (45/45). Vive en `main.go` (el binario), **NO en la API sagrada `enu.*`** (api.md): es la
interfaz del ejecutable, no superficie Lua. El core sigue sin saber lo que es un agente
(ADR-003): el CLI orquesta las extensiones `agent`/`sessions` por la API pública, exactamente
como podría hacerlo un `init.lua` de usuario.

**Decisión 1 — cómo invoca el binario al agente (Lua), respetando ADR-003.** El turno del
agente es ⏸ (`Session:send`), así que NO puede correr en el chunk principal de `EvalString`
(que corre en el estado principal, donde las ⏸ lanzan EINVAL). Opciones consideradas: (a) un
método Go `RunAgentTurn` en `package runtime` —rechazado: mete vocabulario de PRODUCTO (agente)
en el kernel—; (b) el patrón de dos fases de CP-10 (`EvalString` que `enu.task.spawn` + globals +
segundo `EvalString` para leer el resultado) —funciona pero el mapeo a códigos de salida es
torpe—; (c) **un método Go GENERAL `Runtime.EvalTaskString(code) -> ([]string, error)`** que
corre un chunk Lua **como task** a término y devuelve sus retornos / `*StructuredError`.
**Elegida (c):** es una capacidad de kernel legítima y agnóstica ("ejecuta un chunk Lua
suspendible a término"), la contraparte ⏸ de `EvalString`; el DRIVER del agente (que sí sabe de
`agent`/`sessions`) es una const Lua **en main.go** (el binario, no el kernel). Así el core no
acuña vocabulario de producto y el CLI orquesta como un usuario. `EvalTaskString`/`SetStringGlobal`
son interfaz Go del binario (como `EvalString`/`Boot`/`RenderBareScreen`), **fuera de api.md**:
api.md queda INTACTO, APILevel sigue en 2, sin hallazgo `G##` (corolario satisfecho).

**Decisión 2 — pasar los argumentos del CLI al driver SIN inyección.** El prompt puede traer
comillas/saltos de línea; interpolarlo en el código del driver sería una inyección. Se añadió
`Runtime.SetStringGlobal(name, value)` (fija un global Lua string desde Go, bajo el token); el
binario fija `NU_CLI_PROMPT`/`NU_CLI_MODEL`/`NU_CLI_CONTINUE`/`NU_CLI_AUTOPERM` y el driver los
lee. Booleanos van como "1"/"" (el CLI solo necesita strings). Cero escaping frágil.

**Decisión 3 — la convención de códigos de salida (arquitectura nº5 / agente.md §5).**
`0` éxito; `1` error de ejecución (chunk/turno/provider lanzaron, o `Boot` falló); `2` uso
inválido (flags/argumentos: modo agente sin prompt, o sin args y sin TTY); `3` permiso denegado
en headless. El **3 es deliberadamente DISTINTO del 1**: en headless una tool sensible denegada
NO rompe el turno (el agente devuelve el error al modelo como tool_result, que puede recuperarse,
agente.md §5), así que el turno "termina bien"; pero para un script/CI es una señal accionable
distinta de un fallo de ejecución —"el modelo no pudo actuar por permisos; añade `allow` o
`--auto-permissions`"—. El mensaje en stderr lo nombra.

**Decisión 4 — `--continue` (G18), azúcar de reanudación.** G18 dejó el `--continue` fuera de
los contratos por pertenecer a la superficie CLI; aquí se decide: reanuda la sesión MÁS RECIENTE
del proyecto (cwd). "Más reciente" = `sessions.list(cwd)` ordenando los ids descendente (ordenan
lexicográfico = temporal, sesiones.md §2/§7) y tomando el primero, que se pasa como `resume` a
`agent.session{...}`. `--continue` sin sesiones previas → EAGENT accionable (código 1), no una
sesión nueva muda. El proyecto es `enu.fs.cwd()` (donde se lanzó `enu`).

**Decisión 5 — flags.** `-e '<lua>'` (de S01, consolidado); `-p '<prompt>'` (turno de agente
headless; imprime el texto final del asistente a stdout); `--auto-permissions` (→
`permissions.mode = "auto"`); `--model 'prov/modelo'` (→ `opts.model`, anula `agent.toml`);
`--continue`/`-c`. El modo agente exige un prompt no vacío (sin él, los modificadores no tienen
turno que ejecutar → código 2). Separar el parseo (`run`) de la ejecución (`runWith(rt, opts)`)
hace el CLI testeable sin lanzar el proceso.

**HALLAZGO DE IMPLEMENTACIÓN (no `G##`; ningún cambio de docs salvo este registro).** La
detección de "permiso denegado" en el driver se hace observando el evento `agent:tool.end`
(agente.md §4) y mirando su texto de error (acoplamiento estable, como CP-10: el wording lo fija
la extensión `agent` congelada en S39). **Al implementarlo se descubrió que un upvalue ESCALAR
mutado desde el handler del evento NO se propagaba** de vuelta al thread del driver: la detección
fallaba intermitentemente con `local denied = false; ... denied = true`. **Causa:** los handlers
de `enu.events` corren sobre un thread EFÍMERO (ADR-008/S10, `callEventHandler`); mutar el upvalue
escalar desde ese thread no se ve fiablemente desde el thread del driver, pero mutar el CONTENIDO
de una TABLA capturada SÍ (la tabla es una referencia compartida). **Solución (sin tocar el
core):** el driver guarda el estado en una tabla (`local state = { denied = false }`;
`state.denied = true`) — el mismo patrón que ya usan el agente y el chat para el estado
compartido entre handlers. No es un `G##` (no falta API ni hay grieta de contrato: es una
disciplina de uso de los upvalues con handlers en thread efímero, que las extensiones ya seguían).

**Refactor de soporte.** `structuredFromError` (errors.go) ahora delega en un nuevo
`structuredFromValue(LValue)`, que recupera el error estructurado (§1.4) desde el `errValue`
crudo (un `LValue`) que una task guarda al lanzar — lo necesita `EvalTaskString`, donde el error
no llega como `error` de Go sino como el valor Lua. Invariante 🔒 de S02 intacto (no traga ni
reescribe el code).

**Tests** (`main_test.go`, package main, HERMÉTICO): `runWith` sobre un Runtime con dirs de
prueba y headless. `enu -e` éxito (0) + error estructurado (1, stderr con code, nada en stdout) +
sintaxis (1); agente con `--auto-permissions` (write_file CONCEDIDO, 0, fichero creado) vs sin él
(DENEGADO, 3, stderr nombra `--auto-permissions`/`allow`, fichero NO creado); read_file (solo
lectura) sin auto-permissions → 0; `--continue` reanuda la MÁS RECIENTE (3 sesiones montadas; el
JSONL de la última crece y contiene el prompt nuevo, los viejos no se tocan); `--continue` sin
sesiones → 1; modo agente sin prompt → 2. Smoke del binario compilado: `-e` (0/1), uso sin args
sin TTY (2), `-p` sin extensión activa (EAGENT accionable, 1).

**Verificación.** `CGO_ENABLED=0 go build ./...` y `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde (~69 s) y la raíz
(`.`) verde bajo `-race -count=2`; no regresiona S01–S44.

**CIERRE.** Con S45 la Fase 8 y el plan entero quedan COMPLETOS (45/45 sesiones, las 8 fases
marcadas). Pendiente solo lo MANUAL no ejecutable en CI headless: CP-7 (teclado con TTY real) y
CP-11 contra un provider real (red/credenciales).
