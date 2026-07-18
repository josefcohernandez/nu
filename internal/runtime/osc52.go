package runtime

// Portapapeles de `enu.ui` vía OSC 52 (api.md §9.2, sesión S32). Dos firmas, ambas
// **solo estado principal** (ADR-008: `enu.ui` no cruza a workers ni existe headless):
//
//   - `enu.ui.clipboard_set(s)` — copia `s` al portapapeles del sistema escribiendo
//     la secuencia OSC 52 `ESC ] 52 ; c ; <base64(s)> BEL` al terminal. No ⏸: es un
//     write directo de unos bytes; el terminal la interpreta y no responde nada.
//   - `enu.ui.clipboard_get() -> string?` ⏸ — pide el portapapeles enviando la
//     consulta OSC 52 (`ESC ] 52 ; c ; ? BEL`) y **espera la respuesta** del terminal
//     (de ahí ⏸): el terminal contesta con `ESC ] 52 ; c ; <base64> ST/BEL`, que se
//     parsea a la cadena original. `nil` si el terminal no soporta OSC 52 o si pasa el
//     timeout sin respuesta.
//
// POR QUÉ OSC 52 Y NO UN PORTAPAPELES NATIVO. La regla "cero dependency hell"
// (ADR-001/010: un binario estático sin CGO) descarta enlazar contra X11/Wayland/
// AppKit. OSC 52 es la vía in-band del propio protocolo del terminal: copia y pega
// viajan por el mismo flujo que el resto del render, funcionan sobre SSH sin
// reenvío de X, y no añaden ninguna dependencia de sistema. Su límite —que el
// terminal debe soportarlo y, para `get`, permitir la lectura (muchos la desactivan
// por seguridad)— se modela honestamente: `get` devuelve `nil` si no llega
// respuesta, en vez de fingir un portapapeles vacío.
//
// QUÉ ES DRIVER Y QUÉ ES LÓGICA PROBADA. Igual que el input (S31): en este entorno
// headless no hay un TTY real con el que hacer el ida y vuelta de `get`. La lógica
// propia —codificar la secuencia de `set` y **parsear** la respuesta de `get`— se
// blinda por unidad con bytes sintéticos (`osc52_test.go`); el ida y vuelta real
// sobre un terminal vivo es del driver de TTY (CP-7 manual, S33+).

import (
	"encoding/base64"
	"io"
	"strings"
	"time"
)

// clipboardReadTimeout es cuánto espera `clipboard_get` la respuesta OSC 52 del
// terminal antes de rendirse y devolver `nil`. Un terminal que soporta la lectura
// responde en microsegundos (es local); uno que no la soporta —o que la tiene
// desactivada por seguridad— no responde nunca, así que un timeout corto evita
// colgar la task perceptiblemente. 200 ms es holgado para un terminal local o por
// SSH y aún imperceptible para un humano que pega.
const clipboardReadTimeout = 200 * time.Millisecond

// encodeOSC52Set construye la secuencia OSC 52 que copia `s` al portapapeles del
// sistema (`c` = clipboard, el destino por defecto): `ESC ] 52 ; c ; <base64> BEL`.
// El contenido va en base64 estándar (el que el protocolo exige), de modo que
// cualquier byte de `s` —incluido un salto de línea o UTF-8 multibyte— viaje intacto
// sin romper la secuencia. Es una función pura para poder verificar los bytes exactos
// por unidad (el camino caliente real solo los escribe al TTY).
func encodeOSC52Set(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	return "\x1b]52;c;" + enc + "\a"
}

// encodeOSC52Query es la secuencia que PIDE el portapapeles: `ESC ] 52 ; c ; ? BEL`.
// El `?` en lugar del base64 es lo que distingue una consulta de un `set` en el
// protocolo; el terminal responde con un `set` equivalente que `parseOSC52Reply`
// decodifica.
func encodeOSC52Query() string {
	return "\x1b]52;c;?\a"
}

