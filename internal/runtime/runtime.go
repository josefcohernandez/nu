// Package runtime levanta el intérprete Lua del core de enu sobre el backend wasm
// (PUC-Lua oficial en internal/vmwasm): construye el estado wasm con el sandbox
// curado, cuelga el catálogo `enu.*` (registerWasmCatalog) y expone la evaluación
// de código. Desde M17 wasm es la única VM (gopher-lua se retiró). Es la quilla
// sobre la que las extensiones cuelgan cada submódulo de la API (task, fs, http…).
package runtime

import (
	"path/filepath"
	"time"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

// defaultSliceBudget es el presupuesto por slice del watchdog (api.md §1.3, S09):
// el tiempo máximo que una task puede correr Lua de forma continua sin suspender.
// 100 ms por defecto; `WithSliceBudget` lo ajusta (el gancho que S11/S12
// cablearán a la lectura de `enu.toml`).
const defaultSliceBudget = 100 * time.Millisecond

// Runtime envuelve un estado Lua ya sandboxeado y con el global `enu` inyectado.
// El estado principal es single-threaded (ADR-004); un Runtime se usa desde una
// sola goroutine.
type Runtime struct {
	// sched conserva el token de ejecución y el presupuesto por slice del watchdog
	// (scheduler.go). El scheduler de tasks real (corrutinas Lua) vive dentro del
	// backend wasm (internal/vmwasm); aquí el token serializa el pintor del
	// compositor con el driver de TTY y los tests que leen el compositor.
	sched *scheduler

	// log respalda `enu.log` (§15): un fichero append-only en data_dir.
	log *logger

	// fs es el estado de sesión de `enu.fs` (§5, S14): hoy solo el directorio
	// temporal propio (`tmpdir`), creado perezosamente y borrado en `Close`. Las
	// primitivas de `fs` son funciones sobre `rt` (el IO es sin estado salvo el
	// tmpdir de sesión).
	fs *fsState

	// sys es el estado de sesión de `enu.sys` (§7, S17): hoy solo el **overlay de
	// `setenv`** (variables que `enu.sys.setenv` registra y que `enu.proc` aplica al
	// lanzar un subproceso futuro, sin mutar el entorno del proceso `enu` actual).
	// El candado del overlay protege la carrera entre `setenv` (estado principal,
	// bajo el token) y las goroutines de fondo de `enu.proc` que lo leen sin él.
	sys *sysState

	// ldr es el loader de plugins (§14, S11): descubre los directorios con
	// `plugin.toml`, los ordena topológicamente por `requires` y ejecuta su
	// arranque canónico. También respalda `enu.plugin.current/list` y
	// `enu.config.dir/data_dir`. Inmutable tras `New`.
	ldr *loader

	// http es el estado de sesión de `enu.http` (§8, S19): la config de red
	// (`[net]` de `enu.toml`: CA corporativa y proxy por defecto, G12) y el cliente
	// HTTP **reutilizable** del caso común (sin overrides de TLS/proxy por
	// petición), con su pool de conexiones. Una petición que pide TLS/proxy
	// específico construye un cliente por-petición; el resto reusa este.
	http *httpState

	// ui es el estado de sesión de `enu.ui` (§9.1, S29): el **compositor** (rejilla
	// de pantalla, regiones con z-order, diff→ANSI, coalescing). Vive en el estado
	// principal bajo el token (ADR-008: `enu.ui` es solo estado principal). Las
	// regiones que entrega son `ownedHandle` (S13): un `reload` las destruye con el
	// resto de los handles del plugin (G2). Inmutable tras `New` (el compositor sí
	// muta: regiones, tamaño, frames). **En headless es `nil`** (G20, S32): sin
	// superficie de UI no hay compositor; `armPainter`/`stopPainter`/`Close` ya lo
	// toleran (`rt.ui == nil`).
	ui *uiState

	// uiActive decide el GATING HEADLESS de `enu.ui` (G20, §9, S32): si es true, el
	// módulo `enu.ui` se registra en el global `enu`, el compositor se construye y
	// `enu.has("ui")` es true; si es false (headless: `enu -e`, CI, salida redirigida),
	// `enu.ui` NO EXISTE y `enu.has("ui")` es false. Lo fija `New`: por `WithForceUI`
	// (precedencia, lo usan los tests de UI) o, en su defecto, por la detección de un
	// TTY interactivo (`detectTTY`). Inmutable tras `New`.
	uiActive bool

	// isWorker marca que este Runtime es el de un **worker** (§13, S34), no el
	// estado principal. Un worker es un mini-runtime completo (scheduler propio,
	// tasks/timers/futures) pero SIN watchdog (G15), SIN `enu.ui`/`enu.events`/
	// `enu.fs.watch`/`enu.worker.spawn`/`enu.plugin` (§16) y con la superficie [W]
	// recortada por `caps` (G6). Lo construye `newWorkerRuntime` (worker_registry.go);
	// el estado principal lo deja en false. Inmutable tras la construcción.
	isWorker bool

	// wasmPool / wasm son el estado del backend wasm: el Pool que compila el catálogo
	// de primitivas enu.* y la Instance —el estado Lua aislado— sobre la que corren
	// `EvalString`/`EvalTaskString`. Los construye `buildWasmState` (siempre, desde
	// M17); `Close` los libera. El estado de sesión (fs, http, sys, ui, log) lo
	// reusa el catálogo wasm (rt.fs/rt.http/...).
	wasmPool *vmwasm.Pool
	wasm     *vmwasm.Instance
	// wasmErr guarda un fallo de construcción del estado wasm (`buildWasmState`).
	// La firma de `New` es sagrada (no devuelve error), así que un fallo del backend
	// wasm no puede propagarse ahí: se aparca aquí y lo devuelven `EvalString`/
	// `EvalTaskString` al primer intento de evaluar (como el `configErr` aplazado a
	// `Boot`).
	wasmErr error

	// ownerStack es la pila de contextos de plugin activos (§14). El tope es el
	// plugin "en cuyo contexto corre el código" que devuelve `enu.plugin.current`;
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
	// precedencia a la Option sobre `enu.toml` `watchdog.slice_budget_ms` (S12): un
	// test que fija un presupuesto pequeño no lo pisa la config de disco. Sin la
	// Option, manda `enu.toml`; sin `enu.toml`, el default (100 ms).
	sliceBudgetSet bool

	// configDir respalda `enu.config.dir()` (§14): `~/.config/enu` por defecto. De
	// ahí cuelga el `init.lua` del usuario (el último del arranque canónico) y, en
	// S12, `enu.toml`. Los tests lo apuntan a un `t.TempDir()`.
	configDir string
	// pluginDirs son los directorios donde el loader busca plugins (cada
	// subdirectorio con `plugin.toml` es un plugin, §14). En S11 se pasan por
	// Option; la activación gobernada por `enu.toml` y las embebidas (`go:embed`)
	// son S12. Vacío = arranque desnudo (sin plugins), solo el `init.lua` del
	// usuario.
	pluginDirs []string

	// uiW, uiH fijan el tamaño inicial de la pantalla de `enu.ui` (§9.1) cuando no
	// hay un TTY del que leerlo (entorno headless de S29: la negociación con el
	// terminal real son S33+). Cero = sin Option: se resuelve por `COLUMNS`/`LINES`
	// del entorno o, en su defecto, 80×24 (default razonable). Los tests del
	// compositor inyectan un tamaño pequeño con `WithUISize` para forzar el recorte de
	// regiones fuera de pantalla (G1).
	uiW, uiH int

	// forceUI / forceUISet gobiernan el GATING HEADLESS de `enu.ui` (G20, §9, S32). Sin
	// `WithForceUI` (`forceUISet=false`), la activación de `enu.ui` la decide la
	// detección de un TTY interactivo (`detectTTY`): así `enu -e` o una salida
	// redirigida arrancan SIN `enu.ui`. Con `WithForceUI(v)` (`forceUISet=true`), `v`
	// manda y se salta la detección de TTY: es la vía de los TESTS, que corren
	// headless pero necesitan `enu.ui` para ejercitar el compositor/input/clipboard
	// (`newHarness` la activa). El gating REAL por TTY aplica al binario `enu`.
	forceUI    bool
	forceUISet bool

	// enabledOverride / enabledOverrideSet permiten fijar `plugins.enabled` EN MEMORIA,
	// sobrescribiendo lo que diga `enu.toml`, SIN tocar disco (ADR-015, G33). Es la vía del
	// modo EFÍMERO de `enu --default-config -p/-e`: activar el conjunto oficial de producto
	// solo para ese proceso (Docker/CI inmutable). Con `enabledOverrideSet=true`, el valor
	// de `enabledOverride` reemplaza a `nuCfg.Plugins.Enabled` tras leer `enu.toml`. Una lista
	// vacía explícita es válida (activa nada); distinta de "no fijada" (usa `enu.toml`).
	enabledOverride    []string
	enabledOverrideSet bool
}

// Option ajusta la construcción de un Runtime. El default sirve para producción
// (`enu -e`); los tests inyectan, p. ej., un data_dir temporal.
type Option func(*config)

// WithDataDir fija el directorio donde vive el estado en disco (de momento, solo
// el fichero de `enu.log`). Los tests lo apuntan a un `t.TempDir()` para no
// escribir en el data_dir real del usuario.
func WithDataDir(dir string) Option {
	return func(c *config) { c.dataDir = dir }
}

// WithSliceBudget ajusta el presupuesto por slice del watchdog (S09, api.md
// §1.3). Es el **gancho de configuración** que S11/S12 cablearán a `enu.toml`; por
// ahora lo usan los tests para fijar un presupuesto pequeño (corte rápido) o
// desactivar el watchdog (`<= 0`). En producción, sin opción, rige
// `defaultSliceBudget` (100 ms).
func WithSliceBudget(d time.Duration) Option {
	return func(c *config) { c.sliceBudget = d; c.sliceBudgetSet = true }
}

// WithConfigDir fija el directorio de configuración (`enu.config.dir()`, §14): de
// ahí sale el `init.lua` del usuario y, en S12, `enu.toml`. Los tests lo apuntan a
// un `t.TempDir()` para no leer el `~/.config/enu` real ni depender del entorno.
func WithConfigDir(dir string) Option {
	return func(c *config) { c.configDir = dir }
}

// WithPluginDir añade un directorio donde el loader busca plugins (cada
// subdirectorio con `plugin.toml` es un plugin, §14). Acumulable. En S11 es la vía
// de carga; S12 añade las extensiones embebidas (`go:embed`) y la activación por
// `enu.toml`. Los directorios se exploran en el orden en que se añaden (antes de la
// ordenación topológica, que es la que fija el orden de carga real).
func WithPluginDir(dir string) Option {
	return func(c *config) { c.pluginDirs = append(c.pluginDirs, dir) }
}

// WithUISize fija el tamaño inicial de la pantalla de `enu.ui` en celdas (§9.1).
// Es el gancho de los tests del compositor: inyectan una pantalla pequeña para
// forzar el recorte de regiones fuera de pantalla (G1) sin depender de un TTY. En
// producción, sin Option, el tamaño sale del entorno (`COLUMNS`/`LINES`) o del
// default 80×24 (la negociación con el terminal real es S33+). Valores no positivos
// se ignoran (se cae al default).
func WithUISize(w, h int) Option {
	return func(c *config) {
		if w > 0 && h > 0 {
			c.uiW, c.uiH = w, h
		}
	}
}

// WithForceUI fuerza el estado del GATING HEADLESS de `enu.ui` (G20, §9, S32),
// saltándose la detección de TTY. `WithForceUI(true)` registra `enu.ui` y deja
// `enu.has("ui")` en true aunque no haya terminal; `WithForceUI(false)` lo desactiva
// aunque lo haya. Es la vía de los **tests**: corren headless (sin TTY) pero
// necesitan `enu.ui` para ejercitar el compositor/input/clipboard —el arnés
// (`newHarness`) la activa, y por eso los tests de S22–S31 siguen verdes pese a que
// ahora, sin esta Option, `enu.ui` no existiría en su entorno sin TTY—. En el binario
// `enu` real esta Option NO se pasa: el gating lo decide `detectTTY` (un `enu -e` o una
// salida redirigida arrancan sin `enu.ui`, como exige el "Criterio de hecho" de S32).
func WithForceUI(active bool) Option {
	return func(c *config) { c.forceUI = active; c.forceUISet = true }
}

// WithEnabledPlugins fija `plugins.enabled` EN MEMORIA, sobrescribiendo lo que diga
// `config.dir()/enu.toml`, SIN escribir nada a disco (ADR-015, G33). Es la vía del modo
// EFÍMERO de `enu --default-config` combinado con una acción headless (`-p`/`-e`): el
// binario activa el conjunto oficial de producto solo para ESE proceso, sin reescribir la
// config del usuario —el caso del contenedor inmutable—. El valor reemplaza por completo
// a la lista de `enu.toml` (no se fusiona): una lista vacía explícita activa nada. El resto
// de `enu.toml` (watchdog, dirs, net) se sigue respetando. La usan también los tests para
// inyectar un conjunto activo sin escribir un `enu.toml` temporal.
func WithEnabledPlugins(names []string) Option {
	return func(c *config) {
		c.enabledOverride = append([]string(nil), names...)
		c.enabledOverrideSet = true
	}
}

// WithVMBackend existía para forzar un backend de VM (patrón estrangulador). Desde
// M17 sólo hay wasm, así que es un **no-op** que se conserva para no romper la firma
// de los tests que aún la pasan (`WithVMBackend(VMWasm)`).
func WithVMBackend(VMBackend) Option {
	return func(*config) {}
}

// VMBackend devuelve el motor de VM de este Runtime: desde M17, siempre wasm.
func (rt *Runtime) VMBackend() VMBackend { return VMWasm }

// New construye un Runtime listo para ejecutar Lua: abre solo las librerías
// permitidas por el baseline (§1.2), recorta `os`, elimina `io`/`dofile`/
// `loadfile`, redirige `print` a `enu.log.info` e inyecta el global `enu` con sus
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

	// `enu.toml` gobierna al core (§14, ADR-010, S12): activación de plugins, rutas
	// extra y presupuesto del watchdog. Se lee de `config.dir()/enu.toml` ya en la
	// construcción porque sus valores deben estar disponibles antes de `Boot`
	// (presupuesto del watchdog → scheduler) y antes de descubrir plugins (rutas
	// extra, lista de activación). Un `enu.toml` ausente es lo normal (runtime
	// desnudo): no activa nada y no es error. Un `enu.toml` MAL FORMADO sí es un
	// error de arranque accionable, pero `New` no devuelve error (su firma es
	// sagrada): se aplaza al `Boot`, que sí lo devuelve a `main`/tests —el arranque
	// no debe quedar a medias por una config rota—.
	nuCfg, _, tomlErr := loadNuToml(cfg.configDir)

	// El presupuesto del watchdog: precedencia `WithSliceBudget` (Option explícita,
	// p. ej. tests) > `enu.toml` `watchdog.slice_budget_ms` > default (100 ms). Un
	// valor de `enu.toml` solo se aplica si la Option no lo fijó.
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

	// GATING HEADLESS de `enu.ui` (G20, §9, S32): decide si hay superficie de UI. La
	// Option `WithForceUI` manda (la usan los tests); sin ella, lo decide la detección
	// de un TTY interactivo (`detectTTY`). En headless (`enu -e`, CI, salida
	// redirigida) sale false: ni `enu.ui` ni su compositor se construyen, y
	// `enu.has("ui")` será false.
	uiActive := cfg.forceUI
	if !cfg.forceUISet {
		uiActive = detectTTY()
	}

	rt := &Runtime{
		log:      newLogger(filepath.Join(cfg.dataDir, logFileName)),
		fs:       &fsState{},
		sys:      &sysState{},
		uiActive: uiActive,
		// Defaults de red de `[net]` (§8, G12, S19). Un `enu.toml` ausente o sin
		// `[net]` deja ambos vacíos (comportamiento estándar). No se aplica si el
		// `enu.toml` está mal formado (el error se aplaza a `Boot`).
		http: newHTTPState(nuCfg.Net.CAFile, nuCfg.Net.Proxy),
		// El compositor de `enu.ui` (§9.1, S29) **solo si hay UI** (G20, S32): en
		// headless `rt.ui` queda nil y no se gasta ni rejilla ni timer. Su tamaño sale
		// de la Option (tests), del entorno (`COLUMNS`/`LINES`) o del default 80×24. El
		// timer de coalescing (a lo sumo cada ~30 ms) se arma en `Boot`, cuando el event
		// loop ya corre; en `New` solo se construye el estado.
		ui: maybeUIState(uiActive, cfg.uiW, cfg.uiH),
	}
	rt.ldr = newLoader(rt, cfg.dataDir, cfg.configDir, pluginDirs)
	// El gating por `enu.toml` (qué se activa) y el error de config aplazado viven en
	// el loader, que es quien descubre y carga (S12). `Boot` consultará ambos.
	rt.ldr.enabled = nuCfg.Plugins.Enabled
	rt.ldr.configErr = tomlErr
	// `WithEnabledPlugins` (modo efímero de `enu --default-config`, ADR-015/G33) GANA sobre
	// `enu.toml`: fija la lista de activación en memoria sin tocar disco. Se aplica tras leer
	// `enu.toml` para que el resto de la config (watchdog, dirs, net) se respete y solo se
	// sustituya `enabled`. Un override con `enu.toml` mal formado NO limpia `configErr`: el
	// fichero roto sigue siendo un error de arranque que `Boot` reportará (no lo enmascaramos
	// silenciosamente por pasar `-e`/`-p`).
	if cfg.enabledOverrideSet {
		rt.ldr.enabled = cfg.enabledOverride
	}

	// Tolerancia de la retirada (M17): si el entorno (`NU_VM`) o `enu.toml [vm] backend`
	// piden todavía el backend `gopher`, ya eliminado, se avisa por stderr y se sigue
	// sobre wasm —enu no rompe el arranque por una config/script legacy—.
	warnIfGopherRequested(nuCfg.VM.Backend)

	rt.sched = newScheduler(rt, cfg.sliceBudget)

	// Arranque del backend wasm (la única VM desde M17): construye el Pool con el
	// catálogo completo de primitivas enu.* y la Instance. Reusa el estado de sesión
	// ya armado (rt.fs/rt.http/rt.sys/rt.ui/rt.log/rt.sched). Un fallo se aparca en
	// rt.wasmErr (la firma de New no devuelve error).
	rt.buildWasmState()
	return rt
}

