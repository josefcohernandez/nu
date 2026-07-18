package runtime

// Arranque de plugins y extensiones oficiales sobre el backend wasm (M13d-ext,
// api.md §14, DM5). Es el gemelo de `loader.Boot` (loader.go) para la Instance
// wasm: REUSA la MISMA discovery (`discover`) y el MISMO orden topológico
// (`topoSort`) —no se reimplementan ni el embed ni la topología—; lo único que
// ramifica es DÓNDE corre el `init.lua` de cada extensión (sobre `rt.wasm`, no
// sobre `rt.L`) y CÓMO se resuelve `require` (el loader curado del preludio
// —vmwasm/loader.go, M13a— sirve las fuentes que aquí registramos con
// `Pool.RegisterModule`, en vez del `package.path` de gopher).
//
// Flujo (espejo del arranque canónico §14):
//  1. `discover` + `topoSort`: el MISMO grafo que gopher (embebidas activadas por
//     `plugins.enabled`, unicidad de nombre, deps y ciclos validados antes de
//     correr una línea de Lua).
//  2. `registerWasmModules`: registra en el Pool wasm las fuentes Lua de los
//     `lua/` de cada plugin bajo su nombre de `require` (el mismo mapeo que
//     `moduleNames`/`setupRequirePaths`: `foo.lua`→`foo`, `foo/init.lua`→`foo`,
//     `bar/baz.lua`→`bar.baz`). Todas ANTES de correr ningún init, para que un
//     `require` cruzado (agent→providers) resuelva desde el primer momento.
//  3. `runInitWasm` por cada plugin EN ORDEN TOPOLÓGICO, empujando su contexto al
//     `ownerStack` (dueño correcto de `enu.log`/`enu.plugin.current` durante el init).
//  4. `runUserInitWasm`: el `init.lua` del usuario, el ÚLTIMO (§14).
//  5. `core:ready` una sola vez, y `RunTasks` para drenar las tasks que los init
//     lanzaron (el equivalente wasm de soltar el token en gopher: las tasks
//     encoladas durante el arranque progresan tras él).
//
// Aislamiento de fallos (ADR-008): un `init.lua` que lanza NO tumba el arranque —se
// loguea, se emite `core:plugin.error`, y los demás siguen—, exactamente como
// `runInit` en gopher. El grafo, en cambio, se valida ANTES: un grafo roto
// (colisión, ciclo, dep ausente) devuelve error sin correr ningún init.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BootWasm ejecuta el arranque canónico (§14) sobre la Instance wasm. Es el gemelo
// de `Boot` (loader.go) que `Runtime.Boot` invoca cuando el backend es wasm.
// Devuelve un error de carga accionable (grafo inválido, `enu.toml` roto, fallo de
// construcción del estado wasm) sin haber corrido ningún `init.lua`; los fallos de
// un init individual se aíslan (ADR-008) y no se propagan.
func (l *loader) BootWasm() error {
	if l.booted {
		return nil
	}
	l.booted = true

	// Una config de runtime rota (`enu.toml` mal formado) aborta antes de tocar
	// plugin alguno (§12), igual que en gopher.
	if l.configErr != nil {
		return l.configErr
	}
	// Un fallo de construcción del estado wasm (buildWasmState, aplazado desde `New`
	// porque la firma de `New` es sagrada) se reporta aquí: sin Instance no hay dónde
	// cargar las extensiones.
	if l.rt.wasmErr != nil {
		return l.rt.wasmErr
	}
	if l.rt.wasm == nil || l.rt.wasmPool == nil {
		return &StructuredError{Code: CodeEIO,
			Message: "el estado wasm no se construyó: no se pueden cargar extensiones"}
	}

	plugins, err := l.discover()
	if err != nil {
		return err
	}
	ordered, err := topoSort(plugins)
	if err != nil {
		return err
	}
	l.ordered = ordered

	// Registra las fuentes de los módulos `lua/` de TODOS los plugins antes de
	// correr ningún init: un `require` cruzado debe resolver desde el primer init.
	if err := l.registerWasmModules(ordered); err != nil {
		return err
	}

	// Cada plugin: empuja su contexto, corre su `init.lua`, emite
	// `core:plugin.loaded`. Un init que lanza se aísla (ADR-008).
	for _, p := range ordered {
		l.runInitWasm(p)
	}

	// `init.lua` del usuario: el ÚLTIMO (§14), con owner "user" (pila vacía).
	l.runUserInitWasm()

	// `core:ready` UNA vez, al final del arranque canónico (§4, §14). Sin payload:
	// no hay strings que escapar y sus handlers (repl/chat montan UI si hay TTY) se
	// disparan síncronos aquí.
	l.emitCoreEventWasm("core:ready", nil)

	// Drena las tasks que los init.lua (y los handlers de core:ready) hayan lanzado:
	// el equivalente wasm de soltar el token en gopher (las tasks encoladas durante
	// el arranque corren después de él). Retorna en la quiescencia de primer plano
	// SIN matar el fondo (G44): los `every` que un init.lua arrancara quedan
	// pausados y el bombeo continuo del modo interactivo (PumpTasks, driver.go) o
	// el próximo drenaje headless los reanudan.
	if err := l.rt.wasm.RunTasks(context.Background()); err != nil {
		return err
	}
	return nil
}

