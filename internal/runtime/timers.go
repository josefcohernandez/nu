package runtime

import (
	"time"

	lua "github.com/yuin/gopher-lua"
)

// Timers y diferidos del scheduler (api.md §3, sesión S05). Tres firmas, todas
// sobre el modelo goroutine-por-task + token Lua de S04 (ADR-011):
//
//   - `nu.task.sleep(ms)` ⏸ — suspende la task N ms sin congelar el loop. Es un
//     ⏸ puro: se construye sobre `suspend`, igual que `suspendEcho`. La goroutine
//     de fondo solo duerme (no toca Lua); mientras, otras tasks progresan.
//   - `nu.task.defer(fn)` — encola `fn` para el **siguiente tick** del loop. El
//     handler es **síncrono** (no ⏸): corre bajo el token, en su propio thread.
//   - `nu.task.every(ms, fn) -> Timer` — timer **periódico** de handler síncrono;
//     `Timer:stop()` lo corta sin dejar goroutines colgadas.
//
// Handlers síncronos vs tasks (decisión de §3): `defer`/`every` NO lanzan una
// task —su `fn` no puede suspender (no es ⏸)—. Corren como el chunk y los
// handlers de eventos: tomando el token y ejecutando hasta el final. Para que no
// corrompan la pila del estado principal (donde `EvalString` deja los valores de
// retorno del chunk mientras espera en `waitIdle`), cada disparo corre sobre un
// **thread Lua dedicado**, no sobre `host`. Y van **bajo `pcall`** por frontera
// (ADR-008): un error en el handler queda en el log (best-effort de S05, como
// `runTask`; el evento `core:plugin.misbehaved` llega en S10), nunca tumba el
// proceso.
//
// Quiescencia (decisión clave de S05, ver claude_decisions.md): `EvalString`
// vuelve cuando el conjunto queda quiescente (`waitIdle`). Un `defer` pendiente
// **sí** cuenta —es "el siguiente tick", debe correr antes de devolver—; un
// `every` activo **no** —un timer periódico nunca termina, y haría que `nu -e`
// no volviera jamás—. Es decir: el trabajo en primer plano (chunk + tasks +
// defers encolados) determina el fin; los timers periódicos son facilidad de
// fondo que `Close` apaga. En un `nu` interactivo (S33+) el loop sigue vivo por
// la UI; bajo `nu -e` el fin natural es la quiescencia del primer plano.

// timerTypeName identifica la metatabla del handle `Timer` (el que devuelve
// `every`), de la que cuelga `stop`.
const timerTypeName = "nu.task.Timer"

// luaTimer es el handle Go detrás del userdata `Timer`. Su goroutine corre un
// `time.Ticker`; `stopCh` la corta (cerrado por `stop`, idempotente vía
// `stopped`).
type luaTimer struct {
	s      *scheduler
	fn     *lua.LFunction
	stopCh chan struct{}

	// ownerName es el dueño con que se etiquetó el timer al crearse
	// (`currentOwner()` vigente en `every`, S11). Es lo que hace que
	// `nu.plugin.reload` (S13, G2) pare exactamente los timers de ESE plugin: un
	// `every` que un plugin arrancó en su `init.lua` no debe seguir tickeando tras
	// recargarlo —"reload no deja handlers huérfanos", inventario 🔒—.
	ownerName string
}

// luaTimer implementa ownedHandle (S13): el registro de handles por dueño
// (handles.go) lo para al recargar su plugin sin conocer su tipo concreto.

// release corta el timer (su goroutine de ticker), igual que `Timer:stop`.
// `stopTimer` es idempotente (no vuelve a cerrar `stopCh` si ya se cerró), así que
// liberar un timer ya parado es inocuo. NO toca el registro de handles (eso lo
// orquesta `releaseOwnerHandles`, que ya vació la lista del dueño).
func (t *luaTimer) release() { t.s.stopTimer(t) }

// owner devuelve el dueño con que se etiquetó el timer al crearse.
func (t *luaTimer) owner() string { return t.ownerName }

