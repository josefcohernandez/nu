# Problemas abiertos

Lista de trabajo viva: grietas encontradas en las rondas de validación
([pseudocodigo.md](pseudocodigo.md)) y revisiones posteriores que están
**pendientes de resolver**.
Método: se resuelven una a una, discutiendo opciones; al decidirse, la
resolución se aplica a los documentos afectados y la entrada pasa a
"Resuelto" con enlace al cambio. Distinto de [pospuesto.md](pospuesto.md):
aquello es lo que decidimos no decidir; esto son agujeros que la v1 sí
necesita cerrados.

**Estado: 35 registradas, resueltas** (G36 y G37 añadidas 2026-06-28 al pulir la
UI/UX de las extensiones oficiales para que parezcan producto: G36, el doble
auto-montaje de chat+repl; G37, un bug latente del eje X de `blitBlock`; G35 añadida
2026-06-27 al usar el binario tras el onramp de ADR-015; G34 añadida 2026-06-27 al
validar con pseudocódigo el
control de razonamiento; G33 añadida 2026-06-23 al probar el
binario con las extensiones oficiales; G32 añadida 2026-06-22 desde la
construcción de la extensión sesiones). Las dieciséis de las
rondas 3-4, las seis de la revisión de coherencia de la documentación
completa (G17-G22, sobre todo contratos que presuponían API inexistente) y
las de la revisión filosófico-técnica del proyecto (G23, vocabulario de
producto en la API sagrada; G26, namespaces de extensión reservados al
core) están cerradas. La numeración salta de G23 a G26 porque G24-G25 son
grietas de la misma revisión en curso, registradas en sus propias ramas;
G27 sale de la ronda 5 de pseudocódigo (orquestación de agentes por un
tercero). G28-G30 salen de la ronda 6 (reconstruir un harness de coding
estilo claude-code sobre `nu.ui`): G28 (blit recorta por ambos extremos,
scrollback), G29 (hit-testing del ratón es del toolkit, mismo reparto que
G1/G22) y G30 (pegar imágenes inyecta una ruta). G31 es la primera grieta
que sale de la **construcción** y no de una ronda de pseudocódigo: gopher-lua
no deja ceder una corrutina a través de `pcall`/tail call, lo que obligó a
realizar el scheduler sin yields (ADR-011). G32 es la segunda que sale de la
construcción (la extensión sesiones, S38): el lock de §6 necesita el pid del
proceso *propio* y la API no lo exponía — el cabo suelto de G17. G33 es la
tercera de la construcción y la primera de *usar* el binario terminado: el
arranque sin TTY no tenía onramp (la pantalla desnuda de G21 es solo-TTY) y
"el conjunto oficial" estaba sin definir frente a `example` — resuelta con el
flag `nu --default-config` y ADR-015 (sin tocar la API sagrada: es superficie
CLI). G35 es la **segunda** de *usar* el binario terminado: ese mismo onramp
activa los siete plugins pero **no deja config de agente** (modelo/provider), así
que el primer `nu` muere sin modelo y deja la UI atrapada — resuelta con ADR-017
(plantillas activas en el onramp + degradación con gracia del chat). La lista queda
como registro del proceso; los problemas nuevos que surjan (spike incluido) se
añaden aquí con el mismo método.

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

## G23 · Vocabulario LLM en la API sagrada (`nu.text.approx_tokens`) — `api.md` §10 / `providers.md` §5 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §10, [providers.md](providers.md)
§4/§5 y [agente.md](agente.md) §8): la primitiva **sale del core**. Falla
las dos varas a la vez: "token LLM" es vocabulario de producto
([filosofia.md](filosofia.md) §2), y la heurística (~4 bytes/token) es una
división en Lua puro — sin trabajo pesado no hay primitiva que justificar
("Lua decide, Go ejecuta"). A diferencia de markdown/highlighting, cuya
concesión la sostiene el rendimiento, esta no tenía sostén. El helper pasa
a la extensión de providers — dueña del vocabulario de tokens y del
`count_tokens?` exacto — como `providers.approx_tokens(s)`, en Lua.
Renombrar en el core a algo neutro se descartó (cualquier nombre seguiría
existiendo solo para estimar tokens: maquillaje, no resolución); mantenerla
como concesión documentada se descartó (sin coste de rendimiento que la
justifique, sentaría el precedente de que la vara de filosofía §2 es
negociable en la propia superficie sagrada).

**Problema.** `api.md` §10 exponía `nu.text.approx_tokens(s)` documentada
como "estimación heurística de tokens LLM", mientras `providers.md` §5
afirmaba en la misma frase que el conteo de tokens es "nunca del core
(ADR-003: el core no sabe lo que es un LLM)". La vara de filosofía §2 —
vocabulario de producto = extensión — quedaba desautorizada dentro de la
propia API sagrada.

**Impacto.** Filosófico más que funcional, pero sobre la superficie que se
congela: lo que entre con vocabulario de producto no se puede descongelar,
y debilita el argumento del kernel mínimo ante cada caso dudoso futuro.

**Opciones.** (a) Renombrar en el core a vocabulario neutro
(`bytes_estimate` o similar); (b) mantener como concesión documentada,
estilo markdown/highlighting; (c) eliminar del core y mover el helper a la
extensión de providers (una línea de Lua).

