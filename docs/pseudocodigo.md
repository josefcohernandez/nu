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
[agente.md](agente.md) §3); lo mismo vale para el `enu.proc.spawn(argv, {})`
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
`task.sleep`. Resolución: añadir a [api.md](api.md) §3
`enu.task.future() -> Future`, con `Future:set(v)` (síncrono, una sola vez)
y `Future:await() -> v ⏸` (varios pueden esperar; si ya está resuelto,
retorna inmediato).

**H2 — Timeout de inactividad en streams.** `timeout_ms` razonablemente
cubre hasta recibir cabeceras, pero un SSE puede quedarse mudo para
siempre. Resolución: `opts.idle_timeout_ms` en `enu.http.stream` (lanza
`ETIMEOUT` si pasan N ms sin bytes).

**H3 — `require` dentro de workers.** El escenario 4 necesita cargar el
módulo del adaptador en el worker. Resolución: aclarar en [api.md](api.md)
§13 que las rutas de `require` del loader (módulos Lua de plugins) están
disponibles en workers; lo que no existe es la API `enu.plugin` (ciclo de
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
  local p = enu.proc.spawn({ "sh", "-c", args.command }, { cwd = ctx.cwd })
  enu.task.cleanup(function() p:kill() end)   -- ← NO EXISTÍA; sin esto, el
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
  M.proc = enu.proc.spawn(argv, {})
  enu.task.spawn(function()                       -- task lectora permanente
    enu.task.cleanup(function() M.proc:kill() end) -- [F1/F2] otra vez
    while true do
      local line = M.proc:read_line("stdout")    -- ⏸
      if line == nil then return M.reconnect() end
      local msg = enu.json.decode(line)
      local fut = M.pending[msg.id]               -- correlación por id:
      if fut then M.pending[msg.id] = nil; fut:set(msg) end  -- futures ✓ (H1)
    end
  end)
end

function M.request(method, params)                -- concurrente sin fricción
  M.next_id = M.next_id + 1
  local fut = enu.task.future()
  M.pending[M.next_id] = fut
  M.proc:write(enu.json.encode{ jsonrpc = "2.0", id = M.next_id,
                               method = method, params = params } .. "\n") -- ⏸
  return fut:await()                              -- ⏸
end

-- Apagado limpio: enu.events.on("core:shutdown", function() M.proc:kill() end) ✓
-- Arranque: connect() NO se llama al cargar el módulo (guía §1), sino en el
-- primer uso o en core:ready. ✓
```

Veredicto: el patrón future-por-id resuelve JSON-RPC concurrente con
elegancia. Reaparece la necesidad de `cleanup` ([F1](#hallazgos-ronda-2)).

## Escenario 9: un worker que inunda al principal

Un subagente en worker emite `delta` por cada token; el principal va lento
(pintando). ¿Qué pasa con la cola de mensajes?

```lua
enu.worker.parent.send{ type = "delta", text = ev.text }
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
-- 1. ¿Desactivar chat? No había mecanismo [F4]: enu.plugin.list() muestra
--    "enabled" pero nada lo gobierna. → enu.toml del usuario:
--      [plugins]
--      disabled = ["chat"]

-- 2. ¿Sus keymaps ganan? Depende del ORDEN DE ARRANQUE, que no estaba
--    especificado [F4]. Sin orden definido, "quién gana" es una carrera.
--    → Orden canónico: core → plugins (topológico, respetando disabled)
--      → init.lua del usuario → core:ready.
--    El init.lua va último: la pila de input hace que el registro más
--    reciente gane → el usuario tiene la última palabra por construcción,
--    sin mecanismo especial de prioridades.

-- 3. Su chat alternativo: agent.* + enu.events "agent:*" + toolkit — todo
--    público (ya validado en rondas previas). ✓
```

## Escenario 11: coste del re-render en streaming (análisis, no código)

Cada `agent:delta` añade texto al mensaje en curso; ¿re-renderizar el
markdown del mensaje entero por cada token es cuadrático? Respuesta: el
repintado va coalescido a ~30 ms (ADR-007), así que el patrón correcto es
re-renderizar el mensaje en curso **una vez por tick de pintado**, no por
delta — y `enu.text.markdown` sobre unos pocos KB en Go son microsegundos.
No es grieta de API; es un patrón que debe estar escrito en la guía
([F5](#hallazgos-ronda-2)).

---

## Hallazgos (ronda 2)

**F1 — La cancelación no puede ser un error capturable, y faltaba
`enu.task.cleanup`.** Si el aborto (por `cancel()` o por watchdog) se
entrega como error normal, cualquier `pcall` del ecosistema lo captura y
el programa sigue como si nada (escenario 7). Resolución en
[api.md](api.md) §1.3 y §3: el aborto desenrolla la task **sin pasar por
`pcall`**, y `enu.task.cleanup(fn)` registra liberadores LIFO que corren
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
`config.dir()/enu.toml` (`plugins.disabled`, presupuesto del watchdog) y
orden canónico **core → plugins → init.lua del usuario → `core:ready`** —
el usuario gana por ir último, sin sistema de prioridades.
*Nota posterior: ADR-010 invirtió el defecto — las extensiones oficiales
se distribuyen **inactivas** y `enu.toml` gobierna la activación, no la
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
local reg = enu.ui.region{ x = 4, y = 2, w = enu.ui.size().w - 8, h = 20, z = 100 }
-- El usuario encoge el terminal a 60 columnas. ¿Y ahora qué?
--   · La región tiene w = 112 sobre una pantalla de 60: ¿se recorta? ¿error?
--     La spec define el clipping de blit DENTRO de la región, pero no qué
--     hace una región que se sale de la pantalla.                    [G1]
--   · Nadie recoloca el picker: no se suscribió a "ui:resize". ¿Convención,
--     anclajes declarativos (x = "center"), o cada plugin a su suerte? [G1]
```

## Escenario 13: el ciclo de desarrollo del autor de plugins

```lua
-- Edito mi plugin y quiero probarlo SIN reiniciar enu:
enu.plugin.reload("mi-plugin")   -- ← no existe
-- Y aunque existiera: require cachea módulos; re-ejecutar init.lua
-- duplicaría tools, comandos, keymaps y hooks (no hay des-registro masivo).
-- Todos los registros devuelven handle (Sub, Keymap, Hook...), pero nadie
-- los rastrea por plugin → no se puede deshacer "todo lo de mi-plugin".
-- Hoy la única vía es reiniciar enu en cada iteración.               [G2]
-- (Mismo agujero menor: editar providers.toml o enu.toml en caliente.)
```

## Escenario 14: dos sesiones de agente en la misma UI

```lua
-- Un subagente en marcha + la sesión principal, ambos emitiendo:
enu.events.emit("agent:delta", { text = ev.text })        -- ¿de QUIÉN es?
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
-- Terminal A: enu --continue  → abre sessions/proy/2026-...jsonl
-- Terminal B: enu --continue  → ¡abre EL MISMO fichero!
-- Dos procesos haciendo fs.append intercalado sobre un JSONL: corrupción
-- silenciosa (líneas entrelazadas). sesiones.md no contempla lock alguno.
--                                                                   [G5]
```

## Escenario 16: el subagente de solo lectura no se puede expresar

```lua
-- Quiero un subagente auditor: que lea TODO, que no escriba NADA.
local w = enu.worker.spawn("auditor", { caps = { "fs", "text", "search" } })
-- caps concede MÓDULOS ENTEROS: "fs" incluye write, remove, rename...
-- No existe "fs de solo lectura" ni caps por función o por ruta. La
-- granularidad módulo-entero se queda corta justo en el caso estrella
-- del sandboxing.                                                   [G6]
```

## Escenario 17: flecos detectados sin escenario propio

```lua
-- a) enu.fs.watch(path, fn): ¿recursivo o un solo path? ¿respeta
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
enu.events.on("agent:message", function(p)
  enu.events.emit("mi-plugin:resumen", digest(p))   -- emit DENTRO de un emit
end)
enu.events.on("agent:message", function(p)
  sub:cancel()                                     -- ¿y si cancela una sub
  otra = enu.events.on("agent:message", g)          --  o suscribe NUEVOS
end)                                               --  durante el despacho?
-- ¿El emit anidado despacha en profundidad (recursión) o se encola?
-- ¿Un handler recién suscrito ve el evento EN CURSO? ¿Y uno cancelado
-- a mitad? Todo indefinido — y es el tipo de indefinición que produce
-- bugs según el orden de carga de plugins.                          [G10]
```

## Escenario 19: bytes que no son texto

```lua
-- La tool bash hace cat de un PNG por error:
local r = enu.proc.run({ "cat", "logo.png" }, {})
return r.stdout   -- bytes arbitrarios → tool_result → tres fronteras JSON:
-- 1) enu.json.encode hacia el provider: JSON exige UTF-8 válido. ¿Lanza?
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
-- enu.http no tiene opciones TLS: ni ca_file, ni insecure, ni proxy
-- explícito (¿se respeta HTTPS_PROXY del entorno? sin especificar).
-- El caso anunciado no se puede configurar.                         [G12]
```

## Escenario 21: provider por suscripción (OAuth)

```lua
-- Un adaptador para un plan de suscripción (no API key): OAuth device flow
-- sí es escribible (http.request en bucle de polling + abrir URL con
-- enu.proc). Pero el flujo con callback localhost NO: no existe primitiva
-- de servidor/listener HTTP. ¿Y dónde guarda el adaptador el refresh
-- token? (¿plugins/<nombre>/? ¿en claro?) Sin convención.           [G13]
```

## Escenario 22: el repo malicioso (modelo de confianza)

```lua
-- enu se abre en un repo clonado de internet. El repo trae:
--   .enu/skills/inocente/SKILL.md   → se inyecta su índice en el system
--                                     prompt (agente §6-§7) SIN preguntar
--   .enu/agent.toml                 → ¡puede traer allow = ["bash:*"]!
--                                     (precedencia: proyecto > global)
-- Resultado: clonar un repo y abrir enu ya es ejecutar la voluntad del
-- repo. Mismo problema con descripciones de tools de servidores MCP de
-- terceros (texto no confiable inyectado al modelo). No hay modelo de
-- confianza: ni trust-on-first-use, ni qué config del repo se honra sin
-- preguntar.                                                        [G14]
```

## Escenario 23: dentro de un worker, ¿qué hay exactamente?

```lua
-- worker con task [W]: ¿el worker tiene su PROPIO scheduler/event loop?
enu.task.spawn(...)   -- ¿múltiples tasks dentro de un worker? ¿timers?
enu.task.race(...)    -- (el escenario 4 ya lo asumió para multiplexar
                     --  stream y cancelación... sin que estuviera escrito)
-- ¿Aplica watchdog dentro del worker? ¿Con qué presupuesto?        [G15]

-- Y dos subagentes paralelos editando el MISMO fichero vía proxy de
-- tools: las tools se intercalan en el principal pero nada coordina
-- escrituras al mismo path — last-write-wins silencioso.            [G16]
```

Menores anotados al pasar: rotación del fichero de `enu.log`
(→ [P20](pospuesto.md)); propiedad de los `Timer` (¿mueren con la task?
→ convención `cleanup`); restricciones de versión en `requires` (se
pliega a [P4](pospuesto.md) cuando se reabra).

---

# Ronda 5: un tercero monta orquestación de agentes

Pregunta del stress test: si la extensión oficial `agent` existe, ¿puede
**otro** plugin construir encima loops deterministas de agentes y correrlos
en paralelo, usando solo el contrato público ([agente.md](agente.md)) +
`enu.task` + `enu.worker`? Misma regla de siempre. Dos ejes que tirar de los
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
enu.events.on("agent:tool.end", function(p)
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
-- Semáforo construido SOLO con enu.task.future (como el picker construyó el
-- modal): valida otra vez que la primitiva basta.
local function semaphore(n)
  local free, waiters = n, {}
  return {
    acquire = function()                              -- ⏸
      if free > 0 then free = free - 1; return end
      local f = enu.task.future(); waiters[#waiters + 1] = f; f:await()
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
      enu.task.cleanup(sem.release)                    -- libera en éxito/error/aborto (F1)
      local ok, res = pcall(function()
        return root:spawn{                            -- task por defecto (§9)
          permissions = recortar(root.permissions, terr),   -- nunca amplía (§11)
          skills = terr.skills,
        }:run(terr.prompt)                            -- ⏸
      end)
      return { ok = ok, value = res }                 -- allSettled: jamás relanza
    end
  end
  return enu.task.all(fns)                             -- ⏸ espera a todos  [HALLAZGO G27]
end
```

Esto **es paralelismo real donde importa**: cada `spawn{}:run()` corre como
task y se suspende en su `enu.http.stream` al LLM; mientras una espera, las
otras avanzan, y las goroutines de red van de verdad en paralelo (§9, el caso
caliente de [modelo-ejecucion.md](modelo-ejecucion.md)). El `pcall` por rama
me da el *allSettled* que `task.all` no ofrece de fábrica (solo trae
fail-fast: "si una lanza, cancela el resto y relanza"). El semáforo de
`future` me da el límite de concurrencia sin API nueva.

**[HALLAZGO G27] — `enu.task.all` no promete alinear resultados con
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
--   enu.worker.spawn(...)   → NO existe dentro de un worker (P11): sin anidar.
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

**G27 — `enu.task.all` debe garantizar resultados alineados con los inputs.**
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
`compact` para loops reproducibles (§24); semáforo de `enu.task.future` para
acotar la concurrencia de un fan-out (§25); *allSettled* envolviendo cada
rama en `pcall` antes de `task.all` (§25); worktree por subagente para
aislar escrituras paralelas (§27).

---

# Ronda 6: reconstruir un harness estilo claude-code sobre `enu.ui`

Pregunta del stress test: ¿se puede montar la TUI de un harness de coding
(estilo claude-code) **entera** sobre `enu.ui` crudo + el contrato de
[chat.md](chat.md)? La respuesta corta es que `chat.md` ya *es* ese harness;
así que esta ronda no reescribe lo ya validado (transcript, modales, slash,
statusline — escenario 5 cubrió el picker modal) sino que tortura lo que
`chat.md` da por hecho: el **scrollback** del transcript, el **cursor real**
del editor multilínea, el **spinner en vivo**, y el **ratón** sobre bloques
colapsables. Ahí salen tres grietas, todas de `enu.ui` §9. Hallazgos G28-G30
al final.

## Escenario 28: las tres zonas y el scrollback del transcript

```lua
-- plugins/cc-ui/init.lua — una UI estilo coding-harness sobre enu.ui
local function layout()
  local s = enu.ui.size()
  return {
    transcript = enu.ui.region{ x = 0, y = 0,       w = s.w, h = s.h - 4 },
    input      = enu.ui.region{ x = 0, y = s.h - 4, w = s.w, h = 3,  z = 10 },
    status     = enu.ui.region{ x = 0, y = s.h - 1, w = s.w, h = 1,  z = 10 },
  }
end

-- El transcript es un Block alto (todo el historial renderizado) que se
-- "asoma" por la región vía un offset vertical. Scroll = re-blit con otro y.
local scroll, doc = 0, enu.ui.block({})           -- doc.height puede ser >> región
local function repaint_transcript(reg)
  reg:clear()
  reg:blit(0, -scroll, doc)                       -- [HALLAZGO G28] ¿blit acepta y<0?
end
enu.events.on("ui:resize", function() relayout() end)   -- G1: tu región, tu resize
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
  local wrapped = enu.text.wrap(buf, reg.w)        -- Block; .height conocido
  if wrapped.height + 1 ~= reg.h then reg:resize(reg.w, wrapped.height + 1) end
  reg:clear(); reg:blit(0, 0, wrapped)
  local cx, cy = caret_to_cell(buf, cur, reg.w)   -- enu.text.width por grafema
  reg:cursor(cx, cy)                               -- cursor real del terminal
end

enu.ui.on_input(function(ev)
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

Veredicto: sale entero. `enu.text.wrap` da la altura para crecer la caja,
`Region:cursor` coloca el caret real, y los popups `@`/`/` son el picker del
escenario 5 reutilizado. El único trabajo feo es `caret_to_cell` (índice de
byte → celda con `enu.text.width`), pero eso es del toolkit, no API que falte.
Pegar una imagen aparece aquí como **ruta** (G30, abajo).

## Escenario 30: el spinner "Thinking…" en vivo con `esc` para interrumpir

```lua
local function thinking_indicator(session)
  local t0  = enu.sys.mono_ms()
  local reg = enu.ui.region{ x = 0, y = spin_y, w = 40, h = 1 }
  local frame = 0
  local timer = enu.task.every(80, function()       -- handler síncrono, repinta
    frame = frame + 1
    local secs = math.floor((enu.sys.mono_ms() - t0) / 1000)
    local toks = providers.approx_tokens(session.usage)   -- vocabulario de producto
    reg:blit(0, 0, enu.ui.block({{
      { text = SPIN[frame % #SPIN + 1] .. " Thinking… ", style = { italic = true } },
      { text = secs .. "s · " .. toks .. " tok · esc to interrupt",
        style = { fg = "#808080" } },
    }}))
  end)
  enu.task.cleanup(function() timer:stop(); reg:destroy() end)   -- F1/F2: muere con el turno
end
-- esc → Session:cancel() (chat.md §3); el cleanup mata timer y región.
```

Veredicto: limpio. `enu.task.every` anima, `mono_ms` cuenta, `cleanup`
garantiza que el spinner muere con el turno aunque lo aborten — es el patrón
F5 (repintar coalescido, no por delta).

## Escenario 31: ratón sobre un bloque de tool colapsable (análisis)

```lua
-- Clicar la cabecera de un bloque de tool para plegarlo:
enu.ui.on_input(function(ev)
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
se montan **enteros** sobre `enu.ui` + el contrato de `chat`. La conclusión de
la pregunta que abrió la ronda se sostiene: la TUI de un harness de coding no
"sale del core" — el core da el sustrato y `chat.md` ya es ese harness. Las
únicas grietas (G28, G29) son de **ergonomía de `enu.ui`**, no de mecanismo
que falte.


---

# Ronda 7: control de razonamiento (`thinking`) por-modelo

Una zona que las rondas previas no torturaron: **pedir** razonamiento
extendido al modelo (no recibir sus bloques `thinking`, que ya se validaron —
viajan con su firma en `meta`, §2.2 —, sino el parámetro de *solicitud* del
request canónico). El disparador es real: el modelo por defecto del proyecto es
`claude-opus-4-8`, de la familia que cambió la forma de pedir razonamiento.

## Escenario 32: activar razonamiento en dos modelos con el contrato canónico

```lua
-- Un plugin (o una futura feature del agente) quiere activar razonamiento por
-- turno. Con SOLO el contrato canónico de hoy (providers.md §2.1) la única
-- forma es `thinking = { budget }`.

agent.hook("request.pre", function(req, ctx)
  req.thinking = { budget = 8000 }   -- lo único que el canónico sabe expresar
  return req
end)

-- (a) Modelo "legacy" (extended thinking con presupuesto): EXPRESABLE.
--     El adaptador anthropic traduce { budget = 8000 } -> el wire
--     { type = "enabled", budget_tokens = 8000 }, que esos modelos aceptan.

-- (b) Opus 4.6+ (claude-opus-4-8, el modelo POR DEFECTO): el MISMO código
--     produce el MISMO wire { type = "enabled", budget_tokens = 8000 } -> la API
--     real responde 400: esa familia RETIRÓ budget_tokens y espera
--     { type = "adaptive" }. El contrato canónico NO TIENE forma de pedir
--     "adaptive": no hay nada que `req.thinking` pueda llevar para expresarlo, y
--     el adaptador —traductor fiel— no puede inventar lo que el canónico calla.
--                                                                        [G34]
```

Veredicto: la rama (a) es expresable; la (b) **no**. El modelo canónico solo
sabe pedir razonamiento por *presupuesto* (`budget`), una forma que los modelos
modernos rechazan, y no ofrece un *modo* "adaptive". Es una grieta del **modelo
canónico** (no del adaptador, que cumple el contrato congelado al pie): falta
vocabulario para expresar el modo de razonamiento, y falta el **dato** de qué
forma entiende cada modelo. **[G34]**

> Nota: la grieta está **latente** hoy —el agente headless no rellena
> `req.thinking` en el ensamblado del turno (§2 paso 2), así que el 400 solo
> aparece por un hook `request.pre` como el de arriba o por una futura feature
> de control de razonamiento—. Se torturó y resolvió **antes** de cablear
> thinking para que esa feature nazca sobre un canónico ya correcto.

---

## Hallazgos (ronda 7)

**G34 — el modelo canónico de `thinking` no expresa el modo adaptativo.**
Resuelta en [ADR-016](adr.md#adr-016--modelo-canónico-de-thinking-con-mode-y-traducción-por-modelo-en-el-adaptador)
(que **reabre y cierra [P21](pospuesto.md)**, hasta hoy pospuesta): el parámetro
canónico crece por adición a `thinking = { mode?: "off"|"adaptive"|"budget",
budget? }` (con `{budget=N}` como alias compatible de `mode="budget"`), y el
**dialecto de razonamiento de cada modelo se declara como dato** en el
`providers.toml` (`thinking = "adaptive"|"budget"|"none"`), que el adaptador lee
para traducir por-modelo. El adaptador sigue siendo un traductor puro
(ADR-003/ADR-005): cero tablas de versiones de modelos en el código. Registrada
en [problemas.md](problemas.md#g34) (G34). Es la primera grieta nacida de
*usar* el binario contra la realidad de la API de un proveedor (el 400 de Opus
4.6+), no de una incompletitud interna.

---

# Ronda 8: una malla distribuida de agentes ("kubernetes de agentes")

Pregunta del stress test: si la **unidad de trabajo es la rama/worktree** y la
coordinación viaja por git (o por un broker externo), ¿puede un tercero montar
con solo el contrato público una malla de nodos `enu -e` headless que ejecutan
**specs declarativas en dos capas** (Role reutilizable + Job instancia),
soportan **fork-como-replicación** (local y entre máquinas) y colocan al humano
en las fronteras que importan (los Roles y los merges, nunca el torrente de
turnos)? La hipótesis dura de la ronda es **pull-only**: enu solo actúa de
cliente — sin listener, P1/P19 siguen dormidos. Fuera de alcance, como en la
Ronda 5: el no-determinismo del *muestreo* del modelo (territorio de
[providers.md](providers.md); el "replay adapter" del escenario 27 sigue siendo
el escape para reproducibilidad total). Hallazgos G38-G40 al final.

## Escenario 33: nodo de malla con claim por CAS de git

La spec en dos capas, como datos TOML puros. El **Role** (el *quién*) lo revisó
un humano y viaja versionado en el repo; el **Job** (el *qué*) lo estampa un
controlador en masa. La atención humana se gasta en la capa que cambia despacio.

```
roles/reviewer.toml                     jobs/J-0142.toml
-------------------                     ----------------
model = "anthropic/opus"                role   = "reviewer"
[permissions]                           base   = "9f3c1e..."   # sha pineado, NUNCA
allow = ["read", "grep", "glob",        branch = "fleet/J-0142"  # un nombre de rama
         "edit", "bash:pytest *"]       territory = ["src/parser/**"]
[budget]                                prompt = "revisa y corrige ..."
max_turns = 40
max_cost_usd = 2.0
[[skills]]
name = "review"
hash = "b52f..."    # git hash-object: cuando el sustrato es git, git es el hasher
```

```lua
-- plugins/fleet/node.lua — corre con `enu -e node.lua` en cada máquina: el motor
-- headless es gratis por diseño (agente.md §1); default deny sin supervisión (§5).
local agent = require("agent")

-- El claim es un CAS distribuido SIN servidor propio: crear una ref en el
-- remoto es atómico — si otro nodo la creó antes, el push se rechaza.
local function claim(job_id)
  local r = enu.proc.run({ "git", "push", "origin",
                          "HEAD:refs/enu/claims/" .. job_id })          -- ⏸
  return r.code == 0                               -- perdiste la carrera = code ≠ 0 ✓
end

-- Liveness cross-machine: el lock de sesiones.md §6 (pid + proc.alive) es
-- deliberadamente LOCAL — un pid no significa nada en otra máquina. El patrón
-- aquí es heartbeat: re-empujar la claim-ref con un commit {hostname, ts}
-- usando --force-with-lease (CAS otra vez: solo late quien posee el claim), y
-- re-claim por staleness con umbral generoso (relojes de pared distintos:
-- enu.sys.now_ms() no está sincronizado entre nodos). Expresable entero con
-- enu.proc; es un PATRÓN para la guía, no API que falte.

local function run_job(job, role)
  local wt = enu.fs.tmpdir() .. "/" .. job.id                           -- ⏸
  enu.proc.run({ "git", "worktree", "add", wt, job.base })              -- ⏸
  enu.task.cleanup(function()
    enu.proc.run({ "git", "worktree", "remove", "--force", wt })
  end)

  local s = agent.session{                 -- spec→opts es una FUNCIÓN PURA: todos
    model = role.model,                    -- los opts eran JSON-ables por contrato ✓
    cwd = wt,                              -- el territorio físico es el worktree
    permissions = role.permissions,        -- headless: lo no listado se deniega (§5)
    skills = skill_names(role.skills),
  }

  -- Presupuesto DURO en el driver, no en la fe: send() corre el turno entero,
  -- así que el tope de coste se vigila por eventos (atribución G3) y se corta
  -- con cancel — mismo reparto que max_turns, que ya es opt.
  local sub = enu.events.on("agent:message", function(p)
    if p.session == s.id and s.usage.cost_usd > role.budget.max_cost_usd then
      s:cancel()                                   -- P22; cierra como cancelado (§4)
    end
  end)
  enu.task.cleanup(function() sub:cancel() end)

  local msg = s:send(job.prompt)                                       -- ⏸
  commit_and_push(wt, job.branch)          -- la RAMA es el resultado; el merge es
                                           -- la puerta humana, fuera de este nodo
  attach_transcript(wt, s.id)              -- ...y la auditoría viaja con el trabajo
end

-- attach_transcript quiere comitear el JSONL de la sesión DENTRO de la rama:
-- quien revise el diff tendrá al lado el cómo se llegó a él. sesiones.md se
-- documenta como "convención pública: cualquier herramienta externa puede leer
-- sesiones" (§1), y la ruta es data_dir()/sessions/<proyecto>/<id>.jsonl con
-- "<proyecto> = cwd codificado como slug" (§2). Pero el algoritmo cwd→slug NO
-- está escrito en ningún sitio: la promesa de lectura por terceros no se puede
-- ejercer sin adivinar la codificación.                    [HALLAZGO G38]
```

Veredicto: el nodo entero — claim atómico, heartbeat, worktree, spec→sesión,
presupuesto duro, rama-resultado — se escribe con `enu.proc` + `enu.fs` +
`enu.toml` + el contrato de `agent`. Una sola grieta y es de especificación:
la ruta del transcript es inencontrable para el tercero al que el contrato
invita a leerla. **[G38]**

## Escenario 34: torneo de forks (fork-como-replicación, local)

"Replicar" un agente no es clonar un proceso — sus unidades no son fungibles,
como los pods: el valor está en el transcript. Replicar es **bifurcar una
historia**: K variantes que comparten todo el prefijo (contexto *y* caché de
prompt: con los breakpoints de P31, el fan-out cuesta marginalmente poco en
input). Torneo de salida: verificadores deterministas filtran, un juez ordena,
el humano solo fusiona.

```lua
local root = agent.session{ model = M, cwd = repo }
root:send("estudia el bug de #412 y escribe un plan")   -- ⏸ el prefijo se paga UNA vez

local NUDGES = {
  "aplica el plan minimizando el diff",
  "aplica el plan; refactoriza si simplifica",
  "descarta el plan si encuentras una vía más corta",
}

local fns = {}
for i, nudge in ipairs(NUDGES) do
  fns[i] = function()
    local v = root:fork()                  -- sesión nueva con meta.parent ✓ (sesiones §5)
    -- ...y aquí el torneo se atasca: la variante necesita SU worktree (el
    -- remedio de G16: territorio físico por rama), pero fork() no acepta opts
    -- — no hay forma de re-alojarla en otro cwd ni de recortarle permisos o
    -- cambiarle el modelo por variante. ¿Qué hereda del padre? Tampoco está
    -- escrito. El rodeo natural es cerrar y reabrir con opts efímeros (§2, G18):
    --     v:close()
    --     v = agent.session{ resume = v.id, cwd = worktree(i) }
    -- ...pero `close` aparece en la nota de estado de §2 ("implementado
    -- send/spawn/set_model/close") y NO en la firma del contrato: el rodeo se
    -- apoya en un método que oficialmente no existe.          [HALLAZGO G39]
    local msg = v:send(nudge)                                          -- ⏸
    return { id = v.id, dir = worktree(i) }
  end
end
local variants = enu.task.all(fns)          -- ⏸ K streams en paralelo real,
                                           -- resultados alineados con inputs ✓ (G27)

-- Torneo. Primera línea SIEMPRE determinista: un humano no debería ver nada
-- que una máquina podía rechazar.
local alive = {}
for i, v in ipairs(variants) do
  local t = enu.proc.run({ "pytest", "-q" }, { cwd = v.dir })           -- ⏸
  if t.code == 0 then alive[#alive + 1] = v end
end
-- Segunda línea: un juez LLM de solo lectura ordena las supervivientes.
local judge = agent.session{ model = M, permissions = { allow = { "read" } } }
local ranking = judge:send(render_diffs(alive))                        -- ⏸
-- Tercera línea: el humano fusiona la ganadora. Las perdedoras se descartan a
-- coste CERO (worktree fuera) — el slop ni siquiera llega a existir como rama.
```

El eje *rewind* del torneo (bifurcar en un punto ANTERIOR, no en la cabeza)
tropieza con lo mismo dos veces: `fork(at)` no define qué indexa `at`
(¿entrada del JSONL, mensaje, turno? — `meta.parent = {id, entry}` sugiere
entradas, pero está implícito), y para *elegir* el punto el orquestador tiene
que leer el transcript y contar — lo que vuelve a exigir localizar el fichero
(G38, segunda mordida).

Veredicto: el torneo se compone entero — fan-out de la Ronda 5, verificadores
por `enu.proc`, juez de solo lectura, humano en el merge — salvo que **fork no
re-aloja**: sin `opts` (cwd/permisos/modelo por variante) y con `at` sin unidad
definida, el fork-como-replicación se queda a un paso. **[G39]** (El plan B —
subagentes frescos con el plan en el prompt — pierde justo lo que hacía valioso
el fork: la fidelidad del prefijo compartido y su caché.)

## Escenario 35: fork distribuido — el transcript viaja en la rama

```lua
-- El nodo A dejó su transcript dentro de la rama-resultado (escenario 33). Un
-- fork-job pide continuar ESA historia en otra máquina con otro nudge:
--
--   jobs/J-0177.toml
--   ----------------
--   role = "fixer"
--   parent_branch = "fleet/J-0142"
--   parent_transcript = ".enu/transcript.jsonl"
--   fork_at = 12
--   nudge = "los tests de parser pasan; arregla ahora los de lexer"

-- Nodo B — importar una sesión ajena = COPIAR el fichero a su sitio. Esta es
-- la promesa de P9 ("el formato JSONL es la API") puesta a prueba de verdad:
local raw  = enu.fs.read(wt .. "/" .. job.parent_transcript)            -- ⏸
local meta = enu.json.decode(first_line(raw))     -- {id, cwd, created, parent?} ✓ (§3)
enu.fs.write(sessions_dir(cwd_B) .. "/" .. meta.id .. ".jsonl", raw)    -- ⏸
--          ^^^^^^^^^^^^^^^^^^^
--          otra vez: ¿cómo se llama el directorio del proyecto? El slug de
--          sesiones.md §2 sin especificar, tercera mordida.       [G38]

-- Reabrir y bifurcar. El replay (§3) no se inmuta: meta.cwd apunta a una ruta
-- de A que aquí no existe, pero es metadato, no estado que se re-ejecute ✓.
-- El lock tampoco estorba: el .lock de A no viajó (nadie lo comitea), y si el
-- directorio llegara sincronizado con un lock ajeno, §6 ya lo contempla:
-- hostname distinto → "no se puede verificar: se pregunta, nunca se asume" ✓.
local parent = agent.session{ resume = meta.id, cwd = wt }   -- adquiere el lock (§6)
local v = parent:fork(job.fork_at)               -- ¿fork_at cuenta entradas? [G39]
-- Roce anotado sin elevarlo: B abre el padre COMO ESCRITOR (resume) solo para
-- bifurcarlo — un "fork de solo lectura" no existe. Aquí es inocuo (nadie más
-- escribe ese fichero en B), pero el lock sobra conceptualmente. Si G39 acaba
-- en fork(at, opts), la pareja resume-para-forkear merece una línea en la guía.
```

Veredicto: el fork distribuido **es** copiar un fichero — P9 sale reforzada de
su primer test real: ni el replay, ni los locks, ni los ids (timestamp+azar,
sin estado de máquina) ponen pega alguna. Las dos muescas son las ya abiertas:
localizar el directorio destino (G38) y la semántica de `fork(at)` (G39).
Ninguna nueva — buena señal para el formato.

## Escenario 36: broker de contraste + la denegación como dato

```lua
-- (a) El MISMO nodo sobre el otro sustrato: un broker al que enu se conecta
-- SALIENTE (pull-only se mantiene; P1/P19 siguen dormidos).
local ws = enu.ws.connect("wss://broker.example/fleet")                 -- ⏸
while true do
  local job = enu.json.decode(ws:recv())          -- ⏸ claim y liveness: del broker
  local result = run_job(job.spec, job.role)     -- ⏸ el run_job del escenario 33,
  ws:send(enu.json.encode(result))                -- ⏸ SIN TOCAR: la capa Role/Job
end                                              --   no sabe en qué sustrato viaja ✓
-- Reparto honesto: git puro paga claim+liveness con CAS+heartbeat pero no
-- añade infraestructura; el broker los regala pero es una pieza más que operar.
-- Que run_job sea idéntico en ambos es el dato que importa: la spec es
-- agnóstica al sustrato.

-- (b) El escalado asíncrono de permisos: default deny COMO MECANISMO. El
-- modelo pide `bash:npm install`; el Role no lo lista; headless deniega con el
-- error accionable de §5 ("añade allow = [\"bash:npm *\"]"). El controlador
-- quiere convertir eso en una enmienda del Role que un humano aprueba y un
-- re-run barato (el job es idempotente: sha pineado). ¿Cómo recoge QUÉ se
-- denegó? Torturemos las tres vías:

enu.events.on("agent:permission.asked", function(p) end)
-- ✗ no aplica: asked es el flujo interactivo; el deny de política ni pregunta

agent.hook("permission", function(p) end)
-- ✗ tampoco: el pipeline es deny → allow → hooks (§5) — un deny de la lista
--   corta la cadena ANTES de llegar a los hooks; para ellos es invisible

enu.events.on("agent:tool.end", function(p) end)
-- ✗ sin especificar siquiera si se emite para una call denegada (su handler
--   nunca corrió), y su payload no lleva el patrón denegado

-- Queda leer el transcript: el tool_result con is_error lleva la prosa
-- accionable — PERFECTA para un humano, inservible para un controlador, que
-- acaba parseando prosa en busca del patrón. La denegación necesita viajar
-- como DATO.                                               [HALLAZGO G40]
```

Veredicto: el contraste confirma que la capa declarativa no depende del
sustrato, y el bucle deny → enmienda del Role → re-run idempotente es el
human-in-the-loop asíncrono que la malla necesitaba — la fricción del default
deny convertida en el mecanismo de escalado, sin transporte nuevo. Le falta un
solo dato: el patrón denegado en forma estructurada. **[G40]**

---

## Hallazgos (ronda 8)

**G38 — el slug de proyecto de `sessions/<proyecto>/` no está especificado.**
[sesiones.md](sesiones.md) §1 promete que "cualquier herramienta externa puede
leer sesiones", pero §2 codifica el directorio como "slug del cwd" sin escribir
el algoritmo: la promesa no se puede ejercer. La ronda lo necesitó tres veces
(comitear el transcript en la rama, contar entradas para elegir el punto de
fork, importar una sesión ajena). **Resuelto**: el algoritmo pasa a ser parte
del formato ([sesiones.md](sesiones.md) §2, congelado tal cual con sus
propiedades — legible, con pérdida, clave de agrupación y no identidad) y la
extensión lo expone como `sessions.slug/dir`; detalle y contraindicaciones en
[problemas.md](problemas.md#g38).

**G39 — `Session:fork` no re-aloja: sin `opts` y con `at` sin unidad definida.**
Fork-como-replicación exige worktree (cwd) propio por variante — el remedio de
G16 — y a veces permisos/modelo distintos; `fork(at?)` no acepta opts, no
documenta qué hereda, y el rodeo (close + `resume` con opts efímeros) se apoya
en un `close` que la firma del contrato omite. Además `at` no define qué indexa
(la unidad de `meta.parent.entry` está implícita). **Resuelto**:
`fork(at?, opts?)` (opts efímeros, permisos solo recortan) y `close()` entran
en el contrato ([agente.md](agente.md) §2), `at` indexa el historial de
mensajes vigente, la herencia queda especificada completa, y se bendice la
copia del prefijo — la hija autocontenida hace viajar los transcripts
([sesiones.md](sesiones.md) §5). Detalle en [problemas.md](problemas.md#g39).

**G40 — las denegaciones de permisos no son observables como dato.**
El deny de política corta antes de los hooks `permission`, `permission.asked`
es solo del flujo interactivo, `tool.end` no especifica si se emite para calls
denegadas, y el error accionable de §5 es prosa. Un orquestador headless no
puede convertir denegaciones en enmiendas de Role sin parsear texto.
**Resuelto**: toda denegación produce un objeto estructurado
(`{ id, tool, args?, source, pattern?, suggested? }`) con dos destinos — el
evento `agent:permission.denied` para observadores vivos y el `meta` del
`tool_result` para que la denegación viaje con el transcript —, y `tool.end`
queda especificado también para denegaciones ([agente.md](agente.md) §4/§5).
Detalle en [problemas.md](problemas.md#g40).

Confirmaciones (sin API nueva): el **claim distribuido** es un push atómico de
ref y el heartbeat un `--force-with-lease` — CAS dos veces, todo con `enu.proc`
(§33); el **presupuesto duro** se vigila desde el driver con eventos (G3) +
`Session:cancel`, mismo reparto que `max_turns` (§33); la spec **Role/Job es
agnóstica al sustrato** — el mismo `run_job` corre sobre git puro y sobre un
broker `enu.ws` saliente, y la hipótesis pull-only aguanta la ronda entera sin
despertar P1/P19 (§36); el **fork distribuido es copiar un fichero** — P9 ("el
formato es la API") sale reforzada de su primer test real (§35); y el torneo
valida la **pirámide anti-slop**: verificadores deterministas → juez de solo
lectura → humano únicamente en el merge, con las variantes perdedoras
descartadas a coste cero (§34).

Patrones para la guía (sin cambio de API): claim por creación atómica de ref +
heartbeat con lease + re-claim por staleness con umbral generoso (relojes no
sincronizados) (§33); spec en dos capas con skills pineadas por hash (`git
hash-object` como hasher de la casa cuando el sustrato es git) (§33); tope de
coste en el driver por `agent:message` + `cancel` (§33); torneo
determinista-primero sobre worktrees desechables (§34); transcript comiteado en
la rama-resultado para que la auditoría viaje con el trabajo (§33/§35);
denegación → enmienda de Role → re-run idempotente como human-in-the-loop
asíncrono (§36).
