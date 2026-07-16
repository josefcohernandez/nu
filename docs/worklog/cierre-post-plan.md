# Cierre de coherencia post-plan — P21 (thinking adaptativo, pospuesto) + fix del log espurio de `EvalTaskString`

Esto **no es una sesión del plan** (el plan está cerrado, 45/45; no se mueve el puntero ni se
añade fila a la bitácora de `implementacion.md`): es un cierre de coherencia que captura por el
flujo de diseño (CLAUDE.md) dos cabos sueltos que las revisiones destaparon. La API sagrada
(`api.md`) NO se toca; el contrato de providers (`providers.md`) tampoco (P21 es POSPONER, no
resolver).

**1. P21 — desajuste del modelo canónico de `thinking` (POSPUESTO, no resuelto).** La revisión de
S37 (adaptador Anthropic) dejó claro que el modelo canónico de `providers.md` §2.1 congela
`thinking?: { budget?: integer }`, y `adapter_anthropic.lua` (`to_wire`) lo traduce a la forma
extended-thinking *legacy* `{type="enabled", budget_tokens=N}`. Los modelos Opus 4.6+ (incl.
`claude-opus-4-8`) han **retirado** `budget_tokens` y esperan `thinking: {type:"adaptive"}`: un
request con `thinking.budget` sobre ellos daría 400 contra la API real. **No es un defecto del
código** —el adaptador cumple fielmente el contrato congelado y sus tests usan SSE grabado, no
red—, sino una grieta del **modelo canónico** que conviene NO decidir aún: cambiar el modelo de
thinking es transversal (§2.1 + el adaptador + posiblemente el control de razonamiento del agente)
y no urge sin un consumidor real. Queda como **[P21](pospuesto.md)** con su disparador
(conectar el adaptador contra la API real con un Opus 4.6+ y recibir 400, o querer thinking
adaptativo de primera clase). No se tocó `providers.md` ni el adaptador.

**2. Fix del log espurio de `EvalTaskString` (deuda cosmética de S45).** `EvalTaskString` (eval.go)
es el ejecutor headless del binario (un turno de agente, `--continue`...): lanza el chunk como una
task y, cuando termina, **recoge su desenlace** —incluido un error, vía `t.errValue`— y lo devuelve
al llamante (que el CLI mapea a un código de salida). Pero la task se lanzaba con `spawn`, que la
deja con `awaited=false`; en una ruta de error LEGÍTIMA (p. ej. `--continue` sin sesiones, un turno
que lanza `EPROVIDER`), `runTask` (scheduler.go) escribía la línea best-effort *"una task terminó
con error y nadie hizo await"* —ruido, porque el error SÍ se propaga, no se pierde—.

*Arreglo (mínimo y sin carrera).* La línea best-effort avisa de errores que se PIERDEN (fire-and-
forget); cuando el host consume el desenlace, no aplica. Se añade `spawnConsumed` (scheduler.go),
que pre-marca `awaited=true` sobre la `task` **antes** de lanzar su goroutine (`go runTask(t)`), y
`EvalTaskString` la usa en vez de `spawn`. La clave de la sincronización: el flag se fija antes de
la creación de la goroutine, que establece el happens-before; así la lectura de `runTask` (que
evalúa el log bajo el token) ve `awaited=true` **sin data race** y a tiempo. La alternativa de
marcarlo desde el host tras `spawn` tenía doble grieta: (a) data race con esa lectura bajo el token,
y (b) llegaría tarde si `runTask` ya hubiera corrido el log (el host suelta el token antes de
`spawn`, así que `runTask` puede completar antes de que el host vuelva a tocar nada). `spawn` se
refactoriza a un cuerpo común `spawnTask(fn, args, awaited)`; los demás llamantes (`taskSpawn`,
`all`/`race`) siguen pasando `awaited=false`, intactos. El modelo del scheduler, la cancelación y
el watchdog no cambian. La semántica de `t.awaited` se amplía en su comentario: "alguien hizo await
**o el host consume el desenlace síncronamente**".

*Tests.* `TestEvalTaskStringErrorNoSpuriousLog` (scheduler_test.go): una task lanzada por
`EvalTaskString` que lanza un error estructurado (a) sigue devolviéndolo como `*StructuredError`
con el `code` intacto y (b) NO deja la línea "nadie hizo await" en el log. La contraparte
`TestUnhandledTaskErrorLogged` (fire-and-forget vía `enu.task.spawn` por `EvalString`, que sí pasa
por `spawn` con `awaited=false`) sigue verde: el aviso legítimo de error perdido NO se desactiva
—el fix es estrictamente para tasks consumidas por el host—.

**Verificación.** `CGO_ENABLED=0 go build ./...` y `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./...` (incluye package main) verde. El flake
preexistente de S41 `TestMCPToolServerError` es deuda anterior, no de este cambio.

