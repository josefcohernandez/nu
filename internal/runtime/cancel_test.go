package runtime

import (
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

// Tests de cancelación (api.md §1.3, §3, sesión S08). S08 está en el inventario
// de lógica clave 🔒 del plan y es un **hito de veto** (valida ADR-008): el
// desenrollado **no capturable por `pcall`** es la propiedad estrella. Cada caso
// del inventario lleva un test que lo nombra para blindarlo de regresiones:
//
//   - Aborto NO capturable por `pcall` Y por `xpcall` de usuario alrededor de un
//     ⏸: la cancelación atraviesa el `pcall`; la task corre sus cleanups y queda
//     abortada (no entrega resultado).
//   - Los errores NORMALES de §1.4 SIGUEN siendo capturables por `pcall`/`xpcall`
//     (no rompimos `pcall`).
//   - Orden **LIFO** de `nu.task.cleanup` y que corre en los TRES finales: éxito,
//     error y aborto.
//   - `ECANCELED` **solo observable**: `Task:await` de una task cancelada entrega
//     `ECANCELED`, que el awaiter SÍ captura con `pcall` (es observación, no el
//     aborto).
//   - `nu.task.cleanup` fuera de task → `EINVAL`.
//
// Patrón de coordinación determinista: la "víctima" señala con un `future` que ya
// está dentro de su ⏸ (suspendida), el "cancelador" hace `cancel` y luego
// `await` para observar el desenlace. Los resultados se dejan en globals que un
// `expectEval` posterior lee. Periodos de sleep cortos y holgados; sin flaky bajo
// `-race -count=4`.

// --- Snippet que ejercita Task:cancel y nu.task.cleanup (Definition of Done §2) ---

// TestCancelCleanupSnippet: el camino que ejercita `Task:cancel` y
// `nu.task.cleanup` desde el lado del autor de extensiones. Una task registra un
// cleanup y se cuelga en un `sleep` largo; otra la cancela; el cleanup corre y la
// observación del desenlace es `ECANCELED`. Prueba de humo de S08.
func TestCancelCleanupSnippet(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ready = nu.task.future()
			local victim = nu.task.spawn(function()
				nu.task.cleanup(function() out.cleaned = true end)
				ready:set(true)             -- ya estoy a punto de suspenderme
				nu.task.sleep(10000)        -- cuelga hasta que me cancelen
				out.despues = true          -- NO debe ejecutarse
			end)
			ready:await()                 -- espera a que la víctima esté lista
			nu.task.sleep(1)              -- cédele el turno para que entre en el sleep
			victim:cancel()
			local ok, err = pcall(function() victim:await() end)
			out.await_ok = ok            -- false: await observa ECANCELED (capturable)
			out.await_code = err and err.code
		end)
	`)
	h.expectEval(`return tostring(out.cleaned)`, "true")
	h.expectEval(`return tostring(out.despues)`, "nil")
	h.expectEval(`return tostring(out.await_ok)`, "false")
	h.expectEval(`return out.await_code`, "ECANCELED")
}

// --- 🔒 Aborto NO capturable por pcall que envuelve un ⏸ ---

// TestCancelNotCapturableByPcall: 🔒 la propiedad estrella (§1.3). La víctima
// envuelve su ⏸ en un `pcall`; al cancelarla, el aborto desenrolla ATRAVESANDO
// ese `pcall` —el `pcall` NO lo atrapa—, así que el código tras el `pcall` no
// corre, pero el `cleanup` SÍ. Si el `pcall` capturara el aborto (el bug que S08
// debe blindar), `out.tras_pcall` se pondría a true y este test lo cazaría.
func TestCancelNotCapturableByPcall(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ready = nu.task.future()
			local victim = nu.task.spawn(function()
				nu.task.cleanup(function() out.cleaned = true end)
				local ok, err = pcall(function()
					ready:set(true)
					nu.task.sleep(10000)   -- aquí llega el aborto
				end)
				-- Si llegamos aquí, el pcall ATRAPÓ el aborto: prohibido (§1.3).
				out.tras_pcall = true
				out.pcall_ok = ok
			end)
			ready:await()
			nu.task.sleep(1)
			victim:cancel()
			pcall(function() victim:await() end)  -- drena el ECANCELED observado
		end)
	`)
	h.expectEval(`return tostring(out.tras_pcall)`, "nil") // el pcall NO atrapó
	h.expectEval(`return tostring(out.pcall_ok)`, "nil")
	h.expectEval(`return tostring(out.cleaned)`, "true") // pero el cleanup SÍ corrió
}

