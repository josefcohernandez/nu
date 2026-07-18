-- toolkit.app — la raíz del árbol de widgets: vincula el árbol a una región del
-- compositor, gestiona el FOCUS, enruta el input y orquesta el repintado por
-- nodos sucios.
--
-- LA RAÍZ. Una `App` es el puente entre el árbol retenido (widgets) y el core:
--   * posee una `Region` (api.md §9.1) —su lienzo— y un `root` (un contenedor que
--     ocupa la región entera);
--   * recompone los nodos SUCIOS y los blittea en su área (dirty tracking: los
--     limpios reusan su Block cacheado, ver widget.lua);
--   * mantiene el FOCO (un widget enfocado) y enruta el input (api.md §9.3, S31):
--     apila UN `on_input` que entrega la tecla al widget enfocado; lo que el
--     widget no consume, la app lo deja pasar (devuelve false), respetando la pila
--     del core (un keymap de capa superior puede recogerlo);
--   * resuelve los colores con su `theme` (G22): los widgets guardan nombres
--     semánticos y la app les da el theme al componer.
--
-- SIN COLISIÓN ENTRE PLUGINS (el criterio de hecho de S42). Cada app es
-- INDEPENDIENTE: su propia región (z-order propiedad de quien la crea, api.md
-- §9.1), su propio árbol, su propio foco y su propio `on_input` en la pila. Dos
-- plugins que montan cada uno su app no se pisan: componen en regiones distintas y
-- el input fluye por la pila (quien tiene el handler más reciente y consume,
-- gana; quien no consume, deja pasar al de abajo —que puede ser otra app—). No
-- hay estado global compartido entre apps; toda la retención vive en la instancia.

local widget = require("toolkit.widget")
local layout = require("toolkit.layout")
local theme_mod = require("toolkit.theme")

local M = {}

local App = {}
App.__index = App

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- toolkit.app(opts) -> App. Monta una app sobre una región.
--   * `region`: una `Region` ya creada (api.md §9.1) sobre la que pintar. O bien
--     `x/y/w/h/z` para que la app cree la suya con `enu.ui.region`. Necesita
--     `enu.ui` (headless, G20, no hay UI: el consumidor comprueba `enu.has("ui")`
--     antes —chat.md §8—; aquí, si no hay `enu.ui`, es EINVAL accionable).
--   * `root`: el contenedor raíz (default: un `vbox` que ocupa la región). Se le
--     da el área de la región.
--   * `theme`: el Theme (default `toolkit.theme.default`).
--   * `manage_input` (default true): si la app apila su `on_input` para enrutar el
--     foco. Un test puede desactivarlo y enrutar a mano con `app:handle_key`.
function M.app(opts)
  opts = opts or {}
  if enu.has == nil or not enu.has("ui") then
    einval("toolkit.app: enu.ui no está disponible (headless, G20). "
      .. "Comprueba enu.has(\"ui\") antes de montar una app (chat.md §8).")
  end

  local region = opts.region
  if region == nil then
    if opts.w == nil or opts.h == nil then
      -- Sin región dada ni tamaño: ocupa la pantalla entera.
      local s = enu.ui.size()
      opts.w = opts.w or s.w
      opts.h = opts.h or s.h
      opts.x = opts.x or 0
      opts.y = opts.y or 0
    end
    region = enu.ui.region({ x = opts.x or 0, y = opts.y or 0, w = opts.w, h = opts.h, z = opts.z })
    -- guardamos el tamaño con que la creamos (el compositor recorta, pero el
    -- lienzo lógico es este).
  end

  local self = setmetatable({
    region    = region,
    own_region = (opts.region == nil), -- la creamos nosotros → la destruimos al cerrar
    theme     = opts.theme or theme_mod.default,
    focused   = nil,
    _input    = nil,    -- el InputHandle apilado (api.md §9.3)
    _paint_pending = false,
    _alive    = true,
    -- el tamaño del lienzo de la app: si nos dieron región, usamos su w/h si los
    -- conocemos; si no, lo que pidió el usuario. La región es opaca a Lua, así que
    -- guardamos el tamaño con el que montamos.
    w = opts.w,
    h = opts.h,
    z = opts.z or 0,    -- z de la región (las viewport hijas van por encima)
    _viewports = nil,   -- regiones-viewport de widgets desplazables (se crean al vuelo)
  }, App)

  -- raíz: un contenedor que ocupa la región. Por defecto un vbox (la columna del
  -- chat). Se le inyecta la referencia a la app para que el dirty/focus suban.
  self.root = opts.root or layout.vbox({ id = "root" })
  self.root:set_app(self)

  -- enrutado de input (api.md §9.3, S31): UN on_input que entrega al foco.
  if opts.manage_input ~= false then
    self._input = enu.ui.on_input(function(ev)
      return self:handle_key(ev)
    end)
  end

  -- primer layout + pintura.
  self:relayout()
  self:_request_paint()
  return self
