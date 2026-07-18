-- Módulo público de la extensión `sessions` (S38).
--
-- Implementa el contrato de persistencia de
-- [sesiones.md](../../../../../docs/sesiones.md):
--
--   1. **JSONL append-only** (§1-§4): una sesión es un fichero al que solo se
--      añaden líneas (`enu.fs.append` + `enu.json.encode`), nunca se reescribe. El
--      estado se reconstruye por **replay** (leer de arriba abajo, `enu.fs.read` +
--      `enu.json.decode`). Reutiliza el modelo canónico de mensajes
--      ([providers.md](../../../../../docs/providers.md) §2): el `Message` que el
--      `done` del adaptador (S37) entrega se persiste tal cual en una entrada
--      `message`.
--   2. **Un escritor por sesión** (§6, G5): un lockfile `<sesión>.jsonl.lock`
--      creado con `enu.fs.write{ exclusive = true, mode = 0600 }` (atómico, G17;
--      permisos no world-readable, G57). Su contenido
--      es la identidad del escritor —`{ pid, hostname, started }`— con el pid de
--      `enu.sys.pid()` (G32) y el hostname de `enu.sys.hostname()` (G17). Un lock
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

-- Permisos del transcript y del lockfile (sesiones.md §2/§6): 0600, solo el dueño.
-- Contienen código y salidas de comandos (transcript) e identidad del escritor
-- (lock): no deben quedar legibles por otros usuarios. Se aplican con `opts.mode`
-- (G57), que hace chmod explícito NO recortado por el umask —Lua no tiene literal
-- octal, de ahí `tonumber("600", 8)`—. El transcript se crea VACÍO con este modo
-- antes del primer append; los append siguientes preservan el modo del fichero
-- existente (`O_CREATE` no re-chmod-ea lo ya creado), así que basta fijarlo al crear.
local SESSION_MODE = tonumber("600", 8)

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
  return enu.config.data_dir() .. "/sessions"
end

-- project_dir(cwd) -> string. data_dir/sessions/<slug>.
local function project_dir(cwd)
  return sessions_root() .. "/" .. slug(cwd)
end

-- ---------------------------------------------------------------------------
-- Superficie pública de rutas (G38). El slug es PARTE DEL FORMATO (sesiones.md
-- §2): el algoritmo está especificado para que las herramientas externas
-- compongan rutas sin enu; estas funciones puras son la comodidad para plugins
-- (nadie reimplementa la codificación — única fuente Lua de verdad).
-- ---------------------------------------------------------------------------

-- sessions.slug(cwd) -> string. La codificación cwd→clave de agrupación.
function M.slug(cwd)
  return slug(cwd)
end

-- sessions.dir(cwd) -> string. data_dir()/sessions/<slug> (sesiones.md §2).
function M.dir(cwd)
  return project_dir(cwd)
end

-- gen_id() -> string. Id de sesión = timestamp UTC ordenable + sufijo aleatorio
-- (§2: ordenación lexicográfica = ordenación temporal). El timestamp en ms va en
-- hex de ancho fijo para que ordene; el sufijo evita colisiones en el mismo ms.
-- El PRNG se siembra una sola vez con `now_ms` + `pid` (G32): sin la semilla,
-- gopher-lua daría la MISMA secuencia entre arranques, así que dos procesos `enu`
-- que crearan una sesión en el mismo ms colisionarían en el sufijo; el pid los
-- separa (el lock exclusivo, §6, solo protege ante ids iguales).
local seeded = false
local function gen_id()
  if not seeded then
    math.randomseed(math.floor(enu.sys.now_ms()) + enu.sys.pid())
    seeded = true
  end
  local ms = math.floor(enu.sys.now_ms())
  return string.format("%013d-%04x", ms, math.random(0, 0xffff))
end

-- ---------------------------------------------------------------------------
-- Lockfile: un escritor por sesión (sesiones.md §6, G5/G17/G32).
-- ---------------------------------------------------------------------------

