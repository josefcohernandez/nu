-- Adaptador `openai-compat` de la extensión `providers` (P30): el dialecto de la
-- **Chat Completions API** de OpenAI, compartido por todo el ecosistema
-- compatible (OpenAI, Together, Groq, OpenRouter, vLLM, Ollama en su modo
-- `/v1`...). Por eso el nombre es `openai-compat` y no `openai`: el mismo
-- traductor sirve a cualquier `base_url` que hable ese dialecto (providers.md §4).
--
-- Cumple el **contrato del adaptador** de
-- [providers.md](../../../../../docs/providers.md) §3, igual que `anthropic`
-- (S37): una tabla con `name`, `caps`, `stream(req, provider) -> iterator<Event>`
-- (⏸) y `count_tokens?`. TRADUCE en dos direcciones:
--
--   1. la petición CANÓNICA (providers.md §2.1) al cuerpo de `/chat/completions`
--      (mensajes con roles, `tools` de tipo `function`, `tool_calls` en el
--      assistant, mensajes `role="tool"` para los resultados), y
--   2. el **SSE de OpenAI** (`data: {choices:[{delta,finish_reason}], usage}` y el
--      centinela `data: [DONE]`) al **stream de Eventos CANÓNICO** de §2.3
--      (`text`, `tool_call.begin/delta/end`, `usage`, `done`).
--
-- Todo sobre la API pública (api.md, corolario de completitud): `enu.http.stream`
-- + `Stream:events()` (§8), `enu.json` (§12), `error` estructurado (ADR-009).
-- NINGÚN privilegio de kernel: Lua puro sobre la superficie congelada (ADR-003).
--
-- DIFERENCIA ESTRUCTURAL con Anthropic que el traductor absorbe (providers.md
-- §2.2): el modelo canónico mete los `tool_result` en UN mensaje de rol `user`;
-- OpenAI quiere CADA resultado en su propio mensaje `role="tool"`. Y un assistant
-- canónico con texto + varias `tool_call` se funde en UN mensaje con `content` +
-- `tool_calls[]`. `to_wire` aplana esa diferencia (un mensaje canónico puede
-- producir 0+ mensajes de OpenAI).

local providers = require("providers")

-- Capacidades del dialecto (providers.md §3 `caps`). `thinking = false`: el
-- razonamiento de los modelos `o*` no se expone como bloque canónico `thinking`
-- (es opaco), así que el adaptador no traduce `req.thinking` (no es un error:
-- la obligación 5 de §3 solo fuerza fallar ante `tools` no soportadas).
local M = {
  name = "openai-compat",
  caps = { tools = true, images = true, thinking = false, system = true, usage = true },
}

-- ---------------------------------------------------------------------------
-- Errores estructurados del adaptador (EPROVIDER, providers.md §3 / ADR-009).
-- ---------------------------------------------------------------------------

local function eprovider(message, detail)
  error({ code = "EPROVIDER", message = message, detail = detail })
end

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- ---------------------------------------------------------------------------
-- Traducción CANÓNICO -> dialecto OpenAI (request).
-- ---------------------------------------------------------------------------

-- text_of(blocks) -> string. Concatena el texto de los bloques `text` de un
-- contenido canónico (providers.md §2.2). OpenAI acepta `content` como string
-- para el assistant; lo usamos cuando no hay imágenes.
local function text_of(blocks)
  local t = ""
  for _, b in ipairs(blocks or {}) do
    if b.type == "text" then
      t = t .. (b.text or "")
    end
  end
  return t
end

