-- chat.permission — el diálogo modal de permisos (chat.md §5).
--
-- QUÉ ES. Ante `agent:permission.asked` (agente.md §5, cuando una tool en modo
-- "ask" no está cubierta por allow/deny y hay UI), el chat abre un MODAL con la
-- tool, sus args COMPLETOS (chat.md §5: «sin truncar lo peligroso: el comando
-- entero, la ruta entera») y las opciones: permitir una vez / permitir siempre /
-- denegar. Es un widget FOCUSABLE del toolkit (S42): la app le enruta el input
-- mientras es la capa de encima del `stack` (chat.md §1).
--
-- ALCANCE S43. El diálogo responde con `agent.permission.respond(id, granted)` —la
-- decisión que el turno espera (agente.md §5, future sin timeout, G3)—. Las
-- opciones por TECLA: `a` = permitir una vez (respond true), `d`/`esc` = denegar
-- (respond false). «Permitir siempre» (añadir el patrón a la política de la sesión
-- / global del usuario, chat.md §5) y la EDICIÓN del patrón propuesto antes de
-- aceptar requieren escribir `agent.toml` y un editor de patrón: su superficie
-- excede S43 (el agente S39 no expone aún la edición de política en caliente);
-- v1 ofrece una/denegar, muestra el patrón propuesto, y documenta «siempre» como
-- mejora (docs/decisiones-implementacion.md S43). El contrato del modal (un widget focusable que
-- responde al ask) queda ejercido.
--
-- THEME (G22, chat.md §7): el realce del diálogo usa colores semánticos (accent
-- para el marco, dim para la ayuda); resueltos por el theme de la app al componer.

local widget = require("toolkit.widget")
local theme_mod = require("toolkit.theme")

local M = {}

local Dialog = widget.derive()

-- split_lines(s) -> string[]: parte por "\n" (los args multilínea). Helper local
-- usado por `compose` para volcar el resumen de args línea a línea.
--
-- OJO con el idioma: el texto se asigna a un LOCAL antes del `:gmatch`. Llamar el
-- método directamente sobre `(tostring(s) .. "\n"):gmatch(...)` —método sobre una
-- expresión entre paréntesis que contiene una llamada (`tostring`) seguida de una
-- concatenación— dispara un nil-deref en el VM de gopher-lua (un fallo de su
-- generación de código, no del patrón). El local lo evita y es lo que ya hacen el
-- resto de splits del repo (p. ej. chat.input).
local function split_lines(s)
  local out = {}
  local text = tostring(s) .. "\n"
  for l in text:gmatch("(.-)\n") do
    out[#out + 1] = l
  end
  return out
end

local function resolve_theme(w)
  if w._app and w._app.theme then
    return w._app.theme
  end
  return theme_mod.default
end

-- args_summary(args) -> string. Una representación legible de los args COMPLETOS
-- (chat.md §5: sin truncar el comando/ruta). v1: serializa los pares clave=valor en
-- una línea por clave; para args anidados, su JSON. Suficiente para que el humano
-- vea exactamente qué se pide.
local function args_summary(args)
  if type(args) ~= "table" then
    return tostring(args == nil and "" or args)
  end
  local lines = {}
  for k, v in pairs(args) do
    local sv
    if type(v) == "table" then
      local ok, j = pcall(enu.json.encode, v)
      sv = ok and j or "<tabla>"
    else
      sv = tostring(v)
    end
    lines[#lines + 1] = string.format("  %s = %s", tostring(k), sv)
  end
  table.sort(lines)
  if #lines == 0 then
    return "  (sin argumentos)"
  end
  return table.concat(lines, "\n")
end

-- on_key(ev) -> boolean (chat.md §5). Opciones por tecla (P29):
--   `a` = permitir una vez · `s` = permitir siempre (sesión) ·
--   `g` = permitir siempre (global, persiste a agent.toml) · `d`/`n`/`esc` = denegar.
-- Modal: consume TODO el input (no llega al editor). Llama `on_respond(action)`
-- una sola vez, con la acción elegida ("once"|"always"|"always_global"|"deny").
function Dialog:on_key(ev)
  if ev.type ~= "key" then
    return true -- modal: traga el resto del input (paste, mouse) también
  end
  local k = ev.key
  if k == "a" or k == "y" then
    self:_respond("once")
    return true
  elseif k == "s" then
    self:_respond("always")
    return true
  elseif k == "g" then
    self:_respond("always_global")
    return true
  elseif k == "d" or k == "n" or k == "esc" then
    self:_respond("deny")
    return true
  end
  -- otras teclas: el modal las traga (no llegan al editor), sin decidir.
  return true
end

function Dialog:_respond(action)
  if self._answered then
    return
  end
  self._answered = true
  if self.on_respond then
    self.on_respond(action)
  end
end

-- compose(w, h) -> Block (chat.md §5). El diálogo: una cabecera con la tool, los
-- args completos y la línea de ayuda con las opciones. Estilizado con el theme
-- (accent para el título, dim para la ayuda; G22). Recorta cada línea a `w`.
function Dialog:compose(w, _h)
  if w <= 0 then
    return nil
  end
  local th = resolve_theme(self)
  local accent = th:style({ fg = "accent", bold = true })
  local dim = th:style({ fg = "dim" })
  local warn = th:style({ fg = "warn" })

  local function line(spans)
    return spans
  end

  local lines = {}
  -- el título lo pinta el marco (toolkit.box); aquí va el contenido.
  lines[#lines + 1] = line({ { text = enu.text.truncate("Tool: " .. tostring(self.tool), w), style = warn } })
  for _, l in ipairs(split_lines(args_summary(self.args))) do
    lines[#lines + 1] = line({ { text = enu.text.truncate(l, w) } })
  end
  if self.suggested then
    lines[#lines + 1] = line({ { text = enu.text.truncate("Patrón sugerido: " .. tostring(self.suggested), w), style = dim } })
  end
  lines[#lines + 1] = line({ { text = enu.text.truncate(
    "[a] una vez   [s] siempre (sesión)   [g] siempre (global)   [d] denegar", w), style = dim } })
  return enu.ui.block(lines)
end

-- chat.permission.new{ tool, args, suggested?, on_respond } -> Dialog. Un modal
-- FOCUSABLE. El chat lo añade al stack y le da el foco (chat.md §5).
function M.new(opts)
  opts = opts or {}
  local d = setmetatable(widget.new({ id = "perm-dialog", focusable = true }), Dialog)
  d.tool = opts.tool
  d.args = opts.args
  d.suggested = opts.suggested
  d.on_respond = opts.on_respond
  d._answered = false
  return d
end

return M
