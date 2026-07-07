package vmwasm

// Registro de host functions y el preludio Lua (migracion-vm.md M05, categoría
// C2 del censo). Es el equivalente wasm de `registerNu`: cada primitiva `nu.*`
// del kernel se registra aquí como un HostFn Go asociado a un id; el preludio
// Lua construye la tabla `nu` cuyas entradas son thunks que serializan sus args
// al wire (wire.go), llaman a `__nu_host(id, ...)`, y deserializan el resultado
// —o lanzan un error estructurado si la primitiva falló—.
//
// Así M09 (las primitivas de IO/datos) es mecánico: registrar un HostFn por
// primitiva y declararla en el preludio; el marshaling y el cruce de errores ya
// están resueltos aquí.

import (
	"fmt"
	"strings"
)

// HostFn es la firma canónica de una primitiva del kernel sobre el backend wasm
// (censo C2): recibe los args ya deserializados del wire y devuelve los valores
// de retorno (a serializar) o un error. Un error se propaga al lado Lua como
// `error(...)` con la tabla estructurada de §1.4 si el HostFn devuelve un
// *StructuredError; cualquier otro error viaja como mensaje.
type HostFn func(inst *Instance, args []any) ([]any, error)

// StructuredError es un error del contrato (api.md §1.4: {code, message, detail})
// que cruza la frontera preservando sus campos, para que el lado Lua lo relance
// como la misma tabla y un `pcall` lo capture idéntico (paridad con gopher, C4).
type StructuredError struct {
	Code    string
	Message string
	Detail  any
}

func (e *StructuredError) Error() string { return e.Code + ": " + e.Message }

// hostRegistry mapea id→HostFn. Vive en el Pool (compartido por instancias): las
// primitivas son las mismas para todas; el estado por-instancia lo lleva el
// *Instance que recibe el HostFn.
type hostRegistry struct {
	fns        []HostFn // indexado por id
	names      []string // id→nombre (para diagnósticos y el preludio)
	byName     map[string]int32
	suspending []bool                  // id→¿suspende? (M09): su thunk cede al scheduler
	methods    map[string]HandleMethod // "Tipo.metodo" → método de handle (M10, C5)
}

func newHostRegistry() *hostRegistry {
	return &hostRegistry{byName: make(map[string]int32)}
}

// register añade una primitiva y devuelve su id. Nombre único (un duplicado es
// error de programación del kernel).
func (r *hostRegistry) register(name string, fn HostFn, suspends bool) int32 {
	if _, dup := r.byName[name]; dup {
		panic("vmwasm: primitiva duplicada: " + name)
	}
	id := int32(len(r.fns))
	r.fns = append(r.fns, fn)
	r.names = append(r.names, name)
	r.suspending = append(r.suspending, suspends)
	r.byName[name] = id
	return id
}

// Register expone el registro de una primitiva SÍNCRONA (§10/§12: codecs, text,
// sys...). Su thunk llama al dispatch directo (M05). Debe llamarse antes de
// instanciar (el preludio se arma con el catálogo completo).
func (p *Pool) Register(name string, fn HostFn) int32 {
	return p.reg.register(name, fn, false)
}

// RegisterSuspending expone el registro de una primitiva ⏸ (§5/§6/§8/§11: fs,
// proc, http, ws, search). Su thunk CEDE al scheduler (op "hostcall") para que
// otras tasks corran mientras el trabajo bloqueante ocurre en una goroutine de
// fondo. Contrato: un HostFn suspendente **no debe tocar el *Instance** (corre en
// otra goroutine); recibe args, hace el trabajo, devuelve valores. La asignación
// de handles (M10) y demás toques a la instancia van en primitivas síncronas.
func (p *Pool) RegisterSuspending(name string, fn HostFn) int32 {
	return p.reg.register(name, fn, true)
}

// dispatch resuelve el id, deserializa los args, llama al HostFn y serializa el
// resultado. El protocolo de retorno hacia __nu_host: el primer byte del buffer
// distingue éxito (0x00 + valores wire) de error (0x01 + error wire), para que el
// thunk Lua sepa si retornar o lanzar. Lo usa hostDispatch (vmwasm.go).
func (inst *Instance) dispatchPrimitive(id int32, args []byte) ([]byte, error) {
	reg := inst.pool.reg
	if id < 0 || int(id) >= len(reg.fns) {
		return nil, fmt.Errorf("vmwasm: id de primitiva fuera de rango: %d", id)
	}
	decoded, err := Decode(args)
	if err != nil {
		return nil, err
	}
	rets, callErr := reg.fns[id](inst, decoded)
	if callErr != nil {
		return encodeError(callErr)
	}
	body, err := Encode(rets)
	if err != nil {
		return nil, err
	}
	return append([]byte{0x00}, body...), nil
}

// encodeError serializa un error para el thunk Lua: 0x01 + wire de
// {code, message, detail}. Un *StructuredError preserva sus campos; cualquier
// otro error se envuelve con code="EIO" (fallo interno) y su mensaje.
func encodeError(callErr error) ([]byte, error) {
	var se *StructuredError
	if s, ok := callErr.(*StructuredError); ok {
		se = s
	} else {
		se = &StructuredError{Code: "EIO", Message: callErr.Error()}
	}
	m := map[string]any{"code": se.Code, "message": se.Message}
	if se.Detail != nil {
		m["detail"] = se.Detail
	}
	body, err := Encode([]any{m})
	if err != nil {
		return nil, err
	}
	return append([]byte{0x01}, body...), nil
}

