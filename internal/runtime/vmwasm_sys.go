package runtime

// Catálogo de enu.sys sobre el backend wasm (M13b, §7). Contraparte de sys.go:
// platform/env/setenv/now_ms/mono_ms/hostname/pid. Todas SÍNCRONAS (consultas
// locales inmediatas; ninguna ⏸). Reusan el overlay de entorno del Runtime
// (rt.sys) y el mismo origen monotónico (monoOrigin, var de paquete en sys.go).

import (
	"os"
	"runtime"
	"time"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

func registerSysWasm(p *vmwasm.Pool, rt *Runtime) {
	// enu.sys.platform() -> string (runtime.GOOS).
	p.Register("sys.platform", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		return []any{runtime.GOOS}, nil
	})
	// enu.sys.env(name) -> string? (overlay de setenv sobre el entorno del SO).
	p.Register("sys.env", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		if v, ok := rt.sys.envLookup(argString(args, 0)); ok {
			return []any{v}, nil
		}
		return []any{nil}, nil
	})
	// enu.sys.setenv(name, value) — sólo afecta a subprocesos futuros (overlay).
	p.Register("sys.setenv", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		rt.sys.setenv(argString(args, 0), argString(args, 1))
		return nil, nil
	})
	// enu.sys.now_ms() -> number (float): reloj de pared en ms desde el epoch.
	p.Register("sys.now_ms", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		return []any{float64(time.Now().UnixMilli())}, nil
	})
	// enu.sys.mono_ms() -> number (float): reloj monotónico en ms (origen arbitrario).
	p.Register("sys.mono_ms", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		return []any{float64(time.Since(monoOrigin).Milliseconds())}, nil
	})
	// enu.sys.hostname() -> string (os.Hostname; fallo → EIO).
	p.Register("sys.hostname", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		name, err := os.Hostname()
		if err != nil {
			return nil, &vmwasm.StructuredError{Code: "EIO", Message: "enu.sys.hostname: " + err.Error()}
		}
		return []any{name}, nil
	})
	// enu.sys.pid() -> integer (os.Getpid; el pid del proceso enu, G32).
	p.Register("sys.pid", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		return []any{int64(os.Getpid())}, nil
	})
}