-- write_lock(lock_path) intenta crear el lockfile con creación EXCLUSIVA (G17):
-- `enu.fs.write{ exclusive = true }` es atómico (O_EXCL) —dos procesos no pueden
-- ganar a la vez—, lanza `EEXIST` si ya existe. `mode = 0600` fija los permisos con
-- chmod explícito, no recortado por el umask (§2/§6, G57): el lockfile guarda la
-- identidad del escritor y no debe quedar world-readable bajo un umask laxo. El
-- contenido es la identidad del escritor (§6): pid de `enu.sys.pid()` (G32),
-- hostname de `enu.sys.hostname()` (G17), started de `enu.sys.now_ms()`.
local function write_lock(lock_path)
  local meta = {
    pid      = enu.sys.pid(),
    hostname = enu.sys.hostname(),
    started  = enu.sys.now_ms(),
  }
  enu.fs.write(lock_path, enu.json.encode(meta), { exclusive = true, mode = SESSION_MODE })
end

-- read_lock(lock_path) -> meta? Lee y decodifica el lockfile, o nil si no existe
-- o está corrupto (un lock ilegible se trata como ausente: basura a reemplazar).
local function read_lock(lock_path)
  local raw = nil
  local ok = pcall(function() raw = enu.fs.read(lock_path) end)
  if not ok or raw == nil then
    return nil
  end
  local decoded
  ok = pcall(function() decoded = enu.json.decode(raw) end)
  if not ok or type(decoded) ~= "table" then
    return nil
  end
  return decoded
end

-- acquire_lock(lock_path) adquiere el lock para escritura, reclamando huérfanos.
-- Lógica de §6:
--   - intento de creación exclusiva; si lo crea, listo;
--   - si ya existe (`EEXIST`), se inspecciona el lock vivo:
--       * mismo hostname y pid NO vivo (`enu.proc.alive`=false) → HUÉRFANO (crash):
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
  local my_host = enu.sys.hostname()

  if meta == nil then
    -- Lock ilegible/corrupto en esta máquina: basura de un crash. Se limpia y
    -- se reintenta (un único reintento: si vuelve a chocar, es una carrera real).
    enu.fs.remove(lock_path)
  elseif meta.hostname ~= my_host then
    esession("la sesión está bloqueada por otra máquina (" .. tostring(meta.hostname) ..
      "); no se puede verificar si sigue viva", { reason = "foreign", lock = meta })
  elseif meta.pid ~= nil and enu.proc.alive(meta.pid) then
    esession("la sesión ya tiene un escritor vivo (pid " .. tostring(meta.pid) .. ")",
      { reason = "busy", lock = meta })
  else
    -- Mismo hostname, pid muerto (o sin pid): huérfano de un crash. Se limpia en
    -- silencio (§6) y se reintenta.
    enu.fs.remove(lock_path)
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
-- `enu.json.encode` y se escribe con `enu.fs.append` —UNA línea, una operación
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
    entry.ts = enu.sys.now_ms()
  end
  enu.fs.append(self.path, enu.json.encode(entry) .. "\n")
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
  local ok = pcall(function() raw = enu.fs.read(self.path) end)
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
        if pcall(function() decoded = enu.json.decode(line) end) and type(decoded) == "table" then
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
-- idempotente. Se registra además en `enu.task.cleanup` al abrir, así que el lock
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
      if meta and meta.pid == enu.sys.pid() and meta.hostname == enu.sys.hostname() then
        enu.fs.remove(self.lock_path)
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
-- Registra el `close` en `enu.task.cleanup` para soltar el lock pase lo que pase.
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
  enu.fs.mkdir(dir) -- mkdir -p (api.md §5): crea data_dir/sessions/<proyecto>
  local path = dir .. "/" .. id .. ".jsonl"
  local lock_path = path .. ".lock"

  if not creating then
    -- Reanudar exige que el fichero exista (replay del transcript, §3/G18).
    if enu.fs.stat(path) == nil then
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
    enu.task.cleanup(function() self:close() end)
  end

  if creating then
    -- Crea el transcript VACÍO con 0600 (sesiones.md §2, G57) ANTES del primer
    -- append: `enu.fs.write{ mode }` hace el chmod explícito no recortado por el
    -- umask, y los append posteriores preservan ese modo (`enu.fs.append` no
    -- re-chmod-ea un fichero existente). Creación exclusiva: el id es fresco, así
    -- que el fichero no debería existir; si existiera (colisión de id), `EEXIST`
    -- evita pisar un transcript ajeno.
    enu.fs.write(path, "", { exclusive = true, mode = SESSION_MODE })
    -- Primera línea: la entrada `meta` (§3). Sin `ts` (no es actividad).
    self:append({
      t       = "meta",
      v       = FORMAT_VERSION,
      id      = id,
      cwd     = opts.cwd,
      created = opts.created or enu.sys.now_ms(),
      parent  = opts.parent,
    })
  end

  return self
