// `enu update` (S51, ADR-026 pieza 4; espec en release.md §Instalador): actualiza el
// binario en uso a la última release estable (o a `ENU_VERSION`), heredando toda la
// disciplina del instalador. Checksum OBLIGATORIO en Go compartido (verifyChecksum):
// un artefacto corrupto o un `checksums.txt` ausente NO tocan el binario instalado
// —instalar un binario corrupto es el fallo silencioso por antonomasia—. Reemplazo
// ATÓMICO del binario en uso: escribir-al-lado (mismo directorio que el destino, así
// el `rename` nunca cruza sistema de ficheros) + `rename`. Reinstalar la misma versión
// es un no-op honesto. Destino no escribible → aborta con REMEDIO, jamás eleva
// privilegios (a diferencia del `sudo` que release.md §Instalador prohíbe). Superficie
// CLI (package main), no API sagrada.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/dbareagimeno/enu/internal/runtime"
)

const repoSlug = "dbareagimeno/enu"

// releaseFetcher abstrae el acceso a GitHub para que `runUpdate` sea TESTEABLE sin red:
// un test inyecta un fake que devuelve un tag y unos bytes canónicos; producción usa
// `httpReleaseFetcher`. Sin esta costura no se podría probar el caso 🔒 «checksum
// corrupto no toca el binario» sin depender de una release real.
type releaseFetcher interface {
	// LatestStable devuelve el tag de la última release NO prerelease (p. ej. "v0.2.0").
	LatestStable() (tag string, err error)
	// Download baja el cuerpo completo de una URL de asset.
	Download(url string) ([]byte, error)
}

// updateConfig son los parámetros del núcleo testeable: todo lo que en producción sale
// del entorno (versión actual del binario, `ENU_VERSION`, ruta del propio ejecutable,
// os/arch) se pasa como dato para poder fijarlo en un test.
type updateConfig struct {
	currentTag string         // versión de ESTE binario, p. ej. "v0.2.0"
	pinnedVer  string         // ENU_VERSION (vacío = usar la última estable)
	destPath   string         // ruta del binario a reemplazar (os.Executable en producción)
	os, arch   string         // plataforma del asset a bajar
	fetcher    releaseFetcher // acceso a GitHub (inyectable)
}

// runUpdateMain parsea `enu update` (sin flags propios en v1; `ENU_VERSION` por entorno),
// resuelve el entorno real y delega en `runUpdate`.
func runUpdateMain(args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "uso: enu update (argumento inesperado: %q; la versión se fija con ENU_VERSION=vX.Y.Z)\n", args[0])
		return exitUsage
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: no pude resolver la ruta del binario en uso:", err)
		return exitError
	}
	// Resuelve symlinks: reemplazamos el fichero REAL, no un enlace que apunte a él.
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	cfg := updateConfig{
		currentTag: currentVersionTag(),
		pinnedVer:  os.Getenv("ENU_VERSION"),
		destPath:   exe,
		os:         goruntime.GOOS,
		arch:       goruntime.GOARCH,
		fetcher:    &httpReleaseFetcher{},
	}
	return runUpdate(cfg, os.Stdout)
}

