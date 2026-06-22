package runtime

// Tests de la PANTALLA DE RUNTIME DESNUDO (api.md §14, G21, S33). Blindan lo
// AUTOMATIZABLE en este entorno headless (sin TTY); la interacción de teclado, el
// streaming visible y el resize/paste visibles son el CP-7 MANUAL con TTY (ver
// claude_decisions.md). Lo que sí se cubre por unidad:
//
//   - CONDICIÓN: la pantalla se muestra SSI hay superficie de UI (`uiActive`) Y no
//     hay plugins activos. Sin UI (headless) → no se muestra (arranca desnudo). Con
//     algún plugin activo (enabled o de disco) → no se muestra (arranque normal).
//   - CONTENIDO: el render FIJO incluye la versión + nivel de API, las rutas (config
//     y plugins) y el catálogo de embebidas y las acciones; se inspecciona tanto el
//     modelo como la rejilla del compositor (las cadenas esperadas están pintadas).
//   - ACCIÓN "activar conjunto oficial": escribe `plugins.enabled` en `nu.toml` (con
//     todas las embebidas), PRESERVA el resto del fichero, y un `Boot` posterior las
//     carga con `source="builtin"`.
//   - ACCIÓN "activar suelta" (p. ej. solo `example`): escribe solo esa.
//   - NO REGRESIÓN: headless (`WithForceUI(false)`) → `bareScreenActive` es false.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newBareRuntime construye un Runtime con UI forzada (simula TTY, como el arnés) y un
// configDir propio, SIN plugins de disco ni `nu.toml`: el escenario de la pantalla
// desnuda. Devuelve también el configDir para inspeccionar/escribir `nu.toml`.
func newBareRuntime(t *testing.T, opts ...Option) (*Runtime, string) {
	t.Helper()
	cfg := t.TempDir()
	base := []Option{WithDataDir(t.TempDir()), WithConfigDir(cfg), WithForceUI(true)}
	base = append(base, opts...)
	rt := New(base...)
	t.Cleanup(rt.Close)
	return rt, cfg
}

// TestBareScreenCondition blinda la CONDICIÓN (§14): la pantalla se muestra SSI hay
// UI y no hay plugins activos.
func TestBareScreenCondition(t *testing.T) {
	t.Run("UI y sin plugins -> activa", func(t *testing.T) {
		rt, _ := newBareRuntime(t)
		if !rt.BareScreenActive() {
			t.Fatal("con UI y sin plugins, la pantalla desnuda debería estar activa")
		}
	})

	t.Run("headless (sin TTY) -> no activa (arranca desnudo)", func(t *testing.T) {
		rt := New(WithDataDir(t.TempDir()), WithConfigDir(t.TempDir()), WithForceUI(false))
		t.Cleanup(rt.Close)
		if rt.BareScreenActive() {
			t.Fatal("sin TTY no hay pantalla: el runtime arranca desnudo")
		}
	})

	t.Run("UI pero con embebida activada -> no activa", func(t *testing.T) {
		cfg := t.TempDir()
		writeNuToml(t, cfg, "[plugins]\nenabled = [\"example\"]\n")
		rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithForceUI(true))
		t.Cleanup(rt.Close)
		if rt.BareScreenActive() {
			t.Fatal("con un plugin activo (enabled) no se pinta la pantalla")
		}
	})

	t.Run("UI pero con plugin de disco -> no activa", func(t *testing.T) {
		pdir := t.TempDir()
		makeDiskPlugin(t, pdir, "alfa")
		rt := New(WithDataDir(t.TempDir()), WithConfigDir(t.TempDir()),
			WithForceUI(true), WithPluginDir(pdir))
		t.Cleanup(rt.Close)
		if rt.BareScreenActive() {
			t.Fatal("con un plugin de disco no se pinta la pantalla")
		}
	})
}

// makeDiskPlugin crea un plugin de disco mínimo (`plugin.toml` + `init.lua`) bajo
// `root/name`, para los tests de "hay plugins de disco".
func makeDiskPlugin(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, pluginManifestName),
		[]byte("name = \""+name+"\"\nversion = \"0.0.1\"\n"), 0o644); err != nil {
		t.Fatalf("write plugin.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, pluginInitName), []byte("-- noop\n"), 0o644); err != nil {
		t.Fatalf("write init.lua: %v", err)
	}
}

