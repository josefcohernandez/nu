package runtime

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// CP-5 · "El camino de red, incluido streaming" (checkpoint de integración tras
// S21, **cierra la Fase 4 — Red**). Prueba de humo contra servidores **locales**
// de test (herméticos, sin red externa), que ejercita las cuatro capacidades de la
// fase juntas (plan, §"CP-5"):
//
//	(a) `enu.http.request` (S19) trata un 404 como **dato**, no lo lanza.
//	(b) un SSE consumido con `Stream:events()` (S20) **mientras otra task
//	    progresa** —el event loop NO se bloquea: una task contadora avanza a la par
//	    que otra consume el stream, demostrando que el puente ⏸ libera el token—.
//	(c) un `enu.ws` (S21) de **eco** hace round-trip.
//	(d) un consumidor lento que desborda el buffer del stream recibe `EIO`
//	    (backpressure real, reusa el modelo acotado de S20).
//
// Si CP-5 pasa, la Fase 4 queda cerrada (tablero) y el puntero avanza a S22.

// TestCP5CaminoDeRed ejercita (a)+(b)+(c) en un solo runtime, demostrando que las
// tres primitivas conviven y que el loop progresa mientras una task consume un SSE.
func TestCP5CaminoDeRed(t *testing.T) {
	// --- (a) servidor que responde 404 (status como dato) ---
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "no aqui")
	}))
	defer srv404.Close()

	// --- (b) servidor SSE que emite N eventos espaciados en el tiempo ---
	// El espaciado (10 ms entre eventos) garantiza que, mientras la task consumidora
	// espera el siguiente evento (suspendida, sin el token), la task contadora corre:
	// si el loop se bloqueara, el contador no avanzaría.
	sseSrv := flushServer(t, func(w io.Writer, flush func()) {
		for i := 0; i < 5; i++ {
			_, _ = io.WriteString(w, "data: ev"+itoa(i)+"\n\n")
			flush()
			time.Sleep(10 * time.Millisecond)
		}
	})
	defer sseSrv.Close()

	// --- (c) servidor websocket de eco ---
	wsSrv := wsEchoServer(t)
	defer wsSrv.Close()

	h := newHarness(t)
	setURLGlobal(h, "URL404", srv404.URL)
	setURLGlobal(h, "URLSSE", sseSrv.URL)
	setURLGlobal(h, "URLWS", wsSrv.URL)

	h.eval(`
		-- (a) 404 como dato: no lanza.
		status404, threw404 = nil, false
		enu.task.spawn(function()
			local ok, res = pcall(function() return enu.http.request({ url = URL404() }) end)
			threw404 = not ok
			if ok then status404 = res.status end
		end)

		-- (b) consumo de SSE + (demostración) otra task avanza en paralelo.
		sse_count, ticks, sse_done = 0, 0, false
		-- task contadora: corre mientras el SSE se consume; si el loop se bloqueara
		-- esperando el stream, 'ticks' se quedaría en 0.
		local counter = enu.task.spawn(function()
			while not sse_done do
				ticks = ticks + 1
				enu.task.sleep(5)
			end
		end)
		enu.task.spawn(function()
			local st = enu.http.stream({ url = URLSSE() })
			for ev in st:events() do
				sse_count = sse_count + 1
			end
			st:close()
			sse_done = true
		end)

		-- (c) ws de eco: round-trip.
		ws_reply = nil
		enu.task.spawn(function()
			local w = enu.ws.connect(URLWS())
			w:send("eco!")
			ws_reply = w:recv()
			w:close()
		end)
	`)

	// (a)
	h.expectEval(`return tostring(threw404)`, "false")
	h.expectEval(`return tostring(status404)`, "404")
	// (b) todos los eventos llegaron...
	h.expectEval(`return tostring(sse_count)`, "5")
	// ...y la task contadora avanzó MIENTRAS el SSE se consumía (loop no bloqueado).
	h.expectEval(`return tostring(ticks > 0)`, "true")
	// (c)
	h.expectEval(`return ws_reply`, "eco!")
}

// TestCP5BackpressureEIO ejercita (d): un consumidor lento que deja desbordar el
// buffer acotado del stream recibe `EIO` (backpressure real, S20). Es el mismo
// modelo que `TestStreamBackpressureEIO` pero vive aquí como parte del checkpoint
// de cierre de fase.
func TestCP5BackpressureEIO(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		blob := make([]byte, 64<<10) // 64 KiB por trozo
		for i := range blob {
			blob[i] = 'x'
		}
		for i := 0; i < 200; i++ { // ~12 MiB total, muy por encima del tope (8 MiB)
			if _, err := w.Write(blob); err != nil {
				return
			}
			flush()
		}
	})
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		code = nil
		enu.task.spawn(function()
			local st = enu.http.stream({ url = URL() })
			enu.task.sleep(300) -- deja que el servidor desborde el buffer antes de leer
			local ok, e = pcall(function()
				for c in st:chunks() do end
			end)
			if not ok then code = e.code end
			st:close()
		end)
	`)
	h.expectEval(`return code`, "EIO")
}

// setURLGlobal registra una "constante" Lua `name()` que devuelve `s` (mismo idioma
// que `withURL`/`URL()`, pero permite varias URLs en el mismo runtime).
func setURLGlobal(h *harness, name, s string) {
	h.t.Helper()
	// El valor cruza sin interpolar (SetStringGlobal); el accesor `name()` se define
	// con el nombre (identificador controlado del test).
	valName := "__" + name + "_val"
	h.rt.SetStringGlobal(valName, s)
	h.defWasmGlobal("function " + name + "() return " + valName + " end")
}
