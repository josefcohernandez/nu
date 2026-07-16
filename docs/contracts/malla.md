---
title: "La extensión oficial de la malla (`mesh`): contrato"
description: "Contrato de la extensión oficial mesh (borrador v0.1; su §11 sigue abierta)."
type: "contrato"
layer: "contracts"
web: "none"
status: "borrador"
---
# La extensión oficial de la malla (`mesh`): contrato

Estado: **borrador para discusión — v0.1 construida**. Nace de la [Ronda 8 de
pseudocódigo](../validation/README.md) ("kubernetes de agentes"): una malla de nodos
`enu` headless que ejecutan trabajos declarativos sobre ramas de git, con el
humano en las dos fronteras que importan (los Roles y los merges). Como el
resto de extensiones oficiales, NO es API sagrada del core: es el contrato
público del plugin `mesh`, versionado aparte, construido **íntegramente** sobre
[api.md](api.md), [agente.md](agente.md) y [sesiones.md](sesiones.md) — si algo
de aquí no se puede implementar con esas superficies, son ellas las que están
incompletas (ADR-003). La Ronda 8 validó exactamente eso antes de construir
(G38-G40, resueltas).

> ⚠ **Decisiones pendientes de aprobación**: ver §11. Esta v0.1 toma varias
> decisiones provisionales anotadas allí; ninguna es irreversible.

## 1. Decisiones estructurales

1. **Primitivas componibles, no un daemon.** `mesh` ofrece las piezas (specs,
   claim, worktrees, runner, torneo); el bucle de polling de un nodo es un
   script del usuario (§10 trae el patrón). Un daemon con su ciclo de vida
   sería producto encima del producto — cuando duela escribirlo a mano, se
   reabre.
2. **Pull-only.** enu solo actúa de **cliente** (git, y en el futuro `enu.ws`
   saliente): sin listener, [P1/P19](../postponed/pospuesto.md) siguen dormidos.
3. **Git es el único sustrato v0.1**: transporte, almacén y coordinación
   (claim por CAS de refs). La Ronda 8 (escenario 36) validó que la capa
   Role/Job es agnóstica al sustrato; el broker queda pospuesto con disparador
   (§12).
4. **Fuera del conjunto oficial de producto** (ADR-015): `mesh` viaja embebida
   en el binario pero ni el onramp ni la pantalla de runtime desnudo la
   activan — es una herramienta de orquestación, no el harness por defecto.
   Se activa explícitamente (`plugins.enabled` += `"mesh"`).

## 2. Las specs: Role + Job (datos TOML, dos capas)

La atención humana se gasta en la capa que cambia despacio. El **Role** (el
*quién*) lo revisa un humano y se versiona en el repo; el **Job** (el *qué*) se
estampa en masa (por un humano, un script o un agente — la autocreación emite
*manifiestos*, nunca código).

```toml
# roles/reviewer.toml                  # jobs/J-0142.toml
model = "anthropic/opus"               id     = "J-0142"
system = "..."                         # opc.  role   = "roles/reviewer.toml"  # ruta relativa al repo
thinking = "adaptive"                  # opc.  base   = "9f3c1e..."   # sha PINEADO, nunca una rama
                                       branch = "mesh/J-0142" # la rama-resultado
[permissions]                          prompt = "revisa y corrige ..."
mode  = "ask"                          territory = ["src/parser/**"]  # opc., informativo (G16:
allow = ["read", "grep", "glob",       #        el reparto de territorio va por prompt)
         "edit", "bash:pytest *"]
deny  = ["bash:rm *"]                  [fork]                 # opc.: fork-job (§8)
                                       parent_transcript = ".enu/mesh/transcript.jsonl"
[budget]                               at    = 12
max_turns    = 40                      nudge = "arregla ahora los tests de lexer"
max_cost_usd = 2.0

[[skills]]
name = "review"
hash = "b52f..."   # git hash-object del SKILL.md; pineada = aprobada (§9)
```

```
mesh.role.load(path) ⏸ -> Role     -- valida; EMESH accionable si falta un campo
mesh.job.load(path) ⏸ -> Job       -- ídem (id, base, branch y prompt son obligatorios)
mesh.to_session_opts(role, job) -> tabla   -- función PURA spec→opts de agent.session
```

`to_session_opts` no toca disco ni red: mismo Role + mismo Job → mismos opts.
El `mode`/`allow`/`deny` del Role van tal cual a la sesión; en un nodo headless
el default deny de [agente.md](agente.md) §5 hace el resto — **la denegación es
el mecanismo de escalado**, no un fallo (§7).

## 3. Claim y liveness (CAS por refs de git)

Crear una ref en el remoto es **atómico**: dos nodos que reclaman el mismo job
empujan la misma ref y solo uno gana. Sin servidor propio.

