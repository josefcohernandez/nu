package runtime

import (
	"fmt"
	"os"
)

// Desde M17 (la retirada) el kernel tiene UNA sola VM: **wasm** (PUC-Lua oficial
// sobre wazero, internal/vmwasm). gopher-lua se eliminó del binario. El selector
// del patrón estrangulador (DM2: `enu.toml [vm] backend` + env `NU_VM`) se retiró
// con él; sólo queda este tipo mínimo por compatibilidad de la API interna del
// Runtime (`VMBackend()`, `WithVMBackend`) y el aviso de tolerancia de abajo.
type VMBackend int

// VMWasm es el único backend que existe: la VM productiva del kernel.
const VMWasm VMBackend = iota

func (b VMBackend) String() string { return "wasm" }

// warnIfGopherRequested emite un aviso claro por stderr si el entorno (`NU_VM`) o
// `enu.toml [vm] backend` piden todavía el backend `gopher`, ya retirado (M17). NO
// rompe el arranque: enu ignora la petición y sigue sobre wasm. Es la tolerancia
// del ciclo de gracia para configs/scripts que aún fijaban el backend legacy.
func warnIfGopherRequested(tomlBackend string) {
	req := os.Getenv("NU_VM")
	if req == "" {
		req = tomlBackend
	}
	if req == "gopher" {
		fmt.Fprintln(os.Stderr,
			"enu: el backend gopher se retiró (M17); enu corre siempre sobre wasm")
	}
}
