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
	fns    []HostFn // indexado por id
	names  []string // id→nombre (para diagnósticos y el preludio)
	byName map[string]int32
}

func newHostRegistry() *hostRegistry {
	return &hostRegistry{byName: make(map[string]int32)}
}

// register añade una primitiva y devuelve su id. Nombre único (un duplicado es
// error de programación del kernel).
func (r *hostRegistry) register(name string, fn HostFn) int32 {
	if _, dup := r.byName[name]; dup {
		panic("vmwasm: primitiva duplicada: " + name)
	}
	id := int32(len(r.fns))
	r.fns = append(r.fns, fn)
	r.names = append(r.names, name)
	r.byName[name] = id
	return id
}

// Register expone el registro de primitivas al kernel (lo usa M09+). Debe
// llamarse antes de instanciar (el preludio se arma con el catálogo completo).
func (p *Pool) Register(name string, fn HostFn) int32 {
	return p.reg.register(name, fn)
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
	b.WriteString(preludioMonta)
	b.WriteString(preludioSched)
	b.WriteString(preludioTask)
	b.WriteString(preludioEvents)
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
  elseif t == "table" then
    -- ¿secuencia (array) o mapa? Heurística: #v cubre 1..n contiguos.
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
    error("nu: valor no serializable a la frontera VM: " .. t)
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
    local h = string.unpack("<I4", s, pos); return { __handle = h }, pos + 4
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
  t[parts[#parts]] = function(...) return __call_host(myid, ...) end
end

nu.json = nu.json or {}
nu.json.NULL = NULL

_G.nu = nu
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

nu.task = nu.task or {}

local function __enqueue(id, arg, iserr)
  __ready[#__ready+1] = { id = id, arg = arg, iserr = iserr }
end

-- nu.task.spawn(fn, ...) -> id. Crea una corrutina y la encola lista.
function nu.task.spawn(fn, ...)
  local packed = table.pack(...)
  local id = __next_id; __next_id = __next_id + 1
  local co = coroutine.create(function()
    return fn(table.unpack(packed, 1, packed.n))
  end)
  __tasks[id] = { co = co, done = false, awaiters = {} }
  __enqueue(id, nil, false)
  return id
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
  local t = __tasks[id]
  if not t then error("nu.task.await: id de task desconocido") end
  if t.done then
    if t.ok then return t.result else error(t.result) end
  end
  local r = coroutine.yield({ op = "await", id = id })
  if r.ok then return r.result else error(r.result) end
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
    for i = #t.cleanups, 1, -1 do pcall(t.cleanups[i]) end
  end
  for _, aw in ipairs(t.awaiters) do
    __enqueue(aw, { ok = t.ok, result = t.result }, false)
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
    __finish(t)
    return
  end
  local prev = __current; __current = id
  local ok, yielded
  if iserr then
    ok, yielded = coroutine.resume(t.co, { __err = true, msg = arg })
  else
    ok, yielded = coroutine.resume(t.co, arg)
  end
  __current = prev
  if not ok then
    t.done = true; t.ok = false; t.result = yielded; __finish(t)
  elseif coroutine.status(t.co) == "dead" then
    t.done = true; t.ok = true; t.result = yielded; __finish(t)
  elseif yielded.op == "await" then
    local target = __tasks[yielded.id]
    if target and target.done then
      __enqueue(id, { ok = target.ok, result = target.result }, false)
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
  local pending = {}
  local guard = 0
  while #__ready > 0 do
    guard = guard + 1
    if guard > 1000000 then error("nu.task: bucle de scheduler sin fin") end
    local r = table.remove(__ready, 1)
    __resume(r.id, r.arg, r.iserr, pending)
  end
  return __wire.enc_list(pending)
end
`

// preludioTask: el resto de la superficie nu.task (M07), toda sobre el bucle de
// M06: future (rendez-vous), all (alineado con inputs, G27), race, cleanup (LIFO),
// cancel (cooperativo, §1.3) y defer. Semántica de api.md §3.
const preludioTask = `
-- nu.task.future() -> { set, await } (§3). Rendez-vous de un solo uso: una task
-- espera un valor que otra producirá, sin polling.
function nu.task.future()
  local fid = __next_fid; __next_fid = __next_fid + 1
  __futures[fid] = { resolved = false, waiters = {} }
  return {
    set = function(v)
      local f = __futures[fid]
      if f.resolved then error({ code = "EINVAL", message = "future ya resuelto" }) end
      f.resolved = true; f.value = v
      for _, taskid in ipairs(f.waiters) do __enqueue(taskid, v, false) end
      f.waiters = {}
    end,
    await = function()
      local f = __futures[fid]
      if f.resolved then return f.value end
      return coroutine.yield({ op = "future", fid = fid })
    end,
  }
end

-- nu.task.cleanup(fn) (§3). Registra un liberador en la pila LIFO de la task
-- actual; corre al terminar (éxito/error/aborto). El "defer" de esta casa.
function nu.task.cleanup(fn)
  local t = __tasks[__current]
  if t then t.cleanups = t.cleanups or {}; t.cleanups[#t.cleanups+1] = fn end
end

-- nu.task.cancel(id) (§3, Task:cancel). Cancelación cooperativa: aborta la task
-- en su siguiente punto de suspensión (no capturable); corren sus cleanups. Si
-- está suspendida, se encola para que el scheduler la finalice.
function nu.task.cancel(id)
  local t = __tasks[id]
  if t and not t.done then
    t.cancelled = true
    __enqueue(id, nil, false)  -- fuerza un paso que la finalice (§1.3)
  end
end

-- nu.task.all(fns) -> resultados (§3, G27). Espera a todas; resultados ALINEADOS
-- con los inputs (out[i] es el de fns[i]), no en orden de terminación. Si una
-- lanza, su error se relanza (fail-fast a través del await).
function nu.task.all(fns)
  local ids = {}
  for i = 1, #fns do ids[i] = nu.task.spawn(fns[i]) end
  local out = {}
  for i = 1, #ids do out[i] = nu.task.await(ids[i]) end
  return out
end

-- nu.task.race(fns) -> (winner_index, result) (§3). La primera en terminar gana.
function nu.task.race(fns)
  local f = nu.task.future()
  local settled = false
  for i = 1, #fns do
    local idx, fn = i, fns[i]
    nu.task.spawn(function()
      local ok, r = pcall(fn)
      if not settled then settled = true; f.set({ idx, ok, r }) end
    end)
  end
  local res = f.await()
  if not res[2] then error(res[3]) end
  return res[1], res[3]
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
