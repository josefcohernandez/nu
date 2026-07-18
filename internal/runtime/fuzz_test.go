package runtime

// Dianas de fuzzing para la pasada de salud del repo (skill /salud, capa
// mecánica). Cubren la lógica 🔒 cuyo riesgo es exactamente el que el fuzzing
// caza: parsers incrementales y algoritmos con bordes (S20 SSE, S22 width/wrap,
// S23 markdown streaming-safe, S25 diff, S31/CP-7 parser de bytes del TTY).
//
// Cada diana asevera un INVARIANTE, no solo "no hace panic":
//   - SSE y decodeInput: partir la entrada en trozos arbitrarios produce
//     exactamente los mismos eventos que entregarla entera (la exigencia
//     incremental de S20 y la disciplina pending/flush del driver).
//   - computeDiff: los hunks reconstruyen `b` a partir de `a` (aplicar el
//     patch), y cada op es consistente con las líneas reales de ambos lados.
//   - wrapText: ninguna línea excede el ancho salvo que sea un único grapheme
//     más ancho que el ancho pedido; el contenido no-blanco se conserva.
//   - renderMarkdownBlocks: cualquier prefijo de la entrada (streaming) se
//     renderiza sin romper.
//
// El corpus generado se acumula en el cache de Go entre pasadas ($GOCACHE/fuzz):
// cada ejecución de /salud parte de donde llegó la anterior. Con `go test` a
// secas solo corre el corpus semilla (barato: CI no paga el fuzzing).

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

// --- S20: parser SSE incremental -------------------------------------------

// sseCollect alimenta el parser con los trozos dados, drenando eventos tras
// cada feed (como hace Stream:events()), y cierra con flush (EOF).
func sseCollect(chunks [][]byte) []sseEvent {
	var p sseParser
	var out []sseEvent
	for _, c := range chunks {
		p.feed(c)
		for {
			ev, ok := p.next()
			if !ok {
				break
			}
			out = append(out, ev)
		}
	}
	if ev, ok := p.flush(); ok {
		out = append(out, ev)
	}
	return out
}

// FuzzSSEChunkSplit blinda la exigencia clave de S20: los límites de los trozos
// TCP no significan nada. Partir el stream en 3 trozos por puntos arbitrarios
// (incluso a mitad de un "\r\n" o de un "data:") produce los mismos eventos que
// el stream entero.
func FuzzSSEChunkSplit(f *testing.F) {
	f.Add([]byte("data: hola\n\n"), uint(3), uint(7))
	f.Add([]byte("event: x\r\ndata: a\r\ndata: b\r\n\r\nid: 9\ndata:\n\n"), uint(20), uint(30))
	f.Add([]byte(": comentario\ndata\n\ndata: sin fin"), uint(1), uint(2))
	f.Fuzz(func(t *testing.T, data []byte, c1, c2 uint) {
		whole := sseCollect([][]byte{data})
		i := int(c1 % uint(len(data)+1))
		j := int(c2 % uint(len(data)+1))
		if i > j {
			i, j = j, i
		}
		split := sseCollect([][]byte{data[:i], data[i:j], data[j:]})
		if !reflect.DeepEqual(whole, split) {
			t.Fatalf("S20: el corte en trozos [0:%d:%d] cambia los eventos:\nentero: %#v\npartido: %#v", i, j, whole, split)
		}
	})
}

// --- S31/CP-7: parser de bytes del terminal ---------------------------------

// ttyCollect procesa los trozos con la misma disciplina pending/flush que
// ttyDriver.feed: acumula lo no consumido, flush=false entre trozos (podrían
// llegar más bytes) y flush=true al final (timeout/EOF).
func ttyCollect(t *testing.T, chunks [][]byte) []inputEvent {
	t.Helper()
	var pending []byte
	var out []inputEvent
	step := func(flush bool) {
		evs, consumed := decodeInput(pending, flush)
		if consumed < 0 || consumed > len(pending) {
			t.Fatalf("decodeInput consumió fuera de rango: %d de %d", consumed, len(pending))
		}
		pending = pending[consumed:]
		out = append(out, evs...)
	}
	for _, c := range chunks {
		pending = append(pending, c...)
		step(false)
	}
	step(true)
	return out
}

