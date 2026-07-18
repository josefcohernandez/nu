package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests de `enu.log` (S03, api.md §15). S03 no está en el inventario 🔒, pero el
// formateo del mensaje, la apertura perezosa del fichero y el alias `print` son
// lógica propia con casos de borde (vararg vs argumento único, fichero aún sin
// crear), así que se prueban desde el lado del autor de extensiones con el
// arnés —el mismo Lua que escribiría un plugin— y se lee el fichero resultante.

// línea esperada: "<timestamp> <LEVEL> [<owner>] <mensaje>". Estos helpers
// comprueban las partes estables sin atarse al timestamp exacto.

func TestLogInfoEscribeLinea(t *testing.T) {
	h := newHarness(t)
	h.eval(`enu.log.info("hola")`)

	lines := h.logLines()
	if len(lines) != 1 {
		t.Fatalf("se esperaba 1 línea, hay %d: %q", len(lines), lines)
	}
	line := lines[0]
	if !strings.Contains(line, "INFO") {
		t.Errorf("la línea no anota el nivel INFO: %q", line)
	}
	if !strings.Contains(line, "[user]") {
		t.Errorf("la línea no anota el plugin de origen [user]: %q", line)
	}
	if !strings.HasSuffix(line, "hola") {
		t.Errorf("la línea no termina con el mensaje: %q", line)
	}
}

func TestLogNiveles(t *testing.T) {
	cases := []struct {
		fn    string
		label string
	}{
		{"debug", "DEBUG"},
		{"info", "INFO"},
		{"warn", "WARN"},
		{"error", "ERROR"},
	}
	for _, c := range cases {
		t.Run(c.fn, func(t *testing.T) {
			h := newHarness(t)
			h.eval(`enu.log.` + c.fn + `("m")`)
			lines := h.logLines()
			if len(lines) != 1 {
				t.Fatalf("se esperaba 1 línea, hay %d: %q", len(lines), lines)
			}
			// La etiqueta va seguida (tras el padding) de " [": comprobamos que
			// el nivel aparece como token, no como subcadena de otra cosa.
			if !strings.Contains(lines[0], " "+c.label+" ") &&
				!strings.Contains(lines[0], " "+c.label+"  ") {
				t.Errorf("nivel %s no aparece etiquetado: %q", c.label, lines[0])
			}
		})
	}
}

func TestLogFormateaVarargs(t *testing.T) {
	h := newHarness(t)
	// fmt + args -> string.format, semántica de Lua.
	h.eval(`enu.log.info("valor: %d (%s)", 42, "ok")`)
	lines := h.logLines()
	if len(lines) != 1 {
		t.Fatalf("se esperaba 1 línea, hay %d: %q", len(lines), lines)
	}
	if !strings.HasSuffix(lines[0], "valor: 42 (ok)") {
		t.Errorf("el mensaje no se formateó: %q", lines[0])
	}
}

func TestLogArgumentoUnicoNoSeFormatea(t *testing.T) {
	h := newHarness(t)
	// Un solo argumento con un `%` no debe tratarse como formato (no hay args
	// que consumir): se loguea tal cual vía tostring.
	h.eval(`enu.log.info("100% hecho")`)
	lines := h.logLines()
	if len(lines) != 1 {
		t.Fatalf("se esperaba 1 línea, hay %d: %q", len(lines), lines)
	}
	if !strings.HasSuffix(lines[0], "100% hecho") {
		t.Errorf("el argumento único se alteró: %q", lines[0])
	}
}

func TestLogVariasLineasSeAcumulan(t *testing.T) {
	h := newHarness(t)
	h.eval(`enu.log.info("una"); enu.log.warn("dos"); enu.log.error("tres")`)
	lines := h.logLines()
	if len(lines) != 3 {
		t.Fatalf("se esperaban 3 líneas, hay %d: %q", len(lines), lines)
	}
	wants := []string{"una", "dos", "tres"}
	for i, w := range wants {
		if !strings.HasSuffix(lines[i], w) {
			t.Errorf("línea %d: %q no termina en %q", i, lines[i], w)
		}
	}
}

func TestPrintEsAliasDeInfo(t *testing.T) {
	h := newHarness(t)
	// `print` (§15) escribe al log como info, no a la pantalla.
	h.eval(`print("desde print")`)
	lines := h.logLines()
	if len(lines) != 1 {
		t.Fatalf("se esperaba 1 línea, hay %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "INFO") || !strings.HasSuffix(lines[0], "desde print") {
		t.Errorf("print no escribió como info: %q", lines[0])
	}

	// Y es **la misma** función, no una copia: la identidad lo garantiza.
	h.expectEval(`return print == enu.log.info`, "true")
}

func TestLogFicheroPerezoso(t *testing.T) {
	// Sin loguear nada, el fichero (y su directorio) no deben existir: un
	// `enu -e` que no usa el log no ensucia el disco.
	dir := t.TempDir()
	sub := filepath.Join(dir, "datos")
	rt := New(WithDataDir(sub))
	defer rt.Close()

	logPath := filepath.Join(sub, logFileName)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("el fichero de log existe antes de loguear: stat err=%v", err)
	}

	if _, err := rt.EvalString(`enu.log.info("ahora sí")`); err != nil {
		t.Fatalf("log falló: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("el fichero de log no se creó tras loguear: %v", err)
	}
}

func TestLogPermisos0600(t *testing.T) {
	dir := t.TempDir()
	rt := New(WithDataDir(dir))
	defer rt.Close()
	if _, err := rt.EvalString(`enu.log.info("x")`); err != nil {
		t.Fatalf("log falló: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permisos del log: got %o, want 600", perm)
	}
}

// TestLogOwnerSigueElCampo comprueba que el plugin de origen anotado es el del
// contexto activo en el momento de loguear. S11 movió el owner a una pila
// (`ownerStack`) que el loader empuja durante el `init.lua` de cada plugin; aquí
// se simula ese contexto empujando un plugin a mano y se valida que la anotación
// lo lee, no que esté hardcodeada.
func TestLogOwnerSigueElCampo(t *testing.T) {
	h := newHarness(t)
	h.rt.ownerStack = append(h.rt.ownerStack, &pluginInfo{Name: "miplugin"})
	h.eval(`enu.log.info("hey")`)
	lines := h.logLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "[miplugin]") {
		t.Fatalf("la línea no anota el owner actual: %q", lines)
	}
}

// TestPadLevel cubre el alineado de la etiqueta de nivel (lógica de formato
// propia, table-driven).
func TestPadLevel(t *testing.T) {
	cases := map[logLevel]string{
		levelDebug: "DEBUG",
		levelInfo:  "INFO ",
		levelWarn:  "WARN ",
		levelError: "ERROR",
	}
	for level, want := range cases {
		if got := padLevel(level); got != want {
			t.Errorf("padLevel(%q): got %q, want %q", level, got, want)
		}
	}
}
