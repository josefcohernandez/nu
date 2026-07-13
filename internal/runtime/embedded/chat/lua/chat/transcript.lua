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
-- markdown. DESVIACIÓN documentada en docs/decisiones-implementacion.md (S43).
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

-- tool_progress(id, text) actualiza el PROGRESO en vivo de una tool en curso
-- (chat.md §2 / P27: evento `agent:tool.progress`). Busca por id; guarda el último
-- texto de progreso, que el render muestra mientras la tool está "running".
function Transcript:tool_progress(id, text)
  for i = #self.items, 1, -1 do
    local it = self.items[i]
    if it.kind == "tool" and it.id == id then
      it.progress = tostring(text or "")
      return it
    end
  end
end

-- add_compact_marker(summary?) inserta una marca visible de que la historia de
-- arriba fue compactada (chat.md §2 / P27: marca de `agent:compact`). Es un item
-- propio que el render pinta como una regla con una nota.
function Transcript:add_compact_marker()
  self.items[#self.items + 1] = { kind = "compact" }
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
-- args_summary(args) -> string. Un resumen COMPACTO de los argumentos de una tool,
-- para que el bloque diga QUÉ se va a ejecutar (no solo el nombre): `read_file` sin
-- más no informa; `read_file(path=main.go)` sí. Toma los valores escalares de la
-- tabla `args` (los más informativos: path, cmd, pattern…) y los une; recorta lo
-- largo. Sin args, "".
local function args_summary(args)
  if type(args) ~= "table" then return "" end
  -- Prioriza las claves más informativas; si no, toma las primeras escalares.
  local prefer = { "path", "file", "cmd", "command", "pattern", "query", "url", "name" }
  local parts = {}
  local seen = {}
  local function add(k, v)
    if seen[k] then return end
    if type(v) == "string" or type(v) == "number" or type(v) == "boolean" then
      local s = tostring(v):gsub("%s+", " ")
      if #s > 48 then s = s:sub(1, 47) .. "…" end
      parts[#parts + 1] = k .. "=" .. s
      seen[k] = true
    end
  end
  for _, k in ipairs(prefer) do
    if args[k] ~= nil then add(k, args[k]) end
  end
  if #parts == 0 then
    for k, v in pairs(args) do
      add(tostring(k), v)
      if #parts >= 2 then break end
    end
  end
  return table.concat(parts, " ")
end

local function render_item(item)
  if item.kind == "user" then
    -- El usuario, destacado: un gutter de acento (negrita) + el texto. El theme del
    -- markdown pinta el `**…**` con su color `strong` (brillante), separándolo del
    -- texto del asistente, que va plano.
    return "**▌ Tú** " .. item.text
  elseif item.kind == "assistant" then
    return item.text
  elseif item.kind == "thinking" then
    -- bloque de razonamiento como cita (atenuado por el theme, chat.md §2).
    local quoted = item.text:gsub("\n", "\n> ")
    return "> *razonando…* " .. quoted
  elseif item.kind == "tool" then
    -- Cabecera con nombre Y argumentos (chat.md §2): saber qué se ejecuta es
    -- funcionalidad, no adorno. El estado se marca con un glifo (⏵ en curso, ✓ ok,
    -- ✗ error); el theme colorea el code-span de la cabecera.
    local arg = args_summary(item.args)
    local label = item.name .. (arg ~= "" and ("  " .. arg) or "")
    local glyph = (item.status == "running" and "⏵")
      or (item.status == "error" and "✗") or "✓"
    local head = "`" .. glyph .. " " .. label .. "`"
    if item.status == "running" and item.progress and item.progress ~= "" then
      head = head .. " _" .. item.progress:gsub("\n", " ") .. "_"
    end
    if item.result and item.result ~= "" then
      -- resultado plegado si es largo: hasta 6 líneas, con nota del resto.
      local lines = {}
      for ln in (item.result .. "\n"):gmatch("(.-)\n") do lines[#lines + 1] = ln end
      local shown = lines
      local extra = 0
      if #lines > 6 then
        shown = {}
        for i = 1, 6 do shown[i] = lines[i] end
        extra = #lines - 6
      end
      head = head .. "\n> " .. table.concat(shown, "\n> ")
      if extra > 0 then
        head = head .. "\n> _… " .. extra .. " líneas más_"
      end
    end
    return head
  elseif item.kind == "error" then
    return "> ⚠ " .. item.text:gsub("\n", "\n> ")
  elseif item.kind == "system" then
    return "*" .. item.text .. "*"
  elseif item.kind == "compact" then
    -- marca de compactación (P27): una regla con una nota atenuada.
    return "---\n\n*🗜 Historia compactada arriba*\n\n---"
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
