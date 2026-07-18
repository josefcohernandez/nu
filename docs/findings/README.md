---
title: "Problemas abiertos"
type: "indice"
status: "vigente"
---
# Problemas abiertos

Lista de trabajo viva: grietas encontradas en las rondas de validación
([pseudocodigo.md](../validation/README.md)) y revisiones posteriores que están
**pendientes de resolver**.
Método: se resuelven una a una, discutiendo opciones; al decidirse, la
resolución se aplica a los documentos afectados y la entrada pasa a
"Resuelto" con enlace al cambio. Distinto de [pospuesto.md](../postponed/pospuesto.md):
aquello es lo que decidimos no decidir; esto son agujeros que la v1 sí
necesita cerrados.

**Estado: 57 registradas, 56 resueltas, 1 abierta** (G58 y G59 añadidas
2026-07-18 desde la misma suite e2e de los plugins oficiales, caracterizando
dos bugs de producto PREEXISTENTES en vez de un hueco de API: G58, el bucle del
driver del chat (`select` sin timeout sobre `<-chunks`) no observaba el
`core:shutdown` que `/quit` emite desde una task hasta que llegaba otra
pulsación de teclado, pese a que `chat.md` §8 promete que salir del chat apaga
el runtime — **resuelta 2026-07-18** con la opción (a): la `Instance` expone un
canal `quitSignal` (cerrado por la primitiva interna `__driver_notify_quit`
desde el handler de `core:shutdown`) que el `select` de `drive()` escucha junto
al input, sin API pública nueva ni polling (ADR-004); el `.jsonl.lock` resultó
un bug DISTINTO que se orfana en el arranque (será G60, en discusión). Queda
**ABIERTA** G59, el
auto-connect de servidores de `mcp.toml` es una task efímera cuyo `cleanup`
desconecta las tools ANTES de que corra el turno de `enu -p` (contradice el
propio módulo, que documenta `connect_configured` para una task de larga
vida), y además `env` como array de `mcp.toml` no llega al subproceso porque
`enu.proc.spawn` solo interpreta `env` como tabla `{K=V}`. Ambas caracterizadas
sin trampa por `e2e/chat_test.go` y `e2e/mcp_test.go`. G57 añadida
2026-07-18 desde la suite e2e de los plugins oficiales: la aserción de permisos
del transcript destapó que `sessions` no alcanza el `0600` que `sesiones.md`
§2/§8 prometen —crea el fichero con `fsFilePerm` (0644) recortado por el umask,
sin chmod— y que la API pública no dejaba fijar el modo (corolario de
completitud); **resuelta el mismo día** por adición a `api.md` §5
(`opts.mode`: chmod explícito no recortado por el umask, componible con
`exclusive`), con `sessions` creando transcript y lock en `0600` y `append`
preservando el modo, nivel de API 4→5. G53–G56 añadidas
2026-07-16 desde la auditoría de seguridad
([auditoria-seguridad-2026-07-16.md](../audits/auditoria-seguridad-2026-07-16.md)):
grietas de diseño de SEC-02/03/04/07 —semántica de emparejamiento de permisos,
control de redirects en `enu.http`, scrubbing de secretos del entorno, e
identidad de un worker para las primitivas [W]—; las **cuatro resueltas** el
mismo día: G53 — emparejamiento por subcomando con fail-closed, ADR-023,
alternativa mayor pospuesta como P39 —; G54 —control de redirects— por
adición a `api.md` §8 (`opts.max_redirects` y recorte de cabeceras
cross-host, nivel de API 3→4); G55 —el scrubbing, de SEC-04— en las
extensiones (providers.md §4 + agente.md §3, core intacto); y G56 —la
identidad del worker— con la foto del dueño en el spawn (ADR-024; cierra de
paso el data race de SEC-05). G52 añadida
2026-07-14 desde A-38 de la auditoría integral — `Ws:send` sin vía binaria y
`Ws:recv` sin distinguir el tipo de frame — resuelta por adición a `api.md`
§8, nivel de API 2→3; G44–G51
añadidas 2026-07-12 desde la auditoría integral
([auditoria-2026-07-12.md](../audits/auditoria-2026-07-12.md)): G47–G51 —incoherencias
documentales— resueltas el mismo día; G44 —el bombeo del scheduler— resuelta
y **construida** el 2026-07-13 con la opción (b), `RunTasks` persistente
(bitácora de [implementacion.md](../plan/implementacion.md)); G45 —la superficie [W]
de los workers— resuelta y **construida** el 2026-07-13 con la opción (a),
marca worker-safe por snippet de preludio; G46 —el replay de `event`—
resuelta y **construida** el 2026-07-13 con la opción (a) más la (c):
precedencia `opts > transcript > agent.toml` y allow/deny reaplicados en
orden. G42–G43 añadidas 2026-07-08 desde la auditoría de arquitectura
post-M17
([informe-arquitectura-2026-07-08.md](../audits/informe-arquitectura-2026-07-08.md))
— dos promesas de contrato que la implementación no cumplía y ningún registro
recogía: el reintento con backoff de agente.md §2 no existía en el motor (G42)
y `agent:error` descartaba el `code`/`retryable` que chat.md necesita para su
acción de reintento (G43); resueltas y construidas el 2026-07-16
(`stream_with_retry`, `Session:retry` y `/retry` en chat). G41 añadida 2026-07-03 desde la
construcción — un handler que escribía en un upvalue de una task suspendida
"perdía" la escritura: bug de gopher-lua en el desenrollado de `pcall`,
blindado en el kernel el mismo día; G38-G40
añadidas 2026-07-02 desde la ronda 8 de pseudocódigo — una malla distribuida de
agentes sobre git, con specs Role+Job y fork-como-replicación — y resueltas el
mismo día: G38, el slug de
`sessions/<proyecto>/` sin especificar — el
algoritmo pasa a ser parte del formato y la extensión expone
`sessions.slug/dir`; G39, `Session:fork` sin `opts` y con
`at` sin unidad — `fork(at?, opts?)` y `close()`
entran en el contrato, la herencia queda especificada y se bendice la copia
del prefijo (hija autocontenida); G40, denegaciones de permisos no
observables como dato — evento `agent:permission.denied` + el mismo objeto
en el `meta` del `tool_result`, y `tool.end` especificado para denegaciones;
G36 y G37 añadidas 2026-06-28 al pulir la
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
estilo claude-code sobre `enu.ui`): G28 (blit recorta por ambos extremos,
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
flag `enu --default-config` y ADR-015 (sin tocar la API sagrada: es superficie
CLI). G35 es la **segunda** de *usar* el binario terminado: ese mismo onramp
activa los siete plugins pero **no deja config de agente** (modelo/provider), así
que el primer `enu` muere sin modelo y deja la UI atrapada — resuelta con ADR-017
(plantillas activas en el onramp + degradación con gracia del chat). La lista queda
como registro del proceso; los problemas nuevos que surjan (spike incluido) se
añaden aquí con el mismo método.

---

## Índice

> Los números G24–G25 no existen como fichero: son un hueco histórico que
> nunca se asignó. La numeración es append-only: el próximo hallazgo es G60,
> los huecos no se reutilizan.

| # | Título | Docs afectados | Estado | Fichero |
|---|---|---|---|---|
| G1 | Comportamiento ante resize | `api.md` §9 | RESUELTO | [g01-comportamiento-ante-resize.md](g01-comportamiento-ante-resize.md) |
| G2 | Hot-reload de plugins (ciclo de desarrollo) | loader / `api.md` §14 | RESUELTO | [g02-hot-reload-de-plugins-ciclo.md](g02-hot-reload-de-plugins-ciclo.md) |
| G3 | Multi-sesión: atribución de eventos y modales concurrentes | `agente.md` §4 / `chat.md` | RESUELTO | [g03-multi-sesion-atribucion-de-eventos.md](g03-multi-sesion-atribucion-de-eventos.md) |
| G4 | Reentrada de `Session:send` | `agente.md` §2 | RESUELTO | [g04-reentrada-de-session-send.md](g04-reentrada-de-session-send.md) |
| G5 | Doble reanudación de la misma sesión | `sesiones.md` | RESUELTO | [g05-doble-reanudacion-de-la-misma.md](g05-doble-reanudacion-de-la-misma.md) |
| G6 | Granularidad de `caps` | `api.md` §13 | RESUELTO | [g06-granularidad-de-caps.md](g06-granularidad-de-caps.md) |
| G7 | Semántica de `fs.watch` | `api.md` §5 | RESUELTO | [g07-semantica-de-fs-watch.md](g07-semantica-de-fs-watch.md) |
| G8 | `on_message` vs `recv` simultáneos | `api.md` §13 | RESUELTO | [g08-on-message-vs-recv-simultaneos.md](g08-on-message-vs-recv-simultaneos.md) |
| G9 | Alcance Windows en v1 | transversal | RESUELTO | [g09-alcance-windows-en-v1.md](g09-alcance-windows-en-v1.md) |
| G10 | Reentrada del bus de eventos | `api.md` §4 | RESUELTO | [g10-reentrada-del-bus-de-eventos.md](g10-reentrada-del-bus-de-eventos.md) |
| G11 | Datos no-UTF-8 en las fronteras JSON | `api.md` §12 / transversal | RESUELTO | [g11-datos-no-utf-8.md](g11-datos-no-utf-8.md) |
| G12 | TLS/proxy para endpoints corporativos | `api.md` §8 | RESUELTO | [g12-tls-proxy-para-endpoints-corporativos.md](g12-tls-proxy-para-endpoints-corporativos.md) |
| G13 | Providers por suscripción (OAuth) | `providers.md` / `api.md` | RESUELTO | [g13-providers-por-suscripcion-oauth.md](g13-providers-por-suscripcion-oauth.md) |
| G14 | Modelo de confianza del contenido del repo | `agente.md` §6-§7 / transversal | RESUELTO | [g14-modelo-de-confianza-del-contenido.md](g14-modelo-de-confianza-del-contenido.md) |
| G15 | El interior de un worker: scheduler propio y watchdog | `api.md` §13 / `modelo-ejecucion.md` | RESUELTO | [g15-el-interior-de-un-worker.md](g15-el-interior-de-un-worker.md) |
| G16 | Subagentes paralelos escribiendo los mismos ficheros | `agente.md` §9 | RESUELTO | [g16-subagentes-paralelos-escribiendo-los-mismos.md](g16-subagentes-paralelos-escribiendo-los-mismos.md) |
| G17 | El lockfile de sesiones no es implementable con la API actual | `api.md` §5-§7 / `sesiones.md` §6 | RESUELTO | [g17-el-lockfile-de-sesiones.md](g17-el-lockfile-de-sesiones.md) |
| G18 | Reanudar una sesión no tiene API | `agente.md` §2 | RESUELTO | [g18-reanudar-una-sesion-no-tiene.md](g18-reanudar-una-sesion-no-tiene.md) |
| G19 | Cambio de modelo a mitad de sesión sin API | `agente.md` §2 / `chat.md` §4 | RESUELTO | [g19-cambio-de-modelo-a-mitad.md](g19-cambio-de-modelo-a-mitad.md) |
| G20 | Detección de interactividad (TTY/headless) | `api.md` / `agente.md` §5 / `chat.md` §8 | RESUELTO | [g20-deteccion-de-interactividad-tty-headless.md](g20-deteccion-de-interactividad-tty-headless.md) |
| G21 | El primer arranque de ADR-010 no tiene dueño | ADR-010 / `api.md` §14 | RESUELTO | [g21-el-primer-arranque-de-adr.md](g21-el-primer-arranque-de-adr.md) |
| G22 | Resolución de colores semánticos entre core y toolkit | `api.md` §9.2 | RESUELTO | [g22-resolucion-de-colores-semanticos-entre.md](g22-resolucion-de-colores-semanticos-entre.md) |
| G23 | Vocabulario LLM en la API sagrada (`enu.text.approx_tokens`) | `api.md` §10 / `providers.md` §5 | RESUELTO | [g23-vocabulario-llm-en-la-api.md](g23-vocabulario-llm-en-la-api.md) |
| G26 | Namespaces de extensión reservados al core | `api.md` §4 / guía §7 / `agente.md` §4 | RESUELTO | [g26-namespaces-de-extension-reservados.md](g26-namespaces-de-extension-reservados.md) |
| G27 | `enu.task.all` no especifica el orden de los resultados | `api.md` §3 | RESUELTO | [g27-task-all-no-especifica.md](g27-task-all-no-especifica.md) |
| G28 | `Region:blit` con coordenadas locales negativas (viewport/scrollback) | `api.md` §9.1 | RESUELTO | [g28-region-blit-con-coordenadas-locales.md](g28-region-blit-con-coordenadas-locales.md) |
| G29 | Ratón en coordenadas globales sin traducción a región (hit-testing) | `api.md` §9.1/§9.3 | RESUELTO | [g29-raton-en-coordenadas-globales-sin.md](g29-raton-en-coordenadas-globales-sin.md) |
| G30 | Pegar una imagen: el evento `paste` solo trae texto | `api.md` §9.3 | RESUELTO | [g30-pegar-una-imagen-el-evento.md](g30-pegar-una-imagen-el-evento.md) |
| G31 | El puente ⏸ no puede ceder a través de `pcall`/tail call en gopher-lua | `api.md` §1.3/§1.4 | RESUELTO | [g31-el-puente-no-puede-ceder.md](g31-el-puente-no-puede-ceder.md) |
| G32 | El lock de sesión necesita el pid PROPIO y la API no lo expone | `api.md` §7 / `sesiones.md` §6 | RESUELTO | [g32-el-lock-de-sesion-necesita.md](g32-el-lock-de-sesion-necesita.md) |
| G33 | El arranque sin TTY no tiene onramp y "el conjunto oficial" está sin definir | `api.md` §14 / ADR-010 / G21 | RESUELTO | [g33-el-arranque-sin-tty.md](g33-el-arranque-sin-tty.md) |
| G34 | El modelo canónico de `thinking` no expresa el modo adaptativo (Opus 4.6+ 400ea con `budget_tokens`) | `providers.md` §2.1/§3 | RESUELTO | [g34-el-modelo-canonico-de-thinking.md](g34-el-modelo-canonico-de-thinking.md) |
| G35 | El onramp de ADR-015 activa los plugins pero no deja config de agente: el primer `enu` muere sin modelo y deja la UI atrapada | ADR-015 / `chat.md` §8 / `agente.md` §10 | RESUELTO | [g35-el-onramp-de-adr-015.md](g35-el-onramp-de-adr-015.md) |
| G36 | El conjunto oficial de producto auto-monta dos UIs (chat y repl): salir del chat deja el REPL debajo | ADR-015 / `arquitectura.md` §Distribución / `chat.md` §8 | RESUELTO | [g36-el-conjunto-oficial-de-producto.md](g36-el-conjunto-oficial-de-producto.md) |
| G37 | `blitBlock` invierte el signo del offset X respecto a Y y al contrato de `Region:blit` | `api.md` §9.1 / `compositor.go` | RESUELTO | [g37-blitblock-invierte-el-signo.md](g37-blitblock-invierte-el-signo.md) |
| G38 | El slug de proyecto de `sessions/<proyecto>/` no está especificado | `sesiones.md` §2/§7 | RESUELTO | [g38-el-slug-de-proyecto.md](g38-el-slug-de-proyecto.md) |
| G39 | `Session:fork` no re-aloja: sin `opts` (cwd/permisos/modelo) y con `at` sin unidad definida | `agente.md` §2 / `sesiones.md` §5 | RESUELTO | [g39-session-fork-no-re-aloja.md](g39-session-fork-no-re-aloja.md) |
| G40 | Las denegaciones de permisos no son observables como dato | `agente.md` §4/§5 | RESUELTO | [g40-las-denegaciones-de-permisos.md](g40-las-denegaciones-de-permisos.md) |
| G41 | Un error capturado por `pcall` cierra upvalues de frames VIVOS (y el aborto no cerraba los suyos bajo el arreglo) | gopher-lua / `cancel.go` / `scheduler.go` | RESUELTO | [g41-un-error-capturado-por-pcall.md](g41-un-error-capturado-por-pcall.md) |
| G42 | El reintento con backoff prometido por agente.md §2 no existe en el motor | `agente.md` §2/§4/§10 | RESUELTO | [g42-reintento-con-backoff-de-apertura.md](g42-reintento-con-backoff-de-apertura.md) |
| G43 | `agent:error` descarta el `code` y el `retryable` que chat.md promete pintar | `agente.md` §2/§4 · `chat.md` §2/§4 | RESUELTO | [g43-agent-error-descarta-code-retryable.md](g43-agent-error-descarta-code-retryable.md) |
| G44 | El scheduler no se bombea fuera de los `Eval`: el modo interactivo no ejecuta tasks y los timers de fondo mueren en cada quiescencia | `api.md` §3 / `modelo-ejecucion.md` | RESUELTO | [g44-el-scheduler-no-se-bombea.md](g44-el-scheduler-no-se-bombea.md) |
| G45 | La superficie [W] prometida en `api.md` §16 no llega a los workers: los wrappers Lua de `extraPreludio` no cruzan | `api.md` §16 / `vmwasm/worker.go` | RESUELTO | [g45-la-superficie-w-prometida.md](g45-la-superficie-w-prometida.md) |
| G46 | El replay de `resume` ignora las entradas `event`: los cambios en caliente persistidos se pierden al reanudar | `sesiones.md` §3 / `agente.md` §2 (tensión G18/G19) | RESUELTO | [g46-el-replay-de-resume-ignora.md](g46-el-replay-de-resume-ignora.md) |
| G47 | `api.md` §1.5 promete `opts.timeout_ms` universal y no define el valor 0, que hoy diverge entre módulos | `api.md` §1.5/§5/§6/§8 | RESUELTO | [g47-api-md-1-5-promete.md](g47-api-md-1-5-promete.md) |
| G48 | `EAGENT` se usa en `chat.md`/`adr.md` (y en la extensión) pero `agente.md` nunca lo acuña | `agente.md` §10 | RESUELTO | [g48-eagent-se-usa-en-chat.md](g48-eagent-se-usa-en-chat.md) |
| G49 | `chat.md` §5 enseña `agent.permission.respond(id, "once")`, que la API real interpreta como DENEGACIÓN | `chat.md` §5 / `agente.md` §5 | RESUELTO | [g49-chat-md-5-ensena-agent.md](g49-chat-md-5-ensena-agent.md) |
| G50 | ADR-002 sigue "Aceptada" con su decisión de implementación (gopher-lua / Lua 5.1) obsoleta y sin anotación de reemplazo | `adr.md` | RESUELTO | [g50-adr-002-sigue-aceptada.md](g50-adr-002-sigue-aceptada.md) |
| G51 | El inventario de primitivas de `arquitectura.md` omite `enu.search` y el codec YAML | `arquitectura.md` / `api.md` §11-§12 | RESUELTO | [g51-el-inventario-de-primitivas.md](g51-el-inventario-de-primitivas.md) |
| G52 | `enu.ws` no tiene vía binaria: `Ws:send` siempre manda frame de texto y `Ws:recv` no distingue el tipo de frame | `api.md` §8 / `runtime/ws.go` | RESUELTO | [g52-ws-no-tiene-via-binaria.md](g52-ws-no-tiene-via-binaria.md) |
| G53 | La semántica de emparejamiento de los patrones de permiso `tool[:argumento]` no está especificada, y en `bash` el encadenamiento la vuelve una frontera falsa | `agente.md` §5 / `chat.md` §5 / `guia-plugins.md` | RESUELTO | [g53-la-semantica-de-emparejamiento.md](g53-la-semantica-de-emparejamiento.md) |
| G54 | `enu.http`/`enu.http.stream` siguen redirects sin control: no es expresable no-seguirlos ni observar la cadena | `api.md` §8 | RESUELTO | [g54-http-http-stream-siguen-redirects.md](g54-http-http-stream-siguen-redirects.md) |
| G55 | Los secretos del provider se heredan por defecto en el entorno de todo subproceso lanzado por la tool `bash`/`enu.proc` | extensión `agent` / `enu.proc` §6 | RESUELTO | [g55-los-secretos-del-provider.md](g55-los-secretos-del-provider.md) |
| G56 | El contrato [W] no define la identidad/dueño de un worker para las primitivas atribuidas por owner | `api.md` §13/§16 / `agente.md` | RESUELTO | [g56-el-contrato-w-no-define.md](g56-el-contrato-w-no-define.md) |
| G57 | El transcript y el lock de sesiones no alcanzan el `0600` prometido: la API no dejaba fijar el modo de creación | `api.md` §5/§17 / `sesiones.md` §2/§6/§8 / `guia-plugins.md` §7 | RESUELTO | [g57-transcript-y-lock-de-sesiones-no-alcanzan-0600.md](g57-transcript-y-lock-de-sesiones-no-alcanzan-0600.md) |
| G58 | El chat no se cierra hasta la siguiente tecla: `/quit` despacha `core:shutdown` desde una task, pero el driver solo lo sondea al llegar más input | `chat.md` §8 / driver | RESUELTO | [g58-el-chat-no-se-cierra-hasta.md](g58-el-chat-no-se-cierra-hasta.md) |
| G59 | El auto-connect de `mcp.toml` es inservible en headless `-p`: la task efímera desconecta las tools antes del turno, y `env` (array) no llega al subproceso | extensión `mcp` / `enu.proc` | ABIERTO | [g59-el-auto-connect-de-mcp-toml.md](g59-el-auto-connect-de-mcp-toml.md) |
