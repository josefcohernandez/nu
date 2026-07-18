---
title: "Arquitectura de enu"
description: "Vista estática: las capas y el inventario de primitivas del kernel."
type: "contrato"
layer: "core"
web: "wiki"
status: "vigente"
---
# Arquitectura de enu

Estado: borrador fundacional. Esto describe la forma del sistema, no una
especificación cerrada. Las decisiones y su razonamiento viven en
[adr.md](../decisions/adr/README.md); la definición formal de la API v1 del core, en
[api.md](../contracts/api.md); la vista dinámica (comunicación, orquestación y
limitaciones, con diagramas), en [modelo-ejecucion.md](modelo-ejecucion.md).
Contratos de las extensiones oficiales: [providers.md](../contracts/providers.md),
[sesiones.md](../contracts/sesiones.md), [agente.md](../contracts/agente.md), [chat.md](../contracts/chat.md).
Convenciones prácticas para autores: [guia-plugins.md](../contracts/guia-plugins.md). Lo
aplazado, con su disparador de reapertura: [pospuesto.md](../postponed/pospuesto.md).
Grietas pendientes de resolver antes de congelar: [problemas.md](../findings/README.md).

## Vista general

```
┌─────────────────────────────────────────────────────────┐
│                  Extensiones de usuario (Lua)           │
├─────────────────────────────────────────────────────────┤
│         Extensiones oficiales (Lua, go:embed)           │
│   agente · MCP · UI de chat · comandos · providers      │
├─────────────────────────────────────────────────────────┤
│                    API del core (v1)                    │
├─────────────────────────────────────────────────────────┤
│                   Kernel (Go, binario único)            │
│  scheduler · IO · red · UI terminal · texto · codecs    │
└─────────────────────────────────────────────────────────┘
```

El kernel es un **runtime**: una stdlib de primitivas más un event loop. No
contiene lógica de agente, ni de MCP, ni de chat. Cuanto más pequeño es el core
conceptualmente, más completa tiene que ser su superficie de primitivas: Lua
puro no puede hacer TLS ni pintar un terminal, así que el kernel se lo da.

## El kernel: inventario de primitivas

| Módulo | Responsabilidad |
|---|---|
| **scheduler** | Event loop, timers, puente ⏸ task-Lua ↔ goroutines (realizado con tasks como corrutinas Lua nativas sobre wazero, ADR-020, tras la retirada de gopher en M17; la realización previa goroutine-por-task + token Lua de ADR-011 quedó reemplazada), workers |
| **io** | Filesystem, spawn de procesos con streams, entorno, búsqueda paralela del árbol (`files`/`grep`, G51) |
| **net** | Cliente HTTP/HTTPS con respuesta en streaming (SSE), TCP/websocket |
| **ui** | Celdas + regiones + compositor (z-order, blit de bloques, damage tracking, coalescing ~30 ms), eventos de input, keymaps |
| **text** | UTF-8/graphemes, regex, render de markdown, syntax highlighting |
| **data** | Codecs JSON, TOML y YAML (G51) |
| **loader** | `require`, rutas de plugins, extensiones embebidas |

Notas:

- **text** incluye markdown y highlighting como builtins aunque viole la pureza
  del kernel mínimo: en Lua interpretado serían dolorosamente lentos. Es la
  misma concesión que hace Neovim embebiendo tree-sitter (ADR-004, regla
  "Lua decide, Go ejecuta").
- La API de **ui** es deliberadamente de bajo nivel (ADR-007): el core expone
  celdas/regiones y un compositor; el **toolkit de widgets es una extensión
  Lua oficial** (retenida por dentro: árbol + nodos sucios) que aporta slots,
  focus, composición entre plugins, **decoración** (caja/borde, padding,
  spinner, texto multi-span — [ADR-018](../decisions/adr/README.md)) y el sistema de themes — los
  nombres semánticos de color se resuelven aquí, no en el core (G22), y el
  theme cablea su paleta al render de markdown (`Theme:markdown_opts`) —, y se
  versiona aparte de la API sagrada.
  Lua coloca bloques pre-rendidos por `text`, no celdas sueltas, en los
  caminos calientes. Es el patrón de ADR-003 aplicado a la UI: el core no
  sabe lo que es un widget.

## Modelo de concurrencia: el modelo del navegador

Tres patas (ADR-004):

