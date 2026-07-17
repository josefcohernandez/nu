package runtime

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// setRoot registra un global Lua `ROOT` con la ruta `root`, para que los snippets
// la usen al construir rutas de búsqueda (mismo idioma que `BASE` en fs_test).
// Usa `regStringFn` (dual gopher/wasm): en wasm inyecta el valor sin interpolar.
func setRoot(h *harness, root string) {
	h.regStringFn("rootPath", root)
	h.eval(`ROOT = rootPath()`)
}

// Tests de `enu.search` (S27, api.md §11; inventario 🔒). Tres bloques de lógica
// a blindar:
//
//   - `files` respeta `.gitignore` (G7): ignorado NO aparece, no-ignorado SÍ;
//     `glob` filtra; `hidden` controla ocultos; `max` corta.
//   - `fuzzy` ordena por score de forma ESTABLE: empates conservan el orden de
//     entrada; no-match excluidos; `index` 1-based.
//   - `grep` itera según llegan: encuentra todos los matches con
//     `{path, line_no, line, ranges}`; ranges byte 1-based (coherente con S26);
//     el pool paralelo no pierde ni duplica; `max` corta; respeta gitignore/
//     glob/case.
//
// Las pruebas son **herméticas**: montan un árbol de ficheros en un `t.TempDir()`
// y operan sobre él, sin red ni dependencias externas. Las primitivas ⏸
// (`files`/`grep`) se ejercitan desde una task (lo exige el puente, §1.3); el
// resultado se expone por un global que la prueba lee tras `waitIdle` (idiomático
// del arnés, como en fs_test/re_test).

// makeSearchTree monta bajo un TempDir un árbol con `.gitignore`, ficheros
// normales, un fichero ignorado, un fichero oculto y un subdirectorio. Devuelve la
// raíz. Es el escenario común de los tests de `files`/`grep`.
func makeSearchTree(t *testing.T) string {
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
	write(".secretdir/d.go", "package secret\n")
	return root
}

// --- enu.search.files (🔒: respeta .gitignore) ---------------------------------

// TestSearchFilesGitignore blinda que `files` respeta `.gitignore` (G7,
// inventario 🔒): el fichero ignorado NO aparece, el no-ignorado SÍ, un
// directorio ignorado se poda entero, y `.git/`/ocultos quedan fuera por defecto.
func TestSearchFilesGitignore(t *testing.T) {
	h := newHarness(t)
	root := makeSearchTree(t)
	// Un .git/ interno con un fichero: debe podarse siempre (ruido universal).
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	setRoot(h, root)

	got := searchFilesList(h, `enu.search.files(ROOT)`)
	want := []string{"a.go", "b.txt", "sub/c.go"}
	assertRelPaths(t, root, got, want)
}

// TestSearchFilesHidden blinda `opts.hidden`: por defecto los ocultos quedan
// fuera; con `hidden = true` aparecen (salvo `.git/`, ruido permanente). El
// fichero ignorado por gitignore sigue excluido aunque `hidden` esté activo.
func TestSearchFilesHidden(t *testing.T) {
	h := newHarness(t)
	root := makeSearchTree(t)
	setRoot(h, root)

	got := searchFilesList(h, `enu.search.files(ROOT, { hidden = true })`)
	// Con hidden: aparecen `.gitignore`, `.hidden.txt` y `.secretdir/d.go` (todos
	// ficheros ocultos reales); siguen fuera los ignorados por gitignore
	// (ignored.txt, debug.log, build/out.bin). El `.gitignore` es un fichero más:
	// no se autoexcluye (sus patrones no lo nombran), y con hidden=true se lista.
	want := []string{".gitignore", ".hidden.txt", ".secretdir/d.go", "a.go", "b.txt", "sub/c.go"}
	assertRelPaths(t, root, got, want)
}

