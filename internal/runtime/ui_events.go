package runtime

// Eventos `ui:*` del core (api.md §4, §9, sesión S32). El kernel reserva el
// namespace `ui:` (junto a `core:`, §4) para los eventos del ciclo de vida del
// terminal: cambio de tamaño, foco y suspensión/reanudación. Se emiten por el bus de
// `nu.events` (events.go), igual que `core:ready`/`core:plugin.misbehaved`, de modo
// que cualquier extensión (toolkit, chat) reaccione a ellos con `nu.events.on`.
//
// DÓNDE NACEN ESTOS EVENTOS. La FUENTE real es el **driver de TTY** (CP-7 manual,
// S33+): es quien negocia el terminal en raw mode, observa el `SIGWINCH`/secuencias de
// tamaño (→ `ui:resize`), las secuencias de foco (`ESC[I`/`ESC[O` → `ui:focus`) y la
// señal de suspensión (`SIGTSTP` → `ui:suspend`, y la reanudación → `ui:resume`).
// S32 cabla la EMISIÓN por el bus y deja los puntos de inyección (`emitUI*`,
// `resizeUI`) que el driver y los tests usan; aquí no hay lector de TTY (headless), y
// por el gating G20 estos eventos solo tienen sentido con `nu.ui` activo.
//
// SOLO ESTADO PRINCIPAL (ADR-008). Como todo `nu.ui`, la emisión corre bajo el token
// en el estado principal: `emit` (events.go) lo presupone. El driver, cuando observe
// un evento del SO en su goroutine, lo encolará al loop para emitirlo bajo el token
// (igual que el painter toma el token para pintar); las vías de aquí presuponen ese
// invariante (se llaman con el token tomado).

// emitUIEvent emite un evento `ui:*` por el bus de la Instance wasm con su payload
// (un mapa de datos, o nil para un evento sin datos). Es el punto único por el que
// pasan todos los `ui:*`. En headless, donde `nu.ui` no se registra, nadie llama a
// las vías de abajo, pero el bus sigue ahí —`ui:` es del core—.
func (rt *Runtime) emitUIEvent(name string, payload map[string]any) {
	if rt.wasm != nil {
		_ = rt.wasm.EmitEvent(name, payload)
	}
}

// resizeUI aplica un cambio de tamaño de la pantalla y emite `ui:resize` (§9.1: "el
// tamaño del terminal en celdas; cambios → evento `ui:resize`"). Reasigna las
// rejillas del compositor (que recortará las regiones al nuevo rectángulo, G1) y, si
// el tamaño REALMENTE cambió, emite `ui:resize` con `{w, h}` para que las extensiones
// recoloquen lo suyo (convención "tu región, tu `ui:resize`", §9.1). Si el tamaño es
// el mismo, es no-op silencioso (no se emite un evento espurio). Lo llaman el driver
// de TTY (S33+, ante un SIGWINCH) y los tests (vía inyección). No-op si no hay UI
// (headless): sin compositor no hay nada que redimensionar.
func (rt *Runtime) resizeUI(w, h int) {
	if rt.ui == nil {
		return
	}
	// La consulta y la reasignación del compositor van bajo el candado de la UI
	// (G44: el bombeo puede estar mutándolo); la emisión queda FUERA — EmitEvent
	// re-entra la VM y toma el mutex por su cuenta (no es reentrante).
	changed := false
	rt.withUILock(func() {
		if w != rt.ui.comp.w || h != rt.ui.comp.h {
			rt.ui.comp.resize(w, h)
			changed = true
		}
	})
	if !changed {
		return // sin cambio real: no emitir un ui:resize espurio
	}
	rt.emitUIEvent("ui:resize", map[string]any{"w": int64(w), "h": int64(h)})
}

// emitUIFocus emite `ui:focus` cuando el terminal gana o pierde el foco (§4). El
// payload es `{focused = bool}`: la extensión puede atenuar el cursor o pausar
// animaciones al perder foco. Lo dispara el driver de TTY (S33+) al recibir las
// secuencias de reporte de foco; aquí queda la vía de emisión (e inyección en test).
// No-op si no hay UI.
func (rt *Runtime) emitUIFocus(focused bool) {
	if rt.ui == nil {
		return
	}
	rt.emitUIEvent("ui:focus", map[string]any{"focused": focused})
}

// emitUISuspend emite `ui:suspend` cuando el proceso se va a suspender (`SIGTSTP`,
// Ctrl-Z): la extensión restaura el terminal (sale del modo alternativo, muestra el
// cursor) antes de que el shell recupere el control. Sin payload. Lo dispara el
// driver de TTY (S33+); aquí la vía de emisión. No-op si no hay UI.
func (rt *Runtime) emitUISuspend() {
	if rt.ui == nil {
		return
	}
	rt.emitUIEvent("ui:suspend", nil)
}

// emitUIResume emite `ui:resume` al reanudar el proceso tras una suspensión (`fg`): la
// extensión vuelve a entrar en raw mode y repinta. Sin payload. Simétrico de
// `emitUISuspend`. Lo dispara el driver de TTY (S33+); aquí la vía de emisión. No-op
// si no hay UI.
func (rt *Runtime) emitUIResume() {
	if rt.ui == nil {
		return
	}
	rt.emitUIEvent("ui:resume", nil)
}
