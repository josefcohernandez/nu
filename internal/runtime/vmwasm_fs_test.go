package runtime

// Tests de M13b: enu.fs sobre wasm (§5). Paridad con fs.go: read/write round-trip,
// write atómico y exclusivo (EEXIST), stat inexistente→nil, list, mkdir/remove
// idempotentes, tmpdir de sesión, cwd (la única no-⏸). Las primitivas ⏸ corren en
// una task y el driver (RunTasks) las lleva a término.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

// wasmFsRun registra enu.fs sobre una Instance, evalúa `setup` (que crea tasks) y
// conduce el bucle; devuelve la global `out`.
func wasmFsRun(t *testing.T, setup string) string {
	t.Helper()
	p, err := vmwasm.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	rt := &Runtime{fs: &fsState{}}
	t.Cleanup(func() { rt.fs.closeTmpdir() })
	registerFsWasm(p, rt)
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

// M13b.fs.1: write luego read (round-trip por disco de verdad).
func TestFsWasmWriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.ToSlash(filepath.Join(dir, "hola.txt"))
	out := wasmFsRun(t, `
		enu.task.spawn(function()
			enu.fs.write("`+path+`", "contenido")
			out = enu.fs.read("`+path+`")
		end)`)
	if out != "contenido" {
		t.Fatalf("write/read: got %q", out)
	}
}

// M13b.fs.2: write{exclusive} falla con EEXIST si el fichero ya existe (G17).
func TestFsWasmWriteExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.ToSlash(filepath.Join(dir, "lock"))
	out := wasmFsRun(t, `
		enu.task.spawn(function()
			enu.fs.write("`+path+`", "1", { exclusive = true })   -- crea
			local ok, e = pcall(function()
				enu.fs.write("`+path+`", "2", { exclusive = true }) -- ya existe
			end)
			out = tostring(ok) .. ":" .. tostring(e.code)
		end)`)
	if out != "false:EEXIST" {
		t.Fatalf("write exclusive: got %q", out)
	}
}

// M13b.fs.3: stat de un fichero inexistente → nil (no lanza, §5).
func TestFsWasmStatInexistente(t *testing.T) {
	dir := t.TempDir()
	path := filepath.ToSlash(filepath.Join(dir, "noexiste"))
	out := wasmFsRun(t, `
		enu.task.spawn(function()
			out = tostring(enu.fs.stat("`+path+`"))
		end)`)
	if out != "nil" {
		t.Fatalf("stat inexistente: got %q", out)
	}
}

// M13b.fs.4: stat de un fichero real → {size, is_dir=false}.
func TestFsWasmStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := wasmFsRun(t, `
		enu.task.spawn(function()
			local s = enu.fs.stat("`+filepath.ToSlash(path)+`")
			out = tostring(s.size) .. ":" .. tostring(s.is_dir)
		end)`)
	if out != "5:false" {
		t.Fatalf("stat: got %q", out)
	}
}

// M13b.fs.5: list de un directorio con entradas.
func TestFsWasmList(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	out := wasmFsRun(t, `
		enu.task.spawn(function()
			local es = enu.fs.list("`+filepath.ToSlash(dir)+`")
			local n, dirs = 0, 0
			for _, e in ipairs(es) do n = n + 1; if e.is_dir then dirs = dirs + 1 end end
			out = tostring(n) .. ":" .. tostring(dirs)
		end)`)
	if out != "2:1" {
		t.Fatalf("list: got %q (esperado 2 entradas, 1 dir)", out)
	}
}

// M13b.fs.6: mkdir (mkdir -p) + remove idempotente (borrar lo ausente es no-op).
func TestFsWasmMkdirRemove(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.ToSlash(filepath.Join(dir, "a", "b", "c"))
	out := wasmFsRun(t, `
		enu.task.spawn(function()
			enu.fs.mkdir("`+nested+`")
			local existe = (enu.fs.stat("`+nested+`") ~= nil)
			enu.fs.remove("`+nested+`")
			enu.fs.remove("`+nested+`")   -- idempotente: no-op, no lanza
			out = tostring(existe) .. ":" .. tostring(enu.fs.stat("`+nested+`") == nil)
		end)`)
	if out != "true:true" {
		t.Fatalf("mkdir/remove: got %q", out)
	}
}

// M13b.fs.7: tmpdir de sesión — se crea una vez y se reutiliza.
func TestFsWasmTmpdir(t *testing.T) {
	out := wasmFsRun(t, `
		enu.task.spawn(function()
			local a = enu.fs.tmpdir()
			local b = enu.fs.tmpdir()
			out = tostring(a == b and #a > 0)
		end)`)
	if out != "true" {
		t.Fatalf("tmpdir: got %q", out)
	}
}

// M13b.fs.8: cwd — la única no-⏸ (se puede llamar fuera de una task).
func TestFsWasmCwd(t *testing.T) {
	out := wasmFsRun(t, `out = tostring(#enu.fs.cwd() > 0)`)
	if out != "true" {
		t.Fatalf("cwd: got %q", out)
	}
}
