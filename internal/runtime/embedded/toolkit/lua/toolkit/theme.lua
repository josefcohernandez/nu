-- toolkit.theme — el sistema de themes del toolkit (G22).
--
-- EL PROBLEMA (G22). El core (`enu.ui`, api.md §9.2) solo entiende colores
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
--     listo para `enu.ui.block`/`Region:fill`. Los atributos (bold/italic/…) se
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
-- LITERALES, listo para `enu.ui.block`/`Region:fill`/`enu.text.*`. Copia los
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

-- Theme:markdown_opts() -> tabla. Construye la tabla `theme` que `enu.text.markdown`
-- acepta (api.md §10): un `Style` con colores ya LITERALES por elemento markdown
-- (`h1`..`h6`, `code`, `emphasis`, `strong`, `link`, `bullet`, `blockquote`,
-- `rule`). Es el PUENTE que cablea la paleta semántica del theme al render de
-- markdown: sin esto el transcript del chat sale monocromo (los widgets de texto lo
-- pasan en `opts.theme`). Cada elemento se construye con `:style{...}` (resuelve
-- nombres→literales, G22) usando nombres que el theme garantiza (los de la paleta
-- por defecto). El resultado se cachea: la paleta no cambia entre llamadas.
function Theme:markdown_opts()
  if self._md_opts then
    return self._md_opts
  end
  local s = function(spec) return self:style(spec) end
  -- Headings: el h1 con acento y negrita; del h2 en adelante, acento sin tanto
  -- peso (una jerarquía visual suave, no un muro de color).
  local h1 = s({ fg = "heading", bold = true })
  local hn = s({ fg = "heading", bold = true })
  self._md_opts = {
    h1 = h1, h2 = hn, h3 = hn, h4 = hn, h5 = hn, h6 = hn,
    code       = s({ fg = "code" }),
    emphasis   = s({ fg = "fg", italic = true }),
    strong     = s({ fg = "strong", bold = true }),
    link       = s({ fg = "link", underline = true }),
    bullet     = s({ fg = "accent" }),
    blockquote = s({ fg = "dim", italic = true }),
    rule       = s({ fg = "border" }),
  }
  return self._md_opts
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
-- "accent" a otro nombre, o a basura, fallaría más tarde dentro de `enu.ui.block`
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

-- El theme por defecto: una paleta CURADA (no un placeholder), la identidad visual
-- del harness. Acento cálido (coral, la firma de la familia), texto suave sobre
-- fondo casi-negro, y nombres semánticos para TODO lo que la UI de producto pinta:
-- roles (user/assistant), superficies (surface/overlay para tarjetas y modales),
-- selección/foco, código y enlaces, y los colores de diff. Todos resueltos a
-- literales hex (G22). El usuario lo sustituye o deriva con `:with{...}`; un theme
-- alternativo (claro, o de otra marca) es un plugin del toolkit (chat.md §7).
M.default = M.new({
  name = "default",
  colors = {
    -- Base.
    fg        = "#d4d4d4", -- texto normal
    bg        = "#0c0c0c", -- fondo
    dim       = "#7a7a7a", -- atenuado (thinking, metadatos, hints)
    secondary = "#a0a0a0", -- texto secundario (menos que fg, más que dim)
    -- Acentos y estados.
    accent    = "#e0875f", -- realce/firma (coral cálido): foco, viñetas, títulos
    error     = "#ff6b6b", -- error
    warn      = "#e5c07b", -- aviso (umbral de contexto)
    success   = "#98c379", -- éxito (tool ok)
    info       = "#61afef", -- informativo (frío)
    -- Superficies y bordes.
    bg_surface = "#161616", -- fondo de tarjeta/panel (un peldaño sobre bg)
    overlay    = "#1c1c1c", -- fondo de modal (sobre el transcript atenuado)
    border     = "#3a3a3a", -- bordes/separadores en reposo
    border_focus = "#e0875f", -- borde del widget enfocado (= accent)
    selection  = "#2d3b4d", -- fondo de la fila seleccionada en un picker
    -- Roles del transcript.
    role_user      = "#61afef", -- marcador del usuario (azul frío)
    role_assistant = "#e0875f", -- marcador del asistente (coral, la firma)
    -- Markdown y código.
    heading = "#e0875f", -- encabezados (acento)
    strong  = "#f0f0f0", -- **negrita** (un punto más brillante que fg)
    link    = "#61afef", -- enlaces
    code    = "#e5c07b", -- code inline / spans de código (ámbar)
    -- Diff (para el render de ediciones de un coding harness).
    diff_add     = "#98c379", -- líneas añadidas
    diff_del     = "#ff6b6b", -- líneas borradas
    diff_context = "#7a7a7a", -- contexto sin cambios
  },
})

return M
