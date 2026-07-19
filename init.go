// Subcomandos de GESTIÓN del binario (ADR-026): `enu <subcomando>`. El primero es
// `enu init` (S49), el flujo de configuración guiado. La regla de frontera (pieza 1)
// es dura: los subcomandos gestionan el binario y su config; la funcionalidad de
// PRODUCTO (agente, chat, evaluar) va por `enu` y sus flags, no por subcomandos —no
// hay `enu chat` ni `enu run`—. Igual que el resto de la superficie CLI (S45), esto
// vive en `package main`, no en la API sagrada `enu.*`: el core no lo conoce.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dbareagimeno/enu/internal/runtime"
	"golang.org/x/term"
)

// emitf/emitln escriben mensajes de `init`/wizard a un `io.Writer`, descartando A
// PROPÓSITO el error de escritura: un stdout roto no es accionable en este flujo
// interactivo, y centralizar el descarte evita salpicar `_, _ =` por todo el wizard
// (errcheck lo ve una sola vez). Los errores REALES —de escritura de config— sí se
// comprueban (WriteInitConfig/WriteDefaultConfig).
func emitf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func emitln(w io.Writer, a ...any)               { _, _ = fmt.Fprintln(w, a...) }

// El conjunto CERRADO de verbos de gestión (ADR-026 pieza 1) es el `switch` de abajo:
// `init` implementado (S49); `doctor`/`update`/`uninstall` reservados (S50/S51). Nada
// de producto entra ahí.

// dispatchSubcommand decide si os.Args (sin el argv[0]) expresa un SUBCOMANDO y, si es
// así, lo ejecuta. GRAMÁTICA (decisión de S49, superficie CLI, no espec sagrada): el
// primer argumento que NO empieza por '-' es candidato a subcomando. Devuelve
// `(handled, code)`: si `handled`, `run` retorna `code`; si no, el flujo sigue con el
// parseo de flags legado (modos `-e`/`-p`/`--default-config`, intactos). Esto mantiene
// `enu -e 'return 1'` funcionando: `-e` empieza por '-', no es subcomando.
func dispatchSubcommand(args []string) (handled bool, code int) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return false, exitOK // no hay subcomando: parseo de flags legado
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "init":
		return true, runInitMain(rest)
	case "doctor":
		return true, runDoctorMain(rest)
	case "update", "uninstall":
		fmt.Fprintf(os.Stderr, "el subcomando '%s' está reservado (ADR-026) pero aún no está implementado; llega en una sesión posterior\n", sub)
		return true, exitError
	default:
		// Regla de frontera (ADR-026 pieza 1): no existen subcomandos de PRODUCTO.
		fmt.Fprintf(os.Stderr, "subcomando desconocido: %q\n", sub)
		fmt.Fprintln(os.Stderr, "subcomandos de gestión: init, doctor, update, uninstall")
		fmt.Fprintln(os.Stderr, "la funcionalidad de producto (agente, chat) va por `enu` y sus flags, no por un subcomando (ADR-026)")
		return true, exitUsage
	}
}

// runInitMain parsea los flags de `enu init` (`--yes`), construye el Runtime y delega
// en `runInit` (el núcleo testeable). Detecta si STDIN es un TTY —el wizard LEE del
// usuario, así que lo que importa es la interactividad de stdin, no de stdout—.
func runInitMain(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	var yes bool
	fs.BoolVar(&yes, "yes", false, "no interactivo: equivale a `enu --default-config` (plantilla anthropic)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "uso: enu init [--yes] (argumento inesperado: %q)\n", fs.Arg(0))
		return exitUsage
	}
	rt := runtime.New()
	defer rt.Close()
	isTTY := term.IsTerminal(int(os.Stdin.Fd()))
	return runInit(rt, yes, isTTY, os.Stdin, os.Stdout)
}

// runInit es el núcleo TESTEABLE de `enu init` (ADR-026 pieza 2): recibe el Runtime, el
// flag `--yes`, si stdin es un TTY, y los streams inyectables. SIN TTY o con `--yes`:
// equivale EXACTAMENTE a `enu --default-config` (mismos bytes, vía WriteDefaultConfig).
// CON TTY: el wizard anthropic-only (G61). Nunca sobrescribe config; sin red (ADR-010).
func runInit(rt *runtime.Runtime, yes, isTTY bool, in io.Reader, out io.Writer) int {
	if yes || !isTTY {
		return initNonInteractive(rt, out)
	}
	return initWizard(rt, in, out)
}

