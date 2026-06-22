package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	lua "github.com/yuin/gopher-lua"
	yaml "gopkg.in/yaml.v3"
)

// `nu.json` / `nu.toml` / `nu.yaml` — codecs (api.md §12, sesión S18). Tres
// pares `encode`/`decode` que convierten valores Lua a/desde los tres formatos
// de serialización del ecosistema. **Ninguno ⏸** (son CPU puro: no hay IO que
// esperar, solo parsear/serializar un string ya en memoria) y todos [W] (§16:
// disponibles en workers; los workers son S34, así que hoy se registran en el
// estado principal). "Lua decide, Go ejecuta" (ADR-004): el trabajo de parseo y
// serialización es Go (stdlib `encoding/json`, BurntSushi/toml, yaml.v3), no Lua
// puro —en particular YAML, "demasiado traicionero para Lua puro" (§12)—.
//
// EL MAPEO Lua↔Go (compartido por los tres formatos). El puente es un valor
// intermedio Go (`interface{}` con `map[string]interface{}`/`[]interface{}`/
// `float64`/`string`/`bool`/`nil`) que las tres librerías saben serializar:
//
//   - `nil` (Lua) → null. En decode, un null → `nil` PERDERÍA la clave en una
//     tabla Lua (`t.k = nil` la borra), así que JSON usa el **sentinel `NULL`**
//     (ver abajo) para que `decode` no pierda claves; toml/yaml mapean el valor
//     faltante a `nil` (no se da en su forma típica de config).
//   - boolean → bool.
//   - number → float64. Lua no distingue int de float; un número se serializa
//     como entero si no tiene parte fraccionaria (lo decide el lado Go), pero
//     internamente es float64 (suficiente para JSON/config).
//   - string → string. **JSON es ESTRICTO con UTF-8 (G11):** un string Lua con
//     bytes UTF-8 inválidos hace que `json.encode` lance `EINVAL` en vez de
//     reemplazarlos por U+FFFD (lo que `encoding/json` haría en silencio).
//     Sanear es una decisión visible de quien tiene el contexto (la tool), nunca
//     del codec (§12).
//   - table → **array** si sus claves son exactamente 1..n contiguas (la
//     convención de secuencia de Lua); en caso contrario, **objeto/map** (claves
//     a string). Una tabla **vacía** es ambigua (¿`[]` o `{}`?): se decide
//     **objeto** (`{}`) —la mayoría de las tablas-config de este proyecto son
//     mapas, y una lista vacía es el caso raro—. Decisión registrada en
//     claude_decisions.md (S18).
//
// EL SENTINEL `nu.json.NULL`. Es un **userdata único** (un solo `*lua.LUserData`
// por Runtime, `rt.jsonNull`) que representa `null` de JSON sin colisionar con
// ningún valor Lua legítimo. En `decode`, un `null` JSON se convierte en este
// sentinel (NO en `nil`, que al asignarse a una tabla borraría la clave: una
// ida y vuelta perdería claves con valor null). En `encode`, el sentinel →
// `null`. Es el patrón canónico para "null que sobrevive el round-trip".

// registerCodecs cuelga `nu.json`, `nu.toml` y `nu.yaml` del global `nu` con sus
// firmas de §12. Lo llama `registerNu` (nu.go). El sentinel `nu.json.NULL` se
// crea una sola vez (`rt.jsonNull`) y se expone como campo de la tabla `json`.
func (rt *Runtime) registerCodecs(nu *lua.LTable) {
	L := rt.L

	// El sentinel NULL: un userdata único del Runtime. Sin metatabla ni valor
	// interno —solo importa su identidad (un puntero único que `==` distingue de
	// cualquier otro valor Lua)—.
	rt.jsonNull = L.NewUserData()

	jsonT := L.NewTable()
	jsonT.RawSetString("encode", L.NewFunction(rt.jsonEncode))
	jsonT.RawSetString("decode", L.NewFunction(rt.jsonDecode))
	jsonT.RawSetString("NULL", rt.jsonNull)
	nu.RawSetString("json", jsonT)

	tomlT := L.NewTable()
	tomlT.RawSetString("encode", L.NewFunction(rt.tomlEncode))
	tomlT.RawSetString("decode", L.NewFunction(rt.tomlDecode))
	nu.RawSetString("toml", tomlT)

	yamlT := L.NewTable()
	yamlT.RawSetString("encode", L.NewFunction(rt.yamlEncode))
	yamlT.RawSetString("decode", L.NewFunction(rt.yamlDecode))
	nu.RawSetString("yaml", yamlT)
}

// ── Lua → Go ────────────────────────────────────────────────────────────────