// FuzzDecodeInputChunkSplit blinda el lazo bytes→eventos del driver: una
// secuencia de escape partida entre dos reads (ESC en un trozo, "[A" en el
// siguiente) produce los mismos eventos que si llegara entera.
func FuzzDecodeInputChunkSplit(f *testing.F) {
	f.Add([]byte("\x1b[A"), uint(1))
	f.Add([]byte("a\x1b[1;5Cb\x1b"), uint(4))
	f.Add([]byte("\x1b[200~pega\x1b[201~\r"), uint(9))
	f.Add([]byte("\x1bx\x1b[M !!"), uint(2))
	f.Fuzz(func(t *testing.T, data []byte, cut uint) {
		whole := ttyCollect(t, [][]byte{data})
		i := int(cut % uint(len(data)+1))
		split := ttyCollect(t, [][]byte{data[:i], data[i:]})
		if !reflect.DeepEqual(whole, split) {
			t.Fatalf("CP-7: el corte en [0:%d] cambia los eventos:\nentero: %#v\npartido: %#v", i, whole, split)
		}
	})
}

// --- S25: diff --------------------------------------------------------------

// FuzzComputeDiffReconstruct blinda la corrección de los hunks aplicándolos:
// recorrer `a` copiando lo que queda fuera de los hunks y, dentro de cada uno,
// verificar cada op contra las líneas reales de `a`/`b` y emitir context+add,
// debe reconstruir `b` exactamente. a == b debe dar cero hunks.
func FuzzComputeDiffReconstruct(f *testing.F) {
	f.Add("a\nb\nc", "a\nX\nc")
	f.Add("", "solo\nadds")
	f.Add("borra\ntodo", "")
	f.Add("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10", "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11")
	f.Add("a\nb", "a\nb\n")
	f.Fuzz(func(t *testing.T, a, b string) {
		hunks := computeDiff(a, b)
		aL, bL := splitDiffLines(a), splitDiffLines(b)
		if reflect.DeepEqual(aL, bL) && len(hunks) != 0 {
			t.Fatalf("S25: a == b (por líneas) pero hay %d hunks", len(hunks))
		}
		var out []string
		aPos := 0 // índice 0-based de la siguiente línea de `a` no consumida
		for _, h := range hunks {
			if h.oldStart > 0 {
				if h.oldStart-1 < aPos || h.oldStart-1 > len(aL) {
					t.Fatalf("S25: hunk desordenado o solapado: oldStart=%d con aPos=%d", h.oldStart, aPos)
				}
				out = append(out, aL[aPos:h.oldStart-1]...)
				aPos = h.oldStart - 1
			} else if len(aL) > 0 {
				t.Fatalf("S25: hunk con oldStart=0 pero `a` tiene %d líneas", len(aL))
			}
			for _, op := range h.lines {
				switch op.kind {
				case "context":
					if aL[op.oldLine-1] != op.text || bL[op.newLine-1] != op.text {
						t.Fatalf("S25: op context inconsistente en old=%d/new=%d", op.oldLine, op.newLine)
					}
					out = append(out, op.text)
					aPos++
				case "del":
					if aL[op.oldLine-1] != op.text {
						t.Fatalf("S25: op del no coincide con a[%d]", op.oldLine)
					}
					aPos++
				case "add":
					if bL[op.newLine-1] != op.text {
						t.Fatalf("S25: op add no coincide con b[%d]", op.newLine)
					}
					out = append(out, op.text)
				default:
					t.Fatalf("S25: kind desconocido %q", op.kind)
				}
			}
		}
		out = append(out, aL[aPos:]...)
		if len(out) != len(bL) || strings.Join(out, "\n") != strings.Join(bL, "\n") {
			t.Fatalf("S25: aplicar los hunks no reconstruye b:\nreconstruido: %q\nb: %q", out, bL)
		}
	})
}