1. **Estado Lua principal, single-threaded.** UI, keymaps, hooks y
   orquestación. El monohilo aquí es una *feature*: orden determinista de
   eventos y cero data races para el 95% de los plugins. El IO nunca bloquea:
   las goroutines de Go hacen el trabajo y publican resultados en la cola que
   el loop de Lua consume; de cara al autor de extensiones todo es async vía
   coroutines (estilo `await`).
2. **Workers explícitos.** Una primitiva tipo `worker.spawn()` levanta otro
   estado Lua en otra goroutine, sin memoria compartida, comunicado por paso
   de mensajes. Paralelismo real, opt-in, para la extensión que necesite
   masticar datos. Los workers **no tienen acceso al módulo `ui`**: la
   pantalla solo se pinta desde el estado principal (como los Web Workers
   respecto al DOM). Los mensajes son copias — un worker devuelve resultados
   digeridos, no datos crudos masivos. Opcionalmente, un worker puede nacer
   con la API recortada (`opts.caps`): los módulos no concedidos no existen
   dentro del estado — sandboxing por capacidades para subagentes y código
   no confiable.
3. **Primitivas Go paralelas por dentro.** `core.search()` y compañía saturan
   todos los cores sin que Lua se entere. El rendimiento bruto nunca depende
   de la velocidad del intérprete.

Restricción técnica que motiva el diseño: el intérprete de Lua embebido **no es
thread-safe** (ni la instancia PUC-Lua sobre wazero de hoy ni el gopher-lua
legacy lo eran); un estado Lua solo puede tocarse desde una goroutine. El patrón
es el de Node/libuv/`vim.uv`, ya validado.

El aislamiento es **por tarea, no por plugin** (ADR-008): todos los plugins
conviven en el estado principal — lo que permite que se `require` entre sí y
compongan, como en Neovim — y la robustez se obtiene con dos guardas del core:

- **Watchdog**: cada handler tiene un presupuesto de tiempo en el estado
  principal; si lo excede, se aborta vía cancelación por contexto y el plugin
  se marca como sospechoso.
- **`pcall` en cada frontera de hook**: un error en un plugin nunca tumba el
  event loop ni a los demás plugins.

## Capas de extensión

- **Capa 1 — Lua embebido.** El mecanismo universal: hooks del ciclo de vida,
  comandos, UI, keybindings, y también el propio agente y los adaptadores de
  protocolo de los LLMs. Distribución v1: `~/.config/enu/plugins/` + git clone;
  sin package manager propio de momento.
- **Capa 2 — Procesos externos.** Herramientas pesadas o en otros lenguajes
  vía subproceso (JSON-RPC/stdio). MCP vive aquí, **implementado como
  extensión oficial Lua** sobre las primitivas `io.spawn` + codecs: el core no
  sabe qué es MCP.

## Providers de LLM

División datos/código (ADR-005):

- **TOML** declara el registro: endpoints, API keys, modelos, límites de
  contexto. Configuración, no programación.
- **Adaptadores de protocolo en Lua** (extensiones oficiales) implementan cada
  dialecto (Anthropic, OpenAI, Gemini, Ollama...): formato SSE, tool calls,
  system prompts, thinking blocks. Parsear SSE en Lua es viable: es texto a
  velocidad de lectura humana.

Añadir un provider raro (vLLM, proxy corporativo) es un fichero Lua, no una
recompilación. El contrato del adaptador y el formato del registro están en
[providers.md](../contracts/providers.md).

## Distribución

- Binario estático Go, `CGO_ENABLED=0`, cross-compile a todas las plataformas.
  Soporte v1: Linux y macOS nativos; en Windows, **WSL2** (G9) — así el
  contrato POSIX se cumple íntegro sin especificación condicional. Windows
  nativo: [P18](../postponed/pospuesto.md).
