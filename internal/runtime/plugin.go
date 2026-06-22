package runtime

// Superficie Lua del loader (api.md §14): `nu.plugin.current/list` y
// `nu.config.dir/data_dir`. Es glue de paso sobre el estado del loader (loader.go):
// la lógica clave (orden topológico, unicidad de nombre, arranque canónico) vive
// allí y se blinda con tests Go; aquí solo se expone a Lua.
//
// `nu.plugin` es **solo estado principal** (§16): el ciclo de vida de plugins no
// existe en un worker. `nu.config.dir`/`data_dir` SÍ son **[W]**: un worker puede
// necesitar saber dónde vive la config/los datos (p. ej. para componer rutas), y
// son funciones puras que devuelven un string fijo.

import (
	lua "github.com/yuin/gopher-lua"
)

// registerPlugin cuelga `nu.plugin` (current/list) y `nu.config` (dir/data_dir) del
// global `nu`. Lo llama `registerNu` (nu.go).
func (rt *Runtime) registerPlugin(nu *lua.LTable) {
	L := rt.L

	plugin := L.NewTable()
	plugin.RawSetString("current", L.NewFunction(rt.pluginCurrent))
	plugin.RawSetString("list", L.NewFunction(rt.pluginList))
	plugin.RawSetString("reload", L.NewFunction(rt.pluginReload))
	nu.RawSetString("plugin", plugin)

	config := L.NewTable()
	config.RawSetString("dir", L.NewFunction(rt.configDir))
	config.RawSetString("data_dir", L.NewFunction(rt.configDataDir))
	nu.RawSetString("config", config)
}

// pluginCurrent implementa `nu.plugin.current() -> {name, version, dir}` (§14): el
// plugin en cuyo contexto corre el código ahora mismo. Durante el `init.lua` de un
// plugin es ese plugin (el loader lo empujó al `ownerStack`); fuera de todo plugin
// —el chunk de `-e`, el `init.lua` del usuario, un handler sin plugin dueño—
// devuelve el contexto del usuario `{name="user", version="", dir=config.dir}`. Así
// `current()` nunca es `nil`: siempre hay un contexto, aunque sea el del usuario.
func (rt *Runtime) pluginCurrent(L *lua.LState) int {
	t := L.NewTable()
	if n := len(rt.ownerStack); n > 0 {
		p := rt.ownerStack[n-1]
		t.RawSetString("name", lua.LString(p.Name))
		t.RawSetString("version", lua.LString(p.Version))
		t.RawSetString("dir", lua.LString(p.Dir))
	} else {
		// Contexto del usuario/core: no es un plugin del disco, pero su "dir" natural
		// es el directorio de config (de donde sale su `init.lua`).
		t.RawSetString("name", lua.LString(ownerUser))
		t.RawSetString("version", lua.LString(""))
		t.RawSetString("dir", lua.LString(rt.ldr.configDir))
	}
	L.Push(t)
	return 1
}

// pluginList implementa `nu.plugin.list() -> {name, version, source, enabled}[]`
// (§14): los plugins cargados, en el orden topológico en que corrieron. En S11
// todos son `source="user"`, `enabled=true`; S12 añade las embebidas ("builtin") y
// la activación por `nu.toml`.
func (rt *Runtime) pluginList(L *lua.LState) int {
	arr := L.NewTable()
	for i, p := range rt.ldr.ordered {
		entry := L.NewTable()
		entry.RawSetString("name", lua.LString(p.Name))
		entry.RawSetString("version", lua.LString(p.Version))
		entry.RawSetString("source", lua.LString(string(p.Source)))
		entry.RawSetString("enabled", lua.LBool(p.Enabled))
		arr.RawSetInt(i+1, entry)
	}
	L.Push(arr)
	return 1
}

// pluginReload implementa `nu.plugin.reload(name)` ⏸ (§14): herramienta de
// desarrollo **best-effort** (G2). Recarga un plugin ya cargado para iterar sin
// reiniciar el proceso. Los pasos, en orden (api.md §14):
//
//  1. Emite `core:plugin.unload {name}` —las extensiones enganchadas a ese evento
//     limpian SUS registros (tools, comandos slash, lo que el core no conoce)—.
//  2. **Suelta TODOS los handles del plugin** que el core etiquetó por dueño
//     (S13, handles.go): cancela sus suscripciones de eventos (`on`/`once`) y para
//     sus timers (`every`). Es la pieza 🔒 —"reload no deja handlers huérfanos"—:
//     tras esto, las suscripciones VIEJAS ya no disparan y los timers VIEJOS no
//     tickean; solo correrá lo que el `init.lua` re-ejecutado vuelva a registrar.
//  3. **Vacía la caché de `require`** del plugin (sus módulos `lua/` en
//     `package.loaded`): así un módulo que cambió en disco se re-ejecuta, no queda
//     servido de la versión cacheada.
//  4. **Recarga su `init.lua`** con su contexto empujado al `ownerStack` (como en
//     el arranque): lo que registre allí queda de nuevo etiquetado por dueño.
//
// **Por qué ⏸** (§14): aunque hoy todos los pasos son trabajo del estado principal
// bajo el token (emit síncrono, liberar handles, re-correr el init), `reload` se
// declara suspendiente en api.md —reservando que pueda hacer IO (leer el init de
// disco puede volverse ⏸ en el futuro) y para que solo se invoque desde una task,
// como el resto de herramientas async—. Aquí la detección es la misma de §1.3:
// fuera de una task → `EINVAL`.
//
// **Best-effort (G2):** solo deshace lo que el core sabe deshacer —los handles que
// entregó y la caché de `require`—. Un plugin con efectos globales exóticos (una
// variable global que ensucia, un metatable monkeypatcheado) puede no descargarse
// limpio; por eso es para iterar, no para producción. Un plugin desconocido (no
// cargado) es `EINVAL` accionable.
func (rt *Runtime) pluginReload(L *lua.LState) int {
	if L == rt.L {
		raiseError(L, CodeEINVAL, "nu.plugin.reload solo puede llamarse dentro de una task", lua.LNil)
		return 0
	}
	name := L.CheckString(1)
	if err := rt.ldr.reload(L, name); err != nil {
		if se, ok := err.(*StructuredError); ok {
			raiseError(L, se.Code, se.Message, lua.LNil)
			return 0
		}
		raiseError(L, CodeEINVAL, err.Error(), lua.LNil)
		return 0
	}
	return 0
}

// configDir implementa `nu.config.dir() -> string` [W] (§14): `~/.config/nu` (o el
// equivalente por plataforma / lo fijado por `WithConfigDir`).
func (rt *Runtime) configDir(L *lua.LState) int {
	L.Push(lua.LString(rt.ldr.configDir))
	return 1
}

// configDataDir implementa `nu.config.data_dir() -> string` [W] (§14):
// `~/.local/share/nu`. Promociona la `defaultDataDir` de S03 (log.go), ahora
// también superficie pública.
func (rt *Runtime) configDataDir(L *lua.LState) int {
	L.Push(lua.LString(rt.ldr.dataDir))
	return 1
}
