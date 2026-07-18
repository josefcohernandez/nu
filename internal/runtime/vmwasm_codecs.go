package runtime

// Catálogo de codecs sobre el backend wasm (migracion-vm.md M13b, §12). Es la
// contraparte de codecs.go para internal/vmwasm: registra enu.json/toml/yaml como
// HostFn de vmwasm, reutilizando las MISMAS librerías Go (encoding/json,
// BurntSushi/toml, yaml.v3). El trabajo de serialización es idéntico al del backend
// gopher; lo único que cambia es el marshaling de la frontera, que en wasm ya lo
// resuelve el wire: los valores llegan como `any` Go (map[string]any / []any /
// int64 / float64 / string / bool / vmwasm.Null), justo el intermedio que las tres
// librerías saben (de)serializar.
//
// Diferencias observables respecto a gopher, TODAS a mejor (anotadas):
//   - integer vs float: el wire distingue los dos subtipos de Lua 5.4, así que un
//     JSON con `42` decodifica a un INTEGER Lua (no un float), preservado en el
//     round-trip. gopher (Lua 5.1) los colapsaba a número.
//   - el sentinel NULL cruza como vmwasm.Null (no un userdata por-estado), pero la
//     semántica es la misma: null → enu.json.NULL en decode, y de vuelta a null.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	yaml "gopkg.in/yaml.v3"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

// registerCodecsWasm cuelga enu.json/toml/yaml del catálogo de un Pool wasm. El
// preludio los monta bajo enu.<fmt> y añade enu.json.NULL (el sentinel).
func registerCodecsWasm(p *vmwasm.Pool) {
	p.Register("json.encode", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		pretty := false
		if len(args) > 1 {
			if opts, ok := args[1].(map[string]any); ok {
				pretty, _ = opts["pretty"].(bool)
			}
		}
		goVal, err := wireToEncodable(arg0(args), "json")
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false) // codec de propósito general: no escapa <>& (§12)
		if pretty {
			enc.SetIndent("", "  ")
		}
		if err := enc.Encode(goVal); err != nil {
			return nil, codecErr("json", "encode", err.Error())
		}
		return []any{string(bytes.TrimRight(buf.Bytes(), "\n"))}, nil
	})
	p.Register("json.decode", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		s, ok := arg0(args).(string)
		if !ok {
			return nil, codecErr("json", "decode", "se esperaba un string")
		}
		dec := json.NewDecoder(bytes.NewReader([]byte(s)))
		dec.UseNumber() // no degradar enteros grandes a notación científica
		var goVal any
		if err := dec.Decode(&goVal); err != nil {
			return nil, codecErr("json", "decode", err.Error())
		}
		return []any{decodedToWire(goVal, true)}, nil // useNull: null → enu.json.NULL
	})

	p.Register("toml.encode", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		goVal, err := wireToEncodable(arg0(args), "toml")
		if err != nil {
			return nil, err
		}
		if _, ok := goVal.(map[string]any); !ok {
			return nil, codecErr("toml", "encode",
				"la raíz de un documento TOML debe ser una tabla (objeto), no un array ni un escalar")
		}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(goVal); err != nil {
			return nil, codecErr("toml", "encode", err.Error())
		}
		return []any{buf.String()}, nil
	})
	p.Register("toml.decode", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		s, ok := arg0(args).(string)
		if !ok {
			return nil, codecErr("toml", "decode", "se esperaba un string")
		}
		var goVal map[string]any
		if err := toml.Unmarshal([]byte(s), &goVal); err != nil {
			return nil, codecErr("toml", "decode", err.Error())
		}
		return []any{decodedToWire(goVal, false)}, nil
	})

	p.Register("yaml.encode", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		goVal, err := wireToEncodable(arg0(args), "yaml")
		if err != nil {
			return nil, err
		}
		out, err := yaml.Marshal(goVal)
		if err != nil {
			return nil, codecErr("yaml", "encode", err.Error())
		}
		return []any{string(out)}, nil
	})
	p.Register("yaml.decode", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		s, ok := arg0(args).(string)
		if !ok {
			return nil, codecErr("yaml", "decode", "se esperaba un string")
		}
		var goVal any
		if err := yaml.Unmarshal([]byte(s), &goVal); err != nil {
			return nil, codecErr("yaml", "decode", err.Error())
		}
		return []any{decodedToWire(goVal, false)}, nil
	})
}

