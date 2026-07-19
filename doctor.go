// `enu doctor` (S50, ADR-026 pieza 3): diagnóstico de solo lectura del binario y su
// config. Batería de checks con salida humana o `--json` conforme a `doctor.v1`
// (docs/ops/doctor.md). Sin red por defecto (`--net` opt-in). v1 implementa los 7
// checks KERNEL; los 4 de PRODUCTO salen como `skip` (G62: necesitan introspección
// de extensiones que aún no existe, diferida como P45). Superficie CLI (package
// main), no API sagrada.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/dbareagimeno/enu/internal/runtime"
	"golang.org/x/term"
)

const (
	doctorSchema = "doctor.v1"
	statusOKd    = "ok"
	statusFaild  = "fail"
	statusSkipd  = "skip"
)

// doctorCheck es una entrada del array `checks` de `doctor.v1`. `Detail`/`Remedy` son
// punteros para serializar `null` cuando no aplican (doctor.md §esquema): `Remedy`
// solo se rellena en `fail`; en `ok`/`skip` es `null` (la pista de un `skip` va en
// `Detail`).
type doctorCheck struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"`
	Summary string  `json:"summary"`
	Detail  *string `json:"detail"`
	Remedy  *string `json:"remedy"`
}

type doctorCounts struct {
	OK   int `json:"ok"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

type doctorReport struct {
	Schema   string        `json:"schema"`
	Version  string        `json:"version"`
	OS       string        `json:"os"`
	Arch     string        `json:"arch"`
	Checks   []doctorCheck `json:"checks"`
	Counts   doctorCounts  `json:"counts"`
	ExitCode int           `json:"exit_code"`
}

type doctorOpts struct {
	json      bool
	net       bool
	stdoutTTY bool
}

func strptr(s string) *string { return &s }

// g62Skip: pista común de los checks de producto no implementados en v1.
const g62Skip = "check de producto no implementado en v1; ver G62/P45 (doctor.md)"

// runDoctorMain parsea los flags de `enu doctor` (`--json`, `--net`), construye el
// Runtime y delega en `runDoctor` (el núcleo testeable).
func runDoctorMain(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	var jsonOut, net bool
	fs.BoolVar(&jsonOut, "json", false, "salida JSON conforme a doctor.v1")
	fs.BoolVar(&net, "net", false, "incluye el check de red provider.reach (apagado por defecto)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "uso: enu doctor [--json] [--net] (argumento inesperado: %q)\n", fs.Arg(0))
		return exitUsage
	}
	rt := runtime.New()
	defer rt.Close()
	opts := doctorOpts{json: jsonOut, net: net, stdoutTTY: term.IsTerminal(int(os.Stdout.Fd()))}
	return runDoctor(rt, opts, os.Stdout)
}

// runDoctor es el núcleo TESTEABLE: recolecta los checks, calcula `counts` y el código
// de salida (0 todo verde/skip, 1 si hay algún `fail`), y escribe la salida (humana o
// `--json`). Nunca hace red salvo `provider.reach` con `--net` (que en v1 es `skip`).
func runDoctor(rt *runtime.Runtime, opts doctorOpts, out io.Writer) int {
	checks := collectDoctorChecks(rt, opts)
	var counts doctorCounts
	for _, c := range checks {
		switch c.Status {
		case statusOKd:
			counts.OK++
		case statusFaild:
			counts.Fail++
		case statusSkipd:
			counts.Skip++
		}
	}
	exit := exitOK
	if counts.Fail > 0 {
		exit = exitError
	}
	report := doctorReport{
		Schema:   doctorSchema,
		Version:  fmt.Sprintf("%d.%d.%d", runtime.VersionMajor, runtime.VersionMinor, runtime.VersionPatch),
		OS:       goruntime.GOOS,
		Arch:     goruntime.GOARCH,
		Checks:   checks,
		Counts:   counts,
		ExitCode: exit,
	}
	if opts.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		writeDoctorHuman(out, report)
	}
	return exit
}

// collectDoctorChecks corre los checks en el ORDEN del catálogo (doctor.md): los 7
// kernel primero, los 4 de producto (skip, G62) después.
func collectDoctorChecks(rt *runtime.Runtime, opts doctorOpts) []doctorCheck {
	diag := rt.DiagnosePluginGraph()
	return []doctorCheck{
		checkBinaryVersion(),
		checkConfigDir(rt),
		checkConfigParse(rt),
		checkPluginsEnabled(diag),
		checkPluginsRequires(diag),
		checkSessionsPerms(rt),
		checkTTYCaps(opts),
		// Producto (skip en v1, G62/P45):
		{ID: "provider.model", Status: statusSkipd, Summary: "resolución del modelo por defecto", Detail: strptr(g62Skip)},
		{ID: "provider.key", Status: statusSkipd, Summary: "variable api_key_env del provider", Detail: strptr(g62Skip)},
		{ID: "tools.external", Status: statusSkipd, Summary: "herramientas externas de las extensiones", Detail: strptr(g62Skip)},
		checkProviderReach(opts),
	}
}

func checkBinaryVersion() doctorCheck {
	d := fmt.Sprintf("enu %d.%d.%d · API %d (%s/%s)",
		runtime.VersionMajor, runtime.VersionMinor, runtime.VersionPatch, runtime.APILevel,
		goruntime.GOOS, goruntime.GOARCH)
	return doctorCheck{ID: "binary.version", Status: statusOKd, Summary: "versión y arquitectura del binario", Detail: strptr(d)}
}

func checkConfigDir(rt *runtime.Runtime) doctorCheck {
	dir := rt.ConfigDir()
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		// Ausente = runtime desnudo (ADR-010), no es error.
		return doctorCheck{ID: "config.dir", Status: statusOKd, Summary: "directorio de configuración", Detail: strptr(dir + " (ausente: runtime desnudo)")}
	}
	if err != nil || !info.IsDir() {
		return doctorCheck{ID: "config.dir", Status: statusFaild, Summary: "directorio de configuración",
			Detail: strptr(dir), Remedy: strptr("no es un directorio legible: revisa " + dir)}
	}
	if _, rerr := os.ReadDir(dir); rerr != nil {
		return doctorCheck{ID: "config.dir", Status: statusFaild, Summary: "directorio de configuración",
			Detail: strptr(dir), Remedy: strptr("sin permiso de lectura: chmod +rx " + dir)}
	}
	return doctorCheck{ID: "config.dir", Status: statusOKd, Summary: "directorio de configuración", Detail: strptr(dir)}
}

func checkConfigParse(rt *runtime.Runtime) doctorCheck {
	dir := rt.ConfigDir()
	files := []string{"enu.toml", "agent.toml", "providers.toml"}
	var states, broken []string
	for _, f := range files {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			states = append(states, f+": ausente")
			continue
		}
		var v map[string]any
		if _, err := toml.DecodeFile(path, &v); err != nil {
			states = append(states, f+": ROTO ("+err.Error()+")")
			broken = append(broken, f)
		} else {
			states = append(states, f+": ok")
		}
	}
	detail := strings.Join(states, "; ")
	if len(broken) > 0 {
		return doctorCheck{ID: "config.parse", Status: statusFaild, Summary: "sintaxis TOML de la config",
			Detail: strptr(detail), Remedy: strptr("corrige el TOML de: " + strings.Join(broken, ", "))}
	}
	return doctorCheck{ID: "config.parse", Status: statusOKd, Summary: "sintaxis TOML de la config", Detail: strptr(detail)}
}

func checkPluginsEnabled(diag runtime.PluginGraphDiag) doctorCheck {
	if diag.EnabledOK {
		return doctorCheck{ID: "plugins.enabled", Status: statusOKd, Summary: "los plugins activados existen"}
	}
	return doctorCheck{ID: "plugins.enabled", Status: statusFaild, Summary: "los plugins activados existen",
		Detail: strptr(diag.EnabledDetail), Remedy: strptr("corrige plugins.enabled en enu.toml (ver el detalle)")}
}

func checkPluginsRequires(diag runtime.PluginGraphDiag) doctorCheck {
	if !diag.RequiresRun {
		return doctorCheck{ID: "plugins.requires", Status: statusSkipd, Summary: "dependencias de los plugins",
			Detail: strptr("no evaluable: el descubrimiento de plugins falló (ver plugins.enabled)")}
	}
	if diag.RequiresOK {
		return doctorCheck{ID: "plugins.requires", Status: statusOKd, Summary: "dependencias de los plugins"}
	}
	return doctorCheck{ID: "plugins.requires", Status: statusFaild, Summary: "dependencias de los plugins",
		Detail: strptr(diag.RequiresDetail), Remedy: strptr("resuelve las dependencias `requires` (ver el detalle)")}
}

func checkSessionsPerms(rt *runtime.Runtime) doctorCheck {
	root := filepath.Join(rt.DataDir(), "sessions")
	var offenders []string
	seen := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // un subárbol ilegible no rompe el muestreo
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		seen++
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Mode().Perm() != 0o600 {
			offenders = append(offenders, fmt.Sprintf("%s (%04o)", path, info.Mode().Perm()))
		}
		return nil
	})
	if seen == 0 {
		return doctorCheck{ID: "sessions.perms", Status: statusSkipd, Summary: "permisos 0600 de las sesiones",
			Detail: strptr("sin sesiones que muestrear en " + root)}
	}
	if len(offenders) > 0 {
		return doctorCheck{ID: "sessions.perms", Status: statusFaild, Summary: "permisos 0600 de las sesiones",
			Detail: strptr(strings.Join(offenders, "; ")), Remedy: strptr("chmod 0600 los transcripts listados (G57)")}
	}
	return doctorCheck{ID: "sessions.perms", Status: statusOKd, Summary: "permisos 0600 de las sesiones",
		Detail: strptr(fmt.Sprintf("%d transcript(s) en 0600", seen))}
}

func checkTTYCaps(opts doctorOpts) doctorCheck {
	if !opts.stdoutTTY {
		return doctorCheck{ID: "tty.caps", Status: statusSkipd, Summary: "TTY y capacidades del terminal",
			Detail: strptr("stdout no es un TTY (headless/CI)")}
	}
	return doctorCheck{ID: "tty.caps", Status: statusOKd, Summary: "TTY y capacidades del terminal", Detail: strptr("stdout es un TTY")}
}

func checkProviderReach(opts doctorOpts) doctorCheck {
	if !opts.net {
		return doctorCheck{ID: "provider.reach", Status: statusSkipd, Summary: "alcanzabilidad del provider",
			Detail: strptr("requiere --net (sin red por defecto)")}
	}
	// Con --net seguiría siendo producto: no implementado en v1 (G62/P45).
	return doctorCheck{ID: "provider.reach", Status: statusSkipd, Summary: "alcanzabilidad del provider", Detail: strptr(g62Skip)}
}

// writeDoctorHuman pinta el informe legible: una línea por check con su estado, y un
// resumen. La clave jamás aparece: ningún check kernel la lee, y los de producto son
// skip.
func writeDoctorHuman(out io.Writer, r doctorReport) {
	emitf(out, "enu doctor · v%s (%s/%s) · API %d\n", r.Version, r.OS, r.Arch, runtime.APILevel)
	for _, c := range r.Checks {
		mark := map[string]string{statusOKd: "ok  ", statusFaild: "FAIL", statusSkipd: "skip"}[c.Status]
		emitf(out, "  [%s] %-18s %s\n", mark, c.ID, c.Summary)
		if c.Detail != nil && (c.Status == statusFaild || c.Status == statusSkipd) {
			emitf(out, "         %s\n", *c.Detail)
		}
		if c.Remedy != nil {
			emitf(out, "         → %s\n", *c.Remedy)
		}
	}
	emitf(out, "resumen: %d ok · %d fail · %d skip\n", r.Counts.OK, r.Counts.Fail, r.Counts.Skip)
}
