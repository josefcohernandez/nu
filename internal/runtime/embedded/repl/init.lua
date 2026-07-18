-- Extensión oficial `repl` (S44): un **REPL de Lua** sobre la API pública.
--
-- Implementa el contrato de [arquitectura.md](../../../../docs/arquitectura.md)
-- §"Distribución" (G21): el conjunto de extensiones embebidas incluye, además del
-- harness (agente, chat, providers, MCP, toolkit), un `repl` —REPL de Lua sobre la
-- API pública, **activable SOLO**, el punto de partida del autor de extensiones que
-- no quiere el harness—. Es la prueba de que el runtime sirve para más que el
-- agente: `enu` con SOLO `repl` activo es un intérprete Lua interactivo con `nu.*`.
--
-- ADR-003: el core NO sabe lo que es un REPL; todo es Lua puro sobre la API pública
-- congelada ([api.md](../../../../docs/api.md)), SIN privilegio de kernel. El
-- `plugin.toml` NO declara `requires`: el repl se activa SOLO (G21), sin arrastrar
-- el harness. La EVALUACIÓN de Lua arbitrario la da `load` del baseline 5.4
-- (que absorbió al `loadstring` de 5.1; el sandbox de S01 retiró `dofile`/`loadfile`
-- —disco— pero no `load` —memoria—): la API pública BASTA, sin primitiva nueva (corolario de completitud
-- satisfecho; APILevel sigue en 2, api.md intacto). El detalle, en el módulo.
--
-- El `init.lua` CABLEA el módulo (accesible por `require("repl")`) y, si hay TTY,
-- arranca la UI interactiva al `core:ready` —igual que el chat (chat.md §8)—. En
-- headless (G20, `enu -e`, CI) NO monta UI: deja el módulo accesible (`repl.eval`
-- evalúa una línea sin pantalla; lo que prueban los tests). Así `enu` con solo
-- `repl` activo: en TTY abre el REPL interactivo; en headless, el módulo está listo.

local repl = require("repl")

-- Arranque automático en TTY (G21): solo si hay `enu.ui` (`enu.has("ui")`, api.md
-- §9/G20). Se monta al `core:ready` —el último evento del arranque canónico (api.md
-- §14), cuando todas las extensiones (incluido el `init.lua` del usuario) ya están
-- cargadas—. En headless ni se suscribe: el módulo queda accesible por
-- `require("repl")` para `repl.eval` y scripts, pero no hay UI que montar.
-- chat_is_active() -> bool. ¿Está el `chat` (la UI oficial del harness) entre los
-- plugins ACTIVOS? (`enu.plugin.list()`, devuelve `{name, …, enabled}`). DESACOPLA al
-- repl del chat (G36): el repl no `require`a chat (debe poder activarse SOLO, G21),
-- pero sí puede mirar el registro del loader para saber si OTRA extensión reclamará la
-- pantalla.
local function chat_is_active()
  if enu.plugin == nil or enu.plugin.list == nil then
    return false
  end
  local ok, list = pcall(enu.plugin.list)
  if not ok or type(list) ~= "table" then
    return false
  end
  for _, p in ipairs(list) do
    if p.name == "chat" and p.enabled ~= false then
      return true
    end
  end
  return false
end

-- Arranque automático en TTY (G21/G36): solo si hay `enu.ui` Y el chat NO está activo.
--
-- POR QUÉ CEDE AL CHAT (G36). El conjunto oficial de producto (ADR-015) activa chat Y
-- repl; ambos auto-montaban una `toolkit.app` a pantalla completa en `core:ready`, se
-- solapaban, y al salir del chat quedaba el REPL debajo —esa sensación de "salir de una
-- capa para caer en otra"—. El repl es la herramienta del autor que NO quiere el harness
-- (activable SOLO, G21); cuando el harness (chat) está presente, es ESTE quien posee la
-- pantalla y el repl queda como módulo accesible (`require("repl")`, `repl.eval`) sin
-- montar UI. Así `enu` con el conjunto oficial abre SOLO el chat; `enu` con solo `repl`
-- abre el REPL. En headless, ninguno monta UI.
if enu.has("ui") then
  enu.events.once("core:ready", function()
    if chat_is_active() then
      return -- el chat posee la pantalla (G36)
    end
    -- `repl.start` monta la UI (no suspende, pero la lanzamos protegida por si el
    -- toolkit no estuviera): un fallo se loguea (aún no hay UI donde pintarlo).
    local ok, err = pcall(repl.start)
    if not ok then
      enu.log.error("repl: no se pudo arrancar la UI: %s",
        (type(err) == "table" and err.message) or tostring(err))
    end
  end)
end
