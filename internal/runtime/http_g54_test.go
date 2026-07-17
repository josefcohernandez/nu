package runtime

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Tests del control de redirects de `enu.http` (G54, api.md §8, nivel de API 4).
// Blindan la resolución EXACTA del hallazgo contra servidores **locales**
// (`net/http/httptest`), herméticos y sin red externa:
//
//   - `opts.max_redirects` acota el presupuesto de saltos que el cliente sigue; el
//     default es 10 y `0` = no seguir ninguno (parseo → EINVAL si es negativo o
//     fraccionario).
//   - Agotado el presupuesto **no se lanza error**: la última respuesta `3xx` se
//     entrega **como dato** (status 3xx + `Location` en `headers`), coherente con
//     "el status es dato" de §8 —así el llamante que pone `0` observa el `302` a
//     mano y corta la amplificación de SSRF—.
//   - En cada salto **cross-host** (host —nombre y puerto efectivo— distinto, o
//     degradación de esquema `https`→`http`) se recortan TODAS las cabeceras que el
//     llamante puso en `opts.headers`, sin lista blanca, y el recorte **no se
//     restaura** aunque un salto posterior regrese al host inicial.
//
// Se cubren ambas superficies del contrato: `enu.http.request` (buffereada) y
// `enu.http.stream` (streaming). Dos httptest.Server distintos escuchan en
// `127.0.0.1` con puertos distintos, así que un redirect entre ellos es cross-host
// por puerto —justo el eje que ejercitan los tests de recorte de cabeceras—.

// hopServer sirve una cadena de redirects sobre UN solo host (same-host, por path):
// una petición a `/N` responde `302 Location: /N-1`, y `/0` responde `200 "final"`.
// Devuelve además el contador de veces que se alcanzó el endpoint final `/0`, para
// afirmar que un presupuesto agotado (o `0`) NO lo alcanzó.
func hopServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var finalHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			http.Error(w, "path no numérico", http.StatusBadRequest)
			return
		}
		if n <= 0 {
			atomic.AddInt32(&finalHits, 1)
			_, _ = io.WriteString(w, "final")
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/%d", n-1), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &finalHits
}

// TestHTTPRedirectFollowedWithinBudget (G54): con el default (sin `max_redirects`)
// una cadena de 3 redirects se sigue hasta el `200` final —la política implícita de
// Go elevada a contrato (default 10) sigue funcionando sin pedir nada—.
func TestHTTPRedirectFollowedWithinBudget(t *testing.T) {
	srv, finalHits := hopServer(t)

	h := newHarness(t)
	withURL(h, srv.URL+"/3")
	h.eval(`
		status, body = nil, nil
		enu.task.spawn(function()
			local res = enu.http.request({ url = URL() })
			status = res.status
			body = res.body
		end)
	`)
	h.expectEval(`return tostring(status)`, "200")
	h.expectEval(`return body`, "final")
	if got := atomic.LoadInt32(finalHits); got != 1 {
		t.Fatalf("el endpoint final se alcanzó %d veces, esperado 1", got)
	}
}

