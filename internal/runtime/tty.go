package runtime

// Parser de input del driver de TTY (api.md §9.3, sesión S33, CP-7). Convierte el
// flujo de bytes crudos que un terminal en raw mode entrega por stdin en los
// `inputEvent` normalizados que la pila de input (`feedInput`, input.go) despacha a
// Lua. Es la FUENTE de eventos que input.go describía como "el driver" y que hasta
// S33 no existía: input.go ya tenía la lógica 🔒 (pila + secuencias + timeout) y su
// punto de inyección (`feedInput`); aquí está lo que la alimenta desde un terminal de
// verdad.
//
// POR QUÉ AQUÍ Y NO EN EL DRIVER. El parseo de las secuencias ANSI (flechas, teclas
// con nombre, modificadores, pegado entre corchetes) es **lógica pura y determinista**
// —de un `[]byte` a una lista de eventos— y por eso se prueba por unidad sin un TTY
// (`tty_test.go`), igual que la lógica de despacho de input.go se prueba con eventos
// inyectados. La cáscara que de verdad necesita un terminal (raw mode, leer stdin,
// señales) es fina y vive en driver.go; toda la decisión está aquí.
//
// COBERTURA. El parser reconoce: caracteres imprimibles (UTF-8 multibyte), las teclas
// de control con nombre (enter/tab/backspace/esc), `ctrl+<letra>`, `alt+<tecla>` (ESC
// como prefijo, "meta"), las secuencias CSI/SS3 de flechas y navegación
// (home/end/insert/delete/pageup/pagedown/F1–F12) con sus modificadores
// (shift/alt/ctrl, `ESC[1;5A` = ctrl+up), el **pegado entre corchetes**
// (`ESC[200~`…`ESC[201~`) como un evento `paste`, y los reportes de **foco**
// (`ESC[I`/`ESC[O`) que el driver traduce a `ui:focus`. Lo que no reconoce (un CSI
// exótico) se descarta sin romper el flujo —un byte basura nunca cuelga el parser—.

import (
	"unicode/utf8"
)

// decodeInput parsea el prefijo de `buf` en eventos de input, devolviendo los eventos
// reconocidos y cuántos bytes consumió (el resto —una secuencia incompleta al final—
// se conserva para el siguiente read). Es PURA: misma entrada, misma salida; el driver
// (driver.go) la llama con lo que acumuló de stdin.
//
// `flush` resuelve la ambigüedad del **ESC solitario**: en raw mode un ESC es a la vez
// la tecla Escape y el prefijo de toda secuencia (flechas, etc.), así que un ESC al
// final del buffer puede ser cualquiera de las dos. Con `flush=false` (aún podrían
// llegar más bytes) una secuencia incompleta se DEJA sin consumir; con `flush=true`
// (venció el timeout entre teclas, o EOF) un ESC pendiente se emite como la tecla
// `esc` y un CSI a medias se descarta. Es la misma disciplina "se resuelve lo que haya
// o pasa el input" que input.go aplica a las secuencias de keymap, un nivel más abajo.
func decodeInput(buf []byte, flush bool) (evs []inputEvent, consumed int) {
	i := 0
	for i < len(buf) {
		b := buf[i]

		switch {
		case b == 0x1b: // ESC: tecla Escape o prefijo de una secuencia
			ev, n, ok := decodeEscape(buf[i:], flush)
			if !ok {
				// Secuencia incompleta: sin `flush` esperamos más bytes (no consumir).
				return evs, i
			}
			if ev != nil {
				evs = append(evs, *ev)
			}
			i += n

		case b == 0x0d || b == 0x0a: // CR / LF → enter
			evs = append(evs, keyEvent("enter", modSet{}))
			i++
			// Un CRLF (\r\n) es UN solo enter: traga el LF que sigue a un CR.
			if b == 0x0d && i < len(buf) && buf[i] == 0x0a {
				i++
			}

		case b == 0x09: // TAB
			evs = append(evs, keyEvent("tab", modSet{}))
			i++

		case b == 0x7f || b == 0x08: // DEL / BS → backspace
			evs = append(evs, keyEvent("backspace", modSet{}))
			i++

		case b == 0x00: // NUL → ctrl+space (ctrl+@)
			evs = append(evs, keyEvent("space", modSet{ctrl: true}))
			i++

		case b < 0x20: // otros controles 0x01–0x1f → ctrl+<letra>
			// 0x01='a' … 0x1a='z'. Los 0x1b–0x1f (ESC ya tratado, FS/GS/RS/US) son raros
			// en un teclado; se mapean a su letra de control por consistencia.
			letter := rune(b - 1 + 'a')
			evs = append(evs, keyEvent(string(letter), modSet{ctrl: true}))
			i++

		default: // byte imprimible: un grapheme UTF-8 (puede ser multibyte)
			r, size := utf8.DecodeRune(buf[i:])
			if r == utf8.RuneError && size == 1 {
				// UTF-8 incompleto al final del buffer: espera más bytes salvo en flush.
				if !flush && i+utf8.UTFMax > len(buf) {
					return evs, i
				}
				i++ // byte inválido suelto: descártalo
				continue
			}
			evs = append(evs, keyEvent(string(r), modSet{}))
			i += size
		}
	}
	return evs, i
}

