package runtime

import (
	lua "github.com/yuin/gopher-lua"
)

// Versión del runtime y nivel de la API del core (§2). `APILevel` se incrementa
// con cada adición a la superficie sagrada (api.md §17); arranca en 1 con la
// primera sesión que inyecta `nu`.
const (
	VersionMajor = 0
	VersionMinor = 1
	VersionPatch = 0
	APILevel     = 1
)

// caps construye el mapa que respalda `nu.has` PARA ESTE runtime (§2): detección de
// capacidades para extensiones portables. No es un mapa global porque algunas caps
// dependen del runtime concreto —en particular `"ui"`, TRUE solo si el módulo
// `nu.ui` se registró (hay TTY interactivo, o se forzó por test), y FALSE en headless
// (G20: sin TTY el módulo directamente no existe, y `nu.has("ui")` lo refleja, §9)—.
// Lo no listado es false (deny-by-default): una capacidad desconocida nunca se
// afirma. Crece por adición conforme las sesiones implementan cada superficie.
func (rt *Runtime) caps() map[string]bool {
	return map[string]bool{
		// `"ui"` sigue al gating de §9/G20: el módulo `nu.ui` existe ⇔ hay superficie
		// de UI concedida (TTY interactivo o `WithForceUI` en test). Es el mismo modelo
		// que las caps de los workers: "la superficie no concedida no está".
		"ui": rt.uiActive,
		// `"ui.images"` exige, además de UI, que el terminal negocie el protocolo de
		// imágenes (kitty/iTerm). Esa negociación es del driver de TTY (S33+); hasta
		// entonces se reporta false (deny-by-default), nunca true sin comprobarlo.
		"ui.images": false,
		// `net.tcp` es superficie reservada para el futuro (no v1, §8): false.
		"net.tcp": false,
	}
}

