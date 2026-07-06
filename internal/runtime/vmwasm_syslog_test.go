package runtime

// Tests de M13b: nu.sys y nu.log sobre wasm (§7, §15). Ambos módulos síncronos.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbareagimeno/nu/internal/vmwasm"
)

// M13b.sys.1: platform/pid/hostname devuelven valores coherentes con el proceso.
func TestSysWasmBasico(t *testing.T) {
	p, err := vmwasm.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	rt := &Runtime{sys: &sysState{}}
	registerSysWasm(p, rt)
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inst.Close() })

	out := evalWasm(t, inst, `
		return nu.sys.platform() .. ":" ..
			tostring(nu.sys.pid() == math.floor(nu.sys.pid())) .. ":" ..
			tostring(#nu.sys.hostname() > 0) .. ":" ..
			math.type(nu.sys.pid())`)
	// platform es un string no vacío; pid es integer; hostname no vacío.
	parts := strings.Split(out, ":")
	if len(parts) != 4 || parts[0] == "" || parts[1] != "true" || parts[2] != "true" || parts[3] != "integer" {
		t.Fatalf("sys básico: got %q", out)
	}
}

// M13b.sys.2: setenv/env — el overlay se lee de vuelta (afecta a subprocesos).
func TestSysWasmEnvOverlay(t *testing.T) {
	p, _ := vmwasm.NewPool()
	t.Cleanup(func() { _ = p.Close() })
	rt := &Runtime{sys: &sysState{}}
	registerSysWasm(p, rt)
	inst, _ := p.NewInstance()
	t.Cleanup(func() { _ = inst.Close() })

	out := evalWasm(t, inst, `
		local antes = nu.sys.env("NU_TEST_X")
		nu.sys.setenv("NU_TEST_X", "hola")
		return tostring(antes) .. ":" .. tostring(nu.sys.env("NU_TEST_X"))`)
	if out != "nil:hola" {
		t.Fatalf("env overlay: got %q", out)
	}
}

// M13b.sys.3: mono_ms es monotónico (dos lecturas no decrecen).
func TestSysWasmMonoMs(t *testing.T) {
	p, _ := vmwasm.NewPool()
	t.Cleanup(func() { _ = p.Close() })
	registerSysWasm(p, &Runtime{sys: &sysState{}})
	inst, _ := p.NewInstance()
	t.Cleanup(func() { _ = inst.Close() })

	out := evalWasm(t, inst, `
		local a = nu.sys.mono_ms()
		local b = nu.sys.mono_ms()
		return tostring(b >= a)`)
	if out != "true" {
		t.Fatalf("mono_ms: got %q", out)
	}
}

// M13b.log.1: nu.log.info formatea (string.format) y escribe una línea al log.
func TestLogWasmEscribe(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "nu.log")
	p, _ := vmwasm.NewPool()
	t.Cleanup(func() { _ = p.Close() })
	rt := &Runtime{log: newLogger(logPath)}
	t.Cleanup(func() { _ = rt.log.close() })
	registerLogWasm(p, rt)
	inst, _ := p.NewInstance()
	t.Cleanup(func() { _ = inst.Close() })

	if _, lerr, err := inst.Eval(`nu.log.info("x=%d y=%s", 5, "z")`); err != nil || lerr != "" {
		t.Fatalf("log: lerr=%q err=%v", lerr, err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	line := string(data)
	if !strings.Contains(line, "x=5 y=z") || !strings.Contains(line, "INFO") {
		t.Fatalf("log no escribió la línea formateada: %q", line)
	}
}

// M13b.log.2: print es alias de nu.log.info (§15).
func TestLogWasmPrintAlias(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "nu.log")
	p, _ := vmwasm.NewPool()
	t.Cleanup(func() { _ = p.Close() })
	rt := &Runtime{log: newLogger(logPath)}
	t.Cleanup(func() { _ = rt.log.close() })
	registerLogWasm(p, rt)
	inst, _ := p.NewInstance()
	t.Cleanup(func() { _ = inst.Close() })

	if _, lerr, err := inst.Eval(`print("desde-print")`); err != nil || lerr != "" {
		t.Fatalf("print: lerr=%q err=%v", lerr, err)
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "desde-print") {
		t.Fatalf("print no escribió al log: %q", string(data))
	}
}
