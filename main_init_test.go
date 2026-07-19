package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbareagimeno/enu/internal/runtime"
)

// Tests de `enu init` (S49, ADR-026 pieza 2) y del dispatch de subcomandos (pieza 1).
// Sesión de código: cubren el inventario 🔒 de S49 —nunca sobrescribe, equivalencia
// byte a byte con --default-config, códigos de salida, la clave jamás en fichero— más
// el dispatch y el wizard (anthropic-only, G61).

func newInitTestRuntime(t *testing.T) (*runtime.Runtime, string) {
	t.Helper()
	cfg := t.TempDir()
	rt := runtime.New(runtime.WithDataDir(t.TempDir()), runtime.WithConfigDir(cfg), runtime.WithForceUI(false))
	t.Cleanup(rt.Close)
	return rt, cfg
}

func readFileT(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leer %s: %v", path, err)
	}
	return data
}

// 🔒 S49 — `init --yes`/sin-TTY produce ficheros IDÉNTICOS byte a byte a
// `--default-config`: la equivalencia de ADR-026 pieza 2 es un contrato, no una
// intención. Se blinda comparando los tres ficheros de dos config.dir separados.
func TestInitYesEqualsDefaultConfigBytewise(t *testing.T) {
	rtA, cfgA := newInitTestRuntime(t)
	var outA bytes.Buffer
	if code := runInit(rtA, true /*yes*/, false /*isTTY*/, strings.NewReader(""), &outA); code != exitOK {
		t.Fatalf("init --yes: code=%d out=%q", code, outA.String())
	}

	rtB, cfgB := newInitTestRuntime(t)
	captureOutput(t, func() {
		if code := runDefaultConfig(rtB); code != exitOK {
			t.Fatalf("--default-config: code=%d", code)
		}
	})

	for _, f := range []string{"enu.toml", "agent.toml", "providers.toml"} {
		a := readFileT(t, filepath.Join(cfgA, f))
		b := readFileT(t, filepath.Join(cfgB, f))
		if !bytes.Equal(a, b) {
			t.Fatalf("%s difiere entre `init --yes` y `--default-config`:\n--- init ---\n%s\n--- default ---\n%s", f, a, b)
		}
	}
}

