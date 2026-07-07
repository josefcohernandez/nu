package runtime

// Loader de plugins (api.md §14, S11). Un plugin es un directorio con
// `plugin.toml` (`name`, `version`, `requires?: string[]`) e `init.lua`, que se
// ejecuta al cargar. El directorio `lua/` del plugin se añade a las rutas de
// `require`, de modo que los plugins se requieren entre sí (composabilidad,
// ADR-008). El **nombre es la identidad** del plugin (§14): el loader la mantiene
// única —dos plugins con el mismo nombre son un error de carga accionable—, y esa
// unicidad es lo que deja que los namespaces de eventos (§4) sean libres de
// colisión por convención (namespace = nombre del plugin), sin que el core reserve
// nombre de extensión alguno (G26).
//
// Arranque canónico (§14): core → plugins activados (orden **topológico por
// `requires`**) → `init.lua` del usuario → evento `core:ready`. El init del
// usuario va **el último** a propósito: como en la pila de input el registro más
// reciente gana, el usuario tiene la última palabra (keymaps, theme, overrides)
// por construcción, sin sistema de prioridades.
//
// Activación (S12, ADR-010). Los plugins de DISCO (directorios pasados por
// `WithPluginDir` o `plugins.dirs` de `nu.toml`) se cargan tal cual —son del
// usuario, explícitos—. Las **extensiones oficiales embebidas** (`go:embed`,
// embed.go) están **INACTIVAS por defecto**: solo se materializan y cargan si
// `config.dir()/nu.toml` `plugins.enabled` las nombra (config_toml.go). El
// directorio de usuario **sustituye** a la embebida del mismo nombre (§14); un
// `enabled` que no resuelve a nada es un error accionable que nombra la línea de
// `nu.toml`. Una embebida activada queda con `source = "builtin"`.
//
// RELACIÓN con S13/S33. `nu.plugin.reload` (S13, ya implementado en plugin.go) se
// apoya en este loader: el etiquetado de handles por dueño usa el `ownerStack` que
// este loader empuja. La **pantalla de runtime desnudo** (G21: render TTY del
// catálogo de embebidas + activar/salir, S30/S33) es UI aparte; este loader (S12) no
// pinta nada (puede no haber UI), solo gobierna el arranque por config.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// ownerUser es el owner que se anota cuando no hay plugin en el contexto: el chunk
// de `-e`, el `init.lua` del usuario y los handlers sin plugin dueño (§14, §15).
const ownerUser = "user"

// pluginSource distingue de dónde vino un plugin (§14). En S11 todos son "user"
// (cargados de un directorio del disco); las embebidas ("builtin") llegan en S12.
type pluginSource string

const (
	sourceUser    pluginSource = "user"
	sourceBuiltin pluginSource = "builtin"
)

// pluginManifest es el `plugin.toml` decodificado (§14). Solo estos tres campos
// son del contrato del kernel; un plugin puede añadir los suyos al fichero y el
// loader los ignora (forward-compatibilidad: `toml.Decode` no falla por claves de
// más). El parseo es interno del loader —reusa la misma librería TOML pura-Go que
// S18 expondrá como `nu.toml`, pero NO es esa API.
type pluginManifest struct {
	Name     string   `toml:"name"`
	Version  string   `toml:"version"`
	Requires []string `toml:"requires"`
}

// pluginInfo es un plugin descubierto y listo para cargar: su manifiesto + su
// directorio raíz en disco + su procedencia. El tope del `ownerStack` (runtime.go)
// es un `*pluginInfo`, y `nu.plugin.current/list` se construyen a partir de él.
type pluginInfo struct {
	Name     string
	Version  string
	Requires []string
	Dir      string       // directorio raíz del plugin (el que contiene plugin.toml)
	Source   pluginSource // "user" en S11; "builtin" para embebidas (S12)
	Enabled  bool         // true por defecto en S11; S12 lo gobierna desde nu.toml
}

// loader administra el descubrimiento, la ordenación y la carga de plugins, y
// respalda `nu.config.dir/data_dir`. Vive en el Runtime (rt.ldr) y se toca solo
// desde el estado principal con el token tomado (el arranque es síncrono).
type loader struct {
	rt         *Runtime
	dataDir    string
	configDir  string
	pluginDirs []string

	// enabled es `plugins.enabled` de `nu.toml` (S12): los nombres a activar. Da
	// vida a las extensiones embebidas, INACTIVAS por defecto (ADR-010): una
	// embebida solo se carga si su nombre está aquí. Vacío/nil = nada que activar de
	// las embebidas (runtime desnudo).
	enabled []string
	// configErr es el error de parseo de `nu.toml` aplazado desde `New` (cuya firma
	// no devuelve error, §17). `Boot` lo devuelve antes de cargar nada: una config
	// rota no debe dejar el arranque a medias.
	configErr error

	// ordered es el resultado de `Boot`: los plugins efectivamente cargados, en el
	// orden topológico en que corrieron. Lo lee `nu.plugin.list`.
	ordered []*pluginInfo
	booted  bool
}

