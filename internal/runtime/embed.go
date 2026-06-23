package runtime

// Infraestructura de extensiones embebidas (api.md §14, ADR-010, S12).
//
// Las extensiones oficiales de nu (agente, chat, providers, MCP, toolkit...) se
// distribuyen DENTRO del binario con `go:embed` —para no romper la promesa "un
// binario, offline" (ADR-001/ADR-010): activar una embebida NUNCA requiere red—,
// pero están **INACTIVAS por defecto** (ADR-010): nu instalado es un runtime
// desnudo, y el harness es una elección del usuario, no un hecho consumado. Una
// embebida solo se carga si `config.dir()/nu.toml` la nombra en `plugins.enabled`.
//
// FRONTERA temporal. Las extensiones oficiales reales son la Fase 8 (S36-S45) y
// aún no existen. Para poder montar y PROBAR el mecanismo de embebido + gating ya
// en S12, el árbol embebido contiene una sola extensión STUB de ejemplo
// (`embedded/example/`), suficiente para verificar que: (a) por defecto NO se
// carga; (b) activada por `nu.toml` sí, con `source="builtin"`; (c) un directorio
// de usuario del mismo nombre la sustituye. Cuando lleguen las oficiales reales,
// se añaden bajo `embedded/` sin tocar este mecanismo.
//
// Cómo se materializan. El loader (loader.go) descubre y carga plugins de
// DIRECTORIOS en disco (lee `plugin.toml` con `os.ReadFile`, corre `init.lua` con
// `L.LoadFile`, añade `lua/` a las rutas de `require`). Para que una embebida se
// cargue **exactamente igual que un plugin de usuario** (§14), se extrae su árbol
// del `embed.FS` a un directorio efímero bajo `data_dir` (`<data_dir>/embedded/`)
// la primera vez que se necesita. Así no hay un segundo camino de carga: solo se
// extrae lo embebido a disco y se reusa el loader de S11. La alternativa —enseñar
// al loader a leer de un `fs.FS`— duplicaría la lógica de descubrimiento por una
// ganancia nula (el árbol embebido es diminuto y la extracción, idempotente).

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// embeddedFS contiene el árbol de extensiones oficiales embebidas. La raíz lógica
// es el subdirectorio `embedded/`: cada subdirectorio suyo con `plugin.toml` es una
// extensión embebida (mismo formato que un plugin de disco, §14).
//
//go:embed embedded
var embeddedFS embed.FS

// embeddedRoot es la raíz dentro de `embeddedFS` donde viven las extensiones.
const embeddedRoot = "embedded"

// embeddedNames devuelve los nombres de directorio de las extensiones embebidas
// disponibles (cada subdirectorio de `embedded/` que tenga `plugin.toml`). Es el
// catálogo de lo que `nu.toml` puede activar sin tenerlo en disco. El nombre de
// DIRECTORIO no tiene por qué coincidir con el `name` del manifiesto, pero por
// convención lo hace; el loader usa el `name` real tras extraer y parsear.
func embeddedNames() ([]string, error) {
	entries, err := fs.ReadDir(embeddedFS, embeddedRoot)
	if err != nil {
		// `embed` garantiza que `embedded/` existe en tiempo de compilación; un fallo
		// aquí sería un bug de build, no un error de runtime accionable.
		return nil, &StructuredError{Code: CodeEIO,
			Message: fmt.Sprintf("no se pudo enumerar las extensiones embebidas: %v", err)}
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifest := embeddedRoot + "/" + e.Name() + "/" + pluginManifestName
		if _, err := fs.Stat(embeddedFS, manifest); err != nil {
			continue // subdirectorio sin manifiesto: no es una extensión
		}
		names = append(names, e.Name())
	}
	return names, nil
}

// nonProductEmbedded lista las extensiones embebidas que NO son parte del
// "conjunto oficial de producto" (ADR-015): andamiaje que se distribuye en el
// binario para probar el mecanismo, no para activarse en la config del usuario.
// Hoy solo `example` (el stub del gating, S12). Se mantiene como conjunto —no como
// una sola constante— para que añadir otro andamiaje futuro sea una línea aquí.
var nonProductEmbedded = map[string]bool{
	"example": true,
}

// officialProductSet devuelve el **conjunto oficial de producto** (ADR-015, G33):
// las extensiones embebidas DISPONIBLES (`embeddedNames`) menos el andamiaje de
// `nonProductEmbedded` (`example`). Es la ÚNICA fuente de verdad de "qué activa el
// conjunto oficial": la usan tanto la acción de la pantalla de runtime desnudo
// (`ActivateOfficial`, vía TTY, G21) como el flag `nu --default-config` (sin TTY),
// de modo que ambos enchufan exactamente lo mismo (la coherencia que G33 exige). El
// orden hereda el de `embeddedNames` (no garantizado por `fs.ReadDir`); el loader lo
// reordena topológicamente por `requires`, así que el orden aquí es irrelevante.
func officialProductSet() ([]string, error) {
	names, err := embeddedNames()
	if err != nil {
		return nil, err
	}
	product := names[:0:0] // copia nueva, no aliasa el slice de embeddedNames
	for _, n := range names {
		if nonProductEmbedded[n] {
			continue
		}
		product = append(product, n)
	}
	return product, nil
}

// extractEmbedded materializa el árbol de la extensión embebida `name` (su
// subdirectorio de `embedded/`) en `<destRoot>/<name>` y devuelve esa ruta, lista
// para que el loader la trate como un plugin de disco más. La extracción es
// idempotente (sobrescribe): así un binario nuevo —con una versión nueva de la
// extensión— gana sobre lo extraído por un arranque anterior, sin caché obsoleta.
// No usa red (ADR-010): todo sale del binario.
func extractEmbedded(name, destRoot string) (string, error) {
	src := embeddedRoot + "/" + name
	dest := filepath.Join(destRoot, name)

	err := fs.WalkDir(embeddedFS, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, p)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := embeddedFS.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return "", &StructuredError{Code: CodeEIO,
			Message: fmt.Sprintf("no se pudo materializar la extensión embebida %q: %v", name, err)}
	}
	return dest, nil
}
