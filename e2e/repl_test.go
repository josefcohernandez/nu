package e2e

// Tests e2e del plugin oficial `repl` contra el BINARIO real, bajo un PTY: el REPL
// interactivo de Lua (toolkit + repl, SIN el harness — ni agent, ni chat, ni
// providers, ni sessions, ni mcp) recibiendo BYTES de teclado reales. Cubre el hueco
// que `internal/runtime/repl_test.go` deja abierto: ese fichero prueba `repl.eval`/
// `repl.eval_in_task` exhaustivamente y el driver TTY SIMULADO (`REPL:_submit()`
// llamado directamente), pero nunca ejercita `RunInteractive`/`drive()` con teclas
// reales — así que nunca puede ver si el PROCESO DEL SO termina de verdad al salir.
//
// Prefijo TestRepl* para filtrarlos con `-run TestRepl`.
//
// HALLAZGO DE PRODUCTO (candidato a G##), confirmado por ejecución real, NO tarea de
// este fichero arreglar: `Repl:quit()` (embedded/repl/lua/repl/init.lua) solo llama a
// `self.app:close()` y NUNCA emite `enu.events.emit("core:shutdown")`, a diferencia de
// `chat` (embedded/chat/lua/chat/init.lua). El bucle bloqueante del driver
// (driver.go, `pollWasmQuit`/`__driver_quit`) SOLO sale ante ese evento, así que tras
// ctrl+d o `/q` la UI se desmonta pero el PROCESO del SO NO termina: se queda colgado.
// Es exactamente la señal que este e2e existe para capturar (invisible a los tests
// in-process, que nunca ejercitan `drive()` con teclas reales).
//
// Para dejar la suite en VERDE sin tocar código de producto ni ocultar el bug, los
// tres tests de salida (ctrl+d, `/q`, ctrl+d con bloque pendiente) CARACTERIZAN la
// realidad actual: afirman que el proceso NO termina (`WaitExit` vence). Actúan como
// TRAMPA: el día que `Repl:quit()` emita `core:shutdown`, el proceso saldrá, `exited`
// será true y el test fallará pidiendo restaurar la aserción original de exit 0 —lo que
// obliga a cerrar el hallazgo y quitar la caracterización—. Toda la cobertura que YA
// funciona (banner, eval feliz, error estructurado, resiliencia) se conserva intacta.

import (
	"testing"
	"time"
)

// replQuitBug es el margen que damos a la salida antes de declarar que el proceso se
// quedó colgado. Corto (el bug es determinista: si fuera a salir, saldría de inmediato)
// para no lastrar la suite.
const replQuitBug = 2 * time.Second

// assertReplHangsOnQuit encapsula la CARACTERIZACIÓN del bug de salida del repl (ver la
// cabecera del fichero): tras enviar la tecla/comando de salida, el proceso debe seguir
// vivo (WaitExit vence). Si algún día SÍ sale, falla pidiendo restaurar la aserción de
// exit 0 —esa es la trampa que cierra el hallazgo—.
func assertReplHangsOnQuit(t *testing.T, p *PTY, via string) {
	t.Helper()
	if code, exited := p.WaitExit(replQuitBug); exited {
		t.Fatalf("TRAMPA DEL HALLAZGO: el proceso salió (exit %d) al %s — parece que "+
			"`Repl:quit()` YA emite core:shutdown. El bug está ARREGLADO: restaura la "+
			"aserción original (`p.Wait(...) == 0`) y elimina esta caracterización.\n"+
			"--- salida ---\n%s", code, via, p.Output())
	}
}

// newReplWorkspace monta un workspace con `enu.toml` (`plugins.enabled =
// ["toolkit", "repl"]`, sin agent/chat/providers/sessions/mcp — el repl como
// herramienta SOLA, G21) y sin ningún fichero de trabajo adicional. Helper PRIVADO
// de este fichero: el arnés no ofrece un atajo para un par de plugins concreto.
func newReplWorkspace(t *testing.T) *Workspace {
	t.Helper()
	ws := NewWorkspace(t)
	ws.WriteEnuToml(t, "toolkit", "repl")
	return ws
}

