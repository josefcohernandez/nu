-- Módulo público de la extensión `toolkit` (S42): el toolkit de widgets.
--
-- Implementa la nota de [arquitectura.md](../../../../../docs/arquitectura.md)
-- §"kernel" sobre `ui`: el core expone celdas/regiones + compositor (api.md §9,
-- bajo nivel, ADR-007); el TOOLKIT —esta extensión Lua oficial, retenida por
-- dentro (árbol + nodos sucios)— aporta lo de alto nivel: SLOTS (contenedores de
-- layout), FOCUS (enrutado del input al widget enfocado), composición ENTRE
-- PLUGINS (cada app su árbol/región, sin colisión) y el sistema de THEMES (los
-- nombres semánticos de color se resuelven aquí a literales, G22). Se versiona
-- aparte de la API sagrada (no es API del core).
--
-- ADR-003 / ADR-012: Lua puro sobre la API pública congelada
-- ([api.md](../../../../../docs/api.md) §9 `enu.ui` + §10 `enu.text`), sin
-- privilegio de kernel; el spike de S28 (ADR-012) descartó el veto de ADR-007 (el
-- toolkit en Lua es fluido porque el trabajo pesado es primitiva Go), así que aquí
-- vive. Reusa SOLO: `enu.ui.region`/`blit`/`fill`/`clear`/`cursor`/`size` (§9.1),
-- `enu.ui.block`/`Style` (§9.2), `enu.ui.on_input` (§9.3), `enu.text.markdown`/
-- `wrap`/`truncate`/`width` (§10), `enu.events.emit` (§4 — emite su propio
-- `toolkit:focus`, en el namespace del plugin; `ui:` es del core, §4), `enu.has`
-- (§2). NO amplía api.md (corolario de completitud satisfecho: la API §9/§10 basta
-- exacta para un toolkit con árbol/dirty/slots/focus/themes; ni una función de
-- más).
--
-- La superficie pública del módulo (la "API del toolkit", cuestión abierta nº3 de
-- arquitectura.md, fijada al construirlo):
--
--   toolkit.app{region?|x,y,w,h,z?, root?, theme?, manage_input?} -> App
--       La raíz: vincula el árbol a una región, gestiona foco, enruta input,
--       repinta por nodos sucios. App: relayout/resize/set_focus/focus_next/
--       focus_prev/handle_key/paint/close.
--   toolkit.vbox/hbox/stack{...} -> contenedor (slots): apilan/superponen hijos.
--   toolkit.label{text?, style?} -> Label        (una línea estilizada)
--   toolkit.text{text?, markdown?} -> Text        (bloque multilínea con scroll)
--   toolkit.input{value?, placeholder?} -> Input  (editor focusable de una línea)
--   toolkit.theme.new{name?, colors} / toolkit.theme.default -> Theme (G22)
--   toolkit.widget.new{...} -> Widget             (el nodo base, para tipos nuevos)
--
-- Lo que reusará S43 (chat) — chat.md §1/§7:
--   * `toolkit.app` como raíz de su UI (una columna), suscrita a `ui:resize`;
--   * `toolkit.vbox` para la columna transcript/input/statusline y `toolkit.stack`
--     para las capas modales (diálogo de permisos, pickers);
--   * `toolkit.text{markdown=true}` para el transcript (deltas de `agent:delta` →
--     `set_text` del bloque en curso; el scroll por viewport ya está);
--   * `toolkit.input` para el editor (focusable, on_key; el multilínea es la
--     extensión natural del mismo contrato);
--   * `toolkit.label` para la statusline (segmentos = labels en un hbox);
--   * `toolkit.theme` para los colores semánticos `accent`/`error`/`dim`… que
--     chat.md §7 exige (chat NO hardcodea un color: pide nombres al theme).

local M = {}

-- Submódulos. El theme y el widget base se exponen enteros (los consumidores
-- construyen themes propios y tipos de widget nuevos); contenedores y hojas se
-- re-exportan como funciones de fábrica para una superficie plana cómoda.
M.theme  = require("toolkit.theme")
M.widget = require("toolkit.widget")

local layout  = require("toolkit.layout")
local widgets = require("toolkit.widgets")
local app     = require("toolkit.app")

-- Contenedores (slots).
M.vbox  = layout.vbox
M.hbox  = layout.hbox
M.stack = layout.stack

-- Widgets hoja y de decoración.
M.label    = widgets.label
M.text     = widgets.text
M.input    = widgets.input
M.box      = widgets.box      -- marco (borde + título + padding) alrededor de un hijo
M.spinner  = widgets.spinner  -- indicador de actividad animado (enu.task.every)
M.richtext = widgets.richtext -- una línea de varios spans estilizados (line builder)

-- La raíz.
M.app = app.app

return M
