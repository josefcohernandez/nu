package runtime

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// `enu.ui` — Blocks, estilos, capacidades, compositor, input y portapapeles (api.md
// §9). El catálogo `enu.ui` (block/caps/size/region + métodos, on_input/keymap,
// clipboard) lo monta el backend wasm sobre el compositor real (vmwasm_ui.go);
// aquí sobreviven las piezas Go compartidas: el estado de sesión (`uiState`), el
// **pintor** del compositor (armPainter/paintLocked/flushFrame) que el timer de
// coalescing y el driver de TTY usan, la detección de TTY/tamaño/colores y la
// validación de color literal (isHexColor), que el binding wasm reutiliza.
//
// GATING HEADLESS (G20, §9). Sin TTY interactivo (`enu -e`, CI, salida redirigida)
// el módulo `enu.ui` directamente NO EXISTE —el mismo modelo que las caps de los
// workers: "la superficie no concedida no está"; la detección es `enu.has("ui")`—.
// En headless `rt.ui` es nil y el pintor es no-op.

// coalesceInterval es el periodo de pintado del compositor (§9.1, ADR-007): se
// pinta como mucho cada ~30 ms. Los cambios entre dos pintados se acumulan y
// producen UN frame, no N —no hay "flush" manual—. 30 ms ≈ 33 fps, la frontera de
// la fluidez percibida que el spike de ADR-012 usó como presupuesto.
const coalesceInterval = 30 * time.Millisecond

// uiState es el estado de sesión de `enu.ui` (§9.1, S29): el compositor y el timer
// de coalescing que lo pinta. Vive en el estado principal bajo el token (ADR-008);
// el timer (una goroutine armada en `Boot`) toma el token para pintar, de modo que
// el pintado nunca pisa una mutación de Lua. En headless (S29) el "pintado" solo
// construye el buffer ANSI en memoria (no hay TTY hasta S32); su forma y su tamaño
// son inspeccionables por los tests.
type uiState struct {
	comp   *compositor
	stopCh chan struct{} // cierra el timer de coalescing en `Close`
	armed  bool          // el timer ya se armó (idempotencia de `armPainter`)

	// clipWriter es el destino de las secuencias OSC 52 de `clipboard_set` (§9.2,
	// S32): el terminal. En producción es `os.Stdout` (el TTY interactivo que el
	// gating G20 garantizó); los tests lo sustituyen por un buffer para inspeccionar
	// los bytes exactos emitidos. Lo consume el backend de UI wasm (compositorBackend).
	clipWriter io.Writer

	// clipReader es la fuente de la RESPUESTA OSC 52 de `clipboard_get` (§9.2, S32).
	// Lo provee el driver de TTY (S33+): el flujo de bytes del terminal del que se
	// extrae la respuesta a la consulta. En este entorno headless es **nil**.
	clipReader io.Reader

	// out es el DESTINO real del frame pintado (driver de TTY, S33). El compositor
	// construye el buffer ANSI del diff en `comp.enc`; `out` es el `os.Stdout` del
	// proceso interactivo, que el driver fija con `attachOutput` al entrar en raw mode.
	// Tras cada `paint`, el painter vuelca el diff a `out` bajo el token. En headless
	// (tests del compositor, `enu -e`) es **nil** y no se vuelca nada.
	out io.Writer
}

// maybeUIState construye el estado de UI **solo si hay superficie de UI concedida**
// (`active`, el gating G20 de §9 que `New` resuelve). En headless devuelve nil: sin
// `enu.ui` no hay compositor que mantener ni timer que armar, y `armPainter`/
// `stopPainter`/`Close` ya toleran `rt.ui == nil`. Con UI activa delega en
// `newUIState`.
func maybeUIState(active bool, w, h int) *uiState {
	if !active {
		return nil
	}
	return newUIState(w, h)
}

// newUIState construye el estado de UI con un compositor del tamaño pedido. Si la
// Option `WithUISize` no fijó tamaño (w/h <= 0), se resuelve por el entorno
// (`COLUMNS`/`LINES`) o el default 80×24. El destino del portapapeles (OSC 52, S32)
// es `os.Stdout` por defecto (el TTY que el gating garantizó); los tests lo
// sustituyen. El timer de coalescing NO se arma aquí (no hay event loop todavía): lo
// arma `Boot` con `armPainter`.
func newUIState(w, h int) *uiState {
	if w <= 0 || h <= 0 {
		w, h = detectSize()
	}
	return &uiState{comp: newCompositor(w, h), stopCh: make(chan struct{}), clipWriter: os.Stdout}
}

// detectTTY decide si hay un TTY interactivo del que colgar `enu.ui` (el GATING
// HEADLESS de G20, §9, S32). Exige que **tanto la salida estándar como la entrada
// estándar** sean terminales: la UI a pantalla completa necesita escribir el render
// (stdout) y leer las teclas (stdin), así que si CUALQUIERA está redirigida (`enu -e`,
// un pipe, CI, salida a fichero) no hay superficie viable y `enu.ui` no existe. Usa
// `golang.org/x/term.IsTerminal` (puro-Go, sin CGO, coherente con ADR-001).
func detectTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stdin.Fd()))
}

