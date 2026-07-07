package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Tests de S35 — `Worker:on_message` (excluyente con `recv`, G8) + tasks/timers/
// futures DENTRO del worker (G15) + `terminate` inmediato y seguro (§13). Cierra la
// Fase 7 (CP-8). La lógica 🔒 a blindar (todos nombran su hallazgo):
//
//   - **EXCLUSIVIDAD G8**: con un `recv` pendiente, `on_message` → `EINVAL` en el
//     acto; con un `on_message` registrado, `recv` → `EINVAL` en el acto; tras
//     cancelar el `on_message`, `recv` vuelve a funcionar. Nunca prioridad silenciosa.
//   - **on_message entrega**: los mensajes del worker llegan a `fn(msg)` en orden, en
//     el estado principal, bajo `pcall` (un `fn` que lanza no rompe el drenado).
//     Cancelar el `Sub` deja de entregar.
//   - **tasks/timers/futures DENTRO del worker (G15)**: un worker corre varias tasks
//     (spawn/await), un timer (`every`/`sleep`) y un future; sin watchdog.
//   - **terminate**: corta el worker (incl. tasks suspendidas) inmediato, no afecta al
//     padre, idempotente, sin fuga.

// pollEval reintenta `code` hasta que su único valor de retorno sea `want` o se agote
// el plazo. Lo usan los tests de `on_message`: la entrega es ASÍNCRONA (el drenador es
// una goroutine de fondo, como un `every`, que no cuenta para la quiescencia de
// `eval`), así que el resultado no está listo al volver de `eval` —hay que sondear—.
func pollEval(h *harness, code, want string) bool {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := h.eval(code)
		if len(got) == 1 && got[0] == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestWorkerOnMessageExclusiveWithRecvPending (G8) blinda que registrar un
// `on_message` MIENTRAS hay un `Worker:recv` suspendido lanza `EINVAL` EN EL ACTO
// (nunca prioridad silenciosa). El worker no envía nada, así que el `recv` queda
// pendiente; el `on_message` que se intenta a continuación debe rechazarse.
func TestWorkerOnMessageExclusiveWithRecvPending(t *testing.T) {
	h := workerHarness(t, `nu.task.sleep(60000)`) // el worker no envía: el recv queda pendiente
	h.eval(`
		ERRCODE, EDONE = nil, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			-- Un recv que nunca completa (el worker no envía): queda suspendido.
			local recver = nu.task.spawn(function() w:recv() end)
			nu.task.sleep(10) -- deja que el recv tome recvPending y se suspenda
			local ok, e = pcall(function() w:on_message(function() end) end)
			ERRCODE = (not ok) and e.code or "no-lanzo"
			recver:cancel()
			w:terminate()
			EDONE = true
		end)
	`)
	h.expectEval(`return tostring(EDONE)`, "true")
	h.expectEval(`return ERRCODE`, CodeEINVAL)
}

// TestWorkerRecvExclusiveWithOnMessage (G8) blinda la dirección inversa: hacer un
// `Worker:recv` MIENTRAS hay un `on_message` registrado lanza `EINVAL` en el acto.
func TestWorkerRecvExclusiveWithOnMessage(t *testing.T) {
	h := workerHarness(t, `nu.task.sleep(60000)`)
	h.eval(`
		ERRCODE2, E2DONE = nil, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			local sub = w:on_message(function() end)
			local ok, e = pcall(function() w:recv() end)
			ERRCODE2 = (not ok) and e.code or "no-lanzo"
			sub:cancel()
			w:terminate()
			E2DONE = true
		end)
	`)
	h.expectEval(`return tostring(E2DONE)`, "true")
	h.expectEval(`return ERRCODE2`, CodeEINVAL)
}

// TestWorkerOnMessageSecondRejected (G8) blinda que registrar un SEGUNDO `on_message`
// con uno ya activo lanza `EINVAL` (uno a la vez: un único consumidor del canal).
func TestWorkerOnMessageSecondRejected(t *testing.T) {
	h := workerHarness(t, `nu.task.sleep(60000)`)
	h.eval(`
		ERRCODE3, E3DONE = nil, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			local sub = w:on_message(function() end)
			local ok, e = pcall(function() w:on_message(function() end) end)
			ERRCODE3 = (not ok) and e.code or "no-lanzo"
			sub:cancel()
			w:terminate()
			E3DONE = true
		end)
	`)
	h.expectEval(`return tostring(E3DONE)`, "true")
	h.expectEval(`return ERRCODE3`, CodeEINVAL)
}

// TestWorkerRecvAfterOnMessageCancel (G8) blinda que tras CANCELAR el `on_message`,
// el worker queda libre para volver a `recv` —cancelar libera el canal—. El worker
// envía un mensaje; el padre, tras cancelar el `on_message`, lo recibe por `recv`.
func TestWorkerRecvAfterOnMessageCancel(t *testing.T) {
	h := workerHarness(t, `
		nu.worker.parent.recv()           -- espera la señal del padre
		nu.worker.parent.send("tras-cancel")
	`)
	h.eval(`
		GOTBACK, RCDONE = nil, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			local sub = w:on_message(function() end)
			sub:cancel()                    -- libera el worker para recv (G8)
			-- recv ya no lanza EINVAL: el on_message se canceló.
			w:send("go")                    -- destraba el worker
			GOTBACK = w:recv()
			w:terminate()
			RCDONE = true
		end)
	`)
	h.expectEval(`return tostring(RCDONE)`, "true")
	h.expectEval(`return GOTBACK`, "tras-cancel")
}

// TestWorkerOnMessageDelivery blinda la ENTREGA: los mensajes del worker llegan a
// `fn(msg)` EN ORDEN, en el estado principal. El worker envía 1..5; el handler los
// acumula en una global del estado principal. La entrega es asíncrona (drenador de
// fondo), así que se sondea hasta que llegan los cinco.
func TestWorkerOnMessageDelivery(t *testing.T) {
	h := workerHarness(t, `
		for i = 1, 5 do nu.worker.parent.send(i) end
	`)
	h.eval(`
		ACC = {}
		W = nu.worker.spawn("wmod")
		SUB = W:on_message(function(msg) ACC[#ACC+1] = msg end)
	`)
	if !pollEval(h, `return tostring(#ACC)`, "5") {
		t.Fatalf("on_message no entregó los 5 mensajes: #ACC=%v", h.eval(`return tostring(#ACC)`))
	}
	// Orden preservado: 1,2,3,4,5.
	h.expectEval(`return table.concat(ACC, ",")`, "1,2,3,4,5")
	h.eval(`SUB:cancel(); W:terminate()`)
}

// TestWorkerOnMessageHandlerThrows blinda que un `fn` que LANZA no rompe el drenado
// (pcall por frontera, ADR-008): los mensajes siguientes se siguen entregando. El
// handler lanza en el primer mensaje pero cuenta el resto.
func TestWorkerOnMessageHandlerThrows(t *testing.T) {
	h := workerHarness(t, `
		for i = 1, 4 do nu.worker.parent.send(i) end
	`)
	h.eval(`
		COUNT, THREW = 0, false
		W2 = nu.worker.spawn("wmod")
		SUB2 = W2:on_message(function(msg)
			COUNT = COUNT + 1
			if msg == 1 then THREW = true; error({ code = "EINVAL", message = "boom" }) end
		end)
	`)
	// Pese a que el handler lanza en el primer mensaje, los cuatro se entregan.
	if !pollEval(h, `return tostring(COUNT)`, "4") {
		t.Fatalf("un handler que lanza rompió el drenado: COUNT=%v", h.eval(`return tostring(COUNT)`))
	}
	h.expectEval(`return tostring(THREW)`, "true")
	h.eval(`SUB2:cancel(); W2:terminate()`)
}

// TestWorkerInternalTasksTimersFutures (G15) blinda que el worker es un mini-runtime
// COMPLETO: dentro corren `nu.task.spawn`/`await`, `nu.task.sleep` (timer ⏸),
// `nu.task.every` (timer periódico) y `nu.task.future` (set/await), todo `nu.task`
// [W], SIN watchdog. El worker orquesta las cuatro cosas y devuelve un digesto.
func TestWorkerInternalTasksTimersFutures(t *testing.T) {
	h := workerHarness(t, `
		-- (1) varias tasks con spawn/await
		local a = nu.task.spawn(function() return 10 end)
		local b = nu.task.spawn(function() nu.task.sleep(5); return 20 end)
		local sum = a:await() + b:await()         -- 30

		-- (2) un future: una task lo resuelve, otra lo espera
		local f = nu.task.future()
		nu.task.spawn(function() nu.task.sleep(5); f:set(7) end)
		local fv = f:await()                       -- 7

		-- (3) un timer periódico every: cuenta 3 ticks y se para
		local ticks = 0
		local done = nu.task.future()
		local timer = nu.task.every(2, function()
			ticks = ticks + 1
			if ticks >= 3 then done:set(true) end
		end)
		done:await()
		timer:stop()

		nu.worker.parent.send({ sum = sum, fv = fv, ticks = ticks })
	`)
	h.eval(`
		DIG, IDONE = nil, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			DIG = w:recv()
			w:terminate()
			IDONE = true
		end)
	`)
	h.expectEval(`return tostring(IDONE)`, "true")
	h.expectEval(`return tostring(DIG.sum)`, "30")
	h.expectEval(`return tostring(DIG.fv)`, "7")
	h.expectEval(`return tostring(DIG.ticks >= 3)`, "true")
}

// TestWorkerTerminateDoesNotAffectParent blinda que `terminate()` corta el worker
// (incluida una task suspendida en un `recv` que nunca llega) SIN afectar al padre: el
// padre sigue ejecutando con normalidad tras el `terminate`, y un segundo `terminate`
// es idempotente (no entra en pánico).
func TestWorkerTerminateDoesNotAffectParent(t *testing.T) {
	h := workerHarness(t, `nu.worker.parent.recv()`) // task suspendida en recv que no llega
	h.eval(`
		PARENT_OK, TT_DONE = false, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod")
			nu.task.sleep(10)
			w:terminate()
			w:terminate()          -- idempotente: no debe lanzar
			-- El padre sigue: lanza otra task que progresa con normalidad.
			local t2 = nu.task.spawn(function() nu.task.sleep(5); return 99 end)
			PARENT_OK = (t2:await() == 99)
			TT_DONE = true
		end)
	`)
	h.expectEval(`return tostring(TT_DONE)`, "true")
	h.expectEval(`return tostring(PARENT_OK)`, "true")
}

// TestCP8WorkerIndexesRepo es el CHECKPOINT CP-8 (cierra la Fase 7): prueba de humo de
// paralelismo real + sandbox por capacidades. Un worker con `caps={"fs.read","search"}`
// indexa un repo de prueba (lo recorre con `nu.search.files`, lee con `nu.fs.read`) y
// devuelve un DIGESTO al estado principal. DENTRO del worker, `nu.fs.write` y `nu.ui`
// NO existen (deny-by-default, G6). Además: `send` suspende al llenar la cola acotada
// (backpressure, coherente con CP-5) y `terminate` a mitad no afecta al padre.
func TestCP8WorkerIndexesRepo(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	dataDir := t.TempDir()

	// Un "repo" de prueba: un plugin `p` con su `lua/wmod.lua` (el cuerpo del worker)
	// y, dentro, un subárbol `repo/` con ficheros a indexar.
	escribir := func(path, contenido string) {
		if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	dir := filepath.Join(root, "p")
	if err := os.MkdirAll(filepath.Join(dir, "lua"), 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	escribir(filepath.Join(dir, "plugin.toml"), "name=\"p\"\nversion=\"1.0\"\n")
	escribir(filepath.Join(dir, "init.lua"), "")

	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	escribir(filepath.Join(repo, "a.txt"), "alpha\nbeta\n")
	escribir(filepath.Join(repo, "b.txt"), "gamma\n")
	escribir(filepath.Join(repo, "sub", "c.txt"), "delta\nepsilon\nzeta\n")

	// El cuerpo del worker: recorre el repo con search.files, lee cada fichero con
	// fs.read, suma bytes y líneas, y devuelve el digesto. Comprueba ANTES que
	// fs.write y ui NO existen (deny-by-default, G6: solo se concedió fs.read+search).
	wmod := `
		assert(nu.fs.read ~= nil, "fs.read deberia existir")
		assert(nu.fs.write == nil, "fs.write NO deberia existir (deny-by-default)")
		assert(nu.ui == nil, "nu.ui NO deberia existir en un worker")
		assert(nu.search ~= nil, "search deberia existir")

		local root = nu.worker.parent.recv()   -- el padre manda la raíz del repo
		local files = nu.search.files(root)
		local nbytes, nlines, nfiles = 0, 0, 0
		for _, f in ipairs(files) do
			local content = nu.fs.read(f)
			nbytes = nbytes + #content
			nfiles = nfiles + 1
			for _ in string.gmatch(content, "\n") do nlines = nlines + 1 end
		end
		nu.worker.parent.send({ files = nfiles, bytes = nbytes, lines = nlines })
	`
	escribir(filepath.Join(dir, "lua", "wmod.lua"), wmod)

	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithPluginDir(root), WithForceUI(true))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}

	h.eval(`
		DIGEST, CPDONE = nil, false
		REPO = ` + luaString(repo) + `
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod", { caps = {"fs.read", "search"} })
			w:send(REPO)
			DIGEST = w:recv()
			w:terminate()
			CPDONE = true
		end)
	`)
	h.expectEval(`return tostring(CPDONE)`, "true")
	// 3 ficheros, 6 líneas (2 en a.txt + 1 en b.txt + 3 en sub/c.txt).
	h.expectEval(`return tostring(DIGEST.files)`, "3")
	h.expectEval(`return tostring(DIGEST.lines)`, "6")

	// Segundo worker: terminate a mitad no afecta al padre (el padre sigue indexando con
	// otro worker o, más simple, lanza una task que progresa tras el corte).
	h.eval(`
		AFTER_OK, A2DONE = false, false
		nu.task.spawn(function()
			local w = nu.worker.spawn("wmod", { caps = {"fs.read", "search"} })
			w:terminate()   -- corta antes de enviarle la raíz: no afecta al padre
			local t = nu.task.spawn(function() nu.task.sleep(5); return 123 end)
			AFTER_OK = (t:await() == 123)
			A2DONE = true
		end)
	`)
	h.expectEval(`return tostring(A2DONE)`, "true")
	h.expectEval(`return tostring(AFTER_OK)`, "true")
}

// luaString rinde una cadena Go a un literal Lua entrecomillado seguro para inyectarla
// en un snippet (escapa comillas y backslashes). Lo usa CP-8 para pasar la ruta del
// repo (que en Windows/CI podría llevar caracteres a escapar).
func luaString(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		switch r {
		case '\\':
			out = append(out, '\\', '\\')
		case '"':
			out = append(out, '\\', '"')
		default:
			out = append(out, r)
		}
	}
	out = append(out, '"')
	return string(out)
}
