package runtime

// Soporte de solo lectura para `enu doctor` (S50, ADR-026 pieza 3). Estos métodos
// EXPONEN lo que el loader y las rutas ya calculan —descubrimiento y orden
// topológico del grafo de plugins, y los directorios de config/datos— para que el
// subcomando `doctor` (en `package main`) los USE sin re-implementar su semántica ni
// arrancar el runtime (`Boot` corre los `init.lua`, lo que un diagnóstico «sin
// efectos» no debe hacer). No amplían la API sagrada `enu.*`: son superficie Go
// interna, consumida por el binario.

// ConfigDir devuelve el directorio de configuración resuelto (`config.dir()`, §14).
func (rt *Runtime) ConfigDir() string { return rt.ldr.configDir }

// DataDir devuelve el directorio de datos resuelto (donde viven las sesiones, §14).
func (rt *Runtime) DataDir() string { return rt.ldr.dataDir }

// PluginGraphDiag es el diagnóstico del grafo de activación de plugins SIN arrancar
// nada: reusa `discover()` (existencia y unicidad de los activados) y `topoSort()`
// (dependencias `requires` y ciclos). Alimenta los checks `plugins.enabled` y
// `plugins.requires` de `enu doctor`.
type PluginGraphDiag struct {
	EnabledOK      bool   // los plugins activados existen (ni ausentes ni colisión)
	EnabledDetail  string // si no, el error accionable (nombra la línea de enu.toml)
	RequiresRun    bool   // false si discover falló: no se pudo evaluar requires
	RequiresOK     bool   // requires resuelven, sin ciclos
	RequiresDetail string // si no, el error accionable (nombra plugin/dependencia/ciclo)
}

// DiagnosePluginGraph corre el descubrimiento y el orden topológico del grafo de
// plugins y devuelve el resultado como dato, sin `Boot()` (cero `init.lua`
// ejecutado). Un fallo de `discover` deja `RequiresRun=false`: no se puede juzgar
// `requires` si ni siquiera se resolvió qué plugins hay.
func (rt *Runtime) DiagnosePluginGraph() PluginGraphDiag {
	plugins, err := rt.ldr.discover()
	if err != nil {
		return PluginGraphDiag{EnabledOK: false, EnabledDetail: err.Error()}
	}
	diag := PluginGraphDiag{EnabledOK: true, RequiresRun: true}
	if _, terr := topoSort(plugins); terr != nil {
		diag.RequiresDetail = terr.Error()
	} else {
		diag.RequiresOK = true
	}
	return diag
}
