---
title: "Ronda 5: un tercero monta orquestación de agentes"
type: "ronda"
id: "ronda-5"
zone: "un tercero monta orquestación de agentes"
status: "cerrada"
scenarios: [24, 25, 26, 27]
findings: ["G27"]
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
