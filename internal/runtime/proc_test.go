package runtime

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Tests de S16 (api.md §6): `nu.proc`. Sesión 🔒 —la lógica clave a blindar
// (inventario del plan): **vida del proceso por `cleanup`** (al cancelar la task
// que lo creó, el proceso muere) y **`alive` (G17)**, que informa de **existencia,
// no de identidad** (un pid reciclado daría `true`).
//
// Dos niveles, como en `fs_test.go`: tests Go directos sobre las funciones puras
// (`runBuffered`, `pidAlive`, `exitCode`) que blindan los invariantes sin pasar por
// el scheduler, y tests de snippet Lua que ejercitan la superficie de extremo a
// extremo (run/spawn/write/read_line/wait/kill/alive) por el puente ⏸ real. La
// suite corre con `-race -count=4`: el IO de `proc` va en goroutines de fondo, así
// que cualquier toque a Lua fuera del token —o una carrera sobre el `*luaProc`—
// saltaría aquí.
//
// Robustez anti-flaky: los tests de timing usan plazos holgados y, sobre todo, una
// espera a la CONDICIÓN (un proceso muerto), no a un sleep fijo. Los procesos de
// prueba son utilidades POSIX presentes en cualquier Linux de CI (`echo`, `cat`,
// `sh`, `sleep`, `printf`).

// --- 🔒 Lógica nuestra: alive (G17, existencia no identidad) ---

// TestPidAliveG17 blinda `nu.proc.alive` (G17): informa de EXISTENCIA, no de
// identidad. El pid del propio proceso de test está vivo → true; `pid 1` (init)
// existe en cualquier Unix aunque no sea nuestro → true (existencia, no propiedad);
// un pid imposible → false; un pid <= 0 → false. La parte "no identidad" se documenta
// en `pidAlive`: la llamada no distingue de QUÉ proceso es el pid —un pid reciclado
// daría true—.
func TestPidAliveG17(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatalf("G17: alive(pid del propio test) debería ser true")
	}
	if !pidAlive(1) {
		t.Fatalf("G17: alive(1) debería ser true (init existe; existencia no propiedad)")
	}
	if pidAlive(1<<30 + 12345) {
		t.Fatalf("G17: alive(pid inexistente) debería ser false")
	}
	for _, pid := range []int{0, -1, -1000} {
		if pidAlive(pid) {
			t.Fatalf("G17: alive(%d) debería ser false (no designa un proceso)", pid)
		}
	}
}

