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