// buildWasmState construye el estado del backend wasm (M13d): un Pool con el
// CATÁLOGO COMPLETO ya portado (registerWasmCatalog) y una Instance —el estado
// Lua aislado— lista para evaluar. Se llama desde `New` sólo cuando el backend
// resuelto es `VMWasm`, DESPUÉS de armar el `rt` gopher (patrón estrangulador: el
// catálogo reusa rt.fs/rt.http/rt.sys/rt.ui/rt.log/rt.sched, que `New` ya construyó).
// Un fallo aquí NO rompe la firma de `New` (que no devuelve error): se guarda en
// `rt.wasmErr` y lo propagan `EvalString`/`EvalTaskString` al primer eval. El
// watchdog por slice (DM4) ya está cableado (`SetSliceBudget`); la carga de las 8
// extensiones oficiales (M13d-ext) es sesión aparte: aquí se cablea el arranque, el
// catálogo síncrono/⏸ y el presupuesto del watchdog.
func (rt *Runtime) buildWasmState() {
	p, err := vmwasm.NewPool()
	if err != nil {
		rt.wasmErr = err
		return
	}
	// El nivel de enu.version.api que el preludio inyecta (api.md §2): el mismo que
	// el estado gopher (APILevel). Debe fijarse antes de NewInstance.
	p.SetAPIVersion(APILevel)
	// enu.version.major/minor/patch (api.md §1): las mismas constantes que el estado
	// gopher expone en registerNu, para que las extensiones (p. ej. el banner del
	// chat) formateen la versión completa. Debe fijarse antes de NewInstance.
	p.SetVersion(VersionMajor, VersionMinor, VersionPatch)
	// Presupuesto del watchdog por slice (DM4): el mismo `sliceBudget` que rige el
	// estado gopher (rt.sched.budget, ya resuelto por precedencia Option>enu.toml>
	// default). Un bucle de CPU en una task wasm se aborta con EBUDGET tras el slice,
	// idéntico a gopher. Debe fijarse antes de NewInstance (el preludio arma el hook).
	p.SetSliceBudget(rt.sched.budget)
	rt.registerWasmCatalog(p)
	inst, err := p.NewInstance()
	if err != nil {
		p.StopWorkers()
		_ = p.Close()
		rt.wasmErr = err
		return
	}
	rt.wasmPool = p
	rt.wasm = inst
}