// runUpdate es el núcleo TESTEABLE de `enu update`. Orden de operaciones pensado para
// las dos garantías 🔒: (1) el checksum se verifica ANTES de escribir nada, así un
// artefacto corrupto jamás toca el binario; (2) la escribibilidad del destino se prueba
// ANTES de descargar, así un destino gestionado por un tercero aborta con remedio sin
// gastar red ni, por supuesto, elevar privilegios.
func runUpdate(cfg updateConfig, out io.Writer) int {
	// 1. Resolver la versión objetivo (pin explícito o la última estable).
	target := cfg.pinnedVer
	if target == "" {
		t, err := cfg.fetcher.LatestStable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: no pude resolver la última release estable:", err)
			return exitError
		}
		target = t
	}

	// 2. No-op honesto si ya estamos en la versión objetivo (reinstalar = no-op).
	if sameVersion(cfg.currentTag, target) {
		emitf(out, "ya estás en %s: nada que actualizar\n", cfg.currentTag)
		return exitOK
	}

	// 3. Probar que el destino es escribible SIN privilegios. Si no lo es (gestor de
	// paquetes ajeno, /usr/local/bin sin permiso), aborta con remedio —nunca sudo—.
	destDir := filepath.Dir(cfg.destPath)
	if err := probeWritable(destDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: no puedo escribir en %s sin privilegios: %v\n", destDir, err)
		fmt.Fprintf(os.Stderr, "tu enu parece gestionado por otra vía (paquete del sistema, directorio protegido). "+
			"Actualízalo por ahí, o reinstala en un destino tuyo: ENU_INSTALL_DIR=~/.local/bin y el instalador. "+
			"enu update no eleva privilegios.\n")
		return exitError
	}

	// 4. Descargar el tarball y el checksums.txt de la release objetivo.
	verNoV := strings.TrimPrefix(target, "v")
	name := fmt.Sprintf("enu-v%s-%s-%s", verNoV, cfg.os, cfg.arch)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", repoSlug, target)
	tarball, err := cfg.fetcher.Download(base + "/" + name + ".tar.gz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: no pude descargar el binario:", err)
		return exitError
	}
	sums, err := cfg.fetcher.Download(base + "/checksums.txt")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: no pude descargar checksums.txt:", err)
		return exitError
	}

	// 5. Verificar el checksum ANTES de tocar el binario. Si no cuadra o falta, se aborta
	// con el binario instalado INTACTO (aún no hemos escrito nada).
	if err := verifyChecksum(tarball, name+".tar.gz", string(sums)); err != nil {
		fmt.Fprintln(os.Stderr, "error: verificación de integridad fallida:", err)
		fmt.Fprintln(os.Stderr, "el binario instalado NO se ha tocado.")
		return exitError
	}

	// 6. Extraer el binario del tar.gz.
	bin, err := extractBinaryFromTarGz(tarball)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: no pude extraer el binario del artefacto:", err)
		return exitError
	}

	// 7. Reemplazo atómico: escribir-al-lado (mismo dir) + rename sobre el destino.
	if err := atomicReplace(cfg.destPath, bin); err != nil {
		fmt.Fprintln(os.Stderr, "error: no pude reemplazar el binario:", err)
		return exitError
	}

	emitf(out, "actualizado: %s → %s (%s)\n", cfg.currentTag, target, cfg.destPath)
	return exitOK
}

// verifyChecksum computa el sha256 de `data` y lo compara con el hash esperado para
// `name` dentro del contenido de un `checksums.txt` (formato `<sha256>  <nombre>`, con
// el nombre opcionalmente prefijado por `*` —modo binario de sha256sum—, igual que el
// awk de install.sh). Es la verificación COMPARTIDA (la usa `enu update`); su disciplina
// se blinda con un test table-driven. Un nombre ausente o un hash que no cuadra lanza un
// error estructurado; jamás devuelve nil ante la duda (fail-closed).
func verifyChecksum(data []byte, name, checksums string) error {
	expected := ""
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		entry := strings.TrimPrefix(fields[1], "*")
		if entry == name {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("no encontré el checksum de %q en checksums.txt", name)
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("el checksum de %q no coincide (esperado %s, obtenido %s)", name, expected, actual)
	}
	return nil
}

// extractBinaryFromTarGz descomprime un tar.gz y devuelve los bytes de la entrada llamada
// `enu` (la que produce release.yml y busca install.sh). Un tar sin esa entrada es un
// artefacto malformado.
func extractBinaryFromTarGz(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("gzip inválido: %w", err)
	}
	defer func() { _ = gz.Close() }() // solo lectura: el error de cierre no es accionable
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar inválido: %w", err)
		}
		if filepath.Base(hdr.Name) == "enu" && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("el artefacto no contiene el binario 'enu'")
}