-- user_content(blocks) -> string|tabla. El `content` de un mensaje de usuario:
-- string si solo hay texto, o un array de partes `{type="text"|"image_url"}` si
-- hay imágenes (providers.md §2.2 -> visión de OpenAI: `image_url` con data URL).
local function user_content(blocks)
  local has_image = false
  for _, b in ipairs(blocks or {}) do
    if b.type == "image" then has_image = true break end
  end
  if not has_image then
    return text_of(blocks)
  end
  local parts = {}
  for _, b in ipairs(blocks or {}) do
    if b.type == "text" then
      parts[#parts + 1] = { type = "text", text = b.text or "" }
    elseif b.type == "image" then
      local url = "data:" .. tostring(b.media_type or "image/png") ..
        ";base64," .. tostring(b.data_base64 or "")
      parts[#parts + 1] = { type = "image_url", image_url = { url = url } }
    end
  end
  return parts
end

-- push_canonical_message(out, msg) traduce UN mensaje canónico (providers.md
-- §2.2) a 0+ mensajes de la Chat Completions API y los anexa a `out`. Aplana las
-- dos diferencias estructurales con el modelo canónico (ver cabecera).
local function push_canonical_message(out, msg)
  local role = msg.role
  local blocks = msg.content or {}

  if role == "assistant" then
    -- Texto + tool_calls en UN mensaje (OpenAI: `content` + `tool_calls[]`).
    -- Los bloques `thinking` no se reenvían (OpenAI no los reingiere).
    local tool_calls = {}
    for _, b in ipairs(blocks) do
      if b.type == "tool_call" then
        tool_calls[#tool_calls + 1] = {
          id = b.id,
          type = "function",
          ["function"] = { name = b.name, arguments = enu.json.encode(b.args or {}) },
        }
      end
    end
    local m = { role = "assistant", content = text_of(blocks) }
    if #tool_calls > 0 then
      m.tool_calls = tool_calls
    end
    out[#out + 1] = m
    return
  end

  -- role == "user": los tool_result van CADA UNO a un mensaje `role="tool"`
  -- (OpenAI), el resto (texto/imagen) a un mensaje `role="user"`.
  local user_blocks = {}
  for _, b in ipairs(blocks) do
    if b.type == "tool_result" then
      out[#out + 1] = {
        role = "tool",
        tool_call_id = b.id,
        content = text_of(b.content),
      }
    else
      user_blocks[#user_blocks + 1] = b
    end
  end
  if #user_blocks > 0 then
    out[#out + 1] = { role = "user", content = user_content(user_blocks) }
  end
end

-- to_wire(req, provider) -> tabla. Cuerpo de `/chat/completions` desde el Request
-- canónico (providers.md §2.1). El `system` canónico es un mensaje `role="system"`
-- al frente. `stream_options.include_usage` pide el bloque `usage` final (sin él
-- OpenAI no manda tokens en streaming).
local function to_wire(req, provider)
  local messages = {}
  if type(req.system) == "string" and req.system ~= "" then
    messages[#messages + 1] = { role = "system", content = req.system }
  end
  for _, msg in ipairs(req.messages or {}) do
    push_canonical_message(messages, msg)
  end

  local body = {
    model = req.model,
    messages = messages,
    stream = true,
    stream_options = { include_usage = true },
  }
  if type(req.max_tokens) == "number" then
    body.max_tokens = req.max_tokens
  end
  if type(req.temperature) == "number" then
    body.temperature = req.temperature
  end
  if req.tools ~= nil and #req.tools > 0 then
    local tools = {}
    for _, tool in ipairs(req.tools) do
      tools[#tools + 1] = {
        type = "function",
        ["function"] = {
          name = tool.name,
          description = tool.description,
          parameters = tool.schema or { type = "object" },
        },
      }
    end
    body.tools = tools
  end
  return body
end

-- auth_headers(provider) -> tabla. Bearer + content-type (el estándar de OpenAI
-- y compatibles). Sin clave (p. ej. un Ollama local) no se manda Authorization.
local function auth_headers(provider)
  local h = { ["content-type"] = "application/json" }
  if type(provider.api_key) == "string" and provider.api_key ~= "" then
    h["authorization"] = "Bearer " .. provider.api_key
  end
  return h
end

-- ---------------------------------------------------------------------------
-- Traducción dialecto OpenAI SSE -> stream CANÓNICO de Eventos (providers.md §2.3).
-- ---------------------------------------------------------------------------

-- map_finish_reason(reason) -> canónico (providers.md §2.3 `done`).
local function map_finish_reason(reason)
  if reason == "tool_calls" or reason == "function_call" then
    return "tool_calls"
  elseif reason == "length" then
    return "max_tokens"
  elseif reason == "content_filter" then
    return "refusal"
  end
  return "end" -- "stop", nil, desconocido
end

-- make_iterator(stream, provider) -> función iteradora de Events. Consume el SSE
-- de OpenAI con `stream:events()` (api.md §8). Mantiene la máquina de estados del
-- mensaje: el texto se acumula; las `tool_calls` llegan troceadas por `index`
-- (id+name en el primer fragmento, `arguments` en JSON troceado después), igual
-- patrón que el `input_json_delta` de Anthropic. Cierra con `done` + Message.
local function make_iterator(stream, provider)
  local sse = stream:events()

  local message = { role = "assistant", content = {} }
  local usage = { input_tokens = nil, output_tokens = nil }
  local stop_reason = "end"
  local text_acc = ""
  -- tool_calls en construcción por índice: { id, name, json_acc, begun }.
  local tcalls = {}
  local order = {}            -- índices vistos, en orden de aparición
  local finished = false
  local done_emitted = false

  local pending = {}
  local function enqueue(ev) pending[#pending + 1] = ev end

  -- finalize() ensambla `message.content` (providers.md §2.1) al cerrar: el
  -- bloque de texto (si lo hubo) y cada tool_call con su JSON decodificado,
  -- emitiendo además los `tool_call.end` pendientes.
  local function finalize()
    if text_acc ~= "" then
      message.content[#message.content + 1] = { type = "text", text = text_acc }
    end
    for _, idx in ipairs(order) do
      local tc = tcalls[idx]
      if tc ~= nil then
        local args = {}
        if tc.json_acc ~= nil and tc.json_acc ~= "" then
          local ok, decoded = pcall(enu.json.decode, tc.json_acc)
          if ok and type(decoded) == "table" then
            args = decoded
          end
        end
        message.content[#message.content + 1] =
          { type = "tool_call", id = tc.id, name = tc.name, args = args }
        enqueue({ type = "tool_call.end", id = tc.id })
      end
    end
  end

  -- handle_choice(choice) traduce el `delta` de un choice a Events canónicos.
  local function handle_choice(choice)
    local delta = choice.delta or {}
    if type(delta.content) == "string" and delta.content ~= "" then
      text_acc = text_acc .. delta.content
      enqueue({ type = "text", text = delta.content })
    end
    if type(delta.tool_calls) == "table" then
      for _, tc in ipairs(delta.tool_calls) do
        -- `index` ubica el tool_call (OpenAI lo trocea por índice). Si falta,
        -- usamos el orden de llegada.
        local idx = tc.index or (#order)
        local cur = tcalls[idx]
        if cur == nil then
          cur = { id = tc.id, name = nil, json_acc = "", begun = false }
          tcalls[idx] = cur
          order[#order + 1] = idx
        end
        if tc.id ~= nil then cur.id = tc.id end
        local fn = tc["function"] or {}
        if fn.name ~= nil then cur.name = fn.name end
        -- Emite `tool_call.begin` en cuanto haya id y name (providers.md §2.3).
        if not cur.begun and cur.id ~= nil and cur.name ~= nil then
          cur.begun = true
          enqueue({ type = "tool_call.begin", id = cur.id, name = cur.name })
        end
        if type(fn.arguments) == "string" and fn.arguments ~= "" then
          cur.json_acc = cur.json_acc .. fn.arguments
          if cur.begun then
            enqueue({ type = "tool_call.delta", id = cur.id, args_json = fn.arguments })
          end
        end
      end
    end
    if choice.finish_reason ~= nil then
      stop_reason = map_finish_reason(choice.finish_reason)
    end
  end

  -- handle(evt): traduce UN evento SSE de OpenAI. `evt.data` es el JSON del chunk
  -- o el centinela `[DONE]`.
  local function handle(evt)
    local data = evt.data
    if data == nil then return end
    if data == "[DONE]" then
      finished = true
      return
    end
    local ok, d = pcall(enu.json.decode, data)
    if not ok or type(d) ~= "table" then
      return -- chunk no decodificable: robustez (como un comentario SSE)
    end
    -- Error en banda (algunos gateways compatibles lo mandan como chunk).
    if type(d.error) == "table" then
      local code = d.error.type or d.error.code
      eprovider("openai-compat: " .. tostring(d.error.message or "error del proveedor"),
        { provider_code = code, retryable = false })
    end
    if type(d.choices) == "table" then
      for _, choice in ipairs(d.choices) do
        handle_choice(choice)
      end
    end
    -- `usage` llega en el chunk final (con stream_options.include_usage), a veces
    -- con `choices` vacío.
    if type(d.usage) == "table" then
      usage.input_tokens = d.usage.prompt_tokens or usage.input_tokens
      usage.output_tokens = d.usage.completion_tokens or usage.output_tokens
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
        -- Ensambla el Message una sola vez, justo antes del primer intento de
        -- cerrar (drena los tool_call.end que finalize encola y luego el done).
        if not message._finalized then
          message._finalized = true
          finalize()
          if #pending > 0 then
            return table.remove(pending, 1)
          end
        end
        done_emitted = true
        message._finalized = nil
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

-- stream(req, provider) -> iterator<Event> ⏸ (providers.md §3). Traduce el
-- request, abre `enu.http.stream` (⏸) a `/chat/completions`, comprueba el status
-- (>=400 -> EPROVIDER accionable; 429 y 5xx retryables) y devuelve el iterador.
function M.stream(req, provider)
  if type(req) ~= "table" then
    einval("adaptador openai-compat: el request debe ser una tabla (providers.md §2.1)")
  end
  if type(req.model) ~= "string" or req.model == "" then
    einval("adaptador openai-compat: el request necesita `model` (providers.md §2.1)")
  end

  local body = to_wire(req, provider)
  local stream = enu.http.stream({
    url = provider.base_url .. "/chat/completions",
    method = "POST",
    headers = auth_headers(provider),
    body = enu.json.encode(body),
  })

  if stream.status ~= nil and stream.status >= 400 then
    local msg = "openai-compat: HTTP " .. tostring(stream.status)
    local code = nil
    local ok_chunks, raw = pcall(function()
      local acc = ""
      for chunk in stream:chunks() do acc = acc .. chunk end
      return acc
    end)
    if ok_chunks and raw ~= "" then
      local okj, payload = pcall(enu.json.decode, raw)
      if okj and type(payload) == "table" and type(payload.error) == "table" then
        code = payload.error.type or payload.error.code
        if type(payload.error.message) == "string" then
          msg = "openai-compat: " .. payload.error.message
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
-- de la extensión (`approx_tokens`, G23) sobre system + bloques de texto: Lua puro,
-- sin red. La fuente de verdad es el `usage` del propio turno (providers.md §5).
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
