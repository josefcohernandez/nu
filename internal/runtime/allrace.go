package runtime

import (
	"strconv"

	lua "github.com/yuin/gopher-lua"
)

// Combinadores de tasks: `nu.task.all` y `nu.task.race` (api.md §3, sesión S07).
// Ambos orquestan un fan-out de tasks y son **⏸**: corren dentro de una task,
// suspenden hasta que el conjunto resuelve, y devuelven el resultado. Se apoyan en
// el modelo de S04 (goroutine-por-task + token Lua, ADR-011) y en el **substrato
// de cancelación interno** de S07 (scheduler.go: `cancelTask`/`cancelCh`), que es
// lo mínimo para "cancelar el resto" sin la cancelación pública de S08.
//
// Las dos firmas aceptan una **tabla-array** cuyos elementos son, indistintamente,
// **handles `Task` ya creados** (a los que se adjuntan) o **funciones** (a las que
// hacen `spawn`). Lo que devuelve cada task es su **primer valor de retorno** (el
// modelo de §3: `all -> any[]`, `race -> (i, result)` —un valor por entrada—).
//
// Invariante 🔒 de S07 (G27): en `all`, `out[i]` es el resultado de `fns[i]`,
// **alineado con la entrada**, NUNCA en orden de terminación. Esto es lo que deja
// correlacionar resultado con entrada en un fan-out sin acarrear el índice a mano
// ([api.md](api.md) §3). El alineamiento sale gratis de indexar por posición: se
// rellena `out[i]` con el resultado de la task de la posición `i`, no del orden en
// que cierran sus `doneCh`.

// registerAllRace cuelga `all` y `race` de la tabla `nu.task` ya creada por
// `scheduler.register`. Se llama desde ahí para mantener junta la superficie de
// `nu.task`.
func (s *scheduler) registerAllRace(taskTbl *lua.LTable) {
	L := s.host
	taskTbl.RawSetString("all", L.NewFunction(s.taskAll))
	taskTbl.RawSetString("race", L.NewFunction(s.taskRace))
}

// resolveTasks toma el argumento 1 (una tabla-array de funciones o handles Task) y
// devuelve la lista de tasks a esperar, **en orden de la tabla** (clave 1..n).
// Cada función se lanza con `spawn`; cada handle Task se adjunta tal cual. Tasks
// recién creadas aquí se anotan en `spawned` para poder cancelarlas si algo falla
// antes de devolverlas (no aplica a las adjuntas: no las creamos nosotros, pero
// `all`/`race` sí las cancelan al perder, que es su contrato).
//
// Errores de forma (no es tabla, hueco en el array, elemento de tipo inválido) son
// `EINVAL`: `all`/`race` no adivinan, exigen un array bien formado.
func (s *scheduler) resolveTasks(L *lua.LState, who string) []*task {
	tbl := L.CheckTable(1)
	n := tbl.Len()
	if n == 0 {
		raiseError(L, CodeEINVAL, who+": la lista de tasks no puede estar vacía", lua.LNil)
		return nil
	}

	tasks := make([]*task, n)
	for i := 1; i <= n; i++ {
		v := tbl.RawGetInt(i)
		switch x := v.(type) {
		case *lua.LFunction:
			tasks[i-1] = s.spawn(x, nil)
		case *lua.LUserData:
			t, ok := x.Value.(*task)
			if !ok {
				raiseError(L, CodeEINVAL, who+": el elemento "+strconv.Itoa(i)+" no es una Task ni una función", lua.LNil)
				return nil
			}
			tasks[i-1] = t
		default:
			raiseError(L, CodeEINVAL, who+": el elemento "+strconv.Itoa(i)+" no es una Task ni una función", lua.LNil)
			return nil
		}
	}
	return tasks
}

