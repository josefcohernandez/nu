-- chat.commands — los comandos slash del chat (chat.md §4).
--
-- PUNTO DE EXTENSIÓN DE PRIMERA CLASE (chat.md §4). `chat.command{ name,
-- description, args?, complete?, handler }` registra un comando. El usuario lo
-- invoca escribiendo `/<name> ...` al inicio del input (chat.md §3). Los builtins
-- (`/model`, `/sessions`, `/help`, `/quit`...) se registran con esta MISMA función
-- (dogfooding, chat.md §4).
--
-- ALCANCE S43. El registro y el dispatch (`/name args` → handler) son el contrato.
-- Los builtins que NO necesitan pickers interactivos completos se implementan
-- enteros (`/help`, `/quit`, `/model <ref>`, `/clear`); los que chat.md §4 describe
-- con un PICKER difuso (`/model` sin arg, `/sessions`, `/fork`) quedan con su
-- handler conectado a la API del agente/providers/sesiones pero el picker visual es
-- la capa modal (chat.md §1, `toolkit.stack`) — v1 acepta el argumento por texto y
-- documenta el picker como mejora (docs/decisiones-implementacion.md S43). El handler SUSPENDE
-- (⏸): aplica `Session:set_model`, reanuda, etc.

local M = {}

-- Registro de comandos por nombre (sin la barra). { name, description, args,
-- complete, handler }.
local commands = {}

-- chat.command{ name, description, args?, complete?, handler } (chat.md §4).
-- Registra (o sustituye) un comando slash. `handler(args, ctx) ⏸` recibe el texto
-- de argumentos (lo que sigue a `/name `) y el contexto del chat. Devuelve nil o
-- un string (un mensaje a mostrar en el transcript).
function M.command(spec)
  if type(spec) ~= "table" or type(spec.name) ~= "string" or spec.name == "" then
    error({ code = "EINVAL", message = "chat.command espera { name, description, handler }" })
  end
  if type(spec.handler) ~= "function" then
    error({ code = "EINVAL",
      message = string.format("chat.command %q: handler debe ser una función (args, ctx)", spec.name) })
  end
  commands[spec.name] = {
    name = spec.name,
    description = spec.description or "",
    args = spec.args,
    complete = spec.complete,
    handler = spec.handler,
  }
end

-- M.get(name) -> command|nil. M.list() -> commands (para /help y el autocompletado
-- de §3). M.parse(text) -> name, args | nil: parte un input "/name resto" en sus
-- partes (nil si no empieza por "/").
function M.get(name)
  return commands[name]
end

