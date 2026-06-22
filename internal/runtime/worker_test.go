package runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Tests de S34 — `nu.worker.spawn` + caps (G6) + send/recv con colas acotadas
// (§13). La lógica 🔒 a blindar:
//
//   - **caps deny-by-default + dos granularidades (G6)**: un worker con
//     `caps={"fs.read"}` TIENE `nu.fs.read` pero NO `nu.fs.write` ni otros módulos;
//     con `caps={"fs"}` tiene todo `fs`; sin `caps` toda la API [W]; con `caps={}`
//     casi nada. Se verifica DESDE DENTRO del worker (reporta al padre qué existe).
//   - **colas acotadas / backpressure**: `Worker:send` SUSPENDE al llenarse la cola
//     (worker que no consume) y otra task del padre PROGRESA mientras tanto.
//   - **mensajes copiados**: una tabla enviada se COPIA; un valor no-serializable
//     (función) → `EINVAL`.
//   - **round-trip**: padre send → worker parent.recv → worker parent.send → padre
//     recv (eco).
//   - **sin watchdog (G15)**: un worker quema CPU (cómputo largo) sin ser abortado.
//
// El arnés `newHarness` (G20) corre headless con `WithForceUI(true)`; el worker, en
// cambio, es SIEMPRE headless (`uiActive=false`): un worker no tiene `nu.ui`.

// workerHarness construye un harness cuyo runtime tiene un directorio de plugins con
// el plugin `wmod` (un módulo `lua/wmod.lua` con `init.lua` vacío), de modo que un
// worker pueda `require("wmod")`. Devuelve el harness ya con `Boot()` hecho (para
// poblar las rutas de require del loader).
func workerHarness(t *testing.T, wmodLua string) *harness {
	t.Helper()
	root := t.TempDir()
	cfg := t.TempDir()
	dir := filepath.Join(root, "p")
	if err := os.MkdirAll(filepath.Join(dir, "lua"), 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"),
		[]byte("name = \"p\"\nversion = \"1.0\"\n"), 0o644); err != nil {
		t.Fatalf("write plugin.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.lua"), []byte(""), 0o644); err != nil {
		t.Fatalf("write init.lua: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lua", "wmod.lua"), []byte(wmodLua), 0o644); err != nil {
		t.Fatalf("write wmod.lua: %v", err)
	}
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithPluginDir(root), WithForceUI(true))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}
}

// TestWorkerRoundTrip blinda el camino feliz: el padre manda un mensaje, el worker
// lo recibe por `parent.recv`, lo procesa y devuelve un eco por `parent.send`, y el
// padre lo lee con `Worker:recv`. Valores correctos en ambos sentidos (§13).
func TestWorkerRoundTrip(t *testing.T) {
	h := workerHarness(t, `
		-- El módulo ES el cuerpo del worker (corre como task): recibe, transforma, responde.
		local msg = nu.worker.parent.recv()
		nu.worker.parent.send({ echo = msg.text, n = msg.n * 2 })
	`)

	// La task del padre corre durante el `waitIdle` de este primer eval; deja el
	// resultado en globales que el segundo eval lee.
	h.eval(`
		ECHO, N, DONE = nil, nil, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			w:send({ text = "hola", n = 21 })
			local result = w:recv()
			ECHO, N = result.echo, result.n
			w:terminate()
			DONE = true
		end)
	`)
	h.expectEval(`return tostring(DONE)`, "true")
	h.expectEval(`return ECHO`, "hola")
	h.expectEval(`return tostring(N)`, "42")
}

