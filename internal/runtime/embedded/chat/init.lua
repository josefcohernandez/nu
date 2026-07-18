-- Extensión oficial `chat` (S43): la **UI oficial del harness** — la cara
-- visible de enu, lo que el usuario ve al arrancar.
--
-- Implementa el contrato de [chat.md](../../../../docs/chat.md): el LAYOUT
-- (transcript desplazable + input multilínea + statusline, §1), el RENDER DEL
-- TURNO (consume los eventos `agent:*` —`agent:delta` pintado con markdown en
-- streaming en el transcript, `agent:message`, `agent:tool.*`,
-- `agent:permission.asked`—, §2), el INPUT multilínea (enter envía, shift/alt+enter
-- nueva línea, §3), el DIÁLOGO DE PERMISOS (§5), la STATUSLINE (§6) y el THEMING
-- semántico del toolkit (§7). Sin privilegios: consume la API pública del agente
-- ([agente.md](../../../../docs/agente.md)), el toolkit de widgets (extensión
-- oficial S42 sobre [api.md](../../../../docs/api.md) §9) y el bus de eventos —una
-- UI de terceros podría hacer lo mismo (ADR-003).
--
-- ADR-003: el core NO sabe lo que es un chat; todo es Lua puro sobre la API
-- pública congelada y sobre las extensiones `toolkit`/`agent`/`providers`/`sessions`
-- (declaradas en `requires` del plugin.toml: el loader §14 las ordena antes). El
-- namespace de eventos de esta extensión sería `chat:` (el del propio plugin,
-- §4); en S43 no emite eventos propios (consume `agent:*` y `toolkit:focus`).
--
-- chat SOLO tiene sentido con TTY interactivo (chat.md §8): necesita `enu.ui`
-- (headless, G20, no existe). El `init.lua` solo CABLEA el módulo y arranca la UI
-- si hay TTY; en headless deja el módulo accesible (para tests/inspección) pero NO
-- monta ninguna app. Así `enu -e` (headless) carga la extensión sin tocar `enu.ui`,
-- y un `enu` interactivo con `chat` activo abre el chat al emitirse `core:ready`.

local chat = require("chat")

-- Arranque automático en TTY (chat.md §8): solo si hay `enu.ui` (`enu.has("ui")`,
-- api.md §9/G20). El chat se monta al `core:ready` —el último evento del arranque
-- canónico (api.md §14), cuando todas las extensiones (incluido el `init.lua` del
-- usuario, que puede remapear `chat.keys` o el theme) ya están cargadas—. En
-- headless ni se suscribe: el módulo queda accesible por `require("chat")` para
-- tests y scripts, pero no hay UI que montar.
if enu.has("ui") then
  enu.events.once("core:ready", function()
    -- `chat.start` SUSPENDE (lee config, crea/reanuda la sesión), así que se lanza
    -- como task (el handler de un evento es síncrono, api.md §4). Falta de config
    -- (no hay modelo/provider) NO llega aquí: `chat.start` arranca DEGRADADO con una
    -- UI accionable (chat.md §8, ADR-017/G35). Este `pcall` solo atrapa fallos
    -- INESPERADOS, que se loguean (no hay UI donde pintarlos).
    enu.task.spawn(function()
      local ok, err = pcall(chat.start)
      if not ok then
        enu.log.error("chat: no se pudo arrancar: %s",
          (type(err) == "table" and err.message) or tostring(err))
      end
    end)
  end)
end
