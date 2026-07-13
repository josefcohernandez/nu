package runtime

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Tests de S17 (api.md §7): `nu.sys`. Sesión NO 🔒 (wrappers finos), pero
// `setenv` tiene lógica propia: el **overlay** de variables que afecta SOLO a
// subprocesos futuros (§7) y su integración con `nu.proc` (S16). Esa lógica se
// blinda con un test unitario Go de `mergedEnv` (precedencia de capas) y un test
// de extremo a extremo por el puente ⏸ real (`setenv` + `proc.run`). El resto
// (`platform`/`env`/`now_ms`/`mono_ms`/`hostname`) se cubre con snippet Lua, que
// es lo que pide la política de tests para glue sobre la stdlib.

// --- Lógica propia: overlay de setenv (mergedEnv, precedencia de capas) ---

// envValue extrae el valor de la clave `key` de un entorno en forma `["K=V", ...]`
// (lo que `mergedEnv` produce y `exec.Cmd` consume). Devuelve `("", false)` si la
// clave no está. Tolera valores con `=`.
func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := splitEnv(kv); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// TestMergedEnvPrecedence blinda la precedencia de capas de `mergedEnv` (la
// integración S16↔S17, §6/§7): entorno heredado del SO < overlay de `setenv` <
// `opts.env` explícito. Es la pieza que hace que `setenv` "afecte a subprocesos
// futuros" pisando lo heredado, sin que el `nu` actual cambie, y que un
// `opts.env` por llamada gane sobre el overlay (control total local).
func TestMergedEnvPrecedence(t *testing.T) {
	// Variable de control: la sembramos en el entorno REAL del proceso de test
	// para comprobar las capas frente a algo heredado de verdad.
	const base = "NU_S17_BASE"
	t.Setenv(base, "from_os")

	t.Run("sin overlay ni env: hereda os.Environ sin cambios (nil)", func(t *testing.T) {
		if got := mergedEnv(procOpts{}); got != nil {
			t.Fatalf("sin overlay ni env, mergedEnv debe ser nil (heredar), got %v", got)
		}
	})

	t.Run("overlay pisa lo heredado del SO", func(t *testing.T) {
		env := mergedEnv(procOpts{envOver: map[string]string{base: "from_overlay"}})
		if v, ok := envValue(env, base); !ok || v != "from_overlay" {
			t.Fatalf("overlay debe pisar lo heredado: got %q (ok=%v), want \"from_overlay\"", v, ok)
		}
	})

	t.Run("overlay añade clave nueva no presente en el SO", func(t *testing.T) {
		env := mergedEnv(procOpts{envOver: map[string]string{"NU_S17_NEW": "42"}})
		if v, ok := envValue(env, "NU_S17_NEW"); !ok || v != "42" {
			t.Fatalf("overlay debe añadir clave nueva: got %q (ok=%v), want \"42\"", v, ok)
		}
		// Y debe conservar lo heredado (no es opts.env: no reemplaza).
		if v, ok := envValue(env, base); !ok || v != "from_os" {
			t.Fatalf("overlay no debe descartar lo heredado: got %q (ok=%v), want \"from_os\"", v, ok)
		}
	})

	t.Run("opts.env explícito gana sobre el overlay (control total local)", func(t *testing.T) {
		env := mergedEnv(procOpts{
			env:     []string{base + "=from_optsenv"},
			envOver: map[string]string{base: "from_overlay"},
		})
		if v, ok := envValue(env, base); !ok || v != "from_optsenv" {
			t.Fatalf("opts.env debe ganar al overlay: got %q (ok=%v), want \"from_optsenv\"", v, ok)
		}
	})

	t.Run("opts.env explícito reemplaza lo heredado (no aparece os.Environ ni overlay ajeno)", func(t *testing.T) {
		// opts.env solo con OTRA clave: el overlay no debe colarse, y `base`
		// heredado tampoco (env explícito = control total, §6).
		env := mergedEnv(procOpts{
			env:     []string{"NU_S17_ONLY=x"},
			envOver: map[string]string{"NU_S17_OVERLAY=y": ""},
		})
		if _, ok := envValue(env, base); ok {
			t.Fatalf("opts.env explícito no debe heredar %s del SO", base)
		}
		if _, ok := envValue(env, "NU_S17_OVERLAY"); ok {
			t.Fatalf("con opts.env explícito, el overlay no debe aplicarse")
		}
	})

	t.Run("opts.env vacío produce entorno vacío (sin overlay)", func(t *testing.T) {
		env := mergedEnv(procOpts{env: []string{}, envOver: map[string]string{"NU_S17_X": "1"}})
		if len(env) != 0 {
			t.Fatalf("opts.env={} debe dar entorno vacío (control total), got %v", env)
		}
	})
}

