package runtime

// Tests de M13b: enu.ws sobre wasm (§11). Paridad con ws_test.go: eco send/recv
// round-trip, recv→nil al cerrar el servidor, send-tras-close→ECLOSED, connect a
// un puerto cerrado→ENET, close idempotente y validación de opts→EINVAL. Todo
// contra servidores LOCALES (net/http/httptest + el Accept de coder/websocket):
// herméticos, sin red externa. connect/send/recv son ⏸: corren dentro de una task
// y el driver (RunTasks) las lleva a término (`wsEchoServer` se reusa de ws_test.go).

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dbareagimeno/enu/internal/vmwasm"
)

// wasmWsRun registra enu.ws sobre una Instance, evalúa `setup` (que crea tasks) y
// conduce el bucle; devuelve la global `out`. Un plazo acota un cuelgue accidental
// (un recv que nunca vuelve) a un fallo claro, no a un test colgado.
func wasmWsRun(t *testing.T, rt *Runtime, setup string) string {
	t.Helper()
	p, err := vmwasm.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	registerWsWasm(p, rt)
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

// M13b.ws.1: eco send/recv — VARIOS mensajes cruzan y vuelven en orden (criterio
// de hecho de S21), luego close. El equivalente wasm de TestWsEchoRoundTrip.
func TestWsWasmEchoRoundTrip(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	out := wasmWsRun(t, &Runtime{}, `
		enu.task.spawn(function()
			local w = enu.ws.connect("`+srv.URL+`")
			w:send("hola")
			local r1 = w:recv()
			w:send("mundo")
			local r2 = w:recv()
			w:send("tres")
			local r3 = w:recv()
			w:close()
			out = r1 .. ":" .. r2 .. ":" .. r3
		end)`)
	if out != "hola:mundo:tres" {
		t.Fatalf("eco round-trip: got %q", out)
	}
}

// M13b.ws.2: recv() devuelve nil cuando el SERVIDOR cierra ordenadamente, y un
// recv posterior sigue dando nil (no cuelga, no lanza). El equivalente wasm de
// TestWsRecvNilAfterServerClose.
func TestWsWasmRecvNilAfterServerClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte("ultimo"))
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	out := wasmWsRun(t, &Runtime{}, `
		enu.task.spawn(function()
			local w = enu.ws.connect("`+srv.URL+`")
			local first = w:recv()   -- "ultimo"
			local second = w:recv()  -- nil: el servidor cerró
			local third = w:recv()   -- sigue nil (idempotente)
			w:close()
			out = first .. ":" .. tostring(second) .. ":" .. tostring(third)
		end)`)
	if out != "ultimo:nil:nil" {
		t.Fatalf("recv nil tras cierre del servidor: got %q", out)
	}
}

// M13b.ws.3: enviar tras Ws:close() lanza ECLOSED (§1.4). El equivalente wasm de
// TestWsSendAfterCloseECLOSED.
func TestWsWasmSendAfterCloseECLOSED(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	out := wasmWsRun(t, &Runtime{}, `
		enu.task.spawn(function()
			local w = enu.ws.connect("`+srv.URL+`")
			w:close()
			local ok, e = pcall(function() w:send("tarde") end)
			out = tostring(ok) .. ":" .. tostring(e.code)
		end)`)
	if out != "false:ECLOSED" {
		t.Fatalf("send tras close: got %q", out)
	}
}

// M13b.ws.4: conectar a un puerto cerrado lanza ENET (fallo de transporte). El
// equivalente wasm de TestWsConnectRefusedENET.
func TestWsWasmConnectRefusedENET(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo reservar puerto: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nada escucha ahí

	out := wasmWsRun(t, &Runtime{}, `
		enu.task.spawn(function()
			local ok, e = pcall(function() enu.ws.connect("ws://`+addr+`", { timeout_ms = 2000 }) end)
			out = tostring(ok) .. ":" .. tostring(e.code)
		end)`)
	if out != "false:ENET" {
		t.Fatalf("connect a puerto cerrado: got %q", out)
	}
}

// M13b.ws.5: Ws:close() es idempotente — llamarlo varias veces no lanza ni rompe.
// El equivalente wasm de TestWsCloseIdempotent (además prueba que NO se libera el
// handle en close: el segundo close resuelve el mismo handle sin ECLOSED).
func TestWsWasmCloseIdempotent(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	out := wasmWsRun(t, &Runtime{}, `
		enu.task.spawn(function()
			local w = enu.ws.connect("`+srv.URL+`")
			w:close()
			w:close()
			w:close()
			out = "ok"
		end)`)
	if out != "ok" {
		t.Fatalf("close idempotente: got %q", out)
	}
}

// M13b.ws.7 (G52/A-38): sobre wasm, `send` con `opts.binary` emite un frame binario
// (bytes no-UTF-8 intactos) y `recv` distingue el tipo del frame entrante en su
// segundo valor. El servidor de eco re-emite cada frame con el MISMO tipo, así el
// cliente ve en recv el tipo que envió. Paridad wasm de TestWsSendBinaryFrameType_G52
// y TestWsRecvFrameType_G52 (que capturan el tipo del lado del servidor).
func TestWsWasmBinaryFrames_G52(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	out := wasmWsRun(t, &Runtime{}, `
		enu.task.spawn(function()
			local w = enu.ws.connect("`+srv.URL+`")
			w:send("\255\254\0", { binary = true })  -- frame binario
			local d1, b1 = w:recv()                    -- eco binario: (data, true)
			w:send("texto")                            -- frame de texto (default)
			local d2, b2 = w:recv()                    -- eco de texto: (data, false)
			w:close()
			local binOk = (#d1 == 3 and string.byte(d1,1) == 255 and string.byte(d1,3) == 0)
			out = tostring(b1) .. ":" .. tostring(binOk) .. ":" .. tostring(b2) .. ":" .. d2
		end)`)
	if out != "true:true:false:texto" {
		t.Fatalf("frames binarios wasm: got %q, want %q", out, "true:true:false:texto")
	}
}

// M13b.ws.6: validación de url/opts → EINVAL accionable (url vacía, opts no-tabla,
// headers mal tipados, timeout no positivo). El equivalente wasm de
// TestWsBadOptsEINVAL. La validación corre en parseWsOptsWasm, antes de dialear.
func TestWsWasmBadOptsEINVAL(t *testing.T) {
	out := wasmWsRun(t, &Runtime{}, `
		enu.task.spawn(function()
			local _, e1 = pcall(function() enu.ws.connect("") end)
			local _, e2 = pcall(function() enu.ws.connect("ws://x", 7) end)
			local _, e3 = pcall(function() enu.ws.connect("ws://x", { headers = { [1] = "v" } }) end)
			local _, e4 = pcall(function() enu.ws.connect("ws://x", { timeout_ms = -5 }) end)
			out = e1.code .. ":" .. e2.code .. ":" .. e3.code .. ":" .. e4.code
		end)`)
	if out != "EINVAL:EINVAL:EINVAL:EINVAL" {
		t.Fatalf("validación de opts: got %q", out)
	}
}