// preludio genera el código Lua que construye la tabla `nu` y su codec de wire
// (el espejo de wire.go). Se ejecuta al arrancar cada instancia (tras nu_new),
// una vez el catálogo de primitivas está completo. El catálogo (nombre→id) se
// inyecta como una tabla al principio.
//
// El nombre de una primitiva es una ruta con puntos ("fs.read", "task.spawn"):
// el preludio crea los submódulos anidados. Los nombres los declara el kernel al
// registrar (M09+); en M05 el catálogo puede estar vacío (sólo se valida el codec).
func (p *Pool) preludio() string {
	var b strings.Builder
	b.WriteString(preludioBase)
	b.WriteString("\nlocal __catalogo = {\n")
	for id, name := range p.reg.names {
		fmt.Fprintf(&b, "  [%q] = %d,\n", name, id)
	}
	b.WriteString("}\n")
	b.WriteString("local __suspending = {\n")
	for id, s := range p.reg.suspending {
		if s {
			fmt.Fprintf(&b, "  [%d] = true,\n", id)
		}
	}
	b.WriteString("}\n")
	fmt.Fprintf(&b, "local __api_version = %d\n", p.apiVersion)
	fmt.Fprintf(&b, "local __ver_major, __ver_minor, __ver_patch = %d, %d, %d\n",
		p.verMajor, p.verMinor, p.verPatch)
	b.WriteString(preludioMonta)
	b.WriteString(preludioSched)
	b.WriteString(preludioTask)
	b.WriteString(preludioLoader)
	b.WriteString(preludioWorkerCommon)
	if p.isWorker {
		// Un worker (M12) es un mini-runtime: su propio scheduler (nu.task) y su
		// canal con el padre (nu.worker.parent), pero SIN nu.ui, SIN nu.events (bus
		// principal) y SIN nu.worker.spawn (no hay workers anidados) — api.md §13.
		b.WriteString(preludioWorkerParent)
	} else {
		b.WriteString(preludioEvents)
		b.WriteString(preludioInput)
		b.WriteString(preludioWorkerHost)
	}
	// Snippets del catálogo (M13b): wrappers finos en Lua, con `nu` ya montado.
	for _, s := range p.extraPreludio {
		b.WriteString("\n")
		b.WriteString(s)
	}
	return b.String()
}

// preludioBase: el codec de wire en Lua (espejo de wire.go) sobre string.pack/
// unpack de 5.4. Byte-seguro (los strings de Lua son byte-arrays), distingue
// integer de float (5.4 tiene los dos subtipos) y honra el sentinel NULL.
const preludioBase = `
-- Tags del wire (deben coincidir con wire.go).
local W_NIL, W_FALSE, W_TRUE, W_INT, W_FLOAT = 0, 1, 2, 3, 4
local W_STR, W_ARRAY, W_MAP, W_HANDLE, W_NULL = 5, 6, 7, 8, 9

-- NULL: sentinel único (nu.json.NULL, G11). Una tabla vacía por identidad.
local NULL = setmetatable({}, { __tostring = function() return "null" end })

-- Metatable de los handles opacos (C5, M10). h:metodo(...) despacha a la host
-- function genérica __hcall (síncrona), que resuelve el tipo del handle en Go y
-- llama al método registrado. Los métodos suspendentes (Proc:wait) se cablean en
-- M13 con un despacho a __hcall_s según una tabla de "suspende".
local __handle_mt = {
  __index = function(self, method)
    return function(_, ...) return _G.__hcall(self.__id, method, ...) end
  end,
}

-- __enc(v, out): serializa v al array de trozos "out".
local function __enc(v, out)
  local t = type(v)
  if v == nil then out[#out+1] = string.char(W_NIL)
  elseif v == NULL then out[#out+1] = string.char(W_NULL)
  elseif t == "boolean" then out[#out+1] = string.char(v and W_TRUE or W_FALSE)
  elseif t == "number" then
    if math.type(v) == "integer" then
      out[#out+1] = string.char(W_INT) .. string.pack("<i8", v)
    else
      out[#out+1] = string.char(W_FLOAT) .. string.pack("<d", v)
    end
  elseif t == "string" then
    out[#out+1] = string.char(W_STR) .. string.pack("<I4", #v) .. v
  elseif t == "table" and getmetatable(v) == __handle_mt then
    -- un handle opaco (C5): cruza como su índice, no como tabla.
    out[#out+1] = string.char(W_HANDLE) .. string.pack("<I4", v.__id)
  elseif t == "table" then
    -- ¿secuencia (array) o mapa? Heurística: #v cubre 1..n contiguos. Una tabla
    -- vacía cruza como ARRAY (lo asume el scheduler para la lista de pendientes
    -- vacía); la ambigüedad []/{} de los codecs (§12: vacío → objeto) la resuelve
    -- el propio codec al ver un array vacío, no el wire.
    local n = #v
    local isArray = true
    local count = 0
    for _ in pairs(v) do count = count + 1 end
    if count ~= n then isArray = false end
    if isArray then
      out[#out+1] = string.char(W_ARRAY) .. string.pack("<I4", n)
      for i = 1, n do __enc(v[i], out) end
    else
      out[#out+1] = string.char(W_MAP) .. string.pack("<I4", count)
      for k, val in pairs(v) do
        local ks = tostring(k)
        out[#out+1] = string.char(W_STR) .. string.pack("<I4", #ks) .. ks
        __enc(val, out)
      end
    end
  else
    error({ code = "EINVAL", message = "nu: valor no serializable a la frontera VM: " .. t })
  end
end

-- __dec(s, pos): deserializa un valor desde s en la posición pos; devuelve
-- (valor, nueva_pos).
local function __dec(s, pos)
  local tag = string.byte(s, pos); pos = pos + 1
  if tag == W_NIL then return nil, pos
  elseif tag == W_NULL then return NULL, pos
  elseif tag == W_FALSE then return false, pos
  elseif tag == W_TRUE then return true, pos
  elseif tag == W_INT then local v = string.unpack("<i8", s, pos); return v, pos + 8
  elseif tag == W_FLOAT then local v = string.unpack("<d", s, pos); return v, pos + 8
  elseif tag == W_STR then
    local n = string.unpack("<I4", s, pos); pos = pos + 4
    return string.sub(s, pos, pos + n - 1), pos + n
  elseif tag == W_HANDLE then
    local h = string.unpack("<I4", s, pos)
    -- Un handle opaco (C5): una tabla con su índice y una metatable que despacha
    -- métodos (h:metodo(...)) a la host function genérica __hcall (M10).
    return setmetatable({ __id = h }, __handle_mt), pos + 4
  elseif tag == W_ARRAY then
    local n = string.unpack("<I4", s, pos); pos = pos + 4
    local t = {}
    for i = 1, n do t[i], pos = __dec(s, pos) end
    return t, pos
  elseif tag == W_MAP then
    local n = string.unpack("<I4", s, pos); pos = pos + 4
    local t = {}
    for _ = 1, n do
      local k; k, pos = __dec(s, pos)
      t[k], pos = __dec(s, pos)
    end
    return t, pos
  else
    error("nu: tag de wire desconocido: " .. tostring(tag))
  end
end

-- __enc_list(...) -> string: serializa una lista de valores (count + valores).
local function __enc_list(...)
  local args = table.pack(...)
  local out = { string.pack("<I4", args.n) }
  for i = 1, args.n do __enc(args[i], out) end
  return table.concat(out)
end

-- __dec_list(s) -> valores...: deserializa una lista de valores.
local function __dec_list(s)
  if not s or #s < 4 then return end
  local n = string.unpack("<I4", s, 1)
  local pos = 5
  local vals = {}
  for i = 1, n do vals[i], pos = __dec(s, pos) end
  return table.unpack(vals, 1, n)
end
`