// registerNu construye la tabla global `nu` y cuelga de ella los submódulos
// disponibles en esta sesión: `version`, `has` y `log`.
func registerNu(rt *Runtime) {
	L := rt.L
	nu := L.NewTable()

	version := L.NewTable()
	version.RawSetString("major", lua.LNumber(VersionMajor))
	version.RawSetString("minor", lua.LNumber(VersionMinor))
	version.RawSetString("patch", lua.LNumber(VersionPatch))
	version.RawSetString("api", lua.LNumber(APILevel))
	nu.RawSetString("version", version)

	nu.RawSetString("has", L.NewFunction(rt.nuHas))

	// `nu.task` (§3): scheduler, `spawn`, `Task:await`, `Task:cancel`,
	// `nu.task.cleanup`... La quilla async.
	rt.sched.register(nu)

	// Desenrollado no capturable por `pcall` (§1.3, S08): envuelve los globales
	// `pcall`/`xpcall` (que `applySandbox` abrió nativos) para que un aborto de
	// task atraviese cualquier `pcall` de usuario. Debe ir DESPUÉS de que el
	// baselib esté abierto y ANTES de que corra código de usuario (cancel.go).
	rt.sched.installCancelPcall()

	// `nu.events` (§4, S10): bus de eventos `on`/`once`/`emit`. Despacho síncrono
	// sobre foto de suscriptores, emits anidados encolados por anchura (G10). Solo
	// estado principal (no [W]). Es donde el watchdog (S09) emite ya de verdad
	// `core:plugin.misbehaved` (rt.emitMisbehaved, cableado en runtime.go).
	rt.sched.registerEvents(nu)

	// `nu.fs` (§5, S14): IO de disco. Todas ⏸ (sobre el puente `suspend` de S04)
	// salvo `cwd` ([W], síncrona). Es el primer submódulo de IO real; su patrón de
	// "trabajo Go en la goroutine de fondo, datos a Lua en la deliverFn" lo reusan
	// S15 (watch) y S16 (proc). Registrado en el estado principal (los workers son
	// S34); `fs` es [W] salvo `watch` (§16), pero la API [W] se recorta con `caps`
	// en la Fase 7.
	rt.registerFs(nu)

	// `nu.proc` (§6, S16): subprocesos. `run` (buffers) y los IO de `Proc`
	// (`write`/`read*`/`wait`) son ⏸ sobre el puente `suspend` de S04 (mismo patrón
	// que `nu.fs`); `spawn`/`close_stdin`/`kill`/`alive` no suspenden. Vida del
	// proceso por `nu.task.cleanup` (S08) + red de seguridad del GC (finalizer) y de
	// `Runtime.Close` (mata los vivos). `proc` es [W] (§16): hoy en el estado
	// principal (los workers son S34).
	rt.registerProc(nu)

	// `nu.sys` (§7, S17): entorno y reloj. Wrappers finos sobre la stdlib
	// (`platform`/`now_ms`/`mono_ms`/`hostname`), **ninguno ⏸**. La única lógica
	// propia es el **overlay de `setenv`** (variables que afectan solo a
	// subprocesos futuros, sin mutar el entorno del proceso `nu`): `nu.proc`
	// (S16) lo aplica al construir el entorno del hijo. `sys` es [W] (§16): hoy
	// en el estado principal (los workers son S34).
	rt.registerSys(nu)

	// `nu.json`/`nu.toml`/`nu.yaml` (§12, S18): codecs de serialización. **Ninguno
	// ⏸** (CPU puro, sin IO que esperar) y todos [W] (§16; hoy en el estado
	// principal, los workers son S34). JSON es estricto con UTF-8 (G11) y usa el
	// sentinel `nu.json.NULL` para no perder claves null en el round-trip. TOML
	// reusa la librería del loader (S11); YAML añade yaml.v3 (puro-Go) para el
	// frontmatter de skills (§12).
	rt.registerCodecs(nu)

	// `nu.http` (§8, S19): red. `request` es ⏸ (sobre el puente `suspend` de S04,
	// mismo patrón que `nu.fs`/`nu.proc`): suelta el token y hace la petición HTTP
	// **bloqueante** en la goroutine de fondo, que jamás toca Lua. Respuesta
	// buffereada; **no lanza por status ≥ 400** (el status es dato), sí por fallo de
	// transporte (`ENET`) o timeout (`ETIMEOUT`). TLS y proxy (G12) por petición o
	// por defaults de `[net]` de `nu.toml`. `http` es [W] (§16): hoy en el estado
	// principal (los workers son S34). `stream` (S20) y `ws` (S21) llegan después.
	rt.registerHTTP(nu)

	// `nu.ws` (§8, S21): websockets. `connect`/`send`/`recv` son ⏸ (sobre el puente
	// `suspend` de S04, como `nu.http`): sueltan el token y hacen el handshake/IO
	// **bloqueante** en la goroutine de fondo, que jamás toca Lua. `recv` da `nil` al
	// cerrarse la conexión; un fallo de transporte lanza `ENET`. Cierra la Fase 4.
	rt.registerWs(nu)

	// `nu.text` (§10, S22): width/wrap/truncate. CPU puro (ninguna ⏸) y [W] (§16;
	// hoy en el estado principal, los workers son S34). `width` es la lógica 🔒
	// —anchura en celdas con graphemes/east-asian/emoji ZWJ (uniseg)—, base de todo
	// el layout; `wrap` produce un `Block` (block.go) y `truncate` recorta por
	// grapheme con elipsis. markdown/highlight/diff/re son S23–S26.
	rt.registerText(nu)

	// `nu.re` (§10, S26): expresiones regulares RE2 (`compile` + el handle `Re`
	// con `match`/`find_all`/`replace`). CPU puro (ninguna ⏸) y [W] (§16; hoy en
	// el estado principal, los workers son S34). Usa el `regexp` de la stdlib
	// (RE2, puro-Go): tiempo lineal garantizado, sin backreferences ni lookaround
	// (un patrón con `\1` → `EINVAL` claro). `match` da las capturas (array
	// 1-based + grupos con nombre), `find_all` rangos de byte 1-based estilo
	// `string.find`, y `replace` usa la sintaxis de `repl` de Go (`$1`/`${name}`).
	rt.registerRe(nu)

	// `nu.search` (§11, S27): búsqueda a escala de repo. `files` (recursivo,
	// respeta `.gitignore`) y `grep` (iterador paralelo) son ⏸ (sobre el puente
	// `suspend` de S04): sueltan el token y hacen el recorrido/casado en goroutines
	// de fondo que jamás tocan Lua. `grep` arranca un pool de goroutines que casan
	// el patrón (RE2, S26) e itera matches `{path, line_no, line, ranges}` según
	// llegan, con la vida atada a la task vía `nu.task.cleanup`. `fuzzy` es síncrono
	// (NO ⏸): es la primitiva caliente del picker, ordena por score de forma estable.
	// `search` es [W] (§16; hoy en el estado principal, los workers son S34). Cierra
	// la Fase 5.
	rt.registerSearch(nu)

	// `nu.ui` (§9, S22–S32): `block`/`caps`/`size`/`region` + el compositor, el input
	// y el portapapeles (clipboard OSC 52, S32). **GATING HEADLESS (G20, §9, S32):**
	// el módulo `nu.ui` se cuelga del global `nu` **solo si hay superficie de UI
	// concedida** (`rt.uiActive`: un TTY interactivo, o `WithForceUI` en test). Sin
	// TTY (`nu -e`, CI, salida redirigida) `nu.ui` directamente NO EXISTE —el mismo
	// modelo que las caps de los workers: "la superficie no concedida no está"; la
	// detección es `nu.has("ui")`, nunca probar-y-capturar—. Hasta S31 `nu.ui` se
	// colgaba SIEMPRE (NOTA DE FRONTERA del plan, para que S23–S31 inspeccionaran
	// Blocks); S32 cierra esa deuda: ahora el gating real aplica, y los tests de UI
	// fuerzan la activación con `WithForceUI(true)` (vía `newHarness`).
	if rt.uiActive {
		rt.registerUI(nu)
	}

	// `nu.log` (§15) y, de paso, el alias `print` = `nu.log.info`.
	registerLog(rt, nu)

	// `nu.plugin` y `nu.config` (§14, S11): `current`/`list` del loader y
	// `dir`/`data_dir` de la configuración. El arranque canónico (carga de plugins,
	// `init.lua` del usuario, `core:ready`) lo dispara `Boot` (loader.go), que
	// `main` invoca; aquí solo se cuelga la superficie de consulta.
	rt.registerPlugin(nu)

	// `nu.worker` (§13, S34): paralelismo opt-in. `nu.worker.spawn` levanta un estado
	// Lua NUEVO y aislado en su goroutine (mini-runtime SIN watchdog, G15), con la
	// superficie [W] recortada por `caps` (G6, deny-by-default, dos granularidades) y
	// comunicación por colas acotadas (backpressure). Es **solo estado principal**
	// (§16: sin workers anidados); dentro de un worker no existe `nu.worker.spawn`,
	// solo `nu.worker.parent` (lo cuelga `registerWorkerParent`, worker.go). Por eso
	// se registra aquí, en `registerNu` (el camino del estado principal), y NO en
	// `registerWorkerNu`.
	rt.registerWorker(nu)

	L.SetGlobal("nu", nu)
}

// nuHas implementa `nu.has(cap) -> boolean` (§2). Consulta las caps DE ESTE runtime
// (`rt.caps()`), que incluyen el gating de `nu.ui` (G20: `"ui"` es true ⇔ el módulo
// se registró). Una capacidad desconocida devuelve false: las extensiones preguntan
// por lo que necesitan y no asumen nada que el runtime no afirme (deny-by-default).
func (rt *Runtime) nuHas(L *lua.LState) int {
	cap := L.CheckString(1)
	L.Push(lua.LBool(rt.caps()[cap]))
	return 1
}
