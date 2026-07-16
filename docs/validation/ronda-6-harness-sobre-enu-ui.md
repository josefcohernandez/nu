---
title: "Ronda 6: reconstruir un harness estilo claude-code sobre `enu.ui`"
type: "ronda"
id: "ronda-6"
zone: "reconstruir un harness estilo claude-code sobre `enu.ui`"
status: "cerrada"
scenarios: [28, 29, 30, 31]
findings: ["G28", "G29", "G30"]
---
# Ronda 6: reconstruir un harness estilo claude-code sobre `enu.ui`

Pregunta del stress test: ¿se puede montar la TUI de un harness de coding
(estilo claude-code) **entera** sobre `enu.ui` crudo + el contrato de
[chat.md](chat.md)? La respuesta corta es que `chat.md` ya *es* ese harness;
así que esta ronda no reescribe lo ya validado (transcript, modales, slash,
statusline — escenario 5 cubrió el picker modal) sino que tortura lo que
`chat.md` da por hecho: el **scrollback** del transcript, el **cursor real**
del editor multilínea, el **spinner en vivo**, y el **ratón** sobre bloques
colapsables. Ahí salen tres grietas, todas de `enu.ui` §9. Hallazgos G28-G30
al final.

## Escenario 28: las tres zonas y el scrollback del transcript

```lua
-- plugins/cc-ui/init.lua — una UI estilo coding-harness sobre enu.ui
local function layout()
  local s = enu.ui.size()
  return {
    transcript = enu.ui.region{ x = 0, y = 0,       w = s.w, h = s.h - 4 },
    input      = enu.ui.region{ x = 0, y = s.h - 4, w = s.w, h = 3,  z = 10 },
    status     = enu.ui.region{ x = 0, y = s.h - 1, w = s.w, h = 1,  z = 10 },
  }
end

-- El transcript es un Block alto (todo el historial renderizado) que se
-- "asoma" por la región vía un offset vertical. Scroll = re-blit con otro y.
local scroll, doc = 0, enu.ui.block({})           -- doc.height puede ser >> región
local function repaint_transcript(reg)
  reg:clear()
  reg:blit(0, -scroll, doc)                       -- [HALLAZGO G28] ¿blit acepta y<0?
end
enu.events.on("ui:resize", function() relayout() end)   -- G1: tu región, tu resize
```

Veredicto: sale, salvo una grieta de especificación. `Region:blit`
*"recorta a los límites"*, pero el scrollback necesita estampar el Block con
`y` **negativo** para recortar las primeras filas (asomarse por abajo). El
doc solo habla del recorte por exceso, no de coordenadas locales negativas —
y es la operación central de cualquier transcript con scroll. **[G28]**

## Escenario 29: editor multilínea con cursor real y popups `@` / `/`

```lua
local buf, cur = "", 0                            -- texto y caret (índice de byte)
local function redraw_input(reg)
  local wrapped = enu.text.wrap(buf, reg.w)        -- Block; .height conocido
  if wrapped.height + 1 ~= reg.h then reg:resize(reg.w, wrapped.height + 1) end
  reg:clear(); reg:blit(0, 0, wrapped)
  local cx, cy = caret_to_cell(buf, cur, reg.w)   -- enu.text.width por grafema
  reg:cursor(cx, cy)                               -- cursor real del terminal
end

enu.ui.on_input(function(ev)
  if ev.type == "paste" then
    local ins = ev.text or ev.path                 -- [G30] imagen → ruta, como @
    buf = insert(buf, cur, ins); cur = cur + #ins
  elseif ev.key == "enter" and at_start_slash(buf) then run_slash(buf)
  elseif ev.key == "enter" then session:send(buf); buf = ""
  elseif ev.text == "@" then
    local path = fuzzy_picker("@ file")            -- escenario 5, como popup z=100
    if path then buf = insert(buf, cur, path) end
  end
  redraw_input(input_region); return true
end)
```

Veredicto: sale entero. `enu.text.wrap` da la altura para crecer la caja,
`Region:cursor` coloca el caret real, y los popups `@`/`/` son el picker del
escenario 5 reutilizado. El único trabajo feo es `caret_to_cell` (índice de
byte → celda con `enu.text.width`), pero eso es del toolkit, no API que falte.
Pegar una imagen aparece aquí como **ruta** (G30, abajo).

## Escenario 30: el spinner "Thinking…" en vivo con `esc` para interrumpir

