package runtime

// Catálogo de nu.log sobre el backend wasm (M13b, §15). Contraparte de log.go:
// nu.log.debug/info/warn/error(fmt, ...) y print (alias de info). El formateo
// (string.format multi-arg, tostring de un solo arg) se hace en Lua —su semántica
// exacta de directivas—; el HostFn recibe el string final y lo escribe con el
// logger del Runtime (best-effort: un fallo de disco no se propaga, §15).
//
// Owner (M13d-ext): se anota `rt.currentOwner()` —el tope del `ownerStack` que el
// arranque de extensiones (vmwasm_loader.go) empuja alrededor de cada `init.lua`—.
// Durante el init de una extensión el log queda a su nombre; fuera de todo init
// (una task que corre luego, el chunk de `-e`, el init del usuario) la pila está
// vacía y el owner es "user", idéntico al backend gopher. El HostFn es SÍNCRONO y
// corre en línea (mismo goroutine que el `Eval`/paso del scheduler), así que la
// lectura del `ownerStack` es single-thread, sin candado (ADR-004).

import (
	"github.com/dbareagimeno/nu/internal/vmwasm"
)

func registerLogWasm(p *vmwasm.Pool, rt *Runtime) {
	reg := func(name string, level logLevel) {
		p.Register(name, func(inst *vmwasm.Instance, args []any) ([]any, error) {
			_ = rt.log.write(level, rt.currentOwner(), argString(args, 0))
			return nil, nil
		})
	}
	reg("log._debug", levelDebug)
	reg("log._info", levelInfo)
	reg("log._warn", levelWarn)
	reg("log._error", levelError)

	// Wrapper Lua: formatea los args y llama al primitivo del nivel. print = info.
	p.AddPreludio(`
nu.log = nu.log or {}
local function __logfmt(...)
  local n = select("#", ...)
  if n == 0 then return "" end
  if n == 1 then local a = ...; return tostring(a) end
  return string.format(...)
end
function nu.log.debug(...) nu.log._debug(__logfmt(...)) end
function nu.log.info(...) nu.log._info(__logfmt(...)) end
function nu.log.warn(...) nu.log._warn(__logfmt(...)) end
function nu.log.error(...) nu.log._error(__logfmt(...)) end
print = nu.log.info`)
}
