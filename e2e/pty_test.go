package e2e

// El helper de PTY: lanza el binario dentro de un PSEUDO-TERMINAL real, para los
// plugins INTERACTIVOS (chat/repl) que solo cobran vida con un TTY (el binario detecta
// TTY con term.IsTerminal sobre stdin+stdout, ui.go §detectTTY; sin él sale con uso).
// Permite inyectar teclas (Send) y esperar patrones en la salida ANSI (Expect).
//
// SIN dependencias nuevas: el par maestro/esclavo se abre a mano sobre `/dev/ptmx` con
// golang.org/x/sys/unix (ya en el árbol de módulos como dependencia de x/term), en
// `openPTY` con implementación por plataforma (pty_darwin_test.go / pty_linux_test.go).
// El proceso hijo recibe el ESCLAVO como stdin/stdout/stderr y se hace líder de sesión
// con ese TTY de control (Setsid+Setctty); el test lee/escribe el MAESTRO.

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// PTY es una sesión interactiva del binario corriendo bajo un pseudo-terminal. Un
// goroutine drena la salida del maestro a un buffer; Expect/ExpectRe esperan patrones
// sobre ese buffer con timeout; Send escribe teclas.
type PTY struct {
	master *os.File
	cmd    *exec.Cmd

	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool

	waitOnce sync.Once
	waitErr  error
}

// Start lanza el binario bajo un PTY nuevo (80x24 por defecto) con el entorno y cwd del
// workspace. Es el análogo interactivo de Run: úsalo para chat/repl. Registra la
// limpieza (mata el proceso y cierra el maestro al terminar el test).
func (w *Workspace) Start(t *testing.T, opts RunOpts) *PTY {
	t.Helper()
	// Escotilla para entornos sin PTY: si un runner de CI no pudiera asignar un
	// pseudo-terminal (o quisiéramos aislar los tests interactivos), `E2E_NO_PTY=1`
	// los SALTA en bloque en vez de que fallen por no poder arrancar la UI. Por
	// defecto (variable ausente) corren: los runners GitHub Linux/macOS sí soportan
	// /dev/ptmx, así que la suite interactiva se ejerce normalmente.
	if os.Getenv("E2E_NO_PTY") != "" {
		t.Skip("E2E_NO_PTY: suite interactiva (PTY) deshabilitada en este entorno")
	}
	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("openPTY: %v", err)
	}
	// Tamaño inicial de la pantalla (el compositor lo lee del TTY, no de COLUMNS/LINES
	// cuando hay terminal). 80x24 es el default histórico.
	_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80})

	dir := opts.Dir
	if dir == "" {
		dir = w.Workdir
	}
	cmd := exec.Command(enuBin, opts.Args...)
	cmd.Dir = dir
	cmd.Env = append(w.baseEnv(), opts.Env...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	// El hijo se hace líder de una sesión NUEVA y toma el esclavo como TTY de control
	// (Ctty por defecto = 0 = el fd de stdin del hijo, que es el esclavo): así
	// term.IsTerminal(stdin)=term.IsTerminal(stdout)=true y el binario entra en modo
	// interactivo (driver.go RunInteractive).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	if err := cmd.Start(); err != nil {
		_ = slave.Close()
		_ = master.Close()
		t.Fatalf("arrancar el binario bajo PTY: %v", err)
	}
	// El esclavo ya vive en el hijo; el padre lo cierra para que el maestro vea EOF
	// cuando el hijo termine y cierre su copia.
	_ = slave.Close()

	p := &PTY{master: master, cmd: cmd}
	go p.drain()
	t.Cleanup(func() { p.Close() })
	return p
}