## G26 · Namespaces de extensión reservados al core — `api.md` §4 / guía §7 / `agente.md` §4 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §4 y §14, [guia-plugins.md](guia-plugins.md)
§7 y [agente.md](agente.md) §4): esquema de **dos niveles**, sin reservar
nombres de extensión en el core. (1) El core reserva solo lo suyo — `core:`
y `ui:`, las superficies que el propio kernel emite. (2) Todo otro namespace
pertenece a un plugin por convención (namespace = nombre del plugin), y la
colisión entre extensiones la cierra el loader, que garantiza que el nombre
de un plugin es único — es su identidad (storage `plugins/<nombre>/`,
resolución de `requires`, sustitución por nombre de las embebidas; dos
nombres iguales = error de carga). Así `agent:` deja de ser una reserva del
core y pasa a ser el namespace del plugin `agent`, protegido igual que
`mi-plugin:` — sin privilegio: nadie más puede llamarse `agent`, y el agente
no puede apropiarse de `mi-plugin`. Descartado reservar `agent:` (y los
namespaces de las demás oficiales) en el core: el kernel reservando un nombre
por cuenta de una extensión es justo lo que prohíbe «el kernel solo conoce
sus propias capacidades» ([filosofia.md](filosofia.md) §2, ADR-003) — la
misma vara que cerró G21 y G23. Descartado también un registro central de
namespaces en el core (otra vez vocabulario de extensiones en la superficie
sagrada).

**Problema.** La guía (§7) listaba `core:`, `ui:` **y `agent:`** como
namespaces de eventos reservados, mientras [api.md](api.md) (§4, §17) reserva
solo `core:`/`ui:`. La incoherencia escondía una de fondo: `agent` es una
extensión oficial, no el core; que el core reserve su namespace lo obliga a
conocer una extensión por su nombre, contra ADR-003. Y sin esa reserva
quedaba sin responder qué impide que dos extensiones declaren el mismo
namespace.

**Impacto.** Coherencia del modelo de extensión sobre la superficie que se
congela; toca el principio del kernel mínimo que sostiene G21/G23. Barato
ahora, caro tras congelar.

**Opciones.** (a) Reservar `agent:` (y las demás oficiales) en el core —
cómodo, pero mete nombres de extensión en la API sagrada; (b) un registro de
namespaces en el core que las extensiones reclaman al cargarse — resuelve
colisiones pero a costa de superficie y de que el core sepa de namespaces de
producto; (c) dos niveles por convención: el core reserva solo `core:`/`ui:`,
y la unicidad del nombre de plugin (garantía del loader) protege a las
extensiones entre sí — `agent:` es un namespace de plugin más.

## G27 · `nu.task.all` no especifica el orden de los resultados — `api.md` §3 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §3): `nu.task.all` devuelve los
resultados **alineados con los inputs** (`out[i]` es el de `fns[i]`),
independiente del orden de terminación — semántica `Promise.all`. No es API
nueva: fija la semántica de orden de un primitivo que ya existía. Pasa la
vara de filosofía §4 que descarta las alternativas: *allSettled* (envolver
cada rama en `pcall`) y el límite de concurrencia (semáforo de
`nu.task.future`) un plugin los compone en Lua, así que se quedan en
userland; el orden de un primitivo del core **no** se puede fijar desde
fuera, luego es su contrato. Orden-de-terminación descartado: rompe la
correlación resultado↔entrada y obliga a cada llamante a re-etiquetar, justo
la fricción que «compone mejor a través de capas» (§1.4) quiere evitar;
alinear es además gratis (escribir en el slot indexado al resolver, sin
quitar paralelismo). Una nueva función `nu.task.all_settled`/`map_limit` se
descartó: sería superficie sagrada ad hoc para lo que Lua ya hace
(filosofía §3/§6).

**Problema.** La firma `(fns) -> any[]` dice "espera a todas" pero no que
`out[i]` corresponda a `fns[i]` — las tasks terminan en cualquier orden.
Para una orquestación paralela determinista (un fan-out de subagentes sobre
territorios) es justo lo que hace falta garantizado: sin alineación
posicional no se puede correlacionar resultado con territorio salvo metiendo
el índice dentro de cada payload a mano. Misma clase de indefinición que
cazaban las rondas 3-4 (cf. G8, G10): comportamiento que variaría según el
scheduler dentro de la API sagrada.

**Impacto.** Cualquier consumidor de `task.all` con más de un resultado;
bloquea la orquestación paralela determinista de la ronda 5. Barato ahora,
imposible de cambiar tras congelar.

**Opciones.** (a) Especificar semántica `Promise.all` (orden de inputs,
no de terminación); (b) dejarlo en orden de terminación y que el llamante
acarree el índice (fricción en cada uso, contra §1.4); (c) añadir variantes
nuevas (`all_settled`, `map_limit`) — superficie ad hoc para lo que Lua ya
compone.

