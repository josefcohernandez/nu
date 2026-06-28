package runtime

// Input de `nu.ui` (api.md §9.3, sesión S31, inventario 🔒). Dos firmas públicas,
// ambas **solo estado principal** (ADR-008: `nu.ui` no cruza a workers ni a tasks
// de fondo):
//
//   - `nu.ui.on_input(fn) -> InputHandle` — apila un handler SÍNCRONO `fn(ev) ->
//     boolean` (true = consumido). El input fluye al handler SUPERIOR de la pila;
//     **quien no consume (false/nil) deja pasar al de abajo**. `InputHandle:pop()`
//     lo quita. Es un `ownedHandle` (S13): un `reload` lo suelta con el resto de
//     los handles del plugin (G2).
//   - `nu.ui.keymap(seq, fn, opts?) -> Keymap` — AZÚCAR sobre la pila: registra un
//     handler que reconoce la notación `"ctrl+k"`, `"alt+enter"` y las SECUENCIAS
//     `"g g"`. `Keymap:unmap()` lo quita. Conflictos: **la pila manda** (el keymap
//     más reciente activo gana, como cualquier handler de arriba). Un keymap CONSUME
//     por defecto (disparar el atajo es atenderlo), pero su `fn` puede devolver
//     `false` EXPLÍCITO para CEDER la tecla y que siga bajando por la pila —así el
//     chat aparta `esc`/`enter` cuando hay un modal abierto y la tecla llega al
//     widget enfocado—.
//
// LA LÓGICA 🔒 (lo que esta sesión debe blindar) es doble:
//
//  1. **Pila de input** (`dispatch`): handler superior primero; un false/nil deja
//     pasar al de abajo; un true consume (los de abajo no lo ven). `pop`/`unmap`
//     quitan y el de abajo vuelve a recibir. Bajo `pcall` por frontera (ADR-008):
//     un handler que lanza no rompe la pila ni tumba el proceso —se aísla en el
//     log y se trata como "no consumió" (deja pasar)—.
//
//  2. **Resolución de SECUENCIAS con TIMEOUT en el core** (la pieza nueva): un
//     keymap multi-paso (`"g g"`) no puede resolverse con la primera tecla —hay
//     que esperar la segunda—. El core mantiene un **buffer de secuencia
//     pendiente** y un **timer de un disparo** (un timer de S05, sobre el
//     scheduler): si el siguiente paso completa la secuencia dentro del timeout,
//     dispara `fn`; si pasa el timeout, o llega una tecla que no continúa NINGUNA
//     secuencia pendiente, se **aborta** —las teclas bufferizadas se reinyectan
//     como eventos sueltos por el resto de la pila ("se resuelve lo que haya o
//     pasa el input"), y la tecla actual se procesa normal—. Un keymap de un solo
//     paso (`"ctrl+k"`) dispara al instante (no hay nada que esperar).
//
// QUÉ ES DRIVER Y QUÉ ES LÓGICA PROBADA. Este entorno es **headless** (sin TTY
// real): no hay lector de bytes ANSI. La FUENTE de eventos para producción —raw
// mode + parseo de bytes del terminal a eventos `key`/`mouse`/`paste`— es el
// *driver* (CP-7 manual, S32+ cuando se negocie el terminal); aquí queda un
// esqueleto mínimo (conversión de un `inputEvent` a la tabla Lua) y el punto de
// inyección interno `feedInput`. **La lógica 🔒 (pila + dispatch + secuencias con
// timeout + G30) se blinda con eventos INYECTADOS** (`feedInput`, `feedTimeout`),
// sin depender de un TTY ni de relojes: el test conduce la máquina de estados paso
// a paso. El timer real solo se ejercita en un camino vivo.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// inputHandleTypeName / keymapTypeName identifican las metatablas de los handles
// opacos `InputHandle` (de `on_input`, con `:pop()`) y `Keymap` (de `keymap`, con
// `:unmap()`). Userdata opaco como Region/Block: Lua los pasa de vuelta, no
// inspecciona su interior.
const inputHandleTypeName = "nu.ui.InputHandle"
const keymapTypeName = "nu.ui.Keymap"

// defaultSeqTimeout es el timeout por defecto para resolver una secuencia de teclas
// (`"g g"`): cuánto espera el core el siguiente paso antes de abortar la secuencia
// (§9.3). 500 ms es el valor clásico de los editores (vim `timeoutlen`): largo para
// que un humano encadene dos teclas, corto para que un paso suelto no se quede
// "pegado" perceptiblemente. Configurable por `opts.timeout_ms` en `keymap`.
const defaultSeqTimeout = 500 * time.Millisecond

// chord es un paso de una secuencia: una tecla más sus modificadores normalizados.
// `key` es el nombre canónico de la tecla en minúsculas ("g", "enter", "tab", "k",
// ...); `mods` es el conjunto de modificadores activos. Comparar dos chords es
// comparar `key` y el set de mods —el orden en que se escribieron ("ctrl+shift+x"
// vs "shift+ctrl+x") no importa—.
type chord struct {
	key  string
	mods modSet
}

