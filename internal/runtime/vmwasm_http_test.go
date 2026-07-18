package runtime

// Tests de M13b: enu.http.request y enu.http.stream sobre wasm (§8). Petición/stream
// reales contra servidores httptest LOCALES (herméticos, sin red externa);
// status/headers/body y los trozos/eventos del stream cruzan a Lua. Las primitivas ⏸
// corren en una task y el driver (RunTasks) las lleva a término. El streaming
// incremental se logra con `flushServer` (stream_test.go, mismo paquete): el handler
// escribe trozos y los flushea para que el cliente los reciba según llegan.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

// wasmHTTPRun registra enu.http sobre una Instance, evalúa `setup` (que crea tasks) y
// conduce el bucle; devuelve la global `out`. El plazo acota un cuelgue accidental
// (un `next` de stream que nunca vuelve) a un fallo claro, no a un test colgado.
func wasmHTTPRun(t *testing.T, rt *Runtime, setup string) string {
	t.Helper()
	p, err := vmwasm.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	registerHTTPWasm(p, rt)
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if _, lerr, err := inst.Eval(setup); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := inst.RunTasks(ctx); err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	out, _, _ := inst.Eval(`return tostring(out)`)
	return out
}

// M13b.http.1: request real — status, body y headers cruzan.
func TestHTTPWasmRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "hola")
		w.WriteHeader(201)
		_, _ = fmt.Fprintf(w, "cuerpo:%s", r.Method)
	}))
	defer srv.Close()

	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local res = enu.http.request({ url = "`+srv.URL+`", method = "POST" })
			out = tostring(res.status) .. ":" .. res.body .. ":" .. tostring(res.headers["X-Test"])
		end)`)
	if out != "201:cuerpo:POST:hola" {
		t.Fatalf("http.request: got %q", out)
	}
}

// M13b.http.2: request con headers y body de subida.
func TestHTTPWasmRequestBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		_, _ = fmt.Fprintf(w, "recibi:%s auth:%s", string(body), r.Header.Get("Authorization"))
	}))
	defer srv.Close()

	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local res = enu.http.request({
				url = "`+srv.URL+`",
				method = "PUT",
				body = "datos",
				headers = { Authorization = "Bearer xyz" },
			})
			out = res.body
		end)`)
	if out != "recibi:datos auth:Bearer xyz" {
		t.Fatalf("http.request body/headers: got %q", out)
	}
}

// M13b.http.3: request sin url → EINVAL accionable.
func TestHTTPWasmRequestSinURL(t *testing.T) {
	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local ok, e = pcall(function() return enu.http.request({ method = "GET" }) end)
			out = tostring(ok) .. ":" .. tostring(e.code)
		end)`)
	if out != "false:EINVAL" {
		t.Fatalf("http.request sin url: got %q", out)
	}
}

// --- enu.http.stream (el handle Stream) ----------------------------------------

// M13b.http.stream.1: chunks() — los trozos crudos del body llegan según se emiten
// (flush) y se acumulan hasta el fin (nil). El equivalente wasm de TestStreamChunksRaw:
// la suma de los trozos crudos = el body entero.
func TestHTTPWasmStreamChunks(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		_, _ = io.WriteString(w, "abc")
		flush()
		_, _ = io.WriteString(w, "defg")
		flush()
	})
	defer srv.Close()

	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local st = enu.http.stream({ url = "`+srv.URL+`" })
			local acc, n = "", 0
			for c in st:chunks() do acc = acc .. c; n = n + 1 end
			st:close()
			out = acc .. ":" .. tostring(n > 0)
		end)`)
	if out != "abcdefg:true" {
		t.Fatalf("stream chunks: got %q", out)
	}
}

// M13b.http.stream.2: events() — un SSE con dos eventos {event,data,id} se itera
// correctamente y el status está disponible al recibir las cabeceras. El equivalente
// wasm de TestStreamEventsBasic (el parser SSE 🔒 se prueba aparte en sse_test.go).
func TestHTTPWasmStreamEvents(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		_, _ = io.WriteString(w, "event: ping\ndata: uno\nid: 1\n\n")
		flush()
		_, _ = io.WriteString(w, "data: dos\n\n")
		flush()
	})
	defer srv.Close()

	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local st = enu.http.stream({ url = "`+srv.URL+`" })
			local status = st.status
			local n, e1ev, e1data, e1id, e2data = 0, nil, nil, nil, nil
			for ev in st:events() do
				n = n + 1
				if n == 1 then e1ev, e1data, e1id = ev.event, ev.data, ev.id end
				if n == 2 then e2data = ev.data end
			end
			st:close()
			out = tostring(status) .. "|" .. tostring(n) .. "|" .. e1ev .. "|" .. e1data .. "|" .. e1id .. "|" .. e2data
		end)`)
	if out != "200|2|ping|uno|1|dos" {
		t.Fatalf("stream events: got %q", out)
	}
}

// M13b.http.stream.3: un evento SSE partido en VARIOS writes adversos (línea a
// medias, "\n\n" separado) se parsea como UN evento completo —el criterio de hecho
// 🔒—. El equivalente wasm de TestStreamEventsSplitAcrossChunks; aquí el bucle de
// next_event consume los trozos en la goroutine de fondo de un solo __hcall_s.
func TestHTTPWasmStreamEventsSplit(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		for _, part := range []string{"event: pi", "ng\ndata: ho", "la mun", "do\n", "\n"} {
			_, _ = io.WriteString(w, part)
			flush()
			time.Sleep(5 * time.Millisecond) // fuerza trozos de red separados
		}
	})
	defer srv.Close()

	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local st = enu.http.stream({ url = "`+srv.URL+`" })
			local n, ev_event, ev_data = 0, nil, nil
			for ev in st:events() do
				n = n + 1
				ev_event, ev_data = ev.event, ev.data
			end
			st:close()
			out = tostring(n) .. "|" .. ev_event .. "|" .. ev_data
		end)`)
	if out != "1|ping|hola mundo" {
		t.Fatalf("stream events partido: got %q", out)
	}
}

