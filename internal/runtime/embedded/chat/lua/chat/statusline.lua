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

-- M.install_defaults() registra los segmentos por defecto. Idempotente por id.
function M.install_defaults()
  -- Modelo activo (izquierda).
  M.add({ id = "model", side = "left", priority = 10, render = function(ctx)
    return ctx.model or "?"
  end })

  -- Llenado de contexto en % (chat.md §6: desde Session.usage, aviso cerca del
  -- umbral). El % se calcula contra el `context` del modelo si se conoce; v1
  -- muestra los tokens de contexto si no hay denominador.
  M.add({ id = "context", side = "left", priority = 20, render = function(ctx)
    local u = ctx.usage or {}
    local tokens = u.context_tokens or 0
    if ctx.context_window and ctx.context_window > 0 then
      local pct = math.floor(100 * tokens / ctx.context_window)
      return string.format("ctx %d%%", pct)
    end
    return string.format("ctx %d", tokens)
  end })

  -- Coste acumulado de la sesión (chat.md §6).
  M.add({ id = "cost", side = "left", priority = 30, render = function(ctx)
    local u = ctx.usage or {}
    return string.format("$%.4f", u.cost_usd or 0)
  end })

  -- cwd (derecha).
  M.add({ id = "cwd", side = "right", priority = 20, render = function(ctx)
    return ctx.cwd or ""
  end })

  -- Modo de permisos (derecha).
  M.add({ id = "perms", side = "right", priority = 10, render = function(ctx)
    return ctx.perms_mode or "ask"
  end })

  -- Indicador de asks pendientes / actividad de otras sesiones (chat.md §1, G3):
  -- un contador discreto. Solo aparece si hay algo que señalar.
  M.add({ id = "pending", side = "right", priority = 5, render = function(ctx)
    local n = ctx.pending_asks or 0
    if n > 0 then
      return string.format("⏳ %d perm.", n)
    end
    return ""
  end })
end

return M
