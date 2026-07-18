---
title: "Informe de análisis de arquitectura"
type: "auditoria"
date: "2026-07-08"
status: "cerrada"
---
# Informe de análisis de arquitectura

**Fecha:** 2026-07-08 · **Base analizada:** `main` en `1fae5cb` (merge de la migración VM WASM, M17)

> **Nota de archivo (2026-07-16).** Instantánea histórica, cerrada: se archiva
> tal cual se escribió. Es **anterior al renombrado total nu→enu (ADR-022)** —
> todas las menciones `nu.*` del texto son los nombres de la época — y anterior
> a la auditoría integral del 12-07 (que abrió G44–G52) y a la de seguridad del
> 16-07 (G53–G56), que cubren terreno adyacente. Estado de sus hallazgos a
> fecha de archivo: **H-1 y H-2** se registraron y resolvieron como **G42 y
> G43** (mismo cambio que archiva este informe); **H-4** quedó resuelto en
> `develop` al cerrar el data race de SEC-05 (G56), que reescribió el
> comentario de `Instance.mu` — el test `-race` de watchers concurrentes que
> pedía el bloque 2 llega con este mismo cambio; **H-12** quedó atendido por
> las revisiones sucesivas de `CLAUDE.md` (que dejó de contar ADRs y problemas
> y remite a los registros vivos); y el `govulncheck` de **H-16** corre hoy en
> la pasada periódica `/salud`. El resto (H-3, H-5–H-11, H-13–H-15 y los
> huecos de CI restantes) sigue pendiente y conserva aquí su evidencia.

Este informe es una instantánea de auditoría, no un documento canónico de diseño
(esos viven en `docs/`). Cubre cuatro frentes, analizados en paralelo: los
documentos de diseño, el kernel Go (`internal/runtime` + `internal/vmwasm`,
~15.600 líneas no-test), las extensiones Lua embebidas (~10.300 líneas) y el
proceso (planes de sesión, CI, coherencia docs↔código).

---

## 1. Resumen ejecutivo

El proyecto está en un estado notablemente sano. Las 45 sesiones del plan de
implementación están cerradas y, encima, se completó una segunda fase de
construcción entera —la migración de gopher-lua a PUC-Lua 5.4 sobre wazero
(M01–M17, ADR-019/020)— técnicamente bien ejecutada: aislamiento de memoria
físico por instancia, trampolín de suspensión optimizado, caché de compilación,
y gopher-lua fuera del `go.mod` y del binario. El principio fundacional —el
core no sabe lo que es un agente— **se cumple de forma verificable**: se
contrastaron una a una todas las llamadas `nu.*` de los 34 ficheros Lua
embebidos contra `docs/api.md` y ninguna usa superficie no documentada ni
acoplamiento oculto entre extensiones.

Los problemas encontrados se agrupan en tres familias, por orden de gravedad:

1. **Tres grietas contrato↔implementación no registradas** (candidatas a `G##`
   nuevos): el retry con backoff que `agente.md` §2 promete no existe en el
   código; `agent:error` descarta el `code` y el `retryable` que `chat.md`
   necesita; y la UX de conflicto de lock de sesión (fork / solo lectura /
   forzar) de `sesiones.md` §6 no tiene consumidor. Son grietas *silenciosas*:
   no están ni resueltas ni pospuestas, simplemente nadie las anotó.
2. **Deuda de higiene post-migración en el kernel**: un comentario de
   concurrencia hoy falso que invita a introducir un data race, un puente
   síncrono Go↔Lua frágil basado en `tostring`, duplicación estructural
   heredada del patrón estrangulador y ~1.200 líneas de Lua embebidas como
   string dentro de un fichero Go.
3. **Documentación de gobierno desfasada**: `CLAUDE.md` da números incorrectos
   (10 ADRs cuando hay 20, 25 problemas cuando hay 39, cita mal el ADR de la
   licencia), `api.md` sigue autodeclarándose "borrador" pese a estar
   implementada y vendida como sagrada, y varios ADRs quedaron sin la nota de
   reemplazo/revisión que el propio proceso exige.

Nada es bloqueante. Las recomendaciones de prioridad alta (§6) son baratas y
protegen contra errores futuros; las medias son refactors de bajo riesgo y
alto valor de mantenimiento.

---

## 2. Estado real del proyecto

- **Plan de implementación** (`docs/implementacion.md`): puntero en
  `PLAN COMPLETO: 45/45 sesiones, Fase 8 cerrada`; bitácora al día (última
  fila: G41, 2026-07-03).
