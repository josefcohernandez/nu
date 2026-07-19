package main

// Tests del flag `--version`/`-V` (S53, Fase 10 — convenciones CLI). Superficie CLI
// (package main), no api.md. El flag es un wrapper fino sobre `versionString()`; no lleva
// fila 🔒. Se cubre el formato (la fuente compartida con el check `binary.version` de
// doctor) y el núcleo testeable `runVersion` (imprime + sale 0, sin arrancar el runtime).

import (
	"bytes"
	"fmt"
	"regexp"
	"testing"

	"github.com/dbareagimeno/enu/internal/runtime"
)

// El formato exacto que la superficie CLI promete (arquitectura.md §5): identidad del
// binario en una línea. Ancla el semver, el nivel de API y os/arch reales de este build.
func TestVersionString(t *testing.T) {
	got := versionString()
	if ok, _ := regexp.MatchString(`^enu \d+\.\d+\.\d+ · API \d+ \([a-z0-9]+/[a-z0-9]+\)$`, got); !ok {
		t.Fatalf("formato inesperado: %q", got)
	}
	// Debe reflejar las constantes reales del binario, no un literal.
	wantSemver := fmt.Sprintf("enu %d.%d.%d", runtime.VersionMajor, runtime.VersionMinor, runtime.VersionPatch)
	wantAPI := fmt.Sprintf("API %d", runtime.APILevel)
	if !bytes.Contains([]byte(got), []byte(wantSemver)) {
		t.Fatalf("no contiene el semver real %q: %q", wantSemver, got)
	}
	if !bytes.Contains([]byte(got), []byte(wantAPI)) {
		t.Fatalf("no contiene el nivel de API real %q: %q", wantAPI, got)
	}
}

// runVersion imprime EXACTAMENTE la línea de versión (+ salto) y sale 0, sin construir un
// runtime (el `out` inyectable prueba que no hay dependencia de config/TTY).
func TestRunVersion(t *testing.T) {
	var buf bytes.Buffer
	code := runVersion(&buf)
	if code != exitOK {
		t.Fatalf("runVersion debe salir 0, obtuve %d", code)
	}
	want := versionString() + "\n"
	if buf.String() != want {
		t.Fatalf("salida inesperada: got %q, want %q", buf.String(), want)
	}
}

// El check `binary.version` de `enu doctor` y el flag `--version` comparten fuente: su
// contenido debe COINCIDIR (no dos formatos que puedan divergir). Blinda la
// deduplicación introducida en S53.
func TestVersionMatchesDoctorCheck(t *testing.T) {
	c := checkBinaryVersion()
	if c.Detail == nil {
		t.Fatal("binary.version debe traer detalle")
	}
	if *c.Detail != versionString() {
		t.Fatalf("doctor binary.version %q != --version %q (formatos divergieron)", *c.Detail, versionString())
	}
}
