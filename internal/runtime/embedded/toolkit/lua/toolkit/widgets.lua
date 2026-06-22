-- toolkit.widgets — los widgets hoja del conjunto mínimo.
--
-- ALCANCE (un conjunto MÍNIMO COHERENTE, suficiente para el criterio de hecho de
-- S42 y para que S43 —chat— construya encima; arquitectura.md deja abierto el
-- catálogo exacto). Tres hojas que cubren la anatomía de chat.md §1:
--
--   * **label**: una línea de texto estilizado. La unidad de la statusline y de
--     cabeceras. No focusable. Compone con `nu.ui.block` (un span estilizado).
--   * **text**: un bloque de texto/markdown de varias líneas, con scroll por
--     viewport. El transcript del chat (markdown streaming-safe vía
--     `nu.text.markdown`). No focusable por sí mismo (el chat le pone foco
--     envolviéndolo si quiere scroll con teclado; aquí el scroll es por API).
--   * **input**: un editor de UNA línea, FOCUSABLE: consume teclas (caracteres,
--     backspace, izquierda/derecha, home/end) y mantiene un cursor. Es el germen
--     del editor del chat (el multilínea es una extensión natural; el contrato
--     —focusable + on_key + caret— es el mismo). Compone con `nu.ui.block`.
--
-- TODOS resuelven sus colores con el THEME (G22): guardan estilos con nombres
-- semánticos y llaman `theme:style(...)` al componer, justo antes de construir el
-- Block, de modo que el core solo ve literales. El theme se toma de la app
-- (`self._app.theme`) en tiempo de composición; así un cambio de theme en la app
-- repinta todo sin tocar los widgets.

local widget = require("toolkit.widget")
local theme_mod = require("toolkit.theme")

local M = {}

-- resolve_theme(w) -> Theme. El theme efectivo de un widget: el de su app, o el
-- por defecto si aún no está montado (o si se compone fuera de una app, p. ej. en
-- un test). Centraliza la regla "los colores se resuelven contra el theme de la
-- app".
local function resolve_theme(w)
  if w._app and w._app.theme then
    return w._app.theme
  end
  return theme_mod.default
end

-- ---------------------------------------------------------------------------
-- label: una línea de texto estilizado.
-- ---------------------------------------------------------------------------

local Label = widget.derive()

-- label:set_text(s) cambia el texto y ensucia (recompone su Block, repinta su
-- área — y solo la suya, dirty tracking). Encadenable.
function Label:set_text(s)
  s = tostring(s == nil and "" or s)
  if s ~= self.text then
    self.text = s
    self:mark_dirty()
  end
  return self
end

-- label:set_style(spec) cambia el estilo SEMÁNTICO (nombres de color del theme).
-- No se resuelve aquí: se guarda el spec semántico y se resuelve al componer
-- (G22), para que un cambio de theme lo repinte sin re-set.
function Label:set_style(spec)
  self.style = spec
  self:mark_dirty()
  return self
end

-- label:compose(w, h) -> Block. Una sola línea: el texto, truncado al ancho `w`
-- (con `nu.text.truncate`, que respeta graphemes/east-asian), estilizado con el
-- theme resuelto a literales. Si el texto vacío, un Block de una línea en blanco
-- (ocupa su hueco sin desordenar el layout).
function Label:compose(w, _h)
  local th = resolve_theme(self)
  local txt = self.text or ""
  if w > 0 then
    txt = nu.text.truncate(txt, w)
  end
  local st = th:style(self.style) -- nombres → literales (o nil)
  return nu.ui.block({ { { text = txt, style = st } } })
end

-- toolkit.widgets.label{text?, style?, id?, pref_h?} -> Label. Un label es de UNA
-- línea, así que su alto preferido por defecto es 1 (lo usa un vbox como tamaño
-- fijo si no se le da `flex`): un label sin más ocupa su renglón, no 0. El llamante
-- lo cambia (`flex`/`pref_h`) si quiere otra cosa.
function M.label(opts)
  opts = opts or {}
  local l = setmetatable(widget.new({ id = opts.id, focusable = false }), Label)
  l.text = tostring(opts.text or "")
  l.style = opts.style
  l.pref_h = opts.pref_h or 1
  return l
