package runtime

// Test dedicado de M13d-ext: el CONJUNTO OFICIAL de extensiones cargando sobre el
// backend wasm por el Runtime real. A diferencia de vmwasm_runtime_test.go (que
// prueba el catálogo de primitivas por un Runtime desnudo), aquí se arranca el
// Runtime con las 7 extensiones de producto (ADR-015: las embebidas menos el
// andamiaje `example`/`mesh`) activas —igual que `nu --default-config`— y se
// comprueba que:
//   - `Boot()` NO devuelve error y las 7 aparecen en `nu.plugin.list()` (builtin);
//   - cada módulo público es require-able y expone su API (prueba de que el
//     `init.lua` de cada extensión corrió sobre la Instance wasm);
//   - un e2e hermético (sessions: crear/append/replay por disco) ejercita una
//     extensión de punta a punta sobre el scheduler wasm (EvalTaskString);
//   - el `ownerStack` está bien cableado: el `init.lua` de un plugin de disco se
//     anota en `nu.log` con SU nombre, no con "user" (la decisión del owner,
//     vmwasm_plugin.go / vmwasm_loader.go).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newWasmExtRuntime construye un Runtime headless sobre wasm con el CONJUNTO
// OFICIAL DE PRODUCTO activo en memoria (WithEnabledPlugins, el modo efímero de
// `nu --default-config`, ADR-015/G33) y lo arranca (Boot). Devuelve el Runtime y la
// lista del conjunto oficial. Verifica de paso que el backend se resolvió a wasm y
// que el arranque no falló.
func newWasmExtRuntime(t *testing.T) (*Runtime, []string) {
	t.Helper()
	product, err := officialProductSet()
	if err != nil {
		t.Fatalf("officialProductSet: %v", err)
	}
	rt := New(
		WithVMBackend(VMWasm),
		WithDataDir(t.TempDir()),
		WithConfigDir(t.TempDir()),
		WithEnabledPlugins(product),
	)
	t.Cleanup(rt.Close)
	if rt.VMBackend() != VMWasm {
		t.Fatalf("backend = %v, esperado wasm", rt.VMBackend())
	}
	if rt.wasmErr != nil {
		t.Fatalf("buildWasmState falló: %v", rt.wasmErr)
	}
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot con el conjunto oficial sobre wasm falló: %v", err)
	}
	return rt, product
}

// TestExtWasmCargaConjuntoOficial: el conjunto oficial arranca sobre wasm y todas
// sus extensiones aparecen en nu.plugin.list() como "builtin" (source de una
// embebida activada, §14). Es la prueba de que el loader corrió sobre la Instance.
func TestExtWasmCargaConjuntoOficial(t *testing.T) {
	rt, product := newWasmExtRuntime(t)

	// nu.plugin.list() sobre wasm: recoge {name -> source}.
	got := evalStringOne(t, rt, `
		local t = {}
		for _, p in ipairs(nu.plugin.list()) do
			t[#t+1] = p.name .. "=" .. tostring(p.source)
		end
		table.sort(t)
		return table.concat(t, ",")`)

	byName := map[string]string{}
	for _, kv := range strings.Split(got, ",") {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			byName[parts[0]] = parts[1]
		}
	}
	if len(byName) != len(product) {
		t.Fatalf("nu.plugin.list() = %q (%d), esperado el conjunto oficial (%d): %v",
			got, len(byName), len(product), product)
	}
	for _, name := range product {
		src, ok := byName[name]
		if !ok {
			t.Fatalf("la extensión oficial %q no cargó sobre wasm; list=%q", name, got)
		}
		if src != string(sourceBuiltin) {
			t.Fatalf("la extensión %q debería ser builtin (embebida activada); source=%q", name, src)
		}
	}
}

