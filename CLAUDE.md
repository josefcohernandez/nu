# CLAUDE.md

Guía para asistentes de IA que trabajen en este repositorio.

## Qué es este proyecto

`nu` es **un runtime de Lua orientado a terminal cuya killer app es un coding
harness**: un único binario Go con un kernel mínimo donde todo lo demás —
incluido el propio agente — son extensiones Lua.

**Crítico:** por defecto el proyecto está en **fase de diseño**. Los documentos
en `docs/` **son** el proyecto. La API se valida escribiendo pseudocódigo contra
ella antes de congelarla. Tu trabajo en una tarea de diseño es **diseño y
documentación** (razonar sobre la API, encontrar grietas, registrar
decisiones): **no crees ficheros de código Go o Lua salvo que se pida
explícitamente**; el pseudocódigo ilustrativo vive dentro de los `.md`.

**Excepción — fase de construcción:** cuando la tarea sea *implementar* el
kernel, se rige por [docs/implementacion.md](docs/implementacion.md) y su
**protocolo de sesión** (una feature por sesión, puntero, checkpoints, tests).
Eso *es* "pedirlo explícitamente": ahí sí se escribe código. El default "no
código" sigue valiendo para todo lo demás. Cómo operar en esa fase: la sección
"[Cuando implementes](#cuando-implementes-fase-de-construcción)" de abajo.

## Idioma y estilo

- **Todo el repositorio está en español** (documentos, mensajes de commit).
  Escribe en español, con el mismo registro: prosa densa pero precisa, frases
  que justifican el *porqué* de cada decisión, no solo el *qué*.
- Los **identificadores de la API son en inglés y `snake_case`**
  (`nu.fs.read`, `nu.task.spawn`); la prosa que los rodea, en español.
- Tono: afirmativo y razonado. Cada decisión se acompaña de su motivación y,
  cuando aplica, de las alternativas descartadas. Imita la voz de los
  documentos existentes antes de añadir nada.

## Estructura del repositorio

Todo vive en `docs/`. Orden de lectura sugerido (y dependencias conceptuales):

