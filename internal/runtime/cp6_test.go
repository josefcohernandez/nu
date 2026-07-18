package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// CP-6 · "Render y búsqueda a escala de repo, en headless" (checkpoint de
// integración tras S27, **cierra la Fase 5 — Texto y búsqueda**). Prueba de humo
// **todo inspeccionable en tests sin pintar pantalla** (plan, §"CP-6"):
//
//	(a) `enu.text.markdown` (S23) de un documento → un `Block` con dimensiones.
//	(b) `enu.text.highlight` (S24) de un `.go` → un `Block` con varios spans.
//	(c) `enu.text.diff` (S25) de dos versiones de un fichero → hunks correctos
//	    y, con render, un `Block`.
//	(d) `enu.search.grep` (S27) y `enu.search.fuzzy` (S27) sobre un árbol de
//	    prueba → matches con su forma y un ranking estable.
//
// Las piezas pesadas (las que la UI de la Fase 6 solo *coloca*) quedan así
// validadas de extremo a extremo. Si CP-6 pasa, la Fase 5 queda cerrada (tablero)
// y el puntero avanza a S28.

// TestCP6RenderYBusqueda monta un pequeño "repo" en disco (un README markdown, un
// `.go`, dos versiones de un fichero) y ejercita las cuatro capacidades juntas en
// un solo runtime, comprobando lo esencial de cada salida sin pintar nada.
func TestCP6RenderYBusqueda(t *testing.T) {
	root := t.TempDir()

	// --- el "repo" de prueba ---
	mustWrite(t, filepath.Join(root, "README.md"), ""+
		"# Proyecto nu\n\n"+
		"Un runtime de **Lua** orientado a terminal.\n\n"+
		"- punto uno\n"+
		"- punto dos\n\n"+
		"```go\nfunc Hello() {}\n```\n")
	mustWrite(t, filepath.Join(root, "main.go"), ""+
		"package main\n\n"+
		"import \"fmt\"\n\n"+
		"func main() {\n"+
		"\tfmt.Println(\"hola TODO\")\n"+
		"}\n")
	mustWrite(t, filepath.Join(root, "sub", "util.go"), ""+
		"package sub\n\n"+
		"// TODO: implementar\n"+
		"func Util() int { return 0 }\n")
	mustWrite(t, filepath.Join(root, ".gitignore"), "vendor/\n")
	mustWrite(t, filepath.Join(root, "vendor", "dep.go"), "package vendor\n// TODO ignorado\n")

	h := newHarness(t)
	setURLGlobal(h, "ROOT", root) // reutiliza el helper de constante Lua (devuelve la ruta)

	// (a) markdown del README → Block con dimensiones.
	mdSrc := mustRead(t, filepath.Join(root, "README.md"))
	h.regStringFn("MD", mdSrc)
	h.eval(`
		MDB = enu.text.markdown(MD(), { width = 40 })
		MD_W, MD_H = MDB.width, MDB.height
	`)
	h.expectEval(`return tostring(MD_W <= 40 and MD_H >= 1)`, "true")

	// (b) highlight de main.go → Block con varios spans (height = nº de líneas).
	goSrc := mustRead(t, filepath.Join(root, "main.go"))
	h.regStringFn("GOSRC", goSrc)
	h.eval(`
		HLB = enu.text.highlight(GOSRC(), "go")
		HL_H = HLB.height
	`)
	// main.go tiene 7 líneas; el Block las conserva (una línea de código → una de Block).
	h.expectEval(`return tostring(HL_H)`, "7")

	// (c) diff de dos versiones del fichero → hunks + Block.
	h.regStringFn("OLD", goSrc)
	newGo := goSrc + "\n// una línea nueva al final\n"
	h.regStringFn("NEW", newGo)
	h.eval(`
		local d = enu.text.diff(OLD(), NEW(), { render = true })
		DIFF_HUNKS = #d.hunks
		DIFF_HAS_BLOCK = d.block ~= nil
		-- a == b → sin hunks (control de que el diff distingue cambio de no-cambio).
		local same = enu.text.diff(OLD(), OLD())
		DIFF_SAME = #same.hunks
	`)
	h.expectEval(`return tostring(DIFF_HUNKS >= 1)`, "true")
	h.expectEval(`return tostring(DIFF_HAS_BLOCK)`, "true")
	h.expectEval(`return tostring(DIFF_SAME)`, "0")

	// (d) grep + fuzzy sobre el árbol.
	// grep "TODO": aparece en main.go (1) y sub/util.go (1); vendor/ está ignorado
	// por .gitignore, así que su "TODO ignorado" NO debe contar → 2 matches.
	h.eval(`
		GREP_N = 0
		GREP_SUBOK = true
		enu.task.spawn(function()
			for r in enu.search.grep("TODO", { root = ROOT() }) do
				GREP_N = GREP_N + 1
				local rg = r.ranges[1]
				if r.line:sub(rg[1], rg[2]) ~= "TODO" then GREP_SUBOK = false end
			end
		end)
	`)
	h.expectEval(`return tostring(GREP_N)`, "2")
	h.expectEval(`return tostring(GREP_SUBOK)`, "true")

	// files + fuzzy: lista los ficheros del repo y rankea "util" → util.go primero.
	h.eval(`
		FUZZY_TOP = nil
		enu.task.spawn(function()
			local files = enu.search.files(ROOT())
			-- nombres base relativos para el picker.
			local names = {}
			for _, f in ipairs(files) do names[#names+1] = f end
			local ranked = enu.search.fuzzy("util", names)
			if #ranked > 0 then
				FUZZY_TOP = names[ranked[1].index]
			end
		end)
	`)
	// El mejor candidato para "util" debe ser la ruta que contiene "util.go".
	h.eval(`FUZZY_OK = FUZZY_TOP ~= nil and FUZZY_TOP:find("util%.go") ~= nil`)
	h.expectEval(`return tostring(FUZZY_OK)`, "true")
}

// --- helpers de fichero del checkpoint ----------------------------------------

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
