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
	canceled bool         // se canceló (substrato S07): no se entrega su resultado

	// cancelCh es el **substrato de cancelación interno** que S07 necesita para
	// que `all`/`race` aborten a las tasks perdedoras (ver cancelTask y suspend).
	// Se cierra una sola vez —`cancelOnce` lo protege— y los puntos de suspensión
	// lo observan por `select` para abortar la task en su siguiente ⏸.
	//
	// FRONTERA S07/S08 (ver claude_decisions.md): esto es el SUBSTRATO mínimo. La
	// cancelación PÚBLICA —`Task:cancel()`, `nu.task.cleanup` con su pila LIFO,
	// `ECANCELED` observable en `await`, y la garantía formal de que el
	// desenrollado NO es capturable por un `pcall` de usuario— es S08, que
	// reutilizará y extenderá este canal, no lo reescribirá. Aquí solo se exige lo
	// justo para S07: que una perdedora deje de ejecutar Lua y no entregue su
	// resultado.
	cancelCh   chan struct{}
	cancelOnce sync.Once
}

// abortSignal es el valor del **pánico centinela** con que una task cancelada
// desenrolla su pila Go en el siguiente punto de suspensión (lo lanza `suspend`
// al detectar `cancelCh` cerrado). `runTask` lo recupera y NO lo confunde con un
// error Lua: la task queda marcada `canceled`, sin `results` ni `errValue`.
//
// FRONTERA S07/S08: este centinela es el embrión del **desenrollado no
// capturable** de §1.3. En S07 desenrolla la pila Go de la goroutine de la task
// —que un `pcall` de Lua, al ser una llamada Go anidada en esa misma pila, no
// puede atrapar porque es un panic de Go, no un `error()` de Lua—. S08 lo
// formaliza: correrá la pila LIFO de `nu.task.cleanup` durante el desenrollado y
// hará `ECANCELED` observable en `await`. La forma del centinela está pensada
// para que S08 la reutilice (un tipo distinguible), no para reescribirla.
type abortSignal struct{ t *task }

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

	// coToTask mapea el thread Lua de cada task viva (su `co`) a su `*task`. Lo
	// usa `suspend` para observar el `cancelCh` de la task que se suspende (S07):
	// quien suspende es la goroutine de esa task, que corre sobre `co == L`. Se
	// puebla en `runTask` y se limpia al terminar. `sync.Map` porque se escribe
	// desde la goroutine de cada task y se lee desde `suspend` sin el token.
	coToTask sync.Map // *lua.LState -> *task
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

	// Futures (S06): `nu.task.future` + el tipo `Future` con `set`/`await`,
	// colgados de la misma tabla `nu.task` (future.go).
	s.registerFuture(taskTbl)

	// Combinadores (S07): `nu.task.all` y `nu.task.race`, colgados de la misma
	// tabla `nu.task` (allrace.go).
	s.registerAllRace(taskTbl)
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
	t := &task{co: co, fn: fn, args: args, doneCh: make(chan struct{}), cancelCh: make(chan struct{})}
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

	s.coToTask.Store(t.co, t) // para que `suspend` halle el `cancelCh` de esta task

	err := t.co.CallByParam(lua.P{Fn: t.fn, NRet: lua.MultRet, Protect: true}, t.args...)

	s.coToTask.Delete(t.co)

	switch {
	case t.canceled:
		// La task fue abortada por `cancelTask` (substrato S07): `suspend` lanzó el
		// pánico centinela en su último ⏸ y `CallByParam` lo devolvió como error.
		// No es un desenlace observable —ni `results` ni `errValue`—: una task
		// cancelada simplemente no entrega resultado (S08 hará `ECANCELED`
		// observable en `await`). Tampoco se loguea: la cancelación es deliberada,
		// no un fallo. `err` se ignora a propósito.
	case err == nil:
		if n := t.co.GetTop(); n > 0 {
			t.results = make([]lua.LValue, n)
			for i := 1; i <= n; i++ {
				t.results[i-1] = t.co.Get(i)
			}
		}
	default:
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
	// Substrato de cancelación (S07): si esta task ya está cancelada al llegar al
	// punto de suspensión, aborta sin siquiera lanzar el trabajo de fondo. Si se
	// cancela *mientras* está suspendida, el `select` de abajo la despierta por
	// `cancelCh` en vez de por el resultado.
	t, hasTask := s.taskOf(L)
	if hasTask && isClosed(t.cancelCh) {
		s.abort(t)
	}

	ch := make(chan deliverFn, 1)
	go func() { ch <- work() }()
	s.release()

	var d deliverFn
	if hasTask {
		select {
		case d = <-ch:
		case <-t.cancelCh:
			// Cancelada mientras esperaba: recupera el token y desenrolla. El
			// trabajo de fondo seguirá hasta su fin (su `deliverFn` se descarta);
			// para `sleep` es solo un `time.After`. S08 podrá propagar la
			// cancelación al trabajo en curso vía `context`.
			s.acquire()
			s.abort(t)
		}
	} else {
		d = <-ch // sin task (no debería ocurrir en ⏸ reales): comportamiento de S04
	}

	s.acquire()
	return d(L)
}

