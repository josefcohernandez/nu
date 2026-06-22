-- Módulo público de la extensión `sessions` (S38).
--
-- Implementa el contrato de persistencia de
-- [sesiones.md](../../../../../docs/sesiones.md):
--
--   1. **JSONL append-only** (§1-§4): una sesión es un fichero al que solo se
--      añaden líneas (`nu.fs.append` + `nu.json.encode`), nunca se reescribe. El
--      estado se reconstruye por **replay** (leer de arriba abajo, `nu.fs.read` +
--      `nu.json.decode`). Reutiliza el modelo canónico de mensajes
--      ([providers.md](../../../../../docs/providers.md) §2): el `Message` que el
--      `done` del adaptador (S37) entrega se persiste tal cual en una entrada
--      `message`.
--   2. **Un escritor por sesión** (§6, G5): un lockfile `<sesión>.jsonl.lock`
--      creado con `nu.fs.write{ exclusive = true }` (atómico, G17). Su contenido
--      es la identidad del escritor —`{ pid, hostname, started }`— con el pid de
--      `nu.sys.pid()` (G32) y el hostname de `nu.sys.hostname()` (G17). Un lock
--      huérfano (pid muerto en esta máquina) se reclama en silencio; uno de otro
--      hostname o con pid vivo es un conflicto real que el llamante decide.
--
-- ADR-003: el core NO sabe lo que es una sesión; todo es Lua puro sobre la API
-- pública (api.md), sin privilegio de kernel. Código de error de la extensión:
-- `ESESSION` (acuñado con la forma de los del core, api.md §1.4 / ADR-009), más
-- los reusados del core (`EEXIST`, `EINVAL`).

local M = {}

-- Versión del formato JSONL (sesiones.md §3: `meta.v`). Los lectores ignoran
-- líneas con `t` desconocido (forward-compatible); este número solo sube si el
-- significado de una entrada existente cambiase.
local FORMAT_VERSION = 1

-- ---------------------------------------------------------------------------
-- Errores estructurados de la extensión (ESESSION, ADR-009 / api.md §1.4).
-- ---------------------------------------------------------------------------

local function esession(message, detail)
  error({ code = "ESESSION", message = message, detail = detail })
end

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- ---------------------------------------------------------------------------
-- Rutas: data_dir/sessions/<proyecto>/<id>.jsonl (sesiones.md §2).
-- ---------------------------------------------------------------------------

-- slug(cwd) -> string. Codifica el `cwd` como un nombre de directorio seguro: la
-- agrupación es **por proyecto** (§2), y "continuar la última sesión de este
-- repo" debe ser un listado de directorio. Sustituye todo lo que no sea
-- alfanumérico/`-`/`.` por `_`; recorta los `_` de los bordes; cwd vacío → "root".
-- No pretende ser reversible (no es una ruta, es una clave de agrupación legible).
local function slug(cwd)
  local s = (cwd or ""):gsub("[^%w%-%.]", "_"):gsub("^_+", ""):gsub("_+$", "")
  if s == "" then
    return "root"
  end
  return s
end

-- sessions_root() -> string. data_dir/sessions (la única convención compartida,
-- §2). No crea nada todavía.
local function sessions_root()
  return nu.config.data_dir() .. "/sessions"
end

-- project_dir(cwd) -> string. data_dir/sessions/<slug>.
local function project_dir(cwd)
  return sessions_root() .. "/" .. slug(cwd)
end

-- gen_id() -> string. Id de sesión = timestamp UTC ordenable + sufijo aleatorio
-- (§2: ordenación lexicográfica = ordenación temporal). El timestamp en ms va en
-- hex de ancho fijo para que ordene; el sufijo evita colisiones en el mismo ms.
-- El PRNG se siembra una sola vez con `now_ms` + `pid` (G32): sin la semilla,
-- gopher-lua daría la MISMA secuencia entre arranques, así que dos procesos `nu`
-- que crearan una sesión en el mismo ms colisionarían en el sufijo; el pid los
-- separa (el lock exclusivo, §6, solo protege ante ids iguales).
local seeded = false
local function gen_id()
  if not seeded then
    math.randomseed(math.floor(nu.sys.now_ms()) + nu.sys.pid())
    seeded = true
  end
  local ms = math.floor(nu.sys.now_ms())
  return string.format("%013d-%04x", ms, math.random(0, 0xffff))
