package runtime

import (
	"testing"
	"time"
)

const cp2Budget = 50 * time.Millisecond

// 🔎 CP-2 — "El modelo de concurrencia del navegador, completo" (docs/
// implementacion.md, tras S09). Es el checkpoint de integración que cierra la
// Fase 1 (el scheduler) y el más importante del kernel: valida ADR-004/008 de
// extremo a extremo en **un solo script** Lua, con todo lo construido en S04–S09
// corriendo junto. Sus cuatro patas:
//
//   (a) `task.all` sobre 3 tasks devuelve resultados **alineados** con los inputs
//       (G27), aunque terminen en otro orden.
//   (b) `task.race` devuelve el índice ganador y **cancela a las perdedoras**.
//   (c) una task cancelada corre sus `cleanup` y un `pcall` envolvente **NO**
//       atrapa el aborto (desenrollado no capturable, §1.3).
//   (d) un bucle de CPU puro lo corta el **watchdog** (`EBUDGET`) **sin congelar
//       el loop**: un `every` en paralelo sigue tickeando mientras la víctima es
//       cortada.
//
// Si CP-2 pasa, la Fase 1 se marca cerrada en el tablero del plan. Cualquier
// grieta del puente ⏸ o del desenrollado es barata de cerrar aquí y carísima
// después.

func TestCP2BrowserConcurrencyModel(t *testing.T) {
	// Presupuesto pequeño para que (d) corte rápido; el resto de patas no lo rozan.
	h := newHarnessBudget(t, cp2Budget)

	// Todo corre dentro de una task raíz: `all`/`race`/`await`/`sleep` son ⏸ y solo
	// se pueden llamar desde una task. `EvalString` espera (waitIdle) a que la task
	// raíz termine antes de volver, así que los asserts de abajo leen `out` ya pleno.
	h.eval(`
		out = {}
		enu.task.spawn(function()
			-- (a) alineación (G27)
			out.all = enu.task.all({
				function() enu.task.sleep(30); return "a" end,
				function() enu.task.sleep(20); return "b" end,
				function() enu.task.sleep(10); return "c" end,
			})

			-- (b) race: gana la más rápida (índice 3), las demás se cancelan.
			out.cancelada_perdedora = false
			local idx = enu.task.race({
				function() enu.task.sleep(50); return "lento1" end,
				function()
					enu.task.cleanup(function() out.cancelada_perdedora = true end)
					enu.task.sleep(50); return "lento2"
				end,
				function() enu.task.sleep(1); return "rapido" end,
			})
			out.race_idx = idx

			-- (c) cancelación: cleanup corre y un pcall envolvente NO atrapa el aborto.
			out.c_cleanup = false
			out.c_despues_pcall = false
			local ready = enu.task.future()
			local v = enu.task.spawn(function()
				enu.task.cleanup(function() out.c_cleanup = true end)
				ready:set(true)
				pcall(function() enu.task.sleep(10000) end)  -- el aborto atraviesa este pcall
				out.c_despues_pcall = true                   -- NO debe correr
			end)
			ready:await()
			enu.task.sleep(1)
			v:cancel()
			local ok, err = pcall(function() v:await() end)
			out.c_await_code = err and err.code             -- ECANCELED (observable)

			-- (d) watchdog corta un bucle de CPU puro SIN congelar el loop: un every
			-- en paralelo sigue tickeando.
			out.ticks = 0
			local timer = enu.task.every(10, function() out.ticks = out.ticks + 1 end)
			local cpu = enu.task.spawn(function() while true do end end)
			local okw, errw = pcall(function() cpu:await() end)
			out.d_await_code = errw and errw.code           -- EBUDGET
			out.ticks_al_cortar = out.ticks
			enu.task.sleep(50)                               -- deja tickear tras el corte
			out.ticks_despues = out.ticks
			timer:stop()

			out.fin = true
		end)
	`)

	// (a) G27: alineado con los inputs pese al orden inverso de terminación.
	h.expectEval(`return out.all[1]`, "a")
	h.expectEval(`return out.all[2]`, "b")
	h.expectEval(`return out.all[3]`, "c")
	// (b) race: índice 1-based del ganador (el rápido es el 3) y perdedora cancelada.
	h.expectEval(`return tostring(out.race_idx)`, "3")
	h.expectEval(`return tostring(out.cancelada_perdedora)`, "true")
	// (c) cancelación: cleanup corrió, el pcall NO atrapó el aborto, await observa ECANCELED.
	h.expectEval(`return tostring(out.c_cleanup)`, "true")
	// false (su valor inicial), nunca true: el aborto atravesó el pcall y el código
	// posterior no corrió.
	h.expectEval(`return tostring(out.c_despues_pcall)`, "false")
	h.expectEval(`return out.c_await_code`, "ECANCELED")
	// (d) watchdog: EBUDGET, y el every siguió tickeando (loop no congelado).
	h.expectEval(`return out.d_await_code`, "EBUDGET")
	h.expectEval(`return tostring(out.fin)`, "true")
	got := h.eval(`return (out.ticks_despues > out.ticks_al_cortar) and "creció" or "no creció"`)
	if got[0] != "creció" {
		t.Fatalf("CP-2 (d): el every no progresó tras cortar el bucle de CPU (loop congelado): %v", got)
	}
}