// registerWasmModules registra en el Pool wasm (rt.wasmPool) la fuente Lua de cada
// módulo `lua/` de los plugins, bajo el nombre con que se `require`aría —el mismo
// mapeo que `setupRequirePaths`/`moduleNames` de gopher—. El `require` curado del
// preludio (M13a) las sirve por nombre. Una colisión de nombre de módulo entre dos
// plugins es un error de carga accionable (EEXIST de RegisterModule): el espacio de
// nombres de `require` es global, como en gopher lo es `package.path`.
func (l *loader) registerWasmModules(ordered []*pluginInfo) error {
	for _, p := range ordered {
		luaDir := filepath.Join(p.Dir, "lua")
		for _, m := range moduleFiles(luaDir) {
			src, err := os.ReadFile(m.path)
			if err != nil {
				return &StructuredError{Code: CodeEIO,
					Message: fmt.Sprintf("no se pudo leer el módulo %q del plugin %q: %v", m.path, p.Name, err)}
			}
			if err := l.rt.wasmPool.RegisterModule(m.name, string(src)); err != nil {
				return &StructuredError{Code: CodeEINVAL,
					Message: fmt.Sprintf("colisión de módulo %q al cargar el plugin %q (el nombre de require es único, §14): %v",
						m.name, p.Name, err)}
			}
		}
	}
	return nil
}

// runInitWasm ejecuta el `init.lua` de un plugin sobre la Instance wasm con su
// contexto empujado al `ownerStack` (así `enu.plugin.current()` y el owner de
// `enu.log` son ese plugin durante el init, §14). Un `init.lua` ausente es válido
// (el plugin puede ser solo módulos `lua/`). Un error del init queda AISLADO
// (ADR-008): se loguea, se emite `core:plugin.error`, y el arranque sigue. El owner
// se saca pase lo que pase.
func (l *loader) runInitWasm(p *pluginInfo) {
	initPath := filepath.Join(p.Dir, pluginInitName)
	src, err := os.ReadFile(initPath)
	if err != nil {
		// Sin init.lua (o ilegible): el plugin existe igual (sus módulos `lua/` ya
		// están registrados). Se considera cargado, como en gopher.
		l.emitLoadedWasm(p)
		return
	}

	l.rt.ownerStack = append(l.rt.ownerStack, p)
	_, luaErr, goErr := l.rt.wasm.Eval(string(src))
	l.rt.ownerStack = l.rt.ownerStack[:len(l.rt.ownerStack)-1]

	if goErr != nil {
		// Trap del motor wasm durante el init: es un fallo duro, pero lo AISLAMOS como
		// un error de init (ADR-008) para no tumbar el arranque entero por una
		// extensión rota; los demás y el usuario siguen cargando.
		l.reportInitErrorWasm(p.Name, goErr.Error())
		return
	}
	if luaErr != "" {
		l.reportInitErrorWasm(p.Name, luaErr)
		return
	}
	l.emitLoadedWasm(p)
}

