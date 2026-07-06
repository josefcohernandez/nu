// Package runtime levanta el intérprete Lua del core de nu: construye el estado
// gopher-lua, aplica el baseline del sandbox (api.md §1.2), inyecta el global
// `nu` y expone la evaluación de código. Es la quilla sobre la que las sesiones
// posteriores cuelgan cada submódulo de la API (task, fs, http, ...).
package runtime

import (
	"path/filepath"
	"time"

	"github.com/dbareagimeno/nu/internal/vmwasm"
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

	// http es el estado de sesión de `nu.http` (§8, S19): la config de red
	// (`[net]` de `nu.toml`: CA corporativa y proxy por defecto, G12) y el cliente
	// HTTP **reutilizable** del caso común (sin overrides de TLS/proxy por
	// petición), con su pool de conexiones. Una petición que pide TLS/proxy
	// específico construye un cliente por-petición; el resto reusa este.
	http *httpState

	// jsonNull es el sentinel `nu.json.NULL` (§12, S18): un userdata único del
	// estado Lua que representa `null` de JSON sin colisionar con ningún valor Lua.
	// `decode` lo entrega en lugar de `nil` (que borraría la clave de una tabla,
	// rompiendo el round-trip) y `encode` lo reconoce por identidad para emitir
	// `null`. Se crea una sola vez en `registerCodecs` y nunca cambia.
	jsonNull *lua.LUserData

	// ui es el estado de sesión de `nu.ui` (§9.1, S29): el **compositor** (rejilla
	// de pantalla, regiones con z-order, diff→ANSI, coalescing). Vive en el estado
	// principal bajo el token (ADR-008: `nu.ui` es solo estado principal). Las
	// regiones que entrega son `ownedHandle` (S13): un `reload` las destruye con el
	// resto de los handles del plugin (G2). Inmutable tras `New` (el compositor sí
	// muta: regiones, tamaño, frames). **En headless es `nil`** (G20, S32): sin
	// superficie de UI no hay compositor; `armPainter`/`stopPainter`/`Close` ya lo
	// toleran (`rt.ui == nil`).
	ui *uiState

	// uiActive decide el GATING HEADLESS de `nu.ui` (G20, §9, S32): si es true, el
	// módulo `nu.ui` se registra en el global `nu`, el compositor se construye y
	// `nu.has("ui")` es true; si es false (headless: `nu -e`, CI, salida redirigida),
	// `nu.ui` NO EXISTE y `nu.has("ui")` es false. Lo fija `New`: por `WithForceUI`
	// (precedencia, lo usan los tests de UI) o, en su defecto, por la detección de un
	// TTY interactivo (`detectTTY`). Inmutable tras `New`.
	uiActive bool

	// isWorker marca que este Runtime es el de un **worker** (§13, S34), no el
	// estado principal. Un worker es un mini-runtime completo (scheduler propio,
	// tasks/timers/futures) pero SIN watchdog (G15), SIN `nu.ui`/`nu.events`/
	// `nu.fs.watch`/`nu.worker.spawn`/`nu.plugin` (§16) y con la superficie [W]
	// recortada por `caps` (G6). Lo construye `newWorkerRuntime` (worker_registry.go);
	// el estado principal lo deja en false. Inmutable tras la construcción.
	isWorker bool

	// vmBackend es el motor de VM de este Runtime (migracion-vm.md M04, DM2):
	// gopher-lua (actual) o wasm (PUC-Lua sobre wazero). Inmutable tras `New`. En
	// backend gopher, `L` es el estado real; en wasm, el estado Lua vive en
	// internal/vmwasm (lo que M05-M13 van portando). Se lee con `VMBackend()`.
	vmBackend VMBackend

	// wasmPool / wasm son el estado del backend wasm (migracion-vm.md M13d): el
	// Pool que compila el catálogo de primitivas nu.* portadas y la Instance —el
	// estado Lua aislado— sobre la que corren `EvalString`/`EvalTaskString` cuando
	// `vmBackend == VMWasm`. Ambos son nil en backend gopher (nunca se construyen).
	// Los construye `buildWasmState`, que `New` invoca sólo si el backend resuelto
	// es wasm; `Close` los libera. **Transición del estrangulador:** el `rt` gopher
	// (L, sched, fs, http, sys, ui, log) se construye igual —el catálogo wasm reusa
	// ese estado de sesión (rt.fs/rt.http/...)—; lo que ramifica por backend es dónde
	// corre el chunk (aquí, o en `L`).
	wasmPool *vmwasm.Pool
	wasm     *vmwasm.Instance
	// wasmErr guarda un fallo de construcción del estado wasm (`buildWasmState`).
	// La firma de `New` es sagrada (no devuelve error), así que un fallo del backend
	// wasm no puede propagarse ahí: se aparca aquí y lo devuelven `EvalString`/
	// `EvalTaskString` al primer intento de evaluar (como el `configErr` aplazado a
	// `Boot`). En backend gopher siempre es nil.
	wasmErr error

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

	// uiW, uiH fijan el tamaño inicial de la pantalla de `nu.ui` (§9.1) cuando no
	// hay un TTY del que leerlo (entorno headless de S29: la negociación con el
	// terminal real son S33+). Cero = sin Option: se resuelve por `COLUMNS`/`LINES`
	// del entorno o, en su defecto, 80×24 (default razonable). Los tests del
	// compositor inyectan un tamaño pequeño con `WithUISize` para forzar el recorte de
	// regiones fuera de pantalla (G1).
	uiW, uiH int

	// forceUI / forceUISet gobiernan el GATING HEADLESS de `nu.ui` (G20, §9, S32). Sin
	// `WithForceUI` (`forceUISet=false`), la activación de `nu.ui` la decide la
	// detección de un TTY interactivo (`detectTTY`): así `nu -e` o una salida
	// redirigida arrancan SIN `nu.ui`. Con `WithForceUI(v)` (`forceUISet=true`), `v`
	// manda y se salta la detección de TTY: es la vía de los TESTS, que corren
	// headless pero necesitan `nu.ui` para ejercitar el compositor/input/clipboard
	// (`newHarness` la activa). El gating REAL por TTY aplica al binario `nu`.
	forceUI    bool
	forceUISet bool

	// enabledOverride / enabledOverrideSet permiten fijar `plugins.enabled` EN MEMORIA,
	// sobrescribiendo lo que diga `nu.toml`, SIN tocar disco (ADR-015, G33). Es la vía del
	// modo EFÍMERO de `nu --default-config -p/-e`: activar el conjunto oficial de producto
	// solo para ese proceso (Docker/CI inmutable). Con `enabledOverrideSet=true`, el valor
	// de `enabledOverride` reemplaza a `nuCfg.Plugins.Enabled` tras leer `nu.toml`. Una lista
	// vacía explícita es válida (activa nada); distinta de "no fijada" (usa `nu.toml`).
	enabledOverride    []string
	enabledOverrideSet bool

	// vmBackend / vmBackendSet fijan el motor de VM EN MEMORIA (migracion-vm.md M04,
	// DM2), con la máxima precedencia (por encima de NU_VM y nu.toml): la vía de un
	// test que quiere forzar un backend concreto sin depender del entorno.
	vmBackend    VMBackend
	vmBackendSet bool
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

// WithUISize fija el tamaño inicial de la pantalla de `nu.ui` en celdas (§9.1).
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

// WithForceUI fuerza el estado del GATING HEADLESS de `nu.ui` (G20, §9, S32),
// saltándose la detección de TTY. `WithForceUI(true)` registra `nu.ui` y deja
// `nu.has("ui")` en true aunque no haya terminal; `WithForceUI(false)` lo desactiva
// aunque lo haya. Es la vía de los **tests**: corren headless (sin TTY) pero
// necesitan `nu.ui` para ejercitar el compositor/input/clipboard —el arnés
// (`newHarness`) la activa, y por eso los tests de S22–S31 siguen verdes pese a que
// ahora, sin esta Option, `nu.ui` no existiría en su entorno sin TTY—. En el binario
// `nu` real esta Option NO se pasa: el gating lo decide `detectTTY` (un `nu -e` o una
// salida redirigida arrancan sin `nu.ui`, como exige el "Criterio de hecho" de S32).
func WithForceUI(active bool) Option {
	return func(c *config) { c.forceUI = active; c.forceUISet = true }
}

// WithEnabledPlugins fija `plugins.enabled` EN MEMORIA, sobrescribiendo lo que diga
// `config.dir()/nu.toml`, SIN escribir nada a disco (ADR-015, G33). Es la vía del modo
// EFÍMERO de `nu --default-config` combinado con una acción headless (`-p`/`-e`): el
// binario activa el conjunto oficial de producto solo para ESE proceso, sin reescribir la
// config del usuario —el caso del contenedor inmutable—. El valor reemplaza por completo
// a la lista de `nu.toml` (no se fusiona): una lista vacía explícita activa nada. El resto
// de `nu.toml` (watchdog, dirs, net) se sigue respetando. La usan también los tests para
// inyectar un conjunto activo sin escribir un `nu.toml` temporal.
func WithEnabledPlugins(names []string) Option {
	return func(c *config) {
		c.enabledOverride = append([]string(nil), names...)
		c.enabledOverrideSet = true
	}
}

// WithVMBackend fija el motor de VM del Runtime EN MEMORIA (migracion-vm.md M04,
// DM2), con precedencia sobre la variable de entorno `NU_VM` y sobre
// `nu.toml [vm] backend`. Es la vía de los tests que ejercen un backend concreto
// de forma determinista. Sin esta Option, manda `NU_VM`; sin ella, `nu.toml`; sin
// nada, gopher (el default seguro hasta la conmutación de M16).
func WithVMBackend(b VMBackend) Option {
	return func(c *config) { c.vmBackend = b; c.vmBackendSet = true }
}

// VMBackend devuelve el motor de VM resuelto para este Runtime (M04, DM2).
func (rt *Runtime) VMBackend() VMBackend { return rt.vmBackend }

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

	// GATING HEADLESS de `nu.ui` (G20, §9, S32): decide si hay superficie de UI. La
	// Option `WithForceUI` manda (la usan los tests); sin ella, lo decide la detección
	// de un TTY interactivo (`detectTTY`). En headless (`nu -e`, CI, salida
	// redirigida) sale false: ni `nu.ui` ni su compositor se construyen, y
	// `nu.has("ui")` será false.
	uiActive := cfg.forceUI
	if !cfg.forceUISet {
		uiActive = detectTTY()
	}

	rt := &Runtime{
		L:        L,
		log:      newLogger(filepath.Join(cfg.dataDir, logFileName)),
		fs:       &fsState{},
		sys:      &sysState{},
		uiActive: uiActive,
		// Defaults de red de `[net]` (§8, G12, S19). Un `nu.toml` ausente o sin
		// `[net]` deja ambos vacíos (comportamiento estándar). No se aplica si el
		// `nu.toml` está mal formado (el error se aplaza a `Boot`).
		http: newHTTPState(nuCfg.Net.CAFile, nuCfg.Net.Proxy),
		// El compositor de `nu.ui` (§9.1, S29) **solo si hay UI** (G20, S32): en
		// headless `rt.ui` queda nil y no se gasta ni rejilla ni timer. Su tamaño sale
		// de la Option (tests), del entorno (`COLUMNS`/`LINES`) o del default 80×24. El
		// timer de coalescing (a lo sumo cada ~30 ms) se arma en `Boot`, cuando el event
		// loop ya corre; en `New` solo se construye el estado.
		ui: maybeUIState(uiActive, cfg.uiW, cfg.uiH),
	}
	rt.ldr = newLoader(rt, cfg.dataDir, cfg.configDir, pluginDirs)
	// El gating por `nu.toml` (qué se activa) y el error de config aplazado viven en
	// el loader, que es quien descubre y carga (S12). `Boot` consultará ambos.
	rt.ldr.enabled = nuCfg.Plugins.Enabled
	rt.ldr.configErr = tomlErr
	// `WithEnabledPlugins` (modo efímero de `nu --default-config`, ADR-015/G33) GANA sobre
	// `nu.toml`: fija la lista de activación en memoria sin tocar disco. Se aplica tras leer
	// `nu.toml` para que el resto de la config (watchdog, dirs, net) se respete y solo se
	// sustituya `enabled`. Un override con `nu.toml` mal formado NO limpia `configErr`: el
	// fichero roto sigue siendo un error de arranque que `Boot` reportará (no lo enmascaramos
	// silenciosamente por pasar `-e`/`-p`).
	if cfg.enabledOverrideSet {
		rt.ldr.enabled = cfg.enabledOverride
	}
	// Backend de VM (M04, DM2): precedencia WithVMBackend > NU_VM > nu.toml > gopher.
	// Hoy `New` construye siempre el estado gopher (arriba); el camino de arranque
	// wasm paralelo lo cablea M05-M13 conforme la superficie nu.* se porta. Registrar
	// el backend resuelto ya permite al arnés de tests y al CI ramificar (skip list).
	rt.vmBackend = resolveVMBackend(&cfg, nuCfg.VM.Backend)

	rt.sched = newScheduler(rt, cfg.sliceBudget)
	applySandbox(L)
	registerNu(rt)

	// Arranque del backend wasm (M13d): sólo si el backend resuelto es wasm. El `rt`
	// gopher de arriba se deja intacto (transición del estrangulador): el catálogo
	// wasm reusa su estado de sesión (rt.fs/rt.http/rt.sys/rt.ui/rt.log/rt.sched), y
	// lo que ramifica por backend es dónde corre el chunk (EvalString/EvalTaskString).
	// Un fallo se aparca en rt.wasmErr (la firma de New no devuelve error).
	if rt.vmBackend == VMWasm {
		rt.buildWasmState()
	}
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
	// El nivel de nu.version.api que el preludio inyecta (api.md §2): el mismo que
	// el estado gopher (APILevel). Debe fijarse antes de NewInstance.
	p.SetAPIVersion(APILevel)
	// Presupuesto del watchdog por slice (DM4): el mismo `sliceBudget` que rige el
	// estado gopher (rt.sched.budget, ya resuelto por precedencia Option>nu.toml>
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

// registerWasmCatalog cuelga en `p` el catálogo completo de primitivas nu.* ya
// portado a wasm (migracion-vm.md M13b/M13c), agrupado aquí para no ensuciar `New`.
// Cada `registerXWasm` es la contraparte del `registerX` gopher y reusa las mismas
// implementaciones Go VM-agnósticas del kernel; el estado de sesión que necesitan
// (rt.fs/rt.http/rt.sys/rt.log/rt.ui) llega por `rt`. `registerHTTPWasm` incluye
// `http.stream` y `registerTextWasm` incluye los Blocks de nu.text (se registran
// desde dentro). `registerUIWasm` instala el compositor SÓLO si hay UI concedida
// (`rt.ui != nil`, gating headless G20); en headless no registra nada.
func (rt *Runtime) registerWasmCatalog(p *vmwasm.Pool) {
	registerCodecsWasm(p)     // nu.json/toml/yaml (§12)
	registerReWasm(p)         // nu.re (§10)
	registerFsWasm(p, rt)     // nu.fs (§5)
	registerSysWasm(p, rt)    // nu.sys (§7)
	registerLogWasm(p, rt)    // nu.log (§15)
	registerTextWasm(p, rt)   // nu.text width/truncate + wrap/markdown/highlight/diff (§10)
	registerHTTPWasm(p, rt)   // nu.http.request + nu.http.stream (§8)
	registerWsWasm(p, rt)     // nu.ws (§11)
	registerSearchWasm(p, rt) // nu.search (§11)
	registerProcWasm(p, rt)   // nu.proc (§6)
	registerUIWasm(p, rt)     // nu.ui (§9) — sólo si rt.ui != nil (G20)
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
	// Arma el timer de coalescing de `nu.ui` (§9.1, S29): ahora que el event loop
	// corre, una goroutine pinta el compositor como mucho cada ~30 ms si hay
	// cambios. En headless el pintado solo construye el buffer ANSI en memoria (no
	// hay TTY hasta S32); el timer se corta en `Close`.
	rt.armPainter()
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
		// Cierra los `nu.http.stream` vivos (S20): sus goroutines de lectura del body
		// y sus conexiones no deben sobrevivir al proceso (red de seguridad, tras el
		// `cleanup` de quien abrió el stream).
		rt.sched.stopAllStreams()
		// Cierra los `nu.ws.connect` vivos (S21): sus conexiones y goroutines de IO no
		// deben sobrevivir al proceso (red de seguridad, tras el `cleanup` de quien abrió
		// el websocket).
		rt.sched.stopAllWs()
		// Cancela los `nu.search.grep` vivos (S27): sus pools de goroutines de fondo
		// (recorrido del árbol + casado del patrón) no deben sobrevivir al proceso (red
		// de seguridad, tras el `cleanup` de la task que consume el iterador).
		rt.sched.stopAllGreps()
		// Corta los `nu.worker.spawn` vivos (S34): cierra su `done`, lo que despierta
		// cualquier `send`/`recv` colgado y lleva la goroutine de cada worker a soltar su
		// propio estado Lua (la goroutine del worker es la dueña de su Lua: el padre
		// nunca lo toca). Red de seguridad tras el `terminate` de quien lo creó.
		rt.sched.stopAllWorkers()
	}
	// Corta el timer de coalescing de `nu.ui` (S29): su goroutine de pintado no debe
	// sobrevivir al proceso.
	rt.stopPainter()
	// Borra el directorio temporal de la sesión (`nu.fs.tmpdir`, §5) si llegó a
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
	rt.L.Close()
}
