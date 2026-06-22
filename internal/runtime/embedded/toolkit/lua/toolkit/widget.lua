-- toolkit.widget — el nodo del árbol de widgets y el dirty tracking.
--
-- EL MODELO (arquitectura.md §kernel/nota ui: «el toolkit … retenida por dentro:
-- árbol + nodos sucios»). La UI es un **árbol retenido** de nodos (widgets), no
-- un repintado inmediato cada frame. Cada nodo:
--   * conoce a su `parent` y a sus `children` (un contenedor coloca a los suyos);
--   * tiene un ÁREA (`x,y,w,h`) en coordenadas locales de la región de la app,
--     que el layout del padre le asigna (un widget hoja no decide dónde va);
--   * sabe `compose(w, h) -> Block`: producir el `Block` (api.md §9.2) que pinta
--     su área, vía `nu.text`/`nu.ui.block`. Es lo único específico de cada tipo
--     de widget; el resto (árbol, dirty, focus) es común y vive aquí.
--
-- DIRTY TRACKING (el porqué: eficiencia, no recomponer todo el árbol cada frame).
-- Componer un Block cuesta (medir texto, render de markdown). Si un solo widget
-- cambia (un delta de texto que llega), recomponer y repintar el árbol entero
-- sería el coste cuadrático que ADR-007 quiere evitar. Por eso cada nodo guarda:
--   * `dirty`: su contenido cambió y hay que RECOMPONER su Block;
--   * `_block`: el último Block compuesto, CACHEADO (se reusa si no está sucio);
--   * `_block_w`/`_block_h`: el tamaño con el que se compuso (si el área cambió,
--     el caché no sirve aunque el contenido no haya cambiado).
-- `mark_dirty()` ensucia el nodo y avisa hacia ARRIBA (`_notify`) para que la app
-- sepa que hay trabajo pendiente, SIN ensuciar a los hermanos: el repintado solo
-- toca los nodos sucios (la app re-blittea sus Blocks; los limpios reusan caché).
-- Un cambio de geometría (layout) sí invalida el caché del nodo movido/redimen-
-- sionado, porque su Block se compone a un tamaño nuevo.

local M = {}

local Widget = {}
Widget.__index = Widget
M.Widget = Widget

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- M.new(opts) -> Widget. Construye un nodo base. Los tipos concretos
-- (label/text/input/contenedores) parten de aquí (`M.derive`) y solo sobrescriben
-- `compose` (y, los focusables, `on_key`). `opts`:
--   * `focusable` (bool, default false): ¿puede recibir el foco/input? Las hojas
--     interactivas (input) lo ponen a true; las decorativas (label) a false.
--   * `id` (string, opcional): etiqueta para depurar/buscar.
function M.new(opts)
  opts = opts or {}
  local w = setmetatable({
    parent    = nil,
    children  = {},
    x = 0, y = 0, w = 0, h = 0, -- área local (la asigna el layout del padre)
    dirty     = true,           -- nace sucio: nunca se ha compuesto
    visible   = true,
    focusable = opts.focusable == true,
    id        = opts.id,
    _block    = nil,            -- Block cacheado (nil = hay que componer)
    _block_w  = -1,
    _block_h  = -1,
    _app      = nil,            -- la app raíz (la fija `App:set_root`/`add`)
  }, Widget)
  return w
end

-- M.derive() -> metatable. Crea una metatabla que HEREDA de Widget para un tipo
-- de widget concreto: el tipo define `compose` (y opcionalmente `on_key`) en la
-- metatabla devuelta, y su constructor hace `setmetatable(M.new{...}, mt)`. Así
-- todos los widgets comparten la maquinaria de árbol/dirty/focus sin duplicarla.
function M.derive()
  local mt = setmetatable({}, { __index = Widget })
  mt.__index = mt
  return mt
end

-- Widget:set_app(app) propaga la referencia a la app raíz por el subárbol. La app
-- la necesita para enrutar el input al widget enfocado y para enterarse del dirty
-- (un nodo avisa a su app al ensuciarse). Se llama al insertar un subárbol.
function Widget:set_app(app)
  self._app = app
  for _, c in ipairs(self.children) do
    c:set_app(app)
  end
end