// reloadWasm recarga un plugin ya cargado sobre la Instance wasm (api.md §14, G2).
// Es el gemelo de `loader.reload` (loader.go) para el backend wasm y sigue sus
// MISMOS pasos, en el MISMO orden (best-effort, G2 — deshace lo que el core sabe
// deshacer). Lo invoca el HostFn SUSPENDENTE `enu.plugin.reload` (vmwasm_plugin.go),
// que corre en la goroutine de fondo del scheduler: por eso puede llamar a
// `l.rt.wasm.Eval` (re-entra la VM tomando `inst.mu`, que en ese punto está libre).
//
// Un nombre que no corresponde a ningún plugin cargado es `EINVAL` accionable que lo
// nombra: no se puede recargar lo que no está (el gate ⏸ —fuera de una task— lo
// aplica antes el thunk suspendente del preludio, con `__current`).
func (l *loader) reloadWasm(name string) error {
	p := l.find(name)
	if p == nil {
		return &StructuredError{Code: CodeEINVAL,
			Message: fmt.Sprintf("no se puede recargar el plugin %q: no está cargado (enu.plugin.reload es para plugins ya cargados, §14)", name)}
	}

	// 1. `core:plugin.unload {name}` ANTES de soltar nada: las extensiones enganchadas
	//    limpian sus propios registros (tools, comandos...) —cosas que el core no
	//    conoce y no puede soltar por ellas (filosofía §1)—.
	l.emitCoreEventWasm("core:plugin.unload", map[string]string{"name": p.Name})

	// 2. Suelta TODOS los handles del plugin (registro por dueño del preludio,
	//    __release_owner): cancela sus suscripciones (`on`/`once`) y para sus timers
	//    (`every`). Tras esto los viejos no disparan: "reload no deja handlers
	//    huérfanos" (G2).
	l.releaseOwnerHandlesWasm(p.Name)

	//    Y el registro Go por dueño (handles.go): el preludio solo conoce subs y
	//    timers Lua; los procesos de `enu.proc.spawn` y los watchers de `enu.fs.watch`
	//    del plugin viven en el scheduler Go y sin este release sobrevivirían a la
	//    recarga (el contrato de proc.go es explícito: no deben).
	if l.rt.sched != nil {
		l.rt.sched.releaseOwnerHandles(p.Name)
	}

	// 3. Relee del disco los módulos `lua/` del plugin (pueden haber cambiado) y vacía
	//    su caché de `require`: un módulo modificado debe re-ejecutarse, no servirse
	//    cacheado (paridad con clearRequireCache de gopher, que re-lee de package.path).
	l.reloadModulesWasm(p)

	// 4. Re-ejecuta su `init.lua` con su contexto empujado al `ownerStack` (como en el
	//    arranque, §14): lo que registre queda de nuevo etiquetado por dueño. runInitWasm
	//    aísla un init que lanza (ADR-008) y emite `core:plugin.loaded`.
	l.runInitWasm(p)
	return nil
}

// releaseOwnerHandlesWasm suelta todos los handles del dueño `owner` invocando el
// registro Lua del preludio (__release_owner, preludioReload). Best-effort: un fallo
// del Eval no debe tumbar el reload (se ignora, como emitCoreEventWasm).
func (l *loader) releaseOwnerHandlesWasm(owner string) {
	_, _, _ = l.rt.wasm.Eval("__release_owner(" + luaStringLit(owner) + ")")
}

// reloadModulesWasm relee del disco las fuentes de los módulos `lua/` del plugin,
// las reemplaza en el registro del Pool (Pool.SetModule) y las olvida de la caché de
// `require` del preludio (__loader_forget), de modo que un `require` desde el
// `init.lua` re-ejecutado vuelva a CARGAR la versión nueva. Un módulo ilegible se
// omite (best-effort). Espejo de clearRequireCache (loader.go), que en gopher se
// apoya en que `require` re-lee de package.path; aquí la fuente vive en el Pool, así
// que hay que releerla y reemplazarla explícitamente.
func (l *loader) reloadModulesWasm(p *pluginInfo) {
	luaDir := filepath.Join(p.Dir, "lua")
	var forget strings.Builder
	for _, m := range moduleFiles(luaDir) {
		src, err := os.ReadFile(m.path)
		if err != nil {
			continue // un módulo ilegible: se deja la versión previa (best-effort)
		}
		l.rt.wasmPool.SetModule(m.name, string(src))
		forget.WriteString("__loader_forget(")
		forget.WriteString(luaStringLit(m.name))
		forget.WriteString(")\n")
	}
	if forget.Len() > 0 {
		_, _, _ = l.rt.wasm.Eval(forget.String())
	}
}