- Extensiones oficiales embebidas con `go:embed` pero **inactivas por
  defecto** (ADR-010): activación explícita (pantalla de runtime desnudo
  con TTY — api.md §14 —, el flag `enu --default-config` sin TTY, o `enu.toml`
  a mano), sin red; sobreescribibles por el usuario
  desde su directorio de config. El **conjunto oficial de producto** son
  las embebidas menos el andamiaje `example` y la malla `mesh` (ADR-015;
  [malla.md](../contracts/malla.md) §1.4): además del
  harness (agente, chat, providers, MCP, toolkit), un **`repl`** —REPL de
  Lua sobre la API pública, activable solo, el punto de partida del autor
  de extensiones que no quiere el harness (G21)—. Con TTY, **una sola UI
  primaria posee la pantalla**: el repl **cede al chat** (solo auto-monta su
  UI si el chat no está activo, vía `enu.plugin.list`), así `enu` con el
  conjunto oficial abre una TUI única y no el chat *y* el REPL solapados
  ([G36](../findings/g36-el-conjunto-oficial-de-producto.md), [ADR-018](../decisions/adr/README.md)). La **`mesh`** ([malla.md](../contracts/malla.md),
  nacida de la ronda 8 de pseudocódigo) viaja embebida pero se activa
  explícitamente: es la herramienta de orquestación de mallas de agentes,
  no el harness por defecto.

## Persistencia

Las sesiones del agente se guardan como JSONL append-only bajo
`data_dir()/sessions/`, reutilizando el modelo canónico de mensajes; es una
convención pública legible por otras extensiones, no una primitiva del core.
Contrato completo en [sesiones.md](../contracts/sesiones.md). El resto de extensiones
escriben bajo `data_dir()/plugins/<nombre>/`.

<!-- enu:interno -->

## Cuestiones abiertas

1. ~~**Spike de validación de ADR-007**: celdas/regiones + compositor + toolkit
   Lua mínimo, torturado con (a) streaming de tokens con markdown a pantalla
   completa y (b) fuzzy picker sobre ~100k ficheros. Criterio de veto
   pre-comprometido: si no es fluido, el toolkit se implementa en Go
   conservando la misma API pública.~~ **RESUELTA** por el spike de S28
   ([ADR-012](../decisions/adr/adr-012-resultado-del-spike-de-adr.md)):
   el overhead de orquestar desde Lua resultó despreciable (el trabajo pesado es
   primitiva Go), así que **el veto NO se ejecutó** y el toolkit se construye en
   Lua. ADR-007 ascendió a Aceptada.
2. **Política fina del watchdog**: el presupuesto base ya está fijado
   (100 ms, configurable en `enu.toml` — api.md §1.3); queda lo fino: si es
   configurable por plugin y el flujo de deshabilitación/aviso al usuario
   tras `core:plugin.misbehaved`.
3. **Diseño de la API pública del toolkit oficial** (vocabulario de widgets,
   layout, slots, focus): no es API sagrada del core, pero el ecosistema
   heredará su calidad.
4. ~~**Contrato de la extensión MCP**: citada en toda la documentación
   (ADR-003, [agente.md](../contracts/agente.md) §3, capa 2) pero sin documento propio —
   formato de configuración (qué servidores, cómo se declaran), ciclo de
   vida de los procesos, mapeo de tools y de su confianza.~~ **RESUELTA** por
   la implementación de S41 (extensión `mcp`, [implementacion.md](../plan/implementacion.md)).
   El contrato quedó fijado al construirla —Lua puro sobre la API pública, sin
   tocar el core (corolario de completitud satisfecho)—:
   - **Configuración** (división datos/código, ADR-005): los servidores se
     DECLARAN en `mcp.toml` (`enu.config.dir()`), formato
     `[servers.<nombre>] command = [...] cwd? env?`. Ausente → no se conecta
     nada. También se conectan a mano con `mcp.connect{ name, command, cwd?,
     env? } ⏸ -> Conn`.
   - **Ciclo de vida de los procesos**: el servidor se lanza con
     `enu.proc.spawn`, vive mientras su `Conn` exista, y se mata limpiamente
     (`Proc:kill` registrado en `enu.task.cleanup` + `Conn:close()` idempotente,
     [api.md](../contracts/api.md) §6). Un servidor que muere (EOF en stdout) despierta a
     todos los requests pendientes con `EMCP` (nadie cuelga). El diálogo es
     JSON-RPC 2.0 sobre stdio con **framing por líneas** (una línea = un mensaje
     JSON), demultiplexado por `id` con una task lectora dedicada.
   - **Mapeo de tools y de su confianza**: cada tool anunciada por el servidor
     (`tools/list`) se registra con `agent.tool{...}` ([agente.md](../contracts/agente.md)
     §3) bajo el prefijo `mcp__<servidor>__<tool>`; su handler hace `tools/call`
     por JSON-RPC. La **confianza** —son tools de TERCEROS— se gobierna con el
     pipeline de permisos del agente ([agente.md](../contracts/agente.md) §5): se registran
     con `permissions.default = "ask"`, así que requieren permiso explícito —
     un `allow` por tool, con nombre **exacto**
     (`allow = {"mcp__github__search_code"}`): el emparejamiento de nombres no
     admite glob (G53, [agente.md](../contracts/agente.md) §5), de modo que autorizar un
     servidor entero es enumerar sus tools o conceder por hook `permission` —
     y en headless sin él se DENIEGAN con
     error accionable. No hay caso especial: una tool MCP pasa por la misma
     valla que cualquier otra.
