package runtime

import (
	"os"
	"runtime"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// `nu.sys` — entorno y reloj (api.md §7, sesión S17). Wrappers finos sobre la
// stdlib de Go, **ninguno ⏸** (son consultas/registros inmediatos, sin IO que
// esperar) y todos [W] (§16: disponibles en workers; los workers son S34, así
// que hoy se registran en el estado principal):
//
//   - `platform() -> "linux"|"darwin"|"windows"` — `runtime.GOOS`.
//   - `env(name) -> string?` — valor de una variable de entorno; `nil` si no
//     existe. Lee el **overlay de `setenv`** por ENCIMA del entorno del SO.
//   - `setenv(name, value)` — registra una variable que afecta **solo a
//     subprocesos FUTUROS** (§7). NO muta el entorno del proceso `nu` actual
//     (nada de `os.Setenv`): mutar el entorno global del proceso es un efecto
//     compartido que rompería el aislamiento por tarea (ADR-008) y se vería
//     desde TODO el código. En su lugar se guarda en un **overlay** del Runtime
//     que `nu.proc.run`/`spawn` aplican al construir el entorno del hijo (S16).
//   - `now_ms() -> number` — reloj de pared en ms (`time.Now().UnixMilli`).
//   - `mono_ms() -> number` — reloj monotónico en ms desde un origen arbitrario
//     (para medir duraciones, inmune a saltos del reloj de pared).
//   - `hostname() -> string` — `os.Hostname`.
//   - `pid() -> integer` — `os.Getpid`, el pid del proceso `nu` actual (G32).
//     Junto a `hostname` forma la identidad del escritor de los locks de sesión
//     (sesiones.md §6); distinto de `nu.proc.alive(pid)`, que valida pids ajenos.
//
// EL OVERLAY DE `setenv` (la única lógica propia de S17, lo demás es glue). Es
// un mapa `name -> value` en `sysState`, protegido por un candado: `setenv` lo
// escribe desde el estado principal (bajo el token) PERO lo leen las goroutines
// de fondo de `nu.proc` (que construyen el entorno del hijo SIN el token, S16),
// así que el candado —no el token— es lo que evita la carrera de datos. La
// precedencia al lanzar un subproceso (ver `mergedEnv`, proc.go) es, de menor a
// mayor: entorno heredado del SO < overlay de `setenv` < `opts.env` de la
// llamada. Es decir, `setenv` pisa lo heredado (su razón de ser), pero un
// `opts.env` explícito por llamada gana sobre el overlay (lo más local manda:
// quien pasa `env` toma control de esa clave en ESA invocación).

// sysState es el estado de sesión de `nu.sys`: hoy, solo el overlay de variables
// de entorno que `setenv` acumula y que `nu.proc` aplica a los subprocesos. El
// candado protege el mapa frente a la carrera entre `setenv` (estado principal,
// bajo el token) y las goroutines de fondo de `nu.proc` que lo leen sin el token.
type sysState struct {
	mu      sync.Mutex
	envOver map[string]string // overlay de setenv; nil hasta el primer setenv
}

// envOverlay devuelve una **copia** del overlay de entorno (las claves que
// `setenv` registró), o nil si no hay ninguna. La copia desacopla al llamante
// (las goroutines de fondo de `nu.proc`) del mapa vivo: tras esta llamada puede
// leerla sin candado mientras otro `setenv` muta el original. Coste despreciable
// (el overlay típico tiene pocas entradas) frente a la simplicidad de no
// compartir el mapa vivo.
func (s *sysState) envOverlay() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.envOver) == 0 {
		return nil
	}
	cp := make(map[string]string, len(s.envOver))
	for k, v := range s.envOver {
		cp[k] = v
	}
	return cp
}

// envLookup resuelve una variable consultando el overlay POR ENCIMA del entorno
// del SO (§7: `env` lee lo que `setenv` registró, no solo lo heredado). Devuelve
// `(valor, true)` si existe en cualquiera de los dos; el overlay tiene
// prioridad. Lo usa `nu.sys.env`.
func (s *sysState) envLookup(name string) (string, bool) {
	s.mu.Lock()
	if s.envOver != nil {
		if v, ok := s.envOver[name]; ok {
			s.mu.Unlock()
			return v, true
		}
	}
	s.mu.Unlock()
	return os.LookupEnv(name)
}

// setenv registra `name=value` en el overlay, creándolo perezosamente. Solo lo
// llama `nu.sys.setenv`, en el estado principal bajo el token; el candado lo
// serializa frente a las lecturas de fondo de `nu.proc`.
func (s *sysState) setenv(name, value string) {
	s.mu.Lock()
	if s.envOver == nil {
		s.envOver = make(map[string]string)
	}
	s.envOver[name] = value
	s.mu.Unlock()
}

