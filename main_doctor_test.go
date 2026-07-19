package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbareagimeno/enu/internal/runtime"
)

// Tests de `enu doctor` (S50, ADR-026 pieza 3; estrechado por G62 a los 7 checks
// kernel). Cubren el inventario 🔒: cada check kernel verde+rojo, exit 0/1 con skips,
// la clave JAMÁS en ninguna salida, `--json` conforme a `doctor.v1`, y los 4 checks de
// producto en `skip`.
//
// NOTA de construcción: `runtime.New()` lee `enu.toml` UNA vez (puebla `l.enabled`),
// así que un test que dependa de `plugins.enabled` debe escribir `enu.toml` ANTES de
// crear el runtime — igual que en producción `enu doctor` arranca un proceso fresco
// con el `enu.toml` ya en disco. Los checks que leen en vivo (`config.parse`,
// `sessions.perms`) no dependen del orden.

func mkDoctorRuntime(t *testing.T, cfg, data string) *runtime.Runtime {
	t.Helper()
	rt := runtime.New(runtime.WithDataDir(data), runtime.WithConfigDir(cfg), runtime.WithForceUI(false))
	t.Cleanup(rt.Close)
	return rt
}

// writeHealthyConfig deja una config de producto válida en cfg (vía un runtime
// desechable) para los escenarios "sanos": enu.toml con el conjunto oficial + las
// plantillas de agent/providers.
func writeHealthyConfig(t *testing.T, cfg, data string) {
	t.Helper()
	tmp := runtime.New(runtime.WithDataDir(data), runtime.WithConfigDir(cfg), runtime.WithForceUI(false))
	if _, _, _, err := tmp.WriteDefaultConfig(); err != nil {
		t.Fatalf("preparar config sana: %v", err)
	}
	tmp.Close()
}

func runDoctorJSON(t *testing.T, rt *runtime.Runtime, opts doctorOpts) (doctorReport, int) {
	t.Helper()
	opts.json = true
	var buf bytes.Buffer
	code := runDoctor(rt, opts, &buf)
	var rep doctorReport
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("--json no es JSON válido: %v\n%s", err, buf.String())
	}
	return rep, code
}

func findCheck(t *testing.T, rep doctorReport, id string) doctorCheck {
	t.Helper()
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %q ausente del informe", id)
	return doctorCheck{}
}

// Config de producto sana: los 7 kernel `ok`/`skip`, los 4 de producto `skip`, exit 0.
// Esquema `doctor.v1`, counts cuadran, y plugins.enabled/requires OK con el conjunto
// oficial (se valida de verdad porque enu.toml se escribe ANTES del runtime).
func TestDoctorHealthyIsExitZeroAndSchemaValid(t *testing.T) {
	cfg, data := t.TempDir(), t.TempDir()
	writeHealthyConfig(t, cfg, data)
	rt := mkDoctorRuntime(t, cfg, data)

	rep, code := runDoctorJSON(t, rt, doctorOpts{stdoutTTY: false})
	if code != exitOK {
		t.Fatalf("config sana debe salir 0; got %d (checks=%+v)", code, rep.Checks)
	}
	if rep.Schema != "doctor.v1" {
		t.Fatalf("schema: got %q, want doctor.v1", rep.Schema)
	}
	if len(rep.Checks) != 11 {
		t.Fatalf("esperados 11 checks en el catálogo; got %d", len(rep.Checks))
	}
	if rep.Counts.Fail != 0 {
		t.Fatalf("config sana no debe tener fails; counts=%+v", rep.Counts)
	}
	if rep.ExitCode != code {
		t.Fatalf("exit_code del JSON (%d) != código real (%d)", rep.ExitCode, code)
	}
	if sum := rep.Counts.OK + rep.Counts.Fail + rep.Counts.Skip; sum != len(rep.Checks) {
		t.Fatalf("counts (%+v) no suman el nº de checks (%d)", rep.Counts, len(rep.Checks))
	}
	if c := findCheck(t, rep, "plugins.enabled"); c.Status != statusOKd {
		t.Fatalf("con el conjunto oficial, plugins.enabled debe ser ok; got %q (%v)", c.Status, c.Detail)
	}
	if c := findCheck(t, rep, "plugins.requires"); c.Status != statusOKd {
		t.Fatalf("con el conjunto oficial, plugins.requires debe ser ok; got %q (%v)", c.Status, c.Detail)
	}
}

// Los 4 checks de producto salen `skip` en v1 (G62), nunca `ok` fabricado.
func TestDoctorProductChecksAreSkip(t *testing.T) {
	rt := mkDoctorRuntime(t, t.TempDir(), t.TempDir())
	rep, _ := runDoctorJSON(t, rt, doctorOpts{})
	for _, id := range []string{"provider.model", "provider.key", "tools.external", "provider.reach"} {
		if c := findCheck(t, rep, id); c.Status != statusSkipd {
			t.Fatalf("%s debe ser skip en v1 (G62); got %q", id, c.Status)
		}
	}
}

// 🔒 plugins.enabled: un plugin activado inexistente → fail, exit 1, remedy accionable.
func TestDoctorPluginsEnabledFail(t *testing.T) {
	cfg, data := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "enu.toml"),
		[]byte("[plugins]\nenabled = [\"no_existe_este_plugin\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := mkDoctorRuntime(t, cfg, data)

	rep, code := runDoctorJSON(t, rt, doctorOpts{})
	if code != exitError {
		t.Fatalf("un plugin inexistente debe dar exit 1; got %d", code)
	}
	c := findCheck(t, rep, "plugins.enabled")
	if c.Status != statusFaild || c.Remedy == nil {
		t.Fatalf("plugins.enabled debe ser fail con remedy; got %+v", c)
	}
}

