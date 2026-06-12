# Problemas abiertos

Lista de trabajo viva: grietas encontradas en las rondas de validación
([pseudocodigo.md](pseudocodigo.md)) y revisiones posteriores que están
**pendientes de resolver**.
Método: se resuelven una a una, discutiendo opciones; al decidirse, la
resolución se aplica a los documentos afectados y la entrada pasa a
"Resuelto" con enlace al cambio. Distinto de [pospuesto.md](pospuesto.md):
aquello es lo que decidimos no decidir; esto son agujeros que la v1 sí
necesita cerrados.

**Estado: 22/22 resueltas** (2026-06-12). Las dieciséis de las rondas
3-4 y las seis de la revisión de coherencia de la documentación completa
(G17-G22, sobre todo contratos que presuponían API inexistente) están
cerradas. La lista queda como registro del proceso; los problemas nuevos
que surjan (spike incluido) se añaden aquí con el mismo método.

---

## G1 · Comportamiento ante resize — `api.md` §9 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §9.1 y
[guia-plugins.md](guia-plugins.md) §6): regla dura en el core — las
regiones fuera de pantalla se recortan sin error y conservan sus
coordenadas; recolocarse es del dueño (convención "tu región, tu
`ui:resize`"); el relayout automático es del toolkit. Anclajes
declarativos en `region{}` descartados: sería congelar un mini-lenguaje de
layout en la API sagrada — el patrón de la casa es "el core da garantías,
no comodidades".

**Problema.** Una región que queda fuera (o parcialmente fuera) de la
pantalla tras un resize tiene comportamiento indefinido, y no hay
convención sobre quién recoloca qué: el picker del escenario 12 queda roto
o flotando.

**Impacto.** Todo plugin con UI propia; el toolkit lo necesita resuelto
antes del spike.

**Opciones.** (a) Solo reglas duras: las regiones se recortan a pantalla
sin error, y la convención es "tu región, tu `ui:resize`"; (b) además,
anclajes declarativos en `region{}` (`x = "center"`, `w = "80%"`) que el
compositor reaplica solo en cada resize; (c) delegarlo todo al toolkit y
que el raw `nu.ui` sea explícitamente "a tu suerte".

## G2 · Hot-reload de plugins (ciclo de desarrollo) — loader / `api.md` §14 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §14 y §4):
`nu.plugin.reload(name)` best-effort — handles etiquetados por dueño,
evento `core:plugin.unload` para que las extensiones limpien sus
registros, caché de require vaciada, init.lua recargado. Herramienta de
desarrollo, no garantía de producción. El reinicio-con-`--continue` se
descartó como historia de DX (pierde estado de UI/plugins); posponer
dolía justo donde se ganan los primeros autores.

**Problema.** Iterar sobre un plugin exige reiniciar nu: `require` cachea,
re-ejecutar `init.lua` duplicaría registros, y aunque todos los registros
devuelven handles, nadie los rastrea por plugin (no existe "deshaz todo lo
de X"). Lo mismo aplica a recargar `providers.toml` / `nu.toml` en
caliente.

**Impacto.** DX de la comunidad de plugins — el público objetivo del
proyecto. No bloquea contratos.

**Opciones.** (a) El core rastrea ownership de handles por plugin (ya sabe
`plugin.current()` en cada registro) y ofrece `nu.plugin.reload(name)`;
(b) sin reload: comando de reinicio rápido de nu que repone la sesión
(`--continue` ya casi lo da); (c) posponer con disparador (P-nuevo).

## G3 · Multi-sesión: atribución de eventos y modales concurrentes — `agente.md` §4 / `chat.md` — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §4-§5 y
[chat.md](chat.md) §1/§2/§5): `session` obligatorio en todo payload
`agent:*` (emitido vía helper único); `chat` pinta solo la sesión activa y
señala el resto en statusline; modales en cola FIFO etiquetados por
sesión, **sin timeout** en los asks (un timeout→deny sería no
determinista) — la UI hace visibles los pendientes. Descartado el
namespacing por sesión en el nombre del evento (el bus no tiene wildcards
y un campo lo resuelve gratis).

**Problema.** Los payloads `agent:*` no obligan a llevar `session_id`
(dos sesiones concurrentes mezclarían deltas), `chat.md` no especifica
filtrado, y dos `permission.asked` simultáneos abrirían dos modales sobre
la misma pila de input sin orden definido.

**Impacto.** Los subagentes ya hacen esto real en v1 — no es un caso
futuro. Contrato congelable afectado.

**Opciones.** (a) `session_id` obligatorio en todo payload `agent:*` +
`chat` filtra por sesión activa + cola FIFO de modales (uno visible a la
vez); (b) además, namespacing de eventos por sesión
(`agent:<id>:delta`) para suscripciones selectivas baratas.

## G4 · Reentrada de `Session:send` — `agente.md` §2 — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §2): `send` con turno en
vuelo encola; el loop inyecta lo encolado al ensamblar el siguiente request
(nunca a mitad de stream). `cancel()` no vacía la cola
(`clear_queue()` aparte). Descartado `EBUSY` (cada UI reimplementaría la
cola de forma sutilmente distinta — justo lo que se quería evitar).

**Problema.** Llamar `send` con un turno en vuelo no está definido:
¿error, cola, o cancelar-y-reemplazar? Cada UI improvisaría una semántica
distinta.

**Impacto.** Contrato congelable; afecta a la UX básica (enter impaciente).

**Opciones.** (a) `EBUSY` y que la UI decida (mínimo, predecible); (b) el
motor encola mensajes y los anexa al siguiente turno (lo que hacen los
harnesses maduros); (c) configurable por sesión.

## G5 · Doble reanudación de la misma sesión — `sesiones.md` — **RESUELTO**

**Resolución** (aplicada en [sesiones.md](sesiones.md) §6): un escritor por
sesión vía lockfile `<sesión>.jsonl.lock` con `{pid, hostname, started}`;
lectores sin lock; locks huérfanos (pid muerto local) se limpian en
silencio; conflicto real → aviso con fork por defecto / solo lectura /
forzar con confirmación. `flock` descartado (semántica impredecible en
Windows/red); auto-fork silencioso descartado (bifurca sin conocimiento
del usuario).

**Problema.** Dos procesos nu pueden abrir el mismo JSONL y hacer appends
intercalados: corrupción silenciosa. No hay lock.

**Impacto.** Pérdida de datos del usuario; barato de cerrar ahora, caro
después.

**Opciones.** (a) Lockfile junto al JSONL (`.lock` con pid; el segundo
proceso recibe error claro y ofrece fork); (b) lock advisory del SO
(flock) — ¿portabilidad Windows?; (c) detectar-y-fork automático: el
segundo `--continue` crea fork silenciosamente.

## G6 · Granularidad de `caps` — `api.md` §13 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §13, [agente.md](agente.md)
§9, guía §3; nueva ADR-010): mecanismo por función en el core (dos
granularidades: `"fs"` módulo, `"fs.read"` función; deny-by-default para
funciones futuras), vocabulario como tablas inspeccionables de la
extensión del agente (`agent.caps.FS_RO`). Los paquetes curados en el core
se descartaron (esconden juicios y redistribuyen poder retroactivamente al
crecer la API); el scoping por rutas va a [P17](pospuesto.md). Derivada:
ADR-010 — las extensiones oficiales se distribuyen embebidas pero
**inactivas por defecto**, activación explícita de una tecla.

