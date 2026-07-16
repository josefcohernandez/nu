---
title: "Ronda 2: los caminos feos"
type: "ronda"
id: "ronda-2"
zone: "los caminos feos"
status: "cerrada"
scenarios: [7, 8, 9, 10, 11]
findings: ["F1", "F2", "F3", "F4", "F5"]
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
