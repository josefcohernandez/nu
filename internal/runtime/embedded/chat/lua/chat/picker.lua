-- chat.picker — un picker difuso modal reutilizable (chat.md §3/§4).
--
-- QUÉ ES. Un widget FOCUSABLE (toolkit S42) que muestra una línea de consulta y
-- una lista filtrada de candidatos; se teclea para filtrar (difuso, vía
-- `enu.search.fuzzy`, api.md §11 — la primitiva caliente del picker), `↑/↓` mueven
-- la selección, `enter` elige, `esc` cancela. Lo usan:
--   - las menciones `@` (P26): candidatos = `enu.search.files` del repo; al elegir,
--     la ruta se inyecta en el editor (el agente decide leerla, chat.md §3);
--   - el autocompletado de `/` (P29): candidatos = nombres de comando.
--
-- Es la capa modal del chat (chat.md §1, sobre el `toolkit.stack`): mientras está
-- abierto tiene el foco y traga el input. THEME (G22): colores semánticos del
-- theme de la app, como el resto de widgets.

local widget = require("toolkit.widget")
local theme_mod = require("toolkit.theme")

local M = {}

local Picker = widget.derive()

local function resolve_theme(w)
  if w._app and w._app.theme then
    return w._app.theme
  end
  return theme_mod.default
end

-- Picker:_refilter() recalcula la lista visible a partir de `query`. Sin consulta
-- muestra el principio de los candidatos; con consulta, el ranking difuso
-- (`enu.search.fuzzy` devuelve {index, score} ordenado, api.md §11). Acota a
-- `max_rows*` para no medir/pintar de más en repos enormes.
function Picker:_refilter()
  local cands = self.candidates
  local filtered = {}
  if self.query == "" then
    for i = 1, math.min(#cands, self.max_list) do
      filtered[i] = cands[i]
    end
  else
    local ok, res = pcall(enu.search.fuzzy, self.query, cands, { max = self.max_list })
    if ok and type(res) == "table" then
      for _, r in ipairs(res) do
        filtered[#filtered + 1] = cands[r.index]
      end
    end
  end
  self.filtered = filtered
  if self.sel > #filtered then self.sel = #filtered end
  if self.sel < 1 then self.sel = (#filtered > 0 and 1 or 0) end
  self:mark_dirty()
end

-- Picker:on_key(ev) -> bool. Navegación/edición de la consulta. Es un modal:
-- traga TODO el input (no llega al editor de abajo). enter elige, esc cancela.
function Picker:on_key(ev)
  if ev.type == "paste" and ev.text then
    self.query = self.query .. ev.text:gsub("\n", " ")
    self:_refilter()
    return true
  end
  if ev.type ~= "key" then
    return true
  end
  local k = ev.key
  if k == "esc" then
    if self.on_cancel then self.on_cancel() end
    return true
  elseif k == "enter" then
    local v = self.filtered[self.sel]
    if v ~= nil and self.on_select then self.on_select(v) end
    return true
  elseif k == "up" then
    if self.sel > 1 then self.sel = self.sel - 1; self:mark_dirty() end
    return true
  elseif k == "down" then
    if self.sel < #self.filtered then self.sel = self.sel + 1; self:mark_dirty() end
    return true
  elseif k == "backspace" then
    self.query = self.query:sub(1, #self.query - 1)
    self:_refilter()
    return true
  elseif type(k) == "string" and #k == 1 then
    self.query = self.query .. k
    self:_refilter()
    return true
  end
  return true
end

-- Picker:compose(w, h) -> Block. Título + consulta + filas (la seleccionada
-- resaltada) + ayuda. Cada línea recortada al ancho (`enu.text.truncate`).
function Picker:compose(w, _h)
  if w <= 0 then return nil end
  local th = resolve_theme(self)
  local dim = th:style({ fg = "dim" })
  local accent = th:style({ fg = "accent", bold = true })
  -- fila seleccionada: texto en acento sobre un FONDO de selección (resalte de
  -- producto, no solo un glifo). El título lo pinta el marco (toolkit.box).
  local sel = th:style({ fg = "accent", bg = "selection", bold = true })
  local sel_fill = th:style({ bg = "selection" })

  local lines = {}
  -- línea de consulta con un prompt de búsqueda y un contador de resultados.
  lines[#lines + 1] = {
    { text = "⌕ ", style = accent },
    { text = enu.text.truncate(self.query, math.max(0, w - 12)) },
    { text = string.format("   (%d)", #self.filtered), style = dim },
  }
  local n = math.min(#self.filtered, self.max_rows)
  for i = 1, n do
    if i == self.sel then
      local txt = enu.text.truncate("› " .. tostring(self.filtered[i]), w)
      -- rellena la fila seleccionada hasta el ancho para que el fondo sea continuo.
      local pad = w - enu.text.width(txt)
      local row = { { text = txt, style = sel } }
      if pad > 0 then row[#row + 1] = { text = string.rep(" ", pad), style = sel_fill } end
      lines[#lines + 1] = row
    else
      lines[#lines + 1] = { { text = enu.text.truncate("  " .. tostring(self.filtered[i]), w) } }
    end
  end
  if #self.filtered == 0 then
    lines[#lines + 1] = { { text = "(sin coincidencias)", style = dim } }
  end
  lines[#lines + 1] = { { text = enu.text.truncate("↑↓ mover · enter elegir · esc cancelar", w), style = dim } }
  return enu.ui.block(lines)
end

-- chat.picker.new{ title, candidates, on_select(value), on_cancel?, query?,
-- max_rows? } -> Picker. Un modal FOCUSABLE; el chat lo añade al stack y le da el
-- foco (chat.md §1/§5).
function M.new(opts)
  opts = opts or {}
  local p = setmetatable(widget.new({ id = opts.id or "picker", focusable = true }), Picker)
  p.title = opts.title or "Buscar"
  p.candidates = opts.candidates or {}
  p.on_select = opts.on_select
  p.on_cancel = opts.on_cancel
  p.query = opts.query or ""
  p.sel = 1
  p.max_rows = opts.max_rows or 10
  p.max_list = opts.max_list or 500
  p:_refilter()
  return p
end

return M