// TestWorkerCapsTwoGranularities (G6) blinda el filtrado deny-by-default con dos
// granularidades. El worker INSPECCIONA su propia API (qué existe / qué es nil) y
// reporta el resultado al padre. Cuatro escenarios:
//   - sin caps           → toda la API [W] presente (fs.read, fs.write, http, ...).
//   - caps={"fs"}        → todo `fs`, pero NO `http` (no concedido).
//   - caps={"fs.read"}   → `fs.read` SÍ, `fs.write` NO, `http` NO.
//   - caps={}            → casi nada: ni `fs` ni `http` (deny-by-default).
func TestWorkerCapsTwoGranularities(t *testing.T) {
	// El módulo reporta un mapa de existencia de funciones/módulos al padre.
	const probe = `
		local function has(path)
			local cur = nu
			for part in string.gmatch(path, "[^.]+") do
				if type(cur) ~= "table" then return false end
				cur = cur[part]
			end
			return cur ~= nil
		end
		nu.worker.parent.send({
			fs        = has("fs"),
			fs_read   = has("fs.read"),
			fs_write  = has("fs.write"),
			http      = has("http"),
			text      = has("text"),
			task      = has("task"),          -- siempre [W]
			version   = has("version"),       -- siempre presente
			ui        = has("ui"),            -- NUNCA en worker (§16)
			events    = has("events"),        -- NUNCA en worker (§16)
			spawn     = has("worker.spawn"),  -- NUNCA (sin anidar, §16)
			parent    = has("worker.parent"), -- SIEMPRE (canal)
		})
	`
	h := workerHarness(t, probe)

	cases := []struct {
		name     string
		spawnLua string
		want     map[string]bool
	}{
		{
			name:     "sin caps: toda la API [W]",
			spawnLua: `nu.worker.spawn("wmod")`,
			want: map[string]bool{
				"fs": true, "fs_read": true, "fs_write": true, "http": true,
				"text": true, "task": true, "version": true,
				"ui": false, "events": false, "spawn": false, "parent": true,
			},
		},
		{
			name:     "caps={'fs'}: modulo entero, nada mas",
			spawnLua: `nu.worker.spawn("wmod", { caps = {"fs"} })`,
			want: map[string]bool{
				"fs": true, "fs_read": true, "fs_write": true, "http": false,
				"text": false, "task": false, "version": true,
				"ui": false, "events": false, "spawn": false, "parent": true,
			},
		},
		{
			name:     "caps={'fs.read'}: solo esa funcion (G6)",
			spawnLua: `nu.worker.spawn("wmod", { caps = {"fs.read"} })`,
			want: map[string]bool{
				"fs": true, "fs_read": true, "fs_write": false, "http": false,
				"text": false, "task": false, "version": true,
				"ui": false, "events": false, "spawn": false, "parent": true,
			},
		},
		{
			name:     "caps={} vacio: deny-by-default, casi nada",
			spawnLua: `nu.worker.spawn("wmod", { caps = {} })`,
			want: map[string]bool{
				"fs": false, "fs_read": false, "fs_write": false, "http": false,
				"text": false, "task": false, "version": true,
				"ui": false, "events": false, "spawn": false, "parent": true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Construye un snippet que spawnea con las caps del caso, recibe el reporte y
			// devuelve cada flag como "k=true"/"k=false" para comparar.
			var keys []string
			for k := range tc.want {
				keys = append(keys, k)
			}
			setup := `
				REP, RDONE = nil, false
				nu.task.spawn(function()
					local w = ` + tc.spawnLua + `
					REP = w:recv()
					w:terminate()
					RDONE = true
				end)
			`
			h.eval(setup)
			h.expectEval(`return tostring(RDONE)`, "true")

			report := `local out = {}` + "\n"
			for _, k := range keys {
				report += `out[#out+1] = "` + k + `=" .. tostring(REP.` + k + ` == true)` + "\n"
			}
			report += `return table.concat(out, ",")`

			got := h.eval(report)
			if len(got) != 1 {
				t.Fatalf("reporte: got %q", got)
			}
			parsed := map[string]bool{}
			for _, kv := range strings.Split(got[0], ",") {
				p := strings.SplitN(kv, "=", 2)
				parsed[p[0]] = p[1] == "true"
			}
			for k, wantV := range tc.want {
				if parsed[k] != wantV {
					t.Errorf("cap %q: got %v, want %v (caps=%s)", k, parsed[k], wantV, tc.spawnLua)
				}
			}
		})
	}
}

