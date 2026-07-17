package runtime

// Catálogo de enu.log sobre el backend wasm (M13b, §15). Contraparte de log.go:
// enu.log.debug/info/warn/error(fmt, ...) y print (alias de info). El formateo
// (string.format multi-arg, tostring de un solo arg) se hace en Lua —su semántica
// exacta de directivas—; el HostFn recibe el string final y lo escribe con el
// logger del Runtime (best-effort: un fallo de disco no se propaga, §15).
//
// Owner (M13d-ext): se anota el dueño resuelto por `rt.ownerForInst(inst)` (G56,
// ADR-024). Desde el estado principal es `rt.currentOwner()` —el tope del
// `ownerStack` que el arranque de extensiones (vmwasm_loader.go) empuja alrededor de
// cada `init.lua`—: durante el init de una extensión el log queda a su nombre; fuera
// de todo init (una task que corre luego, el chunk de `-e`, el init del usuario) la
// pila está vacía y el owner es "user", idéntico al backend gopher. El HostFn es
// SÍNCRONO y corre en línea (mismo goroutine que el `Eval`/paso del scheduler), así
// que la lectura del `ownerStack` es single-thread, sin candado (ADR-004).
//
// Desde un WORKER (G56) el owner es la FOTO del plugin dueño tomada en el spawn,
// inmutable, y se anota distinguible como `<plugin> (worker)` —quién Y desde dónde—:
// la goroutine del worker NO lee el ownerStack del padre (elimina el data race de
// SEC-05 y hace la atribución determinista).

import (
	"github.com/dbareagimeno/enu/internal/vmwasm"
)

func registerLogWasm(p *vmwasm.Pool, rt *Runtime) {
	reg := func(name string, level logLevel) {
		p.Register(name, func(inst *vmwasm.Instance, args []any) ([]any, error) {
			_ = rt.log.write(level, logOwnerLabel(rt.ownerForInst(inst)), argString(args, 0))
			return nil, nil
		})
	}
	reg("log._debug", levelDebug)
	reg("log._info", levelInfo)
	reg("log._warn", levelWarn)
	reg("log._error", levelError)

	// Wrapper Lua: formatea los args y llama al primitivo del nivel. print = info.
	p.AddPreludioW(`
enu.log = enu.log or {}
local function __logfmt(...)
  local n = select("#", ...)
  if n == 0 then return "" end
  if n == 1 then local a = ...; return tostring(a) end
  return string.format(...)
end
function enu.log.debug(...) enu.log._debug(__logfmt(...)) end
function enu.log.info(...) enu.log._info(__logfmt(...)) end
function enu.log.warn(...) enu.log._warn(__logfmt(...)) end
function enu.log.error(...) enu.log._error(__logfmt(...)) end
print = enu.log.info`, "log._debug", "log._info", "log._warn", "log._error")
}

// logOwnerLabel forma la etiqueta de atribución de una línea de log a partir del
// dueño resuelto por `ownerForInst` (G56, ADR-024). Desde un worker se distingue
// como `<plugin> (worker)` —para que la traza diga quién Y desde dónde—; desde el
// estado principal es el nombre del dueño tal cual. Toma los dos valores de
// `ownerForInst` directamente: `logOwnerLabel(rt.ownerForInst(inst))`.
func logOwnerLabel(owner string, fromWorker bool) string {
	if fromWorker {
		return owner + " (worker)"
	}
	return owner
}