end

-- App:relayout() recalcula el layout del árbol sobre el área de la región. La
-- región es opaca (no expone su w/h a Lua), así que la app usa el tamaño con el
-- que se montó (`self.w/h`), o consulta `enu.ui.size()` si ocupa la pantalla. Tras
-- el layout, repinta.
function App:relayout(w, h)
  w = w or self.w
  h = h or self.h
  if w == nil or h == nil then
    local s = enu.ui.size()
    w = w or s.w
    h = h or s.h
  end
  self.w, self.h = w, h
  -- FORZAMOS el reparto (último arg): un `App:relayout()` es una petición explícita
  -- de "el árbol cambió, recolócalo" y no debe fiarse del flag `dirty` del
  -- contenedor —el paint síncrono pudo borrarlo, y un cambio de `pref_h` ni ensucia
  -- al padre—. Sin esto, un modal/picker recién añadido se quedaba en 0×0 (invisible
  -- y atrapando el foco: chat colgado) y el input multilínea no crecía. Ver el
  -- comentario de `relayout` en layout.lua.
  self.root:relayout(0, 0, w, h, true)
  -- si no hay foco aún y hay focusables, enfoca el primero (un layout con un input
  -- arranca con el cursor donde corresponde).
  if self.focused == nil then
    local f = self.root:focusables()
    if f[1] then
      self:set_focus(f[1])
    end
  end
  self:_request_paint()
end