// preludioMonta: usa el catálogo para montar la tabla `nu` con thunks, y expone
// nu.json.NULL. Cada thunk serializa args, llama __nu_host(id, wire), y según el
// primer byte del resultado retorna los valores o lanza el error estructurado.
const preludioMonta = `
-- __call_host(id, ...): el thunk genérico. __nu_host devuelve (ok, resultstr);
-- ok=false es un fallo del DISPATCH (id malo), raro. El resultado lleva un byte
-- de estado: 0=éxito, 1=error estructurado.
local function __call_host(id, ...)
  local ok, res = __nu_host(id, __enc_list(...))
  if not ok then error("nu: fallo de dispatch en la primitiva id " .. tostring(id)) end
  local status = string.byte(res, 1)
  local body = string.sub(res, 2)
  if status == 1 then
    -- error estructurado: {code, message, detail?}
    local e = __dec_list(body)
    error(e)
  end
  return __dec_list(body)
end

-- monta nu.<ruta> = thunk para cada entrada del catálogo, creando submódulos.
local nu = {}
for name, id in pairs(__catalogo) do
  local parts = {}
  for p in string.gmatch(name, "[^.]+") do parts[#parts+1] = p end
  local t = nu
  for i = 1, #parts - 1 do
    t[parts[i]] = t[parts[i]] or {}
    t = t[parts[i]]
  end
  local myid = id
  if __suspending[myid] then
    -- primitiva ⏸ (M09): cede al scheduler; el driver Go la cumple en una
    -- goroutine de fondo y reanuda con el resultado.
    t[parts[#parts]] = function(...)
      local r = coroutine.yield({ op = "hostcall", id = myid, args = { ... } })
      if r.ok == false then error(r.err) end
      return table.unpack(r.values, 1, r.n or #r.values)
    end
  else
    t[parts[#parts]] = function(...) return __call_host(myid, ...) end
  end
end

nu.json = nu.json or {}
nu.json.NULL = NULL

-- nu.version (api.md §1): el nivel de API lo inyecta el Runtime (APILevel). Crece
-- solo por adición; romper una firma rompe el mundo. En los tests aislados de
-- vmwasm queda en 0 (no se fija backend real).
nu.version = { major = __ver_major, minor = __ver_minor, patch = __ver_patch, api = __api_version }

_G.nu = nu
-- __hcall: el despacho síncrono de métodos de handle (M10). La primitiva
-- "__handle_call" la registra registerHandleDispatch; aquí se cablea al global
-- que la metatable de handles usa.
_G.__hcall = nu.__handle_call
_G.__hcall_s = nu.__handle_call_s

-- nu.has(cap): detección de capacidades (api.md §1). Presente siempre (también en
-- workers). Mínimo por ahora: "ui" según haya backend (headless G20); el catálogo
-- completo (net.tcp, images...) llega con la integración del Runtime (M13).
function nu.has(cap)
  if cap == "ui" then return nu.ui ~= nil end
  return false
end
-- exporta el codec para los tests de la frontera (no forma parte de nu.*).
_G.__wire = { enc_list = __enc_list, dec_list = __dec_list, NULL = NULL }
`