// taskAll implementa `nu.task.all(fns) -> any[]` ⏸ (§3). Espera a TODAS las tasks;
// si una lanza, **cancela el resto** y **relanza ese mismo error**. Si todas
// terminan bien, devuelve un array con `out[i]` = primer valor de retorno de la
// task `i` (alineado con la entrada, G27).
//
// El cuerpo es un único `suspend`: la goroutine de fondo (que NO toca Lua) hace el
// fan-in sobre los `doneCh` de las tasks. Distingue dos desenlaces:
//   - **una falló**: devuelve su índice; al recuperar el token, `taskAll` cancela
//     a las demás y relanza el `errValue` de la fallida (capturable con `pcall`;
//     no es cancelación, es el error genuino —§1.3).
//   - **todas bien**: la `deliverFn` construye el array alineado bajo el token.
//
// Las tasks canceladas no entregan resultado (substrato S07); `all` no las espera
// a que terminen de desenrollar: con haber pedido la cancelación basta para su
// contrato ("cancela el resto"). El orden de terminación es irrelevante para el
// resultado: se indexa por posición.
func (s *scheduler) taskAll(L *lua.LState) int {
	if L == s.host {
		raiseError(L, CodeEINVAL, "nu.task.all solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	tasks := s.resolveTasks(L, "nu.task.all")

	// failed != -1 marca el índice (0-based) de la primera task que lanzó.
	failed := -1
	s.suspend(L, func() deliverFn {
		// Fan-in **concurrente** sin tocar Lua: una goroutine por task espera su
		// `doneCh` y, si esa task falló, reporta su índice. Hay que detectar el
		// PRIMER error en cuanto ocurre —no en orden de array—, para cancelar al
		// resto cuanto antes (si esperáramos en orden, una primera task lenta
		// retrasaría ver el fallo de una segunda rápida: ver TestAllCancelsOthers).
		// Si ninguna falla, esperamos a que todas cierren. El cierre de `doneCh`
		// aporta el happens-before para leer `errValue`/`results`.
		failed = waitAllOrFirstError(tasks)
		return func(L *lua.LState) []lua.LValue { return nil }
	})

	if failed != -1 {
		// Cancela a todas las demás (las que aún no terminaron abortarán en su
		// próximo ⏸; las ya terminadas, no-op) y relanza el error de la fallida.
		for i, t := range tasks {
			if i != failed {
				s.cancelTask(t)
			}
		}
		L.Error(tasks[failed].errValue, 1) // relanza el objeto original (intacto); no retorna
		return 0
	}

	// Todas bien: array alineado con la entrada (G27). `out[i]` = primer valor de
	// retorno de la task `i` (o `nil` si no retornó nada).
	out := L.NewTable()
	for i, t := range tasks {
		out.RawSetInt(i+1, firstResult(t))
	}
	L.Push(out)
	return 1
}

// taskRace implementa `nu.task.race(fns) -> (winner_index, result)` ⏸ (§3). La
// primera task en terminar gana; devuelve su **índice 1-based** (Lua) y su primer
// valor de retorno, y **cancela el resto**.
//
// "Primera en terminar" incluye terminar por error: si la ganadora lanzó, `race`
// relanza ese error (tras cancelar a las demás) —es lo coherente con `all` y con
// que el error sea el desenlace genuino de esa task—. La carrera la resuelve la
// goroutine de fondo con un `select` dinámico sobre los `doneCh`; al primer cierre,
// fija el ganador.
func (s *scheduler) taskRace(L *lua.LState) int {
	if L == s.host {
		raiseError(L, CodeEINVAL, "nu.task.race solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	tasks := s.resolveTasks(L, "nu.task.race")

	winner := -1
	s.suspend(L, func() deliverFn {
		winner = waitFirst(tasks) // 0-based; bloquea hasta el primer `doneCh` cerrado
		return func(L *lua.LState) []lua.LValue { return nil }
	})

	// Cancela a las perdedoras (todas menos la ganadora).
	for i, t := range tasks {
		if i != winner {
			s.cancelTask(t)
		}
	}

	w := tasks[winner]
	if w.errValue != nil {
		L.Error(w.errValue, 1) // la ganadora terminó por error: relánzalo
		return 0
	}
	L.Push(lua.LNumber(winner + 1)) // índice 1-based (Lua), no 0-based (G27/§3)
	L.Push(firstResult(w))
	return 2
}

// waitFirst bloquea hasta que alguna de las tasks cierre su `doneCh` y devuelve su
// índice (0-based). No toca Lua: corre en la goroutine de fondo de `suspend`. Usa
// reflect-free fan-in con una goroutine por task que reporta a un canal común el
// índice del primero en cerrar; las goroutines sobrantes terminan al ver `done`.
//
// Es lineal en el nº de tasks (una goroutine efímera por task) y determinista en
// el sentido de §3: el "primero" es el primero cuyo `doneCh` se cierra observado
// por el runtime; empates los resuelve el scheduler de Go (cualquiera vale, el
// contrato no promete más).
func waitFirst(tasks []*task) int {
	first := make(chan int, len(tasks))
	done := make(chan struct{})
	defer close(done)
	for i, t := range tasks {
		go func(i int, t *task) {
			select {
			case <-t.doneCh:
				select {
				case first <- i:
				case <-done:
				}
			case <-done:
			}
		}(i, t)
	}
	return <-first
}

// waitAllOrFirstError espera a que TODAS las tasks terminen, salvo que alguna
// falle: en cuanto una task cierra su `doneCh` con `errValue != nil`, devuelve su
// índice (0-based) sin esperar al resto. Si todas terminan bien, devuelve -1. No
// toca Lua: corre en la goroutine de fondo de `suspend`.
//
// Una goroutine efímera por task espera su cierre y reporta `(índice, falló)` a un
// canal común. El bucle principal cuenta cuántas han terminado bien; al primer
// fallo corta (las goroutines restantes terminan al ver `done`). El cierre de
// `doneCh` da el happens-before para leer `errValue` con seguridad.
func waitAllOrFirstError(tasks []*task) int {
	type report struct {
		idx    int
		failed bool
	}
	reports := make(chan report, len(tasks))
	done := make(chan struct{})
	defer close(done)
	for i, t := range tasks {
		go func(i int, t *task) {
			select {
			case <-t.doneCh:
				select {
				case reports <- report{idx: i, failed: t.errValue != nil}:
				case <-done:
				}
			case <-done:
			}
		}(i, t)
	}
	ok := 0
	for ok < len(tasks) {
		r := <-reports
		if r.failed {
			return r.idx
		}
		ok++
	}
	return -1
}

// firstResult devuelve el primer valor de retorno de una task terminada bien, o
// `nil` si no retornó nada. `all`/`race` entregan un valor por task (§3: el array
// y el `result` son de un valor por entrada, no multivalor).
func firstResult(t *task) lua.LValue {
	if len(t.results) > 0 {
		return t.results[0]
	}
	return lua.LNil
}
