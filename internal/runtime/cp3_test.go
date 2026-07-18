package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// Checkpoint de integración CP-3 (cierra Fase 2; tras S13). Prueba de humo de
// extremo a extremo: la primera vez que "el producto" —un plugin— corre de verdad
// y se recarga. Une lo acumulado en la Fase 2 (S10 eventos, S11 loader, S12
// activación, S13 reload). El plan (implementacion.md §CP-3) exige demostrar:
//
//	(a) dos plugins en disco, uno hace `require` del otro, se cargan en orden
//	    topológico;
//	(b) `core:ready` se emite UNA vez;
//	(c) el `init.lua` del usuario corre el ÚLTIMO;
//	(d) editar un plugin y `reload` NO deja handlers huérfanos (G2);
//	(e) un plugin que lanza en un handler queda aislado por `pcall` sin tumbar a
//	    los demás (ADR-008).
//
// Si CP-3 pasa, la Fase 2 se cierra. Todo en un solo escenario de disco real.
func TestCP3CargarYRecargarPluginsReales(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()

	// --- base: módulo `lua/saludo.lua` del plugin `base`, que `app` requiere. ---
	baseDir := writePlugin(t, root, "base", "1.0", nil, `
_traza = _traza or {}
_traza[#_traza+1] = "base"
`)
	writeModule(t, baseDir, "saludo", `return { texto = "hola" }`)

	// --- app: requiere `base`; usa su módulo; registra un handler que LANZA y un
	//     handler sano; cuenta cuántas veces lo invoca un emit. ---
	writePlugin(t, root, "app", "1.0", []string{"base"}, `
_traza = _traza or {}
_traza[#_traza+1] = "app:" .. require("saludo").texto
-- (e) un handler que lanza: debe quedar aislado por pcall (ADR-008).
enu.events.on("ping", function() error("revienta a propósito") end)
-- handler sano del mismo evento: debe correr pese al anterior.
enu.events.on("ping", function() _sano = (_sano or 0) + 1 end)
`)

	// --- init.lua del usuario: el ÚLTIMO del arranque canónico (§14). ---
	writeUserInit(t, cfg, `
_traza = _traza or {}
_traza[#_traza+1] = "user"
-- cuenta core:ready para verificar que se emite UNA vez.
_ready = 0
enu.events.on("core:ready", function() _ready = _ready + 1 end)
`)

	h := newBootedHarness(t, root, cfg)

	// (a) orden topológico: base antes que app. (c) user el último.
	traza := h.eval(`return table.concat(_traza, ",")`)[0]
	wantPrefix := "base,app:hola,user"
	if traza != wantPrefix {
		t.Fatalf("(a/c) traza de arranque: got %q, want %q", traza, wantPrefix)
	}

	// (b) `core:ready` una sola vez. El handler del usuario se registró DURANTE su
	// init, que corre ANTES de emitir core:ready, así que lo ve exactamente una vez.
	h.expectEval(`return _ready`, "1")

	// (e) un emit "ping" invoca los dos handlers de `app`: el que lanza queda
	// aislado (log), el sano corre. El proceso NO cae; el contador sano sube.
	h.eval(`enu.events.emit("ping")`)
	h.expectEval(`return _sano`, "1")
	assertLogContains(t, h, "revienta a propósito")

	// (d) editar un plugin y recargar no deja handlers huérfanos. Cambiamos el
	// init de `app` para que su handler sano sume DE 10 en 10 (señal de "versión
	// nueva"), recargamos, y comprobamos que tras un emit solo corre la versión
	// nueva (no la vieja además).
	rewritePluginInit(t, root, "app", `
enu.events.on("ping", function() error("revienta v2") end)
enu.events.on("ping", function() _sano = (_sano or 0) + 10 end)
`)
	h.eval(reloadSpawn(`enu.plugin.reload("app")`))

	h.eval(`_sano = 0; enu.events.emit("ping")`)
	// Solo la versión nueva (suma 10). Si las suscripciones viejas de `app`
	// quedaran huérfanas, _sano sería 11 (1 viejo + 10 nuevo).
	h.expectEval(`return _sano`, "10")

	// El plugin `app` queda con exactamente sus 2 handles del init re-ejecutado (no
	// los viejos acumulados). `base` no se recargó: conserva los suyos (ninguno).
	if got := countOwnerHandles(h, "app"); got != 2 {
		t.Fatalf("(d) tras reload, app debe tener 2 handles (los del init v2); hay %d", got)
	}
}

// writeModule crea `dir/lua/<name>.lua` con `body`. Azúcar para CP-3.
func writeModule(t *testing.T, pluginDir, name, body string) {
	t.Helper()
	luaDir := filepath.Join(pluginDir, "lua")
	if err := os.MkdirAll(luaDir, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", luaDir, err)
	}
	if err := os.WriteFile(filepath.Join(luaDir, name+".lua"), []byte(body), 0o644); err != nil {
		t.Fatalf("write módulo %q: %v", name, err)
	}
}

// writeUserInit escribe el `init.lua` del usuario en `configDir`.
func writeUserInit(t *testing.T, configDir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(configDir, pluginInitName), []byte(body), 0o644); err != nil {
		t.Fatalf("write init.lua de usuario: %v", err)
	}
}

// rewritePluginInit sobrescribe el `init.lua` de un plugin ya escrito (simula
// "editar el plugin" antes de un reload).
func rewritePluginInit(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name, pluginInitName), []byte(body), 0o644); err != nil {
		t.Fatalf("rewrite init.lua de %q: %v", name, err)
	}
}
