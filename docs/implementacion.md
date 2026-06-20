# Plan de implementación incremental

Estado: borrador. Este documento ordena la construcción de `nu` en una
secuencia de **sesiones, cada una con una sola feature entregable**. No es un
contrato (no congela API; eso es [api.md](api.md)) sino un mapa de obra: en qué
orden se levanta el kernel, por qué ese orden y qué hace que una sesión esté
*hecha*.

## Seguimiento: dónde retomar en cada sesión

Cada sesión empieza con el contexto en blanco y en un contenedor efímero, así
que **el estado del progreso vive en el repositorio, no en la memoria de
Claude**. El mecanismo es el mismo patrón que ya usan `problemas.md` (campo
*estado*) y `adr.md`: un puntero visible, una bitácora append-only y la
disciplina de actualizarlos en el commit de la propia feature. Tres fuentes,
redundantes a propósito:

1. **El puntero** (esta misma línea, la única imperativa del documento):

   > **▶ Próxima sesión: `S01`** · Fase 0 · ninguna iniciada todavía.

2. **El tablero por fases** (vista de pájaro; se marca al cerrar la última
   sesión de cada fase):

   - [ ] **Fase 0** — Esqueleto y banco de pruebas (S01–S03)
   - [ ] **Fase 1** — Scheduler (S04–S09)
   - [ ] **Fase 2** — Eventos y loader (S10–S13)
   - [ ] **Fase 3** — IO, sistema y codecs (S14–S18)
   - [ ] **Fase 4** — Red (S19–S21)
   - [ ] **Fase 5** — Texto y búsqueda (S22–S27)
   - [ ] **Fase 6** — UI + spike de veto (S28–S33)
   - [ ] **Fase 7** — Workers (S34–S35)
   - [ ] **Fase 8** — Extensiones oficiales (S36–S45)

3. **La bitácora** (al final del documento): una fila por sesión cerrada, con
   su commit, hallazgos y desviaciones. Es el "qué pasó y por qué" que el
   puntero no cuenta.

**Backstop en git**: como cada commit cita su sesión (`S07: ...`), el estado se
puede reconstruir siempre con `git log --grep '^S[0-9]'` aunque el documento se
quedara desfasado. El documento manda; git es la red de seguridad.

### Protocolo de cada sesión

1. **Al empezar**: lee el puntero ▶ y la última fila de la bitácora. Eso es
   "dónde seguir" y "en qué estado quedó". No empieces otra sesión que la que
   marca el puntero (el grafo de dependencias es estricto).
2. **Durante**: implementa **solo** esa sesión, hasta cumplir su Definition of
   Done. Si destapas un hallazgo, párate y resuélvelo por el flujo de diseño
   (`problemas.md`) antes de seguir codificando.
3. **Al terminar, en el mismo commit que la feature**: avanza el puntero ▶ a la
   sesión siguiente, marca el tablero si cerraste una fase, y añade una fila a
   la bitácora. Un commit que toca código pero no mueve el puntero es una
   sesión a medias.

## Antes de empezar: un cambio de fase

Hasta hoy el proyecto está en **fase de diseño** y la regla ha sido *no escribir
código* ([CLAUDE.md](../CLAUDE.md)). Ejecutar este plan **abre la fase de
construcción**: a partir de la sesión S01 sí se crean ficheros Go y Lua. Eso no
deroga el resto de reglas, las refuerza:

- **La API del core es sagrada** (ADR-003). Este plan *implementa* `api.md`, no
  lo amplía. Si construyendo una feature descubres que la API no basta, eso es
  un **hallazgo** (`G##`): páralo, anótalo en [problemas.md](problemas.md),
  resuélvelo en los documentos *y luego* impleméntalo. El código nunca corrige
  la espec por la vía de hecho.
- **Lua decide, Go ejecuta** (ADR-004). Cada primitiva que toque la pantalla o
  escale con el repo se implementa en Go, paralela por dentro.
- **Español en prosa y commits; identificadores en inglés `snake_case`.** Los
  mensajes de commit referencian la sesión (`S07`) y el hallazgo si lo hubo.

## Cómo se usa este plan

Cada fila de las tablas de abajo es **una sesión de Claude**: una unidad de
trabajo que cabe en una ventana de contexto, deja el árbol compilando y añade
una capacidad *observable y probada*. La granularidad es deliberada: una sesión
≈ un submódulo de la API (a veces una porción, cuando el submódulo es grande).