// TestHTTPRedirectBudgetExceededReturnsLast3xx (G54): con `max_redirects = 2` frente
// a una cadena que necesita más, el presupuesto se agota y la última `3xx` se
// entrega **como dato** —status 302, `Location` presente, y SIN lanzar (el pcall va
// bien)—. El endpoint final NO se alcanza: es lo que corta la amplificación de SSRF.
func TestHTTPRedirectBudgetExceededReturnsLast3xx(t *testing.T) {
	srv, finalHits := hopServer(t)

	h := newHarness(t)
	withURL(h, srv.URL+"/5")
	h.eval(`
		ok, status, loc = nil, nil, nil
		enu.task.spawn(function()
			local o, res = pcall(function()
				return enu.http.request({ url = URL(), max_redirects = 2 })
			end)
			ok = o
			status = res.status
			loc = res.headers["Location"]
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true") // presupuesto agotado NO lanza
	h.expectEval(`return tostring(status)`, "302")
	h.expectEval(`return loc`, "/2") // la 3xx entregada apunta al siguiente salto no seguido
	if got := atomic.LoadInt32(finalHits); got != 0 {
		t.Fatalf("el endpoint final se alcanzó %d veces con presupuesto agotado, esperado 0", got)
	}
}

// TestHTTPRedirectZeroDoesNotFollow (G54): `max_redirects = 0` no sigue ningún
// redirect —el primer `302` se entrega como dato—. Es la forma de observar/validar
// la cadena salto a salto (poner `0` y seguirla a mano).
func TestHTTPRedirectZeroDoesNotFollow(t *testing.T) {
	srv, finalHits := hopServer(t)

	h := newHarness(t)
	withURL(h, srv.URL+"/1")
	h.eval(`
		ok, status, loc = nil, nil, nil
		enu.task.spawn(function()
			local o, res = pcall(function()
				return enu.http.request({ url = URL(), max_redirects = 0 })
			end)
			ok = o
			status = res.status
			loc = res.headers["Location"]
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")
	h.expectEval(`return tostring(status)`, "302")
	h.expectEval(`return loc`, "/0")
	if got := atomic.LoadInt32(finalHits); got != 0 {
		t.Fatalf("el endpoint final se alcanzó %d veces con max_redirects=0, esperado 0", got)
	}
}

// TestHTTPRedirectStripsHeadersCrossHost (G54): en un salto cross-host (dos servers
// en puertos distintos) el cliente recorta las cabeceras del llamante antes de
// reenviar —el destino distinto NO hereda la credencial custom (`x-api-key`) que el
// llamante dio al primer host—.
func TestHTTPRedirectStripsHeadersCrossHost(t *testing.T) {
	var mu sync.Mutex
	var gotKey string
	var gotAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey = r.Header.Get("X-Api-Key")
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	h := newHarness(t)
	withURL(h, source.URL)
	h.eval(`
		status = nil
		enu.task.spawn(function()
			local res = enu.http.request({
				url = URL(),
				headers = { ["X-Api-Key"] = "secreto", ["Authorization"] = "Bearer t" },
			})
			status = res.status
		end)
	`)
	h.expectEval(`return tostring(status)`, "200")
	mu.Lock()
	defer mu.Unlock()
	if gotKey != "" {
		t.Fatalf("X-Api-Key llegó al host cruzado (%q), debía recortarse", gotKey)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization llegó al host cruzado (%q), debía recortarse", gotAuth)
	}
}

// TestHTTPRedirectKeepsHeadersSameHost (G54): un redirect same-host (mismo host y
// puerto, solo cambia el path) CONSERVA las cabeceras del llamante —el recorte es
// solo para el cruce de frontera; el mismo host sigue siendo el mismo interlocutor—.
func TestHTTPRedirectKeepsHeadersSameHost(t *testing.T) {
	var mu sync.Mutex
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/end", http.StatusFound)
		case "/end":
			mu.Lock()
			gotKey = r.Header.Get("X-Api-Key")
			mu.Unlock()
			_, _ = io.WriteString(w, "ok")
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	h := newHarness(t)
	withURL(h, srv.URL+"/start")
	h.eval(`
		status = nil
		enu.task.spawn(function()
			local res = enu.http.request({
				url = URL(),
				headers = { ["X-Api-Key"] = "secreto" },
			})
			status = res.status
		end)
	`)
	h.expectEval(`return tostring(status)`, "200")
	mu.Lock()
	defer mu.Unlock()
	if gotKey != "secreto" {
		t.Fatalf("X-Api-Key en salto same-host: got %q, want \"secreto\" (debía conservarse)", gotKey)
	}
}

// TestHTTPRedirectNoRestoreAfterCrossHost (G54): una vez recortadas en un salto
// cross-host, las cabeceras NO se restauran aunque un salto posterior regrese al
// host inicial —la cadena ya pasó por un tercero y dejó de ser de confianza—.
// Cadena: A/start → B (cross-host) → A/final (regreso al host inicial). En A/final la
// cabecera del llamante debe seguir ausente.
func TestHTTPRedirectNoRestoreAfterCrossHost(t *testing.T) {
	var mu sync.Mutex
	var finalKey string
	var finalHit bool

	// `bURL` se rellena tras crear B; los handlers corren en tiempo de petición, ya
	// asignado. A sirve /start (→ B) y /final (registra la cabecera de vuelta).
	var bURL string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, bURL, http.StatusFound)
		case "/final":
			mu.Lock()
			finalKey = r.Header.Get("X-Api-Key")
			finalHit = true
			mu.Unlock()
			_, _ = io.WriteString(w, "ok")
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, a.URL+"/final", http.StatusFound)
	}))
	defer b.Close()
	bURL = b.URL

	h := newHarness(t)
	withURL(h, a.URL+"/start")
	h.eval(`
		status = nil
		enu.task.spawn(function()
			local res = enu.http.request({
				url = URL(),
				headers = { ["X-Api-Key"] = "secreto" },
			})
			status = res.status
		end)
	`)
	h.expectEval(`return tostring(status)`, "200")
	mu.Lock()
	defer mu.Unlock()
	if !finalHit {
		t.Fatalf("el host inicial no recibió el salto de vuelta (/final): la cadena no se completó")
	}
	if finalKey != "" {
		t.Fatalf("X-Api-Key se restauró al volver al host inicial (%q), debía seguir recortada", finalKey)
	}
}

