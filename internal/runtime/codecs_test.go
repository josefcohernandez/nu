package runtime

import (
	"strings"
	"testing"
)

// Tests de los codecs `nu.json`/`nu.toml`/`nu.yaml` (api.md §12, sesión S18, 🔒).
// La lógica clave a blindar (inventario 🔒): JSON **UTF-8 estricto → EINVAL
// (G11)** y el **sentinel NULL ida y vuelta sin perder claves**. Además: el
// mapeo Lua↔Go (array vs objeto, tabla vacía), `pretty`, `toml.decode` de un
// `plugin.toml` real y el round-trip de un frontmatter de skill en YAML.
//
// Los codecs NO son ⏸ (CPU puro): se invocan directamente desde el chunk, sin
// `nu.task.spawn`; uno de los snippets los ejercita además desde dentro de una
// task para confirmar que ahí también funcionan (son [W], §16).

// TestJSONUTF8Estricto blinda G11: `json.encode` de un string con bytes UTF-8
// inválidos lanza `EINVAL` (NO los reemplaza en silencio por U+FFFD, como haría
// `encoding/json` por defecto). Un string válido —incluido multibyte y un emoji—
// encodea bien. Es la mitad del invariante 🔒 de S18.
func TestJSONUTF8Estricto(t *testing.T) {
	h := newHarness(t)

	// Byte 0xff suelto: secuencia UTF-8 inválida. `string.char(255)` lo construye
	// sin que el parser de Lua lo rechace (es un byte en un string Lua, que es
	// una secuencia de bytes cualquiera).
	se := h.evalErr(`return nu.json.encode(string.char(0xff))`)
	if se.Code != CodeEINVAL {
		t.Fatalf("G11: encode de bytes inválidos debe lanzar EINVAL, llegó %q (%s)", se.Code, se.Message)
	}

	// Inválido anidado dentro de una tabla: también se detecta (la validación es
	// recursiva por `luaToGo`).
	se = h.evalErr(`return nu.json.encode({ a = "ok", b = string.char(0x80, 0x80) })`)
	if se.Code != CodeEINVAL {
		t.Fatalf("G11: encode de bytes inválidos anidados debe lanzar EINVAL, llegó %q", se.Code)
	}

	// Inválido como CLAVE de objeto: igual de estricto.
	se = h.evalErr(`return nu.json.encode({ [string.char(0xfe)] = 1 })`)
	if se.Code != CodeEINVAL {
		t.Fatalf("G11: encode de clave con bytes inválidos debe lanzar EINVAL, llegó %q", se.Code)
	}

	// Válido (ASCII, multibyte y emoji): encodea sin problema y round-trip exacto.
	h.expectEval(`
		local s = "café 漢字 🚀"
		local enc = nu.json.encode(s)
		return tostring(nu.json.decode(enc) == s)
	`, "true")
}

// TestJSONSentinelNull blinda la otra mitad del 🔒 de S18: el **sentinel NULL
// ida y vuelta**. `decode('{"a":null,"b":1}')` deja la clave `a` como
// `nu.json.NULL` (PRESENTE, no perdida) y `b == 1`; `encode` de una tabla con
// `a = nu.json.NULL` produce `"a":null`. El round-trip no pierde claves —que es
// justo lo que se perdería si `null` se mapeara a `nil` (la clave desaparecería
// de la tabla Lua)—.
func TestJSONSentinelNull(t *testing.T) {
	h := newHarness(t)

	// decode: la clave con null sobrevive como sentinel, distinto de nil.
	h.expectEval(`
		local t = nu.json.decode('{"a":null,"b":1}')
		assert(t.a == nu.json.NULL, "a debe ser el sentinel NULL, no nil")
		assert(t.a ~= nil, "a NO debe ser nil (se perdería la clave)")
		assert(t.b == 1, "b debe valer 1")
		-- la clave 'a' existe de verdad en la tabla (no es solo un nil disfrazado):
		local visto_a = false
		for k, _ in pairs(t) do if k == "a" then visto_a = true end end
		assert(visto_a, "la clave 'a' debe estar PRESENTE al iterar la tabla")
		return "ok"
	`, "ok")

	// encode: el sentinel produce null.
	h.expectEval(`
		return nu.json.encode({ a = nu.json.NULL, b = 1 })
	`, `{"a":null,"b":1}`)

	// Round-trip completo: decode → encode reproduce el null sin perder la clave.
	h.expectEval(`
		local orig = '{"a":null,"b":1}'
		local back = nu.json.encode(nu.json.decode(orig))
		return back
	`, `{"a":null,"b":1}`)

	// Contraste explícito: una tabla con un valor `nil` PIERDE la clave (es lo que
	// el sentinel evita). Aquí lo demostramos para documentar el porqué.
	h.expectEval(`
		return nu.json.encode({ a = nil, b = 1 })
	`, `{"b":1}`)
}