// runUserInitWasm ejecuta `config.dir()/init.lua` —el último del arranque canónico
// (§14)— sobre la Instance wasm, con owner "user" (pila de plugins vacía). Ausente
// es lo normal; un error se aísla igual que el de un plugin.
func (l *loader) runUserInitWasm() {
	initPath := filepath.Join(l.configDir, pluginInitName)
	src, err := os.ReadFile(initPath)
	if err != nil {
		return
	}
	if _, luaErr, goErr := l.rt.wasm.Eval(string(src)); goErr != nil {
		l.reportInitErrorWasm(ownerUser, goErr.Error())
	} else if luaErr != "" {
		l.reportInitErrorWasm(ownerUser, luaErr)
	}
}

// reportInitErrorWasm registra un fallo de init (log + `core:plugin.error`), el
// gemelo del bloque de error de `runInit`/`runUserInit` en gopher.
func (l *loader) reportInitErrorWasm(owner, msg string) {
	_ = l.rt.log.write(levelError, owner, fmt.Sprintf("el init.lua del plugin %q falló: %s", owner, msg))
	l.emitCoreEventWasm("core:plugin.error", map[string]string{"plugin": owner, "error": msg})
}

// emitLoadedWasm emite `core:plugin.loaded` con `{name, version, dir}` (§4, §14).
func (l *loader) emitLoadedWasm(p *pluginInfo) {
	l.emitCoreEventWasm("core:plugin.loaded", map[string]string{
		"name": p.Name, "version": p.Version, "dir": p.Dir,
	})
}

// emitCoreEventWasm emite un evento `core:*` sobre la Instance wasm con un payload
// de strings (o sin payload si `payload` es nil). Construye la llamada
// `enu.events.emit(name, {k=v, ...})` con literales Lua ESCAPADOS (luaStringLit): el
// payload viene de Go (nombres/rutas de plugin) y no debe poder inyectar código.
// Corre síncrono (emit no suspende, M08): sus handlers se despachan aquí mismo.
func (l *loader) emitCoreEventWasm(name string, payload map[string]string) {
	var b strings.Builder
	b.WriteString("enu.events.emit(")
	b.WriteString(luaStringLit(name))
	if payload != nil {
		b.WriteString(", {")
		// Orden estable (name, version, dir / plugin, error) no importa para el
		// consumidor; iteramos el mapa tal cual (pocas claves, todas conocidas).
		for k, v := range payload {
			b.WriteString(k)
			b.WriteString(" = ")
			b.WriteString(luaStringLit(v))
			b.WriteString(", ")
		}
		b.WriteString("}")
	}
	b.WriteString(")")
	// El emit no debería lanzar (los handlers van bajo pcall, M08); si un handler
	// exótico rompe el propio emit, lo ignoramos: el arranque no debe caer por eso.
	_, _, _ = l.rt.wasm.Eval(b.String())
}

// moduleFile es un módulo `lua/` descubierto: su nombre de `require` y la ruta del
// fichero fuente en disco.
type moduleFile struct {
	name string
	path string
}

// moduleFiles enumera los módulos `require`-ables bajo `luaDir` con su ruta de
// fichero. Espejo EXACTO de `moduleNames` (loader.go) en el mapeo nombre→ruta:
// la ruta relativa sin `.lua`, con `/`→`.`, y un `init.lua` colapsado a su
// directorio (`foo/init.lua`→`foo`); `lua/init.lua` no es require-able. Un `luaDir`
// inexistente devuelve vacío (un plugin puede no tener `lua/`).
func moduleFiles(luaDir string) []moduleFile {
	var mods []moduleFile
	_ = filepath.Walk(luaDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".lua") {
			return nil
		}
		rel, relErr := filepath.Rel(luaDir, path)
		if relErr != nil {
			return nil
		}
		rel = strings.TrimSuffix(rel, ".lua")
		rel = strings.TrimSuffix(rel, string(filepath.Separator)+"init")
		if rel == "init" {
			return nil // `lua/init.lua` no es un módulo require-able por convención
		}
		mods = append(mods, moduleFile{
			name: strings.ReplaceAll(rel, string(filepath.Separator), "."),
			path: path,
		})
		return nil
	})
	return mods
}

// luaStringLit rinde `s` como un literal de string Lua ENTRE COMILLAS y escapado,
// seguro para interpolar en un chunk (los valores vienen de Go —nombres/rutas de
// plugin— y no deben poder cerrar la comilla ni inyectar código). Escapa la
// comilla, la barra, los saltos y el nul; el resto de bytes van tal cual (los
// strings de Lua son byte-arrays).
func luaStringLit(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case 0:
			b.WriteString(`\0`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
