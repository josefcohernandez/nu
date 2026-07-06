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
		local w = nu.worker.spawn([[
			local m = nu.worker.parent.recv()
			nu.worker.parent.send({ text = m.text, n = m.n * 2 })
		]])
		nu.task.spawn(function()
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
		local w = nu.worker.spawn([[
			GLOBAL_WORKER = "soy-worker"
			-- el global del padre no existe aquí:
			nu.worker.parent.send({ ve_padre = (GLOBAL_PADRE == nil), yo = GLOBAL_WORKER })
		]])
		nu.task.spawn(function()
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
		local w = nu.worker.spawn([[ nu.worker.parent.recv() ; nu.task.sleep(100000) ]])
		local enviados = 0
		local acabado = false
		nu.task.spawn(function()
			for i = 1, 1000 do
				w:send({ i = i })
				enviados = enviados + 1
			end
			acabado = true
		end)
		nu.task.spawn(function()
			nu.task.sleep(80)   -- tiempo de sobra para llenar la cola y bloquear
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
		local w = nu.worker.spawn([[
			local m = nu.worker.parent.recv()
			nu.worker.parent.send({ visto = m.v })
		]])
		nu.task.spawn(function()
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
		local w = nu.worker.spawn([[ nu.task.sleep(1) ]])
		nu.task.spawn(function()
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
		nu.worker.parent.send({
			fs = (nu.fs ~= nil),
			fs_read = (nu.fs ~= nil and nu.fs.read ~= nil),
			fs_write = (nu.fs ~= nil and nu.fs.write ~= nil),
			http = (nu.http ~= nil),
			task = (nu.task ~= nil),
			ui = (nu.ui ~= nil),
			spawn = (nu.worker.spawn ~= nil),
			parent = (nu.worker.parent ~= nil),
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
				local w = nu.worker.spawn(` + probe + c.opts + `)
				nu.task.spawn(function()
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
		local w = nu.worker.spawn([[ nu.task.sleep(100000) ]])
		nu.task.spawn(function()
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
		local w = nu.worker.spawn([[ nu.task.sleep(100000) ]])
		nu.task.spawn(function()
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
		local w = nu.worker.spawn([[
			for i = 1, 5 do nu.worker.parent.send(i) end
		]])
		local recibidos = {}
		nu.task.spawn(function()
			local sub = w:on_message(function(m) recibidos[#recibidos+1] = m end)
			-- espera a que lleguen los 5 y compón el resultado.
			while #recibidos < 5 do nu.task.sleep(1) end
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
		local w = nu.worker.spawn([[
			local s = 0
			for i = 1, 2000000 do s = s + 1 end
			nu.worker.parent.send(s)
		]])
		nu.task.spawn(function()
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
		W = nu.worker.spawn([[ while true do end ]])   -- bucle infinito de CPU
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
		local w = nu.worker.spawn([[ nu.worker.parent.recv() ]])  -- se queda esperando
		nu.task.spawn(function()
			w:terminate()
			w:terminate()                 -- idempotente
			nu.task.sleep(5)
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
		local w = nu.worker.spawn([[ nu.task.sleep(100000) ]])
		nu.task.spawn(function()
			w:terminate()
			local m = w:recv()
			out = tostring(m)   -- nil
		end)`)
	if out != "nil" {
		t.Fatalf("recv tras terminate: got %q (se esperaba nil)", out)
	}
}