// modSet es el conjunto de modificadores de un chord, como flags. Independiente del
// orden de escritura, comparable por valor (un `==` entre modSets basta).
type modSet struct {
	ctrl, alt, shift, meta bool
}

// inputEvent es un evento de entrada ya normalizado (lo que un handler recibe como
// la tabla `ev` de §9.3): `{type, key?, mods?, x?, y?, text?, path?}`. Es la
// frontera entre el driver (que lo construye desde el TTY, S32+) o la inyección de
// test (`feedInput`) y la lógica de despacho. Para `paste` con imagen (G30) el
// driver/inyección rellena `pasteBytes` + `pasteIsText=false` y el core lo vuelca a
// `path` antes de entregarlo a Lua —los bytes binarios nunca cruzan a Lua—.
type inputEvent struct {
	typ  string // "key" | "mouse" | "paste"
	key  string // tecla canónica (type=="key")
	mods modSet // modificadores (type=="key")
	x, y int    // coordenadas (type=="mouse")
	hasX bool   // el evento trae coordenadas (mouse)

	// Paste (§9.3, G30). `text` para un pegado de texto. Para una imagen, el driver
	// trae los bytes en `pasteBytes` con `pasteIsText=false`: el core los vuelca a un
	// fichero de `nu.fs.tmpdir` y entrega el evento con `path` en vez de `text`.
	text        string
	path        string
	pasteBytes  []byte
	pasteIsText bool
}

// inputHandler es una entrada de la pila de input: o un handler crudo de
// `on_input` (`raw` no nil) o un keymap de `keymap` (`maps` no nil). Ambos se
// apilan en el mismo sitio —"keymap es azúcar sobre la pila"— para que el orden de
// resolución de conflictos sea uno solo (la pila: el de arriba gana). Es un
// `ownedHandle` (S13): se etiqueta con el dueño y `reload` lo suelta.
type inputHandler struct {
	in *inputState

	// raw es el handler Lua de `on_input` (`fn(ev) -> boolean`). nil si esta entrada
	// es un keymap.
	raw *lua.LFunction

	// maps son las secuencias registradas por un `keymap`. Cada `seqMap` es una
	// secuencia → fn (hoy `keymap` registra una por handler).
	maps []*seqMap

	ownerName string
	live      bool // false tras pop/unmap/release: deja de recibir y se purga
}

// seqMap es una secuencia registrada por `keymap`: los pasos parseados (`steps`),
// la función a disparar y el timeout de resolución de ESTA secuencia (de
// `opts.timeout_ms` o el default).
type seqMap struct {
	steps   []chord
	fn      *lua.LFunction
	timeout time.Duration
}

// release marca el handler muerto (ownedHandle, S13). Idempotente y silencioso (lo
// llama `reload` y, vía `untrack`, el `pop`/`unmap` manual). NO toca el registro de
// handles (lo orquesta `releaseOwnerHandles`). Si una secuencia pendiente dependía de
// él, la cancela: un handler que se va por reload no debe dejar un timer suyo armado.
func (h *inputHandler) release() {
	if h.in != nil && h.in.pendingHandler == h {
		h.in.cancelPending()
	}
	h.live = false
}

// owner devuelve el dueño con que se etiquetó al crearse (para `untrack`/reload).
func (h *inputHandler) owner() string { return h.ownerName }

// inputState es el estado de input de la sesión (§9.3, S31): la pila de handlers y
// la secuencia pendiente con su timer. Vive en `uiState` (estado principal, bajo el
// token, ADR-008): `on_input`/`keymap`/`pop`/`unmap` y el despacho (`dispatch`) lo
// tocan con el token tomado, y el timer de timeout toma el token antes de resolver.
// Por eso no lleva candado propio —el token lo serializa, como el compositor—.
type inputState struct {
	rt    *Runtime
	stack []*inputHandler // pila: el ÚLTIMO es el de ARRIBA (recibe primero)

	// Secuencia pendiente (la lógica 🔒). Cuando una tecla casa el primer paso de
	// algún keymap pero no completa ninguno, el core la bufferiza aquí y arma el
	// timer: espera el siguiente paso hasta `pendingTimeout`. `pendingBuf` son los
	// chords ya recibidos de la secuencia en curso; `pendingHandler` es el handler de
	// keymap al que pertenecen (la resolución es local a un handler: una secuencia no
	// salta de un keymap a otro de otra capa). `pendingGen` numera la secuencia activa
	// para que un timer viejo (de una secuencia ya resuelta/abortada) no dispare.
	pendingBuf     []chord
	pendingHandler *inputHandler
	pendingTimer   *oneShot
	pendingTimeout time.Duration
	pendingGen     uint64
}

