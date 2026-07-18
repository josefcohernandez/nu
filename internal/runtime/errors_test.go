package runtime

import (
	"strings"
	"testing"
)

// Tests unitarios del puente de errores estructurados (S02, inventario 🔒).
// Blindan dos invariantes de api.md §1.4:
//   - la **forma** de la tabla es exactamente `{code, message, detail?}`;
//   - un código reservado **nunca se traga ni se reescribe** al cruzar el puente
//     Go→Lua→Go (ni se degrada a texto, ni se remapea el `code`).

// registerFail instala en el arnés una primitiva Go de andamiaje `fail` que
// lanza un error estructurado con el `code`/`message`/`detail` que reciba. Es la
// forma de "forzar un EINVAL" (criterio de hecho de S02) sin que el runtime de
// producción tenga aún ninguna primitiva que falle.
func registerFail(h *harness) {
	// Andamiaje expresado en Lua sobre wasm: lanza la MISMA tabla estructurada
	// {code, message, detail?}. `detail` sólo aparece si se pasó (nil si no). El
	// error estructurado es un valor Lua puro, no necesita una primitiva Go.
	h.defWasmGlobal(`function fail(code, msg, detail)
  error({ code = code, message = msg, detail = detail })
end`)
}

// allReservedCodes es el orden estable de los códigos reservados para las tablas
// de casos. Si §1.4 crece, esta lista crece con él (y reservedCodes también).
var allReservedCodes = []string{
	CodeENOENT, CodeEEXIST, CodeEACCES, CodeEIO, CodeEHTTP, CodeENET,
	CodeETIMEOUT, CodeECANCELED, CodeEBUDGET, CodeEINVAL, CodeECLOSED,
}

// TestErrorTableShape: la tabla capturada por `pcall` tiene la forma del
// contrato. `detail` aparece solo si se aportó (distingue "sin detalle" de
// "detalle = nil").
func TestErrorTableShape(t *testing.T) {
	h := newHarness(t)
	registerFail(h)

	// Con detalle: code/message/detail presentes y del tipo correcto.
	h.eval(`
		local ok, err = pcall(function() fail("EINVAL", "ruta vacía", { arg = "path" }) end)
		assert(ok == false, "fail debió lanzar")
		assert(type(err) == "table", "el error debe ser una tabla, no " .. type(err))
		assert(err.code == "EINVAL", "code inesperado: " .. tostring(err.code))
		assert(err.message == "ruta vacía", "message inesperado: " .. tostring(err.message))
		assert(type(err.detail) == "table", "detail debe conservarse")
		assert(err.detail.arg == "path", "detail.arg inesperado")
		return true
	`)

	// Sin detalle: la clave `detail` no existe (es nil), no una tabla vacía.
	h.eval(`
		local ok, err = pcall(function() fail("EIO", "disco lleno") end)
		assert(ok == false)
		assert(err.code == "EIO")
		assert(err.message == "disco lleno")
		assert(err.detail == nil, "detail debe ser nil cuando no se aporta")
		return true
	`)
}

// TestReservedCodesNotSwallowedNorRewritten: el corazón del 🔒 de S02. Para cada
// código reservado, lanzado desde Go: (a) Lua lo captura como tabla con el code
// **literal** intacto; (b) si no se captura, el puente Eval+ EvalString lo
// devuelve como *StructuredError con el mismo code (no como texto, no remapeado).
func TestReservedCodesNotSwallowedNorRewritten(t *testing.T) {
	for _, code := range allReservedCodes {
		code := code
		t.Run(code, func(t *testing.T) {
			// (a) capturado en Lua: forma y code exactos.
			h := newHarness(t)
			registerFail(h)
			h.eval(`
				local code = "` + code + `"
				local ok, err = pcall(function() fail(code, "msg de " .. code) end)
				assert(ok == false, "debió lanzar " .. code)
				assert(type(err) == "table", code .. ": el error se degradó a " .. type(err))
				assert(err.code == code, "code reescrito: " .. tostring(err.code) .. " != " .. code)
				assert(err.message == "msg de " .. code, "message alterado")
				return true
			`)

			// (b) sin capturar: cruza el puente como *StructuredError intacto.
			se := h.evalErr(`fail("` + code + `", "no capturado")`)
			if se.Code != code {
				t.Fatalf("code reescrito al cruzar el puente: got %q, want %q", se.Code, code)
			}
			if se.Message != "no capturado" {
				t.Fatalf("message alterado al cruzar el puente: %q", se.Message)
			}
		})
	}
}

// TestExtensionCodePassesThrough: el puente no es exclusivo de los reservados.
// Una extensión acuña su propio código (§1.4) y debe cruzar igual de intacto:
// la regla "no reescribir" vale para cualquier code, no solo los del core.
func TestExtensionCodePassesThrough(t *testing.T) {
	h := newHarness(t)
	registerFail(h)

	se := h.evalErr(`fail("EPROVIDER", "rate limit")`)
	if se.Code != "EPROVIDER" {
		t.Fatalf("code de extensión reescrito: got %q, want %q", se.Code, "EPROVIDER")
	}
	if IsReservedCode("EPROVIDER") {
		t.Fatalf("EPROVIDER no debe figurar como reservado del core")
	}
}

