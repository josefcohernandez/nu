package runtime

import (
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Tests de `enu.ws.connect` (S21, api.md §8). La sesión NO está en el inventario 🔒
// (es un wrapper sobre `coder/websocket` + el puente ⏸ de S04), pero tiene lógica
// propia que sí merece blindaje: el modelo `recv → nil al cerrar` (distinguir un
// cierre ordenado de un fallo de transporte), el cierre idempotente integrado con
// `cleanup`, y el mapeo de errores (`ENET`/`ETIMEOUT`/`ECLOSED`/`EINVAL`). Todo se
// prueba contra servidores **locales** (`net/http/httptest` + el `Accept` de la
// librería): herméticos, sin red externa, no flaky.

// wsEchoServer crea un servidor que acepta una conexión websocket y **devuelve cada
// mensaje** que recibe (eco), hasta que el cliente cierre o el contexto se cancele.
// Es el servidor del criterio de hecho de S21 ("eco websocket: send y recv
// round-trip"). Devuelve el server (a cerrar por el llamante).
func wsEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return // el cliente cerró (o se cortó la conexión): fin del eco
			}
			if err := c.Write(ctx, typ, data); err != nil {
				return
			}
		}
	}))
}

// TestWsEchoRoundTrip blinda el criterio de hecho central (S21): `send` + `recv`
// hacen round-trip contra un servidor de eco, y VARIOS mensajes vuelven en orden.
func TestWsEchoRoundTrip(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		r1, r2, r3, done = nil, nil, nil, false
		enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			w:send("hola")
			r1 = w:recv()
			w:send("mundo")
			r2 = w:recv()
			w:send("tres")
			r3 = w:recv()
			w:close()
			done = true
		end)
	`)
	h.expectEval(`return r1`, "hola")
	h.expectEval(`return r2`, "mundo")
	h.expectEval(`return r3`, "tres")
	h.expectEval(`return tostring(done)`, "true")
}

// TestWsRecvNilAfterServerClose blinda que `recv()` devuelve `nil` cuando el SERVIDOR
// cierra la conexión ordenadamente (no cuelga, no lanza error espurio): el bucle
// `while msg do ... end` termina limpio. El servidor manda un mensaje y luego cierra
// con un código normal.
func TestWsRecvNilAfterServerClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte("ultimo"))
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		first, second, count = nil, "SENTINEL", 0
		enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			first = w:recv()       -- "ultimo"
			second = w:recv()      -- nil: el servidor cerró
			-- un recv más tras el cierre sigue dando nil (idempotente), no cuelga
			local third = w:recv()
			if third == nil then count = 1 end
			w:close()
		end)
	`)
	h.expectEval(`return first`, "ultimo")
	h.expectEval(`return tostring(second)`, "nil")
	h.expectEval(`return tostring(count)`, "1")
}

// TestWsRecvNilAfterLocalClose blinda que tras `Ws:close()` un `recv()` devuelve
// `nil` (la conexión se cerró a propósito: fin de stream, no error). Es el otro
// lado del criterio "recv tras cierre da nil".
func TestWsRecvNilAfterLocalClose(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		afterClose = "SENTINEL"
		enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			w:send("ping")
			local _ = w:recv()
			w:close()
			afterClose = w:recv()  -- tras cerrar nosotros: nil
		end)
	`)
	h.expectEval(`return tostring(afterClose)`, "nil")
}

// TestWsSendAfterCloseECLOSED blinda que enviar tras `Ws:close()` lanza `ECLOSED`
// (el handle está cerrado, §1.4).
func TestWsSendAfterCloseECLOSED(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		code = nil
		enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			w:close()
			local ok, e = pcall(function() w:send("tarde") end)
			if not ok then code = e.code end
		end)
	`)
	h.expectEval(`return code`, "ECLOSED")
}

// TestWsConnectRefusedENET blinda que conectar a un puerto cerrado lanza `ENET`
// (fallo de transporte). Se toma un puerto libre y se cierra antes de conectar.
func TestWsConnectRefusedENET(t *testing.T) {
	// Reserva un puerto y ciérralo: nada escucha ahí.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo reservar puerto: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	h := newHarness(t)
	h.regStringFn("DEADURL", "ws://"+addr)
	h.eval(`
		code = nil
		enu.task.spawn(function()
			local ok, e = pcall(function() enu.ws.connect(DEADURL(), { timeout_ms = 2000 }) end)
			if not ok then code = e.code end
		end)
	`)
	h.expectEval(`return code`, "ENET")
}

