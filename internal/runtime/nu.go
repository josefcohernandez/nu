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

// capabilities es el mapa que respalda `nu.has` (§2): detección de capacidades
// para extensiones portables. En esta sesión no hay ninguna superficie opcional
// activa todavía —no hay UI (headless), ni red TCP cruda (reservada, no v1)—,
// así que todas son false. Crece por adición conforme las sesiones implementan
// cada capacidad; lo no listado es false (deny-by-default).
var capabilities = map[string]bool{
	"ui":        false,
	"ui.images": false,
	"net.tcp":   false,
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

	nu.RawSetString("has", L.NewFunction(nuHas))

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

	// `nu.ui` (§9.2, S22): por ahora solo `block`/`caps` + el parseo de `Style` y la
	// metatabla del tipo opaco `Block`. El compositor (regiones/blit/input) es
	// S28–S31 y el gating headless (G20) es S32; en S22 `nu.ui` se cuelga SIEMPRE
	// (también headless) para que S23–S31 puedan construir e inspeccionar Blocks
	// (NOTA DE FRONTERA del plan). `nu.has("ui")` sigue en false hasta S32.
	rt.registerUI(nu)

	// `nu.log` (§15) y, de paso, el alias `print` = `nu.log.info`.
	registerLog(rt, nu)

	// `nu.plugin` y `nu.config` (§14, S11): `current`/`list` del loader y
	// `dir`/`data_dir` de la configuración. El arranque canónico (carga de plugins,
	// `init.lua` del usuario, `core:ready`) lo dispara `Boot` (loader.go), que
	// `main` invoca; aquí solo se cuelga la superficie de consulta.
	rt.registerPlugin(nu)

	L.SetGlobal("nu", nu)
}

// nuHas implementa `nu.has(cap) -> boolean` (§2). Una capacidad desconocida
// devuelve false: las extensiones preguntan por lo que necesitan y no asumen
// nada que el runtime no afirme.
func nuHas(L *lua.LState) int {
	cap := L.CheckString(1)
	L.Push(lua.LBool(capabilities[cap]))
	return 1
}
