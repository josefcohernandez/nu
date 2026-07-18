package runtime

import (
	"github.com/rivo/uniseg"
)

// `enu.text` — render y procesado de texto (api.md §10, sesión S22, inventario
// 🔒). En S22 se implementan las tres primitivas fundacionales del layout:
// `width` (la lógica 🔒, base de TODO el cálculo de tamaños), `wrap` (word-wrap a
// un Block) y `truncate` (recorte con elipsis). `markdown`/`highlight`/`diff`/
// `re` son S23–S26.
//
// TODAS SON [W] PERO NINGUNA ⏸ (§10, §16). Son **CPU puro**: miden o reordenan un
// string ya en memoria, no esperan IO —como los codecs de S18—. Por eso NO usan
// el puente `suspend` ni `requireTask`: corren síncronas en el estado principal (y
// en workers cuando lleguen, S34). [W] marca "disponible en workers", no
// "suspende".
//
// LA ANCHURA EN CELDAS ES EL CIMIENTO (🔒). "Anchura" aquí no es número de bytes
// ni de runes ni de graphemes, sino **celdas de terminal** —y esa es la única
// medida con la que el compositor (S29) recorta sin romper nada—. Las tres
// trampas, todas delegadas a `github.com/rivo/uniseg` (puro-Go, tablas Unicode
// al día, CGO_ENABLED=0 intacto):
//
//   - **Graphemes (clusters):** "é" puede ser un rune precompuesto (U+00E9) o
//     "e" + combining acute (U+0065 U+0301): dos runes, **un** grapheme, **una**
//     celda. La unidad de anchura es el grapheme, no el rune.
//   - **East-asian wide:** los ideogramas CJK ("你"), el hangul ("한") y otros
//     ocupan **dos** celdas. uniseg consulta la propiedad East_Asian_Width.
//   - **Emoji y secuencias ZWJ:** un emoji simple ("😀") es **un** grapheme de
//     **dos** celdas; una familia "👨‍👩‍👧‍👦" son cuatro emojis unidos por
//     ZWJ (U+200D) que el terminal pinta como **un** glifo —uniseg lo trata como
//     **un** grapheme de anchura **2** (el ZWJ interno aporta 0)—.