// registerSys cuelga `nu.sys` del global `nu` con sus firmas de §7. Lo llama
// `registerNu` (nu.go). Como el resto de la API de sistema, `sys` es [W] (§16),
// hoy registrado en el estado principal (los workers son S34).
func (rt *Runtime) registerSys(nu *lua.LTable) {
	L := rt.L
	sys := L.NewTable()
	sys.RawSetString("platform", L.NewFunction(rt.sysPlatform))
	sys.RawSetString("env", L.NewFunction(rt.sysEnv))
	sys.RawSetString("setenv", L.NewFunction(rt.sysSetenv))
	sys.RawSetString("now_ms", L.NewFunction(rt.sysNowMs))
	sys.RawSetString("mono_ms", L.NewFunction(rt.sysMonoMs))
	sys.RawSetString("hostname", L.NewFunction(rt.sysHostname))
	sys.RawSetString("pid", L.NewFunction(rt.sysPid))
	nu.RawSetString("sys", sys)
}

// sysPlatform implementa `nu.sys.platform() -> string` (§7): el SO en que corre,
// de `runtime.GOOS`. Para los SO soportados devuelve "linux"/"darwin"/"windows";
// en cualquier otro, el `GOOS` literal (no se inventa un valor: el contrato
// enumera los tres comunes, pero el dato crudo es más honesto que mentir).
func (rt *Runtime) sysPlatform(L *lua.LState) int {
	L.Push(lua.LString(runtime.GOOS))
	return 1
}

// sysEnv implementa `nu.sys.env(name) -> string?` (§7): el valor de la variable
// `name`, leyendo el overlay de `setenv` por encima del entorno del SO; `nil` si
// no existe en ninguno.
func (rt *Runtime) sysEnv(L *lua.LState) int {
	name := L.CheckString(1)
	if v, ok := rt.sys.envLookup(name); ok {
		L.Push(lua.LString(v))
	} else {
		L.Push(lua.LNil)
	}
	return 1
}

// sysSetenv implementa `nu.sys.setenv(name, value)` (§7): registra una variable
// que afecta **solo a subprocesos futuros**. NO toca `os.Setenv` —el entorno del
// proceso `nu` actual no cambia (un subproceso lanzado sin heredar el overlay no
// la ve, y `os.Getenv` en Go sigue sin verla)—; se guarda en el overlay del
// Runtime que `nu.proc` aplica al lanzar.
func (rt *Runtime) sysSetenv(L *lua.LState) int {
	name := L.CheckString(1)
	value := L.CheckString(2)
	rt.sys.setenv(name, value)
	return 0
}

// sysNowMs implementa `nu.sys.now_ms() -> number` (§7): reloj de **pared** en
// milisegundos desde el epoch Unix. Puede saltar hacia atrás (ajustes de hora,
// NTP); para medir duraciones úsese `mono_ms`. Es un `number` (float Lua): los
// ms del epoch caben de sobra en la mantisa de un float64 durante siglos.
func (rt *Runtime) sysNowMs(L *lua.LState) int {
	L.Push(lua.LNumber(time.Now().UnixMilli()))
	return 1
}

// monoOrigin es el instante de referencia del reloj monotónico: se fija al
// cargar el paquete. `mono_ms` devuelve los ms transcurridos desde él. El valor
// absoluto no significa nada (origen arbitrario, §7); solo las DIFERENCIAS entre
// dos lecturas son la duración real.
var monoOrigin = time.Now()

// sysMonoMs implementa `nu.sys.mono_ms() -> number` (§7): reloj **monotónico**
// en ms desde un origen arbitrario. Inmune a saltos del reloj de pared: dos
// lecturas siempre crecen (o se mantienen), así que su diferencia es una
// duración fiable. Se apoya en el reloj monotónico que Go embebe en `time.Time`
// (`time.Since` lo usa), no en `now_ms`.
func (rt *Runtime) sysMonoMs(L *lua.LState) int {
	L.Push(lua.LNumber(time.Since(monoOrigin).Milliseconds()))
	return 1
}

// sysHostname implementa `nu.sys.hostname() -> string` (§7): el nombre de la
// máquina (`os.Hostname`). Es el contenido de los locks de sesión (sesiones.md
// §6, junto al pid de `nu.proc.alive`, G17). Un fallo del SO al consultarlo se
// rinde como `EIO` —raro, pero no se inventa un nombre—.
func (rt *Runtime) sysHostname(L *lua.LState) int {
	name, err := os.Hostname()
	if err != nil {
		raiseError(L, CodeEIO, "nu.sys.hostname: "+err.Error(), lua.LNil)
		return 0
	}
	L.Push(lua.LString(name))
	return 1
}

// sysPid implementa `nu.sys.pid() -> integer` (§7, G32): el pid del proceso `nu`
// actual (`os.Getpid`). No ⏸ (consulta local inmediata, como `hostname`) y [W]
// (§16: hereda de que `sys` es módulo [W] entero). Es el `pid` que la extensión
// sesiones graba en el lock `{ pid, hostname, started }` (sesiones.md §6) para
// que otro proceso pueda comprobar con `nu.proc.alive` si el escritor sigue vivo.
// Lo que `nu.proc.alive(pid)` valida es un pid AJENO (existencia, no identidad);
// `pid()` es el camino para conocer el PROPIO, que `nu.proc` —gestor de hijos— no
// daba. Se devuelve como integer Lua: un pid cabe de sobra en el rango entero.
func (rt *Runtime) sysPid(L *lua.LState) int {
	L.Push(lua.LNumber(os.Getpid()))
	return 1
}
