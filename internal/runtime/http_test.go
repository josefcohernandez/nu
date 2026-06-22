package runtime

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// Tests de `nu.http.request` (S19, api.md §8). Toda la lógica propia de S19
// —**no lanzar por status** (el status es dato), mapeo de fallos de transporte a
// `ENET`/`ETIMEOUT`, validación de `opts` a `EINVAL`, y TLS por petición (G12)—
// se blinda aquí contra servidores **locales** (`net/http/httptest`): los tests
// son **herméticos**, sin red externa, así que no son flaky por DNS ni por un
// endpoint remoto caído. Cada caso corre el `request` desde una task (es ⏸) y se
// autovalida con `assert` desde Lua, devolviendo el dato a comprobar.

// withURL inyecta un global Lua `URL` que devuelve la URL del servidor de prueba,
// para que los snippets construyan la petición sin interpolar Go en el código Lua.
func withURL(h *harness, url string) {
	h.t.Helper()
	h.rt.L.SetGlobal("URL", h.rt.L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(url))
		return 1
	}))
}

// TestHTTPRequest200 blinda el camino feliz de extremo a extremo: un GET a un
// servidor que responde 200 devuelve el status, el body y los headers de respuesta
// correctos, y el servidor recibe los headers de petición que se le pasaron.
func TestHTTPRequest200(t *testing.T) {
	var gotMethod, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-Custom")
		w.Header().Set("X-Reply", "pong")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hola mundo")
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		status, body, reply = nil, nil, nil
		nu.task.spawn(function()
			local res = nu.http.request({
				url = URL(),
				headers = { ["X-Custom"] = "abc" },
			})
			status = res.status
			body = res.body
			reply = res.headers["X-Reply"]
		end)
	`)
	h.expectEval(`return tostring(status)`, "200")
	h.expectEval(`return body`, "hola mundo")
	h.expectEval(`return reply`, "pong")
	if gotMethod != http.MethodGet {
		t.Fatalf("método recibido: got %q, want GET", gotMethod)
	}
	if gotHeader != "abc" {
		t.Fatalf("header de petición recibido: got %q, want abc", gotHeader)
	}
}

// TestHTTPRequest404NoThrow blinda la semántica clave (§8): un 404 (o cualquier
// status >= 400) devuelve `{status=404,...}` SIN lanzar —el status es dato—.
func TestHTTPRequest404NoThrow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "no encontrado")
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		threw, status, body = false, nil, nil
		nu.task.spawn(function()
			local ok, res = pcall(function() return nu.http.request({ url = URL() }) end)
			threw = not ok
			if ok then status, body = res.status, res.body end
		end)
	`)
	h.expectEval(`return tostring(threw)`, "false") // NO lanza
	h.expectEval(`return tostring(status)`, "404")
	h.expectEval(`return body`, "no encontrado")
}

// TestHTTPRequest500NoThrow comprueba lo mismo para un 500: tampoco lanza.
func TestHTTPRequest500NoThrow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		threw, status = false, nil
		nu.task.spawn(function()
			local ok, res = pcall(function() return nu.http.request({ url = URL() }) end)
			threw = not ok
			if ok then status = res.status end
		end)
	`)
	h.expectEval(`return tostring(threw)`, "false")
	h.expectEval(`return tostring(status)`, "500")
}

// TestHTTPRequestPOSTBody blinda que un POST con body llega íntegro al servidor.
func TestHTTPRequestPOSTBody(t *testing.T) {
	var gotBody, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		status = nil
		nu.task.spawn(function()
			local res = nu.http.request({ url = URL(), method = "POST", body = "payload-123" })
			status = res.status
		end)
	`)
	h.expectEval(`return tostring(status)`, "201")
	if gotMethod != http.MethodPost {
		t.Fatalf("método: got %q, want POST", gotMethod)
	}
	if gotBody != "payload-123" {
		t.Fatalf("body recibido: got %q, want payload-123", gotBody)
	}
}

// TestHTTPRequestTransportENET blinda que un fallo de transporte (puerto cerrado:
// arrancamos un servidor, anotamos su URL y lo cerramos antes de la petición)
// lanza `ENET`, no `EIO` ni un status.
func TestHTTPRequestTransportENET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // el puerto queda cerrado: conexión rechazada

	h := newHarness(t)
	withURL(h, url)
	h.eval(`
		code = nil
		nu.task.spawn(function()
			local ok, e = pcall(function() nu.http.request({ url = URL(), timeout_ms = 2000 }) end)
			assert(ok == false, "un puerto cerrado debe lanzar")
			code = e.code
		end)
	`)
	h.expectEval(`return code`, "ENET")
}

// TestHTTPRequestTimeoutETIMEOUT blinda que un servidor que tarda más que
// `timeout_ms` hace lanzar `ETIMEOUT`. El servidor duerme bastante más que el
// plazo para que el corte sea por timeout y no por una carrera ajustada (anti-flaky).
func TestHTTPRequestTimeoutETIMEOUT(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release) // desbloquea el handler al terminar (no deja goroutines colgadas)

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		code = nil
		nu.task.spawn(function()
			local ok, e = pcall(function() nu.http.request({ url = URL(), timeout_ms = 50 }) end)
			assert(ok == false, "un server lento debe lanzar por timeout")
			code = e.code
		end)
	`)
	h.expectEval(`return code`, "ETIMEOUT")
}

// TestHTTPRequestMissingURLEINVAL blinda que una `opts` sin `url` (o con `url`
// vacía, o `opts` no-tabla) lanza `EINVAL` —y, por ser un error de uso, lo hace
// antes de suspender.
func TestHTTPRequestMissingURLEINVAL(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		codes = {}
		nu.task.spawn(function()
			local _, e1 = pcall(function() nu.http.request({}) end)
			codes[1] = e1.code
			local _, e2 = pcall(function() nu.http.request({ url = "" }) end)
			codes[2] = e2.code
			local _, e3 = pcall(function() nu.http.request("no-soy-tabla") end)
			codes[3] = e3.code
		end)
	`)
	h.expectEval(`return codes[1]`, "EINVAL")
	h.expectEval(`return codes[2]`, "EINVAL")
	h.expectEval(`return codes[3]`, "EINVAL")
}

