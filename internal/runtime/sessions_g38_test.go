package runtime

// Tests de G38: el slug de proyecto de `sessions/<proyecto>/` es PARTE DEL
// FORMATO (sesiones.md §2) y la extensión lo expone como funciones puras
// (`sessions.slug`, `sessions.dir`) para que ningún plugin reimplemente la
// codificación. Blindan:
//   - el algoritmo especificado: fuera de [A-Za-z0-9.-] → `_`, recorte de `_`
//     en los bordes, vacío → "root";
//   - sus propiedades declaradas: con pérdida (colisiones posibles) y clave de
//     agrupación, no identidad;
//   - que `sessions.dir` compone data_dir()/sessions/<slug> exactamente donde
//     `open` escribe de verdad: una sola fuente de verdad.
//
// Mismo arnés que sessions_test.go (bootSessions + inTask).

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSessionsSlugEspecificacion (G38): el algoritmo del slug cumple la
// especificación de sesiones.md §2, caso a caso.
func TestSessionsSlugEspecificacion(t *testing.T) {
	h, _ := bootSessions(t)
	cases := [][2]string{
		{`/home/diego/nu`, "home_diego_nu"},   // el ejemplo literal de la espec
		{``, "root"},                          // vacío → "root"
		{`/`, "root"},                         // solo separadores → "root"
		{`___`, "root"},                       // solo `_` tras recortar → "root"
		{`/tmp/x-1.2`, "tmp_x-1.2"},           // `-` y `.` se conservan
		{`C:\\repos\\nu`, "C__repos_nu"},      // `:` y `\` → un `_` cada uno (sin colapsar)
		{`/con espacios/x`, "con_espacios_x"}, // espacio → `_`
	}
	for _, c := range cases {
		h.expectEval(`local s = require("sessions"); return s.slug("`+c[0]+`")`, c[1])
	}
}

// TestSessionsSlugConPerdida (G38): la propiedad "con pérdida" está declarada en
// la espec — dos cwd patológicamente parecidos COLISIONAN, y eso es contractual
// (clave de agrupación, no identidad; desambiguar = leer la línea `meta`).
func TestSessionsSlugConPerdida(t *testing.T) {
	h, _ := bootSessions(t)
	h.expectEval(`local s = require("sessions"); return tostring(s.slug("/a/b") == s.slug("/a_b"))`, "true")
}

// TestSessionsDirComponeLaRuta (G38): sessions.dir(cwd) es exactamente
// data_dir()/sessions/<slug>, y coincide con el directorio donde `open` escribe
// de verdad el JSONL — la función pública y la interna son la misma fuente.
func TestSessionsDirComponeLaRuta(t *testing.T) {
	h, dataDir := bootSessions(t)
	got := h.eval(`local s = require("sessions"); return s.dir("/home/diego/nu")`)
	want := filepath.Join(dataDir, "sessions", "home_diego_nu")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("sessions.dir: got %q, want %q", got, want)
	}

	// La prueba de fuego: abrir una sesión con ese cwd escribe DENTRO de dir().
	h.eval(inTask(`
		local sessions = require("sessions")
		local s = sessions.open({ cwd = "/home/diego/nu" })
		SID = s.id
		s:close()
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")
	id := h.eval(`return tostring(SID)`)[0]
	if _, err := os.Stat(filepath.Join(want, id+".jsonl")); err != nil {
		t.Fatalf("el JSONL de la sesión no está en sessions.dir(cwd): %v", err)
	}
}