-- App:resize(w, h) atiende un `ui:resize` (api.md §9.1: "tu región, tu
-- ui:resize"). Redimensiona la región y rehace el layout. El consumidor (chat)
-- suscribe `ui:resize` y llama aquí.
function App:resize(w, h)
  if self.own_region then
    self.region:resize(w, h)
  end
  self:relayout(w, h)
end

-- App:_request_paint() marca que hay repintado pendiente. Lo llaman los widgets al
-- ensuciarse (`_notify`) y la app tras un cambio estructural. El pintado real lo
-- hace `paint()`; en una app viva normalmente se dispara desde el bucle/eventos,
-- pero para mantener la simplicidad (y que los tests vean el resultado al
-- instante) pintamos de forma SÍNCRONA aquí. El compositor del core ya coalesce
-- los blits y pinta como mucho cada ~30 ms (api.md §9), así que blittear de más es
-- barato (es copia, no re-render): la ganancia del dirty tracking es no
-- RECOMPONER los Blocks, que es lo caro, no evitar el blit.
function App:_request_paint()
  if not self._alive then
    return
  end
  self._paint_pending = true
  self:paint()
end

-- App:paint() recompone los nodos sucios y los blittea. Recorre el árbol; por
-- cada nodo VISIBLE con área y Block, blittea su Block en su (x,y) absoluta. Un
-- nodo limpio devuelve su Block cacheado (no recompone): ahí está el ahorro del
-- dirty tracking (no RECOMPONER, que es lo caro; el blit es copia barata, api.md
-- §9.1). Antes de pintar limpia la región (un layout con huecos no deja restos).
--
-- VIEWPORT / RECORTE A LA BANDA (api.md §9.1: «las regiones son la unidad de
-- composición … viewport; scroll = re-blit con otro offset»). El recorte del core
-- es por REGIÓN, no por banda de widget: la región de la app abarca el árbol
-- entero, así que blittear ahí el Block de un widget lo recorta a la REGIÓN, no a
-- su banda. Eso sangra en DOS casos, ambos del `text` (cuyo `compose` devuelve el
-- Block COMPLETO, posiblemente más alto que su banda `h`, ver widgets.lua):
--   * SCROLL (offset negativo): un `text` con `scroll>0` empezaría por una fila
--     posterior, derramando sobre el widget de ARRIBA;
--   * DESBORDE (Block más alto que la banda): un `text` cuyo contenido excede su
--     banda escribiría filas de más sobre el widget de ABAJO.
-- El modelo correcto del core es una región por viewport: por eso un widget que
-- desborda su banda o está desplazado obtiene su PROPIA región hija (creada al
-- vuelo, propiedad de la app), recortada a su banda; ahí el offset recorta limpio
-- por AMBOS extremos (G28) y nada sale de la banda. Los widgets que CABEN en su
-- banda y no están desplazados se blittean directos en la región de la app (la vía
-- rápida: ni región hija ni z extra para un label/input, que nunca desbordan).
function App:paint()
  if not self._alive then
    return
  end
  self._paint_pending = false
  self.region:clear()
  self.root:walk(function(node)
    if not node.visible or node.w <= 0 or node.h <= 0 then
      return
    end
    local blk = node:render() -- recompone solo si sucio (dirty tracking)
    if blk == nil then
      return
    end
    local ax, ay = self:_abs(node)
    local oy = node.scroll or 0
    -- ¿el nodo SANGRARÍA fuera de su banda al blittear directo? Sangra si está
    -- desplazado (`oy~=0`) o si su Block es MÁS ALTO que su banda (`height>h`): en
    -- ambos el recorte por-región de la app no basta. Si no, cabe: vía directa.
    local overflows = (blk.height or 0) > node.h
    if oy ~= 0 or overflows then
      -- su propia región-viewport, recortada a su banda (api.md §9.1): el offset de
      -- scroll recorta por arriba y la altura de la banda por abajo, sin sangrar.
      local vp = self:_viewport_for(node, ax, ay)
      vp:clear()
      vp:blit(0, -oy, blk) -- offset negativo = scroll dentro de SU región (G28)
    else
      -- cabe en su banda: vía directa. Si este nodo tenía una región-viewport de un
      -- frame anterior (cuando desbordaba o estaba desplazado), la OCULTAMOS: su
      -- contenido viejo, situado por encima (z+1), no debe seguir tapando lo que
      -- ahora pinta la región de la app.
      if node._viewport then
        node._viewport:hide()
      end
      self.region:blit(ax, ay, blk)
    end
  end)
  -- coloca el cursor real en el input enfocado (api.md §9.1: Region:cursor).
  if self.focused and self.focused.caret_col then
    local ax, ay = self:_abs(self.focused)
    self.region:cursor(ax + self.focused:caret_col(), ay)
  end
end

-- App:_viewport_for(node, ax, ay) -> Region. Devuelve (creando o recolocando) la
-- región-viewport dedicada de un widget desplazable, situada en su banda
-- (ax,ay,w,h) con z por encima de la región de la app (para verse sobre ella). Se
-- guarda en `node._viewport` y se reusa entre pintados (no se recrea cada frame).
-- La app la registra para destruirla al cerrar (no fuga).
function App:_viewport_for(node, ax, ay)
  local vp = node._viewport
  if vp == nil then
    vp = enu.ui.region({ x = ax, y = ay, w = node.w, h = node.h, z = (self.z or 0) + 1 })
    node._viewport = vp
    self._viewports = self._viewports or {}
    self._viewports[#self._viewports + 1] = vp
  else
    vp:move(ax, ay)
    vp:resize(node.w, node.h)
    vp:show() -- pudo quedar oculta en un frame en que el nodo cupo en su banda.
  end
  return vp
end

-- App:_abs(node) -> (x, y) absolutos del nodo dentro de la región: la suma de las
-- coordenadas locales subiendo por los padres. (vbox/hbox dan coordenadas locales
-- al contenedor; un contenedor anidado las desplaza por su propia posición.)
function App:_abs(node)
  local x, y = 0, 0
  local n = node
  while n ~= nil do
    x = x + (n.x or 0)
    y = y + (n.y or 0)
    n = n.parent
  end
  return x, y
end

-- App:set_focus(w) mueve el foco a `w` (debe ser focusable y del árbol). Ensucia
-- el antiguo y el nuevo enfocado (su render cambia: el realce de foco), repinta y
-- emite `toolkit:focus` (evento del plugin `toolkit`) para que el resto reaccione.
function App:set_focus(w)
  if w == self.focused then
    return
  end
  if w ~= nil and not w.focusable then
    einval("toolkit.app:set_focus: el widget no es focusable")
  end
  local prev = self.focused
  self.focused = w
  if prev then prev:mark_dirty() end
  if w then w:mark_dirty() end
  -- `toolkit:focus` notifica que el foco de WIDGET cambió. El namespace es el del
  -- plugin (`toolkit`), no `ui:` —reservado al core (api.md §4), que ya emite
  -- `ui:focus` con OTRA semántica (el foco del TERMINAL, payload `{focused}`,
  -- ui_events.go)—: pisarlo rompería a sus suscriptores. El enrutado del foco de
  -- widget es vocabulario del toolkit (api.md §9.3), así que su evento vive en el
  -- namespace del toolkit. Payload `{app, widget}`: quién y a qué widget enfocó.
  if enu.events then
    enu.events.emit("toolkit:focus", { app = self, widget = w })
  end
  self:_request_paint()
end

-- App:focus_next() / App:focus_prev() mueven el foco al siguiente/anterior widget
-- focusable en orden de tabulación (preorden del árbol; envuelve por los
-- extremos). Es lo que enruta el input a "el otro widget" (criterio de hecho:
-- mover el foco entre dos widgets). Sin focusables, no hace nada.
function App:focus_next()
  self:_cycle(1)
end

function App:focus_prev()
  self:_cycle(-1)
end

function App:_cycle(dir)
  local f = self.root:focusables()
  if #f == 0 then
    return
  end
  local idx = 1
  for i, wdg in ipairs(f) do
    if wdg == self.focused then
      idx = i
      break
    end
  end
  local nxt = ((idx - 1 + dir) % #f) + 1
  self:set_focus(f[nxt])
end

-- App:handle_key(ev) -> boolean. El enrutado de input (api.md §9.3): entrega el
-- evento al widget ENFOCADO; si lo consume (true), la app consume; si no, la app
-- lo deja pasar (false) para que la pila del core lo ofrezca al handler de abajo
-- (otra app, un keymap). Es el handler que la app apila con `on_input`. Una tecla
-- de navegación de foco por defecto: `tab`/`shift+tab` cambian de widget (lo
-- consume la app); el resto va al foco. El chat remapea con `enu.ui.keymap`.
function App:handle_key(ev)
  if not self._alive then
    return false
  end
  -- navegación de foco por defecto (tab / shift+tab). El chat puede sobreescribir
  -- con keymaps; aquí es el comportamiento base de "un layout con focus".
  if ev.type == "key" and ev.key == "tab" then
    if ev.mods and ev.mods.shift then
      self:focus_prev()
    else
      self:focus_next()
    end
    return true
  end
  if self.focused then
    return self.focused:on_key(ev) == true
  end
  return false
end

-- App:close() desmonta la app: quita su `on_input` de la pila (api.md §9.3) y, si
-- la región es suya, la destruye (api.md §9.1). Idempotente. Tras cerrar, no
-- pinta ni enruta. (Una app es además un `ownedHandle` indirecto: su región y su
-- on_input ya se sueltan en un `reload` del plugin, G2; `close` es el cierre
-- explícito.)
function App:close()
  if not self._alive then
    return
  end
  self._alive = false
  if self._input then
    self._input:pop()
    self._input = nil
  end
  -- destruye las regiones-viewport de los widgets desplazables (no fuga).
  if self._viewports then
    for _, vp in ipairs(self._viewports) do
      vp:destroy()
    end
    self._viewports = nil
  end
  if self.own_region and self.region then
    self.region:destroy()
  end
end

return M
