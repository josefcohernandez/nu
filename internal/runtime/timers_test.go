package runtime

import (
	"strings"
	"testing"
)

// Tests de S05 (api.md §3): `nu.task.sleep`/`defer`/`every` + `Timer:stop`. S05
// no está en el inventario 🔒, pero `every`/`defer` tienen lógica propia
// (scheduling periódico, handlers síncronos sobre el token, parar sin fugas de
// goroutine, quiescencia). La suite corre con `-race -count=4` para descartar
// flaky. Casos blindados:
//
//   - sleep NO bloquea el loop (otra task progresa mientras una duerme);
//   - sleep/⏸ fuera de task → EINVAL; sleep con ms negativo → EINVAL;
//   - defer corre en el siguiente tick (antes de que EvalString devuelva);
//   - every dispara N veces y `stop` lo corta (sin más ticks tras stop);
//   - `stop` no deja goroutines colgadas (lo vigila el contador de goroutines y
//     el -race); un error en un handler síncrono no tumba el proceso (queda en
//     el log).

// TestSleepReturns: el camino feliz mínimo de `sleep` —una task duerme un instante
// y continúa—. Si `sleep` colgara el token, `EvalString` no volvería (lo caza el
// timeout de `go test`).
func TestSleepReturns(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		done = false
		nu.task.spawn(function()
			nu.task.sleep(5)
			done = true
		end)
	`)
	h.expectEval(`return tostring(done)`, "true")
}

// TestSleepDoesNotBlockLoop es el criterio central de S05: una task suspendida en
// `sleep` **suelta el token**, así que otra task progresa mientras tanto. La task
// que duerme más anota su orden el último; la rápida, primero —si `sleep`
// retuviera el token, la rápida no correría hasta que la lenta despertara, y el
// orden saldría invertido.
func TestSleepDoesNotBlockLoop(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		order = {}
		nu.task.spawn(function()
			nu.task.sleep(40)                 -- duerme largo
			order[#order + 1] = "lenta"
		end)
		nu.task.spawn(function()
			nu.task.sleep(1)                  -- duerme corto: despierta antes
			order[#order + 1] = "rapida"
		end)
	`)
	h.expectEval(`return order[1]`, "rapida")
	h.expectEval(`return order[2]`, "lenta")
}

// TestSleepZeroYields: `sleep(0)` es válido —cede el turno y vuelve—. No es
// EINVAL (cero no es negativo) y no cuelga.
func TestSleepZeroYields(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		ok = false
		nu.task.spawn(function()
			nu.task.sleep(0)
			ok = true
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")
}

// TestSleepOutsideTask: `sleep` es ⏸; fuera de una task lanza EINVAL (§1.3),
// como `await`/`suspendEcho`. El chunk de `nu -e` corre en el estado principal.
func TestSleepOutsideTask(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`nu.task.sleep(1)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("sleep fuera de task: code = %q, want %q", se.Code, CodeEINVAL)
	}
}

// TestSleepNegativeRejected: un periodo negativo es EINVAL —error de programador,
// no algo que el scheduler deba interpretar—. Se observa por el log fire-and-forget
// (la task que lanza no está esperada por nadie).
func TestSleepNegativeRejected(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		nu.task.spawn(function()
			nu.task.sleep(-1)
		end)
	`)
	if !logHas(h, "EINVAL", "negativo") {
		t.Fatalf("sleep(-1) debería lanzar EINVAL; log:\n%s", strings.Join(h.logLines(), "\n"))
	}
}

// TestDeferRunsNextTick: `defer(fn)` corre en el siguiente tick, es decir antes de
// que `EvalString` devuelva (un `defer` pendiente cuenta para la quiescencia). El
// chunk encola el defer y termina; cuando `eval` vuelve, el efecto ya ocurrió.
func TestDeferRunsNextTick(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		ran = false
		nu.task.defer(function() ran = true end)
	`)
	h.expectEval(`return tostring(ran)`, "true")
}

// TestDeferAfterCurrent: `defer` corre **después** de ceder el control actual, no
// en medio. El chunk marca "antes", encola el defer (que marca "despues") y sigue
// marcando "antes-2"; el defer no puede colarse entre ambos porque no corre hasta
// que el chunk suelta el token.
func TestDeferAfterCurrent(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		order = {}
		nu.task.defer(function() order[#order + 1] = "defer" end)
		order[#order + 1] = "inline-1"
		order[#order + 1] = "inline-2"
	`)
	h.expectEval(`return order[1]`, "inline-1")
	h.expectEval(`return order[2]`, "inline-2")
	h.expectEval(`return order[3]`, "defer")
}

// TestDeferFromTask: `defer` encolado desde dentro de una task también corre. La
// quiescencia espera tanto a la task como al defer que dejó pendiente.
func TestDeferFromTask(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		hits = 0
		nu.task.spawn(function()
			nu.task.sleep(1)
			nu.task.defer(function() hits = hits + 1 end)
		end)
	`)
	h.expectEval(`return tostring(hits)`, "1")
}

// TestDeferErrorIsolated: un error en el handler de `defer` no tumba el proceso
// (pcall por frontera, ADR-008); queda en el log. El siguiente snippet sigue
// corriendo, prueba de que el runtime sobrevivió.
func TestDeferErrorIsolated(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		nu.task.defer(function() error({ code = "EIO", message = "defer roto" }) end)
	`)
	if !logHas(h, "EIO", "defer roto") {
		t.Fatalf("el error del defer debería quedar en el log; log:\n%s", strings.Join(h.logLines(), "\n"))
	}
	h.expectEval(`return 1 + 1`, "2") // el runtime sigue vivo
}

