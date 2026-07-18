package runtime

// Tests del PARSER de input del driver de TTY (tty.go, S33): de bytes crudos del
// terminal a `inputEvent`. Es la lógica pura del CP-7 —"de un []byte a una lista de
// eventos"— y por eso se blinda por unidad sin un TTY, igual que input.go blinda la pila
// y las secuencias con eventos inyectados. Cubre los caminos que un teclado real ejercita
// (imprimibles UTF-8, control con nombre, ctrl/alt, flechas y navegación CSI/SS3 con
// modificadores, pegado entre corchetes, foco) y los casos límite del troceado de stdin
// (secuencias partidas entre dos reads, ESC solitario, basura).

import (
	"reflect"
	"testing"
)

// dec es un atajo: decodifica todo el buffer en modo flush (resuelve lo pendiente) y
// devuelve solo los eventos —el caso común de un test que pasa una secuencia completa—.
func dec(b string) []inputEvent {
	evs, _ := decodeInput([]byte(b), true)
	return evs
}

func TestDecodePrintableAndUTF8(t *testing.T) {
	got := dec("aZ9")
	want := []inputEvent{
		keyEvent("a", modSet{}),
		keyEvent("Z", modSet{}),
		keyEvent("9", modSet{}),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imprimibles: got %+v want %+v", got, want)
	}
	// Un grapheme multibyte (é = 2 bytes, € = 3, 😀 = 4) es UN evento con la rune entera.
	for _, s := range []string{"é", "€", "😀"} {
		got := dec(s)
		if len(got) != 1 || got[0].key != s {
			t.Fatalf("UTF-8 %q: got %+v, want un único key=%q", s, got, s)
		}
	}
}

func TestDecodeControlKeys(t *testing.T) {
	cases := map[string]inputEvent{
		"\r":   keyEvent("enter", modSet{}),
		"\n":   keyEvent("enter", modSet{}),
		"\t":   keyEvent("tab", modSet{}),
		"\x7f": keyEvent("backspace", modSet{}),
		"\x08": keyEvent("backspace", modSet{}),
		"\x00": keyEvent("space", modSet{ctrl: true}),
		"\x01": keyEvent("a", modSet{ctrl: true}), // ctrl+a
		"\x03": keyEvent("c", modSet{ctrl: true}), // ctrl+c (¡no es señal en raw mode!)
		"\x1a": keyEvent("z", modSet{ctrl: true}), // ctrl+z
	}
	for in, want := range cases {
		got := dec(in)
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Fatalf("control %q: got %+v, want %+v", in, got, want)
		}
	}
}

func TestDecodeCRLFIsOneEnter(t *testing.T) {
	got := dec("\r\n")
	if len(got) != 1 || got[0].key != "enter" {
		t.Fatalf("CRLF debe ser un solo enter: got %+v", got)
	}
}

func TestDecodeArrowsCSIandSS3(t *testing.T) {
	cases := map[string]string{
		"\x1b[A": "up", "\x1b[B": "down", "\x1b[C": "right", "\x1b[D": "left",
		"\x1b[H": "home", "\x1b[F": "end",
		"\x1bOA": "up", "\x1bOB": "down", "\x1bOC": "right", "\x1bOD": "left",
	}
	for in, want := range cases {
		got := dec(in)
		if len(got) != 1 || got[0].key != want || got[0].typ != "key" {
			t.Fatalf("flecha %q: got %+v, want key=%q", in, got, want)
		}
	}
}

func TestDecodeNavTildeKeys(t *testing.T) {
	cases := map[string]string{
		"\x1b[2~": "insert", "\x1b[3~": "delete",
		"\x1b[5~": "pageup", "\x1b[6~": "pagedown",
		"\x1b[1~": "home", "\x1b[4~": "end",
		"\x1b[15~": "f5", "\x1b[24~": "f12",
	}
	for in, want := range cases {
		got := dec(in)
		if len(got) != 1 || got[0].key != want {
			t.Fatalf("nav %q: got %+v, want key=%q", in, got, want)
		}
	}
}

func TestDecodeModifiedArrow(t *testing.T) {
	// ESC[1;5A = ctrl+up; ESC[1;2C = shift+right; ESC[1;3B = alt+down.
	got := dec("\x1b[1;5A")
	if len(got) != 1 || got[0].key != "up" || !got[0].mods.ctrl {
		t.Fatalf("ctrl+up: got %+v", got)
	}
	got = dec("\x1b[1;2C")
	if len(got) != 1 || got[0].key != "right" || !got[0].mods.shift {
		t.Fatalf("shift+right: got %+v", got)
	}
	got = dec("\x1b[1;3B")
	if len(got) != 1 || got[0].key != "down" || !got[0].mods.alt {
		t.Fatalf("alt+down: got %+v", got)
	}
}

func TestDecodeAltKey(t *testing.T) {
	// ESC seguido de un imprimible = alt+<tecla> (meta).
	got := dec("\x1bx")
	if len(got) != 1 || got[0].key != "x" || !got[0].mods.alt {
		t.Fatalf("alt+x: got %+v", got)
	}
	// ESC + enter = alt+enter.
	got = dec("\x1b\r")
	if len(got) != 1 || got[0].key != "enter" || !got[0].mods.alt {
		t.Fatalf("alt+enter: got %+v", got)
	}
}

