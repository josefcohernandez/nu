package runtime

import (
	lua "github.com/yuin/gopher-lua"
)

// Construcción del estado de un worker (api.md §13, §16). Un worker es un Runtime
// "recortado": mismo motor (estado Lua sandboxeado + scheduler) pero con
//
//   - **SIN watchdog** (G15): presupuesto de slice ≤ 0 → el scheduler no arma el
//     temporizador de slice. Los workers existen para quemar CPU; el control es
//     `terminate()` y las `caps`, no el watchdog.
//   - **SIN UI** (`uiActive=false`): `nu.ui` no se registra (§16: no [W]).
//   - **superficie [W] filtrada por `caps`** (G6, deny-by-default, dos
//     granularidades): solo lo concedido EXISTE en el estado del worker.
//   - **canal con el padre** (`nu.worker.parent`) en vez de `nu.worker.spawn` (sin
//     anidar, §16).
//
// El filtrado de caps es lo 🔒 de S34: el worker no recibe una API completa que
// luego "lance EACCES" —la superficie no concedida simplemente NO ESTÁ (es `nil`),
// el mismo modelo que el gating de `nu.ui` (G20). Así un plugin malicioso no puede
// ni nombrar lo que no se le dio.

// workerWModules enumera los submódulos [W] que un worker PUEDE recibir (api.md
// §16, columna "Disponible [W]"). Es la lista contra la que se aplican las `caps`:
// un módulo fuera de esta lista nunca llega a un worker (UI/events/watch/spawn/
// plugin), uno dentro llega solo si las `caps` lo conceden (o si no hay `caps`).
//
// `config` es especial: de él solo `dir` es [W] (§16: "config.dir"); `data_dir`,
// `current`, `list`, `reload`... son solo estado principal. Se trata aparte en
// `registerWorkerNu`.
var workerWModules = []string{
	"task", "fs", "proc", "sys", "http", "ws",
	"text", "re", "search", "json", "toml", "yaml", "log",
}

// newWorkerRuntime construye el Runtime de un worker: un `*lua.LState` nuevo y
// sandboxeado, un scheduler propio SIN watchdog (G15), la superficie [W] filtrada
// por `caps` (G6) y el canal `nu.worker.parent`. Comparte con el padre solo el
// `dataDir`/`configDir` (para `nu.log` y `nu.config.dir`, ambos [W]) y el logger
// (un fichero de log compartido es inocuo: append-only, con owner anotado). NADA de
// Lua se comparte (estado nuevo); la comunicación es por las colas de `chans`.
func newWorkerRuntime(parent *Runtime, chans *workerChannels, caps map[string]bool, capsGiven bool) *Runtime {
	// Estado Lua nuevo, sin librerías por defecto (igual que el principal): el
	// sandbox abre solo lo permitido (§1.2).
	L := lua.NewState(lua.Options{SkipOpenLibs: true})

	wrt := &Runtime{
		L:        L,
		log:      parent.log, // log compartido (append-only, owner anotado): inocuo
		fs:       &fsState{}, // tmpdir propio del worker (aislamiento de scratch)
		sys:      &sysState{},
		uiActive: false, // §16: nada de `nu.ui` en un worker
		http:     newHTTPState(parent.http.caFile, parent.http.proxy),
		ui:       nil, // headless siempre
		isWorker: true,
	}
	// El loader del worker comparte rutas y config con el del padre (para `config.dir`/
	// `data_dir`), pero el worker NUNCA hace `Boot` ni expone `nu.plugin` (§16): solo
	// se usa su `config.dir/data_dir`. Las rutas de `require` las siembra `run` con
	// los patrones que el padre calculó.
	wrt.ldr = newLoader(wrt, parent.ldr.dataDir, parent.ldr.configDir, parent.ldr.pluginDirs)

	// Scheduler SIN watchdog (G15): budget ≤ 0 desactiva el temporizador de slice (el
	// mismo gancho que usan los tests del principal que no quieren watchdog). Un
	// worker puede quemar CPU a gusto; lo corta `terminate()`.
	wrt.sched = newScheduler(wrt, 0)

	applySandbox(L)
	registerWorkerNu(wrt, chans, caps, capsGiven)
	return wrt
}