## G28 · `Region:blit` con coordenadas locales negativas (viewport/scrollback) — `api.md` §9.1 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §9.1 y
[guia-plugins.md](guia-plugins.md) §6): opción (a) con tres clavos. (1)
`blit` recorta por **ambos extremos**: `x/y` negativos recortan el borde
inicial del Block (`blit(0, -3, doc)` muestra `doc` desde su cuarta fila),
simétrico al recorte por exceso — un viewport sobre un Block más grande que
la región. (2) Garantía explícita: blittear el mismo Block con distinto
offset es **copia, nunca re-render** (el coste de scroll es el de copiar la
ventana visible). (3) La **virtualización** (no construir el Block entero
para historiales enormes) es del toolkit, no del core. Descartada la
primitiva de viewport dedicada (b): añade superficie para lo que el negativo
ya da; descartado recortar en Lua (c) por el coste en el estado principal.
El patrón "cachea el Block, mueve el offset" queda en la guía §6 (con su
antipatrón: reconstruir el Block en cada scroll).

**Problema.** `Region:blit(x, y, block)` "recorta a los límites", pero la
especificación solo contempla el recorte por **exceso** (la parte del Block
que se sale del borde de la región). Un transcript con scroll necesita lo
contrario: estampar un Block alto con `y` **negativo** para recortar sus
primeras filas y "asomarlo" por abajo — un viewport sobre un Block grande,
donde scroll = re-blit con otro offset (ronda 6, escenario 28). No está
escrito si las coordenadas locales negativas son legales ni qué hacen.

**Impacto.** Cualquier UI con scrollback — el transcript de `chat` el
primero; el toolkit lo necesita resuelto antes del spike. Si no fuera
legal, cada plugin tendría que recortar el Block en Lua antes de cada
`blit` (trabajo proporcional al contenido en el estado principal, contra
"Lua decide, Go ejecuta").

**Opciones.** (a) `blit` acepta `x/y` negativos y recorta el borde inicial
(filas/columnas iniciales) además del final — un viewport sobre el Block
sin coste en Lua; (b) primitiva de viewport dedicada en `Region`
(`Region:scroll(block, offset)`) que encapsule el clamp y el offset; (c)
dejarlo en el plugin: recortar el Block en Lua antes de `blit` (rechazable
por el coste en el estado principal).

## G29 · Ratón en coordenadas globales sin traducción a región (hit-testing) — `api.md` §9.1/§9.3 — **RESUELTO**

**Resolución** (aplicada en [guia-plugins.md](guia-plugins.md) §6): opción
(c) — el mapeo pantalla→contenido es del **toolkit**, no del core, por el
mismo reparto que G1 (relayout) y G22 (theming): lo que depende del layout
que el plugin posee es del plugin. La razón decisiva es que `Region:hit` (a)
solo podría hacer la **mitad trivial** — restar el origen `x,y` que el plugin
mismo fijó —, mientras la mitad valiosa (qué bloque/línea de un Block
envuelto y **scrolleado** se clicó) necesita el offset de scroll y el layout
del contenido, que el core no retiene (el blit de G28 es efímero). Añadir
`Region:hit` sería superficie sagrada para lo que el plugin ya tiene gratis,
y además ignoraría z-order/oclusión (una región tapada devolvería coords
igual). Descartada (b) entregar el ratón en coordenadas locales: rutear por
geometría dentro del core es meter un trozo de toolkit en el kernel, contra
el modelo de pila de §9.3. Si el toolkit demuestra que repite el mismo
cálculo en todas partes, *entonces* se promueve una primitiva — con
evidencia, no por adelantado.

**Problema.** El evento de ratón (`ev.type == "mouse"`) trae `x, y` en
coordenadas de **pantalla**, pero las regiones viven en coordenadas
**locales** (y su contenido, además, desplazado por el scroll de G28). No
hay `Region:contains(x,y)` ni traducción global→local. Para clicar un
widget — la cabecera de un bloque de tool para plegarlo, un botón de un
modal — el plugin rastrea a mano la geometría de cada región (que él mismo
fijó) y resuelve el hit-test sumando/restando origen y offset (ronda 6,
escenario 31).

**Impacto.** Todo widget clicable del toolkit reimplementa el mismo
cálculo; fricción repetida en la capa que más lo va a usar.

**Opciones.** (a) `Region:hit(x, y) -> (bx, by) | nil` — traduce
pantalla→local y devuelve `nil` si el punto cae fuera (con G28, contando el
offset de scroll); (b) entregar el evento de ratón ya en coordenadas
locales a la región bajo el puntero (cambia el modelo de pila de input de
§9.3, que hoy es global y por consumo); (c) documentar que el mapeo es
responsabilidad del toolkit, ya que el plugin conoce la geometría que fijó
(barato, pero deja el hit-test fuera del core para siempre).

## G30 · Pegar una imagen: el evento `paste` solo trae texto — `api.md` §9.3 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §9.3): pegar contenido
**no-texto** del portapapeles (una imagen) **inyecta una ruta**, no los
bytes. El core vuelca la imagen a un fichero temporal de la sesión
(`nu.fs.tmpdir`) y entrega un evento `paste` con `path` (sin `text`); la UI
inserta la ruta exactamente como una mención `@`, y el agente decide leerla
(no se incrusta el contenido a ciegas, igual que las menciones de
[chat.md](chat.md) §3). Los bytes binarios nunca cruzan las fronteras de
texto/JSON (coherente con G11). Es **distinto de P6** (render de imágenes en
el transcript, pospuesto): aquello es pintar, esto es entrada. Descartado
entregar los bytes en el evento (reintroduce binario en la frontera de
input que G11 cerró) y descartado plegarlo a P6 (P6 es salida; pegar una
ruta es útil aunque nunca se pinte la imagen).

