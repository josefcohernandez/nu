package runtime

import (
	"strconv"
	"strings"
)

// `nu.text.diff` — diff estructurado de dos strings línea a línea (api.md §10,
// sesión S25, inventario 🔒). Es CPU puro: compara dos strings ya en memoria y
// emite los **hunks** (regiones de cambio) y, opcionalmente, un `Block` pintado;
// no espera IO. Por eso es **[W] pero NINGUNA ⏸** (como `width`/`wrap`/
// `truncate`/`markdown`/`highlight` de S22–S24 y los codecs de S18): no usa el
// puente `suspend` ni `requireTask`, corre síncrona en el estado principal (y en
// workers cuando lleguen, S34).
//
// LUA DECIDE, GO EJECUTA (ADR-004). El cómputo del diff (la subsecuencia común
// más larga, el agrupado en hunks) lo hace Go, en puro-Go sin dependencias: el
// algoritmo line-based clásico (LCS por programación dinámica → backtrack →
// agrupado con contexto) es pequeño, correcto y testeable en los bordes, así que
// NO se añade ninguna librería externa (decisión en claude_decisions.md S25). El
// render reusa los helpers de Block de S22 (`newBlock`/spans/`style`) y la
// resolución de colores literales (`parseStyle`/`normalizeColor`, ui.go, G22).
// Ni una función pública de más: solo se cuelga `nu.text.diff`.
//
// ───────────────────────────────────────────────────────────────────────────
// LA FORMA DE LOS HUNKS (lo que el visor de diffs / toolkit consume).
// ───────────────────────────────────────────────────────────────────────────
//
// `nu.text.diff(a, b, opts?) -> {hunks, block?}` compara `a` (texto viejo) y `b`
// (texto nuevo) **línea a línea** y devuelve una tabla con:
//
//   - `hunks`: un array de hunks. Cada hunk describe una región contigua de
//     cambio con un poco de contexto alrededor (diffContextLines = 3 líneas), y
//     tiene la forma:
//
//         {
//           old_start, old_count,  -- rango 1-based en `a` (líneas viejas)
//           new_start, new_count,  -- rango 1-based en `b` (líneas nuevas)
//           lines = {              -- líneas clasificadas del hunk, en orden
//             { kind = "context"|"del"|"add", text = "..." },
//             ...
//           },
//         }
//
//     Los números de línea son **1-based** (convención Lua). `old_start`/
//     `new_start` apuntan a la primera línea (de contexto o de cambio) del hunk
//     en cada lado; `old_count`/`new_count` son cuántas líneas de cada lado
//     abarca el hunk (contexto + borrados para old, contexto + añadidos para
//     new). Un hunk de **inserción pura** no tiene líneas `del`; uno de **borrado
//     puro**, ninguna `add`. Cuando un lado del hunk no tiene líneas propias
//     (p. ej. añadir al final de un fichero vacío, sin contexto), su `*_start`
//     apunta a la posición de inserción (la línea de ese lado tras la cual va el
//     cambio, 1-based; 0 si va al principio) y su `*_count` es 0 — la convención
//     del diff unificado.
//
//   - `block` (solo si `opts.render == true`): un `Block` pintado, una línea por
//     línea mostrada de todos los hunks (con una cabecera "@@ ... @@" por hunk),
//     `+ ` en verde para los añadidos, `- ` en rojo para los borrados y `  `
//     neutro para el contexto. Los colores son literales del theme por defecto o
//     de `opts.theme` (G22).
//
// Si `a == b` no hay cambios: `hunks` es una tabla vacía y, con render, el Block
// es vacío (una sola línea en blanco, height 1 — un Block siempre tiene ≥1
// línea, como en markdown/highlight). Documentado así para que el consumidor
// distinga "sin cambios" por `#hunks == 0`.
//
// ───────────────────────────────────────────────────────────────────────────
// EL ALGORITMO Y LOS BORDES (la lógica 🔒).
// ───────────────────────────────────────────────────────────────────────────
//
// 1. Se parten `a` y `b` en líneas (splitDiffLines: por '\n', sin línea fantasma
//    final — "a\n" y "a" dan ambos ["a"], pero "a\nb" da ["a","b"]; un string
//    vacío da cero líneas). Así "sin newline final" no introduce diferencias
//    espurias frente al mismo texto con newline final.
// 2. Se calcula la **subsecuencia común más larga** (LCS) de las dos listas de
//    líneas por programación dinámica, y se hace backtrack para producir la
//    secuencia de operaciones (context/del/add) que transforma `a` en `b`.
// 3. Se **agrupan** las operaciones en hunks: cada bloque de cambios se rodea de
//    a lo sumo diffContextLines líneas de contexto a cada lado; dos bloques de
//    cambio separados por ≤ 2*diffContextLines líneas de contexto se funden en un
//    solo hunk (su contexto se solapa), como hace el diff unificado.
//
// Los casos de borde que el test 🔒 blinda (todos salen de un LCS correcto +
// agrupado cuidadoso): inserción pura, borrado puro, cambio (del+add), cambio en
// la PRIMERA línea, cambio en la ÚLTIMA línea, `a` vacío → `b` (todo add), `a` →
// `b` vacío (todo del), `a == b` (sin hunks), una sola línea, sin newline final.