// TestWsConnectTimeoutETIMEOUT blinda que un handshake que no completa antes de
// `timeout_ms` lanza `ETIMEOUT`. El servidor acepta la conexión TCP pero NO responde
// al handshake websocket (se queda colgado leyendo), así que el cliente expira.
func TestWsConnectTimeoutETIMEOUT(t *testing.T) {
	// Un servidor TCP crudo que acepta y no contesta nada (el handshake HTTP nunca
	// recibe respuesta). Mantiene la conexión abierta hasta que el test acabe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo escuchar: %v", err)
	}
	defer func() { _ = ln.Close() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				<-done
				_ = conn.Close()
			}()
		}
	}()

	h := newHarness(t)
	h.regStringFn("SLOWURL", "ws://"+ln.Addr().String())
	h.eval(`
		code = nil
		enu.task.spawn(function()
			local ok, e = pcall(function() enu.ws.connect(SLOWURL(), { timeout_ms = 80 }) end)
			if not ok then code = e.code end
		end)
	`)
	h.expectEval(`return code`, "ETIMEOUT")
}

// TestWsCloseIdempotent blinda que `Ws:close()` es idempotente: llamarlo varias veces
// no lanza ni rompe.
func TestWsCloseIdempotent(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		ok = false
		enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			w:close()
			w:close()
			w:close()
			ok = true
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")
}

// TestWsClosedByCleanupOnCancel blinda la integración con `enu.task.cleanup` (§6):
// una task que abre un websocket y registra `cleanup(function() w:close() end)` lo
// libera al ser CANCELADA, sin fuga de goroutines. La task se bloquea en `recv()`
// (el servidor de eco no manda nada espontáneo), se cancela desde fuera, y el
// `cleanup` cierra la conexión —desbloqueando el `Read` colgado—.
func TestWsClosedByCleanupOnCancel(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	before := runtime.NumGoroutine()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		ready, T = false, nil
		T = enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			enu.task.cleanup(function() w:close() end)
			ready = true
			-- recv se bloquea indefinidamente: el servidor de eco no manda nada por su
			-- cuenta. Solo la cancelación de la task (-> cleanup -> close) lo desbloquea.
			local _ = w:recv()
		end)
		enu.task.spawn(function()
			while not ready do enu.task.sleep(5) end
			enu.task.sleep(20) -- deja que recv() se bloquee de verdad
			T:cancel()
		end)
	`)
	h.expectEval(`return tostring(ready)`, "true")

	// La cancelación disparó el cleanup -> close, que cerró la conexión y canceló el
	// contexto: la goroutine de fondo del `recv` debe haberse ido. Se espera a la
	// condición (anti-flaky), no a un sleep fijo.
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before+3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - before; leaked > 3 {
		t.Fatalf("posible fuga de goroutines tras cerrar el ws por cleanup: +%d", leaked)
	}
}

// TestWsOutsideTaskEINVAL blinda que `connect`/`send`/`recv`, por ser ⏸, fuera de
// una task lanzan `EINVAL` (no pueden suspender sin una task, §1.3).
func TestWsOutsideTaskEINVAL(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`enu.ws.connect("ws://x")`)
	if se.Code != CodeEINVAL {
		t.Fatalf("connect fuera de task: got %q, want EINVAL", se.Code)
	}
}

// --- G52 / A-38: frames binarios (opts.binary en send, segundo retorno en recv) ---

// wsCaptureServer acepta una conexión y vuelca CADA frame que recibe (tipo + bytes,
// copiados) por un canal, hasta que el cliente cierre. Deja observar, del lado del
// servidor, el TIPO de frame que `Ws:send` emitió (G52/A-38): un test que solo mire
// el eco no distinguiría un `MessageText` de un `MessageBinary` con los mismos bytes.
type wsFrame struct {
	typ  websocket.MessageType
	data []byte
}

func wsCaptureServer(t *testing.T, frames chan<- wsFrame) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return // el cliente cerró o se cortó: fin de la captura
			}
			frames <- wsFrame{typ, append([]byte(nil), data...)}
		}
	}))
}

// TestWsSendBinaryFrameType_G52 blinda (G52/A-38) que `Ws:send(data, { binary = true })`
// emite un frame **binario** con los bytes intactos (incluidos no-UTF-8, que un frame
// de texto conforme rechazaría con 1007), y que `Ws:send(data)` sin opts sigue siendo
// un frame de **texto** — el default histórico, compatible con todo llamante.
func TestWsSendBinaryFrameType_G52(t *testing.T) {
	frames := make(chan wsFrame, 2)
	srv := wsCaptureServer(t, frames)
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	// \255\254\0 (decimal, portable a cualquier Lua) NO es UTF-8 válido: solo viaja
	// intacto en un frame binario.
	h.eval(`
		done = false
		enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			w:send("\255\254\0", { binary = true })  -- frame binario
			w:send("texto")                            -- frame de texto (default, sin opts)
			done = true
			w:close()
		end)
	`)
	h.expectEval(`return tostring(done)`, "true")

	f1 := <-frames
	if f1.typ != websocket.MessageBinary {
		t.Fatalf("primer frame: got tipo %v, want MessageBinary", f1.typ)
	}
	if want := []byte{0xff, 0xfe, 0x00}; string(f1.data) != string(want) {
		t.Fatalf("primer frame: bytes alterados: got %x want %x", f1.data, want)
	}
	f2 := <-frames
	if f2.typ != websocket.MessageText {
		t.Fatalf("segundo frame (sin opts): got tipo %v, want MessageText", f2.typ)
	}
	if string(f2.data) != "texto" {
		t.Fatalf("segundo frame: got %q", f2.data)
	}
}

// TestWsRecvFrameType_G52 blinda (G52/A-38) que `Ws:recv` distingue el tipo del frame
// entrante en su **segundo valor**: un frame binario → `(data, true)`, uno de texto →
// `(data, false)`. Los bytes no-UTF-8 del frame binario llegan intactos.
func TestWsRecvFrameType_G52(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		_ = c.Write(ctx, websocket.MessageBinary, []byte{0x00, 0xff, 0x01}) // binario
		_ = c.Write(ctx, websocket.MessageText, []byte("hola"))             // texto
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		binOk, b1, txt, b2 = false, nil, nil, nil
		enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			local d1
			d1, b1 = w:recv()  -- frame binario: (data, true)
			binOk = (#d1 == 3 and string.byte(d1,1) == 0 and string.byte(d1,2) == 255 and string.byte(d1,3) == 1)
			txt, b2 = w:recv() -- frame de texto: (data, false)
			w:close()
		end)
	`)
	h.expectEval(`return tostring(binOk)`, "true") // bytes binarios intactos
	h.expectEval(`return tostring(b1)`, "true")    // segundo valor: binario
	h.expectEval(`return txt`, "hola")
	h.expectEval(`return tostring(b2)`, "false") // segundo valor: texto
}

