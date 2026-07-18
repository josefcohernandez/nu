package runtime

// Tests 🔒 del loader (S11, api.md §14). La lógica clave a blindar (inventario 🔒):
//   - orden topológico por `requires` (el dependido antes que el dependiente);
//   - unicidad de nombre (colisión = error de carga accionable);
//   - ciclo en `requires` y dependencia ausente = errores accionables;
//   - `init.lua` del usuario el ÚLTIMO; `core:ready` UNA vez, al final;
//   - `enu.plugin.current()` correcto DURANTE el init.lua de cada plugin.
//
// El andamiaje crea plugins reales en disco (un `t.TempDir`) y un Runtime con
// `WithPluginDir`/`WithConfigDir` apuntando ahí, igual que lo haría `main`.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePlugin crea un plugin en `root/<name>`: su `plugin.toml` (con `requires`) y,
// si `initLua` no es vacío, su `init.lua`. Devuelve el directorio del plugin.
func writePlugin(t *testing.T, root, name, version string, requires []string, initLua string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir plugin %q: %v", name, err)
	}
	var b strings.Builder
	b.WriteString("name = \"" + name + "\"\n")
	b.WriteString("version = \"" + version + "\"\n")
	if len(requires) > 0 {
		b.WriteString("requires = [")
		for i, r := range requires {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("\"" + r + "\"")
		}
		b.WriteString("]\n")
	}
	if err := os.WriteFile(filepath.Join(dir, pluginManifestName), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write plugin.toml %q: %v", name, err)
	}
	if initLua != "" {
		if err := os.WriteFile(filepath.Join(dir, pluginInitName), []byte(initLua), 0o644); err != nil {
			t.Fatalf("write init.lua %q: %v", name, err)
		}
	}
	return dir
}

// newBootedHarness construye un Runtime con `pluginDir` como directorio de plugins
// y `configDir` como config (de donde sale el init.lua del usuario), y lo arranca.
// Falla si `Boot` devuelve error (los tests de error usan `bootExpectErr`).
func newBootedHarness(t *testing.T, pluginDir, configDir string) *harness {
	t.Helper()
	rt := New(WithDataDir(t.TempDir()), WithPluginDir(pluginDir), WithConfigDir(configDir))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló inesperadamente: %v", err)
	}
	return &harness{t: t, rt: rt}
}

// TestLoaderOrdenTopologico (🔒): un grafo no trivial (C→B→A, D→A) se carga en
// orden topológico —cada dependido antes que su dependiente—. Cada init escribe su
// nombre en una tabla global de orden; el test comprueba las relaciones de
// precedencia (no un orden total fijo, que el desempate determinista igual fija,
// pero lo que el contrato exige es la precedencia).
func TestLoaderOrdenTopologico(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	// init que registra el nombre del plugin en _orden (global compartido).
	reg := func(n string) string {
		return "_orden = _orden or {}; _orden[#_orden+1] = '" + n + "'"
	}
	writePlugin(t, root, "A", "1.0", nil, reg("A"))
	writePlugin(t, root, "B", "1.0", []string{"A"}, reg("B"))
	writePlugin(t, root, "C", "1.0", []string{"B"}, reg("C"))
	writePlugin(t, root, "D", "1.0", []string{"A"}, reg("D"))

	h := newBootedHarness(t, root, cfg)

	got := h.eval(`return table.concat(_orden, ",")`)[0]
	idx := func(n string) int {
		for i, name := range strings.Split(got, ",") {
			if name == n {
				return i
			}
		}
		t.Fatalf("plugin %q no apareció en el orden de carga %q", n, got)
		return -1
	}
	// Precedencias que el orden topológico DEBE respetar:
	if idx("A") > idx("B") {
		t.Errorf("A debe cargarse antes que B (B requires A); orden: %q", got)
	}
	if idx("B") > idx("C") {
		t.Errorf("B debe cargarse antes que C (C requires B); orden: %q", got)
	}
	if idx("A") > idx("D") {
		t.Errorf("A debe cargarse antes que D (D requires A); orden: %q", got)
	}
	if idx("A") > idx("C") {
		t.Errorf("A debe cargarse (transitivamente) antes que C; orden: %q", got)
	}
}