// TestLuaErrorStringIsNotStructured: un `error("texto")` de Lua —o cualquier
// error sin la forma del contrato— NO se hace pasar por estructurado. El puente
// solo reconoce tablas con `code` string; lo demás se devuelve tal cual, sin
// inventar un code.
func TestLuaErrorStringIsNotStructured(t *testing.T) {
	h := newHarness(t)

	// Un error de string: EvalString lo devuelve, pero no como *StructuredError.
	_, err := h.rt.EvalString(`error("explosión")`)
	if err == nil {
		t.Fatal("se esperaba un error")
	}
	if _, ok := err.(*StructuredError); ok {
		t.Fatalf("un error de string no debe verse como estructurado: %v", err)
	}

	// Una tabla sin `code` tampoco es estructurada (forma incompleta).
	_, err = h.rt.EvalString(`error({ message = "sin code" })`)
	if err == nil {
		t.Fatal("se esperaba un error")
	}
	if _, ok := err.(*StructuredError); ok {
		t.Fatalf("una tabla sin code no debe verse como estructurada: %v", err)
	}
}

// TestA40ChunkStructuredErrorSurvives (A-40): un chunk de `EvalString` (estado
// PRINCIPAL, no task) que lanza un error ESTRUCTURADO `{code, message}` llega a Go
// como *StructuredError con el code y el message FIELES. Es la contraparte de
// `EvalTaskString` para el camino de chunk: el protocolo de separadores 0x01
// transporta la tabla de error, no su texto rendido. Antes el code se reconstruía
// parseando "CODE: mensaje"; ahora cruza por su tabla.
func TestA40ChunkStructuredErrorSurvives(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`error({ code = "ENOENT", message = "no encontré el fichero" })`)
	if se.Code != "ENOENT" {
		t.Fatalf("code: got %q, want ENOENT", se.Code)
	}
	if se.Message != "no encontré el fichero" {
		t.Fatalf("message: got %q, want %q", se.Message, "no encontré el fichero")
	}
	// Un mensaje que a su vez contiene ": " no confunde el transporte (el 0x01 delimita,
	// no el ": "): message íntegro.
	se = h.evalErr(`error({ code = "EINVAL", message = "clave: valor: raro" })`)
	if se.Code != "EINVAL" || se.Message != "clave: valor: raro" {
		t.Fatalf("code/message alterados: code=%q message=%q", se.Code, se.Message)
	}
}

// TestA40UserStringNotReclassified (A-40): un `error("ENOENT: ...")` de código de
// USUARIO —un string cuyo prefijo coincide con un code reservado— NO se reclasifica
// como error estructurado del core. El apaño anterior parseaba "CODE: mensaje" del
// texto rendido (`strings.Cut` + `IsReservedCode`), y un `error("ENOENT: x", 0)`
// (nivel 0, sin prefijo de posición) cruzaba como *StructuredError{ENOENT}; los
// llamantes decidían códigos de salida por ese `code` inventado. Se cubre el nivel 0
// (el caso que disparaba el falso positivo) y el nivel por defecto.
func TestA40UserStringNotReclassified(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		code       string
		wantSubstr string
	}{
		{`error("ENOENT: no encontré X", 0)`, "ENOENT: no encontré X"},
		{`error("EINVAL: dato raro")`, "EINVAL: dato raro"},
	}
	for _, c := range cases {
		_, err := h.rt.EvalString(c.code)
		if err == nil {
			t.Fatalf("%s: esperaba un error", c.code)
		}
		if se, ok := err.(*StructuredError); ok {
			t.Fatalf("%s: string de usuario reclasificado como estructurado code=%q (falso positivo A-40)", c.code, se.Code)
		}
		if !strings.Contains(err.Error(), c.wantSubstr) {
			t.Fatalf("%s: el texto del usuario se perdió: %q", c.code, err.Error())
		}
	}
}

// TestA40NonStructuredErrorsReadable (A-40): los errores NO estructurados normales
// —un `error("string")` cualquiera y un fallo de SINTAXIS— siguen llegando como error
// simple y legible (nunca *StructuredError). El transporte por 0x01 no debe tragarse
// ni disfrazar el texto.
func TestA40NonStructuredErrorsReadable(t *testing.T) {
	h := newHarness(t)

	_, err := h.rt.EvalString(`error("algo explotó")`)
	if err == nil {
		t.Fatal("esperaba un error")
	}
	if _, ok := err.(*StructuredError); ok {
		t.Fatalf("un error de string no debe verse como estructurado: %v", err)
	}
	if !strings.Contains(err.Error(), "algo explotó") {
		t.Fatalf("mensaje ilegible: %q", err.Error())
	}

	// Error de sintaxis en el propio chunk: sólo hay texto, nunca estructurado.
	_, err = h.rt.EvalString(`return 1 +`)
	if err == nil {
		t.Fatal("esperaba un error de sintaxis")
	}
	if _, ok := err.(*StructuredError); ok {
		t.Fatalf("un error de sintaxis no debe verse como estructurado: %v", err)
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("el error de sintaxis llegó sin texto")
	}
}

// TestIsReservedCode: la lista reservada coincide con §1.4, ni de más ni de
// menos. Protege contra que alguien añada un code al core sin declararlo (o
// declare uno que la espec no reserva).
func TestIsReservedCode(t *testing.T) {
	for _, code := range allReservedCodes {
		if !IsReservedCode(code) {
			t.Errorf("%s debería ser reservado", code)
		}
	}
	if len(reservedCodes) != len(allReservedCodes) {
		t.Errorf("reservedCodes tiene %d entradas, la espec §1.4 lista %d",
			len(reservedCodes), len(allReservedCodes))
	}
	for _, code := range []string{"EPROVIDER", "", "einval", "FOO"} {
		if IsReservedCode(code) {
			t.Errorf("%q no debería ser reservado", code)
		}
	}
}