function M.list()
  local out = {}
  for _, c in pairs(commands) do
    out[#out + 1] = c
  end
  table.sort(out, function(a, b) return a.name < b.name end)
  return out
end

function M.parse(text)
  if type(text) ~= "string" then
    return nil
  end
  local name, rest = text:match("^/(%S+)%s*(.*)$")
  if name == nil then
    return nil
  end
  return name, rest or ""
end

-- M.complete(prefix) -> string[]: nombres de comando que empiezan por `prefix`
-- (sin la barra), para el autocompletado de `/` al inicio (chat.md §3). Ordenado.
function M.complete(prefix)
  prefix = prefix or ""
  local out = {}
  for name in pairs(commands) do
    if name:sub(1, #prefix) == prefix then
      out[#out + 1] = name
    end
  end
  table.sort(out)
  return out
end

-- M.dispatch(text, ctx) ⏸ -> handled, message. Si `text` es un comando (`/name`),
-- lo ejecuta y devuelve (true, message-o-nil). Si no, (false). Un comando
-- desconocido devuelve (true, mensaje de error) —se "maneja" mostrando el error,
-- no se envía al modelo—. El handler corre bajo pcall (un comando que lanza no
-- tumba el chat): su error se muestra en el transcript.
function M.dispatch(text, ctx)
  local name, args = M.parse(text)
  if name == nil then
    return false
  end
  local cmd = commands[name]
  if cmd == nil then
    return true, string.format("comando desconocido: /%s (prueba /help)", name)
  end
  local ok, res = pcall(cmd.handler, args, ctx)
  if not ok then
    return true, string.format("/%s falló: %s", name,
      (type(res) == "table" and res.message) or tostring(res))
  end
  return true, res
end

-- M._reset() limpia el registro (tests deterministas / reload).
function M._reset()
  commands = {}
end

-- ---------------------------------------------------------------------------
-- Builtins (chat.md §4), registrados con la misma `chat.command` (dogfooding).
-- `ctx` lo arma el chat: { chat, session, agent, providers, sessions }.
-- ---------------------------------------------------------------------------

-- M.install_builtins(deps) registra los comandos por defecto. `deps` trae los
-- módulos requeridos (agent/providers) para no re-`require`arlos aquí. Idempotente.
function M.install_builtins(deps)
  deps = deps or {}
  local agent = deps.agent
  local providers = deps.providers

  -- /help: lista los comandos (chat.md §4).
  M.command({ name = "help", description = "lista los comandos disponibles",
    handler = function(_args, _ctx)
      local lines = { "Comandos:" }
      for _, c in ipairs(M.list()) do
        local usage = "/" .. c.name
        if c.args then usage = usage .. " " .. c.args end
        lines[#lines + 1] = string.format("%s — %s", usage, c.description)
      end
      return table.concat(lines, "\n")
    end })

  -- /quit: cierra el chat (chat.md §4). Delega en el chat (ctx.chat:quit()).
  M.command({ name = "quit", description = "cierra el chat",
    handler = function(_args, ctx)
      if ctx.chat and ctx.chat.quit then
        ctx.chat:quit()
      end
      return nil
    end })

  -- /clear: limpia el input (no la historia). Útil tras escribir de más.
  M.command({ name = "clear", description = "limpia el editor de entrada",
    handler = function(_args, ctx)
      if ctx.chat and ctx.chat.input then
        ctx.chat.input:clear()
      end
      return nil
    end })

  -- /model [proveedor/modelo]: con argumento, cambia el modelo en caliente
  -- (Session:set_model, G19); sin argumento, lista los disponibles
  -- (providers.list(), chat.md §4 — el picker difuso es la capa modal, v1 lista).
  M.command({ name = "model", description = "muestra o cambia el modelo activo",
    args = "[proveedor/modelo]",
    handler = function(args, ctx)
      if args ~= nil and args ~= "" then
        ctx.session:set_model(args)   -- valida contra providers; lanza si no existe
        return "modelo cambiado a " .. args
      end
      -- sin argumento: lista (providers.list ⏸ lee disco).
      local lines = { "Modelos disponibles:" }
      if providers then
        local ok, list = pcall(providers.list)
        if ok and type(list) == "table" then
          for _, m in ipairs(list) do
            local ref = (m.provider and (m.provider .. "/" .. m.id)) or m.id
            lines[#lines + 1] = "  " .. tostring(ref)
          end
        end
      end
      lines[#lines + 1] = "(usa /model proveedor/modelo para cambiar)"
      return table.concat(lines, "\n")
    end,
    complete = function(prefix)
      local out = {}
      if providers then
        local ok, list = pcall(providers.list)
        if ok and type(list) == "table" then
          for _, m in ipairs(list) do
            local ref = (m.provider and (m.provider .. "/" .. m.id)) or m.id
            if tostring(ref):sub(1, #prefix) == prefix then
              out[#out + 1] = tostring(ref)
            end
          end
        end
      end
      return out
    end })

  -- /sessions: lista las sesiones del proyecto (sesiones.md §7); reanudar una es
  -- relanzar el chat con agent.session{ resume = id } (chat.md §4). v1 lista; el
  -- picker que reanuda al seleccionar es la capa modal (mejora documentada).
  M.command({ name = "sessions", description = "lista las sesiones guardadas",
    handler = function(_args, ctx)
      local sessions = ctx.sessions
      if sessions == nil or sessions.list == nil then
        return "el listado de sesiones no está disponible"
      end
      local ok, list = pcall(sessions.list, ctx.session.cwd or nu.fs.cwd())
      if not ok or type(list) ~= "table" or #list == 0 then
        return "no hay sesiones guardadas para este proyecto"
      end
      local lines = { "Sesiones:" }
      for _, s in ipairs(list) do
        lines[#lines + 1] = "  " .. tostring(s.id or s)
      end
      lines[#lines + 1] = "(reanuda con  nu --continue  o /sessions <id> en una versión futura)"
      return table.concat(lines, "\n")
    end })
  -- /compact, /fork, /permissions (chat.md §4): delegan en la API del agente
  -- (Session:compact/fork, política de permisos). Implementados sobre los métodos
  -- de control de sesión (P22-P25/P28): /compact compacta, /fork bifurca y sigue
  -- en la rama, /permissions ve y edita la política.
  M.command({ name = "compact", description = "compacta la conversación (manual)",
    handler = function(_args, ctx)
      if ctx.session.compact then
        local ok, err = pcall(ctx.session.compact, ctx.session)
        if ok then return "conversación compactada" end
        return "no se pudo compactar: " .. ((type(err) == "table" and err.message) or tostring(err))
      end
      return "la compactación manual aún no está disponible"
    end })

  -- /fork: bifurca la sesión y CONTINÚA en la rama (chat.md §4, P28). Usa
  -- Session:fork (P22) y cambia la sesión activa del chat (Chat:switch_session).
  M.command({ name = "fork", description = "bifurca la conversación y sigue en la rama",
    handler = function(_args, ctx)
      if not (ctx.session and ctx.session.fork) then
        return "el fork de sesión no está disponible"
      end
      local ok, child = pcall(ctx.session.fork, ctx.session)
      if not ok then
        return "no se pudo bifurcar: " .. ((type(child) == "table" and child.message) or tostring(child))
      end
      if ctx.chat and ctx.chat.switch_session then
        ctx.chat:switch_session(child)
      end
      return "sesión bifurcada; continúas en la rama " .. tostring(child.id)
    end })

  -- /permissions: ve y EDITA la política de permisos de la sesión (chat.md §4/§5,
  -- P28). Sin args, la muestra. Subcomandos: `allow <patrón>`, `deny <patrón>`,
  -- `mode ask|auto`. NUNCA persiste al repo (agente.md §11); para persistir global
  -- usa "permitir siempre (global)" en el diálogo (P29).
  M.command({ name = "permissions", description = "ve y edita la política de permisos",
    args = "[allow|deny <patrón> | mode ask|auto]",
    handler = function(args, ctx)
      local s = ctx.session
      local verb, rest = (args or ""):match("^(%S+)%s*(.*)$")
      if verb == "allow" and rest ~= "" then
        s:allow(rest); return "allow añadido: " .. rest
      elseif verb == "deny" and rest ~= "" then
        s:deny(rest); return "deny añadido: " .. rest
      elseif verb == "mode" and (rest == "ask" or rest == "auto") then
        s:set_permission_mode(rest); return "modo de permisos: " .. rest
      elseif verb ~= nil and verb ~= "" then
        return "uso: /permissions [allow|deny <patrón> | mode ask|auto]"
      end
      -- sin args: muestra la política actual.
      local v = s:permissions_view()
      local lines = { "Permisos (modo: " .. tostring(v.mode) .. ")" }
      lines[#lines + 1] = "  allow:"
      for _, p in ipairs(v.allow) do lines[#lines + 1] = "    - " .. p end
      if #v.allow == 0 then lines[#lines + 1] = "    (ninguno)" end
      lines[#lines + 1] = "  deny:"
      for _, p in ipairs(v.deny) do lines[#lines + 1] = "    - " .. p end
      if #v.deny == 0 then lines[#lines + 1] = "    (ninguno)" end
      return table.concat(lines, "\n")
    end })

  -- /think: ve y cambia el control de razonamiento de la sesión (ADR-016). Sin
  -- args, muestra el modo vigente; con args lo cambia (Session:set_thinking):
  -- `off`, `adaptive`, o `budget <N>`. El dialecto real por-modelo lo resuelve el
  -- adaptador (un modelo "none" ignora la petición); aquí solo se elige el modo.
  M.command({ name = "think", description = "ve o cambia el razonamiento del modelo",
    args = "[off|adaptive|budget <N>]",
    handler = function(args, ctx)
      local s = ctx.session
      if not (s and s.set_thinking) then
        return "el control de razonamiento no está disponible"
      end
      local verb, rest = (args or ""):match("^(%S*)%s*(.*)$")
      if verb == nil or verb == "" then
        return "razonamiento: " .. s:thinking_mode()
      elseif verb == "off" or verb == "adaptive" then
        s:set_thinking(verb)
        return "razonamiento: " .. s:thinking_mode()
      elseif verb == "budget" then
        local n = tonumber(rest)
        if n == nil then
          return "uso: /think budget <N>  (N = presupuesto de tokens)"
        end
        s:set_thinking({ mode = "budget", budget = math.floor(n) })
        return string.format("razonamiento: budget (%d tokens)", math.floor(n))
      end
      return "uso: /think [off|adaptive|budget <N>]"
    end })
end

return M
