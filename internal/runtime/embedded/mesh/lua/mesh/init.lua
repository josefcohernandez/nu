-- Módulo público de la extensión `mesh` (contrato: docs/malla.md).
--
-- Las piezas de la malla, componibles y sin daemon:
--   §2 specs Role+Job (TOML, dos capas) y la función PURA spec→opts;
--   §3 claim/heartbeat/release por CAS de refs de git (crear una ref en el
--      remoto es atómico: sin servidor de coordinación);
--   §4 worktrees (el territorio físico por job/variante, remedio de G16);
--   §5 el runner `run_job` (presupuesto duro, denegaciones como dato G40,
--      la rama como resultado con transcript+result.json a bordo, G38);
--   §6 el torneo de forks `tournament` (fork-como-replicación, G39);
--   §8 fork-jobs (continuar en esta máquina una historia empezada en otra:
--      el transcript viaja en la rama; el formato es la API, P9).
--
-- Código de error de la extensión: `EMESH` (forma de api.md §1.4 / ADR-009),
-- más los reusados (`EINVAL`). git es dependencia declarada de ESTA extensión
-- (se invoca vía enu.proc, sin shell), nunca del core.

local agent = require("agent")
local sessions = require("sessions")

local M = {}

-- ---------------------------------------------------------------------------
-- Errores estructurados (EMESH, api.md §1.4).
-- ---------------------------------------------------------------------------

local function emesh(message, detail)
  error({ code = "EMESH", message = message, detail = detail })
end

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- ---------------------------------------------------------------------------
-- git vía enu.proc (§3-§5). Sin shell: argv explícito (api.md §6).
-- ---------------------------------------------------------------------------

