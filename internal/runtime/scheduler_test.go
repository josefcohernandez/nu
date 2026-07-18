package runtime

import (
	"strings"
	"testing"
)

// Tests 🔒 del scheduler (S04): el puente del modelo goroutine-por-task + token
// Lua (ADR-011). Lo que se blinda (inventario del plan): una task se suspende
// (suelta el token, una goroutine de fondo entrega el valor) y otra la espera con
// `await`; sin data races. La suite corre con `-race` (`go test -race ./...`): si
// algo tocara Lua sin el token, saltaría aquí.
//
// `suspend_echo` es la primitiva ⏸ interna de prueba; se registra como global del
// snippet vía el arnés, no es superficie pública del runtime.

// withEcho registra `suspend_echo` en el runtime bajo prueba: el primitivo ⏸
// interno que suspende la task y la reanuda con el valor que acarrea una
// goroutine de fondo.
func withEcho(h *harness) {
	// suspend_echo expresado en Lua sobre wasm: suspende de verdad con
	// enu.task.sleep(0) (una ida y vuelta real por el driver Go) y devuelve su
	// argumento. Validaciones: sólo dentro de una task (⏸) y sólo string/number.
	// __current es el id de la task en curso (nil fuera de toda task), la señal que
	// usa el propio scheduler.
	h.defWasmGlobal(`function suspend_echo(x)
  if __current == nil then error({ code = "EINVAL", message = "suspend_echo solo puede llamarse dentro de una task" }) end
  local t = type(x)
  if t ~= "string" and t ~= "number" then error({ code = "EINVAL", message = "suspend_echo espera string o number" }) end
  enu.task.sleep(0)
  return x
end`)
}

// TestSpawnAwaitAcrossSuspension es el caso central de S04: una task se suspende
// en un ⏸ (despertada por una goroutine de fondo) y otra la espera con `await`,
// recibiendo su valor. Cubre el puente de extremo a extremo.
func TestSpawnAwaitAcrossSuspension(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = {}
		local producer = enu.task.spawn(function()
			return suspend_echo("hola")
		end)
		enu.task.spawn(function()
			out.v = producer:await()
		end)
	`)
	h.expectEval(`return out.v`, "hola")
}

// TestAwaitInTailPosition: `return ⏸fn()` y `return t:await()` en posición final
// funcionan. En el modelo goroutine-por-task no hay yield de corrutina, así que
// la tail call no destruye la continuación (lo que sí pasaba con corrutinas;
// ver ADR-011 / problemas.md).
func TestAwaitInTailPosition(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = {}
		local p = enu.task.spawn(function() return suspend_echo("cola") end) -- tail ⏸
		local q = enu.task.spawn(function() return p:await() end)            -- tail await
		enu.task.spawn(function() out.v = q:await() end)
	`)
	h.expectEval(`return out.v`, "cola")
}

// TestAwaitMultipleResults: una task puede devolver varios valores y `await` los
// entrega todos (el contrato `-> any` no se limita a uno).
func TestAwaitMultipleResults(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = {}
		local p = enu.task.spawn(function()
			suspend_echo("x")        -- fuerza una suspensión real antes de devolver
			return 1, 2, 3
		end)
		enu.task.spawn(function()
			local a, b, c = p:await()
			out.a, out.b, out.c = a, b, c
		end)
	`)
	h.expectEval(`return out.a .. "," .. out.b .. "," .. out.c`, "1,2,3")
}

// TestAwaitNonSuspendingProducer: `await` sobre una task que no suspende devuelve
// su valor (la rama "ya terminó" o "termina enseguida" la resuelve `await`
// transparentemente, independiente del orden de las goroutines).
func TestAwaitNonSuspendingProducer(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		local p = enu.task.spawn(function() return 42 end)
		enu.task.spawn(function() out.v = p:await() end)
	`)
	h.expectEval(`return out.v`, "42")
}