// TestWorkerBackpressure blinda que `Worker:send` SUSPENDE cuando la cola acotada se
// llena (el worker no consume) y que el loop del padre NO se congela: otra task del
// padre progresa mientras el `send` espera hueco. El worker bloquea sin consumir
// nunca (espera un `done` que nunca llega), así que la cola del padre→worker se
// llena tras `workerQueueCap` envíos y el `(workerQueueCap+1)`-ésimo suspende.
func TestWorkerBackpressure(t *testing.T) {
	// El worker no consume NADA: bloquea para siempre en un recv que no llega (su
	// loop espera un mensaje que el test nunca manda más allá del llenado). Así la
	// cola se llena y el `send` del padre suspende.
	h := workerHarness(t, `
		-- Nunca consume: duerme en un bucle de recv que el test deja llenar.
		-- (Bloquea en el primer recv solo tras una pausa larga, para que la cola se llene.)
		nu.task.sleep(60000)
	`)

	h.eval(`
		PROGRESSED, SENDS, BDONE = false, 0, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")

			-- Task testigo del padre: DEBE progresar aunque el send del productor este
			-- suspendido por backpressure (el loop no se congela).
			local witness = nu.task.spawn(function()
				nu.task.sleep(5)
				PROGRESSED = true
			end)

			-- Productor: manda mas mensajes que la capacidad de la cola. Los primeros
			-- caben; al llenarse, send SUSPENDE (backpressure) y nunca completa los 1000.
			local producer = nu.task.spawn(function()
				for i = 1, 1000 do
					w:send(i)
					SENDS = SENDS + 1
				end
			end)

			witness:await()  -- termina rapido pese al send suspendido
			w:terminate()    -- corta: el producer suspendido se desenrolla (ECLOSED)
			pcall(function() producer:await() end)
			BDONE = true
		end)
	`)
	h.expectEval(`return tostring(BDONE)`, "true")
	h.expectEval(`return tostring(PROGRESSED)`, "true")
	// El productor sufrio backpressure: ni completo los 1000 ni envio cero.
	h.expectEval(`return tostring(SENDS < 1000 and SENDS >= 1)`, "true")
}

// TestWorkerMessageCopied blinda que una tabla enviada se COPIA (no se comparte): el
// padre muta su tabla DESPUÉS de enviarla y el worker debe ver el valor del momento
// del envío, no la mutación posterior. El worker devuelve lo que vio.
func TestWorkerMessageCopied(t *testing.T) {
	h := workerHarness(t, `
		local m = nu.worker.parent.recv()
		nu.worker.parent.send(m.v)  -- eco del valor que el worker VIO
	`)
	h.eval(`
		SEEN, CDONE = nil, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			local t = { v = 7 }
			w:send(t)        -- copia: el worker recibe v=7
			t.v = 999        -- mutacion POSTERIOR del lado del padre
			SEEN = w:recv()
			w:terminate()
			CDONE = true
		end)
	`)
	h.expectEval(`return tostring(CDONE)`, "true")
	// El worker debe haber visto v=7 (copia), no la mutacion posterior a 999.
	h.expectEval(`return tostring(SEEN)`, "7")
}

// TestWorkerSendNonSerializable blinda que enviar un valor NO serializable (una
// función) lanza `EINVAL` claro, reusando el codec de §12. El error se detecta en el
// lado del padre (bajo su token), antes de suspender.
func TestWorkerSendNonSerializable(t *testing.T) {
	h := workerHarness(t, `nu.task.sleep(60000)`)
	h.eval(`
		ERRCODE, EDONE = nil, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			local ok, e = pcall(function() w:send(function() end) end)
			ERRCODE = (not ok) and e.code or "no-lanzo"
			w:terminate()
			EDONE = true
		end)
	`)
	h.expectEval(`return tostring(EDONE)`, "true")
	h.expectEval(`return ERRCODE`, CodeEINVAL)
}

