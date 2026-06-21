package runtime

import (
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// El scheduler es la quilla del kernel (api.md §1.3, §3; ADR-004, realizado por
// ADR-011). El modelo de concurrencia es "del navegador": estado Lua de un solo
// hilo lógico con async por await implícito. Sobre gopher-lua eso se realiza con
// **una goroutine por task + un único token de ejecución Lua** ("GIL"), no con
// yields de corrutina:
//
//   - Cada **task** corre en su propia goroutine, sobre su propio thread Lua
//     (`*lua.LState` hijo del principal, comparten globales `G`). El **token**
//     (un canal de capacidad 1) garantiza que **solo una goroutine toca Lua a la
//     vez** —el invariante single-threaded de ADR-004/008 sin memoria Lua
//     compartida en paralelo.
//   - Una primitiva **⏸** no cede una corrutina: **suelta el token**, hace el
//     trabajo bloqueante en una goroutine de fondo (que jamás toca Lua) y, al
//     terminar, **recupera el token** y retorna el valor con normalidad. Como no
//     hay yield, la pila Lua vive en la pila Go de la task: `pcall` y las tail
//     calls que envuelven a un ⏸ sobreviven nativas (api.md §1.4: los errores se
//     capturan con `pcall`, incluso alrededor de operaciones que suspenden).
//
// Por qué este modelo y no el puente de corrutinas que insinuaban los documentos:
// gopher-lua (semántica Lua 5.1) **no deja a una corrutina ceder a través de una
// frontera de llamada Go** —ni `pcall` ni una tail call hacia una función Go que
// hace yield—. Eso rompía el modelo de errores de §1.4. La decisión y su porqué
// están en ADR-011; la grieta, en problemas.md (hallazgo del puente ⏸).
//
// "Cero data races" (inventario 🔒 de S04) sale del token: el handoff es por
// canal (happens-before), y nada Lua se toca fuera de él. La superficie pública
// de S04 es `nu.task.spawn` + `Task:await` (§3); sleep/defer/every, future,
// all/race, cancel/cleanup y el watchdog llegan en S05–S09 sobre esta base.

// taskTypeName identifica la metatabla del handle `Task` en el registro de tipos
// de gopher-lua. El userdata que devuelve `spawn` la lleva, y de ahí cuelga
// `await`.
const taskTypeName = "nu.task.Task"

// task es una unidad de ejecución concurrente: una goroutine corriendo `fn` sobre
// el thread Lua `co`. Al terminar guarda su desenlace (`results` o `errValue`) y
// cierra `doneCh`, que es donde esperan los `await` pendientes.
type task struct {
	co   *lua.LState
	fn   *lua.LFunction
	args []lua.LValue

	doneCh  chan struct{} // se cierra (bajo token) al terminar la task
	done    bool          // espejo de doneCh legible bajo token sin select
	awaited bool          // alguien hizo (o está haciendo) await sobre ella

	results  []lua.LValue // valores de retorno si terminó bien
	errValue lua.LValue   // objeto lanzado si terminó con error; nil si no
}

// deliverFn construye, **ya con el token recuperado** (es seguro tocar Lua), los
// valores que una primitiva ⏸ devuelve a su task tras completar el trabajo de
// fondo. La goroutine de fondo la *fabrica* capturando datos Go; no la invoca.
type deliverFn func(L *lua.LState) []lua.LValue

// scheduler administra el token Lua y la contabilidad de tasks vivas para saber
// cuándo el conjunto quedó quiescente (lo necesita `EvalString`).
type scheduler struct {
	rt   *Runtime
	host *lua.LState // estado principal: el chunk de `nu -e` y los handlers síncronos corren aquí

	gil chan struct{} // token de ejecución Lua (cap 1); el que lo tiene, corre Lua

	mu      sync.Mutex // protege `live`/`pending`/`timers` y respalda `cond`
	cond    *sync.Cond // señala "no queda trabajo de primer plano" (live+pending == 0)
	live    int        // tasks vivas (lanzadas y no terminadas)
	pending int        // handlers `defer` encolados y aún no ejecutados (S05)

	// timers son los `every` activos. Se rastrean para poder cortarlos todos al
	// cerrar el runtime (`Close`), de modo que no quede ninguna goroutine de
	// ticker colgada tras el fin del proceso. `defer` no se rastrea: es de un
	// solo disparo y se autolimpia.
	timers map[*luaTimer]struct{}
}

// newScheduler prepara el scheduler con el token libre (sembrado en el canal).
func newScheduler(rt *Runtime) *scheduler {
	s := &scheduler{
		rt:     rt,
		host:   rt.L,
		gil:    make(chan struct{}, 1),
		timers: make(map[*luaTimer]struct{}),
	}
	s.cond = sync.NewCond(&s.mu)
	s.gil <- struct{}{} // token disponible: el primero que lo pida corre Lua
	return s
}

// acquire toma el token Lua (bloquea hasta que esté libre). Tras volver, esta
// goroutine es la única autorizada a tocar Lua. release lo devuelve.
func (s *scheduler) acquire() { <-s.gil }
func (s *scheduler) release() { s.gil <- struct{}{} }

// register cuelga `nu.task` (con `spawn`) del global `nu` e instala la metatabla
// del tipo `Task` con su método `await`. `await` es una función Go pura: como
// nunca hay yield, puede relanzar el error de la task esperada con `L.Error`
// (capturable con `pcall`), sin envoltorios.
func (s *scheduler) register(nu *lua.LTable) {
	L := s.host

	mt := L.NewTypeMetatable(taskTypeName)
	index := L.NewTable()
	index.RawSetString("await", L.NewFunction(s.taskAwait))
	L.SetField(mt, "__index", index)

	taskTbl := L.NewTable()
	taskTbl.RawSetString("spawn", L.NewFunction(s.taskSpawn))
	nu.RawSetString("task", taskTbl)

	// Timers y diferidos (S05): `sleep`/`defer`/`every` + el tipo `Timer`,
	// colgados de la misma tabla `nu.task` (timers.go).
	s.registerTimers(nu, taskTbl)
}

// waitIdle bloquea hasta que no quede **trabajo de primer plano**: ninguna task
// viva (`live`) ni ningún `defer` encolado sin ejecutar (`pending`, S05). Lo usa
// `EvalString` tras correr el chunk: las tasks que el chunk lanzó —y los `defer`
// que encoló— corren mientras el hilo principal espera (con el token soltado). El
// patrón mutex+cond evita el wakeup perdido: el chequeo y la espera son atómicos.
//
// Los timers periódicos (`every`) **no** entran en esta cuenta a propósito: un
// timer nunca termina, así que esperar a que pare colgaría `nu -e` para siempre.
// Son facilidad de fondo; `Close` los apaga (ver claude_decisions.md, S05).
func (s *scheduler) waitIdle() {
	s.mu.Lock()
	for s.live > 0 || s.pending > 0 {
		s.cond.Wait()
	}
	s.mu.Unlock()
}

// addPending/donePending contabilizan los `defer` encolados para la quiescencia:
// un `defer` pendiente mantiene el runtime no-quiescente hasta que su handler
// corre (es "el siguiente tick", no trabajo de fondo). Al llegar a cero se
// despierta a `waitIdle`, igual que al terminar la última task.
func (s *scheduler) addPending() {
	s.mu.Lock()
	s.pending++
	s.mu.Unlock()
}

func (s *scheduler) donePending() {
	s.mu.Lock()
	s.pending--
	if s.live == 0 && s.pending == 0 {
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

// trackTimer registra un `every` activo para poder cortarlo al cerrar el runtime.
func (s *scheduler) trackTimer(t *luaTimer) {
	s.mu.Lock()
	s.timers[t] = struct{}{}
	s.mu.Unlock()
}

// stopTimer corta un `every` (vía `Timer:stop` o el cierre del runtime) y lo
// deja de rastrear. Es **idempotente**: cerrar `stopCh` dos veces entraría en
// pánico, así que el cierre solo ocurre la primera vez (cuando el timer sigue en
// el mapa). Tras esto, la goroutine del ticker ve `stopCh` cerrado y retorna.
func (s *scheduler) stopTimer(t *luaTimer) {
	s.mu.Lock()
	_, live := s.timers[t]
	if live {
		delete(s.timers, t)
		close(t.stopCh)
	}
	s.mu.Unlock()
}

// stopAllTimers corta todos los `every` activos. Lo llama `Runtime.Close` para no
// dejar goroutines de ticker colgadas al terminar el proceso.
func (s *scheduler) stopAllTimers() {
	s.mu.Lock()
	for t := range s.timers {
		close(t.stopCh)
		delete(s.timers, t)
	}
	s.mu.Unlock()
}

// spawn crea una task y lanza su goroutine. El arranque no es síncrono: la nueva
// goroutine compite por el token, así que solo corre cuando quien llamó a
// `spawn` lo suelta (al suspenderse o al terminar). Uniforme tanto si `spawn` se
// llama desde el chunk principal como desde dentro de otra task.
func (s *scheduler) spawn(fn *lua.LFunction, args []lua.LValue) *task {
	co, _ := s.host.NewThread()
	t := &task{co: co, fn: fn, args: args, doneCh: make(chan struct{})}
	s.mu.Lock()
	s.live++
	s.mu.Unlock()
	go s.runTask(t)
	return t
}

// runTask es el cuerpo de la goroutine de una task: toma el token, corre `fn`
// protegida (un error Lua no tumba el proceso, ADR-008), guarda el desenlace,
// despierta a quien la espera (cerrando `doneCh`) y suelta el token. El
// decremento de `live` va fuera del token, bajo `mu`, para la quiescencia.
func (s *scheduler) runTask(t *task) {
	s.acquire()

	err := t.co.CallByParam(lua.P{Fn: t.fn, NRet: lua.MultRet, Protect: true}, t.args...)
	if err == nil {
		if n := t.co.GetTop(); n > 0 {
			t.results = make([]lua.LValue, n)
			for i := 1; i <= n; i++ {
				t.results[i-1] = t.co.Get(i)
			}
		}
	} else {
		t.errValue = raisedValue(err)
	}
	t.co.SetTop(0)

	t.done = true
	close(t.doneCh)

	// Error fire-and-forget: si la task lanzó y nadie la espera, déjalo en el
	// log (best-effort de S04; el evento `core:plugin.error` llega en S10).
	// `awaited` ya es true si un `await` se registró antes de terminar, así que
	// el caso esperado-y-fallido no genera ruido.
	if t.errValue != nil && !t.awaited {
		_ = s.rt.log.write(levelError, s.rt.owner,
			"una task terminó con error y nadie hizo await: "+errString(t.errValue))
	}

	s.release()

	s.mu.Lock()
	s.live--
	if s.live == 0 {
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

// suspend es **el puente ⏸** del modelo (ADR-011): suelta el token, corre `work`
// en una goroutine de fondo y, al terminar, recupera el token y devuelve los
// valores que `work` dejó listos. Toda primitiva ⏸ (la de prueba de S04; sleep,
// fs.read, http... en adelante) se construye sobre esto.
//
// Aislamiento (de donde sale "cero data races"): `work` corre fuera del token y
// **no debe tocar Lua**; devuelve una `deliverFn` que sí corre con el token
// recuperado y construye allí los valores. Mientras la goroutine de fondo
// trabaja, la goroutine de la task está bloqueada en `<-ch` sin el token, así que
// otras tasks progresan —el loop no se congela.
func (s *scheduler) suspend(L *lua.LState, work func() deliverFn) []lua.LValue {
	ch := make(chan deliverFn, 1)
	go func() { ch <- work() }()
	s.release()
	d := <-ch
	s.acquire()
	return d(L)
}

// taskSpawn implementa `nu.task.spawn(fn, ...) -> Task` (§3): lanza una task con
// `fn` y los argumentos extra, y devuelve su handle.
func (s *scheduler) taskSpawn(L *lua.LState) int {
	fn := L.CheckFunction(1)
	top := L.GetTop()
	args := make([]lua.LValue, 0, top-1)
	for i := 2; i <= top; i++ {
		args = append(args, L.Get(i))
	}
	t := s.spawn(fn, args)

	ud := L.NewUserData()
	ud.Value = t
	L.SetMetatable(ud, L.GetTypeMetatable(taskTypeName))
	L.Push(ud)
	return 1
}

// taskAwait implementa `Task:await() -> any` (§3): espera el resultado de otra
// task. Si ya terminó, devuelve su desenlace en el acto; si no, suelta el token
// y bloquea hasta que la esperada cierre su `doneCh`, entonces recupera el token.
// Devuelve los valores de la task esperada, o **relanza** su error (nativo, vía
// `L.Error`; capturable con `pcall`, no es cancelación, §1.3).
//
// `await` es ⏸: llamarla fuera de una task es un error (§1.3) → `EINVAL`. La
// detección es por estado de ejecución: el chunk principal y los handlers
// síncronos corren sobre `host`; las tasks, sobre su propio `co`.
func (s *scheduler) taskAwait(L *lua.LState) int {
	if L == s.host {
		raiseError(L, CodeEINVAL, "Task:await solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	ud := L.CheckUserData(1)
	t, ok := ud.Value.(*task)
	if !ok {
		raiseError(L, CodeEINVAL, "Task:await espera un handle de Task", lua.LNil)
		return 0
	}
	if t.co == L {
		raiseError(L, CodeEINVAL, "una task no puede esperarse a sí misma", lua.LNil)
		return 0
	}

	t.awaited = true
	if !t.done {
		s.release()
		<-t.doneCh
		s.acquire()
	}

	if t.errValue != nil {
		L.Error(t.errValue, 1) // relanza el mismo objeto; no retorna (hace panic)
		return 0
	}
	for _, r := range t.results {
		L.Push(r)
	}
	return len(t.results)
}

// suspendEcho es la **primitiva ⏸ interna de prueba** del puente (S04). No es
// superficie pública: las pruebas la registran como global para ejercitar la
// suspensión de extremo a extremo —una task la llama, suelta el token, una
// goroutine de fondo acarrea el valor y, al volver, la task lo recibe—. Acepta
// string o number (datos triviales de copiar a Go sin tocar Lua desde la
// goroutine). Es el modelo en miniatura de toda primitiva ⏸ real.
func (s *scheduler) suspendEcho(L *lua.LState) int {
	if L == s.host {
		raiseError(L, CodeEINVAL, "suspend_echo solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	v := L.Get(1)
	var (
		str   string
		num   float64
		isNum bool
	)
	switch x := v.(type) {
	case lua.LString:
		str = string(x)
	case lua.LNumber:
		num = float64(x)
		isNum = true
	default:
		raiseError(L, CodeEINVAL, "suspend_echo espera string o number", lua.LNil)
		return 0
	}

	vals := s.suspend(L, func() deliverFn {
		// Goroutine de fondo: captura solo datos Go (str/num), nunca el LState.
		return func(L *lua.LState) []lua.LValue {
			if isNum {
				return []lua.LValue{lua.LNumber(num)}
			}
			return []lua.LValue{lua.LString(str)}
		}
	})
	for _, v := range vals {
		L.Push(v)
	}
	return len(vals)
}

// raisedValue extrae el objeto lanzado de un error de `CallByParam`/`PCall`.
// gopher-lua lo envuelve en `*lua.ApiError.Object` (la tabla estructurada de
// §1.4 cuando vino de `raiseError`/`error{...}`); si no, cae al texto del error.
func raisedValue(err error) lua.LValue {
	if ae, ok := err.(*lua.ApiError); ok {
		if ae.Object != nil && ae.Object != lua.LNil {
			return ae.Object
		}
	}
	return lua.LString(err.Error())
}

// errString rinde un error de task a texto para el log: si es la tabla
// estructurada (§1.4), saca `code: message`; si no, su representación por
// defecto.
func errString(v lua.LValue) string {
	if tbl, ok := v.(*lua.LTable); ok {
		code, _ := tbl.RawGetString("code").(lua.LString)
		msg, _ := tbl.RawGetString("message").(lua.LString)
		if code != "" || msg != "" {
			return string(code) + ": " + string(msg)
		}
	}
	return v.String()
}
