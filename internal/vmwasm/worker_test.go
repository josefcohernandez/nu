package vmwasm

// Tests de M12: workers como instancias wasm (ADR-008/§13). Blindan el mecanismo:
// aislamiento físico de memoria, mensajería acotada con backpressure, caps (G6),
// exclusión recv/on_message (G8), terminate (interrumpe incluso un bucle de CPU,
// sin watchdog G15), y que un worker no afecta al padre. El `module` es código
// fuente (la resolución por el loader es M13).

import (
	"context"
	"strings"
	"testing"
	"time"
)

// runMain arranca una Instance principal, evalúa `setup` (que crea el/los
// workers) y conduce SU bucle hasta que termina; devuelve la global `out`. El
// worker corre en su propia goroutine (su propio RunTasks).
func runMain(t *testing.T, setup string) string {
	t.Helper()
	p, err := NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })
	if _, lerr, err := inst.Eval(setup); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	if err := inst.RunTasks(context.Background()); err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	out, _, _ := inst.Eval(`return tostring(out)`)
	return out
}

// M12.1: round-trip — el padre manda, el worker transforma y responde. Prueba la
// mensajería bidireccional y que un worker es un mini-runtime que corre su módulo.
func TestWorkerRoundTrip(t *testing.T) {
	out := runMain(t, `
		out = "?"
		local w = enu.worker.spawn([[
			local m = enu.worker.parent.recv()
			enu.worker.parent.send({ text = m.text, n = m.n * 2 })
		]])
		enu.task.spawn(function()
			w:send({ text = "hola", n = 21 })
			local r = w:recv()
			out = r.text .. ":" .. tostring(r.n)
		end)`)
	if out != "hola:42" {
		t.Fatalf("round-trip: got %q", out)
	}
}

// M12.2: AISLAMIENTO FÍSICO — un global del worker NO existe en el padre y
// viceversa (memorias lineales separadas, el regalo de ADR-019).
func TestWorkerAislamiento(t *testing.T) {
	out := runMain(t, `
		GLOBAL_PADRE = "soy-padre"
		local w = enu.worker.spawn([[
			GLOBAL_WORKER = "soy-worker"
			-- el global del padre no existe aquí:
			enu.worker.parent.send({ ve_padre = (GLOBAL_PADRE == nil), yo = GLOBAL_WORKER })
		]])
		enu.task.spawn(function()
			local r = w:recv()
			-- y el global del worker no existe en el padre:
			out = tostring(r.ve_padre) .. ":" .. r.yo .. ":" .. tostring(GLOBAL_WORKER == nil)
		end)`)
	if out != "true:soy-worker:true" {
		t.Fatalf("aislamiento: got %q", out)
	}
}

// M12.3: BACKPRESSURE — con la cola acotada (cap 16) y el worker consumiendo un
// solo mensaje, Worker:send se ATASCA: exactamente ~17 (16 en cola + 1 consumido)
// pasan y el resto suspende para siempre. El invariante es determinista (acotado y
// bloqueado), no depende de una ventana temporal — robusto bajo -race.
func TestWorkerBackpressure(t *testing.T) {
	out := runMain(t, `
		out = "?"
		-- el worker consume 1 y luego duerme: la cola se llena y no se vacía.
		local w = enu.worker.spawn([[ enu.worker.parent.recv() ; enu.task.sleep(100000) ]])
		local enviados = 0
		local acabado = false
		enu.task.spawn(function()
			for i = 1, 1000 do
				w:send({ i = i })
				enviados = enviados + 1
			end
			acabado = true
		end)
		enu.task.spawn(function()
			enu.task.sleep(80)   -- tiempo de sobra para llenar la cola y bloquear
			-- backpressure: el sender NO acabó (bloqueado) y el count está acotado
			-- por la cola (16) + el 1 consumido, no llegó a 1000.
			out = tostring((not acabado) and enviados >= 16 and enviados <= 18)
			w:terminate()
		end)`)
	if out != "true" {
		t.Fatalf("backpressure: got %q (se esperaba no-acabado y 16<=enviados<=18)", out)
	}
}

