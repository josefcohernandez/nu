package runtime

// Test END-TO-END del driver de TTY (driver.go, S33): cierra el lazo completo que hasta
// S33 no existía —tecla cruda del terminal → parser → pila de input → handler Lua →
// mutación del compositor → frame ANSI volcado al terminal— SIN un TTY real, conduciendo
// el bucle del driver (`drive`) contra tuberías en memoria. Es la demostración de que
// "una TUI en Lua sobre la API de Go" funciona: el mismo camino que un `nu` interactivo,
// pero con la entrada/salida inyectadas para que un test las inspeccione.
//
// Lo no testeable aquí (raw mode, `term.GetSize`, `SIGWINCH`) es la cáscara fina de
// `RunInteractive`; el bucle, el parseo y el despacho —lo que de verdad lleva la lógica—
// sí se ejercitan, igual que input.go probó su pila con eventos inyectados.

import (
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuf es un buffer concurrente: el painter/feed escriben bajo el token (serializados
// entre sí) y el test lee desde su goroutine; el mutex evita la carrera lector/escritor
// sobre el buffer en sí.
type syncBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor sondea `out` hasta que contenga `sub` o venza el plazo. Devuelve si lo encontró.
func waitForOut(out *syncBuf, sub string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), sub) {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return strings.Contains(out.String(), sub)
}

// readRow lee la fila `y` de la pantalla compuesta bajo el token Y el candado de
// la UI (G44: el bombeo continuo puede estar mutando el compositor desde un paso
// del scheduler; el token solo ya no excluye esa vía).
func readRow(h *harness, y int) string {
	h.rt.sched.acquire()
	defer h.rt.sched.release()
	var row string
	h.rt.withUILock(func() { row = composeRow(h.rt.ui.comp, y) })
	return row
}

// waitForRow sondea la fila `y` de la pantalla compuesta hasta que sea `want` o venza el
// plazo.
func waitForRow(h *harness, y int, want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if readRow(h, y) == want {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// TestDriverEndToEnd monta una TUI Lua mínima (una región a pantalla completa con un
// contador y dos keymaps: ↑ incrementa, q sale), conduce el driver con tuberías en
// memoria, inyecta pulsaciones y comprueba que (a) el frame inicial se pinta al conectar,
// (b) cada ↑ repinta el contador, y (c) `q` (que emite `core:shutdown`) apaga el bucle.
func TestDriverEndToEnd(t *testing.T) {
	h := newHarnessUI(t, 24, 4)
	if err := h.rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}

	// La "app" Lua: estado retenido en un closure, repintado por una función, input por
	// keymaps. Usa SOLO la API pública de §9 (region/blit/block/clear, keymap) y §4
	// (events.emit) — exactamente lo que un autor de extensión escribiría.
	h.eval(`
		local r = nu.ui.region({ x = 0, y = 0, w = 24, h = 4 })
		local count = 0
		local function repaint()
		  r:clear()
		  r:blit(0, 0, nu.ui.block({ "count=" .. count }))
		end
		repaint()
		nu.ui.keymap("up", function() count = count + 1; repaint() end)
		nu.ui.keymap("q", function() nu.events.emit("core:shutdown") end)
	`)

	inR, inW := io.Pipe()
	out := &syncBuf{}
	d := newDriver(h.rt, inR, out)
	d.installShutdownHandler()
	d.attachOutput() // pinta el primer frame completo (count=0)

	done := make(chan struct{})
	go func() { d.drive(); close(done) }()

	// (a) Frame inicial: el contador en 0 ya se volcó al terminal (lo pintó attachOutput
	// con el frame COMPLETO al conectar la salida).
	if !waitForOut(out, "count=0", time.Second) {
		t.Fatalf("frame inicial no contiene count=0; salida:\n%q", out.String())
	}

	// (b) Tres flechas arriba → el contador llega a 3. El stream del terminal es un DIFF
	// (solo se reemite el dígito que cambia, no "count=" entero), así que el "count=3"
	// literal no aparece en los bytes; lo que se comprueba es el ESTADO COMPUESTO de la
	// pantalla tras procesar las teclas (lo que el usuario ve), bajo el token.
	if _, err := inW.Write([]byte("\x1b[A\x1b[A\x1b[A")); err != nil {
		t.Fatalf("write ↑: %v", err)
	}
	if !waitForRow(h, 0, "count=3", 2*time.Second) {
		t.Fatalf("tras 3 ↑ la fila compuesta es %q, want %q", readRow(h, 0), "count=3")
	}

	// (c) `q` emite core:shutdown → el handler interno del driver cierra el bucle.
	if _, err := inW.Write([]byte("q")); err != nil {
		t.Fatalf("write q: %v", err)
	}
	select {
	case <-done:
		// apagado limpio
	case <-time.After(2 * time.Second):
		t.Fatal("el bucle del driver no se apagó tras core:shutdown")
	}
	_ = inW.Close()
}

// screenContains compone toda la pantalla y comprueba si alguna fila contiene `sub`.
// Bajo el token y el candado de la UI (G44, como readRow).
func screenContains(h *harness, sub string) bool {
	h.rt.sched.acquire()
	defer h.rt.sched.release()
	found := false
	h.rt.withUILock(func() {
		c := h.rt.ui.comp
		for y := 0; y < c.h; y++ {
			if strings.Contains(composeRow(c, y), sub) {
				found = true
				return
			}
		}
	})
	return found
}

func waitScreen(h *harness, sub string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if screenContains(h, sub) {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return screenContains(h, sub)
}

// TestDriverRunsRealDemoPlugin arranca el PLUGIN DE DEMOSTRACIÓN real
// (examples/nu/plugins/tui-demo) desde disco y lo conduce por el driver, sin TTY. Es la
// prueba de que el artefacto que un humano correría en su terminal funciona: su init.lua
// monta la UI sin errores, pinta el marco/título y responde al teclado (↑ sube el
// contador). Si el demo tuviera un fallo de Lua, Boot o el primer frame lo destaparían.
func TestDriverRunsRealDemoPlugin(t *testing.T) {
	demoDir, err := filepath.Abs(filepath.Join("..", "..", "examples", "nu", "plugins"))
	if err != nil {
		t.Fatalf("ruta del demo: %v", err)
	}
	rt := New(
		WithDataDir(t.TempDir()),
		WithConfigDir(t.TempDir()),
		WithForceUI(true),
		WithUISize(60, 16),
		WithPluginDir(demoDir),
		WithEnabledPlugins([]string{"tui-demo"}),
	)
	t.Cleanup(rt.Close)
	h := &harness{t: t, rt: rt}
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot del demo falló: %v", err)
	}

	inR, inW := io.Pipe()
	out := &syncBuf{}
	d := newDriver(rt, inR, out)
	d.installShutdownHandler()
	d.attachOutput()

	done := make(chan struct{})
	go func() { d.drive(); close(done) }()

	// El título del marco aparece en el primer frame.
	if !waitScreen(h, "tui-demo", 2*time.Second) {
		t.Fatalf("el demo no pintó el título; pantalla:\n%s", dumpScreen(h))
	}
	// El contador arranca en 0.
	if !waitScreen(h, "contador", time.Second) {
		t.Fatalf("el demo no pintó la fila del contador; pantalla:\n%s", dumpScreen(h))
	}

	// ↑ tres veces: el contador sube a 3 (lo busca como "contador" seguido de un 3 en la
	// misma fila — basta con que la cifra 3 esté presente tras incrementar).
	_, _ = inW.Write([]byte("\x1b[A\x1b[A\x1b[A"))
	if !waitScreenRow(h, "contador", "3", 2*time.Second) {
		t.Fatalf("tras 3 ↑ el contador no llegó a 3; pantalla:\n%s", dumpScreen(h))
	}

	// q apaga.
	_, _ = inW.Write([]byte("q"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("el demo no se apagó con q")
	}
	_ = inW.Close()
}

// waitScreenRow espera a que alguna fila contenga AMBOS substrings (p. ej. la etiqueta y
// su valor en la misma línea). Bajo el token.
func waitScreenRow(h *harness, a, b string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		h.rt.sched.acquire()
		c := h.rt.ui.comp
		found := false
		for y := 0; y < c.h; y++ {
			row := composeRow(c, y)
			if strings.Contains(row, a) && strings.Contains(row, b) {
				found = true
				break
			}
		}
		h.rt.sched.release()
		if found {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// dumpScreen vuelca la pantalla compuesta (para mensajes de error legibles). Bajo el token.
func dumpScreen(h *harness) string {
	h.rt.sched.acquire()
	defer h.rt.sched.release()
	c := h.rt.ui.comp
	var b strings.Builder
	for y := 0; y < c.h; y++ {
		b.WriteString(composeRow(c, y))
		b.WriteByte('\n')
	}
	return b.String()
}

// TestDriverEmergencyExit (ADR-017, G35): la RED DE SALIDA DE EMERGENCIA del kernel
// garantiza que, aunque NADA monte UI ni instale atajos (el caso patológico de un
// arranque que falla y deja la terminal en raw mode), el usuario puede salir con
// q/esc/ctrl+c. Sin app ni keymaps, `InstallEmergencyExit` basta para apagar el bucle.
func TestDriverEmergencyExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys string
	}{
		{"q", "q"},
		{"esc", "\x1b"},
		{"ctrl+c", "\x03"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessUI(t, 10, 1)
			if err := h.rt.Boot(); err != nil {
				t.Fatalf("Boot falló: %v", err)
			}
			// Sin montar NINGUNA UI ni keymap: solo la red de emergencia del kernel.
			h.rt.InstallEmergencyExit()

			inR, inW := io.Pipe()
			out := &syncBuf{}
			d := newDriver(h.rt, inR, out)
			d.installShutdownHandler()
			d.attachOutput()

			done := make(chan struct{})
			go func() { d.drive(); close(done) }()

			if _, err := inW.Write([]byte(tc.keys)); err != nil {
				t.Fatalf("write %q: %v", tc.name, err)
			}
			select {
			case <-done:
				// la red de emergencia emitió core:shutdown → bucle apagado.
			case <-time.After(2 * time.Second):
				t.Fatalf("%s no apagó el bucle vía la red de emergencia", tc.name)
			}
			_ = inW.Close()
		})
	}
}
