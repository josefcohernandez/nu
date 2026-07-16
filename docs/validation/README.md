---
title: "Rondas de validación por pseudocódigo"
type: "indice"
status: "vigente"
---
# Rondas de validación por pseudocódigo

Estado: ejercicio de validación previo a congelar la API. Regla del juego:
**solo se puede usar lo especificado** en [api.md](../contracts/api.md),
[providers.md](../contracts/providers.md), [sesiones.md](../contracts/sesiones.md),
[agente.md](../contracts/agente.md) y [chat.md](../contracts/chat.md). Cada punto donde el código no
se pudo escribir es un hallazgo (lista al final). El código es ilustrativo,
no normativo ni completo.

---

## Escenario 1: adaptador Anthropic (providers.md §3)

```lua
-- extensión providers / adapters/anthropic.lua
return {
  name = "anthropic",
  caps = { tools = true, images = true, thinking = true, system = true, usage = true },

  stream = function(req, provider)
    local s = enu.http.stream{
      url = provider.base_url .. "/v1/messages",
      method = "POST",
      headers = {
        ["x-api-key"] = provider.api_key,
        ["anthropic-version"] = "2023-06-01",
        ["content-type"] = "application/json",
      },
      body = enu.json.encode(to_wire(req)),          -- canónico → dialecto
      idle_timeout_ms = 60000,                       -- [HALLAZGO H2]
    }

    if s.status >= 400 then
      local body = {}
      for chunk in s:chunks() do body[#body + 1] = chunk end
      local err = enu.json.decode(table.concat(body))
      error({ code = "EPROVIDER", message = err.error.message,
              detail = { status = s.status,
                         retryable = s.status == 429 or s.status >= 500 } })
    end

    -- SSE de Anthropic → vocabulario canónico de Event
    local assembling = new_message_assembler()       -- Lua puro
    return function()                                 -- iterator<Event>
      for sse in s:events() do                        -- ⏸ api.md §8
        local d = enu.json.decode(sse.data)
        if d.type == "content_block_delta" and d.delta.type == "text_delta" then
          assembling:push_text(d.index, d.delta.text)
          return { type = "text", text = d.delta.text }
        elseif d.type == "content_block_start" and d.content_block.type == "tool_use" then
          assembling:open_tool(d.index, d.content_block)
          return { type = "tool_call.begin",
                   id = d.content_block.id, name = d.content_block.name }
        elseif d.type == "message_delta" then
          assembling:set_stop(d.delta.stop_reason)
          return { type = "usage", output_tokens = d.usage.output_tokens }
        elseif d.type == "message_stop" then
          return { type = "done", stop_reason = assembling.stop_reason,
                   message = assembling:finish() }   -- meta de thinking dentro
        end
        -- ...resto de tipos análogos
      end
      return nil
    end
  end,
}
```

