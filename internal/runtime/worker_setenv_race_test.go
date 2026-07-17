package runtime

import (
	"fmt"
	"runtime"
	"testing"
)

// TestSpawnDesdeWorkerConcurrenteConSetenv (G56, ADR-024; SEC-05) blinda que la
// lectura de la **foto del overlay de `enu.sys.setenv`** que hace `spawnProc`
// (`rt.sys.envOverlay()`) es segura AUNQUE ocurra desde la goroutine de un WORKER.
//
// Tras G56 un `enu.proc.spawn` puede nacer DENTRO de un worker: su HostFn corre en
// la goroutine del worker, así que `spawnProc` —y con él la lectura del overlay—
// deja de correr siempre en el estado principal. Si esa lectura tocara el mapa
// `sysState.envOver` sin candado, competiría con un `enu.sys.setenv` del estado
// principal (que muta el mismo mapa desde la goroutine de la VM) → data race de la
// misma clase que SEC-05. La protección real NO es "correr en el estado principal"
// (falso desde G56), sino el candado `sysState.mu` que envuelve `envOverlay` y
// `setenv` (diseño de S17): el candado —no el token— serializa lector y escritor
// aunque vivan en goroutinas distintas.
//
// El test fuerza justo ese interleaving: el worker spawnéa procesos en bucle (cada
// spawn lee el overlay desde SU goroutine) mientras una task del estado principal
// machaca `enu.sys.setenv` en bucle (escribe el overlay desde la goroutine de la
// VM). Bajo `-race`, un acceso sin candado saltaría; en verde demuestra que el
// candado cubre la lectura del worker. Es el caso que los tests G56 vigentes NO
// ejercen: ninguno corre un `setenv` concurrente con el spawn.
func TestSpawnDesdeWorkerConcurrenteConSetenv(t *testing.T) {
	// Apuntamos a Unix: usamos el ejecutable `true`, que existe y sale al instante
	// (el proceso se crea y se reapea sin quedar vivo). La carrera que probamos —la
	// lectura del overlay en `spawnProc`— ocurre igualmente antes de `Start`.
	if runtime.GOOS == "windows" {
		t.Skip("usa el ejecutable Unix `true`")
	}

	const iter = 60
	h := g56Harness(t, `
		-- Spawn del worker DURANTE el init de p (su foto de dueño es "p"). El worker
		-- espera el número de iteraciones y lanza esos procesos, esperando cada uno:
		-- cada enu.proc.spawn lee la foto del overlay de setenv DESDE su goroutine.
		__WK = enu.worker.spawn([[
			local n = enu.worker.parent.recv()
			for i = 1, tonumber(n) do
				local p = enu.proc.spawn({"true"})
				p:wait()
			end
			enu.worker.parent.send("done")
		]])
	`)

	// Task del estado principal: arranca el worker y, EN PARALELO a los spawns del
	// worker, machaca enu.sys.setenv (escribe el overlay desde la goroutine de la VM).
	// El recv final sincroniza el fin de ambos bucles. Sin candado en el overlay, el
	// solapamiento lectura(worker)/escritura(principal) dispararía el race detector.
	h.eval(fmt.Sprintf(`
		OK = false
		enu.task.spawn(function()
			__WK:send("%d")
			for i = 1, %d do
				enu.sys.setenv("ENU_RACE_" .. i, "v" .. i)
			end
			local r = __WK:recv()   -- "done": ambos bucles han terminado
			__WK:terminate()
			OK = (r == "done")
		end)
	`, iter, iter))
	h.expectEval(`return tostring(OK)`, "true")
}
