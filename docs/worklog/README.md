---
title: "Decisiones y desviaciones de implementación"
type: "indice"
status: "vigente"
---
# Decisiones y desviaciones de implementación

Este fichero recoge decisiones de implementación que no estaban especificadas al
detalle en los documentos de diseño y desviaciones puntuales del plan, una
entrada por sesión. No sustituye al flujo de diseño (`problemas.md`/`adr.md`):
recoge lo operativo que no llega a hallazgo `G##` pero que la sesión siguiente
debe poder reconstruir.

## Índice

| Sesión | Título | Fichero |
|---|---|---|
| S05 | `enu.task.sleep`/`defer`/`every` + `Timer:stop` (api.md §3) | [s05-task-sleep-defer-every-timer.md](s05-task-sleep-defer-every-timer.md) |
| S06 | `enu.task.future` (rendez-vous de un solo uso, api.md §3) | [s06-task-future.md](s06-task-future.md) |
| S07 | `enu.task.all` / `enu.task.race` (api.md §3) | [s07-task-all-task-race.md](s07-task-all-task-race.md) |
| S08 | Cancelación pública: `Task:cancel` + `enu.task.cleanup` + desenrollado no capturable (api.md §1.3, §3) | [s08-cancelacion-publica-task-cancel-task.md](s08-cancelacion-publica-task-cancel-task.md) |
| S09 | Watchdog de slice (api.md §1.3) | [s09-watchdog-de-slice.md](s09-watchdog-de-slice.md) |
| S10 | bus de eventos `enu.events` (api.md §4) | [s10-bus-de-eventos-events.md](s10-bus-de-eventos-events.md) |
| S11 | loader de plugins (api.md §14) | [s11-loader-de-plugins.md](s11-loader-de-plugins.md) |
| S12 | activación de extensiones embebidas gobernada por `enu.toml` (api.md §14, ADR-010) | [s12-activacion-de-extensiones-embebidas-gobernada.md](s12-activacion-de-extensiones-embebidas-gobernada.md) |
| S13 | `enu.plugin.reload` (best-effort, G2) (api.md §14) | [s13-plugin-reload-best-effort-g2.md](s13-plugin-reload-best-effort-g2.md) |
| S14 | `enu.fs` (api.md §5) | [s14-fs.md](s14-fs.md) |
| S15 | `enu.fs.watch` (api.md §5, §16) | [s15-fs-watch.md](s15-fs-watch.md) |
| S16 | `enu.proc` (api.md §6) | [s16-proc.md](s16-proc.md) |
| S17 | `enu.sys` (api.md §7) | [s17-sys.md](s17-sys.md) |
| S18 | codecs `enu.json` / `enu.toml` / `enu.yaml` (api.md §12) | [s18-codecs-json-toml-yaml.md](s18-codecs-json-toml-yaml.md) |
| S19 | `enu.http.request` (api.md §8) | [s19-http-request.md](s19-http-request.md) |
| S20 | `enu.http.stream` + parser SSE (api.md §8, 🔒) | [s20-http-stream-parser-sse.md](s20-http-stream-parser-sse.md) |
| S21 | `enu.ws.connect` (api.md §8; cierra Fase 4 — Red, CP-5) | [s21-ws-connect.md](s21-ws-connect.md) |
| S22 | `enu.text` (width/wrap/truncate) + tipo `Block` + `enu.ui.block`/`caps`/`Style` (api.md §10, §9.2, 🔒) | [s22-text-width-wrap-truncate-tipo.md](s22-text-width-wrap-truncate-tipo.md) |
| S23 | `enu.text.markdown` (render completo, streaming-safe, themable) (api.md §10, 🔒) | [s23-text-markdown-render-completo-streaming.md](s23-text-markdown-render-completo-streaming.md) |
| S24 | `enu.text.highlight` (syntax highlighting, degrada a texto plano) (api.md §10) | [s24-text-highlight-syntax-highlighting-degrada.md](s24-text-highlight-syntax-highlighting-degrada.md) |
| S25 | `enu.text.diff` (hunks estructurados + render a Block) (api.md §10, 🔒) | [s25-text-diff-hunks-estructurados-render.md](s25-text-diff-hunks-estructurados-render.md) |
| S26 | `enu.re` (RE2: compile/match/find_all/replace) | [s26-re.md](s26-re.md) |
| S27 | `enu.search` (files/grep/fuzzy) (api.md §11, 🔒; cierra Fase 5 — CP-6) | [s27-search-files-grep-fuzzy.md](s27-search-files-grep-fuzzy.md) |
| S28 | SPIKE de ADR-007 (compositor + toolkit Lua mínimos; HITO DE VETO) | [s28-spike-de-adr-007.md](s28-spike-de-adr-007.md) |
| S29 | `enu.ui` compositor real (§9.1) | [s29-ui-compositor-real.md](s29-ui-compositor-real.md) |
| S30 | ciclo de vida de `Region` (move/resize/raise/lower/show/hide/destroy/cursor) (api.md §9.1) | [s30-ciclo-de-vida-de-region.md](s30-ciclo-de-vida-de-region.md) |
| S31 | input (`enu.ui.on_input` / `keymap`) (api.md §9.3) | [s31-input-ui-on-input-keymap.md](s31-input-ui-on-input-keymap.md) |
| S32 | resto de `enu.ui`: clipboard OSC 52 + eventos `ui:*` + gating headless G20 (api.md §9.2, §9, §4, §2) | [s32-resto-de-ui-clipboard-osc.md](s32-resto-de-ui-clipboard-osc.md) |
| S33 | pantalla de runtime desnudo (api.md §14, G21; cierra Fase 6, CP-7 manual) | [s33-pantalla-de-runtime-desnudo.md](s33-pantalla-de-runtime-desnudo.md) |
| S34 | `enu.worker.spawn` + caps (G6) + send/recv con colas acotadas (api.md §13, 🔒; abre Fase 7) | [s34-worker-spawn-caps-g6-send.md](s34-worker-spawn-caps-g6-send.md) |
| S35 | `Worker:on_message` (excluyente con `recv`, G8) + tasks/timers/futures dentro del worker + `terminate` (api.md §13, 🔒; cierra Fase 7 — CP-8) | [s35-worker-on-message-excluyente.md](s35-worker-on-message-excluyente.md) |
| S36 | extensión oficial `providers` (registro TOML + contrato del adaptador + `approx_tokens`) (providers.md) | [s36-extension-oficial-providers-registro-toml.md](s36-extension-oficial-providers-registro-toml.md) |
| S37 | Adaptador Anthropic (primer dialecto real); CP-9 | [s37-adaptador-anthropic-primer-dialecto-real.md](s37-adaptador-anthropic-primer-dialecto-real.md) |
| S38 | Extensión sesiones (JSONL, lockfiles) + enu.sys.pid (G32, APILevel 1→2) | [s38-extension-sesiones-jsonl-lockfiles-sys.md](s38-extension-sesiones-jsonl-lockfiles-sys.md) |
| S39 | Extensión oficial `agent` (motor headless: turno, tools, permisos, hooks, eventos `agent:*`); CP-10 (agente.md) | [s39-extension-oficial-agent-motor-headless.md](s39-extension-oficial-agent-motor-headless.md) |
| S40 | Subagentes del agente (workers + caps recortadas + digesto al padre) (agente.md §9) | [s40-subagentes-del-agente-workers-caps.md](s40-subagentes-del-agente-workers-caps.md) |
| S41 | Extensión oficial `mcp` (capa 2: cliente JSON-RPC/stdio; mapeo de tools + confianza) (arquitectura.md §capa 2, cierra cuestión abierta nº4) | [s41-extension-oficial-mcp-capa-2.md](s41-extension-oficial-mcp-capa-2.md) |
| S42 | Toolkit de widgets (árbol+dirty, slots, focus, themes G22) (arquitectura.md §kernel/nota ui) | [s42-toolkit-de-widgets-arbol-dirty.md](s42-toolkit-de-widgets-arbol-dirty.md) |
| S43 | Extensión oficial `chat` (UI del harness sobre toolkit + agente; streaming markdown); CP-11 (chat.md) | [s43-extension-oficial-chat-ui.md](s43-extension-oficial-chat-ui.md) |
| S44 | Extensión oficial `repl` (REPL de Lua sobre la API pública, activable solo, G21) (arquitectura §Distribución) | [s44-extension-oficial-repl-repl.md](s44-extension-oficial-repl-repl.md) |
| S45 | Superficie CLI (flags, --auto-permissions, --continue/G18, códigos de salida); cierra la Fase 8 y el plan (arquitectura nº5) | [s45-superficie-cli-flags-auto-permissions.md](s45-superficie-cli-flags-auto-permissions.md) |
| S46 | README raíz en inglés y filosofía a la tesis de motor de harnesses (Fase 9, ADR-025 Fase 1) | [s46-readme-raiz-en-ingles.md](s46-readme-raiz-en-ingles.md) |
| S47 | Copy de la web a la tesis de motor de harnesses + legibilidad de doc larga (Fase 9, ADR-025 Fase 1) | [s47-copy-web-a-la-tesis-y-legibilidad.md](s47-copy-web-a-la-tesis-y-legibilidad.md) |
| S48 | Matriz de smoke tests de instalación en sistemas limpios (Fase 9, ADR-025 Fase 1; refina ADR-013) | [s48-matriz-smoke-instalacion-sistemas-limpios.md](s48-matriz-smoke-instalacion-sistemas-limpios.md) |
| Cierre | coherencia post-plan — P21 (thinking adaptativo, pospuesto) + fix del log espurio de `EvalTaskString` | [cierre-post-plan.md](cierre-post-plan.md) |
| G42+G43 | Retry con backoff en el motor + `agent:error` estructurado con reintento manual (agente.md §2/§4/§10, chat.md §2/§4) | [g42-g43-retry-backoff.md](g42-g43-retry-backoff.md) |
| G53–G56 | Construcción del lote de seguridad G53+G54+G55+G56 (auditoría 2026-07-16) | [g53-g56-seguridad.md](g53-g56-seguridad.md) |
| E2E | Suite e2e de los plugins oficiales contra el binario real (paquete e2e/) | [e2e-plugins.md](e2e-plugins.md) |
