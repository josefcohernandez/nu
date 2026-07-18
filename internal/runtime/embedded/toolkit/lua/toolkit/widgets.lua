-- toolkit.widgets — los widgets hoja del conjunto mínimo.
--
-- ALCANCE (un conjunto MÍNIMO COHERENTE, suficiente para el criterio de hecho de
-- S42 y para que S43 —chat— construya encima; arquitectura.md deja abierto el
-- catálogo exacto). Tres hojas que cubren la anatomía de chat.md §1:
--
--   * **label**: una línea de texto estilizado. La unidad de la statusline y de
--     cabeceras. No focusable. Compone con `enu.ui.block` (un span estilizado).
--   * **text**: un bloque de texto/markdown de varias líneas, con scroll por
--     viewport. El transcript del chat (markdown streaming-safe vía
--     `enu.text.markdown`). No focusable por sí mismo (el chat le pone foco
--     envolviéndolo si quiere scroll con teclado; aquí el scroll es por API).
--   * **input**: un editor de UNA línea, FOCUSABLE: consume teclas (caracteres,
--     backspace, izquierda/derecha, home/end) y mantiene un cursor. Es el germen
--     del editor del chat (el multilínea es una extensión natural; el contrato
--     —focusable + on_key + caret— es el mismo). Compone con `enu.ui.block`.
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
-- (con `enu.text.truncate`, que respeta graphemes/east-asian), estilizado con el
-- theme resuelto a literales. Si el texto vacío, un Block de una línea en blanco
-- (ocupa su hueco sin desordenar el layout).
function Label:compose(w, _h)
  local th = resolve_theme(self)
  local txt = self.text or ""
  if w > 0 then
    txt = enu.text.truncate(txt, w)
  end
  local st = th:style(self.style) -- nombres → literales (o nil)
  return enu.ui.block({ { { text = txt, style = st } } })
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
-- está activo, `enu.text.markdown` (streaming-safe, S23); si no, `enu.text.wrap`
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
    -- Cableamos la paleta del theme al render de markdown (G22, api.md §10): sin
    -- `opts.theme` el markdown sale monocromo (solo bold/italic), que era lo que
    -- hacía que el transcript del chat pareciera "una terminal en blanco". El theme
    -- resuelve los nombres semánticos a literales una sola vez (cacheado).
    local th = resolve_theme(self)
    return enu.text.markdown(txt, { width = w, theme = th:markdown_opts() })
  end
  return enu.text.wrap(txt, w)
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
-- lo recoja. `ev` es el evento de `enu.ui.on_input` (§9.3): `{type, key, text?}`.
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
    spans = { { text = enu.text.truncate(self.placeholder, w), style = th:style({ fg = "dim" }) } }
  else
    local shown = txt
    -- Marca visible del caret cuando está enfocado: un '|' en la posición del
    -- caret. Es un render simple (v1); el cursor REAL del terminal lo coloca la
    -- app con `Region:cursor` (api.md §9.1) cuando este input tiene el foco.
    if focused then
      shown = txt:sub(1, self.caret) .. "|" .. txt:sub(self.caret + 1)
    end
    if w > 0 then
      shown = enu.text.truncate(shown, w)
    end
    local st = focused and th:style({ fg = "accent" }) or nil
    spans = { { text = shown, style = st } }
  end
  return enu.ui.block({ spans })
end

-- input:caret_col() -> integer. La columna (en celdas) donde va el cursor real,
-- para que la app llame `Region:cursor`. Es el ancho del texto a la izquierda del
-- caret (con `enu.text.width`, graphemes correctos).
function Input:caret_col()
  return enu.text.width((self.text or ""):sub(1, self.caret))
end

