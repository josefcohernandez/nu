package runtime

import (
	"strings"
	"testing"
)

// Tras la retirada de gopher-lua (M17) el kernel corre SIEMPRE sobre wasm y el
// selector de backend desapareció. Lo único que queda del patrón estrangulador es
// la TOLERANCIA: una config o un script que todavía pidan el backend `gopher`
// (vía `NU_VM=gopher` o `enu.toml [vm] backend`) no deben romper el arranque —enu
// ignora la petición, avisa por stderr y sigue sobre wasm—.
//
// Este test blinda esa tolerancia: con `NU_VM=gopher` en el entorno, `New` no
// rompe y el runtime evalúa Lua con normalidad sobre wasm (PUC-Lua reporta
// "Lua 5.4").
func TestRetiradaGopherTolerada(t *testing.T) {
	t.Setenv("NU_VM", "gopher")

	rt := New(WithDataDir(t.TempDir()), WithConfigDir(t.TempDir()))
	defer rt.Close()

	res, err := rt.EvalString("return _VERSION")
	if err != nil {
		t.Fatalf("pese a NU_VM=gopher, el runtime debe evaluar sobre wasm: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("_VERSION devolvió %d valores, want 1: %v", len(res), res)
	}
	if got := strings.TrimSpace(res[0]); got != "Lua 5.4" {
		t.Fatalf("_VERSION = %q, want \"Lua 5.4\" (wasm, PUC-Lua)", got)
	}
}
