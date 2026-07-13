package runtime

import (
	"strings"
	"testing"
	"time"
)

// Tests del bus de eventos `nu.events` (api.md §4, sesión S10, inventario 🔒).
// Esta sesión implementa lógica nuestra con casos de borde silenciosos —orden de
// despacho, foto vs. mutación concurrente del registro, encolado por anchura,
// aislamiento por `pcall`—, así que los unitarios Go son obligatorios y NOMBRAN
// el hallazgo G10 que blindan.

// --- Camino feliz / snippet del autor de extensiones ---------------------------

// TestEventsOnEmitBasic ejercita la firma desde el lado del autor de extensiones:
// `on` + `emit` con payload; el handler recibe el payload tal cual. Es el snippet
// de la Definition of Done §2.
func TestEventsOnEmitBasic(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		got = nil
		nu.events.on("plug:hello", function(p) got = p.msg end)
		nu.events.emit("plug:hello", { msg = "hola" })
		assert(got == "hola", "el handler debe recibir el payload")
	`)
}

// TestEventsEmitNoPayload comprueba que `emit` sin payload entrega `nil` (LNil)
// al handler —la firma `payload?` es opcional (§4)—.
func TestEventsEmitNoPayload(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		seen, value = false, "sentinela"
		nu.events.on("plug:ping", function(p) seen = true; value = p end)
		nu.events.emit("plug:ping")
		assert(seen, "el handler debe correr aunque no haya payload")
		assert(value == nil, "sin payload el handler recibe nil")
	`)
}