**Problema.** `caps` concede módulos enteros: `"fs"` incluye `write` y
`remove`. El subagente auditor de solo lectura — el caso estrella del
sandboxing — no se puede expresar.

**Impacto.** Una de las features diferenciales (permisos duros) se queda
corta en su mejor caso de uso.

**Opciones.** (a) Caps con sufijo de modo: `"fs:ro"` (lista corta y
curada de variantes por módulo, sin inventar un lenguaje de policies);
(b) caps por función (`"fs.read"`, `"fs.stat"`): expresivo pero
N×funciones de superficie a congelar; (c) scoping por ruta además del
modo (`fs:ro:/repo`): el más potente y el más caro de especificar bien;
(d) dejar módulo-entero en v1 y anotar en pospuestos.

## G7 · Semántica de `fs.watch` — `api.md` §5 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §5): `watch(path, opts?, fn)`
con `recursive`, `gitignore = true` por defecto y entrega en lotes con
debounce (`fn(events[])`, ~50 ms). La versión mínima se descartó: habría
obligado a cada consumidor a reimplementar recursión+ignores+debounce en
Lua — trabajo proporcional al repo en el estado principal, contra "Lua
decide, Go ejecuta".

**Problema.** Sin definir: ¿recursivo?, ¿respeta `.gitignore`?
(vigilar `node_modules/` = ruido infinito), ¿coalescing de ráfagas?
(un `git checkout` toca miles de ficheros → miles de callbacks).