// TestAwaitErrorCatchableByPcall: si la task esperada lanza, `await` relanza ese
// error y un `pcall` que envuelve el `await` —**aunque el await suspenda**— lo
// captura (api.md §1.4). Esta es justo la propiedad que el modelo de corrutinas
// no podía dar y que motivó ADR-011. La tabla estructurada llega intacta.
func TestAwaitErrorCatchableByPcall(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = {}
		local p = enu.task.spawn(function()
			suspend_echo("x")        -- suspende, luego falla
			error({ code = "EINVAL", message = "boom" })
		end)
		enu.task.spawn(function()
			local ok, e = pcall(function() return p:await() end)
			out.ok = ok
			out.code = e.code
			out.msg = e.message
		end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "EINVAL")
	h.expectEval(`return out.msg`, "boom")
}

// TestPcallCatchesErrorAfterSuspension: un `pcall` cuyo cuerpo suspende y luego
// lanza captura el error (no es cancelación). Es el invariante de §1.4 que solo
// el modelo sin yield permite; sin él, todo el modelo de errores se cae.
func TestPcallCatchesErrorAfterSuspension(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = {}
		enu.task.spawn(function()
			local ok, e = pcall(function()
				suspend_echo("a")
				error({ code = "EIO", message = "disco" })
			end)
			out.ok, out.code = tostring(ok), e.code
		end)
	`)
	h.expectEval(`return out.ok`, "false")
	h.expectEval(`return out.code`, "EIO")
}

// TestAwaitOutsideTask: `await` es ⏸; llamarla fuera de una task es un error
// (§1.3). El chunk de `enu -e` corre en el estado principal, no en una task.
func TestAwaitOutsideTask(t *testing.T) {
	h := newHarness(t)

	se := h.evalErr(`
		local p = enu.task.spawn(function() return 1 end)
		return p:await()
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("await fuera de task: code = %q, want %q", se.Code, CodeEINVAL)
	}
}

// TestSuspendOutsideTask: lo mismo para cualquier ⏸ (aquí la primitiva interna):
// fuera de una task, lanza EINVAL en vez de bloquear el hilo principal.
func TestSuspendOutsideTask(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	se := h.evalErr(`return suspend_echo("nope")`)
	if se.Code != CodeEINVAL {
		t.Fatalf("⏸ fuera de task: code = %q, want %q", se.Code, CodeEINVAL)
	}
}

// TestSelfAwaitRejected: una task no puede esperarse a sí misma (deadlock
// garantizado) → EINVAL. La task guarda su propio handle en un global y lo
// espera; el EINVAL la hace fallar y, al no estar esperada por nadie, queda en
// el log fire-and-forget, que es donde lo observamos.
func TestSelfAwaitRejected(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		handle = enu.task.spawn(function()
			return handle:await()
		end)
	`)
	lines := h.logLines()
	found := false
	for _, ln := range lines {
		if strings.Contains(ln, "EINVAL") && strings.Contains(ln, "sí misma") {
			found = true
		}
	}
	if !found {
		t.Fatalf("self-await debería lanzar EINVAL (visible en el log fire-and-forget); log:\n%s",
			strings.Join(lines, "\n"))
	}
}

// TestSpawnArgsPassed: los argumentos extra de `spawn` llegan a la función de la
// task (§3).
func TestSpawnArgsPassed(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		local p = enu.task.spawn(function(a, b) return a + b end, 20, 22)
		enu.task.spawn(function() out.v = p:await() end)
	`)
	h.expectEval(`return out.v`, "42")
}