// registerWasmCatalog cuelga en `p` el catálogo completo de primitivas enu.* ya
// portado a wasm (migracion-vm.md M13b/M13c), agrupado aquí para no ensuciar `New`.
// Cada `registerXWasm` es la contraparte del `registerX` gopher y reusa las mismas
// implementaciones Go VM-agnósticas del kernel; el estado de sesión que necesitan
// (rt.fs/rt.http/rt.sys/rt.log/rt.ui) llega por `rt`. `registerHTTPWasm` incluye
// `http.stream` y `registerTextWasm` incluye los Blocks de enu.text (se registran
// desde dentro). `registerUIWasm` instala el compositor SÓLO si hay UI concedida
// (`rt.ui != nil`, gating headless G20); en headless no registra nada.
func (rt *Runtime) registerWasmCatalog(p *vmwasm.Pool) {
	// Resolvedor del dueño vigente para la FOTO del spawn de un worker (G56, ADR-024):
	// enu.worker._spawn lo llama —en la goroutine de la VM, donde el ownerStack es
	// coherente— para capturar la identidad con que arranca cada worker. Sin esto un
	// worker leería el ownerStack del padre desde su propia goroutine (SEC-05/SEC-07).
	p.SetOwnerSnapshot(rt.currentOwner)
	registerCodecsWasm(p)     // enu.json/toml/yaml (§12)
	registerReWasm(p)         // enu.re (§10)
	registerFsWasm(p, rt)     // enu.fs (§5)
	registerSysWasm(p, rt)    // enu.sys (§7)
	registerLogWasm(p, rt)    // enu.log (§15)
	registerTextWasm(p, rt)   // enu.text width/truncate + wrap/markdown/highlight/diff (§10)
	registerHTTPWasm(p, rt)   // enu.http.request + enu.http.stream (§8)
	registerWsWasm(p, rt)     // enu.ws (§11)
	registerSearchWasm(p, rt) // enu.search (§11)
	registerProcWasm(p, rt)   // enu.proc (§6)
	registerUIWasm(p, rt)     // enu.ui (§9) — sólo si rt.ui != nil (G20)
	registerPluginWasm(p, rt) // enu.plugin (current/list) + enu.config (§14)
	registerDriverWasm(p)     // __driver_notify_quit: despertar del driver de TTY (G58, interno)
}

