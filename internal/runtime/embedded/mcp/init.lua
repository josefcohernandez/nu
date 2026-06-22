-- Extensión oficial `mcp` (S41): integración de servidores **MCP** (Model
-- Context Protocol) como tools del agente.
--
-- Implementa la **capa 2** de [arquitectura.md](../../../../docs/arquitectura.md)
-- ("Procesos externos vía subproceso, JSON-RPC/stdio. MCP vive aquí, implementado
-- como extensión oficial Lua sobre las primitivas `io.spawn` + codecs: el core no
-- sabe qué es MCP"). Cierra la **cuestión abierta nº4** de arquitectura.md (el
-- contrato de la extensión MCP: formato de configuración, ciclo de vida de los
-- procesos, mapeo de tools y de su confianza).
--
-- ADR-003: el core NO sabe lo que es MCP; todo es Lua puro sobre la API pública
-- congelada ([api.md](../../../../docs/api.md)) —`nu.proc` (§6, S16) para lanzar y
-- hablar con el servidor por stdio, `nu.json` (§12, S18) para JSON-RPC 2.0,
-- `nu.task` (§4) para el ciclo de vida y el demultiplexado de respuestas— y sobre
-- la extensión `agent` (S39, en `requires`): cada tool que el servidor anuncia se
-- registra con `agent.tool{...}` exactamente igual que una tool de fichero
-- (agente.md §3: "MCP encaja aquí sin caso especial"). Sin privilegio de kernel.
--
-- El `init.lua` solo CABLEA: deja el módulo público accesible por `require`. La
-- API de consumo (`mcp.connect`, `mcp.servers`...) la expone el módulo `mcp`,
-- requerible por `chat` (S43) y cualquier extensión/script con `require("mcp")`.
--
-- NO se conecta a ningún servidor al cargar: un servidor MCP es un proceso externo
-- y lanzarlo es un acto del usuario/host (vía `mcp.toml` o `mcp.connect`
-- explícito). Cargar la extensión solo expone la maquinaria.

local mcp = require("mcp")

-- Auto-conexión por configuración (`mcp.toml`, ver módulo). Es perezosa y tolera
-- la ausencia del fichero (lo normal): sin `mcp.toml` no se lanza nada. Se hace en
-- una task porque leer el fichero y conectar SUSPENDEN (`nu.fs`, `nu.proc`,
-- handshake JSON-RPC) y el `init.lua` corre en el estado principal sin token de
-- task. `connect_configured` tolera la ausencia de `mcp.toml` (devuelve sin hacer
-- nada), así que la task vive lo justo si no hay servidores que lanzar.
nu.task.spawn(function()
  if not mcp._has_config() then
    return
  end
  local ok, err = pcall(mcp.connect_configured)
  if not ok then
    nu.log.warn("mcp: fallo conectando servidores de mcp.toml: %s",
      (type(err) == "table" and err.message) or tostring(err))
  end
end)