// preludioSched: el scheduler de tasks por corrutinas nativas (ADR-020, M06). El
// bucle vive en Go (scheduler.go, driver `RunTasks`); aquí está la lógica Lua:
// tasks como `coroutine`, ⏸ como `coroutine.yield` de una petición de trabajo
// externo, y `__sched_step` como el paso que Go conduce. La semántica observable
// es la de api.md §1.3 (await implícito, código secuencial).
const preludioSched = `
local __tasks = {}      -- id -> { co, done, ok, result, awaiters, cleanups, cancelled }
local __ready = {}      -- lista de { id, arg, iserr } a reanudar en el próximo step
local __next_id = 1
local __futures = {}    -- fid -> { resolved, value, waiters } (nu.task.future)
local __next_fid = 1
__aborted = {}          -- ids abortados en el paso actual; __sched_step lo resetea

nu.task = nu.task or {}

local function __enqueue(id, arg, iserr)
  __ready[#__ready+1] = { id = id, arg = arg, iserr = iserr }
end

-- Task handle (api.md §3): nu.task.spawn devuelve un Task, no un id crudo. Es
-- una tabla con __task_id y una metatable que despacha :await() / :cancel().
-- El id numérico sigue siendo la clave interna en __tasks; el handle lo envuelve
-- para dar la superficie de métodos del contrato (paridad con el backend gopher,
-- que devuelve un userdata con __index await/cancel). __task_id_of normaliza un
-- argumento que puede ser un handle o —uso interno— un id crudo.
local function __task_id_of(x)
  if type(x) == "table" and type(x.__task_id) == "number" then return x.__task_id end
  if type(x) == "number" then return x end
  return nil
end
local __task_mt = {
  __index = {
    await = function(self)
      local id = __task_id_of(self)
      if id == nil then error({ code = "EINVAL", message = "Task:await: el receptor no es una Task" }) end
      return nu.task.await(id)
    end,
    cancel = function(self)
      local id = __task_id_of(self)
      if id == nil then error({ code = "EINVAL", message = "Task:cancel: el receptor no es una Task" }) end
      nu.task.cancel(id)
    end,
  },
}

-- nu.task.spawn(fn, ...) -> Task. Crea una corrutina y la encola lista.
function nu.task.spawn(fn, ...)
  local packed = table.pack(...)
  local id = __next_id; __next_id = __next_id + 1
  local co = coroutine.create(function()
    -- Watchdog (DM4): instala el count-hook en ESTA corrutina (un hilo nuevo no
    -- hereda el hook del padre en 5.4). A partir de aquí, cada WD_COUNT
    -- instrucciones se comprueba el presupuesto del slice; si lo rebasa, el hook
    -- cede y el scheduler aborta la task con EBUDGET. Con el watchdog desactivado
    -- (sliceBudget<=0) el hook nunca cede (nu_over_budget siempre 0).
    __wd_arm()
    return fn(table.unpack(packed, 1, packed.n))
  end)
  __tasks[id] = { co = co, done = false, awaiters = {} }
  __enqueue(id, nil, false)
  return setmetatable({ __task_id = id }, __task_mt)
end

-- nu.task.sleep(ms). Cede una petición de sleep; el driver Go la cumple y
-- reanuda tras ms.
function nu.task.sleep(ms)
  coroutine.yield({ op = "sleep", ms = ms })
end

-- nu.task.await(id) -> resultado. Si la task ya terminó, devuelve su resultado
-- (o relanza su error); si no, cede una petición de await que el scheduler
-- resuelve cuando la task termine.
function nu.task.await(id)
  id = __task_id_of(id)
  local t = id and __tasks[id]
  if not t then error({ code = "EINVAL", message = "nu.task.await: no es una Task" }) end
  if id == __current then
    error({ code = "EINVAL", message = "nu.task.await: una task no puede esperarse a sí misma" })
  end
  if t.done then
    if not t.ok then error(t.result) end
    if t.results then return table.unpack(t.results, 1, t.nresults) end
    return
  end
  -- Va a SUSPENDER: fuera de una task (chunk principal de EvalString) no hay a quién
  -- ceder, así que es EINVAL (§1.3), no un "yield from outside a coroutine" crudo.
  if __current == nil then
    error({ code = "EINVAL", message = "nu.task.await: debe llamarse dentro de una task (⏸)" })
  end
  local r = coroutine.yield({ op = "await", id = id })
  if not r.ok then error(r.result) end
  if r.results then return table.unpack(r.results, 1, r.nresults) end
end

-- la task cuyo código corre AHORA (para nu.task.cleanup, que se llama desde
-- dentro de la task sin conocer su id).
__current = nil

-- __finish(t): la task terminó (ok/error/cancelada). Corre sus cleanups en
-- orden LIFO (§1, "el defer de esta casa": pase lo que pase) y notifica a sus
-- awaiters. Idempotente.
local function __finish(t)
  if t.finished then return end
  t.finished = true
  if t.cleanups then
    for i = #t.cleanups, 1, -1 do
      local cok, cerr = pcall(t.cleanups[i])
      -- pcall por frontera (ADR-008): un cleanup que lanza no impide que corran los
      -- demás ni tumba el proceso; queda en el log (best-effort, como gopher).
      if not cok and nu.log and nu.log.error then
        local msg = type(cerr) == "table" and (cerr.message or cerr.code) or tostring(cerr)
        nu.log.error("un liberador de nu.task.cleanup lanzó: " .. tostring(msg))
      end
    end
  end
  for _, aw in ipairs(t.awaiters) do
    __enqueue(aw, { ok = t.ok, result = t.result, results = t.results, nresults = t.nresults }, false)
  end
  -- Error fire-and-forget (best-effort, api.md §1.4): si la task lanzó y nadie la
  -- espera, déjalo en el log. Se EXCLUYEN los abortos (cancelación/watchdog): no son
  -- errores de la task sino desenlaces de §1.3, sin errValue en gopher. Tampoco se
  -- loguea cuando el HOST consume el desenlace (EvalTaskString envuelve el código en
  -- un pcall, así que su task nunca entra por aquí con error).
  if not t.ok and not t.cancelled and #t.awaiters == 0 and t.result ~= nil then
    local e = t.result
    local code = type(e) == "table" and e.code or nil
    if code ~= "ECANCELED" and code ~= "EBUDGET" and nu.log and nu.log.error then
      local s = code and (tostring(code) .. ": " .. tostring(e.message or "")) or tostring(e)
      nu.log.error("una task terminó con error y nadie hizo await: " .. s)
    end
  end
end

-- resume una task lista; procesa el resultado (done/error/petición/future).
local function __resume(id, arg, iserr, pending)
  local t = __tasks[id]
  if not t or t.done then return end
  -- Cancelación cooperativa (§1.3): una task cancelada no se reanuda; termina
  -- con ECANCELED observable y corre sus cleanups. El aborto no pasa por el
  -- código de la task, así que ningún pcall de usuario lo captura.
  if t.cancelled then
    t.done = true; t.ok = false; t.result = { code = "ECANCELED", message = "task cancelada" }
    -- La task pudo quedar SUSPENDIDA con una petición en vuelo en Go (un sleep, un
    -- hostcall). Al abortarla aquí, esa goroutine de fondo seguiría contando en el
    -- outstanding del driver y RunTasks esperaría su duración completa (un sleep de
    -- 10s colgaría el bucle). Anotamos su id en __aborted para que el paso se lo diga
    -- a Go y cancele la petición (§1.3: la cancelación surte efecto en el acto).
    __aborted[#__aborted+1] = id
    __finish(t)
    return
  end
  local prev = __current; __current = id
  -- Watchdog (DM4): reinicia el deadline del slice que va a correr; el count-hook
  -- de esta corrutina (armado en spawn) lo comparará cada WD_COUNT instrucciones.
  nu.__reset_budget()
  -- table.pack para preservar TODOS los valores de retorno de la task: await -> any
  -- no se limita a uno (§3). Al SUSPENDER, la corrutina cede un único valor (la tabla
  -- op=...), que es resumed[2]; al TERMINAR (dead) puede devolver varios.
  local resumed
  if iserr then
    resumed = table.pack(coroutine.resume(t.co, { __err = true, msg = arg }))
  else
    resumed = table.pack(coroutine.resume(t.co, arg))
  end
  local ok = resumed[1]
  local yielded = resumed[2]
  __current = prev
  if not ok then
    t.done = true; t.ok = false; t.result = yielded; __finish(t)
  elseif coroutine.status(t.co) == "dead" then
    t.done = true; t.ok = true; t.result = yielded
    t.nresults = resumed.n - 1
    t.results = {}
    for i = 2, resumed.n do t.results[i - 1] = resumed[i] end
    __finish(t)
  elseif yielded == nil then
    -- Aborto por WATCHDOG (DM4, §1.3): el count-hook cedió al rebasar el
    -- presupuesto del slice. Un yield del hook NO lleva valor (Lua 5.4 restaura el
    -- top tras el hook), así que yielded == nil es la firma inequívoca del aborto
    -- por budget (todos los ⏸ normales ceden una tabla op=...). El pcall interno
    -- de la task no lo capturó (fue un yield, no un error): NO capturable, gemelo
    -- del aborto por cancelación pero con EBUDGET. La task NO se reencola; corren
    -- sus cleanups (LIFO) y el estado sigue vivo para el resto de tasks.
    t.done = true; t.ok = false
    t.result = { code = "EBUDGET", message = "una task excedió el presupuesto de slice (watchdog)" }
    __finish(t)
  elseif yielded.op == "await" then
    local target = __tasks[yielded.id]
    if target and target.done then
      __enqueue(id, { ok = target.ok, result = target.result, results = target.results, nresults = target.nresults }, false)
    elseif target then
      target.awaiters[#target.awaiters+1] = id
    else
      __enqueue(id, { ok = false, result = "await: id desconocido" }, false)
    end
  elseif yielded.op == "future" then
    local f = __futures[yielded.fid]
    if f and f.resolved then
      __enqueue(id, f.value, false)
    elseif f then
      f.waiters[#f.waiters+1] = id
    else
      __enqueue(id, nil, false)
    end
  else
    pending[#pending+1] = { id = id, request = yielded }
  end
end

-- __sched_step(injected) -> pending: reanuda las tasks listas (más las que se
-- vuelvan listas por await/spawn durante el paso), inyectando primero los
-- resultados de trabajo externo completado, y devuelve las nuevas peticiones.
function __sched_step(injected)
  local arr = __wire.dec_list(injected)
  if arr then
    for _, item in ipairs(arr) do
      __enqueue(item.id, item.result, item.iserr == true)
    end
  end
  __aborted = {}            -- ids de tasks abortadas este paso (peticiones a cancelar en Go)
  local pending = {}
  local guard = 0
  while #__ready > 0 do
    guard = guard + 1
    if guard > 1000000 then error("nu.task: bucle de scheduler sin fin") end
    local r = table.remove(__ready, 1)
    __resume(r.id, r.arg, r.iserr, pending)
  end
  -- El paso devuelve DOS listas: las nuevas peticiones y los ids abortados. Go
  -- cancela la petición en vuelo de cada id abortado (si la hubiera) para no
  -- esperar su duración completa tras una cancelación.
  return __wire.enc_list(pending, __aborted)
end
`

