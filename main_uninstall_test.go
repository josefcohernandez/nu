package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uninstallDirs prepara un binario, un config.dir y un data_dir FÍSICAMENTE DISTINTOS
// (dirs temporales separados), cada uno con contenido, para poder afirmar con precisión
// qué se borra y qué sobrevive. Dos centinelas hacen de red anti-mutante:
//   - `sesion.jsonl` dentro de data_dir: cazaría un `RemoveAll(dataDir)`.
//   - `vecino.txt` dentro del PADRE de config_dir (hermano de config_dir): cazaría un
//     `RemoveAll(filepath.Dir(configDir))` —el borrado-de-más—, que si no existiera
//     pasaría desapercibido (config_dir desaparece igual y data_dir vive en otra rama).
//
// Devuelve también la ruta del centinela vecino para afirmarlo.
func uninstallDirs(t *testing.T) (binPath, configDir, dataDir, vecino string) {
	t.Helper()
	base := t.TempDir()
	binPath = filepath.Join(base, "bin", "enu")
	configDir = filepath.Join(base, "state", "enu") // Dir = base/state
	dataDir = filepath.Join(base, "data", "enu")
	vecino = filepath.Join(filepath.Dir(configDir), "vecino.txt") // base/state/vecino.txt
	for _, d := range []string{filepath.Dir(binPath), configDir, dataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", d, err)
		}
	}
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "enu.toml"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(vecino, []byte("no me toques"), 0o644); err != nil {
		t.Fatalf("write vecino: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "sesion.jsonl"), []byte("centinela"), 0o644); err != nil {
		t.Fatalf("write data: %v", err)
	}
	return
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestUninstallSinPurgeBorraSoloElBinario(t *testing.T) {
	bin, cfg, data, vecino := uninstallDirs(t)
	var out bytes.Buffer
	code := runUninstall(bin, cfg, data, false, strings.NewReader(""), &out)
	if code != exitOK {
		t.Fatalf("esperaba exitOK, obtuve %d", code)
	}
	if exists(bin) {
		t.Fatalf("el binario no se borró")
	}
	if !exists(cfg) {
		t.Fatalf("sin --purge no debe borrarse la config")
	}
	if !exists(data) {
		t.Fatalf("los datos nunca se tocan")
	}
	_ = vecino
}

func TestUninstallPurgeConfirmadoBorraConfigPeroNuncaDatos(t *testing.T) { // 🔒 data_dir intocable
	bin, cfg, data, vecino := uninstallDirs(t)
	centinela := filepath.Join(data, "sesion.jsonl")
	var out bytes.Buffer
	code := runUninstall(bin, cfg, data, true, strings.NewReader("y\n"), &out)
	if code != exitOK {
		t.Fatalf("esperaba exitOK, obtuve %d", code)
	}
	if exists(bin) {
		t.Fatalf("el binario no se borró")
	}
	if exists(cfg) {
		t.Fatalf("--purge confirmado debe borrar la config")
	}
	if !exists(data) {
		t.Fatalf("CRÍTICO: --purge borró el data_dir")
	}
	if got, _ := os.ReadFile(centinela); string(got) != "centinela" {
		t.Fatalf("CRÍTICO: --purge tocó un fichero de datos: %q", got)
	}
	// El purge debe borrar EXACTAMENTE config.dir(), nunca su PADRE: el centinela
	// vecino (hermano de config.dir dentro de su padre) tiene que sobrevivir. Caza un
	// `RemoveAll(filepath.Dir(configDir))` que, sin este centinela, pasaría inadvertido.
	if !exists(vecino) {
		t.Fatalf("CRÍTICO: --purge borró el PADRE de config.dir() (el vecino %s desapareció)", vecino)
	}
	if !exists(filepath.Dir(cfg)) {
		t.Fatalf("CRÍTICO: --purge borró el directorio padre de config.dir()")
	}
}

func TestUninstallPurgeDeclinadoConservaConfig(t *testing.T) {
	bin, cfg, data, _ := uninstallDirs(t)
	var out bytes.Buffer
	code := runUninstall(bin, cfg, data, true, strings.NewReader("n\n"), &out)
	if code != exitOK {
		t.Fatalf("esperaba exitOK, obtuve %d", code)
	}
	if exists(bin) {
		t.Fatalf("el binario se borra siempre")
	}
	if !exists(cfg) {
		t.Fatalf("purge declinado ('n') debe conservar la config")
	}
	if !exists(data) {
		t.Fatalf("los datos nunca se tocan")
	}
	if !strings.Contains(out.String(), "cancelado") {
		t.Fatalf("esperaba mensaje de cancelación, salida: %q", out.String())
	}
}

func TestUninstallPurgeEOFConservaConfig(t *testing.T) {
	// Un stdin cerrado (EOF sin respuesta) NO es un "sí": se conserva la config.
	bin, cfg, data, _ := uninstallDirs(t)
	var out bytes.Buffer
	code := runUninstall(bin, cfg, data, true, strings.NewReader(""), &out)
	if code != exitOK {
		t.Fatalf("esperaba exitOK, obtuve %d", code)
	}
	if !exists(cfg) {
		t.Fatalf("EOF no es confirmación: la config debe conservarse")
	}
	if !exists(data) {
		t.Fatalf("los datos nunca se tocan")
	}
}

func TestUninstallPurgeRehusaSiDatosDentroDeConfig(t *testing.T) {
	// Configuración atípica: data_dir dentro de config_dir. El purge se rehúsa para no
	// arrastrar los datos con un RemoveAll(configDir).
	base := t.TempDir()
	bin := filepath.Join(base, "enu")
	cfg := filepath.Join(base, "config")
	data := filepath.Join(cfg, "datos") // DENTRO de config
	_ = os.WriteFile(bin, []byte("bin"), 0o755)
	_ = os.MkdirAll(data, 0o755)
	_ = os.WriteFile(filepath.Join(data, "s.jsonl"), []byte("c"), 0o644)
	var out bytes.Buffer
	code := runUninstall(bin, cfg, data, true, strings.NewReader("y\n"), &out)
	if code != exitOK {
		t.Fatalf("esperaba exitOK, obtuve %d", code)
	}
	if !exists(data) {
		t.Fatalf("CRÍTICO: se borraron datos anidados en config")
	}
	if !exists(cfg) {
		t.Fatalf("se rehúsa el purge: config conservada")
	}
}

func TestUninstallBinarioAusenteEsNoOp(t *testing.T) {
	// Si el binario ya no está (doble uninstall), no es un error.
	base := t.TempDir()
	bin := filepath.Join(base, "enu-inexistente")
	cfg := filepath.Join(base, "config")
	data := filepath.Join(base, "data")
	_ = os.MkdirAll(cfg, 0o755)
	_ = os.MkdirAll(data, 0o755)
	var out bytes.Buffer
	code := runUninstall(bin, cfg, data, false, strings.NewReader(""), &out)
	if code != exitOK {
		t.Fatalf("binario ausente debe ser no-op (exitOK), obtuve %d", code)
	}
}

func TestIsWithin(t *testing.T) {
	if !isWithin("/a/b/c", "/a/b") {
		t.Fatalf("/a/b/c está dentro de /a/b")
	}
	if isWithin("/a/bc", "/a/b") {
		t.Fatalf("/a/bc NO está dentro de /a/b (prefijo textual, no de árbol)")
	}
	if !isWithin("/a/b", "/a/b") {
		t.Fatalf("un dir está dentro de sí mismo")
	}
	if isWithin("/a", "/a/b") {
		t.Fatalf("/a NO está dentro de /a/b")
	}
}
