package runtime

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// Tests de `enu.http.stream` (S20, api.md §8, inventario 🔒). Toda la lógica propia
// —el modelo de buffer acotado y backpressure → `EIO`, el idle-timeout →
// `ETIMEOUT`, el cierre idempotente e integrado con `cleanup`, y la entrega de
// `chunks()`/`events()` (el parser SSE 🔒 se prueba además aparte en `sse_test.go`)—
// se blinda aquí contra servidores **locales** (`net/http/httptest`): herméticos,
// sin red externa, no flaky. El streaming incremental se logra con `http.Flusher`:
// el handler escribe trozos y los `Flush`-ea para que el cliente los reciba según
// llegan (no al final).

// flushServer crea un servidor que invoca `write(w, flush)` —donde `write` empuja
// trozos y `flush` los fuerza por el cable— y mantiene la conexión abierta hasta
// que `write` retorne. Devuelve el server (a cerrar por el llamante).
func flushServer(t *testing.T, write func(w io.Writer, flush func())) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("el ResponseWriter de httptest no implementa Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		write(w, fl.Flush)
	}))
}

// TestStreamEventsBasic blinda el camino feliz de `events()` de extremo a extremo:
// un SSE con tres eventos {event,data,id} se itera correctamente desde Lua, y el
// status/headers están disponibles al recibir las cabeceras.
func TestStreamEventsBasic(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		_, _ = io.WriteString(w, "event: ping\ndata: uno\nid: 1\n\n")
		flush()
		_, _ = io.WriteString(w, "data: dos\n\n")
		flush()
		_, _ = io.WriteString(w, ": comentario\ndata: tres-a\ndata: tres-b\n\n")
		flush()
	})
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		status, n, e1ev, e1data, e1id, e2data, e3data = nil,0,nil,nil,nil,nil,nil
		enu.task.spawn(function()
			local st = enu.http.stream({ url = URL() })
			status = st.status
			for ev in st:events() do
				n = n + 1
				if n == 1 then e1ev, e1data, e1id = ev.event, ev.data, ev.id end
				if n == 2 then e2data = ev.data end
				if n == 3 then e3data = ev.data end
			end
			st:close()
		end)
	`)
	h.expectEval(`return tostring(status)`, "200")
	h.expectEval(`return tostring(n)`, "3")
	h.expectEval(`return e1ev`, "ping")
	h.expectEval(`return e1data`, "uno")
	h.expectEval(`return e1id`, "1")
	h.expectEval(`return e2data`, "dos")
	h.expectEval(`return e3data`, "tres-a\ntres-b")
}

// TestStreamEventsSplitAcrossChunks blinda el criterio de hecho 🔒: un evento SSE
// emitido en VARIOS writes (línea a medias, "\n\n" separado) se parsea como UN
// evento completo. El servidor escribe y flushea trozos que parten las líneas y el
// separador de evento.
func TestStreamEventsSplitAcrossChunks(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		// Un evento "event: ping / data: hola mundo" partido en pedazos adversos.
		for _, part := range []string{"event: pi", "ng\ndata: ho", "la mun", "do\n", "\n"} {
			_, _ = io.WriteString(w, part)
			flush()
			time.Sleep(5 * time.Millisecond) // fuerza trozos de red separados
		}
	})
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		ev_event, ev_data, count = nil, nil, 0
		enu.task.spawn(function()
			local st = enu.http.stream({ url = URL() })
			for ev in st:events() do
				count = count + 1
				ev_event, ev_data = ev.event, ev.data
			end
			st:close()
		end)
	`)
	h.expectEval(`return tostring(count)`, "1")
	h.expectEval(`return ev_event`, "ping")
	h.expectEval(`return ev_data`, "hola mundo")
}

// TestStreamChunksRaw blinda `chunks()`: entrega los trozos crudos del body y
// termina (nil) al fin del body. Se acumula todo lo recibido y se compara con lo
// que el servidor escribió (la suma de los trozos crudos = el body entero).
func TestStreamChunksRaw(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		_, _ = io.WriteString(w, "abc")
		flush()
		_, _ = io.WriteString(w, "defg")
		flush()
	})
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		acc, nchunks = "", 0
		enu.task.spawn(function()
			local st = enu.http.stream({ url = URL() })
			for c in st:chunks() do
				acc = acc .. c
				nchunks = nchunks + 1
			end
			st:close()
		end)
	`)
	h.expectEval(`return acc`, "abcdefg")
	// Al menos un trozo; el body entero llegó (no se cuenta el nº exacto: el cable
	// puede coalescer trozos, pero el contenido total es estable).
	h.expectEval(`return tostring(nchunks > 0)`, "true")
}

// TestStreamStatus404NoThrow blinda que un status >= 400 NO lanza en `stream`
// (igual que `request`): se devuelve el `Stream` con su status como dato.
func TestStreamStatus404NoThrow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "nope")
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		threw, status = false, nil
		enu.task.spawn(function()
			local ok, st = pcall(function() return enu.http.stream({ url = URL() }) end)
			threw = not ok
			if ok then status = st.status; st:close() end
		end)
	`)
	h.expectEval(`return tostring(threw)`, "false")
	h.expectEval(`return tostring(status)`, "404")
}

