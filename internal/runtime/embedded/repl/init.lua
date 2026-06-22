-- Extensión oficial `repl` (S44): un **REPL de Lua** sobre la API pública.
--
-- Implementa el contrato de [arquitectura.md](../../../../docs/arquitectura.md)
-- §"Distribución" (G21): el conjunto de extensiones embebidas incluye, además del
-- harness (agente, chat, providers, MCP, toolkit), un `repl` —REPL de Lua sobre la
-- API pública, **activable SOLO**, el punto de partida del autor de extensiones que
-- no quiere el harness—. Es la prueba de que el runtime sirve para más que el
-- agente: `nu` con SOLO `repl` activo es un intérprete Lua interactivo con `nu.*`.
--
-- ADR-003: el core NO sabe lo que es un REPL; todo es Lua puro sobre la API pública
-- congelada ([api.md](../../../../docs/api.md)), SIN privilegio de kernel. El
-- `plugin.toml` NO declara `requires`: el repl se activa SOLO (G21), sin arrastrar
-- el harness. La EVALUACIÓN de Lua arbitrario la dan `load`/`loadstring` del
-- baseline (el sandbox de S01 retiró `dofile`/`loadfile` —disco— pero no estas
-- —memoria—): la API pública BASTA, sin primitiva nueva (corolario de completitud
-- satisfecho; APILevel sigue en 2, api.md intacto). El detalle, en el módulo.
--
-- El `init.lua` CABLEA el módulo (accesible por `require("repl")`) y, si hay TTY,
-- arranca la UI interactiva al `core:ready` —igual que el chat (chat.md §8)—. En
-- headless (G20, `nu -e`, CI) NO monta UI: deja el módulo accesible (`repl.eval`
-- evalúa una línea sin pantalla; lo que prueban los tests). Así `nu` con solo
-- `repl` activo: en TTY abre el REPL interactivo; en headless, el módulo está listo.

local repl = require("repl")

-- Arranque automático en TTY (G21): solo si hay `nu.ui` (`nu.has("ui")`, api.md
-- §9/G20). Se monta al `core:ready` —el último evento del arranque canónico (api.md
-- §14), cuando todas las extensiones (incluido el `init.lua` del usuario) ya están
-- cargadas—. En headless ni se suscribe: el módulo queda accesible por
-- `require("repl")` para `repl.eval` y scripts, pero no hay UI que montar.
if nu.has("ui") then
  nu.events.once("core:ready", function()
    -- `repl.start` monta la UI (no suspende, pero la lanzamos protegida por si el
    -- toolkit no estuviera): un fallo se loguea (aún no hay UI donde pintarlo).
    local ok, err = pcall(repl.start)
    if not ok then
      nu.log.error("repl: no se pudo arrancar la UI: %s",
        (type(err) == "table" and err.message) or tostring(err))
    end
  end)
end