| Documento | Rol |
|---|---|
| `docs/filosofia.md` | Principios fundacionales y "lo que nu no es". El *porqué* del proyecto. |
| `docs/arquitectura.md` | Vista estática: las capas, el inventario de primitivas del kernel. |
| `docs/modelo-ejecucion.md` | Vista dinámica: concurrencia, comunicación, limitaciones (con diagramas mermaid). |
| `docs/api.md` | **La API v1 del core — la "superficie sagrada".** Firmas y semánticas. |
| `docs/adr.md` | Registro de decisiones técnicas (Architecture Decision Records). |
| `docs/providers.md` | Contrato de la extensión oficial de providers (registro TOML + adaptadores Lua). |
| `docs/agente.md` | Contrato de la extensión oficial `agent` (motor headless: turno, tools, permisos, subagentes). |
| `docs/sesiones.md` | Contrato de persistencia: JSONL append-only. |
| `docs/chat.md` | Contrato de la extensión oficial `chat` (la UI). |
| `docs/guia-plugins.md` | Sabiduría práctica para autores de plugins + checklist. |
| `docs/malla.md` | Contrato de la extensión oficial `mesh` (borrador v0.1; su §11 sigue abierta). |
| `docs/pseudocodigo.md` | **El ejercicio de validación**: rondas de pseudocódigo que torturan la API. |
| `docs/problemas.md` | Grietas que la v1 *necesita* cerradas (hallazgos G##, con estado). |
| `docs/pospuesto.md` | Lo que se decidió no decidir todavía (P##), cada uno con su *disparador* de reapertura. |
| `docs/implementacion.md` | Plan de construcción incremental: una feature por sesión (S##), ordenado por dependencias del kernel. |
| `docs/decisiones-implementacion.md` | Bitácora operativa: decisiones y desviaciones por sesión, por debajo del umbral de `G##`. |

Además, dos carpetas por tipo de artefacto: `docs/audits/` (informes de
auditoría fechados y cerrados) y `docs/archive/` (planes ya ejecutados que solo
conservan valor histórico, como la migración de la VM). Nada de lo que hay ahí
gobierna el diseño actual.

`README.md` es el índice de entrada con el mismo orden de lectura;
[docs/README.md](docs/README.md) es el mapa por capas de `docs/`.

## Las ideas centrales que NUNCA debes contradecir

Antes de proponer cualquier cosa, interiorízalas (detalle en `docs/filosofia.md`
y `docs/adr.md`):

1. **El core no sabe lo que es un agente.** Modelo Emacs/Textadept, no Neovim:
   kernel diminuto de primitivas + intérprete Lua. Agente, MCP, chat, comandos
   slash, providers: **todo son extensiones Lua**, incluidas las oficiales, sin
   privilegio arquitectónico. Vara para casos dudosos: si algo se describe con
   el vocabulario del kernel (plugins, rutas, versiones), es del kernel; si
   necesita vocabulario de producto (agente, chat, tools, token), es de una
   extensión.
2. **Corolario de completitud:** si una feature oficial no se puede construir
   con la API pública, **la API está incompleta** — el arreglo va en la API, no
   en un atajo privilegiado. Este es el motor de las rondas de pseudocódigo.
3. **Lua decide, Go ejecuta.** Todo trabajo pesado (búsqueda, diff, markdown,
   highlighting, streaming HTTP) es primitiva Go, paralela por dentro. CPU
   ardiendo en Lua = señal de que falta una primitiva o de que el trabajo va a
   un worker.
4. **La API del core es sagrada** (`docs/api.md`): pequeña, aburrida, **crece
   solo por adición**; `nu.version.api` se incrementa con cada adición. Romper
   una firma rompe el mundo.
5. **Modelo de concurrencia "del navegador"** (ADR-004): estado Lua principal
   single-threaded con event loop (async por corrutinas, await implícito) +
   workers explícitos opt-in (sin memoria compartida, sin `ui`, paso de
   mensajes JSON-ables) + primitivas Go paralelas. Aislamiento **por tarea, no
   por plugin** (ADR-008); robustez por watchdog + `pcall` en cada frontera de
   hook.
6. **Cero dependency hell:** un binario estático Go (`CGO_ENABLED=0`),
   extensiones oficiales embebidas con `go:embed` pero **inactivas por defecto**
   (ADR-010).

## Convenciones de la API (al editar `docs/api.md` o contratos)

- Namespace global `nu` con submódulos; `require` reservado para módulos de
  plugins. Identificadores en inglés, `snake_case`.
- Notación de firmas: `nu.mod.fn(arg: tipo, opts?: tabla) -> tipo`.
- Marcadores: **⏸ suspende** (solo dentro de una task) y **[W]** (disponible en
  workers). Úsalos consistentemente.
- Async por funciones suspendientes (estilo secuencial), no callbacks ni
  promesas explícitas.
- Errores **estructurados y lanzados**: `error({ code, message, detail? })`,
  capturables con `pcall`. Códigos reservados v1: `ENOENT`, `EEXIST`, `EACCES`,
  `EIO`, `EHTTP`, `ENET`, `ETIMEOUT`, `ECANCELED`, `EBUDGET`, `EINVAL`,
  `ECLOSED`. Las extensiones acuñan los suyos con la misma forma (p. ej.
  `EPROVIDER`).
- Tiempos en milisegundos; rutas UTF-8.
- Namespaces de eventos reservados al core: solo `core:` y `ui:`. Todo lo demás
  (incluido `agent:`) es de un plugin por convención (namespace = nombre del
  plugin); el loader garantiza unicidad de nombre.

## El flujo de trabajo de diseño (cómo se decide aquí)

Este es el corazón del proyecto y debes respetarlo:

1. **Validación por pseudocódigo** (`docs/pseudocodigo.md`). Se escriben
   escenarios reales usando **solo** lo especificado en los contratos. Cada
   punto donde el código no se puede escribir es un **hallazgo**. Las rondas
   acumuladas hasta hoy: Ronda 1 (H1-H3), Ronda 2 "caminos feos" (F1-F5),
   Rondas 3-4 "zonas sin torturar" (G1-G16), Ronda 5 "orquestación por un
   tercero" (G27). Revisiones de coherencia añadieron G17-G23, G26.

2. **Registro de problemas** (`docs/problemas.md`). Las grietas que la v1 *sí*
   necesita cerradas se numeran `G##` y se resuelven **una a una**: se discuten
   opciones, se decide, se aplica la resolución a *todos* los documentos
   afectados, y la entrada pasa a **RESUELTO** con descripción de la
   resolución. El estado vivo (contador y abiertas) está en la **cabecera del
   propio `problemas.md`** — consúltala ahí, no aquí.

3. **Decisiones pospuestas** (`docs/pospuesto.md`). Lo que se decide *no*
   decidir todavía se numera `P##` y **siempre lleva un disparador**: la señal
   concreta que indica que toca reabrirlo. Nada está rechazado; está esperando.
   Cuando un `P##` se reabre y se decide, sale de aquí y entra en el ADR.

4. **ADR** (`docs/adr.md`). Formato ligero: contexto → decisión →
   consecuencias. **Las entradas nunca se reescriben**: si una decisión cambia,
   se añade una nueva que la *reemplaza* (supersede), y la vieja se marca
   "Reemplazada por ADR-NNN". Estados: Aceptada · Propuesta · Abierta ·
   Reemplazada. Hay diez ADRs (ADR-001…ADR-010).

**Reglas de oro del flujo:**

- Una resolución no está hecha hasta que es **coherente en todos los
  documentos**. Si tocas una semántica en `api.md`, busca y actualiza cada
  contrato que la presuponía (`agente.md`, `providers.md`, `sesiones.md`,
  `chat.md`, `guia-plugins.md`). La mayoría de hallazgos G17-G23 nacieron
  justo de contratos que presuponían API inexistente.
- **Respeta los enlaces cruzados.** Los documentos se referencian entre sí con
  rutas relativas (`[api.md](docs/api.md) §3`) y por número de hallazgo/ADR. Al
  resolver algo, deja el rastro: enlaza el cambio desde `problemas.md` y cita
  el `G##`/`F##`/`P##`/`ADR-NNN` que lo motiva.
- No inventes API para tapar un hallazgo sin antes comprobar que el patrón se
  repite y que no se compone con lo existente. Varios "hallazgos" se cierran
  demostrando que ya eran expresables (semáforo con `nu.task.future`, etc.).
- Antes de añadir una primitiva al core, pregúntate si es **vocabulario de
  producto** (entonces va a una extensión) o si la división "Lua decide, Go
  ejecuta" la justifica por rendimiento.

## Cuando implementes (fase de construcción)

Si la tarea es construir el kernel (no diseñar), **el plan manda**:
[docs/implementacion.md](docs/implementacion.md). No improvises el orden ni
juntes features: una sesión = una feature. El estado vive en el repo, no en tu
memoria. Protocolo, sin saltarte pasos:

1. **Antes de tocar nada**, abre `docs/implementacion.md` y lee el **puntero ▶**
   ("Próxima sesión") y la **última fila de la bitácora**. Eso es dónde seguir y
   en qué estado quedó. Implementa **solo** esa sesión; respeta el grafo de
   dependencias (no abras una sesión cuyas dependencias no estén cerradas).
2. **Tests — la lógica clave no se skippea.** Si la sesión está en el
   **inventario 🔒** (§"Política de tests" del plan), lleva tests unitarios Go
   exhaustivos de sus casos límite, **obligatorios**, nombrando el `G##` que
   blindan. Si es un wrapper fino, basta el snippet Lua + el checkpoint; no
   inventes tests de código ajeno. Toda sesión deja `go build ./...` verde.
3. **La API del core es sagrada.** Implementas [api.md](docs/api.md), no lo amplías.
   Si descubres que la API no basta, **párate**: es un hallazgo `G##` que se
   resuelve primero en los documentos (problemas.md → api.md → contratos) y solo
   *después* se implementa. El código nunca corrige la espec por la vía de hecho.
4. **Al terminar, en el mismo commit que la feature:** avanza el puntero ▶,
   marca el tablero si cerraste una fase, y añade fila a la bitácora. Las
   decisiones operativas y desviaciones que no llegan a `G##` se registran en
   `docs/decisiones-implementacion.md` (una entrada por sesión). Si cierras
   una fase, ejecuta antes su **checkpoint de integración (🔎)**; si falla, no
   avances el puntero. Un commit que toca código sin mover el puntero es una
   sesión a medias.
5. **Commit en español** citando la sesión (`S07: ...`) y el `G##` si lo hubo.

El plan tiene todo el detalle (las 45 sesiones, los 11 checkpoints, el
inventario 🔒 y los hitos de veto). Esta sección solo garantiza que lo
**consultes y lo sigas** aunque arranques sin más contexto que este fichero.

## Agentes y skills del flujo

El flujo de arriba está mecanizado en [.claude/README.md](.claude/README.md)
(el mapa): skills `/planificar-sesion`, `/sesion`, `/hallazgo`, `/ronda`,
`/juicio` y `/mutacion`, y los agentes clean-room (jueces, verificador,
auditor, escenarista BDD). Ante una tarea de desarrollo, consulta el mapa y
usa la skill que corresponda en vez de improvisar el protocolo. Las reglas de
no-contaminación de los jueces (qué reciben y qué herramientas tienen) no se
relajan.

## Convenciones de Git

- **Modelo de ramas (desde 2026-07-14):** `develop` es la rama de integración
  y la **rama por defecto** del repo — ahí aterriza todo el trabajo y de ahí
  salen las versiones *no estables*. `main` queda reservada para **versiones
  estables**: solo recibe merges desde `develop` cuando se corta una estable,
  nunca trabajo directo.
- **Rama de trabajo:** desarrolla en la rama indicada por la tarea (p. ej.
  `claude/...`); créala localmente si no existe y ábrela **desde `develop`**.
  Nunca empujes a otra rama sin permiso explícito.
- **Mensajes de commit en español**, descriptivos y referenciando el hallazgo
  cuando aplique. Estilo observado en el historial:
  - `Resuelve G27: nu.task.all alinea resultados con inputs (Promise.all)`
  - `Ronda 5 de pseudocódigo: orquestación de agentes (loops deterministas + paralelo)`
  - `Resolve G6 (per-function caps) and add ADR-010 (official extensions opt-in)`
- `git push -u origin <rama>`; reintenta solo ante fallos de red (backoff
  exponencial 2s/4s/8s/16s).
- **No abras un Pull Request salvo que se pida explícitamente.**
- **Licencia:** el proyecto es **Apache 2.0** ([LICENSE](LICENSE)), copyright de
  Diego Barea, que conserva la titularidad (ADR-013). No introduzcas código de
  terceros con licencia incompatible ni cabeceras de copyright ajenas sin
  acordarlo; las contribuciones externas se rigen por [CONTRIBUTING.md](CONTRIBUTING.md).

## Glosario de prefijos de seguimiento

- **ADR-NNN** — decisión técnica registrada (en `adr.md`).
- **G##** — grieta/hallazgo que la v1 necesita cerrar (en `problemas.md`).
- **H##, F##** — hallazgos de las rondas 1 y 2 de pseudocódigo (ya resueltos e
  integrados en la API).
- **P##** — discusión pospuesta con su disparador (en `pospuesto.md`).
- **§N** — referencia a una sección numerada dentro de un documento.
- **⏸** — función suspendiente (solo en task). **[W]** — disponible en workers.
