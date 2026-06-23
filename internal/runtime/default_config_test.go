package runtime

// Tests que BLINDAN G33 (ADR-015) a nivel de runtime: el conjunto oficial de producto
// (`officialProductSet`), la activación EFÍMERA en memoria (`WithEnabledPlugins`) y la
// escritura PERSISTENTE (`WriteDefaultConfig`). Los del flag de CLI viven en main_test.go.

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestOfficialProductSetExcludeExample blinda la pieza 2 de G33: el conjunto oficial
// de producto son las embebidas DISPONIBLES menos el andamiaje `example`. Sin él, el
// flag escribiría el plugin de pruebas en la config del usuario.
func TestOfficialProductSetExcludeExample(t *testing.T) {
	all, err := embeddedNames()
	if err != nil {
		t.Fatalf("embeddedNames: %v", err)
	}
	product, err := officialProductSet()
	if err != nil {
		t.Fatalf("officialProductSet: %v", err)
	}

	// `example` está en el catálogo embebido pero NO en el conjunto de producto.
	if !contains(all, "example") {
		t.Fatalf("precondición: `example` debería estar entre las embebidas; got %v", all)
	}
	if contains(product, "example") {
		t.Fatalf("el conjunto de producto NO debe incluir `example`; got %v", product)
	}

	// El conjunto de producto = catálogo - {example}, sin perder ni añadir nada más.
	if len(product) != len(all)-1 {
		t.Fatalf("el conjunto de producto debería ser el catálogo menos 1 (example); all=%v product=%v", all, product)
	}
	for _, n := range product {
		if !contains(all, n) {
			t.Fatalf("el conjunto de producto nombra %q, que no está embebido (all=%v)", n, all)
		}
	}

	// Las de producto reales que esperamos (Fase 8): si alguna falta, el conjunto
	// quedó incompleto. (No fija el orden: `fs.ReadDir` no lo garantiza.)
	for _, want := range []string{"providers", "sessions", "agent", "mcp", "chat", "repl", "toolkit"} {
		if !contains(product, want) {
			t.Fatalf("el conjunto de producto debería incluir %q; got %v", want, product)
		}
	}
}

// TestWithEnabledPluginsEphemeral blinda el modo EFÍMERO: `WithEnabledPlugins` fija la
// activación EN MEMORIA sin escribir `nu.toml`, ganando sobre el fichero (o su ausencia).
func TestWithEnabledPluginsEphemeral(t *testing.T) {
	cfg := t.TempDir()
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg),
		WithEnabledPlugins([]string{"example"}))
	t.Cleanup(rt.Close)

	// No hay `nu.toml` previo y el override NO debe crear uno (es en memoria).
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot con override falló: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg, nuTomlName)); !os.IsNotExist(err) {
		t.Fatalf("el modo efímero NO debe escribir nu.toml; err=%v", err)
	}

	// La extensión inyectada en memoria se cargó (source=builtin).
	h := &harness{t: t, rt: rt}
	if src := listSource(h, "example"); src != string(sourceBuiltin) {
		t.Fatalf("la extensión del override debería estar activa (builtin); source=%q", src)
	}
}

