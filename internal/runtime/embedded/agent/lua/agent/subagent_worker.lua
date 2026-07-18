-- Bootstrap del LOOP de un subagente-worker (S40, agente.md §9).
--
-- Este módulo es el `module` que `enu.worker.spawn` carga DENTRO del worker para
-- correr el turno de un subagente AISLADO (agente.md §9: "el loop corre en un
-- worker, con caps recortadas; los handlers de tools se ejecutan en el estado
-- principal vía proxy de mensajes"). Es, por tanto, código que corre BAJO LAS CAPS
-- RECORTADAS del worker: la superficie no concedida NO EXISTE aquí (p. ej.
-- `enu.fs.write`, `enu.ui`, `enu.events` —el bus principal no cruza, api.md §13/§16—).
-- El subagente es HEADLESS por construcción.
--
-- PROTOCOLO con el padre (por `enu.worker.parent`, api.md §13, mensajes JSON-ables):
--   1. el padre manda `{ kind="init", model, system, thinking, prompt, tool_defs,
--      adapters, max_turns, max_tokens, temperature }` —`system` ya trae el índice
--      de skills y `thinking` ya está resuelto (A-22): el worker no re-descubre—;
--   2. el worker ensambla el request canónico (agente.md §7, con `thinking`) y
--      consume el stream del adaptador (providers.md §2.3) hasta el `done`;
--   3. si el modelo pidió tools, el worker NO las ejecuta: por cada tool_call manda
--      `{ kind="tool_call", id, name, args }` al padre y espera
--      `{ kind="tool_result", result }` —el padre la corrió por su pipeline
--      (permisos/hooks/handler) en el estado principal—; anexa el resultado y
--      RE-PIDE (vuelve al paso 2);
--   4. al terminar (modelo para sin tools, o se agota max_turns), manda el DIGESTO
--      `{ kind="done", digest = { text, message, stop_reason, usage, turns } }`.
--   * PERSISTENCIA (A-21, sesiones.md §7): el worker NO tiene `fs.write` (caps
--     FS_RO), así que no persiste ni toca el lock. Manda cada Message conforme
--     avanza con `{ kind="message", message, usage?, model? }` (el prompt del
--     usuario, el assistant de cada turno con su usage/model, y los tool_result
--     como mensaje de usuario); el PADRE los anexa al transcript hijo, en orden y
--     con la misma forma que el modo task. El padre no responde a `message`.
--   Cualquier fallo se manda como `{ kind="error", message }` para que el padre lo
--   reporte como EAGENT (en vez de quedarse colgado esperando un digesto).
--
-- ¿POR QUÉ REGISTRAR ADAPTADORES AQUÍ? El `init.lua` de `providers` (que registra
-- los oficiales) NO corre dentro del worker —un worker solo ejecuta `require(module)`
-- (worker.go); no hay ciclo de vida de plugins, §13—. Así que el registro vivo de
-- adaptadores de `providers` arranca VACÍO en el worker; este bootstrap lo rellena
-- requiriendo los módulos de adaptador que el padre nombró en `init.adapters` (los
-- oficiales son require-ables: `providers.adapter_anthropic`). Es re-ejecutar lo que
-- haría init.lua, sin privilegio: Lua puro sobre la API pública.
--
-- ADR-003: Lua puro sobre la API pública [W] (api.md §16: task/json/toml/fs/...)
-- + el módulo `providers`. Sin privilegio de kernel.

local providers = require("providers")

-- Cada adaptador oficial expone un `name`; se registra bajo ese name para que
-- `providers.resolve` (que mira `provider.adapter` del providers.toml) lo encuentre.
local function register_adapters(modules)
  for _, modname in ipairs(modules or {}) do
    local ok, mod = pcall(require, modname)
    if ok and type(mod) == "table" and type(mod.name) == "string" then
      providers.register_adapter(mod.name, mod)
    end
    -- Un módulo de adaptador que no resuelve no es fatal aquí: si NINGUNO casa con el
    -- provider del modelo, `providers.resolve` fallará accionable más abajo (y se
    -- reporta como error al padre).
  end
end

-- text_of(message) -> string. El texto plano del mensaje final (para el digesto).
local function text_of(message)
  if type(message) ~= "table" or type(message.content) ~= "table" then
    return ""
  end
  local parts = {}
  for _, block in ipairs(message.content) do
    if block.type == "text" and type(block.text) == "string" then
      parts[#parts + 1] = block.text
    end
  end
  return table.concat(parts, "")
end

-- consume_stream(iter) -> done. Consume el iterador de Events del adaptador
-- (providers.md §2.3) y devuelve el `done` (con stop_reason y el Message ensamblado)
-- y el último `usage`. El subagente-worker es HEADLESS: no hay bus de eventos
-- (`enu.events` no existe en el worker, §16), así que los deltas se descartan —el
-- padre solo recibe el DIGESTO, no el stream crudo (agente.md §9)—.
local function consume_stream(iter)
  local done, usage = nil, nil
  for ev in iter do
    if ev.type == "done" then
      done = ev
    elseif ev.type == "usage" then
      usage = ev
    end
    -- text/thinking/tool_call.*: se descartan (headless, sin UI; sin stream crudo).
  end
  if done == nil then
    error({ code = "EAGENT", message = "el adaptador cerró el stream sin un evento `done`" })
  end
  return done, usage
end

-- stream_with_retry(adapter, request, config, max_retries, retry_base_ms) -> iter.
-- Misma política que el motor del estado principal (G42, agente.md §2): reintenta
-- SOLO la apertura del stream ante un error tabla con `detail.retryable == true`,
-- con backoff exponencial `retry_base_ms · 2^(intento−1)`, hasta `max_retries`. La
-- ÚNICA diferencia con el padre: SIN evento `agent:retry` — los workers no tienen
-- bus (`enu.events` no existe aquí, ADR-004/§16). Agotados los reintentos o error no
-- retryable / no tabla, relanza tal cual (el padre lo reporta como error al recv).
-- El consumo del stream queda FUERA (un fallo a mitad de stream no se reintenta).
local function stream_with_retry(adapter, request, config, max_retries, retry_base_ms)
  local attempt = 0
  while true do
    local ok, iter = pcall(adapter.stream, request, config)
    if ok then
      return iter
    end
    local err = iter
    local retryable = type(err) == "table" and type(err.detail) == "table"
      and err.detail.retryable == true
    if not retryable or attempt >= max_retries then
      error(err) -- relanza tal cual: preserva la tabla estructurada
    end
    attempt = attempt + 1
    enu.task.sleep(retry_base_ms * (2 ^ (attempt - 1)))
  end
end

-- run_turn(init) -> digest. El LOOP del subagente, idéntico en forma al de S39
-- (Session:send) pero con la ejecución de tools DELEGADA al padre por mensajes.
local function run_turn(init)
  local resolved = providers.resolve(init.model)
  local adapter = resolved.adapter
  local config = resolved.config
  -- Reintentos de la apertura del stream heredados del padre (G42), con los mismos
  -- defaults que el motor (agente.md §2): 3 reintentos, base 1 s.
  local max_retries = init.max_retries or 3
  local retry_base_ms = init.retry_base_ms or 1000

  -- persist(message, usage, model): manda un Message al padre para que lo anexe al
  -- transcript hijo (A-21). El worker no persiste directamente (sin `fs.write`); el
  -- padre —que tiene el lock (§6)— es el único escritor. Fire-and-forget: el padre
  -- no responde (no es un tool_call).
  local function persist(message, usage, model)
    enu.worker.parent.send({ kind = "message", message = message, usage = usage, model = model })
  end

  -- Historial en memoria del subagente. Su transcript PROPIO se persiste en el
  -- padre (agente.md §9 / sesiones.md §7): por eso cada Message que entra al
  -- historial se manda también con `persist` para que el padre lo anexe. Arranca
  -- con el prompt del usuario (que también se persiste, igual que el modo task).
  local history = {}
  local prompt = init.prompt
  if type(prompt) == "string" then
    history[1] = { role = "user", content = { { type = "text", text = prompt } } }
  elseif type(prompt) == "table" then
    history[1] = { role = "user", content = prompt }
  end
  if history[1] then persist(history[1]) end

  local max_turns = init.max_turns or 32
  local final_message, last_usage, last_stop = nil, nil, nil
  local turns = 0

  while true do
    turns = turns + 1
    if turns > max_turns then
      error({ code = "EAGENT", message = "se agotó max_turns en el subagente", detail = { reason = "max_turns" } })
    end

    local request = {
      model       = config.model.id,
      system      = init.system,
      messages    = history,
      tools       = (init.tool_defs and #init.tool_defs > 0) and init.tool_defs or nil,
      max_tokens  = init.max_tokens,
      temperature = init.temperature,
      -- Control de razonamiento (providers.md §2.1, ADR-016): el padre ya resolvió
      -- el `thinking` de la sesión hija (A-22) y lo mandó en el init; el adaptador
      -- lo traduce por-modelo. nil = sin razonamiento.
      thinking    = init.thinking,
    }

    local iter = stream_with_retry(adapter, request, config, max_retries, retry_base_ms)
    local done, usage = consume_stream(iter)
    local assistant = done.message
    history[#history + 1] = assistant
    final_message, last_usage, last_stop = assistant, usage, done.stop_reason
    -- Persiste el assistant con su usage/model (A-21), igual que el modo task
    -- (init.lua §2 paso 4): coste y llenado de contexto auditables en el JSONL hijo.
    persist(assistant, usage, init.model)

    if done.stop_reason ~= "tool_calls" then
      break
    end

    -- Tools: el worker NO las ejecuta. Por cada tool_call, proxy al padre y espera
    -- su tool_result (agente.md §9: handlers en el estado principal). EN ORDEN (P12).
    local results = {}
    for _, block in ipairs(assistant.content) do
      if block.type == "tool_call" then
        enu.worker.parent.send({
          kind = "tool_call", id = block.id, name = block.name, args = block.args,
        })
        local reply = enu.worker.parent.recv()
        if reply == nil then
          error({ code = "EAGENT", message = "el padre cerró el canal sin devolver el tool_result" })
        end
        results[#results + 1] = reply.result
      end
    end
    if #results == 0 then
      break -- stop_reason=tool_calls sin bloques tool_call: no hacer loop vacío.
    end
    local tool_message = { role = "user", content = results }
    history[#history + 1] = tool_message
    -- Persiste los tool_result como mensaje de usuario (A-21), igual que el modo task.
    persist(tool_message)
  end

  return {
    text        = text_of(final_message),
    message     = final_message,
    -- El motivo de parada REAL del último done (providers.md §2.3): el padre
    -- distingue así un final normal de un max_tokens o un refusal.
    stop_reason = last_stop,
    usage       = last_usage,
    turns       = turns,
  }
end

-- Cuerpo del worker (corre como task, así que puede ⏸): recibe el init, corre el
-- turno y devuelve el digesto. Un fallo se reporta como `error` para no colgar al
-- padre (que está en `w:recv()` esperando o un tool_result o el digesto).
local init = enu.worker.parent.recv()
if init == nil or init.kind ~= "init" then
  enu.worker.parent.send({ kind = "error", message = "primer mensaje no es un init" })
  return
end

register_adapters(init.adapters)

local ok, digest_or_err = pcall(run_turn, init)
if ok then
  enu.worker.parent.send({ kind = "done", digest = digest_or_err })
else
  local message = (type(digest_or_err) == "table" and digest_or_err.message) or tostring(digest_or_err)
  enu.worker.parent.send({ kind = "error", message = message })
end