end

-- ---------------------------------------------------------------------------
-- Lockfile: un escritor por sesión (sesiones.md §6, G5/G17/G32).
-- ---------------------------------------------------------------------------

-- write_lock(lock_path) intenta crear el lockfile con creación EXCLUSIVA (G17):
-- `nu.fs.write{ exclusive = true }` es atómico (O_EXCL) —dos procesos no pueden
-- ganar a la vez—, lanza `EEXIST` si ya existe. El contenido es la identidad del
-- escritor (§6): pid de `nu.sys.pid()` (G32), hostname de `nu.sys.hostname()`
-- (G17), started de `nu.sys.now_ms()`. Devuelve true si lo adquirió.
local function write_lock(lock_path)
  local meta = {
    pid      = nu.sys.pid(),
    hostname = nu.sys.hostname(),
    started  = nu.sys.now_ms(),
  }
  nu.fs.write(lock_path, nu.json.encode(meta), { exclusive = true })
end

-- read_lock(lock_path) -> meta? Lee y decodifica el lockfile, o nil si no existe
-- o está corrupto (un lock ilegible se trata como ausente: basura a reemplazar).
local function read_lock(lock_path)
  local raw = nil
  local ok = pcall(function() raw = nu.fs.read(lock_path) end)
  if not ok or raw == nil then
    return nil
  end
  local decoded
  ok = pcall(function() decoded = nu.json.decode(raw) end)
  if not ok or type(decoded) ~= "table" then
    return nil
  end
  return decoded
end

-- acquire_lock(lock_path) adquiere el lock para escritura, reclamando huérfanos.
-- Lógica de §6:
--   - intento de creación exclusiva; si lo crea, listo;
--   - si ya existe (`EEXIST`), se inspecciona el lock vivo:
--       * mismo hostname y pid NO vivo (`nu.proc.alive`=false) → HUÉRFANO (crash):
--         se borra en silencio y se reintenta una vez;
--       * mismo hostname y pid vivo → CONFLICTO REAL → ESESSION{detail.reason="busy"};
--       * otro hostname → NO verificable (directorio sincronizado) → ESESSION
--         {detail.reason="foreign"} (se pregunta, nunca se asume).
-- El llamante (agente/chat) decide qué hacer con el conflicto (fork / solo lectura
-- / forzar, §6); aquí solo se distingue el caso reclamable del que no lo es.
local function acquire_lock(lock_path)
  local ok, err = pcall(write_lock, lock_path)
  if ok then
    return
  end
  if type(err) ~= "table" or err.code ~= "EEXIST" then
    error(err) -- error inesperado (permisos, IO): se propaga tal cual
  end

  -- El lock ya existe: ¿huérfano reclamable o conflicto real?
  local meta = read_lock(lock_path)
  local my_host = nu.sys.hostname()

  if meta == nil then
    -- Lock ilegible/corrupto en esta máquina: basura de un crash. Se limpia y
    -- se reintenta (un único reintento: si vuelve a chocar, es una carrera real).
    nu.fs.remove(lock_path)
  elseif meta.hostname ~= my_host then
    esession("la sesión está bloqueada por otra máquina (" .. tostring(meta.hostname) ..
      "); no se puede verificar si sigue viva", { reason = "foreign", lock = meta })
  elseif meta.pid ~= nil and nu.proc.alive(meta.pid) then
    esession("la sesión ya tiene un escritor vivo (pid " .. tostring(meta.pid) .. ")",
      { reason = "busy", lock = meta })
  else
    -- Mismo hostname, pid muerto (o sin pid): huérfano de un crash. Se limpia en
    -- silencio (§6) y se reintenta.
    nu.fs.remove(lock_path)
  end

  -- Reintento único tras reclamar el huérfano/basura.
  local ok2, err2 = pcall(write_lock, lock_path)
  if not ok2 then
    if type(err2) == "table" and err2.code == "EEXIST" then
      esession("no se pudo adquirir el lock tras reclamar el huérfano (carrera con otro proceso)",
        { reason = "race" })
    end
    error(err2)
  end
end

-- ---------------------------------------------------------------------------
-- El handle Session.
-- ---------------------------------------------------------------------------

local Session = {}
Session.__index = Session

