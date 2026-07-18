package e2e

// El ARNÉS e2e: compila el binario `enu` UNA vez (TestMain), monta un WORKSPACE con
// HOME/XDG aislados (config y data en temporales — el binario resuelve sus rutas por
// entorno, log.go: `$XDG_CONFIG_HOME/enu` y `$XDG_DATA_HOME/enu`), y lanza el proceso
// real con `Run`/`Start`. Todo hermético: nada toca el `~/.config/enu` del usuario ni
// la red (el provider es un FAKE por httptest, ver provider_test.go).
//
// Filosofía del arnés (imita main_test.go, pero un nivel más arriba): main_test.go
// ejercita el NÚCLEO testeable del CLI in-process (`runWith(rt, opts)`); aquí se
// ejercita el BINARIO —flags, exit codes, arranque real, el bucle del proceso—, la
// única capa que un test in-process no cubre.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// enuBin es la ruta del binario `enu` compilado UNA vez por TestMain. Los tests lo
// leen (nunca lo recompilan). Vacío = TestMain no llegó a construirlo (fallo fatal).
var enuBin string

// TestMain compila el binario `enu` (CGO desactivado, ADR-001: binario estático puro
// Go) a un directorio temporal ANTES de correr los tests, y lo expone en `enuBin`.
// Compilar una sola vez amortiza el coste (wazero + chroma tardan) entre todos los
// tests del paquete. Si el build falla, aborta la suite: sin binario no hay e2e.
func TestMain(m *testing.M) {
	root, err := repoRoot()
	if err != nil {
		panic("e2e: no encuentro la raíz del repo (go.mod): " + err.Error())
	}

	binDir, err := os.MkdirTemp("", "enu-e2e-bin-")
	if err != nil {
		panic("e2e: no puedo crear el dir del binario: " + err.Error())
	}
	bin := filepath.Join(binDir, "enu")

	// Build con un timeout amplio (la primera compilación cruza todo el árbol) pero
	// acotado: un build colgado no debe colgar la suite. CGO_ENABLED=0 fija el binario
	// estático de producción.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(binDir)
		panic("e2e: fallo compilando el binario enu:\n" + string(out) + "\n" + err.Error())
	}
	enuBin = bin

	code := m.Run()
	_ = os.RemoveAll(binDir)
	os.Exit(code)
}

// repoRoot sube desde el cwd del test (el dir del paquete e2e) hasta encontrar el
// `go.mod` del módulo: esa es la raíz desde la que `go build .` compila el `package
// main`. No depende de la profundidad del worktree.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod no encontrado subiendo desde " + dir)
		}
		dir = parent
	}
}

// --- Workspace: HOME/XDG aislados por test ---------------------------------------

// Workspace es un entorno de disco AISLADO para una ejecución del binario: HOME y los
// XDG dirs cuelgan de un `t.TempDir()`, así que el binario nunca lee ni escribe la
// config real del usuario. `ConfigDir`/`DataDir` son ya los subdirectorios `enu/` que
// el binario resuelve (XDG_CONFIG_HOME/enu, XDG_DATA_HOME/enu): escribe tus `enu.toml`,
// `providers.toml`, `agent.toml` en `ConfigDir`.
type Workspace struct {
	Home      string // $HOME aislado
	ConfigDir string // = $XDG_CONFIG_HOME/enu — aquí van enu.toml/providers.toml/agent.toml
	DataDir   string // = $XDG_DATA_HOME/enu — aquí escribe el binario (log, sesiones)
	Workdir   string // cwd por defecto del proceso (enu.fs.cwd())

	xdgConfig string // $XDG_CONFIG_HOME (padre de ConfigDir)
	xdgData   string // $XDG_DATA_HOME (padre de DataDir)
}

// NewWorkspace crea el árbol de directorios aislado y lo registra para limpieza. No
// escribe ninguna config: un `enu.toml` ausente = ningún plugin activo (arranque
// desnudo), que es lo que quieren los tests de flags puros.
func NewWorkspace(t *testing.T) *Workspace {
	t.Helper()
	base := t.TempDir()
	w := &Workspace{
		Home:      filepath.Join(base, "home"),
		xdgConfig: filepath.Join(base, "config"),
		xdgData:   filepath.Join(base, "data"),
		Workdir:   filepath.Join(base, "work"),
	}
	w.ConfigDir = filepath.Join(w.xdgConfig, "enu")
	w.DataDir = filepath.Join(w.xdgData, "enu")
	for _, d := range []string{w.Home, w.ConfigDir, w.DataDir, w.Workdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("crear dir del workspace %s: %v", d, err)
		}
	}
	return w
}

// baseEnv arma el entorno HERMÉTICO del proceso: HOME/XDG aislados, un PATH mínimo
// heredado (por si el agente lanza `bash`), un TERM para el modo interactivo, y la
// API key del provider fake (inofensiva si el test no usa provider). El llamante añade
// lo suyo con `RunOpts.Env`, que gana sobre esta base (va después).
func (w *Workspace) baseEnv() []string {
	env := []string{
		"HOME=" + w.Home,
		"XDG_CONFIG_HOME=" + w.xdgConfig,
		"XDG_DATA_HOME=" + w.xdgData,
		"TERM=xterm-256color",
		FakeAPIKeyEnv + "=" + FakeAPIKey,
	}
	if path := os.Getenv("PATH"); path != "" {
		env = append(env, "PATH="+path)
	}
	return env
}