// TestManyConcurrentSuspensions estresa el puente con muchas tasks suspendidas a
// la vez: cada una lanza su goroutine de fondo y todas deben reanudarse con su
// propio valor. Es el caso que `-race` vigila (N goroutines a la vez) y que
// comprueba que no se cruzan los valores.
func TestManyConcurrentSuspensions(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		sum = 0
		count = 0
		local tasks = {}
		for i = 1, 50 do
			tasks[i] = enu.task.spawn(function()
				return suspend_echo(i)   -- cada task espera su propio i
			end)
		end
		enu.task.spawn(function()
			for i = 1, 50 do
				sum = sum + tasks[i]:await()
				count = count + 1
			end
		end)
	`)
	h.expectEval(`return tostring(sum)`, "1275") // 1+2+...+50
	h.expectEval(`return tostring(count)`, "50")
}

// TestUnhandledTaskErrorLogged: una task fire-and-forget que lanza y nadie espera
// deja rastro en el log (best-effort de S04; S10 trae el evento).
func TestUnhandledTaskErrorLogged(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		enu.task.spawn(function()
			error({ code = "EINVAL", message = "nadie me espera" })
		end)
	`)
	lines := h.logLines()
	found := false
	for _, ln := range lines {
		if strings.Contains(ln, "nadie me espera") && strings.Contains(ln, "EINVAL") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no se registró el error de la task sin await; log:\n%s", strings.Join(lines, "\n"))
	}
}

// TestEvalTaskStringErrorNoSpuriousLog: la contraparte de
// TestUnhandledTaskErrorLogged. Cuando una task la lanza el HOST vía
// `EvalTaskString` (el ejecutor headless del binario: un turno de agente,
// `--continue`...), su error NO es fire-and-forget: `EvalTaskString` lo recoge de
// `t.errValue` y lo devuelve como `*StructuredError` al llamante (que el CLI mapea
// a un código de salida). Por eso una ruta de error LEGÍTIMA —p. ej. `--continue`
// sin sesiones, que lanza un error estructurado— NO debe ensuciar el log con la
// línea best-effort "una task terminó con error y nadie hizo await". Esta prueba
// blinda las dos mitades: (a) el error sigue propagándose con su `code` intacto;
// (b) la línea espuria NO aparece en el log.
func TestEvalTaskStringErrorNoSpuriousLog(t *testing.T) {
	h := newHarness(t)

	_, err := h.rt.EvalTaskString(`error({ code = "EPROVIDER", message = "sin sesiones" })`)
	if err == nil {
		t.Fatalf("EvalTaskString debió devolver el error de la task")
	}
	se, ok := err.(*StructuredError)
	if !ok {
		t.Fatalf("se esperaba *StructuredError, llegó %T: %v", err, err)
	}
	if se.Code != "EPROVIDER" {
		t.Fatalf("code inesperado: got %q, want EPROVIDER", se.Code)
	}

	// La línea best-effort de error sin await NO debe haberse escrito: el host SÍ
	// consumió el desenlace (lo devolvió arriba), así que no es un error perdido.
	for _, ln := range h.logLines() {
		if strings.Contains(ln, "nadie hizo await") {
			t.Fatalf("EvalTaskString dejó la línea espuria 'nadie hizo await' en el log:\n%s", ln)
		}
	}
}

// TestRuntimeReusableAcrossEvals: el scheduler queda quiescente tras cada
// `EvalString`, así que el mismo runtime corre varios chunks con tasks sin
// arrastrar estado (lo asume el arnés, que reusa el runtime entre asserts).
func TestRuntimeReusableAcrossEvals(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`r1 = nil; enu.task.spawn(function() r1 = suspend_echo("a") end)`)
	h.expectEval(`return r1`, "a")
	h.eval(`r2 = nil; enu.task.spawn(function() r2 = suspend_echo("b") end)`)
	h.expectEval(`return r2`, "b")
}

// TestSpawnFromWithinTask: lanzar una task desde dentro de otra funciona —la
// nueva goroutine corre cuando la creadora suelta el token (al suspenderse o al
// terminar)—, y la creadora puede esperarla.
func TestSpawnFromWithinTask(t *testing.T) {
	h := newHarness(t)
	withEcho(h)

	h.eval(`
		out = {}
		enu.task.spawn(function()
			local inner = enu.task.spawn(function() return suspend_echo("anidada") end)
			out.v = inner:await()
		end)
	`)
	h.expectEval(`return out.v`, "anidada")
}