**Problema.** Un harness de coding (estilo claude-code) pega imágenes del
portapapeles, pero el evento `paste` solo trae `text` y `clipboard_get`
devuelve `string`: pegar una imagen no se podía expresar (ronda 6,
escenario 29).

**Impacto.** Flujo cotidiano de un harness de coding; barato de cerrar
ahora sobre la superficie que se congela.

**Opciones.** (a) El evento `paste` de contenido no-texto entrega `path`
(fichero temporal volcado), insertable como `@` — la elegida; (b)
`nu.ui.clipboard_get_image() -> path?` aparte (superficie extra para lo
mismo); (c) dejarlo fuera de v1, plegado a P6 (descartado: P6 es salida).

---

## G31 · El puente ⏸ no puede ceder a través de `pcall`/tail call en gopher-lua — `api.md` §1.3/§1.4 — **RESUELTO**

**Resolución** (decisión en [adr.md](adr.md) ADR-011; sin cambios en
[api.md](api.md): la API era correcta, fallaba la técnica de realización).
El scheduler se realiza **sin yields de corrutina**: una goroutine por task
+ un único token de ejecución Lua. Una primitiva ⏸ suelta el token, hace el
trabajo bloqueante en una goroutine de fondo y al volver lo recupera; como no
hay yield, `pcall`, las tail calls y el desenrollado de errores son los
nativos de gopher-lua y sobreviven a la suspensión. Implementado en S04
(`internal/runtime/scheduler.go`), validado con `-race`.

## G32 · El lock de sesión necesita el pid PROPIO y la API no lo expone — `api.md` §7 / `sesiones.md` §6 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §7/§16/§17 y
[sesiones.md](sesiones.md) §6): una primitiva mínima —`nu.sys.pid() ->
integer`, el pid del proceso `nu` actual—, consulta local inmediata (no ⏸) y
[W] como el resto de `nu.sys`. Junto a `nu.sys.hostname()` forma la **identidad
del escritor** que el lock graba (`{ pid, hostname, started }`, §6). Es la
cuarta pieza que el corolario de completitud (filosofía §2) reclama: G17 añadió
`fs.write{exclusive}` + `nu.proc.alive(pid)` + `nu.sys.hostname()` para *crear*
el lock y *validar pids ajenos*, pero se le escapó la forma de conocer el pid
**propio** que va dentro del lock. Como es la **primera adición a la superficie
sagrada tras el congelado**, `nu.version.api` sube de 1 a **2** (api.md §17:
crece solo por adición, el contador se incrementa con cada una); es estricta
adición, no rompe ninguna firma. La primitiva dedicada se justifica como las
de G17: es vocabulario del **kernel** (un pid es del proceso, no del producto)
y no se compone con lo existente —`nu.proc` solo conoce los pids de los hijos
que lanza, jamás el del propio `nu`—. Descartado derivarlo de un subproceso
(`nu.proc.run(["sh","-c","echo $PPID"])` es frágil, caro y POSIX-only) y
descartado plegarlo a `nu.proc.alive` (es existencia de un pid dado, no
descubrimiento del propio).

**Problema.** El lockfile de [sesiones.md](sesiones.md) §6 graba
`{ pid, hostname, started }` con el **pid del proceso que escribe**, pero
[api.md](api.md) no lo expone: `nu.sys` da `platform`/`env`/`setenv`/`now_ms`/
`mono_ms`/`hostname` (sin pid) y `nu.proc.alive(pid)` valida pids **ajenos**
(para detectar locks huérfanos) pero no hay forma de obtener el **propio**. Sin
él la extensión sesiones (S38) no puede escribir el lock especificado: misma
clase de grieta que G17 (resolución correcta en prosa, no escribible con la API
especificada), y nacida igual al *construir* el contra-código (S38), no en una
ronda de pseudocódigo.

**Impacto.** Bloquea S38 (la extensión sesiones); reabre de hecho G5/G17 (la
corrupción de sesiones que cerraban vuelve a ser posible si el lock no se puede
escribir como está especificado). Barato de cerrar ahora, sobre la superficie
que se congela.

**Opciones.** (a) `nu.sys.pid() -> integer` (la elegida): mínima, vocabulario de
kernel, hermana de `hostname`; (b) ampliar `nu.proc` con un `nu.proc.self()` —
mete el pid propio en el módulo de *subprocesos*, donde no encaja (proc gestiona
hijos); (c) rebajar el contenido del lock a solo `{ hostname, started }` y
confiar la unicidad al `O_EXCL` — pierde la detección de huérfanos por pid de
§6 (un crash dejaría el lock para siempre), descartable.

