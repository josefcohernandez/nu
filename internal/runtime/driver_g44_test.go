package runtime

// Test 🔒 de G44 en el DRIVER: la manifestación A-34 de la auditoría
// (docs/auditoria-2026-07-12.md). Antes de G44, `drive()` solo hacía
// FeedInput/Eval/flushFrame: una task spawneada desde un keymap (exactamente
// cómo la extensión `chat` lanza el turno del agente) encolaba y NADIE la
// reanudaba jamás — la killer app no podía correr sobre el TTY. Con el bombeo
// continuo (PumpTasks junto a drive), el patrón keymap → nu.task.spawn →
// primitiva ⏸ → repintado funciona de punta a punta, y el input sigue
// respondiendo mientras la task duerme en el fondo.

import (
	"io"
	"testing"
	"time"
)

func TestG44DriverEjecutaTaskDeKeymap(t *testing.T) {
	h := newHarnessUI(t, 24, 4)
	if err := h.rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}

	// La "app" Lua: `t` spawnea una task que SUSPENDE (sleep, primitiva ⏸) y al
	// reanudarse repinta — el esqueleto del turno del agente del chat (spawn +
	// IO suspendiente + blit del resultado). `up` es el keymap síncrono de
	// control (el input debe responder mientras la task duerme). `q` apaga.
	h.eval(`
		local r = nu.ui.region({ x = 0, y = 0, w = 24, h = 4 })
		local teclas = 0
		r:blit(0, 0, nu.ui.block({ "esperando" }))
		nu.ui.keymap("t", function()
			nu.task.spawn(function()
				nu.task.sleep(30)
				r:clear()
				r:blit(0, 0, nu.ui.block({ "turno-hecho" }))
			end)
		end)
		nu.ui.keymap("up", function()
			teclas = teclas + 1
			r:blit(0, 1, nu.ui.block({ "teclas=" .. teclas }))
		end)
		nu.ui.keymap("q", function() nu.events.emit("core:shutdown") end)
	`)

	inR, inW := io.Pipe()
	out := &syncBuf{}
	d := newDriver(h.rt, inR, out)
	d.installShutdownHandler()
	d.attachOutput()

	done := make(chan struct{})
	go func() { d.drive(); close(done) }()

	if !waitForOut(out, "esperando", time.Second) {
		t.Fatalf("frame inicial ausente; salida:\n%q", out.String())
	}

	// (a) `t` lanza la task. Sin el bombeo de G44 se encolaba para siempre y la
	// pantalla nunca cambiaba (este Fatal era el comportamiento del repo).
	if _, err := inW.Write([]byte("t")); err != nil {
		t.Fatalf("write t: %v", err)
	}

	// (b) MIENTRAS la task duerme sus 30 ms en el fondo, el input responde: una
	// tecla síncrona repinta sin esperar a que la task termine.
	if _, err := inW.Write([]byte("\x1b[A")); err != nil {
		t.Fatalf("write ↑: %v", err)
	}
	if !waitForRow(h, 1, "teclas=1", time.Second) {
		t.Fatal("el input no respondió mientras la task del keymap dormía en el fondo")
	}

	// (c) La task reanudada tras su sleep repinta: el turno completó sobre el
	// driver. El repintado lo entrega el painter periódico o el flush de la
	// próxima tecla; se sondea el estado compuesto (lo que ve el usuario).
	if !waitForRow(h, 0, "turno-hecho", 2*time.Second) {
		t.Fatalf("la task spawneada desde el keymap no corrió bajo el driver (fila 0 = %q, want %q)", readRow(h, 0), "turno-hecho")
	}

	// (d) Apagado limpio: `q` cierra el bucle Y el bombeo (drive espera a que
	// PumpTasks retorne antes de volver — si colgara, este timeout lo caza).
	if _, err := inW.Write([]byte("q")); err != nil {
		t.Fatalf("write q: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drive() no retornó tras core:shutdown (¿el bombeo no se apagó?)")
	}
	_ = inW.Close()
}
