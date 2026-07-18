-- Subagentes de la extensión `agent` (S40, agente.md §9).
--
-- Un **subagente** es un agente que corre AISLADO y devuelve a su padre un
-- RESULTADO DIGERIDO (no el stream crudo, sino un resumen estructurado que el
-- padre integra como un tool_result o un mensaje). Dos modos (agente.md §9):
--
--   - `worker = false` (defecto): el subagente corre como una TASK en el estado
--     principal —comparte el registro de tools, los permisos y los hooks; barato—.
--     Es, literalmente, una `agent.session` hija con `meta.parent` cuyo turno se
--     ejecuta con el motor de S39.
--   - `worker = true`: el **loop** corre en un `enu.worker` (api.md §13) con `caps`
--     RECORTADAS (G6/S34) —paralelismo real y la versión DURA del aislamiento: la
--     superficie no concedida NO EXISTE dentro del worker (p. ej. sin `fs.write`
--     ni `ui`)—. Pero los **handlers de tools se ejecutan en el estado principal
--     vía proxy de mensajes** (agente.md §9): cuando el turno del worker necesita
--     una tool, manda un mensaje al padre, el padre la corre por su pipeline
--     completo (permisos → hooks → handler) y le devuelve el resultado. Así:
--       * un solo registro de tools (ninguna se duplica "en versión worker");
--       * la seguridad queda centralizada en el padre (el worker no puede saltarse
--         permisos/hooks porque la ejecución nunca ocurre en su lado);
--       * DOS vallas para dos riesgos (agente.md §9): los *permisos* heredados
--         limitan QUÉ tools usa el subagente; las *caps* limitan QUÉ hace su código
--         Lua directamente.
--
-- DOS VALLAS, en detalle:
--   1. **caps** (worker, G6): qué API tiene el código Lua del worker. La extensión
--      ofrece paquetes con nombre INSPECCIONABLES (`agent.caps.FS_RO = {...}`,
--      definidos en `agent/init.lua` §9): el vocabulario vive en la extensión, el
--      mecanismo (sandbox) en el core. Por defecto un subagente-worker arranca en
--      solo-lectura (FS_RO + SEARCH): lee y busca, no escribe ni toca la red.
--   2. **permisos** (padre): el subagente HEREDA los del padre, RECORTADOS por sus
--      `opts.permissions` (nunca ampliados, agente.md §9/§11). Como las tools
--      corren en el padre, su pipeline de permisos (§5) es la valla efectiva.
--
-- DIGESTO (agente.md §9): `Sub:run(prompt)` no devuelve el stream; devuelve un
-- Message DIGERIDO —el mensaje final del subagente (texto + stop_reason + usage)—.
-- En modo worker cruza la frontera como valor JSON-able (api.md §13: copiado, sin
-- closures/Blocks): el worker manda datos digeridos, el padre los integra.
--
-- ADR-003: Lua puro sobre la API pública (api.md §13 enu.worker + enu.task + enu.json)
-- + las extensiones providers/sessions. Sin privilegio de kernel.

local M = {}

local function eagent(message, detail)
  error({ code = "EAGENT", message = message, detail = detail })
end

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- digest_of(message, usage, stop_reason, turns) -> tabla. Reduce el resultado de
-- un turno a su DIGESTO (agente.md §9): el texto del mensaje final, su stop_reason,
-- el usage y el número de vueltas. Es JSON-able a propósito (cruza la frontera del
-- worker sin Blocks/closures, api.md §13). El `message` completo se conserva (es
-- JSON-able: roles + bloques de texto/tool_*); el `text` plano es el atajo que el
-- padre suele integrar como un tool_result.
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

local function digest_of(message, usage, stop_reason, turns)
  return {
    text        = text_of(message),
    message     = message,
    stop_reason = stop_reason,
    usage       = usage,
    turns       = turns,
  }
end

-- =============================================================================
-- Construcción del módulo: `attach(agent)` lo cablea sobre el módulo `agent` ya
-- creado (agent/init.lua). Necesita de él: `session` (modo task), `run_tool_proxy`
-- (correr una tool por su pipeline para el proxy del worker), `caps` (paquetes con
-- nombre) y `emit` (eventos). Se inyecta para no crear un require circular.
-- =============================================================================