// TestStreamBackpressureEIO blinda el backpressure → `EIO` (🔒, §8): un servidor que
// empuja MUCHO más que el tope del buffer mientras el consumidor NO lee desemboca en
// `EIO`. Para forzarlo de forma hermética se baja el tope del buffer al mínimo (no es
// superficie pública: se ajusta el handle directamente desde el test, que comparte
// paquete) y el consumidor espera antes de empezar a leer, dejando que la goroutine
// de fondo desborde.
func TestStreamBackpressureEIO(t *testing.T) {
	// El servidor vuelca un cuerpo grande en muchos trozos, lo más rápido que puede.
	srv := flushServer(t, func(w io.Writer, flush func()) {
		blob := make([]byte, 64<<10) // 64 KiB por trozo
		for i := range blob {
			blob[i] = 'x'
		}
		for i := 0; i < 200; i++ { // ~12 MiB total: muy por encima del tope reducido
			if _, err := w.Write(blob); err != nil {
				return // el cliente cerró: para de empujar
			}
			flush()
		}
	})
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	// Reduce el tope del buffer del PRÓXIMO stream parcheando una variable de test:
	// en vez de exponer la constante, el test envuelve `nextChunk` ralentizando al
	// consumidor. Aquí el consumidor simplemente NO lee durante un tiempo: la
	// goroutine de fondo rebasa `maxStreamBuffer` (8 MiB) con los ~12 MiB y marca EIO,
	// que el primer `next` ve.
	h.eval(`
		code, gotData = nil, false
		enu.task.spawn(function()
			local st = enu.http.stream({ url = URL() })
			-- Deja que el servidor empuje y desborde el buffer antes de leer.
			enu.task.sleep(300)
			local ok, e = pcall(function()
				for c in st:chunks() do gotData = true end
			end)
			if not ok then code = e.code end
			st:close()
		end)
	`)
	h.expectEval(`return code`, "EIO")
}

