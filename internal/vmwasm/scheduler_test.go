package vmwasm

// Tests de M06: el scheduler por corrutinas nativas (ADR-020). Blindan que el
// modelo funciona de punta a punta —tasks que ceden por ⏸ y el driver Go las
// reanuda— con la semántica observable de api.md §1.3.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// runScript evalúa `setup` (que crea tasks con nu.task.spawn) y conduce el bucle
// hasta que todas terminan; luego devuelve el valor de la global `out`.
func runScript(t *testing.T, setup string) string {
	t.Helper()
	inst := newInstance(t)
	if _, lerr, err := inst.Eval(setup); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	if err := inst.RunTasks(context.Background()); err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	out, _, _ := inst.Eval(`return tostring(out)`)
	return out
}

// M06.1: una task corre, cede por sleep y termina; el driver la lleva a fin.
func TestSchedTaskSimple(t *testing.T) {
	out := runScript(t, `
		out = "no"
		nu.task.spawn(function()
			nu.task.sleep(1)
			out = "hecho"
		end)`)
	if out != "hecho" {
		t.Fatalf("got %q", out)
	}
}

// M06.2: CONCURRENCIA REAL — dos tasks se intercalan por sus sleeps. La task A
// duerme más que B en el primer tramo, así que el orden de completado es
// determinista (B1, A1, B2, A2...) y prueba que las corrutinas ceden de verdad
// y el driver las planifica cooperativamente (lo que ADR-011 no podía hacer sin
// el baile del token).
func TestSchedConcurrencia(t *testing.T) {
	out := runScript(t, `
		local traza = {}
		out = ""
		nu.task.spawn(function()
			nu.task.sleep(20); traza[#traza+1] = "A1"
			nu.task.sleep(20); traza[#traza+1] = "A2"
		end)
		nu.task.spawn(function()
			nu.task.sleep(5);  traza[#traza+1] = "B1"
			nu.task.sleep(5);  traza[#traza+1] = "B2"
		end)
		-- una tercera task espera a que las otras "duerman" y compone la traza
		nu.task.spawn(function()
			nu.task.sleep(100)
			out = table.concat(traza, ",")
		end)`)
	// B corre sus dos tramos (5+5=10ms) antes de que A complete el primero (20ms):
	if out != "B1,B2,A1,A2" {
		t.Fatalf("intercalado incorrecto: got %q (esperado B1,B2,A1,A2)", out)
	}
}

// M06.3: await — una task espera el resultado de otra.
func TestSchedAwait(t *testing.T) {
	out := runScript(t, `
		out = "no"
		local worker = nu.task.spawn(function()
			nu.task.sleep(5)
			return 42
		end)
		nu.task.spawn(function()
			local r = nu.task.await(worker)
			out = "recibido:" .. tostring(r)
		end)`)
	if out != "recibido:42" {
		t.Fatalf("got %q", out)
	}
}

// M06.4: await de una task que YA terminó devuelve su resultado sin ceder.
func TestSchedAwaitYaTerminada(t *testing.T) {
	out := runScript(t, `
		out = "no"
		nu.task.spawn(function()
			local w = nu.task.spawn(function() return "listo" end)
			nu.task.sleep(10)  -- da tiempo a que w termine
			out = nu.task.await(w)
		end)`)
	if out != "listo" {
		t.Fatalf("got %q", out)
	}
}

// M06.5: yield a través de pcall dentro de una task (G31/ADR-011 imposible en
// gopher; aquí natural). La task suspende DENTRO de un pcall y sigue viva.
func TestSchedYieldEnPcallDentroDeTask(t *testing.T) {
	out := runScript(t, `
		out = "no"
		nu.task.spawn(function()
			local ok, v = pcall(function()
				nu.task.sleep(1)
				return "ok-tras-sleep"
			end)
			out = tostring(ok) .. ":" .. tostring(v)
		end)`)
	if out != "true:ok-tras-sleep" {
		t.Fatalf("got %q", out)
	}
}

// M06.6: el bucle respeta la cancelación del contexto (base del apagado, M07).
func TestSchedContextCancel(t *testing.T) {
	inst := newInstance(t)
	if _, lerr, err := inst.Eval(`
		nu.task.spawn(function() nu.task.sleep(60000) end)`); err != nil || lerr != "" {
		t.Fatalf("setup: %v %q", err, lerr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	err := inst.RunTasks(ctx)
	if err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("el bucle debía cortarse por el contexto; got %v", err)
	}
}