**Impacto.** Cualquier plugin de auto-contexto o recarga; riesgo de
rendimiento en el estado principal.

**Opciones.** (a) `watch(path, opts, fn)` con `opts = { recursive,
gitignore = true, debounce_ms = 50 }` y entrega de eventos en lotes
(`fn(events[])`); (b) mínimo v1: un path, sin recursión (los plugins
componen), y a pospuestos lo demás.

## G8 · `on_message` vs `recv` simultáneos — `api.md` §13 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §13): mutuamente excluyentes,
`EINVAL` en el acto al registrar uno con el otro pendiente. Prioridad
silenciosa descartada (esconde el bug); competencia por cola descartada
(no determinismo de serie).

**Problema.** Son "alternativas" pero nada impide usar ambas sobre el
mismo worker: ¿quién recibe el mensaje? Indefinido.

**Impacto.** Menor, pero es exactamente el tipo de indefinición que
genera bugs irreproducibles.

**Opciones.** (a) Mutuamente excluyentes: registrar `on_message` con un
`recv` pendiente (o viceversa) lanza `EINVAL`; (b) `on_message` gana
siempre y `recv` tras él lanza; (c) cola única y cualquier consumidor
compite (no determinista — probablemente descartable).

## G9 · Alcance Windows en v1 — transversal — **RESUELTO**

**Resolución**: v1 soporta Linux y macOS nativos; en Windows, **nu se usa
dentro de WSL2** (documentado como requisito, no como apología). Ventaja
decisiva: dentro de WSL2 el contrato POSIX se cumple íntegro — cero
especificación condicional, cero shell portable, cero semántica dual de
señales. Windows nativo queda en pospuestos ([P18](pospuesto.md)) con su
disparador. La promesa "cross-compile a todas las plataformas" se matiza en
la arquitectura: el binario *compila* para Windows, el soporte v1 es WSL2.

**Problema.** La tool `bash` asume `sh`, `Proc:kill` habla señales POSIX,
y el input de terminal difiere (IME, teclas). Go cross-compila a Windows,
pero "compila" no es "funciona bien". Sin decisión de alcance, cada
contrato asume POSIX en silencio.

**Impacto.** Decisión de producto más que técnica; condiciona promesas de
la distribución ("un binario para todas las plataformas").

**Opciones.** (a) v1 = Linux/macOS de primera + Windows best-effort
documentado (la tool bash exige WSL o git-bash); (b) Windows de primera
desde v1 (coste alto: shell portable, semántica kill, pruebas de
terminal); (c) v1 sin Windows, explícitamente.

## G10 · Reentrada del bus de eventos — `api.md` §4 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §4): despacho sobre snapshot
de suscriptores; cancelación con efecto inmediato; suscritos durante el
despacho solo ven eventos futuros; emits anidados encolados (anchura, no
profundidad — el ping-pong infinito se vuelve bucle plano que corta el
watchdog). Recursión en profundidad descartada (desborde de pila + orden
sorpresa); `defer` obligatorio descartado (la UI iría un tick por detrás).

**Problema.** `emit` dentro de un handler (¿recursión o cola?), suscribir
o cancelar durante el despacho (¿el handler nuevo ve el evento en curso?
¿el cancelado a mitad se ejecuta?): todo indefinido. Produce bugs
dependientes del orden de carga de plugins.

**Impacto.** Núcleo del modelo de extensión; barato de definir, imposible
de cambiar después.

**Opciones.** (a) Despacho sobre snapshot de la lista de handlers + emits
anidados encolados al final del despacho en curso (sin recursión); (b)
despacho recursivo en profundidad con límite anti-ciclos; (c) emits
anidados via `task.defer` obligatorio (más simple en el core, más
sorpresa para el autor).

## G11 · Datos no-UTF-8 en las fronteras JSON — `api.md` §12 / transversal — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §12 y guía §5): el codec es
estricto (`encode` lanza `EINVAL` ante UTF-8 inválido) y las tools sanean
en el origen, visiblemente (`[output binario: NKB omitidos]`). Base64
automático descartado (blob inesperado para el LLM, ambigüedad para el
lector); `U+FFFD` silencioso en el codec descartado (esconde corrupción en
todas las fronteras — sanear es decisión con contexto).

