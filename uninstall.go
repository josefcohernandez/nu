// `enu uninstall` (S51, ADR-026 pieza 4; espec en release.md §Instalador): elimina el
// binario en uso e informa de qué NO borra. Con `--purge` borra además —y EXCLUSIVAMENTE—
// `config.dir()` (~/.config/enu), con confirmación explícita. `data_dir()`
// (~/.local/share/enu: sesiones, transcripts, plugins instalados, log) NO se toca NUNCA,
// ni con `--purge`: es el trabajo del usuario, y borrarlo por sorpresa sería el fallo
// silencioso imperdonable. Superficie CLI (package main), no API sagrada.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dbareagimeno/enu/internal/runtime"
)

// runUninstallMain parsea `enu uninstall [--purge]`, resuelve el entorno real (ruta del
// propio binario y los directorios de config/datos) y delega en `runUninstall`.
func runUninstallMain(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	var purge bool
	fs.BoolVar(&purge, "purge", false, "borra además config.dir() (~/.config/enu), con confirmación; data_dir() nunca se toca")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "uso: enu uninstall [--purge] (argumento inesperado: %q)\n", fs.Arg(0))
		return exitUsage
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: no pude resolver la ruta del binario en uso:", err)
		return exitError
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	rt := runtime.New()
	defer rt.Close()
	return runUninstall(exe, rt.ConfigDir(), rt.DataDir(), purge, os.Stdin, os.Stdout)
}

// runUninstall es el núcleo TESTEABLE: recibe la ruta del binario, los directorios de
// config y datos, el flag `--purge` y los streams inyectables. SIEMPRE elimina el binario
// e informa de qué deja en pie. Con `--purge`, pide confirmación y —solo si se concede—
// borra EXACTAMENTE `configDir` (nunca su padre, nunca `dataDir`). Si `dataDir` cae dentro
// de `configDir` (configuración atípica), rehúsa el purge para no arrastrar los datos.
func runUninstall(binPath, configDir, dataDir string, purge bool, in io.Reader, out io.Writer) int {
	// 1. Eliminar el binario. Si ya no está, es un no-op honesto (no un error).
	if err := os.Remove(binPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: no pude eliminar el binario %s: %v\n", binPath, err)
		fmt.Fprintln(os.Stderr, "si lo instaló un gestor de paquetes o vive en un directorio protegido, desinstálalo por esa vía (enu no eleva privilegios).")
		return exitError
	}
	emitf(out, "binario eliminado: %s\n", binPath)

	// 2. Sin --purge: se informa de lo que NO se toca y se termina.
	if !purge {
		emitf(out, "no se ha tocado tu configuración (%s) ni tus datos (%s).\n", configDir, dataDir)
		emitf(out, "para borrar también la configuración: enu uninstall --purge (los datos NUNCA se borran).\n")
		return exitOK
	}

	// 3. --purge: guardia de seguridad. Nunca borrar si data_dir vive dentro de config_dir
	// (un RemoveAll(configDir) se llevaría los datos por delante).
	if dataDir == configDir || isWithin(dataDir, configDir) {
		fmt.Fprintf(os.Stderr, "aviso: tus datos (%s) están dentro de la config (%s); no borro la config para no arrastrarlos.\n", dataDir, configDir)
		return exitOK
	}

	// 4. Confirmación explícita (por defecto NO: el purge es destructivo).
	emitf(out, "¿borrar tu configuración en %s? Tus datos en %s NO se tocan. [y/N]: ", configDir, dataDir)
	answer, _ := bufio.NewReader(in).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" && answer != "s" && answer != "si" {
		emitf(out, "purge cancelado: se conserva la configuración en %s.\n", configDir)
		return exitOK
	}

	// 5. Borrar EXACTAMENTE config.dir(). data_dir() queda intacto por construcción.
	if err := os.RemoveAll(configDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: no pude borrar la configuración %s: %v\n", configDir, err)
		return exitError
	}
	emitf(out, "configuración borrada: %s\n", configDir)
	emitf(out, "tus datos siguen intactos: %s\n", dataDir)
	return exitOK
}

// isWithin indica si `child` está DENTRO del árbol de `parent` (no meramente que comparta
// prefijo textual: `/a/bc` no está dentro de `/a/b`). Ambos se limpian antes de comparar.
func isWithin(child, parent string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
