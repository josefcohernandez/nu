package runtime

import (
	lua "github.com/yuin/gopher-lua"
)

// Futures: rendez-vous de un solo uso (api.md §3, sesión S06). La pieza para
// "una task espera un valor que otro código producirá" —diálogos, pickers,
// proxies— sin polling. Dos métodos sobre el modelo goroutine-por-task + token
// Lua de S04 (ADR-011):
//
//   - `Future:set(v)` — **síncrono**, una sola vez. Corre bajo el token, en
//     cualquier contexto (chunk, task o handler síncrono): resuelve el valor y
//     despierta a TODOS los que esperan. Un segundo `set` lanza `EINVAL`.
//   - `Future:await() -> v` ⏸ — varios pueden esperar el mismo future. Si ya
//     está resuelto, retorna en el acto; si no, **suelta el token** y bloquea
//     hasta que `set` resuelva, entonces recupera el token y lee el valor. Como
//     todo ⏸, fuera de una task es `EINVAL` (§1.3).
//
// Por qué se parece tanto a `Task:await`: es el mismo patrón de suspensión por
// suelta/recupera del token con un canal de señalización (`resolvedCh` aquí,
// `doneCh` en `task`). La diferencia es la dirección: un `Task:await` espera a
// que *una task* termine; un `Future:await` espera a que *alguien* haga `set`,
// que puede ser un handler síncrono que ni siquiera es una task.
//
// Concurrencia y cero data races (inventario 🔒 de S06): tanto `set` como el
// tramo de `await` que lee el valor corren **bajo el token**, así que `resolved`
// y `value` se tocan en exclusión mutua sin necesidad de candado propio —el
// token es el candado—. El único cruce entre goroutines es el cierre de
// `resolvedCh`: lo cierra `set` (bajo token) y lo esperan los awaiters (sin
// token, como en `taskAwait`); el cierre del canal aporta el happens-before que
// hace visible el `value` ya escrito cuando el awaiter recupera el token.
//
// Quiescencia (decisión clave de S06, ver claude_decisions.md): ni `set` ni
// `await` tocan `live`/`pending`. Un awaiter bloqueado en `Future:await` es una
// task que **ya** está contada en `live` (se contó al hacer `spawn`); no termina
// hasta que el `await` retorna, exactamente igual que una task bloqueada en
// `Task:await`. Por tanto un `await` sin un `set` que lo resuelva cuelga
// `waitIdle` —es el mismo "deadlock de primer plano" que una task esperando a
// otra que nunca acaba—; no es responsabilidad del future arreglarlo, y hacerlo
// exigiría API nueva (detección de deadlock) que §3 no contempla.

// futureTypeName identifica la metatabla del handle `Future` (el que devuelve
// `nu.task.future`), de la que cuelgan `set` y `await`.
const futureTypeName = "nu.task.Future"

// luaFuture es el handle Go detrás del userdata `Future`. `resolvedCh` es el
// canal de rendez-vous: cerrado por `set` (bajo token), esperado por los
// awaiters pendientes (igual papel que `doneCh` en `task`). `resolved` y `value`
// solo se tocan bajo el token, así que no llevan candado propio.
type luaFuture struct {
	resolvedCh chan struct{} // se cierra (bajo token) cuando `set` resuelve
	resolved   bool          // ¿ya hubo `set`? legible bajo token sin select
	value      lua.LValue    // el valor resuelto; válido solo si `resolved`
}

// registerFuture cuelga `future` de la tabla `nu.task` ya creada por
// `scheduler.register`, e instala la metatabla del tipo `Future`. Lo llama
// `scheduler.register` para mantener toda la superficie de `nu.task` junta.
func (s *scheduler) registerFuture(taskTbl *lua.LTable) {
	L := s.host

	mt := L.NewTypeMetatable(futureTypeName)
	index := L.NewTable()
	index.RawSetString("set", L.NewFunction(s.futureSet))
	index.RawSetString("await", L.NewFunction(s.futureAwait))
	L.SetField(mt, "__index", index)

	taskTbl.RawSetString("future", L.NewFunction(s.taskFuture))
}

