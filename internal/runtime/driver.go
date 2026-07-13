package runtime

// Driver de TTY (api.md §9, §4, sesión S33, CP-7 manual). Es la pieza que conecta el
// `nu.ui` —compositor, regiones, pila de input, eventos `ui:*`, todo construido y
// probado headless en S22–S32— a un **terminal de verdad**. Hasta S33 ese cableado no
// existía: el compositor componía el frame en memoria (`comp.enc`) pero nada lo enviaba
// a la pantalla, y la pila de input (`feedInput`) tenía su lógica pero **ninguna fuente
// de bytes** —los comentarios de input.go/ui.go la llamaban "el driver (S33+)" y la
// daban por pendiente—. Sin esto, un programa Lua podía `nu.ui.region`/`blit`/`keymap`
// y no se veía ni respondía nada: la UI no era funcional. Este fichero la cierra.
//
// QUÉ HACE EL DRIVER (las cuatro conexiones del CP-7):
//   1. SALIDA: pone stdout en alt-screen y, vía `attachOutput`, fija `rt.ui.out` para
//      que el painter (ui.go) vuelque cada frame al terminal.
//   2. ENTRADA: pone stdin en raw mode y lee sus bytes en una goroutine, los parsea con
//      `decodeInput` (tty.go) y los inyecta en la pila por `feedInput` bajo el token.
//   3. TAMAÑO: atiende `SIGWINCH` y reaplica el tamaño real (`resizeUI` → `ui:resize`).
//   4. CICLO DE VIDA: bloquea hasta que se pide apagar (una señal de terminación, o un
//      `core:shutdown` que emita la propia UI Lua) y entonces restaura el terminal.
//
// LO TESTEABLE Y LO QUE NO. Igual que input.go separó la lógica 🔂 (probada con eventos
// inyectados) de la fuente real, aquí el BUCLE del driver (`drive`) trabaja contra un
// `io.Reader`/`io.Writer` cualesquiera, así que un test lo conduce con tuberías en
// memoria y comprueba el ida y vuelta completo (tecla inyectada → handler Lua → frame
// ANSI en el writer). La cáscara que de verdad necesita un terminal —`term.MakeRaw`,
// `term.GetSize`, `signal.Notify(SIGWINCH)`— vive en `RunInteractive` y es fina; el
// parseo (tty.go) y el despacho (input.go) que la rodean ya están blindados por unidad.
//
// QUIT SIN API NUEVA. El apagado no añade superficie a `nu.*` (la API es sagrada): se
// apoya en el evento de ciclo de vida `core:shutdown` que §4 ya reserva al core. El
// driver registra un handler INTERNO (una `*lua.LFunction` que envuelve una closure Go,
// como hace `emitMisbehaved` con el bus) sobre `core:shutdown`: cuando algo lo emite
// —una app Lua que mapea su tecla de salida a `nu.events.emit("core:shutdown")`, o el
// propio driver al recibir `SIGTERM`/`SIGINT`— el handler cierra el canal `quit` y el
// bucle termina. Así "Lua decide cuándo salir, Go ejecuta el apagado" sin inventar
// `nu.ui.quit` ni tocar `nu.version.api`.

