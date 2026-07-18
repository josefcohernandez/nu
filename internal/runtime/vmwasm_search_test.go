package runtime

// Tests de M13b: enu.search sobre wasm (§11). Paridad con search_test.go sobre el
// backend wasm: files respeta .gitignore/glob, fuzzy ordena estable por score,
// grep itera según llegan con {path, line_no, line, ranges} y sus rangos byte
// 1-based (reconstruibles por line:sub, coherente con S26), case/max, y los
// caminos de error accionables (root inexistente → ENOENT, opts malas → EINVAL).
// Todo hermético: el árbol se monta en un t.TempDir y las primitivas ⏸ (files,
// grep) corren dentro de una task que el driver (RunTasks) lleva a término. La
// misma arquitectura de wasmWsRun/wasmHTTPRun: NewPool → registerSearchWasm →
// NewInstance → Eval(setup con task) → RunTasks → leer la global `out`.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

// wasmSearchRun registra enu.search sobre una Instance, inyecta ROOT, evalúa el
// `setup` (que crea tasks) y conduce el bucle; devuelve la global `out`. Un plazo
// acota un cuelgue accidental (un grep que nunca cierra) a un fallo claro.
func wasmSearchRun(t *testing.T, root, setup string) string {
	t.Helper()
	p, err := vmwasm.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	registerSearchWasm(p, &Runtime{})
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	// ROOT como global Lua (la raíz absoluta del árbol de pruebas), igual que setRoot
	// del backend gopher hace con el harness.
	if _, lerr, err := inst.Eval(`ROOT = ` + luaQuote(root)); err != nil || lerr != "" {
		t.Fatalf("set ROOT: lerr=%q err=%v", lerr, err)
	}
	if _, lerr, err := inst.Eval(setup); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := inst.RunTasks(ctx); err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	out, _, _ := inst.Eval(`return tostring(out)`)
	return out
}

// luaQuote envuelve una ruta en un literal Lua largo ([[...]]), suficiente para las
// rutas de t.TempDir (sin corchetes ni saltos de línea).
func luaQuote(s string) string { return "[[" + s + "]]" }

// makeSearchTreeWasm monta el mismo árbol que makeSearchTree (search_test.go) bajo
// un t.TempDir: .gitignore, ficheros normales, uno ignorado, uno oculto y un
// subdirectorio. Devuelve la raíz.
func makeSearchTreeWasm(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(".gitignore", "ignored.txt\n*.log\nbuild/\n")
	write("a.go", "package main\nfunc Hello() {}\n// TODO: algo\n")
	write("b.txt", "hola mundo\nsegunda linea TODO\n")
	write("sub/c.go", "package sub\n// TODO en sub\nfunc World() {}\n")
	write("ignored.txt", "esto esta ignorado por gitignore\n")
	write("debug.log", "esto tambien (por *.log)\n")
	write("build/out.bin", "artefacto ignorado por build/\n")
	write(".hidden.txt", "fichero oculto\n")
	return root
}

// M13b.search.1: files respeta .gitignore (G7) — el ignorado NO aparece, el
// no-ignorado SÍ, y ocultos/.git quedan fuera por defecto. Las rutas se
// relativizan en Lua (p:sub(#ROOT+2)) y se ordenan para un veredicto determinista.
func TestSearchWasmFilesGitignore(t *testing.T) {
	root := makeSearchTreeWasm(t)
	out := wasmSearchRun(t, root, `
		enu.task.spawn(function()
			local fs = enu.search.files(ROOT)
			local rel = {}
			for _, p in ipairs(fs) do rel[#rel+1] = p:sub(#ROOT + 2) end
			table.sort(rel)
			out = table.concat(rel, ",")
		end)`)
	if out != "a.go,b.txt,sub/c.go" {
		t.Fatalf("files gitignore: got %q, want %q", out, "a.go,b.txt,sub/c.go")
	}
}

// M13b.search.2: files con glob filtra por el nombre base; el gitignore sigue
// aplicándose.
func TestSearchWasmFilesGlob(t *testing.T) {
	root := makeSearchTreeWasm(t)
	out := wasmSearchRun(t, root, `
		enu.task.spawn(function()
			local fs = enu.search.files(ROOT, { glob = "*.go" })
			local rel = {}
			for _, p in ipairs(fs) do rel[#rel+1] = p:sub(#ROOT + 2) end
			table.sort(rel)
			out = table.concat(rel, ",")
		end)`)
	if out != "a.go,sub/c.go" {
		t.Fatalf("files glob: got %q, want %q", out, "a.go,sub/c.go")
	}
}

