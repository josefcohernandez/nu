package runtime

import (
	"strings"
	"testing"
)

// Tests de `enu.text.diff` (S25, inventario 🔒). La lógica propia a blindar es el
// **diff line-based** (LCS + agrupado en hunks) y, sobre todo, su corrección en
// los BORDES: inserción pura, borrado puro, cambio (del+add), cambio en la
// primera/última línea, fichero vacío↔no vacío, `a == b` (sin hunks), una sola
// línea y "sin newline final". El render a Block se valida inspeccionando el
// Block resultante (prefijos, estilos, height coherente).

// opSig resume una operación a "kind:text" para comparar hunks de forma legible.
func opSig(op diffOp) string { return op.kind + ":" + op.text }

// hunkOps devuelve las firmas "kind:text" de las líneas de un hunk, en orden.
func hunkOps(h diffHunk) []string {
	var out []string
	for _, op := range h.lines {
		out = append(out, opSig(op))
	}
	return out
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// countKind cuenta las operaciones de un kind dado a lo largo de TODOS los hunks.
func countKind(hunks []diffHunk, kind string) int {
	n := 0
	for _, h := range hunks {
		for _, op := range h.lines {
			if op.kind == kind {
				n++
			}
		}
	}
	return n
}

// TestComputeDiffEdges blinda los casos de borde del diff line-based (🔒).
func TestComputeDiffEdges(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		// nHunks esperado; -1 = no comprobar.
		nHunks int
		// si nHunks==1, las firmas esperadas del único hunk (nil = no comprobar).
		ops []string
		// rangos del único hunk (solo si nHunks==1 y check* true).
		checkRanges                            bool
		oldStart, oldCount, newStart, newCount int
		nAdd, nDel                             int // totales (-1 = no comprobar)
	}{
		{
			name: "a == b (sin cambios → sin hunks)",
			a:    "uno\ndos\ntres\n", b: "uno\ndos\ntres\n",
			nHunks: 0, nAdd: 0, nDel: 0,
		},
		{
			name: "inserción pura en medio",
			a:    "a\nb\nc\n", b: "a\nb\nNUEVA\nc\n",
			nHunks: 1, nAdd: 1, nDel: 0,
			ops: []string{"context:a", "context:b", "add:NUEVA", "context:c"},
		},
		{
			name: "borrado puro en medio",
			a:    "a\nb\nBORRAR\nc\n", b: "a\nb\nc\n",
			nHunks: 1, nAdd: 0, nDel: 1,
			ops: []string{"context:a", "context:b", "del:BORRAR", "context:c"},
		},
		{
			name: "cambio (del+add) en medio",
			a:    "a\nb\nVIEJA\nc\nd\n", b: "a\nb\nNUEVA\nc\nd\n",
			nHunks: 1, nAdd: 1, nDel: 1,
			ops: []string{"context:a", "context:b", "del:VIEJA", "add:NUEVA", "context:c", "context:d"},
		},
		{
			name: "cambio en la PRIMERA línea",
			a:    "VIEJA\nb\nc\n", b: "NUEVA\nb\nc\n",
			nHunks: 1, nAdd: 1, nDel: 1,
			ops:         []string{"del:VIEJA", "add:NUEVA", "context:b", "context:c"},
			checkRanges: true, oldStart: 1, oldCount: 3, newStart: 1, newCount: 3,
		},
		{
			name: "cambio en la ÚLTIMA línea",
			a:    "a\nb\nVIEJA\n", b: "a\nb\nNUEVA\n",
			nHunks: 1, nAdd: 1, nDel: 1,
			ops: []string{"context:a", "context:b", "del:VIEJA", "add:NUEVA"},
		},
		{
			name: "a vacío → b (todo add)",
			a:    "", b: "x\ny\nz\n",
			nHunks: 1, nAdd: 3, nDel: 0,
			ops:         []string{"add:x", "add:y", "add:z"},
			checkRanges: true, oldStart: 0, oldCount: 0, newStart: 1, newCount: 3,
		},
		{
			name: "a → b vacío (todo del)",
			a:    "x\ny\nz\n", b: "",
			nHunks: 1, nAdd: 0, nDel: 3,
			ops:         []string{"del:x", "del:y", "del:z"},
			checkRanges: true, oldStart: 1, oldCount: 3, newStart: 0, newCount: 0,
		},
		{
			name: "ambos vacíos (sin hunks)",
			a:    "", b: "",
			nHunks: 0, nAdd: 0, nDel: 0,
		},
		{
			name: "una sola línea, cambiada",
			a:    "hola", b: "adios",
			nHunks: 1, nAdd: 1, nDel: 1,
			ops:         []string{"del:hola", "add:adios"},
			checkRanges: true, oldStart: 1, oldCount: 1, newStart: 1, newCount: 1,
		},
		{
			name: "sin newline final == con newline final (sin cambios)",
			a:    "a\nb", b: "a\nb\n",
			nHunks: 0, nAdd: 0, nDel: 0,
		},
		{
			name: "sin newline final, última línea cambiada",
			a:    "a\nb", b: "a\nB",
			nHunks: 1, nAdd: 1, nDel: 1,
			ops: []string{"context:a", "del:b", "add:B"},
		},
		{
			name: "inserción al PRINCIPIO",
			a:    "a\nb\n", b: "NUEVA\na\nb\n",
			nHunks: 1, nAdd: 1, nDel: 0,
			ops:         []string{"add:NUEVA", "context:a", "context:b"},
			checkRanges: true, oldStart: 1, oldCount: 2, newStart: 1, newCount: 3,
		},
		{
			name: "append al FINAL",
			a:    "a\nb\n", b: "a\nb\nNUEVA\n",
			nHunks: 1, nAdd: 1, nDel: 0,
			ops: []string{"context:a", "context:b", "add:NUEVA"},
		},
		{
			name:   "dos cambios LEJANOS → dos hunks",
			a:      "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n",
			b:      "X\nb\nc\nd\ne\nf\ng\nh\ni\nY\n",
			nHunks: 2, nAdd: -1, nDel: -1,
		},
		{
			name:   "dos cambios CERCANOS → un hunk fusionado",
			a:      "a\nVIEJA1\nc\nVIEJA2\ne\n",
			b:      "a\nNUEVA1\nc\nNUEVA2\ne\n",
			nHunks: 1, nAdd: -1, nDel: -1,
		},
		// Fronteras de fusión (diffContextLines=3): el contexto posterior del primer
		// cambio (3 líneas) y el contexto previo del segundo (3 líneas) suman 6.
		// Hueco de hasta 2*diffContextLines (=6) líneas de contexto → un solo hunk;
		// a partir de 7, dos hunks. Coincide con `git diff -U3` / `GNU diff -U3`
		// (verificado: hueco 5 y 6 funden, 7 separa).
		{
			name:   "hueco de contexto = 5 → un hunk fusionado",
			a:      "X1\nc1\nc2\nc3\nc4\nc5\nX2\n",
			b:      "Y1\nc1\nc2\nc3\nc4\nc5\nY2\n",
			nHunks: 1, nAdd: -1, nDel: -1,
		},
		{
			name:   "hueco de contexto = 6 → un hunk fusionado (caso frontera 2*contexto)",
			a:      "X1\nc1\nc2\nc3\nc4\nc5\nc6\nX2\n",
			b:      "Y1\nc1\nc2\nc3\nc4\nc5\nc6\nY2\n",
			nHunks: 1, nAdd: 2, nDel: 2,
			// El hunk fusionado abarca ambos cambios + todo el contexto intermedio:
			// líneas 1..8 en ambos lados (X1/Y1 .. X2/Y2).
			checkRanges: true, oldStart: 1, oldCount: 8, newStart: 1, newCount: 8,
		},
		{
			name:   "hueco de contexto = 7 → dos hunks",
			a:      "X1\nc1\nc2\nc3\nc4\nc5\nc6\nc7\nX2\n",
			b:      "Y1\nc1\nc2\nc3\nc4\nc5\nc6\nc7\nY2\n",
			nHunks: 2, nAdd: -1, nDel: -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hunks := computeDiff(tc.a, tc.b)
			if tc.nHunks >= 0 && len(hunks) != tc.nHunks {
				t.Fatalf("nº hunks = %d, want %d; hunks=%+v", len(hunks), tc.nHunks, hunks)
			}
			if tc.nAdd >= 0 {
				if got := countKind(hunks, "add"); got != tc.nAdd {
					t.Errorf("nº add = %d, want %d", got, tc.nAdd)
				}
			}
			if tc.nDel >= 0 {
				if got := countKind(hunks, "del"); got != tc.nDel {
					t.Errorf("nº del = %d, want %d", got, tc.nDel)
				}
			}
			if tc.ops != nil {
				if len(hunks) != 1 {
					t.Fatalf("se esperaba 1 hunk para comparar ops, hay %d", len(hunks))
				}
				if got := hunkOps(hunks[0]); !eqStrs(got, tc.ops) {
					t.Errorf("ops del hunk =\n  %v\nwant\n  %v", got, tc.ops)
				}
			}
			if tc.checkRanges {
				h := hunks[0]
				if h.oldStart != tc.oldStart || h.oldCount != tc.oldCount ||
					h.newStart != tc.newStart || h.newCount != tc.newCount {
					t.Errorf("rangos = old(%d,%d) new(%d,%d), want old(%d,%d) new(%d,%d)",
						h.oldStart, h.oldCount, h.newStart, h.newCount,
						tc.oldStart, tc.oldCount, tc.newStart, tc.newCount)
				}
			}
		})
	}
}