// TestCancelNotCapturableByNestedPcall: 🔒 variante con `pcall` ANIDADO. El
// aborto debe atravesar TODAS las fronteras `pcall` apiladas, no solo la más
// interna. Tres niveles de `pcall`; ninguno atrapa; el cleanup corre.
func TestCancelNotCapturableByNestedPcall(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ready = nu.task.future()
			local victim = nu.task.spawn(function()
				nu.task.cleanup(function() out.cleaned = true end)
				pcall(function()
					pcall(function()
						pcall(function()
							ready:set(true)
							nu.task.sleep(10000)
						end)
						out.nivel2 = true  -- prohibido
					end)
					out.nivel1 = true      -- prohibido
				end)
				out.nivel0 = true          -- prohibido
			end)
			ready:await()
			nu.task.sleep(1)
			victim:cancel()
			pcall(function() victim:await() end)
		end)
	`)
	h.expectEval(`return tostring(out.nivel2)`, "nil")
	h.expectEval(`return tostring(out.nivel1)`, "nil")
	h.expectEval(`return tostring(out.nivel0)`, "nil")
	h.expectEval(`return tostring(out.cleaned)`, "true")
}

// TestCancelNotCapturableByXpcall: 🔒 lo mismo con `xpcall`. El aborto NO debe
// pasar por el manejador de errores (`errfn`) del usuario ni ser atrapado por el
// `xpcall`. Si `errfn` corriera sobre el aborto, `out.handler_corrio` se pondría
// a true.
func TestCancelNotCapturableByXpcall(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ready = nu.task.future()
			local victim = nu.task.spawn(function()
				nu.task.cleanup(function() out.cleaned = true end)
				xpcall(function()
					ready:set(true)
					nu.task.sleep(10000)
				end, function(e)
					out.handler_corrio = true  -- prohibido: el aborto no pasa por errfn
					return e
				end)
				out.tras_xpcall = true         -- prohibido
			end)
			ready:await()
			nu.task.sleep(1)
			victim:cancel()
			pcall(function() victim:await() end)
		end)
	`)
	h.expectEval(`return tostring(out.handler_corrio)`, "nil")
	h.expectEval(`return tostring(out.tras_xpcall)`, "nil")
	h.expectEval(`return tostring(out.cleaned)`, "true")
}

// --- 🔒 Los errores normales de §1.4 SIGUEN capturándose ---

// TestNormalErrorsStillCapturableByPcall: 🔒 regresión — no rompimos `pcall`. Un
// error estructurado normal (§1.4) lanzado dentro de un `pcall` SÍ se captura, y
// su `code` llega intacto. Y un `error("texto")` también. Esto demuestra que la
// inmunidad es exclusiva del aborto, no un `pcall` mutilado.
func TestNormalErrorsStillCapturableByPcall(t *testing.T) {
	h := newHarness(t)
	h.register("boom_einval", func(L *lua.LState) int {
		raiseError(L, CodeEINVAL, "kaboom", lua.LNil)
		return 0
	})

	h.eval(`
		out = {}
		nu.task.spawn(function()
			-- error estructurado del core: capturable, code intacto.
			local ok, err = pcall(function() boom_einval() end)
			out.struct_ok = ok
			out.struct_code = err and err.code
			-- error de texto: capturable como string.
			local ok2, err2 = pcall(function() error("texto plano") end)
			out.str_ok = ok2
			out.str_is_string = type(err2) == "string"
			-- xpcall también captura un error normal y corre su handler.
			local ok3, mapped = xpcall(function() error("via xpcall") end, function(e)
				return "manejado"
			end)
			out.xp_ok = ok3
			out.xp_mapped = mapped
		end)
	`)
	h.expectEval(`return tostring(out.struct_ok)`, "false")
	h.expectEval(`return out.struct_code`, "EINVAL")
	h.expectEval(`return tostring(out.str_ok)`, "false")
	h.expectEval(`return tostring(out.str_is_string)`, "true")
	h.expectEval(`return tostring(out.xp_ok)`, "false")
	h.expectEval(`return out.xp_mapped`, "manejado")
}

// --- 🔒 Orden LIFO de cleanup, en los TRES finales ---

