package runtime

// Catálogo de enu.re sobre el backend wasm (M13b, §10). Contraparte de re.go:
// enu.re.compile(pattern) -> Re, con Re:match/find_all/replace. Un `Re` es un
// handle (C5) que envuelve un *regexp.Regexp (RE2, seguro para uso concurrente).
// El trabajo es idéntico al backend gopher (stdlib regexp); sólo cambia el
// marshaling de la frontera.
//
// `match` necesita un wrapper Lua (AddPreludio): su tabla de capturas es MIXTA
// —parte array 1-based (caps[1]=match completo, caps[2..]=grupos) MÁS los grupos
// con nombre por clave string (caps.name)—, y el wire no cruza una tabla mixta de
// una pieza. El wrapper ensambla ambas partes en Lua. find_all y replace no son
// mixtos, así que se despachan por la metatable genérica del handle.

import (
	"regexp"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

func registerReWasm(p *vmwasm.Pool) {
	// enu.re._compile(pattern) -> Re (handle). El wrapper enu.re.compile lo envuelve.
	p.Register("re._compile", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		pattern, _ := args[0].(string)
		re, err := regexp.Compile(pattern)
		if err != nil {
			// El mensaje de regexp.Compile nombra qué construye falla (§10).
			return nil, &vmwasm.StructuredError{Code: "EINVAL", Message: "enu.re.compile: " + err.Error()}
		}
		return []any{inst.AllocHandle("Re", re)}, nil
	})

	// Re:_match(s) -> (arr, named): las dos mitades de la tabla de capturas, que el
	// wrapper Lua fusiona. nil si no casa (no casar es un resultado válido, no error).
	p.RegisterHandleMethod("Re", "_match", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		re := val.(*regexp.Regexp)
		s, _ := args[0].(string)
		sub := re.FindStringSubmatch(s)
		if sub == nil {
			return []any{nil, nil}, nil
		}
		arr := make([]any, len(sub)) // [1]=match completo (grupo 0), [2..]=grupos
		for i, g := range sub {
			arr[i] = g
		}
		named := map[string]any{}
		for i, name := range re.SubexpNames() {
			if name != "" && i < len(sub) {
				named[name] = sub[i]
			}
		}
		return []any{arr, named}, nil
	})

	// Re:find_all(s) -> ranges: todas las coincidencias como {start,end} en BYTES
	// 1-based inclusive (convenio de string.find de Lua). Coincidencia vacía →
	// end=start-1. Array de arrays: cruza el wire sin problema.
	p.RegisterHandleMethod("Re", "find_all", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		re := val.(*regexp.Regexp)
		s, _ := args[0].(string)
		idxs := re.FindAllStringIndex(s, -1)
		ranges := make([]any, len(idxs))
		for i, pair := range idxs {
			ranges[i] = []any{int64(pair[0] + 1), int64(pair[1])}
		}
		return []any{ranges}, nil
	})

	// Re:replace(s, repl) -> string: sustituye todas las coincidencias (sintaxis de
	// repl la de Go: $1, ${name}, $$...).
	p.RegisterHandleMethod("Re", "replace", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		re := val.(*regexp.Regexp)
		s, _ := args[0].(string)
		repl, _ := args[1].(string)
		return []any{re.ReplaceAllString(s, repl)}, nil
	})

	// Wrapper Lua: enu.re.compile devuelve el handle con un método `match` propio que
	// fusiona (array + nombrados) la tabla mixta de capturas; find_all/replace/_match
	// se despachan por la metatable genérica del handle.
	p.AddPreludioW(`
enu.re = enu.re or {}
function enu.re.compile(pattern)
  local re = enu.re._compile(pattern)   -- handle {__id} con la metatable de handles
  re.match = function(self, s)
    local arr, named = self:_match(s)
    if arr == nil then return nil end
    local caps = {}
    for i = 1, #arr do caps[i] = arr[i] end
    if named then for k, v in pairs(named) do caps[k] = v end end
    return caps
  end
  return re
end`, "re._compile")
}