**Problema.** Un tool result con bytes binarios (cat de un PNG) cruza
tres fronteras que asumen JSON/UTF-8 (request al provider, transcript
JSONL, mensajes de worker) sin regla definida: ¿lanzar, reemplazar,
base64? El bug aparecería lejos del origen.

**Impacto.** Robustez básica de la tool `bash` — pasará el primer día.

**Opciones.** (a) `nu.json.encode` lanza `EINVAL` ante UTF-8 inválido y
las tools sanean (reemplazo lossy + nota "output binario truncado") —
regla en la guía y en la tool oficial; (b) base64 automático con marca;
(c) reemplazo silencioso con U+FFFD en el codec (cómodo, pero esconde
corrupción).

## G12 · TLS/proxy para endpoints corporativos — `api.md` §8 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §8): `opts.tls = { ca_file?,
insecure? }` en `request`/`stream`; las variables de entorno
`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` se respetan por defecto (el estándar
de facto corporativo); defaults globales en `[net]` de `nu.toml`
sobreescribibles por petición.

**Problema.** El "proxy corporativo" es caso anunciado en la filosofía,
pero `nu.http` no tiene opciones TLS (CA propia, insecure) ni política de
proxy (¿se respeta `HTTPS_PROXY`?). El caso no se puede configurar.

**Impacto.** Adopción en empresas — público natural de un binario sin
dependencias.

**Opciones.** (a) `opts.tls = { ca_file?, insecure? }` + respetar
`HTTP(S)_PROXY`/`NO_PROXY` por defecto (documentado); (b) además,
configuración global en `nu.toml` para no repetirlo por petición.

## G13 · Providers por suscripción (OAuth) — `providers.md` / `api.md` — **RESUELTO**

**Resolución** (aplicada en [providers.md](providers.md) §4 y guía §7):
camino v1 sin listener — device flow o pegado manual de código (patrón
`gh`/`gcloud`), escribible con `http.request` + `nu.proc`; tokens en
`data_dir()/plugins/<nombre>/` con `0600`, en claro (coherente con P7). El
listener localhost (`listen_once`) va a [P19](pospuesto.md) con disparador
"provider real sin device flow ni pegado de código".

**Problema.** El device flow es escribible con lo que hay (polling +
abrir URL), pero el flujo con callback localhost no: no existe primitiva
de listener HTTP. Y no hay convención de dónde/cómo guarda un adaptador
sus refresh tokens.

**Impacto.** Los planes de suscripción (no API key) son cada vez más
comunes; decide si nu los soporta de primera.

**Opciones.** (a) Bendecir device flow como el camino v1 + convención de
almacenamiento de tokens (`plugins/<nombre>/`, `0600`) y nada de
listener; (b) añadir un listener HTTP mínimo (`nu.http.listen_once` para
callbacks de OAuth, efímero, solo loopback) — superficie pequeña y
acotada; (c) posponer OAuth entero con disparador.

## G14 · Modelo de confianza del contenido del repo — `agente.md` §6-§7 / transversal — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §11): el repo no es el
usuario. (1) La config del repo **solo recorta** permisos: sus `deny` se
honran, sus `allow`/`mode` se ignoran. (2) **TOFU de una tecla** por repo
para skills y `nu.md` (patrón `:trust` de Neovim); sin sí explícito
(incluido headless), no se inyectan. Las descripciones de tools MCP quedan
como responsabilidad del usuario (instalar un servidor es acto consciente).

**Problema.** Abrir nu en un repo clonado ya ejecuta la voluntad del
repo: sus `.nu/skills/` se inyectan al system prompt y su
`.nu/agent.toml` puede ampliar permisos (`allow = ["bash:*"]`) por la
precedencia proyecto > global. Las descripciones de tools de servidores
MCP de terceros son el mismo agujero (texto no confiable al modelo). No
hay trust-on-first-use ni distinción entre config inocua y config
peligrosa.

**Impacto.** **El problema de seguridad más serio de la lista**: convierte
"clonar y abrir" en vector de ataque. Hay que resolverlo antes de
congelar el contrato del agente.

**Opciones.** (a) Trust-on-first-use por directorio (primer arranque en
un repo: diálogo "¿confías?"; sin confianza: se ignoran skills y config
del repo); (b) TOFU granular: la config del repo se divide en inocua
(siempre) y sensible (permisos: NUNCA ampliables desde el repo, solo
recortables — los `allow` del proyecto requieren confirmación explícita);
(c) ambas: TOFU para skills/contexto + regla dura "el repo solo recorta
permisos, jamás amplía".

## G15 · El interior de un worker: scheduler propio y watchdog — `api.md` §13 / `modelo-ejecucion.md` — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §13): cada worker es un
mini-runtime completo (scheduler propio, multi-task, timers, futures) y
**sin watchdog** — los workers existen para quemar CPU a gusto; el control
es `terminate()` + `caps`. El watchdog configurable se descartó: un mando
sin modelo de amenaza (no hay UI dentro que proteger).

**Problema.** `task` es [W] y el escenario 4 ya asumió multiplexar con
`race` dentro del worker, pero nunca se escribió que cada worker tenga su
propio event loop, ni si admite múltiples tasks y timers, ni si el
watchdog aplica dentro (¿con qué presupuesto, si no hay UI que proteger?).

**Impacto.** Clarificación de contrato; el escenario 4 depende de ello.

**Opciones.** (a) Cada worker = mini-runtime completo (loop propio,
multi-task, timers) sin watchdog (no hay UI que proteger; `terminate()`
es el control); (b) igual pero con watchdog configurable (protege de
workers zombis quemando CPU).

## G16 · Subagentes paralelos escribiendo los mismos ficheros — `agente.md` §9 — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §9): limitación conocida
documentada + remedio prescrito (repartir territorio vía prompt, como los
harnesses de referencia). Lock en tools oficiales descartado: seguridad
falsa — bash y tools de terceros escriben sin pasar por él, prometería una
garantía incumplible ("casi bien es peor que no"). Detección a posteriori
descartada por el mismo agujero de cobertura.

**Problema.** Las tools de subagentes paralelos se intercalan en el
principal, pero nada coordina dos escrituras al mismo path:
last-write-wins silencioso.

**Impacto.** Calidad de resultados con subagentes paralelos; los
harnesses de referencia tampoco lo resuelven (mitigan repartiendo
territorio vía prompt).

**Opciones.** (a) Documentar como limitación conocida + guía ("reparte
territorio entre subagentes"); (b) lock advisory por fichero dentro de la
sesión (las tools oficiales de escritura lo respetan, aviso al chocar);
(c) detección a posteriori (aviso si dos subagentes tocaron el mismo
path).

## G17 · El lockfile de sesiones no es implementable con la API actual — `api.md` §5-§7 / `sesiones.md` §6 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §1.4/§5/§6/§7 y
[sesiones.md](sesiones.md) §6): tres primitivas genéricas mínimas —
`opts.exclusive = true` en `nu.fs.write` (creación atómica
solo-si-no-existe vía `O_EXCL`, sin temporal+rename, lanza el nuevo código
reservado `EEXIST`), `nu.proc.alive(pid)` (existencia, no identidad: un
pid reciclado da `true`) y `nu.sys.hostname()`. El lockfile sigue siendo
lógica de la extensión del agente, en Lua. El `nu.fs.lockfile` dedicado se
descartó (metería la política de sesiones — pids, huérfanos, hostnames —
en el kernel: el core da garantías, no comodidades); el best-effort se
descartó ("casi bien es peor que no").

**Problema.** La resolución de G5 exige tres piezas que [api.md](api.md)
no tiene: (1) creación **exclusiva** de fichero — `nu.fs.write` es atómico
vía temporal+rename, pero rename *sobreescribe*: dos procesos pueden
"ganar" el lock a la vez; (2) comprobar si un `pid` ajeno está vivo
(`nu.proc` solo gestiona hijos propios) — necesario para limpiar locks
huérfanos; (3) el `hostname` (no está en `nu.sys`) — necesario para el
contenido del lock.

**Impacto.** G5 quedó resuelto en prosa pero no se puede escribir con la
API especificada; la corrupción de sesiones que cerraba sigue siendo
posible. Mismo tipo de grieta que cazaban las rondas de pseudocódigo —
esta se escapó porque G5 se resolvió sin escribir el código.

**Opciones.** (a) Tres primitivas mínimas: `opts.exclusive = true` en
`nu.fs.write` (lanza si el fichero existe), `nu.proc.alive(pid) ->
boolean`, `nu.sys.hostname() -> string`; (b) una primitiva dedicada
`nu.fs.lockfile(path, meta) -> Lock` que empaquete la semántica completa
de sesiones.md §6 (menos superficie general, más opinionada); (c) rebajar
G5 a best-effort (asumir la carrera como improbable) — probablemente
descartable: "casi bien es peor que no".

## G18 · Reanudar una sesión no tiene API — `agente.md` §2 — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §2 y
[chat.md](chat.md) §4/§8): `agent.session{ resume = id }` — una sola
función, dos modos. Reabre con replay del transcript (sesiones.md §3) y
adquisición del lock de escritor (§6); el resto de `opts` es estado
efímero, no se persiste. `agent.resume()` aparte se descartó (firma
duplicada sin ganancia); reanudar-como-fork se descartó (bifurca el
historial en cada reanudación). El azúcar CLI (`nu --continue`) queda
deliberadamente fuera de los contratos: pertenece a la superficie CLI
(cuestión abierta 5 de [arquitectura.md](arquitectura.md)).

**Problema.** `agent.session(opts)` solo crea sesiones nuevas (sus `opts`
no admiten id). Pero [chat.md](chat.md) §8 (`nu --continue`, picker de
`/sessions`) presupone reanudación, y [sesiones.md](sesiones.md) §7
describe el listado que la alimenta. Falta el punto de entrada.

**Impacto.** Contrato congelable; la feature está prometida en dos
documentos.

**Opciones.** (a) `agent.resume(id) -> Session` (replay de sesiones.md §3
+ lock de §6); (b) `agent.session{ resume = id, ... }` (una sola función,
dos modos); (c) reanudar = fork del último punto (unifica mecánica con §5
pero bifurca el historial en cada reanudación — probablemente
descartable).

## G19 · Cambio de modelo a mitad de sesión sin API — `agente.md` §2 / `chat.md` §4 — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §2 y
[chat.md](chat.md) §4): `Session:set_model("proveedor/modelo")` — valida
contra el registro de providers, escribe la entrada `event` en el
transcript (sesiones.md §3) y aplica desde el siguiente request; con un
turno en vuelo, al ensamblar la siguiente iteración (como la cola de G4),
nunca a mitad de un stream. `Session.model` mutable descartado (sin punto
claro de validación ni de registro en el transcript); fork-por-modelo
descartado (fragmenta sesiones para una operación cotidiana).

**Problema.** `/model` existe en `chat` (picker desde `providers.list()`)
y [sesiones.md](sesiones.md) §3 pone "cambio de modelo a mitad de sesión"
como ejemplo canónico de entrada `event`, pero `Session` no expone ninguna
forma de cambiarlo: `opts.model` solo existe en la creación.

**Impacto.** Feature básica de UX, presupuesta por dos contratos.

**Opciones.** (a) `Session:set_model("proveedor/modelo")`: valida contra
el registro, escribe la entrada `event` y aplica desde el siguiente
request; (b) `Session.model` mutable (menos explícito, sin punto claro de
validación); (c) sin cambio en caliente: `/model` hace fork con el modelo
nuevo (consistente con append-only, pero fragmenta sesiones).

## G20 · Detección de interactividad (TTY/headless) — `api.md` / `agente.md` §5 / `chat.md` §8 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §2/§9, [agente.md](agente.md)
§5 y [chat.md](chat.md) §8): en headless el módulo `nu.ui` directamente
**no existe**; el test es `nu.has("ui")` — coherente con el
deny-by-default de las `caps` de workers (la superficie no concedida no
está) y sin primitiva nueva. `nu.ui.interactive()` se descartó (un módulo
de UI presente pero "apagado" invita a llamadas que no pintan nada);
exponer el modo de arranque en `nu.sys` se descartó como redundante con
lo anterior.

**Problema.** El default-deny de permisos en headless y "chat solo se
activa en TTY interactivo" dependen de saber si hay terminal; ninguna
primitiva lo dice (el pseudocódigo del turno usa un `interactive()` que
no existe).

**Impacto.** El pipeline de permisos — una decisión de seguridad — apoya
su rama principal en una función sin especificar.

**Opciones.** (a) `nu.ui.interactive() -> boolean` (o un cap:
`nu.has("ui.tty")`); (b) en headless el módulo `nu.ui` directamente no
existe y el test es `nu.has("ui")` — coherente con caps de workers
(deny-by-default de superficie); (c) exponer el modo de arranque en
`nu.sys` (`nu -e` = headless por definición).

## G21 · El primer arranque de ADR-010 no tiene dueño — ADR-010 / `api.md` §14 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §14,
[filosofia.md](filosofia.md) §2 y [arquitectura.md](arquitectura.md)):
opción (a), reencuadrada con la formulación general del principio — **el
kernel solo conoce sus propias capacidades** —, bajo la cual esto no es
una excepción: las extensiones embebidas y su activación son capacidad
del loader, así que la pregunta es del kernel. El runtime desnudo (TTY +
ningún plugin activo) pinta una **pantalla fija de runtime**: versión y
API, rutas, extensiones embebidas y acciones (activar el conjunto
oficial, activar sueltas, salir) — render fijo, pre-Lua, sin lógica de
producto; es la cara permanente de nu sin plugins, no un diálogo de
primera vez. El apetito de "algo usable sin el harness" lo cubre una
extensión oficial más: **`repl`** (REPL de Lua sobre la API pública),
activable sola desde esa pantalla. Descartados: la extensión bootstrap
siempre-activa (un plugin privilegiado sin precedente, y exigiría añadir
activación de plugins en runtime a la API sagrada solo para esa
pantalla) e imprimir-y-salir (contradice la "una tecla" de ADR-010 y la
filosofía §5).

**Problema.** Con las extensiones oficiales inactivas por defecto y un
core que no pinta ni sabe de agentes (`nu.log` "nunca a la pantalla"),
¿qué código muestra el ofrecimiento de activación "de una tecla" del
primer arranque? La consecuencia central de ADR-010 no tiene mecanismo.

**Impacto.** La primera experiencia del usuario — exactamente lo que
ADR-010 dice proteger.

**Opciones.** (a) Excepción mínima y declarada en el loader: si no hay
plugins activos y hay TTY, el core pinta un prompt fijo de activación
(la única UI del core, deliberadamente trivial); (b) una extensión
oficial `bootstrap` siempre activa que hace solo esto (¿contradice el
"ninguna se activa sola" de ADR-010?); (c) sin UI: el binario imprime
instrucciones (`nu --enable-official`) y sale — austero pero hostil.

## G22 · Resolución de colores semánticos entre core y toolkit — `api.md` §9.2 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §9.2,
[arquitectura.md](arquitectura.md) y guía §6): opción (b) — el core solo
acepta colores **literales** (`#rrggbb`, índice 0-255; degradados a
`nu.ui.caps().colors` al pintar); el vocabulario semántico y los themes
son enteramente del toolkit, que resuelve nombre → literal al construir
los Blocks. Razón decisiva: no congelar un único modelo de theming en la
API sagrada — una paleta global del core restringiría a toolkits
alternativos con modelos más ricos; en espacio de extensiones el theming
puede competir e iterar. Mitigaciones de los costes conocidos: el árbol
retenido del toolkit re-renderiza solo al cambiar de theme (sus
consumidores cambian en vivo gratis); los plugins de `nu.ui` crudo que
usen colores del theme se suscriben a su evento de cambio (misma
convención que `ui:resize`: tu región, tu repintado); el cambio en vivo
para plugins que no cooperan se asume imperfecto. Descartadas: (a) tabla
`nu.ui.theme` en el core (bendice un modelo único y mete vocabulario de
theming en la API sagrada); (c) estilos por referencia (mucha superficie
para el mismo resultado).

**Problema.** Un `Style` del core acepta nombres semánticos (`"accent"`,
`"error"`), pero los themes son plugins del toolkit
([chat.md](chat.md) §7): no está definido quién traduce nombre → color
concreto, ni cuándo (¿al construir el Block o al pintar?).

**Impacto.** `Style` es API sagrada; el theming entero (y la regla "solo
colores semánticos" de la guía §6) depende de esta pieza.

**Opciones.** (a) Registro mínimo en el core — `nu.ui.theme(tabla)`
define la paleta semántica; los themes (plugins del toolkit) la llaman y
el compositor resuelve al pintar (cambiar de theme repinta todo, los
Blocks no se rehacen); (b) los nombres semánticos no son del core: el
toolkit resuelve a colores concretos antes de construir Blocks y `Style`
solo acepta colores literales (core más puro; pero cada Block queda
"horneado" con su theme y la guía §6 pasaría a ser regla del toolkit);
(c) indirection por referencia en el Block, resuelta al pintar (la más
flexible y la más cara de especificar).
