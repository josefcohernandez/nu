package runtime

// Tests que BLINDAN G33 (ADR-015) a nivel de runtime: el conjunto oficial de producto
// (`officialProductSet`), la activación EFÍMERA en memoria (`WithEnabledPlugins`) y la
// escritura PERSISTENTE (`WriteDefaultConfig`). Los del flag de CLI viven en main_test.go.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestOfficialProductSetExcludeExample blinda la pieza 2 de G33: el conjunto oficial
// de producto son las embebidas DISPONIBLES menos `nonProductEmbedded` — el
// andamiaje `example` y la malla `mesh` (malla.md §1.4: embebida pero de
// activación explícita). Sin esto, el flag escribiría plugins que el usuario no
// pidió en su config.
func TestOfficialProductSetExcludeExample(t *testing.T) {
	all, err := embeddedNames()
	if err != nil {
		t.Fatalf("embeddedNames: %v", err)
	}
	product, err := officialProductSet()
	if err != nil {
		t.Fatalf("officialProductSet: %v", err)
	}

	// Las excluidas están en el catálogo embebido pero NO en el conjunto de producto.
	for name := range nonProductEmbedded {
		if !contains(all, name) {
			t.Fatalf("precondición: %q debería estar entre las embebidas; got %v", name, all)
		}
		if contains(product, name) {
			t.Fatalf("el conjunto de producto NO debe incluir %q; got %v", name, product)
		}
	}

	// El conjunto de producto = catálogo - nonProductEmbedded, sin perder nada más.
	if len(product) != len(all)-len(nonProductEmbedded) {
		t.Fatalf("el conjunto de producto debería ser el catálogo menos las de nonProductEmbedded; all=%v product=%v", all, product)
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
// activación EN MEMORIA sin escribir `enu.toml`, ganando sobre el fichero (o su ausencia).
func TestWithEnabledPluginsEphemeral(t *testing.T) {
	cfg := t.TempDir()
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg),
		WithEnabledPlugins([]string{"example"}))
	t.Cleanup(rt.Close)

	// No hay `enu.toml` previo y el override NO debe crear uno (es en memoria).
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot con override falló: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg, nuTomlName)); !os.IsNotExist(err) {
		t.Fatalf("el modo efímero NO debe escribir enu.toml; err=%v", err)
	}

	// La extensión inyectada en memoria se cargó (source=builtin).
	h := &harness{t: t, rt: rt}
	if src := listSource(h, "example"); src != string(sourceBuiltin) {
		t.Fatalf("la extensión del override debería estar activa (builtin); source=%q", src)
	}
}

// TestWithEnabledPluginsOverridesToml blinda que el override (efímero) GANA sobre un
// `enu.toml` existente: el fichero queda intacto, pero la activación es la del override.
func TestWithEnabledPluginsOverridesToml(t *testing.T) {
	cfg := t.TempDir()
	// Un `enu.toml` que NO activa example, con una clave ajena que debe sobrevivir.
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
		t.Fatalf("el override no debe tocar enu.toml; quedó:\n%s", data)
	}
	// Pero example está activo pese a no estar en el fichero.
	h := &harness{t: t, rt: rt}
	if src := listSource(h, "example"); src != string(sourceBuiltin) {
		t.Fatalf("el override debería activar example; source=%q", src)
	}
}

// TestWriteDefaultConfigPersistent blinda el modo PERSISTENTE: `WriteDefaultConfig`
// escribe el conjunto de producto en `enu.toml`, devuelve qué escribió y dónde, y es
// idempotente.
func TestWriteDefaultConfigPersistent(t *testing.T) {
	rt, cfg := newBareRuntime(t)

	dir, names, createdTemplates, err := rt.WriteDefaultConfig()
	if err != nil {
		t.Fatalf("WriteDefaultConfig: %v", err)
	}
	if dir != cfg {
		t.Fatalf("configDir devuelto %q != %q", dir, cfg)
	}
	if contains(names, "example") {
		t.Fatalf("el conjunto escrito no debe incluir example; got %v", names)
	}
	// G35/ADR-017: en un config virgen, el onramp siembra ambas plantillas.
	if !contains(createdTemplates, agentTomlName) || !contains(createdTemplates, providersTomlName) {
		t.Fatalf("se esperaban %s y %s entre las plantillas creadas; got %v",
			agentTomlName, providersTomlName, createdTemplates)
	}

	// Lo escrito coincide con `plugins.enabled` del fichero.
	cfgData, err := loadNuTomlForTest(cfg)
	if err != nil {
		t.Fatalf("releer enu.toml: %v", err)
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

	// Idempotente: una segunda escritura deja los ficheros igual y NO reporta
	// plantillas creadas (ya existen).
	before, _ := os.ReadFile(filepath.Join(cfg, nuTomlName))
	beforeAgent, _ := os.ReadFile(filepath.Join(cfg, agentTomlName))
	_, _, created2, err := rt.WriteDefaultConfig()
	if err != nil {
		t.Fatalf("segunda WriteDefaultConfig: %v", err)
	}
	if len(created2) != 0 {
		t.Fatalf("la segunda llamada no debe crear plantillas (ya existen); got %v", created2)
	}
	after, _ := os.ReadFile(filepath.Join(cfg, nuTomlName))
	if string(before) != string(after) {
		t.Fatalf("WriteDefaultConfig no es idempotente:\nantes:\n%s\ndespués:\n%s", before, after)
	}
	afterAgent, _ := os.ReadFile(filepath.Join(cfg, agentTomlName))
	if string(beforeAgent) != string(afterAgent) {
		t.Fatalf("agent.toml no debe reescribirse en la segunda llamada")
	}
}

// TestWriteDefaultConfigPreservesAndRejectsMalformed blinda dos garantías heredadas de
// `writeEnabledPlugins`: (a) preserva claves ajenas del `enu.toml`; (b) un `enu.toml` mal
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
		if _, _, _, err := rt.WriteDefaultConfig(); err != nil {
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

	t.Run("enu.toml mal formado no se sobrescribe", func(t *testing.T) {
		cfg := t.TempDir()
		bad := "esto no es toml = = [[[\n"
		if err := os.WriteFile(filepath.Join(cfg, nuTomlName), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg))
		t.Cleanup(rt.Close)
		_, _, _, err := rt.WriteDefaultConfig()
		if err == nil {
			t.Fatal("WriteDefaultConfig debería fallar ante un enu.toml mal formado")
		}
		// El fichero roto sigue intacto (no se pisó la config del usuario).
		data, _ := os.ReadFile(filepath.Join(cfg, nuTomlName))
		if string(data) != bad {
			t.Fatalf("el enu.toml mal formado NO debe sobrescribirse; quedó:\n%s", data)
		}
	})
}

