# S41 — Extensión oficial `mcp` (capa 2: cliente JSON-RPC/stdio; mapeo de tools + confianza) (arquitectura.md §capa 2, cierra cuestión abierta nº4)

Sexto eslabón de la Fase 8. **Lua puro sobre la API pública congelada** (ADR-003, sin privilegio
de kernel — el core NO sabe lo que es MCP). Implementa la **capa 2** de arquitectura.md
("procesos externos vía subproceso, JSON-RPC/stdio; MCP vive aquí como extensión oficial Lua sobre
`io.spawn` + codecs") y **cierra la cuestión abierta nº4** de arquitectura.md (el contrato de la
extensión MCP: configuración, ciclo de vida, mapeo de tools y confianza).

Plugin embebido nuevo `internal/runtime/embedded/mcp/`: `plugin.toml` (name="mcp",
`requires=["agent"]`), `init.lua` (cablea + auto-conexión perezosa de `mcp.toml` en una task) y el
módulo `lua/mcp/init.lua`. INACTIVO por defecto (ADR-010); activable por `enu.toml`
`plugins.enabled=[..., "mcp"]`, `source="builtin"`. El `embed.FS` lo descubre solo (cualquier
subdirectorio de `embedded/` con `plugin.toml`), sin tocar el mecanismo de S12.

## El cliente JSON-RPC 2.0 sobre stdio (`Conn`)

`mcp.connect{ name, command, cwd?, env? } ⏸ -> Conn` lanza el servidor con `enu.proc.spawn` (S16) y
le habla por stdin (requests JSON con `enu.json.encode` + `Proc:write`), leyendo responses de
stdout línea a línea (`Proc:read_line` + `enu.json.decode`). Demultiplexado: una **task lectora
dedicada** (`dispatch_loop`) lee stdout y reparte cada response a su request pendiente por `id`
(cada `request` registra un `enu.task.future` que el lector resuelve), permitiendo varios requests
en vuelo sin mezclar respuestas. Las notificaciones del servidor (sin id) se ignoran en v1.

## Decisiones de la extensión (no tocan el core; cierran nº4)

1. **Framing newline-delimited.** Una línea = un mensaje JSON terminado en `\n`. Es el framing del
   transporte stdio de MCP en su forma simple. La alternativa **Content-Length** (cabeceras estilo
   LSP) se descartó para v1: añade complejidad de parseo sin beneficio para el harness, y el
   transporte por líneas compone exactamente con `Proc:read_line` (api.md §6) sin buffering extra.
   Se documenta en el módulo; si un servidor exigiera Content-Length, sería una iteración futura
   (el cliente lee/escribe en un único punto, fácil de extender).
2. **Prefijo `mcp__<servidor>__<tool>`.** Las tools MCP se registran en el agente con este nombre.
   Es la convención de namespacing del ecosistema MCP: evita choques entre servidores y entre una
   tool MCP y una propia, y hace legible el patrón de permiso (`allow = {"mcp__github__*"}`).
3. **Confianza = `permissions.default = "ask"`.** Las tools MCP son de TERCEROS; se registran con
   default "ask" (agente.md §5), nunca el "allow" de las de solo lectura propias. Así requieren
   permiso EXPLÍCITO y en headless sin `allow` el pipeline de §5 las DENIEGA con error accionable.
   No hay caso especial en el agente: una tool MCP pasa por la misma valla (permisos → hooks →
   handler) que cualquier otra. Coherente con agente.md §3 ("MCP encaja aquí sin caso especial").
4. **`mcp.toml` como formato de configuración** (división datos/código, ADR-005):
   `[servers.<nombre>] command = [...] cwd? env?`. Ausente → no se conecta nada (lo normal).
   `mcp.connect_configured` los lanza desde una task; un servidor que falla no impide a los demás.

## Ciclo de vida del proceso (api.md §6)

