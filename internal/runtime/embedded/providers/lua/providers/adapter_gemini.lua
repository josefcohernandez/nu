-- Adaptador `gemini` de la extensión `providers` (P30): el dialecto de la
-- **Generative Language API** de Google (`generativelanguage.googleapis.com`),
-- modelos Gemini. Tercer adaptador oficial embebido (providers.md §4), junto a
-- `anthropic` (S37) y `openai-compat` (P30).
--
-- Cumple el **contrato del adaptador** de
-- [providers.md](../../../../../docs/providers.md) §3: `name`, `caps`,
-- `stream(req, provider) -> iterator<Event>` (⏸) y `count_tokens?`. TRADUCE:
--
--   1. la petición CANÓNICA (providers.md §2.1) al cuerpo de
--      `:streamGenerateContent` (Gemini llama `contents` a los mensajes, `model`
--      al rol assistant, `parts` a los bloques, `functionCall`/`functionResponse`
--      a las tools, `systemInstruction` al system), y
--   2. el **SSE de Gemini** (`?alt=sse`: cada `data:` es un `GenerateContentResponse`
--      con `candidates[].content.parts[]`, `finishReason`, `usageMetadata`) al
--      **stream de Eventos CANÓNICO** de §2.3 (`text`, `tool_call.*`, `usage`,
--      `done`).
--
-- Todo sobre la API pública (api.md): `nu.http.stream` + `Stream:events()` (§8),
-- `nu.json` (§12), `error` estructurado (ADR-009). Lua puro (ADR-003).
--
-- DOS DIFERENCIAS que el traductor absorbe:
--   - Gemini identifica una llamada a función por su NOMBRE, no por un id (no hay
--     `tool_use_id`). El modelo canónico sí trae `id` en `tool_call`/`tool_result`
--     (providers.md §2.2); al traducir `functionResponse` recuperamos el nombre
--     mapeando el `id` del resultado al `tool_call` previo. Hacia el stream
--     canónico, sintetizamos un id estable (`call_<n>`) por cada `functionCall`.
--   - Gemini entrega cada `functionCall` ENTERO (args es un objeto, no JSON
--     troceado); emitimos `begin` + una `delta` con el args completo + `end`.

local providers = require("providers")

local M = {
  name = "gemini",
  caps = { tools = true, images = true, thinking = false, system = true, usage = true },
}

local function eprovider(message, detail)
  error({ code = "EPROVIDER", message = message, detail = detail })
end

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- ---------------------------------------------------------------------------
-- Traducción CANÓNICO -> dialecto Gemini (request).
-- ---------------------------------------------------------------------------

