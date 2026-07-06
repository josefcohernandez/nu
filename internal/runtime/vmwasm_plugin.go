package runtime

// Catálogo de nu.plugin y nu.config sobre el backend wasm (M13d-ext, §14).
// Contraparte de plugin.go: `nu.plugin.current`/`nu.plugin.list` (consulta del
// loader) y `nu.config.dir`/`nu.config.data_dir`. Todas SÍNCRONAS (consultas del
// estado del loader, sin IO ni suspensión). Reusan el estado Go-side del loader
// (rt.ldr) y la pila de dueños (rt.ownerStack), que el arranque de extensiones
// sobre wasm (vmwasm_loader.go) empuja y saca alrededor del `init.lua` de cada
// extensión — de modo que `nu.plugin.current()` y el owner de `nu.log` son
// correctos DURANTE ese init, igual que en gopher.
//
// DECISIÓN DEL OWNER (M13d-ext): el `ownerStack` sigue viviendo Go-side
// (rt.ownerStack), no se duplica en Lua. Las primitivas del catálogo wasm que
// dependen del dueño —`nu.plugin.current` (aquí) y `nu.log` (vmwasm_log.go)— son
// HostFn SÍNCRONOS que corren en línea, en el mismo goroutine que conduce el
// `Eval`/paso del scheduler; leen `rt.currentOwner()` sin candado ni carrera (el
// estado principal es single-thread, ADR-004). El arranque empuja el `*pluginInfo`
// del plugin antes de `Eval(init.lua)` y lo saca después: durante el init de una
// extensión, `rt.currentOwner()` es su nombre; fuera de todo init (una task que
// corre luego, el chunk de `-e`) la pila está vacía y el owner es "user" —idéntico
// al backend gopher—.
//
// `nu.plugin.reload` (⏸, G2) NO se expone todavía sobre wasm: recargar exige
// re-correr el `init.lua` de una extensión sobre la Instance y re-registrar sus
// módulos `lua/`; queda pendiente para una iteración posterior (ninguna extensión
// del conjunto oficial lo invoca al cargar, así que no bloquea M13d-ext).

import (
	"github.com/dbareagimeno/nu/internal/vmwasm"
)

// registerPluginWasm cuelga `nu.plugin` (current/list) y `nu.config` (dir/data_dir)
// del catálogo wasm. Lo llama `registerWasmCatalog` (runtime.go).
func registerPluginWasm(p *vmwasm.Pool, rt *Runtime) {
	// nu.plugin.current() -> {name, version, dir} (§14): el plugin en cuyo contexto
	// corre el código ahora mismo (tope de ownerStack) o, fuera de todo plugin, el
	// contexto de usuario {name="user", version="", dir=config.dir}. Nunca nil:
	// siempre hay un contexto (espejo de pluginCurrent en gopher).
	p.Register("plugin.current", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		if n := len(rt.ownerStack); n > 0 {
			pi := rt.ownerStack[n-1]
			return []any{map[string]any{"name": pi.Name, "version": pi.Version, "dir": pi.Dir}}, nil
		}
		return []any{map[string]any{"name": ownerUser, "version": "", "dir": rt.ldr.configDir}}, nil
	})

	// nu.plugin.list() -> {name, version, source, enabled}[] (§14): los plugins
	// cargados, en el orden topológico en que corrieron (rt.ldr.ordered, que fija
	// BootWasm). Espejo de pluginList en gopher.
	p.Register("plugin.list", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		arr := make([]any, 0, len(rt.ldr.ordered))
		for _, pi := range rt.ldr.ordered {
			arr = append(arr, map[string]any{
				"name":    pi.Name,
				"version": pi.Version,
				"source":  string(pi.Source),
				"enabled": pi.Enabled,
			})
		}
		return []any{arr}, nil
	})

	// nu.config.dir() -> string [W] (§14): el directorio de configuración.
	p.Register("config.dir", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		return []any{rt.ldr.configDir}, nil
	})

	// nu.config.data_dir() -> string [W] (§14): el directorio de datos.
	p.Register("config.data_dir", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		return []any{rt.ldr.dataDir}, nil
	})
}