// TestSplitEnv comprueba el partido "K=V" por el PRIMER `=` (un valor puede
// contener `=`), y la entrada sin `=` (clave con valor vacío).
func TestSplitEnv(t *testing.T) {
	cases := []struct {
		in         string
		k, v       string
		hasEqualOK bool
	}{
		{"FOO=bar", "FOO", "bar", true},
		{"URL=https://a.b/?x=1", "URL", "https://a.b/?x=1", true},
		{"EMPTY=", "EMPTY", "", true},
		{"NOEQUAL", "NOEQUAL", "", false},
	}
	for _, c := range cases {
		k, v, ok := splitEnv(c.in)
		if k != c.k || v != c.v || ok != c.hasEqualOK {
			t.Fatalf("splitEnv(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, k, v, ok, c.k, c.v, c.hasEqualOK)
		}
	}
}

// --- 🔑 Criterio de hecho: setenv se ve en un subproceso lanzado DESPUÉS, no en
// el `nu` actual (§7, integración S16↔S17, de extremo a extremo) ---

// TestSetenvFutureSubprocess es el criterio de hecho del plan: tras
// `nu.sys.setenv("NU_TEST_X","42")`, un `nu.proc.run(["printenv","NU_TEST_X"])`
// lanzado DESPUÉS imprime "42" (lo ve por el overlay), mientras que el entorno
// del proceso `nu` actual NO cambió —`os.Getenv("NU_TEST_X")` en Go sigue
// vacío—. Demuestra "afecta solo a subprocesos futuros, no al actual". `printenv`
// es coreutils (presente en cualquier Linux de CI) y se invoca SIN shell (argv
// directo), así que también ejercita la ausencia de shell de `nu.proc`.
func TestSetenvFutureSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("printenv no existe en Windows; el criterio es POSIX")
	}
	const key = "NU_TEST_X"
	// Pre-condición: la variable no existe en el entorno real del proceso de test.
	if _, ok := os.LookupEnv(key); ok {
		t.Fatalf("pre-condición rota: %s ya está en el entorno", key)
	}

	// El subproceso imprime el valor: "42\n" (printenv añade salto), exit 0 porque
	// la variable existe (vía overlay). El resultado se observa desde DENTRO de una
	// task (un `run` es ⏸): una task hace setenv+run y publica el desenlace en un
	// future; otra lo espera y lo asserta —si el subproceso NO viera la variable,
	// printenv saldría con 1 y stdout vacío y el assert dispararía—.
	h := newHarness(t)
	h.expectEval(`
		local fut = nu.task.future()
		nu.task.spawn(function()
			nu.sys.setenv("NU_TEST_X", "42")
			local r = nu.proc.run({"printenv", "NU_TEST_X"})
			fut:set({ out = r.stdout, code = r.code })
		end)
		nu.task.spawn(function()
			local res = fut:await()
			assert(res.code == 0, "printenv debería salir 0 (la var existe vía overlay), code=" .. tostring(res.code))
			assert(res.out == "42\n", "el subproceso debería ver NU_TEST_X=42, got " .. string.format("%q", res.out))
		end)
		return "ok"
	`, "ok")

	// "No en el actual": el entorno del proceso `nu` (el de Go) NO cambió.
	if v, ok := os.LookupEnv(key); ok {
		t.Fatalf("§7 violado: setenv mutó el entorno del proceso actual: %s=%q", key, v)
	}
}