-- ---------------------------------------------------------------------------
-- box: un MARCO (borde + título opcional + padding) alrededor de UN hijo.
-- ---------------------------------------------------------------------------
--
-- Es la primitiva de decoración que faltaba (G36): sin un widget de borde, toda la
-- UI era texto plano apilado contra el margen 0. Con `box` se construyen el input
-- enmarcado (la firma "╭─ > … ─╮"), las tarjetas de tool y los modales con marco.
--
-- MODELO. `box` es un contenedor de UN hijo: COMPONE su propio Block (el marco, de
-- tamaño w×h) y coloca al hijo DENTRO, con inset = borde (1 celda por lado si hay
-- borde) + padding. La app pinta el marco (preorden: padre antes que hijo) y luego
-- blittea al hijo encima del interior, de modo que el borde sobrevive en los cantos.
-- Los caracteres de caja se estilan con el theme (`border` en reposo, `border_focus`
-- si el foco está dentro del subárbol del box); el título, con `title_style`.

local box_chars = {
  rounded = { tl = "╭", tr = "╮", bl = "╰", br = "╯", h = "─", v = "│" },
  square  = { tl = "┌", tr = "┐", bl = "└", br = "┘", h = "─", v = "│" },
}

local Box = widget.derive()

-- Box:_focus_inside() -> bool. ¿El foco de la app está en este box o en un
-- descendiente? Decide el color del borde (realce de foco). Sin app o sin foco, no.
function Box:_focus_inside()
  local f = self._app and self._app.focused
  if not f then return false end
  local n = f
  while n ~= nil do
    if n == self then return true end
    n = n.parent
  end
  return false
end

-- Box:set_title(s) cambia el título del marco (en el borde superior). Ensucia.
function Box:set_title(s)
  self.title = s and tostring(s) or nil
  self:mark_dirty()
  return self
end