// newInputState crea el estado de input vacío.
func newInputState(rt *Runtime) *inputState {
	return &inputState{rt: rt}
}

// push apila un handler (lo pone ARRIBA: recibe el input primero). Lo usan
// `on_input` y `keymap`.
func (in *inputState) push(h *inputHandler) {
	in.stack = append(in.stack, h)
}

// pushBottom inserta un handler en el FONDO de la pila (recibe el input el ÚLTIMO):
// cualquier handler ya presente —o que se apile después— lo tapa. Lo usa la red de
// SALIDA DE EMERGENCIA del kernel (driver.InstallEmergencyExit, ADR-017/G35), que debe
// ceder ante toda UI de producto y solo actuar si nada encima consume la tecla.
func (in *inputState) pushBottom(h *inputHandler) {
	in.stack = append([]*inputHandler{h}, in.stack...)
}

// purge compacta la pila quitando los handlers muertos (pop/unmap/release). Se
// llama al inicio de cada dispatch: no se borra a media iteración (un handler puede
// `pop()` a otro durante su ejecución), sino entre despachos.
func (in *inputState) purge() {
	live := in.stack[:0]
	for _, h := range in.stack {
		if h.live {
			live = append(live, h)
		}
	}
	// Anula las colas sueltas para que el GC recoja los handlers muertos.
	for i := len(live); i < len(in.stack); i++ {
		in.stack[i] = nil
	}
	in.stack = live
}

// registerInput cuelga `on_input`/`keymap` de la tabla `nu.ui` ya creada e instala
// las metatablas de `InputHandle` y `Keymap`. Lo llama `registerUI`.
func (rt *Runtime) registerInput(uiT *lua.LTable) {
	L := rt.L

	ihMt := L.NewTypeMetatable(inputHandleTypeName)
	ihIndex := L.NewTable()
	ihIndex.RawSetString("pop", L.NewFunction(rt.inputHandlePop))
	L.SetField(ihMt, "__index", ihIndex)

	kmMt := L.NewTypeMetatable(keymapTypeName)
	kmIndex := L.NewTable()
	kmIndex.RawSetString("unmap", L.NewFunction(rt.keymapUnmap))
	L.SetField(kmMt, "__index", kmIndex)

	uiT.RawSetString("on_input", L.NewFunction(rt.uiOnInput))
	uiT.RawSetString("keymap", L.NewFunction(rt.uiKeymap))
}

// uiOnInput implementa `nu.ui.on_input(fn) -> InputHandle` (§9.3): apila un handler
// síncrono crudo. Lo etiqueta con el dueño vigente y lo registra como `ownedHandle`
// (S13): un `reload` lo suelta. Devuelve un `InputHandle` con `:pop()`.
func (rt *Runtime) uiOnInput(L *lua.LState) int {
	fn := L.CheckFunction(1)
	h := &inputHandler{in: rt.ui.input, raw: fn, ownerName: rt.currentOwner(), live: true}
	rt.ui.input.push(h)
	rt.sched.track(h)

	ud := L.NewUserData()
	ud.Value = h
	L.SetMetatable(ud, L.GetTypeMetatable(inputHandleTypeName))
	L.Push(ud)
	return 1
}

// uiKeymap implementa `nu.ui.keymap(seq, fn, opts?) -> Keymap` (§9.3): AZÚCAR sobre
// la pila. Parsea `seq` (notación con modificadores y secuencias por espacios),
// apila un handler de keymap con esa secuencia y devuelve un `Keymap` con
// `:unmap()`. Un `seq` mal formado es `EINVAL` accionable. `opts.timeout_ms` (>0)
// fija el timeout de resolución de la secuencia; sin él, el default.
//
// Conflictos (§9.3): NO hay un registro global de teclas —keymap es azúcar sobre la
// pila—, así que dos keymaps para la misma `seq` conviven; el que esté MÁS ARRIBA
// (el más reciente activo) gana, y `unmap` del de arriba restaura al de abajo. Esa
// es la "la pila manda" del contrato, sin lógica de prioridad aparte.
func (rt *Runtime) uiKeymap(L *lua.LState) int {
	seq := L.CheckString(1)
	fn := L.CheckFunction(2)

	steps, err := parseSeq(seq)
	if err != "" {
		raiseError(L, CodeEINVAL, "nu.ui.keymap: "+err, lua.LNil)
		return 0
	}

	timeout := defaultSeqTimeout
	if L.GetTop() >= 3 && L.Get(3) != lua.LNil {
		opts := L.CheckTable(3)
		if n, ok := opts.RawGetString("timeout_ms").(lua.LNumber); ok {
			if ms := int(n); ms > 0 {
				timeout = time.Duration(ms) * time.Millisecond
			}
		}
	}

	h := &inputHandler{
		in:        rt.ui.input,
		maps:      []*seqMap{{steps: steps, fn: fn, timeout: timeout}},
		ownerName: rt.currentOwner(),
		live:      true,
	}
	rt.ui.input.push(h)
	rt.sched.track(h)

	ud := L.NewUserData()
	ud.Value = h
	L.SetMetatable(ud, L.GetTypeMetatable(keymapTypeName))
	L.Push(ud)
	return 1
}