// initNonInteractive es el camino `--yes`/sin-TTY: llama a WriteDefaultConfig —el MISMO
// que `enu --default-config`—, así los ficheros resultantes son idénticos byte a byte
// (la equivalencia de ADR-026 pieza 2 es un contrato, no una intención). Imprime el
// mensaje honesto: qué activó, qué plantillas creó, y el recordatorio de la API key.
func initNonInteractive(rt *runtime.Runtime, out io.Writer) int {
	dir, names, created, err := rt.WriteDefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitError
	}
	emitf(out, "conjunto oficial de producto activado en %s/enu.toml: %s\n",
		dir, strings.Join(names, ", "))
	if len(created) > 0 {
		emitf(out, "plantillas de config creadas: %s\n", strings.Join(created, ", "))
	} else {
		emitln(out, "las plantillas de config ya existían: se respetan (no-op)")
	}
	emitf(out, "exporta tu API key (p. ej. ANTHROPIC_API_KEY) o edita %s/providers.toml; "+
		"luego ejecuta `enu` (chat) o `enu -p '<prompt>'` (headless)\n", dir)
	return exitOK
}

// initWizard es el flujo GUIADO con TTY (ADR-026 pieza 2). Anthropic-only en v1 (G61):
// provider (anthropic) → clave por variable de entorno (detecta presencia, NUNCA la
// escribe, providers.md §1) → modelo (propone el default, acepta con enter o teclea
// otro) → activar el conjunto oficial (sí/no). Lee TODAS las respuestas antes de
// escribir nada: un EOF/ctrl-d a mitad aborta SIN dejar ficheros a medias. Sin red.
func initWizard(rt *runtime.Runtime, in io.Reader, out io.Writer) int {
	r := bufio.NewReader(in)
	const keyEnv = "ANTHROPIC_API_KEY"
	const defModel = "anthropic/opus"

	emitln(out, "enu init — configuración guiada")
	// 1. Provider. v1 ofrece solo anthropic (G61); se enuncia, no se pregunta.
	emitln(out, "provider: anthropic (el asistente v1 solo configura anthropic; edita providers.toml para otros)")

	// 2. Clave por variable de entorno (nunca al fichero).
	if os.Getenv(keyEnv) != "" {
		emitf(out, "clave: %s detectada en el entorno — se referenciará como api_key_env, nunca se escribe en fichero\n", keyEnv)
	} else {
		emitf(out, "clave: %s no está exportada; expórtala antes de usar el agente (nunca se escribe en fichero)\n", keyEnv)
	}

	// 3. Modelo: propone el default, acepta con enter o teclea otro.
	emitf(out, "modelo por defecto [%s]: ", defModel)
	line, err := readLine(r)
	if err != nil {
		return abortWizard(out)
	}
	model := strings.TrimSpace(line)
	if model == "" {
		model = defModel
	}

	// 4. Activar el conjunto oficial de producto (por defecto sí).
	emitf(out, "¿activar el conjunto oficial de producto (agente, chat, providers…)? [S/n]: ")
	line, err = readLine(r)
	if err != nil {
		return abortWizard(out)
	}
	activate := !strings.EqualFold(strings.TrimSpace(line), "n")

	// Escritura (una sola pasada, tras leer todo): nunca sobrescribe (por-fichero).
	dir, activated, created, respected, werr := rt.WriteInitConfig(model, activate)
	if werr != nil {
		fmt.Fprintln(os.Stderr, "error:", werr)
		return exitError
	}

	if activate {
		emitf(out, "conjunto oficial activado en %s/enu.toml: %s\n", dir, strings.Join(activated, ", "))
	} else {
		emitf(out, "conjunto oficial NO activado (edita %s/enu.toml para activarlo)\n", dir)
	}
	if len(created) > 0 {
		emitf(out, "plantillas creadas: %s\n", strings.Join(created, ", "))
	}
	if len(respected) > 0 {
		emitf(out, "config ya existente, respetada: %s\n", strings.Join(respected, ", "))
	}
	emitf(out, "listo. Exporta %s (o edita %s/providers.toml) y ejecuta `enu`\n", keyEnv, dir)
	return exitOK
}

// readLine lee una línea (hasta '\n') del lector. Un EOF sin datos devuelve io.EOF: el
// wizard lo trata como abandono (ctrl-d / stdin cerrado) y aborta sin escribir.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}

// abortWizard reporta un wizard abandonado (EOF a mitad) sin haber escrito ficheros y
// sale con código no cero (1): la interacción no se completó.
func abortWizard(out io.Writer) int {
	emitln(out, "\ninit cancelado: no se escribió ninguna configuración")
	return exitError
}