// preludioTask: el resto de la superficie nu.task (M07), toda sobre el bucle de
// M06: future (rendez-vous), all (alineado con inputs, G27), race, cleanup (LIFO),
// cancel (cooperativo, §1.3) y defer. Semántica de api.md §3.
const preludioTask = `
-- Future handle (api.md §3): un rendez-vous de un solo uso con métodos :set(v) y
-- :await(). Como Task, es una tabla con __future_id y una metatable que despacha
-- por __index, validando que el receptor es un Future (paridad con el userdata del
-- backend gopher; Future:set sobre otro handle → EINVAL). La CONVENCIÓN es de dos
-- puntos (self), como todas las extensiones oficiales (agent/mcp/mesh) la usan.
local function __future_of(self)
  local fid = type(self) == "table" and self.__future_id
  return fid and __futures[fid], fid
end
local __future_mt = {
  __index = {
    set = function(self, v)
      local f = __future_of(self)
      if not f then error({ code = "EINVAL", message = "Future:set: el receptor no es un Future" }) end
      if f.resolved then error({ code = "EINVAL", message = "future ya resuelto" }) end
      f.resolved = true; f.value = v
      for _, taskid in ipairs(f.waiters) do __enqueue(taskid, v, false) end
      f.waiters = {}
    end,
    await = function(self)
      local f, fid = __future_of(self)
      if not f then error({ code = "EINVAL", message = "Future:await: el receptor no es un Future" }) end
      -- Future:await es ⏸: fuera de una task es EINVAL AUNQUE ya esté resuelto (el
      -- contrato prohíbe llamar una suspendiente fuera de task, sin importar si en
      -- este caso concreto no llegaría a suspender). __current es nil fuera de task.
      if __current == nil then
        error({ code = "EINVAL", message = "Future:await: debe llamarse dentro de una task (⏸)" })
      end
      if f.resolved then return f.value end
      return coroutine.yield({ op = "future", fid = fid })
    end,
  },
}

-- nu.task.future() -> Future (§3). Rendez-vous de un solo uso: una task espera un
-- valor que otra producirá, sin polling.
function nu.task.future()
  local fid = __next_fid; __next_fid = __next_fid + 1
  __futures[fid] = { resolved = false, waiters = {} }
  return setmetatable({ __future_id = fid }, __future_mt)
end

-- nu.task.cleanup(fn) (§3). Registra un liberador en la pila LIFO de la task
-- actual; corre al terminar (éxito/error/aborto). El "defer" de esta casa.
function nu.task.cleanup(fn)
  local t = __current and __tasks[__current]
  if not t then
    error({ code = "EINVAL", message = "nu.task.cleanup: debe llamarse dentro de una task" })
  end
  t.cleanups = t.cleanups or {}; t.cleanups[#t.cleanups+1] = fn
end

-- nu.task.cancel(id) (§3, Task:cancel). Cancelación cooperativa: aborta la task
-- en su siguiente punto de suspensión (no capturable); corren sus cleanups. Si
-- está suspendida, se encola para que el scheduler la finalice.
function nu.task.cancel(id)
  id = __task_id_of(id)
  local t = id and __tasks[id]
  if t and not t.done then
    t.cancelled = true
    __enqueue(id, nil, false)  -- fuerza un paso que la finalice (§1.3)
  end
end

-- nu.task.all(fns) -> resultados (§3, G27). Espera a todas; resultados ALINEADOS
-- con los inputs (out[i] es el de fns[i]), no en orden de terminación. FAIL-FAST:
-- si una lanza, se CANCELA al resto (abortan en su próximo ⏸) y se relanza ese
-- error. Cada elemento corre envuelto en una task cuyo pcall reporta su desenlace a
-- un future de coordinación; así se detecta el PRIMER fallo en cuanto ocurre (no en
-- orden de array), condición para cancelar a las demás cuanto antes.
function nu.task.all(fns)
  if __current == nil then
    error({ code = "EINVAL", message = "nu.task.all: debe llamarse dentro de una task (⏸)" })
  end
  local n = #fns
  if n == 0 then
    error({ code = "EINVAL", message = "nu.task.all: la lista de tareas está vacía" })
  end
  local out = {}
  local remaining = n
  local first_err = nil       -- { err = <valor> } una vez que alguna lanza
  local resolved = false
  local coord = nu.task.future()
  local tasks = {}
  local function report(ok, r, idx)
    if ok then out[idx] = r
    elseif first_err == nil then first_err = { err = r } end
    remaining = remaining - 1
    if not resolved and (first_err ~= nil or remaining == 0) then
      resolved = true; coord:set(true)
    end
  end
  for i = 1, n do
    local e, idx = fns[i], i
    if type(e) == "function" then
      tasks[i] = nu.task.spawn(function() local ok, r = pcall(e); report(ok, r, idx) end)
    elseif __task_id_of(e) ~= nil then
      tasks[i] = nu.task.spawn(function()
        local ok, r = pcall(function() return nu.task.await(e) end); report(ok, r, idx)
      end)
    else
      error({ code = "EINVAL", message = "nu.task.all: el elemento " .. i .. " no es una Task ni una función" })
    end
  end
  coord:await()
  if first_err ~= nil then
    for _, tk in ipairs(tasks) do nu.task.cancel(tk) end
    error(first_err.err)
  end
  return out
end

-- nu.task.race(fns) -> (winner_index, result) (§3). La primera en terminar gana
-- (incluido terminar por error: se relanza su error). Se CANCELA a las perdedoras.
function nu.task.race(fns)
  if __current == nil then
    error({ code = "EINVAL", message = "nu.task.race: debe llamarse dentro de una task (⏸)" })
  end
  local n = #fns
  if n == 0 then
    error({ code = "EINVAL", message = "nu.task.race: la lista de tareas está vacía" })
  end
  local coord = nu.task.future()
  local settled = false
  local winner = nil          -- { idx, ok, r } del primero en terminar
  local tasks = {}
  for i = 1, n do
    local fn, idx = fns[i], i
    if type(fn) ~= "function" then
      error({ code = "EINVAL", message = "nu.task.race: el elemento " .. i .. " no es una función" })
    end
    tasks[i] = nu.task.spawn(function()
      local ok, r = pcall(fn)
      if not settled then settled = true; winner = { idx = idx, ok = ok, r = r }; coord:set(true) end
    end)
  end
  coord:await()
  for _, tk in ipairs(tasks) do nu.task.cancel(tk) end   -- la ganadora ya terminó (no-op)
  if not winner.ok then error(winner.r) end
  return winner.idx, winner.r
end

-- nu.task.defer(fn) (§3). Ejecuta fn en el siguiente tick (como una task que no
-- suspende: corre en el próximo paso del bucle).
function nu.task.defer(fn)
  nu.task.spawn(fn)
end
`

