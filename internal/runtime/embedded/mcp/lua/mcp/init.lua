-- Módulo público de la extensión `mcp` (S41): el cliente JSON-RPC/stdio y el
-- mapeo de tools MCP al agente.
--
-- Implementa la **capa 2** de [arquitectura.md](../../../../../docs/arquitectura.md)
-- y cierra su **cuestión abierta nº4**. Tres piezas:
--
--   1. **Cliente JSON-RPC 2.0 sobre stdio** (`Conn`): lanza el servidor con
--      `nu.proc.spawn`, le habla por stdin escribiendo requests JSON con
--      `nu.json.encode` + `Proc:write`, y lee responses de stdout línea a línea
--      con `Proc:read_line` + `nu.json.decode`. **Framing por líneas** (una línea
--      = un mensaje JSON terminado en `\n`): es el framing newline-delimited del
--      transporte stdio de MCP (ver docs/decisiones-implementacion.md S41 para la justificación
--      y la alternativa Content-Length descartada para v1).
--   2. **Ciclo de vida del proceso** (§"Ciclo de vida"): el servidor se lanza,
--      se mantiene vivo mientras la conexión exista, y se mata limpiamente
--      (`Proc:kill` registrado en `nu.task.cleanup`, api.md §6) al cerrar o al
--      morir su task. Un servidor que muere (EOF en stdout) despierta a todos los
--      requests pendientes con un error `EMCP` (nadie cuelga para siempre).
--   3. **Mapeo de tools + CONFIANZA** (§"Tools y confianza"): cada tool que el
--      servidor anuncia en `tools/list` se registra con `agent.tool{...}` (S39);
--      su handler hace `tools/call` por JSON-RPC. Como las tools MCP son de
--      TERCEROS, se registran con `permissions.default = "ask"` (agente.md §5):
--      requieren permiso explícito —nunca se conceden solas como las de solo
--      lectura propias—. El usuario las habilita con `allow = {"mcp__<srv>__*"}`.
--
-- ADR-003: el core NO sabe lo que es MCP. Código de error de la extensión: `EMCP`
-- (forma de los del core, api.md §1.4 / ADR-009).

local agent = require("agent")

local M = {}

-- ---------------------------------------------------------------------------
-- Errores estructurados de la extensión (EMCP / EINVAL).
-- ---------------------------------------------------------------------------

local function emcp(message, detail)
  error({ code = "EMCP", message = message, detail = detail })
end

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- ---------------------------------------------------------------------------
-- Constantes del protocolo MCP.
-- ---------------------------------------------------------------------------

-- Versión del protocolo MCP que anunciamos en `initialize`. MCP versiona por
-- fecha; el servidor responde con la suya (que aceptamos si la habla). Ver
-- docs/decisiones-implementacion.md S41 sobre la negociación mínima de v1.
local PROTOCOL_VERSION = "2025-06-18"

-- Datos del cliente que enviamos en `initialize` (clientInfo, MCP §lifecycle).
local CLIENT_INFO = { name = "nu", version = "0.1.0" }

-- Registro de conexiones vivas por nombre de servidor (un proceso por servidor).
-- Reconectar el mismo nombre cierra la conexión anterior.
local conns = {}

-- Declaración adelantada: el traductor de resultados se define más abajo pero se
-- usa en el handler de las tools (`_register_tools`).
local mcp_result_to_content

-- ---------------------------------------------------------------------------
-- El handle Conn: una conexión a un servidor MCP.
-- ---------------------------------------------------------------------------

local Conn = {}
Conn.__index = Conn

-- next_id reparte ids de JSON-RPC monótonos y únicos por conexión (MCP exige que
-- el id de un request no se reutilice mientras la respuesta no haya llegado).
local function next_id(self)
  self._id = self._id + 1
  return self._id
end

