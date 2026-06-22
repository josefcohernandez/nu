// Package runtime levanta el intérprete Lua del core de nu: construye el estado
// gopher-lua, aplica el baseline del sandbox (api.md §1.2), inyecta el global
// `nu` y expone la evaluación de código. Es la quilla sobre la que las sesiones
// posteriores cuelgan cada submódulo de la API (task, fs, http, ...).
package runtime

import (
	"path/filepath"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// defaultSliceBudget es el presupuesto por slice del watchdog (api.md §1.3, S09):
// el tiempo máximo que una task puede correr Lua de forma continua sin suspender.
// 100 ms por defecto; `WithSliceBudget` lo ajusta (el gancho que S11/S12
// cablearán a la lectura de `nu.toml`).
const defaultSliceBudget = 100 * time.Millisecond

// Runtime envuelve un estado Lua ya sandboxeado y con el global `nu` inyectado.
// El estado principal es single-threaded (ADR-004); un Runtime se usa desde una
// sola goroutine.
type Runtime struct {
	L *lua.LState

	// sched es el event loop y el scheduler de tasks (§1.3, §3). Es la quilla:
	// `nu.task`, los puntos de suspensión ⏸ y, en adelante, todo lo async cuelga
	// de él. Una sola goroutine (la del loop) lo toca.
	sched *scheduler

	// log respalda `nu.log` (§15): un fichero append-only en data_dir.
	log *logger

	// fs es el estado de sesión de `nu.fs` (§5, S14): hoy solo el directorio
	// temporal propio (`tmpdir`), creado perezosamente y borrado en `Close`. Las
	// primitivas de `fs` son funciones sobre `rt` (el IO es sin estado salvo el
	// tmpdir de sesión).
	fs *fsState

	// sys es el estado de sesión de `nu.sys` (§7, S17): hoy solo el **overlay de
	// `setenv`** (variables que `nu.sys.setenv` registra y que `nu.proc` aplica al
	// lanzar un subproceso futuro, sin mutar el entorno del proceso `nu` actual).
	// El candado del overlay protege la carrera entre `setenv` (estado principal,
	// bajo el token) y las goroutines de fondo de `nu.proc` que lo leen sin él.
	sys *sysState

	// ldr es el loader de plugins (§14, S11): descubre los directorios con
	// `plugin.toml`, los ordena topológicamente por `requires` y ejecuta su
	// arranque canónico. También respalda `nu.plugin.current/list` y
	// `nu.config.dir/data_dir`. Inmutable tras `New`.
	ldr *loader

	// jsonNull es el sentinel `nu.json.NULL` (§12, S18): un userdata único del
	// estado Lua que representa `null` de JSON sin colisionar con ningún valor Lua.
	// `decode` lo entrega en lugar de `nil` (que borraría la clave de una tabla,
	// rompiendo el round-trip) y `encode` lo reconoce por identidad para emitir
	// `null`. Se crea una sola vez en `registerCodecs` y nunca cambia.
	jsonNull *lua.LUserData

	// ownerStack es la pila de contextos de plugin activos (§14). El tope es el
	// plugin "en cuyo contexto corre el código" que devuelve `nu.plugin.current`;
	// vacía = código del usuario/core (`init.lua` del usuario, chunk de `-e`),
	// cuyo owner de log es "user". El loader la empuja antes de correr el
	// `init.lua` de un plugin y la saca al terminar. **Solo se muta bajo el token**
	// (el arranque corre en el estado principal con el token tomado), y solo se lee
	// desde código Lua —que también exige el token—: sin candado ni carrera.
	ownerStack []*pluginInfo
}

// config recoge los parámetros de construcción de un Runtime. Es interno: se
// configura con Options.
type config struct {
	dataDir string
	// sliceBudget es el presupuesto por slice del watchdog (S09). Cero o negativo
	// **desactiva** el watchdog —útil para tests que no lo quieren—; el default de
	// producción es `defaultSliceBudget` (100 ms).
	sliceBudget time.Duration
	// sliceBudgetSet marca que `WithSliceBudget` se pasó explícitamente. Da
	// precedencia a la Option sobre `nu.toml` `watchdog.slice_budget_ms` (S12): un
	// test que fija un presupuesto pequeño no lo pisa la config de disco. Sin la
	// Option, manda `nu.toml`; sin `nu.toml`, el default (100 ms).
	sliceBudgetSet bool

	// configDir respalda `nu.config.dir()` (§14): `~/.config/nu` por defecto. De
	// ahí cuelga el `init.lua` del usuario (el último del arranque canónico) y, en
	// S12, `nu.toml`. Los tests lo apuntan a un `t.TempDir()`.
	configDir string
	// pluginDirs son los directorios donde el loader busca plugins (cada
	// subdirectorio con `plugin.toml` es un plugin, §14). En S11 se pasan por
	// Option; la activación gobernada por `nu.toml` y las embebidas (`go:embed`)
	// son S12. Vacío = arranque desnudo (sin plugins), solo el `init.lua` del
	// usuario.
	pluginDirs []string
}

// Option ajusta la construcción de un Runtime. El default sirve para producción
// (`nu -e`); los tests inyectan, p. ej., un data_dir temporal.
type Option func(*config)

// WithDataDir fija el directorio donde vive el estado en disco (de momento, solo
// el fichero de `nu.log`). Los tests lo apuntan a un `t.TempDir()` para no
// escribir en el data_dir real del usuario.
func WithDataDir(dir string) Option {
	return func(c *config) { c.dataDir = dir }
}

// WithSliceBudget ajusta el presupuesto por slice del watchdog (S09, api.md
// §1.3). Es el **gancho de configuración** que S11/S12 cablearán a `nu.toml`; por
// ahora lo usan los tests para fijar un presupuesto pequeño (corte rápido) o
// desactivar el watchdog (`<= 0`). En producción, sin opción, rige
// `defaultSliceBudget` (100 ms).
func WithSliceBudget(d time.Duration) Option {
	return func(c *config) { c.sliceBudget = d; c.sliceBudgetSet = true }
}

// WithConfigDir fija el directorio de configuración (`nu.config.dir()`, §14): de
// ahí sale el `init.lua` del usuario y, en S12, `nu.toml`. Los tests lo apuntan a
// un `t.TempDir()` para no leer el `~/.config/nu` real ni depender del entorno.
func WithConfigDir(dir string) Option {
	return func(c *config) { c.configDir = dir }
}

// WithPluginDir añade un directorio donde el loader busca plugins (cada
// subdirectorio con `plugin.toml` es un plugin, §14). Acumulable. En S11 es la vía
// de carga; S12 añade las extensiones embebidas (`go:embed`) y la activación por
// `nu.toml`. Los directorios se exploran en el orden en que se añaden (antes de la
// ordenación topológica, que es la que fija el orden de carga real).
func WithPluginDir(dir string) Option {
	return func(c *config) { c.pluginDirs = append(c.pluginDirs, dir) }
}

// New construye un Runtime listo para ejecutar Lua: abre solo las librerías
// permitidas por el baseline (§1.2), recorta `os`, elimina `io`/`dofile`/
// `loadfile`, redirige `print` a `nu.log.info` e inyecta el global `nu` con sus
// submódulos disponibles en esta sesión.
func New(opts ...Option) *Runtime {
	cfg := config{
		dataDir:     defaultDataDir(),
		configDir:   defaultConfigDir(),
		sliceBudget: defaultSliceBudget,
	}
	for _, o := range opts {
		o(&cfg)
	}

	// `nu.toml` gobierna al core (§14, ADR-010, S12): activación de plugins, rutas
	// extra y presupuesto del watchdog. Se lee de `config.dir()/nu.toml` ya en la
	// construcción porque sus valores deben estar disponibles antes de `Boot`
	// (presupuesto del watchdog → scheduler) y antes de descubrir plugins (rutas
	// extra, lista de activación). Un `nu.toml` ausente es lo normal (runtime
	// desnudo): no activa nada y no es error. Un `nu.toml` MAL FORMADO sí es un
	// error de arranque accionable, pero `New` no devuelve error (su firma es
	// sagrada): se aplaza al `Boot`, que sí lo devuelve a `main`/tests —el arranque
	// no debe quedar a medias por una config rota—.
	nuCfg, _, tomlErr := loadNuToml(cfg.configDir)

	// El presupuesto del watchdog: precedencia `WithSliceBudget` (Option explícita,
	// p. ej. tests) > `nu.toml` `watchdog.slice_budget_ms` > default (100 ms). Un
	// valor de `nu.toml` solo se aplica si la Option no lo fijó.
	if !cfg.sliceBudgetSet && tomlErr == nil && nuCfg.Watchdog.SliceBudgetMs != nil {
		cfg.sliceBudget = time.Duration(*nuCfg.Watchdog.SliceBudgetMs) * time.Millisecond
	}

	// Las rutas extra de `plugins.dirs` se suman a las de `WithPluginDir` (S12). El
	// loader las trata por igual; el orden de descubrimiento no fija el de carga
	// (eso es el orden topológico, S11).
	pluginDirs := append([]string(nil), cfg.pluginDirs...)
	if tomlErr == nil {
		pluginDirs = append(pluginDirs, nuCfg.Plugins.Dirs...)
	}

	// SkipOpenLibs: abrimos a mano solo lo que el baseline permite, en vez de
	// abrir todo y desactivar después; así una librería peligrosa nueva de
	// gopher-lua no entra por defecto (deny-by-default, coherente con las caps
	// de los workers, §13).
	L := lua.NewState(lua.Options{SkipOpenLibs: true})

	rt := &Runtime{
		L:   L,
		log: newLogger(filepath.Join(cfg.dataDir, logFileName)),
		fs:  &fsState{},
		sys: &sysState{},
	}
	rt.ldr = newLoader(rt, cfg.dataDir, cfg.configDir, pluginDirs)
	// El gating por `nu.toml` (qué se activa) y el error de config aplazado viven en
	// el loader, que es quien descubre y carga (S12). `Boot` consultará ambos.
	rt.ldr.enabled = nuCfg.Plugins.Enabled
	rt.ldr.configErr = tomlErr
	rt.sched = newScheduler(rt, cfg.sliceBudget)
	applySandbox(L)
	registerNu(rt)
	return rt
}

// currentOwner devuelve el nombre del plugin en cuyo contexto corre el código
// ahora mismo: el tope de `ownerStack` o "user" si está vacía (chunk de `-e`,
// `init.lua` del usuario, handlers sin plugin dueño). Es lo que `nu.log` anota en
// cada línea y lo que el watchdog reporta en `core:plugin.misbehaved`. Se lee
// siempre bajo el token (todo el que ejecuta Lua lo tiene), igual que se muta solo
// bajo el token en el arranque: sin carrera.
func (rt *Runtime) currentOwner() string {
	if n := len(rt.ownerStack); n > 0 {
		return rt.ownerStack[n-1].Name
	}
	return ownerUser
}

// emitMisbehaved es el **gancho interno** de `core:plugin.misbehaved` (api.md
// §1.3, §4). El watchdog (`runTask`) lo invoca cuando una task se abortó por
// exceder el presupuesto de un slice. S10 lo **cabló al bus real**: además de
// dejar constancia en el log (best-effort, como el resto de fallos de task),
// emite `core:plugin.misbehaved` por `nu.events` con el payload
// `{ plugin = owner, reason = reason }` (`core:` es el namespace que el kernel
// reserva, §4). El watchdog sigue llamando a este punto único, sin tocar su
// superficie.
//
// SEGURIDAD DEL HILO (la decisión delicada de S10, ver claude_decisions.md). El
// que llama es `runTask`, que corre en la goroutine de la task —sobre el thread
// `co`, NO sobre `host`— pero **con el token tomado** (la emisión ocurre antes de
// `release`). Que estemos en el thread de la task no importa: el bus es del estado
// principal y toca `host` (la tabla del payload, los threads efímeros de los
// handlers), no `co`; lo que protege esos accesos es el **token**, no qué thread
// corre. Por eso se emite **directamente** (síncrono) en vez de re-encolarlo a
// otra goroutine: ya tenemos el invariante que el bus necesita (token + estado
// principal). Si un handler de `core:plugin.misbehaved` re-emitiera, la cola de
// emits de `nu.events` (events.go) lo aplana por anchura, sin recursión.
func (rt *Runtime) emitMisbehaved(owner, reason string) {
	_ = rt.log.write(levelError, owner, "core:plugin.misbehaved: "+reason)

	// Construye el payload sobre `host` (seguro: tenemos el token). El bus puede no
	// estar inicializado en escenarios de test que construyen un scheduler a pelo;
	// en el runtime real `registerEvents` siempre corre antes de cualquier task.
	if rt.sched == nil || rt.sched.events == nil {
		return
	}
	payload := rt.L.NewTable()
	payload.RawSetString("plugin", lua.LString(owner))
	payload.RawSetString("reason", lua.LString(reason))
	// Pasamos `host` (rt.L) como thread llamante: la emisión de misbehaved es un
	// solo evento desde `runTask` (no un drenado de task que deba vigilar su propio
	// watchdog —la task que lo motivó ya está abortada—), así que no se ata al
	// borde cooperativo del watchdog de `emit`.
	rt.sched.emit(rt.L, "core:plugin.misbehaved", payload)
}

// Boot ejecuta el **arranque canónico** del runtime (api.md §14, S11): descubre y
// carga los plugins de los directorios configurados en orden topológico por
// `requires`, ejecuta el `init.lua` del usuario el último y emite `core:ready` una
// sola vez. Devuelve un error de carga **accionable** (colisión de nombre, ciclo o
// dependencia ausente) si el grafo de plugins es inválido; en ese caso no se ejecutó
// ningún `init.lua`. Llamarlo más de una vez es no-op. `main` lo invoca antes de
// `EvalString`; un `nu -e` sin directorios de plugins arranca igual (solo corre el
// `init.lua` del usuario, si existe, y emite `core:ready`).
func (rt *Runtime) Boot() error {
	return rt.ldr.Boot()
}

// Close libera el estado Lua subyacente, corta los timers periódicos activos
// (sus goroutines de ticker, para no dejarlas colgadas) y cierra el fichero de
// log si llegó a abrirse.
func (rt *Runtime) Close() {
	if rt.sched != nil {
		rt.sched.stopAllTimers()
		// Corta los `nu.fs.watch` activos (S15): sus goroutines de fondo y los
		// watchers del SO no deben sobrevivir al proceso.
		rt.sched.stopAllWatchers()
		// Mata los subprocesos vivos de `nu.proc.spawn` (S16): la última red de
		// seguridad de la vida del proceso (§6), tras `cleanup` y el finalizer del GC.
		rt.sched.stopAllProcs()
	}
	// Borra el directorio temporal de la sesión (`nu.fs.tmpdir`, §5) si llegó a
	// crearse: el scratch no debe sobrevivir al proceso.
	if rt.fs != nil {
		rt.fs.closeTmpdir()
	}
	if rt.log != nil {
		_ = rt.log.close()
	}
	rt.L.Close()
}