// taskFuture implementa `nu.task.future() -> Future` (§3): crea un rendez-vous
// de un solo uso, sin resolver. Puede llamarse en cualquier contexto (no es ⏸):
// el future es solo un handle; lo que suspende es `await`.
func (s *scheduler) taskFuture(L *lua.LState) int {
	f := &luaFuture{resolvedCh: make(chan struct{})}

	ud := L.NewUserData()
	ud.Value = f
	L.SetMetatable(ud, L.GetTypeMetatable(futureTypeName))
	L.Push(ud)
	return 1
}

// futureSet implementa `Future:set(v)` (§3): resuelve el future, **una sola
// vez**, de forma síncrona. Corre bajo el token —el chunk, una task o un handler
// síncrono lo invocan teniéndolo—, así que escribir `value`/`resolved` no
// necesita candado. Un segundo `set` lanza `EINVAL`: el rendez-vous es de un
// solo uso (inventario 🔒). Cerrar `resolvedCh` despierta a TODOS los awaiters
// pendientes a la vez (broadcast por cierre de canal, como `doneCh`).
//
// `v` es opcional: `set()` sin argumento resuelve con `nil` (un future puede
// usarse como mera señal "ya ocurrió", no solo como portador de valor).
func (s *scheduler) futureSet(L *lua.LState) int {
	ud := L.CheckUserData(1)
	f, ok := ud.Value.(*luaFuture)
	if !ok {
		raiseError(L, CodeEINVAL, "Future:set espera un handle de Future", lua.LNil)
		return 0
	}
	if f.resolved {
		raiseError(L, CodeEINVAL, "Future:set ya fue llamado: el rendez-vous es de un solo uso", lua.LNil)
		return 0
	}

	// `L.Get(2)` es `LNil` si no se pasó valor: `set()` resuelve con nil.
	f.value = L.Get(2)
	f.resolved = true
	close(f.resolvedCh) // despierta a todos los awaiters pendientes
	return 0
}

// futureAwait implementa `Future:await() -> v` ⏸ (§3): espera a que alguien haga
// `set`. Si ya está resuelto, devuelve el valor en el acto (bajo el token que ya
// tiene); si no, **suelta el token** y bloquea en `resolvedCh` hasta que `set`
// lo cierre, entonces recupera el token y lee el valor —mismo puente que
// `Task:await`—. Varios `await` sobre el mismo future son legales: todos esperan
// el mismo canal y, tras el cierre, todos leen el mismo `value`.
//
// `await` es ⏸: fuera de una task es un error (§1.3) → `EINVAL`, con la misma
// detección por estado de ejecución que el resto de ⏸ (`L == host` es el chunk o
// un handler síncrono, no una task).
func (s *scheduler) futureAwait(L *lua.LState) int {
	if L == s.host {
		raiseError(L, CodeEINVAL, "Future:await solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	ud := L.CheckUserData(1)
	f, ok := ud.Value.(*luaFuture)
	if !ok {
		raiseError(L, CodeEINVAL, "Future:await espera un handle de Future", lua.LNil)
		return 0
	}

	// Substrato de cancelación (S07): un awaiter de future cancelado mientras
	// espera aborta igual que en `suspend`/`Task:await`. Misma frontera S07/S08.
	self, hasSelf := s.taskOf(L)
	if hasSelf && isClosed(self.cancelCh) {
		s.abort(self)
	}
	if !f.resolved {
		s.release()
		if hasSelf {
			select {
			case <-f.resolvedCh:
				s.acquire()
			case <-self.cancelCh:
				s.acquire()
				s.abort(self)
			}
		} else {
			<-f.resolvedCh
			s.acquire()
		}
	}

	// Con el token (re)tomado, `value` es seguro de leer: o ya estaba resuelto al
	// entrar, o el cierre de `resolvedCh` lo hizo visible (happens-before).
	L.Push(f.value)
	return 1
}