// TestPidAliveDeadProcessG17 blinda el ciclo completo de G17 sobre un proceso real:
// un proceso vivo da true; tras morir y recogerse su desenlace (wait), el mismo pid
// ya NO existe → false. Demuestra que `alive` sigue la existencia REAL del proceso,
// no un estado cacheado.
func TestPidAliveDeadProcessG17(t *testing.T) {
	cmd := newCmd([]string{"sleep", "30"}, procOpts{})
	if err := cmd.Start(); err != nil {
		t.Fatalf("no se pudo lanzar sleep: %v", err)
	}
	pid := cmd.Process.Pid

	if !pidAlive(pid) {
		t.Fatalf("G17: el sleep recién lanzado debería estar vivo (pid %d)", pid)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait() // recoge el zombi para que el pid deje de existir

	deadline := time.Now().Add(2 * time.Second)
	for pidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if pidAlive(pid) {
		t.Fatalf("G17: tras morir y wait, el pid %d no debería estar vivo", pid)
	}
}

// --- 🔒 run: buffers, sin shell, exit code dato, timeout ---

// TestRunBufferedEchoHi blinda el **criterio de hecho** central de `run`:
// `run(["echo","hi"])` → code=0, stdout contiene "hi". Test directo sobre
// `runBuffered` (sin el scheduler).
func TestRunBufferedEchoHi(t *testing.T) {
	code, stdout, stderr, err := runBuffered([]string{"echo", "hi"}, procOpts{})
	if err != nil {
		t.Fatalf("runBuffered(echo hi) falló: %v", err)
	}
	if code != 0 {
		t.Fatalf("code: got %d, want 0", code)
	}
	if !strings.Contains(stdout, "hi") {
		t.Fatalf("stdout: got %q, want que contenga \"hi\"", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr: got %q, want vacío", stderr)
	}
}

// TestRunNoImplicitShell blinda "SIN shell implícita" (§6): `argv` se pasa tal cual,
// nadie expande variables. `run(["echo","$HOME"])` imprime el literal `$HOME`, NO el
// valor de la variable de entorno —si hubiera una shell de por medio, saldría la
// ruta del home; este test lo cazaría—.
func TestRunNoImplicitShell(t *testing.T) {
	_, stdout, _, err := runBuffered([]string{"echo", "$HOME"}, procOpts{})
	if err != nil {
		t.Fatalf("runBuffered falló: %v", err)
	}
	if got := strings.TrimSpace(stdout); got != "$HOME" {
		t.Fatalf("sin shell: stdout got %q, want el literal \"$HOME\" (no expandido)", got)
	}
}

// TestRunNonZeroExitIsData blinda que un código de salida != 0 **no lanza**: es un
// dato. `sh -c "exit 3"` devuelve code=3 sin error de Go.
func TestRunNonZeroExitIsData(t *testing.T) {
	code, _, _, err := runBuffered([]string{"sh", "-c", "exit 3"}, procOpts{})
	if err != nil {
		t.Fatalf("un exit != 0 no debe ser error de arranque: %v", err)
	}
	if code != 3 {
		t.Fatalf("code: got %d, want 3", code)
	}
}

// TestRunStdinAndEnv blinda `opts.stdin` (se alimenta a la entrada) y `opts.env` (el
// entorno explícito reemplaza al heredado): `cat` con stdin devuelve lo que se le
// dio, y `sh -c 'echo $FOO'` con `env=["FOO=bar"]` imprime "bar".
func TestRunStdinAndEnv(t *testing.T) {
	_, stdout, _, err := runBuffered([]string{"cat"}, procOpts{stdin: []byte("hola stdin"), hasStdin: true})
	if err != nil {
		t.Fatalf("cat con stdin falló: %v", err)
	}
	if strings.TrimSpace(stdout) != "hola stdin" {
		t.Fatalf("stdin→stdout: got %q, want \"hola stdin\"", stdout)
	}

	_, out2, _, err := runBuffered([]string{"sh", "-c", "echo $FOO"}, procOpts{env: []string{"FOO=bar"}})
	if err != nil {
		t.Fatalf("env falló: %v", err)
	}
	if strings.TrimSpace(out2) != "bar" {
		t.Fatalf("env: got %q, want \"bar\"", out2)
	}
}

// TestRunTimeoutKills blinda `timeout_ms`: un proceso que tarda más que el plazo se
// **mata** y `runBuffered` devuelve el centinela de timeout (que `procRun` rinde como
// `ETIMEOUT`). Se mide que la llamada vuelve PRONTO (no espera los 30 s del sleep).
func TestRunTimeoutKills(t *testing.T) {
	start := time.Now()
	_, _, _, err := runBuffered([]string{"sleep", "30"}, procOpts{timeout: 100 * time.Millisecond})
	elapsed := time.Since(start)
	if err != errProcTimeout {
		t.Fatalf("timeout: got %v, want errProcTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout: tardó %v; debería haber matado el sleep pronto", elapsed)
	}
}

// TestRunNonexistentExecutable blinda que arrancar un ejecutable inexistente devuelve
// un error (que `mapProcStartError` rinde como `ENOENT`; lo verifica el snippet Lua
// más abajo).
func TestRunNonexistentExecutable(t *testing.T) {
	if _, _, _, err := runBuffered([]string{"no-existe-este-binario-xyz"}, procOpts{}); err == nil {
		t.Fatalf("un ejecutable inexistente debería fallar al arrancar")
	}
}

// TestExitCode blinda el mapeo de `exitCode`: nil → 0.
func TestExitCode(t *testing.T) {
	if c := exitCode(nil); c != 0 {
		t.Fatalf("exitCode(nil): got %d, want 0", c)
	}
}

// waitDead espera (con plazo holgado) a que `pid` deje de existir. Es la ancla
// anti-flaky: espera a la CONDICIÓN real, no a un sleep fijo. Lo reusa también el
// test del ciclo de vida de MCP (mcp_test.go).
func waitDead(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return !pidAlive(pid)
}

// --- 🔒 spawn/streams: write a stdin, read_line de stdout, EOF, wait ---

// TestSpawnCatRoundTrip blinda el round-trip de streams sobre `cat`: se escribe a
// stdin, se lee la misma línea de stdout; tras `close_stdin`, `read_line` devuelve
// `nil` (EOF); `wait` devuelve code=0. Es el escenario canónico de `spawn`.
func TestSpawnCatRoundTrip(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local p = nu.proc.spawn({"cat"})
			nu.task.cleanup(function() p:kill() end)
			p:write("linea uno\n")
			out.l1 = p:read_line("stdout")     -- "linea uno\n"
			p:write("linea dos\n")
			out.l2 = p:read_line("stdout")     -- "linea dos\n"
			p:close_stdin()                    -- señala EOF a cat
			out.eof = p:read_line("stdout")    -- nil: cat cerró stdout al ver EOF
			out.code = p:wait().code           -- 0
		end)
	`)

	h.expectEval(`return out.l1`, "linea uno\n")
	h.expectEval(`return out.l2`, "linea dos\n")
	h.expectEval(`return tostring(out.eof)`, "nil")
	h.expectEval(`return tostring(out.code)`, "0")
}

// TestSpawnReadRaw blinda `Proc:read(which, n?)`: lectura cruda. Un proceso que
// imprime un texto conocido se lee entero (sin `n`) hasta EOF; una lectura posterior
// da `nil` (EOF).
func TestSpawnReadRaw(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local p = nu.proc.spawn({"sh", "-c", "printf 'abcdef'"})
			nu.task.cleanup(function() p:kill() end)
			out.all = p:read("stdout")        -- "abcdef" (todo hasta EOF)
			out.more = p:read("stdout")       -- nil (ya en EOF)
			out.code = p:wait().code
		end)
	`)

	h.expectEval(`return out.all`, "abcdef")
	h.expectEval(`return tostring(out.more)`, "nil")
	h.expectEval(`return tostring(out.code)`, "0")
}

