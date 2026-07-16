---
title: "Ronda 8: una malla distribuida de agentes (\"kubernetes de agentes\")"
type: "ronda"
id: "ronda-8"
zone: "una malla distribuida de agentes (\"kubernetes de agentes\")"
status: "cerrada"
scenarios: [33, 34, 35, 36]
findings: ["G38", "G39", "G40"]
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
