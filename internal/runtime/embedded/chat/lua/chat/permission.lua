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
-- mejora (claude_decisions.md S43). El contrato del modal (un widget focusable que
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
local function split_lines(s)
  local out = {}
  for l in (tostring(s) .. "\n"):gmatch("(.-)\n") do
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
      local ok, j = pcall(nu.json.encode, v)
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

-- on_key(ev) -> boolean (chat.md §5). `a` permite una vez, `d`/`esc` deniegan. Lo
-- consume todo (es un modal: el input no debe pasar al editor de abajo mientras
-- está abierto). Llama `on_respond(granted)` una sola vez.
function Dialog:on_key(ev)
  if ev.type ~= "key" then
    return true -- modal: traga el resto del input (paste, mouse) también
  end
  local k = ev.key
  if k == "a" or k == "y" then
    self:_respond(true)
    return true
  elseif k == "d" or k == "n" or k == "esc" then
    self:_respond(false)
    return true
  end
  -- otras teclas: el modal las traga (no llegan al editor), sin decidir.
  return true
end

function Dialog:_respond(granted)
  if self._answered then
    return
  end
  self._answered = true
  if self.on_respond then
    self.on_respond(granted == true)
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
  lines[#lines + 1] = line({ { text = nu.text.truncate("┤ Permiso requerido ├", w), style = accent } })
  lines[#lines + 1] = line({ { text = nu.text.truncate("Tool: " .. tostring(self.tool), w), style = warn } })
  for _, l in ipairs(split_lines(args_summary(self.args))) do
    lines[#lines + 1] = line({ { text = nu.text.truncate(l, w) } })
  end
  if self.suggested then
    lines[#lines + 1] = line({ { text = nu.text.truncate("Patrón sugerido: " .. tostring(self.suggested), w), style = dim } })
  end
  lines[#lines + 1] = line({ { text = nu.text.truncate("[a] permitir una vez   [d] denegar   [esc] denegar", w), style = dim } })
  return nu.ui.block(lines)
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
