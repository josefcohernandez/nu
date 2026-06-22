// Command nu es el binario del runtime: un kernel Lua mínimo donde todo lo demás
// son extensiones (filosofia.md). Expone la evaluación de un chunk con `-e` y, sin
// `-e`, el arranque canónico de plugins (§14) o —con un TTY interactivo y ningún
// plugin activo— la PANTALLA DE RUNTIME DESNUDO (§14, G21, S33).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dbareagimeno/nu/internal/runtime"
)

func main() {
	os.Exit(run())
}

func run() int {
	eval := flag.String("e", "", "ejecuta el código Lua dado e imprime sus valores de retorno")
	flag.Parse()

	rt := runtime.New()
	defer rt.Close()

	if *eval == "" {
		// Sin `-e`: si hay un TTY interactivo y NINGÚN plugin activo, se pinta la
		// PANTALLA DE RUNTIME DESNUDO (§14, G21, S33): un render FIJO con la versión y
		// el nivel de API, las rutas de config y plugins, el catálogo de extensiones
		// embebidas disponibles y las acciones (activar el conjunto oficial / sueltas /
		// salir). La elección con el TECLADO (y ver su efecto) la cablea el driver de
		// TTY —es el CP-7 MANUAL—; aquí se pinta la pantalla y se vuelcan sus líneas a
		// stdout. Sin TTY (salida redirigida, CI) no hay pantalla: se imprime el uso.
		if rt.BareScreenActive() {
			for _, ln := range rt.RenderBareScreen() {
				fmt.Println(ln)
			}
			return 0
		}
		fmt.Fprintln(os.Stderr, "uso: nu -e '<código lua>'")
		return 2
	}

	// Arranque canónico (§14, S11/S12): lee `config.dir()/nu.toml` (activación de
	// plugins, rutas extra, presupuesto del watchdog), carga los plugins activados
	// en orden topológico —las extensiones embebidas solo si `plugins.enabled` las
	// nombra, ADR-010—, ejecuta el `init.lua` del usuario el último y emite
	// `core:ready`. Un grafo roto (colisión, ciclo, dependencia ausente), un
	// `nu.toml` mal formado o un `plugins.enabled` que nombra algo inexistente es un
	// error de arranque accionable que apunta a la línea de `nu.toml` que lo arregla.
	if err := rt.Boot(); err != nil {
		fmt.Fprintln(os.Stderr, "error de arranque:", err)
		return 1
	}

	results, err := rt.EvalString(*eval)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	// Imprime cada valor de retorno en su propia línea, a stdout. `print` (que
	// va a stderr en esta sesión) no interfiere con esta salida.
	for _, r := range results {
		fmt.Println(r)
	}
	return 0
}
