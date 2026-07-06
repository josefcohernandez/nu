package vmwasm

// Tests de M11: el binding de UI sobre la frontera wasm. Blindan el MECANISMO
// (api.md §9): nu.ui.* como primitivas host, Region/Block como handles opacos
// (C5, sobre M10), y la pila de input + resolución de secuencias en el preludio
// (como el bus de eventos). El compositor real (compositor.go, VM-agnóstico) se
// enchufa a la interfaz UIBackend en M13; aquí un backend de grabación prueba que
// las llamadas cruzan bien y que las semánticas finas (consumo/cesión, secuencia,
// timeout, ECLOSED al destruir, recorte del blit) se cumplen.

import (
	"fmt"
	"unicode/utf8"

	"testing"
)

// --- backend de grabación -----------------------------------------------------

type recUI struct {
	w, h    int
	regions []*recRegion
	cursor  string // "regionSeq:x,y" del dueño actual del cursor (último gana)
	nextSeq int
}

func (u *recUI) Size() (int, int)             { return u.w, u.h }
func (u *recUI) Caps() map[string]any         { return map[string]any{"colors": int64(256), "mouse": true} }
func (u *recUI) ClipboardSet(s string)        {}
func (u *recUI) ClipboardGet() (string, bool) { return "portapapeles", true }

func (u *recUI) NewBlock(lines []any) (BlockObj, error) {
	w, h := 0, len(lines)
	for _, ln := range lines {
		switch v := ln.(type) {
		case string:
			if n := utf8.RuneCountInString(v); n > w {
				w = n
			}
		case []any: // spans: {text, style?}
			n := 0
			for _, sp := range v {
				if m, ok := sp.(map[string]any); ok {
					if t, ok := m["text"].(string); ok {
						n += utf8.RuneCountInString(t)
					}
				}
			}
			if n > w {
				w = n
			}
		}
	}
	return &recBlock{w: w, h: h}, nil
}

func (u *recUI) NewRegion(x, y, w, h, z int) RegionObj {
	u.nextSeq++
	r := &recRegion{ui: u, seq: u.nextSeq, x: x, y: y, w: w, h: h, z: z, alive: true, visible: true}
	u.regions = append(u.regions, r)
	return r
}

type recBlock struct{ w, h int }

func (b *recBlock) Dims() (int, int) { return b.w, b.h }

type recRegion struct {
	ui             *recUI
	seq            int
	x, y, w, h, z  int
	alive, visible bool
	log            []string // registro de métodos invocados
}

func (r *recRegion) rec(s string) { r.log = append(r.log, s) }
func (r *recRegion) Blit(x, y int, b BlockObj) {
	bw, bh := b.Dims()
	r.rec(fmt.Sprintf("blit:%d,%d/%dx%d", x, y, bw, bh))
}
func (r *recRegion) Fill(style map[string]any) { r.rec("fill") }
func (r *recRegion) Clear()                    { r.rec("clear") }
func (r *recRegion) Move(x, y int)             { r.x, r.y = x, y; r.rec(fmt.Sprintf("move:%d,%d", x, y)) }
func (r *recRegion) Resize(w, h int)           { r.w, r.h = w, h; r.rec(fmt.Sprintf("resize:%d,%d", w, h)) }
func (r *recRegion) Raise()                    { r.rec("raise") }
func (r *recRegion) Lower()                    { r.rec("lower") }
func (r *recRegion) Show()                     { r.visible = true; r.rec("show") }
func (r *recRegion) Hide()                     { r.visible = false; r.rec("hide") }
func (r *recRegion) Destroy()                  { r.alive = false; r.rec("destroy") }
func (r *recRegion) Cursor(x, y int, show bool) {
	if show {
		r.ui.cursor = fmt.Sprintf("%d:%d,%d", r.seq, x, y)
	} else {
		r.ui.cursor = fmt.Sprintf("%d:hide", r.seq)
	}
}