```
mesh.claim(job_id, opts?) ⏸ -> boolean      -- crea refs/enu/mesh/claims/<id>; false = carrera perdida
mesh.heartbeat(job_id, opts?) ⏸ -> boolean  -- re-empuja la claim-ref (--force-with-lease: solo
                                            --   late quien la posee); false = te la robaron
mesh.claim_info(job_id, opts?) ⏸ -> { hostname, ts }?  -- nil si no hay claim
mesh.release(job_id, opts?) ⏸               -- borra la claim-ref (job terminado o abandonado)
```

- `opts`: `{ cwd?, remote? = "origin" }`. Todo vía `enu.proc.run(["git", ...])`;
  git es dependencia declarada de la extensión, no del core.
- El contenido del commit de claim/heartbeat es `{ hostname, ts }`
  (`enu.sys.hostname/now_ms`). **Los relojes de los nodos no están
  sincronizados**: el umbral de staleness que un re-claimer aplique sobre
  `claim_info().ts` debe ser generoso (minutos, no segundos). El lock local de
  [sesiones.md](sesiones.md) §6 (pid + `proc.alive`) no cruza máquinas — aquí
  la liveness es el heartbeat, deliberadamente.
- Robar un claim viejo = `release` + `claim` (el CAS arbitra si dos re-claimers
  compiten).

## 4. Worktrees (el territorio físico)

```
mesh.worktree.add(base, opts?) ⏸ -> dir   -- git worktree add <tmp>/<...> <base> (sha pineado)
mesh.worktree.remove(dir, opts?) ⏸        -- git worktree remove --force
```

Un worktree por job y por variante de torneo: el remedio de G16 (last-write-wins
entre escritores paralelos) es repartir territorio físico. `dir` por defecto
bajo `enu.fs.tmpdir()`.

## 5. El runner: `mesh.run_job`

```
mesh.run_job(job, role, opts?) ⏸ -> Result
  opts: { cwd? (repo), remote?, keep_worktree? = false }
  Result = { ok: boolean, job_id, branch?, usage?, denials: Denial[],
             error?: { code, message } }
```

Pasos (todo sobre contratos públicos; cada paso es sustituible componiendo las
piezas de §2-§4 a mano):

1. Worktree desde `job.base` (§4) con `cleanup` garantizado.
2. **Verificación de skills pineadas** (§9): el hash de cada skill del Role se
   comprueba contra el worktree (`git hash-object`); mismatch → `EMESH`
   accionable y el job falla ANTES de abrir sesión.
3. Sesión desde `to_session_opts` con `cwd = worktree` (motor headless,
   agente.md §1).
4. **Presupuesto duro en el driver**: `max_turns` va en los opts; el tope
   `max_cost_usd` se vigila con `agent:message` + `Session:cancel` (posible
   desde la fase A: `usage.cost_usd` se acumula con la tarifa del
   providers.toml).
5. **Denegaciones como dato** (G40): el runner se suscribe a
   `agent:permission.denied` y devuelve la lista en `Result.denials` — cada una
   con su `suggested`, el dato del bucle de escalado (denegación → un humano
   enmienda el Role → re-run barato: el job es idempotente, `base` es un sha).
6. Turno(s): `send(job.prompt)` — o el flujo de fork si `job.fork` (§8).
7. **La rama es el resultado y la auditoría viaja con ella**: commit del
   worktree + el transcript de la sesión (localizado con `sessions.dir`, G38)
   copiado a `.enu/mesh/transcript.jsonl` + `Result` serializado a
   `.enu/mesh/result.json` → push a `job.branch`. Un controlador remoto lee la
   rama y extrae denials/usage/error **sin parsear prosa**.

Un fallo en cualquier paso no lanza hacia fuera: `Result.ok = false` con
`error` estructurado (*allSettled* por diseño: en un fan-out, un job caído no
mata a los demás — pseudocódigo, escenario 25).

## 6. El torneo de forks: `mesh.tournament`

Fork-como-replicación (Ronda 8, escenario 34; posible desde G39): K variantes
que comparten el prefijo exacto del transcript, cada una en su worktree.

```
mesh.tournament{ session, variants, at?, verify?, limit? } ⏸ -> Outcome[]
  variants: { { nudge, cwd, opts? }, ... }
  at?:      punto de fork (índice de mensaje; default: la cabeza)
  verify?:  function(dir, outcome) ⏸ -> boolean   -- verificador DETERMINISTA (tests)
  limit?:   concurrencia máxima (default: #variants)
  Outcome = { ok, message?, error?, verified?, session_id, dir }
```

- Cada variante es `session:fork(at, { cwd = v.cwd, ... })` + `send(v.nudge)`,
  en paralelo real (tasks; los streams se solapan), con semáforo si `limit`.
- Resultados **alineados con `variants`** (G27) y *allSettled* (un fallo no
  cancela a las hermanas).
- `verify` corre tras cada variante (pirámide anti-slop: nadie humano debería
  ver lo que una máquina podía rechazar). El **juez** y el **merge** quedan
  fuera a propósito: el juez es otra sesión (componible); el merge es la
  puerta humana.

## 7. Permisos y escalado asíncrono