**Problema.** Surgió **construyendo** la quilla (S04), no en una ronda de
pseudocódigo. gopher-lua (semántica Lua 5.1) no deja que una corrutina ceda a
través de una frontera de llamada Go. Verificado contra v1.1.2: (1)
`pcall(fn)` con `fn` que suspende **aborta** la corrutina en el `pcall` en vez
de ceder — pero §1.4 promete capturar los errores estructurados con `pcall`,
y el pseudocódigo lo hace alrededor de operaciones que hacen IO (⏸); (2)
`return ⏸fn()` en cola pierde la continuación (el `OP_TAILCALL` elide el frame
antes del yield). Misma raíz: el yield no cruza fronteras Go.

**Impacto.** Fundacional: sin esto el modelo de errores de §1.4 (pcall sobre
código que suspende) no se sostiene, y toda la API ⏸ tiene footguns en
posición de cola. Es la quilla — barato de cerrar aquí, carísimo después.

**Opciones.** (a) **Goroutine-por-task + token Lua** (sin yield):
pcall/tail call/errores nativos — la elegida (ADR-011); (b) seguir con el
puente de corrutinas y construir un `pcall` *yieldable* (pcall como
sub-corrutina) + trampolines Lua para las tail calls: más invasivo, defería un
`pcall` roto-por-defecto y aún así frágil; (c) cambiar de runtime Lua —
desproporcionado (ADR-002 ya está decidido). El desenrollado **no capturable**
de S08 (cancelación/watchdog) se diseñará sobre (a) con un panic centinela
propio, no con el yield aquí descartado.

## G33 · El arranque sin TTY no tiene onramp y "el conjunto oficial" está sin definir — `api.md` §14 / ADR-010 / G21 — **RESUELTO**