// TestWorkerNoWatchdog (G15) blinda que el worker es un mini-runtime SIN watchdog:
// puede quemar CPU a gusto. Se comprueba de dos formas complementarias para no
// depender de la temporización (frágil bajo `-race`):
//
//  1. ESTRUCTURAL: el scheduler del worker tiene presupuesto de slice ≤ 0 (el
//     gancho que desactiva el watchdog, G15). Se inspecciona el sub-Runtime del
//     worker directamente (mismo paquete).
//  2. FUNCIONAL: un cómputo largo de CPU pura (sin suspender) DENTRO del worker
//     COMPLETA y devuelve su resultado —si hubiera watchdog, lo abortaría—. El
//     PADRE corre con el watchdog DESACTIVADO para que la task orquestadora no sea
//     ella misma víctima de un corte (lo que probamos es el worker, no el padre).
func TestWorkerNoWatchdog(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	dir := filepath.Join(root, "p")
	if err := os.MkdirAll(filepath.Join(dir, "lua"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte("name=\"p\"\nversion=\"1.0\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "init.lua"), []byte(""), 0o644)
	// Cómputo de CPU pura (sin suspender) dentro del worker. Si el worker tuviera
	// watchdog (presupuesto pequeño), un bucle así se cortaría; sin él, completa.
	os.WriteFile(filepath.Join(dir, "lua", "wmod.lua"), []byte(`
		local s = 0
		for i = 1, 2000000 do s = s + 1 end
		nu.worker.parent.send(s)
	`), 0o644)

	// Padre con watchdog DESACTIVADO (budget 0): probamos el worker, no el padre.
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithPluginDir(root),
		WithForceUI(true), WithSliceBudget(0))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}

	h.eval(`
		SUM, WDONE = nil, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			SUM = w:recv()
			w:terminate()
			WDONE = true
		end)
	`)
	h.expectEval(`return tostring(WDONE)`, "true")
	// El worker completo el computo (2e6) sin que ningun watchdog lo abortara (G15).
	h.expectEval(`return tostring(SUM)`, "2000000")
}

// TestWorkerSchedulerHasNoWatchdog (G15, estructural) blinda que el scheduler de un
// worker NO tiene presupuesto de slice (≤ 0 desactiva el watchdog), AUNQUE el padre
// SÍ lo tenga. Inspecciona el sub-Runtime del worker directamente (mismo paquete),
// sin depender de temporización.
func TestWorkerSchedulerHasNoWatchdog(t *testing.T) {
	// Padre CON watchdog agresivo: el worker debe seguir sin watchdog igualmente.
	parent := New(WithDataDir(t.TempDir()), WithConfigDir(t.TempDir()),
		WithForceUI(true), WithSliceBudget(1*time.Millisecond))
	defer parent.Close()

	chans := &workerChannels{
		toWorker:   make(chan interface{}, workerQueueCap),
		fromWorker: make(chan interface{}, workerQueueCap),
		done:       make(chan struct{}),
	}
	wrt := newWorkerRuntime(parent, chans, nil, false)
	defer wrt.Close()

	if parent.sched.budget <= 0 {
		t.Fatalf("precondicion: el padre deberia tener watchdog (budget>0), got %v", parent.sched.budget)
	}
	if wrt.sched.budget > 0 {
		t.Fatalf("G15: el worker NO debe tener watchdog (budget<=0), got %v", wrt.sched.budget)
	}
	if !wrt.isWorker {
		t.Fatalf("el sub-Runtime del worker deberia tener isWorker=true")
	}
}

// TestWorkerTerminateInterruptsSleep blinda que `terminate()` es **inmediato y sin
// fuga** (§13): un worker cuya task está suspendida en un `nu.task.sleep` LARGO (60 s)
// es cortado al acto por `terminate()` —NO espera a que venza el sleep— y NO deja la
// goroutine del worker colgada. Sin el arreglo del review, `terminate` solo cerraba
// `done`, que un `sleep` no observa: el worker colgaba ~60 s y su goroutine fugaba.
//
// Se comprueba en dos ejes:
//   - TIEMPO: `terminate()` + `Close()` completan muy por debajo del sleep (no se
//     espera 60 s). Un margen amplio evita falsos positivos bajo `-race`.
//   - FUGA: `runtime.NumGoroutine()` tras `Close()` vuelve (con margen) al nivel
//     previo al spawn: la goroutine del worker terminó, no quedó colgada tocando el
//     `data_dir`/`log` compartidos.
func TestWorkerTerminateInterruptsSleep(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	dir := filepath.Join(root, "p")
	if err := os.MkdirAll(filepath.Join(dir, "lua"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte("name=\"p\"\nversion=\"1.0\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "init.lua"), []byte(""), 0o644)
	// El módulo del worker se suspende en un sleep LARGO: si `terminate` no lo cortara
	// en su punto de suspensión, el worker colgaría hasta que venciera (60 s).
	os.WriteFile(filepath.Join(dir, "lua", "wmod.lua"), []byte(`
		nu.task.sleep(60000)
	`), 0o644)

	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithPluginDir(root), WithForceUI(true))
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}

	// Nivel de goroutines ANTES de spawnear el worker: la referencia para detectar la
	// fuga. Una pequeña estabilización para no contar goroutines transitorias.
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	before := runtime.NumGoroutine()

	start := time.Now()
	h.eval(`
		TDONE = false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			-- Da un instante a que el worker arranque y se suspenda en el sleep largo.
			nu.task.sleep(20)
			w:terminate()  -- DEBE cortar el sleep del worker de inmediato, no esperar 60 s
			TDONE = true
		end)
	`)
	h.expectEval(`return tostring(TDONE)`, "true")

	// `Close` cierra el runtime del padre: `stopAllWorkers` corta y ESPERA a la
	// goroutine del worker (su `wait()`), de modo que tras esto no debe quedar viva.
	rt.Close()
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("terminate+Close tardó %v: el sleep largo NO se cortó (terminate no es inmediato)", elapsed)
	}

	// Tras `Close` la goroutine del worker terminó: el conteo vuelve cerca del nivel
	// previo. Se da un pequeño margen por goroutines de runtime/GC transitorias.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("fuga de goroutines: antes=%d, después de terminate+Close=%d (la goroutine del worker quedó colgada)", before, after)
	}
}