// M12.4: el mensaje se COPIA — mutar la tabla tras enviarla no afecta a lo que ve
// el worker (no cruzan referencias, ADR-008).
func TestWorkerMensajeCopiado(t *testing.T) {
	out := runMain(t, `
		out = "?"
		local w = enu.worker.spawn([[
			local m = enu.worker.parent.recv()
			enu.worker.parent.send({ visto = m.v })
		]])
		enu.task.spawn(function()
			local t = { v = 7 }
			w:send(t)
			t.v = 999          -- muta DESPUÉS de enviar
			local r = w:recv()
			out = tostring(r.visto)
		end)`)
	if out != "7" {
		t.Fatalf("copia: got %q (el worker vio la mutación → cruzó una referencia)", out)
	}
}

// M12.5: enviar un valor no-serializable (una función) → EINVAL antes de ceder.
func TestWorkerSendNoSerializable(t *testing.T) {
	out := runMain(t, `
		out = "?"
		local w = enu.worker.spawn([[ enu.task.sleep(1) ]])
		enu.task.spawn(function()
			local ok, e = pcall(function() w:send({ fn = function() end }) end)
			out = tostring(ok) .. ":" .. tostring(e.code)
			w:terminate()
		end)`)
	if out != "false:EINVAL" {
		t.Fatalf("no-serializable: got %q", out)
	}
}

// M12.6: caps (G6) — dos granularidades y deny-by-default. Se prueban desde DENTRO
// del worker qué módulos/funciones existen.
func TestWorkerCapsGranularidades(t *testing.T) {
	reg := func(p *Pool) {
		p.Register("fs.read", func(inst *Instance, a []any) ([]any, error) { return []any{"r"}, nil })
		p.Register("fs.write", func(inst *Instance, a []any) ([]any, error) { return nil, nil })
		p.Register("http.get", func(inst *Instance, a []any) ([]any, error) { return []any{"g"}, nil })
	}
	probe := `[[
		enu.worker.parent.send({
			fs = (enu.fs ~= nil),
			fs_read = (enu.fs ~= nil and enu.fs.read ~= nil),
			fs_write = (enu.fs ~= nil and enu.fs.write ~= nil),
			http = (enu.http ~= nil),
			task = (enu.task ~= nil),
			ui = (enu.ui ~= nil),
			spawn = (enu.worker.spawn ~= nil),
			parent = (enu.worker.parent ~= nil),
		})
	]]`
	cases := []struct {
		name, opts, want string
	}{
		// sin caps: toda la API [W]; nunca ui ni worker.spawn.
		{"sin-caps", "", "fs=true http=true task=true ui=false spawn=false parent=true"},
		// caps={"fs"}: módulo fs entero; http no.
		{"modulo-fs", `, { caps = { "fs" } }`, "fs=true fs_read=true fs_write=true http=false"},
		// caps={"fs.read"}: sólo fs.read; fs.write no.
		{"fn-fs-read", `, { caps = { "fs.read" } }`, "fs=true fs_read=true fs_write=false http=false"},
		// caps={}: deny casi todo (fs/http fuera; task intrínseco al worker sí).
		{"caps-vacias", `, { caps = {} }`, "fs=false http=false task=true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := NewPool()
			if err != nil {
				t.Fatal(err)
			}
			reg(p)
			inst, err := p.NewInstance()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })
			setup := `
				out = ""
				local w = enu.worker.spawn(` + probe + c.opts + `)
				enu.task.spawn(function()
					local r = w:recv()
					out = r
				end)`
			if _, lerr, err := inst.Eval(setup); err != nil || lerr != "" {
				t.Fatalf("setup: lerr=%q err=%v", lerr, err)
			}
			if err := inst.RunTasks(context.Background()); err != nil {
				t.Fatalf("RunTasks: %v", err)
			}
			// lee el mapa de resultados y compón una cadena estable para comparar.
			checkCaps(t, inst, c.want)
		})
	}
}