// currentOwner devuelve el nombre del plugin en cuyo contexto corre el código
// ahora mismo: el tope de `ownerStack` o "user" si está vacía (chunk de `-e`,
// `init.lua` del usuario, handlers sin plugin dueño). Es lo que `enu.log` anota en
// cada línea y lo que el watchdog reporta en `core:plugin.misbehaved`. Se lee
// siempre bajo el token (todo el que ejecuta Lua lo tiene), igual que se muta solo
// bajo el token en el arranque: sin carrera.
func (rt *Runtime) currentOwner() string {
	if n := len(rt.ownerStack); n > 0 {
		return rt.ownerStack[n-1].Name
	}
	return ownerUser
}

// ownerForInst resuelve el dueño con que corre una primitiva [W] atribuida por dueño
// (enu.log, enu.proc), dado el `inst` que la ejecuta (G56, ADR-024). Desde el estado
// principal es el dueño VIGENTE (currentOwner, tope del ownerStack), leído
// single-thread bajo el token. Desde un WORKER es la FOTO capturada en el spawn
// —inmutable durante toda la vida del worker—: JAMÁS se lee rt.ownerStack del padre
// desde la goroutine del worker. Eso hace la atribución DETERMINISTA (no depende de
// lo que el principal esté haciendo en ese instante) y elimina por diseño el data
// race de SEC-05 (dos hilos tocando la pila del padre). `fromWorker` deja que el
// llamador distinga el artefacto de atribución: enu.log anota `<plugin> (worker)`
// (quién y desde dónde), mientras enu.proc registra el proceso bajo el plugin dueño
// —el nombre CRUDO— para que `plugin.reload` lo alcance igual que a los del estado
// principal (árbol de supervisión sin fugas por la frontera del worker).
func (rt *Runtime) ownerForInst(inst *vmwasm.Instance) (owner string, fromWorker bool) {
	if photo, isWorker := inst.WorkerOwner(); isWorker {
		return photo, true
	}
	return rt.currentOwner(), false
}