import (
	"context"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

// escFlushTimeout es cuánto espera el driver más bytes tras un ESC pendiente antes de
// resolverlo como la tecla `esc` (la ambigüedad "ESC tecla vs. prefijo de secuencia",
// tty.go). 30 ms basta: una secuencia real (flecha) llega pegada al ESC en el mismo
// read del terminal; un humano no encadena ESC + corchete a mano en menos de eso. Es el
// mismo orden que el `timeoutlen` de los editores, pero más corto porque aquí solo
// resuelve el primer byte, no una secuencia de teclado completa.
const escFlushTimeout = 30 * time.Millisecond

// ttyDriver mantiene el estado del bucle del driver: el runtime, la entrada/salida
// (stdin/stdout reales en producción, tuberías en los tests) y el canal de apagado.
type ttyDriver struct {
	rt  *Runtime
	in  io.Reader
	out io.Writer

	quit     chan struct{}
	quitOnce sync.Once
}

// newDriver construye un driver sobre un reader/writer dados. No toca el terminal (eso
// es `RunInteractive`): así un test lo instancia con tuberías en memoria.
func newDriver(rt *Runtime, in io.Reader, out io.Writer) *ttyDriver {
	return &ttyDriver{rt: rt, in: in, out: out, quit: make(chan struct{})}
}

// requestQuit cierra el canal de apagado (idempotente). Lo dispara el handler interno de
// `core:shutdown` y las señales de terminación.
func (d *ttyDriver) requestQuit() {
	d.quitOnce.Do(func() { close(d.quit) })
}

// installShutdownHandler suscribe en el bus de la Instance wasm un handler de
// `core:shutdown` que enciende un flag global; el bucle lo sondea tras cada lote de
// input (`pollWasmQuit`). Así un `core:shutdown` emitido por la UI Lua (una app que
// mapea su tecla de salida) termina el bucle. Corre bajo el token (lo toma por forma,
// aunque en wasm el bus lo gobierna el mutex de la Instance).
func (d *ttyDriver) installShutdownHandler() {
	s := d.rt.sched
	s.acquire()
	defer s.release()
	if d.rt.wasm == nil {
		return
	}
	_, _, _ = d.rt.wasm.Eval("_G.__driver_quit = false\n" +
		`nu.events.on("core:shutdown", function() _G.__driver_quit = true end)`)
}

// pollWasmQuit sondea el flag `__driver_quit` que el handler Lua de `core:shutdown`
// enciende en el bus de la Instance (installShutdownHandler): si está activo, pide el
// apagado. Presupone el token (lo llama `feed`).
func (d *ttyDriver) pollWasmQuit() {
	if d.rt.wasm == nil {
		return
	}
	if out, _, err := d.rt.wasm.Eval("return tostring(__driver_quit)"); err == nil && out == "true" {
		d.requestQuit()
	}
}

// attachOutput conecta el compositor al terminal: fija `rt.ui.out` (el destino del
// painter) e **invalida** el front para forzar un repintado completo en el próximo
// frame —el terminal acaba de entrar en alt-screen y está en blanco; cualquier pintado
// previo (la pantalla desnuda, el primer layout de una app) llenó el `front` pero sus
// bytes no llegaron a ningún sitio, así que hay que reemitir la pantalla entera, no un
// diff—. Bajo el token (toca el compositor).
func (d *ttyDriver) attachOutput() {
	s := d.rt.sched
	s.acquire()
	defer s.release()
	if d.rt.ui == nil {
		return
	}
	// Las tres operaciones (conectar la salida, invalidar, pintar) bajo el
	// candado de la UI de una vez (G44: el bombeo puede estar mutando el
	// compositor); flushFrameUnlocked porque el candado ya está tomado.
	d.rt.withUILock(func() {
		d.rt.ui.out = d.out
		d.rt.ui.comp.invalidate()
		d.rt.flushFrameUnlocked() // primer frame completo, ya
	})
}

// drive es el BUCLE del driver (lo testeable): lee bytes de `d.in`, los parsea y los
// inyecta, hasta que se pide apagar. La lectura ocurre en una goroutine aparte (un
// `Read` de un terminal bloquea), que empuja los trozos por un canal; el bucle principal
// selecciona entre esos trozos, el timeout del ESC pendiente y el canal de apagado.
//
// G44: junto al bucle de input arranca el BOMBEO CONTINUO del scheduler
// (`PumpTasks`), la pieza que hasta ahora no existía: sin ella, una task
// spawneada desde un keymap o un handler (el turno del agente del chat, el
// spinner `nu.task.every`) encolaba y nadie la reanudaba jamás. Los dos bucles
// conviven serializados por el mutex de la Instance (un solo hilo entra a la VM
// a la vez; el bombeo lo toma por-paso y espera sin él). El bombeo vive
// exactamente lo que vive drive(): al salir se corta su ctx y se espera su
// retorno — sin barrer el trabajo de fondo, que reclama `Runtime.Close`.
func (d *ttyDriver) drive() {
	if d.rt.wasm != nil {
		pumpCtx, pumpCancel := context.WithCancel(context.Background())
		pumpDone := make(chan struct{})
		go func() {
			defer close(pumpDone)
			// El error esperado al apagar es el ctx cancelado; un error duro del
			// motor también termina aquí (el driver sigue vivo para poder salir).
			_ = d.rt.wasm.PumpTasks(pumpCtx)
		}()
		defer func() {
			pumpCancel()
			<-pumpDone
		}()
	}

	chunks := make(chan []byte, 8)
	go d.readChunks(chunks)

	var pending []byte
	for {
		// Si hay una secuencia ESC a medias, espera más bytes pero con un timeout que la
		// resuelva (ESC solitario → tecla esc). Si no, espera indefinido al próximo trozo.
		var timeout <-chan time.Time
		if len(pending) > 0 {
			timeout = time.After(escFlushTimeout)
		}
		select {
		case <-d.quit:
			return
		case chunk, ok := <-chunks:
			if !ok {
				// EOF de stdin (el terminal se cerró): resuelve lo pendiente y apaga.
				d.feed(&pending, true)
				d.requestQuit()
				return
			}
			pending = append(pending, chunk...)
			d.feed(&pending, false)
		case <-timeout:
			d.feed(&pending, true) // venció el ESC: resuélvelo
		}
	}
}

// readChunks lee de `d.in` en bucle y empuja cada lectura por `chunks`, cerrándolo al
// llegar a EOF/error. Vive en su propia goroutine porque un `Read` de un TTY bloquea
// hasta que hay tecla; el bucle principal no debe bloquearse en él (tiene que atender el
// apagado y el timeout del ESC). No toca Lua: solo mueve bytes.
func (d *ttyDriver) readChunks(chunks chan<- []byte) {
	buf := make([]byte, 4096)
	for {
		n, err := d.in.Read(buf)
		if n > 0 {
			c := make([]byte, n)
			copy(c, buf[:n])
			select {
			case chunks <- c:
			case <-d.quit:
				return
			}
		}
		if err != nil {
			close(chunks)
			return
		}
	}
}

// feed parsea el buffer pendiente y despacha los eventos reconocidos, dejando en
// `*pending` la cola no consumida (una secuencia incompleta, salvo en `flush`). Toma el
// token UNA vez para todo el lote (no por evento) y, tras despachar, pinta el frame
// resultante de inmediato (feedback por tecla sin esperar al tick del painter).
func (d *ttyDriver) feed(pending *[]byte, flush bool) {
	evs, consumed := decodeInput(*pending, flush)
	if consumed > 0 {
		*pending = (*pending)[consumed:]
	}
	if len(evs) == 0 {
		return
	}
	s := d.rt.sched
	s.acquire()
	defer s.release()
	for _, ev := range evs {
		if ev.typ == "focus" {
			// Reporte de foco del terminal: lo traduce a `ui:focus` (no es una tecla).
			d.rt.emitUIFocus(ev.text == "in")
			continue
		}
		// La pila de input y el despacho de secuencias viven en el preludio Lua de la
		// Instance (vmwasm/ui.go): se inyecta el evento crudo por `FeedInput`, que lo
		// despacha por `__ui_dispatch_input` (la misma vía que el TTY real usa).
		if d.rt.wasm != nil {
			_, _ = d.rt.wasm.FeedInput(inputEventToWasm(ev))
		}
	}
	// Sobre wasm, un handler Lua pudo pedir el apagado (`core:shutdown`) al despachar
	// estas teclas: se sondea el flag ahora (el bus gopher no lo vería, M13d).
	d.pollWasmQuit()
	// Pinta ya el resultado de estas teclas (el painter periódico cubre el resto).
	d.rt.flushFrame()
}

// inputEventToWasm traduce un `inputEvent` (tty.go) al mapa crudo que el preludio de
// input wasm espera (`{type, key?, mods?, x?, y?, text?, path?}`, vmwasm/ui.go). Es el
// espejo VM-agnóstico de `inputState.eventTable` de gopher: mismos campos, mismos
// nombres, para que un keymap Lua reaccione idéntico en ambos backends. Los mods van
// como submapa de flags activos (`{ctrl?, alt?, shift?, meta?}`), como `modsTable`.
func inputEventToWasm(ev inputEvent) map[string]any {
	m := map[string]any{"type": ev.typ}
	switch ev.typ {
	case "key":
		m["key"] = ev.key
		m["mods"] = modsWasm(ev.mods)
	case "mouse":
		if ev.hasX {
			m["x"] = int64(ev.x)
			m["y"] = int64(ev.y)
		}
		m["mods"] = modsWasm(ev.mods)
	case "paste":
		// G30: un paste de imagen entrega `path` (la ruta ya volcada); uno de texto, `text`.
		if ev.path != "" {
			m["path"] = ev.path
		} else {
			m["text"] = ev.text
		}
	}
	return m
}

// modsWasm construye el submapa `mods` de un evento wasm: solo los flags activos, para
// que `ev.mods.ctrl` sea `true`/nil (espejo de `modsTable` de gopher).
func modsWasm(m modSet) map[string]any {
	out := map[string]any{}
	if m.ctrl {
		out["ctrl"] = true
	}
	if m.alt {
		out["alt"] = true
	}
	if m.shift {
		out["shift"] = true
	}
	if m.meta {
		out["meta"] = true
	}
	return out
}

// alt-screen y modos del terminal que el driver activa al entrar y restaura al salir.
//   - `?1049h`/`l`: buffer de pantalla alternativo (entrar/salir sin pisar el scrollback).
//   - `?2004h`/`l`: pegado entre corchetes (el terminal envuelve lo pegado en
//     `ESC[200~`…`ESC[201~`, que `decodeInput` reconoce como un evento `paste`).
//   - `?1004h`/`l`: reporte de foco (`ESC[I`/`ESC[O` → `ui:focus`).
//   - `?25l`/`h`: oculta el cursor de arranque (el compositor decide cuándo mostrarlo,
//     según qué región lo reclame, S30); al salir se restaura visible.
//   - `2J`/`H`: limpia y va a (1,1) al entrar, para partir de una pantalla en blanco
//     coherente con el `front` en blanco del compositor.
const (
	ttyEnterSeq = "\x1b[?1049h\x1b[?2004h\x1b[?1004h\x1b[?25l\x1b[2J\x1b[H"
	ttyLeaveSeq = "\x1b[?1004l\x1b[?2004l\x1b[?25h\x1b[?1049l"
)

// RunInteractive arranca el runtime en modo INTERACTIVO sobre el terminal real: pone
// stdin/stdout en raw mode + alt-screen, ajusta el tamaño al del terminal, atiende las
// señales (SIGWINCH → resize; SIGTERM/SIGINT/SIGHUP → apagar) y BLOQUEA ejecutando el
// bucle del driver hasta que se pide apagar, momento en que restaura el terminal. Es la
// contraparte interactiva de `EvalTaskString`/`RenderBareScreen`: interfaz Go del
// BINARIO (lo invoca main.go), no superficie Lua sagrada (fuera de api.md) —el core no
// aprende aquí lo que es un agente; solo da vida al `nu.ui` que las extensiones usan—.
//
// El contenido (qué se pinta) ya lo montó quien llama: o la pantalla desnuda (G21) o el
// `Boot` canónico que corrió los `init.lua`. Aquí solo se conecta ese `nu.ui` ya vivo al
// terminal. Requiere `rt.ui != nil` (un TTY interactivo, `rt.uiActive`); sin él devuelve
// un error —no hay superficie que conducir—.
func (rt *Runtime) RunInteractive() (err error) {
	if rt.ui == nil {
		return &StructuredError{Code: CodeEINVAL,
			Message: "RunInteractive: no hay superficie de UI (headless): nada que conducir"}
	}

	inFd := int(os.Stdin.Fd())
	oldState, rawErr := term.MakeRaw(inFd)
	if rawErr != nil {
		return &StructuredError{Code: CodeEIO,
			Message: "RunInteractive: no se pudo poner el terminal en raw mode: " + rawErr.Error()}
	}
	// Restaurar el terminal pase lo que pase (incluido un panic que se re-lanza tras
	// limpiar): un `nu` que muere sin restaurar deja el terminal inservible.
	defer func() {
		_, _ = io.WriteString(os.Stdout, ttyLeaveSeq)
		_ = term.Restore(inFd, oldState)
	}()

	if _, werr := io.WriteString(os.Stdout, ttyEnterSeq); werr != nil {
		return &StructuredError{Code: CodeEIO, Message: "RunInteractive: no se pudo inicializar el terminal: " + werr.Error()}
	}

	// Ajusta el compositor al tamaño real del terminal antes de conectar la salida (así
	// el primer frame completo ya sale con las dimensiones correctas). Si falla la
	// consulta, se queda con el tamaño que tenía (entorno/default).
	if w, h, gerr := term.GetSize(inFd); gerr == nil && w > 0 && h > 0 {
		rt.sched.acquire()
		rt.resizeUI(w, h)
		rt.sched.release()
	}

	d := newDriver(rt, os.Stdin, os.Stdout)
	d.installShutdownHandler()
	d.attachOutput()

	// Señales del terminal. SIGWINCH se reconsulta y reaplica; las de terminación piden
	// apagado ordenado (que restaurará el terminal por el `defer`). Corre en su goroutine.
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	go rt.handleSignals(d, sigCh, inFd)

	d.drive() // bloquea hasta el apagado
	return nil
}

// UIActive expone a `main` (el binario) si hay superficie de UI interactiva (un TTY, o
// `WithForceUI` en test): es la condición para entrar en `RunInteractive` en vez de
// imprimir el uso. Espejo público de `rt.uiActive` (inmutable tras `New`).
func (rt *Runtime) UIActive() bool { return rt.uiActive }

// PrepareBareScreen monta la PANTALLA DE RUNTIME DESNUDO (G21, §14) para el modo
// interactivo: la compone en el compositor (`renderBareScreen`), arma el painter (que en
// el camino sin plugins no lo armó `Boot`, porque no se llama) e instala un handler de
// teclado MÍNIMO del kernel para poder salir —`q`, `esc` o `ctrl+c` emiten
// `core:shutdown`, que el driver convierte en apagado—. La activación por teclado del
// conjunto de extensiones (acciones 1/2/3) sigue siendo el CP-7 ampliable; el onramp sin
// teclado es `nu --default-config` (G33). Bajo el token (toca compositor e input).
func (rt *Runtime) PrepareBareScreen() {
	rt.renderBareScreen()
	rt.armPainter()

	s := rt.sched
	s.acquire()
	defer s.release()
	if rt.ui == nil {
		return
	}
	// La pila de input vive en el preludio de la Instance: el handler de salida se
	// instala como un `on_input` Lua. Sobre la pila vacía de la pantalla desnuda queda
	// arriba (nada de producto que le gane).
	rt.installKernelExitWasm()
}

// InstallEmergencyExit instala la RED DE SALIDA DE EMERGENCIA del kernel (ADR-017, G35)
// al FONDO de la pila de input: `q`/`esc`/`ctrl+c` emiten `core:shutdown` —que el driver
// convierte en apagado—. Al estar en el fondo, CUALQUIER app de producto (que apila sus
// handlers encima) la tapa: en el chat normal, esc cancela el turno y ctrl+c cierra como
// siempre. Solo dispara cuando NADA encima consume la tecla —el caso patológico de una
// UI que no llegó a montarse (p. ej. un fallo inesperado de arranque)—, de modo que
// NINGUNA ruta interactiva deje la terminal en raw mode sin forma de salir con teclado.
// Se instala ANTES de `Boot` (con la pila vacía) para quedar debajo de todo. Bajo el
// token (toca input). Tolera la ausencia de UI (`rt.ui == nil`): no-op.
func (rt *Runtime) InstallEmergencyExit() {
	s := rt.sched
	s.acquire()
	defer s.release()
	if rt.ui == nil {
		return
	}
	// La pila de input vive en el preludio de la Instance: la red de emergencia se
	// instala como un `on_input` Lua. Como el despacho va de arriba abajo y `on_input`
	// apila, instalarla ANTES de `Boot` (pila vacía) la deja al fondo, bajo cualquier
	// handler que una app apile después.
	rt.installKernelExitWasm()
}

// installKernelExitWasm instala en el preludio de input wasm el handler de SALIDA del
// kernel (q/esc/ctrl+c → `core:shutdown`), espejo de `newKernelExitHandler` para el
// backend wasm, donde la pila de input y su despacho viven en la Instance (M13d). La
// POSICIÓN en la pila la fija el orden de instalación (on_input apila; el despacho va de
// arriba abajo): quien lo llame antes de `Boot` lo deja al fondo (red de emergencia);
// sobre una pila vacía (pantalla desnuda) queda arriba. Presupone el token tomado.
func (rt *Runtime) installKernelExitWasm() {
	if rt.wasm == nil {
		return
	}
	_, _, _ = rt.wasm.Eval(`nu.ui.on_input(function(ev)
  if ev == nil or ev.type ~= "key" then return false end
  local ctrl = ev.mods and ev.mods.ctrl
  if ev.key == "q" or ev.key == "esc" or (ev.key == "c" and ctrl) then
    nu.events.emit("core:shutdown")
    return true
  end
  return false
end)`)
}

// handleSignals atiende las señales del terminal en su propia goroutine. Un `SIGWINCH`
// reconsulta el tamaño y lo reaplica (bajo el token: toca el compositor y emite
// `ui:resize`). Una señal de terminación pide el apagado ordenado del bucle. Termina
// cuando se cierra `quit` (el `signal.Stop` del llamante deja de alimentar el canal).
func (rt *Runtime) handleSignals(d *ttyDriver, sigCh <-chan os.Signal, inFd int) {
	for {
		select {
		case <-d.quit:
			return
		case sig := <-sigCh:
			if sig == syscall.SIGWINCH {
				if w, h, err := term.GetSize(inFd); err == nil && w > 0 && h > 0 {
					rt.sched.acquire()
					rt.resizeUI(w, h)
					rt.flushFrame()
					rt.sched.release()
				}
				continue
			}
			// SIGTERM/SIGINT/SIGHUP: apagado. Anuncia `core:shutdown` por el bus de la
			// Instance (que las extensiones limpien lo suyo) y fuerza el quit directo.
			if rt.wasm != nil {
				_ = rt.wasm.EmitEvent("core:shutdown", nil)
			}
			d.requestQuit()
			return
		}
	}
}