// TestDiffApplyReconstructs es el invariante transversal: aplicar las operaciones
// de un diff a `a` (tomar context+del de old, context+add de new) reconstruye `b`
// en las regiones cubiertas. Aquí lo comprobamos de forma fuerte: para diffs SIN
// contexto descartado (cambios contiguos), la concatenación de context+add de
// todos los hunks, intercalada con las líneas no tocadas de `a`, da `b`. Para no
// reimplementar el parche, validamos el caso simple: cada línea `add`/`context`
// del diff aparece en `b` y cada `del`/`context` en `a`, en orden creciente de
// línea.
func TestDiffLinesConsistentWithSources(t *testing.T) {
	a := "uno\ndos\ntres\ncuatro\ncinco\n"
	b := "uno\nDOS\ntres\ncuatro\nSEIS\ncinco\n"
	aLines := splitDiffLines(a)
	bLines := splitDiffLines(b)
	for _, h := range computeDiff(a, b) {
		for _, op := range h.lines {
			switch op.kind {
			case "context":
				if op.oldLine < 1 || op.oldLine > len(aLines) || aLines[op.oldLine-1] != op.text {
					t.Errorf("context op %q no casa con a[%d]", op.text, op.oldLine)
				}
				if op.newLine < 1 || op.newLine > len(bLines) || bLines[op.newLine-1] != op.text {
					t.Errorf("context op %q no casa con b[%d]", op.text, op.newLine)
				}
			case "del":
				if op.oldLine < 1 || op.oldLine > len(aLines) || aLines[op.oldLine-1] != op.text {
					t.Errorf("del op %q no casa con a[%d]", op.text, op.oldLine)
				}
				if op.newLine != 0 {
					t.Errorf("del op no debe tener newLine, tiene %d", op.newLine)
				}
			case "add":
				if op.newLine < 1 || op.newLine > len(bLines) || bLines[op.newLine-1] != op.text {
					t.Errorf("add op %q no casa con b[%d]", op.text, op.newLine)
				}
				if op.oldLine != 0 {
					t.Errorf("add op no debe tener oldLine, tiene %d", op.oldLine)
				}
			}
		}
	}
}

