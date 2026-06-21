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

	// `nu.log` (§15) y, de paso, el alias `print` = `nu.log.info`.
	registerLog(rt, nu)

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