// 🔒 S49 — el fallo de borde nombrado en el inventario: `enu init` JAMÁS sobrescribe un
// fichero de config existente (pérdida silenciosa de config del usuario). Se prueba en
// el modo sin-TTY (--yes); el wizard comparte la misma escritura por-fichero.
func TestInitNeverOverwritesExistingConfig(t *testing.T) {
	rt, cfg := newInitTestRuntime(t)
	custom := "# MI CONFIG PERSONAL — NO TOCAR\nmodel = \"openai/gpt-x\"\n"
	if err := os.WriteFile(filepath.Join(cfg, "agent.toml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := runInit(rt, true, false, strings.NewReader(""), &out); code != exitOK {
		t.Fatalf("code=%d", code)
	}

	if got := readFileT(t, filepath.Join(cfg, "agent.toml")); string(got) != custom {
		t.Fatalf("agent.toml se sobrescribió (pérdida silenciosa):\n%s", got)
	}
	// providers.toml SÍ se crea (faltaba): semántica por-fichero.
	if _, err := os.Stat(filepath.Join(cfg, "providers.toml")); err != nil {
		t.Fatalf("providers.toml debía crearse (faltaba): %v", err)
	}
}

// S49 — semántica por-fichero: con solo `enu.toml` presente, escribe agent.toml y
// providers.toml (los que faltan) y respeta el existente.
func TestInitPartialConfigWritesOnlyMissing(t *testing.T) {
	rt, cfg := newInitTestRuntime(t)
	enuC := "# mi enu.toml\n[algo]\nclave = 1\n"
	if err := os.WriteFile(filepath.Join(cfg, "enu.toml"), []byte(enuC), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runInit(rt, true, false, strings.NewReader(""), &out); code != exitOK {
		t.Fatalf("code=%d", code)
	}
	// enu.toml conserva la clave del usuario (writeEnabledPlugins preserva el resto).
	if got := readFileT(t, filepath.Join(cfg, "enu.toml")); !strings.Contains(string(got), "clave = 1") {
		t.Fatalf("enu.toml perdió la clave del usuario:\n%s", got)
	}
	for _, f := range []string{"agent.toml", "providers.toml"} {
		if _, err := os.Stat(filepath.Join(cfg, f)); err != nil {
			t.Fatalf("%s (faltaba) debía crearse: %v", f, err)
		}
	}
}

// S49 — con las plantillas ya presentes, `init` es un no-op honesto (lo dice, sale 0),
// sin tocarlas.
func TestInitNoOpTemplatesWhenPresent(t *testing.T) {
	rt, cfg := newInitTestRuntime(t)
	agentC := "# mío\nmodel = \"x/y\"\n"
	provC := "# míos providers\n"
	if err := os.WriteFile(filepath.Join(cfg, "agent.toml"), []byte(agentC), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(provC), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runInit(rt, true, false, strings.NewReader(""), &out); code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if string(readFileT(t, filepath.Join(cfg, "agent.toml"))) != agentC {
		t.Fatal("agent.toml se sobrescribió")
	}
	if string(readFileT(t, filepath.Join(cfg, "providers.toml"))) != provC {
		t.Fatal("providers.toml se sobrescribió")
	}
	if !strings.Contains(out.String(), "no-op") {
		t.Fatalf("el mensaje debería declarar no-op; got %q", out.String())
	}
}

// 🔒 S49 (adyacente, providers.md §1) — la clave JAMÁS se escribe en fichero ni se
// imprime, aunque el wizard la detecte en el entorno. Solo `api_key_env`.
func TestInitNeverWritesApiKey(t *testing.T) {
	const secret = "sk-super-secret-abc123"
	t.Setenv("ANTHROPIC_API_KEY", secret)
	rt, cfg := newInitTestRuntime(t)

	var out bytes.Buffer
	// wizard (TTY): modelo por defecto (enter) + activar oficial (S).
	if code := runInit(rt, false, true, strings.NewReader("\nS\n"), &out); code != exitOK {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
	for _, f := range []string{"enu.toml", "agent.toml", "providers.toml"} {
		if strings.Contains(string(readFileT(t, filepath.Join(cfg, f))), secret) {
			t.Fatalf("%s contiene el VALOR de la clave", f)
		}
	}
	if strings.Contains(out.String(), secret) {
		t.Fatalf("stdout imprimió el valor de la clave: %q", out.String())
	}
	if prov := readFileT(t, filepath.Join(cfg, "providers.toml")); !strings.Contains(string(prov), "api_key_env") {
		t.Fatalf("providers.toml debe referenciar api_key_env; got:\n%s", prov)
	}
}

// S49 — el wizard con el modelo por defecto (enter) produce el MISMO agent.toml que
// --default-config: `agentTomlFor("anthropic/opus")` == la plantilla por defecto.
func TestInitWizardDefaultModelMatchesDefaultConfig(t *testing.T) {
	rtW, cfgW := newInitTestRuntime(t)
	var out bytes.Buffer
	if code := runInit(rtW, false, true, strings.NewReader("\nS\n"), &out); code != exitOK {
		t.Fatalf("wizard: code=%d", code)
	}
	rtD, cfgD := newInitTestRuntime(t)
	captureOutput(t, func() { runDefaultConfig(rtD) })

	if a, b := readFileT(t, filepath.Join(cfgW, "agent.toml")), readFileT(t, filepath.Join(cfgD, "agent.toml")); !bytes.Equal(a, b) {
		t.Fatalf("agent.toml del wizard (modelo default) difiere del de --default-config:\n%s\n---\n%s", a, b)
	}
}

// S49 — el wizard usa el modelo TECLEADO si el usuario lo sobrescribe.
func TestInitWizardModelOverride(t *testing.T) {
	rt, cfg := newInitTestRuntime(t)
	var out bytes.Buffer
	if code := runInit(rt, false, true, strings.NewReader("anthropic/sonnet\nS\n"), &out); code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if agent := readFileT(t, filepath.Join(cfg, "agent.toml")); !strings.Contains(string(agent), "anthropic/sonnet") {
		t.Fatalf("agent.toml no usó el modelo tecleado:\n%s", agent)
	}
}

// S49 — declinar el conjunto oficial escribe las plantillas pero NO activa plugins.
func TestInitWizardDeclineOfficial(t *testing.T) {
	rt, cfg := newInitTestRuntime(t)
	var out bytes.Buffer
	if code := runInit(rt, false, true, strings.NewReader("\nn\n"), &out); code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if _, err := os.Stat(filepath.Join(cfg, "agent.toml")); err != nil {
		t.Fatalf("agent.toml debía escribirse: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(cfg, "enu.toml")); err == nil {
		if strings.Contains(string(data), "providers") {
			t.Fatalf("declinar no debe activar el conjunto oficial:\n%s", data)
		}
	}
	if !strings.Contains(out.String(), "NO activado") {
		t.Fatalf("el mensaje debería decir que no se activó; got %q", out.String())
	}
}

// S49 — un EOF a mitad del wizard aborta SIN escribir ninguna config y con código != 0.
func TestInitWizardEOFAborts(t *testing.T) {
	rt, cfg := newInitTestRuntime(t)
	var out bytes.Buffer
	code := runInit(rt, false, true, strings.NewReader(""), &out) // EOF inmediato
	if code == exitOK {
		t.Fatalf("EOF debe abortar con código no cero; got %d", code)
	}
	for _, f := range []string{"enu.toml", "agent.toml", "providers.toml"} {
		if _, err := os.Stat(filepath.Join(cfg, f)); err == nil {
			t.Fatalf("%s no debía escribirse tras abortar", f)
		}
	}
}

// S49 (ADR-026 pieza 1) — dispatch: enruta los verbos de gestión, veta el producto por
// la regla de frontera, y NO intercepta los flags legados.
func TestDispatchSubcommandRouting(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantHandled bool
		wantCode    int
	}{
		{"producto_chat_vetado", []string{"chat"}, true, exitUsage},
		{"producto_run_vetado", []string{"run"}, true, exitUsage},
		{"desconocido", []string{"frobnicate"}, true, exitUsage},
		// `init`/`doctor`/`update`/`uninstall` construyen un Runtime real o tienen
		// efectos (uninstall borraría os.Executable()): su enrutado se prueba aparte
		// con costuras inyectables (TestDispatchInitYesWritesConfig, TestDoctorUsageExit2,
		// TestUpdate*, TestUninstall*). Aquí solo el veto de frontera y los no-subcomandos.
		{"flag_e_no_es_subcomando", []string{"-e", "return 1"}, false, exitOK},
		{"sin_args", []string{}, false, exitOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var handled bool
			var code int
			captureOutput(t, func() { handled, code = dispatchSubcommand(c.args) })
			if handled != c.wantHandled {
				t.Fatalf("handled: got %v, want %v", handled, c.wantHandled)
			}
			if handled && code != c.wantCode {
				t.Fatalf("code: got %d, want %d", code, c.wantCode)
			}
		})
	}
}

// S49 — el mensaje de un subcomando de producto cita la regla de frontera (ADR-026).
func TestDispatchProductSubcommandCitesFrontera(t *testing.T) {
	_, stderr := captureOutput(t, func() { dispatchSubcommand([]string{"chat"}) })
	if !strings.Contains(stderr, "producto") || !strings.Contains(stderr, "subcomando") {
		t.Fatalf("stderr debería explicar la regla de frontera; got %q", stderr)
	}
}

// S49 — integración: `enu init --yes` a través del dispatch escribe la config bajo
// XDG_CONFIG_HOME/enu (construye el Runtime real con dirs de entorno de prueba).
func TestDispatchInitYesWritesConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var handled bool
	var code int
	captureOutput(t, func() { handled, code = dispatchSubcommand([]string{"init", "--yes"}) })
	if !handled || code != exitOK {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
	if _, err := os.Stat(filepath.Join(tmp, "enu", "enu.toml")); err != nil {
		t.Fatalf("enu.toml no se escribió bajo XDG_CONFIG_HOME/enu: %v", err)
	}
}