-- Session:append(entry) anexa UNA entrada al transcript (§3-§4). La entrada se
-- completa con `ts` (epoch ms) si la lleva el tipo y no se dio; se serializa con
-- `nu.json.encode` y se escribe con `nu.fs.append` —UNA línea, una operación
-- atómica de append (§4: nunca medio mensaje)—. Solo el escritor (con lock)
-- puede anexar; una sesión en solo-lectura lanza.
function Session:append(entry)
  if self.read_only then
    esession("esta sesión está abierta en solo-lectura: no se puede anexar")
  end
  if type(entry) ~= "table" or type(entry.t) ~= "string" then
    einval("sessions: una entrada debe ser una tabla con `t` (tipo) string")
  end
  -- Las entradas de actividad llevan `ts` (epoch ms, §3). `meta` no lo lleva.
  if entry.t ~= "meta" and entry.ts == nil then
    entry.ts = nu.sys.now_ms()
  end
  nu.fs.append(self.path, nu.json.encode(entry) .. "\n")
end

-- Session:append_message(message, opts?) azúcar para la entrada `message` (§3):
-- el `Message` canónico (providers.md §2.1) tal cual, con `usage`/`model`
-- opcionales adjuntos (auditoría de coste, §3). `opts = { usage?, model? }`.
function Session:append_message(message, opts)
  opts = opts or {}
  self:append({ t = "message", message = message, usage = opts.usage, model = opts.model })
end