// diffContextLines es el número de líneas de contexto que rodean cada región de
// cambio en un hunk (las 3 del diff unificado por convención). Es fijo en S25
// (no se expone por `opts`: la firma §10 no lo contempla y 3 es el estándar de
// facto; reabrible si un consumidor lo pide).
const diffContextLines = 3

// diffOp es una operación elemental del diff: una línea que es contexto (igual en
// ambos), un borrado (solo en `a`) o un añadido (solo en `b`). `oldLine`/
// `newLine` son los índices 1-based en cada lado (0 cuando la línea no existe en
// ese lado: un add no tiene oldLine, un del no tiene newLine).
type diffOp struct {
	kind    string // "context" | "del" | "add"
	text    string
	oldLine int // 1-based en `a`, 0 si no aplica
	newLine int // 1-based en `b`, 0 si no aplica
}

// diffHunk es una región contigua de cambio con su contexto, en la forma que se
// expone a Lua (ver cabecera). `lines` son las operaciones del hunk en orden.
type diffHunk struct {
	oldStart, oldCount int
	newStart, newCount int
	lines              []diffOp
}

// splitDiffLines parte `s` en líneas por '\n' SIN una línea fantasma final: "a"
// y "a\n" dan ambos ["a"], "" da nil (cero líneas), "a\nb" da ["a","b"] y "a\n\n"
// da ["a",""]. Tratar el newline final como terminador (no separador) hace que
// "sin newline final" no genere diferencias espurias frente al mismo texto con
// newline (caso de borde 🔒). El '\r' de un CRLF se conserva en la línea (el diff
// es por contenido exacto de línea; normalizar finales de línea es del consumidor).
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	// Recorta UN '\n' final (terminador de la última línea), si lo hay, para no
	// inventar una línea vacía tras él.
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// lcsTable calcula la tabla de longitudes de la subsecuencia común más larga de
// `a` y `b` (líneas). dp[i][j] = longitud de la LCS de a[i:] y b[j:]. Se rellena
// de atrás hacia delante para que el backtrack en `diffOps` avance hacia delante
// (produciendo las operaciones en orden de fichero). O(len(a)*len(b)) en tiempo y
// espacio: suficiente para diffs de tamaño humano (un visor de diffs no compara
// ficheros de millones de líneas; si hiciera falta, Myers en O(ND) sería el
// siguiente paso, reabrible).
func lcsTable(a, b []string) [][]int {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	return dp
}