// --- S22: word-wrap y anchura -----------------------------------------------

// stripSpace elimina todo el espacio en blanco Unicode (la moneda que el
// word-wrap tiene permitido gastar: colapsa y reparte espacios, nunca letras).
func stripSpace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// FuzzWrapText blinda los dos invariantes del wrap de S22: (1) ninguna línea
// excede el ancho pedido salvo que sea UN único grapheme cluster más ancho que
// el propio ancho (un emoji de 2 celdas con width=1 no se puede partir); (2) el
// contenido no-blanco sobrevive íntegro y en orden.
func FuzzWrapText(f *testing.F) {
	f.Add("hola mundo cruel", uint(5))
	f.Add("palabra_larguísima_sin_cortes y más", uint(8))
	f.Add("emoji 👩‍👩‍👧‍👦 zwj y ancho 漢字 asiático", uint(4))
	f.Add("línea\n\notra tras párrafo", uint(10))
	f.Fuzz(func(t *testing.T, s string, w uint) {
		width := int(w%200) + 1
		lines := wrapText(s, width)
		for _, ln := range lines {
			if uniseg.StringWidth(ln) > width && uniseg.GraphemeClusterCount(ln) > 1 {
				t.Fatalf("S22: línea de %d celdas excede width=%d sin ser un cluster único: %q",
					uniseg.StringWidth(ln), width, ln)
			}
		}
		if got, want := stripSpace(strings.Join(lines, " ")), stripSpace(s); got != want {
			t.Fatalf("S22: el wrap alteró el contenido no-blanco:\ngot:  %q\nwant: %q", got, want)
		}
	})
}

// FuzzTruncateText blinda el contrato de recorte: el resultado nunca excede el
// ancho pedido (truncate con elipsis incluida).
func FuzzTruncateText(f *testing.F) {
	f.Add("hola mundo", uint(4), true)
	f.Add("漢字漢字", uint(3), false)
	f.Add("👩‍👩‍👧‍👦x", uint(1), true)
	f.Fuzz(func(t *testing.T, s string, w uint, conElipsis bool) {
		width := int(w%200) + 1
		ellipsis := ""
		if conElipsis {
			ellipsis = "…"
		}
		got := truncateText(s, width, ellipsis)
		if uniseg.StringWidth(got) > width {
			t.Fatalf("S22: truncate a width=%d devolvió %d celdas: %q", width, uniseg.StringWidth(got), got)
		}
	})
}

// --- S23: markdown streaming-safe -------------------------------------------

// FuzzMarkdownStreamingSafe blinda la promesa de S23: la entrada incompleta (un
// prefijo arbitrario del documento, incluso cortado a mitad de un bloque de
// código o de una secuencia UTF-8) se renderiza sin romper. El invariante es
// "no rompe" — el render de un prefijo no tiene por qué parecerse al del total.
func FuzzMarkdownStreamingSafe(f *testing.F) {
	f.Add("# título\n\n```lua\nlocal x = 1\n", uint(12), uint(60))
	f.Add("- lista\n- con `code` y **negrita\n\n> cita", uint(30), uint(20))
	f.Add("| tabla | rota\n|---|\nπ≈3.14159", uint(7), uint(15))
	f.Fuzz(func(t *testing.T, s string, cut, w uint) {
		width := int(w%120) + 1
		theme := defaultTheme()
		_ = renderMarkdownBlocks(s, width, &theme)
		prefix := s[:int(cut%uint(len(s)+1))]
		_ = renderMarkdownBlocks(prefix, width, &theme)
	})
}
