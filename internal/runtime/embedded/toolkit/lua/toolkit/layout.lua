-- toolkit.layout — los contenedores (slots) del toolkit.
--
-- EL MODELO DE LAYOUT (arquitectura.md §kernel/nota ui: el toolkit «aporta
-- slots»). Un contenedor es un widget que NO pinta él mismo: COLOCA a sus hijos,
-- asignándole a cada uno su área (`set_geometry`) dentro de la suya. Cada hijo
-- ocupa su rectángulo y compone su propio Block ahí. Tres contenedores, el
-- conjunto mínimo coherente que el harness necesita (chat.md §1: una columna
-- transcript/input/statusline + capas modales):
--
--   * **vbox**: apila los hijos en VERTICAL (uno debajo de otro). Es la columna
--     del chat. Cada hijo tiene un alto: fijo (`opts.h` del hijo) o FLEXIBLE
--     (reparte el alto sobrante entre los hijos con `flex>0`, proporcional a su
--     `flex`). El ancho de cada hijo es el del contenedor.
--   * **hbox**: lo mismo en HORIZONTAL (los hijos lado a lado; el flex reparte el
--     ancho sobrante). Para barras/segmentos.
--   * **stack**: superpone a TODOS los hijos en la MISMA área (todos ocupan el
--     contenedor entero), en orden de inserción (el último, "encima"). Es la base
--     de las capas modales (un diálogo de permisos sobre el transcript): la app
--     enruta el foco/input a la capa de encima.
--
-- El layout se RECALCULA solo cuando el contenedor está sucio (un hijo nuevo, un
-- resize, un cambio de visibilidad) — es parte del dirty tracking: colocar a los
-- hijos cuesta y no se rehace cada frame. La app llama `relayout(x,y,w,h)` con el
-- área del contenedor; este reparte y, recursivamente, sus hijos contenedores
-- reparten lo suyo.
--
-- LA PROPIEDAD DE LAYOUT EN EL HIJO. Un hijo declara cómo quiere ocupar su eje
-- principal con dos campos opcionales (los lee el contenedor, no el core):
--   * `flex` (número >= 0, default 0): si >0, el hijo es flexible y recibe una
--     parte del espacio sobrante proporcional a su `flex`.
--   * en vbox, `h` (alto fijo) si `flex==0`; en hbox, `w` (ancho fijo) si
--     `flex==0`. Un hijo sin flex ni tamaño fijo ocupa 0 en el eje principal
--     (decisión explícita: un widget que no dice cuánto ocupa no acapara espacio).

local widget = require("toolkit.widget")

local M = {}

