-- Adaptador STUB de la extensión `providers` (S36).
--
-- Es la prueba viva del **contrato del adaptador** de
-- [providers.md](../../../../../docs/providers.md) §3: una tabla con `name`,
-- `caps` y `stream(req, provider) -> iterator<Event>` (⏸). NO toca la red: emite
-- una secuencia FIJA de `Event`s canónicos (providers.md §2.3) derivada del
-- request, de modo que un test pueda comprobar el stream canónico sin un servidor.
-- El adaptador `anthropic` REAL (SSE de Anthropic sobre `nu.http.stream`) llega
-- en S37 y reusará exactamente esta forma —solo cambia el cuerpo de `stream`,
-- que en vez de eventos fijos parseará el SSE del provider con `Stream:events()`—.
--
-- Cumple las obligaciones de §3 que son verificables sin red:
--   - degradación declarada (obligación 5): si `caps.tools = false` y el request
--     trae `tools`, lanza `EINVAL` —no simula silenciosamente—. El stub declara
--     `tools = false` a propósito para ejercitar esta regla;
--   - `stream` devuelve un iterador de Events y CIERRA con un `done` que incluye
--     el `Message` canónico completo (providers.md §2.3: "done incluye el Message
--     ensamblado, listo para anexar"), de modo que el agente no re-ensambla deltas.

local M = {
  name = "stub",
  -- El stub declara capacidades mínimas: sin tools (para ejercitar la
  -- degradación declarada de §3), con system y usage. Un adaptador real declara
  -- las de su proveedor.
  caps = { tools = false, images = false, thinking = false, system = true, usage = true },
}

-- last_user_text(req) -> string. Recorre el último mensaje de usuario y concatena
-- el texto de sus bloques `text` (providers.md §2.2). El stub "responde" con eco
-- de ese texto, lo justo para que el test verifique que el request fluye al stream.
local function last_user_text(req)
  local text = ""
  for i = #req.messages, 1, -1 do
    local msg = req.messages[i]
    if msg.role == "user" then
      for _, block in ipairs(msg.content) do
        if block.type == "text" then
          text = text .. block.text
        end
      end
      break
    end
  end
  return text
end

-- stream(req, provider) -> iterator<Event> (providers.md §3). En el stub no
-- suspende (no hay red), pero respeta la FIRMA suspendiente: se llama desde la
-- task del agente. Devuelve un iterador estilo Lua (función que entrega un Event
-- por llamada y `nil` al agotarse), que es lo que `for ev in adapter.stream(...)`
-- consume —mismo protocolo que `Stream:events()` (api.md §8) envolverá en S37—.
function M.stream(req, provider)
  -- Degradación declarada (providers.md §3, obligación 5): este adaptador no
  -- soporta tools; si el request las trae, error EXPLÍCITO, no simulación.
  if req.tools ~= nil and #req.tools > 0 then
    error({ code = "EINVAL",
      message = "el adaptador 'stub' no soporta tools (caps.tools=false); providers.md §3 obliga a fallar, no simular" })
  end

  local echo = last_user_text(req)
  -- Reparte el eco en dos deltas de texto para imitar el streaming, más un
  -- `usage` y el `done` final con el Message ensamblado (providers.md §2.3).
  local part1 = echo
  local reply_text = "eco: " .. part1

  -- El Message canónico completo que va en el `done` (providers.md §2.1/§2.3):
  -- rol assistant, un bloque de texto con la respuesta entera.
  local assembled = {
    role = "assistant",
    content = { { type = "text", text = reply_text } },
  }

  -- Secuencia FIJA de Events (providers.md §2.3). El `done` cierra el stream.
  local events = {
    { type = "text", text = "eco: " },
    { type = "text", text = part1 },
    { type = "usage",
      input_tokens  = require("providers").approx_tokens(echo),
      output_tokens = require("providers").approx_tokens(reply_text) },
    { type = "done", stop_reason = "end", message = assembled },
  }

  local i = 0
  return function()
    i = i + 1
    return events[i] -- nil al agotarse: fin del iterador
  end
end

-- count_tokens?(req, provider) -> integer (providers.md §3, opcional). El stub lo
-- implementa con la heurística de la extensión para ejercitar también esta vía
-- del contrato; un adaptador real consultaría el endpoint de conteo del provider.
function M.count_tokens(req, provider)
  local total = 0
  local at = require("providers").approx_tokens
  if type(req.system) == "string" then
    total = total + at(req.system)
  end
  for _, msg in ipairs(req.messages) do
    for _, block in ipairs(msg.content) do
      if block.type == "text" then
        total = total + at(block.text)
      end
    end
  end
  return total
end

return M