// decodeEscape parsea una secuencia que empieza por ESC (`buf[0] == 0x1b`). Devuelve el
// evento (o nil si la secuencia se reconoce pero no produce tecla, p. ej. un reporte de
// foco que el driver maneja aparte vía `focusEvent`), los bytes consumidos y si la
// secuencia estaba COMPLETA. Con la secuencia incompleta devuelve `ok=false` salvo que
// `flush` fuerce a resolverla (ESC solo → tecla `esc`; CSI a medias → descartar).
func decodeEscape(buf []byte, flush bool) (ev *inputEvent, n int, ok bool) {
	// Solo el ESC en el buffer.
	if len(buf) == 1 {
		if flush {
			e := keyEvent("esc", modSet{})
			return &e, 1, true
		}
		return nil, 0, false
	}

	switch buf[1] {
	case '[': // CSI: flechas, navegación, modificadores, paste, foco
		return decodeCSI(buf, flush)
	case 'O': // SS3: flechas/F1–F4 en "application mode"
		return decodeSS3(buf, flush)
	default:
		// ESC <byte>: alt+<tecla> (meta). Reusa el decodificador de un byte simple para
		// el segundo byte, marcándolo con alt. Un ESC ESC es alt+esc.
		if buf[1] == 0x1b {
			e := keyEvent("esc", modSet{alt: true})
			return &e, 2, true
		}
		inner, m := decodeAltByte(buf[1:])
		if inner == "" {
			// Segundo byte no decodificable como tecla: trátalo como ESC solo + el byte.
			if flush {
				e := keyEvent("esc", modSet{})
				return &e, 1, true
			}
			return nil, 0, false
		}
		m.alt = true
		e := keyEvent(inner, m)
		return &e, 1 + altByteLen(buf[1:]), true
	}
}

// decodeAltByte decodifica el byte que sigue a un ESC para `alt+<tecla>`: un imprimible
// da su rune; enter/tab/backspace dan su nombre. Devuelve "" si no es decodificable.
func decodeAltByte(buf []byte) (string, modSet) {
	if len(buf) == 0 {
		return "", modSet{}
	}
	b := buf[0]
	switch {
	case b == 0x0d || b == 0x0a:
		return "enter", modSet{}
	case b == 0x09:
		return "tab", modSet{}
	case b == 0x7f || b == 0x08:
		return "backspace", modSet{}
	case b < 0x20:
		return string(rune(b - 1 + 'a')), modSet{ctrl: true}
	default:
		r, size := utf8.DecodeRune(buf)
		if r == utf8.RuneError && size == 1 {
			return "", modSet{}
		}
		return string(r), modSet{}
	}
}

// altByteLen devuelve cuántos bytes ocupa el segundo "carácter" de un `ESC <char>`
// (para que `decodeEscape` consuma el ESC + ese carácter completo).
func altByteLen(buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	if buf[0] >= 0x20 && buf[0] != 0x7f {
		_, size := utf8.DecodeRune(buf)
		return size
	}
	return 1
}