// taskOf devuelve la task cuya goroutine corre sobre el thread `L`, si la hay.
// El chunk principal y los handlers síncronos corren sobre `host` (sin task).
func (s *scheduler) taskOf(L *lua.LState) (*task, bool) {
	v, ok := s.coToTask.Load(L)
	if !ok {
		return nil, false
	}
	return v.(*task), true
}

// cancelTask es el **punto de entrada del substrato de cancelación** (S07). Lo
// llaman `all`/`race` para abortar a las tasks perdedoras. Marca la task como
// cancelada y cierra su `cancelCh` (una sola vez, `cancelOnce`); la task abortará
// en su siguiente punto de suspensión (`suspend`). Si la task ya terminó, es un
// no-op inocuo. Puede llamarse con el token tomado (lo está siempre `all`/`race`,
// que son ⏸ y corren bajo token entre suspensiones).
//
// FRONTERA S07/S08: S08 expondrá esto como `Task:cancel()` público, correrá la
// pila LIFO de `nu.task.cleanup` durante el aborto y dejará `ECANCELED`
// observable en `await`. Aquí solo cierra el canal: lo mínimo para que la
// perdedora deje de ejecutar Lua.
func (s *scheduler) cancelTask(t *task) {
	t.cancelOnce.Do(func() {
		t.canceled = true
		close(t.cancelCh)
	})
}

// abort desenrolla la pila Go de la task lanzando el pánico centinela. Lo llama
// `suspend` cuando detecta `cancelCh` cerrado. El pánico sube por la pila Go de
// la goroutine de la task hasta el `CallByParam` de `runTask`, que lo recupera
// (gopher-lua convierte cualquier pánico Go en error al cruzar `PCall`); `runTask`
// ve `t.canceled` y descarta el desenlace.
//
// FRONTERA S07/S08: en S07 este desenrollado SÍ podría ser atrapado por un
// `pcall` de Lua que envuelva el punto de suspensión (gopher-lua recupera todo
// pánico Go en su `PCall` interno —el mismo motivo de ADR-011—). Para S07 basta
// porque las perdedoras de `all`/`race` no envuelven su ⏸ en `pcall`. La garantía
// formal de "**no capturable** por `pcall`" es S08: requerirá su propio mecanismo
// (re-lanzar el centinela tras cada frontera `pcall`, o marcar el thread como
// "abortando" para que el `pcall` de usuario no lo trague). El tipo `abortSignal`
// está pensado para que S08 lo reconozca y reinyecte, no para reescribirlo.
func (s *scheduler) abort(t *task) {
	panic(abortSignal{t: t})
}

// isClosed comprueba sin bloquear si un canal-señal (cerrado para difundir) ya
// fue cerrado. Solo válido para canales que únicamente se cierran (nunca se les
// envía), como `cancelCh`.
func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
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
	// Substrato de cancelación (S07): si la task que espera (`self`) es cancelada
	// mientras está bloqueada en este `await`, debe abortar igual que en `suspend`.
	self, hasSelf := s.taskOf(L)
	if hasSelf && isClosed(self.cancelCh) {
		s.abort(self)
	}
	if !t.done {
		s.release()
		if hasSelf {
			select {
			case <-t.doneCh:
				s.acquire()
			case <-self.cancelCh:
				s.acquire()
				s.abort(self)
			}
		} else {
			<-t.doneCh
			s.acquire()
		}
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