// Boot ejecuta el **arranque canónico** del runtime (api.md §14, S11): descubre y
// carga los plugins de los directorios configurados en orden topológico por
// `requires`, ejecuta el `init.lua` del usuario el último y emite `core:ready` una
// sola vez. Devuelve un error de carga **accionable** (colisión de nombre, ciclo o
// dependencia ausente) si el grafo de plugins es inválido; en ese caso no se ejecutó
// ningún `init.lua`. Llamarlo más de una vez es no-op. `main` lo invoca antes de
// `EvalString`; un `enu -e` sin directorios de plugins arranca igual (solo corre el
// `init.lua` del usuario, si existe, y emite `core:ready`).
func (rt *Runtime) Boot() error {
	// Arma el timer de coalescing de `enu.ui` (§9.1, S29): ahora que el event loop
	// corre, una goroutine pinta el compositor como mucho cada ~30 ms si hay
	// cambios. En headless el pintado solo construye el buffer ANSI en memoria (no
	// hay TTY hasta S32); el timer se corta en `Close`.
	rt.armPainter()
	// Las extensiones oficiales se cargan sobre la Instance wasm (BootWasm hace la
	// discovery/topología y corre cada init.lua; ver vmwasm_loader.go).
	return rt.ldr.BootWasm()
}

