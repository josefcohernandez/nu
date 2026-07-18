package vmwasm

// Valida el despacho de métodos de handle SUSPENDENTES (M13b, prerequisito de
// proc/http/ws): un método registrado con RegisterHandleMethod se invoca vía
// __hcall_s (enu.__handle_call_s, la primitiva de despacho ⏸ que registró M10) y
// CEDE al scheduler —su trabajo bloqueante corre en una goroutine de fondo,
// mientras otras tasks avanzan—. Es lo que deja modelar Proc:wait / Ws:recv sin
// un mecanismo nuevo: el wrapper del tipo llama a __hcall_s para los métodos ⏸ y a
// __hcall para los síncronos.

import (
	"testing"
	"time"
)

func TestHandleMetodoSuspendente(t *testing.T) {
	out := poolRun(t, func(p *Pool) {
		// una primitiva que crea un handle Timer.
		p.Register("test.timer", func(inst *Instance, args []any) ([]any, error) {
			return []any{inst.AllocHandle("Timer", "T")}, nil
		})
		// su método "wait" TARDA 50ms (trabajo bloqueante). Se despacha vía __hcall_s,
		// así que corre en la goroutine de fondo del scheduler.
		p.RegisterHandleMethod("Timer", "wait", func(inst *Instance, val any, args []any) ([]any, error) {
			time.Sleep(50 * time.Millisecond)
			return []any{"listo"}, nil
		})
	}, `
		local traza = {}
		out = ""
		enu.task.spawn(function()
			local t = enu.test.timer()
			traza[#traza+1] = "A-espera"
			local r = __hcall_s(t.__id, "wait")   -- método de handle SUSPENDENTE
			traza[#traza+1] = "A-recibe:" .. r
		end)
		enu.task.spawn(function()
			enu.task.sleep(5); traza[#traza+1] = "B-5"
			enu.task.sleep(5); traza[#traza+1] = "B-10"
		end)
		enu.task.spawn(function()
			enu.task.sleep(100); out = table.concat(traza, ",")
		end)`)
	// B-5 y B-10 (5/10ms) ocurren ANTES de que A reciba (~50ms): el método ⏸ cedió.
	if out != "A-espera,B-5,B-10,A-recibe:listo" {
		t.Fatalf("el método de handle suspendente no cedió al scheduler: got %q", out)
	}
}
