package e2e

// Humo del propio ARNÉS: ejercita el binario real por cada helper para probar que el
// andamiaje funciona (build, workspace aislado, Run, provider fake, PTY). Los tests que
// escriben ENCIMA (chat/repl/agente) reutilizan estos mismos helpers.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSmokeEvalVersion: `enu -e 'return enu.version.api'` imprime el nivel de API a
// stdout y sale con 0. Es el smoke que hoy corre CI, aquí sobre el arnés completo.
func TestSmokeEvalVersion(t *testing.T) {
	ws := NewWorkspace(t)
	res := ws.Run(t, RunOpts{Args: []string{"-e", "return enu.version.api"}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	got := strings.TrimSpace(res.Stdout)
	if n, err := strconv.Atoi(got); err != nil || n < 1 {
		t.Fatalf("stdout debía ser el nivel de API (entero >=1); got %q", got)
	}
}

// TestSmokeNoArgsNoTTYExit2: `enu` sin flags y SIN TTY (Run usa tuberías) es uso
// inválido: no hay superficie interactiva que montar, así que imprime el uso a stderr y
// sale con 2 (la convención de S45).
func TestSmokeNoArgsNoTTYExit2(t *testing.T) {
	ws := NewWorkspace(t)
	res := ws.Run(t, RunOpts{})
	if res.ExitCode != 2 {
		t.Fatalf("exit: got %d, want 2 (stdout=%q, stderr=%q)", res.ExitCode, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "uso:") {
		t.Fatalf("stderr debía imprimir el uso; got %q", res.Stderr)
	}
}

// TestSmokeEvalErrorExit1: `enu -e 'error("x")'` es un error de ejecución headless:
// sale con 1 y deja el mensaje en stderr, nada en stdout.
func TestSmokeEvalErrorExit1(t *testing.T) {
	ws := NewWorkspace(t)
	res := ws.Run(t, RunOpts{Args: []string{"-e", `error("boom-e2e")`}})
	if res.ExitCode != 1 {
		t.Fatalf("exit: got %d, want 1 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Fatalf("un error no debe escribir a stdout; got %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "boom-e2e") {
		t.Fatalf("stderr debía traer el mensaje del error; got %q", res.Stderr)
	}
}

// TestSmokeAgentTurn: un turno mínimo `enu -p '<prompt>'` contra el provider FAKE (por
// el adaptador anthropic real) imprime el texto del asistente a stdout y sale con 0.
// Ejercita el arnés entero: build, workspace, providers.toml→fake, agent.toml, y el
// adaptador HTTP+SSE real contra httptest.
func TestSmokeAgentTurn(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	fp.PushText("hola desde el asistente e2e")

	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	ws.UseFakeProvider(t, fp)

	res := ws.Run(t, RunOpts{Args: []string{"-p", "saluda"}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hola desde el asistente e2e") {
		t.Fatalf("stdout debía traer el texto del asistente; got %q (stderr=%q)", res.Stdout, res.Stderr)
	}
	// El adaptador habló con el fake: al menos un POST, con la cabecera de auth.
	if fp.RequestCount() < 1 {
		t.Fatalf("el fake no recibió ningún request")
	}
	if h := fp.Header(0); h == nil || h.Get("x-api-key") == "" {
		t.Fatalf("el request no llevó la cabecera x-api-key")
	}
}

// TestSmokeAgentToolLoop: el arnés modela el LOOP de tools encolando primero una
// respuesta tool_use (read_file sobre un fichero del workdir, tool de solo lectura →
// concedida sin --auto-permissions) y luego el texto final. El binario ejecuta la tool
// y vuelve a pedir: dos POST al fake, y el texto del 2º turno sale a stdout. Es el
// patrón que los tests de agente encima del arnés reutilizarán.
func TestSmokeAgentToolLoop(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	ws.WriteFile(t, "LEEME.txt", "contenido del fichero\n")
	fp.PushToolUse("call-1", "read_file", map[string]any{"path": "LEEME.txt"})
	fp.PushText("he leido el fichero")

	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	ws.UseFakeProvider(t, fp)

	res := ws.Run(t, RunOpts{Args: []string{"-p", "lee LEEME.txt"}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "he leido el fichero") {
		t.Fatalf("stdout debía traer el texto final del asistente; got %q (stderr=%q)", res.Stdout, res.Stderr)
	}
	if fp.RequestCount() < 2 {
		t.Fatalf("el loop de tools debía disparar >=2 requests; got %d", fp.RequestCount())
	}
}

// TestSmokePTYBareScreen: humo del helper de PTY. Sin plugins (sin enu.toml) y CON TTY,
// el binario pinta la PANTALLA DE RUNTIME DESNUDO (G21); `q` emite core:shutdown y el
// proceso sale limpio con 0. Prueba que openPTY + Start + Expect + Send + Wait funcionan.
func TestSmokePTYBareScreen(t *testing.T) {
	ws := NewWorkspace(t)
	p := ws.Start(t, RunOpts{})
	// La pantalla desnuda rotula "enu — runtime desnudo" (bare_screen.go).
	p.Expect(t, "runtime desnudo", 5*time.Second)
	// `q` cierra (installKernelExitWasm: q/esc/ctrl+c → core:shutdown).
	p.Send(t, "q")
	if code := p.Wait(t, 5*time.Second); code != 0 {
		t.Fatalf("la pantalla desnuda debía salir con 0 tras `q`; got %d\n--- salida ---\n%s", code, p.Output())
	}
}