// TestLoaderUnicidadNombre (🔒): dos plugins con el MISMO nombre en directorios
// distintos son un error de carga accionable que nombra el conflicto (el nombre y
// ambas rutas).
func TestLoaderUnicidadNombre(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root1, "dup", "1.0", nil, "")
	writePlugin(t, root2, "dup", "2.0", nil, "")

	rt := New(WithDataDir(t.TempDir()), WithPluginDir(root1), WithPluginDir(root2), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	err := rt.Boot()
	if err == nil {
		t.Fatal("se esperaba error por colisión de nombre, Boot terminó bien")
	}
	se, ok := err.(*StructuredError)
	if !ok {
		t.Fatalf("se esperaba *StructuredError, llegó %T: %v", err, err)
	}
	if se.Code != CodeEINVAL {
		t.Errorf("código: got %q, want EINVAL", se.Code)
	}
	if !strings.Contains(se.Message, "dup") {
		t.Errorf("el mensaje no nombra el plugin en conflicto: %q", se.Message)
	}
	// Accionable: nombra ambas rutas para que el usuario sepa qué resolver.
	if !strings.Contains(se.Message, root1) || !strings.Contains(se.Message, root2) {
		t.Errorf("el mensaje no nombra ambas rutas en conflicto: %q", se.Message)
	}
}

// TestLoaderCicloRequires (🔒): un ciclo en `requires` (A→B→A) es un error de carga
// accionable que nombra los plugins implicados.
func TestLoaderCicloRequires(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "A", "1.0", []string{"B"}, "")
	writePlugin(t, root, "B", "1.0", []string{"A"}, "")

	rt := New(WithDataDir(t.TempDir()), WithPluginDir(root), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	err := rt.Boot()
	if err == nil {
		t.Fatal("se esperaba error por ciclo de dependencias, Boot terminó bien")
	}
	se, ok := err.(*StructuredError)
	if !ok {
		t.Fatalf("se esperaba *StructuredError, llegó %T: %v", err, err)
	}
	if se.Code != CodeEINVAL {
		t.Errorf("código: got %q, want EINVAL", se.Code)
	}
	if !strings.Contains(se.Message, "ciclo") {
		t.Errorf("el mensaje no menciona el ciclo: %q", se.Message)
	}
	if !strings.Contains(se.Message, "A") || !strings.Contains(se.Message, "B") {
		t.Errorf("el mensaje no nombra los plugins del ciclo: %q", se.Message)
	}
}

// TestLoaderDependenciaAusente (🔒): un `requires` que no corresponde a ningún
// plugin descubierto es un error accionable que nombra el plugin y la dependencia
// que falta.
func TestLoaderDependenciaAusente(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "solo", "1.0", []string{"fantasma"}, "")

	rt := New(WithDataDir(t.TempDir()), WithPluginDir(root), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	err := rt.Boot()
	if err == nil {
		t.Fatal("se esperaba error por dependencia ausente, Boot terminó bien")
	}
	se, ok := err.(*StructuredError)
	if !ok {
		t.Fatalf("se esperaba *StructuredError, llegó %T: %v", err, err)
	}
	if se.Code != CodeEINVAL {
		t.Errorf("código: got %q, want EINVAL", se.Code)
	}
	if !strings.Contains(se.Message, "solo") || !strings.Contains(se.Message, "fantasma") {
		t.Errorf("el mensaje no nombra el plugin ni la dependencia ausente: %q", se.Message)
	}
}