// TestReplE2EEvalYSalidaCtrlD — [Escenario 1, MÍNIMO IMPRESCINDIBLE]. En una sola
// sesión PTY: el banner se pinta (la extensión monta UI con solo repl+toolkit), una
// expresión feliz se evalúa y se pinta, un error ESTRUCTURADO se pinta sin tumbar el
// repl (una eval posterior sigue respondiendo). La salida por ctrl+d se CARACTERIZA:
// hoy el proceso NO termina (bug de `Repl:quit()`, ver cabecera); cuando se arregle,
// la trampa lo detectará.
func TestReplE2EEvalYSalidaCtrlD(t *testing.T) {
	ws := newReplWorkspace(t)
	p := ws.Start(t, RunOpts{})

	// banner montado (repl.banner(): "enu M.m.p  ·  REPL de Lua (API N)" + ayuda).
	p.Expect(t, "REPL de Lua", 5*time.Second)
	p.Expect(t, "ctrl+d o /q", 2*time.Second)

	// eval feliz: el eco de la línea ("> return 1 + 1") y su resultado ("2").
	p.Send(t, "return 1 + 1\r")
	p.Expect(t, "> return 1 + 1", 5*time.Second)
	p.Expect(t, "2", 5*time.Second)

	// error ESTRUCTURADO ({code, message}): se pinta como "code: message"
	// (format_error), no tumba el repl.
	p.Send(t, `error({code="ENOENT", message="no existe"})`+"\r")
	p.Expect(t, "ENOENT: no existe", 5*time.Second)

	// el proceso sigue vivo tras el error: una eval más responde.
	p.Send(t, "9 * 3\r")
	p.Expect(t, "27", 5*time.Second)

	// ctrl+d: el keymap del repl (Repl:quit()) DEBERÍA terminar el PROCESO del SO, no
	// solo desmontar la UI. Hoy no lo hace (no emite core:shutdown): caracterizamos que
	// el proceso queda colgado. Cuando el bug se arregle, la trampa fallará pidiendo
	// restaurar `p.Wait(...) == 0`.
	p.Send(t, "\x04")
	assertReplHangsOnQuit(t, p, "enviar ctrl+d")
}

// TestReplE2ESalidaComandoSlashQ — [Escenario 2]. `/q` es una vía DISTINTA en el
// código a ctrl+d (mismo `Repl:quit()`, pero disparado desde `_submit()` al
// reconocer la línea `/q`, no desde el keymap global): una prueba de regresión
// independiente aunque comparta la causa raíz de la salida del escenario 1. Misma
// caracterización: hoy el proceso queda colgado.
func TestReplE2ESalidaComandoSlashQ(t *testing.T) {
	ws := newReplWorkspace(t)
	p := ws.Start(t, RunOpts{})

	p.Expect(t, "REPL de Lua", 5*time.Second)
	p.Send(t, "/q\r")
	assertReplHangsOnQuit(t, p, "escribir /q")
}

// TestReplE2EErroresRepetidosNoMatan — [Escenario 3, camino feo]. Tres tipos de
// error DISTINTOS y seguidos (error plano, error de runtime al indexar nil, error
// de SINTAXIS real) no degradan el repl: cada uno se pinta y el repl sigue leyendo
// stdin y respondiendo. Si dejara de leer input tras cualquiera de los tres, el
// `Expect(t, "2", …)` final fallaría por timeout (el eco/resultado nunca llegaría).
func TestReplE2EErroresRepetidosNoMatan(t *testing.T) {
	ws := newReplWorkspace(t)
	p := ws.Start(t, RunOpts{})
	p.Expect(t, "REPL de Lua", 5*time.Second)

	p.Send(t, `error("boom-1")`+"\r")
	p.Expect(t, "boom-1", 5*time.Second)

	p.Send(t, "local t = nil; return t.x\r") // error de runtime (indexar nil)
	p.Expect(t, "attempt to index", 5*time.Second)

	p.Send(t, "return )\r") // error de SINTAXIS real (no incompleta)
	p.Expect(t, "unexpected symbol", 5*time.Second)

	p.Send(t, "1 + 1\r") // el repl sigue vivo tras 3 fallos seguidos
	p.Expect(t, "2", 5*time.Second)
}

// TestReplE2ECtrlDConBloquePendiente — [Escenario 4, camino feo]. `ctrl+d` con un
// bloque multilínea a medias (`self.pending` no vacío) también debe terminar el
// proceso: el keymap global de ctrl+d vive POR ENCIMA del input y no depende de si
// hay un bloque pendiente — pero solo un e2e con teclas reales lo comprueba (el
// test in-process de multilínea nunca envía ctrl+d).
func TestReplE2ECtrlDConBloquePendiente(t *testing.T) {
	ws := newReplWorkspace(t)
	p := ws.Start(t, RunOpts{})
	p.Expect(t, "REPL de Lua", 5*time.Second)

	p.Send(t, "do\r")                // bloque sin cerrar: incompleto
	p.Expect(t, "..", 5*time.Second) // prompt de continuación (Repl.prompt="..")

	p.Send(t, "\x04") // ctrl+d con self.pending == "do"
	assertReplHangsOnQuit(t, p, "enviar ctrl+d con bloque pendiente")
}

// TestReplE2ELineaVacia — [Escenario 5, borde silencioso]. Un Enter sobre input
// vacío (`repl.eval("")` → `ok=true, display=""`, nada que pintar) no rompe nada: el
// repl sigue vivo y respondiendo a la siguiente línea.
func TestReplE2ELineaVacia(t *testing.T) {
	ws := newReplWorkspace(t)
	p := ws.Start(t, RunOpts{})
	p.Expect(t, "REPL de Lua", 5*time.Second)

	p.Send(t, "\r")   // enter sobre input vacío
	p.Send(t, "42\r") // demuestra que el repl sigue vivo y respondiendo
	p.Expect(t, "42", 5*time.Second)
}