-- caps por defecto de un subagente-worker (agente.md §9 / §5 amortiguador 1):
-- SOLO LECTURA. Lee y busca; ni escribe en disco ni toca la red. El usuario sube
-- el listón con `opts.caps` (otra tabla `agent.caps.*` o una lista a mano).
local function default_worker_caps(agent)
  local caps = {}
  for _, c in ipairs(agent.caps.FS_RO) do caps[#caps + 1] = c end
  for _, c in ipairs(agent.caps.SEARCH) do caps[#caps + 1] = c end
  -- enu.task y enu.json son necesarios DENTRO del worker para correr el loop y
  -- serializar el digesto/los mensajes del proxy. Sin caps explícitas el worker
  -- tendría toda la API [W]; con caps recortadas hay que incluir lo que el loop usa.
  caps[#caps + 1] = "task"
  caps[#caps + 1] = "json"
  -- providers.resolve lee providers.toml del disco: necesita toml + config.dir.
  caps[#caps + 1] = "toml"
  caps[#caps + 1] = "config.dir"
  caps[#caps + 1] = "log"
  return caps
end

-- normalize_caps(opts_caps, agent) -> string[]. Acepta:
--   - nil           → solo-lectura por defecto (default_worker_caps).
--   - una lista de strings (un paquete `agent.caps.*` ES eso, o una a mano).
-- Siempre se AÑADEN las caps mínimas del loop (task/json/toml/config.dir/log) si
-- el llamante dio una lista que las omite: sin ellas el worker no podría ni correr
-- su turno ni devolver el digesto. No se amplía la superficie de fs/net del usuario.
local function normalize_caps(opts_caps, agent)
  if opts_caps == nil then
    return default_worker_caps(agent)
  end
  if type(opts_caps) ~= "table" then
    einval("Session:spawn: opts.caps debe ser una lista de capacidades (string[]) o nil")
  end
  local seen = {}
  local out = {}
  local function add(c)
    if type(c) == "string" and c ~= "" and not seen[c] then
      seen[c] = true
      out[#out + 1] = c
    end
  end
  for _, c in ipairs(opts_caps) do add(c) end
  -- Mínimos del loop (siempre): el worker debe poder orquestar (task), serializar
  -- (json) y resolver el modelo —`providers.resolve` lee `providers.toml` del disco
  -- con `enu.fs.read`+`enu.toml.decode` desde `enu.config.dir`—; `log` para diagnósticos.
  add("task"); add("json"); add("toml"); add("config.dir"); add("log"); add("fs.read")
  return out
end

function M.attach(agent)
  local Sub = {}
  Sub.__index = Sub

  -- M.default_caps() -> string[]: las caps con que arranca un subagente-worker sin
  -- `opts.caps` (solo-lectura + mínimos del loop). Inspeccionable para tests/tooling
  -- (agente.md §9: los paquetes de caps son tablas Lua normales).
  function M.default_caps()
    return default_worker_caps(agent)
  end

  -- M.normalize_caps(opts_caps): expuesta para tests/tooling (valida y completa).
  function M.normalize_caps(opts_caps)
    return normalize_caps(opts_caps, agent)
  end

  -- ---------------------------------------------------------------------------
  -- Modo TASK (worker = false, agente.md §9): el subagente es una sesión hija.
  -- ---------------------------------------------------------------------------

  -- spawn_task(parent, opts) -> Sub. Crea una `agent.session` hija (meta.parent del
  -- transcript, sesiones.md §7) que comparte tools/permisos/hooks del proceso. El
  -- turno corre con el motor de S39 en el estado principal.
  local function spawn_task(parent, opts)
    local child_opts = {}
    for k, v in pairs(opts) do child_opts[k] = v end
    child_opts.worker = nil
    child_opts.caps   = nil
    child_opts.parent = parent.id
    -- Permisos del subagente: heredan los del padre RECORTADOS por opts.permissions
    -- (agente.md §9: nunca ampliados). La sesión hija ya recorta por su cuenta; el
    -- recorte explícito vive en opts.permissions que el llamante pasó.
    local child = agent.session(child_opts)
    return setmetatable({
      mode    = "task",
      child   = child,
      parent  = parent,
      closed  = false,
    }, Sub)
  end

  -- ---------------------------------------------------------------------------
  -- Modo WORKER (worker = true, agente.md §9): el loop corre aislado en un worker
  -- con caps recortadas; las tools se proxyean al padre.
  -- ---------------------------------------------------------------------------

  -- spawn_worker(parent, opts) -> Sub. Levanta el worker pero NO corre aún el turno
  -- (eso es `Sub:run`). El worker carga el módulo `agent.subagent_worker` (el
  -- bootstrap del loop aislado). La sesión hija de PERSISTENCIA (transcript con
  -- meta.parent) vive en el PADRE: el worker manda los mensajes y el padre los
  -- persiste —el worker no tiene por qué tocar el lock (sesiones.md §6)—.
  local function spawn_worker(parent, opts)
    local caps = normalize_caps(opts.caps, agent)
    -- Adaptadores que el worker debe registrar (init.lua NO corre dentro del worker,
    -- así que el registro vivo de adaptadores de providers está VACÍO allí). Por
    -- defecto los oficiales require-ables; el llamante puede pasar otros módulos
    -- (los tests inyectan un stub require-able). Son NOMBRES DE MÓDULO (require).
    local adapter_modules = opts.adapter_modules or { "providers.adapter_anthropic" }

    local w = enu.worker.spawn("agent.subagent_worker", { caps = caps })

    return setmetatable({
      mode            = "worker",
      worker          = w,
      parent          = parent,
      opts            = opts,
      adapter_modules = adapter_modules,
      closed          = false,
      started         = false,
    }, Sub)
  end

  -- run_worker(self, prompt) ⏸ -> digest. Conduce el turno del subagente-worker:
  -- manda el init (modelo, system, prompt, defs de tools, adaptadores) y entra en
  -- un bucle de PROXY: cada mensaje del worker es o una petición de tool (que el
  -- PADRE ejecuta por su pipeline y responde) o el digesto final. Devuelve el
  -- digesto, que el padre integra (agente.md §9).
  local function run_worker(self, prompt)
    local w = self.worker

    -- tools_for_request: las defs (name/description/schema) que el worker pasa al
    -- provider. El padre las calcula (tiene el registro) y se las manda: el worker
    -- NO necesita el registro de tools, solo sus DEFINICIONES (JSON-ables).
    local tool_defs = agent.tools()
    if type(self.opts.tools) == "table" then
      local allow = {}
      for _, t in ipairs(self.opts.tools) do allow[t] = true end
      local filtered = {}
      for _, d in ipairs(tool_defs) do
        if allow[d.name] then filtered[#filtered + 1] = d end
      end
      tool_defs = filtered
    end

    -- Sesión hija de persistencia en el PADRE (transcript con meta.parent, A-21).
    -- El worker no tiene `fs.write` (caps FS_RO por defecto): NO puede persistir ni
    -- tocar el lock (sesiones.md §6). Así que manda cada mensaje al padre con un
    -- `{ kind="message" }` y el padre —que ya tiene el lock de la `proxy_session`—
    -- lo anexa al transcript hijo (bucle de proxy, abajo). Se omite si el padre es
    -- no_store (subagente in-memory para tests): la proxy_session no tiene store.
    --
    -- `system` y `thinking` los aporta la `proxy_session` (que ES la sesión hija con
    -- todo resuelto igual que el modo task, A-22): su `_assemble_system` ya inyecta
    -- el ÍNDICE DE SKILLS (filtrado por `opts.skills`, gated por confianza) sobre el
    -- system base; su `thinking` ya aplica la precedencia opts < agent.toml. El
    -- worker no re-descubre nada: recibe el system y el thinking ya cocinados.
    local proxy = self.proxy_session
    w:send({
      kind        = "init",
      model       = self.opts.model or self.parent.model,
      system      = (proxy and proxy:_assemble_system()) or self.opts.system,
      thinking    = proxy and proxy.thinking or nil,
      prompt      = prompt,
      tool_defs   = tool_defs,
      adapters    = self.adapter_modules,
      max_turns   = self.opts.max_turns,
      max_tokens  = self.opts.max_tokens,
      temperature = self.opts.temperature,
      -- Reintentos de la apertura del stream (G42): el worker aplica la misma
      -- política que el padre, sin evento (no hay bus). Toma el override del spec del
      -- subagente si lo trae, si no hereda el de la sesión padre.
      max_retries   = self.opts.max_retries or self.parent.max_retries,
      retry_base_ms = self.opts.retry_base_ms or self.parent.retry_base_ms,
    })

    -- Bucle de proxy: el worker pide tools, el padre las corre y responde, hasta el
    -- digesto final. Las tools corren en el PADRE con la sesión hija como contexto
    -- (permisos/hooks/handlers centralizados, agente.md §9).
    while true do
      local msg = w:recv()
      if msg == nil then
        eagent("el subagente-worker terminó sin devolver un digesto", { reason = "worker_closed" })
      end
      if msg.kind == "tool_call" then
        -- El padre ejecuta la tool por su pipeline COMPLETO (permisos → hooks →
        -- handler). agent.run_tool_proxy devuelve un bloque tool_result canónico
        -- (con is_error si se denegó/falló): JSON-able, cruza al worker.
        local result = agent.run_tool_proxy(self.proxy_session, {
          id = msg.id, name = msg.name, args = msg.args,
        })
        w:send({ kind = "tool_result", result = result })
      elseif msg.kind == "message" then
        -- Persistencia del transcript hijo (A-21, sesiones.md §7): el worker manda
        -- cada Message conforme avanza (prompt del usuario, assistant de cada turno
        -- con su usage/model, y los tool_result como mensaje de usuario) y el padre
        -- lo anexa a la `proxy_session` —que tiene el lock (§6)—, en el mismo orden
        -- y con la misma forma que el modo task. Sin store (no_store) no se persiste
        -- (el worker sigue mandándolos; el padre los ignora). No se responde.
        if self.proxy_session and self.proxy_session.store then
          self.proxy_session.store:append_message(msg.message,
            { usage = msg.usage, model = msg.model })
        end
      elseif msg.kind == "done" then
        return msg.digest
      elseif msg.kind == "error" then
        eagent("el subagente-worker falló: " .. tostring(msg.message), { reason = "worker_error" })
      else
        eagent("mensaje desconocido del subagente-worker: " .. tostring(msg.kind))
      end
    end
  end

  -- ---------------------------------------------------------------------------
  -- Session:spawn(opts) -> Sub (agente.md §9).
  -- ---------------------------------------------------------------------------

  -- agent.Session:spawn(opts) lanza un subagente. opts = los de agent.session +
  --   { worker? = false, caps?: string[] }. Devuelve un `Sub` con :run/:cancel.
  function M.spawn(parent, opts)
    opts = opts or {}
    if type(opts) ~= "table" then
      einval("Session:spawn espera una tabla de opciones (los de agent.session + worker?/caps?)")
    end
    if opts.worker == true then
      local sub = spawn_worker(parent, opts)
      -- proxy_session: el contexto con el que el padre corre las tools del worker.
      -- Es una sesión hija REAL (sin red: no manda turnos; solo aporta permisos,
      -- cwd y, si persiste, el transcript hijo). Sus permisos heredan del padre
      -- recortados por opts.permissions (agente.md §9).
      local proxy_opts = {}
      for k, v in pairs(opts) do proxy_opts[k] = v end
      proxy_opts.worker = nil
      proxy_opts.caps   = nil
      proxy_opts.parent = parent.id
      proxy_opts.model  = opts.model or parent.model
      sub.proxy_session = agent.session(proxy_opts)
      return sub
    end
    return spawn_task(parent, opts)
  end

  -- Sub:run(prompt) ⏸ -> Message/digest (agente.md §9). Corre el/los turno(s) del
  -- subagente y devuelve su DIGESTO. En modo task es el Message final del turno
  -- (que YA es un digesto: el mensaje, no el stream); en modo worker es la tabla
  -- digesto que cruzó la frontera.
  function Sub:run(prompt)
    if self.closed then
      eagent("el subagente está cerrado")
    end
    if self.mode == "task" then
      local final = self.child:send(prompt)
      -- Digesto alineado con el modo worker: el usage del proveedor del último turno
      -- (input/output_tokens), no el acumulado de la sesión, y el stop_reason REAL
      -- del último done — no un "end" fijo que ocultaría max_tokens/refusal (§9).
      return digest_of(final, self.child.last_usage,
        self.child.last_stop_reason or (final and "end" or nil), self.child.usage.turns)
    else
      return run_worker(self, prompt)
    end
  end

  -- Sub:cancel() corta el subagente (agente.md §9). En modo worker, `terminate` el
  -- worker (inmediato y seguro, api.md §13) y cierra la sesión proxy; en modo task,
  -- cierra la sesión hija. Idempotente.
  function Sub:cancel()
    if self.closed then
      return
    end
    self.closed = true
    if self.mode == "worker" then
      if self.worker then self.worker:terminate() end
      if self.proxy_session then self.proxy_session:close() end
    else
      if self.child then self.child:close() end
    end
  end

  return M
end

return M
