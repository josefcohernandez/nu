package runtime

import (
	"strings"
	"testing"
	"time"
)

// Tests del watchdog de slice (api.md §1.3, sesión S09). S09 está en el
// inventario de lógica clave 🔒 del plan y es un **hito de veto** (junto con S08
// valida el modelo de robustez de ADR-008): cortar un slice de **CPU puro** que
// no coopera, de forma **no capturable**, sin congelar el resto del loop. Cada
// caso del inventario lleva un test que lo nombra para blindarlo de regresiones:
//
//   - Un bucle Lua de CPU puro que excede el presupuesto **es abortado**.
//   - El aborto por watchdog **NO es capturable** por un `pcall` de usuario.
//   - Corren los `cleanup` de la task abortada por watchdog.
//   - `Task:await` de una abortada por watchdog observa **EBUDGET**.
//   - **Sin congelar el loop**: mientras una task quema CPU y es cortada, otra
//     task / un `every` en paralelo PROGRESA.
//   - Una task que **no** excede el presupuesto (trabajo normal con suspensiones)
//     NUNCA es abortada (sin falsos positivos; el slice se reinicia en cada ⏸).
//
// El watchdog se corta con `LState.SetContext` (gopher-lua v1.1.2): cada thread
// de task lleva un contexto que el intérprete vigila en cada instrucción
// (`mainLoopWithContext`); el watchdog lo cancela al exceder el presupuesto, lo
// que rompe incluso un `while true do end`. La integración con el desenrollado no
// capturable de S08 (flag `aborting` + wrappers de `pcall`/`xpcall`) hace que el
// corte atraviese cualquier `pcall` del usuario. Ver watchdog.go.
//
// Presupuestos pequeños (50 ms) para corte rápido; coordinación determinista vía
// `future` cuando hace falta; sin flaky bajo `-race -count=4`.

const wdBudget = 50 * time.Millisecond

// --- Snippet / 🔒 Un bucle de CPU puro que excede el presupuesto es abortado ---

// TestWatchdogCutsPureCPULoop: 🔒 el caso base. Una task corre `while true do end`
// (CPU pura, jamás suspende); el watchdog la corta al exceder el presupuesto. El
// awaiter observa `EBUDGET` y el código tras el bucle NO corre.
func TestWatchdogCutsPureCPULoop(t *testing.T) {
	h := newHarnessBudget(t, wdBudget)

	h.eval(`
		out = {}
		enu.task.spawn(function()
			local victim = enu.task.spawn(function()
				while true do end          -- CPU pura: nunca suspende
				out.despues = true         -- NO debe ejecutarse
			end)
			local ok, err = pcall(function() victim:await() end)
			out.await_ok = ok            -- false: await observa EBUDGET
			out.await_code = err and err.code
		end)
	`)
	h.expectEval(`return tostring(out.despues)`, "nil")
	h.expectEval(`return tostring(out.await_ok)`, "false")
	h.expectEval(`return out.await_code`, "EBUDGET")
}

// --- 🔒 El aborto por watchdog NO es capturable por un pcall de usuario ---

// TestWatchdogNotCapturableByPcall: 🔒 la propiedad estrella (§1.3). La víctima
// envuelve su bucle de CPU puro en un `pcall`; el corte del watchdog desenrolla
// ATRAVESANDO ese `pcall` —no lo atrapa—, así que ni el cuerpo del `pcall` retorna
// ni el código de después corre; el awaiter ve `EBUDGET`.
func TestWatchdogNotCapturableByPcall(t *testing.T) {
	h := newHarnessBudget(t, wdBudget)

	h.eval(`
		out = {}
		enu.task.spawn(function()
			local victim = enu.task.spawn(function()
				local ok, err = pcall(function() while true do end end)
				out.pcall_volvio = true    -- NO debe ejecutarse: el aborto no es capturable
				out.pcall_ok = ok
			end)
			local ok, err = pcall(function() victim:await() end)
			out.await_code = err and err.code
		end)
	`)
	h.expectEval(`return tostring(out.pcall_volvio)`, "nil")
	h.expectEval(`return out.await_code`, "EBUDGET")
}