-- Widget:add(child) inserta `child` como hijo (al final). Lo desvincula de un
-- padre anterior si lo tenía (un widget vive en un solo sitio del árbol — así no
-- hay aliasing entre dos posiciones). Hereda la app del padre y marca al padre
-- sucio (su layout cambió: hay un hijo nuevo que colocar). Devuelve `child` para
-- encadenar. Es la operación que CONSTRUYE el árbol.
function Widget:add(child)
  if type(child) ~= "table" or getmetatable(child) == nil then
    einval("Widget:add: el hijo debe ser un widget")
  end
  if child.parent ~= nil then
    child.parent:remove(child)
  end
  child.parent = self
  child:set_app(self._app)
  self.children[#self.children + 1] = child
  self:mark_dirty() -- el layout del padre cambió
  return child
end

-- Widget:remove(child) desvincula `child` del árbol. Marca al padre sucio (su
-- layout cambió). No destruye `child` (puede reinsertarse en otro sitio); sí
-- corta su `parent`/`_app`.
function Widget:remove(child)
  for i, c in ipairs(self.children) do
    if c == child then
      table.remove(self.children, i)
      child.parent = nil
      child:set_app(nil)
      self:mark_dirty()
      return
    end
  end
end

-- Widget:mark_dirty() ensucia ESTE nodo (su contenido/area cambió → su Block hay
-- que recomponerlo) e invalida su caché. Avisa hacia arriba (`_notify`) para que
-- la app programe un repintado, SIN ensuciar a los hermanos (esa es la clave del
-- dirty tracking: el repintado solo recompone los nodos sucios). Es idempotente.
function Widget:mark_dirty()
  self.dirty = true
  self._block = nil -- el caché ya no vale
  self:_notify()
end

-- Widget:_notify() propaga "hay trabajo sucio" hasta la app raíz. NO marca a los
-- ancestros como sucios (ellos no han cambiado: su Block sigue siendo válido, lo
-- que cambió es un descendiente que la app re-blitteará); solo le dice a la app
-- "tienes nodos pendientes, repinta cuando puedas". Un nodo aún no insertado en
-- una app (sin `_app`) no avisa a nadie (se compondrá al insertarlo).
function Widget:_notify()
  if self._app then
    self._app:_request_paint()
  end
end

-- Widget:set_geometry(x, y, w, h) fija el área local del nodo (lo llama el layout
-- del padre). Si el TAMAÑO cambia, invalida el caché del Block (se compuso a otro
-- tamaño y ya no sirve) y ensucia el nodo. Mover sin redimensionar (solo x/y) NO
-- recompone el Block —el contenido es el mismo, la app lo re-blittea en la nueva
-- posición—: solo cambia dónde se pinta, no qué. Esa distinción es parte del
-- ahorro del dirty tracking.
function Widget:set_geometry(x, y, w, h)
  local resized = (w ~= self.w) or (h ~= self.h)
  self.x, self.y, self.w, self.h = x, y, w, h
  if resized then
    self._block = nil
    self.dirty = true
  end
end

-- Widget:render() -> Block. Devuelve el Block del nodo, RECOMPONIÉNDOLO solo si
-- está sucio o si el caché se compuso a otro tamaño. Si no, reusa `_block` (el
-- corazón del ahorro: un nodo limpio no vuelve a llamar a `nu.text`/`nu.ui`).
-- `compose(w, h)` es lo que cada tipo de widget implementa. Tras componer, el
-- nodo queda limpio. Un widget de tamaño 0 (no le tocó área) devuelve nil: no hay
-- nada que pintar.
function Widget:render()
  if self.w <= 0 or self.h <= 0 then
    self.dirty = false
    return nil
  end
  if (not self.dirty) and self._block ~= nil
    and self._block_w == self.w and self._block_h == self.h then
    return self._block -- caché válido
  end
  local blk = self:compose(self.w, self.h)
  self._block = blk
  self._block_w = self.w
  self._block_h = self.h
  self.dirty = false
  return blk
end

-- Widget:compose(w, h) -> Block. POR DEFECTO: un nodo base sin contenido propio no
-- pinta nada (los contenedores no pintan ellos mismos; pintan sus hijos). Los
-- tipos hoja (label/text/input) lo sobrescriben. Recibe el tamaño de su área.
function Widget:compose(_w, _h)
  return nil
end

-- Widget:on_key(ev) -> boolean. POR DEFECTO: un widget no consume teclas (las deja
-- pasar). Los focusables (input) lo sobrescriben para editar/navegar. La app solo
-- lo llama en el widget ENFOCADO (el enrutado de focus, app.lua).
function Widget:on_key(_ev)
  return false
end

-- Widget:walk(fn) recorre el subárbol en preorden (este nodo, luego cada hijo en
-- orden) llamando `fn(node)`. Lo usan la app (recoger focusables, repintar) y los
-- tests. Saltarse los invisibles es decisión de quien recorre (la app sí los
-- salta para focus/paint; un recorrido de depuración no).
function Widget:walk(fn)
  fn(self)
  for _, c in ipairs(self.children) do
    c:walk(fn)
  end
end

-- Widget:focusables(acc) -> Widget[]. Recoge, en orden de preorden (el orden
-- natural de tabulación), los nodos VISIBLES y focusables del subárbol. Es la
-- lista por la que la app mueve el foco con focus_next/focus_prev.
function Widget:focusables(acc)
  acc = acc or {}
  if not self.visible then
    return acc
  end
  if self.focusable then
    acc[#acc + 1] = self
  end
  for _, c in ipairs(self.children) do
    c:focusables(acc)
  end
  return acc
end

-- Widget:set_visible(v) muestra/oculta el subárbol. Un nodo oculto no se pinta ni
-- recibe foco. Cambia el layout del padre (un hueco aparece/desaparece), así que
-- ensucia al padre.
function Widget:set_visible(v)
  v = (v == true)
  if v == self.visible then
    return
  end
  self.visible = v
  if self.parent then
    self.parent:mark_dirty()
  else
    self:mark_dirty()
  end
end

return M