// TestWriteDefaultConfigSeedsAgentTemplates blinda G35/ADR-017: el onramp deja config
// de agente USABLE —`agent.toml` con `model`, `providers.toml` con `api_key_env`—,
// escrita SOLO si no existe y sin pisar jamás un fichero del usuario.
func TestWriteDefaultConfigSeedsAgentTemplates(t *testing.T) {
	t.Run("siembra plantillas válidas en config virgen", func(t *testing.T) {
		rt, cfg := newBareRuntime(t)
		_, _, created, err := rt.WriteDefaultConfig()
		if err != nil {
			t.Fatalf("WriteDefaultConfig: %v", err)
		}
		if !contains(created, agentTomlName) || !contains(created, providersTomlName) {
			t.Fatalf("plantillas creadas inesperadas: %v", created)
		}

		// agent.toml: TOML VÁLIDO con un `model` no vacío (lo que agent.session exige, G35).
		var agentCfg struct {
			Model    string `toml:"model"`
			MaxTurns int    `toml:"max_turns"`
		}
		if _, err := toml.DecodeFile(filepath.Join(cfg, agentTomlName), &agentCfg); err != nil {
			t.Fatalf("agent.toml no es TOML válido: %v", err)
		}
		if agentCfg.Model == "" {
			t.Fatal("agent.toml debe traer un model no vacío (G35)")
		}

		// providers.toml: TOML VÁLIDO que declara un provider con api_key_env. La clave
		// NUNCA va al fichero (providers.md §1): solo el NOMBRE de la variable de entorno.
		raw, err := os.ReadFile(filepath.Join(cfg, providersTomlName))
		if err != nil {
			t.Fatalf("leer providers.toml: %v", err)
		}
		var providersCfg map[string]any
		if err := toml.Unmarshal(raw, &providersCfg); err != nil {
			t.Fatalf("providers.toml no es TOML válido: %v", err)
		}
		s := string(raw)
		if !strings.Contains(s, "api_key_env") {
			t.Fatalf("providers.toml debe declarar api_key_env; got:\n%s", s)
		}
		if strings.Contains(s, "api_key =") || strings.Contains(s, "api_key=") {
			t.Fatalf("providers.toml NO debe llevar la clave, solo api_key_env; got:\n%s", s)
		}
	})

	t.Run("no pisa un agent.toml del usuario", func(t *testing.T) {
		rt, cfg := newBareRuntime(t)
		userAgent := "model = \"local/qwen3:32b\"\n"
		if err := os.WriteFile(filepath.Join(cfg, agentTomlName), []byte(userAgent), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, created, err := rt.WriteDefaultConfig()
		if err != nil {
			t.Fatalf("WriteDefaultConfig: %v", err)
		}
		// El agent.toml existente no se reporta como creado y queda intacto...
		if contains(created, agentTomlName) {
			t.Fatalf("agent.toml existente no debe reportarse como creado; got %v", created)
		}
		got, _ := os.ReadFile(filepath.Join(cfg, agentTomlName))
		if string(got) != userAgent {
			t.Fatalf("agent.toml del usuario fue pisado; quedó:\n%s", got)
		}
		// ...mientras que providers.toml sí se crea (no existía).
		if !contains(created, providersTomlName) {
			t.Fatalf("providers.toml debería crearse; got %v", created)
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