// decodeCSI parsea una secuencia CSI (`ESC [ ... letra`). Acumula los parámetros
// numéricos (separados por ';') hasta la letra final, decide la tecla por la letra (o
// por el primer parámetro en las secuencias `~`) y aplica los modificadores del segundo
// parámetro (`ESC[1;5A` = ctrl+up). Reconoce además el pegado entre corchetes
// (`ESC[200~`) y los reportes de foco (`ESC[I`/`ESC[O`).
func decodeCSI(buf []byte, flush bool) (*inputEvent, int, bool) {
	// buf[0]=ESC, buf[1]='['. Busca el byte final (0x40–0x7e) tras los parámetros.
	end := -1
	for j := 2; j < len(buf); j++ {
		c := buf[j]
		if (c >= '0' && c <= '9') || c == ';' || c == '<' || c == '?' {
			continue // bytes de parámetro
		}
		if c >= 0x40 && c <= 0x7e {
			end = j
			break
		}
		// Byte inesperado dentro de un CSI: secuencia corrupta, descártala hasta aquí.
		return nil, j, true
	}
	if end == -1 {
		// CSI incompleto: espera más bytes salvo en flush (entonces descártalo).
		if flush {
			return nil, len(buf), true
		}
		return nil, 0, false
	}

	final := buf[end]
	params := parseParams(buf[2:end])

	// Pegado entre corchetes: ESC[200~ abre, ESC[201~ cierra. El cuerpo es todo lo que
	// haya hasta el cierre; lo entregamos como un evento `paste` de texto.
	if final == '~' && len(params) > 0 && params[0] == 200 {
		return decodeBracketedPaste(buf, end, flush)
	}

	// Reporte de foco del terminal (modo 1004): ESC[I (gana foco) / ESC[O (pierde). No
	// es una tecla; el driver lo traduce a `ui:focus`. Se marca con un evento especial.
	if final == 'I' {
		return &inputEvent{typ: "focus", text: "in"}, end + 1, true
	}
	if final == 'O' && len(params) == 0 {
		return &inputEvent{typ: "focus", text: "out"}, end + 1, true
	}

	mods := modsFromParam(params, 1)
	key := csiKey(final, params)
	if key == "" {
		// CSI reconocido en forma pero sin tecla asociada (ratón, respuestas de
		// consulta…): consúmelo sin emitir. El ratón fino es trabajo futuro (caps.mouse).
		return nil, end + 1, true
	}
	e := keyEvent(key, mods)
	return &e, end + 1, true
}

// decodeBracketedPaste extrae el cuerpo de un pegado entre corchetes. `openEnd` es el
// índice del '~' que cierra el `ESC[200~`. Busca el `ESC[201~` de cierre y entrega el
// texto intermedio como un evento `paste`. Si el cierre aún no llegó, espera (salvo
// flush, donde entrega lo que haya).
func decodeBracketedPaste(buf []byte, openEnd int, flush bool) (*inputEvent, int, bool) {
	body := openEnd + 1
	closeSeq := []byte("\x1b[201~")
	idx := indexOf(buf[body:], closeSeq)
	if idx == -1 {
		if flush {
			// Sin cierre pero hay que resolver: entrega lo acumulado como paste.
			e := pasteEvent(string(buf[body:]))
			return &e, len(buf), true
		}
		return nil, 0, false
	}
	text := string(buf[body : body+idx])
	consumed := body + idx + len(closeSeq)
	e := pasteEvent(text)
	return &e, consumed, true
}

// parseParams parte los parámetros numéricos de un CSI ("1;5" → [1,5]). Un parámetro
// vacío es 0. Ignora prefijos no numéricos ('<'/'?') tratándolos como 0-grupos.
func parseParams(b []byte) []int {
	if len(b) == 0 {
		return nil
	}
	var out []int
	cur := 0
	has := false
	for _, c := range b {
		if c >= '0' && c <= '9' {
			cur = cur*10 + int(c-'0')
			has = true
		} else if c == ';' {
			out = append(out, cur)
			cur, has = 0, false
		}
		// '<','?' y otros: se ignoran (no abren parámetro numérico).
	}
	if has || len(out) > 0 {
		out = append(out, cur)
	}
	return out
}

