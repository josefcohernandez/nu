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

-- parse_pad(p) -> (top, right, bottom, left). Normaliza la opción `pad` de un
-- contenedor: un número (uniforme en los cuatro lados), una tabla `{v, h}` (vertical
-- / horizontal) o `{t, r, b, l}` (CSS-like), o nil (cero). Es azúcar para no calcular
-- insets a mano en cada UI.
local function parse_pad(p)
  if p == nil then
    return 0, 0, 0, 0
  end
  if type(p) == "number" then
    return p, p, p, p
  end
  if type(p) == "table" then
    if p.t or p.r or p.b or p.l then
      return p.t or 0, p.r or 0, p.b or 0, p.l or 0
    end
    -- {v, h} o {top/bottom, left/right} posicional.
    local v = p[1] or 0
    local h = p[2] or v
    return v, h, v, h
  end
  return 0, 0, 0, 0
end
M._parse_pad = parse_pad

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

  -- container:relayout(x, y, w, h, force) fija el área del contenedor y reparte
  -- entre los hijos VISIBLES. Solo recalcula si está sucio, si el área cambió o si
  -- el llamante FUERZA (dirty tracking del layout); si no, conserva la geometría de
  -- los hijos. Tras repartir, propaga `relayout` a los hijos que sean contenedores.
  --
  -- POR QUÉ `force`. El flag `self.dirty` sirve a DOS amos: "recomponer el Block"
  -- (lo consume `render` al pintar) y "rehacer el reparto" (lo consume aquí). Como
  -- `mark_dirty` pinta de forma SÍNCRONA (app.lua: `_request_paint`), un cambio
  -- ESTRUCTURAL —`add`/`remove`/`set_visible`— dispara un paint que limpia `dirty`
  -- ANTES de que el `relayout` posterior lo lea, y el hijo nuevo se quedaba sin
  -- geometría (0×0: modal/picker invisible, input multilínea que no crecía). Y un
  -- cambio de `pref_h` ni siquiera ensucia al PADRE que debe repartir. Por eso un
  -- `App:relayout()` —siempre EXPLÍCITO: "cambié el árbol, recolócalo"— fuerza el
  -- reparto completo en vez de fiarse de un `dirty` que el paint ya pudo borrar. El
  -- coste es solo aritmético (colocar hijos), no recompone Blocks (eso lo decide
  -- `set_geometry` por cambio de tamaño), así que el ahorro caro del dirty tracking
  -- —no RECOMPONER— se conserva intacto.
  function mt:relayout(x, y, w, h, force)
    local area_changed = (x ~= self.x) or (y ~= self.y) or (w ~= self.w) or (h ~= self.h)
    self:set_geometry(x, y, w, h)
    if force or self.dirty or area_changed or self._layout_done ~= true then
      self:_distribute(w, h)
      self._layout_done = true
    end
    -- Desciende: un hijo contenedor reparte su propia área (ya asignada arriba). El
    -- `force` se propaga: un reparto forzado recoloca el subárbol entero.
    for _, c in ipairs(self.children) do
      if c.visible and c.relayout then
        c:relayout(c.x, c.y, c.w, c.h, force)
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

    -- vbox/hbox: reparto en un eje (alto en vbox, ancho en hbox), con padding
    -- interior, gap entre hijos, justificado en el eje principal y alineación en el
    -- eje cruzado. Pasadas:
    --   1. suma el tamaño FIJO de los hijos no-flex, el `flex` total y los gaps.
    --   2. reparte el sobrante entre los flexibles (el último se queda el remanente,
    --      evitando perder celdas por redondeo); si NO hay flexibles, `justify`
    --      coloca el bloque de hijos dentro del hueco (start/center/end/between).
    --   3. en el eje cruzado, `align` coloca cada hijo (stretch por defecto: llena;
    --      start/center/end lo respetan si el hijo declara un tamaño cruzado).
    function mt:_distribute(w, h)
      local pt, pr, pb, pl = parse_pad(self.pad)
      local gap = math.max(0, tonumber(self.gap) or 0)
      local align = self.align or "stretch"     -- eje cruzado
      local justify = self.justify or "start"    -- eje principal

      -- Áreas interiores (descontando el padding) y orígenes de cada eje.
      local main = math.max(0, (horizontal and (w - pl - pr) or (h - pt - pb)))
      local cross = math.max(0, (horizontal and (h - pt - pb) or (w - pl - pr)))
      local main_origin = horizontal and pl or pt
      local cross_origin = horizontal and pt or pl

      local vis = {}
      for _, c in ipairs(self.children) do
        if c.visible then vis[#vis + 1] = c end
      end
      local n = #vis
      local gaps_total = (n > 1) and gap * (n - 1) or 0

      -- tamaño fijo (eje principal) de un hijo no-flex.
      local function fixed_main(c)
        local size = horizontal and (tonumber(c.w_fixed) or tonumber(c.pref_w) or 0)
          or (tonumber(c.h_fixed) or tonumber(c.pref_h) or 0)
        return math.max(0, size)
      end
      -- tamaño cruzado PREFERIDO de un hijo (para align != stretch); nil = llenar.
      local function pref_cross(c)
        local size = horizontal and (tonumber(c.h_fixed) or tonumber(c.pref_h))
          or (tonumber(c.w_fixed) or tonumber(c.pref_w))
        return size and math.max(0, size) or nil
      end

      local fixed_total, flex_total, flex_count = 0, 0, 0
      for _, c in ipairs(vis) do
        local f = tonumber(c.flex) or 0
        if f > 0 then
          flex_total = flex_total + f
          flex_count = flex_count + 1
        else
          fixed_total = fixed_total + fixed_main(c)
        end
      end

      local slack = math.max(0, main - fixed_total - gaps_total)
      -- Sin flexibles: el bloque de hijos (fijos + gaps) se justifica en el hueco.
      local pos = main_origin
      local extra_gap = 0
      if flex_count == 0 then
        local used = fixed_total + gaps_total
        local free = math.max(0, main - used)
        if justify == "center" then
          pos = main_origin + math.floor(free / 2)
        elseif justify == "end" then
          pos = main_origin + free
        elseif justify == "between" and n > 1 then
          extra_gap = math.floor(free / (n - 1))
        end
      end

      local flex_done, slack_used = 0, 0
      for _, c in ipairs(vis) do
        local f = tonumber(c.flex) or 0
        local size
        if f > 0 then
          flex_done = flex_done + 1
          if flex_done == flex_count then
            size = math.max(0, slack - slack_used)
          else
            size = math.floor(slack * f / flex_total)
            slack_used = slack_used + size
          end
        else
          size = fixed_main(c)
        end

        -- Eje cruzado: stretch (llena) o un tamaño preferido alineado.
        local csize, cpos = cross, cross_origin
        if align ~= "stretch" then
          local pc = pref_cross(c)
          if pc then
            csize = math.min(pc, cross)
            if align == "center" then
              cpos = cross_origin + math.floor((cross - csize) / 2)
            elseif align == "end" then
              cpos = cross_origin + (cross - csize)
            end
          end
        end

        if horizontal then
          c:set_geometry(pos, cpos, size, csize)
        else
          c:set_geometry(cpos, pos, csize, size)
        end
        pos = pos + size + gap + extra_gap
      end
    end
  end

  -- constructor del contenedor. `opts` se pasa al widget base (id, focusable —un
  -- contenedor normalmente no es focusable—). Empieza sucio (hay que repartir).
  return function(opts)
    opts = opts or {}
    local c = setmetatable(widget.new(opts), mt)
    c.kind = kind
    -- Propiedades de layout (las lee `_distribute`): padding interior, gap entre
    -- hijos, alineación en el eje cruzado y justificado en el principal. Opcionales;
    -- los defaults reproducen el comportamiento anterior (sin pad/gap, stretch/start).
    c.pad = opts.pad
    c.gap = opts.gap
    c.align = opts.align
    c.justify = opts.justify
    return c
  end
end

M.vbox  = make_box("vbox")
M.hbox  = make_box("hbox")
M.stack = make_box("stack")

return M
