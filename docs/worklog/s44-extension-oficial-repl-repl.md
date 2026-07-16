# S44 — Extensión oficial `repl` (REPL de Lua sobre la API pública, activable solo, G21) (arquitectura §Distribución)

Noveno eslabón de la Fase 8: **Lua puro sobre la API congelada** (ADR-003, sin privilegio de
kernel — el core NO sabe lo que es un REPL). Plugin embebido nuevo
`internal/runtime/embedded/repl/` (`plugin.toml` name="repl" **SIN `requires`** —el repl se
activa SOLO, G21, sin arrastrar el harness—; `init.lua` que cablea + arranca en TTY; módulo
`lua/repl/init.lua`), INACTIVO por defecto (ADR-010), activable por `enu.toml`
`plugins.enabled=["repl"]`, `source="builtin"`.

## CÓMO EVALÚA Lua arbitrario (el punto delicado de S44; corolario de completitud)

La pregunta central: un REPL NECESITA compilar y ejecutar código del usuario, y el baseline del
sandbox (§1.2, S01) deshabilitó IO bloqueante. ¿Basta la API pública o falta una primitiva
(`enu.eval`/reabrir `load`) → hallazgo? **Se investigó a fondo ANTES de decidir** (con probes
temporales `loadstring`/`load`/`dofile`/`loadfile` contra el runtime real, luego borrados):

- **`sandbox.go` (S01) retira `dofile`/`loadfile`** (cargan FICHEROS de disco saltándose el
  loader) e `io`/`os.execute`… **pero NO `load`/`loadstring`**: `OpenBase` de gopher-lua los
  define y el sandbox no los toca. Compilan un string EN MEMORIA, sin IO bloqueante, así que NO
  violan la razón declarada del baseline ("todo IO debe pasar por las primitivas async del
  core"). Quedan disponibles para el Lua de usuario (verificado: `type(load)`/`type(loadstring)`
  == "function"; `dofile`/`loadfile` == nil).
- **Conclusión: la API pública BASTA exacta para un REPL. NO hizo falta ninguna primitiva nueva
  (ni `enu.eval` ni reabrir `load` controlado); APILevel sigue en 2, api.md INTACTO.** Sin
  hallazgo `G##`. El sandbox ya había dejado la puerta justa abierta: el REPL es expresable con
  lo que hay (corolario de completitud satisfecho, como en S36/S39–S43). El protocolo de "párate
  y repórtalo" (estilo `enu.sys.pid` en S38) NO se disparó porque la investigación demostró que el
  patrón ya era construible.

## EL MODELO del REPL (la lógica probada, headless)

`repl.eval(src) -> { ok, values?, n, display, error?, incomplete? }` es el núcleo PURO:

1. **Truco return<expr> / sentencia** (`compile`): primero `loadstring("return "..src)` —así una
   EXPRESIÓN suelta (`1+1`, `enu.version.api`) se evalúa sin que el usuario escriba `return`, el
   truco clásico del REPL de Lua—; si no compila, `loadstring(src)` tal cual (una SENTENCIA:
   `x=5`, `for...`). Ambos disponibles porque `loadstring` lo está.
2. **Ejecución bajo `pcall`**: el error del usuario se CAPTURA (no tumba el repl — una frontera
   con `pcall`, en espíritu de ADR-008); `ok=false` con el error y su `display`.
3. **Formato**: los retornos por `tostring` (un string se entrecomilla con `%q` para
   distinguirlo de un identificador), unidos por tab; los `nil` intercalados se preservan con un
   contador `n` explícito (no `#values`). Cero retornos → `display=""` (una sentencia no imprime).
4. **Error estructurado del core** (§1.4): se muestra `code: message` (la forma reconocible,
   p. ej. `ENOENT: no existe`); el puente NO lo degrada (invariante de S02): el repl lo recibe
   entero (`r.error.code`).
5. **Multilínea / incompletitud**: gopher-lua marca la entrada INCOMPLETA (bloque/función/string
   sin cerrar) con **`at EOF`** en el mensaje (frente a un error real, que trae
   `line:N(column:M) near '<token>'`). `repl.eval` lo distingue: una entrada incompleta NO es un
   error (`incomplete=true`), es la señal de "dame otra línea". Es la base del modo multilínea.

`repl.eval_in_task(src, cb)` evalúa la misma lógica DENTRO de una task y entrega el resultado a
`cb`: así una línea que llama a una función **⏸** del core (`enu.fs.read`, `enu.http.request`,
`enu.search.grep`…) funciona —solo corren en una task, §1.3—. Es el patrón de `chat:submit`
(S43) con `Session:send`. La mayoría de la API (no-⏸: `enu.version`, `enu.text.*`, `enu.json.*`) no
lo necesita y va por `repl.eval` directo.

