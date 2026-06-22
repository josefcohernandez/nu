-- chat.transcript — el modelo del TRANSCRIPT del chat (chat.md §1/§2).
--
-- QUÉ ES. El transcript es la conversación visible: mensajes del usuario y del
-- asistente, bloques de tools y de thinking (chat.md §1). chat.md §2 manda
-- pintarlos con MARKDOWN vía `nu.text.markdown` («streaming-safe»). El widget que
-- los pinta es un `toolkit.text{markdown=true}` (S42): un bloque multilínea de
-- markdown con scroll por viewport. Este módulo es el MODELO detrás de ese widget:
-- mantiene la lista de "items" (mensajes/tools) y produce el TEXTO MARKDOWN
-- acumulado que se vuelca al widget con `set_text` (el widget re-renderiza el
-- markdown; el dirty tracking del toolkit recompone solo cuando cambia, S42).
--
-- EL STREAMING (chat.md §2, el corazón de S43). Cuando llega `agent:delta` de
-- tipo `text`, el texto se ACUMULA en el item del mensaje del asistente EN CURSO y
-- el transcript re-emite su markdown: el widget `text` crece INCREMENTALMENTE con
-- el texto en streaming (exactamente el camino caliente de CP-9, ahora dentro del
-- chat: delta → acumula → markdown → blit). `agent:message` SELLA el mensaje
-- (sustituye los deltas por el render final del Message del `done`, chat.md §2).
--
-- POR QUÉ UN ÚNICO `text` y no un widget por mensaje. El toolkit `text` ya pinta
-- markdown multilínea con scroll por viewport (S42) y `nu.text.markdown` es
-- streaming-safe (S23): un único bloque markdown con TODA la conversación es lo
-- más simple que cumple el criterio de hecho (conversación con streaming markdown)
-- y reusa el viewport/scroll del toolkit tal cual. Un widget-por-mensaje (para
-- renderers enchufables de tools, chat.md §2) es una mejora natural posterior
-- sobre este mismo modelo (los items ya están separados); v1 los serializa a
-- markdown. DESVIACIÓN documentada en claude_decisions.md (S43).
--
-- THEME (G22, chat.md §7): el render final lo hace `nu.text.markdown`, que es
-- themable (api.md §10); los marcadores de rol (un encabezado por mensaje) usan
-- markdown estándar, así el theme del markdown los colorea. chat NO hardcodea
-- colores: el `toolkit.text` resuelve el estilo contra el theme de la app.

local M = {}

local Transcript = {}
Transcript.__index = Transcript

-- M.new() -> Transcript. El modelo del transcript: una lista de items y el texto
-- markdown acumulado cacheado.
function M.new()
  return setmetatable({
    items = {},      -- { {kind="user"|"assistant"|"tool"|"thinking"|"error", ...}, ... }
    _streaming = nil, -- el item del assistant en curso (acumula deltas), o nil
  }, Transcript)
end

