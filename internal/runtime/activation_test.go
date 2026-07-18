package runtime

// Tests de la activación de extensiones embebidas gobernada por `enu.toml` (S12,
// api.md §14, ADR-010). La lógica clave a blindar (table-driven donde aplica):
//
//   - una extensión embebida NO se carga salvo que `enu.toml` `plugins.enabled` la
//     nombre (INACTIVA por defecto, ADR-010);
//   - activarla por `enu.toml` la materializa y la carga con `source="builtin"`;
//   - un `plugins.enabled` que nombra algo inexistente es un error accionable que
//     NOMBRA la línea de `enu.toml`;
//   - un directorio de usuario del mismo nombre SUSTITUYE a la embebida (gana
//     usuario, `source="user"`; no coexisten, §14);
//   - `enu.toml` cablea el presupuesto del watchdog y las rutas extra de plugins.
//
// El andamiaje escribe un `enu.toml` real en el `config.dir` (un `t.TempDir`) y
// arranca un Runtime apuntando ahí, igual que lo haría `main`. La extensión
// embebida bajo prueba es el STUB `example` (internal/runtime/embedded/example):
// una fixture trivial e independiente de las extensiones oficiales reales (Fase 8,
// hoy ya presentes bajo embedded/), de modo que estos tests del gating no quedan
// acoplados a la lógica de ninguna de ellas.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeNuToml escribe `enu.toml` en `configDir` con el contenido dado.
func writeNuToml(t *testing.T, configDir, content string) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir configDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, nuTomlName), []byte(content), 0o644); err != nil {
		t.Fatalf("write enu.toml: %v", err)
	}
}

// bootWithToml construye un Runtime con `pluginDir` (puede estar vacío) y un
// `configDir` que contiene el `enu.toml` dado, y lo arranca. Falla si `Boot` da
// error (los tests de error usan bootTomlExpectErr). data_dir es propio (temporal):
// ahí se materializan las embebidas.
func bootWithToml(t *testing.T, pluginDir, configDir string, opts ...Option) *harness {
	t.Helper()
	base := []Option{WithDataDir(t.TempDir()), WithConfigDir(configDir)}
	if pluginDir != "" {
		base = append(base, WithPluginDir(pluginDir))
	}
	base = append(base, opts...)
	rt := New(base...)
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló inesperadamente: %v", err)
	}
	return &harness{t: t, rt: rt}
}

// listSource devuelve el `source` con que `name` aparece en `enu.plugin.list()`, o
// "" si no está cargado. Es la observación que blinda el gating desde el lado del
// autor de extensiones (Definition of Done §2: snippet Lua).
func listSource(h *harness, name string) string {
	h.t.Helper()
	code := `
		local out = ""
		for _, p in ipairs(enu.plugin.list()) do
			if p.name == "` + name + `" then out = p.source end
		end
		return out`
	return h.eval(code)[0]
}

// TestEmbebidaInactivaPorDefecto (ADR-010): sin `enu.toml`, o con uno que no la
// nombra, la extensión embebida `example` NO se carga.
func TestEmbebidaInactivaPorDefecto(t *testing.T) {
	t.Run("sin enu.toml", func(t *testing.T) {
		cfg := t.TempDir()
		h := bootWithToml(t, "", cfg)
		if src := listSource(h, "example"); src != "" {
			t.Fatalf("la embebida no debía cargarse sin enu.toml; source=%q", src)
		}
		if got := h.eval(`return _example_embedded_cargada == true`)[0]; got != "false" {
			t.Fatalf("el init.lua de la embebida no debía correr; got %q", got)
		}
	})
	t.Run("enu.toml que no la nombra", func(t *testing.T) {
		cfg := t.TempDir()
		writeNuToml(t, cfg, "[plugins]\nenabled = [\"otra\"]\n")
		// "otra" no existe → error; este subtest solo comprueba que sin nombrarla no
		// se carga, así que activamos una lista vacía.
		cfg2 := t.TempDir()
		writeNuToml(t, cfg2, "[plugins]\nenabled = []\n")
		h := bootWithToml(t, "", cfg2)
		if src := listSource(h, "example"); src != "" {
			t.Fatalf("la embebida no debía cargarse si enabled no la nombra; source=%q", src)
		}
		_ = cfg
	})
}