// TestWorkerSinPlugin blinda que `plugin.*` NO cruza a los workers ni siquiera
// sin caps (api.md §13/§16: es solo estado principal). Antes se filtraba, y con
// él un `plugin.reload` cuyo HostFn cierra sobre el runtime principal: ejecutarlo
// desde la goroutine del worker re-entraba la VM principal en paralelo.
func TestWorkerSinPlugin(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	// Simula las primitivas que registerPluginWasm cuelga en el Pool principal.
	p.Register("plugin.current", func(inst *Instance, a []any) ([]any, error) { return []any{"x"}, nil })
	p.Register("plugin.list", func(inst *Instance, a []any) ([]any, error) { return []any{[]any{}}, nil })
	p.RegisterSuspending("plugin.reload", func(inst *Instance, a []any) ([]any, error) { return nil, nil })
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })

	setup := `
		out = ""
		local w = enu.worker.spawn([[
			enu.worker.parent.send({
				plugin = (enu.plugin ~= nil),
			})
		]])
		enu.task.spawn(function()
			out = w:recv()
		end)`
	if _, lerr, err := inst.Eval(setup); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	if err := inst.RunTasks(context.Background()); err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	checkCaps(t, inst, "plugin=false")
}

// checkCaps evalúa `want` (pares "k=bool") contra la tabla `out` del estado.
func checkCaps(t *testing.T, inst *Instance, want string) {
	t.Helper()
	for _, kv := range strings.Fields(want) {
		parts := strings.SplitN(kv, "=", 2)
		got, _, _ := inst.Eval(`return tostring(out.` + parts[0] + `)`)
		if got != parts[1] {
			t.Fatalf("cap %q: got %q want %q", parts[0], got, parts[1])
		}
	}
}

// M12.7: G8(a) — recv con on_message activo → EINVAL en el acto.
func TestWorkerRecvExcluyeOnMessage(t *testing.T) {
	out := runMain(t, `
		out = "?"
		local w = enu.worker.spawn([[ enu.task.sleep(100000) ]])
		enu.task.spawn(function()
			w:on_message(function(m) end)
			local ok, e = pcall(function() return w:recv() end)
			out = tostring(ok) .. ":" .. tostring(e.code)
			w:terminate()
		end)`)
	if out != "false:EINVAL" {
		t.Fatalf("G8(a): got %q", out)
	}
}

// M12.8: G8(c) — un segundo on_message → EINVAL.
func TestWorkerOnMessageSegundoRechazado(t *testing.T) {
	out := runMain(t, `
		out = "?"
		local w = enu.worker.spawn([[ enu.task.sleep(100000) ]])
		enu.task.spawn(function()
			w:on_message(function(m) end)
			local ok, e = pcall(function() return w:on_message(function(m) end) end)
			out = tostring(ok) .. ":" .. tostring(e.code)
			w:terminate()
		end)`)
	if out != "false:EINVAL" {
		t.Fatalf("G8(c): got %q", out)
	}
}

// M12.9: on_message entrega los mensajes en orden al callback del padre.
func TestWorkerOnMessageEntrega(t *testing.T) {
	out := runMain(t, `
		out = "?"
		local w = enu.worker.spawn([[
			for i = 1, 5 do enu.worker.parent.send(i) end
		]])
		local recibidos = {}
		enu.task.spawn(function()
			local sub = w:on_message(function(m) recibidos[#recibidos+1] = m end)
			-- espera a que lleguen los 5 y compón el resultado.
			while #recibidos < 5 do enu.task.sleep(1) end
			out = table.concat(recibidos, ",")
			sub.cancel()
			w:terminate()
		end)`)
	if out != "1,2,3,4,5" {
		t.Fatalf("on_message entrega: got %q", out)
	}
}

