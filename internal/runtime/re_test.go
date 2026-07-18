package runtime

import (
	"testing"
)

// Tests de `enu.re` (api.md §10, sesión S26). La lógica propia a blindar (no es
// 🔒 pero la forma de capturas/rangos es decisión nuestra): la **forma de las
// capturas** de `match` (array 1-based + grupos con nombre), las **unidades de
// los rangos** de `find_all` (offsets de byte 1-based estilo `string.find`,
// reconstruibles con `s:sub`), la **sintaxis de `repl`** de `replace`, y que
// RE2 **rechaza backreferences** con un `EINVAL` claro.
//
// Se prueban desde el lado del autor de extensiones (snippets Lua sobre el
// arnés) porque toda la lógica de S26 vive en la frontera Lua↔Go: la forma de
// la tabla de capturas y de los rangos solo es observable desde Lua.

// --- compile + match con grupos -----------------------------------------------

// TestReMatchPositional: un patrón con grupos posicionales sobre "12-34"
// devuelve caps con [1] = match completo, [2]/[3] = los grupos. Es el caso
// central de la forma de capturas (array 1-based, [1] siempre es el match
// entero). Nota: en los snippets la sintaxis es RE2 (`\d`), no la de patrones
// de Lua (`%d`); la barra se escapa doble dentro de la cadena Lua.
func TestReMatchPositional(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("(\\d+)-(\\d+)")
		local caps = re:match("12-34")
		assert(caps ~= nil, "debería casar")
		assert(caps[1] == "12-34", "caps[1] match completo, got "..tostring(caps[1]))
		assert(caps[2] == "12", "caps[2] primer grupo, got "..tostring(caps[2]))
		assert(caps[3] == "34", "caps[3] segundo grupo, got "..tostring(caps[3]))
		return "ok"
	`, "ok")
}

// TestReMatchNamed: grupos con nombre (?P<name>...) aparecen ADEMÁS por su
// clave string, conviviendo con la parte array.
func TestReMatchNamed(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("(?P<year>\\d{4})-(?P<month>\\d{2})")
		local caps = re:match("2026-06")
		assert(caps[1] == "2026-06", "match completo")
		assert(caps[2] == "2026", "grupo 1 posicional")
		assert(caps[3] == "06", "grupo 2 posicional")
		assert(caps.year == "2026", "grupo por nombre year, got "..tostring(caps.year))
		assert(caps.month == "06", "grupo por nombre month, got "..tostring(caps.month))
		return "ok"
	`, "ok")
}

// TestReMatchNoGroups: un patrón sin grupos da solo el match completo en [1].
func TestReMatchNoGroups(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("\\w+")
		local caps = re:match("hola mundo")
		assert(caps[1] == "hola", "match completo, got "..tostring(caps[1]))
		assert(caps[2] == nil, "no hay grupo 2")
		return "ok"
	`, "ok")
}

// TestReMatchNoMatch: sin coincidencia, match devuelve nil (no lanza).
func TestReMatchNoMatch(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("\\d+")
		local caps = re:match("sin numeros aqui")
		assert(caps == nil, "sin match debe ser nil")
		return "ok"
	`, "ok")
}

// TestReMatchEmptyString: casar sobre string vacío no rompe.
func TestReMatchEmptyString(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("\\d+")
		assert(re:match("") == nil, "patrón que exige dígitos no casa el vacío")
		local re2 = enu.re.compile("\\d*")
		local caps = re2:match("")
		assert(caps ~= nil and caps[1] == "", "\\d* casa el vacío con match vacío")
		return "ok"
	`, "ok")
}

// TestReMatchOptionalGroup: un grupo opcional ausente se entrega como "".
func TestReMatchOptionalGroup(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("(a)?b")
		local caps = re:match("b")
		assert(caps[1] == "b", "match completo")
		assert(caps[2] == "", "grupo opcional ausente -> string vacío, got "..tostring(caps[2]))
		return "ok"
	`, "ok")
}

// --- find_all rangos ----------------------------------------------------------

// TestReFindAllRanges: varias coincidencias dan un array de {start,end}; el
// invariante clave es que s:sub(start,end) reconstruye CADA match (byte-offsets
// 1-based, ambos inclusive, estilo string.find).
func TestReFindAllRanges(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local s = "a1 b22 c333"
		local re = enu.re.compile("\\d+")
		local ranges = re:find_all(s)
		assert(#ranges == 3, "3 coincidencias, got "..#ranges)
		-- reconstrucción exacta por s:sub
		assert(s:sub(ranges[1][1], ranges[1][2]) == "1", "match 1")
		assert(s:sub(ranges[2][1], ranges[2][2]) == "22", "match 2")
		assert(s:sub(ranges[3][1], ranges[3][2]) == "333", "match 3")
		-- offsets concretos 1-based: "1" en pos 2, "22" en 5-6, "333" en 9-11
		assert(ranges[1][1] == 2 and ranges[1][2] == 2, "rango 1 = 2,2")
		assert(ranges[2][1] == 5 and ranges[2][2] == 6, "rango 2 = 5,6")
		assert(ranges[3][1] == 9 and ranges[3][2] == 11, "rango 3 = 9,11")
		return "ok"
	`, "ok")
}

// TestReFindAllNone: sin coincidencias, tabla vacía.
func TestReFindAllNone(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("\\d+")
		local ranges = re:find_all("sin nada")
		assert(#ranges == 0, "tabla vacía")
		return "ok"
	`, "ok")
}