// luaToGo convierte un valor Lua al valor Go intermedio que las tres librerías
// de serialización saben emitir. Lanza `EINVAL` (vía `raiseError`) ante un valor
// inconvertible: un string con UTF-8 inválido (G11), un número no finito
// (NaN/Inf, sin representación en JSON/TOML/YAML), o un tipo Lua sin equivalente
// (función, userdata distinto del sentinel, thread). `format` nombra el codec en
// el mensaje de error para que sea accionable.
//
// El sentinel `nu.json.NULL` (`rt.jsonNull`) se reconoce por identidad y se
// convierte a `nil` Go (que las librerías emiten como null). Es válido en los
// tres formatos al codificar; su razón de ser es el round-trip de JSON.
func (rt *Runtime) luaToGo(L *lua.LState, v lua.LValue, format string) interface{} {
	switch val := v.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(val)
	case lua.LNumber:
		f := float64(val)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			raiseError(L, CodeEINVAL,
				fmt.Sprintf("nu.%s.encode: número no finito (NaN/Inf) no es representable", format), lua.LNil)
		}
		return f
	case lua.LString:
		s := string(val)
		if !utf8.ValidString(s) {
			// G11: UTF-8 estricto. `encoding/json` reemplazaría los bytes inválidos
			// por U+FFFD en silencio; en su lugar lo detectamos y lanzamos EINVAL —
			// sanear es decisión de quien tiene el contexto, no del codec (§12).
			raiseError(L, CodeEINVAL,
				fmt.Sprintf("nu.%s.encode: el string contiene bytes UTF-8 inválidos", format), lua.LNil)
		}
		return s
	case *lua.LUserData:
		if val == rt.jsonNull {
			return nil
		}
		raiseError(L, CodeEINVAL,
			fmt.Sprintf("nu.%s.encode: userdata no es serializable (¿olvidaste nu.json.NULL?)", format), lua.LNil)
	case *lua.LTable:
		return rt.luaTableToGo(L, val, format)
	default:
		// función, thread, channel: sin equivalente serializable.
		raiseError(L, CodeEINVAL,
			fmt.Sprintf("nu.%s.encode: valor de tipo %s no es serializable", format, v.Type().String()), lua.LNil)
	}
	return nil // inalcanzable: raiseError desenrolla la pila.
}

// luaTableToGo decide si una tabla Lua es un **array** (claves 1..n contiguas) o
// un **objeto** (resto), y la convierte recursivamente. Una tabla vacía → objeto
// (`{}`), decisión documentada (ver cabecera). Las claves de un objeto se
// convierten a string (un número 1.0 → "1", etc., como exigen JSON/YAML, cuyas
// claves son strings); claves no escalares (tabla, función) → `EINVAL`.
func (rt *Runtime) luaTableToGo(L *lua.LState, t *lua.LTable, format string) interface{} {
	// Cuenta la longitud de la secuencia (1..n contiguos) y el total de claves.
	// Si coinciden y hay al menos una, es un array puro.
	seqLen := 0
	for {
		if t.RawGetInt(seqLen+1) == lua.LNil {
			break
		}
		seqLen++
	}

	total := 0
	hasNonSeq := false
	t.ForEach(func(k, _ lua.LValue) {
		total++
		// ¿Es esta clave parte de la secuencia 1..seqLen?
		if n, ok := k.(lua.LNumber); ok {
			i := float64(n)
			if i == math.Trunc(i) && i >= 1 && int(i) <= seqLen {
				return
			}
		}
		hasNonSeq = true
	})

	if seqLen > 0 && !hasNonSeq && total == seqLen {
		// Array: claves exactamente 1..seqLen, nada más.
		arr := make([]interface{}, seqLen)
		for i := 1; i <= seqLen; i++ {
			arr[i-1] = rt.luaToGo(L, t.RawGetInt(i), format)
		}
		return arr
	}

	// Objeto (incluida la tabla vacía): claves a string, valores recursivos.
	obj := make(map[string]interface{}, total)
	var convErr lua.LValue
	t.ForEach(func(k, v lua.LValue) {
		if convErr != nil {
			return
		}
		key, ok := luaKeyToString(k)
		if !ok {
			convErr = lua.LString(fmt.Sprintf(
				"nu.%s.encode: clave de tabla de tipo %s no es serializable como objeto", format, k.Type().String()))
			return
		}
		// G11: UTF-8 estricto también en las CLAVES de objeto (un string-clave con
		// bytes inválidos rompe el documento igual que un valor).
		if !utf8.ValidString(key) {
			convErr = lua.LString(fmt.Sprintf(
				"nu.%s.encode: la clave de tabla contiene bytes UTF-8 inválidos", format))
			return
		}
		obj[key] = rt.luaToGo(L, v, format)
	})
	if convErr != nil {
		raiseError(L, CodeEINVAL, string(convErr.(lua.LString)), lua.LNil)
	}
	return obj
}

