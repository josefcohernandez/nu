-- chat.statusline — la barra de estado del chat (chat.md §6).
--
-- QUÉ ES. Una línea (un `hbox` de `toolkit.label`, S42) con segmentos: modelo
-- activo · llenado de contexto (% desde `Session.usage`) · coste acumulado · cwd ·
-- modo de permisos (chat.md §6). Más un indicador discreto de actividad de otras
-- sesiones / permisos pendientes (chat.md §1, multi-sesión G3).
--
-- EXTENSIBLE (chat.md §6/§9): `chat.statusline.add{ id, side, priority, render }`
-- registra segmentos de terceros. Los segmentos por defecto se registran con esta
-- MISMA función (dogfooding, como los builtins de §4). En S43 el conjunto por
-- defecto cubre lo que chat.md §6 nombra; el render de cada segmento devuelve un
-- string (v1) que el label pinta —`Span[]` con estilos semánticos del theme es la
-- forma rica de chat.md §6, soportada de forma natural extendiendo el render—.
--
-- THEME (G22, chat.md §7): los labels resuelven su estilo contra el theme de la
-- app. El segmento de aviso (contexto cerca del umbral) usa el color semántico
-- "warn"; chat NO hardcodea literales.

local M = {}

-- Registro de segmentos (chat.md §6/§9). Cada uno: { id, side="left"|"right",
-- priority, render = fn(ctx) -> string|Span[] }. Se ordenan por priority asc.
local segments = {}
local seg_seq = 0

-- chat.statusline.add{ id, side?, priority?, render } (chat.md §6). Registra un
-- segmento. `render(ctx)` recibe el contexto del chat (sesión activa, etc.) y
-- devuelve el texto del segmento (v1: string). Un re-`add` del mismo id lo
-- sustituye (un plugin afina un segmento por defecto). Devuelve un handle con
-- `:remove()`.
function M.add(spec)
  if type(spec) ~= "table" or type(spec.render) ~= "function" then
    error({ code = "EINVAL",
      message = "chat.statusline.add espera { id, side?, priority?, render = fn(ctx) }" })
  end
  seg_seq = seg_seq + 1
  local entry = {
    id = spec.id or ("seg-" .. seg_seq),
    side = (spec.side == "right") and "right" or "left",
    priority = spec.priority or 0,
    render = spec.render,
    seq = seg_seq,
    live = true,
  }
  -- sustituye por id si ya existe.
  for i, e in ipairs(segments) do
    if e.id == entry.id then
      segments[i] = entry
      return { remove = function() entry.live = false end }
    end
  end
  segments[#segments + 1] = entry
  return { remove = function() entry.live = false end }
end

-- M.ordered(side) -> entries: los segmentos vivos de un lado, ordenados por
-- (priority, seq). Lo usa el chat para pintar la barra.
function M.ordered(side)
  local out = {}
  for _, e in ipairs(segments) do
    if e.live and e.side == side then
      out[#out + 1] = e
    end
  end
  table.sort(out, function(a, b)
    if a.priority ~= b.priority then
      return a.priority < b.priority
    end
    return a.seq < b.seq
  end)
  return out
end

-- M._reset() limpia el registro (tests deterministas / reload). Inofensivo.
function M._reset()
  segments = {}
  seg_seq = 0
end

-- ---------------------------------------------------------------------------
-- Segmentos por defecto (chat.md §6), registrados con la misma `add` (dogfooding).
-- El `ctx` que reciben lo arma el chat: { session, model, perms_mode, cwd,
-- pending_asks, other_activity }.
-- ---------------------------------------------------------------------------

-- abbrev_cwd(path) -> string. Acorta una ruta larga para la barra: sustituye el
-- home por `~` y, si aún es muy larga, conserva solo los dos últimos segmentos
-- (`…/proyecto/sub`). Así una ruta profunda no desborda el lado derecho.
local function abbrev_cwd(path)
  if not path or path == "" then return "" end
  local home = os and os.getenv and os.getenv("HOME")
  if home and home ~= "" and path:sub(1, #home) == home then
    path = "~" .. path:sub(#home + 1)
  end
  if enu.text.width(path) <= 28 then return path end
  local segs = {}
  for s in path:gmatch("[^/]+") do segs[#segs + 1] = s end
  if #segs >= 2 then
    return "…/" .. segs[#segs - 1] .. "/" .. segs[#segs]
  end
  return path
end

-- M.install_defaults() registra los segmentos por defecto. Idempotente por id. Cada
-- `render(ctx)` devuelve `{ text, style }` (un texto y un nombre semántico de color
-- del theme, G22) —o "" para no mostrarse—; el chat los pinta como spans coloreados
-- de la barra (chat.md §6, la "forma rica" que el contrato ya anticipaba).
function M.install_defaults()
  -- Modelo activo (izquierda) — el acento de la barra.
  M.add({ id = "model", side = "left", priority = 10, render = function(ctx)
    return { text = ctx.model or "?", style = { fg = "role_assistant", bold = true } }
  end })

  -- Llenado de contexto en % (chat.md §6: desde Session.usage, AVISO cerca del
  -- umbral). El % se calcula contra el `context` del modelo si se conoce; cerca del
  -- umbral de compactación (>= 80%) el segmento pasa a color `warn` (lo prometido en
  -- la cabecera de este módulo, ahora codificado).
  M.add({ id = "context", side = "left", priority = 20, render = function(ctx)
    local u = ctx.usage or {}
    local tokens = u.context_tokens or 0
    if ctx.context_window and ctx.context_window > 0 then
      local pct = math.floor(100 * tokens / ctx.context_window)
      local style = (pct >= 80) and { fg = "warn" } or { fg = "dim" }
      return { text = string.format("ctx %d%%", pct), style = style }
    end
    return { text = string.format("ctx %d", tokens), style = { fg = "dim" } }
  end })

  -- Coste acumulado de la sesión (chat.md §6).
  M.add({ id = "cost", side = "left", priority = 30, render = function(ctx)
    local u = ctx.usage or {}
    return { text = string.format("$%.4f", u.cost_usd or 0), style = { fg = "dim" } }
  end })

  -- Modo de razonamiento (ADR-016): indicador discreto, solo si está activo.
  M.add({ id = "thinking", side = "left", priority = 25, render = function(ctx)
    local mode = ctx.thinking
    if mode and mode ~= "off" then
      return { text = "🧠 " .. tostring(mode), style = { fg = "info" } }
    end
    return ""
  end })

  -- cwd (derecha), abreviada.
  M.add({ id = "cwd", side = "right", priority = 20, render = function(ctx)
    return { text = abbrev_cwd(ctx.cwd), style = { fg = "secondary" } }
  end })

  -- Modo de permisos (derecha): `auto` resalta (verde) porque cambia el riesgo;
  -- `ask` en atenuado.
  M.add({ id = "perms", side = "right", priority = 10, render = function(ctx)
    local mode = ctx.perms_mode or "ask"
    local style = (mode == "auto") and { fg = "success" } or { fg = "dim" }
    return { text = "⏚ " .. mode, style = style }
  end })

  -- Indicador de asks pendientes / actividad de otras sesiones (chat.md §1, G3):
  -- un contador discreto. Solo aparece si hay algo que señalar (en color de aviso).
  M.add({ id = "pending", side = "right", priority = 5, render = function(ctx)
    local n = ctx.pending_asks or 0
    if n > 0 then
      return { text = string.format("⏳ %d perm.", n), style = { fg = "warn" } }
    end
    return ""
  end })
end

return M
