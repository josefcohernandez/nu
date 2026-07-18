package runtime

// Tests del portapapeles OSC 52 (§9.2, S32). Blindan la LÓGICA PROPIA y arriesgada de
// `enu.ui.clipboard_set`/`clipboard_get`:
//
//   - **set**: la secuencia OSC 52 emitida es exactamente `ESC ] 52 ; c ; <base64> BEL`
//     con el base64 del contenido (round-trip de bytes arbitrarios: salto de línea,
//     UTF-8 multibyte).
//   - **get (parseo)**: una respuesta OSC 52 sintética se decodifica a la cadena
//     original; terminador BEL o ST; ruido por delante; el selector se ignora.
//   - **get (sin soporte)**: respuesta vacía, base64 corrupto, sin terminador, o el
//     `?` de la consulta rebotado → `nil` (false), no una cadena espuria.
//
// Son tests de caja blanca sobre las funciones puras (`encodeOSC52Set`,
// `parseOSC52Reply`): el ida y vuelta real con un TTY es del driver (S33+), aquí se
// prueba la lógica con bytes sintéticos (como el input de S31 con `feedInput`).

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestEncodeOSC52Set comprueba la forma exacta de la secuencia de copia y el round-trip
// del contenido por base64 (incluido contenido con bytes "peligrosos").
func TestEncodeOSC52Set(t *testing.T) {
	cases := []string{
		"hola",
		"",                // vacío: base64 vacío, secuencia bien formada igual
		"línea1\nlínea2",  // salto de línea y UTF-8 multibyte
		"emoji 🎉 y ñandú", // multibyte ancho
		"\x1b]52;c;x\a",   // contenido que PARECE una OSC 52: el base64 lo neutraliza
	}
	for _, in := range cases {
		seq := encodeOSC52Set(in)
		if !strings.HasPrefix(seq, "\x1b]52;c;") {
			t.Fatalf("set(%q): prefijo OSC 52 ausente: %q", in, seq)
		}
		if !strings.HasSuffix(seq, "\a") {
			t.Fatalf("set(%q): terminador BEL ausente: %q", in, seq)
		}
		// El payload entre el prefijo y el BEL debe ser el base64 estándar del contenido.
		payload := seq[len("\x1b]52;c;") : len(seq)-1]
		want := base64.StdEncoding.EncodeToString([]byte(in))
		if payload != want {
			t.Fatalf("set(%q): base64 = %q, quería %q", in, payload, want)
		}
		// Round-trip: decodificar el payload recupera el contenido original.
		dec, err := base64.StdEncoding.DecodeString(payload)
		if err != nil || string(dec) != in {
			t.Fatalf("set(%q): round-trip falló: %q err=%v", in, dec, err)
		}
	}
}

// TestParseOSC52ReplyOK blinda el parseo de una respuesta válida a la cadena original,
// con los dos terminadores y tolerando ruido y el selector arbitrario.
func TestParseOSC52ReplyOK(t *testing.T) {
	mk := func(content, sel, term string) []byte {
		return []byte("\x1b]52;" + sel + ";" + base64.StdEncoding.EncodeToString([]byte(content)) + term)
	}
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"bel", mk("hola", "c", "\a"), "hola"},
		{"st", mk("hola", "c", "\x1b\\"), "hola"},
		{"selector p ignorado", mk("texto", "p", "\a"), "texto"},
		{"selector vacío", mk("x", "", "\a"), "x"},
		{"multibyte", mk("ñ 🎉", "c", "\a"), "ñ 🎉"},
		{"salto de línea", mk("a\nb", "c", "\a"), "a\nb"},
		{"ruido por delante", append([]byte("basura previa"), mk("ok", "c", "\a")...), "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseOSC52Reply(c.data)
			if !ok {
				t.Fatalf("parse(%q) = (_, false), quería ok=true", c.data)
			}
			if got != c.want {
				t.Fatalf("parse(%q) = %q, quería %q", c.data, got, c.want)
			}
		})
	}
}

// TestParseOSC52ReplyFail blinda que una respuesta ilegible o un "sin soporte" se
// traduzca a (false) —que `clipboard_get` convierte a `nil`—, nunca a una cadena.
func TestParseOSC52ReplyFail(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"vacío", []byte("")},
		{"basura sin OSC", []byte("respuesta cualquiera del terminal")},
		{"sin terminador (truncada)", []byte("\x1b]52;c;aG9sYQ==")},
		{"sin selector", []byte("\x1b]52;aG9sYQ==\a")},
		{"payload vacío", []byte("\x1b]52;c;\a")},
		{"query rebotada (?)", []byte("\x1b]52;c;?\a")},
		{"base64 corrupto", []byte("\x1b]52;c;no-es-base64!!!\a")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, ok := parseOSC52Reply(c.data); ok {
				t.Fatalf("parse(%q) = (%q, true), quería ok=false", c.data, got)
			}
		})
	}
}

// TestReadOSC52ReplyNilReader comprueba que, sin driver de TTY (reader nil, el caso de
// este entorno headless), la lectura resuelve a (false): `clipboard_get` devolverá nil.
func TestReadOSC52ReplyNilReader(t *testing.T) {
	if _, ok := readOSC52Reply(nil, clipboardReadTimeout); ok {
		t.Fatal("readOSC52Reply(nil) debería ser (_, false) sin driver")
	}
}

// TestReadOSC52ReplyFromReader comprueba el camino de lectura+parseo desde un reader
// sintético (lo que el driver de S33+ proveerá): un terminal que responde con una OSC
// 52 válida → la cadena; uno que no responde nada (EOF) → nil.
func TestReadOSC52ReplyFromReader(t *testing.T) {
	reply := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("pegado")) + "\a"
	got, ok := readOSC52Reply(strings.NewReader(reply), clipboardReadTimeout)
	if !ok || got != "pegado" {
		t.Fatalf("readOSC52Reply(reply) = (%q, %v), quería (\"pegado\", true)", got, ok)
	}

	if _, ok := readOSC52Reply(strings.NewReader(""), clipboardReadTimeout); ok {
		t.Fatal("readOSC52Reply(reader vacío) debería ser (_, false)")
	}
}
