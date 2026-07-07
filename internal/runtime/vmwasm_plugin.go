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
// `nu.plugin.reload` (⏸, G2) SÍ se expone sobre wasm (M13d-triage): se registra como
// primitiva SUSPENDENTE, de modo que su HostFn corre en la goroutine de fondo del
// scheduler (performHostcall) —fuera del `inst.mu` que toma un paso del bucle—, donde
// puede RE-ENTRAR la VM con `Eval` sin bloqueo para re-correr el `init.lua`. El
// orquestador Go (`loader.reloadWasm`, vmwasm_loader.go) es el gemelo de
// `loader.reload` (loader.go): emite `core:plugin.unload`, suelta los handles del
// dueño (registro Lua del preludio, `__release_owner`), relee y olvida los módulos
// `lua/` del plugin (Pool.SetModule + `__loader_forget`) y re-ejecuta su `init.lua`
// con el contexto empujado al `ownerStack`. El gate ⏸ (fuera de una task → EINVAL con
// "task" en el mensaje) lo da gratis el thunk suspendente del preludio (`__current`).
//
// El etiquetado de handles por dueño (G2) reposa en `nu.plugin._owner`, un HostFn
// SÍNCRONO que devuelve `rt.currentOwner()` (el tope del ownerStack). El preludio lo
// consulta al crear cada sub/timer para etiquetarlo con el dueño vigente —la MISMA
// fuente de verdad que gopher, un solo ownerStack Go-side—.

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

	// nu.plugin._owner() -> string: el dueño vigente (rt.currentOwner()). Interno del
	// preludio (etiquetado de handles por dueño, G2): SÍNCRONO, sin IO, corre en línea
	// leyendo el tope del ownerStack sin candado (estado principal single-thread,
	// ADR-004). Durante el init.lua de un plugin es su nombre; fuera, "user".
	p.Register("plugin._owner", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		return []any{rt.currentOwner()}, nil
	})

	// nu.plugin.reload(name) ⏸ (§14, G2): recarga un plugin ya cargado. SUSPENDENTE
	// (ver la nota de cabecera): su HostFn corre en la goroutine de fondo del
	// scheduler y re-entra la VM para re-correr el init. El trabajo lo hace el gemelo
	// Go de loader.reload; aquí solo se desempaqueta el nombre y se propaga el error
	// estructurado (EINVAL para un plugin no cargado, con su nombre en el mensaje).
	p.RegisterSuspending("plugin.reload", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		name, _ := args[0].(string)
		if err := rt.ldr.reloadWasm(name); err != nil {
			// Cruza el error del core a la forma que entiende la frontera wasm (§1.4):
			// un *runtime.StructuredError debe re-envolverse en *vmwasm.StructuredError
			// para que el scheduler (errToMap) preserve el código (EINVAL) y no lo
			// degrade a EIO. El reload solo produce EINVAL (plugin no cargado); sin detail.
			if se, ok := err.(*StructuredError); ok {
				return nil, &vmwasm.StructuredError{Code: se.Code, Message: se.Message}
			}
			return nil, err
		}
		return nil, nil
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