// TestHTTPRedirectMaxRedirectsEINVAL (G54): `max_redirects` de tipo equivocado,
// negativo o fraccionario es uso inválido (`EINVAL`) —el presupuesto es un número
// de saltos, no cabe media redirección ni un valor negativo—.
func TestHTTPRedirectMaxRedirectsEINVAL(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		c1, c2, c3 = nil, nil, nil
		enu.task.spawn(function()
			local _, e1 = pcall(function() enu.http.request({ url = "http://x", max_redirects = -1 }) end)
			c1 = e1.code
			local _, e2 = pcall(function() enu.http.request({ url = "http://x", max_redirects = 2.5 }) end)
			c2 = e2.code
			local _, e3 = pcall(function() enu.http.request({ url = "http://x", max_redirects = "tres" }) end)
			c3 = e3.code
		end)
	`)
	h.expectEval(`return c1`, "EINVAL")
	h.expectEval(`return c2`, "EINVAL")
	h.expectEval(`return c3`, "EINVAL")
}

// --- enu.http.stream (misma resolución, superficie de streaming) ---------------

// TestStreamRedirectFollowedWithinBudget (G54): `stream` sigue la cadena de redirects
// igual que `request` y entrega el `Stream` sobre el `200` final (su body se itera).
func TestStreamRedirectFollowedWithinBudget(t *testing.T) {
	srv, finalHits := hopServer(t)

	h := newHarness(t)
	withURL(h, srv.URL+"/3")
	h.eval(`
		status, body = nil, ""
		enu.task.spawn(function()
			local st = enu.http.stream({ url = URL() })
			status = st.status
			for chunk in st:chunks() do body = body .. chunk end
			st:close()
		end)
	`)
	h.expectEval(`return tostring(status)`, "200")
	h.expectEval(`return body`, "final")
	if got := atomic.LoadInt32(finalHits); got != 1 {
		t.Fatalf("el endpoint final se alcanzó %d veces, esperado 1", got)
	}
}

// TestStreamRedirectBudgetExceededReturnsLast3xx (G54): agotado el presupuesto,
// `stream` entrega un `Stream` con el status `3xx` (con `Location` en `headers`),
// como un `200` cualquiera —"el status es dato"— y sin lanzar.
func TestStreamRedirectBudgetExceededReturnsLast3xx(t *testing.T) {
	srv, finalHits := hopServer(t)

	h := newHarness(t)
	withURL(h, srv.URL+"/5")
	h.eval(`
		ok, status, loc = nil, nil, nil
		enu.task.spawn(function()
			local o, st = pcall(function()
				return enu.http.stream({ url = URL(), max_redirects = 1 })
			end)
			ok = o
			status = st.status
			loc = st.headers["Location"]
			st:close()
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")
	h.expectEval(`return tostring(status)`, "302")
	h.expectEval(`return loc`, "/3") // /5 → (sigue 1) → /4 → 302 a /3, entregado como dato
	if got := atomic.LoadInt32(finalHits); got != 0 {
		t.Fatalf("el endpoint final se alcanzó %d veces con presupuesto agotado, esperado 0", got)
	}
}

// TestStreamRedirectStripsHeadersCrossHost (G54): en `stream`, igual que en `request`,
// un salto cross-host recorta las cabeceras del llamante antes de reenviar.
func TestStreamRedirectStripsHeadersCrossHost(t *testing.T) {
	var mu sync.Mutex
	var gotKey string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey = r.Header.Get("X-Api-Key")
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	h := newHarness(t)
	withURL(h, source.URL)
	h.eval(`
		status = nil
		enu.task.spawn(function()
			local st = enu.http.stream({
				url = URL(),
				headers = { ["X-Api-Key"] = "secreto" },
			})
			status = st.status
			st:close()
		end)
	`)
	h.expectEval(`return tostring(status)`, "200")
	mu.Lock()
	defer mu.Unlock()
	if gotKey != "" {
		t.Fatalf("X-Api-Key llegó al host cruzado por stream (%q), debía recortarse", gotKey)
	}
}

// TestCrossHostRule_G54 blinda a nivel de unidad la regla EXACTA de "cross-host" de
// G54, incluida la rama que los tests de red no cubren cómodamente (degradación de
// esquema `https`→`http`) y la equivalencia de puerto por defecto.
func TestCrossHostRule_G54(t *testing.T) {
	cases := []struct {
		name          string
		initial, next string
		wantCross     bool
	}{
		{"mismo host, solo cambia el path", "http://a.com/x", "http://a.com/y", false},
		{"puerto por defecto equivale al explícito (http)", "http://a.com/x", "http://a.com:80/y", false},
		{"puerto por defecto equivale al explícito (https)", "https://a.com/x", "https://a.com:443/y", false},
		{"puerto distinto es cross-host", "http://a.com/x", "http://a.com:8080/y", true},
		{"hostname distinto es cross-host", "http://a.com/x", "http://b.com/y", true},
		{"degradación https→http mismo host es cross-host", "https://a.com/x", "http://a.com/y", true},
		{"upgrade http→https mismo host NO es cross-host", "http://a.com/x", "https://a.com/y", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			iu, err := url.Parse(c.initial)
			if err != nil {
				t.Fatalf("URL inicial inválida: %v", err)
			}
			nu, err := url.Parse(c.next)
			if err != nil {
				t.Fatalf("URL destino inválida: %v", err)
			}
			if got := isCrossHost(iu, nu); got != c.wantCross {
				t.Fatalf("isCrossHost(%q, %q) = %v, want %v", c.initial, c.next, got, c.wantCross)
			}
		})
	}
}