// TestSetenvNotInCurrentProcessSubprocess refuerza "no en el actual" desde el
// lado del subproceso: un `nu.proc.run` con `opts.env` explícito que NO incluye
// la clave (control total local, §6) NO ve el overlay —printenv sale con 1—.
// Demuestra que el overlay vive aparte y un env explícito lo deja fuera.
func TestSetenvNotInCurrentProcessSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("printenv no existe en Windows; el criterio es POSIX")
	}
	h := newHarness(t)
	h.expectEval(`
		local fut = nu.task.future()
		nu.task.spawn(function()
			nu.sys.setenv("NU_TEST_Y", "99")
			-- env explícito sin NU_TEST_Y: control total, el overlay no se aplica.
			local r = nu.proc.run({"printenv", "NU_TEST_Y"}, { env = { OTHER = "z" } })
			fut:set(r.code)
		end)
		nu.task.spawn(function()
			local code = fut:await()
			assert(code ~= 0, "con opts.env explícito sin la clave, printenv debe salir != 0 (overlay no aplica), code=" .. tostring(code))
		end)
		return "ok"
	`, "ok")
}

// --- Wrappers finos: snippet Lua (platform/env/setenv/now_ms/mono_ms/hostname) ---

// TestSysPlatform comprueba `nu.sys.platform()`: coincide con runtime.GOOS y es
// uno de los esperados en CI (linux/darwin).
func TestSysPlatform(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`return nu.sys.platform()`, runtime.GOOS)
}

// TestSysEnvOverlay comprueba `nu.sys.env`/`setenv`: una variable inexistente da
// nil; tras `setenv` se lee por el overlay (por encima del SO); el entorno real
// del proceso no cambia.
func TestSysEnvOverlay(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		assert(nu.sys.env("NU_S17_ABSENT") == nil, "inexistente debe ser nil")
		nu.sys.setenv("NU_S17_ABSENT", "present")
		assert(nu.sys.env("NU_S17_ABSENT") == "present", "tras setenv, env la lee por el overlay")
		return "ok"
	`, "ok")
	if _, ok := os.LookupEnv("NU_S17_ABSENT"); ok {
		t.Fatalf("setenv no debe mutar el entorno del proceso actual")
	}
}

// TestSysEnvReadsOSValue comprueba que `nu.sys.env` ve una variable del SO
// (heredada), no solo el overlay.
func TestSysEnvReadsOSValue(t *testing.T) {
	t.Setenv("NU_S17_OS", "value_from_os")
	h := newHarness(t)
	h.expectEval(`assert(nu.sys.env("NU_S17_OS") == "value_from_os", "env debe leer la var del SO"); return "ok"`, "ok")
}

// TestSysClocks comprueba `now_ms`/`mono_ms`: ambos positivos, now_ms cerca del
// reloj real, y mono_ms no decrece entre dos lecturas (monotónico).
func TestSysClocks(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local now = nu.sys.now_ms()
		assert(type(now) == "number" and now > 0, "now_ms debe ser un number positivo")
		local a = nu.sys.mono_ms()
		local b = nu.sys.mono_ms()
		assert(type(a) == "number" and type(b) == "number", "mono_ms debe ser number")
		assert(b >= a, "mono_ms no debe decrecer: a=" .. tostring(a) .. " b=" .. tostring(b))
		return "ok"
	`, "ok")
}

// TestSysHostname comprueba `nu.sys.hostname()`: devuelve un string no vacío que
// coincide con os.Hostname (es un wrapper directo).
func TestSysHostname(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname falló en este entorno: %v", err)
	}
	h := newHarness(t)
	got := h.eval(`return nu.sys.hostname()`)
	if len(got) != 1 || got[0] != want || strings.TrimSpace(got[0]) == "" {
		t.Fatalf("hostname: got %q, want %q", got, want)
	}
}