// preludioEvents: el bus de eventos nu.events (M08, api.md §4) con la semántica
// de G10 (foto de suscriptores al emitir, cancelar surte efecto inmediato, subs
// nuevos solo ven eventos futuros, emits anidados encolados por ANCHURA), y los
// timers periódicos nu.task.every. Todo síncrono y en Lua (emit no suspende).
const preludioEvents = `
local __ev_subs = {}         -- name -> lista de { fn, live, once }
local __ev_queue = {}        -- emits pendientes { name, payload }
local __ev_dispatching = false

nu.events = nu.events or {}

function nu.events.on(name, fn)
  __ev_subs[name] = __ev_subs[name] or {}
  local sub = { fn = fn, live = true, once = false }
  __ev_subs[name][#__ev_subs[name]+1] = sub
  return { cancel = function() sub.live = false end }
end

function nu.events.once(name, fn)
  local subs = __ev_subs[name] or {}
  __ev_subs[name] = subs
  local sub = { fn = fn, live = true, once = true }
  subs[#subs+1] = sub
  return { cancel = function() sub.live = false end }
end

local function __ev_dispatch(name, payload)
  local subs = __ev_subs[name]
  if not subs then return end
  -- G10: foto de suscriptores tomada al emitir; los subs añadidos durante el
  -- despacho no corren (no están en la foto), y cancelar surte efecto inmediato
  -- (se comprueba live antes de cada uno).
  local snap = {}
  for i = 1, #subs do snap[i] = subs[i] end
  for _, s in ipairs(snap) do
    if s.live then
      if s.once then s.live = false end
      pcall(s.fn, payload)   -- cada handler bajo pcall (ADR-008)
    end
  end
  -- compacta los muertos (once consumidos, cancelados)
  local kept = {}
  for _, s in ipairs(subs) do if s.live then kept[#kept+1] = s end end
  __ev_subs[name] = kept
end

function nu.events.emit(name, payload)
  __ev_queue[#__ev_queue+1] = { name = name, payload = payload }
  if __ev_dispatching then return end   -- G10: anidado → encolado, no recursión
  __ev_dispatching = true
  local guard = 0
  while #__ev_queue > 0 do
    guard = guard + 1
    if guard > 1000000 then __ev_dispatching = false; error("nu.events: ping-pong infinito") end
    local e = table.remove(__ev_queue, 1)
    __ev_dispatch(e.name, e.payload)
  end
  __ev_dispatching = false
end

-- nu.task.every(ms, fn) -> { stop } (§3). Timer periódico: una task que repite
-- sleep+fn hasta que se para.
function nu.task.every(ms, fn)
  local stopped = false
  local id = nu.task.spawn(function()
    while not stopped do
      nu.task.sleep(ms)
      if stopped then break end
      pcall(fn)
    end
  end)
  return { stop = function() stopped = true; nu.task.cancel(id) end }
end
`