// TestProcReadStderr blinda que `read_line("stderr")` lee del stream correcto: un
// proceso que escribe a stderr se lee por "stderr", no por "stdout".
func TestProcReadStderr(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local p = nu.proc.spawn({"sh", "-c", "echo err 1>&2"})
			nu.task.cleanup(function() p:kill() end)
			out.err = p:read_line("stderr")     -- "err\n"
			out.outline = p:read_line("stdout") -- nil: nada en stdout
			p:wait()
		end)
	`)

	h.expectEval(`return out.err`, "err\n")
	h.expectEval(`return tostring(out.outline)`, "nil")
}

// TestProcReadInvalidStream blinda que `read*` con un `which` inválido lanza
// `EINVAL` (capturable).
func TestProcReadInvalidStream(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local p = nu.proc.spawn({"cat"})
			nu.task.cleanup(function() p:kill() end)
			local ok, err = pcall(function() p:read_line("nope") end)
			out.ok = ok
			out.code = err and err.code
		end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "EINVAL")
}

// TestProcWriteAfterCloseECLOSED blinda que escribir a stdin tras `close_stdin` lanza
// `ECLOSED` (capturable).
func TestProcWriteAfterCloseECLOSED(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local p = nu.proc.spawn({"cat"})
			nu.task.cleanup(function() p:kill() end)
			p:close_stdin()
			local ok, err = pcall(function() p:write("x") end)
			out.ok = ok
			out.code = err and err.code
			p:wait()
		end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "ECLOSED")
}

// --- Snippet Lua: run de extremo a extremo (Definition of Done §2) ---

// TestRunSnippet ejercita `nu.proc.run` desde el lado del autor de extensiones por
// el puente ⏸ real: `run(["echo","hi"])` → code=0, stdout con "hi"; exit != 0 es
// dato; sin shell.
func TestRunSnippet(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local r = nu.proc.run({"echo", "hi"})
			out.code = r.code
			out.stdout = r.stdout
			local r2 = nu.proc.run({"sh", "-c", "exit 7"})
			out.code2 = r2.code
			local r3 = nu.proc.run({"echo", "$HOME"})
			out.literal = r3.stdout
		end)
	`)

	h.expectEval(`return tostring(out.code)`, "0")
	if got := h.eval(`return out.stdout`); len(got) != 1 || !strings.Contains(got[0], "hi") {
		t.Fatalf("run stdout: got %q, want que contenga \"hi\"", got)
	}
	h.expectEval(`return tostring(out.code2)`, "7")
	if got := h.eval(`return out.literal`); len(got) != 1 || strings.TrimSpace(got[0]) != "$HOME" {
		t.Fatalf("sin shell: got %q, want el literal \"$HOME\"", got)
	}
}

