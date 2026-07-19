package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTarGz construye un tar.gz con una sola entrada `enu` de contenido `bin` y devuelve
// sus bytes. Reproduce la forma del artefacto de release.yml para los tests de `update`.
func makeTarGz(t *testing.T, bin []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "enu", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tw.Write(bin); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeFetcher implementa releaseFetcher sin red. Registra si se descargó algo (para
// afirmar que los caminos de no-op / destino-no-escribible NO tocan la red).
type fakeFetcher struct {
	latest         string
	latestErr      error
	tarball        []byte
	checksums      []byte
	downloadErr    error
	downloadCalled bool
	latestCalled   bool
}

func (f *fakeFetcher) LatestStable() (string, error) {
	f.latestCalled = true
	return f.latest, f.latestErr
}

func (f *fakeFetcher) Download(url string) ([]byte, error) {
	f.downloadCalled = true
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	if strings.HasSuffix(url, "checksums.txt") {
		return f.checksums, nil
	}
	return f.tarball, nil
}

// --- verifyChecksum: núcleo 🔒 compartido, table-driven ---------------------------

func TestVerifyChecksum(t *testing.T) {
	data := []byte("artefacto de prueba")
	good := sha256hex(data)
	name := "enu-v1.2.3-linux-amd64.tar.gz"

	cases := []struct {
		nombre    string
		checksums string
		wantErr   bool
	}{
		{
			nombre:    "dado_hash_correcto_entonces_ok",
			checksums: good + "  " + name + "\n",
			wantErr:   false,
		},
		{
			nombre:    "dado_hash_correcto_con_prefijo_estrella_entonces_ok",
			checksums: good + "  *" + name + "\n",
			wantErr:   false,
		},
		{
			nombre:    "dado_hash_en_mayusculas_entonces_ok",
			checksums: strings.ToUpper(good) + "  " + name + "\n",
			wantErr:   false,
		},
		{
			nombre:    "dado_hash_incorrecto_entonces_error",
			checksums: strings.Repeat("0", 64) + "  " + name + "\n",
			wantErr:   true,
		},
		{
			nombre:    "dado_nombre_ausente_entonces_error",
			checksums: good + "  otro-artefacto.tar.gz\n",
			wantErr:   true,
		},
		{
			nombre:    "dado_checksums_vacio_entonces_error",
			checksums: "",
			wantErr:   true,
		},
		{
			nombre:    "dado_varias_entradas_entonces_elige_la_correcta",
			checksums: strings.Repeat("a", 64) + "  otro.tar.gz\n" + good + "  " + name + "\n",
			wantErr:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.nombre, func(t *testing.T) {
			err := verifyChecksum(data, name, c.checksums)
			if c.wantErr && err == nil {
				t.Fatalf("esperaba error, obtuve nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("esperaba ok, obtuve error: %v", err)
			}
		})
	}
}

// --- runUpdate: flujos ------------------------------------------------------------

// writeDestBin crea un fichero "binario instalado" con contenido conocido en un dir
// temporal escribible y devuelve su ruta.
func writeDestBin(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	dest := filepath.Join(dir, "enu")
	if err := os.WriteFile(dest, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile dest: %v", err)
	}
	return dest
}

func TestUpdateSameVersionEsNoOp(t *testing.T) {
	dest := writeDestBin(t, "binario-viejo")
	f := &fakeFetcher{}
	var out bytes.Buffer
	cfg := updateConfig{
		currentTag: "v0.2.0",
		pinnedVer:  "v0.2.0", // misma versión pineada
		destPath:   dest,
		os:         "linux", arch: "amd64",
		fetcher: f,
	}
	code := runUpdate(cfg, &out)
	if code != exitOK {
		t.Fatalf("esperaba exitOK, obtuve %d", code)
	}
	if f.downloadCalled {
		t.Fatalf("un no-op no debe descargar nada")
	}
	if got, _ := os.ReadFile(dest); string(got) != "binario-viejo" {
		t.Fatalf("el binario cambió en un no-op: %q", got)
	}
	if !strings.Contains(out.String(), "ya estás") {
		t.Fatalf("mensaje de no-op esperado, salida: %q", out.String())
	}
}

func TestUpdateChecksumCorruptoNoTocaElBinario(t *testing.T) { // 🔒 garantía S51
	dest := writeDestBin(t, "binario-viejo")
	newBin := makeTarGz(t, []byte("binario-nuevo"))
	f := &fakeFetcher{
		tarball:   newBin,
		checksums: []byte(strings.Repeat("0", 64) + "  enu-v0.3.0-linux-amd64.tar.gz\n"), // hash falso
	}
	var out bytes.Buffer
	cfg := updateConfig{
		currentTag: "v0.2.0", pinnedVer: "v0.3.0",
		destPath: dest, os: "linux", arch: "amd64", fetcher: f,
	}
	code := runUpdate(cfg, &out)
	if code != exitError {
		t.Fatalf("esperaba exitError ante checksum corrupto, obtuve %d", code)
	}
	if got, _ := os.ReadFile(dest); string(got) != "binario-viejo" {
		t.Fatalf("CRÍTICO: el binario instalado se tocó pese al checksum corrupto: %q", got)
	}
}

func TestUpdateChecksumsAusenteNoTocaElBinario(t *testing.T) { // 🔒 (checksums.txt ausente ≠ corrupto)
	// La espec trata "checksums ausente" como caso propio junto a "corrupto": ambos
	// abortan sin tocar el binario. checksums.txt vacío → verifyChecksum no halla la
	// entrada → aborta.
	dest := writeDestBin(t, "binario-viejo")
	f := &fakeFetcher{
		tarball:   makeTarGz(t, []byte("binario-nuevo")),
		checksums: []byte(""), // ausente/vacío
	}
	var out bytes.Buffer
	cfg := updateConfig{
		currentTag: "v0.2.0", pinnedVer: "v0.3.0",
		destPath: dest, os: "linux", arch: "amd64", fetcher: f,
	}
	if code := runUpdate(cfg, &out); code != exitError {
		t.Fatalf("esperaba exitError con checksums ausente, obtuve %d", code)
	}
	if got, _ := os.ReadFile(dest); string(got) != "binario-viejo" {
		t.Fatalf("CRÍTICO: el binario se tocó con checksums.txt ausente: %q", got)
	}
}

func TestUpdateCaminoFelizReemplazaElBinario(t *testing.T) {
	dest := writeDestBin(t, "binario-viejo")
	tarball := makeTarGz(t, []byte("binario-nuevo"))
	name := "enu-v0.3.0-linux-amd64.tar.gz"
	f := &fakeFetcher{
		tarball:   tarball,
		checksums: []byte(sha256hex(tarball) + "  " + name + "\n"),
	}
	var out bytes.Buffer
	cfg := updateConfig{
		currentTag: "v0.2.0", pinnedVer: "v0.3.0",
		destPath: dest, os: "linux", arch: "amd64", fetcher: f,
	}
	code := runUpdate(cfg, &out)
	if code != exitOK {
		t.Fatalf("esperaba exitOK, obtuve %d (salida: %q)", code, out.String())
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "binario-nuevo" {
		t.Fatalf("el binario no se reemplazó: %q", got)
	}
	// El reemplazo debe conservar permisos de ejecución.
	info, _ := os.Stat(dest)
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("el binario nuevo no es ejecutable: %v", info.Mode())
	}
}

func TestUpdateDestinoNoEscribibleAbortaConRemedioSinDescargar(t *testing.T) {
	// Destino cuyo "directorio" es en realidad un fichero: escribir el sidecar falla
	// (ENOTDIR) incluso como root, ejerciendo el camino "no escribible → aborta con
	// remedio" sin depender de bits de permiso (que root ignoraría).
	dir := t.TempDir()
	fakeParent := filepath.Join(dir, "soy-un-fichero")
	if err := os.WriteFile(fakeParent, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dest := filepath.Join(fakeParent, "enu") // parent no es un directorio
	f := &fakeFetcher{}
	var out bytes.Buffer
	cfg := updateConfig{
		currentTag: "v0.2.0", pinnedVer: "v0.3.0",
		destPath: dest, os: "linux", arch: "amd64", fetcher: f,
	}
	var code int
	_, stderr := captureOutput(t, func() { code = runUpdate(cfg, &out) })
	if code != exitError {
		t.Fatalf("esperaba exitError, obtuve %d", code)
	}
	if f.downloadCalled {
		t.Fatalf("un destino no escribible no debe descargar (aborta antes)")
	}
	// El aborto debe dar REMEDIO y afirmar que no eleva privilegios (release.md).
	if !strings.Contains(stderr, "privilegios") || !strings.Contains(stderr, "ENU_INSTALL_DIR") {
		t.Fatalf("el aborto debe ofrecer remedio sin elevar privilegios; stderr: %q", stderr)
	}
}

func TestUpdateSinPinResuelveLaUltimaEstable(t *testing.T) {
	dest := writeDestBin(t, "binario-viejo")
	tarball := makeTarGz(t, []byte("binario-nuevo"))
	name := "enu-v0.9.0-linux-amd64.tar.gz"
	f := &fakeFetcher{
		latest:    "v0.9.0",
		tarball:   tarball,
		checksums: []byte(sha256hex(tarball) + "  " + name + "\n"),
	}
	var out bytes.Buffer
	cfg := updateConfig{
		currentTag: "v0.2.0", pinnedVer: "", // sin pin → usa LatestStable
		destPath: dest, os: "linux", arch: "amd64", fetcher: f,
	}
	code := runUpdate(cfg, &out)
	if code != exitOK {
		t.Fatalf("esperaba exitOK, obtuve %d (salida: %q)", code, out.String())
	}
	if !f.latestCalled {
		t.Fatalf("sin pin, update debe consultar la última estable")
	}
	if got, _ := os.ReadFile(dest); string(got) != "binario-nuevo" {
		t.Fatalf("no se reemplazó: %q", got)
	}
}

func TestParseLatestStableTag(t *testing.T) {
	// La API devuelve de más reciente a más antigua; la primera estable gana.
	body := `[
	{ "tag_name": "v1.0.0-rc1", "prerelease": true },
	{ "tag_name": "v0.9.0", "prerelease": false },
	{ "tag_name": "v0.8.0", "prerelease": false }
]`
	if got := parseLatestStableTag(body); got != "v0.9.0" {
		t.Fatalf("esperaba v0.9.0, obtuve %q", got)
	}
	soloPre := `[ { "tag_name": "v1.0.0-rc1", "prerelease": true } ]`
	if got := parseLatestStableTag(soloPre); got != "" {
		t.Fatalf("solo prereleases: esperaba vacío, obtuve %q", got)
	}
}

// Asegura que el nombre del asset que construye runUpdate case con el de release.yml.
func TestExtractBinaryFromTarGz(t *testing.T) {
	tarball := makeTarGz(t, []byte("contenido"))
	got, err := extractBinaryFromTarGz(tarball)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if string(got) != "contenido" {
		t.Fatalf("contenido extraído incorrecto: %q", got)
	}
	if _, err := extractBinaryFromTarGz([]byte("no soy gzip")); err == nil {
		t.Fatalf("esperaba error con datos no-gzip")
	}
	// Un tar.gz sin la entrada 'enu' es malformado.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "otracosa", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	if _, err := extractBinaryFromTarGz(buf.Bytes()); err == nil {
		t.Fatalf("esperaba error si falta la entrada 'enu'")
	}
}

// TestAtomicReplace ejercita directamente el reemplazo atómico: el camino feliz no deja
// sidecar residual, y un fallo de `rename` (destino que es un directorio no vacío) deja
// el destino INTACTO y sin residuos. Fuerza el fallo de forma determinista (incluso como
// root) sin depender de bits de permiso.
func TestAtomicReplace(t *testing.T) {
	t.Run("exito_reemplaza_y_no_deja_residuo", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "enu")
		if err := os.WriteFile(dest, []byte("viejo"), 0o755); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := atomicReplace(dest, []byte("nuevo")); err != nil {
			t.Fatalf("atomicReplace: %v", err)
		}
		if got, _ := os.ReadFile(dest); string(got) != "nuevo" {
			t.Fatalf("contenido no reemplazado: %q", got)
		}
		assertNoSidecar(t, dir)
	})
	t.Run("fallo_de_rename_deja_destino_intacto_y_sin_residuo", func(t *testing.T) {
		dir := t.TempDir()
		// dest es un DIRECTORIO no vacío: os.Rename(fichero, dir) falla → el reemplazo
		// debe abortar limpiando el sidecar, sin dejar basura en el directorio padre.
		dest := filepath.Join(dir, "enu")
		if err := os.MkdirAll(filepath.Join(dest, "sub"), 0o755); err != nil {
			t.Fatalf("mkdir dest: %v", err)
		}
		if err := atomicReplace(dest, []byte("nuevo")); err == nil {
			t.Fatalf("esperaba error al renombrar sobre un directorio no vacío")
		}
		if !exists(filepath.Join(dest, "sub")) {
			t.Fatalf("el destino se dañó tras un fallo de rename")
		}
		assertNoSidecar(t, dir)
	})
}

// assertNoSidecar exige que no quede ningún temporal `.enu-new-*` en `dir` (el reemplazo
// consume el sidecar en el éxito y lo borra en el fallo). Caza una fuga de sidecar.
func assertNoSidecar(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".enu-new-") {
			t.Fatalf("sidecar residual sin limpiar: %s", e.Name())
		}
	}
}

func TestSameVersion(t *testing.T) {
	if !sameVersion("v0.2.0", "0.2.0") {
		t.Fatalf("v0.2.0 debe igualar 0.2.0")
	}
	if sameVersion("v0.2.0", "v0.3.0") {
		t.Fatalf("v0.2.0 no debe igualar v0.3.0")
	}
}