// inputHandlePop implementa `InputHandle:pop()` (§9.3): quita el handler de la pila.
// El de abajo vuelve a recibir el input. Lo desregistra del registro de handles por
// dueño (S13) —un `pop` a mano no debe dejar el handler colgando en `ownerHandles`,
// fuga— y lo marca muerto (la pila lo compacta en el próximo dispatch). Idempotente:
// `pop` dos veces es inocuo.
func (rt *Runtime) inputHandlePop(L *lua.LState) int {
	h := checkInputHandler(L, 1, "InputHandle:pop")
	rt.popHandler(h)
	return 0
}

// keymapUnmap implementa `Keymap:unmap()` (§9.3): igual que `pop`, quita el keymap
// de la pila (restaura el conflicto de abajo). Idempotente.
func (rt *Runtime) keymapUnmap(L *lua.LState) int {
	h := checkInputHandler(L, 1, "Keymap:unmap")
	rt.popHandler(h)
	return 0
}

// popHandler quita un handler de la pila a mano (pop/unmap): lo marca muerto y lo
// desregistra de `ownerHandles` (S13, idempotente). Si tenía una secuencia
// pendiente, la cancela (un handler que se va no debe dejar un timer suyo armado).
func (rt *Runtime) popHandler(h *inputHandler) {
	if !h.live {
		return // idempotente: ya estaba fuera
	}
	in := rt.ui.input
	if in.pendingHandler == h {
		in.cancelPending()
	}
	h.live = false
	rt.sched.untrack(h)
}

// checkInputHandler recupera el `*inputHandler` del userdata `idx` (sirve para
// InputHandle y Keymap: ambos son el mismo tipo Go). Lanza `EINVAL` accionable si no
// es uno de esos handles. No exige `live`: pop/unmap son idempotentes (la
// idempotencia la resuelve `popHandler`, no este check).
func checkInputHandler(L *lua.LState, idx int, ctx string) *inputHandler {
	ud := L.CheckUserData(idx)
	h, ok := ud.Value.(*inputHandler)
	if !ok {
		raiseError(L, CodeEINVAL, ctx+": se esperaba el handle correcto", lua.LNil)
		return nil
	}
	return h
}

// --- Despacho: la pila (lógica 🔒) -----------------------------------------

// feedInput es el PUNTO DE INYECCIÓN interno (no público): entrega un `inputEvent`
// ya normalizado a la tubería de despacho. Lo llamarán el driver del TTY (S32+) y
// los tests. Presupone el token tomado (estado principal, ADR-008). G30: si el
// evento es un paste de imagen (bytes no-texto), aquí se vuelca a `nu.fs.tmpdir` y
// se convierte a un paste con `path` ANTES de despachar —los bytes nunca llegan a
// Lua—. Devuelve true si algún handler lo consumió.
func (in *inputState) feedInput(ev inputEvent) bool {
	in.materializePaste(&ev)
	return in.dispatch(ev)
}

// dispatch enruta un evento por la pila de input (la lógica 🔒): del handler de
// ARRIBA hacia ABAJO; el primero que CONSUME (devuelve true) corta —los de abajo no
// lo ven—; quien no consume (false/nil, o lanza) deja pasar al de abajo. Los keymaps
// (secuencias) se resuelven aquí: ver `dispatchKeymap`.
//
// Hay una secuencia pendiente activa cuando una tecla anterior casó el primer paso
// de un keymap pero aún no lo completó. La siguiente tecla la maneja `feedPending`
// (continuar/completar/abortar la secuencia), no la pila normal —la secuencia tiene
// prioridad sobre re-despachar la tecla cruda mientras está viva—.
func (in *inputState) dispatch(ev inputEvent) bool {
	in.purge()

	// Si hay una secuencia en curso y llega una tecla, la resuelve la máquina de
	// secuencias (puede completar, continuar o abortar y reinyectar). Un evento que no
	// es tecla (mouse/paste) aborta la secuencia pendiente y se despacha normal.
	if in.pendingHandler != nil {
		if ev.typ == "key" {
			return in.feedPending(ev)
		}
		in.abortPendingReinject()
		// y sigue al despacho normal de este evento no-tecla
	}

	return in.dispatchFrom(ev, len(in.stack)-1)
}

// dispatchFrom recorre la pila desde el índice `top` hacia abajo entregando `ev`.
// Para un handler crudo (`on_input`) llama a su `fn(ev)` y respeta su retorno. Para
// un keymap intenta casar la secuencia (`dispatchKeymap`): si arranca o completa
// una, consume; si no, deja pasar. El primero que consume corta.
func (in *inputState) dispatchFrom(ev inputEvent, top int) bool {
	for i := top; i >= 0; i-- {
		h := in.stack[i]
		if !h.live {
			continue
		}
		if h.raw != nil {
			if in.callRaw(h.raw, ev) {
				return true
			}
			continue
		}
		// Handler de keymap: ¿esta tecla arranca o dispara alguna de sus secuencias?
		if consumed, handled := in.dispatchKeymap(h, ev); handled {
			return consumed
		}
	}
	return false
}