// TestBareScreenContent blinda el CONTENIDO del render FIJO (§14): versión + nivel de
// API, rutas (config y plugins), catálogo de embebidas y acciones. Se comprueba en el
// MODELO (las líneas) y en la REJILLA del compositor (lo efectivamente pintado).
func TestBareScreenContent(t *testing.T) {
	pdir := t.TempDir() // un directorio de plugins (vacío): debe aparecer en "plugins:"
	rt, cfg := newBareRuntime(t, WithPluginDir(pdir))

	lines := rt.RenderBareScreen()
	joined := strings.Join(lines, "\n")

	// Versión + nivel de API (§2).
	wantVer := "nu 0.1.0 · API 1"
	if !strings.Contains(joined, wantVer) {
		t.Errorf("falta la versión/API %q en la pantalla:\n%s", wantVer, joined)
	}
	// Rutas: config.dir y el directorio de plugins.
	if !strings.Contains(joined, cfg) {
		t.Errorf("falta la ruta de config %q:\n%s", cfg, joined)
	}
	if !strings.Contains(joined, pdir) {
		t.Errorf("falta el directorio de plugins %q:\n%s", pdir, joined)
	}
	// Catálogo de embebidas DISPONIBLES: el STUB `example` está en el binario.
	if !strings.Contains(joined, "example") {
		t.Errorf("falta el catálogo de embebidas (example):\n%s", joined)
	}
	// Acciones (§14): activar conjunto oficial, activar sueltas, salir.
	for _, want := range []string{"conjunto oficial", "sueltas", "salir"} {
		if !strings.Contains(joined, want) {
			t.Errorf("falta la acción %q:\n%s", want, joined)
		}
	}

	// La pantalla se PINTÓ en el compositor: la rejilla `back` contiene las cadenas
	// esperadas (render FIJO a celdas, no solo el modelo en memoria).
	grid := gridText(rt.ui.comp.back)
	if !strings.Contains(grid, "runtime desnudo") {
		t.Errorf("la rejilla del compositor no contiene el título; rejilla:\n%s", grid)
	}
	if !strings.Contains(grid, "example") {
		t.Errorf("la rejilla del compositor no contiene el catálogo de embebidas; rejilla:\n%s", grid)
	}
	// El compositor emitió un frame no vacío (algo se pintó).
	if rt.ui.comp.encoded() == "" {
		t.Error("el compositor no emitió ningún frame para la pantalla desnuda")
	}
}