// TestJSONArrayVsObjeto blinda el mapeo Lua↔Go: una tabla con claves 1..n
// contiguas → array JSON; cualquier otra → objeto; una tabla vacía → objeto
// (`{}`), la decisión documentada (claude_decisions.md S18).
func TestJSONArrayVsObjeto(t *testing.T) {
	h := newHarness(t)

	// Secuencia 1..n → array.
	h.expectEval(`return nu.json.encode({10, 20, 30})`, `[10,20,30]`)

	// Mapa con claves string → objeto (orden de claves estable: alfabético).
	h.expectEval(`return nu.json.encode({nombre = "ana", edad = 30})`, `{"edad":30,"nombre":"ana"}`)

	// Tabla vacía → objeto {} (decisión documentada).
	h.expectEval(`return nu.json.encode({})`, `{}`)

	// Mezcla (1 contiguo + clave string) → objeto, no array (no es secuencia pura).
	h.expectEval(`return nu.json.encode({ [1] = "x", clave = "y" })`, `{"1":"x","clave":"y"}`)

	// decode de un array → secuencia Lua 1..n.
	h.expectEval(`
		local a = nu.json.decode('[7,8,9]')
		assert(#a == 3 and a[1] == 7 and a[3] == 9, "array decodificado")
		return "ok"
	`, "ok")

	// Anidamiento: array de objetos.
	h.expectEval(`return nu.json.encode({ {n=1}, {n=2} })`, `[{"n":1},{"n":2}]`)
}

// TestJSONPretty comprueba que `opts.pretty` produce un JSON indentado y válido
// (round-trip estable: decode del pretty da lo mismo que del compacto).
func TestJSONPretty(t *testing.T) {
	h := newHarness(t)

	got := h.eval(`return nu.json.encode({ a = 1, b = {2, 3} }, { pretty = true })`)
	pretty := got[0]
	if !strings.Contains(pretty, "\n") || !strings.Contains(pretty, "  ") {
		t.Fatalf("pretty debe indentar con saltos y espacios, llegó:\n%s", pretty)
	}
	// El pretty es JSON válido: decodificarlo y re-encodearlo compacto da lo
	// canónico.
	h.expectEval(`
		local p = nu.json.encode({ a = 1, b = {2, 3} }, { pretty = true })
		return nu.json.encode(nu.json.decode(p))
	`, `{"a":1,"b":[2,3]}`)
}

// TestJSONDecodeInvalido: JSON mal formado → `EINVAL` accionable (no se traga el
// error ni se devuelve un valor a medias).
func TestJSONDecodeInvalido(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`return nu.json.decode('{ roto: ')`)
	if se.Code != CodeEINVAL {
		t.Fatalf("decode de JSON inválido debe lanzar EINVAL, llegó %q", se.Code)
	}
}