-- new_container(kind) -> (constructor). Fabrica el constructor de un contenedor de
-- tipo `kind` ("vbox"/"hbox"/"stack"), todos derivados del Widget base y con su
-- propio `relayout`. Comparten el grueso (no pintan, recolocan hijos); solo
-- difieren en cómo reparten el área.
local function make_box(kind)
  local mt = widget.derive()

  -- Un contenedor no compone Block propio: pinta a través de sus hijos. La app
  -- recorre el árbol y blittea el Block de cada hoja en su área; el contenedor
  -- solo aporta geometría. Por eso `compose` devuelve nil (heredado del base,
  -- explícito aquí para que se lea la intención).
  function mt:compose(_w, _h)
    return nil
  end

  -- container:relayout(x, y, w, h) fija el área del contenedor y reparte entre los
  -- hijos VISIBLES. Solo recalcula si está sucio o si el área cambió (dirty
  -- tracking del layout); si no, conserva la geometría de los hijos. Tras
  -- repartir, propaga `relayout` a los hijos que sean contenedores (un subárbol).
  function mt:relayout(x, y, w, h)
    local area_changed = (x ~= self.x) or (y ~= self.y) or (w ~= self.w) or (h ~= self.h)
    self:set_geometry(x, y, w, h)
    -- Si nada cambió (ni el área ni la estructura), no rehagas el reparto: solo
    -- desciende para que los hijos contenedores se recoloquen si ELLOS están
    -- sucios. Pero si el contenedor está sucio (hijo nuevo/visibilidad), o el
    -- área cambió, recalcula.
    if self.dirty or area_changed or self._layout_done ~= true then
      self:_distribute(w, h)
      self._layout_done = true
    end
    -- Desciende: un hijo contenedor reparte su propia área (ya asignada arriba).
    for _, c in ipairs(self.children) do
      if c.visible and c.relayout then
        c:relayout(c.x, c.y, c.w, c.h)
      end
    end
  end

  if kind == "stack" then
    -- stack: todos los hijos visibles ocupan el área entera del contenedor,
    -- superpuestos. El orden de inserción es el z lógico (el último, encima); la
    -- app lo respeta al pintar (blittea en orden) y al enrutar el foco (la capa de
    -- encima manda).
    function mt:_distribute(w, h)
      for _, c in ipairs(self.children) do
        if c.visible then
          c:set_geometry(0, 0, w, h)
        else
          c:set_geometry(0, 0, 0, 0)
        end
      end
    end
  else
    local horizontal = (kind == "hbox")

    -- vbox/hbox: reparto en un eje (alto en vbox, ancho en hbox). Dos pasadas:
    --   1. suma el tamaño FIJO de los hijos no-flex y el `flex` total.
    --   2. reparte el sobrante entre los flexibles, proporcional a su flex; el
    --      último flexible se queda el remanente entero (evita perder celdas por
    --      el redondeo de la división). Cada hijo se coloca a continuación del
    --      anterior; en el eje cruzado ocupa el tamaño completo del contenedor.
    function mt:_distribute(w, h)
      local main = horizontal and w or h
      local fixed_total, flex_total = 0, 0
      local vis = {}
      for _, c in ipairs(self.children) do
        if c.visible then
          vis[#vis + 1] = c
          local f = tonumber(c.flex) or 0
          if f > 0 then
            flex_total = flex_total + f
          else
            local size = horizontal and (tonumber(c.w_fixed) or tonumber(c.pref_w) or 0)
              or (tonumber(c.h_fixed) or tonumber(c.pref_h) or 0)
            fixed_total = fixed_total + math.max(0, size)
          end
        end
      end
      -- `slack` es el espacio sobrante a repartir SOLO entre los flexibles (lo que
      -- queda tras reservar el tamaño fijo de los no-flex). El ÚLTIMO flexible se
      -- queda el slack que reste tras dar a los anteriores su parte proporcional
      -- (así no se pierden celdas por el redondeo de la división, SIN robarle el
      -- hueco a los fijos que vengan después de él).
      local slack = math.max(0, main - fixed_total)
      -- ¿cuántos flexibles hay y cuál es el último? (para asignarle el remanente).
      local flex_count = 0
      for _, c in ipairs(vis) do
        if (tonumber(c.flex) or 0) > 0 then
          flex_count = flex_count + 1
        end
      end

      local pos = 0
      local flex_done = 0      -- flexibles ya colocados
      local slack_used = 0     -- slack ya repartido
      for _, c in ipairs(vis) do
        local f = tonumber(c.flex) or 0
        local size
        if f > 0 then
          flex_done = flex_done + 1
          if flex_done == flex_count then
            -- último flexible: el slack que reste (evita perder celdas por redondeo).
            size = math.max(0, slack - slack_used)
          else
            size = math.floor(slack * f / flex_total)
            slack_used = slack_used + size
          end
        else
          size = horizontal and math.max(0, tonumber(c.w_fixed) or tonumber(c.pref_w) or 0)
            or math.max(0, tonumber(c.h_fixed) or tonumber(c.pref_h) or 0)
        end
        -- Recorta para no salirse del contenedor (si los fijos sumaban de más).
        if pos + size > main then
          size = math.max(0, main - pos)
        end
        if horizontal then
          c:set_geometry(pos, 0, size, h)
        else
          c:set_geometry(0, pos, w, size)
        end
        pos = pos + size
      end
    end
  end

  -- constructor del contenedor. `opts` se pasa al widget base (id, focusable —un
  -- contenedor normalmente no es focusable—). Empieza sucio (hay que repartir).
  return function(opts)
    local c = setmetatable(widget.new(opts or {}), mt)
    c.kind = kind
    return c
  end
end

M.vbox  = make_box("vbox")
M.hbox  = make_box("hbox")
M.stack = make_box("stack")

return M