`mesh` no añade capa de permisos propia: usa la del agente tal cual. El Role es
un allowlist declarado y auditable; el nodo corre headless con default deny
(agente.md §5); lo no listado se deniega **con dato** (G40) y vuelve en
`Result.denials` y en el `result.json` de la rama. El operador enmienda el
Role (un fichero versionado que revisa un humano) y relanza. Ninguna
denegación bloquea el nodo: no hay asks colgados en headless.

## 8. Fork-jobs (fork distribuido)

Un job con tabla `[fork]` continúa una historia empezada en otro nodo
(escenario 35): el runner lee `fork.parent_transcript` **del worktree** (viajó
en la rama padre), lo importa copiándolo a `sessions.dir(cwd)` (G38; el formato
es la API, P9), reabre con `resume`, bifurca con `fork(fork.at, {cwd=worktree})`
(G39) y envía `fork.nudge`. La hija es autocontenida (sesiones.md §5), así que
la nueva rama vuelve a llevar TODO su linaje.

## 9. Confianza: el hash es la aprobación

El TOFU interactivo de agente.md §11.2 no existe en headless (sin respuesta no
se inyecta contenido del repo). La malla lo sustituye por algo **más fuerte**:
las skills del Role van **pineadas por hash de contenido**, y ese pin lo
escribió el humano que revisó el Role. Si el hash del worktree coincide, el
runner marca el worktree como confiado (`agent.trust.set(dir, true)`) **solo
para ese job**; si no coincide, `EMESH` y el job muere antes de abrir sesión.
Un Role **sin** skills pineadas no confía nada: el `enu.md` y las skills del
repo no se inyectan (el default headless de §11.2 se mantiene).

## 10. El nodo, como patrón (no API)

```lua
-- node.lua — corre con `enu -e node.lua` en cada máquina
local mesh = require("mesh")
while true do
  for _, jf in ipairs(enu.search.files(JOBS_DIR, { glob = "*.toml" })) do
    local job = mesh.job.load(jf)
    if mesh.claim(job.id) then                       -- CAS: solo un nodo gana
      local hb = enu.task.every(60000, function()
        enu.task.spawn(function() mesh.heartbeat(job.id) end)
      end)
      local role = mesh.role.load(repo_path(job.role))
      local r = mesh.run_job(job, role)              -- allSettled: nunca lanza
      hb:stop(); mesh.release(job.id)
      log_result(r)                                  -- r.denials → enmiendas de Role
    end
  end
  enu.task.sleep(POLL_MS)
end
```

## 11. Decisiones pendientes de aprobación (anotadas, no cerradas)

| # | Decisión provisional de la v0.1 | Alternativa / pregunta abierta |
|---|---|---|
| D1 | Nombre `mesh` (identificadores en inglés; "malla" en la prosa) | `fleet` evocaría "flota de trabajadores"; renombrar es barato hoy |
| D2 | Fuera del conjunto oficial de producto (§1.4) | ¿Debería el onramp ofrecerla como extra de una tecla? |
| D3 | Convenciones git: `refs/enu/mesh/claims/<id>` y `.enu/mesh/{transcript.jsonl,result.json}` en la rama | Nombres alternativos; ¿un `refs/enu/mesh/results/<id>` además de la rama? |
| D4 | El hash pineado sustituye al TOFU (§9): `trust.set` programático por worktree | ¿Exigir además una firma/allowlist de hashes global del operador? |
| D5 | El bucle del nodo es patrón, no API (§10); el broker WS queda pospuesto | Disparador de reapertura: una malla real donde el polling de git duela |
| D6 | El controlador que estampa jobs queda fuera de v0.1 (los escribe un humano/script) | ¿Merece `mesh.job.emit(spec)` que valide y comitee el TOML? |
| D7 | `source = "user"` añadido al enum de G40 (el rechazo interactivo también es dato) | Ya aplicado en agente.md §5; revertir si se prefiere el enum original |
| D8 | La clave del meta del tool_result denegado es `denied` | Ya aplicado en agente.md §5 |
| D9 | ~~Candidato a grieta del kernel: escrituras de handlers a upvalues locales de tasks suspendidas se perdían~~ | **DECIDIDA Y RESUELTA como [G41](../findings/g41-un-error-capturado-por-pcall.md)**: era un bug de gopher-lua (el desenrollado de `pcall` cerraba upvalues de frames vivos), blindado en el kernel — la semántica de Lua vuelve a ser la estándar, sin limitación que documentar |

## 12. Relación con lo pospuesto

- **Broker como segundo sustrato** (`enu.ws` saliente): validado como expresable
  (Ronda 8, escenario 36); se construirá cuando exista una malla real donde el
  polling de git no baste.
- **Tool calls paralelas** ([P12](../postponed/pospuesto.md)): un job sigue siendo secuencial
  por dentro; el paralelismo de la malla es entre jobs/variantes.
- **Workers anidados** ([P11](../postponed/pospuesto.md)): irrelevante aquí — la carga es
  LLM+IO y se solapa entre tasks (escenario 26).