-- canon_to_parts(blocks, id_to_name) -> parts[]. Bloques canónicos (providers.md
-- §2.2) a `parts` de Gemini. `id_to_name` resuelve el nombre de función de un
-- `tool_result` (Gemini lo indexa por nombre, no por id).
local function canon_to_parts(blocks, id_to_name)
  local parts = {}
  for _, b in ipairs(blocks or {}) do
    if b.type == "text" then
      parts[#parts + 1] = { text = b.text or "" }
    elseif b.type == "image" then
      parts[#parts + 1] = { inlineData = { mimeType = b.media_type or "image/png",
        data = b.data_base64 or "" } }
    elseif b.type == "tool_call" then
      parts[#parts + 1] = { functionCall = { name = b.name, args = b.args or {} } }
    elseif b.type == "tool_result" then
      -- El texto del resultado (providers.md §2.2: el content es Block[]).
      local txt = ""
      for _, sub in ipairs(b.content or {}) do
        if sub.type == "text" then txt = txt .. (sub.text or "") end
      end
      parts[#parts + 1] = {
        functionResponse = {
          name = id_to_name[b.id] or b.id or "function",
          response = { result = txt },
        },
      }
    end
    -- bloques `thinking`: no se reenvían (Gemini no los reingiere).
  end
  return parts
end

-- to_wire(req, provider) -> tabla. Cuerpo de `:streamGenerateContent`. Mapea roles
-- (`assistant` -> `model`), arma `systemInstruction`, `tools.functionDeclarations`
-- y `generationConfig`. Construye antes el mapa id->nombre de las tool_calls del
-- assistant para resolver los `functionResponse`.
local function to_wire(req, provider)
  -- Mapa id -> nombre, recorriendo las tool_call del historial (providers.md §2.2).
  local id_to_name = {}
  for _, msg in ipairs(req.messages or {}) do
    for _, b in ipairs(msg.content or {}) do
      if b.type == "tool_call" and b.id ~= nil then
        id_to_name[b.id] = b.name
      end
    end
  end

  local contents = {}
  for _, msg in ipairs(req.messages or {}) do
    local role = (msg.role == "assistant") and "model" or "user"
    contents[#contents + 1] = { role = role, parts = canon_to_parts(msg.content, id_to_name) }
  end

  local body = { contents = contents }

  if type(req.system) == "string" and req.system ~= "" then
    body.systemInstruction = { parts = { { text = req.system } } }
  end

  if req.tools ~= nil and #req.tools > 0 then
    local decls = {}
    for _, tool in ipairs(req.tools) do
      decls[#decls + 1] = {
        name = tool.name,
        description = tool.description,
        parameters = tool.schema or { type = "object" },
      }
    end
    body.tools = { { functionDeclarations = decls } }
  end

  local gen = {}
  if type(req.max_tokens) == "number" then gen.maxOutputTokens = req.max_tokens end
  if type(req.temperature) == "number" then gen.temperature = req.temperature end
  if next(gen) ~= nil then body.generationConfig = gen end

  return body
end

-- ---------------------------------------------------------------------------
-- Traducción dialecto Gemini SSE -> stream CANÓNICO de Eventos (providers.md §2.3).
-- ---------------------------------------------------------------------------

-- map_finish_reason(reason) -> canónico. Gemini: STOP -> "end"; MAX_TOKENS ->
-- "max_tokens"; SAFETY/RECITATION/etc -> "refusal". Si hubo functionCall, el
-- stop final es "tool_calls" (se decide en el iterador, no aquí).
local function map_finish_reason(reason)
  if reason == "MAX_TOKENS" then
    return "max_tokens"
  elseif reason == "SAFETY" or reason == "RECITATION" or reason == "BLOCKLIST"
      or reason == "PROHIBITED_CONTENT" or reason == "SPII" then
    return "refusal"
  end
  return "end"
end

local function make_iterator(stream, provider)
  local sse = stream:events()

  local message = { role = "assistant", content = {} }
  local usage = { input_tokens = nil, output_tokens = nil }
  local stop_reason = "end"
  local text_acc = ""
  local saw_tool_call = false
  local call_n = 0
  local finished = false
  local done_emitted = false

  local pending = {}
  local function enqueue(ev) pending[#pending + 1] = ev end

  -- flush_text() cierra el bloque de texto acumulado y lo AÑADE AL FINAL del
  -- content, preservando el orden real de llegada de las parts (A-12). Gemini
  -- entrega el texto troceado en varias parts/eventos, que aquí se funden en un
  -- único bloque contiguo; se llama justo antes de cada functionCall (para que la
  -- tool_call quede DESPUÉS del texto que la precedió) y al cerrar el stream. Así
  -- un `[text, functionCall, text]` produce tres bloques en ese mismo orden, en
  -- vez de fundir todo el texto y anteponerlo a las tool_calls.
  local function flush_text()
    if text_acc ~= "" then
      message.content[#message.content + 1] = { type = "text", text = text_acc }
      text_acc = ""
    end
  end

  -- handle_part(part): traduce UNA `part` de un candidate a Events canónicos.
  local function handle_part(part)
    if type(part.text) == "string" and part.text ~= "" then
      text_acc = text_acc .. part.text
      enqueue({ type = "text", text = part.text })
    elseif type(part.functionCall) == "table" then
      -- Cierra el texto acumulado ANTES de la tool_call: preserva el orden real
      -- de llegada en el Message canónico (A-12), en vez de reordenar al final.
      flush_text()
      saw_tool_call = true
      call_n = call_n + 1
      local id = "call_" .. tostring(call_n)
      local name = part.functionCall.name
      local args = part.functionCall.args or {}
      -- Gemini da el functionCall entero: begin + delta (args completo) + end.
      enqueue({ type = "tool_call.begin", id = id, name = name })
      enqueue({ type = "tool_call.delta", id = id, args_json = nu.json.encode(args) })
      enqueue({ type = "tool_call.end", id = id })
      message.content[#message.content + 1] =
        { type = "tool_call", id = id, name = name, args = args }
    end
  end

  local function handle(evt)
    local data = evt.data
    if data == nil or data == "" then return end
    local ok, d = pcall(nu.json.decode, data)
    if not ok or type(d) ~= "table" then
      return
    end
    if type(d.error) == "table" then
      local status = d.error.status or d.error.code
      eprovider("gemini: " .. tostring(d.error.message or "error del proveedor"),
        { provider_code = d.error.status, retryable = false })
    end
    if type(d.candidates) == "table" then
      for _, cand in ipairs(d.candidates) do
        local content = cand.content or {}
        for _, part in ipairs(content.parts or {}) do
          handle_part(part)
        end
        if cand.finishReason ~= nil then
          stop_reason = map_finish_reason(cand.finishReason)
        end
      end
    end
    if type(d.usageMetadata) == "table" then
      usage.input_tokens = d.usageMetadata.promptTokenCount or usage.input_tokens
      usage.output_tokens = d.usageMetadata.candidatesTokenCount or usage.output_tokens
      enqueue({ type = "usage",
        input_tokens = usage.input_tokens, output_tokens = usage.output_tokens })
    end
  end

  return function()
    while true do
      if #pending > 0 then
        return table.remove(pending, 1)
      end
      if finished then
        if done_emitted then
          return nil
        end
        done_emitted = true
        flush_text()
        if saw_tool_call and stop_reason == "end" then
          stop_reason = "tool_calls"
        end
        return { type = "done", stop_reason = stop_reason, message = message }
      end
      local evt = sse()
      if evt == nil then
        finished = true
      else
        handle(evt)
      end
    end
  end
end

-- ---------------------------------------------------------------------------
-- Contrato §3: stream + count_tokens.
-- ---------------------------------------------------------------------------

-- model_path(model) -> string. Gemini pide el modelo en la RUTA, no en el body:
-- `/v1beta/models/<id>:streamGenerateContent`. Acepta tanto `gemini-2.5-pro`
-- como un `models/gemini-2.5-pro` ya prefijado.
local function model_path(model)
  if model:sub(1, 7) == "models/" then
    return model
  end
  return "models/" .. model
end

-- stream(req, provider) -> iterator<Event> ⏸ (providers.md §3). La clave va en la
-- cabecera `x-goog-api-key`; `?alt=sse` pide Server-Sent Events (sin él, Gemini
-- devuelve un array JSON, no un stream).
function M.stream(req, provider)
  if type(req) ~= "table" then
    einval("adaptador gemini: el request debe ser una tabla (providers.md §2.1)")
  end
  if type(req.model) ~= "string" or req.model == "" then
    einval("adaptador gemini: el request necesita `model` (providers.md §2.1)")
  end

  local headers = { ["content-type"] = "application/json" }
  if type(provider.api_key) == "string" and provider.api_key ~= "" then
    headers["x-goog-api-key"] = provider.api_key
  end

  local body = to_wire(req, provider)
  local url = provider.base_url .. "/v1beta/" .. model_path(req.model) ..
    ":streamGenerateContent?alt=sse"
  local stream = nu.http.stream({
    url = url,
    method = "POST",
    headers = headers,
    body = nu.json.encode(body),
  })

  if stream.status ~= nil and stream.status >= 400 then
    local msg = "gemini: HTTP " .. tostring(stream.status)
    local code = nil
    local ok_chunks, raw = pcall(function()
      local acc = ""
      for chunk in stream:chunks() do acc = acc .. chunk end
      return acc
    end)
    if ok_chunks and raw ~= "" then
      local okj, payload = pcall(nu.json.decode, raw)
      if okj and type(payload) == "table" and type(payload.error) == "table" then
        code = payload.error.status
        if type(payload.error.message) == "string" then
          msg = "gemini: " .. payload.error.message
        end
      end
    end
    stream:close()
    local retryable = (stream.status == 429 or stream.status >= 500)
    eprovider(msg, { status = stream.status, provider_code = code, retryable = retryable })
  end

  return make_iterator(stream, provider)
end

-- count_tokens(req, provider) -> integer ⏸ (providers.md §3, opcional). Heurística
-- `approx_tokens` (G23) sobre system + texto; Lua puro, sin red.
function M.count_tokens(req, provider)
  local total = 0
  local at = providers.approx_tokens
  if type(req.system) == "string" then
    total = total + at(req.system)
  end
  for _, msg in ipairs(req.messages or {}) do
    for _, block in ipairs(msg.content or {}) do
      if block.type == "text" then
        total = total + at(block.text)
      end
    end
  end
  return total
end

return M
