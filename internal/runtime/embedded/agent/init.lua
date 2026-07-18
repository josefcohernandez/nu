-- Extensión oficial `agent` (S39): el **motor headless** del harness.
--
-- Implementa el contrato de [agente.md](../../../../docs/agente.md): el TURNO
-- (loop de conversación que pide al provider, consume el stream canónico de
-- Eventos, ejecuta tool calls y re-pide hasta `done` sin tools), el registro de
-- TOOLS, los PERMISOS (con error accionable al denegar), los HOOKS-MIDDLEWARE
-- (`request.pre`/`tool.pre`/`tool.post`/`permission`, registro PROPIO de la
-- extensión, NO el bus `enu.events`) y los eventos `agent:*` (sí en el bus).
--
-- ADR-003: el core NO sabe lo que es un agente; todo es Lua puro sobre la API
-- pública congelada ([api.md](../../../../docs/api.md)) y sobre las extensiones
-- `providers` (S36/S37) y `sessions` (S38) —declaradas en `requires` del
-- plugin.toml: el loader (§14) garantiza que se carguen antes—. Sin privilegio
-- de kernel. El namespace de eventos de esta extensión es `agent:` (el del
-- propio plugin, agente.md §4; el core solo reserva `core:`/`ui:`, CLAUDE.md).
--
-- El `init.lua` solo CABLEA: deja el módulo público accesible por `require` y
-- registra las tools básicas de fichero (dogfooding, agente.md §3). La API de
-- consumo (`agent.session`, `agent.tool`, `agent.hook`, `agent.permission`,
-- `agent.caps`...) la expone el módulo `agent`, requerible por `chat` (S43) y
-- cualquier extensión/script con `require("agent")`.

local agent = require("agent")

-- Tools básicas de fichero, registradas con la MISMA `agent.tool` que usaría
-- cualquier extensión de terceros (agente.md §3, dogfooding). Las de solo
-- lectura llevan `default = "allow"` (agente.md §5 amortiguador 1): no piden
-- permiso ni en headless. Las que mutan el disco quedan en el default "ask"
-- (deny en headless sin respuesta), así el permiso denegado muerde solo a ellas.
require("agent.tools_fs")

-- Tool `bash`: ejecuta comandos de shell. Recorta por defecto los secretos del
-- provider del entorno del hijo (G55, agente.md §3, SEC-04).
require("agent.tools_bash")