// M13b.search.3: files con hidden=true incluye los ocultos (salvo gitignore); con
// max corta a N.
func TestSearchWasmFilesHiddenMax(t *testing.T) {
	root := makeSearchTreeWasm(t)
	out := wasmSearchRun(t, root, `
		enu.task.spawn(function()
			local fs = enu.search.files(ROOT, { hidden = true })
			local hasHidden = false
			for _, p in ipairs(fs) do if p:find("%.hidden%.txt$") then hasHidden = true end end
			local fm = enu.search.files(ROOT, { max = 2 })
			out = tostring(hasHidden) .. ":" .. tostring(#fm)
		end)`)
	if out != "true:2" {
		t.Fatalf("files hidden/max: got %q, want %q", out, "true:2")
	}
}

// M13b.search.4: fuzzy ordena por score descendente, excluye los que no casan
// (index 1-based) y es ESTABLE en los empates (inventario 🔒). "abc" casa 1,2,4;
// "zzz" (3) no casa. Cuatro "ab" idénticos salen en orden de entrada 1234.
func TestSearchWasmFuzzy(t *testing.T) {
	out := wasmSearchRun(t, t.TempDir(), `
		enu.task.spawn(function()
			local r = enu.search.fuzzy("abc", { "abc", "axbxc", "zzz", "xxabc" })
			local desc = true
			for i = 2, #r do if r[i].score > r[i-1].score then desc = false end end
			local has3 = false
			for _, m in ipairs(r) do if m.index == 3 then has3 = true end end
			-- estabilidad: cuatro candidatos idénticos → orden de entrada 1,2,3,4.
			local s = enu.search.fuzzy("ab", { "ab", "ab", "ab", "ab" })
			local stable = s[1].index .. s[2].index .. s[3].index .. s[4].index
			out = #r .. ":" .. tostring(r[1].index) .. ":" .. tostring(desc) .. ":" .. tostring(has3) .. ":" .. stable
		end)`)
	// 3 casan, el mejor es el índice 1 (contiguo desde el inicio), score descendente,
	// el no-match (3) ausente, y los empates estables (1234).
	if out != "3:1:true:false:1234" {
		t.Fatalf("fuzzy: got %q, want %q", out, "3:1:true:false:1234")
	}
}

// M13b.search.5: fuzzy con max recorta a los N mejores.
func TestSearchWasmFuzzyMax(t *testing.T) {
	out := wasmSearchRun(t, t.TempDir(), `
		enu.task.spawn(function()
			local r = enu.search.fuzzy("a", { "a", "ba", "xa", "aa" }, { max = 2 })
			out = tostring(#r)
		end)`)
	if out != "2" {
		t.Fatalf("fuzzy max: got %q, want %q", out, "2")
	}
}

// M13b.search.6: grep encuentra TODOS los matches con {path, line_no, line,
// ranges}; los ranges (byte 1-based inclusive, S26) reconstruyen el match por
// line:sub; respeta gitignore (los ignorados NO contribuyen). "TODO" está en a.go,
// b.txt y sub/c.go → 3 matches.
func TestSearchWasmGrepAll(t *testing.T) {
	root := makeSearchTreeWasm(t)
	out := wasmSearchRun(t, root, `
		enu.task.spawn(function()
			local n = 0
			local subok = true
			local lnok = true
			for r in enu.search.grep("TODO", { root = ROOT }) do
				n = n + 1
				local rg = r.ranges[1]
				if r.line:sub(rg[1], rg[2]) ~= "TODO" then subok = false end
				if r.line_no < 1 then lnok = false end
				if r.line:find("\n") then lnok = false end
			end
			out = n .. ":" .. tostring(subok) .. ":" .. tostring(lnok)
		end)`)
	if out != "3:true:true" {
		t.Fatalf("grep all: got %q, want %q", out, "3:true:true")
	}
}