end

-- M.list(cwd) -> {id, path, meta}[] lista las sesiones de un proyecto (§7):
-- enumerar el directorio y adjuntar la primera línea (`meta`) de cada `.jsonl`.
-- Sin índice global (§7): si algún día duele, se añade un caché reconstruible.
-- Los `.jsonl.lock` se ignoran (no son sesiones). Orden: el de `enu.fs.list` (los
-- ids ordenan lexicográfico = temporal, §2; el llamante ordena si lo necesita).
--
-- LAS `meta` VÍA `enu.search.grep`, NO leyendo cada fichero entero (A-38b). La
-- versión previa hacía `enu.fs.read` de CADA transcript solo para quedarse con su
-- primera línea: con transcripts de MB, listar costaba O(bytes totales) en IO y
-- en memoria cruzando la frontera wasm. `enu.search.grep` (api.md §11) casa el
-- patrón en Go, paralelo por dentro, y **solo las líneas que casan cruzan a
-- Lua**: el escaneo del transcript se queda en Go. Sin API nueva —compone con lo
-- existente—.
--
-- EL PATRÓN. La entrada `meta` lleva las claves ORDENADAS por `enu.json.encode`
-- (encoding/json ordena alfabéticamente las claves de un objeto), así que `t` no
-- es la primera clave y NO se puede anclar con `^`; se casa la subcadena
-- distintiva `"t":"meta"` en cualquier posición (RE2, S26; ninguno de sus
-- caracteres es metacarácter). Se tolera el espacio opcional de JSON alrededor
-- de `:` (`"t"\s*:\s*"meta"`) por si el fichero lo escribió una herramienta
-- externa (el slug es parte del formato, §2). `meta` es SIEMPRE la primera línea
-- (§3): la coincidencia buena está en `line_no == 1`; nos quedamos con ella e
-- ignoramos cualquier otra (una subcadena `"t":"meta"` incrustada en el
-- contenido de un `message` posterior). Además se decodifica y se comprueba
-- `t == "meta"`, como antes: una línea que casa pero no decodifica a una `meta`
-- válida se descarta (fichero corrupto → `meta = nil`, igual que la versión
-- previa, pero el fichero SIGUE en la lista vía el enumerado del directorio).
function M.list(cwd)
  if type(cwd) ~= "string" or cwd == "" then
    einval("sessions.list requiere `cwd` (string no vacío)")
  end
  local dir = project_dir(cwd)
  if enu.fs.stat(dir) == nil then
    return {} -- proyecto sin sesiones aún
  end

  -- Recolecta las `meta` por NOMBRE DE FICHERO (basename): la clave de fusión es
  -- el nombre, no la ruta completa, para no depender de cómo `grep` normaliza las
  -- rutas (`filepath.Join`) frente a la concatenación en Lua. Los `.jsonl` son
  -- planos en `dir`, así que el basename es único.
  local metas = {}
  for r in enu.search.grep([["t"\s*:\s*"meta"]], { root = dir, glob = "*.jsonl" }) do
    if r.line_no == 1 then
      local name = r.path:match("[^/]+$") or r.path
      if metas[name] == nil then
        local decoded
        if pcall(function() decoded = enu.json.decode(r.line) end)
          and type(decoded) == "table" and decoded.t == "meta" then
          metas[name] = decoded
        end
      end
    end
  end

  -- El enumerado del directorio define el CONJUNTO y el ORDEN del resultado (como
  -- antes): toda `.jsonl` aparece, con su `meta` si se encontró o `nil` si el
  -- fichero está corrupto / sin `meta`. Listar el directorio es O(nº sesiones),
  -- no O(bytes): el coste que A-38b señalaba estaba en LEER cada fichero, no en
  -- enumerarlos.
  local out = {}
  for _, ent in ipairs(enu.fs.list(dir)) do
    if not ent.is_dir and ent.name:sub(-6) == ".jsonl" then
      local id = ent.name:sub(1, -7) -- quita ".jsonl"
      local path = dir .. "/" .. ent.name
      out[#out + 1] = { id = id, path = path, meta = metas[ent.name] }
    end
  end
  return out
end

return M
