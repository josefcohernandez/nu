package runtime

import (
	lua "github.com/yuin/gopher-lua"
)

// Desenrollado NO capturable por `pcall` (api.md §1.3, sesión S08, inventario
// 🔒). Esta es la pieza que hace que la **cancelación** (`Task:cancel`, S08) y el
// **watchdog** (S09) aborten una task atravesando cualquier `pcall`/`xpcall` del
// usuario sin que estos lo atrapen —"si fueran errores normales, cualquier pcall
// los capturaría y el programa seguiría como si nada" (§1.3)—.
//
// EL PROBLEMA. El aborto se realiza con un **pánico Go** (el centinela
// `abortSignal`, ver scheduler.go) que desenrolla la pila de la goroutine de la
// task. Pero gopher-lua implementa `pcall`/`xpcall` en Go con un `recover()`
// (`LState.PCall`): recupera **cualquier** pánico Go —es el mismo motivo de
// ADR-011— y lo entrega a Lua como `false, err`. Por tanto, un `pcall` de usuario
// que envuelva un punto de suspensión atraparía el aborto. Inaceptable: §1.3
// exige que NO sea capturable.
//
// LA SOLUCIÓN (la técnica conocida del wrapper). Reemplazamos los globales
// `pcall` y `xpcall` —que el baseline de S01 controla (sandbox.go)— por versiones
// Go propias que delegan en el `pcall` nativo (`LState.PCall`) y, **si la task en
// curso está abortando** (`task.aborting`, puesto por `scheduler.abort` justo
// antes de lanzar el centinela), **re-lanzan** el centinela en vez de devolver
// `false, err` a Lua. Así el aborto "se cuela" por cada frontera `pcall`/`xpcall`
// hasta el `CallByParam` de `runTask`, que es quien legítimamente lo recupera,
// corre los `cleanup` y descarta el desenlace.
//
// POR QUÉ `task.aborting` Y NO EL VALOR DEL PÁNICO. Al cruzar `LState.PCall`, un
// pánico que no sea `*lua.ApiError` se convierte en un `*ApiError` con su mensaje
// vía `fmt.Sprint` —se pierde el tipo Go `abortSignal`—, así que detectar el
// aborto por el valor recuperado sería frágil. En cambio `aborting` es un flag de
// la propia task, escrito y leído por su única goroutine bajo el token: detección
// robusta e independiente de cómo gopher-lua represente el pánico. Sale gratis el
// re-lanzado idéntico: reconstruimos `abortSignal{t: t}` a partir de la task.
//
// LOS ERRORES NORMALES SIGUEN CAPTURÁNDOSE. Si la task NO está abortando, estos
// envoltorios se comportan EXACTAMENTE como los nativos: devuelven `false, err`
// para cualquier error de §1.4 (un `EINVAL`, un `error("texto")`, etc.). Solo el
// aborto —y solo mientras se está desenrollando— es inmune. Es decir: no rompemos
// `pcall` (§1.4), solo lo blindamos contra el aborto (§1.3).
//
// DÓNDE SE ATERRIZA. El chunk principal y los handlers síncronos corren sobre
// `host` (sin task en `coToTask`): ahí `aborting` nunca aplica, así que `pcall`
// se comporta como el nativo —no hay aborto que filtrar fuera de una task—. Los
// `cleanup` corren con `aborting` ya bajado (`runCleanups`), de modo que un
// `pcall` dentro de un cleanup vuelve a capturar con normalidad.

// installCancelPcall reemplaza los globales `pcall` y `xpcall` por las versiones
// envueltas. Lo llama `registerNu` tras `applySandbox` (que abre el baselib con
// los `pcall`/`xpcall` nativos): aquí los sustituimos. La superficie pública no
// cambia —siguen siendo `pcall(fn, ...)` y `xpcall(fn, errfn)` con su semántica
// de §1.4—; lo único que añadimos es la inmunidad al aborto de §1.3.
func (s *scheduler) installCancelPcall() {
	L := s.host
	L.SetGlobal("pcall", L.NewFunction(s.protectedPCall))
	L.SetGlobal("xpcall", L.NewFunction(s.protectedXPCall))
}