**Resolución** (registrada en [ADR-015](adr.md#adr-015--conjunto-oficial-de-producto-y-onramp-no-interactivo), que **refina** ADR-010; aplicada en [api.md](api.md) §14, [arquitectura.md](arquitectura.md) §5, [filosofia.md](filosofia.md) §5 y el sitio de docs): dos piezas, ninguna en la API sagrada.

1. **Onramp no interactivo: el flag CLI `nu --default-config`.** La pantalla de runtime desnudo de G21 resolvió el primer arranque **solo con TTY** (es UI; §14 lo cierra con "Sin TTY no hay pantalla: arranca desnudo"). El caso sin TTY —CI, Docker, scripts— quedaba sin un paso para activar el conjunto oficial: había que escribir `config.dir()/nu.toml` a mano. El flag lo cubre con **dos modos**: solo (`nu --default-config`) **escribe** `plugins.enabled` con el conjunto de producto y sale (idempotente, atómico, preservando el resto del fichero — reusa `writeEnabledPlugins`, la misma vía que la acción TTY); combinado con una acción headless (`--default-config -p '…'` / `-e '…'`) **no toca disco**: activa el conjunto **solo para ese proceso** (option interna `WithEnabledPlugins`) y ejecuta la acción. Vive en el **binario** (`main.go`), no en `nu.*`: es la superficie CLI de S45 —como `-e`/`-p`/`--continue`—, así que **`nu.version.api` no cambia** (a diferencia de G17/G32, que sí ampliaron la superficie sagrada). El core sigue sin saber lo que es un agente (ADR-003): el flag orquesta extensiones por la API pública, como podría un `init.lua`.

2. **Definición de "el conjunto oficial de producto".** Hasta ahora "el conjunto oficial" era, de hecho, `embeddedNames()` (*todo* lo embebido), que incluye `example` — el plugin-andamiaje que existe **solo para probar el gating** de ADR-010 ([implementacion.md](implementacion.md), Fase 8). Meterlo en la config por defecto del usuario es ruido. Se fija el conjunto en las **siete de producto** —`providers, sessions, agent, mcp, chat, repl, toolkit`— = el catálogo embebido **menos `example`**, cerrado bajo dependencias (`agent → providers, sessions`; `mcp → agent`; `chat → toolkit, agent, providers, sessions`). Por **coherencia** (regla de oro del flujo: una semántica no se contradice entre documentos), la acción TTY de G21 pasa a activar **el mismo** conjunto: la pantalla desnuda y el flag enchufan lo mismo. La distinción "producto vs todo lo embebido" vive en una sola fuente (`officialProductSet`, derivada de `embeddedNames` filtrando `example`).

**Mismo conjunto en ambos modos**, incluido `chat`: aunque `chat`/`repl` necesitan TTY, sus `init.lua` ya se auto-gatean con `if nu.has("ui")` — sin superficie de UI quedan inertes solos (G20/§9). Activarlos en headless no estorba, y omitirlos exigiría una segunda lista y un caso borde sin ganancia. Descartado.

**Problema.** Dos grietas que afloran al *usar* el binario terminado para probarlo con sus extensiones oficiales (no en una ronda de pseudocódigo ni construyendo el kernel: usándolo). (a) ADR-010 deja las oficiales **inactivas por defecto** y G21 dio el onramp del primer arranque, pero **solo para TTY**; en headless (`nu -e`/CI/Docker) no hay forma de un paso de activar el conjunto: hay que editar `nu.toml` a mano, contradiciendo de hecho la ergonomía "de una tecla" que ADR-010 promete. (b) "El conjunto oficial" nunca se definió con precisión frente a `example`: `ActivateOfficial()` activa `embeddedNames()` entero, así que la acción TTY de hoy ya mete el plugin de pruebas en la config del usuario.

**Impacto.** Es la **primera experiencia** de quien instala `nu` y quiere el harness en CI/contenedor — justo lo que ADR-010 dice proteger, pero por el lado no interactivo que G21 no cubrió. No bloquea ninguna sesión de construcción (el plan está cerrado, 45/45); es deuda de producto barata de saldar sobre la superficie CLI ya congelada (S45), sin tocar la API sagrada.

**Opciones.** (a) **El flag `nu --default-config`** (la elegida): espejo no interactivo de la acción 1 de la pantalla, con modo efímero para Docker inmutable; vive en el binario, no roza `nu.*`. (b) Exponer la escritura a Lua (`nu.config.enable_official()`) y resolverlo con `nu -e`: **amplía la API sagrada** (`nu.version.api`++) para *empeorar* la ergonomía (`nu -e 'nu.config.enable_official()'` no es más fácil que el flag) — contradice el objetivo; descartada. (c) Un subcomando `nu init`: honesto semánticamente, pero estrena el **primer subcomando** del binario (hoy solo flags), una puerta a `nu run`/`nu chat`… que S45 evitó al mantener el binario delgado; prematuro por una sola necesidad. (d) No hacer nada y documentar "edita `nu.toml`": austero y hostil, justo lo que ADR-010 quiso evitar (es la opción (c) descartada en G21, ahora para el caso sin TTY).

## G34 · El modelo canónico de `thinking` no expresa el modo adaptativo (Opus 4.6+ 400ea con `budget_tokens`) — `providers.md` §2.1/§3 — **RESUELTO**

**Resolución** (registrada en [ADR-016](adr.md#adr-016--modelo-canónico-de-thinking-con-mode-y-traducción-por-modelo-en-el-adaptador), que **reabre y cierra** [P21](pospuesto.md); aplicada en [providers.md](providers.md) §2.1/§3 y la nota `⚠` del adaptador `anthropic`): el parámetro canónico crece **por adición** a `thinking?: { mode?: "off"|"adaptive"|"budget", budget? }` —con `{budget=N}` como **alias compatible** de `mode="budget"`, así que la forma congelada sigue válida—, y el **dialecto de razonamiento de cada modelo se declara como DATO** en el `providers.toml` (`thinking = "adaptive"|"budget"|"none"`, default `"budget"`), que viaja en el `ModelInfo` y el adaptador lee para traducir **por-modelo** (`adaptive` → `{type="adaptive"}`, `budget` → `{type="enabled", budget_tokens=N}`, degradando entre ambos según el dialecto; `none`/ausente → no se envía, degradación declarada §3 ob.5). El adaptador sigue siendo un **traductor puro** (ADR-003/ADR-005): cero tablas de versiones de modelos en el código. La superficie sagrada `nu.*` no cambia (es contrato de extensión). **Implementado** (sesión de construcción posterior al ADR, como manda el protocolo "el contrato lidera, el código sigue"): `thinking_to_wire` en `adapter_anthropic.lua` traduce por dialecto, `resolve` lleva `model.thinking` al `ModelInfo`, y `providers_p21_test.go` blinda las ocho combinaciones (dialecto × modo); el bloque legacy `budget_tokens` incondicional ya no existe.

**Problema.** El canónico congeló `thinking?: { budget?: integer }` y el adaptador `anthropic` lo emite como `{type="enabled", budget_tokens=N}` (extended thinking *legacy*). La familia Opus 4.6+ —incluido el modelo por defecto `claude-opus-4-8`— retiró `budget_tokens` y espera `{type="adaptive"}`: la petición da **400** contra la API real. No es defecto del adaptador (cumple el contrato congelado) sino del **modelo canónico**, al que le falta (1) vocabulario para pedir el modo adaptativo y (2) el dato de qué forma entiende cada modelo. Validado en [pseudocodigo.md](pseudocodigo.md) Ronda 7 (escenario 32): la rama "budget sobre legacy" es expresable, la rama "adaptive sobre Opus 4.6+" **no** hay código que la escriba. Estuvo pospuesta como P21; el disparador (modelo por defecto ya Opus 4.8) la reabre.

**Impacto.** **Latente** —el agente headless no rellena `req.thinking` en el ensamblado del turno, así que el 400 solo aparece por un hook `request.pre` o una futura feature de control de razonamiento— pero **bloquea la capacidad** de usar razonamiento extendido con los modelos Opus modernos, que para un harness de código es de primera. Barato de cerrar en el contrato ahora; caro después, con thinking cableado y consumidores que presupongan el canónico viejo.

**Opciones.** (a) `mode` en el canónico + dialecto por-modelo como dato del TOML (**la elegida**, ADR-016): traductor puro, crecimiento por adición; (b) heurística por id del modelo en el adaptador (`model:match("opus%-4%-[6-9]")`) — frágil, mete conocimiento de versiones de producto en un traductor, falla con ids renombrados; (c) **reemplazar** `budget` por la forma nueva — rompe la firma congelada y los tests grabados; (d) dejarlo pospuesto — descartado: el disparador (modelo por defecto Opus 4.8) ya está activo.

## G35 · El onramp de ADR-015 activa los plugins pero no deja config de agente: el primer `nu` muere sin modelo y deja la UI atrapada — ADR-015 / `chat.md` §8 / `agente.md` §10 — **RESUELTO**

**Resolución** (registrada en [ADR-017](adr.md#adr-017--el-onramp-deja-config-de-agente-usable-y-el-chat-degrada-con-gracia), que **refina** ADR-015; aplicada en [chat.md](chat.md) §8, [agente.md](agente.md) §10, [providers.md](providers.md) y el binario): dos piezas, ninguna en la API sagrada (es superficie CLI, loader y Lua de extensión; `nu.version.api` no cambia).

1. **Onramp completo: `nu --default-config` deja config de agente USABLE.** El modo persistente, además de escribir `plugins.enabled` en `nu.toml` (G33), escribe **plantillas activas** de `agent.toml` (`model = "anthropic/opus"`, `max_turns`) y `providers.toml` (provider `anthropic` con `base_url`, `api_key_env = "ANTHROPIC_API_KEY"` y el modelo `claude-opus-4-8`/alias `opus`) **solo si no existen** (nunca sobrescribe; atómico, idempotente — reusa `writeAtomic` y el patrón "no pisar TOML existente" de `writeEnabledPlugins`). Default **opinado a Anthropic** (la identidad del producto, un harness estilo claude-code). La clave **nunca** va al fichero (providers.md §1): vive en el entorno (`api_key_env`). El mensaje de éxito deja de ser engañoso ("ya puedes ejecutar el agente: nu -p") y pasa a ser **honesto y accionable**: lista los ficheros escritos y recuerda exportar `ANTHROPIC_API_KEY` (o editar `providers.toml`) antes de arrancar.

2. **Degradación con gracia del chat (robustez, principio 5).** Si `chat.start` no puede construir la sesión inicial (`agent.session` lanza `EINVAL` por modelo ausente, `EPROVIDER` por provider/modelo no resoluble, o `EAGENT`/`EPROVIDER` por TOML roto), el chat **no muere al log**: monta una **UI mínima accionable y salible** —un texto que explica cómo configurar (`agent.toml`, `providers.toml`, la API key) y un keymap de salida (`esc`/`q`/`ctrl+c` → `core:shutdown`)—. Los errores **inesperados** (no de config) se siguen propagando como hoy. Como **red de seguridad** del kernel, el modo interactivo instala además un handler de salida de emergencia al fondo de la pila de input (cualquier app montada lo tapa), de modo que **ninguna** ruta deje la terminal en raw mode sin forma de salir con teclado.

**Por qué plantillas activas y no comentadas.** Con la key en el entorno, `nu` *just works* tras un solo comando (la promesa "batteries-included" de ADR-015, ahora real). Sin la key, `providers.resolve` **no falla** (deja `api_key=nil`): el chat monta igual, la statusline muestra el modelo y el error por clave ausente sale **in-transcript** al primer turno (`agent:error` → `transcript:add_error`, que el chat ya renderiza) — mucho mejor que una pantalla muerta. Plantillas comentadas obligarían a editar TOML antes del primer arranque, justo la fricción que el onramp quería borrar.

**Problema.** Aflora al *usar* el binario terminado (como G33, no en pseudocódigo ni construyendo): tras `nu --default-config`, ejecutar `nu` deja la terminal en blanco. El log lo dice: `ERROR [user] chat: no se pudo arrancar: agent.session requiere model ("proveedor/modelo") en opts o en agent.toml`. Dos capas: (a) el onramp activa los siete plugins pero **no deja `model`/`provider`**, así que `core:ready` → `chat.start` → `agent.session({model=nil})` lanza `EINVAL`; (b) el `pcall` del `init.lua` del chat manda el error a `nu.log.error` (a disco, nunca a pantalla, §15) y **no monta nada**, y como la pantalla desnuda (la única ruta que instala un handler de salida de emergencia) no se toma con plugins activos, el usuario **queda atrapado** —en raw mode `ctrl+c` no genera `SIGINT`—.

**Impacto.** Es la **primera experiencia** de quien sigue el onramp de ADR-015 al pie de la letra: el comando que prometía dejar el harness listo lo deja roto e inservible. Bloquea por completo el arranque interactivo del producto. Barato de cerrar sobre la superficie CLI ya congelada (S45) y la Lua de las extensiones, sin tocar la API sagrada.

**Opciones.** (a) **Onramp completo + degradación con gracia** (la elegida, ADR-017): el onramp deja config usable *y* el chat sobrevive a la falta de config. (b) Solo degradación: el chat monta una UI accionable, pero el primer `nu` sigue sin modelo y exige editar TOML a mano — deshace la ergonomía de ADR-015. (c) Solo onramp: escribir las plantillas, pero el chat seguiría muriendo si el usuario borra/rompe la config — deja el segundo defecto (UI atrapada) sin cerrar. (d) Un **modelo por defecto cableado en el agente** sin `providers.toml`: mete vocabulario de producto (qué modelo, qué endpoint, qué env) en el motor, contra ADR-003/ADR-005; descartada. (e) No hacer nada y documentar "edita `agent.toml`/`providers.toml`": hostil, justo lo que ADR-010/ADR-015 quisieron evitar.

## G36 · El conjunto oficial de producto auto-monta dos UIs (chat y repl): salir del chat deja el REPL debajo — ADR-015 / `arquitectura.md` §Distribución / `chat.md` §8 — **RESUELTO**

**Resolución** (aplicada en el `init.lua` de la extensión `repl`, sin tocar la API sagrada; documentada en [arquitectura.md](arquitectura.md) §Distribución y [chat.md](chat.md) §8): el repl **cede la pantalla al chat**. Su auto-montaje en `core:ready` pasa a ser condicional: solo monta su UI si el `chat` **no** está entre los plugins activos (lo comprueba con `nu.plugin.list()`, sin `require`ar chat —el repl debe poder activarse SOLO, G21). Con el conjunto oficial activo, abre **solo** el chat; el repl queda como módulo accesible (`require("repl")`, `repl.eval`) pero inerte como UI. Con solo `repl` activo (G21), abre el REPL. En headless, ninguno monta UI. Además, `Chat:quit` (y `ctrl+c`) emiten `core:shutdown`: **cerrar el chat apaga el binario** en vez de devolver al usuario a una capa inferior.

**Problema.** Aflora al *usar* el producto, no en pseudocódigo. ADR-015 fijó el conjunto oficial como "las siete embebidas menos `example`", incluido `repl`, razonando **solo el caso headless** ("chat/repl se auto-gatean con `nu.has("ui")` y quedan inertes sin UI, así que activarlos juntos no estorba"). Pero **con TTY** —la experiencia real del producto— los `init.lua` de chat *y* de repl se suscriben a `core:ready` y **ambos** montan una `toolkit.app` a pantalla completa sobre el mismo compositor. Se solapan; y como el chat no apagaba el runtime al salir, cerrar el chat dejaba el REPL de Lua montado debajo: la sensación, descrita por el usuario, de "salir de la extensión de chat y luego del intérprete de lua". El razonamiento de ADR-015 tenía un hueco: *activarlos en headless* no estorba, pero *activarlos juntos en TTY* sí.

**Impacto.** Es la primera impresión del producto terminado: en vez de una TUI única y pulida, el usuario percibe capas que hay que ir cerrando. Barato de cerrar sobre la Lua de las extensiones (el repl mira el registro del loader ya existente) sin tocar la API sagrada ni el conjunto de ADR-015 (el repl sigue en él, instalado y accesible; solo no compite por la pantalla).

**Por qué el repl cede y no se saca del conjunto.** Sacar `repl` de `officialProductSet` lo desinstalaría del producto (no estaría disponible para activarse suelto desde una sesión con el conjunto oficial). El repl es valioso como herramienta del autor de extensiones (G21); lo que sobra no es su *presencia* sino su *competencia por la pantalla*. Cederla —el patrón "una sola extensión posee la UI primaria"— preserva ADR-015 y resuelve el solape. El chat, la UI del harness, es quien manda cuando está presente.

## G37 · `blitBlock` invierte el signo del offset X respecto a Y y al contrato de `Region:blit` — `api.md` §9.1 / `compositor.go` — **RESUELTO**

**Resolución** (aplicada en `compositor.go`; sin cambio en api.md —corrige la *implementación* para que cumpla el contrato ya documentado): `blitBlock` estampa el origen del Block en `(ox, oy)` con el **mismo** signo en ambos ejes. El eje X pasa de `lx = col - ox` a `lx = col + ox`, igual que el eje Y ya hacía con `by = ly - oy` (un `oy` negativo recorta el borde inicial). El test horizontal de viewport (G28) se corrige a la semántica coherente: `blit(-2,0)` recorta el inicio ("CDEF…"), `blit(+2,0)` desplaza a la derecha ("  AB").

**Problema.** [api.md](api.md) §9.1 documenta `Region:blit(x, y, block)` como un viewport simétrico: "`x/y` pueden ser **negativos** y recortan el borde *inicial* del bloque (`blit(0,-3,doc)` muestra `doc` desde su cuarta fila)". El eje Y lo cumplía; el X estaba **invertido** (`lx = col - ox`: era el *positivo* el que recortaba el inicio). Nunca se notó porque **ningún widget se blitteaba en x>0**: el chat, la pantalla desnuda y el repl apilaban todo contra el margen izquierdo. Al introducir `padding`/alineación en el toolkit (G36), un widget colocado en x=1 perdía su primera columna (el borde izquierdo de una caja, la viñeta de una línea), porque la app llama `region:blit(ax, ay)` esperando *posicionar* y en X obtenía un *scroll*.

**Impacto.** Latente pero real: bloquea cualquier layout con margen/padding/centrado horizontal —es decir, casi toda la UI de producto (cajas, modales centrados, statusline con padding)—. Se descubrió al construir el primer widget de borde. La corrección alinea la implementación con el contrato; no amplía ni cambia la API (`nu.version.api` no se mueve).