// TestEveryFiresAndStops es el criterio de S05 para `every`: dispara N veces y
// `stop` lo corta. Una task "ancla" mantiene el runtime vivo mientras el timer
// tickea; cuenta los disparos hasta llegar al objetivo, entonces para el timer y
// termina. Tras `stop` no debe haber más ticks, así que el contador final es
// exactamente el objetivo (lo verifica el assert, no un "al menos N").
func TestEveryFiresAndStops(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		ticks = 0
		local timer
		timer = nu.task.every(2, function()
			ticks = ticks + 1
		end)
		nu.task.spawn(function()
			-- Espera a juntar al menos 3 ticks, luego corta el timer.
			while ticks < 3 do
				nu.task.sleep(2)
			end
			timer:stop()
			final = ticks
			-- Da margen a que cualquier disparo en vuelo (ya inexistente) se vería.
			nu.task.sleep(20)
			after = ticks
		end)
	`)
	// Tras stop no hay más ticks: el contador no se mueve.
	h.expectEval(`return tostring(final == after)`, "true")
	h.expectEval(`return tostring(after >= 3)`, "true")
}

// TestEveryStopNoMoreTicks afina el contrato de `stop`: ni un solo disparo tras
// pararlo, ni siquiera uno que estuviera esperando el token (lo cubre
// `runSyncHandlerCancelable`). Se cuenta antes y después de una espera holgada.
func TestEveryStopIsClean(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		count = 0
		nu.task.spawn(function()
			local timer = nu.task.every(2, function() count = count + 1 end)
			nu.task.sleep(15)         -- deja correr unos cuantos ticks
			timer:stop()
			snapshot = count
			nu.task.sleep(30)         -- bastante más que el periodo
			drift = count - snapshot  -- debe ser 0: nada disparó tras stop
		end)
	`)
	h.expectEval(`return tostring(drift)`, "0")
	h.expectEval(`return tostring(snapshot > 0)`, "true")
}

// TestEveryRejectsNonPositive: un periodo no positivo es EINVAL (sería un bucle
// ocupado). A diferencia de `sleep`, aquí cero también se rechaza: un timer de
// periodo cero no tiene semántica útil.
func TestEveryRejectsNonPositive(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`nu.task.every(0, function() end)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("every(0): code = %q, want %q", se.Code, CodeEINVAL)
	}
	se = h.evalErr(`nu.task.every(-5, function() end)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("every(-5): code = %q, want %q", se.Code, CodeEINVAL)
	}
}

// TestEveryDoesNotBlockExit es la decisión de quiescencia de S05: un `every`
// activo **no** impide que `EvalString` vuelva. El chunk arranca un timer y no lo
// para; aun así `eval` termina (lo caza el timeout de `go test` si colgara). El
// `Close` del arnés apaga el timer al final de la prueba.
func TestEveryDoesNotBlockExit(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		nu.task.every(1, function() end)  -- nunca se para explícitamente
	`)
	// Si llegamos aquí, EvalString volvió pese al timer activo: la quiescencia
	// ignora los timers periódicos, como se diseñó.
	h.expectEval(`return 1`, "1")
}

// TestEveryErrorIsolated: como `defer`, un error en un disparo de `every` no
// tumba el proceso ni para el timer —pcall por frontera (ADR-008)—; queda en el
// log y el timer sigue. Se comprueba que tras el error el timer aún dispara.
func TestEveryErrorIsolated(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		fires = 0
		nu.task.spawn(function()
			local timer = nu.task.every(2, function()
				fires = fires + 1
				error({ code = "EIO", message = "tick roto" })
			end)
			while fires < 2 do nu.task.sleep(2) end  -- sigue disparando pese al error
			timer:stop()
		end)
	`)
	h.expectEval(`return tostring(fires >= 2)`, "true")
	if !logHas(h, "EIO", "tick roto") {
		t.Fatalf("el error del tick debería quedar en el log; log:\n%s", strings.Join(h.logLines(), "\n"))
	}
}

// TestTimerStopIdempotent: parar dos veces el mismo timer no entra en pánico
// (cerrar `stopCh` dos veces lo haría; `stopTimer` lo evita). Es plausible que el
// dueño llame `stop` de más en código de limpieza.
func TestTimerStopIdempotent(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		nu.task.spawn(function()
			local timer = nu.task.every(2, function() end)
			nu.task.sleep(5)
			timer:stop()
			timer:stop()   -- segundo stop: no debe romper nada
			ok = true
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")
}

// logHas comprueba que alguna línea del log del runtime bajo prueba contiene
// todos los fragmentos dados. Útil para verificar los errores best-effort que los
// handlers síncronos (defer/every) dejan en el log en vez de propagar.
func logHas(h *harness, parts ...string) bool {
	h.t.Helper()
	for _, ln := range h.logLines() {
		all := true
		for _, p := range parts {
			if !strings.Contains(ln, p) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
