-- tui-demo — una TUI mínima pero COMPLETA escrita en Lua sobre la API pública del
-- core (`nu.ui` §9, `nu.events` §4, `nu.task` §3). Demuestra que, con el driver de
-- TTY (S33), una extensión Lua pinta y responde en un terminal de verdad: regiones,
-- blocks estilizados, teclado (keymap + on_input), reloj en vivo y `ui:resize`.
--
-- No usa el toolkit a propósito: se queda en la API de bajo nivel del core para que
-- se vea, sin intermediarios, qué primitivas bastan para una UI funcional. El toolkit
-- (extensión oficial) construye sus widgets exactamente sobre estas mismas funciones.
--
-- Cómo correrlo (desde la raíz del repo):
--     XDG_CONFIG_HOME=examples go run .
--   o, con el binario instalado:
--     XDG_CONFIG_HOME=examples nu
-- Teclas: ↑/↓ o j/k mueven el contador · escribe para rellenar el campo ·
--         Enter lo confirma · Backspace borra · q o Ctrl+C salen.

-- Sin TTY (headless: `nu -e`, CI, salida redirigida) no hay `nu.ui` (G20). La
-- extensión se carga igual, pero no monta nada: una UI sin terminal no existe.
if not nu.has("ui") then
  nu.log.info("tui-demo: sin TTY, no se monta la UI (headless, G20)")
  return
end

local theme = {
  border = { fg = "#7aa2f7" },
  title  = { fg = "#bb9af7", bold = true },
  label  = { fg = "#565f89" },
  value  = { fg = "#c0caf5", bold = true },
  hint   = { fg = "#565f89", italic = true },
  field  = { fg = "#9ece6a" },
}

local state = {
  count = 0,
  last_key = "—",
  field = "",
  ticks = 0,
}

-- Una región a pantalla completa. El compositor recorta sola si la pantalla encoge,
-- y reaparece intacta si crece (G1); en `ui:resize` la redimensionamos al nuevo tamaño.
local size = nu.ui.size()
local region = nu.ui.region({ x = 0, y = 0, w = size.w, h = size.h, z = 0 })

-- repeat_str: una utilidad para los bordes (Lua 5.1 tiene string.rep, pero lo
-- exponemos con nombre propio por claridad).
local function rep(s, n)
  if n <= 0 then return "" end
  return string.rep(s, n)
end