## DRIVER TTY vs LÓGICA PROBADA

- **LÓGICA PROBADA (headless, sin TTY)**: `repl.eval` (expresión/sentencia/llamada-API/error
  estructurado y plano/sintaxis/incompletitud), `repl.eval_in_task` (⏸ vía task, leyendo un
  fichero real + un ⏸ que lanza), `repl.banner`, la activación SOLA (G21). Es el grueso de S44 y
  donde vive el riesgo (la máquina de compilar/formatear/detectar incompletitud).
- **DRIVER TTY (`repl.start`, necesita `enu.ui`, G20)**: monta una `toolkit.app` (S42) con un
  `vbox` de un `toolkit.text` (transcript: banner + ecos + resultados, flex) y una fila de
  entrada (`hbox` de un label-prompt `>`/`..` + un `toolkit.input`). Enter evalúa (keymap global;
  el input deja pasar enter "pelado", como el editor del chat), ctrl+d sale. El toolkit es
  dependencia **BLANDA** (no en `requires`: el repl se activa solo): se `require` perezosamente
  bajo `pcall` dentro de `start`; sin él, `start` da EINVAL accionable, pero `repl.eval` SOLO
  sigue funcionando. En headless, `start` da EINVAL accionable (nombra TTY y `repl.eval`). Lo
  AUTOMATIZABLE del driver se prueba con `WithForceUI(true)`+`WithUISize` (banner pintado,
  input→`_submit`→eval→resultado en la rejilla del compositor, recuperación tras error, prompt de
  continuación multilínea); la elección con el TECLADO real (ver el efecto en un terminal) es
  manual con TTY (como el CP-7 de la pantalla desnuda).

La **pantalla de runtime desnudo** (S33) ya ofrecía "activar extensiones sueltas (p. ej. solo
repl)"; ahora `repl` existe en el catálogo embebido, así que esa acción lo activa de verdad. El
catálogo creció (los tests de `bare_screen` comprueban por SUBSTRING `example`, no por lista
exacta: no se rompen).

## NO amplía api.md

Corolario de completitud satisfecho: `load`/`loadstring` del baseline §1.2 (la EVALUACIÓN) +
`enu.task.spawn` §3 (eval_in_task) + `enu.version`/`enu.has` §2 + el módulo `toolkit` (la UI) +
`enu.ui.keymap`/`enu.events.on` §9.3/§4 bastaron EXACTOS para un REPL de Lua interactivo activable
solo; APILevel sigue en **2**; ni una función pública del core de más. Errores: reusa `EINVAL`
del core (no acuña código propio; no le hizo falta). **Sin hallazgos `G##`.**

## Desviaciones (menores; ninguna toca el core)

1. **El toolkit es dependencia BLANDA, no `requires`.** Si fuera `requires=["toolkit"]`, activar
   `repl` arrastraría el toolkit SIEMPRE, rompiendo "activable SOLO" (G21). En su lugar, `start`
   lo `require` perezosamente bajo `pcall`: el repl-solo evalúa por `repl.eval`; la UI es el plus
   que pide el toolkit (activable `plugins.enabled=["toolkit","repl"]`). Decisión de la extensión.
2. **El formato de salida** (un string con `%q`, retornos unidos por tab, `code: message` para el
   error estructurado, `display=""` para una sentencia) lo fija esta extensión: es vocabulario de
   producto (cómo IMPRIME un REPL), no del core. Un serializador fiel es `enu.json.encode`, que el
   usuario llama si quiere.
3. **Comandos mínimos del repl** (`/q`/`/quit`/`/exit` para salir, solo en la primera línea de un
   bloque): conveniencia de la UI, no contrato. ctrl+d es el equivalente por teclado.
4. **El sondeo de incompletitud en el driver** (`Repl:_compile_probe`) compila en seco (sin
   ejecutar) para decidir acumular vs. evaluar; `repl.eval` (que sí ejecuta) hace su propio
   sondeo. La doble compilación es barata (memoria) y evita ejecutar efectos colaterales al
   sondear si el bloque está completo.

## Lo que reusará S45 (CLI, el último eslabón de la Fase 8)

S45 = **superficie CLI** (cuestión abierta nº5 de arquitectura): flags de `enu -e`,
`--auto-permissions`, headless, códigos de salida, azúcar `--continue` sobre
`agent.session{resume}` (G18). Lo que hereda: `repl.eval` como evaluador de una línea sin TTY
(el `enu -e` ya evalúa por `EvalString`/Go; el repl es su contraparte interactiva). El patrón de
"activable solo en TTY vs. lógica headless" (G20/G21) que el CLI replica para `enu -e`. Con S45 se
CIERRA la Fase 8 (todas las extensiones oficiales).
