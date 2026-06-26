package runtime

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
	"golang.org/x/term"
)

// `nu.ui` — Blocks, estilos, capacidades, compositor, input y portapapeles (api.md
// §9). Se construyó por capas: `block`/`caps`/`Style` (S22), el compositor y las
// regiones (S29/S30), el input (S31) y, en S32, el **portapapeles OSC 52**
// (`osc52.go`), los **eventos `ui:*`** (`uiEvents`) y el **GATING HEADLESS (G20)**.
//
// GATING HEADLESS (G20, §9, S32). El contrato dice que sin TTY interactivo (`nu -e`,
// CI, salida redirigida) el módulo `nu.ui` directamente NO EXISTE —el mismo modelo
// que las caps de los workers: "la superficie no concedida no está"; la detección es
// `nu.has("ui")`, nunca probar-y-capturar—. Hasta S31 `nu.ui` se colgaba SIEMPRE
// (NOTA DE FRONTERA del plan: S23–S31 necesitaban inspeccionar Blocks en sus tests
// headless). S32 cierra esa deuda: `registerUI` solo se llama si `rt.uiActive`
// (`registerNu`), que `New` fija por `WithForceUI` (tests) o por `detectTTY` (binario
// real). Los tests de UI fuerzan la activación con `WithForceUI(true)` vía
// `newHarness`, de modo que el gating real (por TTY) aplica al binario `nu` sin
// romper la suite.

// regionTypeName identifica la metatabla del handle opaco `Region` (§9.1). De ella
// cuelgan los métodos de S29 (`blit`/`fill`/`clear`) y el ciclo de vida de S30
// (`move`/`resize`/`raise`/`lower`/`show`/`hide`/`destroy`/`cursor`); el input es
// S31. Userdata opaco, como el Block: Lua lo pasa de vuelta, no inspecciona su
// interior.
const regionTypeName = "nu.ui.Region"

// coalesceInterval es el periodo de pintado del compositor (§9.1, ADR-007): se
// pinta como mucho cada ~30 ms. Los cambios entre dos pintados se acumulan y
// producen UN frame, no N —no hay "flush" manual—. 30 ms ≈ 33 fps, la frontera de
// la fluidez percibida que el spike de ADR-012 usó como presupuesto.
const coalesceInterval = 30 * time.Millisecond

// uiState es el estado de sesión de `nu.ui` (§9.1, S29): el compositor y el timer
// de coalescing que lo pinta. Vive en el estado principal bajo el token (ADR-008);
// el timer (una goroutine armada en `Boot`) toma el token para pintar, de modo que
// el pintado nunca pisa una mutación de Lua. En headless (S29) el "pintado" solo
// construye el buffer ANSI en memoria (no hay TTY hasta S32); su forma y su tamaño
// son inspeccionables por los tests.
type uiState struct {
	comp   *compositor
	stopCh chan struct{} // cierra el timer de coalescing en `Close`
	armed  bool          // el timer ya se armó (idempotencia de `armPainter`)

	// input es la pila de input de `nu.ui` (§9.3, S31): los handlers de `on_input`/
	// `keymap` y la máquina de secuencias con timeout. Se construye en `registerUI`
	// (que ya tiene el `*Runtime` completo), no en `newUIState` (donde `rt` aún no
	// existe). Vive bajo el token, como el compositor.
	input *inputState

	// clipWriter es el destino de las secuencias OSC 52 de `clipboard_set` (§9.2,
	// S32): el terminal. En producción es `os.Stdout` (el TTY interactivo que el
	// gating G20 garantizó); los tests lo sustituyen por un buffer para inspeccionar
	// los bytes exactos emitidos. Solo se toca bajo el token (estado principal).
	clipWriter io.Writer

	// clipReader es la fuente de la RESPUESTA OSC 52 de `clipboard_get` (§9.2, S32).
	// Lo provee el driver de TTY (S33+): el flujo de bytes del terminal del que se
	// extrae la respuesta a la consulta. En este entorno headless es **nil** (no hay
	// driver), así que `clipboard_get` resuelve a `nil` —y, por el gating G20, en
	// headless `nu.ui` ni se registra—. El parseo de la respuesta (`parseOSC52Reply`)
	// se prueba por unidad con bytes sintéticos. La lectura corre en la goroutine de
	// fondo del ⏸ (sin token), así que `clipReader` no se toca bajo el token.
	clipReader io.Reader

	// out es el DESTINO real del frame pintado (driver de TTY, S33). El compositor
	// construye el buffer ANSI del diff en `comp.enc`; hasta S33 ese buffer vivía solo
	// en memoria (inspeccionable por los tests) y **nada lo enviaba a un terminal**: la
	// UI se componía pero no se veía. `out` cierra ese hueco —es el `os.Stdout` del
	// proceso interactivo, que el driver fija con `attachOutput` al entrar en raw mode—:
	// tras cada `paint`, el painter (`armPainter`) vuelca el diff a `out` bajo el token.
	// En headless (tests del compositor, `nu -e`) es **nil** y no se vuelca nada (la
	// salida sigue siendo solo `comp.enc`). Solo se toca bajo el token (estado principal).
	out io.Writer
}