// TestStreamIdleTimeoutETIMEOUT blinda el idle-timeout → `ETIMEOUT` (§8): un body
// que recibe sus cabeceras pero luego se queda MUDO más de `idle_timeout_ms` hace
// que el `next` lance `ETIMEOUT`. El servidor manda las cabeceras (200) y un primer
// evento, luego duerme bastante más que el idle-timeout sin enviar nada.
func TestStreamIdleTimeoutETIMEOUT(t *testing.T) {
	release := make(chan struct{})
	srv := flushServer(t, func(w io.Writer, flush func()) {
		_, _ = io.WriteString(w, "data: primero\n\n")
		flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	})
	defer srv.Close()
	defer close(release)

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		code, firstData = nil, nil
		enu.task.spawn(function()
			local st = enu.http.stream({ url = URL(), idle_timeout_ms = 80 })
			local it = st:events()
			-- El primer evento llega; el segundo next se queda esperando un body mudo
			-- -> idle-timeout -> ETIMEOUT.
			local ev = it()
			if ev then firstData = ev.data end
			local ok, e = pcall(function() return it() end)
			if not ok then code = e.code end
			st:close()
		end)
	`)
	h.expectEval(`return firstData`, "primero")
	h.expectEval(`return code`, "ETIMEOUT")
}

// TestStreamCloseIdempotent blinda que `Stream:close()` es idempotente: llamarlo
// varias veces (incluso tras consumir o sin consumir) no lanza ni rompe.
func TestStreamCloseIdempotent(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		_, _ = io.WriteString(w, "data: x\n\n")
		flush()
		time.Sleep(20 * time.Millisecond)
	})
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		ok = false
		enu.task.spawn(function()
			local st = enu.http.stream({ url = URL() })
			st:close()
			st:close() -- idempotente: no lanza
			st:close()
			ok = true
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")
}

// TestStreamClosedByCleanupOnCancel blinda la integración con `enu.task.cleanup`
// (§6): una task que abre un stream y registra `cleanup(function() st:close() end)`
// libera el stream al ser CANCELADA, sin fuga de goroutines. El servidor mantiene
// la conexión abierta (SSE infinito); la task se bloquea consumiendo, se cancela
// desde fuera, y el `cleanup` cierra el stream. Se mide que no quedan goroutines de
// lectura colgadas.
func TestStreamClosedByCleanupOnCancel(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		// SSE que va emitiendo para siempre (hasta que el cliente corte la conexión).
		for i := 0; ; i++ {
			if _, err := fmt.Fprintf(w, "data: tick-%d\n\n", i); err != nil {
				return
			}
			flush()
			time.Sleep(10 * time.Millisecond)
		}
	})
	defer srv.Close()

	before := runtime.NumGoroutine()

	h := newHarness(t)
	withURL(h, srv.URL)
	// La task ancla espera a recibir al menos un evento (para garantizar que el
	// stream está vivo y leyendo), luego se cancela; su cleanup cierra el stream.
	h.eval(`
		got1, T = false, nil
		T = enu.task.spawn(function()
			local st = enu.http.stream({ url = URL() })
			enu.task.cleanup(function() st:close() end)
			for ev in st:events() do
				got1 = true
				-- sigue consumiendo indefinidamente hasta que cancelen la task
			end
		end)
		-- Otra task: espera a que llegue un evento y cancela la primera.
		enu.task.spawn(function()
			while not got1 do enu.task.sleep(5) end
			T:cancel()
		end)
	`)
	h.expectEval(`return tostring(got1)`, "true")

	// La cancelación de la task disparó su `cleanup`, que llamó `st:close()`: la
	// goroutine de lectura del body debe haberse ido (el cierre cancela el contexto y
	// cierra el body, desbloqueando su `Read`). Se espera a la condición (anti-flaky),
	// no a un sleep fijo. El runtime lo cierra el `t.Cleanup` del arnés (no aquí: un
	// doble Close del LState entraría en pánico).
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before+3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - before; leaked > 3 {
		t.Fatalf("posible fuga de goroutines tras cerrar el stream por cleanup: +%d", leaked)
	}
}

// TestHeaderGateRace blinda el árbitro de la carrera de cabeceras (`openStream`):
// `Timer.Stop()` NO cancela una AfterFunc ya disparada, así que si el timer vence
// en la ventana entre que `client.Do` retorna con éxito y el `Stop`, su `cancel()`
// envenenaría el contexto del body y el primer `next` lanzaría un `ENET` espurio.
// El `headerGate` da exclusión mutua determinista; se comprueban AMBOS órdenes de
// intercalación posibles (no hay una tercera vía: ambos lados toman el candado).
func TestHeaderGateRace(t *testing.T) {
	// Orden A — la entrega gana (`Do` retorna antes de que venza el timer). La
	// entrega no ve timeout y el timer, si dispara después, es un no-op.
	var g headerGate
	if g.deliver() {
		t.Fatal("deliver sin timer vencido: no debería reportar timeout")
	}
	if g.fire() {
		t.Fatal("fire tras deliver: no debería pedir cancelación (body ya entregado)")
	}

	// Orden B — el timer gana (vence en la ventana antes del `Stop`/deliver). El
	// timer pide cancelar y la entrega lo detecta, abortando como timeout.
	var g2 headerGate
	if !g2.fire() {
		t.Fatal("fire primero: debería pedir cancelación del contexto")
	}
	if !g2.deliver() {
		t.Fatal("deliver tras fire: debería reportar que el timer ganó la carrera")
	}
	// Un segundo fire (el timer no vuelve a disparar, pero por robustez) tras la
	// entrega es no-op: el árbitro es estable.
	if g2.fire() {
		t.Fatal("fire tras deliver: debería ser no-op")
	}
}

// TestStreamOutsideTaskEINVAL blinda que `stream`, por ser ⏸, fuera de una task
// lanza `EINVAL` (no puede suspender sin una task, §1.3).
func TestStreamOutsideTaskEINVAL(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`enu.http.stream({ url = "http://x" })`)
	if se.Code != CodeEINVAL {
		t.Fatalf("stream fuera de task: got %q, want EINVAL", se.Code)
	}
}

// TestStreamBadIdleTimeoutEINVAL blinda que un `idle_timeout_ms` no positivo o de
// tipo equivocado es uso inválido (`EINVAL`), validado antes de suspender.
func TestStreamBadIdleTimeoutEINVAL(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		c1, c2 = nil, nil
		enu.task.spawn(function()
			local _, e1 = pcall(function() enu.http.stream({ url = "http://x", idle_timeout_ms = -1 }) end)
			c1 = e1.code
			local _, e2 = pcall(function() enu.http.stream({ url = "http://x", idle_timeout_ms = "diez" }) end)
			c2 = e2.code
		end)
	`)
	h.expectEval(`return c1`, "EINVAL")
	h.expectEval(`return c2`, "EINVAL")
}