5. ~~**Superficie CLI**: `enu -e` y `--auto-permissions` aparecen en los
   contratos sin especificación propia (flags, subcomandos, comportamiento
   headless, códigos de salida). El azúcar de reanudación (un `--continue`
   sobre `agent.session{ resume }`) se decidirá aquí: G18 lo dejó
   deliberadamente fuera de los contratos.~~ **RESUELTA** por la
   implementación de S45 ([implementacion.md](../plan/implementacion.md)). La
   superficie CLI vive en el **binario** (`main.go`), NO en la API sagrada
   `enu.*` (api.md): es la interfaz de línea de comandos del ejecutable, y el
   core sigue sin saber lo que es un agente (ADR-003) — el CLI orquesta las
   extensiones (`agent`, `sessions`) por la API pública, como podría hacerlo un
   `init.lua` de usuario. Lo fijado:
   - **Flags**: `enu -e '<lua>'` (evalúa un chunk Lua headless e imprime sus
     retornos, ya de S01); `enu -p '<prompt>'` (ejecuta un **turno de agente
     headless** — agente.md §1, "modo scripting/CI gratis" — e imprime el texto
     final del asistente a stdout); `--auto-permissions` (permisos del agente en
     modo `"auto"`, agente.md §5 amortiguador 3 — sin él, en headless las tools
     sensibles se DENIEGAN); `--model 'prov/modelo'` (anula el modelo por defecto
     de `agent.toml`); `--continue`/`-c` (azúcar de reanudación, abajo);
     `--default-config` (activa el **conjunto oficial de producto** sin TTY —el
     onramp que la pantalla desnuda de G21 no cubría—: solo, escribe
     `plugins.enabled` en `enu.toml` —y plantillas activas de
     `agent.toml`/`providers.toml` si no existen, para dejar el harness usable,
     ADR-017/G35— y sale; con `-p`/`-e`, lo activa solo para ese
     proceso sin tocar disco. ADR-015, G33).
   - **Headless / códigos de salida**: `enu -e` y el modo agente corren SIN TTY
     (G20) con códigos de salida coherentes para CI/scripts — **0** éxito;
     **1** error de ejecución (el chunk, el turno o el provider lanzaron, o el
     arranque falló); **2** uso inválido (flags/argumentos); **3** permiso
     denegado en headless (una tool sensible se denegó por falta de
     `--auto-permissions`, agente.md §5 — código DISTINTO para que un script
     distinga "el modelo no pudo actuar por permisos" de un fallo de ejecución).
   - **`--continue` (G18)**: reanuda la sesión MÁS RECIENTE del proyecto (cwd)
     antes de enviar el prompt — `sessions.list(cwd)` (los ids ordenan
     lexicográfico = temporal, sesiones.md §2/§7) elige la última, que se pasa
     como `resume` a `agent.session{...}`. Es el `--continue` que G18 dejó
     deliberadamente fuera de los contratos por pertenecer a esta superficie.
   - **Arranque** (S33): sin args y con TTY → arranque normal (pantalla de
     runtime desnudo si no hay plugins, G21); sin args y sin TTY → uso (código 2);
     `enu -e`/`-p`/`--continue` → modo headless. `--default-config` solo (sin acción
     headless) escribe el conjunto de producto en `enu.toml` —más plantillas de
     `agent.toml`/`providers.toml` si faltan (ADR-017/G35)— y sale (G33): el onramp
     sin TTY que la pantalla desnuda no daba.
   El ejecutor headless de los modos suspendientes (el turno del agente es ⏸) es
   `Runtime.EvalTaskString` (corre un chunk Lua como TASK a término): interfaz Go
   del binario, NO superficie Lua sagrada (como `EvalString`/`RenderBareScreen`);
   api.md quedó INTACTO (corolario de completitud satisfecho: la API pública +
   las extensiones bastaron, sin hallazgo `G##`).

<!-- /enu:interno -->
