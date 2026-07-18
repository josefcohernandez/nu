package runtime

import (
	"os"
	"sync"
	"time"
)

// `enu.sys` — entorno y reloj (api.md §7, sesión S17). Wrappers finos sobre la
// stdlib de Go, **ninguno ⏸** (son consultas/registros inmediatos, sin IO que
// esperar) y todos [W] (§16: disponibles en workers; los workers son S34, así
// que hoy se registran en el estado principal):
//
//   - `platform() -> "linux"|"darwin"|"windows"` — `runtime.GOOS`.
//   - `env(name) -> string?` — valor de una variable de entorno; `nil` si no
//     existe. Lee el **overlay de `setenv`** por ENCIMA del entorno del SO.
//   - `setenv(name, value)` — registra una variable que afecta **solo a
//     subprocesos FUTUROS** (§7). NO muta el entorno del proceso `enu` actual
//     (nada de `os.Setenv`): mutar el entorno global del proceso es un efecto
//     compartido que rompería el aislamiento por tarea (ADR-008) y se vería
//     desde TODO el código. En su lugar se guarda en un **overlay** del Runtime
//     que `enu.proc.run`/`spawn` aplican al construir el entorno del hijo (S16).
//   - `now_ms() -> number` — reloj de pared en ms (`time.Now().UnixMilli`).
//   - `mono_ms() -> number` — reloj monotónico en ms desde un origen arbitrario
//     (para medir duraciones, inmune a saltos del reloj de pared).
//   - `hostname() -> string` — `os.Hostname`.
//   - `pid() -> integer` — `os.Getpid`, el pid del proceso `enu` actual (G32).
//     Junto a `hostname` forma la identidad del escritor de los locks de sesión
//     (sesiones.md §6); distinto de `enu.proc.alive(pid)`, que valida pids ajenos.
//
// EL OVERLAY DE `setenv` (la única lógica propia de S17, lo demás es glue). Es
// un mapa `name -> value` en `sysState`, protegido por un candado: `setenv` lo
// escribe desde el estado principal (bajo el token) PERO lo leen las goroutines
// de fondo de `enu.proc` (que construyen el entorno del hijo SIN el token, S16),
// así que el candado —no el token— es lo que evita la carrera de datos. La
// precedencia al lanzar un subproceso (ver `mergedEnv`, proc.go) es, de menor a
// mayor: entorno heredado del SO < overlay de `setenv` < `opts.env` de la
// llamada. Es decir, `setenv` pisa lo heredado (su razón de ser), pero un
// `opts.env` explícito por llamada gana sobre el overlay (lo más local manda:
// quien pasa `env` toma control de esa clave en ESA invocación).

// sysState es el estado de sesión de `enu.sys`: hoy, solo el overlay de variables
// de entorno que `setenv` acumula y que `enu.proc` aplica a los subprocesos. El
// candado protege el mapa frente a la carrera entre `setenv` (estado principal,
// bajo el token) y las goroutines de fondo de `enu.proc` que lo leen sin el token.
type sysState struct {
	mu      sync.Mutex
	envOver map[string]string // overlay de setenv; nil hasta el primer setenv
}

// envOverlay devuelve una **copia** del overlay de entorno (las claves que
// `setenv` registró), o nil si no hay ninguna. La copia desacopla al llamante
// (las goroutines de fondo de `enu.proc`) del mapa vivo: tras esta llamada puede
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
// prioridad. Lo usa `enu.sys.env`.
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
// llama `enu.sys.setenv`, en el estado principal bajo el token; el candado lo
// serializa frente a las lecturas de fondo de `enu.proc`.
func (s *sysState) setenv(name, value string) {
	s.mu.Lock()
	if s.envOver == nil {
		s.envOver = make(map[string]string)
	}
	s.envOver[name] = value
	s.mu.Unlock()
}

// monoOrigin es el instante de referencia del reloj monotónico: se fija al
// cargar el paquete. `mono_ms` devuelve los ms transcurridos desde él. El valor
// absoluto no significa nada (origen arbitrario, §7); solo las DIFERENCIAS entre
// dos lecturas son la duración real.
var monoOrigin = time.Now()