// TestEmbebidaActivadaPorToml (ADR-010): nombrarla en `plugins.enabled` la
// materializa y la carga con `source="builtin"`, y su `init.lua` corre.
func TestEmbebidaActivadaPorToml(t *testing.T) {
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"example\"]\n")
	h := bootWithToml(t, "", cfg)

	if src := listSource(h, "example"); src != "builtin" {
		t.Fatalf(`enu.plugin.list() debía mostrar example con source="builtin"; got %q`, src)
	}
	// El init.lua de la embebida corrió de verdad (huella global + línea de log).
	if got := h.eval(`return _example_embedded_cargada == true`)[0]; got != "true" {
		t.Fatalf("el init.lua de la embebida activada debía correr; got %q", got)
	}
}

// TestUsuarioSustituyeEmbebida (§14): un directorio de usuario con el mismo nombre
// que una embebida la SUSTITUYE —no coexisten—; gana el de usuario (source="user").
func TestUsuarioSustituyeEmbebida(t *testing.T) {
	cfg := t.TempDir()
	pluginRoot := t.TempDir()
	// Plugin de usuario llamado "example" que deja una huella DISTINTA de la embebida.
	writePlugin(t, pluginRoot, "example", "9.9", nil, "_example_es_de_usuario = true")
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"example\"]\n")

	h := bootWithToml(t, pluginRoot, cfg)

	if src := listSource(h, "example"); src != "user" {
		t.Fatalf(`el dir de usuario debía sustituir a la embebida (source="user"); got %q`, src)
	}
	// Corrió el init del usuario, NO el de la embebida (no coexisten).
	if got := h.eval(`return _example_es_de_usuario == true`)[0]; got != "true" {
		t.Fatalf("debía correr el init del usuario; got %q", got)
	}
	if got := h.eval(`return _example_embedded_cargada == true`)[0]; got != "false" {
		t.Fatalf("el init de la embebida NO debía correr (sustituida); got %q", got)
	}
	// Una sola entrada "example" en la lista: no coexisten.
	count := h.eval(`
		local n = 0
		for _, p in ipairs(enu.plugin.list()) do if p.name == "example" then n = n + 1 end end
		return n`)[0]
	if count != "1" {
		t.Fatalf("debía haber UNA sola entrada example (no coexisten); got %q", count)
	}
}

// TestEnabledInexistenteEsErrorAccionable (§14): activar algo que no existe ni
// embebido ni en disco es un error de arranque accionable que NOMBRA la línea de
// `enu.toml` (`plugins.enabled`).
func TestEnabledInexistenteEsErrorAccionable(t *testing.T) {
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"fantasma\"]\n")

	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	err := rt.Boot()
	if err == nil {
		t.Fatal("se esperaba error por extensión inexistente, Boot terminó bien")
	}
	se, ok := err.(*StructuredError)
	if !ok {
		t.Fatalf("se esperaba *StructuredError, llegó %T: %v", err, err)
	}
	if se.Code != CodeEINVAL {
		t.Fatalf("code: got %q, want EINVAL", se.Code)
	}
	// Accionable: nombra la extensión y la línea de enu.toml que lo arregla.
	for _, want := range []string{"fantasma", "plugins.enabled", nuTomlName} {
		if !strings.Contains(se.Message, want) {
			t.Errorf("el mensaje debe nombrar %q (accionable); mensaje: %q", want, se.Message)
		}
	}
}

