-- toolkit.theme — el sistema de themes del toolkit (G22).
--
-- EL PROBLEMA (G22). El core (`nu.ui`, api.md §9.2) solo entiende colores
-- **literales**: un "#rrggbb" o un índice 0-255. Los nombres semánticos
-- ("accent", "error", "dim"…) **no son del core** —`normalizeColor` los rechaza
-- a propósito—: son vocabulario del theme del toolkit. Que un plugin escriba
-- `fg = "accent"` y NO un hex concreto es lo que hace que un cambio de theme
-- repinte toda la UI sin tocar el código de los widgets. Pero ese nombre tiene
-- que convertirse en un literal **antes** de construir el Block/Style, porque el
-- core no sabrá qué es "accent". Esa traducción nombre→literal es exactamente lo
-- que vive aquí (arquitectura.md §kernel/nota ui: «los nombres semánticos de
-- color se resuelven aquí, no en el core»).
--
-- EL CONTRATO. Un `Theme` es una tabla de nombres semánticos → literales. Sus
-- métodos resuelven:
--   * `theme:color(name) -> literal`  — un nombre semántico a su literal. Un
--     literal ya válido ("#rrggbb"/índice) pasa tal cual (un widget puede
--     mezclar nombres y literales). Un nombre desconocido es un error accionable
--     (un theme incompleto se nota, no se traga en silencio).
--   * `theme:style(spec) -> Style`    — una tabla de estilo cuyos `fg`/`bg` son
--     nombres semánticos (o literales) a un `Style` con `fg`/`bg` ya LITERALES,
--     listo para `nu.ui.block`/`Region:fill`. Los atributos (bold/italic/…) se
--     copian tal cual.
--
-- Así el resto del toolkit (widgets, layout) trabaja siempre con nombres
-- semánticos y delega la resolución al theme en el último momento, al componer
-- el Block. El core nunca ve un nombre.

local M = {}

local Theme = {}
Theme.__index = Theme

-- einval: error estructurado del core (api.md §1.4) — el toolkit reusa los
-- códigos del core para lo que ya tiene código (un argumento inválido es EINVAL,
-- no merece un código propio del toolkit).
local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- is_literal_color: ¿`v` es ya un color LITERAL que el core aceptaría (api.md
-- §9.2)? Un "#rrggbb" (6 dígitos hex) o un índice 0-255 (número o string
-- numérica). NO valida nombres semánticos: esos los resuelve `colors`. Es la
-- misma forma que `normalizeColor` del core, replicada en Lua para poder
-- distinguir "ya es literal" de "es un nombre que hay que resolver" SIN tener que
-- intentar construir un Block y capturar el error.
local function is_literal_color(v)
  if type(v) == "number" then
    return v == math.floor(v) and v >= 0 and v <= 255
  end
  if type(v) == "string" then
    if v:match("^#%x%x%x%x%x%x$") then
      return true
    end
    local n = tonumber(v)
    return n ~= nil and n == math.floor(n) and n >= 0 and n <= 255
  end
  return false
end

-- M.is_literal_color se expone para que los tests (y consumidores) puedan
-- afirmar "esto es un literal que el core aceptará".
M.is_literal_color = is_literal_color

-- Theme:color(name) -> literal. Resuelve un nombre semántico a su literal según
-- la paleta del theme. Un literal ya válido pasa intacto (mezclar nombres y
-- literales es legítimo). Un nombre desconocido es EINVAL accionable: un theme
-- al que le falta un color debe notarse, no degradar a un default silencioso que
-- enmascara el bug.
function Theme:color(name)
  if is_literal_color(name) then
    -- Ya es un literal: normaliza un índice numérico a string (el core acepta
    -- ambos, pero devolver una forma estable simplifica las comparaciones).
    if type(name) == "number" then
      return tostring(math.floor(name))
    end
    return name
  end
  if type(name) ~= "string" then
    einval(string.format(
      "toolkit.theme: un color debe ser un nombre semántico (string) o un literal; llegó %s",
      type(name)))
  end
  local lit = self.colors[name]
  if lit == nil then
    einval(string.format(
      "toolkit.theme: nombre de color %q desconocido en el theme %q (define la paleta con toolkit.theme.new{...})",
      name, self.name or "?"))
  end
  return lit