-- Session:replay() -> entries[] reconstruye el estado leyendo el fichero de
-- arriba abajo (§2: no hay segundo fichero de "estado actual"). Robustez de
-- lectura (§3): una última línea truncada (crash a mitad de escritura) se
-- descarta en silencio; líneas con `t` desconocido se CONSERVAN (forward-compat:
-- el llamante decide qué hacer con ellas, pero no se pierden). Devuelve TODAS las
-- entradas en orden; la política de replay para el LLM (tomar el último `compact`
-- y los `message` siguientes, §3) es del agente, no de la persistencia.
function Session:replay()
  local raw = ""
  local ok = pcall(function() raw = nu.fs.read(self.path) end)
  if not ok then
    return {}
  end
  local entries = {}
  -- Particiona por '\n'. La última pieza sin '\n' final es una línea truncada
  -- por un crash (§3): se descarta. Una línea vacía (entre dos '\n') se ignora.
  local last = 1
  local n = #raw
  for i = 1, n do
    if raw:byte(i) == 10 then -- '\n'
      local line = raw:sub(last, i - 1)
      last = i + 1
      if #line > 0 then
        local decoded
        if pcall(function() decoded = nu.json.decode(line) end) and type(decoded) == "table" then
          entries[#entries + 1] = decoded
        end
        -- Una línea no-vacía que no decodifica (no debería, salvo corrupción a
        -- mitad de fichero) se ignora en silencio: el resto del transcript es
        -- válido (append-only: lo escrito, escrito está).
      end
    end
  end
  -- Cola sin '\n' final = línea truncada (§4): se descarta deliberadamente.
  return entries
end

-- Session:meta() -> meta? devuelve la primera entrada (`meta`, §3) por replay, o
-- nil si la sesión está vacía / no empieza por `meta`. Atajo para los pickers.
function Session:meta()
  local entries = self:replay()
  if entries[1] and entries[1].t == "meta" then
    return entries[1]
  end
  return nil
end

-- Session:close() libera el lock (si lo tenía) y marca la sesión cerrada. Es
-- idempotente. Se registra además en `nu.task.cleanup` al abrir, así que el lock
-- se suelta aunque la task termine por error o aborto (sesiones.md §6: "se libera
-- al salir").
function Session:close()
  if self.closed then
    return
  end
  self.closed = true
  if self.lock_held and self.lock_path then
    -- Solo borramos NUESTRO lock (mismo pid/hostname): no robamos el de otro si
    -- entremedias nos lo reclamaron. Best-effort: un fallo al borrar no es fatal
    -- (un lock huérfano lo reclama el siguiente que abra).
    pcall(function()
      local meta = read_lock(self.lock_path)
      if meta and meta.pid == nu.sys.pid() and meta.hostname == nu.sys.hostname() then
        nu.fs.remove(self.lock_path)
      end
    end)
    self.lock_held = false
  end
end

-- ---------------------------------------------------------------------------
-- API pública del módulo.
-- ---------------------------------------------------------------------------

-- M.open(opts) -> Session abre una sesión para ESCRIBIR (crear o reanudar, §6).
-- opts:
--   - `cwd`     (string, requerido): el proyecto (agrupación, §2).
--   - `resume?` (string): id de una sesión existente a reanudar (G18). Si falta,
--     se CREA una nueva (id generado, entrada `meta` escrita).
--   - `read_only?` (bool): abre sin adquirir lock (lectores, §6: "leer nunca
--     requiere lock"). En este modo `append` lanza.
--   - `created?` (number): epoch ms de creación para `meta` (default now).
--   - `parent?`  (tabla `{ id, entry }`): enlace de fork/subagente (§5/§7).
--
-- Adquiere el lock con `acquire_lock` (reclama huérfanos) salvo en `read_only`.
-- Registra el `close` en `nu.task.cleanup` para soltar el lock pase lo que pase.
function M.open(opts)
  if type(opts) ~= "table" then
    einval("sessions.open espera una tabla de opciones")
  end
  if type(opts.cwd) ~= "string" or opts.cwd == "" then
    einval("sessions.open requiere `cwd` (string no vacío): la sesión se agrupa por proyecto")
  end

  local id = opts.resume
  local creating = (id == nil)
  if creating then
    id = gen_id()
  elseif type(id) ~= "string" or id == "" then
    einval("sessions.open: `resume` debe ser un id de sesión (string no vacío)")
  end

  local dir = project_dir(opts.cwd)
  nu.fs.mkdir(dir) -- mkdir -p (api.md §5): crea data_dir/sessions/<proyecto>
  local path = dir .. "/" .. id .. ".jsonl"
  local lock_path = path .. ".lock"

  if not creating then
    -- Reanudar exige que el fichero exista (replay del transcript, §3/G18).
    if nu.fs.stat(path) == nil then
      esession("no existe la sesión a reanudar: " .. id, { reason = "missing", id = id })
    end
  end

  local self = setmetatable({
    id        = id,
    path      = path,
    lock_path = lock_path,
    cwd       = opts.cwd,
    read_only = opts.read_only == true,
    lock_held = false,
    closed    = false,
  }, Session)

  if not self.read_only then
    acquire_lock(lock_path) -- lanza ESESSION en conflicto real / foreign
    self.lock_held = true
    -- Suelta el lock pase lo que pase con la task (éxito, error o aborto), §6.
    nu.task.cleanup(function() self:close() end)
  end

  if creating then
    -- Primera línea: la entrada `meta` (§3). Sin `ts` (no es actividad).
    self:append({
      t       = "meta",
      v       = FORMAT_VERSION,
      id      = id,
      cwd     = opts.cwd,
      created = opts.created or nu.sys.now_ms(),
      parent  = opts.parent,
    })
  end

  return self
end

-- M.list(cwd) -> {id, path, meta}[] lista las sesiones de un proyecto (§7):
-- listar el directorio y leer la primera línea (`meta`) de cada `.jsonl`. Sin
-- índice global (§7): si algún día duele, se añade un caché reconstruible. Los
-- `.jsonl.lock` se ignoran (no son sesiones). Orden: el de `nu.fs.list` (los ids
-- ordenan lexicográfico = temporal, §2; el llamante ordena si lo necesita).
function M.list(cwd)
  if type(cwd) ~= "string" or cwd == "" then
    einval("sessions.list requiere `cwd` (string no vacío)")
  end
  local dir = project_dir(cwd)
  if nu.fs.stat(dir) == nil then
    return {} -- proyecto sin sesiones aún
  end
  local out = {}
  for _, ent in ipairs(nu.fs.list(dir)) do
    if not ent.is_dir and ent.name:sub(-6) == ".jsonl" then
      local id = ent.name:sub(1, -7) -- quita ".jsonl"
      local path = dir .. "/" .. ent.name
      local meta = nil
      pcall(function()
        local raw = nu.fs.read(path)
        local nl = raw:find("\n", 1, true)
        local first = nl and raw:sub(1, nl - 1) or raw
        if #first > 0 then
          local decoded = nu.json.decode(first)
          if type(decoded) == "table" and decoded.t == "meta" then
            meta = decoded
          end
        end
      end)
      out[#out + 1] = { id = id, path = path, meta = meta }
    end
  end
  return out
end

return M