// diffOps produce la secuencia completa de operaciones (context/del/add) que
// transforma `a` en `b`, recorriendo la tabla LCS de delante hacia atrás. El
// criterio de desempate (ante un cambio, emitir el borrado antes que el añadido)
// es el del diff unificado: una línea modificada aparece como su `del` seguido de
// su `add`. Las líneas comunes salen como `context`.
func diffOps(a, b []string) []diffOp {
	dp := lcsTable(a, b)
	var ops []diffOp
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			ops = append(ops, diffOp{kind: "context", text: a[i], oldLine: i + 1, newLine: j + 1})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			// Avanzar en `a` conserva (o iguala) la LCS: la línea a[i] se borró.
			ops = append(ops, diffOp{kind: "del", text: a[i], oldLine: i + 1})
			i++
		} else {
			// Avanzar en `b`: la línea b[j] se añadió.
			ops = append(ops, diffOp{kind: "add", text: b[j], newLine: j + 1})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{kind: "del", text: a[i], oldLine: i + 1})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{kind: "add", text: b[j], newLine: j + 1})
	}
	return ops
}

// groupHunks agrupa la secuencia plana de operaciones en hunks: cada región de
// cambio (uno o más del/add contiguos, posiblemente intercalados con poco
// contexto) se rodea de a lo sumo `diffContextLines` líneas de contexto a cada
// lado; el contexto sobrante entre dos cambios lejanos se descarta (no genera
// hunk). Dos cambios separados por ≤ 2*diffContextLines líneas de contexto caen
// en el MISMO hunk (su contexto se solapa), como en el diff unificado. Sin
// cambios → ningún hunk (tabla vacía).
func groupHunks(ops []diffOp) []diffHunk {
	// Índices de las operaciones que son cambios (del/add).
	var changeIdx []int
	for i, op := range ops {
		if op.kind != "context" {
			changeIdx = append(changeIdx, i)
		}
	}
	if len(changeIdx) == 0 {
		return nil
	}

	var hunks []diffHunk
	// Recorre los cambios fundiendo los que comparten contexto. `start`/`end` son
	// el rango [start, end] de operaciones del hunk en curso (inclusive), ya
	// extendido con su contexto.
	k := 0
	for k < len(changeIdx) {
		first := changeIdx[k]
		start := first - diffContextLines
		if start < 0 {
			start = 0
		}
		end := changeIdx[k] + diffContextLines
		// Funde con el siguiente cambio mientras su contexto previo solape con el
		// contexto posterior del actual (hueco de contexto ≤ 2*diffContextLines).
		k++
		for k < len(changeIdx) {
			next := changeIdx[k]
			// Funde cuando el contexto previo del siguiente cambio (next −
			// diffContextLines) toca o queda inmediatamente tras el contexto
			// posterior del actual (end). El "+1" cubre el caso frontera en que los
			// dos bloques de contexto son adyacentes sin solaparse: un hueco de
			// exactamente 2*diffContextLines líneas de contexto sigue en un solo
			// hunk, igual que `git diff -U3` y `GNU diff -U3` (funden hasta hueco=6,
			// separan a partir de 7 con diffContextLines=3).
			if next-diffContextLines <= end+1 {
				end = next + diffContextLines
				k++
			} else {
				break
			}
		}
		if end > len(ops)-1 {
			end = len(ops) - 1
		}
		hunks = append(hunks, buildHunk(ops[start:end+1]))
	}
	return hunks
}

