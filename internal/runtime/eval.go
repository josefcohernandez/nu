package runtime

import (
	lua "github.com/yuin/gopher-lua"
)

// EvalString compila y ejecuta `code` como un chunk Lua y devuelve sus valores
// de retorno convertidos a string (vía `tostring`), en orden. Es lo que respalda
// `nu -e`: el chunk `return nu.version.api` produce `["1"]`.
//
// Si el chunk lanza un error estructurado del core (§1.4), se devuelve como
// `*StructuredError` con su `code`/`message` intactos: el puente no traga ni
// reescribe el error al cruzar la frontera Lua→Go (invariante 🔒 de S02). Un
// error de sintaxis o un `error("string")` cualquiera se devuelve tal cual.
//
// El chunk de `nu -e` corre en el estado principal, **no es una task**: puede
// lanzar tasks con `nu.task.spawn` pero no usar funciones ⏸ (que exigen estar en
// una task, §1.3). Corre con el token Lua tomado; al soltarlo, las tasks que
// lanzó progresan, y `waitIdle` espera a que todas terminen antes de leer los
// valores de retorno del chunk (que viven en la pila del estado principal, que
// las tasks —en sus propios threads— nunca tocan).
func (rt *Runtime) EvalString(code string) ([]string, error) {
	L := rt.L
	s := rt.sched

	s.acquire()
	fn, err := L.LoadString(code)
	if err != nil {
		s.release()
		return nil, err
	}

	base := L.GetTop()
	L.Push(fn)
	perr := L.PCall(0, lua.MultRet, nil)
	s.release()

	// Espera a que las tasks lanzadas por el chunk corran a término (sus efectos,
	// sus liberaciones en S08) antes de devolver el control.
	s.waitIdle()

	s.acquire()
	defer s.release()

	if perr != nil {
		if se, ok := structuredFromError(perr); ok {
			return nil, se
		}
		return nil, perr
	}

	n := L.GetTop() - base
	results := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		v := L.Get(base + i)
		results = append(results, L.ToStringMeta(v).String())
	}
	L.SetTop(base)
	return results, nil
}