end

-- Theme:style(spec) -> Style. Convierte una tabla de estilo con `fg`/`bg`
-- semánticos (o literales) a un `Style` (api.md §9.2) con los colores ya
-- LITERALES, listo para `nu.ui.block`/`Region:fill`/`nu.text.*`. Copia los
-- atributos booleanos tal cual. `spec` puede ser nil (devuelve nil: "sin
-- estilo"). El resultado es una tabla NUEVA: no muta `spec` (un mismo spec
-- semántico se reusa entre themes).
function Theme:style(spec)
  if spec == nil then
    return nil
  end
  if type(spec) ~= "table" then
    einval("toolkit.theme:style: spec debe ser una tabla {fg?, bg?, bold?, ...}")
  end
  local out = {}
  if spec.fg ~= nil then
    out.fg = self:color(spec.fg)
  end
  if spec.bg ~= nil then
    out.bg = self:color(spec.bg)
  end
  -- Atributos: se copian sin tocar (no son colores).
  out.bold = spec.bold
  out.italic = spec.italic
  out.underline = spec.underline
  out.reverse = spec.reverse
  return out
end

-- Theme:with(overrides) -> Theme. Deriva un theme nuevo con algunos colores
-- sustituidos/añadidos (el resto se hereda). No muta el original: el theme base
-- (`default`) es compartido, así que personalizarlo crea una copia. Útil para
-- que el usuario ajuste un par de colores en su `init.lua` sin redefinir la
-- paleta entera.
function Theme:with(overrides)
  local colors = {}
  for k, v in pairs(self.colors) do
    colors[k] = v
  end
  for k, v in pairs(overrides or {}) do
    if not is_literal_color(v) then
      einval(string.format(
        "toolkit.theme:with: el color %q debe ser un literal \"#rrggbb\"/0-255, no %q (un theme resuelve A literales)",
        tostring(k), tostring(v)))
    end
    colors[k] = (type(v) == "number") and tostring(math.floor(v)) or v
  end
  return M.new({ name = (self.name or "theme") .. "+", colors = colors })
end

-- toolkit.theme.new{name?, colors} -> Theme. Construye un theme. `colors` es la
-- paleta: nombres semánticos → literales. Se VALIDA al construir que cada valor
-- sea un literal que el core aceptará (api.md §9.2): un theme que mapeara
-- "accent" a otro nombre, o a basura, fallaría más tarde dentro de `nu.ui.block`
-- con un error menos claro; validarlo aquí lo ancla al theme.
function M.new(opts)
  opts = opts or {}
  local colors = opts.colors or {}
  if type(colors) ~= "table" then
    einval("toolkit.theme.new: `colors` debe ser una tabla nombre→literal")
  end
  local norm = {}
  for k, v in pairs(colors) do
    if not is_literal_color(v) then
      einval(string.format(
        "toolkit.theme.new: el color %q debe ser un literal \"#rrggbb\" o 0-255, no %q "
          .. "(un theme RESUELVE nombres semánticos A literales, G22)",
        tostring(k), tostring(v)))
    end
    norm[k] = (type(v) == "number") and tostring(math.floor(v)) or v
  end
  return setmetatable({ name = opts.name or "theme", colors = norm }, Theme)
end

-- El theme por defecto. Una paleta mínima pero suficiente para el harness: los
-- nombres que chat.md §7 nombra (`accent`, `error`, `dim`) más los básicos de
-- texto/fondo/aviso. Todos resueltos a literales hex (G22). El usuario lo
-- sustituye o deriva con `:with{...}`.
M.default = M.new({
  name = "default",
  colors = {
    fg      = "#c0c0c0", -- texto normal
    bg      = "#000000", -- fondo
    accent  = "#5fafff", -- realce (selección, foco, enlaces)
    error   = "#ff5f5f", -- error
    warn    = "#ffd75f", -- aviso
    success = "#5fd75f", -- éxito
    dim     = "#808080", -- atenuado (thinking, metadatos)
    border  = "#444444", -- bordes/separadores
  },
})

return M