-- _dispatch_loop es el LECTOR: una task dedicada que lee stdout línea a línea y
-- reparte cada response a su request pendiente (por `id`), o ignora las
-- notificaciones del servidor. Corre hasta EOF (el servidor cerró stdout / murió)
-- o hasta que se cierra la conexión. Es el demultiplexador que permite tener
-- varios requests en vuelo sin mezclar respuestas (cada uno espera su `future`).
--
-- Al terminar (EOF o cierre), despierta a TODOS los requests pendientes con un
-- error EMCP: nadie cuelga para siempre si el servidor muere. (G de robustez:
-- "Maneja el caso de servidor que muere", S41.)
local function dispatch_loop(self)
  while true do
    if self.closed then
      break
    end
    local ok, line = pcall(self.proc.read_line, self.proc, "stdout")
    if not ok then
      -- Error leyendo (proceso muerto, fd cerrado): tratamos como EOF.
      line = nil
    end
    if line == nil then
      -- EOF: el servidor cerró stdout (murió o terminó). Fin del lector.
      break
    end
    -- Líneas en blanco entre mensajes: las toleramos (framing newline-delimited).
    if line ~= "" and line:match("%S") then
      local okd, msg = pcall(nu.json.decode, line)
      if not okd then
        -- Una línea que no es JSON válido: el servidor MCP no debería emitirla.
        -- La logueamos y seguimos (no tumbamos el lector por un mensaje sucio;
        -- algunos servidores emiten logs por stdout pese a la spec).
        nu.log.warn("mcp[%s]: línea no-JSON ignorada en stdout: %s",
          self.name, line)
      elseif type(msg) == "table" and msg.id ~= nil and self.pending[msg.id] then
        -- Es una RESPONSE a un request nuestro: despierta a quien espera.
        local fut = self.pending[msg.id]
        self.pending[msg.id] = nil
        fut:set(msg)
      else
        -- Notificación del servidor (sin id, o id desconocido). v1 no consume
        -- notificaciones del servidor; se ignoran silenciosamente.
      end
    end
  end
  -- Fin del lector: marca caída y despierta a todos los pendientes con error.
  self.dead = true
  local pend = self.pending
  self.pending = {}
  for _, fut in pairs(pend) do
    -- Cada request pendiente recibe un "envoltorio de error" que `request`
    -- traduce a EMCP. No usamos `error` aquí (estamos en otra task).
    fut:set({ __mcp_dead = true })
  end
end

-- request(self, method, params) ⏸ -> result. Envía un request JSON-RPC y espera
-- su response (por `id`, vía future que el dispatch_loop resuelve). Lanza EMCP si
-- el servidor responde con `error`, si la conexión está muerta, o si muere
-- mientras esperamos.
local function request(self, method, params)
  if self.dead or self.closed then
    emcp(string.format("la conexión MCP %q está cerrada o el servidor murió", self.name),
      { server = self.name, method = method })
  end
  local id = next_id(self)
  local fut = nu.task.future()
  self.pending[id] = fut
  local payload = nu.json.encode({
    jsonrpc = "2.0",
    id = id,
    method = method,
    params = params or {},
  })
  -- Una línea por mensaje (framing newline-delimited del transporte stdio MCP).
  local okw, werr = pcall(self.proc.write, self.proc, payload .. "\n")
  if not okw then
    self.pending[id] = nil
    emcp(string.format("no se pudo escribir al servidor MCP %q: %s", self.name,
      (type(werr) == "table" and werr.message) or tostring(werr)),
      { server = self.name, method = method })
  end
  local msg = fut:await()
  if type(msg) == "table" and msg.__mcp_dead then
    emcp(string.format("el servidor MCP %q murió mientras esperaba la respuesta a %q",
      self.name, method), { server = self.name, method = method })
  end
  if type(msg) == "table" and msg.error ~= nil then
    local e = msg.error
    emcp(string.format("el servidor MCP %q devolvió un error en %q: %s", self.name,
      method, (type(e) == "table" and tostring(e.message)) or tostring(e)),
      { server = self.name, method = method, rpc_code = (type(e) == "table" and e.code) })
  end
  return (type(msg) == "table") and msg.result or nil
end

-- notify(self, method, params) envía una NOTIFICACIÓN JSON-RPC (sin id, sin
-- respuesta esperada). MCP usa `notifications/initialized` tras el handshake.
local function notify(self, method, params)
  local payload = nu.json.encode({
    jsonrpc = "2.0",
    method = method,
    params = params or {},
  })
  pcall(self.proc.write, self.proc, payload .. "\n")
end

