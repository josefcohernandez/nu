package runtime

import (
	"strings"
	"testing"
)

// Tests de `enu.task.future` (api.md §3, sesión S06). Future está en el
// inventario de lógica clave 🔒 del plan: cada caso límite lleva test que lo
// nombra para blindarlo de regresiones. La lógica a blindar:
//
//   - `set` una sola vez: el segundo `set` lanza `EINVAL`.
//   - varios `await` ven el valor ya resuelto, tanto si el `set` precede al
//     `await` como al revés; varios awaiters concurrentes despiertan de un único
//     `set`.
//   - `await` fuera de una task lanza `EINVAL` (es ⏸, §1.3).
//
// El arnés (harness_test.go) corre cada snippet contra un Runtime real; donde el
// orden de ejecución importa, se fuerza con `suspend_echo` (la primitiva ⏸
// interna de S04, registrada por `withEcho`) para que el awaiter suspenda de
// verdad antes de que llegue el `set`.

// --- Snippet que ejercita cada firma (Definition of Done §2) ---

// TestFutureSetThenAwaitSnippet: el camino feliz que ejercita las tres firmas
// (`future`, `set`, `await`) desde el lado del autor de extensiones, con el `set`
// **antes** de que el awaiter espere. Cubre la rama "ya resuelto" de `await`.
func TestFutureSetThenAwaitSnippet(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		local f = enu.task.future()
		f:set("listo")            -- resuelto antes de cualquier await
		enu.task.spawn(function()
			out.v = f:await()     -- ve el valor ya resuelto, retorna inmediato
		end)
	`)
	h.expectEval(`return out.v`, "listo")
}

// TestFutureAwaitThenSet: el otro orden —el awaiter suspende **antes** de que
// nadie haga `set`—. Una task espera el future (suelta el token y se bloquea);
// otra, tras una suspensión real, hace `set`. El awaiter despierta con el valor.
// Es el caso "rendez-vous" de verdad: el productor llega después del consumidor.
func TestFutureAwaitThenSet(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = {}
		local f = enu.task.future()
		enu.task.spawn(function()
			out.v = f:await()         -- suspende: aún no hay set
		end)
		enu.task.spawn(function()
			suspend_echo("x")         -- cede el turno; el awaiter ya está esperando
			f:set("desde productor")
		end)
	`)
	h.expectEval(`return out.v`, "desde productor")
}

// --- 🔒 set una sola vez ---

// TestFutureSetOnceRejectsSecond: 🔒 el segundo `set` lanza `EINVAL`. El
// rendez-vous es de un solo uso (§3). El primer `set` resuelve; el segundo, sobre
// el mismo handle, es un error estructurado capturable.
func TestFutureSetOnceRejectsSecond(t *testing.T) {
	h := newHarness(t)

	se := h.evalErr(`
		local f = enu.task.future()
		f:set(1)
		f:set(2)            -- segundo set: prohibido
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("segundo set: code got %q, want %q", se.Code, CodeEINVAL)
	}
}

// TestFutureSetOnceErrorCatchable: 🔒 el `EINVAL` del segundo `set` es un error
// normal (no una cancelación), así que `pcall` lo captura —y el primer `set`
// queda firme: el future conserva su valor original pese al intento fallido.
func TestFutureSetOnceErrorCatchable(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		local f = enu.task.future()
		f:set("primero")
		local ok, err = pcall(function() f:set("segundo") end)
		out.ok = ok
		out.code = err.code
		enu.task.spawn(function() out.v = f:await() end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "EINVAL")
	h.expectEval(`return out.v`, "primero") // el valor original sobrevive al set fallido
}

// --- 🔒 varios await ven el valor resuelto ---

// TestFutureMultipleAwaitersResolvedFirst: 🔒 varios `await` sobre un future ya
// resuelto ven todos el mismo valor (rama "ya resuelto", sin suspensión).
func TestFutureMultipleAwaitersResolvedFirst(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		local f = enu.task.future()
		f:set("compartido")
		enu.task.spawn(function() out.a = f:await() end)
		enu.task.spawn(function() out.b = f:await() end)
		enu.task.spawn(function() out.c = f:await() end)
	`)
	h.expectEval(`return out.a .. "," .. out.b .. "," .. out.c`, "compartido,compartido,compartido")
}