-- draw reconstruye el Block de la pantalla entera a partir del estado y lo blittea.
-- Es "Lua decide, Go ejecuta": Lua arma las líneas estilizadas; el compositor difa y
-- pinta en Go (api.md §9.1). Repintar entero es barato: el blit es copia y el diff
-- solo reemite las celdas que cambian.
local function draw()
  local s = nu.ui.size()
  local w, h = s.w, s.h
  region:resize(w, h)
  region:fill()

  local inner = w - 2
  local lines = {}

  local function span_line(spans) lines[#lines + 1] = spans end
  local function text_line(text, style) span_line({ { text = text, style = style } }) end

  -- Borde superior con el título incrustado.
  local title = " nu · tui-demo "
  local left = math.floor((inner - #title) / 2)
  if left < 0 then left = 0 end
  span_line({
    { text = "╭" .. rep("─", left), style = theme.border },
    { text = title, style = theme.title },
    { text = rep("─", math.max(0, inner - left - #title)) .. "╮", style = theme.border },
  })

  -- Cuerpo: pares etiqueta/valor.
  local function row(label, value, vstyle)
    local pad = rep(" ", math.max(0, 16 - #label))
    span_line({
      { text = "│ ", style = theme.border },
      { text = label .. pad, style = theme.label },
      { text = value, style = vstyle or theme.value },
      { text = rep(" ", math.max(0, inner - 1 - #label - #pad - #value)) .. "│", style = theme.border },
    })
  end

  text_line("│" .. rep(" ", inner) .. "│", theme.border)
  row("contador", tostring(state.count))
  row("última tecla", state.last_key)
  row("tamaño", w .. "x" .. h)
  row("reloj (ticks)", tostring(state.ticks))
  row("campo", (state.field == "" and "(escribe algo…)" or state.field),
    state.field == "" and theme.hint or theme.field)
  text_line("│" .. rep(" ", inner) .. "│", theme.border)

  -- Relleno hasta el pie.
  while #lines < h - 2 do
    text_line("│" .. rep(" ", inner) .. "│", theme.border)
  end

  -- Pie con las teclas.
  local hint = " ↑/↓ j/k: contador · escribe: campo · q / Ctrl+C: salir "
  local hleft = math.max(0, math.floor((inner - #hint) / 2))
  span_line({
    { text = "╰" .. rep("─", hleft), style = theme.border },
    { text = hint, style = theme.hint },
    { text = rep("─", math.max(0, inner - hleft - #hint)) .. "╯", style = theme.border },
  })

  region:blit(0, 0, nu.ui.block(lines))

  -- El cursor real, al final del campo (api.md §9.1: Region:cursor en coords locales).
  -- El campo vive en la fila 6 (índice 5), tras "│ campo           ".
  region:cursor(2 + 16 + #state.field, 6)
end

-- ORDEN DE LA PILA DE INPUT (§9.3). La pila prueba el handler MÁS RECIENTE primero, y
-- el que no consume deja pasar al de abajo. Queremos que las TECLAS DE COMANDO
-- (q, j, k, flechas) ganen, y que el resto de imprimibles rellenen el campo. Así que
-- registramos PRIMERO el handler del campo (queda al fondo) y DESPUÉS los keymaps de
-- comando (quedan encima): una `q` la atrapa el keymap de salir, no el campo; una `a`
-- no casa ningún keymap y cae al campo. Es la regla "la pila manda" del contrato.

-- Edición del campo: handler crudo (on_input) al FONDO. Acepta imprimibles y backspace.
nu.ui.on_input(function(ev)
  if ev.type ~= "key" then return false end
  if ev.key == "backspace" then
    state.field = state.field:sub(1, -2)
    state.last_key = "⌫"
    draw()
    return true
  end
  if ev.key == "enter" then
    state.last_key = "⏎"
    draw()
    return true
  end
  -- Una tecla imprimible (un solo grapheme, sin ctrl/alt) se añade al campo.
  if #ev.key == 1 and not (ev.mods and (ev.mods.ctrl or ev.mods.alt)) then
    state.field = state.field .. ev.key
    state.last_key = ev.key
    draw()
    return true
  end
  return false
end)

-- Teclado de navegación y acciones (keymap = azúcar sobre la pila, ENCIMA del campo).
local function bump(delta)
  state.count = state.count + delta
  state.last_key = delta > 0 and "↑" or "↓"
  draw()
end

nu.ui.keymap("up", function() bump(1) end)
nu.ui.keymap("k", function() bump(1) end)
nu.ui.keymap("down", function() bump(-1) end)
nu.ui.keymap("j", function() bump(-1) end)

-- Salir: emite core:shutdown (§4), que el driver de TTY convierte en apagado ordenado
-- (restaura el terminal). "Lua decide cuándo salir; Go ejecuta el apagado."
nu.ui.keymap("q", function() nu.events.emit("core:shutdown") end)
nu.ui.keymap("ctrl+c", function() nu.events.emit("core:shutdown") end)

-- Reloj en vivo: una task periódica (§3) que repinta cada segundo. Demuestra que la UI
-- reacciona a fuentes ASÍNCRONAS, no solo al teclado —el painter coalescea los frames—.
nu.task.every(1000, function()
  state.ticks = state.ticks + 1
  draw()
end)

-- Resize: "tu región, tu ui:resize" (§9.1). Redibujamos al nuevo tamaño.
nu.events.on("ui:resize", function() draw() end)

-- Primer frame.
draw()
nu.log.info("tui-demo: UI montada")
