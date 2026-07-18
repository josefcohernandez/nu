package runtime

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Arnés de tests del runtime (S02): corre snippets Lua contra un Runtime real y
// hace asserts. Es **reutilizable por todas las sesiones siguientes** —cada
// submódulo nuevo de la API se prueba desde el lado del autor de extensiones,
// escribiendo el Lua que lo usaría y comprobando el resultado o el error
// estructurado que lanza (Definition of Done §2 del plan).
//
// Vive en un fichero `_test.go` a propósito: es andamiaje de pruebas, no
// superficie del binario. Como las pruebas de las demás sesiones comparten el
// paquete `runtime`, todas lo tienen a mano sin exportarlo.

// harness envuelve un Runtime para una prueba concreta. El estado se cierra solo
// al acabar la prueba (vía t.Cleanup), así que cada test arranca de un runtime
// limpio sin fugas entre casos.
type harness struct {
	t  *testing.T
	rt *Runtime
}

// newHarness construye un runtime sandboxeado y listo para snippets, con cierre
// automático al terminar la prueba.
//
// **`WithForceUI(true)` (gating G20, S32):** los tests corren headless (sin TTY), así
// que sin forzarlo `enu.ui` no existiría (el gating real lo decide la detección de
// TTY). Como muchas pruebas de S22–S31 ejercitan `enu.ui` (block/region/input) en este
// entorno headless, el arnés FUERZA la activación de la UI: así el gating real (por
// TTY) sigue aplicando al binario `enu` y la suite no se rompe. Una prueba que quiera
// observar el comportamiento HEADLESS (que `enu.ui` no exista) construye su runtime con
// `WithForceUI(false)` a mano (ver `gating_test.go`).
func newHarness(t *testing.T) *harness {
	t.Helper()
	// data_dir y config_dir temporales: `enu.log` escribe en disco y el arranque LEE
	// `enu.toml` de config_dir; ninguno debe tocar el directorio real del usuario (que,
	// con el conjunto de producto activado, arrancaría el chat y demás). El TempDir se
	// borra al acabar la prueba. (Hermeticidad del config — necesaria desde G35, en que
	// un chat sin modelo arranca una UI degradada que rompería tests ajenos.)
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(t.TempDir()), WithForceUI(true))
	t.Cleanup(rt.Close)
	return &harness{t: t, rt: rt}
}

// newHarnessBudget es como newHarness pero fija un **presupuesto de slice**
// pequeño para el watchdog (S09), de modo que un bucle de CPU puro se corte
// rápido en los tests. Un `budget <= 0` desactiva el watchdog (lo usan los tests
// que comprueban que el trabajo normal nunca se aborta sin necesidad de esperar).
// Fuerza la UI (G20, como `newHarness`).
func newHarnessBudget(t *testing.T, budget time.Duration) *harness {
	t.Helper()
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(t.TempDir()), WithSliceBudget(budget), WithForceUI(true))
	t.Cleanup(rt.Close)
	return &harness{t: t, rt: rt}
}

// logLines devuelve las líneas escritas hasta ahora en el fichero de `enu.log`
// del runtime bajo prueba. Devuelve vacío si nada se ha logueado (el fichero se
// crea perezosamente en la primera escritura). Es la vía por la que una prueba
// comprueba lo que un snippet logueó.
func (h *harness) logLines() []string {
	h.t.Helper()
	data, err := os.ReadFile(h.rt.log.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatalf("no se pudo leer el log: %v", err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// withToken corre `fn` con el token tomado: la vía de los tests que leen el
// compositor (composeRow) serializados con el pintor (paintLocked, ui.go).
func withToken(rt *Runtime, fn func()) {
	rt.sched.acquire()
	defer rt.sched.release()
	fn()
}

// defWasmGlobal define un global Lua evaluando `code` en el ESTADO PRINCIPAL del
// backend wasm. Es la vía por la que un test inyecta una primitiva de andamiaje
// EXPRESABLE EN LUA (p. ej. un echo que suspende con enu.task.sleep). Falla la prueba
// si el snippet de definición no evalúa limpio.
func (h *harness) defWasmGlobal(code string) {
	h.t.Helper()
	if _, luaErr, goErr := h.rt.wasm.Eval(code); goErr != nil {
		h.t.Fatalf("defWasmGlobal: fallo del motor wasm: %v\n%s", goErr, code)
	} else if luaErr != "" {
		h.t.Fatalf("defWasmGlobal: error Lua: %s\n%s", luaErr, code)
	}
}

// regStringFn instala una "constante" Lua `name()` que devuelve el string `value`,
// inyectando el valor sin interpolar (SetStringGlobal) y definiendo el accesor sobre
// el estado wasm. Es el idioma de withURL/BASE generalizado, para tests que pasan
// varios blobs de texto (fuentes, markdown) a los snippets.
func (h *harness) regStringFn(name, value string) {
	h.t.Helper()
	valName := "__" + name + "_val"
	h.rt.SetStringGlobal(valName, value)
	h.defWasmGlobal("function " + name + "() return " + valName + " end")
}

// eval corre `code` exigiendo que termine sin error y devuelve sus valores de
// retorno como strings. Falla la prueba si el snippet lanza: es el camino para
// snippets que se autovalidan con `assert(...)` y devuelven, p. ej., `true`.
func (h *harness) eval(code string) []string {
	h.t.Helper()
	res, err := h.rt.EvalString(code)
	if err != nil {
		h.t.Fatalf("el snippet falló inesperadamente: %v\n--- código ---\n%s", err, code)
	}
	return res
}

// evalErr corre `code` exigiendo que lance un **error estructurado** del core
// (§1.4) y lo devuelve para que la prueba haga asserts sobre `code`/`message`/
// `detail`. Falla si el snippet no lanza, o si lanza algo que no es estructurado
// (un `error("string")` o un fallo de sintaxis): justo lo que blinda que el
// puente no degrada un error estructurado a texto plano.
func (h *harness) evalErr(code string) *StructuredError {
	h.t.Helper()
	_, err := h.rt.EvalString(code)
	if err == nil {
		h.t.Fatalf("se esperaba un error estructurado, pero el snippet terminó bien\n--- código ---\n%s", code)
	}
	se, ok := err.(*StructuredError)
	if !ok {
		h.t.Fatalf("se esperaba *StructuredError, llegó %T: %v\n--- código ---\n%s", err, err, code)
	}
	return se
}

// expectEval corre `code` y comprueba que sus valores de retorno (como strings)
// son exactamente `want`. Azúcar para el caso más común de "este snippet debe
// devolver esto".
func (h *harness) expectEval(code string, want ...string) {
	h.t.Helper()
	got := h.eval(code)
	if len(got) != len(want) {
		h.t.Fatalf("nº de resultados: got %d %q, want %d %q\n--- código ---\n%s",
			len(got), got, len(want), want, code)
	}
	for i := range want {
		if got[i] != want[i] {
			h.t.Fatalf("resultado %d: got %q, want %q\n--- código ---\n%s", i, got[i], want[i], code)
		}
	}
}

// sanity: el arnés es operativo cuando newHarness, eval, evalErr existen.
// Una prueba de humo mínima del propio arnés (que sepa correr Lua y leer un
// retorno) vive aquí para que un fallo del andamiaje se distinga de un fallo de
// la feature bajo prueba.
func TestHarnessSmoke(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`return 1 + 1`, "2")
	if got := h.eval(`return enu.version.api`); len(got) != 1 || strings.TrimSpace(got[0]) == "" {
		t.Fatalf("enu.version.api inesperado: %q", got)
	}
}