// newLoader prepara el loader sin tocar el disco todavía (el descubrimiento ocurre
// en `Boot`).
func newLoader(rt *Runtime, dataDir, configDir string, pluginDirs []string) *loader {
	return &loader{
		rt:         rt,
		dataDir:    dataDir,
		configDir:  configDir,
		pluginDirs: pluginDirs,
	}
}

// discover recorre los directorios de plugins configurados y devuelve un
// `*pluginInfo` por cada subdirectorio que tenga `plugin.toml`, más las extensiones
// **embebidas activadas** por `nu.toml` (S12). Valida el manifiesto (nombre no
// vacío), la **unicidad de nombre** (§14) —dos plugins de DISCO con el mismo
// nombre son un error accionable que nombra ambas rutas— y, para las embebidas
// (ADR-010):
//
//   - una embebida solo se materializa/carga si `plugins.enabled` la nombra
//     (INACTIVA por defecto);
//   - un directorio de usuario del mismo nombre **sustituye** a la embebida (no
//     coexisten, §14): gana el de disco, la embebida se descarta sin error;
//   - un nombre de `plugins.enabled` que no corresponde a ninguna extensión —ni de
//     disco ni embebida— es un error de arranque accionable que **nombra la línea
//     de `nu.toml`** que lo arregla (§14).
func (l *loader) discover() ([]*pluginInfo, error) {
	byName := make(map[string]*pluginInfo)
	var found []*pluginInfo

	for _, root := range l.pluginDirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Un directorio de plugins inexistente no es fatal: simplemente no
				// aporta plugins (p. ej. el usuario aún no creó `~/.config/nu/plugins`).
				continue
			}
			return nil, &StructuredError{Code: CodeEIO,
				Message: fmt.Sprintf("no se pudo leer el directorio de plugins %q: %v", root, err)}
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name())
			manifestPath := filepath.Join(dir, pluginManifestName)
			if _, err := os.Stat(manifestPath); err != nil {
				// Subdirectorio sin `plugin.toml`: no es un plugin, se ignora en
				// silencio (puede ser cualquier carpeta del usuario).
				continue
			}
			p, err := l.loadManifest(dir, manifestPath)
			if err != nil {
				return nil, err
			}
			if prev, dup := byName[p.Name]; dup {
				return nil, &StructuredError{Code: CodeEINVAL,
					Message: fmt.Sprintf("colisión de nombre de plugin %q: definido en %q y en %q (el nombre es la identidad del plugin, §14)",
						p.Name, prev.Dir, p.Dir)}
			}
			byName[p.Name] = p
			found = append(found, p)
		}
	}

	// Extensiones embebidas (ADR-010): inactivas salvo que `nu.toml`
	// `plugins.enabled` las nombre. Las añade aquí, tras los plugins de disco, para
	// que la sustitución por nombre sea trivial (un nombre ya presente en `byName`
	// es un plugin de usuario que gana). El catálogo de embebidas se enumera del
	// `embed.FS` (embed.go).
	embedded, err := embeddedNames()
	if err != nil {
		return nil, err
	}
	embeddedSet := make(map[string]bool, len(embedded))
	for _, name := range embedded {
		embeddedSet[name] = true
	}

	for _, name := range l.enabled {
		if _, onDisk := byName[name]; onDisk {
			// El directorio de usuario SUSTITUYE a la embebida del mismo nombre (§14):
			// ya está en `found` como "user"; no se materializa la embebida.
			continue
		}
		if !embeddedSet[name] {
			// `plugins.enabled` nombra algo que no existe ni en disco ni embebido:
			// error de arranque accionable que apunta a la línea de `nu.toml` (§14).
			return nil, &StructuredError{Code: CodeEINVAL,
				Message: fmt.Sprintf("la extensión %q activada en %s no existe (ni embebida ni en un directorio de plugins); revisa la línea `plugins.enabled` de %s",
					name, nuTomlName, nuTomlName)}
		}
		// Embebida activada: materialízala a disco y cárgala como un plugin más, con
		// `source="builtin"`. La extracción no usa red (ADR-010): sale del binario.
		dir, err := extractEmbedded(name, filepath.Join(l.dataDir, "embedded"))
		if err != nil {
			return nil, err
		}
		p, err := l.loadManifest(dir, filepath.Join(dir, pluginManifestName))
		if err != nil {
			return nil, err
		}
		p.Source = sourceBuiltin // se materializó de una embebida, no de un dir de usuario
		if prev, dup := byName[p.Name]; dup {
			// El `name` del manifiesto de la embebida choca con un plugin ya cargado
			// cuyo nombre de directorio era distinto: sigue siendo colisión de
			// identidad (§14).
			return nil, &StructuredError{Code: CodeEINVAL,
				Message: fmt.Sprintf("colisión de nombre de plugin %q: extensión embebida y %q (el nombre es la identidad del plugin, §14)",
					p.Name, prev.Dir)}
		}
		byName[p.Name] = p
		found = append(found, p)
	}

	return found, nil
}

