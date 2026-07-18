package vmwasm

// Tests de M09: el mecanismo de primitivas host, síncronas y SUSPENDENTES. La
// pieza de riesgo es la suspendente: una primitiva ⏸ cede al scheduler, su
// trabajo bloqueante corre en una goroutine de fondo (sin tocar la VM), y otras
// tasks avanzan mientras tanto. Registrar el resto de primitivas (fs/proc/http/
// ws/search/text/codecs) es más de lo mismo — este test blinda el mecanismo.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// poolRun registra primitivas, evalúa setup y conduce el bucle; devuelve `out`.
func poolRun(t *testing.T, reg func(p *Pool), setup string) string {
	t.Helper()
	p, err := NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	reg(p)
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if _, lerr, err := inst.Eval(setup); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	if err := inst.RunTasks(context.Background()); err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	out, _, _ := inst.Eval(`return tostring(out)`)
	return out
}

// M09.1: una primitiva SÍNCRONA registrada (no cede) — se llama directa.
func TestPrimSincrona(t *testing.T) {
	out := poolRun(t, func(p *Pool) {
		p.Register("sys.suma", func(inst *Instance, args []any) ([]any, error) {
			a := args[0].(int64)
			b := args[1].(int64)
			return []any{a + b}, nil
		})
	}, `
		enu.task.spawn(function()
			out = tostring(enu.sys.suma(3, 4))
		end)`)
	if out != "7" {
		t.Fatalf("got %q", out)
	}
}

// M09.2: una primitiva SUSPENDENTE real — enu.fs.read leyendo un fichero de disco.
func TestPrimFsRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hola.txt")
	if err := os.WriteFile(path, []byte("contenido de prueba"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := poolRun(t, func(p *Pool) {
		p.RegisterSuspending("fs.read", func(inst *Instance, args []any) ([]any, error) {
			data, err := os.ReadFile(args[0].(string))
			if err != nil {
				return nil, &StructuredError{Code: "ENOENT", Message: err.Error()}
			}
			return []any{string(data)}, nil
		})
	}, `
		enu.task.spawn(function()
			out = enu.fs.read("`+filepath.ToSlash(path)+`")
		end)`)
	if out != "contenido de prueba" {
		t.Fatalf("got %q", out)
	}
}

// M09.3: la clave del mecanismo — mientras una task espera en una primitiva ⏸
// (que tarda), OTRA task avanza. Prueba que la suspensión cede de verdad al
// scheduler (no bloquea el bucle).
func TestPrimSuspendenteNoBloquea(t *testing.T) {
	out := poolRun(t, func(p *Pool) {
		// una primitiva ⏸ que "tarda" 50ms antes de devolver.
		p.RegisterSuspending("test.lento", func(inst *Instance, args []any) ([]any, error) {
			time.Sleep(50 * time.Millisecond)
			return []any{"lento-listo"}, nil
		})
	}, `
		local traza = {}
		out = ""
		enu.task.spawn(function()
			traza[#traza+1] = "A-pide-lento"
			local r = enu.test.lento()          -- ⏸ 50ms
			traza[#traza+1] = "A-recibe:" .. r
		end)
		enu.task.spawn(function()
			-- B corre sus tramos rápidos MIENTRAS A espera en la primitiva lenta
			enu.task.sleep(5);  traza[#traza+1] = "B-5"
			enu.task.sleep(5);  traza[#traza+1] = "B-10"
		end)
		enu.task.spawn(function()
			enu.task.sleep(100)
			out = table.concat(traza, ",")
		end)`)
	// B-5 y B-10 (a 5 y 10ms) ocurren ANTES de que A reciba (a ~50ms):
	if out != "A-pide-lento,B-5,B-10,A-recibe:lento-listo" {
		t.Fatalf("la primitiva ⏸ bloqueó el bucle: got %q", out)
	}
}

// M09.4: el error estructurado de una primitiva ⏸ cruza y es capturable por pcall.
func TestPrimSuspendenteError(t *testing.T) {
	out := poolRun(t, func(p *Pool) {
		p.RegisterSuspending("fs.read", func(inst *Instance, args []any) ([]any, error) {
			return nil, &StructuredError{Code: "ENOENT", Message: "no existe"}
		})
	}, `
		enu.task.spawn(function()
			local ok, e = pcall(function() return enu.fs.read("x") end)
			out = tostring(ok) .. ":" .. tostring(e.code) .. ":" .. tostring(e.message)
		end)`)
	if out != "false:ENOENT:no existe" {
		t.Fatalf("got %q", out)
	}
}