// gridText reconstruye el texto visible de una rejilla del compositor (una fila por
// línea, graphemes concatenados), para que un test compruebe que una cadena esperada
// se pintó. Las celdas de continuación (w=0) y los espacios se vuelven " ".
func gridText(g *grid) string {
	var b strings.Builder
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			c := g.at(x, y)
			if c == nil || c.r == "" {
				b.WriteByte(' ')
				continue
			}
			b.WriteString(c.r)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// TestBareScreenActivateOfficial blinda la ACCIÓN "activar el conjunto oficial": se
// escribe `plugins.enabled` en `nu.toml` con TODAS las embebidas, y el `Boot` que
// continúa las carga con `source="builtin"` —sin red (ADR-010)—.
func TestBareScreenActivateOfficial(t *testing.T) {
	rt, cfg := newBareRuntime(t)

	// Antes: no hay nu.toml y la pantalla está activa.
	if !rt.BareScreenActive() {
		t.Fatal("precondición: la pantalla debería estar activa")
	}

	if err := rt.ActivateOfficial(); err != nil {
		t.Fatalf("ActivateOfficial falló: %v", err)
	}

	// El `nu.toml` se escribió con `plugins.enabled` conteniendo el catálogo (example).
	data, err := os.ReadFile(filepath.Join(cfg, nuTomlName))
	if err != nil {
		t.Fatalf("nu.toml no se escribió: %v", err)
	}
	if !strings.Contains(string(data), "example") {
		t.Fatalf("plugins.enabled no nombra la embebida; nu.toml:\n%s", data)
	}

	// El Boot que continuó cargó la embebida con source="builtin" y corrió su init.
	h := &harness{t: t, rt: rt}
	if src := listSource(h, "example"); src != string(sourceBuiltin) {
		t.Fatalf("tras activar, example debería ser builtin; source=%q", src)
	}
	if got := h.eval(`return _example_embedded_cargada == true`)[0]; got != "true" {
		t.Fatalf("el init.lua de la embebida activada no corrió; got %q", got)
	}
}

// TestBareScreenActivateSingle blinda la ACCIÓN "activar extensiones sueltas": se
// escribe SOLO la extensión pedida en `plugins.enabled`.
func TestBareScreenActivateSingle(t *testing.T) {
	rt, cfg := newBareRuntime(t)

	if err := rt.activateAndBoot([]string{"example"}); err != nil {
		t.Fatalf("activateAndBoot falló: %v", err)
	}

	cfgData, err := loadNuTomlForTest(cfg)
	if err != nil {
		t.Fatalf("releer nu.toml: %v", err)
	}
	if len(cfgData.Plugins.Enabled) != 1 || cfgData.Plugins.Enabled[0] != "example" {
		t.Fatalf("plugins.enabled debería ser [example]; got %v", cfgData.Plugins.Enabled)
	}

	h := &harness{t: t, rt: rt}
	if src := listSource(h, "example"); src != string(sourceBuiltin) {
		t.Fatalf("example debería ser builtin; source=%q", src)
	}
}

// TestBareScreenPreservesConfig blinda que escribir `plugins.enabled` PRESERVA el
// resto del `nu.toml` (otras secciones y claves que el core ni siquiera entiende):
// la pantalla no pisa configuración del usuario.
func TestBareScreenPreservesConfig(t *testing.T) {
	cfg := t.TempDir()
	// Un nu.toml previo con watchdog, una clave desconocida y una sección ajena.
	writeNuToml(t, cfg, `[plugins]
dirs = ["/algun/dir"]

[watchdog]
slice_budget_ms = 250

[mi_seccion]
clave = "valor"
`)

	if err := writeEnabledPlugins(cfg, []string{"example"}); err != nil {
		t.Fatalf("writeEnabledPlugins falló: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cfg, nuTomlName))
	if err != nil {
		t.Fatalf("leer nu.toml: %v", err)
	}
	s := string(data)
	// Se añadió enabled y se conservó dirs, el watchdog, y la sección/clave ajenas.
	for _, want := range []string{"example", "/algun/dir", "250", "mi_seccion", "valor"} {
		if !strings.Contains(s, want) {
			t.Errorf("writeEnabledPlugins no preservó %q; nu.toml resultante:\n%s", want, s)
		}
	}

	// Y el resultado sigue siendo un nu.toml válido que el core relee.
	cfgData, _, err := loadNuToml(cfg)
	if err != nil {
		t.Fatalf("el nu.toml escrito no es válido: %v", err)
	}
	if len(cfgData.Plugins.Enabled) != 1 || cfgData.Plugins.Enabled[0] != "example" {
		t.Fatalf("enabled mal escrito; got %v", cfgData.Plugins.Enabled)
	}
	if len(cfgData.Plugins.Dirs) != 1 || cfgData.Plugins.Dirs[0] != "/algun/dir" {
		t.Fatalf("dirs no preservado; got %v", cfgData.Plugins.Dirs)
	}
	if cfgData.Watchdog.SliceBudgetMs == nil || *cfgData.Watchdog.SliceBudgetMs != 250 {
		t.Fatalf("watchdog no preservado; got %v", cfgData.Watchdog.SliceBudgetMs)
	}
}

// TestBareScreenMalformedTomlNotClobbered blinda que un `nu.toml` MAL FORMADO no se
// sobrescribe a ciegas (perdería config del usuario): la acción devuelve un error
// accionable y deja el fichero intacto.
func TestBareScreenMalformedTomlNotClobbered(t *testing.T) {
	cfg := t.TempDir()
	bad := "[plugins\nenabled = roto"
	writeNuToml(t, cfg, bad)

	err := writeEnabledPlugins(cfg, []string{"example"})
	if err == nil {
		t.Fatal("un nu.toml mal formado debería dar error, no sobrescribirse")
	}
	se, ok := err.(*StructuredError)
	if !ok || se.Code != CodeEINVAL {
		t.Fatalf("se esperaba EINVAL; got %v", err)
	}
	// El fichero original sigue intacto.
	data, _ := os.ReadFile(filepath.Join(cfg, nuTomlName))
	if string(data) != bad {
		t.Fatalf("el nu.toml mal formado no debía tocarse; got:\n%s", data)
	}
}

// loadNuTomlForTest es un atajo de los tests para releer el `nu.toml` (descarta el
// flag `found`).
func loadNuTomlForTest(configDir string) (runtimeConfig, error) {
	cfg, _, err := loadNuToml(configDir)
	return cfg, err
}