// TestWatchdogNotCapturableByNestedPcallAndXpcall: 🔒 ni siquiera varios `pcall`
// anidados ni un `xpcall` con su manejador atrapan el corte del watchdog; el
// manejador de `xpcall` tampoco corre sobre el aborto (§1.3).
func TestWatchdogNotCapturableByNestedPcallAndXpcall(t *testing.T) {
	h := newHarnessBudget(t, wdBudget)

	h.eval(`
		out = {}
		enu.task.spawn(function()
			local victim = enu.task.spawn(function()
				xpcall(function()
					pcall(function()
						pcall(function() while true do end end)
						out.nivel1 = true   -- NO
					end)
					out.nivel0 = true       -- NO
				end, function(e)
					out.errfn_corrio = true -- NO: el aborto no pasa por el manejador
					return e
				end)
				out.despues = true          -- NO
			end)
			local ok, err = pcall(function() victim:await() end)
			out.await_code = err and err.code
		end)
	`)
	h.expectEval(`return tostring(out.nivel1)`, "nil")
	h.expectEval(`return tostring(out.nivel0)`, "nil")
	h.expectEval(`return tostring(out.errfn_corrio)`, "nil")
	h.expectEval(`return tostring(out.despues)`, "nil")
	h.expectEval(`return out.await_code`, "EBUDGET")
}

// --- 🔒 Corren los cleanup de la task abortada por watchdog ---

// TestWatchdogRunsCleanups: 🔒 una task abortada por el watchdog corre sus
// `enu.task.cleanup` (en LIFO), igual que una cancelada (S08): el aborto por
// presupuesto reusa `runCleanups`.
func TestWatchdogRunsCleanups(t *testing.T) {
	h := newHarnessBudget(t, wdBudget)

	h.eval(`
		out = { orden = {} }
		enu.task.spawn(function()
			local victim = enu.task.spawn(function()
				enu.task.cleanup(function() table.insert(out.orden, "primero") end)
				enu.task.cleanup(function() table.insert(out.orden, "segundo") end)
				while true do end          -- el watchdog corta aquí
			end)
			pcall(function() victim:await() end)
		end)
	`)
	// LIFO: el último registrado corre primero.
	h.expectEval(`return out.orden[1]`, "segundo")
	h.expectEval(`return out.orden[2]`, "primero")
}

// --- 🔒 Task:await de una abortada por watchdog observa EBUDGET (y es capturable) ---