// preludioInput: la pila de input y la resolución de secuencias de teclas (M11,
// api.md §9.3). Como los handlers son funciones Lua, la pila vive en el preludio
// (igual que el bus de eventos), no Go-side; Go sólo inyecta eventos crudos
// (FeedInput → __ui_dispatch_input) y, en M13, dispara el timeout con un timer.
// El catálogo nu.ui.* (size/region/block/caps/clipboard) lo montan las primitivas
// (ui.go); aquí se añaden on_input/keymap/block y el despacho. Si no hay backend
// de UI (headless, G20), `nu.ui` no existe y este bloque no lo crea.
//
// Aproximación anotada (M11): el resolver de secuencias usa progreso POR handler
// (cada keymap recuerda cuántos acordes lleva), no el buffer global único de
// input.go. Prueba consumo/cesión/secuencia/timeout; la paridad fina de input.go
// (re-inyección del prefijo abortado, generaciones del timer) se completa al
// cablear el driver real en M13.
const preludioInput = `
if nu.ui then
  local __stack = {}   -- pila: { live, raw=fn } (on_input) o { live, seq, pos, fn } (keymap)

  local function __purge()
    local kept = {}
    for _, h in ipairs(__stack) do if h.live then kept[#kept+1] = h end end
    __stack = kept
  end

  -- nu.ui.on_input(fn) -> InputHandle. Apila un handler crudo; fn(ev)->bool.
  function nu.ui.on_input(fn)
    local h = { live = true, raw = fn }
    __stack[#__stack+1] = h
    return { pop = function() h.live = false end }
  end

  -- Parseo de la notación de teclas: "ctrl+k" -> {key="k", mods={ctrl=true}};
  -- "g g" (separado por espacios) -> lista de acordes (una secuencia).
  local __modnames = { ctrl = true, alt = true, shift = true, meta = true }
  local function __parse_chord(tok)
    local mods, key = {}, nil
    for part in string.gmatch(tok, "[^+]+") do
      if __modnames[part] then mods[part] = true else key = part end
    end
    return { key = key, mods = mods }
  end
  local function __parse_seq(seq)
    local chords = {}
    for tok in string.gmatch(seq, "%S+") do chords[#chords+1] = __parse_chord(tok) end
    return chords
  end

  -- nu.ui.keymap(seq, fn, opts?) -> Keymap. Azúcar sobre la pila (§9.3): la más
  -- reciente activa gana. Consume por defecto; fn puede devolver false EXPLÍCITO
  -- para ceder la tecla (que siga bajando por la pila).
  function nu.ui.keymap(seq, fn, opts)
    local h = { live = true, seq = __parse_seq(seq), pos = 0, fn = fn }
    __stack[#__stack+1] = h
    return { unmap = function() h.live = false end }
  end

  local function __mods_eq(a, b)
    a = a or {}; b = b or {}
    for m in pairs(__modnames) do
      if (a[m] or false) ~= (b[m] or false) then return false end
    end
    return true
  end
  local function __chord_matches(ev, c)
    return ev.type == "key" and ev.key == c.key and __mods_eq(ev.mods, c.mods)
  end
  local function __reset_seqs()
    for _, h in ipairs(__stack) do if h.seq then h.pos = 0 end end
  end

  -- __ui_timeout(): el prefijo de secuencia pendiente caducó. Go lo dispara con
  -- un timer (M13); en M11 lo llama el test. Resetea todo progreso de secuencia.
  _G.__ui_timeout = function() __reset_seqs() end

  -- __ui_dispatch_input(ev) -> consumed. Despacha de arriba a abajo (§9.3): el
  -- handler superior que consuma corta la propagación.
  _G.__ui_dispatch_input = function(ev)
    if ev == nil then return false end
    __purge()
    for i = #__stack, 1, -1 do
      local h = __stack[i]
      if h.live then
        if h.raw then
          if h.raw(ev) == true then return true end
        elseif h.seq then
          local nextc = h.seq[h.pos + 1]
          if nextc and __chord_matches(ev, nextc) then
            h.pos = h.pos + 1
            if h.pos >= #h.seq then
              h.pos = 0
              local r = h.fn(ev)
              if r ~= false then __reset_seqs(); return true end
              -- r == false: cede explícito, deja pasar al siguiente handler
            else
              return true   -- match parcial: consume la tecla (secuencia en curso)
            end
          elseif h.pos > 0 then
            h.pos = 0        -- una tecla que no continúa la secuencia la aborta
          end
        end
      end
    end
    return false
  end

  -- nu.ui.block(lines) -> Block. Envuelve el id que da nu.ui._block como handle
  -- con los campos read-only .width/.height (api.md §9.2).
  function nu.ui.block(lines)
    local m = nu.ui._block(lines)
    return setmetatable({ __id = m.id, width = m.width, height = m.height }, __handle_mt)
  end
end`