// drain copia continuamente la salida del maestro al buffer hasta EOF (el hijo cerró su
// lado) o error de lectura. Corre en su goroutine desde Start.
func (p *PTY) drain() {
	tmp := make([]byte, 4096)
	for {
		n, err := p.master.Read(tmp)
		if n > 0 {
			p.mu.Lock()
			p.buf.Write(tmp[:n])
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Output devuelve TODO lo que el proceso ha escrito hasta ahora (ANSI crudo incluido).
func (p *PTY) Output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.String()
}

// Send escribe `s` en el maestro: llega al proceso como pulsaciones de teclado. Para
// teclas de control usa los bytes crudos ("\r" enter, "\x1b" esc, "\x03" ctrl+c,
// "\x04" ctrl+d).
func (p *PTY) Send(t *testing.T, s string) {
	t.Helper()
	if _, err := p.master.WriteString(s); err != nil {
		t.Fatalf("PTY.Send(%q): %v", s, err)
	}
}

// Expect espera hasta que la salida acumulada CONTENGA la subcadena `sub`, o falla la
// prueba al agotar `timeout` (0 = 5 s). Devuelve la salida completa vista. Sondea el
// buffer cada pocos ms (la salida llega asíncrona por el goroutine drain).
func (p *PTY) Expect(t *testing.T, sub string, timeout time.Duration) string {
	t.Helper()
	return p.waitFor(t, func(s string) bool { return strings.Contains(s, sub) },
		"subcadena "+strconv.Quote(sub), timeout)
}

// ExpectRe espera hasta que la salida acumulada CASE con `re`, o falla al agotar
// `timeout` (0 = 5 s). Devuelve la salida completa vista.
func (p *PTY) ExpectRe(t *testing.T, re *regexp.Regexp, timeout time.Duration) string {
	t.Helper()
	return p.waitFor(t, func(s string) bool { return re.MatchString(s) },
		"patrón "+re.String(), timeout)
}

// waitFor es el bucle común de sondeo con deadline compartido por Expect/ExpectRe.
func (p *PTY) waitFor(t *testing.T, ok func(string) bool, desc string, timeout time.Duration) string {
	t.Helper()
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		out := p.Output()
		if ok(out) {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("PTY.Expect: no apareció %s en %s\n--- salida vista ---\n%s", desc, timeout, out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Resize cambia el tamaño de la pantalla del PTY (emite SIGWINCH al hijo). Útil para
// ejercitar el reflow del compositor.
func (p *PTY) Resize(cols, rows int) {
	_ = unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ,
		&unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
}

// Wait espera a que el proceso TERMINE por sí mismo (tras un Send de salida, p. ej.
// "q"), o falla al agotar `timeout` (0 = 5 s) matándolo. Devuelve el código de salida
// (0 en salida limpia). No lo llames si vas a matar el proceso con Close: usa uno u otro.
func (p *PTY) Wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	done := make(chan struct{})
	go func() {
		p.waitOnce.Do(func() { p.waitErr = p.cmd.Wait() })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-done
		t.Fatalf("PTY.Wait: el proceso no terminó en %s\n--- salida vista ---\n%s", timeout, p.Output())
	}
	if p.waitErr == nil {
		return 0
	}
	if exitErr, ok := p.waitErr.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	t.Fatalf("PTY.Wait: error inesperado: %v", p.waitErr)
	return -1
}

// WaitExit es la variante NO fatal de Wait: espera hasta `timeout` (0 = 5 s) a que el
// proceso termine y devuelve `(exitCode, true)` si lo hizo, o `(-1, false)` si venció el
// plazo (el proceso sigue vivo; lo matará el Close del Cleanup). Úsala cuando la propia
// terminación —o su ausencia— es el dato a afirmar sin abortar el test de inmediato
// (p. ej. caracterizar un bug de salida conocido: ver los tests TestReplE2E*).
func (p *PTY) WaitExit(timeout time.Duration) (int, bool) {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	done := make(chan struct{})
	go func() {
		p.waitOnce.Do(func() { p.waitErr = p.cmd.Wait() })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		// El proceso sigue vivo; la goroutine queda bloqueada en cmd.Wait hasta que el
		// Close del Cleanup mate el proceso (comparte el waitOnce, sin fugas más allá
		// del test).
		return -1, false
	}
	if p.waitErr == nil {
		return 0, true
	}
	if exitErr, ok := p.waitErr.(*exec.ExitError); ok {
		return exitErr.ExitCode(), true
	}
	return -1, true
}

// Close mata el proceso (si sigue vivo) y cierra el maestro. Idempotente; lo llama el
// t.Cleanup de Start, así que normalmente no hace falta invocarlo a mano.
func (p *PTY) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.waitOnce.Do(func() { p.waitErr = p.cmd.Wait() })
	_ = p.master.Close()
}