// buildHunk construye un hunk a partir de su rebanada de operaciones (ya con el
// contexto recortado por groupHunks). Calcula los rangos 1-based de cada lado a
// partir de las líneas que el hunk toca: para `old`, las de contexto y borrado;
// para `new`, las de contexto y añadido. Si un lado no tiene ninguna línea propia
// (p. ej. una inserción al principio de un fichero vacío), su `start` apunta a la
// posición de inserción (la última línea de ese lado antes del hunk, 0 si va al
// principio) y su `count` es 0 — la convención del diff unificado.
func buildHunk(ops []diffOp) diffHunk {
	h := diffHunk{lines: ops}
	oldFirst, oldLast := 0, 0
	newFirst, newLast := 0, 0
	for _, op := range ops {
		if op.oldLine != 0 {
			if oldFirst == 0 {
				oldFirst = op.oldLine
			}
			oldLast = op.oldLine
		}
		if op.newLine != 0 {
			if newFirst == 0 {
				newFirst = op.newLine
			}
			newLast = op.newLine
		}
	}
	if oldFirst != 0 {
		h.oldStart = oldFirst
		h.oldCount = oldLast - oldFirst + 1
	} else {
		// El hunk no toca ninguna línea de `a` (inserción pura sin contexto): el
		// start apunta a la línea de `a` tras la que se inserta (0 si va al
		// principio del fichero — no hay línea previa en `a`).
		h.oldStart = 0
		h.oldCount = 0
	}
	if newFirst != 0 {
		h.newStart = newFirst
		h.newCount = newLast - newFirst + 1
	} else {
		h.newStart = 0
		h.newCount = 0
	}
	return h
}

// computeDiff es el núcleo puro (sin Lua): de dos strings produce sus hunks. Lo
// usan tanto el wrapper Lua como el render y los tests 🔒.
func computeDiff(a, b string) []diffHunk {
	return groupHunks(diffOps(splitDiffLines(a), splitDiffLines(b)))
}

// ───────────────────────────────────────────────────────────────────────────
// Registro y wrapper Lua.
// ───────────────────────────────────────────────────────────────────────────

// diffTheme agrupa los estilos (literales) de las líneas del render: añadidos en
// verde, borrados en rojo, contexto y cabecera neutros. Punteros a `style` (nil =
// sin estilo). Se construye una vez por llamada desde `opts.theme` (con
// defaultDiffTheme rellenando lo ausente).
type diffTheme struct {
	add     *style // línea "+ ..."
	del     *style // línea "- ..."
	context *style // línea "  ..."
	header  *style // la cabecera "@@ ... @@"
}

// defaultDiffTheme construye el theme por defecto del render: verde para los
// añadidos, rojo para los borrados (colores ANSI literales por índice, coherentes
// con G22 — el Block guarda literales y el compositor S29 los degrada), contexto
// sin color y cabecera en negrita. Los índices 2 (verde) y 1 (rojo) son los ANSI
// estándar, presentes en cualquier terminal con color.
func defaultDiffTheme() diffTheme {
	return diffTheme{
		add:     &style{fg: "2", fgSet: true},
		del:     &style{fg: "1", fgSet: true},
		context: nil,
		header:  &style{bold: true},
	}
}

// renderDiffBlock pinta los hunks a un Block: una cabecera "@@ -o,oc +n,nc @@"
// por hunk (estilo header) y, debajo, una línea por operación con su prefijo
// ("+ "/"- "/"  ") y su estilo (add verde, del rojo, context neutro). El texto de
// cada línea es el de la operación tras el prefijo, en UN span (el highlight por
// contenido es S24, no se mezcla aquí). Sin hunks → un Block vacío (una línea en
// blanco, height 1: un Block siempre tiene ≥1 línea).
func renderDiffBlock(hunks []diffHunk, theme *diffTheme) *block {
	var lines [][]span
	for _, h := range hunks {
		header := "@@ -" + rangeStr(h.oldStart, h.oldCount) + " +" + rangeStr(h.newStart, h.newCount) + " @@"
		lines = append(lines, []span{{text: header, st: theme.header}})
		for _, op := range h.lines {
			var prefix string
			var st *style
			switch op.kind {
			case "add":
				prefix, st = "+ ", theme.add
			case "del":
				prefix, st = "- ", theme.del
			default:
				prefix, st = "  ", theme.context
			}
			lines = append(lines, []span{{text: prefix + op.text, st: st}})
		}
	}
	if len(lines) == 0 {
		lines = [][]span{{}}
	}
	return newBlock(lines)
}

// rangeStr formatea un rango de diff unificado "start,count" (o solo "start"
// cuando count es 1, como en el formato unificado clásico).
func rangeStr(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(count)
}
