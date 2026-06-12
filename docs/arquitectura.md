# Arquitectura de nu

Estado: borrador fundacional. Esto describe la forma del sistema, no una
especificación cerrada. Las decisiones y su razonamiento viven en
[adr.md](adr.md); la definición formal de la API v1 del core, en
[api.md](api.md); la vista dinámica (comunicación, orquestación y
limitaciones, con diagramas), en [modelo-ejecucion.md](modelo-ejecucion.md).
Contratos de las extensiones oficiales: [providers.md](providers.md),
[sesiones.md](sesiones.md), [agente.md](agente.md), [chat.md](chat.md).
Convenciones prácticas para autores: [guia-plugins.md](guia-plugins.md). Lo
aplazado, con su disparador de reapertura: [pospuesto.md](pospuesto.md).
Grietas pendientes de resolver antes de congelar: [problemas.md](problemas.md).

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
| **scheduler** | Event loop, timers, puente coroutines-Lua ↔ goroutines, workers |
| **io** | Filesystem, spawn de procesos con streams, entorno |
| **net** | Cliente HTTP/HTTPS con respuesta en streaming (SSE), TCP/websocket |
| **ui** | Celdas + regiones + compositor (z-order, blit de bloques, damage tracking, coalescing ~30 ms), eventos de input, keymaps |
| **text** | UTF-8/graphemes, regex, render de markdown, syntax highlighting |
| **data** | Codecs JSON y TOML |
| **loader** | `require`, rutas de plugins, extensiones embebidas |

Notas:

- **text** incluye markdown y highlighting como builtins aunque viole la pureza
  del kernel mínimo: en Lua interpretado serían dolorosamente lentos. Es la
  misma concesión que hace Neovim embebiendo tree-sitter (ADR-004, regla
  "Lua decide, Go ejecuta").
- La API de **ui** es deliberadamente de bajo nivel (ADR-007): el core expone
  celdas/regiones y un compositor; el **toolkit de widgets es una extensión
  Lua oficial** (retenida por dentro: árbol + nodos sucios) que aporta slots,
  focus y composición entre plugins, y se versiona aparte de la API sagrada.
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

Restricción técnica que motiva el diseño: gopher-lua **no es thread-safe**; un
estado Lua solo puede tocarse desde una goroutine. El patrón es el de
Node/libuv/`vim.uv`, ya validado.

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
  protocolo de los LLMs. Distribución v1: `~/.config/nu/plugins/` + git clone;
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
[providers.md](providers.md).

## Distribución

- Binario estático Go, `CGO_ENABLED=0`, cross-compile a todas las plataformas.
  Soporte v1: Linux y macOS nativos; en Windows, **WSL2** (G9) — así el
  contrato POSIX se cumple íntegro sin especificación condicional. Windows
  nativo: [P18](pospuesto.md).
- Extensiones oficiales embebidas con `go:embed` pero **inactivas por
  defecto** (ADR-010): activación explícita (primer arranque o `nu.toml`),
  sin red; sobreescribibles por el usuario desde su directorio de config.

## Persistencia

Las sesiones del agente se guardan como JSONL append-only bajo
`data_dir()/sessions/`, reutilizando el modelo canónico de mensajes; es una
convención pública legible por otras extensiones, no una primitiva del core.
Contrato completo en [sesiones.md](sesiones.md). El resto de extensiones
escriben bajo `data_dir()/plugins/<nombre>/`.

## Cuestiones abiertas

1. **Spike de validación de ADR-007**: celdas/regiones + compositor + toolkit
   Lua mínimo, torturado con (a) streaming de tokens con markdown a pantalla
   completa y (b) fuzzy picker sobre ~100k ficheros. Criterio de veto
   pre-comprometido: si no es fluido, el toolkit se implementa en Go
   conservando la misma API pública.
2. **Política fina del watchdog**: valor del presupuesto por handler, si es
   configurable por plugin, y el flujo de deshabilitación/aviso al usuario.
3. **Diseño de la API pública del toolkit oficial** (vocabulario de widgets,
   layout, slots, focus): no es API sagrada del core, pero el ecosistema
   heredará su calidad.
