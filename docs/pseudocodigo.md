# Ejercicio de validación: pseudocódigo de punta a punta

Estado: ejercicio de validación previo a congelar la API. Regla del juego:
**solo se puede usar lo especificado** en [api.md](api.md),
[providers.md](providers.md), [sesiones.md](sesiones.md),
[agente.md](agente.md) y [chat.md](chat.md). Cada punto donde el código no
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
    local s = nu.http.stream{
      url = provider.base_url .. "/v1/messages",
      method = "POST",
      headers = {
        ["x-api-key"] = provider.api_key,
        ["anthropic-version"] = "2023-06-01",
        ["content-type"] = "application/json",
      },
      body = nu.json.encode(to_wire(req)),          -- canónico → dialecto
      idle_timeout_ms = 60000,                       -- [HALLAZGO H2]
    }

    if s.status >= 400 then
      local body = {}
      for chunk in s:chunks() do body[#body + 1] = chunk end
      local err = nu.json.decode(table.concat(body))
      error({ code = "EPROVIDER", message = err.error.message,
              detail = { status = s.status,
                         retryable = s.status == 429 or s.status >= 500 } })
    end

    -- SSE de Anthropic → vocabulario canónico de Event
    local assembling = new_message_assembler()       -- Lua puro
    return function()                                 -- iterator<Event>
      for sse in s:events() do                        -- ⏸ api.md §8
        local d = nu.json.decode(sse.data)
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

Veredicto: se escribe entero con `nu.http.stream` + `s:events()` +
`nu.json`. Único roce: el timeout de inactividad ([H2](#hallazgos)).

## Escenario 2: el turno del agente (agente.md §2) y la espera de permisos

```lua
-- extensión agent (núcleo del loop, simplificado)
function Session:send(content)
  self:append{ t = "message", ts = nu.sys.now_ms(),
               message = { role = "user", content = as_blocks(content) } }

  while true do
    local req = run_hook_chain("request.pre", self:assemble_request(), self.ctx)
    local msg = with_retries(function()              -- backoff: nu.task.sleep
      return consume_stream(self, req)               -- escenario 1 + agent:delta
    end)

    self:append{ t = "message", ts = nu.sys.now_ms(),
                 message = msg, usage = self.last_usage, model = self.model }
    nu.events.emit("agent:message", { session = self.id, message = msg })

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
    local fut = nu.task.future()                     -- [HALLAZGO H1]
    pending_asks[call.id] = fut
    nu.events.emit("agent:permission.asked", { id = call.id, call = call })
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
    local p = nu.proc.spawn({ "sh", "-c", args.command }, { cwd = ctx.cwd })
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

## Escenario 4: subagente en worker con proxy de tools (agente.md §9)

```lua
-- Lado principal: lanzar y atender el proxy
function Session:spawn_worker_sub(opts)
  local w = nu.worker.spawn("agent.sub_loop", { caps = opts.caps })
  w:on_message(function(m)
    if m.type == "tool" then
      nu.task.spawn(function()
        local result = self:run_tool(m.call)         -- pipeline completo (§2)
        w:send{ type = "tool_result", id = m.call.id, result = result }
      end)
    elseif m.type == "delta" then
      nu.events.emit("agent:delta", { sub = m.sub_id, text = m.text })
    end
  end)
  w:send{ type = "start", opts = strip_to_json(opts) }   -- solo datos
  return wrap_sub(w)
end

-- Lado worker (agent/sub_loop.lua): sin nu.ui, sin nu.events
local adapter = require("providers.adapters.anthropic")  -- [HALLAZGO H3]
local start = nu.worker.parent.recv()                    -- ⏸
for _ = 1, start.opts.max_turns do
  for ev in adapter.stream(req, cfg) do
    if ev.type == "text" then
      nu.worker.parent.send{ type = "delta", text = ev.text }
    elseif ev.type == "done" then msg = ev.message end
  end
  local calls = tool_calls_in(msg)
  if #calls == 0 then break end
  for _, call in ipairs(calls) do
    nu.worker.parent.send{ type = "tool", call = call }
    local r = nu.worker.parent.recv()                    -- ⏸ resultado del proxy
    append_result(req, call, r.result)
  end
end
```

Veredicto: el proxy funciona como se diseñó (args/resultados JSON-ables).
Roce: el worker necesita `require` de módulos Lua de plugins (el adaptador)
y la especificación no decía explícitamente que el loader resuelve dentro
de workers ([H3](#hallazgos)).

## Escenario 5: un picker difuso sobre `nu.ui` crudo (api.md §9 y §11)

Sin toolkit (no está especificado a propósito): esto valida que la
primitiva basta para construirlo.

```lua
function fuzzy_picker(title)
  local size = nu.ui.size()
  local reg = nu.ui.region{ x = 4, y = 2, w = size.w - 8, h = 20, z = 100 }
  local fut, query, files, sel = nu.task.future(), "", {}, 1   -- [H1] otra vez

  nu.task.spawn(function()
    files = nu.search.files(nu.fs.cwd())             -- ⏸ respeta .gitignore
    repaint()
  end)

  local function repaint()
    local ranked = nu.search.fuzzy(query, files, { max = 18 })  -- síncrono
    local lines = { { { text = title .. " " .. query, style = { bold = true } } } }
    for i, m in ipairs(ranked) do
      lines[#lines + 1] = { { text = files[m.index],
                              style = i == sel and { reverse = true } or {} } }
    end
    reg:clear(); reg:blit(0, 0, nu.ui.block(lines))
    reg:cursor(nu.text.width(title) + 1 + nu.text.width(query), 0)
  end

  local input = nu.ui.on_input(function(ev)          -- tope de la pila
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
    local r = nu.proc.run({ "pytest", "-q", args.filter or "." },
                          { cwd = ctx.cwd, timeout_ms = 120000 })
    return nu.json.decode(parse_summary(r.stdout))   -- tabla estructurada
  end,
}

chat.renderer("run_tests", function(result, width)
  local lines = {}
  for _, t in ipairs(result.failures) do
    lines[#lines + 1] = { { text = "✗ " .. t.name, style = { fg = "error" } } }
  end
  lines[#lines + 1] = { { text = result.passed .. " passed", style = { fg = "ok" } } }
  return nu.ui.block(lines)
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

**H1 — Falta una primitiva de rendez-vous (`nu.task.future`).** Apareció
tres veces (espera de permisos, picker modal, y en general "una task espera
un valor que otro código producirá"). Sin ella, el patrón sería polling con
`task.sleep`. Resolución: añadir a [api.md](api.md) §3
`nu.task.future() -> Future`, con `Future:set(v)` (síncrono, una sola vez)
y `Future:await() -> v ⏸` (varios pueden esperar; si ya está resuelto,
retorna inmediato).

**H2 — Timeout de inactividad en streams.** `timeout_ms` razonablemente
cubre hasta recibir cabeceras, pero un SSE puede quedarse mudo para
siempre. Resolución: `opts.idle_timeout_ms` en `nu.http.stream` (lanza
`ETIMEOUT` si pasan N ms sin bytes).

**H3 — `require` dentro de workers.** El escenario 4 necesita cargar el
módulo del adaptador en el worker. Resolución: aclarar en [api.md](api.md)
§13 que las rutas de `require` del loader (módulos Lua de plugins) están
disponibles en workers; lo que no existe es la API `nu.plugin` (ciclo de
vida).

Ningún otro punto de los seis escenarios requirió inventar API. Con H1-H3
aplicados, el corpus queda listo para congelar.

---

# Ronda 2: los caminos feos

Misma regla, peor intención: cancelaciones a mitad de todo, recursos
huérfanos, colas inundadas, el usuario radical y el arranque. Hallazgos
F1-F5 al final.

## Escenario 7: esc a mitad de un turno con de todo en vuelo

Estado: stream SSE abierto, una tool `bash` con un proceso corriendo, y un
diálogo de permisos pendiente de otra tool. El usuario pulsa `esc` →
`Session:cancel()`.

```lua
-- ¿Qué DEBERÍA pasar? La task del turno se aborta en su siguiente
-- suspensión... pero al intentar escribirlo aparecieron dos grietas:

-- GRIETA A [F1]: el loop envuelve los handlers en pcall (errores → is_error).
local ok, result = pcall(tool.handler, args, ctx)
-- Si la cancelación se entrega como un error ECANCELED "normal", este pcall
-- LA CAPTURA: la cancelación se convierte en un tool_result de error y el
-- turno sigue con la siguiente tool como si nada. La cancelación necesita
-- ser un aborto NO capturable, o todo pcall del ecosistema la rompe.

-- GRIETA B [F2]: el handler de bash tenía un proceso vivo:
handler = function(args, ctx)
  local p = nu.proc.spawn({ "sh", "-c", args.command }, { cwd = ctx.cwd })
  nu.task.cleanup(function() p:kill() end)   -- ← NO EXISTÍA; sin esto, el
  ...                                         --   aborto deja el proceso huérfano
end
-- Lo mismo el picker del escenario 5: si la task que lo llama se aborta,
-- ¿quién hace input:pop() y reg:destroy()? Sin un mecanismo de limpieza
-- ligado a la task, toda cancelación deja basura (procesos, regiones,
-- handlers de input apilados).

-- El future del permiso pendiente: el turno abortado deja de esperar; el
-- respond() tardío hace set() sobre un future que nadie espera — inocuo. ✓
```

## Escenario 8: la extensión MCP completa (proceso longevo + JSON-RPC)

```lua
-- mcp/client.lua — un servidor MCP por stdio, vivo toda la sesión
local M = { pending = {}, next_id = 0 }

function M.connect(argv)
  M.proc = nu.proc.spawn(argv, {})
  nu.task.spawn(function()                       -- task lectora permanente
    nu.task.cleanup(function() M.proc:kill() end) -- [F1/F2] otra vez
    while true do
      local line = M.proc:read_line("stdout")    -- ⏸
      if line == nil then return M.reconnect() end
      local msg = nu.json.decode(line)
      local fut = M.pending[msg.id]               -- correlación por id:
      if fut then M.pending[msg.id] = nil; fut:set(msg) end  -- futures ✓ (H1)
    end
  end)
end

function M.request(method, params)                -- concurrente sin fricción
  M.next_id = M.next_id + 1
  local fut = nu.task.future()
  M.pending[M.next_id] = fut
  M.proc:write(nu.json.encode{ jsonrpc = "2.0", id = M.next_id,
                               method = method, params = params } .. "\n") -- ⏸
  return fut:await()                              -- ⏸
end

-- Apagado limpio: nu.events.on("core:shutdown", function() M.proc:kill() end) ✓
-- Arranque: connect() NO se llama al cargar el módulo (guía §1), sino en el
-- primer uso o en core:ready. ✓
```

Veredicto: el patrón future-por-id resuelve JSON-RPC concurrente con
elegancia. Reaparece la necesidad de `cleanup` ([F1](#hallazgos-ronda-2)).

## Escenario 9: un worker que inunda al principal

Un subagente en worker emite `delta` por cada token; el principal va lento
(pintando). ¿Qué pasa con la cola de mensajes?

```lua
nu.worker.parent.send{ type = "delta", text = ev.text }
-- La especificación no decía NADA del tamaño de la cola ni de qué pasa al
-- llenarse [F3]. Sin límite: memoria sin cota (el mismo agujero que ya
-- cerramos en streams §8). Con límite y error: ¿el worker revienta por ir
-- rápido? La respuesta coherente con todo el diseño es backpressure:
-- send suspende (⏸) hasta que haya hueco — el worker se frena solo al
-- ritmo del consumidor, igual que un stream.
```

## Escenario 10: el usuario radical y el arranque

Quiere: desactivar la extensión oficial `chat`, cargar la suya, y que sus
keymaps ganen a los de cualquier plugin.

```lua
-- 1. ¿Desactivar chat? No había mecanismo [F4]: nu.plugin.list() muestra
--    "enabled" pero nada lo gobierna. → nu.toml del usuario:
--      [plugins]
--      disabled = ["chat"]

-- 2. ¿Sus keymaps ganan? Depende del ORDEN DE ARRANQUE, que no estaba
--    especificado [F4]. Sin orden definido, "quién gana" es una carrera.
--    → Orden canónico: core → plugins (topológico, respetando disabled)
--      → init.lua del usuario → core:ready.
--    El init.lua va último: la pila de input hace que el registro más
--    reciente gane → el usuario tiene la última palabra por construcción,
--    sin mecanismo especial de prioridades.

-- 3. Su chat alternativo: agent.* + nu.events "agent:*" + toolkit — todo
--    público (ya validado en rondas previas). ✓
```

## Escenario 11: coste del re-render en streaming (análisis, no código)

Cada `agent:delta` añade texto al mensaje en curso; ¿re-renderizar el
markdown del mensaje entero por cada token es cuadrático? Respuesta: el
repintado va coalescido a ~30 ms (ADR-007), así que el patrón correcto es
re-renderizar el mensaje en curso **una vez por tick de pintado**, no por
delta — y `nu.text.markdown` sobre unos pocos KB en Go son microsegundos.
No es grieta de API; es un patrón que debe estar escrito en la guía
([F5](#hallazgos-ronda-2)).

---

## Hallazgos (ronda 2)

**F1 — La cancelación no puede ser un error capturable, y faltaba
`nu.task.cleanup`.** Si el aborto (por `cancel()` o por watchdog) se
entrega como error normal, cualquier `pcall` del ecosistema lo captura y
el programa sigue como si nada (escenario 7). Resolución en
[api.md](api.md) §1.3 y §3: el aborto desenrolla la task **sin pasar por
`pcall`**, y `nu.task.cleanup(fn)` registra liberadores LIFO que corren
siempre (éxito, error o aborto) — el `defer` de esta casa.

**F2 — Vida de los recursos ligada a la task.** Procesos, regiones y
handlers de input no morían con la task que los creó. Resolución: la
convención es `cleanup` (F1) en quien los crea; como red de seguridad, un
`Proc` sin referencias acaba matado por el GC (no determinista — el
cleanup explícito es la regla, guía §3).

**F3 — Backpressure en los canales worker↔principal.** Colas acotadas;
`send` (ambos lados) pasa a ser suspendiente ⏸: quien produce más rápido
de lo que el otro consume, se frena — coherente con los streams de §8.
Desde handlers síncronos: `task.spawn` como siempre.

**F4 — Arranque y gobierno de plugins sin especificar.** No había forma de
desactivar un plugin ni orden de carga definido (¿quién gana un keymap?).
Resolución en [api.md](api.md) §14: fichero de configuración del runtime
`config.dir()/nu.toml` (`plugins.disabled`, presupuesto del watchdog) y
orden canónico **core → plugins → init.lua del usuario → `core:ready`** —
el usuario gana por ir último, sin sistema de prioridades.
*Nota posterior: ADR-010 invirtió el defecto — las extensiones oficiales
se distribuyen **inactivas** y `nu.toml` gobierna la activación, no la
desactivación. El `plugins.disabled` de este hallazgo y del escenario 10
refleja el estado previo a esa ADR.*

**F5 — Patrón de render en streaming** (sin cambio de API): re-renderizar
el mensaje en curso una vez por tick de pintado, no por delta. A la guía
(§6).

---

# Ronda 3: las zonas sin torturar

Cambio de método: esta ronda **no aplica resoluciones** — cada grieta va a
la lista de problemas abiertos ([problemas.md](problemas.md)) para
resolverse una a una.

## Escenario 12: resize del terminal con un modal abierto

```lua
-- El picker del escenario 5, con el terminal a 120 columnas:
local reg = nu.ui.region{ x = 4, y = 2, w = nu.ui.size().w - 8, h = 20, z = 100 }
-- El usuario encoge el terminal a 60 columnas. ¿Y ahora qué?
--   · La región tiene w = 112 sobre una pantalla de 60: ¿se recorta? ¿error?
--     La spec define el clipping de blit DENTRO de la región, pero no qué
--     hace una región que se sale de la pantalla.                    [G1]
--   · Nadie recoloca el picker: no se suscribió a "ui:resize". ¿Convención,
--     anclajes declarativos (x = "center"), o cada plugin a su suerte? [G1]
```

## Escenario 13: el ciclo de desarrollo del autor de plugins

```lua
-- Edito mi plugin y quiero probarlo SIN reiniciar nu:
nu.plugin.reload("mi-plugin")   -- ← no existe
-- Y aunque existiera: require cachea módulos; re-ejecutar init.lua
-- duplicaría tools, comandos, keymaps y hooks (no hay des-registro masivo).
-- Todos los registros devuelven handle (Sub, Keymap, Hook...), pero nadie
-- los rastrea por plugin → no se puede deshacer "todo lo de mi-plugin".
-- Hoy la única vía es reiniciar nu en cada iteración.               [G2]
-- (Mismo agujero menor: editar providers.toml o nu.toml en caliente.)
```

## Escenario 14: dos sesiones de agente en la misma UI

```lua
-- Un subagente en marcha + la sesión principal, ambos emitiendo:
nu.events.emit("agent:delta", { text = ev.text })        -- ¿de QUIÉN es?
-- Los contratos no OBLIGAN a llevar session_id en cada payload agent:*;
-- chat.md tampoco dice que filtre. Dos turnos concurrentes mezclarían
-- deltas en el mismo bloque.                                        [G3]

-- Y si ambas sesiones piden permiso a la vez: dos modales simultáneos
-- sobre la misma pila de input — ¿cola de modales? Sin definir.     [G3]

-- Reentrada: el usuario pulsa enter con un turno en vuelo:
session:send("otra cosa")   -- ¿EBUSY? ¿se encola? ¿cancela y reemplaza?
-- Sin definir; cada UI improvisaría una semántica distinta.         [G4]
```

## Escenario 15: la misma sesión reanudada en dos terminales

```lua
-- Terminal A: nu --continue  → abre sessions/proy/2026-...jsonl
-- Terminal B: nu --continue  → ¡abre EL MISMO fichero!
-- Dos procesos haciendo fs.append intercalado sobre un JSONL: corrupción
-- silenciosa (líneas entrelazadas). sesiones.md no contempla lock alguno.
--                                                                   [G5]
```

## Escenario 16: el subagente de solo lectura no se puede expresar

```lua
-- Quiero un subagente auditor: que lea TODO, que no escriba NADA.
local w = nu.worker.spawn("auditor", { caps = { "fs", "text", "search" } })
-- caps concede MÓDULOS ENTEROS: "fs" incluye write, remove, rename...
-- No existe "fs de solo lectura" ni caps por función o por ruta. La
-- granularidad módulo-entero se queda corta justo en el caso estrella
-- del sandboxing.                                                   [G6]
```

## Escenario 17: flecos detectados sin escenario propio

```lua
-- a) nu.fs.watch(path, fn): ¿recursivo o un solo path? ¿respeta
--    .gitignore? (vigilar node_modules/ = ráfaga infinita) ¿coalesce
--    ráfagas (git checkout toca 5000 ficheros)?                     [G7]

-- b) Worker:on_message(fn) y Worker:recv() son "alternativas", pero nada
--    prohíbe usar ambos: ¿quién recibe el mensaje? Indefinido.      [G8]

-- c) Windows: la tool bash hace { "sh", "-c", ... } (no existe sh),
--    Proc:kill habla de señales POSIX, y el input de terminal (IME,
--    teclas) difiere. ¿Cuál es el alcance v1 en Windows?            [G9]
```

Hallazgos G1-G9 consolidados con impacto y opciones en
[problemas.md](problemas.md).

---

# Ronda 4: ángulos nuevos (verificación de completitud)

Pregunta explícita: ¿estaba todo? Respuesta: no. Esta ronda ataca el bus
bajo reentrada, las fronteras de datos binarios, los providers
corporativos y de suscripción, el modelo de confianza del contenido del
repo, y el interior de los workers. Hallazgos G10-G16, sin resolver, a
[problemas.md](problemas.md).

## Escenario 18: el bus de eventos bajo reentrada

```lua
nu.events.on("agent:message", function(p)
  nu.events.emit("mi-plugin:resumen", digest(p))   -- emit DENTRO de un emit
end)
nu.events.on("agent:message", function(p)
  sub:cancel()                                     -- ¿y si cancela una sub
  otra = nu.events.on("agent:message", g)          --  o suscribe NUEVOS
end)                                               --  durante el despacho?
-- ¿El emit anidado despacha en profundidad (recursión) o se encola?
-- ¿Un handler recién suscrito ve el evento EN CURSO? ¿Y uno cancelado
-- a mitad? Todo indefinido — y es el tipo de indefinición que produce
-- bugs según el orden de carga de plugins.                          [G10]
```

## Escenario 19: bytes que no son texto

```lua
-- La tool bash hace cat de un PNG por error:
local r = nu.proc.run({ "cat", "logo.png" }, {})
return r.stdout   -- bytes arbitrarios → tool_result → tres fronteras JSON:
-- 1) nu.json.encode hacia el provider: JSON exige UTF-8 válido. ¿Lanza?
--    ¿Reemplaza? ¿Silencio?
-- 2) la entrada `message` del transcript JSONL: igual.
-- 3) un Worker:send con ese resultado: "JSON-able"... ¿lo es?
-- Sin regla, cada frontera improvisa y el bug aparece lejos del origen.
--                                                                   [G11]
```

## Escenario 20: el proxy corporativo que pusimos en la filosofía

```lua
-- providers.toml prometía "proxy corporativo" como caso estrella:
[providers.corp]
adapter  = "openai-compat"
base_url = "https://llm.interna.corp"   -- CA corporativa autofirmada
-- nu.http no tiene opciones TLS: ni ca_file, ni insecure, ni proxy
-- explícito (¿se respeta HTTPS_PROXY del entorno? sin especificar).
-- El caso anunciado no se puede configurar.                         [G12]
```

## Escenario 21: provider por suscripción (OAuth)

```lua
-- Un adaptador para un plan de suscripción (no API key): OAuth device flow
-- sí es escribible (http.request en bucle de polling + abrir URL con
-- nu.proc). Pero el flujo con callback localhost NO: no existe primitiva
-- de servidor/listener HTTP. ¿Y dónde guarda el adaptador el refresh
-- token? (¿plugins/<nombre>/? ¿en claro?) Sin convención.           [G13]
```

## Escenario 22: el repo malicioso (modelo de confianza)

```lua
-- nu se abre en un repo clonado de internet. El repo trae:
--   .nu/skills/inocente/SKILL.md   → se inyecta su índice en el system
--                                     prompt (agente §6-§7) SIN preguntar
--   .nu/agent.toml                 → ¡puede traer allow = ["bash:*"]!
--                                     (precedencia: proyecto > global)
-- Resultado: clonar un repo y abrir nu ya es ejecutar la voluntad del
-- repo. Mismo problema con descripciones de tools de servidores MCP de
-- terceros (texto no confiable inyectado al modelo). No hay modelo de
-- confianza: ni trust-on-first-use, ni qué config del repo se honra sin
-- preguntar.                                                        [G14]
```

## Escenario 23: dentro de un worker, ¿qué hay exactamente?

```lua
-- worker con task [W]: ¿el worker tiene su PROPIO scheduler/event loop?
nu.task.spawn(...)   -- ¿múltiples tasks dentro de un worker? ¿timers?
nu.task.race(...)    -- (el escenario 4 ya lo asumió para multiplexar
                     --  stream y cancelación... sin que estuviera escrito)
-- ¿Aplica watchdog dentro del worker? ¿Con qué presupuesto?        [G15]

-- Y dos subagentes paralelos editando el MISMO fichero vía proxy de
-- tools: las tools se intercalan en el principal pero nada coordina
-- escrituras al mismo path — last-write-wins silencioso.            [G16]
```

Menores anotados al pasar: rotación del fichero de `nu.log`
(→ [P20](pospuesto.md)); propiedad de los `Timer` (¿mueren con la task?
→ convención `cleanup`); restricciones de versión en `requires` (se
pliega a [P4](pospuesto.md) cuando se reabra).

---

# Ronda 5: un tercero monta orquestación de agentes

Pregunta del stress test: si la extensión oficial `agent` existe, ¿puede
**otro** plugin construir encima loops deterministas de agentes y correrlos
en paralelo, usando solo el contrato público ([agente.md](agente.md)) +
`nu.task` + `nu.worker`? Misma regla de siempre. Dos ejes que tirar de los
extremos: **determinismo** (un loop reproducible en su control de flujo) y
**paralelismo** (N agentes a la vez). Fuera de alcance a propósito: el
no-determinismo del *muestreo* del modelo (temperatura/seed) es territorio
de [providers.md](providers.md), no del orquestador.

## Escenario 24: driver de loop determinista (plugin de tercero)

Un pipeline fijo plan → implementa → testea → revisa, secuencial y acotado.
Lo escribe alguien que solo `require("agent")`.

```lua
-- plugins/pipeline/init.lua
local agent = require("agent")          -- el loader pone su lua/ en require ✓ (§14)

-- Compactación DETERMINISTA: regla mecánica, sin LLM → el mismo input da el
-- mismo resumen. El hook compact (§8) recibe la conversación y devuelve el
-- mensaje-resumen; nada obliga a que sea un LLM.
agent.hook("compact", function(convo, ctx)
  return keep_system_and_last_k(convo, 12)        -- Lua puro, reproducible ✓
end)

local STEPS = { "plan", "implement", "run tests and fix", "review the diff" }

function run_pipeline(goal)
  local s = agent.session{
    model = "anthropic/claude-...",
    permissions = {                                -- headless = default deny (§5);
      mode = "auto",                               -- el allowlist se declara, no se hereda
      allow = { "read", "grep", "glob", "edit", "bash:pytest *", "bash:git *" },
      deny  = { "bash:rm *", "bash:curl *" },
    },
    -- max_turns viene de agent.toml (§10): cota dura contra divergencia
  }
  local outcomes = {}
  for i, step in ipairs(STEPS) do
    local msg = s:send(goal .. " — fase: " .. step)    -- ⏸ turno completo (§2)
    outcomes[i] = decide(msg)                          -- branch determinista
    if outcomes[i].halt then break end                 -- control de flujo, no del modelo
  end
  return outcomes
end
```

El control de flujo es determinista y el `for` no sufre la reentrada de G4:
cada `send` se hace `await` antes del siguiente, la cola ni se activa.

**Roce — observar lo que pasó DENTRO del turno.** `send` devuelve solo el
mensaje final del asistente; para un branch determinista sobre "¿pasaron los
tests?" el driver necesita el resultado de la tool, no la prosa del modelo.
No hace falta API nueva: se suscribe a `agent:tool.end` filtrando por
`payload.session` (la atribución obligatoria de G3) o lee el transcript JSONL
([sesiones.md](sesiones.md)). Funciona, pero es observación lateral, no valor
de retorno — lo anoto sin elevarlo a hallazgo.

```lua
local seen = {}
nu.events.on("agent:tool.end", function(p)
  if p.session == s.id and p.tool == "run_tests" then seen[#seen+1] = p.result end
end)
```

Veredicto: el loop determinista se escribe entero con `agent.session` +
`send` + `agent.hook("compact")`. La compactación mecánica vía hook es la
pieza que salva el determinismo sin tocar el core.

## Escenario 25: fan-out paralelo de N subagentes

Un "map" sobre territorios disjuntos. Quiero: límite de concurrencia (no
abrir 50 streams y comerme un 429), semántica *allSettled* (un fallo no
mata a los demás), y resultados **alineados con la entrada**.

```lua
-- Semáforo construido SOLO con nu.task.future (como el picker construyó el
-- modal): valida otra vez que la primitiva basta.
local function semaphore(n)
  local free, waiters = n, {}
  return {
    acquire = function()                              -- ⏸
      if free > 0 then free = free - 1; return end
      local f = nu.task.future(); waiters[#waiters + 1] = f; f:await()
    end,
    release = function()
      local w = table.remove(waiters, 1)
      if w then w:set(true) else free = free + 1 end
    end,
  }
end

function fan_out(root, territories, limit)
  local sem = semaphore(limit)
  local fns = {}
  for i, terr in ipairs(territories) do
    fns[i] = function()
      sem.acquire()                                   -- ⏸ respeta el límite
      nu.task.cleanup(sem.release)                    -- libera en éxito/error/aborto (F1)
      local ok, res = pcall(function()
        return root:spawn{                            -- task por defecto (§9)
          permissions = recortar(root.permissions, terr),   -- nunca amplía (§11)
          skills = terr.skills,
        }:run(terr.prompt)                            -- ⏸
      end)
      return { ok = ok, value = res }                 -- allSettled: jamás relanza
    end
  end
  return nu.task.all(fns)                             -- ⏸ espera a todos  [HALLAZGO G27]
end
```

Esto **es paralelismo real donde importa**: cada `spawn{}:run()` corre como
task y se suspende en su `nu.http.stream` al LLM; mientras una espera, las
otras avanzan, y las goroutines de red van de verdad en paralelo (§9, el caso
caliente de [modelo-ejecucion.md](modelo-ejecucion.md)). El `pcall` por rama
me da el *allSettled* que `task.all` no ofrece de fábrica (solo trae
fail-fast: "si una lanza, cancela el resto y relanza"). El semáforo de
`future` me da el límite de concurrencia sin API nueva.

**[HALLAZGO G27] — `nu.task.all` no promete alinear resultados con
entradas.** La firma dice `(fns) -> any[]` y "espera a todas", pero **no**
que `out[i]` corresponda a `fns[i]` (las tasks terminan en cualquier orden).
Para una orquestación determinista esto es justo lo que se necesita
garantizado: sin alineación posicional, el "map" no puede correlacionar
resultado con territorio salvo metiendo el índice dentro de cada payload a
mano. Resolución propuesta: especificar semántica `Promise.all` — resultados
en el **orden de los inputs**, independiente del orden de terminación.

Veredicto: el fan-out paralelo, acotado y robusto a fallos se escribe con
`spawn` + `task.all` + `future`. Una sola grieta real (G27), y es de
*especificación*, no de mecanismo.

## Escenario 26: ¿árbol paralelo de dos niveles? (el límite honesto)

La tentación: "para paralelo de verdad, workers". Quiero 3 líderes en
paralelo, cada uno con sus 3 obreros en paralelo.

```lua
local lider = root:spawn{ worker = true, caps = agent.caps.FS_RO }  -- loop en worker (§9)
-- ...y dentro del líder (que corre en el worker) quiero su propio fan-out:
--   nu.worker.spawn(...)   → NO existe dentro de un worker (P11): sin anidar.
--   sub-obreros como tasks dentro del worker → el worker SÍ tiene scheduler
--     propio (G15), concurren por IO. Pero sus tool calls: el sub_loop del
--     líder ya proxya las suyas al principal (§9); un segundo nivel necesita
--     un SEGUNDO proxy que §9 describe como hoja, no como nodo que re-spawnea.
--                                                            [P11 + matiz §9]
```

Pero el matiz que desactiva casi todo el problema: **para cargas de agente no
hacen falta workers.** La ganancia del worker es CPU-Lua paralela; el camino
caliente de un agente es LLM + IO, que **ya** se solapa entre tasks (todo
suspende). Los subagentes-task del escenario 25 ya dan streams en paralelo de
verdad. El worker solo gana si el subagente quema CPU en Lua — raro en un
agente. Conclusión: el límite de P11 (sin workers anidados) y el carácter de
hoja del worker-subagente (§9) **apenas muerden en la práctica**; el árbol de
paralelismo que un orquestador de agentes necesita de verdad es de tasks, y
ese no tiene límite de profundidad.

Veredicto: confirma P11 y precisa §9 (el worker-subagente es hoja: no
re-spawnea). No es bloqueante para el caso de uso — es el caso equivocado.

## Escenario 27: determinismo ⟂ estado mutable compartido (análisis)

Donde los dos ejes chocan. Tres frentes, ninguno API nueva — son la frontera
que el orquestador debe conocer:

```lua
-- 1) Dos ramas paralelas tocando el mismo path: last-write-wins silencioso
--    (G16, ya conocido). El determinismo del RESULTADO exige territorios
--    disjuntos por prompt, o aislar cada subagente en su worktree:
--      root:spawn{ ... }  con cwd = git_worktree(i)   -- opts.cwd existe (§2)
--    y fusionar al terminar. El reparto de territorio es del orquestador.

-- 2) Cancelación a mitad de escritura: task.all hace fail-fast y aborta el
--    resto. El cleanup (F1/F2) mata el proceso de la tool, pero un fichero a
--    medio escribir queda a medias → estado parcial NO determinista. El
--    remedio es el mismo: worktree por rama, descartar las abortadas. Inherente
--    a "paralelo + IO + cancelación", no grieta del contrato.

-- 3) Reproducibilidad TOTAL (replay de respuestas del modelo): no hay hook de
--    respuesta en §4 (request.pre/tool.pre/tool.post/permission/compact, nunca
--    "response"). Correcto: grabar/reproducir salidas del modelo es un
--    ADAPTADOR de provider (providers.md §3), no el agente — un "replay
--    adapter" lee de un fixture en vez de la red. La capa es la adecuada.
```

Veredicto: la tensión determinismo↔paralelo vive enteramente en el estado
mutable compartido, y el contrato ya nombra el remedio (G16: repartir
territorio; `opts.cwd` para aislar). El orquestador determinista-y-paralelo
es expresable **si** sus ramas son independientes; con estado compartido, la
respuesta correcta es serializar.

---

## Hallazgos (ronda 5)

**G27 — `nu.task.all` debe garantizar resultados alineados con los inputs.**
Único hallazgo nuevo de mecanismo. Sin orden posicional especificado, una
orquestación paralela determinista no puede correlacionar resultado con
entrada sin acarrear el índice a mano. Resolución propuesta: semántica
`Promise.all` en [api.md](api.md) §3 — `out[i]` es el resultado de `fns[i]`,
independiente del orden de terminación. **Resuelto**: aplicado a
[api.md](api.md) §3 y registrado en [problemas.md](problemas.md) (G27).

Confirmaciones (sin API nueva): el loop determinista se monta sobre el
contrato público (§24); el fan-out paralelo acotado y *allSettled* se compone
con `spawn` + `task.all` + `future` + `pcall` (§25); el límite de workers
anidados (P11) y el carácter de hoja del worker-subagente (§9) **no muerden**
en cargas de agente, que son LLM+IO y ya se solapan entre tasks (§26); la
tensión determinismo↔paralelo se reduce a estado mutable compartido, con
remedio ya nombrado (G16 + `opts.cwd`, §27).

Patrones para la guía (sin cambio de API): compactación mecánica vía hook
`compact` para loops reproducibles (§24); semáforo de `nu.task.future` para
acotar la concurrencia de un fan-out (§25); *allSettled* envolviendo cada
rama en `pcall` antes de `task.all` (§25); worktree por subagente para
aislar escrituras paralelas (§27).

---

# Ronda 6: reconstruir un harness estilo claude-code sobre `nu.ui`

Pregunta del stress test: ¿se puede montar la TUI de un harness de coding
(estilo claude-code) **entera** sobre `nu.ui` crudo + el contrato de
[chat.md](chat.md)? La respuesta corta es que `chat.md` ya *es* ese harness;
así que esta ronda no reescribe lo ya validado (transcript, modales, slash,
statusline — escenario 5 cubrió el picker modal) sino que tortura lo que
`chat.md` da por hecho: el **scrollback** del transcript, el **cursor real**
del editor multilínea, el **spinner en vivo**, y el **ratón** sobre bloques
colapsables. Ahí salen tres grietas, todas de `nu.ui` §9. Hallazgos G28-G30
al final.

## Escenario 28: las tres zonas y el scrollback del transcript

```lua
-- plugins/cc-ui/init.lua — una UI estilo coding-harness sobre nu.ui
local function layout()
  local s = nu.ui.size()
  return {
    transcript = nu.ui.region{ x = 0, y = 0,       w = s.w, h = s.h - 4 },
    input      = nu.ui.region{ x = 0, y = s.h - 4, w = s.w, h = 3,  z = 10 },
    status     = nu.ui.region{ x = 0, y = s.h - 1, w = s.w, h = 1,  z = 10 },
  }
end

-- El transcript es un Block alto (todo el historial renderizado) que se
-- "asoma" por la región vía un offset vertical. Scroll = re-blit con otro y.
local scroll, doc = 0, nu.ui.block({})           -- doc.height puede ser >> región
local function repaint_transcript(reg)
  reg:clear()
  reg:blit(0, -scroll, doc)                       -- [HALLAZGO G28] ¿blit acepta y<0?
end
nu.events.on("ui:resize", function() relayout() end)   -- G1: tu región, tu resize
```

Veredicto: sale, salvo una grieta de especificación. `Region:blit`
*"recorta a los límites"*, pero el scrollback necesita estampar el Block con
`y` **negativo** para recortar las primeras filas (asomarse por abajo). El
doc solo habla del recorte por exceso, no de coordenadas locales negativas —
y es la operación central de cualquier transcript con scroll. **[G28]**

## Escenario 29: editor multilínea con cursor real y popups `@` / `/`

```lua
local buf, cur = "", 0                            -- texto y caret (índice de byte)
local function redraw_input(reg)
  local wrapped = nu.text.wrap(buf, reg.w)        -- Block; .height conocido
  if wrapped.height + 1 ~= reg.h then reg:resize(reg.w, wrapped.height + 1) end
  reg:clear(); reg:blit(0, 0, wrapped)
  local cx, cy = caret_to_cell(buf, cur, reg.w)   -- nu.text.width por grafema
  reg:cursor(cx, cy)                               -- cursor real del terminal
end

nu.ui.on_input(function(ev)
  if ev.type == "paste" then
    local ins = ev.text or ev.path                 -- [G30] imagen → ruta, como @
    buf = insert(buf, cur, ins); cur = cur + #ins
  elseif ev.key == "enter" and at_start_slash(buf) then run_slash(buf)
  elseif ev.key == "enter" then session:send(buf); buf = ""
  elseif ev.text == "@" then
    local path = fuzzy_picker("@ file")            -- escenario 5, como popup z=100
    if path then buf = insert(buf, cur, path) end
  end
  redraw_input(input_region); return true
end)
```

Veredicto: sale entero. `nu.text.wrap` da la altura para crecer la caja,
`Region:cursor` coloca el caret real, y los popups `@`/`/` son el picker del
escenario 5 reutilizado. El único trabajo feo es `caret_to_cell` (índice de
byte → celda con `nu.text.width`), pero eso es del toolkit, no API que falte.
Pegar una imagen aparece aquí como **ruta** (G30, abajo).

## Escenario 30: el spinner "Thinking…" en vivo con `esc` para interrumpir

```lua
local function thinking_indicator(session)
  local t0  = nu.sys.mono_ms()
  local reg = nu.ui.region{ x = 0, y = spin_y, w = 40, h = 1 }
  local frame = 0
  local timer = nu.task.every(80, function()       -- handler síncrono, repinta
    frame = frame + 1
    local secs = math.floor((nu.sys.mono_ms() - t0) / 1000)
    local toks = providers.approx_tokens(session.usage)   -- vocabulario de producto
    reg:blit(0, 0, nu.ui.block({{
      { text = SPIN[frame % #SPIN + 1] .. " Thinking… ", style = { italic = true } },
      { text = secs .. "s · " .. toks .. " tok · esc to interrupt",
        style = { fg = "#808080" } },
    }}))
  end)
  nu.task.cleanup(function() timer:stop(); reg:destroy() end)   -- F1/F2: muere con el turno
end
-- esc → Session:cancel() (chat.md §3); el cleanup mata timer y región.
```

Veredicto: limpio. `nu.task.every` anima, `mono_ms` cuenta, `cleanup`
garantiza que el spinner muere con el turno aunque lo aborten — es el patrón
F5 (repintar coalescido, no por delta).

## Escenario 31: ratón sobre un bloque de tool colapsable (análisis)

```lua
-- Clicar la cabecera de un bloque de tool para plegarlo:
nu.ui.on_input(function(ev)
  if ev.type == "mouse" then
    -- ev.x, ev.y vienen en coordenadas de PANTALLA; el bloque vive en
    -- coordenadas LOCALES de la región del transcript, desplazado por el
    -- scroll. No hay Region:contains(x,y) ni traducción global→local: el
    -- plugin rastrea a mano la geometría de cada región (que él fijó) y
    -- resuelve el hit-test sumando/restando origen y scroll.        [G29]
  end
end)
```

Veredicto: expresable, pero a mano. El modelo de pila entrega el ratón en
coordenadas globales y las regiones son locales; sin una primitiva de
traducción/hit-test, cada widget clicable del toolkit reimplementa el mismo
cálculo. **[G29]**

---

## Hallazgos (ronda 6)

Las tres quedaron resueltas tras discutir contraindicaciones (registradas en
[problemas.md](problemas.md)):

**G28 — `Region:blit` con coordenadas locales negativas (viewport/scrollback).**
Mecanismo central del transcript con scroll; el doc solo especificaba el
recorte por exceso. Resuelta en [api.md](api.md) §9.1: `blit` recorta por
**ambos extremos** (negativos recortan el borde inicial), es **copia y no
re-render**, y la virtualización es del toolkit. Las contraindicaciones que
afinaron la resolución: clavar la semántica del negativo, garantizar que no
re-renderiza, y reconocer que no resuelve la virtualización (patrón "cachea
el Block, mueve el offset" en la guía §6).

**G29 — Ratón en coordenadas globales sin traducción a región (hit-testing).**
La tentación era `Region:hit(x,y)`, pero solo haría la mitad trivial (restar
el origen que el plugin ya fijó); la mitad valiosa (qué bloque/línea de un
Block scrolleado) necesita el layout que el plugin posee, no el core.
Resuelta como **convención del toolkit** (opción c), mismo reparto que G1
(relayout) y G22 (theming) — guía §6.

**G30 — Pegar una imagen no es expresable; el evento `paste` solo trae texto.**
Resolución (decidida): pegar contenido no-texto **inyecta una ruta**, no los
bytes — el core vuelca la imagen del portapapeles a un temporal de sesión y
el evento `paste` trae `path`; la UI la inserta igual que una mención `@` y
el agente decide leerla. Mantiene los binarios fuera de las fronteras
texto/JSON (coherente con G11) y es distinto de P6 (render de imágenes en
pantalla, pospuesto). Aplicada a [api.md](api.md) §9.3.

Confirmaciones (sin API nueva): las tres zonas, el editor multilínea con
cursor real, los popups `@`/`/`, el spinner en vivo y los renderers de tools
se montan **enteros** sobre `nu.ui` + el contrato de `chat`. La conclusión de
la pregunta que abrió la ronda se sostiene: la TUI de un harness de coding no
"sale del core" — el core da el sustrato y `chat.md` ya es ese harness. Las
únicas grietas (G28, G29) son de **ergonomía de `nu.ui`**, no de mecanismo
que falte.