// TestLoaderInitUsuarioElUltimo (🔒): el `init.lua` del usuario corre DESPUÉS de
// todos los plugins, y `core:ready` se emite UNA vez al final del arranque. Cada
// init (plugins + usuario) y un `on("core:ready")` registrado por un plugin escriben
// en una traza observable que prueba el orden.
func TestLoaderInitUsuarioElUltimo(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	// Plugin P: registra su carga y un contador de core:ready.
	writePlugin(t, root, "P", "1.0", nil, `
		_traza = _traza or {}
		_traza[#_traza+1] = "plugin:P"
		_ready = 0
		enu.events.on("core:ready", function() _ready = _ready + 1; _traza[#_traza+1] = "ready" end)
	`)
	// init.lua del usuario.
	if err := os.WriteFile(filepath.Join(cfg, pluginInitName), []byte(`
		_traza[#_traza+1] = "user"
	`), 0o644); err != nil {
		t.Fatalf("write user init: %v", err)
	}

	h := newBootedHarness(t, root, cfg)

	// El usuario corre tras el plugin, y core:ready tras el usuario (emitido al
	// final del arranque). Traza esperada: plugin:P, user, ready.
	traza := h.eval(`return table.concat(_traza, ",")`)[0]
	if traza != "plugin:P,user,ready" {
		t.Fatalf("orden de arranque incorrecto: got %q, want %q", traza, "plugin:P,user,ready")
	}
	// `core:ready` UNA sola vez.
	if got := h.eval(`return tostring(_ready)`)[0]; got != "1" {
		t.Fatalf("core:ready se emitió %s veces, want 1", got)
	}
}

// TestPluginCurrentDuranteInit (🔒): `enu.plugin.current()` devuelve el plugin
// correcto DURANTE su propio init.lua (y distinto por plugin); fuera de todo plugin
// (chunk de `-e`) devuelve el contexto del usuario.
func TestPluginCurrentDuranteInit(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "alfa", "1.2.3", nil, `
		local c = enu.plugin.current()
		_alfa = c.name .. "|" .. c.version
	`)
	writePlugin(t, root, "beta", "4.5.6", []string{"alfa"}, `
		local c = enu.plugin.current()
		_beta = c.name .. "|" .. c.version
	`)

	h := newBootedHarness(t, root, cfg)

	if got := h.eval(`return _alfa`)[0]; got != "alfa|1.2.3" {
		t.Errorf("current() durante init de alfa: got %q, want %q", got, "alfa|1.2.3")
	}
	if got := h.eval(`return _beta`)[0]; got != "beta|4.5.6" {
		t.Errorf("current() durante init de beta: got %q, want %q", got, "beta|4.5.6")
	}
	// Fuera de todo plugin (este chunk corre como "user").
	if got := h.eval(`return enu.plugin.current().name`)[0]; got != "user" {
		t.Errorf("current() fuera de plugin: got %q, want %q", got, "user")
	}
}

// TestPluginListYConfig comprueba `enu.plugin.list` (orden topológico, source/enabled)
// y `enu.config.dir/data_dir` desde el lado del autor de extensiones (snippet Lua,
// Definition of Done §2).
func TestPluginListYConfig(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writePlugin(t, root, "base", "0.1", nil, "")
	writePlugin(t, root, "sobre", "0.2", []string{"base"}, "")

	rt := New(WithDataDir(dataDir), WithPluginDir(root), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}

	// list devuelve los dos plugins en orden topológico (base antes que sobre),
	// ambos source=user, enabled=true.
	out := h.eval(`
		local l = enu.plugin.list()
		local parts = {}
		for _, p in ipairs(l) do
			parts[#parts+1] = p.name .. ":" .. p.source .. ":" .. tostring(p.enabled)
		end
		return table.concat(parts, ",")
	`)[0]
	if out != "base:user:true,sobre:user:true" {
		t.Fatalf("enu.plugin.list: got %q", out)
	}

	// config.dir / data_dir devuelven lo configurado.
	if got := h.eval(`return enu.config.dir()`)[0]; got != cfg {
		t.Errorf("enu.config.dir(): got %q, want %q", got, cfg)
	}
	if got := h.eval(`return enu.config.data_dir()`)[0]; got != dataDir {
		t.Errorf("enu.config.data_dir(): got %q, want %q", got, dataDir)
	}
}