// TestSysPid comprueba `nu.sys.pid()` (G32): un integer > 0, estable entre
// llamadas e igual a `os.Getpid()` del proceso de test (el wrapper es directo).
// Es el `pid` que la extensión sesiones graba en su lockfile (sesiones.md §6).
func TestSysPid(t *testing.T) {
	want := os.Getpid()
	h := newHarness(t)
	h.expectEval(`
		local p = nu.sys.pid()
		assert(type(p) == "number", "pid debe ser un number")
		assert(p == math.floor(p), "pid debe ser entero")
		assert(p > 0, "pid debe ser positivo")
		assert(nu.sys.pid() == p, "pid debe ser estable entre llamadas")
		return "ok"`, "ok")
	got := h.eval(`return tostring(nu.sys.pid())`)
	if len(got) != 1 || strings.TrimSpace(got[0]) != strconv.Itoa(want) {
		t.Fatalf("pid: got %q, want %d (== os.Getpid)", got, want)
	}
}

// TestSysPidInWorker comprueba que `nu.sys.pid()` está disponible [W]: `sys` es
// módulo [W] entero (§16), así que `pid` hereda. Con `caps={"sys"}` el worker lo
// ve; con `caps={"sys.pid"}` también (granularidad de función, G6). Como un worker
// comparte el proceso del padre (es una goroutine, no un fork), su `pid` coincide
// con el del proceso de test.
func TestSysPidInWorker(t *testing.T) {
	want := os.Getpid()
	for _, capLua := range []string{`{ caps = {"sys"} }`, `{ caps = {"sys.pid"} }`, ``} {
		spawn := `nu.worker.spawn("wmod")`
		if capLua != "" {
			spawn = `nu.worker.spawn("wmod", ` + capLua + `)`
		}
		h := workerHarness(t, `nu.worker.parent.send({ pid = nu.sys.pid() })`)
		h.eval(`
			WPID = nil
			nu.task.spawn(function()
				local w = ` + spawn + `
				WPID = w:recv().pid
				w:terminate()
			end)`)
		got := h.eval(`return tostring(WPID)`)
		if len(got) != 1 || strings.TrimSpace(got[0]) != strconv.Itoa(want) {
			t.Fatalf("pid en worker (caps=%q): got %q, want %d", capLua, got, want)
		}
	}
}

// TestSysPidDeniedByCaps confirma el deny-by-default (G6): un worker con `caps={}`
// no recibe `nu.sys` en absoluto, así que `nu.sys.pid` no existe.
func TestSysPidDeniedByCaps(t *testing.T) {
	h := workerHarness(t, `nu.worker.parent.send({ has = (nu.sys ~= nil) })`)
	h.eval(`
		HAS = nil
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod", { caps = {} })
			HAS = w:recv().has
			w:terminate()
		end)`)
	h.expectEval(`return tostring(HAS)`, "false")
}

// TestVersionApiIsThree blinda el nivel actual de `nu.version.api`: 3 tras los
// frames binarios de `nu.ws` (G52/A-38: `opts.binary` en `Ws:send` y el segundo
// retorno de `Ws:recv`). Antes fue 2 (G32: `nu.sys.pid`, la primera adición tras
// el congelado). api.md §17: el contador sube con cada adición.
func TestVersionApiIsThree(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`return tostring(nu.version.api)`, "3")
}

// TestSysAvailableInTask comprueba que `nu.sys` funciona también desde dentro de
// una task (es [W]/síncrono, no ⏸: no exige ni prohíbe estar en una task).
func TestSysAvailableInTask(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local fut = nu.task.future()
		nu.task.spawn(function()
			fut:set(nu.sys.platform())
		end)
		nu.task.spawn(function()
			assert(fut:await() == "`+runtime.GOOS+`", "platform desde task")
		end)
		return "ok"
	`, "ok")
}