// TestWorkerTerminateInterruptsCPULoop blinda que `terminate()` también corta un
// worker que corre un bucle de CPU PURA sin suspender (G15: un worker no tiene
// watchdog, así que el único corte posible es la cancelación del contexto que
// `terminate` dispara). Sin ese corte, un `while true do end` dejaría la goroutine
// del worker colgada para siempre. No mide tiempo exacto (frágil): solo que
// `terminate()` + `Close()` retornan en un margen razonable.
func TestWorkerTerminateInterruptsCPULoop(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	dir := filepath.Join(root, "p")
	if err := os.MkdirAll(filepath.Join(dir, "lua"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte("name=\"p\"\nversion=\"1.0\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "init.lua"), []byte(""), 0o644)
	// Bucle de CPU pura infinito: sin punto de suspensión cooperativo. Solo la
	// cancelación del contexto (que `terminate` dispara) puede romperlo.
	os.WriteFile(filepath.Join(dir, "lua", "wmod.lua"), []byte(`
		while true do end
	`), 0o644)

	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithPluginDir(root), WithForceUI(true))
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}

	done := make(chan struct{})
	go func() {
		h.eval(`
			CDONE = false
			nu.task.spawn(function()
				local w = nu.worker.spawn("wmod")
				nu.task.sleep(20) -- deja arrancar el bucle de CPU del worker
				w:terminate()     -- rompe el bucle por cancelación de contexto
				CDONE = true
			end)
		`)
		rt.Close()
		close(done)
	}()

	select {
	case <-done:
		// terminate+Close retornaron: el bucle de CPU se cortó y la goroutine murió.
	case <-time.After(15 * time.Second):
		t.Fatalf("terminate+Close no retornó: el bucle de CPU del worker no se cortó (fuga)")
	}
}

// TestWorkerSpawnValidation blinda los errores de argumento de `nu.worker.spawn`
// (§13), todos `EINVAL` accionables antes de crear nada.
func TestWorkerSpawnValidation(t *testing.T) {
	h := newHarness(t)

	if se := h.evalErr(`nu.worker.spawn("")`); se.Code != CodeEINVAL {
		t.Errorf("module vacio: code=%q, want EINVAL", se.Code)
	}
	if se := h.evalErr(`nu.worker.spawn("m", { caps = "fs" })`); se.Code != CodeEINVAL {
		t.Errorf("caps no-array: code=%q, want EINVAL", se.Code)
	}
	if se := h.evalErr(`nu.worker.spawn("m", { caps = {123} })`); se.Code != CodeEINVAL {
		t.Errorf("caps con no-string: code=%q, want EINVAL", se.Code)
	}
	if se := h.evalErr(`nu.worker.spawn("m", "no-tabla")`); se.Code != CodeEINVAL {
		t.Errorf("opts no-tabla: code=%q, want EINVAL", se.Code)
	}
}

// TestWorkerSendRecvRequireTask blinda que `Worker:send`/`recv` (⏸) fuera de una
// task lanzan `EINVAL` (no se puede suspender en el estado principal, §1.3).
func TestWorkerSendRecvRequireTask(t *testing.T) {
	h := workerHarness(t, `nu.task.sleep(60000)`)
	if se := h.evalErr(`
		local w = nu.worker.spawn("wmod")
		local ok, e = pcall(function() w:send(1) end)
		w:terminate()
		error(e)
	`); se.Code != CodeEINVAL {
		t.Errorf("send fuera de task: code=%q, want EINVAL", se.Code)
	}
}

// TestWorkerRecvAfterTerminate blinda que un `Worker:recv` tras terminar el worker
// (sin mensajes pendientes) da `nil` (fin del canal), no lanza —coherente con
// `Ws:recv`/§8 (una punta cerrada rinde fin de stream)—.
func TestWorkerRecvAfterTerminate(t *testing.T) {
	h := workerHarness(t, `nu.worker.parent.recv()`) // espera un mensaje que no llega
	h.eval(`
		GOT, GDONE, GOTNIL = "sentinel", false, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			w:terminate()
			local got = w:recv()
			GOTNIL = (got == nil)
			GDONE = true
		end)
	`)
	h.expectEval(`return tostring(GDONE)`, "true")
	h.expectEval(`return tostring(GOTNIL)`, "true")
}