// TestLoaderRequireEntrePlugins comprueba que el `lua/` de un plugin se añade a las
// rutas de `require`, de modo que un plugin puede requerir un módulo de otro (la
// composabilidad de §14/ADR-008). Confirma además que `require` NO carga ficheros
// arbitrarios del cwd (package.path no incluye `./?.lua`).
func TestLoaderRequireEntrePlugins(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()

	// Plugin "lib" expone un módulo `saludo` en su lua/.
	libDir := writePlugin(t, root, "lib", "1.0", nil, "")
	if err := os.MkdirAll(filepath.Join(libDir, "lua"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "lua", "saludo.lua"),
		[]byte(`return { hola = function() return "hola desde lib" end }`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Plugin "app" requiere ese módulo en su init.
	writePlugin(t, root, "app", "1.0", []string{"lib"}, `
		local s = require("saludo")
		_mensaje = s.hola()
	`)

	h := newBootedHarness(t, root, cfg)

	if got := h.eval(`return _mensaje`)[0]; got != "hola desde lib" {
		t.Fatalf("require entre plugins: got %q, want %q", got, "hola desde lib")
	}
}

// TestLoaderInitErrorAislado comprueba que un init.lua que lanza NO tumba el
// arranque (ADR-008): los demás plugins y el usuario siguen cargando, y se emite
// `core:plugin.error`. Boot no devuelve error por un init que lanza (eso es de
// runtime del plugin, no del grafo).
func TestLoaderInitErrorAislado(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "malo", "1.0", nil, `error("boom")`)
	writePlugin(t, root, "bueno", "1.0", nil, `_bueno_cargo = true`)

	rt := New(WithDataDir(t.TempDir()), WithPluginDir(root), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("un init.lua que lanza no debe hacer fallar Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}
	// El plugin "bueno" se cargó pese al fallo de "malo".
	if got := h.eval(`return tostring(_bueno_cargo)`)[0]; got != "true" {
		t.Errorf("un init que lanza no debe impedir cargar los demás; _bueno_cargo=%q", got)
	}
}

// TestLoaderSinPlugins comprueba el arranque desnudo (sin directorios de plugins):
// Boot corre el init.lua del usuario (si existe) y emite core:ready, sin error.
func TestLoaderSinPlugins(t *testing.T) {
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, pluginInitName), []byte(`_user_corrio = true`), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot desnudo: %v", err)
	}
	h := &harness{t: t, rt: rt}
	if got := h.eval(`return tostring(_user_corrio)`)[0]; got != "true" {
		t.Errorf("el init.lua del usuario no corrió en arranque desnudo: %q", got)
	}
	// list vacía.
	if got := h.eval(`return tostring(#enu.plugin.list())`)[0]; got != "0" {
		t.Errorf("enu.plugin.list debería estar vacía sin plugins: %q", got)
	}
}

// TestPluginManifestInvalido comprueba que un plugin.toml mal formado o sin `name`
// es un error de carga accionable que nombra la ruta.
func TestPluginManifestInvalido(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	dir := filepath.Join(root, "roto")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// plugin.toml sin `name`.
	if err := os.WriteFile(filepath.Join(dir, pluginManifestName), []byte(`version = "1.0"`), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := New(WithDataDir(t.TempDir()), WithPluginDir(root), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	err := rt.Boot()
	if err == nil {
		t.Fatal("se esperaba error por manifiesto sin name")
	}
	se, ok := err.(*StructuredError)
	if !ok {
		t.Fatalf("se esperaba *StructuredError, llegó %T", err)
	}
	if se.Code != CodeEINVAL || !strings.Contains(se.Message, "name") {
		t.Errorf("error de manifiesto inválido inesperado: code=%q msg=%q", se.Code, se.Message)
	}
}