-- Box:compose(w, h) -> Block. El MARCO: borde superior (con el título embebido si
-- lo hay), laterales, e inferior; interior en blanco (el hijo se pinta encima). Con
-- `border == "none"` no dibuja cantos: solo un relleno de fondo si `bg` está puesto
-- (un panel sin marco), o nada (padding puro). Los colores salen del theme (G22).
function Box:compose(w, h)
  if w <= 0 or h <= 0 then return nil end
  local th = resolve_theme(self)
  local ch = self.ch
  local bordered = self.border ~= "none"
  local bstyle = th:style({ fg = self:_focus_inside() and self.focus_color or self.border_color })
  local fill_style = self.bg and th:style({ bg = self.bg }) or nil

  -- fila interior (sin cantos): laterales + relleno, o solo relleno si sin borde.
  local function blank_row()
    if bordered then
      local inner = math.max(0, w - 2)
      return {
        { text = ch.v, style = bstyle },
        { text = string.rep(" ", inner), style = fill_style },
        { text = ch.v, style = bstyle },
      }
    else
      return { { text = string.rep(" ", w), style = fill_style } }
    end
  end

  local rows = {}
  if not bordered then
    for _ = 1, h do rows[#rows + 1] = blank_row() end
    if not fill_style then return nil end -- padding puro: nada que pintar
    return enu.ui.block(rows)
  end

  -- borde superior, con el título embebido: "tl─ título ───tr".
  local top
  if h >= 1 then
    local inner = math.max(0, w - 2)
    if self.title and self.title ~= "" and inner >= 4 then
      local cap = math.max(0, inner - 3) -- "─ " antes + " " después dejan hueco
      local title = enu.text.truncate(self.title, cap)
      local tw = enu.text.width(title)
      local dashes = math.max(0, inner - 2 - tw - 1) -- "─ " (2) + " " (1) tras título
      top = {
        { text = ch.tl, style = bstyle },
        { text = ch.h .. " ", style = bstyle },
        { text = title, style = th:style(self.title_style) },
        { text = " " .. string.rep(ch.h, dashes), style = bstyle },
        { text = ch.tr, style = bstyle },
      }
    else
      top = {
        { text = ch.tl .. string.rep(ch.h, inner) .. ch.tr, style = bstyle },
      }
    end
    rows[#rows + 1] = top
  end
  for _ = 2, h - 1 do rows[#rows + 1] = blank_row() end
  if h >= 2 then
    local inner = math.max(0, w - 2)
    rows[#rows + 1] = { { text = ch.bl .. string.rep(ch.h, inner) .. ch.br, style = bstyle } }
  end
  return enu.ui.block(rows)
end

-- Box:relayout(x, y, w, h) coloca al ÚNICO hijo dentro del marco, con inset = borde
-- (1 si bordeado) + padding. Las coordenadas del hijo son relativas al box (la app
-- las suma con `_abs`). Desciende si el hijo es a su vez un contenedor.
function Box:relayout(x, y, w, h)
  self:set_geometry(x, y, w, h)
  local bw = (self.border ~= "none") and 1 or 0
  local pt, pr, pb, pl = require("toolkit.layout")._parse_pad(self.pad)
  local ix = bw + pl
  local iy = bw + pt
  local iw = math.max(0, w - 2 * bw - pl - pr)
  local ih = math.max(0, h - 2 * bw - pt - pb)
  local child = self.children[1]
  if child and child.visible then
    child:set_geometry(ix, iy, iw, ih)
    if child.relayout then
      child:relayout(child.x, child.y, child.w, child.h)
    end
  end
end

-- Box:dispose() suelta la suscripción al foco (si la hay). La llama quien creó el
-- box (p. ej. el chat al salir) para no fugar handlers entre reloads (G2).
function Box:dispose()
  if self._focus_sub then
    self._focus_sub:cancel()
    self._focus_sub = nil
  end
end

-- toolkit.widgets.box{child?, title?, border?, pad?, ...} -> Box. Un marco con UN
-- hijo. `border`: "rounded" (default) | "square" | "none". `pad`: padding interior
-- (número/tabla, ver layout). Colores semánticos: `border_color` (default "border"),
-- `focus_color` (default "border_focus"), `title_style` (default acento+negrita),
-- `bg` (relleno interior opcional). Si `react_focus ~= false` y hay borde, se
-- suscribe a `toolkit:focus` para repintar el marco cuando el foco entra/sale.
function M.box(opts)
  opts = opts or {}
  local b = setmetatable(widget.new({ id = opts.id, focusable = false }), Box)
  b.border = opts.border or "rounded"
  b.title = opts.title
  b.pad = opts.pad
  b.bg = opts.bg
  b.border_color = opts.border_color or "border"
  b.focus_color = opts.focus_color or "border_focus"
  b.title_style = opts.title_style or { fg = "heading", bold = true }
  b.ch = box_chars[b.border] or box_chars.rounded
  if opts.child then b:add(opts.child) end
  -- Realce de foco dinámico: cuando el foco se mueve, repinta el marco (su color de
  -- borde depende de si el foco está dentro). Solo si bordeado y reactivo.
  if b.border ~= "none" and opts.react_focus ~= false and enu.events then
    b._focus_sub = enu.events.on("toolkit:focus", function()
      b:mark_dirty()
    end)
  end
  return b
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

-- ---------------------------------------------------------------------------
-- richtext: una línea de VARIOS spans estilizados (line builder con theme).
-- ---------------------------------------------------------------------------
--
-- `label` pinta una línea con UN solo estilo; muchas UIs necesitan mezclar estilos
-- en la misma línea (un segmento de statusline coloreado, "nombre" en negrita +
-- " resto" atenuado, el icono de una tool en color de estado). `richtext` guarda
-- una lista de segmentos `{text, style=spec_semántico}` y los resuelve contra el
-- theme al componer (G22). Recorta al ancho. No focusable.

local RichText = widget.derive()

-- richtext:set_spans(list) reemplaza los segmentos. Cada uno: `{text, style?}` donde
-- `style` es un spec semántico (`{fg=, bold=, ...}`) o nil. Encadenable.
function RichText:set_spans(list)
  self.spans = list or {}
  self:mark_dirty()
  return self
end

function RichText:compose(w, _h)
  if w <= 0 then return nil end
  local th = resolve_theme(self)
  local out, used = {}, 0
  for _, seg in ipairs(self.spans or {}) do
    local txt = tostring(seg.text or "")
    if used >= w then break end
    local avail = w - used
    txt = enu.text.truncate(txt, avail)
    local tw = enu.text.width(txt)
    if tw > 0 then
      out[#out + 1] = { text = txt, style = th:style(seg.style) }
      used = used + tw
    end
  end
  -- Alineación: por defecto a la izquierda. Con `align == "right"` se antepone un
  -- relleno (con `fill_bg` si se dio) que empuja el contenido al borde derecho —lo
  -- que una statusline necesita para su lado derecho—.
  if self.align == "right" and used < w then
    local pad = string.rep(" ", w - used)
    local fill = self.fill_bg and th:style({ bg = self.fill_bg }) or nil
    table.insert(out, 1, { text = pad, style = fill })
  end
  if #out == 0 then out = { { text = "" } } end
  return enu.ui.block({ out })
end

-- toolkit.widgets.richtext{spans?, align?, fill_bg?, id?, pref_h?} -> RichText.
-- `align`: "left" (default) | "right". `fill_bg`: nombre semántico del relleno de
-- alineación (para que la barra mantenga su fondo bajo el padding).
function M.richtext(opts)
  opts = opts or {}
  local r = setmetatable(widget.new({ id = opts.id, focusable = false }), RichText)
  r.spans = opts.spans or {}
  r.align = opts.align
  r.fill_bg = opts.fill_bg
  r.pref_h = opts.pref_h or 1
  return r
end

-- ---------------------------------------------------------------------------
-- spinner: indicador de actividad ANIMADO (frames vía enu.task.every).
-- ---------------------------------------------------------------------------
--
-- Lo que faltaba para que el chat no pareciera "una terminal muerta" entre el envío
-- y el primer delta: un glifo que gira + una etiqueta ("Pensando… 3s · esc para
-- interrumpir"). Avanza el frame con `enu.task.every(ms, fn)` (api.md §3, timer
-- periódico de handler síncrono) y se ensucia para repintar. `start()` arranca el
-- timer; `stop()` lo detiene (y deja de ocupar sitio si `pref_h` se pone a 0 desde
-- fuera). Idempotentes. Siempre llamar `stop()` al desmontar (no fuga el timer).

local Spinner = widget.derive()
local default_frames = { "⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏" }

function Spinner:set_label(s)
  self.label = s and tostring(s) or ""
  self:mark_dirty()
  return self
end

-- spinner:start() arranca la animación (idempotente). Repinta cada `interval` ms.
function Spinner:start()
  if self._timer then return self end
  self.frame = self.frame or 1
  self._timer = enu.task.every(self.interval, function()
    self.frame = (self.frame % #self.frames) + 1
    self:mark_dirty()
  end)
  self:mark_dirty()
  return self
end

-- spinner:stop() detiene la animación (idempotente). El widget deja de repintarse.
function Spinner:stop()
  if self._timer then
    self._timer:stop()
    self._timer = nil
  end
  return self
end

function Spinner:compose(w, _h)
  if w <= 0 then return nil end
  local th = resolve_theme(self)
  local glyph = self.frames[self.frame or 1] or ""
  local spans = { { text = glyph, style = th:style({ fg = self.color }) } }
  if self.label and self.label ~= "" then
    spans[#spans + 1] = { text = " " .. self.label, style = th:style({ fg = "dim" }) }
  end
  -- recorta la línea entera al ancho.
  local line, used = {}, 0
  for _, s in ipairs(spans) do
    local t = enu.text.truncate(s.text, math.max(0, w - used))
    used = used + enu.text.width(t)
    line[#line + 1] = { text = t, style = s.style }
  end
  return enu.ui.block({ line })
end

-- toolkit.widgets.spinner{label?, frames?, interval?, color?, id?} -> Spinner.
-- `interval` en ms (default 80). `color` semántico (default "accent"). Nace PARADO;
-- llama `start()` para animar.
function M.spinner(opts)
  opts = opts or {}
  local s = setmetatable(widget.new({ id = opts.id, focusable = false }), Spinner)
  s.frames = opts.frames or default_frames
  s.frame = 1
  s.interval = math.max(16, tonumber(opts.interval) or 80)
  s.color = opts.color or "accent"
  s.label = tostring(opts.label or "")
  s.pref_h = opts.pref_h or 1
  return s
end

return M