// TestEventsOrderOfRegistration: los handlers corren en **orden de registro**
// (§4). Sin esto, dos plugins no podrían razonar sobre quién ve el evento antes.
func TestEventsOrderOfRegistration(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		order = {}
		nu.events.on("plug:e", function() order[#order+1] = "a" end)
		nu.events.on("plug:e", function() order[#order+1] = "b" end)
		nu.events.on("plug:e", function() order[#order+1] = "c" end)
		nu.events.emit("plug:e")
		assert(table.concat(order) == "abc", "orden de registro: got "..table.concat(order))
	`)
}

// TestEventsNoSubscribersNoop: emitir un evento sin suscriptores no lanza ni hace
// nada observable —un bus genérico no obliga a registrar antes de emitir—.
func TestEventsNoSubscribersNoop(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`nu.events.emit("plug:nadie", {x=1}); return "ok"`, "ok")
}

// --- once ----------------------------------------------------------------------

// TestEventsOnceRunsOnce (🔒): `once` corre UNA vez y se auto-cancela; un segundo
// `emit` ya no lo dispara (§4).
func TestEventsOnceRunsOnce(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		n = 0
		nu.events.once("plug:o", function() n = n + 1 end)
		nu.events.emit("plug:o")
		nu.events.emit("plug:o")
		nu.events.emit("plug:o")
		assert(n == 1, "once debe correr exactamente una vez, corrió "..n)
	`)
}

// TestEventsOnceThatThrowsStillOnce (🔒): un `once` que LANZA no vuelve a correr
// —el contrato de "una vez" no depende de que el handler termine bien—. El error
// queda aislado por `pcall` (ADR-008) y en el log.
func TestEventsOnceThatThrowsStillOnce(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		n = 0
		nu.events.once("plug:o", function() n = n + 1; error("boom") end)
		nu.events.emit("plug:o")
		nu.events.emit("plug:o")
		assert(n == 1, "once que lanza sigue corriendo solo una vez, corrió "..n)
	`)
	assertLogContains(t, h, "un handler de nu.events lanzó")
}

// --- G10 (1): despacho sobre FOTO de suscriptores ------------------------------

// TestG10DispatchOverSnapshot (🔒, G10): un `on` registrado DURANTE el despacho de
// un evento NO ve ese evento en curso —solo los futuros—. El despacho corre sobre
// la foto tomada al emitir.
func TestG10DispatchOverSnapshot(t *testing.T) {
	// G10: el despacho corre sobre una FOTO de suscriptores; suscribirse durante el
	// despacho no hace ver el evento actual.
	h := newHarness(t)
	h.eval(`
		late_ran = false
		nu.events.on("plug:e", function()
			-- Suscriptor tardío: se registra MIENTRAS se despacha "plug:e".
			nu.events.on("plug:e", function() late_ran = true end)
		end)
		nu.events.emit("plug:e")
		assert(late_ran == false, "G10: el suscriptor tardío NO debe ver el evento en curso")

		-- Pero SÍ ve el siguiente.
		nu.events.emit("plug:e")
		assert(late_ran == true, "G10: el suscriptor tardío sí ve los eventos futuros")
	`)
}

// --- G10 (2): cancelar surte efecto inmediato ----------------------------------

// TestG10CancelImmediateDuringDispatch (🔒, G10): si un handler cancela una `Sub`
// que aún no le ha tocado correr en ESTE despacho, esa `Sub` ya no corre —aunque
// estuviera en la foto—. La foto es de identidad; antes de invocar cada handler se
// comprueba que siga viva.
func TestG10CancelImmediateDuringDispatch(t *testing.T) {
	// G10: cancelar durante el despacho surte efecto inmediato (la Sub aún en la
	// foto pero ya muerta no corre).
	h := newHarness(t)
	h.eval(`
		b_ran = false
		local subB
		-- A se registra primero y cancela a B (que va después en la foto).
		nu.events.on("plug:e", function() subB:cancel() end)
		subB = nu.events.on("plug:e", function() b_ran = true end)
		nu.events.emit("plug:e")
		assert(b_ran == false, "G10: cancelar B desde A debe impedir que B corra en este despacho")
	`)
}

// TestG10CancelSelfStopsRedispatch (🔒, G10): un handler que se cancela a sí mismo
// no vuelve a correr en emisiones posteriores (cancelación inmediata + purga).
func TestG10CancelSelfStopsRedispatch(t *testing.T) {
	// G10: cancelar surte efecto inmediato, también para emisiones posteriores.
	h := newHarness(t)
	h.eval(`
		n = 0
		local sub
		sub = nu.events.on("plug:e", function() n = n + 1; sub:cancel() end)
		nu.events.emit("plug:e")
		nu.events.emit("plug:e")
		assert(n == 1, "un handler que se cancela no vuelve a correr, corrió "..n)
	`)
}

// TestEventsCancelIdempotent: cancelar dos veces es inocuo (§4).
func TestEventsCancelIdempotent(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local sub = nu.events.on("plug:e", function() end)
		sub:cancel()
		sub:cancel()
		return "ok"
	`, "ok")
}

// --- G10 (3): emits anidados ENCOLADOS (anchura, no recursión) ------------------

// TestG10NestedEmitQueuedBreadthFirst (🔒, G10): un `emit` dentro de un handler se
// ENCOLA y se despacha al terminar el actual —anchura, no profundidad—. Se observa
// por el orden: el handler de "a" emite "b", pero "b" no se despacha en mitad de
// "a"; corre después.
func TestG10NestedEmitQueuedBreadthFirst(t *testing.T) {
	// G10: emits anidados encolados (anchura, no recursión); el anidado se despacha
	// al terminar el despacho en curso, no recursivamente en su mitad.
	h := newHarness(t)
	h.eval(`
		log = {}
		nu.events.on("plug:a", function()
			log[#log+1] = "a:start"
			nu.events.emit("plug:b")   -- se ENCOLA, no se despacha aquí
			log[#log+1] = "a:end"
		end)
		nu.events.on("plug:b", function() log[#log+1] = "b" end)
		nu.events.emit("plug:a")
		-- Si fuera recursión, sería a:start,b,a:end. Por anchura: a:start,a:end,b.
		assert(table.concat(log, ",") == "a:start,a:end,b",
			"G10: emit anidado debe encolarse (anchura): got "..table.concat(log, ","))
	`)
}

// TestG10NestedEmitOrderTwoChildren (🔒, G10): dos emits anidados desde un handler
// se despachan en el orden en que se encolaron, ambos tras el actual.
func TestG10NestedEmitOrderTwoChildren(t *testing.T) {
	// G10: la cola de emits anidados es FIFO (anchura).
	h := newHarness(t)
	h.eval(`
		log = {}
		nu.events.on("plug:root", function()
			log[#log+1] = "root"
			nu.events.emit("plug:x")
			nu.events.emit("plug:y")
		end)
		nu.events.on("plug:x", function() log[#log+1] = "x" end)
		nu.events.on("plug:y", function() log[#log+1] = "y" end)
		nu.events.emit("plug:root")
		assert(table.concat(log, ",") == "root,x,y",
			"G10: FIFO por anchura: got "..table.concat(log, ","))
	`)
}

// TestG10PingPongFlattenedNoStackOverflow (🔒, G10): un ping-pong ACOTADO entre dos
// handlers (A emite B, B emite A, hasta un tope) se aplana en un bucle plano y NO
// desborda la pila Go. Si fuera recursión, miles de niveles reventarían; aquí es
// un bucle plano que termina. (El caso infinito lo corta el watchdog, otro test.)
func TestG10PingPongFlattenedNoStackOverflow(t *testing.T) {
	// G10: ping-pong entre handlers se aplana (no recursión); 100000 rebotes no
	// desbordan la pila Go.
	h := newHarness(t)
	h.eval(`
		count = 0
		local limit = 100000
		nu.events.on("plug:ping", function()
			count = count + 1
			if count < limit then nu.events.emit("plug:pong") end
		end)
		nu.events.on("plug:pong", function()
			count = count + 1
			if count < limit then nu.events.emit("plug:ping") end
		end)
		nu.events.emit("plug:ping")
		assert(count == limit, "ping-pong acotado aplanado: count="..count)
	`)
}

// TestG10PingPongInfiniteCutByWatchdog (🔒, G10 + S09): un ping-pong INFINITO entre
// dos handlers, con el `emit` raíz lanzado DESDE UNA TASK, lo corta el watchdog.
// El drenado es plano (no recursión), corre en el slice de la task, y el borde
// cooperativo del bucle de drenado observa el disparo del watchdog y aborta con
// `EBUDGET` —sin colgar ni desbordar la pila—. Es el caso que api.md §4 nombra
// ("un ping-pong infinito entre plugins se vuelve un bucle plano que el watchdog
// corta"). Si esto colgara, el test reventaría por el `-timeout` del paquete.
func TestG10PingPongInfiniteCutByWatchdog(t *testing.T) {
	// G10/S09: ping-pong infinito entre handlers, emitido desde una task, cortado
	// por el watchdog (EBUDGET), no recursión.
	h := newHarnessBudget(t, 30*time.Millisecond)
	done := make(chan struct{})
	go func() {
		h.eval(`
			nu.events.on("plug:ping", function() nu.events.emit("plug:pong") end)
			nu.events.on("plug:pong", function() nu.events.emit("plug:ping") end)
			nu.task.spawn(function()
				nu.events.emit("plug:ping")  -- raíz dentro de una task
			end)
		`)
		close(done)
	}()
	select {
	case <-done:
		// El watchdog cortó la task; EvalString volvió. Confirma que emitió misbehaved.
		assertLogContains(t, h, "core:plugin.misbehaved")
	case <-time.After(10 * time.Second):
		t.Fatal("HANG: el ping-pong infinito no fue cortado por el watchdog")
	}
}

// --- G10 (pcall por frontera, ADR-008) -----------------------------------------

// TestG10HandlerErrorIsolated (🔒, G10/ADR-008): un handler que lanza queda aislado
// bajo `pcall` por frontera y NO impide correr a los demás de la foto; el error va
// al log. El orden de registro se mantiene (el que va después del que lanza corre).
func TestG10HandlerErrorIsolated(t *testing.T) {
	// G10/ADR-008: pcall por frontera; un handler que lanza no impide a los demás.
	h := newHarness(t)
	h.eval(`
		ran = {}
		nu.events.on("plug:e", function() ran[#ran+1] = "before" end)
		nu.events.on("plug:e", function() error("boom") end)
		nu.events.on("plug:e", function() ran[#ran+1] = "after" end)
		nu.events.emit("plug:e")
		assert(table.concat(ran, ",") == "before,after",
			"un handler que lanza no debe impedir a los demás: got "..table.concat(ran, ","))
	`)
	assertLogContains(t, h, "un handler de nu.events lanzó")
}

// --- Handlers síncronos: el patrón canónico para IO (§1.3) ----------------------

// TestEventsHandlerSpawnsTaskForIO: el patrón canónico (§1.3) —un handler síncrono
// que necesita IO lanza una task con `nu.task.spawn`—. La task sí puede suspender;
// `EvalString` espera a que termine (waitIdle).
func TestEventsHandlerSpawnsTaskForIO(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		done = false
		nu.events.on("plug:e", function()
			nu.task.spawn(function()
				nu.task.sleep(1)  -- ⏸ legal dentro de la task
				done = true
			end)
		end)
		nu.events.emit("plug:e")
	`)
	// La task lanzada por el handler corre durante waitIdle; al volver, done es true.
	h.expectEval(`return tostring(done)`, "true")
}

// --- Validaciones de firma -----------------------------------------------------

// TestEventsCancelWrongHandle: invocar `cancel` sobre un userdata que NO es un
// `Sub` (p. ej. un handle de `Task`) da `EINVAL` estructurado —no se confunde un
// handle con otro—.
func TestEventsCancelWrongHandle(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`
		local sub = nu.events.on("plug:e", function() end)
		local cancel = getmetatable(sub).__index.cancel
		-- Un handle de Task es userdata, pero no un Sub: debe dar EINVAL.
		local task = nu.task.spawn(function() return 1 end)
		cancel(task)
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("Sub:cancel con handle inválido: got %q, want EINVAL", se.Code)
	}
}

// --- misbehaved cableado (S09 → S10) -------------------------------------------

// TestMisbehavedWiredToBus (🔒, G10): una task abortada por el watchdog produce un
// evento `core:plugin.misbehaved` que un `on` del estado principal captura, con el
// payload `{plugin, reason}`. Esto blinda el cableado del gancho interno
// `rt.emitMisbehaved` (S09) al bus real (S10) —emitido de forma segura desde la
// goroutine de la task, bajo el token (ver docs/decisiones-implementacion.md S10)—.
func TestMisbehavedWiredToBus(t *testing.T) {
	// G10/S09: el gancho rt.emitMisbehaved emite core:plugin.misbehaved por el bus.
	h := newHarnessBudget(t, 30*time.Millisecond)
	h.eval(`
		seen = false
		seen_plugin, seen_reason = nil, nil
		nu.events.on("core:plugin.misbehaved", function(p)
			seen = true
			seen_plugin = p.plugin
			seen_reason = p.reason
		end)
		-- Lanza una task con un bucle de CPU puro que el watchdog cortará.
		nu.task.spawn(function()
			while true do end
		end)
	`)
	// El watchdog corta la task durante waitIdle; emitMisbehaved emite el evento,
	// cuyo handler (registrado en host) corre antes de que el runtime quede idle.
	h.expectEval(`return tostring(seen)`, "true")
	h.expectEval(`return seen_plugin`, "user")
	if got := h.eval(`return seen_reason`); len(got) != 1 || !strings.Contains(got[0], "EBUDGET") {
		t.Fatalf("reason de misbehaved: got %q, quiere mencionar EBUDGET", got)
	}
}

// --- helper -------------------------------------------------------------------

// assertLogContains falla si ninguna línea del log contiene `substr`. Usa el
// helper `logLines` del arnés (harness_test.go).
func assertLogContains(t *testing.T, h *harness, substr string) {
	t.Helper()
	for _, ln := range h.logLines() {
		if strings.Contains(ln, substr) {
			return
		}
	}
	t.Fatalf("ninguna línea del log contiene %q; log:\n%s", substr, strings.Join(h.logLines(), "\n"))
}