func TestDecodeLoneEscWithFlush(t *testing.T) {
	// Un ESC solitario, en flush (venció el timeout), es la tecla esc.
	got := dec("\x1b")
	if len(got) != 1 || got[0].key != "esc" {
		t.Fatalf("esc solo: got %+v", got)
	}
	// Sin flush, un ESC al final NO se consume (podría ser el prefijo de una secuencia).
	evs, consumed := decodeInput([]byte("\x1b"), false)
	if len(evs) != 0 || consumed != 0 {
		t.Fatalf("esc pendiente sin flush: evs=%+v consumed=%d, want vacío/0", evs, consumed)
	}
}

func TestDecodeSplitCRLF(t *testing.T) {
	// CP-7 (pasada de salud 2026-07-17): un CR al final del buffer es ambiguo —
	// enter suelto o primera mitad de un CRLF partido entre reads— y sin flush se
	// deja pendiente, como el ESC solitario. Antes se consumía como enter y el LF
	// del siguiente read producía un SEGUNDO enter.
	evs, consumed := decodeInput([]byte("\r"), false)
	if len(evs) != 0 || consumed != 0 {
		t.Fatalf("CR pendiente sin flush: evs=%+v consumed=%d, want vacío/0", evs, consumed)
	}
	// Con el LF ya llegado, el CRLF completo es UN solo enter.
	evs, consumed = decodeInput([]byte("\r\n"), false)
	if len(evs) != 1 || evs[0].key != "enter" || consumed != 2 {
		t.Fatalf("CRLF completo: evs=%+v consumed=%d, want un enter/2", evs, consumed)
	}
	// En flush (los 30 ms del driver, o EOF), el CR solitario se resuelve como enter.
	evs, consumed = decodeInput([]byte("\r"), true)
	if len(evs) != 1 || evs[0].key != "enter" || consumed != 1 {
		t.Fatalf("CR solo con flush: evs=%+v consumed=%d, want un enter/1", evs, consumed)
	}
	// Un CR en mitad del buffer no es ambiguo: enter inmediato (aquí seguido de texto).
	evs, consumed = decodeInput([]byte("\ra"), false)
	if len(evs) != 2 || evs[0].key != "enter" || evs[1].key != "a" || consumed != 2 {
		t.Fatalf("CR en mitad: evs=%+v consumed=%d", evs, consumed)
	}
}

func TestDecodeSplitSequence(t *testing.T) {
	// Una flecha partida entre dos reads: el primer trozo deja el CSI a medias (no
	// consume); con el resto se completa.
	evs, consumed := decodeInput([]byte("\x1b["), false)
	if len(evs) != 0 || consumed != 0 {
		t.Fatalf("CSI a medias sin flush: evs=%+v consumed=%d", evs, consumed)
	}
	evs, consumed = decodeInput([]byte("\x1b[A"), false)
	if len(evs) != 1 || evs[0].key != "up" || consumed != 3 {
		t.Fatalf("CSI completo: evs=%+v consumed=%d", evs, consumed)
	}
}

func TestDecodePrefixBeforeIncompleteTail(t *testing.T) {
	// Texto completo seguido de un CSI incompleto: se consume el texto y se deja el CSI.
	evs, consumed := decodeInput([]byte("ab\x1b["), false)
	if len(evs) != 2 || evs[0].key != "a" || evs[1].key != "b" {
		t.Fatalf("prefijo: evs=%+v", evs)
	}
	if consumed != 2 {
		t.Fatalf("debe dejar el CSI incompleto sin consumir: consumed=%d, want 2", consumed)
	}
}

func TestDecodeBracketedPaste(t *testing.T) {
	got := dec("\x1b[200~hola mundo\x1b[201~")
	if len(got) != 1 || got[0].typ != "paste" || got[0].text != "hola mundo" || !got[0].pasteIsText {
		t.Fatalf("paste: got %+v", got)
	}
	// Un pegado con una secuencia ESC dentro del cuerpo se entrega literal (hasta el cierre).
	got = dec("\x1b[200~a\x1b[Ab\x1b[201~")
	if len(got) != 1 || got[0].typ != "paste" || got[0].text != "a\x1b[Ab" {
		t.Fatalf("paste con cuerpo ANSI: got %+v", got)
	}
}

func TestDecodeFocusReports(t *testing.T) {
	got := dec("\x1b[I")
	if len(got) != 1 || got[0].typ != "focus" || got[0].text != "in" {
		t.Fatalf("focus in: got %+v", got)
	}
	got = dec("\x1b[O")
	if len(got) != 1 || got[0].typ != "focus" || got[0].text != "out" {
		t.Fatalf("focus out: got %+v", got)
	}
}

func TestDecodeMixedBatch(t *testing.T) {
	// Un read típico con varias teclas pegadas: texto + enter + flecha + ctrl.
	got := dec("hi\r\x1b[B\x01")
	want := []inputEvent{
		keyEvent("h", modSet{}),
		keyEvent("i", modSet{}),
		keyEvent("enter", modSet{}),
		keyEvent("down", modSet{}),
		keyEvent("a", modSet{ctrl: true}),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batch mixto: got %+v want %+v", got, want)
	}
}