// TestTOMLDecodePluginToml blinda el criterio de hecho de S18: `toml.decode` lee
// un `plugin.toml` real (name/version/requires). Es el manifiesto que el loader
// (S11) parsea internamente; aquí se demuestra que un plugin puede leerlo desde
// Lua con la misma forma.
func TestTOMLDecodePluginToml(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local manifiesto = [[
name = "mi-plugin"
version = "1.2.0"
requires = ["base", "ui"]
]]
		local t = nu.toml.decode(manifiesto)
		assert(t.name == "mi-plugin", "name")
		assert(t.version == "1.2.0", "version")
		assert(#t.requires == 2, "requires es una lista de 2")
		assert(t.requires[1] == "base" and t.requires[2] == "ui", "requires contenido")
		return "ok"
	`, "ok")

	// Round-trip: encode de una tabla-manifiesto vuelve a decodificar igual.
	h.expectEval(`
		local orig = { name = "x", version = "0.1.0", requires = {"a"} }
		local s = nu.toml.encode(orig)
		local back = nu.toml.decode(s)
		assert(back.name == "x" and back.version == "0.1.0", "round-trip escalares")
		assert(back.requires[1] == "a", "round-trip lista")
		return "ok"
	`, "ok")

	// TOML inválido → EINVAL.
	se := h.evalErr(`return nu.toml.decode("clave = ")`)
	if se.Code != CodeEINVAL {
		t.Fatalf("toml.decode inválido debe lanzar EINVAL, llegó %q", se.Code)
	}

	// La raíz de un documento TOML debe ser un objeto: un array/escalar → EINVAL.
	se = h.evalErr(`return nu.toml.encode({1, 2, 3})`)
	if se.Code != CodeEINVAL {
		t.Fatalf("toml.encode de raíz no-objeto debe lanzar EINVAL, llegó %q", se.Code)
	}
}

// TestTOMLDecodeArrayDeTablas blinda que `nu.toml.decode` convierte un
// **array-de-tablas** (`[[...]]`) a una lista Lua de tablas, no a un string.
// BurntSushi/toml entrega ese caso como el tipo concreto `[]map[string]interface{}`
// (no el `[]interface{}` "abierto"), que el puente `goToLua` ignoraba y *estringaba*
// (`"[map[id:big-1]]"`). Es el formato CENTRAL de `providers.toml` (providers.md
// §1, S36): una regresión aquí rompe el registro de modelos por completo.
func TestTOMLDecodeArrayDeTablas(t *testing.T) {
	h := newHarness(t)
	h.expectEval(`
		local doc = [==[
[providers.testco]
adapter = "stub"

[[providers.testco.models]]
id = "big-1"
context = 200000
cost = { input = 5.0, output = 25.0 }
aliases = ["big"]

[[providers.testco.models]]
id = "small-1"
]==]
		local d = nu.toml.decode(doc)
		local models = d.providers.testco.models
		assert(type(models) == "table", "models es una tabla, no "..type(models))
		assert(#models == 2, "dos modelos, hay "..tostring(#models))
		assert(models[1].id == "big-1", "primer id")
		assert(models[1].context == 200000, "context numérico")
		-- Tabla inline anidada dentro del elemento del array.
		assert(models[1].cost.input == 5.0, "cost.input anidado")
		assert(models[1].aliases[1] == "big", "aliases lista anidada")
		assert(models[2].id == "small-1", "segundo id")
		return "ok"
	`, "ok")
}

// TestYAMLFrontmatterSkill blinda el round-trip de un frontmatter de skill típico
// (claves, listas, strings) —el uso que motiva añadir YAML (§12: "metadatos del
// ecosistema existente", "demasiado traicionero para Lua puro")—.
func TestYAMLFrontmatterSkill(t *testing.T) {
	h := newHarness(t)

	// decode de un frontmatter típico.
	h.expectEval(`
		local fm = [[
name: code-review
description: Revisa un diff
allowed_tools:
  - Read
  - Grep
enabled: true
]]
		local t = nu.yaml.decode(fm)
		assert(t.name == "code-review", "name")
		assert(t.description == "Revisa un diff", "description")
		assert(#t.allowed_tools == 2, "allowed_tools lista de 2")
		assert(t.allowed_tools[1] == "Read", "allowed_tools[1]")
		assert(t.enabled == true, "enabled bool")
		return "ok"
	`, "ok")

	// Round-trip: encode → decode reproduce las claves, listas y tipos.
	h.expectEval(`
		local orig = { name = "x", tags = {"a", "b"}, n = 3, on = false }
		local back = nu.yaml.decode(nu.yaml.encode(orig))
		assert(back.name == "x", "rt name")
		assert(#back.tags == 2 and back.tags[2] == "b", "rt tags")
		assert(back.n == 3, "rt n")
		assert(back.on == false, "rt on")
		return "ok"
	`, "ok")

	// YAML inválido → EINVAL. Un mapeo con una llave de bloque sin cerrar es
	// sintaxis rota que yaml.v3 rechaza.
	se := h.evalErr(`return nu.yaml.decode("{ a: 1, b")`)
	if se.Code != CodeEINVAL {
		t.Fatalf("yaml.decode inválido debe lanzar EINVAL, llegó %q", se.Code)
	}
}

// TestCodecsDesdeTask confirma que los codecs son usables desde dentro de una
// task (son [W], §16; no ⏸, así que no suspenden, pero deben funcionar en ese
// contexto igual que en el chunk).
func TestCodecsDesdeTask(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		ok = false
		nu.task.spawn(function()
			local j = nu.json.encode({ a = 1 })
			assert(j == '{"a":1}', "json en task")
			local tm = nu.toml.decode('k = "v"')
			assert(tm.k == "v", "toml en task")
			local y = nu.yaml.decode("x: 1")
			assert(y.x == 1, "yaml en task")
			ok = true
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")
}

// TestEncodeNoSerializable: tipos sin representación (función) y números no
// finitos (NaN/Inf) → `EINVAL` (no se serializa basura silenciosamente).
func TestEncodeNoSerializable(t *testing.T) {
	h := newHarness(t)

	se := h.evalErr(`return nu.json.encode(function() end)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("encode de función debe lanzar EINVAL, llegó %q", se.Code)
	}

	se = h.evalErr(`return nu.json.encode(0/0)`) // NaN
	if se.Code != CodeEINVAL {
		t.Fatalf("encode de NaN debe lanzar EINVAL, llegó %q", se.Code)
	}

	se = h.evalErr(`return nu.json.encode(1/0)`) // +Inf
	if se.Code != CodeEINVAL {
		t.Fatalf("encode de Inf debe lanzar EINVAL, llegó %q", se.Code)
	}
}