// dispatchKeymap intenta casar `ev` contra las secuencias de un handler de keymap
// (la lógica 🔒 de secuencias). Devuelve `(consumed, handled)`:
//   - Si `ev` (un chord) COMPLETA una secuencia de un solo paso → dispara su fn. Un
//     keymap CONSUME por defecto (disparar = atender), salvo que su fn devuelva
//     EXPLÍCITAMENTE `false`, en cuyo caso CEDE: `(false, false)` para que el evento
//     siga bajando por la pila (api.md §9.3: "azúcar sobre la pila; quien no consume
//     deja pasar"). De esto depende el chat: sus atajos `esc`/`enter`/`tab` devuelven
//     `false` cuando hay un modal/picker abierto, para que la tecla llegue al widget
//     enfocado en vez de quedar atrapada por el keymap global.
//   - Si `ev` es el PRIMER paso de alguna secuencia multi-paso (y no completa
//     ninguna de un paso) → arma la secuencia pendiente con su timer y consume el
//     evento (lo "retiene" esperando el siguiente paso): `(true, true)`.
//   - Si `ev` no casa el primer paso de ninguna secuencia de este keymap → no lo
//     maneja: `(false, false)` (deja pasar al handler de abajo).
//
// Sólo se aplica a eventos `key` (las secuencias son de teclas). Un mouse/paste no
// lo maneja un keymap.
func (in *inputState) dispatchKeymap(h *inputHandler, ev inputEvent) (consumed, handled bool) {
	if ev.typ != "key" {
		return false, false
	}
	c := chord{key: ev.key, mods: ev.mods}

	var firstStepMatch *seqMap
	for _, m := range h.maps {
		if len(m.steps) == 0 {
			continue
		}
		if !chordEqual(m.steps[0], c) {
			continue
		}
		if len(m.steps) == 1 {
			// Secuencia de un solo paso (`"ctrl+k"`): dispara al instante. Si la fn
			// CEDE (devuelve false), no se consume y el evento sigue bajando.
			if !in.fireKeymap(m.fn) {
				return false, false
			}
			return true, true
		}
		// Primer paso de una secuencia multi-paso: candidata a pendiente.
		if firstStepMatch == nil {
			firstStepMatch = m
		}
	}
	if firstStepMatch != nil {
		// Arranca la secuencia pendiente: bufferiza este chord y arma el timer. El
		// evento queda consumido (retenido) hasta que el siguiente paso lo complete o
		// el timeout lo aborte.
		in.startPending(h, c, firstStepMatch.timeout)
		return true, true
	}
	return false, false
}

// startPending arranca una secuencia pendiente: registra el handler dueño, bufferiza
// el primer chord, fija el timeout y arma el timer de un disparo. Incrementa la
// generación para invalidar cualquier timer de una secuencia anterior.
func (in *inputState) startPending(h *inputHandler, c chord, timeout time.Duration) {
	in.cancelPending() // por si quedaba algo (no debería); incrementa la gen
	in.pendingHandler = h
	in.pendingBuf = []chord{c}
	in.pendingTimeout = timeout
	in.armTimer()
}

// feedPending procesa la SIGUIENTE tecla mientras hay una secuencia en curso (la
// lógica 🔒 de continuar/completar/abortar). Casos:
//   - El chord COMPLETA una secuencia del handler pendiente → dispara su fn, limpia.
//   - El chord CONTINÚA (prefijo de) alguna secuencia más larga → lo bufferiza y
//     re-arma el timer (espera el siguiente paso).
//   - El chord NO continúa ninguna secuencia → ABORTA: reinyecta lo bufferizado
//     como eventos sueltos por el resto de la pila ("se resuelve lo que haya o pasa
//     el input") y vuelve a despachar la tecla actual desde cero.
//
// Devuelve si el evento se consumió.
func (in *inputState) feedPending(ev inputEvent) bool {
	c := chord{key: ev.key, mods: ev.mods}
	h := in.pendingHandler
	cand := append(append([]chord(nil), in.pendingBuf...), c)

	var complete *seqMap
	continues := false
	for _, m := range h.maps {
		if len(m.steps) < len(cand) {
			continue
		}
		if !chordsPrefix(m.steps, cand) {
			continue
		}
		if len(m.steps) == len(cand) {
			if complete == nil {
				complete = m
			}
		} else {
			continues = true
		}
	}

	switch {
	case complete != nil:
		// La secuencia se completó dentro del timeout: dispara y limpia.
		fn := complete.fn
		in.cancelPending()
		in.fireKeymap(fn)
		return true
	case continues:
		// Sigue siendo prefijo de una secuencia más larga: bufferiza y re-arma.
		in.pendingBuf = cand
		in.armTimer()
		return true
	default:
		// No continúa nada: aborta la secuencia (reinyecta lo bufferizado) y procesa
		// la tecla actual desde cero por la pila normal.
		in.abortPendingReinject()
		return in.dispatchFrom(ev, len(in.stack)-1)
	}
}