// uiInst crea una instancia con un backend de UI de grabación instalado.
func uiInst(t *testing.T, u *recUI) *Instance {
	t.Helper()
	p, err := NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.SetUIBackend(u)
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

func evalUI(t *testing.T, inst *Instance, chunk string) string {
	t.Helper()
	if _, lerr, err := inst.Eval(chunk); err != nil || lerr != "" {
		t.Fatalf("eval: lerr=%q err=%v", lerr, err)
	}
	out, _, _ := inst.Eval(`return tostring(out)`)
	return out
}

// --- headless -----------------------------------------------------------------

// M11.1: sin backend de UI, nu.ui NO existe y nu.has("ui") es false (G20).
func TestUIHeadless(t *testing.T) {
	inst := newInstance(t) // sin SetUIBackend
	out, _, err := inst.Eval(`return tostring(nu.ui) .. ":" .. tostring(nu.has("ui"))`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "nil:false" {
		t.Fatalf("headless: got %q (esperado nil:false)", out)
	}
}

// M11.2: con backend, nu.has("ui") es true y size/caps cruzan.
func TestUISizeCaps(t *testing.T) {
	inst := uiInst(t, &recUI{w: 80, h: 24})
	out := evalUI(t, inst, `
		local s = nu.ui.size()
		local c = nu.ui.caps()
		out = tostring(nu.has("ui")) .. ":" .. s.w .. "x" .. s.h .. ":" .. tostring(c.colors)`)
	if out != "true:80x24:256" {
		t.Fatalf("size/caps: got %q", out)
	}
}

// --- Region como handle -------------------------------------------------------

// M11.3: nu.ui.region(opts) da un handle cuyos métodos se despachan al backend.
func TestUIRegionMetodos(t *testing.T) {
	u := &recUI{w: 80, h: 24}
	inst := uiInst(t, u)
	evalUI(t, inst, `
		local r = nu.ui.region({ x = 1, y = 2, w = 10, h = 5 })
		r:move(3, 4)
		r:resize(20, 8)
		r:raise()
		out = "ok"`)
	if len(u.regions) != 1 {
		t.Fatalf("se esperaba 1 región, hay %d", len(u.regions))
	}
	r := u.regions[0]
	got := fmt.Sprint(r.log)
	if got != "[move:3,4 resize:20,8 raise]" {
		t.Fatalf("métodos de región mal despachados: %s", got)
	}
	if r.x != 3 || r.w != 20 {
		t.Fatalf("estado de la región no mutó: x=%d w=%d", r.x, r.w)
	}
}

// M11.4: dos regiones son handles independientes.
func TestUIRegionIdentidad(t *testing.T) {
	u := &recUI{w: 80, h: 24}
	inst := uiInst(t, u)
	evalUI(t, inst, `
		local a = nu.ui.region({ x = 0, y = 0, w = 5, h = 5 })
		local b = nu.ui.region({ x = 0, y = 0, w = 5, h = 5 })
		a:move(1, 1)
		b:move(9, 9)
		out = "ok"`)
	if u.regions[0].x != 1 || u.regions[1].x != 9 {
		t.Fatalf("las regiones no son independientes: a.x=%d b.x=%d", u.regions[0].x, u.regions[1].x)
	}
}

// M11.5: Region:destroy libera su handle → reusarlo da ECLOSED (M10, api.md §6).
func TestUIRegionDestroyECLOSED(t *testing.T) {
	u := &recUI{w: 80, h: 24}
	inst := uiInst(t, u)
	out := evalUI(t, inst, `
		local r = nu.ui.region({ x = 0, y = 0, w = 5, h = 5 })
		r:destroy()
		local ok, e = pcall(function() return r:move(1, 1) end)
		out = tostring(ok) .. ":" .. tostring(e.code)`)
	if out != "false:ECLOSED" {
		t.Fatalf("destroy no dio ECLOSED al reusar: got %q", out)
	}
	if u.regions[0].alive {
		t.Fatal("la región no se destruyó en el backend")
	}
}

// --- Block y blit -------------------------------------------------------------

// M11.6: nu.ui.block(lines) da un Block con .width/.height (api.md §9.2).
func TestUIBlockDims(t *testing.T) {
	inst := uiInst(t, &recUI{w: 80, h: 24})
	out := evalUI(t, inst, `
		local b = nu.ui.block({ "hola", "mundo!" })
		out = tostring(b.width) .. "x" .. tostring(b.height)`)
	if out != "6x2" {
		t.Fatalf("block dims: got %q (esperado 6x2)", out)
	}
}

// M11.7: Region:blit(x, y, block) resuelve el handle del Block en Go (round-trip
// C5): el backend ve las dimensiones del bloque, prueba de que el handle cruzó.
func TestUIBlit(t *testing.T) {
	u := &recUI{w: 80, h: 24}
	inst := uiInst(t, u)
	evalUI(t, inst, `
		local r = nu.ui.region({ x = 0, y = 0, w = 20, h = 10 })
		local b = nu.ui.block({ "abcde", "fg" })
		r:blit(2, -3, b)   -- offset negativo (viewport, G28): lo interpreta el compositor real
		out = "ok"`)
	got := fmt.Sprint(u.regions[0].log)
	if got != "[blit:2,-3/5x2]" {
		t.Fatalf("blit no resolvió el Block: %s", got)
	}
}

// M11.8: el cursor es único; la última llamada gana (api.md §9.1).
func TestUICursorUltimoGana(t *testing.T) {
	u := &recUI{w: 80, h: 24}
	inst := uiInst(t, u)
	evalUI(t, inst, `
		local a = nu.ui.region({ x = 0, y = 0, w = 5, h = 5 })
		local b = nu.ui.region({ x = 0, y = 0, w = 5, h = 5 })
		a:cursor(1, 1)
		b:cursor(7, 7)   -- gana esta
		out = "ok"`)
	// la región b es la segunda creada (seq=2)
	if u.cursor != "2:7,7" {
		t.Fatalf("el último cursor no ganó: %q", u.cursor)
	}
}

// --- pila de input ------------------------------------------------------------

// M11.9: on_input — el handler superior que consume corta la propagación; el que
// no consume deja pasar al de abajo.
func TestInputPilaConsumo(t *testing.T) {
	inst := uiInst(t, &recUI{w: 80, h: 24})
	out := evalUI(t, inst, `
		local traza = {}
		nu.ui.on_input(function(ev) traza[#traza+1] = "bajo"; return true end)   -- consume
		nu.ui.on_input(function(ev) traza[#traza+1] = "medio"; return false end) -- deja pasar
		nu.ui.on_input(function(ev) traza[#traza+1] = "alto"; return false end)  -- deja pasar
		local c = __ui_dispatch_input({ type = "key", key = "a" })
		out = table.concat(traza, ",") .. ":" .. tostring(c)`)
	// alto y medio no consumen; bajo sí. Orden de arriba a abajo.
	if out != "alto,medio,bajo:true" {
		t.Fatalf("pila de input mal despachada: got %q", out)
	}
}

// M11.10: InputHandle:pop() saca el handler de la pila.
func TestInputPop(t *testing.T) {
	inst := uiInst(t, &recUI{w: 80, h: 24})
	out := evalUI(t, inst, `
		local n = 0
		local h = nu.ui.on_input(function(ev) n = n + 1; return true end)
		__ui_dispatch_input({ type = "key", key = "a" })
		h:pop()
		local c = __ui_dispatch_input({ type = "key", key = "a" })  -- ya no hay handler
		out = tostring(n) .. ":" .. tostring(c)`)
	if out != "1:false" {
		t.Fatalf("pop no quitó el handler: got %q", out)
	}
}

// --- keymaps ------------------------------------------------------------------

// M11.11: un keymap de acorde con modificadores dispara y consume por defecto.
func TestKeymapAcorde(t *testing.T) {
	inst := uiInst(t, &recUI{w: 80, h: 24})
	out := evalUI(t, inst, `
		local hit = false
		nu.ui.keymap("ctrl+k", function() hit = true end)
		local c1 = __ui_dispatch_input({ type = "key", key = "k", mods = { ctrl = true } })  -- dispara
		local c2 = __ui_dispatch_input({ type = "key", key = "k" })                           -- sin ctrl: no
		out = tostring(hit) .. ":" .. tostring(c1) .. ":" .. tostring(c2)`)
	if out != "true:true:false" {
		t.Fatalf("keymap de acorde: got %q", out)
	}
}

// M11.12: un keymap cuyo fn devuelve false EXPLÍCITO cede la tecla, que sigue
// bajando por la pila (api.md §9.3, el patrón esc/enter del chat).
func TestKeymapCede(t *testing.T) {
	inst := uiInst(t, &recUI{w: 80, h: 24})
	out := evalUI(t, inst, `
		local traza = {}
		nu.ui.on_input(function(ev) traza[#traza+1] = "abajo"; return true end)
		nu.ui.keymap("esc", function() traza[#traza+1] = "keymap"; return false end)  -- cede
		local c = __ui_dispatch_input({ type = "key", key = "esc" })
		out = table.concat(traza, ",") .. ":" .. tostring(c)`)
	// el keymap corre pero cede; el on_input de abajo lo consume.
	if out != "keymap,abajo:true" {
		t.Fatalf("cesión de keymap: got %q", out)
	}
}

// M11.13: una secuencia "g g" dispara tras los dos acordes; uno solo no.
func TestKeymapSecuencia(t *testing.T) {
	inst := uiInst(t, &recUI{w: 80, h: 24})
	out := evalUI(t, inst, `
		local hits = 0
		nu.ui.keymap("g g", function() hits = hits + 1 end)
		local c1 = __ui_dispatch_input({ type = "key", key = "g" })  -- parcial, consume
		local c2 = __ui_dispatch_input({ type = "key", key = "g" })  -- completa, dispara
		out = tostring(hits) .. ":" .. tostring(c1) .. ":" .. tostring(c2)`)
	if out != "1:true:true" {
		t.Fatalf("secuencia de keymap: got %q", out)
	}
}

// M11.14: el timeout resetea el prefijo pendiente; la secuencia no se completa.
func TestKeymapTimeout(t *testing.T) {
	inst := uiInst(t, &recUI{w: 80, h: 24})
	out := evalUI(t, inst, `
		local hits = 0
		nu.ui.keymap("g g", function() hits = hits + 1 end)
		__ui_dispatch_input({ type = "key", key = "g" })  -- parcial
		__ui_timeout()                                    -- caduca el prefijo
		__ui_dispatch_input({ type = "key", key = "g" })  -- empieza de nuevo, no completa
		out = tostring(hits)`)
	if out != "0" {
		t.Fatalf("el timeout no reseteó la secuencia: hits=%s", out)
	}
}

// M11.15: la puerta Go FeedInput inyecta un evento y devuelve si se consumió.
func TestFeedInputGo(t *testing.T) {
	inst := uiInst(t, &recUI{w: 80, h: 24})
	if _, lerr, err := inst.Eval(`
		got_key = nil
		nu.ui.on_input(function(ev) got_key = ev.key; return true end)`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	consumed, err := inst.FeedInput(map[string]any{"type": "key", "key": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !consumed {
		t.Fatal("FeedInput debía reportar consumido")
	}
	out, _, _ := inst.Eval(`return tostring(got_key)`)
	if out != "x" {
		t.Fatalf("el handler no recibió la tecla: got %q", out)
	}
}