- **Migración VM** (`docs/migracion-vm.md`): M17 cerrada; wazero es la única
  VM. `NU_VM=gopher` solo emite un aviso (blindado por `vm_retirada_test.go`).
  El veto de tamaño de binario **no se cumplió** (+1,99 MB release sobre el
  umbral de +1,5 MB) y se avanzó por excepción razonada del humano; esa
  excepción vive solo en prosa de la bitácora, no en el registro `P##`.
- **Problemas** (`docs/problemas.md`): 39 registradas, todas resueltas (hueco
  documentado G24–G25). **Pseudocódigo**: 8 rondas (la 8, "malla de agentes",
  originó `mesh` y G38–G41).
- **Pendiente estructural**: `docs/malla.md` sigue en v0.1 con 8 decisiones
  abiertas (D1–D8); P32 (peaje de rendimiento del camino caliente wasm, ~5× en
  turno de agente contra stub) queda abierto con disparador.

---

## 3. La arquitectura implementada (mapa real)

Dos paquetes Go con frontera limpia:

- **`internal/vmwasm/`** — el motor de VM, que no sabe nada de `nu.*`:
  `Pool`/`Instance` sobre wazero (compilación única por proceso vía
  `sync.Once` + caché en disco), trampolín `LUAI_TRY`/`LUAI_THROW` con
  Snapshot/Restore, watchdog por slice, y el scheduler de tasks **escrito en
  Lua** (corrutinas nativas, ADR-020) embebido en `host.go`.
- **`internal/runtime/`** — el kernel de producto que define `nu.*`:
  `runtime.go` como eje (estado de sesión, catálogo de los 12 submódulos,
  `Boot`/`Close`), un patrón de doble fichero por primitiva (`fs.go` con la
  lógica + `vmwasm_fs.go` con el binding), compositor/driver/TTY para la UI, y
  el loader de plugins VM-agnóstico.

Flujo de una primitiva ⏸: la corrutina Lua cede una petición → `RunTasks` la
despacha en goroutine de fondo → el `HostFn` toca SO/red (nunca la VM) → se
reanuda la corrutina con `{ok, values}`. Las extensiones oficiales (agent 2.251
líneas, chat 2.457, providers 1.811, toolkit 1.794, mcp, mesh, sessions, repl)
viven en `internal/runtime/embedded/` y consumen exclusivamente la API pública.

---

## 4. Fortalezas verificadas

- **No-privilegio impecable.** Cero llamadas a API no documentada en las
  extensiones; cero acceso cruzado a campos privados de otra extensión; los
  códigos acuñados (`EAGENT`, `EPROVIDER`, `EMESH`, `ESESSION`, `EMCP`) siguen
  la forma de ADR-009. La única dependencia no declarada (`repl` → `toolkit`)
  es blanda, vía `pcall(require)`, deliberada y documentada en el propio
  fichero (G21).
- **Disciplina de errores.** De 56 `error(...)` en el árbol Lua, todos los
  lanzamientos reales son estructurados `error({code=...})`; no hay ni un solo
  `error("texto plano")`. Cero TODO/FIXME/HACK reales en ~10.000 líneas: el
  flujo problemas/pospuesto absorbe la deuda antes de que llegue al código.
- **Rendimiento bien razonado en el motor.** Compilación WASM compartida a
  nivel de proceso más caché en disco (`vmwasm.go:77-127`); reutilización de
  `api.Function` por profundidad de pcall en el trampolín (`vmwasm.go:521`),
  que eliminó ~31 % de asignaciones del turno de agente; aislamiento del
  modelo "sin memoria compartida" a nivel físico (memoria lineal por
  instancia, `vmwasm.go:291`).
- **Cierre de recursos robusto.** `Runtime.Close` corta procesos, streams,
  websockets y greps con copia-antes-de-iterar para evitar deadlocks
  (`scheduler.go:94-202`); quiescencia que distingue tasks vivas de timers
  `every` (`scheduler.go:135-146`); cancelación dirigida de peticiones en
  vuelo sin contextos filtrados.
- **Dependencias disciplinadas.** Las 10 dependencias directas de `go.mod`
  están todas en uso, sin resto de gopher-lua; binario estático
  `CGO_ENABLED=0` como prometen los ADRs.