// maybeUIState construye el estado de UI **solo si hay superficie de UI concedida**
// (`active`, el gating G20 de §9 que `New` resuelve). En headless devuelve nil: sin
// `nu.ui` no hay compositor que mantener ni timer que armar, y `armPainter`/
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

// detectTTY decide si hay un TTY interactivo del que colgar `nu.ui` (el GATING
// HEADLESS de G20, §9, S32). Exige que **tanto la salida estándar como la entrada
// estándar** sean terminales: la UI a pantalla completa necesita escribir el render
// (stdout) y leer las teclas (stdin), así que si CUALQUIERA está redirigida (`nu -e`,
// un pipe, CI, salida a fichero) no hay superficie viable y `nu.ui` no existe. Usa
// `golang.org/x/term.IsTerminal` (puro-Go, sin CGO, coherente con ADR-001). La
// negociación fina del terminal (caps reales, raw mode) es del driver de TTY (S33+);
// aquí solo se decide la existencia del módulo.
func detectTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stdin.Fd()))
}

// detectSize estima el tamaño del terminal en celdas sin tocar el TTY (la
// negociación real con el terminal y el gating headless G20 son S32). Lee
// `COLUMNS`/`LINES` del entorno (que algunos shells exportan) y, si no están o no
// son enteros positivos, cae al default 80×24 —el tamaño clásico, razonable para
// un primer frame headless—. No es sniffing frágil: es un default; con TTY real,
// S32 lo sustituirá por el tamaño del terminal y los `ui:resize`.
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
//
// Toma el token para pintar (como `runSyncHandler`): el pintado toca el compositor
// (estado principal, ADR-008), que las mutaciones de Lua también tocan bajo el
// token; serializarlos por el token evita la carrera sin un candado propio. El
// pintado es Go puro (no llama a Lua), así que no necesita un thread Lua dedicado.
func (rt *Runtime) armPainter() {
	if rt.ui == nil || rt.ui.armed {
		return
	}
	rt.ui.armed = true
	s := rt.sched
	ticker := time.NewTicker(coalesceInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-rt.ui.stopCh:
				return
			case <-ticker.C:
				s.acquire()
				rt.flushFrame()
				s.release()
			}
		}
	}()
}