// probeWritable comprueba que se puede crear un fichero en `dir` sin elevar privilegios,
// creando y borrando un temporal. Falla (sin efectos) si el directorio no existe, no es
// un directorio, o no es escribible por el usuario actual. Es el mismo directorio donde
// `atomicReplace` escribirá el sidecar, así que un probe verde garantiza que el rename
// posterior tampoco cruzará sistemas de ficheros.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".enu-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// atomicReplace reemplaza el fichero `dest` por `data` de forma atómica: escribe un
// sidecar en el MISMO directorio que `dest` (nunca en os.TempDir(), que puede ser otro
// sistema de ficheros y volver el `rename` un EXDEV), le pone permisos de ejecución y lo
// renombra sobre `dest`. En Linux el `rename` sobre un binario EN USO es la sustitución
// atómica canónica: el proceso vivo conserva su inodo, el nuevo arranque ve el nuevo. Si
// algo falla, el sidecar se borra y `dest` queda intacto.
func atomicReplace(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	f, err := os.CreateTemp(dir, ".enu-new-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return werr
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// currentVersionTag rinde la versión de ESTE binario como tag ("v0.2.0"), a partir de las
// constantes de compilación del kernel (las mismas que `enu doctor`/`enu.version`).
func currentVersionTag() string {
	return fmt.Sprintf("v%d.%d.%d", runtime.VersionMajor, runtime.VersionMinor, runtime.VersionPatch)
}

// sameVersion compara dos tags ignorando la 'v' inicial ("v0.2.0" == "0.2.0").
func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// httpReleaseFetcher es el releaseFetcher de PRODUCCIÓN: habla con la API de GitHub.
type httpReleaseFetcher struct{}

// LatestStable trae la lista de releases y devuelve el tag de la primera NO prerelease
// (la API las ordena de más reciente a más antigua). Parseo mínimo sin dependencias, en
// paralelo al de install.sh.
func (h *httpReleaseFetcher) LatestStable() (string, error) {
	body, err := h.Download("https://api.github.com/repos/" + repoSlug + "/releases?per_page=20")
	if err != nil {
		return "", err
	}
	tag := parseLatestStableTag(string(body))
	if tag == "" {
		return "", fmt.Errorf("no hay ninguna release estable de %s (¿solo prereleases? fija ENU_VERSION=vX.Y.Z)", repoSlug)
	}
	return tag, nil
}

// Download hace un GET y devuelve el cuerpo, siguiendo redirects (los assets de GitHub
// redirigen a un CDN). Un status != 2xx es error.
func (h *httpReleaseFetcher) Download(url string) ([]byte, error) {
	resp, err := http.Get(url) //nolint:gosec // URL construida por nosotros desde repoSlug/tag
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() // el error de cierre del cuerpo no es accionable
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s → HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// parseLatestStableTag recorre el JSON de releases recordando el último `tag_name` visto
// y, al primer `"prerelease": false`, devuelve ese tag. Robusto a campos en líneas
// separadas (como la API real). Extraído para poder probarlo sin red.
func parseLatestStableTag(jsonBody string) string {
	tag := ""
	for _, line := range strings.Split(jsonBody, "\n") {
		if strings.Contains(line, "\"tag_name\"") {
			if v := jsonStringValue(line, "tag_name"); v != "" {
				tag = v
			}
		}
		if strings.Contains(line, "\"prerelease\"") && strings.Contains(line, "false") && tag != "" {
			return tag
		}
	}
	return ""
}

// jsonStringValue extrae el valor entrecomillado de `"key": "valor"` de una línea, sin un
// parser JSON completo (suficiente para los campos escalares de la API de releases).
func jsonStringValue(line, key string) string {
	marker := "\"" + key + "\""
	i := strings.Index(line, marker)
	if i < 0 {
		return ""
	}
	rest := line[i+len(marker):]
	c := strings.Index(rest, ":")
	if c < 0 {
		return ""
	}
	rest = rest[c+1:]
	q1 := strings.Index(rest, "\"")
	if q1 < 0 {
		return ""
	}
	rest = rest[q1+1:]
	q2 := strings.Index(rest, "\"")
	if q2 < 0 {
		return ""
	}
	return rest[:q2]
}