// preludioWorkerCommon: el chequeo de serializabilidad de mensajes (§13), común al
// lado padre y al lado worker. Un mensaje debe ser JSON-able; un function/thread o
// un handle (userdata/Block) → EINVAL, ANTES de ceder, para que el error sea
// limpio y capturable por pcall (no un fallo del codec a mitad de camino).
const preludioWorkerCommon = `
local function __check_msg(v, seen)
  local t = type(v)
  if t == "function" or t == "thread" then
    error({ code = "EINVAL", message = "worker: un valor de tipo " .. t .. " no es serializable" })
  elseif t == "table" then
    if getmetatable(v) == __handle_mt then
      error({ code = "EINVAL", message = "worker: un handle (userdata/Block) no cruza a un worker" })
    end
    seen = seen or {}
    if not seen[v] then
      seen[v] = true
      for k, val in pairs(v) do __check_msg(k, seen); __check_msg(val, seen) end
    end
  end
end
_G.__check_msg = __check_msg
`

// preludioWorkerHost: el lado PADRE (§13). nu.worker.spawn devuelve un Worker con
// send/recv/on_message/terminate. La exclusión recv/on_message (G8) vive en campos
// del Worker, serializada por el estado principal single-thread (el "token" de
// esta casa) — las tres reglas lanzan EINVAL en el acto. Sólo en el estado
// principal (un worker no crea workers).
const preludioWorkerHost = `
function nu.worker.spawn(module, opts)
  local wid = nu.worker._spawn(module, opts)
  local W = { __wid = wid, _recvPending = 0, _onMsg = false }

  function W:send(msg)
    __check_msg(msg)
    return nu.worker._send(self.__wid, msg)
  end

  function W:recv()
    if self._onMsg then
      error({ code = "EINVAL", message = "Worker:recv: hay un on_message registrado sobre este worker (excluyentes, G8)" })
    end
    self._recvPending = self._recvPending + 1
    local ok, m = pcall(nu.worker._recv, self.__wid)
    self._recvPending = self._recvPending - 1
    if not ok then error(m) end
    return m
  end

  function W:on_message(fn)
    if self._recvPending > 0 then
      error({ code = "EINVAL", message = "Worker:on_message: hay un Worker:recv pendiente sobre este worker (excluyentes, G8)" })
    end
    if self._onMsg then
      error({ code = "EINVAL", message = "Worker:on_message: ya hay un on_message sobre este worker (uno a la vez, G8)" })
    end
    self._onMsg = true
    local sub = { live = true }
    local wself = self
    nu.task.spawn(function()
      while sub.live do
        local ok, m = pcall(nu.worker._recv, wself.__wid)
        if not ok then break end        -- worker terminó
        if m == nil then break end       -- fin de canal
        if sub.live then pcall(fn, m) end -- cada handler bajo pcall (ADR-008)
      end
    end)
    return { cancel = function() sub.live = false; wself._onMsg = false end }
  end

  function W:terminate()
    nu.worker._terminate(self.__wid)
  end

  return W
end
`

// preludioWorkerParent: el lado WORKER del canal con el padre (§13). Mismas colas
// acotadas; sin spawn (no hay anidamiento). Sólo en el estado de un worker.
const preludioWorkerParent = `
nu.worker = nu.worker or {}
nu.worker.parent = nu.worker.parent or {}
function nu.worker.parent.send(msg)
  __check_msg(msg)
  return nu.worker.parent._send(msg)
end
function nu.worker.parent.recv()
  return nu.worker.parent._recv()
end
`

// preludioLoader: el require curado (M13, DM5, api.md §14). La lib `package` de
// PUC no se abre; este require resuelve por nombre contra el registro Go
// (nu.loader._source), cachea la primera carga, detecta ciclos y ofrece reload
// best-effort (G2). Presente en el estado principal y en los workers.
const preludioLoader = `
local __loaded = {}   -- name -> resultado del módulo (caché de una sola carga)
local __loading = {}  -- name -> true mientras se carga (detección de ciclos)

function require(name)
  local hit = __loaded[name]
  if hit ~= nil then return hit end
  if __loading[name] then
    error({ code = "EINVAL", message = "require: ciclo de dependencias con " .. tostring(name) })
  end
  local src = nu.loader._source(name)
  if src == nil then
    error({ code = "ENOENT", message = "require: módulo no encontrado: " .. tostring(name) })
  end
  __loading[name] = true
  local chunk, cerr = load(src, "@" .. name)
  if not chunk then
    __loading[name] = nil
    error({ code = "EINVAL", message = "require: error de compilación en " .. name .. ": " .. tostring(cerr) })
  end
  local ok, result = pcall(chunk)
  __loading[name] = nil
  if not ok then error(result) end
  if result == nil then result = true end   -- módulos sin return: se marcan cargados
  __loaded[name] = result
  return result
end

-- __loader_reload(name): reload best-effort (G2). Limpia la caché y re-require;
-- las referencias viejas al módulo persisten (por eso "best-effort"). El
-- nu.plugin.reload de la integración lo envuelve (M13).
_G.__loader_reload = function(name)
  __loaded[name] = nil
  return require(name)
end

-- __loader_loaded(name): ¿está cargado? (para tests y para el orden de init).
_G.__loader_loaded = function(name) return __loaded[name] ~= nil end
`