- **CI con guardianes reales.** Formato, vet, lint, `go test -race
  -shuffle=on` en linux+macOS, smoke test headless, gate de coherencia
  tag↔versión ejecutando el binario, y el job `vmblob` que reconstruye
  `nu.wasm` con toolchain hermética (wasi-sdk pineado por sha256) y falla si
  el blob comiteado derivó.

---

## 5. Hallazgos

### 5.1 Grietas contrato↔implementación (candidatas a G## nuevos)

**H-1 · ALTA — El retry con backoff de `agente.md` §2 no existe.**
El contrato promete: *"Errores del adaptador con `retryable = true`: reintento
con backoff exponencial y límite configurable — la política vive aquí, nunca
en el adaptador"* (`docs/agente.md:116-118`). Los tres adaptadores cumplen su
mitad (marcan `retryable` correctamente: `adapter_anthropic.lua:563`,
`adapter_openai_compat.lua:400`, `adapter_gemini.lua:306`), pero el motor
llama al adaptador sin ningún wrapper de reintento
(`agent/lua/agent/init.lua:1062`, `subagent_worker.lua:124`): un `EPROVIDER`
retryable propaga igual que uno permanente hasta cerrar el turno con
`agent:error`. No hay rastro de "backoff"/"retry" ni en `problemas.md` ni en
`pospuesto.md`: es una grieta sin registrar.

**H-2 · ALTA — `agent:error` descarta el `code` y el `retryable` que
`chat.md` necesita.** `docs/chat.md:52` promete un bloque de error *"con el
código estructurado y, si `retryable`, acción de reintento"*. Pero
`Session:_turn_loop` pierde el `detail` al construir el payload
(`agent/lua/agent/init.lua:1004-1006`) y el handler de chat solo usa
`p.message` (`chat/lua/chat/init.lua:502-507`): aunque H-1 se implementara, la
UI no tiene ni el dato ni la interacción prometida.

**H-3 · MEDIA — La UX de conflicto de lock de sesión no tiene consumidor.**
`docs/sesiones.md` §6 describe tres salidas ante conflicto real de escritor
(fork por defecto / solo lectura / forzar); `sessions` delega correctamente en
el llamante (`sessions/lua/sessions/init.lua:149`), pero ni `agent` ni `chat`
referencian `ESESSION` en ninguna parte, y `ESESSION` no está en los
`CONFIG_ERROR_CODES` del arranque degradado (`chat/lua/chat/init.lua:49`): un
conflicto de lock en `resume` hoy revienta como fallo inesperado.

### 5.2 Kernel Go: higiene post-migración

**H-4 · ALTA — Comentario de concurrencia falso en `Instance.mu`.**
`internal/vmwasm/vmwasm.go:339` dice que `mu` *"sólo protege contra reentrada
accidental en tests, no concurrencia real"*. Es falso: `wasmWatcher.run` es
una goroutine de fondo que entrega lotes vía `inst.EmitEvent`
(`vmwasm_fs.go:440`), y `EmitEvent` toma `slotMu`+`mu` para el par
ranura+Eval (`vmwasm.go:184-198`). Con varios watchers activos esas goroutines
compiten de verdad por la VM; ese mutex es la única barrera. Un mantenedor que
se crea el comentario y "optimice" quitando el lock introduce un data race.

**H-5 · ALTA — El puente síncrono del estado principal marshala por
`tostring`.** `EvalString` hace hasta N+4 cruces de VM por evaluación, todos
devolviendo strings (`eval.go:48-79`), y reconstruye el error estructurado
**parseando texto** `"CODE: mensaje"` (`eval.go:254`) — pierde `detail`, falla
si el mensaje contiene `": "` y pierde fidelidad de tipos. El camino de task
ya cruza tablas fieles vía el codec `wire.go`; el principal no.

**H-6 · MEDIA — `host.go` es un fichero-dios con ~1.200 líneas de Lua en un
string Go.** El scheduler de tasks, el bus de eventos, los workers y `require`
viven como literal Lua dentro de `preludio()` (`internal/vmwasm/host.go`,
1.333 líneas totales): sin resaltado ni lint del Lua, imposible de testear en
aislamiento, y cualquier diff de `nu.task.await` toca el fichero más grande
del repo.