El servidor se lanza, vive mientras la `Conn` exista, y se mata limpiamente: `Proc:kill` registrado
en `enu.task.cleanup` (muere al terminar la task dueña) y `Conn:close()` explícito e idempotente.
Un servidor que MUERE (EOF en stdout) hace que `dispatch_loop` marque la conexión caída y despierte
a TODOS los requests pendientes con `EMCP` (nadie cuelga para siempre). Al cerrar, las tools del
servidor se re-registran con un handler que falla accionable (la extensión `agent` no expone un
des-registro público — un re-registro SUSTITUYE, agente.md §3 — y dejar tools que invoquen una
conexión muerta sería peor: el error vuelve como tool_result is_error que el modelo ve).

## Mapeo de resultados

El resultado de `tools/call` de MCP (`{ content = [{type="text",text},...], isError? }`) se traduce
al formato del handler del agente (string | Block[]): se concatenan los bloques de texto; un
`isError = true` se propaga lanzando `EMCP` (el loop lo vuelve tool_result is_error). Imágenes y
otros tipos de bloque quedan para una iteración posterior (v1 cubre texto, el caso central).

## NO amplía api.md (corolario de completitud satisfecho)

`enu.proc` §6 (spawn/write/read_line/kill) + `enu.json` §12 + `enu.task` §4 (spawn/future/cleanup) +
`enu.fs`/`enu.toml`/`enu.config.dir` + el módulo `agent` (`agent.tool`, `agent.tools`) bastaron
EXACTOS para construir MCP. APILevel sigue en **2**; ni una función pública del core de más. Error
de la extensión: `EMCP` (forma ADR-009). Sin hallazgos `G##`.

## Tests (`mcp_test.go`)

El servidor MCP de prueba es un **mini-programa Go** (fuente embebida en el test) que se compila a
un binario temporal con `go build` (sin red, sin dependencias externas más allá de Go, garantizado
en el entorno — la opción más robusta sugerida por el enunciado). Habla JSON-RPC/stdio: responde a
`initialize`, `notifications/initialized`, `tools/list` (anuncia `echo` y `boom`) y `tools/call`
(las ejecuta; `boom` devuelve `isError=true`). Casos: carga+activa (builtin); connect + handshake +
tools/list + registro con prefijo y confianza; **CICLO COMPLETO** (el adaptador de prueba pide
`mcp__srv__echo`, el handler hace `tools/call`, "eco: hola MCP" se realimenta al modelo); confianza
headless (tool MCP sin allow → DENY accionable que nombra "headless"/tool/"allow"); `isError` del
servidor propagado a tool_result is_error; ciclo de vida (pid vivo tras connect, muerto tras
`close()`, vía `pidAlive`/`waitDead` de proc_test).

**Nota anti-race:** registrar globales Go (`SetGlobal`) DESPUÉS de Boot es una carrera con el
scheduler (el auto-connect de mcp ya corre); el test de ciclo de vida instala sus helpers
(`__publish_pid`, `__mcp_pid`) ANTES de Boot (`bootMCPWith(preBoot)`). El resto de tests no tocan
globales tras Boot.

`CGO_ENABLED=0 go build`/`go vet ./...` verdes; `gofmt -l` limpio; `CGO_ENABLED=1 go test -race
-timeout 120s -count=2 ./internal/...` verde (~54 s), sin flaky; no regresiona S01–S40.

## Lo que reusará S43 (chat)

`require("mcp")` (`mcp.connect`/`mcp.servers`/`mcp.get`) como cualquier extensión de tercero, y las
tools MCP ya registradas en el agente que el chat lista/invoca por el pipeline de permisos de §5
igual que las propias (la UI pinta el permiso de una tool MCP como el de cualquier otra).

**Nota de proceso.** Tras código + tests + docs (puntero a S42, bitácora, cierre de arquitectura
nº4, esta entrada) + build/vet/gofmt/race-count=2 verdes, se commitea y pushea SIN demora.