// parseOSC52Reply parsea la respuesta OSC 52 de un terminal a la cadena del
// portapapeles (la LÓGICA 🔒 de `clipboard_get`). La respuesta tiene la forma
// `ESC ] 52 ; <sel> ; <base64> <terminador>`, donde el terminador es BEL (`\a`) o
// ST (`ESC \`), y `<sel>` es el selector (`c`, `p`, `s`, ...) que aquí se ignora —se
// pidió el portapapeles y se acepta lo que el terminal devuelva—. Devuelve
// `(texto, true)` si la respuesta es una OSC 52 bien formada con base64 válido, o
// `("", false)` si no lo es (basura, vacío, base64 corrupto): el llamante traduce el
// `false` a `nil` (terminal sin soporte / respuesta ilegible).
//
// Tolerante a propósito: un terminal puede anteponer/posponer ruido (otras
// respuestas a consultas, restos del buffer de entrada), así que se busca el
// prefijo `ESC ] 52 ;` dentro de `data` en vez de exigir que empiece exactamente
// ahí. Si el `?` de una consulta nuestra rebota (algunos terminales lo hacen al no
// soportar la lectura), el base64 sale vacío o es `?`, que no decodifica a algo útil:
// se trata como sin soporte.
func parseOSC52Reply(data []byte) (string, bool) {
	s := string(data)

	// Localiza el comienzo de la secuencia OSC 52 (tolerando ruido por delante).
	const prefix = "\x1b]52;"
	start := strings.Index(s, prefix)
	if start < 0 {
		return "", false
	}
	body := s[start+len(prefix):]

	// Recorta el terminador: ST (`ESC \`) o BEL (`\a`), lo que llegue primero. Si no
	// hay terminador, la respuesta está truncada → no soportada/ilegible.
	end := len(body)
	if i := strings.IndexByte(body, '\a'); i >= 0 {
		end = i
	}
	if i := strings.Index(body, "\x1b\\"); i >= 0 && i < end {
		end = i
	}
	if end == len(body) {
		return "", false // sin terminador: truncada
	}
	body = body[:end]

	// `body` es `<sel> ; <base64>`. Separa el selector del payload por el primer ';'.
	semi := strings.IndexByte(body, ';')
	if semi < 0 {
		return "", false
	}
	b64 := body[semi+1:]
	if b64 == "" || b64 == "?" {
		// Vacío o el `?` de la consulta rebotado: el terminal no entregó nada.
		return "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", false // base64 corrupto: ilegible
	}
	return string(decoded), true
}

// readOSC52Reply lee del terminal (`r`) la respuesta a una consulta OSC 52 y la
// parsea, con un techo de tiempo `timeout` (el camino de fondo de `clipboard_get`,
// que corre SIN el token). Acumula bytes hasta poder parsear una respuesta completa
// (`parseOSC52Reply`) o hasta que una lectura termine sin más datos. Devuelve
// `(texto, true)` si la respuesta es legible, o `("", false)` si `r` es nil (entorno
// headless sin driver de TTY), si la lectura falla, o si lo leído no es una OSC 52
// válida (terminal sin soporte). El `timeout` se respeta delegándolo en el `r` que el
// driver provea (un lector con deadline): aquí no se arma un timer propio para no
// quedar leyendo de un `r` bloqueante tras devolver —el driver (S33+) es quien sabe
// poner el deadline al TTY real—.
//
// En este entorno headless `r` es siempre nil (no hay driver): la función retorna
// `("", false)` de inmediato, de modo que el camino vivo de `clipboard_get` resuelve
// a `nil`. La lógica de PARSEO —la parte propia y arriesgada— se blinda aparte con
// `parseOSC52Reply` sobre bytes sintéticos.
func readOSC52Reply(r io.Reader, timeout time.Duration) (string, bool) {
	if r == nil {
		return "", false
	}
	_ = timeout // el deadline lo aplica el `r` del driver (S33+); ver cabecera
	var acc []byte
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			if text, ok := parseOSC52Reply(acc); ok {
				return text, true
			}
		}
		if err != nil {
			// EOF o deadline del lector: intenta un último parseo de lo acumulado.
			return parseOSC52Reply(acc)
		}
	}
}