**H-7 · MEDIA — Duplicación y patrón bífido sin valor tras M17.** El
`wasmWatcher` (`vmwasm_fs.go:229-529`, ~300 líneas de debounce, gitignore y
recursión) es el "gemelo" de una versión gopher que ya no existe; `watch.go`
quedó reducido a dos constantes. En general, el patrón `X.go` + `vmwasm_X.go`
(~10 submódulos) nació para compartir lógica entre dos backends y hoy solo hay
uno: o se colapsa, o se registra en un ADR que se conserva deliberadamente
para un hipotético segundo backend.

**H-8 · MEDIA — `StructuredError` duplicado y códigos por literal.** Hay dos
structs (`runtime/errors.go:61` y `vmwasm/host.go:29`, con campos distintos) y
`mapFsErrorWasm` escribe `Code: "ENOENT"` como literal (`vmwasm_fs.go:31-42`)
en vez de usar las constantes `Code*`: el invariante 🔒 de S02 ("un código
reservado nunca se reescribe") no está blindado en el lado wasm.

**H-9 · BAJA — Vestigios del estrangulador y comentarios arqueológicos.**
`vm_backend.go` entero es un no-op conservado por compatibilidad de tests
(`runtime.go:244-249`); decenas de comentarios describen mecanismos de gopher
que ya no existen ("toma el token de gopher", "paridad con deliverBatch de
gopher"); `pollWasmQuit` sondea un global por `Eval` donde un canal Go sería
directo (`driver.go:99-106`).

### 5.3 Extensiones Lua: calidad

**H-10 · MEDIA — Duplicación entre los tres adaptadores de providers.**
Los helpers `eprovider`/`einval` están definidos cuatro veces (tres adaptadores
+ `providers/init.lua:38-44` sin exportar); el bloque de manejo de error HTTP
(~20 líneas: drenar chunks, decodificar error, calcular `retryable`) y el
bucle de `count_tokens` son casi idénticos en los tres. Extraer helpers
compartidos exportados ahorraría ~120–150 líneas sin tocar la lógica de
dialecto.

**H-11 · MEDIA — `agent/init.lua` (1.641 líneas) y `chat/init.lua` (1.013)
son monolitos troceables.** El propio directorio ya sentó precedente
(`subagent.lua`, `tools_fs.lua`, y en chat: `input`/`permission`/`picker`/
`statusline`/`commands`/`transcript`). Candidatos naturales:
`agent/permissions.lua`, `agent/skills.lua`, `agent/trust.lua`,
`agent/session.lua`; y en chat, `render.lua` + `build.lua`.

### 5.4 Documentación y proceso

**H-12 · ALTA — `CLAUDE.md` desfasado en datos que un agente citará mal.**
Dice "diez ADRs (ADR-001…ADR-010)" cuando hay 20; atribuye la licencia a
ADR-013 cuando es ADR-014 (ADR-013 es CI/releases); dice "25/25 resueltas"
cuando son 39; describe las rondas de pseudocódigo solo hasta la 5 cuando hay
8; su tabla de estructura omite `malla.md`, `migracion-vm.md`,
`migracion-vm-censo.md` y el directorio `web/`; y el framing "por defecto el
proyecto está en fase de diseño" ya no describe la realidad (kernel construido
+ migración de motor completada).

**H-13 · MEDIA — Higiene del registro ADR incompleta.** `api.md` sigue
autodeclarándose *"borrador para discusión"* y ADR-009 sigue "Propuesta — se
acepta al congelar api.md", pese a que el kernel entero la implementa y el
proyecto la vende como sagrada: no existe el evento documentado "api.md se
congeló". ADR-002 (gopher-lua/Lua 5.1) no lleva nota de reemplazo pese a que
ADR-019/020 cambian de facto su decisión (contrasta con ADR-011, sí marcada
"Reemplazada por ADR-020"). ADR-006 ("a revisar cuando se cierre ADR-007")
nunca se revisó aunque ADR-007 cerró vía ADR-012. Y hay una discrepancia de
tiempos verbales: ADR-011 y `api.md`/`arquitectura.md` narran la migración
como consumada mientras ADR-019 la sigue describiendo como "dirección sin
fecha comprometida".

**H-14 · MEDIA — README pre-M17 y `spike/` sin archivar.** El README
(2026-07-06) no refleja que el motor ya es PUC-Lua 5.4/wazero salvo por un
enlace suelto. `spike/lua-wasm/` es código muerto de facto (módulo Go aislado,
ningún workflow lo toca, `internal/vmwasm/build.sh` ya no deriva de él) sin
nota de "histórico, no mantenido".

**H-15 · MEDIA — Disparadores de `pospuesto.md` posiblemente ya cumplidos.**
P11 (workers sin anidación) tiene como disparador exactamente "un
subagente-en-worker que necesite su propio paralelismo interno" — el runner de
jobs de `mesh` es un candidato plausible. P12 (tool calls secuenciales) espera
"turnos dominados por tools lentas e independientes", verificable ya con uso
real de `nu -p`. Conviene una pasada de revisión de disparadores.

**H-16 · BAJA — Huecos de CI.** Sin cobertura de tests (ni siquiera
informativa), sin `govulncheck`/Dependabot/CodeQL (para un proyecto que se
vende como "cero dependency hell", nada lo audita automáticamente), y los
binarios cross-compilados de release (linux/arm64) nunca se ejecutan en CI.

---

## 6. Plan de acción propuesto

Ordenado por relación valor/coste; los tres primeros bloques son independientes
entre sí.

**Bloque 1 — Registrar las grietas (solo documentos, el flujo del proyecto lo
exige antes de tocar código).**
1. Abrir tres `G##` nuevos en `problemas.md` para H-1, H-2 y H-3, y decidirlos
   por el cauce habitual (¿retry en `Session`? ¿payload de `agent:error` con
   `code`+`retryable`? ¿quién consume el conflicto de lock?). Si alguno se
   difiere, que sea como `P##` con disparador, no por silencio.

**Bloque 2 — Higiene del kernel (código, riesgo bajo).**
2. Corregir el comentario de `Instance.mu` (H-4) describiendo su papel real, y
   añadir un test `-race` con varios `nu.fs.watch` concurrentes emitiendo.
3. Unificar `EvalString` sobre el codec `wire.go` (H-5): un solo cruce
   estructurado, adiós al parseo de `"CODE: mensaje"`.
4. Extraer el preludio Lua de `host.go` a ficheros `.lua` con `go:embed`
   (H-6), lo que además habilita testearlo sobre una Instance desnuda.
5. Compartir las constantes de código de error entre paquetes y usar
   `CodeENOENT` y compañía en los mapeos wasm (H-8), con un test que blinde el
   invariante de S02.

**Bloque 3 — Documentación de gobierno (barato, evita errores en cadena).**
6. Actualizar `CLAUDE.md` (H-12): 20 ADRs, 39 G##, 8 rondas, ADR-014 para la
   licencia, añadir `malla.md`/`migracion-vm*.md`/`web/` a la tabla, y
   reformular el default "fase de diseño" al estado real (mantenimiento y
   evolución con el mismo flujo G##/ADR).
7. Cerrar la higiene del ADR (H-13): marcar ADR-002 como refinada/reemplazada
   por ADR-019, ascender ADR-009 a Aceptada declarando el evento "api.md v1
   congelada" (y quitar el "borrador" del encabezado de api.md), y revisar
   ADR-006 como su propio texto exige.
8. Actualizar README post-M17 y archivar `spike/lua-wasm/` con nota explícita
   (H-14).

**Bloque 4 — Refactors de mantenimiento (cuando se toque ese código).**
9. Colapsar o justificar por ADR el patrón bífido `X.go`+`vmwasm_X.go`, y
   des-duplicar el watcher (H-7).
10. Extraer helpers compartidos de providers (H-10) y trocear
    `agent/init.lua`/`chat/init.lua` (H-11) siguiendo el precedente ya
    existente en ambos directorios.

**Bloque 5 — Proceso continuo.**
11. Pasada de revisión de disparadores de `pospuesto.md` (H-15), registrar la
    excepción del veto de tamaño de binario como `P##` con disparador propio,
    y cerrar las decisiones D1–D8 de `malla.md` antes de que `mesh` gane uso.
12. Añadir `govulncheck` y cobertura informativa al CI (H-16).

---

## 7. Metodología

El análisis se ejecutó con cuatro agentes en paralelo sobre ámbitos disjuntos:
documentos de diseño (filosofía, arquitectura, api, 20 ADRs), kernel Go
(runtime + vmwasm + main), extensiones Lua embebidas (34 ficheros, con
contraste llamada-a-llamada contra `api.md`), y proceso (planes S##/M##, CI,
coherencia docs↔código contra el historial git). Cada hallazgo de este informe
conserva la evidencia `fichero:línea` reportada por el agente correspondiente y
fue cruzado entre informes cuando dos ámbitos tocaban el mismo tema (p. ej. la
migración VM aparece en los cuatro).