// Close libera el estado Lua subyacente, corta los timers periódicos activos
// (sus goroutines de ticker, para no dejarlas colgadas) y cierra el fichero de
// log si llegó a abrirse.
func (rt *Runtime) Close() {
	if rt.sched != nil {
		// Mata los subprocesos vivos de `enu.proc.spawn` (S16): red de seguridad tras el
		// `cleanup` y el finalizer del GC. Cierra los `enu.http.stream` (S20), `enu.ws`
		// (S21), cancela los `enu.search.grep` (S27) y corta los `enu.fs.watch` (S15)
		// vivos: sus goroutines de fondo, conexiones y descriptores (fsnotify) no
		// deben sobrevivir al proceso. Sus objetos Go se rastrean en el scheduler
		// (las primitivas los registran al crearlos; ver proc.go/stream.go/ws.go/
		// search.go/vmwasm_fs.go). Los `enu.worker` (S34) viven en el Pool wasm y los
		// cierra su Close (abajo).
		rt.sched.stopAllProcs()
		rt.sched.stopAllStreams()
		rt.sched.stopAllWs()
		rt.sched.stopAllGreps()
		rt.sched.stopAllWatchers()
	}
	// Corta el timer de coalescing de `enu.ui` (S29): su goroutine de pintado no debe
	// sobrevivir al proceso.
	rt.stopPainter()
	// Borra el directorio temporal de la sesión (`enu.fs.tmpdir`, §5) si llegó a
	// crearse: el scratch no debe sobrevivir al proceso.
	if rt.fs != nil {
		rt.fs.closeTmpdir()
	}
	// Cierra las conexiones inactivas del cliente HTTP reutilizable (§8, S19), si
	// llegó a crearse: no deben sobrevivir al proceso.
	if rt.http != nil {
		rt.http.close()
	}
	// El log de un worker es el del PADRE (compartido, append-only con owner anotado,
	// newWorkerRuntime): cerrarlo aquí cerraría el fichero del padre. Solo el estado
	// principal cierra su log; el worker lo deja intacto al apagarse.
	if rt.log != nil && !rt.isWorker {
		_ = rt.log.close()
	}
	// Estado del backend wasm (M13d), si se construyó: cierra la Instance (su memoria
	// muere con el módulo) y el Pool (para sus workers y libera). El runtime wazero es
	// compartido a nivel de proceso: `Pool.Close` no lo cierra (vive lo que el proceso).
	if rt.wasm != nil {
		_ = rt.wasm.Close()
	}
	if rt.wasmPool != nil {
		rt.wasmPool.StopWorkers()
		_ = rt.wasmPool.Close()
	}
}