// TestWithEnabledPluginsOverridesToml blinda que el override (efímero) GANA sobre un
// `nu.toml` existente: el fichero queda intacto, pero la activación es la del override.
func TestWithEnabledPluginsOverridesToml(t *testing.T) {
	cfg := t.TempDir()
	// Un `nu.toml` que NO activa example, con una clave ajena que debe sobrevivir.
	if err := os.WriteFile(filepath.Join(cfg, nuTomlName),
		[]byte("[watchdog]\nslice_budget_ms = 250\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg),
		WithEnabledPlugins([]string{"example"}))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	// El fichero en disco NO cambió (override es solo memoria).
	data, _ := os.ReadFile(filepath.Join(cfg, nuTomlName))
	if string(data) != "[watchdog]\nslice_budget_ms = 250\n" {
		t.Fatalf("el override no debe tocar nu.toml; quedó:\n%s", data)
	}
	// Pero example está activo pese a no estar en el fichero.
	h := &harness{t: t, rt: rt}
	if src := listSource(h, "example"); src != string(sourceBuiltin) {
		t.Fatalf("el override debería activar example; source=%q", src)
	}
}

// TestWriteDefaultConfigPersistent blinda el modo PERSISTENTE: `WriteDefaultConfig`
// escribe el conjunto de producto en `nu.toml`, devuelve qué escribió y dónde, y es
// idempotente.
func TestWriteDefaultConfigPersistent(t *testing.T) {
	rt, cfg := newBareRuntime(t)

	dir, names, err := rt.WriteDefaultConfig()
	if err != nil {
		t.Fatalf("WriteDefaultConfig: %v", err)
	}
	if dir != cfg {
		t.Fatalf("configDir devuelto %q != %q", dir, cfg)
	}
	if contains(names, "example") {
		t.Fatalf("el conjunto escrito no debe incluir example; got %v", names)
	}

	// Lo escrito coincide con `plugins.enabled` del fichero.
	cfgData, err := loadNuTomlForTest(cfg)
	if err != nil {
		t.Fatalf("releer nu.toml: %v", err)
	}
	got := append([]string(nil), cfgData.Plugins.Enabled...)
	want := append([]string(nil), names...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("plugins.enabled %v != names devueltos %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("plugins.enabled %v != names %v", got, want)
		}
	}

	// Idempotente: una segunda escritura deja el fichero igual.
	before, _ := os.ReadFile(filepath.Join(cfg, nuTomlName))
	if _, _, err := rt.WriteDefaultConfig(); err != nil {
		t.Fatalf("segunda WriteDefaultConfig: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(cfg, nuTomlName))
	if string(before) != string(after) {
		t.Fatalf("WriteDefaultConfig no es idempotente:\nantes:\n%s\ndespués:\n%s", before, after)
	}
}

// TestWriteDefaultConfigPreservesAndRejectsMalformed blinda dos garantías heredadas de
// `writeEnabledPlugins`: (a) preserva claves ajenas del `nu.toml`; (b) un `nu.toml` mal
// formado NO se sobrescribe (error accionable).
func TestWriteDefaultConfigPreservesAndRejectsMalformed(t *testing.T) {
	t.Run("preserva config existente", func(t *testing.T) {
		cfg := t.TempDir()
		if err := os.WriteFile(filepath.Join(cfg, nuTomlName),
			[]byte("[watchdog]\nslice_budget_ms = 250\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg))
		t.Cleanup(rt.Close)
		if _, _, err := rt.WriteDefaultConfig(); err != nil {
			t.Fatalf("WriteDefaultConfig: %v", err)
		}
		cfgData, err := loadNuTomlForTest(cfg)
		if err != nil {
			t.Fatalf("releer: %v", err)
		}
		if cfgData.Watchdog.SliceBudgetMs == nil || *cfgData.Watchdog.SliceBudgetMs != 250 {
			t.Fatal("el watchdog ajeno se perdió al escribir plugins.enabled")
		}
		if len(cfgData.Plugins.Enabled) == 0 {
			t.Fatal("plugins.enabled no se escribió")
		}
	})

	t.Run("nu.toml mal formado no se sobrescribe", func(t *testing.T) {
		cfg := t.TempDir()
		bad := "esto no es toml = = [[[\n"
		if err := os.WriteFile(filepath.Join(cfg, nuTomlName), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg))
		t.Cleanup(rt.Close)
		_, _, err := rt.WriteDefaultConfig()
		if err == nil {
			t.Fatal("WriteDefaultConfig debería fallar ante un nu.toml mal formado")
		}
		// El fichero roto sigue intacto (no se pisó la config del usuario).
		data, _ := os.ReadFile(filepath.Join(cfg, nuTomlName))
		if string(data) != bad {
			t.Fatalf("el nu.toml mal formado NO debe sobrescribirse; quedó:\n%s", data)
		}
	})
}

// contains es un helper local (los tests de este fichero comparan slices de nombres).
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