// M13b.http.stream.4: un status >= 400 NO lanza en stream (igual que request): se
// devuelve el Stream con su status como dato. El equivalente wasm de
// TestStreamStatus404NoThrow.
func TestHTTPWasmStreamStatus404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "nope")
	}))
	defer srv.Close()

	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local ok, st = pcall(function() return enu.http.stream({ url = "`+srv.URL+`" }) end)
			local status = nil
			if ok then status = st.status; st:close() end
			out = tostring(ok) .. ":" .. tostring(status)
		end)`)
	if out != "true:404" {
		t.Fatalf("stream status 404: got %q", out)
	}
}

// M13b.http.stream.5: Stream:close() es idempotente — llamarlo varias veces no lanza
// ni rompe (no libera el handle: el segundo close resuelve el mismo handle sin
// ECLOSED). El equivalente wasm de TestStreamCloseIdempotent.
func TestHTTPWasmStreamCloseIdempotent(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		_, _ = io.WriteString(w, "data: x\n\n")
		flush()
		time.Sleep(20 * time.Millisecond)
	})
	defer srv.Close()

	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local st = enu.http.stream({ url = "`+srv.URL+`" })
			st:close()
			st:close()
			st:close()
			out = "ok"
		end)`)
	if out != "ok" {
		t.Fatalf("stream close idempotente: got %q", out)
	}
}

// M13b.http.stream.6: el backpressure → EIO (🔒, §8). Un servidor que empuja MUCHO
// más que el tope del buffer (maxStreamBuffer, 8 MiB) mientras el consumidor NO lee
// (duerme) desemboca en EIO en el primer `next`. El equivalente wasm de
// TestStreamBackpressureEIO: la goroutine de fondo llena la cola por encima del tope
// y marca EIO, que el primer chunk ve.
func TestHTTPWasmStreamBackpressureEIO(t *testing.T) {
	srv := flushServer(t, func(w io.Writer, flush func()) {
		blob := make([]byte, 64<<10) // 64 KiB por trozo
		for i := range blob {
			blob[i] = 'x'
		}
		for i := 0; i < 200; i++ { // ~12 MiB total: muy por encima del tope (8 MiB)
			if _, err := w.Write(blob); err != nil {
				return // el cliente cerró: para de empujar
			}
			flush()
		}
	})
	defer srv.Close()

	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local st = enu.http.stream({ url = "`+srv.URL+`" })
			enu.task.sleep(300)   -- deja que el servidor desborde el buffer antes de leer
			local code = nil
			local ok, e = pcall(function()
				for c in st:chunks() do end
			end)
			if not ok then code = e.code end
			st:close()
			out = tostring(code)
		end)`)
	if out != "EIO" {
		t.Fatalf("stream backpressure EIO: got %q", out)
	}
}

// M13b.http.stream.7: el idle-timeout → ETIMEOUT (§8). Un body que recibe sus
// cabeceras y un primer evento, luego se queda MUDO más de idle_timeout_ms, hace que
// el siguiente `next` lance ETIMEOUT. El equivalente wasm de
// TestStreamIdleTimeoutETIMEOUT.
func TestHTTPWasmStreamIdleTimeoutETIMEOUT(t *testing.T) {
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

	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local st = enu.http.stream({ url = "`+srv.URL+`", idle_timeout_ms = 80 })
			local it = st:events()
			local ev = it()                       -- llega el primero
			local first = ev and ev.data or nil
			local code = nil
			local ok, e = pcall(function() return it() end)  -- body mudo → ETIMEOUT
			if not ok then code = e.code end
			st:close()
			out = tostring(first) .. ":" .. tostring(code)
		end)`)
	if out != "primero:ETIMEOUT" {
		t.Fatalf("stream idle timeout: got %q", out)
	}
}

// M13b.http.stream.8: un idle_timeout_ms no positivo o de tipo equivocado es uso
// inválido (EINVAL), validado antes de suspender. El equivalente wasm de
// TestStreamBadIdleTimeoutEINVAL.
func TestHTTPWasmStreamBadIdleTimeoutEINVAL(t *testing.T) {
	rt := &Runtime{http: newHTTPState("", "")}
	t.Cleanup(func() { rt.http.close() })
	out := wasmHTTPRun(t, rt, `
		enu.task.spawn(function()
			local _, e1 = pcall(function() enu.http.stream({ url = "http://x", idle_timeout_ms = -1 }) end)
			local _, e2 = pcall(function() enu.http.stream({ url = "http://x", idle_timeout_ms = "diez" }) end)
			out = e1.code .. ":" .. e2.code
		end)`)
	if out != "EINVAL:EINVAL" {
		t.Fatalf("stream idle_timeout_ms inválido: got %q", out)
	}
}