// TestExtWasmSmokeModulos: cada módulo público del conjunto oficial es require-able
// sobre wasm y expone su API de consumo. Que `require` devuelva una tabla con la
// función esperada prueba que el `init.lua` de la extensión CORRIÓ (cableó el
// módulo) sobre la Instance wasm. Todo síncrono (require + type): EvalString basta.
func TestExtWasmSmokeModulos(t *testing.T) {
	rt, _ := newWasmExtRuntime(t)

	// mod -> función pública que su init deja registrada (agente.md/providers.md/…).
	smokes := []struct{ mod, fn string }{
		{"providers", "approx_tokens"},
		{"sessions", "open"},
		{"agent", "session"},
		{"toolkit", "theme"},
		{"chat", "start"},
		{"mcp", "connect"},
		{"repl", "eval"},
	}
	for _, s := range smokes {
		got := evalStringOne(t, rt, `
			local ok, m = pcall(require, "`+s.mod+`")
			if not ok then return "REQUIRE_ERR:" .. tostring(type(m) == "table" and m.message or m) end
			if type(m) ~= "table" then return "NOT_TABLE:" .. type(m) end
			return type(m.`+s.fn+`)`)
		if got != "function" && got != "table" {
			t.Fatalf("smoke de %q: require(...).%s = %q, esperado function/table", s.mod, s.fn, got)
		}
	}
}

// TestExtWasmProvidersApproxTokens: un smoke FUNCIONAL (no solo de forma) de una
// extensión sobre wasm — providers.approx_tokens("...") devuelve un entero. Ejercita
// Lua puro de la extensión sobre la Instance wasm, sin red ni API key.
func TestExtWasmProvidersApproxTokens(t *testing.T) {
	rt, _ := newWasmExtRuntime(t)
	got := evalStringOne(t, rt, `return tostring(require("providers").approx_tokens("hola mundo"))`)
	if got != "3" {
		t.Fatalf("providers.approx_tokens(\"hola mundo\") = %q, esperado 3", got)
	}
}

// TestExtWasmSessionsRoundTrip: e2e hermético de la extensión `sessions` sobre wasm
// —crea una sesión (JSONL append-only, sesiones.md), le añade un evento, la cierra,
// la reabre por `resume` y la reproduce— por EvalTaskString (las ops de sessions son
// ⏸ sobre nu.fs). Prueba una extensión oficial de punta a punta sobre el scheduler
// wasm, sin red ni API key.
func TestExtWasmSessionsRoundTrip(t *testing.T) {
	rt, _ := newWasmExtRuntime(t)
	proj := filepath.ToSlash(t.TempDir())
	got := evalTaskOne(t, rt, `
		local sessions = require("sessions")
		local s = sessions.open({ cwd = "`+proj+`" })
		local id = s.id
		s:append({ t = "user", text = "hola wasm" })
		s:close()
		-- reabrir por resume y contar los eventos reproducidos (replay -> tabla)
		local s2 = sessions.open({ cwd = "`+proj+`", resume = id })
		local e = s2:replay()
		s2:close()
		return tostring(#e)`)
	if got == "0" || got == "" {
		t.Fatalf("sessions round-trip sobre wasm: replay contó %q eventos, esperado >= 1", got)
	}
}

// TestExtWasmOwnerDuranteInit: el `ownerStack` está bien cableado sobre wasm — el
// `init.lua` de un plugin de disco que hace `nu.log.info` se anota en el log con SU
// nombre, no con "user". Valida la decisión del owner (vmwasm_loader.go empuja el
// contexto del plugin alrededor de su init; vmwasm_log.go lee rt.currentOwner()).
func TestExtWasmOwnerDuranteInit(t *testing.T) {
	// Un plugin de disco mínimo cuyo init deja una marca en el log.
	pluginRoot := t.TempDir()
	pdir := filepath.Join(pluginRoot, "ownlog")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, pluginManifestName),
		[]byte("name = \"ownlog\"\nversion = \"0.0.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, pluginInitName),
		[]byte(`nu.log.info("MARCA_OWNER_WASM")`), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	rt := New(
		WithVMBackend(VMWasm),
		WithDataDir(dataDir),
		WithConfigDir(t.TempDir()),
		WithPluginDir(pluginRoot),
	)
	t.Cleanup(rt.Close)
	if rt.wasmErr != nil {
		t.Fatalf("buildWasmState: %v", rt.wasmErr)
	}
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dataDir, logFileName))
	if err != nil {
		t.Fatalf("no se pudo leer el log: %v", err)
	}
	var marca string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "MARCA_OWNER_WASM") {
			marca = line
			break
		}
	}
	if marca == "" {
		t.Fatalf("no se encontró la marca del init en el log:\n%s", data)
	}
	if !strings.Contains(marca, "[ownlog]") {
		t.Fatalf("el log del init debería anotarse con el owner del plugin ([ownlog]); línea: %q", marca)
	}
	if strings.Contains(marca, "[user]") {
		t.Fatalf("el owner del init NO debe ser \"user\" (ownerStack mal cableado); línea: %q", marca)
	}
}