// luaKeyToString convierte una clave de tabla Lua a la string que usará un
// objeto JSON/YAML/TOML. Acepta strings (tal cual) y números (formateados sin
// decimal superfluo: 1.0 → "1"); rechaza el resto (bool, tabla, función) porque
// no hay convención portable para esas claves en estos formatos.
func luaKeyToString(k lua.LValue) (string, bool) {
	switch key := k.(type) {
	case lua.LString:
		return string(key), true
	case lua.LNumber:
		f := float64(key)
		if f == math.Trunc(f) && !math.IsInf(f, 0) {
			return fmt.Sprintf("%d", int64(f)), true
		}
		return fmt.Sprintf("%g", f), true
	default:
		return "", false
	}
}

// ── Go → Lua ────────────────────────────────────────────────────────────────

// goToLua convierte el valor Go intermedio (lo que devuelven los `Unmarshal` de
// las tres librerías) a un valor Lua. `nil` se mapea al sentinel `nu.json.NULL`
// SOLO para JSON (`useNull=true`): así un `null` JSON sobrevive el round-trip sin
// perder la clave en una tabla Lua. Para TOML/YAML (`useNull=false`) un nil va a
// `nil` Lua.
//
// Lua no distingue int de float, así que todo número va como `LNumber` (float64).
func (rt *Runtime) goToLua(L *lua.LState, v interface{}, useNull bool) lua.LValue {
	switch val := v.(type) {
	case nil:
		if useNull {
			return rt.jsonNull
		}
		return lua.LNil
	case bool:
		return lua.LBool(val)
	case float64:
		return lua.LNumber(val)
	case int:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case json.Number:
		// `decode` de JSON puede entregar `json.Number` (lo activamos con
		// `UseNumber` para no perder precisión de enteros grandes); a Lua va como
		// número (float64).
		f, _ := val.Float64()
		return lua.LNumber(f)
	case string:
		return lua.LString(val)
	case []interface{}:
		arr := L.NewTable()
		for _, e := range val {
			arr.Append(rt.goToLua(L, e, useNull))
		}
		return arr
	case map[string]interface{}:
		obj := L.NewTable()
		// Orden estable de claves: no afecta a la tabla Lua (no preserva orden),
		// pero hace los tests y cualquier traza deterministas.
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			obj.RawSetString(k, rt.goToLua(L, val[k], useNull))
		}
		return obj
	case map[interface{}]interface{}:
		// yaml.v3 sin destino tipado puede dar claves no-string; las pasamos por
		// `fmt` para encajarlas en una tabla Lua (clave string).
		obj := L.NewTable()
		for k, e := range val {
			obj.RawSetString(fmt.Sprintf("%v", k), rt.goToLua(L, e, useNull))
		}
		return obj
	default:
		// Tipo Go inesperado de una librería: lo serializamos a string como último
		// recurso en vez de fallar (no debería ocurrir con los Unmarshal usados).
		return lua.LString(fmt.Sprintf("%v", val))
	}
}

// ── JSON ──────────────────────────────────────────────────────────────────────

// jsonEncode implementa `nu.json.encode(v, opts?) -> string` (§12). Convierte el
// valor Lua al intermedio Go (validando UTF-8 estricto, G11, y rechazando NaN/Inf
// por el camino) y lo serializa con `encoding/json`. `opts.pretty` activa el
// indentado (dos espacios). El sentinel `nu.json.NULL` → `null`.
//
// HTML escaping desactivado: por defecto `encoding/json` escapa `<`/`>`/`&` a
// `<`... (defensa para incrustar en HTML); en un codec de propósito general
// eso sorprende (un round-trip cambiaría el texto), así que se desactiva —quien
// incruste en HTML escapa él, como con UTF-8 (§12)—.
func (rt *Runtime) jsonEncode(L *lua.LState) int {
	v := L.CheckAny(1)
	pretty := false
	if opts := L.Get(2); opts != lua.LNil {
		t, ok := opts.(*lua.LTable)
		if !ok {
			raiseError(L, CodeEINVAL, "nu.json.encode: opts debe ser una tabla", lua.LNil)
			return 0
		}
		pretty = lua.LVAsBool(t.RawGetString("pretty"))
	}

	goVal := rt.luaToGo(L, v, "json")

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(goVal); err != nil {
		raiseError(L, CodeEINVAL, "nu.json.encode: "+err.Error(), lua.LNil)
		return 0
	}
	// `json.Encoder.Encode` añade un '\n' final; lo recortamos para que el
	// resultado sea el string JSON exacto (un round-trip no acumula saltos).
	out := bytes.TrimRight(buf.Bytes(), "\n")
	L.Push(lua.LString(out))
	return 1
}