// 🔒 config.parse: un TOML roto → fail nombrando el fichero; los sanos no lo tumban.
func TestDoctorConfigParseFail(t *testing.T) {
	cfg, data := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "agent.toml"), []byte("esto no [[[ es toml = =\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := mkDoctorRuntime(t, cfg, data)

	rep, code := runDoctorJSON(t, rt, doctorOpts{})
	if code != exitError {
		t.Fatalf("un TOML roto debe dar exit 1; got %d", code)
	}
	c := findCheck(t, rep, "config.parse")
	if c.Status != statusFaild || c.Remedy == nil || !strings.Contains(*c.Remedy, "agent.toml") {
		t.Fatalf("config.parse debe fallar nombrando agent.toml; got %+v", c)
	}
}

// 🔒 sessions.perms: un transcript en 0644 → fail (G57); en 0600 → ok; ninguno → skip.
func TestDoctorSessionsPerms(t *testing.T) {
	t.Run("0644_falla", func(t *testing.T) {
		cfg, data := t.TempDir(), t.TempDir()
		sdir := filepath.Join(data, "sessions", "proj")
		if err := os.MkdirAll(sdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sdir, "s1.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rt := mkDoctorRuntime(t, cfg, data)
		rep, code := runDoctorJSON(t, rt, doctorOpts{})
		if code != exitError {
			t.Fatalf("un transcript 0644 debe dar exit 1; got %d", code)
		}
		if c := findCheck(t, rep, "sessions.perms"); c.Status != statusFaild {
			t.Fatalf("sessions.perms debe fallar; got %q", c.Status)
		}
	})
	t.Run("0600_ok", func(t *testing.T) {
		cfg, data := t.TempDir(), t.TempDir()
		sdir := filepath.Join(data, "sessions", "proj")
		if err := os.MkdirAll(sdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sdir, "s1.jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rt := mkDoctorRuntime(t, cfg, data)
		rep, _ := runDoctorJSON(t, rt, doctorOpts{})
		if c := findCheck(t, rep, "sessions.perms"); c.Status != statusOKd {
			t.Fatalf("sessions.perms debe ser ok; got %q (%v)", c.Status, c.Detail)
		}
	})
	t.Run("sin_sesiones_skip", func(t *testing.T) {
		rt := mkDoctorRuntime(t, t.TempDir(), t.TempDir())
		rep, _ := runDoctorJSON(t, rt, doctorOpts{})
		if c := findCheck(t, rep, "sessions.perms"); c.Status != statusSkipd {
			t.Fatalf("sin sesiones, sessions.perms debe ser skip; got %q", c.Status)
		}
	})
}

// tty.caps: skip en headless, ok con TTY.
func TestDoctorTTYCaps(t *testing.T) {
	rt := mkDoctorRuntime(t, t.TempDir(), t.TempDir())
	repH, _ := runDoctorJSON(t, rt, doctorOpts{stdoutTTY: false})
	if c := findCheck(t, repH, "tty.caps"); c.Status != statusSkipd {
		t.Fatalf("headless: tty.caps debe ser skip; got %q", c.Status)
	}
	repT, _ := runDoctorJSON(t, rt, doctorOpts{stdoutTTY: true})
	if c := findCheck(t, repT, "tty.caps"); c.Status != statusOKd {
		t.Fatalf("con TTY: tty.caps debe ser ok; got %q", c.Status)
	}
}

// provider.reach: sin --net skip "requiere --net"; el flag no lo vuelve ok (G62).
func TestDoctorNetFlag(t *testing.T) {
	rt := mkDoctorRuntime(t, t.TempDir(), t.TempDir())
	repNo, _ := runDoctorJSON(t, rt, doctorOpts{net: false})
	c := findCheck(t, repNo, "provider.reach")
	if c.Status != statusSkipd || c.Detail == nil || !strings.Contains(*c.Detail, "--net") {
		t.Fatalf("sin --net, provider.reach skip mencionando --net; got %+v", c)
	}
	repNet, _ := runDoctorJSON(t, rt, doctorOpts{net: true})
	if c := findCheck(t, repNet, "provider.reach"); c.Status != statusSkipd {
		t.Fatalf("con --net sigue siendo skip en v1 (G62); got %q", c.Status)
	}
}

// 🔒 la clave JAMÁS aparece en ninguna salida (humana ni --json), aunque esté
// exportada: los checks kernel no la leen y los de producto son skip.
func TestDoctorKeyNeverInOutput(t *testing.T) {
	const secret = "sk-doctor-secret-xyz789"
	t.Setenv("ANTHROPIC_API_KEY", secret)
	cfg, data := t.TempDir(), t.TempDir()
	writeHealthyConfig(t, cfg, data)
	rt := mkDoctorRuntime(t, cfg, data)

	for _, jsonOut := range []bool{false, true} {
		var buf bytes.Buffer
		runDoctor(rt, doctorOpts{json: jsonOut}, &buf)
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("la salida (json=%v) contiene el valor de la clave:\n%s", jsonOut, buf.String())
		}
	}
}

// Uso inválido de doctor (argumento de más) → exit 2, vía el dispatch real.
func TestDoctorUsageExit2(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var handled bool
	var code int
	captureOutput(t, func() { handled, code = dispatchSubcommand([]string{"doctor", "argumento-de-mas"}) })
	if !handled || code != exitUsage {
		t.Fatalf("doctor con argumento de más: handled=%v code=%d, want handled=true code=%d", handled, code, exitUsage)
	}
}