// abortPendingReinject aborta la secuencia pendiente reinyectando los chords
// bufferizados como eventos `key` sueltos por la pila —EXCLUYENDO al handler de
// keymap que los retuvo (para no re-arrancar la misma secuencia en bucle)— y limpia
// el estado pendiente. Es el "se resuelve lo que haya o pasa el input" del contrato:
// la primera `g` de un `"g g"` que no se completó se entrega como una `g` normal a
// quien esté por debajo.
func (in *inputState) abortPendingReinject() {
	buf := in.pendingBuf
	owner := in.pendingHandler
	in.cancelPending()
	if owner == nil {
		return
	}
	// Índice del handler dueño en la pila: reinyecta SOLO por debajo de él (los de
	// arriba ya tuvieron su turno cuando la tecla entró la primera vez y no la
	// consumieron, salvo el keymap que la retuvo).
	idx := -1
	for i, h := range in.stack {
		if h == owner {
			idx = i
			break
		}
	}
	for _, c := range buf {
		ev := inputEvent{typ: "key", key: c.key, mods: c.mods}
		in.dispatchFrom(ev, idx-1)
	}
}

// armTimer arma (o re-arma) el timer de un disparo de la secuencia pendiente. Cancela
// el anterior e incrementa la generación, de modo que un disparo del timer viejo
// (que ya estaba en vuelo) se descarte al comprobar la generación. El callback corre
// bajo el token (toma el GIL como el painter) y, si su generación sigue siendo la
// vigente, aborta la secuencia por timeout.
func (in *inputState) armTimer() {
	if in.pendingTimer != nil {
		in.pendingTimer.stop()
	}
	in.pendingGen++
	gen := in.pendingGen
	in.pendingTimer = newOneShot(in.rt.sched, in.pendingTimeout, func() {
		// Corre bajo el token (newOneShot lo toma antes de invocar). Si la generación
		// cambió, esta secuencia ya se resolvió/abortó/re-armó: no hagas nada.
		if in.pendingGen != gen || in.pendingHandler == nil {
			return
		}
		in.abortPendingReinject()
	})
}

// cancelPending limpia el estado de secuencia pendiente y para su timer. Incrementa
// la generación para invalidar un disparo de timer en vuelo. Idempotente.
func (in *inputState) cancelPending() {
	if in.pendingTimer != nil {
		in.pendingTimer.stop()
		in.pendingTimer = nil
	}
	in.pendingGen++
	in.pendingHandler = nil
	in.pendingBuf = nil
}

// feedTimeout es una vía interna de TEST (no producción): dispara el timeout de la
// secuencia pendiente de forma SÍNCRONA y DETERMINISTA, sin esperar al reloj. Así el
// test de "entre las dos g pasa el timeout → no dispara la secuencia" no es flaky.
// Presupone el token tomado. Equivale a lo que haría el callback del timer real.
func (in *inputState) feedTimeout() {
	if in.pendingHandler == nil {
		return
	}
	in.abortPendingReinject()
}

// callRaw invoca un handler crudo de `on_input` con el evento como tabla Lua y
// devuelve si CONSUMIÓ (su retorno fue `true`). Bajo `pcall` por frontera (ADR-008):
// un handler que lanza se aísla en el log y se trata como "no consumió" (deja pasar)
// —un handler roto no rompe la pila ni tumba el proceso—. Corre sobre un thread
// efímero de `host`, como los handlers de eventos (no toca la pila del estado
// principal). Presupone el token tomado.
func (in *inputState) callRaw(fn *lua.LFunction, ev inputEvent) bool {
	s := in.rt.sched
	co, _ := s.host.NewThread()
	tbl := in.eventTable(co, ev)
	err := co.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, tbl)
	if err != nil {
		_ = in.rt.log.write(levelError, in.rt.currentOwner(),
			"un handler de nu.ui.on_input lanzó: "+errString(raisedValue(err)))
		return false // un handler roto no consume: deja pasar (ADR-008)
	}
	ret := co.Get(-1)
	co.Pop(1)
	return lua.LVAsBool(ret)
}