// jsonDecode implementa `nu.json.decode(s) -> v` (§12). Parsea el string JSON a
// un valor Lua. Un `null` JSON → sentinel `nu.json.NULL` (no `nil`, que perdería
// la clave en una tabla: ida y vuelta sin pérdida). JSON inválido → `EINVAL`
// accionable. Se usa `json.Number` (`UseNumber`) para no degradar enteros
// grandes a notación científica en el round-trip.
func (rt *Runtime) jsonDecode(L *lua.LState) int {
	s := L.CheckString(1)

	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var goVal interface{}
	if err := dec.Decode(&goVal); err != nil {
		raiseError(L, CodeEINVAL, "nu.json.decode: "+err.Error(), lua.LNil)
		return 0
	}
	L.Push(rt.goToLua(L, goVal, true)) // useNull=true: null → nu.json.NULL
	return 1
}

// ── TOML ──────────────────────────────────────────────────────────────────────

// tomlEncode implementa `nu.toml.encode(v) -> string` (§12). El valor raíz de
// TOML debe ser una tabla (un documento TOML es un mapa de claves), así que
// `encode` exige un objeto; un array o escalar en la raíz → `EINVAL`. Usa
// BurntSushi/toml (la misma librería que el loader, S11). UTF-8 estricto (G11)
// se aplica igual que en JSON al convertir los strings.
func (rt *Runtime) tomlEncode(L *lua.LState) int {
	v := L.CheckAny(1)
	goVal := rt.luaToGo(L, v, "toml")
	if _, ok := goVal.(map[string]interface{}); !ok {
		raiseError(L, CodeEINVAL,
			"nu.toml.encode: la raíz de un documento TOML debe ser una tabla (objeto), no un array ni un escalar", lua.LNil)
		return 0
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(goVal); err != nil {
		raiseError(L, CodeEINVAL, "nu.toml.encode: "+err.Error(), lua.LNil)
		return 0
	}
	L.Push(lua.LString(buf.String()))
	return 1
}

// tomlDecode implementa `nu.toml.decode(s) -> v` (§12). Parsea un documento TOML
// a una tabla Lua (su raíz siempre es un objeto). TOML inválido → `EINVAL`
// accionable (BurntSushi incluye la línea). Es la vía con la que un plugin lee su
// propio `plugin.toml`/`nu.toml` desde Lua (el core lo parsea internamente, S11).
func (rt *Runtime) tomlDecode(L *lua.LState) int {
	s := L.CheckString(1)

	var goVal map[string]interface{}
	if err := toml.Unmarshal([]byte(s), &goVal); err != nil {
		raiseError(L, CodeEINVAL, "nu.toml.decode: "+err.Error(), lua.LNil)
		return 0
	}
	// useNull=false: TOML no tiene null nativo de uso común; un valor faltante no
	// se da en su forma normal de config.
	L.Push(rt.goToLua(L, goVal, false))
	return 1
}

// ── YAML ──────────────────────────────────────────────────────────────────────

// yamlEncode implementa `nu.yaml.encode(v) -> string` (§12). Serializa el valor
// Lua a YAML con yaml.v3 (puro-Go). Necesario para los metadatos del ecosistema
// existente (frontmatter de skills); YAML es "demasiado traicionero para Lua
// puro" (§12), así que el trabajo es Go. UTF-8 estricto (G11) por el mismo
// `luaToGo`.
func (rt *Runtime) yamlEncode(L *lua.LState) int {
	v := L.CheckAny(1)
	goVal := rt.luaToGo(L, v, "yaml")

	out, err := yaml.Marshal(goVal)
	if err != nil {
		raiseError(L, CodeEINVAL, "nu.yaml.encode: "+err.Error(), lua.LNil)
		return 0
	}
	L.Push(lua.LString(out))
	return 1
}

// yamlDecode implementa `nu.yaml.decode(s) -> v` (§12). Parsea YAML a un valor
// Lua —típicamente el frontmatter de un skill: un mapa con claves, listas y
// strings—. YAML inválido → `EINVAL` accionable. `useNull=false`: un `null`/`~`
// de YAML va a `nil` Lua (los formatos de config no dependen del round-trip de
// null como JSON; quien lo necesite usa JSON).
func (rt *Runtime) yamlDecode(L *lua.LState) int {
	s := L.CheckString(1)

	var goVal interface{}
	if err := yaml.Unmarshal([]byte(s), &goVal); err != nil {
		raiseError(L, CodeEINVAL, "nu.yaml.decode: "+err.Error(), lua.LNil)
		return 0
	}
	L.Push(rt.goToLua(L, goVal, false))
	return 1
}
