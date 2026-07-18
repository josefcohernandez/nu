package runtime

// G36: el repl CEDE la pantalla al chat. El conjunto oficial de producto (ADR-015)
// activa chat Y repl; antes ambos auto-montaban una app a pantalla completa en
// `core:ready` y se solapaban (salir del chat dejaba el REPL debajo). Ahora el repl
// solo auto-monta su UI si el chat NO está activo (lo comprueba con `enu.plugin.list`,
// sin depender de chat —activable SOLO, G21—).

import (
	"path/filepath"
	"testing"
)

// bootWith arranca un runtime con `enabled` activado, UI forzada y un tamaño conocido.
func bootWith(t *testing.T, enabled string) *harness {
	t.Helper()
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = "+enabled+"\n")
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithForceUI(true), WithUISize(80, 24))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}
}

// TestG36ReplCedeAlChat: con chat activo, el repl NO monta su UI (su `_active` queda
// nil); el chat es quien posee la pantalla.
func TestG36ReplCedeAlChat(t *testing.T) {
	// providers.toml para que el chat arranque (modelo resoluble); aun degradado, el
	// plugin `chat` está activo en enu.plugin.list, que es lo que mira el repl.
	cfg := t.TempDir()
	writeNuToml(t, cfg,
		"[plugins]\nenabled = [\"toolkit\", \"providers\", \"sessions\", \"agent\", \"chat\", \"repl\"]\n")
	mustWrite(t, filepath.Join(cfg, "providers.toml"), providersTomlChatStub)
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithForceUI(true), WithUISize(80, 24))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	h := &harness{t: t, rt: rt}
	// el repl está cargado (require funciona) pero NO arrancó su UI: cedió al chat.
	h.expectEval(`return tostring(require("repl") ~= nil)`, "true")
	h.expectEval(`return tostring(require("repl")._active == nil)`, "true")
}

// TestG36ReplSoloMontaUI (control): sin chat, el repl SÍ monta su UI (activable solo,
// G21). Es la prueba de que la cesión es CONDICIONAL al chat, no un apagado del repl.
func TestG36ReplSoloMontaUI(t *testing.T) {
	h := bootWith(t, `["toolkit", "repl"]`)
	h.expectEval(`return tostring(require("repl")._active ~= nil)`, "true")
}