// fireKeymap dispara la fn de un keymap y devuelve si CONSUME la tecla. Un keymap
// consume POR DEFECTO (es un atajo: dispararlo es atenderlo), así que un `nil`/sin
// retorno consume —compatible con el uso común `keymap("q", function() ... end)`—;
// solo un `return false` EXPLÍCITO cede la tecla para que siga bajando por la pila
// (lo usa el chat para apartar `esc`/`enter` con un modal abierto). Un handler que
// LANZA no consume (deja pasar, ADR-008): un atajo roto no debe tragarse la tecla.
func (in *inputState) fireKeymap(fn *lua.LFunction) bool {
	s := in.rt.sched
	co, _ := s.host.NewThread()
	if err := co.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
		_ = in.rt.log.write(levelError, in.rt.currentOwner(),
			"un handler de nu.ui.keymap lanzó: "+errString(raisedValue(err)))
		return false
	}
	ret := co.Get(-1)
	co.Pop(1)
	return ret != lua.LFalse
}

// eventTable construye la tabla Lua `ev` (§9.3) de un `inputEvent`, sobre el thread
// `co` que va a recibirla. Sólo pone los campos pertinentes al tipo: `key`/`mods`
// para teclas, `x`/`y`/`mods` para mouse, `text` o `path` para paste (G30: nunca
// ambos, y nunca bytes crudos).
func (in *inputState) eventTable(co *lua.LState, ev inputEvent) *lua.LTable {
	t := co.NewTable()
	t.RawSetString("type", lua.LString(ev.typ))
	switch ev.typ {
	case "key":
		t.RawSetString("key", lua.LString(ev.key))
		t.RawSetString("mods", modsTable(co, ev.mods))
	case "mouse":
		if ev.hasX {
			t.RawSetString("x", lua.LNumber(ev.x))
			t.RawSetString("y", lua.LNumber(ev.y))
		}
		t.RawSetString("mods", modsTable(co, ev.mods))
	case "paste":
		// G30: un paste de imagen entrega `path` (la ruta volcada); uno de texto
		// entrega `text`. `materializePaste` ya dejó el evento en una de las dos
		// formas: si tiene `path`, es imagen; si no, texto.
		if ev.path != "" {
			t.RawSetString("path", lua.LString(ev.path))
		} else {
			t.RawSetString("text", lua.LString(ev.text))
		}
	}
	return t
}

// modsTable construye la subtabla `mods` de un evento de tecla/mouse: un set de
// flags `{ctrl?, alt?, shift?, meta?}` (solo los activos, para que `ev.mods.ctrl`
// sea `true`/`nil`). Una tecla sin modificadores da una tabla vacía.
func modsTable(co *lua.LState, m modSet) *lua.LTable {
	t := co.NewTable()
	if m.ctrl {
		t.RawSetString("ctrl", lua.LBool(true))
	}
	if m.alt {
		t.RawSetString("alt", lua.LBool(true))
	}
	if m.shift {
		t.RawSetString("shift", lua.LBool(true))
	}
	if m.meta {
		t.RawSetString("meta", lua.LBool(true))
	}
	return t
}

// --- Paste de imagen (G30) -------------------------------------------------

// materializePaste implementa G30: si el evento es un paste de contenido NO-texto
// (bytes binarios, `pasteIsText==false` con `pasteBytes` no vacío), vuelca esos
// bytes a un fichero del directorio temporal de la sesión (`nu.fs.tmpdir`) y deja el
// evento con `path` (la ruta) en vez de `text` —los bytes binarios NUNCA cruzan a
// Lua como texto (coherente con G11/§12)—. Un paste de texto se deja intacto.
//
// SÍNCRONO a propósito (decisión de S31, ver claude_decisions.md): un evento de
// input se entrega de forma síncrona desde el despacho (estamos bajo el token, no en
// una task ⏸), así que el volcado tiene que ser un write directo de Go —no un `fs`
// ⏸—. Reusa la maquinaria de `nu.fs.tmpdir` (`ensureTmpdir`) y un `os.WriteFile`
// directo: el coste es una escritura de unos KB/MB de una imagen pegada, despreciable
// frente a la latencia de un humano pegando. Si el volcado falla, se registra en el
// log y el evento se entrega como un paste vacío (sin text ni path): mejor un paste
// inerte que perder el invariante de no cruzar bytes.
func (in *inputState) materializePaste(ev *inputEvent) {
	if ev.typ != "paste" || ev.pasteIsText || len(ev.pasteBytes) == 0 {
		return
	}
	dir, err := in.rt.fs.ensureTmpdir()
	if err != nil {
		_ = in.rt.log.write(levelError, in.rt.currentOwner(),
			"nu.ui paste de imagen: no se pudo crear el tmpdir de sesión: "+err.Error())
		ev.pasteBytes = nil
		ev.text = ""
		return
	}
	path, werr := writePasteImage(dir, ev.pasteBytes)
	ev.pasteBytes = nil // suelta los bytes: ya están en disco, no cruzan a Lua
	if werr != nil {
		_ = in.rt.log.write(levelError, in.rt.currentOwner(),
			"nu.ui paste de imagen: no se pudo volcar a fichero: "+werr.Error())
		ev.text = ""
		return
	}
	ev.path = path // el evento entregado a Lua llevará `path`, no `text` (G30)
	ev.text = ""
}