// loadManifest parsea el `plugin.toml` de un plugin con la librería TOML pura-Go
// (la misma que S18 expone como `nu.toml`) y valida sus campos mínimos. Un
// manifiesto sin `name`, ilegible o mal formado es un error de carga accionable que
// nombra la ruta.
func (l *loader) loadManifest(dir, manifestPath string) (*pluginInfo, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, &StructuredError{Code: CodeEIO,
			Message: fmt.Sprintf("no se pudo leer %q: %v", manifestPath, err)}
	}
	var m pluginManifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, &StructuredError{Code: CodeEINVAL,
			Message: fmt.Sprintf("plugin.toml inválido en %q: %v", manifestPath, err)}
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, &StructuredError{Code: CodeEINVAL,
			Message: fmt.Sprintf("plugin.toml en %q sin campo `name` (el nombre es obligatorio: es la identidad del plugin, §14)", manifestPath)}
	}
	return &pluginInfo{
		Name:     m.Name,
		Version:  m.Version,
		Requires: m.Requires,
		Dir:      dir,
		Source:   sourceUser, // S11: todo lo cargado de disco es "user"; "builtin" es S12
		Enabled:  true,       // S11: enabled por defecto; S12 lo gobierna desde nu.toml
	}, nil
}

const (
	pluginManifestName = "plugin.toml"
	pluginInitName     = "init.lua"
)

// topoSort ordena los plugins de modo que cada uno aparezca DESPUÉS de aquellos a
// los que `requires` (§14): el dependido se carga antes que el dependiente. Es un
// orden topológico estable —entre plugins sin relación de dependencia se respeta el
// orden de descubrimiento, desempatado por nombre para que el arranque sea
// determinista—. Detecta dos errores de carga accionables:
//
//   - **dependencia ausente**: un `requires` que no corresponde a ningún plugin
//     descubierto (nombra el plugin y la dependencia que falta);
//   - **ciclo en `requires`**: nombra los plugins implicados.
func topoSort(plugins []*pluginInfo) ([]*pluginInfo, error) {
	byName := make(map[string]*pluginInfo, len(plugins))
	for _, p := range plugins {
		byName[p.Name] = p
	}

	// Valida dependencias presentes antes de ordenar: un `requires` colgando es un
	// error accionable, no un nodo fantasma.
	for _, p := range plugins {
		for _, dep := range p.Requires {
			if _, ok := byName[dep]; !ok {
				return nil, &StructuredError{Code: CodeEINVAL,
					Message: fmt.Sprintf("el plugin %q requiere %q, que no está disponible (¿directorio de plugins incorrecto o nombre mal escrito?)",
						p.Name, dep)}
			}
		}
	}

	// Orden de visita determinista: por orden de descubrimiento, desempate por
	// nombre. Un DFS post-orden produce el orden topológico (dependidos primero).
	roots := make([]*pluginInfo, len(plugins))
	copy(roots, plugins)
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Name < roots[j].Name })

	const (
		white = 0 // sin visitar
		gray  = 1 // en la pila de recursión (un re-encuentro = ciclo)
		black = 2 // terminado
	)
	color := make(map[string]int, len(plugins))
	var ordered []*pluginInfo
	var stack []string // para reconstruir el ciclo en el mensaje

	var visit func(p *pluginInfo) error
	visit = func(p *pluginInfo) error {
		switch color[p.Name] {
		case black:
			return nil
		case gray:
			// Re-encontramos un nodo en la pila de recursión: ciclo. Reconstruye el
			// tramo del ciclo desde `stack` para un mensaje accionable.
			return &StructuredError{Code: CodeEINVAL,
				Message: fmt.Sprintf("ciclo de dependencias entre plugins: %s (los `requires` no pueden formar un ciclo, §14)",
					cycleDescription(stack, p.Name))}
		}
		color[p.Name] = gray
		stack = append(stack, p.Name)

		// Visita las dependencias en orden determinista (por nombre) para que el
		// resultado no dependa del orden de `requires` en el TOML.
		deps := make([]string, len(p.Requires))
		copy(deps, p.Requires)
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(byName[dep]); err != nil {
				return err
			}
		}

		stack = stack[:len(stack)-1]
		color[p.Name] = black
		ordered = append(ordered, p) // post-orden: las dependencias ya están dentro
		return nil
	}

	for _, p := range roots {
		if err := visit(p); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// find devuelve el `*pluginInfo` cargado con ese nombre, o nil si no hay ninguno.
// Busca en `ordered` (lo que `Boot` cargó); el nombre es la identidad (§14), así
// que como mucho hay uno.
func (l *loader) find(name string) *pluginInfo {
	for _, p := range l.ordered {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// cycleDescription construye una descripción legible del ciclo: el tramo de la pila
// de recursión desde la primera aparición del nodo que se re-encontró, cerrado
// sobre sí mismo (`a -> b -> c -> a`).
func cycleDescription(stack []string, repeated string) string {
	start := 0
	for i, n := range stack {
		if n == repeated {
			start = i
			break
		}
	}
	cycle := append([]string{}, stack[start:]...)
	cycle = append(cycle, repeated)
	return strings.Join(cycle, " -> ")
}