// detectSize estima el tamaño del terminal en celdas sin tocar el TTY. Lee
// `COLUMNS`/`LINES` del entorno (que algunos shells exportan) y, si no están o no
// son enteros positivos, cae al default 80×24 —el tamaño clásico, razonable para
// un primer frame headless—.
func detectSize() (int, int) {
	w, h := 80, 24
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		w = c
	}
	if l, err := strconv.Atoi(os.Getenv("LINES")); err == nil && l > 0 {
		h = l
	}
	return w, h
}

// armPainter arranca el timer de coalescing: una goroutine que, cada
// `coalesceInterval`, toma el token y pinta el compositor **si hay cambios**
// (`dirty`). Así N mutaciones entre dos ticks producen UN frame (ADR-007). Lo
// llama `Boot` (cuando el event loop ya corre); es idempotente. En headless el
// pintado construye el buffer ANSI en memoria (no hay TTY hasta S32). El timer se
// corta en `Close` cerrando `stopCh`.
func (rt *Runtime) armPainter() {
	if rt.ui == nil || rt.ui.armed {
		return
	}
	rt.ui.armed = true
	ticker := time.NewTicker(coalesceInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-rt.ui.stopCh:
				return
			case <-ticker.C:
				rt.paintLocked()
			}
		}
	}()
}

// paintLocked pinta bajo AMBOS candados relevantes: el token del scheduler —que
// serializa con el código que lee el compositor bajo el token (los tests de UI, el
// driver)— y, dentro de flushFrame, el mutex de la Instance wasm —que serializa con
// las host functions de `enu.ui` que mutan el compositor durante un Call—. Sin doble
// bloqueo problemático: las host functions wasm no toman el token, así que no hay
// ciclo token↔inst.mu. Lo usan el timer de coalescing y el driver de TTY (tras
// alimentar input).
func (rt *Runtime) paintLocked() {
	rt.sched.acquire()
	rt.flushFrame() // flushFrame toma además inst.mu por su cuenta
	rt.sched.release()
}

// withUILock ejecuta `fn` bajo el candado que serializa el compositor frente a
// la VM (el mutex de la Instance wasm). Desde G44 el bombeo continuo del
// scheduler puede estar mutando la UI en cualquier momento (un hostcall de
// `enu.ui` durante un paso), así que TODO acceso al compositor fuera de un Call
// —el driver, el resize, la pantalla desnuda, los lectores de test— debe pasar
// por aquí; el token del scheduler ya no basta (el bombeo no lo toma). Sin
// backend wasm corre `fn` directamente. `fn` no debe re-entrar la VM (Eval):
// el mutex no es reentrante.
func (rt *Runtime) withUILock(fn func()) {
	if rt.wasm != nil {
		rt.wasm.WithLock(fn)
		return
	}
	fn()
}

// flushFrame es el punto ÚNICO de pintado: toma el mutex de la Instance wasm (el
// candado que serializa las mutaciones de `enu.ui` durante un Call) para leer el
// compositor de forma excluyente con ellas (M15, veto de corrección). Todos los
// llamantes (el timer de coalescing y el driver de TTY) pasan por aquí.
func (rt *Runtime) flushFrame() {
	rt.withUILock(rt.flushFrameUnlocked)
}

// flushFrameUnlocked hace el pintado en sí; presupone tomado el candado que serializa
// el compositor (inst.mu de la Instance wasm).
func (rt *Runtime) flushFrameUnlocked() {
	if rt.ui == nil || !rt.ui.comp.dirty {
		return
	}
	rt.ui.comp.paint()
	if rt.ui.out != nil {
		if frame := rt.ui.comp.encoded(); frame != "" {
			_, _ = io.WriteString(rt.ui.out, frame)
		}
	}
}

// stopPainter corta el timer de coalescing (su goroutine). Idempotente: cerrar
// `stopCh` dos veces entraría en pánico, así que se protege con el flag `armed`
// —solo el primer `Close` cierra—. Lo llama `Close`.
func (rt *Runtime) stopPainter() {
	if rt.ui == nil || !rt.ui.armed {
		return
	}
	rt.ui.armed = false
	close(rt.ui.stopCh)
}

// isHexColor comprueba que `s` tenga la forma "#rrggbb": una almohadilla seguida
// de exactamente seis dígitos hexadecimales. Lo reutiliza el binding de color del
// backend wasm (normalizeColorWasm, vmwasm_ui.go).
func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		isHexDigit := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHexDigit {
			return false
		}
	}
	return true
}

// detectColors estima el número de colores del terminal por el entorno, sin tocar
// el terminal (la negociación real es Fase 6). `COLORTERM=truecolor`/`24bit` →
// 16M (1<<24); un `TERM` con "256color" → 256; un `TERM` no vacío → 16; sin TERM
// (headless/CI/redirigido) → 256 como default razonable. No es un sniffing frágil:
// es una pista, y el compositor (S29) degrada con seguridad. Lo consulta el binding
// `enu.ui.caps` del backend wasm (compositorBackend.Caps).
func detectColors() int {
	if ct := strings.ToLower(os.Getenv("COLORTERM")); ct == "truecolor" || ct == "24bit" {
		return 1 << 24
	}
	term := os.Getenv("TERM")
	switch {
	case term == "":
		return 256 // headless / sin TTY: default razonable
	case strings.Contains(term, "256color"):
		return 256
	case strings.Contains(term, "truecolor"):
		return 1 << 24
	case term == "dumb":
		return 0
	default:
		return 16
	}
}