Veredicto: se escribe entero con `enu.http.stream` + `s:events()` +
`enu.json`. Único roce: el timeout de inactividad ([H2](#hallazgos)).

## Escenario 2: el turno del agente (agente.md §2) y la espera de permisos

```lua
-- extensión agent (núcleo del loop, simplificado)
function Session:send(content)
  self:append{ t = "message", ts = enu.sys.now_ms(),
               message = { role = "user", content = as_blocks(content) } }

  while true do
    local req = run_hook_chain("request.pre", self:assemble_request(), self.ctx)
    local msg = with_retries(function()              -- backoff: enu.task.sleep
      return consume_stream(self, req)               -- escenario 1 + agent:delta
    end)

    self:append{ t = "message", ts = enu.sys.now_ms(),
                 message = msg, usage = self.last_usage, model = self.model }
    enu.events.emit("agent:message", { session = self.id, message = msg })

    local calls = tool_calls_in(msg)
    if #calls == 0 then return msg end

    for _, call in ipairs(calls) do                  -- secuencial (P12)
      local result = self:run_tool(call)             -- abajo
      self:append_tool_result(call, result)
    end
    self:maybe_compact()                             -- §8 del contrato
  end
end

function Session:run_tool(call)
  local verdict = static_policy(self.permissions, call)        -- deny/allow/?
  if verdict == nil then
    verdict = run_hook_chain("permission", call, self.ctx)     -- middleware
  end
  if verdict == nil and self.permissions.mode == "ask" then
    if not interactive() then return denied("headless")  end   -- default deny
    local fut = enu.task.future()                     -- [HALLAZGO H1]
    pending_asks[call.id] = fut
    enu.events.emit("agent:permission.asked", { id = call.id, call = call })
    verdict = fut:await()                            -- ⏸ hasta respond()
  end
  if verdict.deny then return denied(verdict.deny) end

  local tool = registry[call.name]
  local args = run_hook_chain("tool.pre", call.args, self.ctx)
  local ok, result = pcall(tool.handler, args, self.ctx)       -- errores → is_error
  if not ok then return { is_error = true, content = err_text(result) } end
  return run_hook_chain("tool.post", result, self.ctx)
end

-- La otra mitad del rendez-vous (la llama la extensión chat):
function agent.permission.respond(id, verdict)
  local fut = pending_asks[id]; pending_asks[id] = nil
  fut:set(verdict)                                   -- despierta al turno
end
```

Veredicto: todo existe **salvo el rendez-vous**: una task (el turno) debe
dormirse hasta que otro código (el diálogo de la UI) la despierte con un
valor. Sin primitiva, la única opción era un bucle de polling con
`task.sleep` — inaceptable como patrón fundacional ([H1](#hallazgos)).

## Escenario 3: una tool con progreso (agente.md §3)

```lua
agent.tool{
  name = "bash", description = "Ejecuta un comando de shell",
  schema = { type = "object", properties = { command = { type = "string" } } },
  permissions = { default = "ask" },                 -- muta: pide permiso
  handler = function(args, ctx)
    local p = enu.proc.spawn({ "sh", "-c", args.command }, { cwd = ctx.cwd })
    local out = {}
    while true do
      local line = p:read_line("stdout")             -- ⏸
      if line == nil then break end
      out[#out + 1] = line
      ctx.progress(line)                             -- agent:tool.progress
    end
    local st = p:wait()                              -- ⏸
    if st.code ~= 0 then error({ code = "EIO", message = "exit " .. st.code,
                                 detail = { output = table.concat(out, "\n") } }) end
    return table.concat(out, "\n")
  end,
}
```

Veredicto: limpio. `read_line` + `progress` dan streaming de salida en vivo
sin nada nuevo.

*Nota posterior: G55 (2026-07-16, de SEC-04) invirtió el default de entorno
que este sketch ilustra — la tool `bash` del contrato monta el entorno del
hijo **sin** los secretos del provider (`providers.secret_env_vars()`,
[agente.md](../contracts/agente.md) §3); lo mismo vale para el `enu.proc.spawn(argv, {})`
del cliente MCP del escenario 8. Ambos sketches reflejan el estado previo a
esa resolución: no los copies como plantilla de lanzamiento de subprocesos.*

## Escenario 4: subagente en worker con proxy de tools (agente.md §9)

```lua
-- Lado principal: lanzar y atender el proxy
function Session:spawn_worker_sub(opts)
  local w = enu.worker.spawn("agent.sub_loop", { caps = opts.caps })
  w:on_message(function(m)
    if m.type == "tool" then
      enu.task.spawn(function()
        local result = self:run_tool(m.call)         -- pipeline completo (§2)
        w:send{ type = "tool_result", id = m.call.id, result = result }
      end)
    elseif m.type == "delta" then
      enu.events.emit("agent:delta", { sub = m.sub_id, text = m.text })
    end
  end)
  w:send{ type = "start", opts = strip_to_json(opts) }   -- solo datos
  return wrap_sub(w)
end

-- Lado worker (agent/sub_loop.lua): sin enu.ui, sin enu.events
local adapter = require("providers.adapters.anthropic")  -- [HALLAZGO H3]
local start = enu.worker.parent.recv()                    -- ⏸
for _ = 1, start.opts.max_turns do
  for ev in adapter.stream(req, cfg) do
    if ev.type == "text" then
      enu.worker.parent.send{ type = "delta", text = ev.text }
    elseif ev.type == "done" then msg = ev.message end
  end
  local calls = tool_calls_in(msg)
  if #calls == 0 then break end
  for _, call in ipairs(calls) do
    enu.worker.parent.send{ type = "tool", call = call }
    local r = enu.worker.parent.recv()                    -- ⏸ resultado del proxy
    append_result(req, call, r.result)
  end
end
```

Veredicto: el proxy funciona como se diseñó (args/resultados JSON-ables).
Roce: el worker necesita `require` de módulos Lua de plugins (el adaptador)
y la especificación no decía explícitamente que el loader resuelve dentro
de workers ([H3](#hallazgos)).

## Escenario 5: un picker difuso sobre `enu.ui` crudo (api.md §9 y §11)

Sin toolkit (no está especificado a propósito): esto valida que la
primitiva basta para construirlo.

```lua
function fuzzy_picker(title)
  local size = enu.ui.size()
  local reg = enu.ui.region{ x = 4, y = 2, w = size.w - 8, h = 20, z = 100 }
  local fut, query, files, sel = enu.task.future(), "", {}, 1   -- [H1] otra vez

  enu.task.spawn(function()
    files = enu.search.files(enu.fs.cwd())             -- ⏸ respeta .gitignore
    repaint()
  end)

  local function repaint()
    local ranked = enu.search.fuzzy(query, files, { max = 18 })  -- síncrono
    local lines = { { { text = title .. " " .. query, style = { bold = true } } } }
    for i, m in ipairs(ranked) do
      lines[#lines + 1] = { { text = files[m.index],
                              style = i == sel and { reverse = true } or {} } }
    end
    reg:clear(); reg:blit(0, 0, enu.ui.block(lines))
    reg:cursor(enu.text.width(title) + 1 + enu.text.width(query), 0)
  end

  local input = enu.ui.on_input(function(ev)          -- tope de la pila
    if ev.type ~= "key" then return true end
    if ev.key == "escape" then fut:set(nil)
    elseif ev.key == "enter" then fut:set(current_selection())
    elseif ev.key == "up" or ev.key == "down" then move_sel(ev.key)
    elseif ev.text then query = query .. ev.text end
    repaint(); return true                           -- consume todo: modal
  end)

  repaint()
  local choice = fut:await()                         -- ⏸ hasta enter/escape
  input:pop(); reg:destroy()
  return choice
end
```

Veredicto: regiones + bloques + pila de input + `search.fuzzy` componen un
picker modal en ~30 líneas. El estado principal nunca hace trabajo
proporcional al repo (el ranking es primitiva Go). `future` reaparece como
la pieza que faltaba para "esperar la elección".

## Escenario 6: plugin de terceros completo (chat.md §9)

```lua
-- plugins/pytest-runner/init.lua
agent.tool{
  name = "run_tests", description = "Ejecuta la suite de tests",
  schema = { type = "object", properties = { filter = { type = "string" } } },
  permissions = { default = "allow" },               -- solo lee y ejecuta tests
  handler = function(args, ctx)
    local r = enu.proc.run({ "pytest", "-q", args.filter or "." },
                          { cwd = ctx.cwd, timeout_ms = 120000 })
    return enu.json.decode(parse_summary(r.stdout))   -- tabla estructurada
  end,
}

chat.renderer("run_tests", function(result, width)
  local lines = {}
  for _, t in ipairs(result.failures) do
    lines[#lines + 1] = { { text = "✗ " .. t.name, style = { fg = "error" } } }
  end
  lines[#lines + 1] = { { text = result.passed .. " passed", style = { fg = "ok" } } }
  return enu.ui.block(lines)
end)

chat.command{
  name = "test", description = "Pide al agente arreglar los tests que fallen",
  handler = function(args, ctx)
    ctx.session:send("Ejecuta run_tests y arregla los fallos que encuentres") -- ⏸
  end,
}

chat.statusline.add{
  id = "pytest", side = "right", priority = 50,
  render = function() return { { text = last_status, style = { fg = "dim" } } } end,
}
```

Veredicto: los cuatro puntos de extensión de `chat` + el registro de tools
componen un plugin real sin tocar nada interno.

---

## Hallazgos

**H1 — Falta una primitiva de rendez-vous (`enu.task.future`).** Apareció
tres veces (espera de permisos, picker modal, y en general "una task espera
un valor que otro código producirá"). Sin ella, el patrón sería polling con
`task.sleep`. Resolución: añadir a [api.md](../contracts/api.md) §3
`enu.task.future() -> Future`, con `Future:set(v)` (síncrono, una sola vez)
y `Future:await() -> v ⏸` (varios pueden esperar; si ya está resuelto,
retorna inmediato).

**H2 — Timeout de inactividad en streams.** `timeout_ms` razonablemente
cubre hasta recibir cabeceras, pero un SSE puede quedarse mudo para
siempre. Resolución: `opts.idle_timeout_ms` en `enu.http.stream` (lanza
`ETIMEOUT` si pasan N ms sin bytes).

**H3 — `require` dentro de workers.** El escenario 4 necesita cargar el
módulo del adaptador en el worker. Resolución: aclarar en [api.md](../contracts/api.md)
§13 que las rutas de `require` del loader (módulos Lua de plugins) están
disponibles en workers; lo que no existe es la API `enu.plugin` (ciclo de
vida).

Ningún otro punto de los seis escenarios requirió inventar API. Con H1-H3
aplicados, el corpus queda listo para congelar.

---

## Índice

| Ronda | Título | Escenarios | Fichero |
|---|---|---|---|
| 1 | Ejercicio de validación: pseudocódigo de punta a punta | 6 | [ronda-1-punta-a-punta.md](ronda-1-punta-a-punta.md) |
| 2 | Ronda 2: los caminos feos | 5 | [ronda-2-caminos-feos.md](ronda-2-caminos-feos.md) |
| 3 | Ronda 3: las zonas sin torturar | 6 | [ronda-3-zonas-sin-torturar.md](ronda-3-zonas-sin-torturar.md) |
| 4 | Ronda 4: ángulos nuevos (verificación de completitud) | 6 | [ronda-4-angulos-nuevos.md](ronda-4-angulos-nuevos.md) |
| 5 | Ronda 5: un tercero monta orquestación de agentes | 4 | [ronda-5-orquestacion-de-terceros.md](ronda-5-orquestacion-de-terceros.md) |
| 6 | Ronda 6: reconstruir un harness estilo claude-code sobre `enu.ui` | 4 | [ronda-6-harness-sobre-enu-ui.md](ronda-6-harness-sobre-enu-ui.md) |
| 7 | Ronda 7: control de razonamiento (`thinking`) por-modelo | 1 | [ronda-7-control-de-thinking.md](ronda-7-control-de-thinking.md) |
| 8 | Ronda 8: una malla distribuida de agentes ("kubernetes de agentes") | 4 | [ronda-8-malla-distribuida.md](ronda-8-malla-distribuida.md) |