// TestWsRecvBinaryNilOnClose_G52 blinda (G52/A-38) que al cierre `recv` sigue dando
// `nil` en el primer valor y **nil también en el segundo** (no `false`): el nuevo
// retorno no altera la semántica de fin de stream.
func TestWsRecvBinaryNilOnClose_G52(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte("uno"))
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		closeData, closeBin = "S", "S"
		enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			local _ = w:recv()                 -- "uno"
			closeData, closeBin = w:recv()     -- cierre: nil, nil
			w:close()
		end)
	`)
	h.expectEval(`return tostring(closeData)`, "nil")
	h.expectEval(`return tostring(closeBin)`, "nil")
}

// TestWsSendBinaryAfterCloseECLOSED_G52 blinda (G52/A-38) que enviar un frame binario
// tras `Ws:close()` sigue lanzando `ECLOSED`: `opts.binary` no cambia la semántica de
// cierre/errores del método.
func TestWsSendBinaryAfterCloseECLOSED_G52(t *testing.T) {
	srv := wsEchoServer(t)
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		code = nil
		enu.task.spawn(function()
			local w = enu.ws.connect(URL())
			w:close()
			local ok, e = pcall(function() w:send("tarde", { binary = true }) end)
			if not ok then code = e.code end
		end)
	`)
	h.expectEval(`return code`, "ECLOSED")
}

// TestWsBadOptsEINVAL blinda la validación de `url`/`opts` antes de suspender:
// url vacía, opts no-tabla, headers mal tipados y timeout no positivo → `EINVAL`.
func TestWsBadOptsEINVAL(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		c1, c2, c3, c4 = nil, nil, nil, nil
		enu.task.spawn(function()
			local _, e1 = pcall(function() enu.ws.connect("") end)
			c1 = e1.code
			local _, e2 = pcall(function() enu.ws.connect("ws://x", 7) end)
			c2 = e2.code
			local _, e3 = pcall(function() enu.ws.connect("ws://x", { headers = { [1] = "v" } }) end)
			c3 = e3.code
			local _, e4 = pcall(function() enu.ws.connect("ws://x", { timeout_ms = -5 }) end)
			c4 = e4.code
		end)
	`)
	h.expectEval(`return c1`, "EINVAL")
	h.expectEval(`return c2`, "EINVAL")
	h.expectEval(`return c3`, "EINVAL")
	h.expectEval(`return c4`, "EINVAL")
}