// M13b.search.7: grep con glob (restringe ficheros), case (sensible/insensible) y
// max (corta). Un árbol con "hello"/"HELLO"/"hello" en x.go y "hello" en y.txt.
func TestSearchWasmGrepGlobCaseMax(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("x.go", "hello\nHELLO\nhello\n")
	mk("y.txt", "hello en txt\n")

	out := wasmSearchRun(t, root, `
		enu.task.spawn(function()
			-- glob *.go + case sensible: "hello" en minúscula → 2 líneas (solo x.go).
			local nGlob = 0
			for r in enu.search.grep("hello", { root = ROOT, glob = "*.go", case = "sensitive" }) do
				nGlob = nGlob + 1
			end
			-- case insensible en x.go: hello/HELLO/hello → 3 líneas.
			local nCI = 0
			for r in enu.search.grep("hello", { root = ROOT, glob = "*.go", case = "insensitive" }) do
				nCI = nCI + 1
			end
			-- max=1 sobre esos 3 matches → exactamente 1.
			local nMax = 0
			for r in enu.search.grep("hello", { root = ROOT, glob = "*.go", case = "insensitive", max = 1 }) do
				nMax = nMax + 1
			end
			out = nGlob .. ":" .. nCI .. ":" .. nMax
		end)`)
	if out != "2:3:1" {
		t.Fatalf("grep glob/case/max: got %q, want %q", out, "2:3:1")
	}
}

// M13b.search.8: grep paralelo completo — muchos ficheros, cada uno con un número
// conocido de matches; el pool no pierde ni duplica (total exacto). Es el gemelo
// wasm de TestSearchGrepParallelComplete.
func TestSearchWasmGrepParallel(t *testing.T) {
	root := t.TempDir()
	const nFiles = 40
	const perFile = 3
	for i := 0; i < nFiles; i++ {
		content := ""
		for j := 0; j < perFile; j++ {
			content += "MARK aqui\nruido sin nada\n"
		}
		if err := os.WriteFile(filepath.Join(root, "f"+itoa(i)+".txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := wasmSearchRun(t, root, `
		enu.task.spawn(function()
			local total = 0
			local perpath = {}
			for r in enu.search.grep("MARK", { root = ROOT }) do
				total = total + 1
				perpath[r.path] = (perpath[r.path] or 0) + 1
			end
			local files = 0
			local allok = true
			for _, v in pairs(perpath) do files = files + 1; if v ~= `+itoa(perFile)+` then allok = false end end
			out = total .. ":" .. files .. ":" .. tostring(allok)
		end)`)
	want := itoa(nFiles*perFile) + ":" + itoa(nFiles) + ":true"
	if out != want {
		t.Fatalf("grep paralelo: got %q, want %q", out, want)
	}
}

// M13b.search.9: caminos de error accionables. grep con root inexistente → ENOENT
// (en la creación del iterador, antes del consumo); files con opts no-tabla /
// glob mal tipado / max mal tipado → EINVAL; grep sin root → EINVAL; fuzzy con
// candidato no-string y opts no-tabla → EINVAL.
func TestSearchWasmErrors(t *testing.T) {
	root := t.TempDir()
	out := wasmSearchRun(t, root, `
		enu.task.spawn(function()
			local _, e1 = pcall(function()
				for r in enu.search.grep("x", { root = ROOT .. "/no-existe" }) do end
			end)
			local _, e2 = pcall(function() return enu.search.files(ROOT, 5) end)
			local _, e3 = pcall(function() return enu.search.files(ROOT, { glob = 7 }) end)
			local _, e4 = pcall(function() return enu.search.grep("x", { glob = "*.go" }) end)
			local _, e5 = pcall(function() return enu.search.fuzzy("a", { "ok", 5 }) end)
			local _, e6 = pcall(function() return enu.search.fuzzy("a", { "ok" }, 5) end)
			out = e1.code .. ":" .. e2.code .. ":" .. e3.code .. ":" .. e4.code .. ":" .. e5.code .. ":" .. e6.code
		end)`)
	if out != "ENOENT:EINVAL:EINVAL:EINVAL:EINVAL:EINVAL" {
		t.Fatalf("errores: got %q, want %q", out, "ENOENT:EINVAL:EINVAL:EINVAL:EINVAL:EINVAL")
	}
}