### Contrato de sesión (Definition of Done)

Una sesión no está hecha hasta cumplir las cinco:

1. **Compila**: `go build ./...` verde, `CGO_ENABLED=0` (ADR-001).
2. **Se prueba**: tests Go de la primitiva *y* al menos un snippet Lua
   ejecutado contra el runtime embebido que ejercite la firma desde el lado del
   autor de extensiones (el arnés llega en S02).
3. **Respeta la espec**: la firma y la semántica implementadas son las de
   `api.md` §correspondiente, marcadores **⏸**/**[W]** incluidos. Ni una
   función de más.
4. **No regresiona**: las features de sesiones previas siguen verdes.
5. **Deja rastro**: commit en español citando la sesión; si hubo hallazgo,
   enlazado desde `problemas.md`.

### Columnas de las tablas

- **Sesión** — identificador secuencial.
- **Feature** — la capacidad entregada.
- **Depende de** — sesiones que deben existir antes (el grafo es estricto).
- **Espec** — sección de `api.md` (u otro contrato) que la define.
- **Criterio de hecho** — la prueba concreta que la cierra.

## Por qué este orden (el grafo de dependencias)

El kernel no se construye módulo a módulo en el orden en que `api.md` los
enumera, sino por **dependencia de ejecución**:

1. **El scheduler es la quilla.** Casi toda la API es **⏸**: no se puede
   probar `nu.fs.read` sin una task que la espere ni un loop que la reanude. Por
   eso, tras el esqueleto, lo primero y más caro es el puente
   corrutina-Lua ↔ goroutine-Go con su event loop (ADR-004). Todo cuelga de ahí.
2. **Eventos y loader** vienen pronto porque son el sustrato para *cargar y
   probar* cualquier cosa real (`init.lua`, `core:ready`) y para las guardas de
   robustez (`pcall` por frontera, ADR-008).
3. **IO, red, texto, búsqueda** son primitivas Go relativamente independientes
   entre sí una vez existe el scheduler; se ordenan por valor de desbloqueo
   (`fs`/`proc` antes que `http`, porque los tests de red se apoyan en ficheros).
4. **La UI va después de un spike de veto** (ADR-007, cuestión abierta nº1 de
   [arquitectura.md](arquitectura.md)): no se compromete la arquitectura de
   compositor + toolkit-en-Lua sin demostrar antes que es fluida.
5. **Los workers** llegan tarde porque son paralelismo opt-in: nada del camino
   básico depende de ellos, y necesitan que la API **[W]** ya exista para
   recortarla con `caps`.
6. **Las extensiones oficiales** (agente, providers, MCP, chat, toolkit, repl)
   son la última fase: son Lua sobre la API ya congelada, sin privilegio de
   kernel (ADR-003). Su orden interno lo manda su propio acoplamiento.

```
S01 esqueleto
      │
      ▼
S04 SCHEDULER ──┬─────────────┬───────────────┬──────────────┐
   (loop+task)  │             │               │              │
      ▼         ▼             ▼               ▼              ▼
   S08 events  S11 fs       S15 http        S18 text       S24 worker
      ▼         S12 watch   S16 stream      S19 markdown
   S09 loader  S13 proc     S17 ws          S20 highlight
      ▼         S14 sys                      S21 diff
   (plugins)    S10 codecs                   S22 re
                                             S23 search
                                                  │
                                                  ▼
                                   S25 SPIKE ADR-007 (veto)
                                                  ▼
                                   S26-S30 UI (compositor, input...)
                                                  ▼
                                   S31+ extensiones oficiales (Lua)
```

---

## Fase 0 — Esqueleto y banco de pruebas

El objetivo es poder ejecutar Lua dentro del binario y poder *probarlo*, sin
ninguna primitiva todavía.

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S01** | Bootstrap: módulo Go, `main`, embebido de gopher-lua, inyección del global `nu`, **baseline del sandbox** (deshabilitar `io`, `os.execute/exit/remove/rename/getenv`, redirigir `print`), `nu.version`, `nu.has`. | — | §1.2, §2 | `nu -e 'return nu.version.api'` imprime el nivel; `os.execute` es `nil` desde Lua. |
| **S02** | Errores estructurados (puente Go↔Lua: `error{code,message,detail}`) + **arnés de tests** que corre snippets Lua contra el runtime y hace asserts. | S01 | §1.4 | Un test fuerza `EINVAL` y captura la tabla con `pcall`; el arnés es reutilizable por todas las sesiones siguientes. |
| **S03** | `nu.log` (a fichero en `data_dir`, anotando plugin de origen; `print` = `info`). | S01 | §15 | Snippet escribe y el test lee la línea del log. Útil para depurar todo lo demás. |

## Fase 1 — El scheduler (la quilla)

Lo más difícil del kernel. Se parte en piezas pequeñas porque cada una es
sutil y debe probarse aislada.

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S04** | **Event loop + cola de eventos + `task.spawn` + `Task:await`** + el puente ⏸: una task se suspende, una goroutine publica su completion en la cola, el loop la reanuda. Primitiva ⏸ de prueba interna para ejercer el puente. | S02 | §1.3, §3 | Una task suspende, otra goroutine la despierta con un valor; `await` lo devuelve. Cero data races (`-race`). |
| **S05** | `nu.task.sleep`, `nu.task.defer`, `nu.task.every` (timers periódicos, handler síncrono) + `Timer:stop()`. | S04 | §3 | `sleep(50)` no bloquea el loop (otra task progresa en paralelo); `every` dispara N veces y `stop` lo corta. |
| **S06** | `nu.task.future` (rendez-vous de un solo uso; `set` síncrono una vez, `await` múltiple). | S04 | §3 | Una task espera `Future:await`, otra hace `set`; segundo `set` lanza `EINVAL`. |
| **S07** | `nu.task.all` (resultados **alineados con inputs**, G27) y `nu.task.race` (índice ganador; cancela el resto). | S04, S06 | §3 | `all` sobre 3 tasks devuelve `out[i]==fns[i]`; si una lanza, las otras se cancelan y relanza. |
| **S08** | **Cancelación**: `Task:cancel()` + `nu.task.cleanup` (pila LIFO) + desenrollado **no capturable por `pcall`** (§1.3); `ECANCELED` solo observable en `await`. | S04 | §1.3, §3 | Una task cancelada corre sus `cleanup`s; un `pcall` envolvente *no* atrapa el aborto. |
| **S09** | **Watchdog**: presupuesto por slice (100 ms, configurable), aborto por `EBUDGET` no capturable, emisión de `core:plugin.misbehaved`. | S08 | §1.3 | Un bucle Lua de CPU puro que excede el presupuesto es abortado; el evento se emite (verificable tras S10). |

## Fase 2 — Bus de eventos y loader

Con esto el runtime ya puede *cargar plugins reales* y emitir su ciclo de vida.

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S10** | `nu.events` (`on`/`once`/`emit`): despacho síncrono sobre **foto** de suscriptores (G10), `pcall` por frontera, **emits anidados encolados** (anchura, no recursión). | S04 | §4 | Cancelar durante el despacho surte efecto inmediato; un ping-pong entre handlers se aplana y el watchdog lo corta, no desborda la pila. |
| **S11** | **Loader**: `plugin.toml`/`init.lua`, rutas de `require` del plugin, **orden de arranque canónico** (core → plugins topológico por `requires` → `init.lua` usuario → `core:ready`), `nu.plugin.current/list`, `nu.config.dir/data_dir`. | S10 | §14 | Dos plugins con dependencia se cargan en orden topológico; el `init.lua` del usuario va último; `core:ready` se emite una vez. |
| **S12** | Activación de extensiones embebidas (`go:embed`, **inactivas por defecto**, ADR-010) gobernada por `nu.toml`; errores por extensión inactiva **accionables** (nombran la línea que lo arregla). Sin red. | S11 | §14, ADR-010 | Una extensión embebida no se carga salvo que `nu.toml` la active; el error apunta a la línea exacta. (La *pantalla* de runtime desnudo, que es UI, llega en S30.) |
| **S13** | `nu.plugin.reload` (best-effort, G2): etiquetado de handles por dueño, `core:plugin.unload`, vaciado de caché de `require`, recarga de `init.lua`. | S11 | §14 | Recargar un plugin suelta sus suscripciones y vuelve a registrar; un test verifica que no quedan handlers huérfanos. |

## Fase 3 — IO, sistema y codecs

Primitivas Go que el agente y las herramientas necesitan. Independientes entre
sí; se ordenan por valor de desbloqueo para los tests posteriores.

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S14** | `nu.fs` síncronas-⏸: `read`, `write`/`append` **atómicas** (temporal+rename), `exclusive`=`O_EXCL` (G17), `stat` (`nil` si no existe), `list`, `mkdir`/`remove`(recursive)/`rename`/`copy`, `tmpdir`, `cwd`. | S04 | §5 | Escritura atómica verificada; `write{exclusive}` sobre fichero existente lanza `EEXIST`; `stat` de inexistente da `nil`, no lanza. |
| **S15** | `nu.fs.watch` (lotes, `gitignore`, `debounce_ms`, `Watcher:stop()`, solo estado principal). | S14 | §5 | Un `git checkout` simulado (muchos ficheros) llega como **un** lote; lo ignorado por git no genera eventos. |
| **S16** | `nu.proc`: `run` (buffers, sin shell implícita), `spawn` (`Proc` con streams), `write`/`close_stdin`, `read_line`/`read`, `wait`/`kill`, `alive` (G17). Vida vía `cleanup` + red de seguridad GC. | S08 | §6 | `run(["echo","hi"])` devuelve `code=0,stdout`; un `spawn` se mata por `cleanup` al cancelar la task. |
| **S17** | `nu.sys`: `platform`, `env`/`setenv` (solo subprocesos futuros), `now_ms`/`mono_ms`, `hostname`. | S01 | §7 | `setenv` se ve en un subproceso lanzado después, no en el actual. |
| **S18** | Codecs: `nu.json` (**UTF-8 estricto** G11, sentinel `NULL`, `pretty`), `nu.toml`, `nu.yaml`. | S02 | §12 | `json.encode` de bytes inválidos lanza `EINVAL`; `NULL` ida y vuelta no pierde claves; `toml.decode` lee un `plugin.toml`. |

## Fase 4 — Red

El streaming es de primera clase porque los adaptadores de providers viven en
Lua y consumen SSE (ADR-005).

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S19** | `nu.http.request` (buffereada; no lanza por status ≥400; `tls`, proxy por entorno y por petición G12). | S04 | §8 | Contra un servidor de test, un 404 devuelve `status=404` sin lanzar; un fallo de transporte lanza `ENET`. |
| **S20** | `nu.http.stream`: `Stream` (status/headers), `chunks()`, **`events()` parser SSE incorporado**, `idle_timeout_ms`, backpressure (buffer acotado → `EIO`). | S19 | §8 | Un SSE de prueba itera `{event,data,id}`; un consumidor lento que desborda el buffer recibe `EIO`. |
| **S21** | `nu.ws.connect` (`send`/`recv`/`close`). | S04 | §8 | Eco websocket: `send` y `recv` round-trip; `recv` tras cierre da `nil`. |

## Fase 5 — Texto y búsqueda (Go pesado)

Lo cuadrático-en-pantalla, en Go. Aquí se define el tipo **Block** (compartido
con la UI).

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S22** | `nu.text` básico + **tipo `Block`**: `width` (graphemes/emoji), `wrap`→Block, `truncate`; `nu.ui.block` (construcción manual), tipo `Style`, `nu.ui.caps` (degradado de color). | S02 | §10, §9.2 | `width("😀")` correcto; `wrap` produce un Block con `.width/.height`; un Block manual se inspecciona en test. |
| **S23** | `nu.text.markdown` (render completo, **streaming-safe** ante entrada incompleta, themable). | S22 | §10 | Markdown parcial (en mitad de un bloque de código) no rompe; el Block crece de forma estable al añadir texto. |
| **S24** | `nu.text.highlight` (syntax highlighting por lenguaje). | S22 | §10 | Resaltado de un snippet Go produce spans con estilo; lenguaje desconocido degrada a texto plano. |
| **S25** | `nu.text.diff` (hunks estructurados; `render=true` → Block). | S22 | §10 | Diff de dos strings devuelve hunks correctos y, con `render`, un Block pintado. |
| **S26** | `nu.re` (RE2): `compile`, `match`, `find_all`, `replace`. | S02 | §10 | Patrón con grupos captura; `find_all` devuelve rangos; RE2 no acepta backreferences (error claro). |
| **S27** | `nu.search`: `files` (recursivo, `.gitignore`), `grep` (iterador paralelo), `fuzzy` (síncrono acotado, para pickers). | S04, S14 | §11 | `grep` itera `{path,line_no,ranges}` según llegan; `fuzzy` ordena por score; `files` respeta `.gitignore`. |

## Fase 6 — UI (con spike de veto primero)

No se compromete la arquitectura de UI sin validarla. El spike es una sesión de
pleno derecho con criterio de **veto pre-comprometido** (ADR-007).

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S28** | **SPIKE ADR-007**: compositor + celdas/regiones mínimos + toolkit Lua mínimo, torturado con (a) streaming de tokens con markdown a pantalla completa y (b) fuzzy picker sobre ~100k ficheros. | S23, S27 | arquitectura §"Cuestiones abiertas" nº1 | Mediciones de fluidez. **Si no es fluido**, se ejecuta el veto: el toolkit se moverá a Go conservando la API pública (esto reordena la Fase 8). Decisión registrada en `adr.md`. |
| **S29** | `nu.ui` compositor real: `region` (z-order, recorte en resize G1), `blit` (**viewport** con offsets negativos G28, copia nunca re-render), `fill`/`clear`, `size`, coalescing ~30 ms. | S28 | §9.1 | Blittear el mismo Block con distinto offset *no* recalcula; una región fuera de pantalla se recorta sin pintar fuera de límites. |
| **S30** | Ciclo de vida de `Region`: `move`/`resize`/`raise`/`lower`/`show`/`hide`/`destroy`/`cursor` (un solo dueño del cursor). | S29 | §9.1 | z-order respeta `raise/lower`; solo la última `cursor()` gana. |
| **S31** | Input: `on_input` (pila, `fn(ev)->consumed`), `keymap` (notación, secuencias con timeout en el core), **paste de imagen** → fichero temporal + `path` (G30). | S29 | §9.3 | La pila enruta al handler superior; `"g g"` resuelve con timeout; una imagen pegada llega como `path`, no bytes. |
| **S32** | Resto de `nu.ui`: `clipboard` (OSC 52) y eventos `ui:resize`/`focus`/`suspend`/`resume`; headless G20 (`nu.ui` **no existe** sin TTY, detectable por `nu.has("ui")`). | S29 | §9.2, §9, §4 | Bajo `nu -e` el módulo `nu.ui` es inexistente y `nu.has("ui")` es `false`. |
| **S33** | **Pantalla de runtime desnudo** (G21): render fijo pre-Lua (versión, rutas, embebidas, acciones) cuando hay TTY y ningún plugin activo. | S12, S29 | §14 | Arrancar sin plugins activos pinta la pantalla; activar el conjunto oficial escribe `plugins.enabled` y continúa el arranque. |

## Fase 7 — Workers (paralelismo opt-in)

Llegan tarde a propósito: necesitan que la API **[W]** ya exista para recortarla
con `caps`. Cada worker es un mini-runtime completo (scheduler propio, **sin
watchdog**).

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S34** | `nu.worker.spawn` (estado Lua aislado, carga `module`), **`caps` con dos granularidades** (`"fs"` vs `"fs.read"`, deny-by-default para superficie nueva, G6), `send`/`recv` con colas **acotadas** (backpressure). | S04, S11 | §13 | Un worker con `caps={"fs.read"}` no ve `fs.write` (no existe); `send` suspende cuando la cola se llena. |
| **S35** | `Worker:on_message` (**excluyente con `recv`**, G8: lanza `EINVAL` si se mezclan), canal `nu.worker.parent`, `terminate` (inmediato y seguro), tasks/timers/futures dentro del worker. | S34 | §13 | Registrar `on_message` con un `recv` pendiente lanza `EINVAL`; un worker corre varias tasks; `terminate` lo corta sin afectar al padre. |

## Fase 8 — Extensiones oficiales (Lua sobre la API congelada)

Aquí ya no se toca el kernel: es Lua sobre la API pública, sin privilegio de
kernel (ADR-003). El orden lo manda el acoplamiento entre extensiones. Si la
sesión S28 ejecutó el veto, el toolkit (S40) se construye en Go en su lugar.

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S36** | Extensión **providers**: lector del registro TOML, contrato del adaptador, `providers.approx_tokens` (la heurística que G23 sacó del core). | S18, S20 | [providers.md](providers.md) | El registro TOML se carga; un adaptador stub responde a una petición simulada. |
| **S37** | **Adaptador Anthropic** (SSE, tool calls, system prompt, thinking blocks) como primer dialecto real. | S36 | providers.md | Contra un SSE grabado, el adaptador emite el stream de mensajes canónico. |
| **S38** | Extensión **sesiones**: JSONL append-only, modelo canónico de mensajes, lockfiles (`fs.write{exclusive}` + `proc.alive` para huérfanos). | S14, S16 | [sesiones.md](sesiones.md) | Una sesión se persiste y se reanuda; un lock huérfano (pid muerto) se detecta y reclama. |
| **S39** | Extensión **agente** (motor headless): turno, registro de tools, **permisos**, hooks-middleware (`tool.pre`...), eventos `agent:*`. | S37, S38 | [agente.md](agente.md) | Un turno completo con una tool de prueba; un permiso denegado produce error accionable. |
| **S40** | **Subagentes** del agente (vía workers con `caps` recortadas / paquetes con nombre). | S39, S35 | agente.md §subagentes | Un subagente corre aislado con API recortada y devuelve resultado digerido. |
| **S41** | Extensión **MCP** (capa 2): `io.spawn` + JSON-RPC/stdio, ciclo de vida de procesos, mapeo de tools y su confianza. Cierra la cuestión abierta nº4 de arquitectura. | S16, S18, S39 | arquitectura §"Providers"/capa 2 | Un servidor MCP de prueba se lanza, anuncia tools y el agente las invoca. |
| **S42** | **Toolkit de widgets** (extensión Lua oficial, o Go si S28 vetó): árbol + nodos sucios, slots, focus, themes (nombres semánticos de color → literales, G22). | S29, S31 | arquitectura §"kernel"/nota ui | Un layout con focus entre dos widgets compone sin colisión entre plugins. |
| **S43** | Extensión **chat** (la UI oficial del harness) sobre toolkit + agente. | S42, S39 | [chat.md](chat.md) | Conversación con streaming de tokens pintada con markdown; input multilínea. |
| **S44** | Extensión **repl** (REPL de Lua sobre la API pública; activable solo, sin el harness, G21). | S32 | arquitectura §"Distribución" | `nu` con solo `repl` activo evalúa expresiones Lua interactivamente. |
| **S45** | **Superficie CLI** (cuestión abierta nº5): flags de `nu -e`, `--auto-permissions`, headless, códigos de salida, azúcar `--continue` sobre `agent.session{resume}` (G18). | S39 | arquitectura §"Cuestiones abiertas" nº5 | `nu -e` ejecuta sin TTY con códigos de salida correctos; `--continue` reanuda la última sesión. |

---

## Hitos de validación

No todo el valor está en las features; tres puntos son **decisiones con veto**:

- **S09 (watchdog) + S08 (cancelación)**: validan el modelo de robustez de
  ADR-008 (aislamiento por tarea, no por plugin). Si el desenrollado no
  capturable no se puede implementar limpio en gopher-lua, es un hallazgo mayor.
- **S28 (spike ADR-007)**: el veto del toolkit. Reordena toda la Fase 8 si cae.
- **S37 (primer adaptador real)**: primera vez que el camino caliente completo
  (HTTP stream → SSE → markdown → blit) corre de punta a punta. Valida que el
  rendimiento de Lua en el camino caliente (limitación nº8 de
  [modelo-ejecucion.md](modelo-ejecucion.md)) es aceptable.

## Coherencia con el flujo de diseño

Este plan **no sustituye** al flujo de `problemas.md`/`adr.md`/`pospuesto.md`:
lo consume. Si una sesión destapa una grieta, se abre como `G##`, se resuelve en
*todos* los documentos afectados y solo entonces se implementa. Si una sesión
toma una decisión nueva (p. ej. el resultado del spike S28), se registra como un
ADR nuevo, nunca reescribiendo uno viejo. El código es el último eslabón de la
cadena de coherencia, no el primero.

---

## Bitácora

Append-only: una fila por sesión cerrada, la más reciente abajo. Es la fuente de
contexto que lee la sesión siguiente (ver "Protocolo de cada sesión"). Anota
desviaciones del plan, hallazgos abiertos (`G##`) y cualquier decisión que la
sesión siguiente necesite saber. Cuando esté vacía, el proyecto no ha empezado a
construirse.

| Fecha | Sesión | Commit | Notas (hallazgos, desviaciones, lo que debe saber la siguiente) |
|---|---|---|---|
| — | — | — | *(aún sin sesiones cerradas)* |
