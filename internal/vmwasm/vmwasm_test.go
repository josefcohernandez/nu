package vmwasm

// Tests de M02: el blob productivo enu.wasm carga, evalúa y suspende sobre
// wazero, con multi-instancia aislada. Heredan y endurecen la validación del
// spike (spike/lua-wasm/go); M03 añade los 🔒 del trampolín.

import (
	"strings"
	"testing"
)

func newInstance(t *testing.T) *Instance {
	t.Helper()
	p, err := NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// M02.1: el intérprete oficial arranca y evalúa (baseline Lua 5.4).
func TestEvalBoot(t *testing.T) {
	inst := newInstance(t)
	out, lerr, err := inst.Eval(`return _VERSION .. " " .. tostring(1+1)`)
	if err != nil || lerr != "" {
		t.Fatalf("eval: out=%q lerr=%q err=%v", out, lerr, err)
	}
	if out != "Lua 5.4 2" {
		t.Fatalf("got %q", out)
	}
}

// M02.2: las libs del baseline están (table/string/math/utf8) y las prohibidas
// no (io/os no abiertas). El recorte fino de os es del sandbox de M04; aquí sólo
// verificamos que io/os NO se abrieron enteras.
func TestBaselineLibs(t *testing.T) {
	inst := newInstance(t)
	cases := map[string]string{
		`return type(table.concat)`:  "function",
		`return type(string.format)`: "function",
		`return type(math.floor)`:    "function",
		`return type(utf8.char)`:     "function",
		`return type(io)`:            "nil",      // no abierta
		`return type(os)`:            "nil",      // no abierta (M04 abre y recorta)
		`return type(package)`:       "nil",      // la lib package de PUC NO se abre (DM5)
		`return type(require)`:       "function", // pero SÍ el require curado del loader (M13, DM5)
	}
	for chunk, want := range cases {
		out, lerr, err := inst.Eval(chunk)
		if err != nil || lerr != "" {
			t.Fatalf("%s: lerr=%q err=%v", chunk, lerr, err)
		}
		if out != want {
			t.Fatalf("%s: got %q want %q", chunk, out, want)
		}
	}
}

// M02.3: un error de Lua se captura y el estado sigue vivo (el trampolín
// funciona; el estado es reusable tras cientos de errores).
func TestErrorRecuperacion(t *testing.T) {
	inst := newInstance(t)
	_, lerr, err := inst.Eval(`error("boom")`)
	if err != nil || !strings.Contains(lerr, "boom") {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	for i := 0; i < 100; i++ {
		if _, lerr, err = inst.Eval(`error("x")`); err != nil || lerr == "" {
			t.Fatalf("iteración %d: lerr=%q err=%v", i, lerr, err)
		}
	}
	out, _, _ := inst.Eval(`return "vivo"`)
	if out != "vivo" {
		t.Fatalf("estado tocado: %q", out)
	}
}

// M02.4: la semántica de G41 es la de referencia (42, no nil) SIN blindaje —
// el bug de gopher-lua no existe en PUC. Este test 🔒 justifica que M17 borre
// el blindaje de cancel.go.
func TestG41SemanticaReferencia(t *testing.T) {
	inst := newInstance(t)
	out, lerr, err := inst.Eval(`
		local X = nil
		local set = function(v) X = v end
		pcall(function() error("boom") end)
		set(42)
		return tostring(X)`)
	if err != nil || lerr != "" || out != "42" {
		t.Fatalf("G41: out=%q lerr=%q err=%v (want 42)", out, lerr, err)
	}
}

// M02.5: el puente ⏸ — una corrutina yield-ea a través de pcall (imposible en
// gopher-lua, G31/ADR-011) y recibe el valor del resume.
func TestYieldATravesDePcall(t *testing.T) {
	inst := newInstance(t)
	ref, err := inst.CoSpawn(`
		local ok, res = pcall(function()
			local r = nu_await("necesito-io")
			return "recibido:" .. tostring(r)
		end)
		return tostring(ok) .. ":" .. tostring(res)`)
	if err != nil {
		t.Fatal(err)
	}
	st, payload, err := inst.CoResume(ref, nil)
	if err != nil || st != CoYield || payload != "necesito-io" {
		t.Fatalf("resume 1: st=%v payload=%q err=%v", st, payload, err)
	}
	arg := "io-lista"
	st, out, err := inst.CoResume(ref, &arg)
	if err != nil || st != CoDone {
		t.Fatalf("resume 2: st=%v out=%q err=%v", st, out, err)
	}
	if out != "true:recibido:io-lista" {
		t.Fatalf("got %q", out)
	}
}

// M02.6: multi-instancia AISLADA — dos instancias no comparten estado (la base
// del aislamiento físico de los workers, M12).
func TestMultiInstanciaAislada(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	a, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })

	if _, _, err := a.Eval(`G = "soy-A"`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Eval(`G = "soy-B"`); err != nil {
		t.Fatal(err)
	}
	oa, _, _ := a.Eval(`return G`)
	ob, _, _ := b.Eval(`return G`)
	if oa != "soy-A" || ob != "soy-B" {
		t.Fatalf("las instancias comparten estado: a=%q b=%q", oa, ob)
	}
}

// M02.7: el dispatcher pluggable — el default rechaza un id inválido; uno
// instalado responde. Es la costura que M05 rellena. Aquí un dispatcher de eco
// valida el ida y vuelta de bytes por el buffer.
//
// Nota (M10): el dispatcher por defecto ya no es "rechaza todo" (M02) sino el
// registro real de primitivas (dispatchPrimitive). Se prueba su rechazo con un id
// FUERA DE RANGO (999): no hay tantas primitivas registradas, así que
// dispatchPrimitive devuelve error antes de tocar el wire. Un id bajo (0,1) hoy
// resuelve a una primitiva real (__handle_call*, registerHandleDispatch) y ya no
// serviría de "rechazo".
func TestDispatcherCostura(t *testing.T) {
	inst := newInstance(t)
	// default: rechaza un id fuera de rango
	out, _, err := inst.Eval(`local ok, r = __nu_host(999, ""); return tostring(ok)`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "false" {
		t.Fatalf("el dispatcher por defecto debía rechazar un id fuera de rango; got %q", out)
	}
	// eco: id 7 devuelve args en mayúsculas
	inst.SetDispatcher(func(id int32, args []byte) ([]byte, error) {
		if id == 7 {
			return []byte(strings.ToUpper(string(args))), nil
		}
		return nil, errUnknownID
	})
	out, _, err = inst.Eval(`local ok, r = __nu_host(7, "hola"); return tostring(ok) .. ":" .. r`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "true:HOLA" {
		t.Fatalf("eco: got %q", out)
	}
}

var errUnknownID = errStr("id desconocido")

type errStr string

func (e errStr) Error() string { return string(e) }
