package runtime

// Catálogo de nu.log sobre el backend wasm (M13b, §15). Contraparte de log.go:
// nu.log.debug/info/warn/error(fmt, ...) y print (alias de info). El formateo
// (string.format multi-arg, tostring de un solo arg) se hace en Lua —su semántica
// exacta de directivas—; el HostFn recibe el string final y lo escribe con el
// logger del Runtime (best-effort: un fallo de disco no se propaga, §15).
//
// Owner (M13b): por ahora "user". El seguimiento del plugin dueño (ownerStack,
// §14) llega con el loader de extensiones (M13d); hasta entonces todo el log wasm
// se anota como "user", igual que un chunk de -e o el init del usuario.

import (
	"github.com/dbareagimeno/nu/internal/vmwasm"
)

func registerLogWasm(p *vmwasm.Pool, rt *Runtime) {
	reg := func(name string, level logLevel) {
		p.Register(name, func(inst *vmwasm.Instance, args []any) ([]any, error) {
			_ = rt.log.write(level, "user", argString(args, 0))
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