end

-- ---------------------------------------------------------------------------
-- text: bloque multilínea de texto/markdown con viewport (scroll).
-- ---------------------------------------------------------------------------

local Text = widget.derive()

-- text:set_text(s) cambia el contenido. Ensucia. Encadenable.
function Text:set_text(s)
  s = tostring(s == nil and "" or s)
  if s ~= self.text then
    self.text = s
    self:mark_dirty()
  end
  return self
end

-- text:scroll_to(line) fija la primera línea visible (0-based). El scroll es un
-- offset de viewport sobre el Block (api.md §9.1: blit con offset = ventana;
-- "scroll = re-blit con otro offset"). Cambiar el scroll NO recompone el Block
-- (el contenido es el mismo): solo cambia el offset con que la app lo blittea, así
-- que basta pedir un repintado, no ensuciar. El recorte negativo lo hace el core.
function Text:scroll_to(line)
  line = math.max(0, math.floor(tonumber(line) or 0))
  if line ~= self.scroll then
    self.scroll = line
    -- No mark_dirty: el Block no cambia, solo el offset de blit. Pero la app debe
    -- repintar para aplicar el nuevo offset.
    self:_notify()
  end
  return self
end

-- text:compose(w, h) -> Block. Render del contenido a ancho `w`. Si `markdown`
-- está activo, `nu.text.markdown` (streaming-safe, S23); si no, `nu.text.wrap`
-- (word-wrap). Devuelve el Block COMPLETO (puede ser más alto que `h`): la app lo
-- blittea con el offset de scroll como viewport (api.md §9.1), sin reconstruirlo
-- al hacer scroll. El theme se pasa a `opts.theme` si la primitiva lo acepta (el
-- markdown es themable, api.md §10); como nuestro theme resuelve nombres y el core
-- pide literales, aquí pasamos el ancho y dejamos el estilo base.
function Text:compose(w, _h)
  local txt = self.text or ""
  if w <= 0 then
    return nil
  end
  if self.markdown then
    return nu.text.markdown(txt, { width = w })
  end
  return nu.text.wrap(txt, w)
end

-- text:content_height(w) -> integer. El alto del Block compuesto a ancho `w`
-- (cuántas líneas ocupa el contenido). Lo usa el chat para saber si hay que hacer
-- scroll y para "auto-scroll al final". Reusa el caché si vale.
function Text:content_height(w)
  local blk = self:compose(w, 0)
  return blk and blk.height or 0
end

-- toolkit.widgets.text{text?, markdown?, id?} -> Text.
function M.text(opts)
  opts = opts or {}
  local t = setmetatable(widget.new({ id = opts.id, focusable = false }), Text)
  t.text = tostring(opts.text or "")
  t.markdown = opts.markdown == true
  t.scroll = 0
  return t
end

-- ---------------------------------------------------------------------------
-- input: editor de una línea, FOCUSABLE (consume teclas).
-- ---------------------------------------------------------------------------

local Input = widget.derive()

-- input:value() -> string  /  input:set_value(s). El texto editado y el caret.
function Input:value()
  return self.text
end

function Input:set_value(s)
  s = tostring(s == nil and "" or s)
  self.text = s
  self.caret = #s
  self:mark_dirty()
  return self
end

-- Helpers de edición sobre `self.text` y `self.caret` (caret = nº de BYTES a la
-- izquierda; el texto del editor es ASCII/UTF-8 simple para v1 —el editor rico
-- multilínea/grapheme es trabajo posterior, chat.md §3—). Toda edición ensucia
-- (el Block del input cambia).
local function insert(self, s)
  self.text = self.text:sub(1, self.caret) .. s .. self.text:sub(self.caret + 1)
  self.caret = self.caret + #s
  self:mark_dirty()