-- Conn:call_tool(name, args) ⏸ -> result. Invoca `tools/call` en el servidor. Lo
-- usa el handler de cada tool registrada. `name` es el nombre REMOTO de la tool
-- (sin el prefijo del servidor que lleva el nombre registrado en el agente).
function Conn:call_tool(remote_name, args)
  return request(self, "tools/call", {
    name = remote_name,
    arguments = args or {},
  })
end

-- Conn:list_tools() ⏸ -> tool[]. Pide `tools/list` y devuelve el array de tools
-- que anuncia el servidor (cada una `{ name, description?, inputSchema? }`).
function Conn:list_tools()
  local result = request(self, "tools/list", {})
  return (type(result) == "table" and result.tools) or {}
end

-- Conn:close() mata el servidor limpiamente y desregistra sus tools. Idempotente.
-- Lo llama `nu.task.cleanup` (vida del proceso, api.md §6) y se puede llamar a
-- mano. Despierta a los pendientes (vía el lector que ve el proceso morir).
function Conn:close()
  if self.closed then
    return
  end
  self.closed = true
  conns[self.name] = nil
  -- Mata el proceso: el lector verá EOF y despertará a los pendientes con EMCP.
  pcall(self.proc.kill, self.proc)
  self:_unregister_tools()
end

-- ---------------------------------------------------------------------------
-- Tools y CONFIANZA (mapeo MCP -> agent.tool, agente.md §3 / arquitectura nº4).
-- ---------------------------------------------------------------------------

-- tool_id(server, remote) -> nombre con el que la tool se registra en el agente.
-- Prefijo `mcp__<servidor>__<tool>` (convención de namespacing del ecosistema
-- MCP): evita choques entre servidores y entre una tool MCP y una propia, y hace
-- el patrón de permiso legible (`allow = {"mcp__github__*"}`). Ver
-- docs/decisiones-implementacion.md S41.
local function tool_id(server, remote)
  return string.format("mcp__%s__%s", server, remote)
end