// TestSearchFilesGlob blinda `opts.glob`: filtra por el nombre base; solo los
// ficheros que casan el patrón aparecen, y el gitignore sigue aplicándose.
func TestSearchFilesGlob(t *testing.T) {
	h := newHarness(t)
	root := makeSearchTree(t)
	setRoot(h, root)

	got := searchFilesList(h, `enu.search.files(ROOT, { glob = "*.go" })`)
	want := []string{"a.go", "sub/c.go"}
	assertRelPaths(t, root, got, want)
}

// TestSearchFilesMax blinda `opts.max`: corta el listado a N resultados.
func TestSearchFilesMax(t *testing.T) {
	h := newHarness(t)
	root := makeSearchTree(t)
	setRoot(h, root)

	got := searchFilesList(h, `enu.search.files(ROOT, { max = 2 })`)
	if len(got) != 2 {
		t.Fatalf("max=2: got %d resultados %q, want 2", len(got), got)
	}
}

// TestSearchFilesErrors blinda los caminos de error: `root` inexistente →
// `ENOENT`; `opts` no-tabla y `opts.glob`/`opts.max` con tipo malo → `EINVAL`;
// fuera de una task → `EINVAL`.
func TestSearchFilesErrors(t *testing.T) {
	h := newHarness(t)
	root := t.TempDir()
	setRoot(h, root)

	// root inexistente → ENOENT (desde una task, capturado y reexpuesto).
	h.eval(`
		ERRC = nil
		enu.task.spawn(function()
			local ok, e = pcall(function() return enu.search.files(ROOT .. "/no-existe") end)
			ERRC = ok and "no-error" or e.code
		end)
	`)
	h.expectEval(`return ERRC`, "ENOENT")

	// opts no-tabla, glob no-string, max no-número → EINVAL.
	h.eval(`
		E2,E3,E4 = nil,nil,nil
		enu.task.spawn(function()
			local _, a = pcall(function() return enu.search.files(ROOT, 5) end)
			E2 = a.code
			local _, b = pcall(function() return enu.search.files(ROOT, { glob = 7 }) end)
			E3 = b.code
			local _, c = pcall(function() return enu.search.files(ROOT, { max = "x" }) end)
			E4 = c.code
		end)
	`)
	h.expectEval(`return E2`, "EINVAL")
	h.expectEval(`return E3`, "EINVAL")
	h.expectEval(`return E4`, "EINVAL")

	// Fuera de una task (en el chunk principal) → EINVAL (no se puede suspender).
	se := h.evalErr(`return enu.search.files(ROOT)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("files fuera de task: got %s, want EINVAL", se.Code)
	}
}

// --- enu.search.fuzzy (🔒: ordena por score de forma estable) ------------------

// TestSearchFuzzyOrder blinda que `fuzzy` ordena por score descendente, excluye
// los que no casan y devuelve `index` 1-based correcto.
func TestSearchFuzzyOrder(t *testing.T) {
	h := newHarness(t)
	// "abc" casa "abc" (1, contiguo desde el inicio), "axbxc" (2, disperso) y
	// "xxabc" (4, contiguo pero no al inicio). "zzz" (3) no casa → excluido.
	h.eval(`
		R = enu.search.fuzzy("abc", { "abc", "axbxc", "zzz", "xxabc" })
		IDX, SCORE = {}, {}
		for i,m in ipairs(R) do IDX[i] = m.index; SCORE[i] = m.score end
		N = #R
	`)
	h.expectEval(`return tostring(N)`, "3") // "zzz" excluido
	// El mejor es "abc" (índice 1: primer carácter + contigüidad). El peor de los
	// tres es "axbxc" (disperso). "xxabc" queda en medio (contiguo, no al inicio).
	h.expectEval(`return tostring(IDX[1])`, "1")
	// Score estrictamente descendente.
	h.eval(`
		DESC = true
		for i = 2, #R do if SCORE[i] > SCORE[i-1] then DESC = false end end
	`)
	h.expectEval(`return tostring(DESC)`, "true")
	// El no-match ("zzz", índice 3) nunca aparece.
	h.eval(`
		HAS3 = false
		for _,v in ipairs(IDX) do if v == 3 then HAS3 = true end end
	`)
	h.expectEval(`return tostring(HAS3)`, "false")
}

// TestSearchFuzzyStable es el test ESTRELLA del inventario 🔒: los empates por
// score conservan el orden de ENTRADA (estabilidad). Se construyen candidatos
// idénticos (mismo texto → mismo score) y se exige que salgan en su orden
// original 1,2,3,4 —no barajados—.
func TestSearchFuzzyStable(t *testing.T) {
	h := newHarness(t)
	// Cuatro candidatos idénticos: todos casan "ab" con EXACTAMENTE el mismo score.
	// Un orden estable debe devolverlos en orden de entrada (1,2,3,4).
	h.eval(`
		R = enu.search.fuzzy("ab", { "ab", "ab", "ab", "ab" })
		ORDER = {}
		for i,m in ipairs(R) do ORDER[i] = m.index end
	`)
	h.expectEval(`return #R == 4 and tostring(ORDER[1]..ORDER[2]..ORDER[3]..ORDER[4])`, "1234")

	// Caso mixto: dos grupos de empate. "aa" y "ba" tienen scores distintos entre
	// grupos, pero dentro de cada grupo (mismos textos) el orden de entrada se
	// preserva. Candidatos: [aXa, bXa, aYa, bYa] con query "a" → todos casan; los
	// que empiezan por "a" (índices 1,3) puntúan más (primer carácter). Estables:
	// dentro del grupo alto debe salir 1 antes que 3; en el bajo, 2 antes que 4.
	h.eval(`
		R2 = enu.search.fuzzy("a", { "aXa", "bXa", "aYa", "bYa" })
		O2 = {}
		for i,m in ipairs(R2) do O2[i] = m.index end
	`)
	// Grupo alto (empieza por a): 1 antes que 3. Grupo bajo: 2 antes que 4.
	h.eval(`
		function posOf(arr, v) for i,x in ipairs(arr) do if x==v then return i end end return -1 end
		STABLE = posOf(O2,1) < posOf(O2,3) and posOf(O2,2) < posOf(O2,4)
	`)
	h.expectEval(`return tostring(STABLE)`, "true")
}

// TestSearchFuzzyEmptyAndMax blinda: query vacío casa todo (con score 0,
// preservando el orden); `opts.max` recorta a los N mejores; opts/candidatos
// malos → EINVAL.
func TestSearchFuzzyEmptyAndMax(t *testing.T) {
	h := newHarness(t)
	// Query vacío: casa todo, orden de entrada (todos score 0, estable).
	h.eval(`
		RE = enu.search.fuzzy("", { "x", "y", "z" })
		EORDER = {}
		for i,m in ipairs(RE) do EORDER[i] = m.index end
	`)
	h.expectEval(`return #RE == 3 and tostring(EORDER[1]..EORDER[2]..EORDER[3])`, "123")

	// max recorta.
	h.eval(`RM = enu.search.fuzzy("a", { "a", "ba", "xa", "aa" }, { max = 2 })`)
	h.expectEval(`return tostring(#RM)`, "2")

	// candidatos con un no-string → EINVAL; opts no-tabla → EINVAL.
	se := h.evalErr(`return enu.search.fuzzy("a", { "ok", 5 })`)
	if se.Code != CodeEINVAL {
		t.Fatalf("fuzzy candidato no-string: got %s, want EINVAL", se.Code)
	}
	se2 := h.evalErr(`return enu.search.fuzzy("a", { "ok" }, 5)`)
	if se2.Code != CodeEINVAL {
		t.Fatalf("fuzzy opts no-tabla: got %s, want EINVAL", se2.Code)
	}
}

// TestFuzzyScoreUnit es la prueba unitaria directa del scorer (lógica nuestra):
// subsecuencia, case-insensitive, bonus de inicio de palabra/contigüidad, y
// no-match cuando un carácter falta o el orden no se respeta.
func TestFuzzyScoreUnit(t *testing.T) {
	cases := []struct {
		query, cand string
		wantMatch   bool
	}{
		{"abc", "abc", true},     // exacto
		{"abc", "aXbXc", true},   // subsecuencia dispersa
		{"abc", "ABC", true},     // case-insensitive
		{"abc", "ab", false},     // falta 'c'
		{"cba", "abc", false},    // orden no respetado
		{"", "cualquiera", true}, // query vacío casa todo
		{"go", "main.go", true},  // sufijo
		{"", "", true},           // ambos vacíos
		{"x", "", false},         // query no vacío contra cand vacío
	}
	for _, c := range cases {
		_, ok := fuzzyScore(c.query, c.cand)
		if ok != c.wantMatch {
			t.Errorf("fuzzyScore(%q,%q): match=%v, want %v", c.query, c.cand, ok, c.wantMatch)
		}
	}

	// Un acierto al inicio de palabra (tras separador) puntúa más que uno en medio.
	// "ab" en "x_ab" (la 'a' tras '_' es inicio de palabra) > "ab" en "xab".
	sBoundary, _ := fuzzyScore("ab", "x_ab")
	sMid, _ := fuzzyScore("ab", "xyab")
	if sBoundary <= sMid {
		t.Errorf("bonus inicio de palabra: x_ab=%d debe superar a xyab=%d", sBoundary, sMid)
	}

	// Un match contiguo puntúa más que uno disperso para la misma query.
	sContig, _ := fuzzyScore("abc", "abc")
	sSparse, _ := fuzzyScore("abc", "axbxc")
	if sContig <= sSparse {
		t.Errorf("bonus contigüidad: abc=%d debe superar a axbxc=%d", sContig, sSparse)
	}
}

// --- enu.search.grep (🔒: itera según llegan, ranges, paralelo, max) -----------

// TestSearchGrepAll blinda que `grep` encuentra TODOS los matches con la forma
// `{path, line_no, line, ranges}` correcta, que los ranges (byte 1-based
// inclusive, coherente con `enu.re.find_all` de S26) reconstruyen el match por
// `line:sub`, y que el pool paralelo no pierde ni duplica resultados (respeta
// gitignore: el match en el fichero ignorado no aparece).
func TestSearchGrepAll(t *testing.T) {
	h := newHarness(t)
	root := makeSearchTree(t)
	setRoot(h, root)

	// "TODO" aparece en a.go (1), b.txt (1) y sub/c.go (1): 3 matches. El fichero
	// ignored.txt/debug.log/build NO deben contribuir (gitignore).
	h.eval(`
		MATCHES = {}
		SUBOK = true
		enu.task.spawn(function()
			for r in enu.search.grep("TODO", { root = ROOT }) do
				MATCHES[#MATCHES+1] = { path = r.path, line_no = r.line_no, line = r.line }
				-- ranges reconstruyen el match exacto por line:sub (S26).
				local rg = r.ranges[1]
				if r.line:sub(rg[1], rg[2]) ~= "TODO" then SUBOK = false end
			end
		end)
	`)
	h.expectEval(`return tostring(#MATCHES)`, "3")
	h.expectEval(`return tostring(SUBOK)`, "true")

	// line_no correcto (1-based) y line completa: comprobamos que ninguna línea
	// trae el '\n' y que line_no >= 1.
	h.eval(`
		LNOK = true
		for _,m in ipairs(MATCHES) do
			if m.line_no < 1 then LNOK = false end
			if m.line:find("\n") then LNOK = false end
		end
	`)
	h.expectEval(`return tostring(LNOK)`, "true")
}

// TestSearchGrepGlobCaseMax blinda glob (restringe ficheros), case
// (sensible/insensible) y max (corta).
func TestSearchGrepGlobCaseMax(t *testing.T) {
	h := newHarness(t)
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("x.go", "hello\nHELLO\nhello\n")
	mk("y.txt", "hello en txt\n")
	setRoot(h, root)

	// glob *.go: solo x.go. case sensible: "hello" en minúscula → 2 líneas.
	h.eval(`
		N_GLOB = 0
		enu.task.spawn(function()
			for r in enu.search.grep("hello", { root = ROOT, glob = "*.go", case = "sensitive" }) do
				N_GLOB = N_GLOB + 1
			end
		end)
	`)
	h.expectEval(`return tostring(N_GLOB)`, "2")

	// case insensible en x.go: "hello"/"HELLO"/"hello" → 3 líneas.
	h.eval(`
		N_CI = 0
		enu.task.spawn(function()
			for r in enu.search.grep("hello", { root = ROOT, glob = "*.go", case = "insensitive" }) do
				N_CI = N_CI + 1
			end
		end)
	`)
	h.expectEval(`return tostring(N_CI)`, "3")

	// max corta: con max=1 sobre x.go (3 matches insensibles) → exactamente 1.
	h.eval(`
		N_MAX = 0
		enu.task.spawn(function()
			for r in enu.search.grep("hello", { root = ROOT, glob = "*.go", case = "insensitive", max = 1 }) do
				N_MAX = N_MAX + 1
			end
		end)
	`)
	h.expectEval(`return tostring(N_MAX)`, "1")
}

// TestSearchGrepParallelComplete es el test anti-pérdida/duplicado del pool
// paralelo: muchos ficheros, cada uno con un número conocido de matches, y se
// exige que el TOTAL recolectado sea exacto y que cada fichero aparezca con
// todas sus líneas (sin perder ni duplicar pese a las goroutines concurrentes).
func TestSearchGrepParallelComplete(t *testing.T) {
	h := newHarness(t)
	root := t.TempDir()
	const nFiles = 50
	const perFile = 3 // líneas con "MARK" por fichero
	for i := 0; i < nFiles; i++ {
		p := filepath.Join(root, "f"+itoa(i)+".txt")
		content := ""
		for j := 0; j < perFile; j++ {
			content += "MARK aqui\n"
			content += "ruido sin nada\n"
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setRoot(h, root)

	h.eval(`
		TOTAL = 0
		PERPATH = {}
		enu.task.spawn(function()
			for r in enu.search.grep("MARK", { root = ROOT }) do
				TOTAL = TOTAL + 1
				PERPATH[r.path] = (PERPATH[r.path] or 0) + 1
			end
		end)
	`)
	// nFiles * perFile matches en total, sin pérdidas ni duplicados.
	h.expectEval(`return tostring(TOTAL)`, itoa(nFiles*perFile))
	// Cada fichero aportó exactamente perFile (ningún fichero perdido/duplicado).
	h.eval(`
		ALLOK = true
		local count = 0
		for _,v in pairs(PERPATH) do count = count + 1; if v ~= ` + itoa(perFile) + ` then ALLOK = false end end
		if count ~= ` + itoa(nFiles) + ` then ALLOK = false end
	`)
	h.expectEval(`return tostring(ALLOK)`, "true")
}

// TestSearchGrepEarlyStopNoLeak blinda que cerrar el iterador antes de drenarlo
// (un `break` en el `for`, o alcanzar `max`) no deja goroutines del pool
// colgadas: tras la task, el número de goroutines vuelve a la línea base. Es el
// criterio "sin fugas de goroutine" del DoD.
func TestSearchGrepEarlyStopNoLeak(t *testing.T) {
	h := newHarness(t)
	root := t.TempDir()
	// Bastantes ficheros con muchos matches: el pool tendrá trabajo de sobra
	// cuando el consumidor abandone tras el primer match.
	for i := 0; i < 40; i++ {
		content := ""
		for j := 0; j < 100; j++ {
			content += "NEEDLE en linea\n"
		}
		if err := os.WriteFile(filepath.Join(root, "g"+itoa(i)+".txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setRoot(h, root)

	base := runtime.NumGoroutine()
	h.eval(`
		FIRST = nil
		enu.task.spawn(function()
			for r in enu.search.grep("NEEDLE", { root = ROOT }) do
				FIRST = r.path
				break -- abandona el iterador con el pool aún trabajando
			end
		end)
	`)
	h.expectEval(`return type(FIRST)`, "string")

	// El cleanup corre al terminar la task y desregistra el iterador de forma
	// síncrona (no bloqueante). Esta aserción es SÍNCRONA y siempre válida: al
	// volver del `break`, el `close` del cleanup ya ejecutó su `untrackGrep`, así
	// que el iterador no puede seguir rastreado. Es lo que este test de nivel Lua
	// blinda de forma determinista.
	h.rt.sched.mu.Lock()
	tracked := len(h.rt.sched.greps)
	h.rt.sched.mu.Unlock()
	if tracked != 0 {
		t.Fatalf("greps vivos tras early-stop: %d", tracked)
	}

	// El drenado real del pool (sin fugas de goroutine) ya NO se asegura contando
	// goroutines bajo un deadline: ese conteo era la fuente de la flake (2026-07-11)
	// —en runners estrangulados las goroutines canceladas tardan en morir— y hacer
	// `close` síncrono para forzarlo bloqueaba el hilo de la VM (panel clean-room
	// NO CONFORME). La no-fuga la blinda ahora, de forma determinista y sin tocar el
	// hilo de la VM, el test blanco TestGrepCloseDrainaElPool: construye el iterador
	// con el pool bloqueado, llama a `close` y espera `<-it.done` DESDE la goroutine
	// del test. Aquí sólo comprobamos que el conteo no se ha disparado sin acotar,
	// con holgura amplia y sin que un fallo dependa del planificador de un runner
	// lento (best-effort, no criterio de fuga).
	if !eventuallyLeqGoroutines(base+8, 2_000) {
		t.Logf("aviso: goroutines aún altas tras early-stop (best-effort): base=%d, ahora=%d", base, runtime.NumGoroutine())
	}
}

// grepFilesConMatches escribe `nFiles` ficheros en un directorio temporal, cada
// uno con `perFile` líneas que casan `NEEDLE`, y devuelve sus rutas. Es la
// materia prima de los tests blancos del pool: bastantes matches como para que
// los workers estén enviando a `results` cuando llega la cancelación.
func grepFilesConMatches(t *testing.T, nFiles, perFile int) []string {
	t.Helper()
	root := t.TempDir()
	files := make([]string, 0, nFiles)
	var content strings.Builder
	for j := 0; j < perFile; j++ {
		content.WriteString("NEEDLE en linea\n")
	}
	blob := []byte(content.String())
	for i := 0; i < nFiles; i++ {
		p := filepath.Join(root, "g"+itoa(i)+".txt")
		if err := os.WriteFile(p, blob, 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	return files
}

// TestGrepCloseDrainaElPool es el test BLANCO que blinda "sin fugas de goroutine"
// SIN bloquear el hilo de la VM. Construye el iterador directamente con
// `newGrepIter` y NO consume `results`: como el canal es sin buffer, los workers
// que encuentran match quedan bloqueados enviando (`it.results <- res`). Entonces
// llama a `close` (que ahora es NO bloqueante: sólo cancela) y espera `<-it.done`
// DESDE LA GOROUTINE DEL TEST —que puede bloquear libremente, no es la VM—,
// verificando que el pool drenó (todos los workers salieron, `results` cerrado).
//
// Este test MATA la reversión del fix de forma determinista: si alguien borra la
// señal de drenado (`close(it.done)` en la cerradora), `<-it.done` no se sella
// nunca y el test falla por timeout, sin depender del planificador de CI. Es la
// garantía que antes se intentaba (mal) forzando `close` a ser síncrono, lo que
// congelaba el event loop (panel clean-room NO CONFORME).
func TestGrepCloseDrainaElPool(t *testing.T) {
	files := grepFilesConMatches(t, 40, 100)
	re := regexp.MustCompile("NEEDLE")

	// Sin scheduler: el iterador se admite aislado y este test sólo mira el pool.
	it := newGrepIter(nil, re, files, 0)

	// Deja que los workers arranquen y se aparquen enviando al canal sin consumidor:
	// no es requisito de correctitud (la garantía de drenado vale igual), sólo hace
	// que el test ejercite el estado que nos importa (workers bloqueados en el send).
	time.Sleep(50 * time.Millisecond)

	it.close() // no bloqueante: cancela el contexto y vuelve

	// El drenado ocurre en las goroutines de fondo; lo esperamos AQUÍ, no en la VM.
	select {
	case <-it.done:
	case <-time.After(2 * time.Second):
		t.Fatal("close() no drenó el pool: la señal `done` no se selló en 2s (¿se borró close(it.done)?)")
	}

	// `done` se sella DESPUÉS de cerrar `results`: al llegar aquí el canal está
	// cerrado y ya no queda ningún worker enviando. Un receive devuelve ok=false.
	if _, ok := <-it.results; ok {
		t.Fatal("results entregó un valor tras done: el pool no había drenado")
	}
}

// TestGrepStopAllGrepsTerminaConGrepVivo cubre el camino de la red de seguridad
// `Runtime.Close` → `stopAllGreps` → `close()` con un grep VIVO y un CONSUMIDOR
// activo, verificando que ambos terminan. Es el hueco de test T2 del panel
// clean-room: antes no había cobertura de este camino, y con el `close` síncrono
// un worker atascado colgaba el shutdown entero. Con `close` no bloqueante,
// `stopAllGreps` vuelve enseguida y el pool drena por su cuenta.
func TestGrepStopAllGrepsTerminaConGrepVivo(t *testing.T) {
	h := newHarness(t)
	files := grepFilesConMatches(t, 40, 100)
	re := regexp.MustCompile("NEEDLE")

	it := newGrepIter(h.rt.sched, re, files, 0)
	h.rt.sched.trackGrep(it)

	// Consumidor: drena `results` en una goroutine, como haría el `next` de una task.
	consumed := make(chan struct{})
	go func() {
		defer close(consumed)
		for range it.results {
		}
	}()

	// La red de seguridad. `close` es no bloqueante, así que esto no puede colgarse
	// aunque un worker estuviera atascado en una lectura no cancelable.
	h.rt.sched.stopAllGreps()

	// El consumidor termina: `results` se cierra cuando el pool drena.
	select {
	case <-consumed:
	case <-time.After(2 * time.Second):
		t.Fatal("el consumidor no terminó: `results` no se cerró tras stopAllGreps")
	}
	// El pool drenó del todo (la cerradora selló `done`).
	select {
	case <-it.done:
	case <-time.After(2 * time.Second):
		t.Fatal("el pool no drenó tras stopAllGreps")
	}
	// Y el grep quedó desregistrado del scheduler.
	h.rt.sched.mu.Lock()
	tracked := len(h.rt.sched.greps)
	h.rt.sched.mu.Unlock()
	if tracked != 0 {
		t.Fatalf("grep aún rastreado tras stopAllGreps: %d", tracked)
	}
}

// TestSearchGrepLineaLargaNoSilenciaElFichero blinda que una línea que supera el
// tope de truncado NO aborta el escaneo del fichero: las líneas posteriores (con
// sus matches) se siguen entregando. Antes, bufio.Scanner devolvía false
// definitivo (ErrTooLong) y el resto del fichero se perdía en silencio.
func TestSearchGrepLineaLargaNoSilenciaElFichero(t *testing.T) {
	h := newHarness(t)
	root := t.TempDir()
	// Línea 1: más larga que grepMaxLine (1 MiB). Línea 2: el match buscado.
	long := strings.Repeat("x", grepMaxLine+4096)
	if err := os.WriteFile(filepath.Join(root, "gen.txt"), []byte(long+"\nAGUJA aquí\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setRoot(h, root)

	h.eval(`
		FOUND = {}
		enu.task.spawn(function()
			for r in enu.search.grep("AGUJA", { root = ROOT }) do
				FOUND[#FOUND+1] = { line_no = r.line_no, line = r.line }
			end
		end)
	`)
	h.expectEval(`return tostring(#FOUND)`, "1")
	h.expectEval(`return tostring(FOUND[1].line_no)`, "2")
}

// TestReadGrepLine cubre el lector de líneas con truncado: línea normal, línea
// que excede el tope (se recorta y se CONTINÚA en la siguiente), CRLF, y última
// línea sin salto final.
func TestReadGrepLine(t *testing.T) {
	read := func(input string, max int) []string {
		r := bufio.NewReaderSize(strings.NewReader(input), 16) // buffer mínimo: fuerza ErrBufferFull
		var lines []string
		for {
			line, eof, err := readGrepLine(r, max)
			if err != nil {
				t.Fatalf("readGrepLine: %v", err)
			}
			if eof && line == "" {
				return lines
			}
			lines = append(lines, line)
			if eof {
				return lines
			}
		}
	}

	got := read("corta\n"+strings.Repeat("L", 100)+"\nfoo\r\nbar", 10)
	want := []string{"corta", strings.Repeat("L", 10), "foo", "bar"}
	if len(got) != len(want) {
		t.Fatalf("líneas: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("línea %d: got %q, want %q", i, got[i], want[i])
		}
	}
	// Fichero terminado en '\n': sin línea vacía fantasma al final.
	if got := read("a\nb\n", 10); len(got) != 2 {
		t.Fatalf("línea fantasma tras salto final: %v", got)
	}
}

// --- helpers ------------------------------------------------------------------

// searchFilesList corre una expresión `enu.search.files(...)` desde una task y
// devuelve las rutas resultantes (ordenadas para comparación determinista).
func searchFilesList(h *harness, expr string) []string {
	h.t.Helper()
	h.eval(`
		FILES_RESULT = nil
		enu.task.spawn(function()
			FILES_RESULT = ` + expr + `
		end)
	`)
	// Lee el array resultante a Go vía un global serializado a líneas con \n.
	h.eval(`
		FILES_JOINED = table.concat(FILES_RESULT, "\n")
	`)
	joined := h.eval(`return FILES_JOINED`)
	if len(joined) != 1 {
		h.t.Fatalf("FILES_JOINED inesperado: %q", joined)
	}
	if joined[0] == "" {
		return nil
	}
	out := strings.Split(joined[0], "\n")
	sort.Strings(out)
	return out
}

// eventuallyLeqGoroutines espera hasta `timeoutMs` a que el número de goroutines
// caiga a `limit` o menos, sondeando con `runtime.GC` para que las goroutines
// terminadas se contabilicen. Devuelve true si lo logra. Se usa para detectar
// fugas tras un early-stop del grep, con holgura frente a las goroutines del
// runtime que fluctúan (no es un número exacto, sino "no creció sin acotar").
func eventuallyLeqGoroutines(limit, timeoutMs int) bool {
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for {
		runtime.GC()
		if runtime.NumGoroutine() <= limit {
			return true
		}
		if time.Now().After(deadline) {
			return runtime.NumGoroutine() <= limit
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertRelPaths comprueba que `got` (rutas absolutas devueltas por `files`)
// coincide, una vez relativizado a `root` y ordenado, con `want`.
func assertRelPaths(t *testing.T, root string, got, want []string) {
	t.Helper()
	rels := make([]string, 0, len(got))
	for _, p := range got {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatalf("Rel(%s,%s): %v", root, p, err)
		}
		rels = append(rels, filepath.ToSlash(rel))
	}
	sort.Strings(rels)
	sort.Strings(want)
	if len(rels) != len(want) {
		t.Fatalf("rutas: got %v, want %v", rels, want)
	}
	for i := range want {
		if rels[i] != want[i] {
			t.Fatalf("ruta %d: got %q, want %q (todas: got %v want %v)", i, rels[i], want[i], rels, want)
		}
	}
}
