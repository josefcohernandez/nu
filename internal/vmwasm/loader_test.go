package vmwasm

// Tests de M13: el loader curado sobre wasm (DM5, api.md §14). Blindan el
// mecanismo: require resuelve por nombre contra el registro Go, cachea una sola
// carga, detecta ciclos, exige unicidad de nombre y ofrece reload best-effort
// (G2); y el orden topológico por dependencias.

import (
	"context"
	"testing"
)

// loaderInst crea una instancia con módulos registrados.
func loaderInst(t *testing.T, mods map[string]string) *Instance {
	t.Helper()
	p, err := NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	for name, src := range mods {
		if err := p.RegisterModule(name, src); err != nil {
			t.Fatalf("RegisterModule %s: %v", name, err)
		}
	}
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// M13.0: enu.version.api refleja el nivel que el Runtime inyecta con SetAPIVersion
// (prep de M13d: el arranque real sobre wasm expone enu.version). En los tests
// aislados sin fijarlo queda en 0.
func TestVersionAPI(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.SetAPIVersion(7)
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	out, lerr, err := inst.Eval(`return tostring(enu.version.api)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "7" {
		t.Fatalf("enu.version.api: got %q want 7", out)
	}
}

// M13.1: require resuelve un módulo y devuelve su valor de retorno.
func TestLoaderRequireBasico(t *testing.T) {
	inst := loaderInst(t, map[string]string{
		"saludo": `return { hola = function() return "hola desde el módulo" end }`,
	})
	out, lerr, err := inst.Eval(`local m = require("saludo"); return m.hola()`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "hola desde el módulo" {
		t.Fatalf("got %q", out)
	}
}

// M13.2: require CACHEA — el módulo se ejecuta una sola vez aunque se pida N veces.
func TestLoaderRequireCachea(t *testing.T) {
	inst := loaderInst(t, map[string]string{
		"contador": `CARGAS = (CARGAS or 0) + 1; return { n = CARGAS }`,
	})
	out, lerr, err := inst.Eval(`
		local a = require("contador")
		local b = require("contador")
		local c = require("contador")
		-- misma tabla (identidad) y una sola carga
		return tostring(a == b and b == c) .. ":" .. tostring(a.n) .. ":" .. tostring(CARGAS)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "true:1:1" {
		t.Fatalf("require no cacheó: got %q (esperado true:1:1)", out)
	}
}

// M13.3: require resuelve dependencias transitivas (un módulo require-a otro).
func TestLoaderDependencias(t *testing.T) {
	inst := loaderInst(t, map[string]string{
		"base":  `return { dos = 2 }`,
		"medio": `local b = require("base"); return { cuatro = b.dos * 2 }`,
		"alto":  `local m = require("medio"); return { ocho = m.cuatro * 2 }`,
	})
	out, lerr, err := inst.Eval(`return tostring(require("alto").ocho)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "8" {
		t.Fatalf("dependencias transitivas: got %q", out)
	}
}

// M13.4: un módulo ausente da un error ENOENT accionable.
func TestLoaderModuloAusente(t *testing.T) {
	inst := loaderInst(t, nil)
	out, lerr, err := inst.Eval(`
		local ok, e = pcall(function() return require("fantasma") end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "false:ENOENT" {
		t.Fatalf("módulo ausente: got %q", out)
	}
}

// M13.5: un ciclo de require se detecta y da EINVAL (no cuelga).
func TestLoaderCiclo(t *testing.T) {
	inst := loaderInst(t, map[string]string{
		"a": `require("b"); return {}`,
		"b": `require("a"); return {}`,
	})
	out, lerr, err := inst.Eval(`
		local ok, e = pcall(function() return require("a") end)
		return tostring(ok) .. ":" .. tostring(e.code)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "false:EINVAL" {
		t.Fatalf("ciclo no detectado: got %q", out)
	}
}

// M13.6: la UNICIDAD de nombre es la identidad (api.md §14): registrar dos veces
// el mismo nombre es un error de carga.
func TestLoaderUnicidad(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if err := p.RegisterModule("dup", `return 1`); err != nil {
		t.Fatalf("primer registro: %v", err)
	}
	err = p.RegisterModule("dup", `return 2`)
	if err == nil {
		t.Fatal("un nombre duplicado debía dar error")
	}
	se, ok := err.(*StructuredError)
	if !ok || se.Code != "EEXIST" {
		t.Fatalf("error de duplicado inesperado: %v", err)
	}
}

// M13.7: reload best-effort (G2) — re-ejecuta el módulo y actualiza la caché.
func TestLoaderReload(t *testing.T) {
	inst := loaderInst(t, map[string]string{
		"v": `CARGAS = (CARGAS or 0) + 1; return { cargas = CARGAS }`,
	})
	out, lerr, err := inst.Eval(`
		local a = require("v")          -- carga 1
		local b = __loader_reload("v")  -- carga 2 (re-ejecuta)
		local c = require("v")          -- caché (sigue la 2)
		return tostring(a.cargas) .. ":" .. tostring(b.cargas) .. ":" .. tostring(c.cargas)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "1:2:2" {
		t.Fatalf("reload: got %q (esperado 1:2:2)", out)
	}
}

// M13.8: TopoOrder ordena por dependencias (una dep va antes que quien la usa) y
// es determinista.
func TestLoaderTopoOrder(t *testing.T) {
	order, err := TopoOrder(map[string][]string{
		"app":  {"ui", "net"},
		"ui":   {"core"},
		"net":  {"core"},
		"core": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	// core antes que ui/net; ui/net antes que app.
	pos := map[string]int{}
	for i, n := range order {
		pos[n] = i
	}
	ordenOK := pos["core"] < pos["ui"] && pos["core"] < pos["net"] && pos["ui"] < pos["app"] && pos["net"] < pos["app"]
	if !ordenOK {
		t.Fatalf("orden topológico incorrecto: %v", order)
	}
}

// M13.9: TopoOrder detecta un ciclo de dependencias.
func TestLoaderTopoOrderCiclo(t *testing.T) {
	_, err := TopoOrder(map[string][]string{
		"x": {"y"},
		"y": {"x"},
	})
	if err == nil {
		t.Fatal("un ciclo debía dar error")
	}
	if se, ok := err.(*StructuredError); !ok || se.Code != "EINVAL" {
		t.Fatalf("error de ciclo inesperado: %v", err)
	}
}

// M13.10: TopoOrder detecta una dependencia ausente.
func TestLoaderTopoOrderDepAusente(t *testing.T) {
	_, err := TopoOrder(map[string][]string{
		"a": {"noexiste"},
	})
	if err == nil {
		t.Fatal("una dependencia ausente debía dar error")
	}
	if se, ok := err.(*StructuredError); !ok || se.Code != "ENOENT" {
		t.Fatalf("error de dep ausente inesperado: %v", err)
	}
}

// M13.11: los workers también tienen require (api.md §13: las rutas del loader
// están disponibles dentro del worker) — comparten los módulos del padre.
func TestLoaderRequireEnWorker(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterModule("compartido", `return { valor = 99 }`); err != nil {
		t.Fatal(err)
	}
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.StopWorkers(); _ = inst.Close(); _ = p.Close() })
	if _, lerr, err := inst.Eval(`
		out = "?"
		local w = enu.worker.spawn([[
			local m = require("compartido")
			enu.worker.parent.send(m.valor)
		]])
		enu.task.spawn(function() out = tostring(w:recv()) end)`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	if err := inst.RunTasks(context.Background()); err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	out, _, _ := inst.Eval(`return tostring(out)`)
	if out != "99" {
		t.Fatalf("require en worker: got %q", out)
	}
}
