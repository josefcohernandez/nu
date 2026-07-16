# S17 — `enu.sys` (api.md §7)

Entorno y reloj. Wrappers finos sobre la stdlib (`platform`/`now_ms`/`mono_ms`/
`hostname`); la única lógica propia es el **overlay de `setenv`** y su precedencia
al lanzar subprocesos. Ninguna función ⏸ (son consultas/registros inmediatos);
todas [W] (§16, hoy en el estado principal: los workers son S34). Sin hallazgos:
§7 y la `procOpts` que dejó S16 bastaron; APILevel sigue en 1 (§7 ya estaba).

**`setenv` NO muta el entorno del proceso `enu` actual (decisión central, §7).**
Nada de `os.Setenv`: mutar el entorno global del proceso es un efecto compartido
que (a) rompería el aislamiento por tarea (ADR-008) —se vería desde TODO el
código, no solo desde quien lo pidió— y (b) contradiría el contrato ("afecta solo
a subprocesos futuros"). En su lugar, `setenv` escribe en un **overlay** del
Runtime (`sysState.envOver map[string]string`) que `enu.proc` aplica al construir
el entorno del hijo. El criterio de hecho ("`setenv` se ve en un subproceso
lanzado después, no en el actual") se cumple por construcción: el `enu` actual
nunca cambia su entorno; lo único que ve la variable es el hijo.

**Candado, no token, para el overlay.** `setenv` escribe el mapa en el estado
principal bajo el token, pero lo **leen las goroutines de fondo de `enu.proc`**
(que montan el entorno del hijo SIN el token, fuera del puente ⏸). Por eso el
overlay lleva su propio `sync.Mutex` —es lo que evita la data race que `-race`
cazaría—, no el token. Para no compartir el mapa vivo con esas goroutines,
`envOverlay()` devuelve una **copia** (coste despreciable: pocas entradas).

**Foto del overlay tomada en la entrada de `run`/`spawn`.** Ambos hacen
`opts.envOver = rt.sys.envOverlay()` justo tras `parseProcArgs`, en el estado
principal bajo el token. Así se fija de forma determinista qué `setenv` ve cada
subproceso: los que ocurrieron ANTES de la llamada (no los de después). Como la
llamada Lua happens-before la goroutine de fondo, cualquier `setenv` previo es
visible.

**Precedencia del entorno del hijo (la integración S16↔S17, `mergedEnv`).** De
menor a mayor: **entorno heredado del SO < overlay de `setenv` < `opts.env`
explícito de la llamada**. Razonamiento:

- El overlay **pisa lo heredado**: esa es la razón de ser de `setenv` (cambiar lo
  que el hijo ve respecto al entorno del proceso).
- `opts.env` explícito es **control total por llamada** (§6, ya decidido en S16):
  lo más local manda. Quien pasa `env` en ESA invocación decide esas claves por
  encima del overlay —p. ej. para AISLAR un subproceso de un `setenv` previo—. Y,
  coherente con S16, `opts.env` **reemplaza** el entorno heredado (parte de
  `opts.env`, no de `os.Environ`); con `opts.env` presente el overlay **no se
  aplica** (la capa explícita es la ganadora completa).
- Alternativa descartada: layar `opts.env` *encima* de (SO + overlay) en vez de
  reemplazar. Se descartó por coherencia con S16 (`opts.env` ya significaba
  "control total / reemplaza heredado"); cambiarlo habría sido una regresión
  silenciosa de esa semántica.

Detalle de implementación: `opts.env != nil` (aun siendo `[]string{}`) marca
"explícito" —`parseProcArgs` pone `[]string{}` no-nil cuando hay tabla `env`—. Sin
overlay ni `opts.env`, `mergedEnv` devuelve `nil` (el caso común: `exec.Cmd`
hereda `os.Environ()` tal cual, sin coste de copia). `splitEnv` parte "K=V" por
el PRIMER `=` (un valor puede contener `=`). Se mantiene **una sola entrada por
clave** (índice clave→posición) para un entorno limpio y determinista.

**`platform`** devuelve `runtime.GOOS` crudo: para los SO soportados es
"linux"/"darwin"/"windows" (lo que enumera §7); en cualquier otro, el literal de
`GOOS` —más honesto que inventar un valor del enum—. **`now_ms`** es el reloj de
pared (`time.Now().UnixMilli`, puede saltar hacia atrás). **`mono_ms`** es
monotónico desde `monoOrigin` (fijado al cargar el paquete, `time.Since`): origen
arbitrario, solo las diferencias entre lecturas son duración fiable.
**`hostname`** es `os.Hostname`; un fallo del SO (raro) → `EIO` en vez de
inventar un nombre.

**Tests (`sys_test.go`).** El overlay y su precedencia (lógica propia) llevan
test Go: `mergedEnv` table-driven (overlay pisa el SO / añade clave nueva
conservando lo heredado / `opts.env` gana al overlay / `opts.env` reemplaza lo
heredado / `opts.env={}` → entorno vacío) y `splitEnv`. El **criterio de hecho**
va de extremo a extremo por el puente ⏸ real: una task hace
`setenv("NU_TEST_X","42")` + `proc.run(["printenv","NU_TEST_X"])` y un future
publica el desenlace; otra task lo espera y assert-a stdout=="42\n"/code==0
(`printenv` es coreutils y se invoca SIN shell, así que también ejercita la
ausencia de shell de S16). El "no en el actual" se comprueba en Go con
`os.LookupEnv` (sigue vacío tras el snippet). El resto (`platform`/`env`/
`now_ms`/`mono_ms` no-decreciente/`hostname`/uso desde una task) con snippet Lua,
como pide la política para glue sobre la stdlib. `CGO_ENABLED=1 go test -race
-timeout 120s -count=2 ./internal/...` verde.