func arg0(args []any) any {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

func codecErr(format, op, msg string) error {
	return &vmwasm.StructuredError{Code: "EINVAL", Message: "enu." + format + "." + op + ": " + msg}
}

// wireToEncodable convierte el valor de la frontera (lo que da el wire) al valor Go
// que las librerías serializan, validando G11 (UTF-8 estricto en strings y claves)
// y NaN/Inf, y traduciendo el sentinel Null → nil. Una tabla VACÍA (que el wire
// cruza como array vacío) se resuelve aquí como OBJETO ({}), la decisión de §12.
func wireToEncodable(v any, format string) (any, error) {
	switch val := v.(type) {
	case nil:
		return nil, nil
	case vmwasm.Null:
		return nil, nil
	case bool:
		return val, nil
	case int64:
		return val, nil
	case float64:
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return nil, codecErr(format, "encode", "número no finito (NaN/Inf) no es representable")
		}
		return val, nil
	case string:
		if !utf8.ValidString(val) {
			return nil, codecErr(format, "encode", "el string contiene bytes UTF-8 inválidos")
		}
		return val, nil
	case vmwasm.Handle:
		return nil, codecErr(format, "encode", "un handle (userdata/Block) no es serializable")
	case []any:
		if len(val) == 0 {
			return map[string]any{}, nil // §12: tabla vacía → objeto
		}
		arr := make([]any, len(val))
		for i, e := range val {
			c, err := wireToEncodable(e, format)
			if err != nil {
				return nil, err
			}
			arr[i] = c
		}
		return arr, nil
	case map[string]any:
		obj := make(map[string]any, len(val))
		for k, e := range val {
			if !utf8.ValidString(k) {
				return nil, codecErr(format, "encode", "la clave de tabla contiene bytes UTF-8 inválidos")
			}
			c, err := wireToEncodable(e, format)
			if err != nil {
				return nil, err
			}
			obj[k] = c
		}
		return obj, nil
	default:
		return nil, codecErr(format, "encode", fmt.Sprintf("valor de tipo %T no es serializable", v))
	}
}

// decodedToWire convierte el resultado de un Unmarshal al valor de la frontera. Un
// nil se mapea al sentinel Null sólo para JSON (useNull), para que un null
// sobreviva el round-trip; para toml/yaml va a nil. json.Number se resuelve a
// integer si no tiene parte fraccionaria (Lua 5.4 distingue), o float si la tiene.
// La reflexión cubre los tipos concretos que las librerías dan (p. ej. el
// []map[string]any de un array-de-tablas TOML, providers.toml).
func decodedToWire(v any, useNull bool) any {
	switch val := v.(type) {
	case nil:
		if useNull {
			return vmwasm.NullValue
		}
		return nil
	case bool:
		return val
	case float64:
		return val
	case int:
		return int64(val)
	case int64:
		return val
	case json.Number:
		if i, err := val.Int64(); err == nil {
			return i
		}
		f, _ := val.Float64()
		return f
	case string:
		return val
	case []any:
		arr := make([]any, len(val))
		for i, e := range val {
			arr[i] = decodedToWire(e, useNull)
		}
		return arr
	case map[string]any:
		obj := make(map[string]any, len(val))
		for k, e := range val {
			obj[k] = decodedToWire(e, useNull)
		}
		return obj
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			arr := make([]any, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				arr[i] = decodedToWire(rv.Index(i).Interface(), useNull)
			}
			return arr
		case reflect.Map:
			obj := make(map[string]any, rv.Len())
			for _, k := range rv.MapKeys() {
				obj[fmt.Sprintf("%v", k.Interface())] = decodedToWire(rv.MapIndex(k).Interface(), useNull)
			}
			return obj
		}
		return fmt.Sprintf("%v", val)
	}
}