-- _register_tools registra en el agente cada tool anunciada por el servidor. La
-- CONFIANZA (arquitectura nº4): las tools MCP son de TERCEROS, así que se
-- registran con `permissions.default = "ask"` —requieren permiso explícito; nunca
-- el "allow" de las de solo lectura propias (agente.md §5 amortiguador 1)—. En
-- headless sin allow, el pipeline de §5 las DENIEGA con error accionable. El
-- handler captura `self` (la conexión) y el nombre remoto, y hace `tools/call`.
function Conn:_register_tools(tools)
  self.tool_names = {}
  for _, t in ipairs(tools or {}) do
    if type(t) == "table" and type(t.name) == "string" then
      local remote = t.name
      local registered = tool_id(self.name, remote)
      self.tool_names[#self.tool_names + 1] = registered
      local conn = self
      agent.tool{
        name = registered,
        description = t.description or string.format("Tool %q del servidor MCP %q.", remote, self.name),
        schema = t.inputSchema or { type = "object" },
        -- CONFIANZA: tool externa -> "ask" (permiso explícito requerido).
        permissions = { default = "ask" },
        handler = function(args, ctx)
          -- El handler corre en el pipeline COMPLETO del agente (permisos →
          -- hooks → este handler): la valla de §5 ya se cruzó al llegar aquí.
          local result = conn:call_tool(remote, args)
          return mcp_result_to_content(result)
        end,
      }
    end
  end
end

-- _unregister_tools: al cerrar la conexión, las tools del servidor dejan de
-- existir. La extensión `agent` no expone un "des-registro" público (un re-
-- registro SUSTITUYE, agente.md §3); para no dejar tools que invoquen una
-- conexión muerta, las re-registramos con un handler que falla accionable. Es
-- coherente con el contrato (una tool MCP de un servidor caído debe avisar al
-- modelo, no romper el loop: el error vuelve como tool_result is_error).
function Conn:_unregister_tools()
  for _, registered in ipairs(self.tool_names or {}) do
    local srv = self.name
    agent.tool{
      name = registered,
      description = string.format("Tool de un servidor MCP (%q) ya desconectado.", srv),
      schema = { type = "object" },
      permissions = { default = "deny" },
      handler = function()
        emcp(string.format("el servidor MCP %q está desconectado; reconéctalo para usar sus tools", srv),
          { server = srv })
      end,
    }
  end
  self.tool_names = {}
end

-- mcp_result_to_content(result) traduce el resultado de `tools/call` de MCP al
-- formato que el agente espera de un handler (string | Block[], agente.md §3).
-- Un resultado MCP es `{ content = [{type="text",text=...}, ...], isError? }`.
-- Mapeamos los bloques de texto a un Block de texto del agente; un `isError`
-- se propaga lanzando (el loop lo vuelve tool_result is_error que el modelo ve).
function mcp_result_to_content(result) -- (declarada local arriba)
  if type(result) ~= "table" then
    return tostring(result)
  end
  local parts = {}
  for _, block in ipairs(result.content or {}) do
    if type(block) == "table" then
      if block.type == "text" and type(block.text) == "string" then
        parts[#parts + 1] = block.text
      elseif block.type == "resource" and type(block.resource) == "table" then
        parts[#parts + 1] = tostring(block.resource.text or block.resource.uri or "")
      end
      -- (imágenes y otros tipos quedan para una iteración posterior; v1 cubre
      -- texto, el caso central de las tools de un servidor MCP.)
    end
  end
  local text = table.concat(parts, "\n")
  if result.isError == true then
    -- El servidor marcó el resultado como error: que el modelo lo vea como tal.
    error({ code = "EMCP", message = text ~= "" and text or "la tool MCP devolvió un error" })
  end
  return text
end

-- ---------------------------------------------------------------------------
-- Handshake e inicialización (MCP lifecycle).
-- ---------------------------------------------------------------------------

-- handshake(self) ⏸ ejecuta la inicialización MCP: `initialize` (negocia versión
-- y capacidades) → `notifications/initialized` → `tools/list` → registro de tools.
local function handshake(self)
  local init_result = request(self, "initialize", {
    protocolVersion = PROTOCOL_VERSION,
    capabilities = {},        -- el cliente nu v1 no anuncia roots/sampling
    clientInfo = CLIENT_INFO,
  })
  self.server_info = (type(init_result) == "table") and init_result.serverInfo or nil
  self.server_capabilities = (type(init_result) == "table") and init_result.capabilities or {}
  -- El servidor está listo: notificamos `initialized` (MCP exige esta secuencia
  -- antes de cualquier otra operación).
  notify(self, "notifications/initialized", {})
  -- Anuncia y registra las tools.
  local tools = self:list_tools()
  self:_register_tools(tools)
  return tools
end

-- ---------------------------------------------------------------------------
-- mcp.connect (API pública): lanza un servidor MCP y registra sus tools.
-- ---------------------------------------------------------------------------

-- mcp.connect(opts) ⏸ -> Conn. Lanza un servidor MCP y completa el handshake.
-- opts:
--   - name (string, requerido): nombre del servidor (prefijo de sus tools).
--   - command (string[], requerido): argv del servidor (sin shell, api.md §6).
--   - cwd? / env?: pasados a nu.proc.spawn.
-- Idempotencia: conectar un `name` ya conectado cierra la conexión anterior.
--
-- VIDA DEL PROCESO (api.md §6): el proceso lo posee la task que llama a `connect`
-- (o su task ancestra); el `cleanup` lo mata al terminar esa task. Para un
-- servidor de larga vida (el caso del harness), `connect` se llama desde una task
-- que vive lo que la sesión; al cerrarla (o al `Conn:close()`) el servidor muere.
function M.connect(opts)
  if type(opts) ~= "table" then
    einval("mcp.connect espera una tabla { name, command, cwd?, env? }")
  end
  if type(opts.name) ~= "string" or opts.name == "" then
    einval("mcp.connect: `name` debe ser una cadena no vacía")
  end
  if type(opts.command) ~= "table" or #opts.command == 0 then
    einval("mcp.connect: `command` debe ser un array no vacío (argv del servidor)")
  end

  -- Cierra una conexión previa con el mismo nombre (reconectar).
  if conns[opts.name] then
    conns[opts.name]:close()
  end

  local proc = nu.proc.spawn(opts.command, { cwd = opts.cwd, env = opts.env })

  local self = setmetatable({
    name = opts.name,
    proc = proc,
    pending = {},        -- id -> future de la response
    _id = 0,
    closed = false,
    dead = false,
    tool_names = {},
  }, Conn)

  -- Vida del proceso: matarlo al terminar la task que lo creó (api.md §6).
  nu.task.cleanup(function()
    self:close()
  end)

  conns[opts.name] = self

  -- El LECTOR/demultiplexador en su propia task (corre en paralelo a los
  -- requests; cada uno espera su future que el lector resuelve). El lector hereda
  -- la vida de esta task vía la jerarquía de tasks (cuando la task dueña termina,
  -- el cleanup mata el proceso → el lector ve EOF y sale).
  self.reader = nu.task.spawn(function()
    dispatch_loop(self)
  end)

  -- Handshake (initialize + initialized + tools/list + registro). Si falla,
  -- cerramos la conexión (no dejamos un proceso a medias) y propagamos.
  local ok, err = pcall(handshake, self)
  if not ok then
    self:close()
    error(err)
  end

  return self
end

-- mcp.get(name) -> Conn?. La conexión viva de un servidor, o nil.
function M.get(name)
  return conns[name]
end

-- mcp.servers() -> string[]. Nombres de los servidores conectados.
function M.servers()
  local out = {}
  for name in pairs(conns) do
    out[#out + 1] = name
  end
  return out
end

-- ---------------------------------------------------------------------------
-- Configuración declarativa (mcp.toml). División datos/código (ADR-005): los
-- servidores se DECLARAN en TOML; el protocolo lo implementa este Lua.
-- ---------------------------------------------------------------------------

-- Formato de `mcp.toml` (en `nu.config.dir()`), cierra arquitectura nº4
-- ("formato de configuración: qué servidores, cómo se declaran"):
--
--   [servers.github]
--   command = ["mcp-server-github"]
--   cwd = "/opt/proyecto"          # opcional
--   env = ["GITHUB_TOKEN=..."]     # opcional
--
-- Ausente → no se conecta nada (lo normal). Cada servidor declarado se lanza con
-- `mcp.connect` desde una task de larga vida.

local function config_path()
  return nu.config.dir() .. "/mcp.toml"
end

-- mcp._has_config() -> bool. ¿Existe `mcp.toml`? (No suspende: stat-like barato.)
-- Lo usa init.lua para decidir si arranca la auto-conexión.
function M._has_config()
  -- nu.fs.stat devuelve nil si no existe (no lanza ENOENT, api.md §5).
  return nu.fs.stat(config_path()) ~= nil
end

-- read_config() ⏸ -> { servers = {...} }. Lee y decodifica `mcp.toml`. Ausente →
-- tabla vacía. Mal formado → EMCP accionable.
local function read_config()
  local ok, raw = pcall(nu.fs.read, config_path())
  if not ok then
    if type(raw) == "table" and raw.code == "ENOENT" then
      return {}
    end
    error(raw)
  end
  local okd, decoded = pcall(nu.toml.decode, raw)
  if not okd then
    emcp(string.format("mcp.toml mal formado (%s): %s", config_path(),
      (type(decoded) == "table" and decoded.message) or tostring(decoded)))
  end
  return decoded or {}
end

-- mcp.connect_configured() ⏸ -> Conn[]. Lanza todos los servidores de `mcp.toml`.
-- Corre en una task de larga vida (su `cleanup` mata los procesos al terminar).
-- Un servidor que falla al conectar se loguea y NO impide a los demás.
function M.connect_configured()
  local cfg = read_config()
  local servers = cfg.servers or {}
  local out = {}
  for name, decl in pairs(servers) do
    if type(decl) == "table" and type(decl.command) == "table" then
      local ok, conn = pcall(M.connect, {
        name = name,
        command = decl.command,
        cwd = decl.cwd,
        env = decl.env,
      })
      if ok then
        out[#out + 1] = conn
      else
        nu.log.warn("mcp: no se pudo conectar el servidor %q: %s", name,
          (type(conn) == "table" and conn.message) or tostring(conn))
      end
    else
      nu.log.warn("mcp: servidor %q en mcp.toml sin `command` (array); ignorado", name)
    end
  end
  return out
end

return M