// --- Helpers de config -----------------------------------------------------------

// WriteConfig escribe un fichero en el CONFIG dir (ConfigDir/name). Úsalo para
// cualquier config a mano; los envoltorios de abajo cubren los casos comunes.
func (w *Workspace) WriteConfig(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w.ConfigDir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("escribir %s: %v", name, err)
	}
}

// WriteFile escribe un fichero relativo al WORKDIR del proceso (crea subdirs). Sirve
// para preparar ficheros que una tool del agente leerá/tocará (read_file, etc.).
func (w *Workspace) WriteFile(t *testing.T, rel, content string) string {
	t.Helper()
	p := filepath.Join(w.Workdir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("crear dir de %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("escribir %s: %v", rel, err)
	}
	return p
}

// WriteEnuToml escribe `enu.toml` con `plugins.enabled = [...]`. Sin llamarla, no hay
// enu.toml y el arranque es desnudo (ningún plugin). Para el conjunto de producto
// (providers/sessions/agent) pásalos aquí.
func (w *Workspace) WriteEnuToml(t *testing.T, plugins ...string) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("[plugins]\nenabled = [")
	for i, p := range plugins {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("\"" + p + "\"")
	}
	b.WriteString("]\n")
	w.WriteConfig(t, "enu.toml", b.String())
}

// WriteAgentToml escribe `agent.toml` con el `model` por defecto del turno ("prov/id").
// El modo agente (`-p`) lo usa cuando no se pasa `--model`.
func (w *Workspace) WriteAgentToml(t *testing.T, model string) {
	t.Helper()
	w.WriteConfig(t, "agent.toml", "model = \""+model+"\"\n")
}

// UseFakeProvider cablea el WORKSPACE para hablar con el provider FAKE por el
// adaptador `anthropic` REAL: escribe un `providers.toml` cuyo `base_url` apunta al
// servidor de `fp`, con un provider `anthropic` y un modelo con alias `opus`; y un
// `agent.toml` con `model="anthropic/opus"`. Deja listo el turno headless (`-p`)
// contra el fake. NO escribe `enu.toml`: llama antes a `WriteEnuToml("providers",
// "sessions", "agent")`.
func (w *Workspace) UseFakeProvider(t *testing.T, fp *FakeProvider) {
	t.Helper()
	toml := "" +
		"[providers.anthropic]\n" +
		"adapter     = \"anthropic\"\n" +
		"base_url    = \"" + fp.URL() + "\"\n" +
		"api_key_env = \"" + FakeAPIKeyEnv + "\"\n\n" +
		"[[providers.anthropic.models]]\n" +
		"id         = \"claude-e2e\"\n" +
		"context    = 200000\n" +
		"max_output = 4096\n" +
		"aliases    = [\"opus\"]\n"
	w.WriteConfig(t, "providers.toml", toml)
	w.WriteAgentToml(t, "anthropic/opus")
}

// --- Lanzador de proceso ---------------------------------------------------------

// RunOpts parametriza una ejecución del binario. Todo tiene default razonable: sin
// Args ejecuta `enu` pelado; Dir vacío = Workdir del workspace; Timeout 0 = 30 s.
type RunOpts struct {
	Args    []string      // argumentos del binario (p. ej. []string{"-e", "return 1"})
	Env     []string      // entorno EXTRA (KEY=VALUE); gana sobre la base hermética
	Stdin   string        // stdin del proceso (vacío = /dev/null)
	Dir     string        // cwd del proceso (vacío = Workspace.Workdir)
	Timeout time.Duration // límite (0 = 30 s); al superarlo se mata y TimedOut=true
}

// Result es el resultado de una ejecución NO interactiva: salidas capturadas y el
// código de salida (0/1/2/3, la convención de S45). TimedOut marca que se mató por
// tiempo (ExitCode entonces es -1).
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

// Run lanza el binario SIN TTY (stdin/stdout son tuberías): el caso headless/CI, en el
// que `enu` sin acción sale con código 2. Captura stdout/stderr por separado y devuelve
// el código de salida. Un fallo de arranque del proceso (binario ausente, etc.) es
// t.Fatalf; un exit != 0 del binario NO lo es (es dato del test).
func (w *Workspace) Run(t *testing.T, opts RunOpts) Result {
	t.Helper()
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	dir := opts.Dir
	if dir == "" {
		dir = w.Workdir
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, enuBin, opts.Args...)
	cmd.Dir = dir
	cmd.Env = append(w.baseEnv(), opts.Env...)
	if opts.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(opts.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res
	}
	if err == nil {
		res.ExitCode = 0
		return res
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res
	}
	// Un error que NO es exit-status (no arrancó, permisos del binario…) es fatal:
	// no es un resultado que el test pueda interpretar.
	t.Fatalf("Run: el proceso no arrancó: %v (stderr=%q)", err, res.Stderr)
	return res
}