-- add_user(text) anexa un mensaje del usuario al transcript (chat.md §2: "Mensajes
-- de usuario y asistente se muestran con su formato"). Devuelve el item.
function Transcript:add_user(text)
  local item = { kind = "user", text = tostring(text or "") }
  self.items[#self.items + 1] = item
  return item
end

-- begin_assistant() abre un item de asistente EN CURSO al arrancar un turno: los
-- `agent:delta` de texto se acumulan aquí (`append_delta`). `agent:message` lo
-- sella (`seal_assistant`). Si ya hay uno abierto (turno reentrante), lo reusa.
function Transcript:begin_assistant()
  if self._streaming == nil then
    local item = { kind = "assistant", text = "", sealed = false }
    self.items[#self.items + 1] = item
    self._streaming = item
  end
  return self._streaming
end

-- append_delta(text) acumula un delta de texto en el item del asistente en curso
-- (chat.md §2: `agent:delta` → texto en streaming al bloque del mensaje en curso).
-- Abre el item si aún no existe (un delta puede llegar antes de begin_assistant).
function Transcript:append_delta(text)
  local item = self:begin_assistant()
  item.text = item.text .. tostring(text or "")
end

-- append_thinking(text) acumula un delta de THINKING en un bloque de razonamiento
-- (chat.md §2: bloque atenuado, colapsado por defecto). v1 lo serializa como una
-- cita markdown atenuada bajo el mensaje en curso (un bloque aparte, no mezclado
-- con el texto). El plegado/colapsado interactivo es mejora posterior (chat.md §2).
function Transcript:append_thinking(text)
  -- buscamos (o creamos) un item de thinking adyacente al assistant en curso.
  local last = self.items[#self.items]
  if last == nil or last.kind ~= "thinking" or last.sealed then
    last = { kind = "thinking", text = "" }
    self.items[#self.items + 1] = last
  end
  last.text = last.text .. tostring(text or "")
end

-- seal_assistant(message) SELLA el mensaje del asistente con el Message canónico
-- del `done` (chat.md §2: "sustituye los deltas por el render final"). Si el
-- Message trae texto, sustituye el acumulado (el ensamblado es la fuente de
-- verdad, providers.md §2.1); si no (p. ej. un turno solo-tools), conserva lo
-- acumulado. Cierra el item en curso.
function Transcript:seal_assistant(message)
  local item = self._streaming
  if item == nil then
    -- ningún streaming abierto (turno sin deltas de texto): abrimos uno para sellar.
    item = { kind = "assistant", text = "", sealed = false }
    self.items[#self.items + 1] = item
  end
  if type(message) == "table" and type(message.content) == "table" then
    local txt = ""
    for _, b in ipairs(message.content) do
      if b.type == "text" and type(b.text) == "string" then
        txt = txt .. b.text
      end
    end
    if txt ~= "" then
      item.text = txt
    end
  end
  item.sealed = true
  self._streaming = nil
  return item
end

-- add_tool(name, args) anexa un bloque de tool al transcript (chat.md §2:
-- `agent:tool.start` → bloque colapsable con nombre + args resumidos). Devuelve el
-- item para actualizarlo (progress/end). v1: cabecera con el nombre y un resumen de
-- args; el plegado/colapsado y los renderers enchufables (chat.md §2) son mejora
-- posterior sobre este mismo item.
function Transcript:add_tool(id, name, args)
  local item = { kind = "tool", id = id, name = tostring(name or "?"),
                 args = args, status = "running", result = nil }
  self.items[#self.items + 1] = item
  return item
end

-- tool_end(id, is_error, errtext) marca un bloque de tool como terminado (ok o
-- error). Busca por id. chat.md §2: al terminar, resultado plegado si es largo.
function Transcript:tool_end(id, is_error, errtext)
  for i = #self.items, 1, -1 do
    local it = self.items[i]
    if it.kind == "tool" and it.id == id then
      it.status = is_error and "error" or "done"
      if is_error and errtext then
        it.result = tostring(errtext)
      end
      return it
    end
  end
end

-- add_error(text) anexa un bloque de error al transcript (chat.md §2: `agent:error`
-- → bloque de error con el código estructurado). v1: una cita markdown marcada.
function Transcript:add_error(text)
  self.items[#self.items + 1] = { kind = "error", text = tostring(text or "") }
end

-- add_system(text) anexa una nota del sistema (p. ej. un comando slash que
-- responde, o "sesión reanudada"). No es del agente: es de la propia UI.
function Transcript:add_system(text)
  self.items[#self.items + 1] = { kind = "system", text = tostring(text or "") }
end

-- render_item(item) -> string. Un item a su fragmento MARKDOWN. Los marcadores de
-- rol son markdown estándar (encabezados/citas) para que el render de
-- `nu.text.markdown` (themable, api.md §10) los pinte; chat NO mete colores
-- literales (G22, chat.md §7). El texto del usuario/asistente va tal cual (el del
-- asistente YA es markdown — lo genera el modelo). El thinking y los errores van
-- como cita (atenuada por el theme del markdown).
local function render_item(item)
  if item.kind == "user" then
    return "**Tú:** " .. item.text
  elseif item.kind == "assistant" then
    return item.text
  elseif item.kind == "thinking" then
    -- bloque de razonamiento como cita (atenuado por el theme, chat.md §2).
    local quoted = item.text:gsub("\n", "\n> ")
    return "> *(razonando)* " .. quoted
  elseif item.kind == "tool" then
    local head = "`⚙ " .. item.name .. "`"
    if item.status == "running" then
      head = head .. " …"
    elseif item.status == "error" then
      head = head .. " ✗"
    else
      head = head .. " ✓"
    end
    if item.result and item.result ~= "" then
      head = head .. "\n> " .. item.result:gsub("\n", "\n> ")
    end
    return head
  elseif item.kind == "error" then
    return "> ⚠ " .. item.text:gsub("\n", "\n> ")
  elseif item.kind == "system" then
    return "*" .. item.text .. "*"
  end
  return item.text or ""
end

-- markdown() -> string. El texto MARKDOWN ACUMULADO de toda la conversación,
-- separando los items por una línea en blanco (párrafos markdown). Es lo que se
-- vuelca al widget `toolkit.text{markdown=true}` con `set_text`: el widget
-- re-renderiza el markdown (streaming-safe, S23) y crece incrementalmente con cada
-- delta. Reconstruir la cadena por cada delta es barato (concatenación de Lua); lo
-- caro —medir/renderizar el markdown— es primitiva Go en el widget (ADR-012/CP-9).
function Transcript:markdown()
  local parts = {}
  for _, item in ipairs(self.items) do
    parts[#parts + 1] = render_item(item)
  end
  return table.concat(parts, "\n\n")
end

-- count() -> integer: nº de items (para tests/inspección).
function Transcript:count()
  return #self.items
end

return M