local function git(argv, opts)
  local full = { "git" }
  for _, a in ipairs(argv) do full[#full + 1] = a end
  return enu.proc.run(full, { cwd = opts and opts.cwd })
end

local function git_ok(argv, opts, what)
  local r = git(argv, opts)
  if r.code ~= 0 then
    emesh(string.format("git falló en %s: %s", what, (r.stderr or ""):gsub("%s+$", "")),
      { argv = argv, code = r.code })
  end
  return r
end

local function remote_of(opts)
  return (opts and opts.remote) or "origin"
end

local function trim(s)
  return (s or ""):gsub("%s+$", ""):gsub("^%s+", "")
end

-- ---------------------------------------------------------------------------
-- §2 Specs: Role + Job. Validación accionable; `to_session_opts` es PURA.
-- ---------------------------------------------------------------------------

local function load_toml(path, what)
  local ok, raw = pcall(enu.fs.read, path)
  if not ok then
    emesh(string.format("no se pudo leer el %s %q: %s", what, path,
      (type(raw) == "table" and raw.message) or tostring(raw)))
  end
  local ok2, spec = pcall(enu.toml.decode, raw)
  if not ok2 then
    emesh(string.format("el %s %q no es TOML válido: %s", what, path,
      (type(spec) == "table" and spec.message) or tostring(spec)))
  end
  return spec
end

M.role = {}

-- mesh.role.load(path) ⏸ -> Role (malla.md §2). El Role es el QUIÉN: lo revisó
-- un humano y se versiona en el repo.
function M.role.load(path)
  local role = load_toml(path, "Role")
  if type(role.model) ~= "string" or role.model == "" then
    emesh(string.format("el Role %q no declara `model` (\"proveedor/modelo\"), obligatorio (malla.md §2)", path))
  end
  for _, s in ipairs(role.skills or {}) do
    if type(s.name) ~= "string" or type(s.hash) ~= "string" or s.hash == "" then
      emesh(string.format("el Role %q pina una skill sin `name`+`hash` — el hash ES la aprobación (malla.md §9)", path))
    end
  end
  return role
end

M.job = {}

-- mesh.job.load(path) ⏸ -> Job (malla.md §2). El Job es el QUÉ: barato, en masa.
function M.job.load(path)
  local job = load_toml(path, "Job")
  for _, field in ipairs({ "id", "base", "branch" }) do
    if type(job[field]) ~= "string" or job[field] == "" then
      emesh(string.format("el Job %q no declara `%s`, obligatorio (malla.md §2)", path, field))
    end
  end
  if type(job.fork) == "table" then
    if type(job.fork.parent_transcript) ~= "string" or type(job.fork.nudge) ~= "string" then
      emesh(string.format("el Job %q trae [fork] sin `parent_transcript`+`nudge` (malla.md §8)", path))
    end
  elseif type(job.prompt) ~= "string" or job.prompt == "" then
    emesh(string.format("el Job %q no declara `prompt` (obligatorio salvo fork-job, malla.md §2)", path))
  end
  return job
end

-- mesh.to_session_opts(role, job) -> tabla (malla.md §2). PURA: sin disco ni
-- red; mismo Role+Job → mismos opts. El cwd (worktree) lo añade el runner.
function M.to_session_opts(role, job)
  if type(role) ~= "table" or type(job) ~= "table" then
    einval("mesh.to_session_opts espera (role, job) como tablas")
  end
  local budget = role.budget or {}
  local skills = nil
  if type(role.skills) == "table" and #role.skills > 0 then
    skills = {}
    for _, s in ipairs(role.skills) do skills[#skills + 1] = s.name end
  end
  return {
    model       = role.model,
    system      = role.system,
    thinking    = role.thinking,
    permissions = role.permissions,
    max_turns   = budget.max_turns,
    skills      = skills,
  }
end

-- ---------------------------------------------------------------------------
-- §3 Claim y liveness: CAS por refs. La claim-ref apunta a un commit sobre el
-- árbol vacío cuyo MENSAJE es el JSON { hostname, ts } (la identidad viaja en
-- el objeto, no hace falta rama). Relojes no sincronizados: umbrales generosos.
-- ---------------------------------------------------------------------------

local CLAIM_PREFIX = "refs/enu/mesh/claims/"

-- El sha del árbol vacío es una constante de git; commit-tree sobre él fabrica
-- el commit-baliza sin tocar el índice ni el working tree.
local EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

local function claim_commit(opts)
  local info = enu.json.encode({ hostname = enu.sys.hostname(), ts = enu.sys.now_ms() })
  local r = git_ok({ "-c", "user.email=mesh@enu", "-c", "user.name=enu-mesh",
    "commit-tree", EMPTY_TREE, "-m", info }, opts, "commit-tree del claim")
  return trim(r.stdout)
end

local function ls_remote_sha(ref, opts)
  local r = git_ok({ "ls-remote", remote_of(opts), ref }, opts, "ls-remote")
  local sha = r.stdout:match("^(%x+)%s")
  return sha
end

-- mesh.claim(job_id, opts?) ⏸ -> boolean. true = el claim es tuyo; false = otro
-- nodo ganó la carrera (la ref ya existía). Cualquier otro fallo lanza EMESH.
function M.claim(job_id, opts)
  if type(job_id) ~= "string" or job_id == "" then einval("mesh.claim espera un job_id") end
  local ref = CLAIM_PREFIX .. job_id
  local sha = claim_commit(opts)
  local r = git({ "push", "--quiet", remote_of(opts), sha .. ":" .. ref }, opts)
  if r.code == 0 then
    return true
  end
  -- ¿Carrera perdida o fallo real? La ref existente distingue.
  if ls_remote_sha(ref, opts) ~= nil then
    return false
  end
  emesh(string.format("el push del claim %q falló sin carrera: %s", job_id, trim(r.stderr)))
end

-- mesh.heartbeat(job_id, opts?) ⏸ -> boolean. Re-empuja la claim-ref con lease
-- (CAS otra vez: solo late quien la posee). false = te la robaron o no existe.
function M.heartbeat(job_id, opts)
  local ref = CLAIM_PREFIX .. job_id
  local cur = ls_remote_sha(ref, opts)
  if cur == nil then
    return false
  end
  local sha = claim_commit(opts)
  local r = git({ "push", "--quiet", "--force-with-lease=" .. ref .. ":" .. cur,
    remote_of(opts), sha .. ":" .. ref }, opts)
  return r.code == 0
end

-- mesh.claim_info(job_id, opts?) ⏸ -> { hostname, ts }? — nil sin claim. Para
-- decidir staleness (con umbral GENEROSO: los relojes de los nodos difieren).
function M.claim_info(job_id, opts)
  local ref = CLAIM_PREFIX .. job_id
  local sha = ls_remote_sha(ref, opts)
  if sha == nil then
    return nil
  end
  git({ "fetch", "--quiet", remote_of(opts), ref }, opts) -- trae el objeto si falta
  local r = git({ "cat-file", "-p", sha }, opts)
  if r.code ~= 0 then
    return nil
  end
  local msg = r.stdout:match("\n\n(.*)$") or ""
  local ok, info = pcall(enu.json.decode, trim(msg))
  if ok and type(info) == "table" then
    return info
  end
  return nil
end

-- mesh.release(job_id, opts?) ⏸ -> boolean. Borra la claim-ref (job terminado o
-- abandonado). Best-effort: false si no se pudo (p. ej. ya no existe).
function M.release(job_id, opts)
  local ref = CLAIM_PREFIX .. job_id
  local r = git({ "push", "--quiet", remote_of(opts), ":" .. ref }, opts)
  return r.code == 0
end

-- ---------------------------------------------------------------------------
-- §4 Worktrees: el territorio físico (remedio de G16).
-- ---------------------------------------------------------------------------

M.worktree = {}

-- mesh.worktree.add(base, opts?) ⏸ -> dir. Worktree desanclado sobre el sha
-- PINEADO (idempotencia del job: nunca un nombre de rama).
function M.worktree.add(base, opts)
  if type(base) ~= "string" or base == "" then einval("mesh.worktree.add espera un sha base") end
  local dir = (opts and opts.dir)
    or (enu.fs.tmpdir() .. "/mesh-" .. base:sub(1, 12) .. "-" .. tostring(enu.sys.now_ms()))
  git_ok({ "worktree", "add", "--detach", dir, base }, opts, "worktree add")
  return dir
end

-- mesh.worktree.remove(dir, opts?) ⏸ -> boolean. Best-effort.
function M.worktree.remove(dir, opts)
  local r = git({ "worktree", "remove", "--force", dir }, opts)
  return r.code == 0
end

-- ---------------------------------------------------------------------------
-- §9 Confianza: el hash es la aprobación. Devuelve true si el Role pina skills
-- (todas verificadas); lanza EMESH al primer mismatch — ANTES de abrir sesión.
-- ---------------------------------------------------------------------------

local function verify_skills(role, wt)
  local pinned = role.skills or {}
  for _, s in ipairs(pinned) do
    local path = wt .. "/.enu/skills/" .. s.name .. "/SKILL.md"
    local r = git({ "hash-object", path }, { cwd = wt })
    if r.code ~= 0 then
      emesh(string.format("la skill pineada %q no existe en el worktree (%s)", s.name, path))
    end
    local h = trim(r.stdout)
    if h ~= s.hash then
      emesh(string.format(
        "la skill %q no coincide con su pin: worktree %s ≠ role %s — el contenido cambió sin re-aprobación (malla.md §9)",
        s.name, h, s.hash))
    end
  end
  return #pinned > 0
end

-- ---------------------------------------------------------------------------
-- §8 Fork-jobs: importar = copiar el JSONL a su sitio (P9: el formato es la
-- API; G38: sessions.dir localiza el destino), resume + fork(at, {cwd}) (G39).
-- ---------------------------------------------------------------------------

local function open_fork_session(job, role, wt, sopts)
  local f = job.fork
  local traw = enu.fs.read(wt .. "/" .. f.parent_transcript)
  local first_line = traw:match("([^\n]*)")
  local ok, meta = pcall(enu.json.decode, first_line)
  if not ok or type(meta) ~= "table" or type(meta.id) ~= "string" then
    emesh(string.format("el transcript padre %q no empieza por una línea `meta` válida (sesiones.md §3)",
      f.parent_transcript))
  end
  local dest = sessions.dir(wt)
  enu.fs.mkdir(dest)
  enu.fs.write(dest .. "/" .. meta.id .. ".jsonl", traw)

  local popts = {}
  for k, v in pairs(sopts) do popts[k] = v end
  popts.cwd = wt
  popts.resume = meta.id
  local parent = agent.session(popts)
  local child = parent:fork(f.at, { cwd = wt })
  parent:close()
  return child, f.nudge
end

-- ---------------------------------------------------------------------------
-- §5 El runner. allSettled por diseño: NUNCA lanza — Result.ok=false con error
-- estructurado (en un fan-out, un job caído no mata a los demás).
-- ---------------------------------------------------------------------------

-- attach_and_push: la rama ES el resultado y la auditoría viaja con ella — el
-- transcript (localizado vía sessions.dir, G38) y el result.json (denials/usage,
-- para que el controlador remoto lea DATOS, no prosa) van a bordo.
local function attach_and_push(s, wt, job, result, opts)
  local tpath = sessions.dir(wt) .. "/" .. s.id .. ".jsonl"
  enu.fs.mkdir(wt .. "/.enu/mesh")
  if enu.fs.stat(tpath) ~= nil then
    enu.fs.copy(tpath, wt .. "/.enu/mesh/transcript.jsonl")
  end
  enu.fs.write(wt .. "/.enu/mesh/result.json", enu.json.encode({
    job_id  = job.id,
    denials = result.denials,
    usage   = result.usage,
  }))
  git_ok({ "add", "-A" }, { cwd = wt }, "add del resultado")
  git_ok({ "-c", "user.email=mesh@enu", "-c", "user.name=enu-mesh",
    "commit", "--allow-empty", "-m", "mesh: resultado de " .. job.id },
    { cwd = wt }, "commit del resultado")
  git_ok({ "push", "--quiet", remote_of(opts), "HEAD:refs/heads/" .. job.branch },
    { cwd = wt }, "push de la rama-resultado")
end

-- mesh.run_job(job, role, opts?) ⏸ -> Result (malla.md §5).
function M.run_job(job, role, opts)
  opts = opts or {}
  local result = { ok = false, job_id = type(job) == "table" and job.id or nil, denials = {} }
  local ok, err = pcall(function()
    if type(job) ~= "table" or type(role) ~= "table" then
      einval("mesh.run_job espera (job, role) como tablas (mesh.job.load / mesh.role.load)")
    end

    -- 1. El territorio físico, con limpieza garantizada (F1: cleanup).
    local wt = M.worktree.add(job.base, { cwd = opts.cwd, remote = opts.remote })
    if opts.keep_worktree ~= true then
      enu.task.cleanup(function() M.worktree.remove(wt, { cwd = opts.cwd }) end)
    end

    -- 2. Skills pineadas: el hash es la aprobación (§9). Mismatch → muere aquí.
    if verify_skills(role, wt) then
      agent.trust.set(wt, true)
    end

    -- 3. La sesión desde la spec (motor headless, agente.md §1).
    local sopts = M.to_session_opts(role, job)
    local s, first_message
    if type(job.fork) == "table" then
      s, first_message = open_fork_session(job, role, wt, sopts)
    else
      sopts.cwd = wt
      s = agent.session(sopts)
      first_message = job.prompt
    end
    enu.task.cleanup(function() s:close() end)

    -- 4. Denegaciones como DATO (G40): vuelven en Result.denials y en el
    -- result.json de la rama — el bucle de escalado (enmienda del Role) las lee.
    local dsub = enu.events.on("agent:permission.denied", function(p)
      if p.session == s.id then result.denials[#result.denials + 1] = p end
    end)
    enu.task.cleanup(function() dsub:cancel() end)

    -- 5. Presupuesto DURO en el driver: max_turns ya viaja en los opts; el tope
    -- de coste corta el turno con cancel (posible desde que cost_usd se acumula).
    local budget = role.budget or {}
    if type(budget.max_cost_usd) == "number" then
      local bsub = enu.events.on("agent:message", function(p)
        if p.session == s.id and s.usage.cost_usd > budget.max_cost_usd then
          s:cancel()
        end
      end)
      enu.task.cleanup(function() bsub:cancel() end)
    end

    -- 6. El turno (o turnos: send corre el loop completo, agente.md §2).
    local msg = s:send(first_message)
    if msg == nil then
      emesh("el turno se canceló antes de completar (presupuesto de coste agotado o cancel externo)",
        { cost_usd = s.usage.cost_usd, max_cost_usd = budget.max_cost_usd })
    end

    result.usage = {
      context_tokens = s.usage.context_tokens,
      cost_usd       = s.usage.cost_usd,
      turns          = s.usage.turns,
    }

    -- 7. La rama es el resultado; el merge es la puerta HUMANA, fuera de aquí.
    attach_and_push(s, wt, job, result, opts)
    result.branch = job.branch
    result.ok = true
  end)
  if not ok then
    result.error = {
      code    = (type(err) == "table" and err.code) or "EMESH",
      message = (type(err) == "table" and err.message) or tostring(err),
    }
  end
  return result
end

-- ---------------------------------------------------------------------------
-- §6 El torneo de forks (fork-como-replicación, G39 + G27). El juez y el merge
-- quedan FUERA a propósito: el juez es otra sesión (componible); el merge, la
-- puerta humana. Las perdedoras se descartan a coste cero (worktree fuera).
-- ---------------------------------------------------------------------------

-- Semáforo de futures (pseudocódigo, escenario 25): acota la concurrencia sin
-- API nueva.
local function semaphore(n)
  local free, waiters = n, {}
  return {
    acquire = function()
      if free > 0 then free = free - 1; return end
      local f = enu.task.future()
      waiters[#waiters + 1] = f
      f:await()
    end,
    release = function()
      local w = table.remove(waiters, 1)
      if w then w:set(true) else free = free + 1 end
    end,
  }
end

-- mesh.tournament{ session, variants, at?, verify?, limit? } ⏸ -> Outcome[]
-- (malla.md §6). Alineado con variants (G27) y allSettled (pcall por rama).
function M.tournament(t)
  if type(t) ~= "table" or type(t.session) ~= "table" or type(t.variants) ~= "table" then
    einval("mesh.tournament espera { session, variants = { { nudge, cwd, opts? }, ... }, at?, verify?, limit? }")
  end
  local sem = nil
  if type(t.limit) == "number" and t.limit > 0 and t.limit < #t.variants then
    sem = semaphore(t.limit)
  end
  local fns = {}
  for i, v in ipairs(t.variants) do
    fns[i] = function()
      if sem then
        sem.acquire()
        enu.task.cleanup(sem.release)
      end
      local out = { dir = v.cwd }
      local ok, err = pcall(function()
        if type(v.nudge) ~= "string" or type(v.cwd) ~= "string" then
          einval("cada variante del torneo necesita { nudge, cwd }")
        end
        local fopts = {}
        for k, val in pairs(v.opts or {}) do fopts[k] = val end
        fopts.cwd = v.cwd
        local child = t.session:fork(t.at, fopts)   -- re-aloja (G39)
        enu.task.cleanup(function() child:close() end)
        out.session_id = child.id
        out.message = child:send(v.nudge)
        if t.verify ~= nil then
          -- Pirámide anti-slop: el verificador DETERMINISTA filtra antes de que
          -- nadie (juez o humano) mire nada.
          out.verified = t.verify(v.cwd, out) == true
        end
      end)
      out.ok = ok
      if not ok then
        out.error = {
          code    = (type(err) == "table" and err.code) or "EMESH",
          message = (type(err) == "table" and err.message) or tostring(err),
        }
      end
      return out
    end
  end
  return enu.task.all(fns) -- alineado con los inputs (G27)
end

return M