// reraiseIfAborting re-lanza el pánico centinela si la task en curso (la que
// corre sobre `L`) está abortando. Lo invocan `pcall`/`xpcall` envueltos
// **después** de que el `PCall` nativo capturó un error: si ese error es en
// realidad un aborto en curso, no debe entregarse a Lua sino seguir
// desenrollando. Si no hay task, o no está abortando, no hace nada (el error es
// uno normal de §1.4 y se devuelve a Lua como siempre).
func (s *scheduler) reraiseIfAborting(L *lua.LState) {
	if t, ok := s.taskOf(L); ok && t.aborting {
		panic(abortSignal{t: t})
	}
}

// protectedPCall es la versión envuelta de `pcall(f, ...)` (§1.4 + inmunidad al
// aborto de §1.3). Reproduce `basePCall` de gopher-lua —incluida la comprobación
// de "es llamable"— y, ante un error capturado, consulta si la task está
// abortando: si lo está, re-lanza el centinela (no capturable); si no, devuelve
// `false, err` como el nativo.
func (s *scheduler) protectedPCall(L *lua.LState) int {
	L.CheckAny(1)
	v := L.Get(1)
	if v.Type() != lua.LTFunction && L.GetMetaField(v, "__call").Type() != lua.LTFunction {
		L.Push(lua.LFalse)
		L.Push(lua.LString("attempt to call a " + v.Type().String() + " value"))
		return 2
	}
	nargs := L.GetTop() - 1
	if err := L.PCall(nargs, lua.MultRet, nil); err != nil {
		s.reraiseIfAborting(L) // aborto en curso → re-lanza; no captures
		L.Push(lua.LFalse)
		L.Push(errToLua(err))
		return 2
	}
	L.Insert(lua.LTrue, 1)
	return L.GetTop()
}

// protectedXPCall es la versión envuelta de `xpcall(f, errfn)` (§1.4 + inmunidad
// al aborto de §1.3). Reproduce `baseXPCall` de gopher-lua. Subraya por qué hace
// falta envolver también `xpcall`: su `errfn` (message handler) correría sobre el
// aborto si no lo filtráramos —el aborto NO debe pasar por el manejador de errores
// del usuario—, así que el re-lanzado se hace ANTES de que `PCall` invoque a
// `errfn`... salvo que gopher-lua ya la invocó dentro de su `PCall`. Como `errfn`
// se ejecuta dentro del `LState.PCall` nativo, para no dejar que toque el aborto
// pasamos `nil` como manejador al `PCall` nativo y aplicamos `errfn` nosotros
// solo si el error NO es un aborto.
func (s *scheduler) protectedXPCall(L *lua.LState) int {
	fn := L.CheckFunction(1)
	errfn := L.CheckFunction(2)

	top := L.GetTop()
	L.Push(fn)
	// Manejador nil al `PCall` nativo: queremos decidir nosotros si `errfn` corre.
	// Si corriera dentro del `PCall` nativo, el aborto pasaría por el manejador del
	// usuario antes de que pudiéramos filtrarlo.
	if err := L.PCall(0, lua.MultRet, nil); err != nil {
		s.reraiseIfAborting(L) // aborto en curso → re-lanza; ni `errfn` ni captura
		// Error normal de §1.4: aplica el manejador del usuario, como `xpcall`.
		L.Push(errfn)
		L.Push(errToLua(err))
		if hErr := L.PCall(1, 1, nil); hErr != nil {
			// El manejador mismo falló o fue abortado: respeta el aborto, y si no,
			// propaga el resultado del manejador como hace el nativo.
			s.reraiseIfAborting(L)
			L.Push(lua.LFalse)
			L.Push(errToLua(hErr))
			return 2
		}
		handlerRet := L.Get(-1)
		L.Pop(1)
		L.Push(lua.LFalse)
		L.Push(handlerRet)
		return 2
	}
	L.Insert(lua.LTrue, top+1)
	return L.GetTop() - top
}

// errToLua extrae el valor Lua que `pcall`/`xpcall` entregan como segundo retorno
// a partir del error de `LState.PCall`: el `Object` de la tabla estructurada
// (§1.4) si lo hay, o el texto del error en otro caso. Es exactamente lo que
// hacen `basePCall`/`baseXPCall` de gopher-lua.
func errToLua(err error) lua.LValue {
	if aerr, ok := err.(*lua.ApiError); ok {
		return aerr.Object
	}
	return lua.LString(err.Error())
}
