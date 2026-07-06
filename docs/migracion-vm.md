# Plan de migración de la VM: de gopher-lua a PUC-Lua sobre wazero

Ejecuta [ADR-019](adr.md#adr-019--la-vm-objetivo-del-kernel-es-puc-lua-sobre-wazero-gopher-lua-queda-en-mantenimiento).
Evidencia técnica y números: [spike/lua-wasm/INFORME.md](../spike/lua-wasm/INFORME.md)
(el spike es la semilla de las sesiones M02-M03 y el detector anti-caducidad).
Rama de la migración: **`claude/migracion-vm-wasm`**.

> **▶ Próxima sesión: `M07`** (y completar la superficie `nu.task`: all/race/
> every/defer/future/cleanup extienden el bucle de M06). · Bitácora abajo.
> Censo de la frontera (M01, cerrado): [migracion-vm-censo.md](migracion-vm-censo.md).

---

## 0. Protocolo de sesión (OBLIGATORIO para el agente ejecutor)

Mismo protocolo que [implementacion.md](implementacion.md), adaptado. Si
arrancas sin más contexto que el repo: lee primero `CLAUDE.md`, después este
documento entero, después ADR-019 y el INFORME del spike. Luego:

1. **Antes de tocar nada**: lee el puntero ▶ (arriba) y la última fila de la
   bitácora (abajo). Implementa **solo** esa sesión. Respeta el grafo de
   dependencias (§4): no abras una sesión cuyas dependencias no estén cerradas.
2. **La API sagrada no se toca.** Esta migración cambia el *motor*, jamás la
   superficie `nu.*` ([api.md](api.md)): mismas firmas, mismas semánticas,
   mismos códigos de error. La única excepción, ya decidida en ADR-019 pieza 2,
   es el baseline de lenguaje (api.md §1.2: "Lua 5.1" → "Lua 5.4"), que se
   aplica en M14 y solo en M14. Si una sesión descubre que algo NO puede
   implementarse con la misma semántica observable, **párate**: es una grieta
   G## que se registra en [problemas.md](problemas.md) y se resuelve en los
   documentos antes de seguir.
3. **La suite dual es la ley** (§3). Toda sesión de las fases B-C termina con
   `go build ./...` verde y la parte correspondiente de la suite verde **en los
   dos backends**. Una sesión que deja rojo el backend gopher es una regresión:
   no se avanza el puntero.
4. **Al terminar, en el mismo commit que la feature**: avanza el puntero ▶,
   añade fila a la bitácora, y si cierras fase ejecuta su checkpoint 🔎 (si
   falla, el puntero no se mueve). Commit en español citando la sesión
   (`M05: ...`). Push a la rama de la migración; nunca a otra sin permiso.
5. **Los hitos de veto (§5) son vinculantes.** Si un veto dispara, se para, se
   registra el resultado en la bitácora y en ADR-019 (nueva entrada ADR si
   supone cambiar la decisión), y se consulta al humano. No se "aprieta hasta
   que pase".

## 1. Objetivo y forma de la migración: patrón estrangulador

No es un big-bang: el backend wasm se construye **en paralelo** al actual,
detrás de un selector, y la suite existente se ejecuta contra ambos. La
conmutación (M16) y la retirada (M17) solo llegan cuando la paridad es total y
los vetos de rendimiento pasan.

```
Runtime ──> backend gopher-lua  (el actual; intacto hasta M17)
       └──> backend wasm        (nuevo: wazero + lua.wasm + puente)
                 selector: nu.toml [vm] backend = "gopher"|"wasm"
                 y para tests: variable de entorno NU_VM (M04)
```

Piezas heredadas del spike (`spike/lua-wasm/`), que NO se copian a ciegas sino
que se promueven con calidad de kernel: el build de `lua.wasm` (PUC-Lua 5.4.7
sin parches + `spike_unwind.h`), el trampolín de desenrollado sobre
`Snapshot/Restore`, y las lecciones documentadas (la no-reentrancia de
`api.Function`, el coste del yield, el stub de `setjmp.h`).

## 2. Decisiones ya tomadas (no re-litigar) y decisiones de entrada

**Cerradas por ADR-019:** dirección (PUC-Lua sobre wazero), baseline Lua 5.4,
puente ⏸ por corrutinas nativas, gopher-lua en mantenimiento hasta M17.

**Decisiones que el plan fija ahora** (anotadas aquí; el ejecutor las sigue
salvo contraorden del humano):

| # | Decisión | Racional |
|---|---|---|
| DM1 | El blob `lua.wasm` **se comitea** en el repo (`internal/vmwasm/lua.wasm`) junto a su `build.sh` reproducible y una nota de licencia (MIT de PUC-Lua, compatible Apache-2.0/ADR-013); un job de CI reconstruye y compara hash para que blob y fuentes no deriven | CI y contribuidores no necesitan clang/wasi-libc; la reproducibilidad queda blindada por el job |
| DM2 | Selector: `nu.toml [vm] backend` + env `NU_VM` (tests). Default `gopher` hasta M16 | Estrangulador clásico; el flip es un cambio de default, no un merge gigante |
| DM3 | Los valores cruzan la frontera wasm como **copias JSON-ables + handles enteros** para userdata (Task, Proc, Region, Block...) con despacho de métodos vía host functions | Coincide con el modelo mental que api.md ya impone (handles opacos; workers ya cruzan solo JSON-ables) |
| DM4 | El watchdog wasm usa la **interrupción por época** de wazero (presupuesto por slice) en vez del mecanismo actual | Es el equivalente natural y más barato; su semántica observable (EBUDGET, no capturable) debe ser idéntica — 🔒 |
| DM5 | `require`/loader: se implementa sobre el estado wasm el MISMO cargador curado de hoy (rutas de plugins, unicidad de nombre); la lib `package` de PUC no se abre | El loader es del kernel, no de la stdlib; idéntico a la decisión del sandbox actual |

## 3. Política de tests: la suite dual es la columna vertebral

- **El mecanismo (se construye en M04):** el arnés de tests de
  `internal/runtime` gana un selector por env (`NU_VM=wasm go test ./...`).
  El CI corre la suite completa con ambos valores desde M04 (la de wasm,
  limitada a lo ya migrado: se mantiene una lista de skip explícita que cada
  sesión RECORTA — nunca amplía).
- **Inventario 🔒 heredado**: todos los tests que hoy blindan semántica de VM
  son de paso obligatorio en wasm — G31 (scheduler), G41 (upvalues vivos:
  `TestG41*` deben pasar en wasm *sin* el blindaje, porque el bug no existe en
  PUC), watchdog (EBUDGET), cancelación (aborto no capturable a través de
  `pcall`), workers (caps, colas acotadas, exclusión recv/on_message), errores
  estructurados (§1.4), y los checkpoints CP existentes.
- **Nuevos 🔒 de esta migración**: el puente (yield/resume con valores, yield a
  través de pcall — la prueba de que ADR-011 muere), el marshaling (UTF-8
  estricto G11, `nu.json.NULL`, handles inválidos → error accionable), y la
  paridad de códigos de error módulo a módulo.
- Los benchmarks del spike se promueven a `internal/vmwasm/bench_test.go` y se
  corren en los checkpoints (no en cada push).

## 4. Las sesiones

### Fase A — Cimientos (la "interfaz de VM" de ADR-019 fase a)

| Sesión | Contenido | Depende de |
|---|---|---|
| **M01** | **Censo de la frontera VM.** Inventario mecánico (script `tools/censo-vm.sh` + tabla en este doc o anexo) de cada símbolo de gopher-lua usado por fichero del kernel, clasificado en: valores, registro de funciones, threads/corrutinas, errores, userdata, otros. Es el mapa de M05-M13; lo que no salga aquí no existe. Sin código de producción | — |
| **M02** | **`internal/vmwasm`: el blob productivo.** Promover el build del spike: `build.sh` reproducible + `lua.wasm` comiteado (DM1) + `go:embed` + shim C consolidado (buffer, exports, unwind) + nota de licencia + job de CI de hash. El shim gana lo que el spike no tenía: `require` hook (DM5), tabla de handles (DM3), multi-instancia limpia | — |
| **M03** | **El puente de desenrollado, calidad kernel.** Trampolín Snapshot/Restore endurecido: LIFO auditado, `__stack_pointer`, funciones frescas (no-reentrancia), detección de traps reales vs throws, y el gate-test del spike promovido a test 🔒. Multi-instancia: N módulos wasm conviviendo (preparación de workers M12) | M02 |

### Fase B — El runtime paralelo (estrangulador)

| Sesión | Contenido | Depende de |
|---|---|---|
| **M04** | **Backend seleccionable + boot desnudo.** `Runtime` acepta backend (DM2); el estado wasm arranca sin plugins con el sandbox curado; el arnés de tests gana `NU_VM` y la lista de skips; CI dual desde aquí | M03 |
| **M05** | **Marshaling + registro de host functions.** La infra genérica para exponer primitivas Go al estado wasm: copias JSON-ables, strings sin re-codificar (G11), errores estructurados `{code,message,detail}` cruzando fielmente (§1.4), tabla de handles con ciclo de vida (DM3). Equivalente wasm de `registerNu`. 🔒 exhaustivo: es la pieza de la que cuelga todo | M04 |
| **M06** | **ADR-020 (diseño del puente definitivo) + scheduler por corrutinas.** PRIMERO el ADR: task = corrutina Lua nativa; ⏸ = yield con petición; el loop Go resume con el resultado; presupuesto del coste de yield con las vías del INFORME §4.1 evaluadas y una elegida. DESPUÉS el código: `nu.task` completo (spawn/sleep/all G27/race/every/defer/future/await/cleanup) sobre corrutinas. ADR-020 **reemplaza** a ADR-011 (se marca allí). 🔒 paridad scheduler_test/allrace/future/timers | M05 |
| **M07** | **Cancelación y watchdog.** Aborto no capturable a través de `pcall` (ahora SIN wrapper especial: el throw de PUC + un marcador propio bastan — diseño en ADR-020) y watchdog por época de wazero (DM4). 🔒 paridad cancel/watchdog (S08/S09) | M06 |
| **M08** | **Bus de eventos, input y lo síncrono.** `nu.events` (G10: encolado en anchura), handlers síncronos, timers. 🔒 paridad events_test — incluida la semántica de G41 SIN blindaje | M06 |
| **M09** | **Primitivas de IO y datos.** fs/proc/sys/http/ws/search/text/re/json/toml/yaml como host functions sobre la infra de M05 (mecánico; en bloque). 🔒 paridad de sus tests módulo a módulo; la lista de skips baja en masa aquí | M05 |
| **M10** | **Userdata como handles.** Task, Proc, Stream, Ws, Watcher, Timer, Future con métodos despachados por host functions y `cleanup`/GC coherentes (la regla "quien crea, mata" + red del GC). 🔒 paridad proc/stream/ws | M09 |
| **M11** | **UI: compositor, regiones, bloques, input.** Region/Block como handles (los Blocks siguen viviendo en Go — hoy ya son opacos); keymaps y pila de input. Criterio de hecho: la pantalla desnuda (G21) y el chat arrancan en wasm. 🔒 paridad compositor/input/toolkit/chat | M10 |
| **M12** | **Workers = instancias wasm.** Un worker por instancia (aislamiento FÍSICO de memoria — el regalo de ADR-019); caps = lista de host functions concedidas (deny-by-default para superficie nueva, G6); colas acotadas con backpressure; exclusión recv/on_message (G8). 🔒 paridad worker_test + nota en pospuesto.md: P2 gana camino natural | M09 |
| **M13** | **Loader, plugins y extensiones oficiales.** `require` curado sobre wasm (DM5), carga topológica, reload best-effort (G2), y las 8 extensiones embebidas corriendo. Criterio de hecho: `nu --default-config && nu` funciona entero en wasm. La lista de skips queda VACÍA | M08, M10, M11 |

### Fase C — Paridad total, veto y conmutación

| Sesión | Contenido | Depende de |
|---|---|---|
| **M14** | **Baseline Lua 5.4** (ADR-019 pieza 2): api.md §1.2 actualizado, barrido de `unpack`/`setfenv`/etc. en extensiones y ejemplos de docs, `nu.version.api` NO se mueve (el baseline no es una firma). Guía de plugins gana la nota de compatibilidad | M13 |
| **M15** | **Checkpoint integral 🔎 + HITO DE VETO (§5).** Suite completa `-race` verde en ambos backends; benchmarks comparados registrados en la bitácora; los tres vetos evaluados con números | M14 |
| **M16** | **La conmutación.** Default = wasm; gopher queda tras `backend = "gopher"` (legacy, un ciclo de gracia). README/arquitectura.md/CLAUDE.md coherentes. El onramp no cambia (superficie CLI intacta) | M15 |
| **M17** | **La retirada.** Se elimina gopher-lua del go.mod, mueren los blindajes que solo existían por él (G41 en cancel.go, el pcall envuelto especial si ADR-020 lo hizo innecesario), ADR-011 se marca "Reemplazada por ADR-020", bitácoras y docs cerrados. El binario queda con UNA VM | M16 + un ciclo de uso real del default wasm sin regresiones |

### Grafo (resumen)

```
M01 ─┐
M02 ─┴─ M03 ── M04 ── M05 ─┬─ M06 ─┬─ M07
                           │       └─ M08 ─┐
                           ├─ M09 ─┬─ M10 ─┼─ M11 ─┐
                           │       └─ M12  │       ├─ M13 ── M14 ── M15 ── M16 ── M17
                           └───────────────┴───────┘
```

## 5. Hitos de veto (vinculantes, se evalúan en M15)

Como el spike de ADR-007/ADR-012: criterios objetivos pactados ANTES de
empezar. Si alguno falla, la migración se PAUSA en M15 (el trabajo no se tira:
queda detrás del selector), se registra, y decide el humano.

1. **Corrección**: la suite completa con `-race`, verde en wasm, incluidos
   todos los 🔒 heredados y nuevos. Sin excepciones ni skips.
2. **Rendimiento del camino caliente**: la simulación del streaming
   (modelo-ejecucion.md: SSE → markdown → blit) y un turno de agente headless
   contra el adaptador stub quedan **dentro de 2×** del backend gopher; el
   ciclo yield+resume del puente definitivo, **≤ 50 µs** sostenido.
3. **Experiencia**: arranque interactivo (`nu` con el conjunto oficial) sin
   degradación perceptible (< 150 ms añadidos con el módulo precompilado en
   caché) y binario final ≤ +1,5 MB sobre el actual.

## 6. Riesgos vigilados (con dueño)

- **API experimental de snapshots de wazero** → el gate-test 🔒 de M03 y el pin
  de versión; un upgrade que la rompa se detecta en CI, no en producción.
- **Coste del yield** (INFORME §4.1) → se decide y presupuesta en el ADR-020
  de M06; el veto 2 lo audita con números en M15.
- **Deriva blob/fuentes** → el job de hash de M02 (DM1).
- **Los tests que usan globales por la vieja limitación de upvalues** siguen
  siendo válidos (los globales funcionan igual); no se reescriben en masa.

---

## Bitácora

| Fecha | Sesión | Resumen |
|---|---|---|
| 2026-07-03 | — (plan) | Nace este plan (ejecuta ADR-019; rama `claude/migracion-vm-wasm`). Puntero en M01. |
| 2026-07-03 | **M01** | Censo de la frontera VM cerrado: `tools/censo-vm.sh` (resumen/`--files`/`--check`) + [migracion-vm-censo.md](migracion-vm-censo.md) con las 6 categorías (C1 valores/marshaling, C2 host functions, C3 puente ⏸, C4 errores/desenrollado, C5 userdata/handles, C6 libs/baseline) y el mapa fichero→categoría→sesión. La guardia `--check` cableada en CI (trinquete: ningún símbolo gopher-lua nuevo). Hallazgo confirmado del censo: C4 no se traduce, se **borra** (cancel.go + blindaje G41 existen solo por defectos de gopher-lua). Sin código de producción. Puntero → M02. |
| 2026-07-03 | **M02** | Blob productivo `internal/vmwasm`: shim consolidado (`shim/nu_shim.c`, renombrado de `spike_*` a `nu_*`, dispatch host genérico en vez de los host functions de benchmark del spike), `build.sh` reproducible (honra `$CC`), `nu.wasm` (477 KB) **comiteado** + `go:embed`, nota de licencia MIT (compatible Apache-2.0, ADR-013) y `.gitignore` de las fuentes de Lua. Cargador Go: `Pool` (compila una vez) + `Instance` (N instancias, **memoria aislada** — base de los workers M12), trampolín Snapshot/Restore heredado del spike, y `Dispatcher` pluggable (la costura que M05 rellena; en M02 rechaza). 7 tests: boot 5.4, libs del baseline, recuperación de errores, **G41 semántica de referencia sin blindaje** 🔒, **yield a través de pcall** 🔒, multi-instancia aislada, costura del dispatcher. Job de CI `vmblob` (reconstruye y verifica que el blob no derivó, DM1). wazero pasa a dependencia directa. Puntero → M03. |
| 2026-07-03 | **M03** | Trampolín endurecido a calidad de kernel (`trampolin_test.go`, 🔒 con `-race`): (1) anidamiento profundo de pcalls con LIFO balanceado; (2) **trap real del motor se propaga como fallo duro** (export de test `nu_selftest_trap` → `__builtin_trap`), jamás se traga como throw; (3) la no-reentrancia de `api.Function` esquivada (funciones frescas por llamada); (4) **N instancias concurrentes en goroutines sin contaminación cruzada** (ctx-routing — la base de M12); (5) error tras yield dentro del pcall que lo cruzó. **Hallazgo (anotado):** el techo de llamadas C de Lua (LUAI_MAXCCALLS ≈ 200) NO lo baja el trampolín de forma apreciable (150 niveles OK) y **degrada con gracia** — rebasarlo es un error de Lua capturable ("C stack overflow"), no un trap, y el estado sobrevive (semántica idéntica al Lua nativo). Puntero → M04. |
| 2026-07-03 | **M04** | Backend seleccionable (DM2): `VMBackend` (gopher/wasm) en `vm_backend.go`, `nu.toml [vm] backend`, env `NU_VM`, Option `WithVMBackend` y método `Runtime.VMBackend()`, con precedencia Option > `NU_VM` > `nu.toml` > gopher (default seguro hasta M16). `New` resuelve y registra el backend; el **camino de arranque wasm paralelo lo cablean M05-M13** (hoy `New` construye siempre gopher). Infra de la **suite dual**: helper `skipIfWasm` (la costura de la lista de skips, que M05-M13 recortan) y job de CI `Suite DUAL (NU_VM=wasm)` que corre `go test ./...` con el backend seleccionado. 4 tests del selector (default/env/toml/precedencia). La suite completa pasa con `NU_VM=wasm` (a M04, sigue sobre gopher por debajo: el selector sólo fija el campo). Puntero → M05. |
| 2026-07-03 | **M06** | **El corazón arquitectónico**: [ADR-020](adr.md#adr-020--el-puente--definitivo-tasks-como-corrutinas-lua-nativas-reemplaza-adr-011-en-la-conmutación) (reemplaza a ADR-011 en la conmutación) + el scheduler por **corrutinas nativas**. Shim: export `nu_sched_step` (puente Go↔bucle Lua). `host.go`: el scheduler Lua (tasks como `coroutine`, ⏸ como `coroutine.yield` de una petición, `__sched_step` como el paso). `scheduler.go`: el driver Go `RunTasks` (event loop de ADR-004 sin token: recoge peticiones cedidas, las cumple en goroutines de fondo, reanuda). 6 tests 🔒 (verde con `-race`): task simple, **concurrencia real** (dos tasks intercaladas `B1,B2,A1,A2` por yield nativo), await (incl. task ya terminada), **yield a través de pcall dentro de una task** (G31 imposible en gopher, aquí natural), y cancelación por contexto (base del apagado M07). **Alcance:** el núcleo del modelo (spawn/sleep/await/loop/cancel) está probado end-to-end; el resto de la superficie `nu.task` (all[G27]/race/every/defer/future/cleanup) **extiende el mismo bucle** y se completa junto a M07 (cancel/cleanup lo necesitan) — anotado en el puntero. Puntero → M07. |
| 2026-07-03 | **M05** | Marshaling (C1) + registro de host functions (C2): la **keystone**. `wire.go`: codec TLV byte-seguro (G11: strings crudos, sin re-codificar) con anchuras fijas u32 compatibles con `string.pack`/`unpack` de Lua 5.4; tags para nil/bool/int/float/string/array/map/**handle** (C5, para M10)/**NULL** (sentinel de G11). `host.go`: `hostRegistry` (nombre→id→`HostFn`) en el `Pool`; `Pool.Register` (la vía de M09+); `dispatchPrimitive` con protocolo de estado (byte 0=éxito/1=error); `StructuredError` que cruza `{code,message,detail}` fielmente (C4, paridad §1.4); y el **preludio Lua** (codec espejo + monta la tabla `nu` de thunks sobre `__nu_host`). El preludio se ejecuta al crear cada instancia. 9 tests 🔒: round-trip Go y Go↔Lua↔Go real (echo), **G11 bytes no-UTF-8 intactos en ambos sentidos**, integer vs float (5.4), tablas anidadas, sentinel NULL distinto de nil, y **cruce de errores estructurados y genéricos** capturables por `pcall`. Suite verde con `-race`. Con esto M09 (las primitivas) es mecánico. Puntero → M06. |