// TestWatchdogAwaitObservesEBUDGET: 🔒 `await` de una task que el watchdog mató
// entrega `EBUDGET`, y el awaiter SÍ lo captura con `pcall` (es *observación*, no
// el aborto del propio awaiter): el código tras el `pcall` del awaiter sigue
// corriendo. Distingue EBUDGET de ECANCELED por el `reason` del aborto.
func TestWatchdogAwaitObservesEBUDGET(t *testing.T) {
	h := newHarnessBudget(t, wdBudget)

	h.eval(`
		out = {}
		enu.task.spawn(function()
			local victim = enu.task.spawn(function() while true do end end)
			local ok, err = pcall(function() victim:await() end)
			out.ok = ok                  -- false
			out.code = err and err.code  -- EBUDGET
			out.sigo_vivo = true         -- el awaiter sobrevive a observar el aborto
		end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "EBUDGET")
	h.expectEval(`return tostring(out.sigo_vivo)`, "true")
}

// --- 🔒 Sin congelar el loop: un every en paralelo sigue tickeando ---

// TestWatchdogDoesNotFreezeLoop: 🔒 (parte de CP-2) mientras una task quema CPU y
// el watchdog la corta, OTRO trabajo del loop progresa. Aquí un `every` en
// paralelo: si el watchdog congelara el loop, el `every` no tickearía. Como el
// watchdog corre en su propia goroutine y, tras cortar, la víctima suelta el
// token, el `every` consigue el token y tickea. Comprobamos que tickeó al menos
// una vez DESPUÉS de que la víctima fue abortada.
func TestWatchdogDoesNotFreezeLoop(t *testing.T) {
	h := newHarnessBudget(t, wdBudget)

	h.eval(`
		out = { ticks = 0, abortada = false }
		enu.task.spawn(function()
			local timer = enu.task.every(10, function() out.ticks = out.ticks + 1 end)
			local victim = enu.task.spawn(function() while true do end end)
			pcall(function() victim:await() end)
			out.abortada = true
			out.ticks_al_abortar = out.ticks
			-- Deja correr al every un poco más para confirmar que sigue vivo tras el corte.
			enu.task.sleep(60)
			out.ticks_despues = out.ticks
			timer:stop()
		end)
	`)
	h.expectEval(`return tostring(out.abortada)`, "true")
	// El every siguió tickeando tras el corte del watchdog (el loop no se congeló).
	got := h.eval(`return (out.ticks_despues > out.ticks_al_abortar) and "creció" or "no creció"`)
	if got[0] != "creció" {
		t.Fatalf("el every NO progresó tras cortar al monopolizador (loop congelado): %v", got)
	}
}

// --- 🔒 Sin falsos positivos: trabajo normal con suspensiones nunca se aborta ---

// TestWatchdogNoFalsePositiveOnSuspendingWork: 🔒 una task que hace muchos tramos
// cortos separados por ⏸ (`sleep`) NO se aborta aunque su tiempo TOTAL exceda el
// presupuesto: el slice se reinicia en cada suspensión, y ningún tramo continuo lo
// excede. Budget pequeño (20 ms), 10 vueltas con sleeps de 5 ms (total ~50 ms,
// por encima del budget, pero ningún slice individual lo roza).
func TestWatchdogNoFalsePositiveOnSuspendingWork(t *testing.T) {
	h := newHarnessBudget(t, 20*time.Millisecond)

	h.eval(`
		out = {}
		enu.task.spawn(function()
			local victim = enu.task.spawn(function()
				for i = 1, 10 do
					-- tramo de CPU trivial, muy por debajo del budget
					local x = 0
					for j = 1, 1000 do x = x + j end
					enu.task.sleep(5)   -- ⏸: reinicia el slice
				end
				return "ok"
			end)
			out.res = victim:await()   -- debe completar normalmente
		end)
	`)
	h.expectEval(`return out.res`, "ok")
}

// TestWatchdogNoFalsePositiveOnFastTask: 🔒 una task que termina por debajo del
// presupuesto en un solo slice nunca dispara el watchdog (el timer se desarma al
// terminar).
func TestWatchdogNoFalsePositiveOnFastTask(t *testing.T) {
	h := newHarnessBudget(t, wdBudget)

	h.eval(`
		out = {}
		enu.task.spawn(function()
			local victim = enu.task.spawn(function() return 42 end)
			out.res = victim:await()
		end)
	`)
	h.expectEval(`return tostring(out.res)`, "42")
	// Nada de misbehaved en el log: no hubo exceso de presupuesto.
	for _, l := range h.logLines() {
		if strings.Contains(l, "misbehaved") {
			t.Fatalf("se emitió misbehaved sin exceso de presupuesto: %q", l)
		}
	}
}

// --- core:plugin.misbehaved se emite por el gancho interno (verificable tras S10) ---

// TestWatchdogEmitsMisbehaved: el gancho interno `emitMisbehaved` deja constancia
// del corte por watchdog (hasta S10, en el log; S10 lo cableará a
// `enu.events.emit("core:plugin.misbehaved", ...)`). Comprobamos la línea.
func TestWatchdogEmitsMisbehaved(t *testing.T) {
	h := newHarnessBudget(t, wdBudget)

	h.eval(`
		enu.task.spawn(function()
			local victim = enu.task.spawn(function() while true do end end)
			pcall(function() victim:await() end)
		end)
	`)
	found := false
	for _, l := range h.logLines() {
		if strings.Contains(l, "core:plugin.misbehaved") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no se emitió core:plugin.misbehaved tras el corte del watchdog; log=%v", h.logLines())
	}
}