// wrapText es el algoritmo de word-wrap puro (sin Lua ni estilos): parte `s` en
// líneas de a lo sumo `width` celdas. Respeta los `\n` explícitos como límites
// duros y envuelve por palabras dentro de cada párrafo; una palabra más ancha que
// `width` se parte por grapheme. Devuelve al menos una línea (string vacío → [""],
// height 1). Es la pieza con casos límite (de ahí su test 🔒).
func wrapText(s string, width int) []string {
	var out []string
	// Los `\n` explícitos son límites de línea duros: cada segmento se envuelve por
	// separado y un "\n\n" produce una línea en blanco entre medias.
	for _, paragraph := range splitLines(s) {
		out = append(out, wrapParagraph(paragraph, width)...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// splitLines parte `s` por '\n' conservando los segmentos vacíos (un "a\n\nb" da
// ["a","","b"]). No usa strings.Split para dejar explícito que las líneas en
// blanco se preservan (importan para .height).
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}

// wrapParagraph envuelve un párrafo (sin '\n') a `width` celdas por palabras. Las
// palabras se separan por espacios ASCII; el espacio entre palabras de una misma
// línea cuenta una celda. Una palabra que no cabe entera se parte por grapheme
// (`splitWide`). Un párrafo vacío → [""] (una línea en blanco).
func wrapParagraph(p string, width int) []string {
	words := splitWords(p)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	var cur string
	curW := 0
	for _, word := range words {
		ww := uniseg.StringWidth(word)
		// La línea en curso está vacía solo si `cur` lo está: una palabra de
		// anchura 0 (un zero-width space suelto, un control char) deja curW en 0
		// con la línea YA ocupada, y usar curW como centinela la sobreescribía
		// (contenido borrado en silencio; lo cazó FuzzWrapText).
		if cur == "" {
			// Primera palabra de la línea. Si no cabe, se parte; el último trozo queda
			// como línea en curso para que la siguiente palabra pueda acompañarlo.
			if ww > width {
				parts := splitWide(word, width)
				lines = append(lines, parts[:len(parts)-1]...)
				cur = parts[len(parts)-1]
				curW = uniseg.StringWidth(cur)
			} else {
				cur, curW = word, ww
			}
			continue
		}
		// ¿Cabe la palabra (más el espacio que la precede) en la línea en curso?
		if curW+1+ww <= width {
			cur += " " + word
			curW += 1 + ww
			continue
		}
		// No cabe: cierra la línea en curso y empieza otra con la palabra (partiéndola
		// si tampoco cabe entera).
		lines = append(lines, cur)
		if ww > width {
			parts := splitWide(word, width)
			lines = append(lines, parts[:len(parts)-1]...)
			cur = parts[len(parts)-1]
			curW = uniseg.StringWidth(cur)
		} else {
			cur, curW = word, ww
		}
	}
	lines = append(lines, cur)
	return lines
}

// splitWords parte un párrafo en palabras por espacios ASCII, descartando los
// espacios (el wrap re-inserta uno entre palabras de la misma línea). Es un split
// simple: el wrap no preserva la cuenta exacta de espacios original (un wrap
// canónico colapsa el espaciado), decisión documentada.
func splitWords(p string) []string {
	var words []string
	start := -1
	for i := 0; i < len(p); i++ {
		if p[i] == ' ' {
			if start >= 0 {
				words = append(words, p[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		words = append(words, p[start:])
	}
	return words
}

// splitWide parte una palabra **más ancha que `width`** en trozos de a lo sumo
// `width` celdas, cortando por **grapheme** (nunca por la mitad de un grapheme ni
// de un emoji ZWJ). Devuelve al menos un trozo. Un grapheme aislado más ancho que
// `width` (un emoji de 2 celdas en un width de 1, patológico) ocupa su propio
// trozo aunque exceda —partir un grapheme es imposible sin romperlo—.
func splitWide(word string, width int) []string {
	var parts []string
	var cur string
	curW := 0
	state := -1
	rest := word
	for len(rest) > 0 {
		var cluster string
		cluster, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		cw := uniseg.StringWidth(cluster)
		// "Pieza no vacía" es cur != "", no curW > 0: una pieza que solo acumula
		// clusters de anchura 0 (un ZWJ huérfano) dejaba curW en 0 y el siguiente
		// cluster ancho se le pegaba, excediendo `width` con 2+ graphemes en la
		// pieza (lo cazó FuzzWrapText con "‍孡" y width=1).
		if cur != "" && curW+cw > width {
			parts = append(parts, cur)
			cur, curW = "", 0
		}
		cur += cluster
		curW += cw
	}
	if cur != "" || len(parts) == 0 {
		parts = append(parts, cur)
	}
	return parts
}

// truncateText es el algoritmo de recorte puro: devuelve `s` recortado a a lo sumo
// `width` celdas, por grapheme, con `ellipsis` al final si hubo recorte. Casos
// límite (de ahí su test 🔒):
//   - `s` cabe entero (`width(s) <= width`) → `s` sin tocar (sin elipsis).
//   - la elipsis no cabe en `width` (es más ancha que el hueco) → se recorta el
//     texto a `width` celdas sin elipsis (mejor texto a secas que nada).
//   - `width == 0` → "" (no cabe nada).
func truncateText(s string, width int, ellipsis string) string {
	if uniseg.StringWidth(s) <= width {
		return s
	}
	if width == 0 {
		return ""
	}

	ellW := uniseg.StringWidth(ellipsis)
	// Hueco para el texto: el ancho total menos el de la elipsis. Si la elipsis no
	// cabe (ancho >= width), no se usa: se recorta el texto a `width` a secas.
	budget := width - ellW
	if ellW > 0 && budget <= 0 {
		ellipsis = ""
		budget = width
	}

	var b string
	used := 0
	state := -1
	rest := s
	for len(rest) > 0 {
		var cluster string
		cluster, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		cw := uniseg.StringWidth(cluster)
		if used+cw > budget {
			break
		}
		b += cluster
		used += cw
	}
	return b + ellipsis
}