// M12.10: G15 — un worker quema CPU (bucle grande) SIN watchdog y completa.
func TestWorkerSinWatchdog(t *testing.T) {
	out := runMain(t, `
		out = "?"
		local w = enu.worker.spawn([[
			local s = 0
			for i = 1, 2000000 do s = s + 1 end
			enu.worker.parent.send(s)
		]])
		enu.task.spawn(function()
			out = tostring(w:recv())
		end)`)
	if out != "2000000" {
		t.Fatalf("sin watchdog: got %q", out)
	}
}

// M12.11: terminate interrumpe un BUCLE DE CPU PURO del worker (el linchpin: sin
// watchdog, sólo terminate lo corta — vía cancelación del ctx del Call en vuelo).
func TestWorkerTerminateInterrumpeBucleCPU(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })
	if _, lerr, err := inst.Eval(`
		W = enu.worker.spawn([[ while true do end ]])   -- bucle infinito de CPU
	`); err != nil || lerr != "" {
		t.Fatalf("spawn: lerr=%q err=%v", lerr, err)
	}
	time.Sleep(100 * time.Millisecond) // deja que el bucle arranque en su goroutine

	// terminate + espera al join, con un tope de tiempo: si no interrumpe, cuelga.
	doneCh := make(chan struct{})
	go func() {
		if _, _, err := inst.Eval(`W:terminate()`); err != nil {
			t.Errorf("terminate: %v", err)
		}
		// StopWorkers hace el wait() del join.
		p.StopWorkers()
		close(doneCh)
	}()
	select {
	case <-doneCh:
		// interrumpido y unido: OK
	case <-time.After(10 * time.Second):
		t.Fatal("terminate NO interrumpió el bucle de CPU del worker (colgado)")
	}
}

// M12.12: terminate de un worker NO afecta al padre (estados aislados); un segundo
// terminate es idempotente (no revienta).
func TestWorkerTerminateNoAfectaPadre(t *testing.T) {
	out := runMain(t, `
		out = "?"
		local w = enu.worker.spawn([[ enu.worker.parent.recv() ]])  -- se queda esperando
		enu.task.spawn(function()
			w:terminate()
			w:terminate()                 -- idempotente
			enu.task.sleep(5)
			out = "padre-vivo"            -- el padre sigue corriendo tras terminar el worker
		end)`)
	if out != "padre-vivo" {
		t.Fatalf("terminate afectó al padre: got %q", out)
	}
}

// M12.13: recv tras terminate sin mensajes pendientes → nil (fin de canal, no error).
func TestWorkerRecvTrasTerminate(t *testing.T) {
	out := runMain(t, `
		out = "?"
		local w = enu.worker.spawn([[ enu.task.sleep(100000) ]])
		enu.task.spawn(function()
			w:terminate()
			local m = w:recv()
			out = tostring(m)   -- nil
		end)`)
	if out != "nil" {
		t.Fatalf("recv tras terminate: got %q (se esperaba nil)", out)
	}
}

// --- A-07: el registro Pool.workers retira los workers terminados -------------
//
// Antes, registerWorker añadía al mapa y nadie borraba: cada spawn dejaba para
// siempre la struct, sus canales y la entrada del mapa (crecimiento monótono en
// procesos de larga vida). Ahora shutdown() —el único punto por el que pasa todo
// fin de worker, sea natural, terminate() o StopWorkers— se retira del registro.
// La degradación semántica DEBE preservarse: send sobre un worker retirado →
// ECLOSED, recv → nil (fin de canal), no EINVAL —el id fue válido, solo que ya no
// está—; un id que nunca existió sí es EINVAL.

