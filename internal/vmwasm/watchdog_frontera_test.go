package vmwasm

// Tests del watchdog en las DOS fronteras que corren en el hilo del scheduler
// (A-36): los cleanups de una task (__finish) y los handlers del bus de eventos
// (__ev_dispatch cuando el emit sale del hilo principal: EmitEvent / el
// core:plugin.misbehaved de __resume). El count-hook (DM4) se arma por corrutina y
// sólo lo llevan las corrutinas de task; en esas dos fronteras se ejecuta código de
// usuario en el hilo principal (GL), que NO lo lleva. Sin el arreglo, un
// `while true do end` ahí congelaría painter, FeedInput, EmitEvent y el bucle sin
// EBUDGET posible —violando la robustez por watchdog + pcall de ADR-008—.
//
// Estos tests blindan (política 🔒) que ahora esas fronteras tienen la MISMA
// semántica que una task: un bucle de CPU se aborta por EBUDGET, el resto de la
// frontera sigue (los demás cleanups/handlers corren) y el runtime sobrevive; y que
// una frontera NORMAL no sufre falsos EBUDGET. Se usa el arnés acotado del watchdog
// (runTasksBounded / evalBounded): si el corte NO ocurriera, el Call giraría para
// siempre y el fallo se manifiesta como FALLO acotado, no como un CI colgado.

import (
	"testing"
	"time"
)

// emitBounded emite `name` (EmitEvent, hilo principal) con un tope de wall-clock. Si
// EmitEvent no vuelve en `cap` —un handler quema CPU y el watchdog NO lo cortó, así
// que el Eval del emit gira sin fin— cancela el ctx de la instancia (corta el Call en
// vuelo) y FALLA en vez de colgar el CI. Espejo de runTasksBounded para el emit.
func emitBounded(t *testing.T, inst *Instance, name string, cap time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- inst.EmitEvent(name, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EmitEvent(%q): %v", name, err)
		}
	case <-time.After(cap):
		inst.cancel()
		t.Fatalf("EmitEvent(%q) no terminó en %v: ¿el watchdog no cubrió el handler del bus?", name, cap)
	}
}

// (a) A-36 🔒: un cleanup con un bucle de CPU puro se aborta por el watchdog, los
// DEMÁS cleanups de la misma task siguen corriendo (LIFO) y el runtime sobrevive
// —otra task avanza después, RunTasks/painter no se congela—. Sin el arreglo, el
// bucle en __finish (hilo del scheduler, sin count-hook) colgaría el Call.
func TestA36CleanupBucleNoCongela(t *testing.T) {
	inst := newInstanceBudget(t, 30*time.Millisecond)
	if _, lerr, err := inst.Eval(`
		traza = ""
		vivo = "no"
		local w = nu.task.spawn(function()
			nu.task.cleanup(function() traza = traza .. "C" end)  -- registrado 1º → corre 3º
			nu.task.cleanup(function() while true do end end)     -- bucle de CPU: lo corta el WD
			nu.task.cleanup(function() traza = traza .. "A" end)  -- registrado 3º → corre 1º
		end)
		nu.task.spawn(function() pcall(function() nu.task.await(w) end) end)
		nu.task.spawn(function()
			nu.task.sleep(1)
			vivo = "si"   -- el estado sigue vivo: esta task avanza tras el corte del cleanup
		end)`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	runTasksBounded(t, inst, 3*time.Second)
	// LIFO: corre A (3º registrado), el bucle se aborta, y C (1º registrado) corre DESPUÉS
	// del corte —la prueba de que el bucle no congeló __finish—.
	if out, _, _ := inst.Eval(`return tostring(traza)`); out != "AC" {
		t.Fatalf("los cleanups no siguieron tras cortar el bucle: traza=%q (want AC)", out)
	}
	if out, _, _ := inst.Eval(`return tostring(vivo)`); out != "si" {
		t.Fatalf("el runtime no sobrevivió al bucle en el cleanup: vivo=%q", out)
	}
}

// (b) A-36 🔒: un handler del bus con un bucle de CPU, despachado desde el HILO
// PRINCIPAL (EmitEvent), se aborta por el watchdog; los demás handlers de ese evento
// siguen corriendo y el runtime sobrevive. Sin el arreglo, el bucle en __ev_dispatch
// (hilo del scheduler, sin count-hook) colgaría el Eval de EmitEvent para siempre.
func TestA36HandlerBusBucleNoCongela(t *testing.T) {
	inst := newInstanceBudget(t, 30*time.Millisecond)
	if _, lerr, err := inst.Eval(`
		got = ""
		nu.events.on("test:boom", function() got = got .. "1" end)
		nu.events.on("test:boom", function() while true do end end)  -- bucle: lo corta el WD
		nu.events.on("test:boom", function() got = got .. "3" end)`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	emitBounded(t, inst, "test:boom", 3*time.Second)
	// El handler 1 corrió, el 2 (bucle) se abortó, y el 3 corrió DESPUÉS del corte.
	if out, _, _ := inst.Eval(`return tostring(got)`); out != "13" {
		t.Fatalf("los handlers no siguieron tras cortar el bucle: got=%q (want 13)", out)
	}
	// El runtime sigue usable tras el corte.
	if out, _, _ := inst.Eval(`return "alive"`); out != "alive" {
		t.Fatalf("el runtime no sobrevivió al bucle en el handler: got=%q", out)
	}
}

// (c) A-36 🔒: un cleanup y un handler NORMALES (trabajo acotado, muy por debajo del
// slice) NO sufren falsos EBUDGET —completan y dejan su efecto—. Guarda contra una
// regresión donde el envoltorio del watchdog cortara fronteras bien portadas.
func TestA36FronteraNormalSinFalsoEBUDGET(t *testing.T) {
	inst := newInstanceBudget(t, 500*time.Millisecond)
	if _, lerr, err := inst.Eval(`
		clean_done = "no"
		local w = nu.task.spawn(function()
			nu.task.cleanup(function()
				local s = 0; for i = 1, 50000 do s = s + i end; clean_done = tostring(s)
			end)
		end)
		nu.task.spawn(function() pcall(function() nu.task.await(w) end) end)
		handler_done = "no"
		nu.events.on("test:ping", function()
			local s = 0; for i = 1, 50000 do s = s + i end; handler_done = tostring(s)
		end)`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	runTasksBounded(t, inst, 3*time.Second)
	const want = "1250025000" // sum 1..50000 = 50000*50001/2
	if out, _, _ := inst.Eval(`return tostring(clean_done)`); out != want {
		t.Fatalf("el cleanup normal fue cortado (falso EBUDGET): clean_done=%q (want %q)", out, want)
	}
	emitBounded(t, inst, "test:ping", 3*time.Second)
	if out, _, _ := inst.Eval(`return tostring(handler_done)`); out != want {
		t.Fatalf("el handler normal fue cortado (falso EBUDGET): handler_done=%q (want %q)", out, want)
	}
}