// flushFrame pinta el compositor (si hay cambios) y **vuelca el diff resultante al
// terminal real** conectado por el driver de TTY (`rt.ui.out`, S33). Es el puente que
// faltaba entre el compositor —que siempre supo construir el buffer ANSI del frame en
// `comp.enc`— y un terminal de verdad: hasta S33 ese buffer vivía solo en memoria y
// **nada lo enviaba a la pantalla**. Lo llaman el timer de coalescing (`armPainter`,
// cada ~30 ms) y el driver tras alimentar input (para feedback inmediato por tecla, sin
// esperar al tick). PRESUPONE el token tomado (toca el compositor, estado principal,
// ADR-008); no-op sin UI o sin cambios. En headless `out` es nil: el frame se compone
// (lo inspeccionan los tests por `comp.encoded()`) pero no se vuelca a ningún sitio.
func (rt *Runtime) flushFrame() {
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

// registerUI cuelga `nu.ui` del global `nu` con TODA la superficie de §9:
// `block`/`caps` (S22), `size`/`region` + métodos de `Region` (S29/S30),
// `on_input`/`keymap` (S31) y `clipboard_set`/`clipboard_get` (OSC 52, S32). Instala
// además la metatabla del tipo `Block` (block.go) y la de `Region`. **Solo se llama
// si `rt.uiActive`** (`registerNu`, gating G20): en headless `nu.ui` no existe.
func (rt *Runtime) registerUI(nu *lua.LTable) {
	L := rt.L

	// La metatabla del tipo opaco `Block`: la instala UI porque `nu.ui.block` es el
	// constructor manual y el resto de productores (nu.text.*) la comparten.
	rt.registerBlockType()

	// La metatabla del tipo opaco `Region` (§9.1, S29) con los métodos de S29.
	rt.registerRegionType()

	// La pila de input (§9.3, S31): se construye aquí, donde `rt` ya está completo
	// (en `newUIState` aún no existe). El despacho y los handles viven bajo el token.
	rt.ui.input = newInputState(rt)

	uiT := L.NewTable()
	uiT.RawSetString("block", L.NewFunction(rt.uiBlock))
	uiT.RawSetString("caps", L.NewFunction(rt.uiCaps))
	uiT.RawSetString("size", L.NewFunction(rt.uiSize))
	uiT.RawSetString("region", L.NewFunction(rt.uiRegionNew))
	rt.registerInput(uiT) // on_input/keymap (§9.3, S31)
	// Portapapeles vía OSC 52 (§9.2, S32, osc52.go): `clipboard_set` (no ⏸) y
	// `clipboard_get` (⏸, espera la respuesta del terminal).
	uiT.RawSetString("clipboard_set", L.NewFunction(rt.uiClipboardSet))
	uiT.RawSetString("clipboard_get", L.NewFunction(rt.uiClipboardGet))
	nu.RawSetString("ui", uiT)
}

// uiClipboardSet implementa `nu.ui.clipboard_set(s)` (§9.2, S32): copia `s` al
// portapapeles del sistema escribiendo la secuencia OSC 52 al terminal (`clipWriter`,
// `os.Stdout` en producción). NO ⏸: es un write de unos bytes y el terminal no
// responde. Un fallo de escritura al TTY se registra best-effort y no lanza —copiar al
// portapapeles es accesorio, no debe tumbar al llamante—. Solo estado principal.
func (rt *Runtime) uiClipboardSet(L *lua.LState) int {
	s := L.CheckString(1)
	seq := encodeOSC52Set(s)
	if _, err := io.WriteString(rt.ui.clipWriter, seq); err != nil {
		_ = rt.log.write(levelError, rt.currentOwner(),
			"nu.ui.clipboard_set: no se pudo escribir OSC 52 al terminal: "+err.Error())
	}
	return 0
}

// uiClipboardGet implementa `nu.ui.clipboard_get() -> string?` (§9.2, S32) ⏸: pide el
// portapapeles enviando la consulta OSC 52 y **espera la respuesta** del terminal,
// que `parseOSC52Reply` decodifica. Devuelve `nil` si el terminal no soporta la
// lectura o si pasa `clipboardReadTimeout` sin respuesta legible.
//
// Es ⏸ porque espera IO del terminal: suelta el token y lee la respuesta en la
// goroutine de fondo (el puente `suspend` de S04, igual que `nu.fs`/`nu.http`), de
// modo que el loop no se congela mientras tanto. La LECTURA real del TTY es del
// driver (S33+, CP-7): aquí la lógica probada es la codificación de la consulta y el
// **parseo** de la respuesta (`osc52_test.go`); el camino vivo lee de un terminal que
// este entorno headless no tiene (y donde, por el gating G20, `nu.ui` ni existiría).
func (rt *Runtime) uiClipboardGet(L *lua.LState) int {
	// Envía la consulta al terminal bajo el token (toca `clipWriter`, estado
	// principal); la espera de la respuesta es lo que suspende.
	if _, err := io.WriteString(rt.ui.clipWriter, encodeOSC52Query()); err != nil {
		_ = rt.log.write(levelError, rt.currentOwner(),
			"nu.ui.clipboard_get: no se pudo escribir la consulta OSC 52: "+err.Error())
		L.Push(lua.LNil)
		return 1
	}
	reader := rt.ui.clipReader
	vals := rt.sched.suspend(L, func() deliverFn {
		// Trabajo de fondo (SIN token, no toca Lua): lee la respuesta del terminal con
		// un timeout. En este entorno headless no hay lector (driver de S33+), así que
		// `reader` es nil y se resuelve a `nil` de inmediato —el ida y vuelta real lo
		// ejercita el driver—.
		text, ok := readOSC52Reply(reader, clipboardReadTimeout)
		return func(L *lua.LState) []lua.LValue {
			if !ok {
				return []lua.LValue{lua.LNil}
			}
			return []lua.LValue{lua.LString(text)}
		}
	})
	return pushAll(L, vals)
}

// registerRegionType instala la metatabla del tipo `Region` con un `__index` que
// resuelve sus métodos: los de S29 (`blit`/`fill`/`clear`) y el ciclo de vida de S30
// (`move`/`resize`/`raise`/`lower`/`show`/`hide`/`destroy`/`cursor`). La Region es
// opaca (§9.1): no se expone su interior a Lua, solo sus métodos.
func (rt *Runtime) registerRegionType() {
	L := rt.L
	mt := L.NewTypeMetatable(regionTypeName)
	index := L.NewTable()
	index.RawSetString("blit", L.NewFunction(rt.regionBlit))
	index.RawSetString("fill", L.NewFunction(rt.regionFill))
	index.RawSetString("clear", L.NewFunction(rt.regionClear))
	// Ciclo de vida de la región (S30, §9.1).
	index.RawSetString("move", L.NewFunction(rt.regionMove))
	index.RawSetString("resize", L.NewFunction(rt.regionResize))
	index.RawSetString("raise", L.NewFunction(rt.regionRaise))
	index.RawSetString("lower", L.NewFunction(rt.regionLower))
	index.RawSetString("show", L.NewFunction(rt.regionShow))
	index.RawSetString("hide", L.NewFunction(rt.regionHide))
	index.RawSetString("destroy", L.NewFunction(rt.regionDestroy))
	index.RawSetString("cursor", L.NewFunction(rt.regionCursor))
	L.SetField(mt, "__index", index)
}

// uiSize implementa `nu.ui.size() -> {w, h}` (§9.1): el tamaño de la pantalla en
// celdas. En S29 sale del compositor (inyectado por `WithUISize` en tests, o del
// entorno/default headless); con TTY real, S32 lo mantendrá al día con los
// `ui:resize`. Devuelve una tabla `{w=, h=}`.
func (rt *Runtime) uiSize(L *lua.LState) int {
	t := L.NewTable()
	t.RawSetString("w", lua.LNumber(rt.ui.comp.w))
	t.RawSetString("h", lua.LNumber(rt.ui.comp.h))
	L.Push(t)
	return 1
}

// uiRegionNew implementa `nu.ui.region(opts) -> Region` (§9.1): crea una región
// rectangular de composición. `opts`: `{x, y, w, h, z?}` (z opcional, default 0).
// El z-order es propiedad de quien la crea (mayor z gana en la zona común). La
// región se etiqueta con el dueño vigente (`currentOwner()`) y se registra como
// `ownedHandle` (S13): un `reload` la destruye con el resto de los handles del
// plugin (G2). NO se exponen aquí move/resize/raise/lower/show/hide/destroy/cursor
// (eso es S30): solo crear la región y sus métodos de S29.
//
// **Resize (G1)**: una región total o parcialmente fuera de pantalla NO es error
// —se crea con sus coordenadas tal cual y el compositor la recorta al pintar—; sus
// `w`/`h` definen su lienzo lógico, independiente del tamaño de la pantalla, así
// que reaparece intacta si la pantalla crece. `w`/`h` deben ser >= 0; `x`/`y` son
// libres (incluso negativos: la región empieza fuera por la izquierda/arriba).
func (rt *Runtime) uiRegionNew(L *lua.LState) int {
	opts := L.CheckTable(1)

	x := optInt(opts, "x", 0)
	y := optInt(opts, "y", 0)
	z := optInt(opts, "z", 0)
	w, okW := reqInt(opts, "w")
	h, okH := reqInt(opts, "h")
	if !okW || !okH {
		raiseError(L, CodeEINVAL, "nu.ui.region: opts necesita `w` y `h` enteros", lua.LNil)
		return 0
	}
	if w < 0 || h < 0 {
		raiseError(L, CodeEINVAL, "nu.ui.region: `w` y `h` no pueden ser negativos", lua.LNil)
		return 0
	}

	reg := rt.ui.comp.addRegion(x, y, w, h, z, rt.currentOwner())
	// Registro de handles por dueño (S13): que `reload` la encuentre y destruya.
	rt.sched.track(reg)

	ud := L.NewUserData()
	ud.Value = reg
	L.SetMetatable(ud, L.GetTypeMetatable(regionTypeName))
	L.Push(ud)
	return 1
}

// optInt lee un campo entero opcional de una tabla de opciones; si falta o no es
// número, devuelve `def`. Lo usan los campos opcionales de `nu.ui.region` (`x`,
// `y`, `z`).
func optInt(t *lua.LTable, key string, def int) int {
	if n, ok := t.RawGetString(key).(lua.LNumber); ok {
		return int(n)
	}
	return def
}

// reqInt lee un campo entero requerido; devuelve `(valor, true)` si está presente
// como número, o `(0, false)` si falta o no es número (el llamante lanza EINVAL con
// el contexto). Lo usan `w`/`h` de `nu.ui.region`.
func reqInt(t *lua.LTable, key string) (int, bool) {
	if n, ok := t.RawGetString(key).(lua.LNumber); ok {
		return int(n), true
	}
	return 0, false
}

// checkRegion recupera el `*uiRegion` del userdata del argumento `idx`. Lanza
// `EINVAL` si no es un handle de Region o si ya se destruyó/recargó (alive=false):
// blittear sobre una región muerta es un error de uso accionable, no un no-op
// silencioso.
func checkRegion(L *lua.LState, idx int) *uiRegion {
	ud := L.CheckUserData(idx)
	r, ok := ud.Value.(*uiRegion)
	if !ok {
		raiseError(L, CodeEINVAL, "Region: se esperaba un handle de Region", lua.LNil)
		return nil
	}
	if !r.alive {
		raiseError(L, CodeEINVAL, "Region: la región ya fue destruida", lua.LNil)
		return nil
	}
	return r
}

// regionBlit implementa `Region:blit(x, y, block)` (§9.1): estampa un Block en
// coordenadas LOCALES de la región. Es **copia, nunca re-render** (G28): copia la
// ventana visible del Block al lienzo de la región sin reconstruir el Block —
// blittear el mismo Block con otro offset es otra copia, no recalcula nada—. `x/y`
// pueden ser **negativos** y recortan el borde inicial del Block (un scroll hacia
// abajo es `blit(0, -n, doc)`); el exceso recorta el final (viewport con recorte
// por ambos extremos, G28). El contenido persiste hasta el próximo blit/fill/clear;
// el pintado real lo coalesce el timer (~30 ms). Marca sucio el compositor.
func (rt *Runtime) regionBlit(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	x := L.CheckInt(2)
	y := L.CheckInt(3)
	b := checkBlock(L, 4)
	r.content.blitBlock(x, y, b)
	r.comp.markDirty()
	return 0
}

// regionFill implementa `Region:fill(style?)` (§9.1): rellena la región con un
// estilo (espacios con ese estilo). Sin `style` (o nil), es fondo sin estilo
// —equivalente a `clear`—. Marca sucio el compositor.
func (rt *Runtime) regionFill(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	var st *style
	if L.GetTop() >= 2 && L.Get(2) != lua.LNil {
		parsed, err := parseStyle(L, L.Get(2))
		if err != "" {
			raiseError(L, CodeEINVAL, "Region:fill: "+err, lua.LNil)
			return 0
		}
		st = parsed
	}
	r.content.fill(st)
	r.comp.markDirty()
	return 0
}

// regionClear implementa `Region:clear()` (§9.1): limpia la región (todas sus
// celdas a fondo, sin estilo). Es `fill(nil)`. Marca sucio el compositor.
func (rt *Runtime) regionClear(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	r.content.fill(nil)
	r.comp.markDirty()
	return 0
}

// regionMove implementa `Region:move(x, y)` (§9.1, S30): recoloca la región a las
// coordenadas de pantalla (x,y). No mueve el contenido (su lienzo) ni cambia su
// tamaño; el siguiente `composite` la pinta en el nuevo sitio, recortada si se sale
// (G1). Síncrono, solo estado principal.
func (rt *Runtime) regionMove(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	r.move(L.CheckInt(2), L.CheckInt(3))
	return 0
}

// regionResize implementa `Region:resize(w, h)` (§9.1, S30): cambia el tamaño lógico
// de la región (reasigna su lienzo a w×h). El contenido se **conserva donde quepa**
// (la esquina superior izquierda; lo que excede se descarta, lo nuevo es fondo),
// coherente con el modelo "la región es una ventana" de S29. `w`/`h` deben ser >= 0
// (un tamaño negativo es EINVAL, igual que en `nu.ui.region`). Síncrono.
func (rt *Runtime) regionResize(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	w := L.CheckInt(2)
	h := L.CheckInt(3)
	if w < 0 || h < 0 {
		raiseError(L, CodeEINVAL, "Region:resize: `w` y `h` no pueden ser negativos", lua.LNil)
		return 0
	}
	r.resizeRegion(w, h)
	return 0
}

// regionRaise implementa `Region:raise()` (§9.1, S30): sube la región al frente del
// z-order (gana en toda zona de solape). Conserva el orden relativo del resto.
func (rt *Runtime) regionRaise(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	r.raise()
	return 0
}

// regionLower implementa `Region:lower()` (§9.1, S30): baja la región al fondo del
// z-order. Simétrico de `raise`.
func (rt *Runtime) regionLower(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	r.lower()
	return 0
}

// regionShow implementa `Region:show()` (§9.1, S30): vuelve a componer una región
// oculta por `hide`. Idempotente.
func (rt *Runtime) regionShow(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	r.show()
	return 0
}

// regionHide implementa `Region:hide()` (§9.1, S30): oculta la región (deja de
// componerse) conservando su lienzo y coordenadas. Si llevaba el cursor real, lo
// suelta. Idempotente.
func (rt *Runtime) regionHide(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	r.hide()
	return 0
}

// regionDestroy implementa `Region:destroy()` (§9.1, S30): elimina la región del
// compositor, suelta el cursor si era suyo, y la **desregistra** del registro de
// handles por dueño (S13) para no dejar un handle muerto que un `reload` posterior
// intente liberar (fuga). Es **idempotente**: destruir dos veces es inocuo (la
// segunda es no-op). Tras destruir, los demás métodos de la región fallan limpio
// (`EINVAL` "ya destruida", vía `checkRegion`), no petan: una región muerta es un
// error de uso accionable, no un crash.
//
// Por qué un handle inválido (no-Region) SÍ lanza pero una Region ya muerta NO: una
// Region muerta es el caso esperado de la idempotencia (el dueño la destruye y el
// reload también podría); pasar algo que no es una Region es un error de tipo del
// llamante. Por eso `destroy` valida el tipo a mano en vez de `checkRegion` (que
// lanzaría sobre la muerta).
func (rt *Runtime) regionDestroy(L *lua.LState) int {
	ud := L.CheckUserData(1)
	r, ok := ud.Value.(*uiRegion)
	if !ok {
		raiseError(L, CodeEINVAL, "Region:destroy: se esperaba un handle de Region", lua.LNil)
		return 0
	}
	if !r.alive {
		return 0 // idempotente: ya destruida (o liberada por reload)
	}
	rt.sched.untrack(r) // quita del registro de handles por dueño (no fuga, S13)
	r.release()         // descuelga del compositor, suelta el cursor, marca muerta
	return 0
}

// regionCursor implementa `Region:cursor(x, y | nil)` (§9.1, S30): coloca el cursor
// real del terminal en coordenadas LOCALES de la región, o lo oculta si el primer
// argumento es `nil`. **SOLO UNA región puede tener el cursor; la ÚLTIMA llamada
// gana**: reclamar el cursor desbanca a la dueña anterior (su `cursor()` previo se
// pierde). El compositor emite la secuencia de posicionar/ocultar en el frame; si la
// posición cae fuera de pantalla, el cursor se oculta (G1). Síncrono.
//
// Forma del argumento: `cursor(nil)` (o sin argumentos) oculta; `cursor(x, y)` con
// dos enteros posiciona. Un solo entero sin el segundo es EINVAL accionable (la
// firma es `(x, y)` o `(nil)`, no `(x)`).
func (rt *Runtime) regionCursor(L *lua.LState) int {
	r := checkRegion(L, 1)
	if r == nil {
		return 0
	}
	// `cursor(nil)` o `cursor()` → ocultar: esta región sigue siendo la "dueña" pero
	// con el cursor apagado (la última llamada gana, también para apagarlo).
	if L.GetTop() < 2 || L.Get(2) == lua.LNil {
		r.comp.setCursor(r, 0, 0, true)
		return 0
	}
	x := L.CheckInt(2)
	y := L.CheckInt(3) // exige el segundo entero: la firma es (x, y)
	r.comp.setCursor(r, x, y, false)
	return 0
}

// uiBlock implementa `nu.ui.block(lines) -> Block` (§9.2): construcción manual de
// un Block. `lines` es un array; cada línea es **un string** (un solo span sin
// estilo) **o** un array de Spans `{text, style?}`. Calcula `.width` (máximo
// ancho de línea en celdas, vía `text.width`) y `.height` (número de líneas) al
// construir (block.go). Un argumento mal formado → `EINVAL` accionable.
func (rt *Runtime) uiBlock(L *lua.LState) int {
	arg := L.CheckTable(1)

	lines := make([][]span, 0, arg.Len())
	var convErr string
	idx := 0
	arg.ForEach(func(k, v lua.LValue) {
		if convErr != "" {
			return
		}
		idx++
		spans, err := rt.parseLine(L, v)
		if err != "" {
			convErr = fmt.Sprintf("nu.ui.block: línea %d: %s", idx, err)
			return
		}
		lines = append(lines, spans)
	})
	if convErr != "" {
		raiseError(L, CodeEINVAL, convErr, lua.LNil)
		return 0
	}

	rt.pushBlock(L, newBlock(lines))
	return 1
}

// parseLine convierte una línea de `nu.ui.block` a una rebanada de spans. Una
// línea puede ser un **string** (un único span sin estilo) o una **tabla** que es
// un array de Spans (`{text, style?}`). Devuelve un mensaje de error (no vacío) en
// vez de lanzar para que `uiBlock` añada el número de línea al contexto.
func (rt *Runtime) parseLine(L *lua.LState, v lua.LValue) ([]span, string) {
	switch line := v.(type) {
	case lua.LString:
		// Una línea-string es un único span sin estilo. Una línea vacía ("") es un
		// span con texto "" (ancho 0): conserva la línea en blanco (afecta a .height).
		return []span{{text: string(line)}}, ""
	case *lua.LTable:
		// Array de Spans. Cada elemento es una tabla `{text, style?}`.
		spans := make([]span, 0, line.Len())
		var spanErr string
		i := 0
		line.ForEach(func(_, sv lua.LValue) {
			if spanErr != "" {
				return
			}
			i++
			st, ok := sv.(*lua.LTable)
			if !ok {
				spanErr = fmt.Sprintf("el span %d debe ser una tabla {text, style?}", i)
				return
			}
			text, ok := st.RawGetString("text").(lua.LString)
			if !ok {
				spanErr = fmt.Sprintf("el span %d necesita un campo `text` de tipo string", i)
				return
			}
			sp := span{text: string(text)}
			if styleVal := st.RawGetString("style"); styleVal != lua.LNil {
				parsed, err := parseStyle(L, styleVal)
				if err != "" {
					spanErr = fmt.Sprintf("el span %d: %s", i, err)
					return
				}
				sp.st = parsed
			}
			spans = append(spans, sp)
		})
		return spans, spanErr
	default:
		return nil, fmt.Sprintf("cada línea debe ser un string o un array de spans, no %s", v.Type().String())
	}
}

// parseStyle convierte una tabla `Style` Lua (`{fg?, bg?, bold?, italic?,
// underline?, reverse?}`, §9.2) a un `*style` Go, validando los colores. Los
// colores son **literales**: un string "#rrggbb" o un índice 0-255 (número o
// string numérica); los nombres semánticos NO son del core (G22), así que un
// string que no sea "#rrggbb" ni un número en rango es `EINVAL`. Devuelve un
// mensaje de error (no vacío) en lugar de lanzar, para componer el contexto.
func parseStyle(L *lua.LState, v lua.LValue) (*style, string) {
	t, ok := v.(*lua.LTable)
	if !ok {
		return nil, "`style` debe ser una tabla"
	}
	s := &style{}

	if fg := t.RawGetString("fg"); fg != lua.LNil {
		norm, err := normalizeColor(fg)
		if err != "" {
			return nil, "style.fg: " + err
		}
		s.fg, s.fgSet = norm, true
	}
	if bg := t.RawGetString("bg"); bg != lua.LNil {
		norm, err := normalizeColor(bg)
		if err != "" {
			return nil, "style.bg: " + err
		}
		s.bg, s.bgSet = norm, true
	}
	s.bold = lua.LVAsBool(t.RawGetString("bold"))
	s.italic = lua.LVAsBool(t.RawGetString("italic"))
	s.underline = lua.LVAsBool(t.RawGetString("underline"))
	s.reverse = lua.LVAsBool(t.RawGetString("reverse"))
	return s, ""
}

// normalizeColor valida y normaliza un color literal de `Style` (§9.2) a su forma
// canónica en string. Acepta:
//   - un string "#rrggbb" (seis dígitos hex tras '#'), normalizado a minúsculas;
//   - un índice 0-255, como número Lua o como string numérica, normalizado al
//     decimal en string ("42").
//
// Cualquier otra cosa (un nombre semántico como "accent", un hex de longitud
// equivocada, un índice fuera de rango) es error: los nombres son del theme del
// toolkit (G22), no del core.
func normalizeColor(v lua.LValue) (string, string) {
	switch c := v.(type) {
	case lua.LNumber:
		f := float64(c)
		i := int(f)
		if float64(i) != f || i < 0 || i > 255 {
			return "", fmt.Sprintf("índice de color debe ser un entero 0-255, no %v", f)
		}
		return strconv.Itoa(i), ""
	case lua.LString:
		s := string(c)
		if strings.HasPrefix(s, "#") {
			if !isHexColor(s) {
				return "", fmt.Sprintf("color hex debe ser \"#rrggbb\" (6 dígitos hex), no %q", s)
			}
			return strings.ToLower(s), ""
		}
		// Una string numérica también es un índice válido (azúcar para quien guarde
		// el índice como texto). Un nombre semántico cae aquí y se rechaza.
		if i, err := strconv.Atoi(s); err == nil {
			if i < 0 || i > 255 {
				return "", fmt.Sprintf("índice de color debe ser 0-255, no %d", i)
			}
			return strconv.Itoa(i), ""
		}
		return "", fmt.Sprintf("color debe ser \"#rrggbb\" o un índice 0-255, no %q (los nombres semánticos los resuelve el theme, G22)", s)
	default:
		return "", fmt.Sprintf("color debe ser un string \"#rrggbb\" o un índice 0-255, no %s", v.Type().String())
	}
}

// isHexColor comprueba que `s` tenga la forma "#rrggbb": una almohadilla seguida
// de exactamente seis dígitos hexadecimales.
func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// uiCaps implementa `nu.ui.caps() -> {colors, kitty_keyboard, mouse, images}`
// (§9.2): las capacidades del terminal. En S22 no hay un terminal vivo del que
// interrogar protocolos (eso es la Fase 6), así que se detecta lo que se puede de
// forma estática por el entorno (`COLORTERM`/`TERM` → número de colores) y el
// resto se deja en un default conservador (false): kitty_keyboard/mouse/images se
// confirman con una negociación de protocolo que aún no existe. Es deny-by-default
// (como `nu.has`, §2): no afirmar una capacidad que no se ha podido comprobar.
func (rt *Runtime) uiCaps(L *lua.LState) int {
	caps := L.NewTable()
	caps.RawSetString("colors", lua.LNumber(detectColors()))
	caps.RawSetString("kitty_keyboard", lua.LBool(false))
	caps.RawSetString("mouse", lua.LBool(false))
	caps.RawSetString("images", lua.LBool(false))
	L.Push(caps)
	return 1
}

// detectColors estima el número de colores del terminal por el entorno, sin tocar
// el terminal (la negociación real es Fase 6). `COLORTERM=truecolor`/`24bit` →
// 16M (1<<24); un `TERM` con "256color" → 256; un `TERM` no vacío → 16; sin TERM
// (headless/CI/redirigido) → 256 como default razonable (la mayoría de terminales
// modernos lo soportan, y el render degrada a lo que de verdad haya, §9.2). No es
// un sniffing frágil: es una pista, y el compositor (S29) degrada con seguridad.
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