// registerTimers cuelga `sleep`, `defer` y `every` de la tabla `nu.task` ya
// creada por `scheduler.register`, e instala la metatabla del tipo `Timer`. Lo
// llama `scheduler.register` para mantener toda la superficie de `nu.task` junta.
func (s *scheduler) registerTimers(nu *lua.LTable, taskTbl *lua.LTable) {
	L := s.host

	mt := L.NewTypeMetatable(timerTypeName)
	index := L.NewTable()
	index.RawSetString("stop", L.NewFunction(s.timerStop))
	L.SetField(mt, "__index", index)

	taskTbl.RawSetString("sleep", L.NewFunction(s.taskSleep))
	taskTbl.RawSetString("defer", L.NewFunction(s.taskDefer))
	taskTbl.RawSetString("every", L.NewFunction(s.taskEvery))
}

// taskSleep implementa `nu.task.sleep(ms)` ⏸ (§3): suspende la task actual `ms`
// milisegundos. Fuera de una task lanza `EINVAL`, como cualquier ⏸ (§1.3): es la
// misma detección por estado de ejecución que `await`/`suspendEcho` (`L == host`
// es el chunk o un handler síncrono, no una task).
//
// Se construye sobre `suspend`: la goroutine de fondo solo duerme (`time.After`),
// sin tocar Lua, así que el token queda libre y otras tasks progresan mientras
// esta espera —"sleep no bloquea el loop". `ms` negativo o no-número es `EINVAL`;
// `ms == 0` cede el turno (duerme cero, pero suelta y recupera el token, dando
// paso a lo que esté listo).
func (s *scheduler) taskSleep(L *lua.LState) int {
	if L == s.host {
		raiseError(L, CodeEINVAL, "nu.task.sleep solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	ms := L.CheckNumber(1)
	if ms < 0 {
		raiseError(L, CodeEINVAL, "nu.task.sleep: ms no puede ser negativo", lua.LNil)
		return 0
	}
	d := time.Duration(ms) * time.Millisecond

	s.suspend(L, func() deliverFn {
		<-time.After(d) // duerme fuera del token; no toca Lua
		return func(L *lua.LState) []lua.LValue { return nil }
	})
	return 0
}

// taskDefer implementa `nu.task.defer(fn)` (§3): ejecuta `fn` en el siguiente
// tick del loop. Lo realiza como un timer de un solo disparo a 0 ms: una
// goroutine que no compite por el token (lo pide en `runSyncHandler`), así que
// `fn` corre cuando quien llamó a `defer` suelta el token. "Siguiente tick" =
// "tras ceder el control actual", que es exactamente lo que da soltar el token.
//
// A diferencia de `every`, un `defer` pendiente **cuenta** para la quiescencia
// (`pending`): es trabajo de primer plano que debe correr antes de que
// `EvalString` devuelva. Se cuenta al encolar y se descuenta al terminar el
// disparo (en `runSyncHandler`).
func (s *scheduler) taskDefer(L *lua.LState) int {
	fn := L.CheckFunction(1)
	s.addPending()
	go func() {
		s.runSyncHandler(fn)
		s.donePending()
	}()
	return 0
}

// taskEvery implementa `nu.task.every(ms, fn) -> Timer` (§3): dispara `fn` cada
// `ms` milisegundos como handler síncrono, hasta que `Timer:stop()`. Devuelve el
// handle. `ms <= 0` es `EINVAL` (un periodo no positivo sería un bucle ocupado).
//
// El timer periódico **no** cuenta para la quiescencia (no toca `pending`): no
// debe impedir que `nu -e` termine. Su goroutine la corta `stop` (o `Close`, vía
// el cierre del proceso). Cada disparo corre bajo el token en su thread, bajo
// `pcall` (`runSyncHandler`).
func (s *scheduler) taskEvery(L *lua.LState) int {
	ms := L.CheckNumber(1)
	fn := L.CheckFunction(2)
	if ms <= 0 {
		raiseError(L, CodeEINVAL, "nu.task.every: ms debe ser positivo", lua.LNil)
		return 0
	}

	t := &luaTimer{s: s, fn: fn, stopCh: make(chan struct{}), ownerName: s.rt.currentOwner()}
	d := time.Duration(ms) * time.Millisecond

	s.trackTimer(t)
	s.track(t) // registro de handles por dueño (S13): que `reload` lo encuentre y pare
	go func() {
		ticker := time.NewTicker(d)
		defer ticker.Stop()
		for {
			select {
			case <-t.stopCh:
				return
			case <-ticker.C:
				if !s.runSyncHandlerCancelable(fn, t.stopCh) {
					return // se pidió `stop` mientras esperábamos el token
				}
			}
		}
	}()

	ud := L.NewUserData()
	ud.Value = t
	L.SetMetatable(ud, L.GetTypeMetatable(timerTypeName))
	L.Push(ud)
	return 1
}

// timerStop implementa `Timer:stop()` (§3): corta el timer periódico sin dejar
// goroutines colgadas. Idempotente —cerrar dos veces el canal entraría en
// pánico, así que el cierre lo protege el scheduler (`stopTimer`)—. Tras `stop`
// no hay más disparos: la goroutine ve `stopCh` cerrado y retorna; un disparo que
// estuviera esperando el token también aborta al ver el cierre.
func (s *scheduler) timerStop(L *lua.LState) int {
	ud := L.CheckUserData(1)
	t, ok := ud.Value.(*luaTimer)
	if !ok {
		raiseError(L, CodeEINVAL, "Timer:stop espera un handle de Timer", lua.LNil)
		return 0
	}
	s.stopTimer(t)
	// Desregistra del registro de handles por dueño (S13): un `stop` a mano no debe
	// dejar el timer parado colgando en `ownerHandles` (fuga; un `reload` posterior
	// intentaría re-pararlo). `untrack` es idempotente. Va aquí —en el camino
	// manual—, no en `stopTimer`, porque `release()` (vía `reload`) ya vacía la
	// lista del dueño y no debe tocarla a media iteración.
	s.untrack(t)
	return 0
}

// runSyncHandler corre un handler síncrono (`defer` o un disparo de `every`)
// tomando el token y ejecutándolo en un thread Lua dedicado, bajo `pcall`. No
// suspende: el handler no es ⏸. Un error queda en el log (best-effort, ADR-008;
// el evento formal llega en S10), nunca tumba el proceso.
func (s *scheduler) runSyncHandler(fn *lua.LFunction) {
	s.acquire()
	defer s.release()
	s.callSyncLocked(fn)
}

// runSyncHandlerCancelable es como runSyncHandler pero, mientras espera el token,
// atiende también a `stopCh`: si se pide `stop` antes de conseguir correr, no
// corre y devuelve false (la goroutine del timer debe terminar). Devuelve true si
// llegó a tomar el token y ejecutar el handler.
//
// Sin esto, un `stop` justo después de un tick podría colarse con un disparo
// extra que ya estaba bloqueado esperando el token: el contrato es "tras `stop`,
// ningún tick más".
func (s *scheduler) runSyncHandlerCancelable(fn *lua.LFunction, stopCh chan struct{}) bool {
	select {
	case <-s.gil:
		// Token tomado a mano (equivale a acquire); hay que soltarlo al salir.
		defer s.release()
		select {
		case <-stopCh:
			// Se pidió stop entre el tick y la toma del token: no dispares.
			return false
		default:
		}
		s.callSyncLocked(fn)
		return true
	case <-stopCh:
		return false
	}
}

// callSyncLocked ejecuta `fn` (sin argumentos) sobre un thread efímero, bajo
// `pcall`. **Presupone el token ya tomado.** El thread se crea por disparo —es la
// misma estrategia que las tasks (cada una sobre su `co`)— para no tocar la pila
// del estado principal, que mientras `EvalString` está en `waitIdle` aún
// custodia los valores de retorno del chunk.
func (s *scheduler) callSyncLocked(fn *lua.LFunction) {
	co, _ := s.host.NewThread()
	err := co.CallByParam(lua.P{Fn: fn, NRet: 0, Protect: true})
	if err != nil {
		_ = s.rt.log.write(levelError, s.rt.currentOwner(),
			"un handler síncrono (defer/every) lanzó: "+errString(raisedValue(err)))
	}
	// El thread efímero queda para que lo recoja el GC de gopher-lua; no hay
	// `Close` por thread en la API, igual que para los `co` de las tasks.
}
