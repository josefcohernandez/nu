package runtime

// Parseo de `nu.toml` (api.md §14, S12). `config.dir()/nu.toml` es la
// configuración del PROPIO core (no la API Lua `nu.toml` —esa es un codec, S18—,
// aunque ambas reusan la misma librería TOML pura-Go añadida en S11). Gobierna
// (§14, ADR-010):
//
//   - `plugins.enabled` — la lista de nombres de plugins a activar. Es lo que da
//     vida a las extensiones embebidas, INACTIVAS por defecto (ADR-010): si una
//     embebida no aparece aquí, no se carga. Un nombre de `enabled` que no
//     corresponde a ninguna extensión (ni embebida ni en disco) es un error de
//     arranque ACCIONABLE que nombra la línea de `nu.toml` que lo arregla.
//   - `plugins.dirs` — directorios extra donde buscar plugins de disco, sumados a
//     los pasados por `WithPluginDir`. Rutas UTF-8 (§1).
//   - `watchdog.slice_budget_ms` — el presupuesto por slice del watchdog (S09), en
//     milisegundos (§1: tiempos en ms). Cablea el gancho `WithSliceBudget`.
//   - `[net].ca_file` / `[net].proxy` — defaults globales de red para `nu.http`
//     (§8, G12, S19): una CA corporativa que añadir a la raíz de confianza de TLS
//     y un proxy por defecto. Cualquiera de los dos es **sobreescribible por
//     petición** (`opts.tls.ca_file`, `opts.proxy`); sin `[net]` rige el
//     comportamiento estándar (CAs del sistema, proxy del entorno
//     `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`).
//
// Forward-compatibilidad: claves desconocidas se ignoran (igual que en
// `plugin.toml`, S11) para que una config de una versión más nueva no rompa el
// arranque de una más vieja. Un `nu.toml` AUSENTE es lo normal en un runtime
// desnudo (ADR-010): no es un error, simplemente no activa nada.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// nuTomlName es el nombre del fichero de configuración del runtime dentro de
// `config.dir()` (§14).
const nuTomlName = "nu.toml"

// runtimeConfig es el `nu.toml` decodificado: solo los campos que el core entiende
// en v1. La estructura refleja las secciones del TOML (`[plugins]`, `[watchdog]`).
type runtimeConfig struct {
	Plugins struct {
		Enabled []string `toml:"enabled"`
		Dirs    []string `toml:"dirs"`
	} `toml:"plugins"`
	Watchdog struct {
		// SliceBudgetMs es el presupuesto por slice en milisegundos (§1.3). Puntero
		// para distinguir "no especificado" (nil → rige el default/`WithSliceBudget`)
		// de "especificado como 0" (0 → desactiva el watchdog explícitamente, §9).
		SliceBudgetMs *int `toml:"slice_budget_ms"`
	} `toml:"watchdog"`
	// Net son los defaults globales de red para `nu.http` (§8, G12, S19): una CA
	// corporativa y un proxy por defecto, ambos sobreescribibles por petición. Un
	// `[net]` ausente deja el comportamiento estándar (CAs del sistema, proxy del
	// entorno).
	Net struct {
		CAFile string `toml:"ca_file"` // CA corporativa a añadir a la raíz de confianza TLS
		Proxy  string `toml:"proxy"`   // URL de proxy por defecto (vacío = proxy del entorno)
	} `toml:"net"`
}

// loadNuToml lee y parsea `config.dir()/nu.toml`. Devuelve un `runtimeConfig` cero
// (todo vacío) y `found=false` si el fichero no existe —el caso normal de un
// runtime desnudo, ADR-010—; un fichero ilegible o mal formado es un error de
// arranque accionable que nombra la ruta y la causa. La línea del TOML que el
// parser señala se propaga en el mensaje cuando la hay (BurntSushi la incluye).
func loadNuToml(configDir string) (cfg runtimeConfig, found bool, err error) {
	path := filepath.Join(configDir, nuTomlName)
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return runtimeConfig{}, false, nil
		}
		return runtimeConfig{}, false, &StructuredError{Code: CodeEIO,
			Message: fmt.Sprintf("no se pudo leer %q: %v", path, readErr)}
	}
	if decErr := toml.Unmarshal(data, &cfg); decErr != nil {
		return runtimeConfig{}, true, &StructuredError{Code: CodeEINVAL,
			Message: fmt.Sprintf("%s inválido en %q: %v", nuTomlName, path, decErr)}
	}
	return cfg, true, nil
}