// TestFutureMultipleAwaitersWokenByOneSet: 🔒 el caso central del inventario —
// **varios awaiters concurrentes bloqueados** despiertan de un **único** `set`.
// Tres tasks esperan el future (las tres suspenden, sueltan el token); una cuarta
// hace `set` una sola vez tras ceder el turno. Las tres deben despertar con el
// mismo valor (cierre de canal como broadcast).
func TestFutureMultipleAwaitersWokenByOneSet(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = {}
		local f = enu.task.future()
		enu.task.spawn(function() out.a = f:await() end)
		enu.task.spawn(function() out.b = f:await() end)
		enu.task.spawn(function() out.c = f:await() end)
		enu.task.spawn(function()
			suspend_echo("x")     -- deja que los tres awaiters lleguen a esperar
			f:set("uno-para-todos")
		end)
	`)
	h.expectEval(`return out.a .. "," .. out.b .. "," .. out.c`,
		"uno-para-todos,uno-para-todos,uno-para-todos")
}

// TestFutureAwaitAfterResolvedStillWorks: 🔒 un `await` que llega **después** de
// que otros awaiters ya consumieron el valor también lo ve (el future guarda el
// valor; no se "agota" al primer await). Garantiza que la rama "ya resuelto" sigue
// sirviendo el valor indefinidamente tras el `set`.
func TestFutureAwaitAfterResolvedStillWorks(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = {}
		fut = enu.task.future()                  -- global: persiste entre evals
		enu.task.spawn(function()
			out.first = fut:await()             -- suspende hasta el set
		end)
		enu.task.spawn(function()
			suspend_echo("x")
			fut:set("valor")
		end)
	`)
	h.expectEval(`return out.first`, "valor")
	// Un await posterior, con el future ya resuelto hace rato, sigue dando el valor.
	h.eval(`enu.task.spawn(function() out.later = fut:await() end)`)
	h.expectEval(`return out.later`, "valor")
}

// --- 🔒 await fuera de task ---

// TestFutureAwaitOutsideTask: 🔒 `Future:await` es ⏸; llamarla desde el chunk
// principal (no una task) lanza `EINVAL` (§1.3), igual que `Task:await` o
// `enu.task.sleep`.
func TestFutureAwaitOutsideTask(t *testing.T) {
	h := newHarness(t)

	se := h.evalErr(`
		local f = enu.task.future()
		f:set(1)
		return f:await()        -- fuera de task: prohibido aunque ya esté resuelto
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("await fuera de task: code got %q, want %q", se.Code, CodeEINVAL)
	}
}

// --- Variantes de valor ---

// TestFutureSetNilSignal: `set()` sin argumento resuelve con `nil` —un future
// puede usarse como mera señal "ya ocurrió", no solo como portador de valor. El
// awaiter recibe nil y `set` queda consumido (un segundo set seguiría dando
// EINVAL: resolver con nil es resolver).
func TestFutureSetNilSignal(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		local f = enu.task.future()
		f:set()                                  -- señal sin valor
		enu.task.spawn(function()
			local v = f:await()
			out.isnil = (v == nil)
		end)
	`)
	h.expectEval(`return tostring(out.isnil)`, "true")
}

// TestFutureCarriesTable: el valor de un future puede ser cualquier LValue,
// incluida una tabla (se entrega por referencia, como cualquier valor Lua bajo el
// token). No hay copia ni serialización: el future no cruza estados (eso es
// `enu.worker`, §13).
func TestFutureCarriesTable(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		local f = enu.task.future()
		f:set({ n = 7 })
		enu.task.spawn(function() out.v = f:await().n end)
	`)
	h.expectEval(`return out.v`, "7")
}

// TestFutureBadHandle: pasar algo que no es un Future a `set` lanza EINVAL con
// mensaje accionable (no un pánico de Go ni un type assert silencioso). Se invoca
// el método `set` del Future sobre un userdata `Task` para forzar el handle
// equivocado.
func TestFutureBadHandle(t *testing.T) {
	h := newHarness(t)

	se := h.evalErr(`
		local f = enu.task.future()
		local t = enu.task.spawn(function() return 1 end)  -- userdata Task, no Future
		return getmetatable(f).__index.set(t)
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("set con handle equivocado: code got %q, want %q", se.Code, CodeEINVAL)
	}
}

// TestFutureNoRaceManyAwaiters: estrés de concurrencia —muchos awaiters
// bloqueados liberados por un único `set`— para que `-race` tenga superficie que
// inspeccionar (el cierre del canal es el único cruce entre goroutines; `value`
// se escribe bajo token antes del cierre). Comprueba además que todos ven el
// mismo valor.
func TestFutureNoRaceManyAwaiters(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = { vals = {} }
		local f = enu.task.future()
		for i = 1, 50 do
			enu.task.spawn(function() out.vals[i] = f:await() end)
		end
		enu.task.spawn(function()
			suspend_echo("go")        -- deja que los 50 lleguen a esperar
			f:set("v")
		end)
	`)
	got := h.eval(`
		local ok = true
		for i = 1, 50 do if out.vals[i] ~= "v" then ok = false end end
		return tostring(ok)
	`)
	if len(got) != 1 || strings.TrimSpace(got[0]) != "true" {
		t.Fatalf("no todos los awaiters vieron el valor: %q", got)
	}
}