// modsFromParam traduce el parámetro de modificador de un CSI (el de índice `idx`, 1-
// based en la convención xterm) al `modSet`. El valor `m` codifica `m-1` como bitmask:
// bit0=shift, bit1=alt(meta), bit2=ctrl, bit3=meta. Sin ese parámetro, sin modificadores.
func modsFromParam(params []int, idx int) modSet {
	if len(params) <= idx || params[idx] <= 1 {
		return modSet{}
	}
	bits := params[idx] - 1
	return modSet{
		shift: bits&1 != 0,
		alt:   bits&2 != 0,
		ctrl:  bits&4 != 0,
		meta:  bits&8 != 0,
	}
}

// csiKey resuelve el nombre de tecla de un CSI por su byte final y, para las secuencias
// `~`, por su primer parámetro (la convención xterm de "tilde codes").
func csiKey(final byte, params []int) string {
	switch final {
	case 'A':
		return "up"
	case 'B':
		return "down"
	case 'C':
		return "right"
	case 'D':
		return "left"
	case 'H':
		return "home"
	case 'F':
		return "end"
	case 'Z':
		return "tab" // ESC[Z = shift+tab (el modificador lo pone modsFromParam/terminal)
	case 'P':
		return "f1"
	case 'Q':
		return "f2"
	case 'R':
		return "f3"
	case 'S':
		return "f4"
	case '~':
		if len(params) == 0 {
			return ""
		}
		switch params[0] {
		case 1, 7:
			return "home"
		case 2:
			return "insert"
		case 3:
			return "delete"
		case 4, 8:
			return "end"
		case 5:
			return "pageup"
		case 6:
			return "pagedown"
		case 11:
			return "f1"
		case 12:
			return "f2"
		case 13:
			return "f3"
		case 14:
			return "f4"
		case 15:
			return "f5"
		case 17:
			return "f6"
		case 18:
			return "f7"
		case 19:
			return "f8"
		case 20:
			return "f9"
		case 21:
			return "f10"
		case 23:
			return "f11"
		case 24:
			return "f12"
		}
	}
	return ""
}

// decodeSS3 parsea una secuencia SS3 (`ESC O <letra>`): las flechas y F1–F4 que algunos
// terminales emiten en "application keypad mode". Equivale a su CSI (`ESC O A` = up).
func decodeSS3(buf []byte, flush bool) (*inputEvent, int, bool) {
	if len(buf) < 3 {
		if flush {
			return nil, len(buf), true
		}
		return nil, 0, false
	}
	var key string
	switch buf[2] {
	case 'A':
		key = "up"
	case 'B':
		key = "down"
	case 'C':
		key = "right"
	case 'D':
		key = "left"
	case 'H':
		key = "home"
	case 'F':
		key = "end"
	case 'P':
		key = "f1"
	case 'Q':
		key = "f2"
	case 'R':
		key = "f3"
	case 'S':
		key = "f4"
	default:
		return nil, 3, true // SS3 desconocido: consúmelo sin emitir
	}
	e := keyEvent(key, modSet{})
	return &e, 3, true
}

// keyEvent construye un `inputEvent` de tecla con su nombre canónico y modificadores.
func keyEvent(key string, mods modSet) inputEvent {
	return inputEvent{typ: "key", key: key, mods: mods}
}

// pasteEvent construye un `inputEvent` de pegado de TEXTO (el cuerpo de un pegado entre
// corchetes). `pasteIsText=true` para que `materializePaste` (input.go, G30) lo deje
// intacto —solo un pegado de bytes binarios se vuelca a fichero—.
func pasteEvent(text string) inputEvent {
	return inputEvent{typ: "paste", text: text, pasteIsText: true}
}

// indexOf devuelve el índice de la primera aparición de `sep` en `b`, o -1. Pequeño
// helper local (sin tirar de bytes.Index para no introducir el import por un solo uso).
func indexOf(b, sep []byte) int {
	if len(sep) == 0 {
		return 0
	}
	for i := 0; i+len(sep) <= len(b); i++ {
		match := true
		for j := range sep {
			if b[i+j] != sep[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