// spawnRun replica lo que hace worker._spawn: crea el worker aislado, lo registra
// en el Pool (fija id/parent) y arranca su goroutine. Devuelve el worker y su id.
func spawnRun(t *testing.T, inst *Instance, src string) (*worker, int64) {
	t.Helper()
	w, err := inst.spawnWorker(src, nil, false, "")
	if err != nil {
		t.Fatalf("spawnWorker: %v", err)
	}
	id := inst.pool.registerWorker(w)
	go w.run(src)
	return w, id
}

// hostFn localiza una primitiva host registrada por su nombre. En la ruta de
// degradación de A-07 (worker ya retirado) las primitivas del handle Worker
// retornan SIN suspender, así que en el test se pueden invocar directamente.
func hostFn(p *Pool, name string) HostFn {
	for id, n := range p.reg.names {
		if n == name {
			return p.reg.fns[id]
		}
	}
	return nil
}

// workerCount lee el tamaño del registro bajo su lock.
func workerCount(p *Pool) int {
	p.workerMu.Lock()
	defer p.workerMu.Unlock()
	return len(p.workers)
}

// A-07(a): spawn+terminate repetido NO hace crecer el registro. Tras cada
// terminate+wait el worker está fuera del mapa; el registro vuelve a 0.
func TestWorkerRegistroNoCreceA07(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })

	const n = 50
	for i := 0; i < n; i++ {
		w, _ := spawnRun(t, inst, `enu.task.sleep(100000)`)
		w.terminate()
		w.wait() // espera al shutdown completo (que se retira del mapa)
		if c := workerCount(p); c != 0 {
			t.Fatalf("A-07: tras spawn+terminate #%d el registro tiene %d entradas (se esperaba 0)", i+1, c)
		}
	}
	// Los ids siguen siendo monótonos (no se reutilizan): sanity de que la retirada
	// borra la entrada, no reinicia el contador.
	if p.workerNext != n {
		t.Fatalf("A-07: workerNext=%d, se esperaba %d", p.workerNext, n)
	}
}

// A-07(b): send sobre un worker YA RETIRADO (terminado y reapeado) sigue dando
// ECLOSED, no EINVAL; un id que nunca existió sí es EINVAL.
func TestWorkerSendTrasTerminateReapedA07(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })

	w, id := spawnRun(t, inst, `enu.task.sleep(100000)`)
	w.terminate()
	w.wait() // reapeado: fuera del mapa
	if lw, known := p.lookupWorker(id); lw != nil || !known {
		t.Fatalf("A-07: lookupWorker(%d) = (%v, known=%v), se esperaba (nil, true)", id, lw, known)
	}

	send := hostFn(p, "worker._send")
	if send == nil {
		t.Fatal("no se encontró la primitiva worker._send")
	}
	// worker retirado → ECLOSED (degradación preservada).
	if _, err := send(inst, []any{id, map[string]any{"x": int64(1)}}); !isCode(err, "ECLOSED") {
		t.Fatalf("A-07: send tras terminate+reap → %v, se esperaba ECLOSED", err)
	}
	// id que nunca existió → EINVAL (se conserva la distinción conocido/forjado).
	if _, err := send(inst, []any{int64(99999), map[string]any{"x": int64(1)}}); !isCode(err, "EINVAL") {
		t.Fatalf("A-07: send con id forjado → %v, se esperaba EINVAL", err)
	}
}

// A-07(c): recv sobre un worker YA RETIRADO devuelve nil (fin de canal), sin
// error; un id que nunca existió sí es EINVAL.
func TestWorkerRecvTrasTerminateReapedA07(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })

	w, id := spawnRun(t, inst, `enu.task.sleep(100000)`)
	w.terminate()
	w.wait() // reapeado: fuera del mapa

	recv := hostFn(p, "worker._recv")
	if recv == nil {
		t.Fatal("no se encontró la primitiva worker._recv")
	}
	res, err := recv(inst, []any{id})
	if err != nil {
		t.Fatalf("A-07: recv tras terminate+reap dio error %v (se esperaba nil sin error)", err)
	}
	if len(res) != 1 || res[0] != nil {
		t.Fatalf("A-07: recv tras terminate+reap → %v, se esperaba [nil] (fin de canal)", res)
	}
	// id que nunca existió → EINVAL.
	if _, err := recv(inst, []any{int64(99999)}); !isCode(err, "EINVAL") {
		t.Fatalf("A-07: recv con id forjado → %v, se esperaba EINVAL", err)
	}
}