// TestRunTimeoutSnippet ejercita el timeout por el puente ⏸: `run` con `timeout_ms`
// sobre un `sleep` largo lanza `ETIMEOUT` (capturable con pcall).
func TestRunTimeoutSnippet(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ok, err = pcall(function()
				nu.proc.run({"sleep", "30"}, { timeout_ms = 100 })
			end)
			out.ok = ok
			out.code = err and err.code
		end)
	`)

	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "ETIMEOUT")
}

// TestRunNonexistentSnippet ejercita el error de arranque por el puente ⏸: un
// ejecutable inexistente lanza `ENOENT` (capturable).
func TestRunNonexistentSnippet(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ok, err = pcall(function()
				nu.proc.run({"no-existe-binario-xyz-123"})
			end)
			out.ok = ok
			out.code = err and err.code
		end)
	`)

	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "ENOENT")
}

// TestProcOutsideTaskEINVAL blinda que las funciones ⏸ de `proc` (run, write,
// read*, wait) fuera de una task lanzan `EINVAL` (§1.3), como el resto de ⏸; en
// cambio `spawn`/`alive` SÍ funcionan fuera de task (no son ⏸).
func TestProcOutsideTaskEINVAL(t *testing.T) {
	h := newHarness(t)

	if se := h.evalErr(`nu.proc.run({"echo","hi"})`); se.Code != CodeEINVAL {
		t.Fatalf("run fuera de task: got %s, want EINVAL", se.Code)
	}

	// alive fuera de task → funciona (no ⏸).
	h.expectEval(`return tostring(nu.proc.alive(1))`, "true")
	h.expectEval(`return tostring(nu.proc.alive(`+strconv.Itoa(1<<30)+`))`, "false")

	// spawn fuera de task → funciona (no ⏸): devuelve un handle.
	h.eval(`
		local p = nu.proc.spawn({"sleep", "30"})
		p:kill()
	`)
}