// pasteSeq numera los ficheros de paste de imagen de la sesión para que dos pegados
// no colisionen. Se toca solo bajo el token (el despacho de input es estado
// principal), así que no necesita atomicidad propia.
var pasteSeq uint64

// writePasteImage vuelca los bytes de una imagen pegada a un fichero único dentro del
// tmpdir de la sesión y devuelve su ruta (G30). Nombre `paste-N.bin`: el contenido es
// opaco al core (no inspecciona el formato; la UI/el agente deciden qué hacer con la
// ruta, §9.3). Escritura directa con permisos de usuario (0600): un scratch de
// sesión, no compartido.
func writePasteImage(dir string, data []byte) (string, error) {
	pasteSeq++
	name := "paste-" + strconv.FormatUint(pasteSeq, 10) + ".bin"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// --- Notación de teclas (`parseSeq`) ----------------------------------------

// parseSeq parsea la notación de `keymap` (§9.3) a una lista de chords (los pasos de
// la secuencia). Una secuencia es uno o más chords separados por ESPACIOS ("g g",
// "ctrl+x ctrl+s"); un chord es una tecla con modificadores opcionales unidos por
// '+' ("ctrl+k", "alt+enter", "shift+tab"). Devuelve un mensaje de error (no vacío)
// si la notación es inválida (vacía, un modificador suelto sin tecla, un '+' colgado).
func parseSeq(seq string) ([]chord, string) {
	fields := strings.Fields(seq)
	if len(fields) == 0 {
		return nil, "la secuencia no puede estar vacía"
	}
	steps := make([]chord, 0, len(fields))
	for _, f := range fields {
		c, err := parseChord(f)
		if err != "" {
			return nil, err
		}
		steps = append(steps, c)
	}
	return steps, ""
}

// parseChord parsea un chord ("ctrl+k", "g", "alt+enter") a su forma canónica. Los
// modificadores reconocidos son ctrl/alt/shift/meta (con alias habituales). El
// ÚLTIMO componente es la tecla; los anteriores, modificadores. La tecla se
// normaliza a minúsculas. Errores: un componente vacío (un '+' colgado: "ctrl+"), un
// modificador como tecla final, o ningún componente.
func parseChord(s string) (chord, string) {
	parts := strings.Split(s, "+")
	var c chord
	for i, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			return chord{}, "chord mal formado (componente vacío) en " + strconv.Quote(s)
		}
		last := i == len(parts)-1
		if !last {
			// Componente intermedio: debe ser un modificador.
			if !applyMod(&c.mods, p) {
				return chord{}, "modificador desconocido " + strconv.Quote(p) + " en " + strconv.Quote(s)
			}
			continue
		}
		// Último componente: la tecla. No puede ser un modificador suelto.
		if isModName(p) {
			return chord{}, "falta la tecla tras el modificador en " + strconv.Quote(s)
		}
		c.key = canonKey(p)
	}
	if c.key == "" {
		return chord{}, "falta la tecla en " + strconv.Quote(s)
	}
	return c, ""
}

// applyMod activa el flag del modificador `p` en `m` (con alias). Devuelve false si
// `p` no es un modificador conocido.
func applyMod(m *modSet, p string) bool {
	switch p {
	case "ctrl", "control":
		m.ctrl = true
	case "alt", "option", "opt":
		m.alt = true
	case "shift":
		m.shift = true
	case "meta", "cmd", "super", "win", "command":
		m.meta = true
	default:
		return false
	}
	return true
}

// isModName indica si `p` es un nombre de modificador (para rechazar "ctrl" como
// tecla final). Reusa `applyMod` sobre un set descartable.
func isModName(p string) bool {
	var tmp modSet
	return applyMod(&tmp, p)
}

// canonKey normaliza el nombre de una tecla a su forma canónica. Hoy es la minúscula
// tal cual (las teclas con nombre como "enter"/"tab"/"esc" se dejan como el autor las
// escribió, en minúsculas); el mapeo fino de los nombres del terminal a este
// vocabulario es trabajo del driver (S32+). "space" es un alias del espacio literal.
func canonKey(p string) string {
	if p == "space" {
		return " "
	}
	return p
}

// chordEqual compara dos chords: misma tecla y mismo set de modificadores. El set se
// compara por valor (independiente del orden de escritura).
func chordEqual(a, b chord) bool { return a.key == b.key && a.mods == b.mods }

// chordsPrefix indica si `pre` es un prefijo de `steps` (los primeros `len(pre)`
// pasos coinciden). Lo usa la máquina de secuencias para decidir si una tecla
// continúa/completa una secuencia más larga.
func chordsPrefix(steps, pre []chord) bool {
	if len(pre) > len(steps) {
		return false
	}
	for i := range pre {
		if !chordEqual(steps[i], pre[i]) {
			return false
		}
	}
	return true
}