// TestHTTPRequestBadTimeoutEINVAL blinda que un `timeout_ms` no positivo o de
// tipo equivocado es uso inválido (`EINVAL`).
func TestHTTPRequestBadTimeoutEINVAL(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		c1, c2 = nil, nil
		nu.task.spawn(function()
			local _, e1 = pcall(function() nu.http.request({ url = "http://x", timeout_ms = -1 }) end)
			c1 = e1.code
			local _, e2 = pcall(function() nu.http.request({ url = "http://x", timeout_ms = "diez" }) end)
			c2 = e2.code
		end)
	`)
	h.expectEval(`return c1`, "EINVAL")
	h.expectEval(`return c2`, "EINVAL")
}

// TestHTTPRequestOutsideTaskEINVAL blinda que `request`, por ser ⏸, llamada fuera
// de una task (sobre el chunk de `-e`, sin task) lanza `EINVAL` —no puede
// suspender sin una task (§1.3)—.
func TestHTTPRequestOutsideTaskEINVAL(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`nu.http.request({ url = "http://x" })`)
	if se.Code != CodeEINVAL {
		t.Fatalf("request fuera de task: got %q, want EINVAL", se.Code)
	}
}

// TestHTTPRequestTLSInsecure blinda el camino TLS (G12): contra un servidor TLS
// con certificado autofirmado, una petición normal falla por CA desconocida
// (mapeada a `ENET`, es un fallo de transporte/handshake) y `insecure=true` la
// hace pasar. Es el cliente **por-petición** (TLS a medida) del modelo de S19.
func TestHTTPRequestTLSInsecure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tls-ok")
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		failedDefault, okInsecure, body = nil, nil, nil
		nu.task.spawn(function()
			-- sin insecure: el cert autofirmado no lo confía nadie -> falla (transporte)
			local ok = pcall(function() nu.http.request({ url = URL(), timeout_ms = 3000 }) end)
			failedDefault = not ok
			-- con insecure: pasa
			local ok2, res = pcall(function()
				return nu.http.request({ url = URL(), timeout_ms = 3000, tls = { insecure = true } })
			end)
			okInsecure = ok2
			if ok2 then body = res.body end
		end)
	`)
	h.expectEval(`return tostring(failedDefault)`, "true")
	h.expectEval(`return tostring(okInsecure)`, "true")
	h.expectEval(`return body`, "tls-ok")
}

// TestHTTPRequestTLSCAFile blinda el otro camino TLS de G12: añadir la CA del
// servidor (su propio certificado, que httptest expone) como `ca_file` hace que la
// verificación pase **sin** `insecure`. Es la pieza de la CA corporativa.
func TestHTTPRequestTLSCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ca-ok")
	}))
	defer srv.Close()

	// httptest expone el certificado del servidor; lo escribimos como PEM a un
	// fichero temporal para usarlo de `ca_file` (la CA que firma = el propio cert
	// autofirmado del server de prueba).
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := srv.Certificate().Raw
	if err := os.WriteFile(caPath, certToPEM(pemBytes), 0o600); err != nil {
		t.Fatalf("no se pudo escribir la CA: %v", err)
	}

	h := newHarness(t)
	withURL(h, srv.URL)
	h.rt.L.SetGlobal("CA", h.rt.L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(caPath))
		return 1
	}))
	h.eval(`
		ok, body = nil, nil
		nu.task.spawn(function()
			local o, res = pcall(function()
				return nu.http.request({ url = URL(), timeout_ms = 3000, tls = { ca_file = CA() } })
			end)
			ok = o
			if o then body = res.body end
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")
	h.expectEval(`return body`, "ca-ok")
}

// TestHTTPRequestMultiValueHeaders blinda la decisión sobre headers de respuesta
// con valores múltiples (claude_decisions.md S19): se **unen por ", "**.
func TestHTTPRequestMultiValueHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Multi", "a")
		w.Header().Add("X-Multi", "b")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		multi = nil
		nu.task.spawn(function()
			local res = nu.http.request({ url = URL() })
			multi = res.headers["X-Multi"]
		end)
	`)
	h.expectEval(`return multi`, "a, b")
}

// TestHTTPRequestConcurrent blinda que varias peticiones concurrentes (varias
// tasks) progresan en paralelo por el puente ⏸ (el loop no se congela mientras una
// espera) y todas reciben su respuesta. Es también la red anti-data-race del
// cliente reutilizable bajo `-race`.
func TestHTTPRequestConcurrent(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL)
	h.eval(`
		done = 0
		for i = 1, 5 do
			nu.task.spawn(function()
				local res = nu.http.request({ url = URL() })
				assert(res.status == 200, "status 200")
				assert(res.body == "ok", "body ok")
				done = done + 1
			end)
		end
	`)
	h.expectEval(`return tostring(done)`, "5")
	if atomic.LoadInt64(&hits) != 5 {
		t.Fatalf("hits al servidor: got %d, want 5", hits)
	}
}

// certToPEM envuelve los bytes DER de un certificado en el bloque PEM
// `CERTIFICATE` que `ca_file` (y `AppendCertsFromPEM`) esperan.
func certToPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