// A-07 (regresión): un worker que termina con mensajes BUFFERIZADOS hacia el
// padre NO se retira en shutdown —retirarlo perdería la cola: recv sobre un id
// retirado da nil inmediato—. La entrada sobrevive como zombie, recv drena
// todos los mensajes, y el fin de canal (nil) reapea la entrada. Blinda la
// promesa de recvOnChan («un mensaje encolado justo antes de terminar aún se
// entrega»), que la primera versión de A-07 rompió (TestWorkerOnMessageEntrega
// perdía el último mensaje).
func TestWorkerZombieDrenaYReapeaA07(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })

	// El worker manda 3 (caben en workerQueueCap) y retorna: fin natural.
	if _, lerr, err := inst.Eval(`
		out = "?"
		__w = enu.worker.spawn([[
			for i = 1, 3 do enu.worker.parent.send(i) end
		]])`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	// Espera el shutdown COMPLETO antes de leer: los 3 mensajes quedan
	// bufferizados y la entrada debe seguir en el registro (zombie).
	w, _ := p.lookupWorker(1)
	if w == nil {
		t.Fatal("A-07: el worker recién spawneado no está en el registro")
	}
	w.wait()
	if c := workerCount(p); c != 1 {
		t.Fatalf("A-07: tras terminar con mensajes pendientes el registro tiene %d entradas (se esperaba 1, zombie)", c)
	}

	// Drena: los 3 mensajes se entregan en orden y el nil final reapea.
	if _, lerr, err := inst.Eval(`
		enu.task.spawn(function()
			local got = {}
			while true do
				local m = __w:recv()
				if m == nil then break end
				got[#got+1] = m
			end
			out = table.concat(got, ",")
		end)`); err != nil || lerr != "" {
		t.Fatalf("drenado: lerr=%q err=%v", lerr, err)
	}
	if err := inst.RunTasks(context.Background()); err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	if out, _, _ := inst.Eval(`return tostring(out)`); out != "1,2,3" {
		t.Fatalf("A-07: se drenó %q, se esperaba \"1,2,3\"", out)
	}
	if c := workerCount(p); c != 0 {
		t.Fatalf("A-07: tras drenar el registro tiene %d entradas (se esperaba 0)", c)
	}
}

// A-07(d): StopWorkers retira todas las entradas; el fin NATURAL (un módulo que
// retorna solo) también, vía el mismo shutdown().
func TestWorkerStopWorkersYFinNaturalRetiranA07(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })

	// Cinco workers vivos, bloqueados esperando al padre.
	for i := 0; i < 5; i++ {
		spawnRun(t, inst, `enu.worker.parent.recv()`)
	}
	if c := workerCount(p); c != 5 {
		t.Fatalf("A-07: se esperaban 5 workers vivos, hay %d", c)
	}
	p.StopWorkers()
	if c := workerCount(p); c != 0 {
		t.Fatalf("A-07: StopWorkers dejó %d entradas (se esperaba 0)", c)
	}

	// Fin natural: un módulo que retorna de inmediato se retira él solo.
	w, _ := spawnRun(t, inst, `return 1`)
	w.wait()
	if c := workerCount(p); c != 0 {
		t.Fatalf("A-07: fin natural dejó %d entradas (se esperaba 0)", c)
	}
}

// isCode comprueba que err es un StructuredError con el código dado.
func isCode(err error, code string) bool {
	se, ok := err.(*StructuredError)
	return ok && se.Code == code
}