// TestCleanupLIFOOrder: 🔒 los cleanups corren en orden INVERSO al de registro
// (semántica `defer`). Se registran tres; deben correr 3,2,1.
func TestCleanupLIFOOrder(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		order = {}
		nu.task.spawn(function()
			nu.task.cleanup(function() order[#order+1] = "1" end)
			nu.task.cleanup(function() order[#order+1] = "2" end)
			nu.task.cleanup(function() order[#order+1] = "3" end)
		end)
	`)
	h.expectEval(`return table.concat(order, ",")`, "3,2,1")
}

// TestCleanupRunsOnSuccess: 🔒 los cleanups corren cuando la task termina BIEN.
func TestCleanupRunsOnSuccess(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			nu.task.cleanup(function() out.ran = true end)
			return 42
		end)
	`)
	h.expectEval(`return tostring(out.ran)`, "true")
}

// TestCleanupRunsOnError: 🔒 los cleanups corren cuando la task termina por ERROR
// (no capturado dentro de la task).
func TestCleanupRunsOnError(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			nu.task.cleanup(function() out.ran = true end)
			error("la task peta")
		end)
	`)
	h.expectEval(`return tostring(out.ran)`, "true")
}

// TestCleanupRunsOnAbort: 🔒 los cleanups corren cuando la task es ABORTADA por
// cancelación (ya cubierto en los tests de no-capturable, pero aislado aquí para
// que el "TRES finales" quede explícito en su propio test).
func TestCleanupRunsOnAbort(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ready = nu.task.future()
			local victim = nu.task.spawn(function()
				nu.task.cleanup(function() out.ran = true end)
				ready:set(true)
				nu.task.sleep(10000)
			end)
			ready:await()
			nu.task.sleep(1)
			victim:cancel()
			pcall(function() victim:await() end)
		end)
	`)
	h.expectEval(`return tostring(out.ran)`, "true")
}

// --- 🔒 ECANCELED solo observable en await ---

// TestAwaitObservesECANCELED: 🔒 `Task:await` de una task cancelada lanza un
// `ECANCELED` que el awaiter SÍ captura con `pcall`. Es observación, no el aborto
// del propio awaiter: el awaiter sigue vivo tras el `pcall` (corre el código de
// después). Si el aborto se "contagiara" al awaiter, `out.awaiter_sigue` no se
// pondría.
func TestAwaitObservesECANCELED(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ready = nu.task.future()
			local victim = nu.task.spawn(function()
				ready:set(true)
				nu.task.sleep(10000)
			end)
			ready:await()
			nu.task.sleep(1)
			victim:cancel()
			local ok, err = pcall(function() victim:await() end)
			out.ok = ok
			out.code = err and err.code
			out.awaiter_sigue = true   -- el awaiter NO fue abortado: sigue vivo
		end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "ECANCELED")
	h.expectEval(`return tostring(out.awaiter_sigue)`, "true")
}

// --- 🔒 nu.task.cleanup fuera de task → EINVAL ---

// TestCleanupOutsideTaskEINVAL: 🔒 registrar un cleanup en el chunk principal (no
// es una task) lanza `EINVAL`. La cancelación misma no tiene a qué atarse fuera
// de una task.
func TestCleanupOutsideTaskEINVAL(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`nu.task.cleanup(function() end)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("nu.task.cleanup fuera de task: code got %q, want EINVAL", se.Code)
	}
}

// --- Casos de borde de Task:cancel ---

// TestCancelAlreadyDoneIsNoop: cancelar una task que ya terminó es un no-op
// inocuo (no entra en pánico ni cambia nada). Su `await` devuelve el valor
// normal, no ECANCELED: terminó bien antes de la cancelación.
func TestCancelAlreadyDoneIsNoop(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local done = nu.task.spawn(function() return "valor" end)
			done:await()          -- ya terminó
			done:cancel()         -- no-op
			out.v = done:await()  -- sigue dando el valor original
		end)
	`)
	h.expectEval(`return out.v`, "valor")
}

// TestCancelTwiceIsIdempotent: cancelar dos veces no entra en pánico (cancelOnce)
// y el desenlace sigue siendo cancelado.
func TestCancelTwiceIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ready = nu.task.future()
			local victim = nu.task.spawn(function()
				ready:set(true)
				nu.task.sleep(10000)
			end)
			ready:await()
			nu.task.sleep(1)
			victim:cancel()
			victim:cancel()   -- segunda vez: idempotente
			local ok, err = pcall(function() victim:await() end)
			out.code = err and err.code
		end)
	`)
	h.expectEval(`return out.code`, "ECANCELED")
}

// TestCancelBadHandleEINVAL: `Task:cancel` sobre un userdata que NO es una Task
// (aquí, un Future) lanza `EINVAL` estructurado. Se invoca el método sobre un
// handle de otro tipo para llegar a la comprobación de tipo de `taskCancel`.
func TestCancelBadHandleEINVAL(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`
		local mt = getmetatable(nu.task.spawn(function() end))
		mt.__index.cancel(nu.task.future())  -- userdata, pero no una Task
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("Task:cancel con handle inválido: code got %q, want EINVAL", se.Code)
	}
}

// TestCleanupErrorIsolated: un cleanup que lanza no impide que corran los demás ni
// tumba el proceso; el error queda en el log (best-effort, ADR-008). Se registran
// dos cleanups: el segundo (que corre primero, LIFO) lanza; el primero corre igual.
func TestCleanupErrorIsolated(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			nu.task.cleanup(function() out.ok_cleanup = true end)
			nu.task.cleanup(function() error("cleanup roto") end)
		end)
	`)
	h.expectEval(`return tostring(out.ok_cleanup)`, "true")

	var found bool
	for _, line := range h.logLines() {
		if strings.Contains(line, "nu.task.cleanup") {
			found = true
		}
	}
	if !found {
		t.Fatalf("se esperaba una línea de log por el cleanup roto; log:\n%s",
			strings.Join(h.logLines(), "\n"))
	}
}

// TestPcallOutsideTaskStillWorks: en el chunk principal (no es una task), `pcall`
// envuelto se comporta como el nativo —no hay aborto que filtrar fuera de una
// task—. Captura un error normal sin tocar nada del camino de cancelación.
func TestPcallOutsideTaskStillWorks(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local ok, err = pcall(function() error("fuera de task") end)
		return tostring(ok), tostring(type(err) == "string")
	`, "false", "true")
}