end

local function backspace(self)
  if self.caret > 0 then
    self.text = self.text:sub(1, self.caret - 1) .. self.text:sub(self.caret + 1)
    self.caret = self.caret - 1
    self:mark_dirty()
  end
end

-- input:on_key(ev) -> boolean. El contrato de focus (api.md §9.3: input al
-- handler superior; aquí la app lo llama solo en el input ENFOCADO). Consume las
-- teclas de edición y devuelve true (consumido); lo que no entiende lo deja pasar
-- (false), para que un keymap de la app (p. ej. "enviar" con enter, chat.md §3)
-- lo recoja. `ev` es el evento de `nu.ui.on_input` (§9.3): `{type, key, text?}`.
function Input:on_key(ev)
  if ev.type == "paste" and ev.text then
    insert(self, ev.text)
    return true
  end
  if ev.type ~= "key" then
    return false
  end
  local k = ev.key
  if k == "backspace" then
    backspace(self)
    return true
  elseif k == "left" then
    if self.caret > 0 then self.caret = self.caret - 1; self:_notify() end
    return true
  elseif k == "right" then
    if self.caret < #self.text then self.caret = self.caret + 1; self:_notify() end
    return true
  elseif k == "home" then
    self.caret = 0; self:_notify(); return true
  elseif k == "end" then
    self.caret = #self.text; self:_notify(); return true
  elseif type(k) == "string" and #k == 1 then
    -- un carácter imprimible (las teclas con nombre como "enter"/"tab" tienen
    -- key de más de un byte; un carácter suelto es la entrada de texto).
    insert(self, k)
    return true
  end
  -- enter/tab/esc y demás: no las consume el editor de una línea (las gestiona la
  -- app: enviar, cambiar foco, cancelar). Las deja pasar.
  return false
end

-- input:compose(w, h) -> Block. Pinta el texto (con un placeholder atenuado si
-- está vacío y no enfocado) más, si está enfocado, un marcador de caret. Resuelve
-- el estilo con el theme (G22): el borde/realce de foco usa el color semántico
-- "accent". Recorta a ancho `w`.
function Input:compose(w, _h)
  local th = resolve_theme(self)
  local txt = self.text or ""
  local focused = (self._app and self._app.focused == self)

  local spans
  if txt == "" and not focused and self.placeholder then
    spans = { { text = nu.text.truncate(self.placeholder, w), style = th:style({ fg = "dim" }) } }
  else
    local shown = txt
    -- Marca visible del caret cuando está enfocado: un '|' en la posición del
    -- caret. Es un render simple (v1); el cursor REAL del terminal lo coloca la
    -- app con `Region:cursor` (api.md §9.1) cuando este input tiene el foco.
    if focused then
      shown = txt:sub(1, self.caret) .. "|" .. txt:sub(self.caret + 1)
    end
    if w > 0 then
      shown = nu.text.truncate(shown, w)
    end
    local st = focused and th:style({ fg = "accent" }) or nil
    spans = { { text = shown, style = st } }
  end
  return nu.ui.block({ spans })
end

-- input:caret_col() -> integer. La columna (en celdas) donde va el cursor real,
-- para que la app llame `Region:cursor`. Es el ancho del texto a la izquierda del
-- caret (con `nu.text.width`, graphemes correctos).
function Input:caret_col()
  return nu.text.width((self.text or ""):sub(1, self.caret))
end

-- toolkit.widgets.input{value?, placeholder?, id?} -> Input. Nace FOCUSABLE.
function M.input(opts)
  opts = opts or {}
  local i = setmetatable(widget.new({ id = opts.id, focusable = true }), Input)
  i.text = tostring(opts.value or "")
  i.caret = #i.text
  i.placeholder = opts.placeholder
  -- alto preferido por defecto: 1 línea (lo usa el vbox como tamaño fijo si no se
  -- le da flex).
  i.pref_h = opts.pref_h or 1
  return i
end

return M