// registerWorkerNu construye la tabla global `nu` del worker: la superficie [W]
// (§16) filtrada por `caps` (G6) más `nu.worker.parent` (§13). NO registra
// `nu.ui`, `nu.events`, `nu.fs.watch`, `nu.worker.spawn` ni `nu.plugin` (§16).
//
// El filtrado es **deny-by-default** con dos granularidades:
//   - sin `caps` (`capsGiven=false`): toda la API [W].
//   - con `caps`: solo lo enumerado. `"fs"` concede el módulo `fs` entero; `"fs.read"`
//     concede solo `nu.fs.read`. Lo no concedido NO EXISTE (es `nil`). Una `caps`
//     antigua nunca concede una función añadida luego: solo lo que el array nombra
//     sobrevive (deny-by-default para superficie nueva).
func registerWorkerNu(wrt *Runtime, chans *workerChannels, caps map[string]bool, capsGiven bool) {
	L := wrt.L
	nu := L.NewTable()

	// `version` y `has` SIEMPRE presentes (§2): no son superficie recortable por
	// caps —son la detección de capacidades, que debe existir para preguntar—.
	version := L.NewTable()
	version.RawSetString("major", lua.LNumber(VersionMajor))
	version.RawSetString("minor", lua.LNumber(VersionMinor))
	version.RawSetString("patch", lua.LNumber(VersionPatch))
	version.RawSetString("api", lua.LNumber(APILevel))
	nu.RawSetString("version", version)
	nu.RawSetString("has", L.NewFunction(wrt.nuHas))

	// Registra TODA la superficie [W] en `nu`, igual que en el principal (reusa las
	// mismas funciones `registerXxx`): el estado del worker es un Runtime de pleno
	// derecho, así que sus primitivas son las mismas. El recorte por caps se aplica
	// DESPUÉS, podando el árbol —registrar y luego podar es más simple y robusto que
	// registrar selectivamente función a función, y deja un solo punto de verdad
	// (las mismas `registerXxx` del principal)—.
	wrt.sched.register(nu)         // nu.task (§3) [W]
	wrt.sched.installCancelPcall() // desenrollado no capturable (§1.3): SIEMPRE
	wrt.registerFs(nu)             // nu.fs (§5) [W] salvo watch (que registerFs no añade)
	wrt.registerProc(nu)           // nu.proc (§6) [W]
	wrt.registerSys(nu)            // nu.sys (§7) [W]
	wrt.registerCodecs(nu)         // nu.json/toml/yaml (§12) [W]
	wrt.registerHTTP(nu)           // nu.http (§8) [W]
	wrt.registerWs(nu)             // nu.ws (§8) [W]
	wrt.registerText(nu)           // nu.text (§10) [W]
	wrt.registerRe(nu)             // nu.re (§10) [W]
	wrt.registerSearch(nu)         // nu.search (§11) [W]
	registerLog(wrt, nu)           // nu.log (§15) [W] + alias print

	// `nu.config.dir`/`data_dir` son [W] (§14/§16); el resto de `plugin`
	// (`current`/`list`/`reload`) NO (ciclo de vida, solo estado principal). En vez
	// de registrar `nu.plugin` entero se cuelga una tabla `config` mínima con solo
	// las dos funciones [W].
	config := L.NewTable()
	config.RawSetString("dir", L.NewFunction(wrt.configDir))
	config.RawSetString("data_dir", L.NewFunction(wrt.configDataDir))
	nu.RawSetString("config", config)

	// PODA por caps (G6): si hay `caps`, se eliminan de `nu` los módulos/funciones
	// no concedidos. `nu.config.dir` se trata como la capacidad `"config.dir"`
	// (granularidad de función) o `"config"` (módulo); deny-by-default si no se nombra.
	if capsGiven {
		pruneByCaps(L, nu, caps)
	}

	// El canal con el padre (§13) va SIEMPRE, tras la poda: es el medio de
	// comunicación del worker, no una capacidad recortable —sin él un worker no
	// puede ni reportar resultados—. `nu.worker.parent` (sin `spawn`: no anidar, §16).
	wrt.registerWorkerParent(nu, chans)

	L.SetGlobal("nu", nu)
}

// pruneByCaps elimina de la tabla `nu` del worker todo módulo/función [W] que las
// `caps` no concedan (G6, deny-by-default). Recorre los módulos [W] conocidos
// (`workerWModules` + `config`); para cada uno:
//
//   - `caps["M"]` (módulo entero) → se conserva tal cual.
//   - existe algún `caps["M.fn"]` → se conservan SOLO esas funciones de M; si no
//     queda ninguna (todas las nombradas eran inexistentes), M se elimina entero.
//   - ni una ni otra → M se elimina (deny-by-default).
//
// `version`/`has` y `worker` (que se añade después) NO se tocan: no son superficie
// recortable. Solo se podan los módulos [W] enumerados; cualquier otra clave de `nu`
// (no debería haberla en un worker) se deja, pero la lista es exhaustiva por
// construcción.
func pruneByCaps(L *lua.LState, nu *lua.LTable, caps map[string]bool) {
	modules := append(append([]string(nil), workerWModules...), "config")
	for _, mod := range modules {
		modV := nu.RawGetString(mod)
		modT, ok := modV.(*lua.LTable)
		if !ok {
			continue // el módulo no estaba registrado (no debería): nada que podar
		}

		if caps[mod] {
			continue // módulo entero concedido: se conserva tal cual
		}

		// ¿Alguna función concreta de este módulo concedida (`"mod.fn"`)? Conserva solo
		// esas; elimina el resto. Construye una tabla nueva con las supervivientes para
		// no depender de borrar mientras se itera.
		kept := L.NewTable()
		any := false
		modT.ForEach(func(k, v lua.LValue) {
			name, isStr := k.(lua.LString)
			if !isStr {
				return // claves no-string (no debería haberlas en un módulo): se descartan
			}
			if caps[mod+"."+string(name)] {
				kept.RawSetString(string(name), v)
				any = true
			}
		})

		if any {
			nu.RawSetString(mod, kept) // granularidad de función: solo lo concedido
		} else {
			nu.RawSetString(mod, lua.LNil) // nada concedido de M: M no existe
		}
	}
}
