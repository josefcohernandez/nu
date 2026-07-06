package vmwasm

// Tests de M08: el bus de eventos nu.events con la semántica de G10 (foto,
// cancelar-inmediato, subs-nuevos-solo-futuros, emits-anidados-por-anchura) y
// los timers nu.task.every. Todo síncrono (emit no suspende) — se prueba con
// Eval directo, sin el driver del scheduler.

import "testing"

// evalOut evalúa un chunk y devuelve la global `out` (sin driver de tasks).
func evalOut(t *testing.T, chunk string) string {
	t.Helper()
	inst := newInstance(t)
	if _, lerr, err := inst.Eval(chunk); err != nil || lerr != "" {
		t.Fatalf("eval: lerr=%q err=%v", lerr, err)
	}
	out, _, _ := inst.Eval(`return tostring(out)`)
	return out
}

// M08.1: on/emit básico y payload.
func TestEventsBasico(t *testing.T) {
	out := evalOut(t, `
		out = ""
		nu.events.on("x:ping", function(p) out = "recibido:" .. p.v end)
		nu.events.emit("x:ping", { v = "hola" })`)
	if out != "recibido:hola" {
		t.Fatalf("got %q", out)
	}
}

// M08.2: once corre una sola vez.
func TestEventsOnce(t *testing.T) {
	out := evalOut(t, `
		local n = 0
		nu.events.once("x:e", function() n = n + 1 end)
		nu.events.emit("x:e"); nu.events.emit("x:e"); nu.events.emit("x:e")
		out = tostring(n)`)
	if out != "1" {
		t.Fatalf("once corrió %s veces, esperado 1", out)
	}
}

// M08.3: cancel surte efecto (un sub cancelado no corre).
func TestEventsCancel(t *testing.T) {
	out := evalOut(t, `
		local n = 0
		local sub = nu.events.on("x:e", function() n = n + 1 end)
		nu.events.emit("x:e")   -- corre: n=1
		sub.cancel()
		nu.events.emit("x:e")   -- no corre
		out = tostring(n)`)
	if out != "1" {
		t.Fatalf("got %q", out)
	}
}

// M08.4: G10 — cancelar un sub DURANTE el despacho surte efecto inmediato: si
// aún no le tocó, ya no corre.
func TestEventsCancelDuranteDespacho(t *testing.T) {
	out := evalOut(t, `
		local corrio_b = false
		local subB
		nu.events.on("x:e", function() if subB then subB.cancel() end end)  -- A cancela a B
		subB = nu.events.on("x:e", function() corrio_b = true end)          -- B
		nu.events.emit("x:e")
		out = tostring(corrio_b)`)
	if out != "false" {
		t.Fatalf("G10 cancel-inmediato: B no debía correr; got %q", out)
	}
}

// M08.5: G10 — un sub añadido DURANTE el despacho solo ve eventos futuros (no el
// que está en curso).
func TestEventsSubDuranteDespacho(t *testing.T) {
	out := evalOut(t, `
		local nuevo_corrio = false
		nu.events.on("x:e", function()
			nu.events.on("x:e", function() nuevo_corrio = true end)  -- se añade durante el despacho
		end)
		nu.events.emit("x:e")   -- el nuevo NO corre en este emit
		out = tostring(nuevo_corrio)`)
	if out != "false" {
		t.Fatalf("G10 sub-durante-despacho: el nuevo no debía correr; got %q", out)
	}
}

// M08.6: G10 — emits anidados se encolan por ANCHURA (no recursión). Un handler
// que emite deja su emit para después del actual: el orden es plano.
func TestEventsAnidadoAnchura(t *testing.T) {
	out := evalOut(t, `
		local traza = {}
		nu.events.on("x:a", function()
			traza[#traza+1] = "a-inicio"
			nu.events.emit("x:b")          -- se encola, NO corre aquí
			traza[#traza+1] = "a-fin"
		end)
		nu.events.on("x:b", function() traza[#traza+1] = "b" end)
		nu.events.emit("x:a")
		out = table.concat(traza, ",")`)
	// anchura: a-inicio, a-fin (el handler de a termina entero), luego b.
	if out != "a-inicio,a-fin,b" {
		t.Fatalf("G10 anchura: got %q (esperado a-inicio,a-fin,b)", out)
	}
}

// M08.7: un handler que lanza no rompe a los demás (cada uno bajo pcall).
func TestEventsHandlerAislado(t *testing.T) {
	out := evalOut(t, `
		local b_corrio = false
		nu.events.on("x:e", function() error("boom") end)
		nu.events.on("x:e", function() b_corrio = true end)
		nu.events.emit("x:e")
		out = tostring(b_corrio)`)
	if out != "true" {
		t.Fatalf("un handler que lanza no debe romper a los demás; got %q", out)
	}
}

// M08.8: nu.task.every — timer periódico que se para. Usa el driver del scheduler.
func TestEventsEvery(t *testing.T) {
	out := runScript(t, `
		out = "no"
		local n = 0
		local timer
		timer = nu.task.every(5, function()
			n = n + 1
			if n >= 3 then timer.stop(); out = "ticks:" .. tostring(n) end
		end)
		-- una task guardiana para que el bucle no acabe antes de los ticks
		nu.task.spawn(function() nu.task.sleep(200) end)`)
	if out != "ticks:3" {
		t.Fatalf("every: got %q", out)
	}
}