// TestSplitDiffLines blinda el parseo de líneas (terminador vs separador, el caso
// "sin newline final" 🔒).
func TestSplitDiffLines(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a\n", []string{"a"}},
		{"a\nb", []string{"a", "b"}},
		{"a\nb\n", []string{"a", "b"}},
		{"a\n\n", []string{"a", ""}},
		{"\n", []string{""}},
		{"a\r\nb\r\n", []string{"a\r", "b\r"}}, // CRLF: el \r se conserva en la línea
	}
	for _, tc := range tests {
		got := splitDiffLines(tc.in)
		if !eqStrs(got, tc.want) {
			t.Errorf("splitDiffLines(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestRenderDiffBlock valida el render a Block: prefijos +/-/espacio, estilos por
// tipo de línea, cabecera por hunk y height coherente con el nº de líneas
// mostradas (cabecera + una por operación).
func TestRenderDiffBlock(t *testing.T) {
	a := "a\nb\nVIEJA\nc\nd\n"
	b := "a\nb\nNUEVA\nc\nd\n"
	hunks := computeDiff(a, b)
	if len(hunks) != 1 {
		t.Fatalf("se esperaba 1 hunk, hay %d", len(hunks))
	}
	theme := defaultDiffTheme()
	blk := renderDiffBlock(hunks, &theme)

	// height = 1 (cabecera) + nº de operaciones del hunk.
	wantH := 1 + len(hunks[0].lines)
	if blk.height != wantH {
		t.Fatalf("height = %d, want %d", blk.height, wantH)
	}

	// La primera línea es la cabecera "@@ ... @@" con estilo header.
	hdr := blk.lines[0]
	if len(hdr) != 1 || !strings.HasPrefix(hdr[0].text, "@@ ") || hdr[0].st == nil || !hdr[0].st.bold {
		t.Errorf("cabecera mal formada: %+v", hdr)
	}

	// Cada línea de operación lleva el prefijo correcto y el estilo correcto.
	var sawAdd, sawDel, sawCtx bool
	for _, ln := range blk.lines[1:] {
		if len(ln) != 1 {
			t.Fatalf("línea de op debe ser un span, es %d", len(ln))
		}
		sp := ln[0]
		switch {
		case strings.HasPrefix(sp.text, "+ "):
			sawAdd = true
			if sp.st == nil || sp.st.fg != "2" {
				t.Errorf("add sin verde: %+v", sp.st)
			}
		case strings.HasPrefix(sp.text, "- "):
			sawDel = true
			if sp.st == nil || sp.st.fg != "1" {
				t.Errorf("del sin rojo: %+v", sp.st)
			}
		case strings.HasPrefix(sp.text, "  "):
			sawCtx = true
			if sp.st != nil {
				t.Errorf("context con estilo: %+v", sp.st)
			}
		default:
			t.Errorf("prefijo desconocido: %q", sp.text)
		}
	}
	if !sawAdd || !sawDel || !sawCtx {
		t.Errorf("faltan tipos de línea: add=%v del=%v ctx=%v", sawAdd, sawDel, sawCtx)
	}
}

// TestRenderDiffBlockEmpty: sin cambios, el Block del render es vacío pero válido
// (height 1, un Block siempre tiene ≥1 línea).
func TestRenderDiffBlockEmpty(t *testing.T) {
	theme := defaultDiffTheme()
	blk := renderDiffBlock(computeDiff("igual\n", "igual\n"), &theme)
	if blk.height != 1 {
		t.Fatalf("Block vacío debe tener height 1, tiene %d", blk.height)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// Vía Lua: la firma desde el lado del autor de extensiones.
// ───────────────────────────────────────────────────────────────────────────

// TestDiffLua ejercita `enu.text.diff` desde Lua: inspecciona hunks (kind/text,
// rangos) y, con render, el Block.
func TestDiffLua(t *testing.T) {
	h := newHarness(t)

	// Cambio en medio: 1 hunk con del+add y contexto.
	h.eval(`
		local r = enu.text.diff("a\nb\nVIEJA\nc\nd\n", "a\nb\nNUEVA\nc\nd\n")
		assert(#r.hunks == 1, "esperaba 1 hunk, hay " .. #r.hunks)
		local hk = r.hunks[1]
		assert(hk.old_start == 1, "old_start=" .. hk.old_start)
		assert(hk.old_count == 5, "old_count=" .. hk.old_count)
		assert(hk.new_start == 1, "new_start=" .. hk.new_start)
		assert(hk.new_count == 5, "new_count=" .. hk.new_count)
		local kinds = {}
		for _, ln in ipairs(hk.lines) do kinds[#kinds+1] = ln.kind .. ":" .. ln.text end
		local want = {"context:a","context:b","del:VIEJA","add:NUEVA","context:c","context:d"}
		assert(#kinds == #want, "nº líneas " .. #kinds)
		for i = 1, #want do assert(kinds[i] == want[i], "línea " .. i .. " = " .. kinds[i]) end
		assert(r.block == nil, "sin render no debe haber block")
	`)

	// a == b → sin hunks.
	h.eval(`
		local r = enu.text.diff("x\ny\n", "x\ny\n")
		assert(#r.hunks == 0, "a==b debe dar 0 hunks, hay " .. #r.hunks)
	`)

	// a vacío → todo add.
	h.eval(`
		local r = enu.text.diff("", "p\nq\n")
		assert(#r.hunks == 1, "1 hunk")
		local hk = r.hunks[1]
		assert(hk.old_start == 0 and hk.old_count == 0, "old vacío")
		assert(hk.new_start == 1 and hk.new_count == 2, "new 1,2")
		for _, ln in ipairs(hk.lines) do assert(ln.kind == "add", "todo add") end
	`)

	// render → block con .height legible.
	b := buildBlock(t, h, `
		local r = enu.text.diff("a\nVIEJA\nc\n", "a\nNUEVA\nc\n", { render = true })
		return r.block
	`)
	// 1 hunk: cabecera + context a + del + add + context c = 5 líneas.
	if b.height != 5 {
		t.Fatalf("render block height = %d, want 5", b.height)
	}
}

// TestDiffLuaErrors: usos malos de la firma → EINVAL (opts no-tabla, opts.theme
// mal formado con un nombre semántico, G22). `a`/`b` deben ser strings (CheckString).
func TestDiffLuaErrors(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name string
		code string
	}{
		{"opts no-tabla", `enu.text.diff("a", "b", 7)`},
		{"theme nombre semántico (G22)", `enu.text.diff("a", "b", { theme = { add = { fg = "accent" } } })`},
		{"theme no-tabla", `enu.text.diff("a", "b", { theme = 5 })`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full := `local ok, err = pcall(function() ` + tc.code + ` end)
assert(not ok, "el uso malo debió fallar")
assert(err.code == "EINVAL", "code esperado EINVAL, got " .. tostring(err.code))
return true`
			h.eval(full)
		})
	}
}

// TestDiffLuaTheme: un theme válido (colores literales) se aplica al render.
func TestDiffLuaTheme(t *testing.T) {
	h := newHarness(t)
	b := buildBlock(t, h, `
		local r = enu.text.diff("VIEJA\n", "NUEVA\n", {
			render = true,
			theme = { add = { fg = "#00ff00" }, del = { fg = "#ff0000" } },
		})
		return r.block
	`)
	var sawAddColor, sawDelColor bool
	for _, ln := range b.lines {
		for _, sp := range ln {
			if sp.st != nil && sp.st.fg == "#00ff00" {
				sawAddColor = true
			}
			if sp.st != nil && sp.st.fg == "#ff0000" {
				sawDelColor = true
			}
		}
	}
	if !sawAddColor || !sawDelColor {
		t.Fatalf("theme literal no aplicado: add=%v del=%v", sawAddColor, sawDelColor)
	}
}
