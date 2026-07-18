-- chat.input — el editor MULTILÍNEA del chat (chat.md §3).
--
-- POR QUÉ UN WIDGET PROPIO. El `toolkit.input` (S42) es un editor de UNA línea:
-- su contrato (focusable + `on_key` + caret) es el correcto, pero solo edita una
-- línea y deja pasar `enter`/`tab`. chat.md §3 pide un editor MULTILÍNEA donde
-- «enter envía, shift+enter (o alt+enter según terminal) inserta línea». Como el
-- toolkit deja el catálogo de widgets abierto (es la extensión quien fija los
-- suyos, S42), el multilínea es «la extensión natural del mismo contrato»
-- (toolkit.lua §"Lo que reusará S43"): mismo árbol/dirty/focus del
-- `toolkit.widget`, solo cambia `on_key`/`compose`/`caret`.
--
-- EL MODELO. El texto se guarda como un array de LÍNEAS (`self.lines`) y un caret
-- `(row, col)` en BYTES (v1: texto ASCII/UTF-8 simple, como el `toolkit.input`; el
-- editor rico por graphemes es trabajo posterior, chat.md §3). `on_key`:
--   * un carácter imprimible se inserta en el caret;
--   * `backspace` borra a la izquierda (uniendo líneas en el borde);
--   * flechas/home/end mueven el caret (con salto de línea en los bordes);
--   * `enter` SIN modificador NO lo consume el editor: lo DEJA PASAR (devuelve
--     false) para que la app/keymap lo recoja como "enviar" (chat.md §3) — el
--     mismo patrón que el `toolkit.input` con enter;
--   * `enter` CON shift/alt (`ev.mods.shift`/`ev.mods.alt`, api.md §9.3) inserta
--     una línea nueva (lo consume). Así "enter envía / shift+enter nueva línea"
--     vive en un solo sitio: el editor consume el salto explícito y delega el
--     envío a quien tiene el contexto de la sesión.
--   * historial con `↑/↓` EN EL BORDE del editor (chat.md §3): si el caret está en
--     la primera/última línea, `up`/`down` no mueven dentro del texto sino que
--     piden el mensaje anterior/siguiente del historial (vía callbacks
--     `on_history_prev`/`on_history_next` que el chat conecta); si no, navegan
--     líneas. Esto deja el historial al chat sin que el editor sepa de sesiones.
--
-- THEME (G22, chat.md §7): como todo widget del toolkit, resuelve sus colores con
-- el theme de la app al componer (placeholder atenuado con "dim"). El cursor REAL
-- lo coloca la app con `Region:cursor` (api.md §9.1) vía `caret_col`/`caret_row`.

local widget = require("toolkit.widget")
local theme_mod = require("toolkit.theme")

local M = {}

local Input = widget.derive()

-- resolve_theme(w): el theme efectivo (el de la app, o el default si se compone
-- fuera de una app, p. ej. en un test). Igual criterio que los widgets del toolkit.
local function resolve_theme(w)
  if w._app and w._app.theme then
    return w._app.theme
  end
  return theme_mod.default
end

-- value() -> string: el texto completo (líneas unidas por "\n"). set_value(s) lo
-- reemplaza y coloca el caret al final. clear() lo vacía. Encadenables.
function Input:value()
  return table.concat(self.lines, "\n")
end

