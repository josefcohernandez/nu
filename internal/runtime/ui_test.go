package runtime

// Tests del lado Lua de `enu.ui` §9.1 (S29): `size`, `region`, `blit`/`fill`/
// `clear`, sus errores accionables, y la integración con `reload` (una región es
// un `ownedHandle`: al recargar su plugin se destruye, G2). El recorte/viewport/
// z-order/coalescing —la lógica 🔒— se blindan en `compositor_test.go` con
// inspección de la rejilla; aquí se cubre la superficie pública desde el autor de
// extensiones (Definition of Done §2).

import (
	"testing"
	"time"
)

// newHarnessUI construye un harness con un tamaño de pantalla de `enu.ui` fijo
// (inyectado por `WithUISize`), para que `size()` y el recorte de regiones sean
// deterministas sin depender del entorno ni de un TTY. Fuerza la UI con
// `WithForceUI(true)` (gating G20, S32): el entorno de test es headless, así que sin
// forzarlo `enu.ui` no existiría.
func newHarnessUI(t *testing.T, w, h int) *harness {
	t.Helper()
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(t.TempDir()), WithUISize(w, h), WithForceUI(true))
	t.Cleanup(rt.Close)
	return &harness{t: t, rt: rt}
}

// El timer de coalescing (armado por Boot) pinta el compositor sin carrera con las
// mutaciones de Lua (toma el token antes de pintar). Tras Boot, un blit desde Lua y
// una espera mayor que el intervalo dejan un frame pintado. Es el camino vivo del
// painter (la lógica fina del coalescing está en compositor_test.go); aquí se
// ejercita la goroutine bajo -race. La inspección se hace bajo el token.
func TestUIPainterLive(t *testing.T) {
	h := newHarnessUI(t, 10, 1)
	if err := h.rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	h.eval(`
		local r = enu.ui.region({ x = 0, y = 0, w = 10, h = 1 })
		r:blit(0, 0, enu.ui.block({ "hey" }))
	`)
	// El painter pinta como mucho cada ~30 ms; espera holgado a un par de ticks.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.rt.sched.acquire()
		painted := h.rt.ui.comp.frames > 0 && !h.rt.ui.comp.dirty
		h.rt.sched.release()
		if painted {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("el painter no pintó ningún frame tras Boot + blit")
}

// enu.ui.size() devuelve el tamaño inyectado.
func TestUISize(t *testing.T) {
	h := newHarnessUI(t, 100, 40)
	h.expectEval(`local s = enu.ui.size(); return s.w, s.h`, "100", "40")
}

// enu.ui.region + blit + size del Block: el snippet crea una región, blittea un
// Block construido a mano y comprueba que no lanza. La inspección fina del
// contenido es de los tests Go (la región es opaca a Lua).
func TestUIRegionBlitSnippet(t *testing.T) {
	h := newHarnessUI(t, 20, 5)
	h.expectEval(`
		local r = enu.ui.region({ x = 1, y = 1, w = 10, h = 3, z = 2 })
		local blk = enu.ui.block({ "hola", { { text = "mundo", style = { bold = true } } } })
		r:blit(0, 0, blk)
		r:blit(0, -1, blk)   -- scroll: re-blit con otro offset (G28), no debe lanzar
		r:fill({ bg = "#202020" })
		r:clear()
		return "ok"
	`, "ok")
}

// enu.ui.region exige `w` y `h`: sin ellos, EINVAL accionable.
func TestUIRegionRequiresWH(t *testing.T) {
	h := newHarnessUI(t, 20, 5)
	if se := h.evalErr(`return enu.ui.region({ x = 0, y = 0 })`); se.Code != CodeEINVAL {
		t.Fatalf("region sin w/h: code = %q, want EINVAL", se.Code)
	}
	if se := h.evalErr(`return enu.ui.region({ w = -1, h = 2 })`); se.Code != CodeEINVAL {
		t.Fatalf("region con w negativo: code = %q, want EINVAL", se.Code)
	}
}

// Region:blit valida que el tercer argumento sea un Block: pasarle otro userdata
// (una Region) que no es un Block → EINVAL accionable de `checkBlock`.
func TestUIRegionBlitBadBlock(t *testing.T) {
	h := newHarnessUI(t, 20, 5)
	se := h.evalErr(`
		local r = enu.ui.region({ x = 0, y = 0, w = 5, h = 2 })
		return r:blit(0, 0, r)   -- una Region no es un Block
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("blit con block inválido: code = %q, want EINVAL", se.Code)
	}
}

// Region:fill con un style inválido (color no literal) → EINVAL (reusa
// parseStyle/normalizeColor, G22).
func TestUIRegionFillBadStyle(t *testing.T) {
	h := newHarnessUI(t, 20, 5)
	se := h.evalErr(`
		local r = enu.ui.region({ x = 0, y = 0, w = 5, h = 2 })
		return r:fill({ bg = "accent" })   -- nombre semántico: no es del core
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("fill con style inválido: code = %q, want EINVAL", se.Code)
	}
}

// S30: snippet que ejercita TODO el ciclo de vida de la región desde el lado del
// autor de extensiones (Definition of Done §2): move/resize/raise/lower/show/hide/
// cursor/destroy. No inspecciona la rejilla (la región es opaca a Lua); comprueba que
// la superficie no lanza en el camino feliz. La lógica fina la blindan los tests Go.
func TestUIRegionLifecycleSnippet(t *testing.T) {
	h := newHarnessUI(t, 20, 10)
	h.expectEval(`
		local a = enu.ui.region({ x = 0, y = 0, w = 5, h = 3 })
		local b = enu.ui.region({ x = 2, y = 1, w = 5, h = 3 })
		a:blit(0, 0, enu.ui.block({ "hola" }))
		a:move(1, 1)            -- recolocar
		a:resize(8, 4)          -- cambiar tamaño lógico (conserva lo que cabe)
		a:raise()               -- al frente
		b:lower()               -- al fondo
		a:hide(); a:show()      -- ocultar y volver
		a:cursor(2, 1)          -- reclama el cursor
		b:cursor(0, 0)          -- la última gana: el cursor pasa a b
		b:cursor(nil)           -- ocultar el cursor
		a:destroy()             -- elimina del compositor
		a:destroy()             -- idempotente: no peta
		return "ok"
	`, "ok")
}

// S30: tras destroy, los métodos de la región fallan limpio (EINVAL "ya destruida"
// vía checkRegion), no petan ni son no-op silenciosos para los métodos que mutan.
func TestUIRegionMethodsAfterDestroy(t *testing.T) {
	h := newHarnessUI(t, 20, 5)
	se := h.evalErr(`
		local r = enu.ui.region({ x = 0, y = 0, w = 5, h = 2 })
		r:destroy()
		return r:move(1, 1)   -- región muerta: EINVAL accionable
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("move tras destroy: code = %q, want EINVAL", se.Code)
	}
}

// S30: resize con tamaño negativo → EINVAL (coherente con enu.ui.region).
func TestUIRegionResizeNegative(t *testing.T) {
	h := newHarnessUI(t, 20, 5)
	se := h.evalErr(`
		local r = enu.ui.region({ x = 0, y = 0, w = 5, h = 2 })
		return r:resize(-1, 2)
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("resize negativo: code = %q, want EINVAL", se.Code)
	}
}

// S30: destroy desregistra la región del registro de handles por dueño (S13): tras
// destruirla a mano, un `reload` (releaseOwnerHandles) no debe encontrar un handle
// muerto que liberar (no fuga). Caja blanca, como TestUIRegionOwnedHandleG2.
func TestUIRegionDestroyUntracks(t *testing.T) {
	rt := New(WithDataDir(t.TempDir()), WithUISize(20, 5), WithForceUI(true))
	defer rt.Close()

	rt.ownerStack = append(rt.ownerStack, &pluginInfo{Name: "P"})
	reg := rt.ui.comp.addRegion(0, 0, 5, 2, 0, rt.currentOwner())
	rt.sched.track(reg)
	rt.ownerStack = rt.ownerStack[:0]

	// destroy a mano: descuelga del compositor Y desregistra del registro de handles.
	rt.sched.untrack(reg)
	reg.release()
	if _, ok := rt.sched.ownerHandles["P"]; ok {
		t.Fatal("destroy: el handle debería haberse desregistrado (no fuga)")
	}
	// reload posterior del mismo dueño no encuentra nada que liberar (no peta).
	rt.sched.releaseOwnerHandles("P")
}

// Integración con el registro de handles por dueño (S13, G2): una región creada
// bajo un dueño se etiqueta con él y se trackea; al liberarla (lo que `reload`
// hace) se descuelga del compositor y queda muerta. Test de caja blanca: crea la
// región desde Go simulando un dueño, comprueba el track y el efecto de release.
func TestUIRegionOwnedHandleG2(t *testing.T) {
	rt := New(WithDataDir(t.TempDir()), WithUISize(20, 5), WithForceUI(true))
	defer rt.Close()

	// Simula el contexto de un plugin "P" (como hace el loader al correr su init).
	rt.ownerStack = append(rt.ownerStack, &pluginInfo{Name: "P"})
	reg := rt.ui.comp.addRegion(0, 0, 5, 2, 0, rt.currentOwner())
	rt.sched.track(reg)
	rt.ownerStack = rt.ownerStack[:0]

	if reg.owner() != "P" {
		t.Fatalf("la región se etiquetó con %q, want \"P\"", reg.owner())
	}
	if got := len(rt.sched.ownerHandles["P"]); got != 1 {
		t.Fatalf("P debería tener 1 handle trackeado, tiene %d", got)
	}
	if len(rt.ui.comp.regions) != 1 {
		t.Fatalf("el compositor debería tener 1 región, tiene %d", len(rt.ui.comp.regions))
	}

	// `reload` libera los handles del dueño: la región se destruye (se descuelga del
	// compositor y queda muerta), igual que un sub o un timer.
	rt.sched.releaseOwnerHandles("P")
	if reg.alive {
		t.Fatal("G2: tras releaseOwnerHandles la región debería estar muerta")
	}
	if len(rt.ui.comp.regions) != 0 {
		t.Fatalf("G2: la región debería haberse descolgado del compositor, quedan %d", len(rt.ui.comp.regions))
	}
	// release es idempotente (reload puede liberar algo ya soltado).
	reg.release()
}

// S31: snippet de la superficie pública de input (§9.3) desde el autor de
// extensiones (Definition of Done §2): apila un `on_input`, registra un `keymap` y
// comprueba que las firmas no lanzan y devuelven handles con sus métodos. La lógica
// fina (pila, secuencias, timeout, G30) se blinda en `input_test.go`.
func TestUIInputSnippet(t *testing.T) {
	h := newHarnessUI(t, 20, 5)
	h.expectEval(`
		local ih = enu.ui.on_input(function(ev) return false end)
		local km = enu.ui.keymap("g g", function() end)
		local km2 = enu.ui.keymap("ctrl+k", function() end, { timeout_ms = 100 })
		ih:pop()      -- quita el handler de la pila
		km:unmap()    -- quita el keymap
		km2:unmap()
		ih:pop()      -- idempotente: no lanza
		return "ok"
	`, "ok")
}

// S31: un `seq` mal formado en keymap es EINVAL accionable (no un panic ni un
// keymap muerto silencioso).
func TestUIKeymapBadSeqEINVAL(t *testing.T) {
	h := newHarnessUI(t, 20, 5)
	se := h.evalErr(`enu.ui.keymap("ctrl+", function() end)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("un seq mal formado debería ser EINVAL, fue %q", se.Code)
	}
}

// (El test de caja blanca TestUIInputOwnedHandleG2 —handlers de input soltados por
// dueño en un reload, G2— se retiró con el inputState gopher en M17: la pila de
// input vive en el preludio Lua de la Instance y su liberación por dueño la ejerce
// el reload wasm (`__release_owner`, vmwasm_loader.go), cubierta por los tests de
// reload de las extensiones sobre wasm.)