// TestReFindAllUTF8: con texto multibyte, los offsets son de BYTE (no de rune),
// y s:sub (que indexa por byte en Lua) sigue reconstruyendo el match. Es la
// prueba de que la unidad documentada (byte) es coherente con string.sub.
func TestReFindAllUTF8(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		-- "áéí" ocupa 6 bytes (2 cada uno); el dígito viene después.
		local s = "áéí7"
		local re = enu.re.compile("\\d")
		local ranges = re:find_all(s)
		assert(#ranges == 1, "una coincidencia")
		-- "7" está en el byte 7 (tras 6 bytes de las vocales)
		assert(ranges[1][1] == 7 and ranges[1][2] == 7, "byte-offset del dígito, got "..ranges[1][1]..","..ranges[1][2])
		assert(s:sub(ranges[1][1], ranges[1][2]) == "7", "reconstrucción por byte")
		return "ok"
	`, "ok")
}

// TestReFindAllEmptyMatch: una coincidencia vacía (longitud cero) da un rango
// con end == start-1, de modo que s:sub devuelve "" (convenio de Lua).
func TestReFindAllEmptyMatch(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local s = "ab"
		local re = enu.re.compile("x*")  -- casa el vacío en cada posición
		local ranges = re:find_all(s)
		assert(#ranges >= 1, "al menos una coincidencia vacía")
		-- el primer match vacío está al inicio: start=1, end=0
		assert(ranges[1][1] == 1 and ranges[1][2] == 0, "match vacío start=1 end=0, got "..ranges[1][1]..","..ranges[1][2])
		assert(s:sub(ranges[1][1], ranges[1][2]) == "", "s:sub de un match vacío es ''")
		return "ok"
	`, "ok")
}

// --- replace ------------------------------------------------------------------

// TestReReplaceNumbered: $1/$2 reemplazan por número, en TODAS las ocurrencias.
func TestReReplaceNumbered(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("(\\d+)-(\\d+)")
		local out = re:replace("12-34 y 56-78", "$2/$1")
		assert(out == "34/12 y 78/56", "swap de grupos en ambas, got "..out)
		return "ok"
	`, "ok")
}

// TestReReplaceNamed: ${name} reemplaza por grupo con nombre.
func TestReReplaceNamed(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("(?P<y>\\d{4})-(?P<m>\\d{2})")
		local out = re:replace("2026-06", "${m}/${y}")
		assert(out == "06/2026", "reorden por nombre, got "..out)
		return "ok"
	`, "ok")
}

// TestReReplaceNoMatch: sin coincidencias, el string vuelve intacto.
func TestReReplaceNoMatch(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("\\d+")
		assert(re:replace("sin numeros", "X") == "sin numeros", "intacto")
		return "ok"
	`, "ok")
}

// TestReReplaceAll: una sustitución simple toca todas las ocurrencias.
func TestReReplaceAll(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local re = enu.re.compile("a")
		assert(re:replace("banana", "o") == "bonono", "todas las 'a'")
		return "ok"
	`, "ok")
}

// --- RE2 sin backreferences + patrones inválidos ------------------------------

// TestReCompileBackreference: una backreference (\1) NO la soporta RE2 →
// EINVAL con mensaje claro (el de regexp.Compile). Es el criterio de hecho.
func TestReCompileBackreference(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`return enu.re.compile("(a)\\1")`)
	if se.Code != CodeEINVAL {
		t.Fatalf("backreference debería dar EINVAL, got %s", se.Code)
	}
	if se.Message == "" {
		t.Fatalf("el EINVAL debería traer un mensaje accionable")
	}
}

// TestReCompileInvalidSyntax: un patrón sintácticamente inválido → EINVAL.
func TestReCompileInvalidSyntax(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`return enu.re.compile("(abc")`) // paréntesis sin cerrar
	if se.Code != CodeEINVAL {
		t.Fatalf("sintaxis inválida debería dar EINVAL, got %s", se.Code)
	}
}

// --- uso desde una task -------------------------------------------------------

// TestReFromTask: enu.re funciona dentro de una task (es [W], no ⏸: no necesita
// task, pero tampoco debe romper en una).
func TestReFromTask(t *testing.T) {
	h := newHarness(t)
	// La task coordinadora escribe su resultado en un global, que se comprueba
	// en el estado principal TRAS waitIdle (un assert dentro de la task quedaría
	// aislado por ADR-008 y un fallo pasaría desapercibido; el global lo expone).
	h.eval(`
		RE_TASK_V, RE_TASK_N = nil, nil
		local t1 = enu.task.spawn(function()
			local re = enu.re.compile("\\d+")
			return re:match("abc 42 def")[1]
		end)
		-- await SÍ es válido dentro de una task; ejercita re en dos tasks distintas.
		enu.task.spawn(function()
			RE_TASK_V = t1:await()
			RE_TASK_N = #enu.re.compile("\\w+"):find_all("hi there")
		end)
		return "ok"
	`)
	h.expectEval(`return tostring(RE_TASK_V), tostring(RE_TASK_N)`, "42", "2")
}

// --- usos malos de la firma ---------------------------------------------------

// TestReTypeMismatch: llamar un método de Re sobre algo que no es un Re → EINVAL.
func TestReTypeMismatch(t *testing.T) {
	h := newHarness(t)
	// match sobre un userdata equivocado (un Block) → EINVAL del checkRe.
	se := h.evalErr(`
		local b = enu.ui.block({"hola"})
		local re = enu.re.compile("\\d")
		return re.match(b, "1")
	`)
	if se.Code != CodeEINVAL {
		t.Fatalf("self no-Re debería dar EINVAL, got %s", se.Code)
	}
}