```lua
local function thinking_indicator(session)
  local t0  = enu.sys.mono_ms()
  local reg = enu.ui.region{ x = 0, y = spin_y, w = 40, h = 1 }
  local frame = 0
  local timer = enu.task.every(80, function()       -- handler síncrono, repinta
    frame = frame + 1
    local secs = math.floor((enu.sys.mono_ms() - t0) / 1000)
    local toks = providers.approx_tokens(session.usage)   -- vocabulario de producto
    reg:blit(0, 0, enu.ui.block({{
      { text = SPIN[frame % #SPIN + 1] .. " Thinking… ", style = { italic = true } },
      { text = secs .. "s · " .. toks .. " tok · esc to interrupt",
        style = { fg = "#808080" } },
    }}))
  end)
  enu.task.cleanup(function() timer:stop(); reg:destroy() end)   -- F1/F2: muere con el turno
end
-- esc → Session:cancel() (chat.md §3); el cleanup mata timer y región.
```

Veredicto: limpio. `enu.task.every` anima, `mono_ms` cuenta, `cleanup`
garantiza que el spinner muere con el turno aunque lo aborten — es el patrón
F5 (repintar coalescido, no por delta).

## Escenario 31: ratón sobre un bloque de tool colapsable (análisis)

```lua
-- Clicar la cabecera de un bloque de tool para plegarlo:
enu.ui.on_input(function(ev)
  if ev.type == "mouse" then
    -- ev.x, ev.y vienen en coordenadas de PANTALLA; el bloque vive en
    -- coordenadas LOCALES de la región del transcript, desplazado por el
    -- scroll. No hay Region:contains(x,y) ni traducción global→local: el
    -- plugin rastrea a mano la geometría de cada región (que él fijó) y
    -- resuelve el hit-test sumando/restando origen y scroll.        [G29]
  end
end)
```

Veredicto: expresable, pero a mano. El modelo de pila entrega el ratón en
coordenadas globales y las regiones son locales; sin una primitiva de
traducción/hit-test, cada widget clicable del toolkit reimplementa el mismo
cálculo. **[G29]**

---

## Hallazgos (ronda 6)

Las tres quedaron resueltas tras discutir contraindicaciones (registradas en
[problemas.md](problemas.md)):

**G28 — `Region:blit` con coordenadas locales negativas (viewport/scrollback).**
Mecanismo central del transcript con scroll; el doc solo especificaba el
recorte por exceso. Resuelta en [api.md](api.md) §9.1: `blit` recorta por
**ambos extremos** (negativos recortan el borde inicial), es **copia y no
re-render**, y la virtualización es del toolkit. Las contraindicaciones que
afinaron la resolución: clavar la semántica del negativo, garantizar que no
re-renderiza, y reconocer que no resuelve la virtualización (patrón "cachea
el Block, mueve el offset" en la guía §6).

**G29 — Ratón en coordenadas globales sin traducción a región (hit-testing).**
La tentación era `Region:hit(x,y)`, pero solo haría la mitad trivial (restar
el origen que el plugin ya fijó); la mitad valiosa (qué bloque/línea de un
Block scrolleado) necesita el layout que el plugin posee, no el core.
Resuelta como **convención del toolkit** (opción c), mismo reparto que G1
(relayout) y G22 (theming) — guía §6.

**G30 — Pegar una imagen no es expresable; el evento `paste` solo trae texto.**
Resolución (decidida): pegar contenido no-texto **inyecta una ruta**, no los
bytes — el core vuelca la imagen del portapapeles a un temporal de sesión y
el evento `paste` trae `path`; la UI la inserta igual que una mención `@` y
el agente decide leerla. Mantiene los binarios fuera de las fronteras
texto/JSON (coherente con G11) y es distinto de P6 (render de imágenes en
pantalla, pospuesto). Aplicada a [api.md](api.md) §9.3.

Confirmaciones (sin API nueva): las tres zonas, el editor multilínea con
cursor real, los popups `@`/`/`, el spinner en vivo y los renderers de tools
se montan **enteros** sobre `enu.ui` + el contrato de `chat`. La conclusión de
la pregunta que abrió la ronda se sostiene: la TUI de un harness de coding no
"sale del core" — el core da el sustrato y `chat.md` ya es ese harness. Las
únicas grietas (G28, G29) son de **ergonomía de `enu.ui`**, no de mecanismo
que falte.


---