// TestNuTomlMalFormadoEsError: un `enu.toml` ilegible para TOML es un error de
// arranque accionable que nombra la ruta (aplazado desde New a Boot).
func TestNuTomlMalFormadoEsError(t *testing.T) {
	cfg := t.TempDir()
	writeNuToml(t, cfg, "esto no es toml [[[ válido")

	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	err := rt.Boot()
	if err == nil {
		t.Fatal("se esperaba error por enu.toml mal formado, Boot terminó bien")
	}
	se, ok := err.(*StructuredError)
	if !ok {
		t.Fatalf("se esperaba *StructuredError, llegó %T: %v", err, err)
	}
	if se.Code != CodeEINVAL {
		t.Fatalf("code: got %q, want EINVAL", se.Code)
	}
	if !strings.Contains(se.Message, nuTomlName) {
		t.Errorf("el mensaje debe nombrar %s; mensaje: %q", nuTomlName, se.Message)
	}
}

// TestNuTomlCablearWatchdog: `watchdog.slice_budget_ms` de `enu.toml` se aplica al
// scheduler (un budget pequeño corta un bucle de CPU puro con EBUDGET). Confirma el
// cableado del gancho `WithSliceBudget` desde la config de disco.
func TestNuTomlCablearWatchdog(t *testing.T) {
	cfg := t.TempDir()
	// 50 ms: igual que los tests del watchdog (S09); un bucle infinito debe cortarse.
	writeNuToml(t, cfg, "[watchdog]\nslice_budget_ms = 50\n")

	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	if got := rt.sched.budget; got != 50*time.Millisecond {
		t.Fatalf("el budget de enu.toml debía aplicarse; got %v, want 50ms", got)
	}
	h := &harness{t: t, rt: rt}
	// Un bucle de CPU puro en una task se aborta con EBUDGET (no se cuelga). El
	// `await` solo suspende dentro de una task, así que la observación va en otra.
	h.eval(`
		out = {}
		enu.task.spawn(function()
			local victim = enu.task.spawn(function() while true do end end)
			local ok, err = pcall(function() victim:await() end)
			out.code = err and err.code
		end)`)
	if got := h.eval(`return out.code`)[0]; got != "EBUDGET" {
		t.Fatalf("se esperaba EBUDGET por el budget de enu.toml; got %q", got)
	}
}

// TestWithSliceBudgetTienePrecedenciaSobreToml: la Option explícita gana sobre
// `enu.toml` (un test que fija su budget no lo pisa la config de disco).
func TestWithSliceBudgetTienePrecedenciaSobreToml(t *testing.T) {
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[watchdog]\nslice_budget_ms = 50\n")

	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithSliceBudget(0))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	if got := rt.sched.budget; got != 0 {
		t.Fatalf("WithSliceBudget(0) debía ganar sobre enu.toml; got %v, want 0", got)
	}
}

// TestNuTomlCablearRutasPlugins: `plugins.dirs` de `enu.toml` se suma a las rutas de
// descubrimiento (un plugin de un dir nombrado solo en enu.toml se carga).
func TestNuTomlCablearRutasPlugins(t *testing.T) {
	cfg := t.TempDir()
	extraRoot := t.TempDir()
	writePlugin(t, extraRoot, "desde_toml", "1.0", nil, "_desde_toml = true")
	// El dir va SOLO en enu.toml, no por WithPluginDir.
	writeNuToml(t, cfg, "[plugins]\ndirs = [\""+filepath.ToSlash(extraRoot)+"\"]\n")

	h := bootWithToml(t, "", cfg)
	if got := h.eval(`return _desde_toml == true`)[0]; got != "true" {
		t.Fatalf("el plugin del dir de enu.toml debía cargarse; got %q", got)
	}
}

// TestEmbebidaActivadaListadaComoBuiltin: snippet Lua de la Definition of Done
// (§2): `enu.plugin.list()` refleja la embebida activada con source="builtin" y
// enabled=true.
func TestEmbebidaActivadaListadaComoBuiltin(t *testing.T) {
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"example\"]\n")
	h := bootWithToml(t, "", cfg)

	got := h.eval(`
		for _, p in ipairs(enu.plugin.list()) do
			if p.name == "example" then
				return tostring(p.source) .. "," .. tostring(p.enabled)
			end
		end
		return "no encontrada"`)[0]
	if got != "builtin,true" {
		t.Fatalf(`list() debía dar "builtin,true" para example; got %q`, got)
	}
}