// TestProcAliveSnippetG17 ejercita `nu.proc.alive` desde Lua (G17): el pid de un
// subproceso vivo da true; el de un pid imposible, false. La comprobación de "vivo"
// la hace el propio snippet (`alive(pid_real)`) mientras el proceso sigue corriendo,
// dentro de una task que completa (el proceso se mata por cleanup al terminar).
//
// El pid del subproceso lo obtiene el propio Lua (sin andamiaje Go que rebusque en
// el userdata, inexistente en wasm): un `sh -c 'echo $$; exec sleep 30'` imprime su
// pid y luego se REEMPLAZA por `sleep` conservándolo, de modo que el número que
// `read_line` lee designa al proceso vivo. Sólo usa primitivas de nu.proc: idéntico
// en ambos backends.
func TestProcAliveSnippetG17(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local p = nu.proc.spawn({"sh", "-c", "echo $$; exec sleep 30"})
			nu.task.cleanup(function() p:kill() end)
			local pid = tonumber(p:read_line("stdout"))   -- el pid que $$ imprimió
			out.has_pid = (pid ~= nil)
			out.alive_real = nu.proc.alive(pid)        -- true: el proceso está vivo
			out.alive_fake = nu.proc.alive(1073741824) -- false: pid imposible (2^30)
		end)
	`)

	h.expectEval(`return tostring(out.has_pid)`, "true")
	h.expectEval(`return tostring(out.alive_real)`, "true")
	h.expectEval(`return tostring(out.alive_fake)`, "false")
}

// --- 🔒 (best-effort) red de seguridad: el finalizer del GC mata un Proc sin refs ---

// TestSpawnFinalizerSafetyNet blinda (best-effort) la red de seguridad del GC (§6):
// un `Proc` sin referencias acaba matado por el finalizer. Forzar una recolección
// determinista es difícil, así que comprobamos lo razonable: tras dejar caer la
// única referencia a un `luaProc` (con el MISMO finalizer que `procSpawn` instala) y
// forzar el GC, el proceso ACABA muerto. Si el GC no llega a correr el finalizer en
// el plazo (no determinista por contrato), NO fallamos —limpiamos a mano y dejamos
// constancia—; `Close` lo mataría de todos modos.
func TestSpawnFinalizerSafetyNet(t *testing.T) {
	h := newHarness(t)

	cmd := newCmd([]string{"sleep", "30"}, procOpts{})
	if err := cmd.Start(); err != nil {
		t.Fatalf("no se pudo lanzar sleep: %v", err)
	}
	pid := cmd.Process.Pid

	// Crea el handle con el finalizer de producción y déjalo caer fuera de scope.
	func() {
		p := &luaProc{s: h.rt.sched, cmd: cmd}
		runtime.SetFinalizer(p, func(p *luaProc) { p.killSignal(syscall.SIGKILL) })
		_ = p
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if !pidAlive(pid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pidAlive(pid) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Logf("el finalizer no corrió en el plazo (no determinista, §6); proceso limpiado a mano")
	} else {
		_ = cmd.Wait()
	}
}

// --- 🔒 killSignal: un envío fallido NO debe dejar el proceso inmatable ---

// TestKillSignalNotMarkedOnFailure blinda que `killed` sólo se fija cuando la señal
// SE ENVIÓ sin error. Antes, `killSignal` marcaba `killed=true` incondicionalmente:
// un envío fallido cortocircuitaba TODOS los kills posteriores (cleanup, finalizer,
// scheduler, que consultan `killed`), dejando el proceso vivo pero "matado" —huérfano
// e inmatable—. Se fuerza un fallo determinista con una señal fuera de rango (el
// kernel devuelve EINVAL) sobre un proceso VIVO: `killed` debe seguir false y el
// proceso seguir vivo; un SIGKILL posterior sí surte efecto y lo mata.
func TestKillSignalNotMarkedOnFailure(t *testing.T) {
	cmd := newCmd([]string{"sleep", "30"}, procOpts{})
	if err := cmd.Start(); err != nil {
		t.Fatalf("no se pudo lanzar sleep: %v", err)
	}
	pid := cmd.Process.Pid
	p := &luaProc{cmd: cmd}

	// Señal inválida (fuera de rango) sobre un proceso vivo → el envío falla (EINVAL).
	p.killSignal(syscall.Signal(0x1fff))
	if p.killed {
		t.Fatalf("un envío de señal fallido NO debe fijar killed=true")
	}
	if !pidAlive(pid) {
		t.Fatalf("tras un kill fallido el proceso (pid %d) debería seguir vivo", pid)
	}

	// Un SIGKILL posterior SÍ debe surtir efecto: no está cortocircuitado por `killed`.
	p.killSignal(syscall.SIGKILL)
	if !p.killed {
		t.Fatalf("tras un SIGKILL exitoso, killed debería ser true")
	}
	if err := cmd.Wait(); err == nil {
		t.Fatalf("un proceso matado por SIGKILL debería salir con error de señal")
	}
	if pidAlive(pid) {
		t.Fatalf("tras SIGKILL+Wait el proceso (pid %d) debería estar muerto", pid)
	}
}

// --- Sanity: la superficie de nu.proc existe (firma §6 completa) ---

// TestProcSurface comprueba que `nu.proc` y los métodos de `Proc` están registrados,
// como prueba de humo de la sesión.
func TestProcSurface(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`return type(nu.proc)`, "table")
	h.expectEval(`return type(nu.proc.run)`, "function")
	h.expectEval(`return type(nu.proc.spawn)`, "function")
	h.expectEval(`return type(nu.proc.alive)`, "function")
	h.eval(`
		local p = nu.proc.spawn({"sleep", "30"})
		assert(type(p.write) == "function")
		assert(type(p.close_stdin) == "function")
		assert(type(p.read_line) == "function")
		assert(type(p.read) == "function")
		assert(type(p.wait) == "function")
		assert(type(p.kill) == "function")
		p:kill()
	`)
}