function Input:set_value(s)
  s = tostring(s == nil and "" or s)
  self.lines = {}
  for line in (s .. "\n"):gmatch("(.-)\n") do
    self.lines[#self.lines + 1] = line
  end
  if #self.lines == 0 then
    self.lines = { "" }
  end
  self.row = #self.lines
  self.col = #self.lines[self.row]
  self:mark_dirty()
  return self
end

function Input:clear()
  self.lines = { "" }
  self.row = 1
  self.col = 0
  self:mark_dirty()
  return self
end

-- is_empty() -> bool: ¿el editor está vacío? (una sola línea vacía). Lo usa el
-- chat para no enviar un mensaje en blanco.
function Input:is_empty()
  return #self.lines == 1 and self.lines[1] == ""
end

-- Helpers de edición sobre `self.lines`/`self.row`/`self.col`. Toda edición ensucia.
local function cur(self)
  return self.lines[self.row] or ""
end

-- changed(self): avisa al chat de que el CONTENIDO cambió (no solo el caret), para
-- que recalcule el alto del editor (crece/encoge con las líneas) y repinte. Sin
-- callback (uso fuera del chat), no hace nada.
local function changed(self)
  if self.on_change then self.on_change() end
end

local function insert_text(self, s)
  -- `s` puede traer varios "\n" (pegado multilínea, chat.md §3): se parte y se
  -- intercala, abriendo líneas nuevas.
  local line = cur(self)
  local before = line:sub(1, self.col)
  local after = line:sub(self.col + 1)
  local parts = {}
  for p in (s .. "\n"):gmatch("(.-)\n") do
    parts[#parts + 1] = p
  end
  if #parts == 1 then
    self.lines[self.row] = before .. parts[1] .. after
    self.col = #before + #parts[1]
  else
    -- primera parte se pega al final de `before`; la última precede a `after`; las
    -- intermedias son líneas completas nuevas.
    self.lines[self.row] = before .. parts[1]
    local insert_at = self.row
    for i = 2, #parts do
      insert_at = insert_at + 1
      table.insert(self.lines, insert_at, parts[i])
    end
    self.lines[insert_at] = self.lines[insert_at] .. after
    self.row = insert_at
    self.col = #parts[#parts]
  end
  self:mark_dirty()
  changed(self)
end

local function newline(self)
  local line = cur(self)
  local before = line:sub(1, self.col)
  local after = line:sub(self.col + 1)
  self.lines[self.row] = before
  table.insert(self.lines, self.row + 1, after)
  self.row = self.row + 1
  self.col = 0
  self:mark_dirty()
  changed(self)
end

local function backspace(self)
  if self.col > 0 then
    local line = cur(self)
    self.lines[self.row] = line:sub(1, self.col - 1) .. line:sub(self.col + 1)
    self.col = self.col - 1
    self:mark_dirty()
  elseif self.row > 1 then
    -- borde de línea: une con la anterior.
    local prev = self.lines[self.row - 1]
    local merged_col = #prev
    self.lines[self.row - 1] = prev .. cur(self)
    table.remove(self.lines, self.row)
    self.row = self.row - 1
    self.col = merged_col
    self:mark_dirty()
  end
  changed(self)
end

-- Input:insert(s) inserta `s` en el caret (público; lo usa el picker de menciones
-- `@` para inyectar la ruta elegida, chat.md §3 / P26). Reusa el helper local.
function Input:insert(s)
  insert_text(self, tostring(s or ""))
  return self
end

-- on_key(ev) -> boolean (chat.md §3). El contrato de focus (api.md §9.3): la app
-- llama solo en el editor ENFOCADO. Consume edición/navegación; deja pasar `enter`
-- (sin mods), `tab`, `esc` para que la app los gestione (enviar / foco / cancelar).
function Input:on_key(ev)
  if ev.type == "paste" and ev.text then
    insert_text(self, ev.text)
    return true
  end
  if ev.type ~= "key" then
    return false
  end
  local k = ev.key
  local mods = ev.mods or {}

  if k == "enter" then
    -- shift+enter / alt+enter = nueva línea (lo consume); enter "pelado" = enviar
    -- (lo deja pasar a la app, chat.md §3).
    if mods.shift or mods.alt then
      newline(self)
      return true
    end
    return false
  elseif k == "backspace" then
    backspace(self)
    return true
  elseif k == "left" then
    if self.col > 0 then
      self.col = self.col - 1
    elseif self.row > 1 then
      self.row = self.row - 1
      self.col = #cur(self)
    end
    self:_notify()
    return true
  elseif k == "right" then
    if self.col < #cur(self) then
      self.col = self.col + 1
    elseif self.row < #self.lines then
      self.row = self.row + 1
      self.col = 0
    end
    self:_notify()
    return true
  elseif k == "up" then
    if self.row > 1 then
      self.row = self.row - 1
      self.col = math.min(self.col, #cur(self))
      self:_notify()
    elseif self.on_history_prev then
      -- borde superior: historial (chat.md §3).
      self.on_history_prev()
    end
    return true
  elseif k == "down" then
    if self.row < #self.lines then
      self.row = self.row + 1
      self.col = math.min(self.col, #cur(self))
      self:_notify()
    elseif self.on_history_next then
      self.on_history_next()
    end
    return true
  elseif k == "home" then
    self.col = 0
    self:_notify()
    return true
  elseif k == "end" then
    self.col = #cur(self)
    self:_notify()
    return true
  elseif type(k) == "string" and #k == 1 then
    insert_text(self, k)
    -- Menciones `@`: al teclear `@`, avisa al chat para abrir el picker difuso de
    -- ficheros (chat.md §3 / P26). El `@` queda insertado; el picker inyecta la
    -- ruta tras él. Sin callback (uso fuera del chat), es un carácter normal.
    if k == "@" and self.on_mention then
      self.on_mention()
    end
    return true
  end
  -- tab/esc y demás: no las consume el editor (las gestiona la app).
  return false
end

-- compose(w, h) -> Block (api.md §9.2). Pinta TODAS las líneas (un Block
-- multilínea). Si el texto está vacío y el editor no tiene foco, un placeholder
-- atenuado (theme "dim", G22). Cada línea se trunca al ancho `w` (`enu.text.truncate`,
-- graphemes correctos). El caret real lo coloca la app con `Region:cursor`.
function Input:compose(w, _h)
  local th = resolve_theme(self)
  if w <= 0 then
    return nil
  end
  local focused = (self._app and self._app.focused == self)

  -- Placeholder: visible siempre que el editor esté VACÍO (también con foco). Antes
  -- se ocultaba al enfocar, pero el chat arranca con el foco en el editor, así que la
  -- pista no se veía nunca; el cursor real (Region:cursor) cae sobre el inicio del
  -- placeholder atenuado, que es justo lo que se espera de un prompt vacío.
  if self:is_empty() and self.placeholder then
    local txt = enu.text.truncate(self.placeholder, w)
    return enu.ui.block({ { { text = txt, style = th:style({ fg = "dim" }) } } })
  end

  local lines = {}
  for _, line in ipairs(self.lines) do
    local shown = line
    if w > 0 then
      shown = enu.text.truncate(shown, w)
    end
    lines[#lines + 1] = { { text = shown } }
  end
  return enu.ui.block(lines)
end

-- content_height(w) -> integer: nº de líneas que ocupa el editor (para que el chat
-- dimensione su banda: un input que crece al añadir líneas). v1 sin word-wrap del
-- input: una línea lógica = una fila.
function Input:content_height(_w)
  return #self.lines
end

-- caret_col()/caret_row() -> integer: dónde va el cursor REAL del terminal, para
-- `Region:cursor` (api.md §9.1). `caret_col` = ancho (en celdas, graphemes) del
-- texto a la izquierda del caret en su línea; `caret_row` = fila (0-based) del
-- caret dentro del Block del editor. La app suma su posición absoluta.
function Input:caret_col()
  return enu.text.width(cur(self):sub(1, self.col))
end

function Input:caret_row()
  return self.row - 1
end

-- toolkit.app coloca el cursor en `(_abs(focused).x + caret_col, _abs(focused).y)`
-- (una sola fila). Para que el cursor caiga en la fila correcta de un input
-- multilínea, exponemos `caret_row` y la app lo suma — pero la app del toolkit
-- (S42) solo conoce `caret_col`. Para no tocar el toolkit, el chat usa su PROPIA
-- colocación de cursor tras pintar (ver chat.lua): lee `caret_row`/`caret_col`.

-- chat.input{value?, placeholder?, id?, on_history_prev?, on_history_next?} ->
-- Input. Nace FOCUSABLE (edita texto). `pref_h` lo fija el chat según el contenido.
function M.new(opts)
  opts = opts or {}
  local i = setmetatable(widget.new({ id = opts.id, focusable = true }), Input)
  i.lines = { "" }
  i.row = 1
  i.col = 0
  i.placeholder = opts.placeholder
  i.on_history_prev = opts.on_history_prev
  i.on_history_next = opts.on_history_next
  i.on_mention = opts.on_mention
  i.on_change = opts.on_change
  if opts.value and opts.value ~= "" then
    i:set_value(opts.value)
  end
  i.pref_h = opts.pref_h or 1
  return i
end

return M
