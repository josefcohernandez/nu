package runtime

// Catálogo de nu.proc sobre el backend wasm (M13b, §6). Contraparte de proc.go: la
// conveniencia con buffers nu.proc.run(argv, opts?) -> {code, stdout, stderr} (⏸),
// el control fino nu.proc.spawn(argv, opts?) -> Proc (síncrono: arrancar no
// bloquea) y la consulta nu.proc.alive(pid) -> boolean (síncrona). El handle Proc
// lleva write/read_line/read/wait (⏸, IO bloqueante → __hcall_s) y close_stdin/kill
// (síncronos → __hcall). Reusa TODO el núcleo VM-agnóstico de proc.go —el parseo
// (parseProcArgsWasm es el gemelo de parseProcArgs), spawnProc (pipes + reaper +
// finalizer), runBuffered, y los métodos de luaProc (writeStdin/reader/readLineFrom/
// readFrom/wait/closeStdin/killSignal)—; sólo cambia el marshaling de la frontera y
// el despacho de los métodos.
//
// SPAWN NO BLOQUEA, PERO CREA UN HANDLE. A diferencia de nu.ws.connect (⏸ + handle,
// porque el handshake bloquea), spawn arranca con cmd.Start() —fork/exec, no espera
// al proceso— y devuelve el handle en el acto. Por eso proc._spawn es una primitiva
// SÍNCRONA (p.Register): corre en el hilo principal, donde AllocHandle es seguro. El
// wrapper Lua nu.proc.spawn (AddPreludio) envuelve el handle y le cuelga los seis
// métodos apuntando al despacho correcto —los cuatro de IO por __hcall_s (ceden al
// scheduler; su lectura/escritura/espera bloqueante corre en la goroutine de fondo),
// close_stdin/kill por __hcall (inmediatos)—.
//
// LOS ⏸ CEDEN Y NO TOCAN LA VM. write/read_line/read/wait corren en la goroutine de
// fondo de un __hcall_s: su trabajo (Write al pipe, ReadString, cmd.Wait) es
// justamente el IO bloqueante que proc.go ya aísla en `luaProc` con sus candados
// (stdinMu/stdoutMu/stderrMu/killMu + waitOnce), sin tocar Lua. `reader` sólo valida
// el nombre del stream y devuelve punteros: también seguro fuera del hilo principal.
//
// CICLO DE VIDA. `kill` NO libera el handle (idempotente, §6: matar dos veces es
// inocuo vía `killed`); tampoco lo hace `close_stdin`. El rastreo para el apagado
// ordenado (Runtime.Close → stopAllProcs) y el registro por dueño (reload, G2) los
// hace spawnProc sólo si hay scheduler; en M13b el rt de los tests es mínimo (sin
// scheduler), y la red del GC (SetFinalizer) sigue matando un Proc sin referencias.

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/dbareagimeno/nu/internal/vmwasm"
)

func registerProcWasm(p *vmwasm.Pool, rt *Runtime) {
	// nu.proc.run(argv, opts?) -> {code, stdout, stderr} ⏸ — la conveniencia con
	// buffers. Todo el ciclo (start + IO + wait) corre en la goroutine de fondo
	// (runBuffered, el mismo núcleo que gopher). Un code != 0 NO lanza (es dato); lo
	// que lanza: arranque fallido (ENOENT/EACCES/EIO) o timeout_ms excedido (ETIMEOUT,
	// tras matar el proceso).
	p.RegisterSuspending("proc.run", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		argv, opts, err := parseProcArgsWasm(arg(args, 0), arg(args, 1))
		if err != nil {
			return nil, err
		}
		// Foto del overlay de nu.sys.setenv (§7): los subprocesos futuros ven los setenv
		// previos. En el rt mínimo de los tests puede no haber sys (hereda os.Environ).
		if rt.sys != nil {
			opts.envOver = rt.sys.envOverlay()
		}
		code, stdout, stderr, rerr := runBuffered(argv, opts)
		if rerr != nil {
			if errors.Is(rerr, errProcTimeout) {
				return nil, &vmwasm.StructuredError{Code: CodeETIMEOUT, Message: "nu.proc.run: el proceso excedió timeout_ms y fue terminado"}
			}
			return nil, mapProcStartErrorWasm(rerr)
		}
		return []any{map[string]any{
			"code":   int64(code),
			"stdout": stdout,
			"stderr": stderr,
		}}, nil
	})

	// nu.proc._spawn(argv, opts?) -> Proc (handle) — el wrapper nu.proc.spawn lo
	// envuelve. SÍNCRONA: arranca el proceso y devuelve el handle en el acto. Un fallo
	// de arranque → ENOENT/EACCES/EIO (no se devuelve un handle a medias); argv malo →
	// EINVAL. `opts.stdin` no aplica a spawn (el streaming es Proc:write): se ignora.
	p.Register("proc._spawn", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		argv, opts, err := parseProcArgsWasm(arg(args, 0), arg(args, 1))
		if err != nil {
			return nil, err
		}
		pr, serr := rt.spawnProc(argv, opts)
		if serr != nil {
			return nil, mapProcStartErrorWasm(serr)
		}
		return []any{inst.AllocHandle("Proc", pr)}, nil
	})

	// nu.proc.alive(pid) -> boolean — consulta inmediata (G17: existencia, no
	// identidad), SÍNCRONA (sin IO que esperar). Reusa pidAlive del núcleo.
	p.Register("proc.alive", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		return []any{pidAlive(argInt(args, 0))}, nil
	})

	// Proc:write(data) ⏸ — escribe a stdin en streaming (puede bloquear por
	// backpressure). Tras close_stdin (o sin stdin) → ECLOSED; otro fallo de IO → EIO.
	p.RegisterHandleMethod("Proc", "write", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		pr := val.(*luaProc)
		if err := pr.writeStdin([]byte(argString(args, 0))); err != nil {
			if errors.Is(err, errStdinClosed) {
				return nil, &vmwasm.StructuredError{Code: CodeECLOSED, Message: "Proc:write: stdin cerrado"}
			}
			return nil, &vmwasm.StructuredError{Code: CodeEIO, Message: "Proc:write: " + err.Error()}
		}
		return nil, nil
	})

	// Proc:read_line(which) -> string? ⏸ — una línea (con el \n) de stdout/stderr, o
	// nil en EOF (fin del stream). which inválido → EINVAL; fallo de lectura → EIO.
	p.RegisterHandleMethod("Proc", "read_line", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		pr := val.(*luaProc)
		which := argString(args, 0)
		r, mu, err := pr.reader(which)
		if err != nil {
			return nil, &vmwasm.StructuredError{Code: CodeEINVAL, Message: err.Error()}
		}
		line, eof, rerr := readLineFrom(mu, r)
		if rerr != nil {
			return nil, &vmwasm.StructuredError{Code: CodeEIO, Message: "Proc:read_line: " + rerr.Error()}
		}
		if eof {
			pr.noteEOF(which) // stream agotado: candidato a reap temprano (proc.go)
		}
		if eof && line == "" {
			return []any{nil}, nil // EOF sin datos: nil (§6)
		}
		return []any{line}, nil
	})

	// Proc:read(which, n?) -> string? ⏸ — lectura cruda: con n, hasta n bytes (puede
	// devolver menos); sin n, todo hasta EOF. nil en EOF sin datos. which inválido o n
	// negativo → EINVAL; fallo de lectura → EIO. El wrapper pasa n=nil cuando se omite,
	// y el wire descarta ese nil final, así que aquí `arg(args, 1)` es nil → lee todo.
	p.RegisterHandleMethod("Proc", "read", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		pr := val.(*luaProc)
		which := argString(args, 0)
		r, mu, err := pr.reader(which)
		if err != nil {
			return nil, &vmwasm.StructuredError{Code: CodeEINVAL, Message: err.Error()}
		}
		n := -1
		if arg(args, 1) != nil {
			n = argInt(args, 1)
			if n < 0 {
				return nil, &vmwasm.StructuredError{Code: CodeEINVAL, Message: "Proc:read: n no puede ser negativo"}
			}
		}
		data, eof, rerr := readFrom(mu, r, n)
		if rerr != nil {
			return nil, &vmwasm.StructuredError{Code: CodeEIO, Message: "Proc:read: " + rerr.Error()}
		}
		if eof {
			pr.noteEOF(which) // stream agotado: candidato a reap temprano (proc.go)
		}
		if eof && len(data) == 0 {
			return []any{nil}, nil // EOF sin datos: nil (§6)
		}
		return []any{string(data)}, nil
	})

	// Proc:wait() -> {code} ⏸ — espera a que el proceso termine y devuelve su código
	// de salida. Memoizado (waitOnce): varios wait ven el mismo desenlace, sin
	// re-esperar. El reaper de fondo (spawnProc) no compite: comparten el waitOnce.
	p.RegisterHandleMethod("Proc", "wait", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		code := val.(*luaProc).wait()
		return []any{map[string]any{"code": int64(code)}}, nil
	})

	// Proc:close_stdin() — cierra stdin (EOF a la entrada del proceso). Síncrono e
	// idempotente.
	p.RegisterHandleMethod("Proc", "close_stdin", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		val.(*luaProc).closeStdin()
		return nil, nil
	})

	// Proc:kill(signal?) — envía una señal (por defecto TERM). Síncrono (señalar es
	// inmediato; esperar la muerte es wait). Idempotente y best-effort (killed).
	p.RegisterHandleMethod("Proc", "kill", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		sig := syscall.SIGTERM // por defecto TERM (§6)
		// Si se da una señal, DEBE ser numérica. No usamos argInt aquí: degradaría
		// cualquier tipo no numérico (un string "KILL" incluido) a 0, y syscall.Signal(0)
		// es la sonda de existencia —no mata—, de modo que un kill con señal mal tipada
		// no mataría y tampoco daría error. Type-switch explícito: int64 o float64 con
		// valor entero; cualquier otra cosa → EINVAL accionable.
		if a := arg(args, 0); a != nil {
			switch v := a.(type) {
			case int64:
				sig = syscall.Signal(v)
			case float64:
				if iv := int64(v); float64(iv) == v {
					sig = syscall.Signal(iv)
				} else {
					return nil, &vmwasm.StructuredError{Code: CodeEINVAL, Message: "Proc:kill: la señal debe ser un entero (p. ej. 9 para KILL o 15 para TERM); el default sin argumento es TERM"}
				}
			default:
				return nil, &vmwasm.StructuredError{Code: CodeEINVAL, Message: "Proc:kill: la señal debe ser numérica (p. ej. 9 para KILL o 15 para TERM); el default sin argumento es TERM"}
			}
		}
		val.(*luaProc).killSignal(sig)
		return nil, nil
	})

	// Wrapper Lua: nu.proc.spawn envuelve el handle de proc._spawn y le cuelga los
	// seis métodos de §6 —los de IO por __hcall_s (⏸), close_stdin/kill por __hcall
	// (síncronos)—. Mismo patrón que nu.ws.connect (vmwasm_ws.go).
	p.AddPreludio(`
nu.proc = nu.proc or {}
function nu.proc.spawn(argv, opts)
  local p = nu.proc._spawn(argv, opts)   -- handle {__id}
  p.write       = function(self, data)     return __hcall_s(self.__id, "write", data) end
  p.read_line   = function(self, which)    return __hcall_s(self.__id, "read_line", which) end
  p.read        = function(self, which, n) return __hcall_s(self.__id, "read", which, n) end
  p.wait        = function(self)           return __hcall_s(self.__id, "wait") end
  p.close_stdin = function(self)           return __hcall(self.__id, "close_stdin") end
  p.kill        = function(self, sig)      return __hcall(self.__id, "kill", sig) end
  return p
end`)
}

// parseProcArgsWasm valida y extrae (argv, opts) del wire de nu.proc.run/spawn (§6).
// Gemelo VM-agnóstico de parseProcArgs (el backend gopher): argv obligatorio (array
// no vacío de strings; sin ejecutable no hay proceso → EINVAL), y opts? con cwd/env/
// stdin/timeout_ms. Reproduce la MISMA permisividad que gopher: un opts no-tabla, un
// env no-tabla, una entrada de env no-string o un timeout_ms no-numérico se ignoran
// en silencio (no lanzan); sólo un argv malo o un timeout_ms negativo → EINVAL.
func parseProcArgsWasm(argvArg, optsArg any) ([]string, procOpts, error) {
	arr, ok := argvArg.([]any)
	if !ok || len(arr) == 0 {
		return nil, procOpts{}, einvalProc("argv debe ser un array no vacío (argv[0] es el ejecutable)")
	}
	argv := make([]string, len(arr))
	for i, v := range arr {
		s, ok := v.(string)
		if !ok {
			return nil, procOpts{}, einvalProc("cada elemento de argv debe ser un string")
		}
		argv[i] = s
	}

	var opts procOpts
	if o, ok := optsArg.(map[string]any); ok {
		if v, ok := o["cwd"].(string); ok {
			opts.cwd = v
		}
		// `env`: una tabla { K = V } se traduce a ["K=V", ...]. Presente (aunque vacía)
		// REEMPLAZA el entorno heredado (§6); ausente lo hereda (con el overlay de
		// setenv por encima, ver mergedEnv). Sólo se toman las entradas string→string.
		if envm, ok := o["env"].(map[string]any); ok {
			opts.env = []string{}
			for k, val := range envm {
				if s, ok := val.(string); ok {
					opts.env = append(opts.env, k+"="+s)
				}
			}
		}
		if v, ok := o["stdin"].(string); ok {
			opts.stdin = []byte(v)
			opts.hasStdin = true
		}
		if tv, present := o["timeout_ms"]; present && tv != nil {
			if tm, ok := httpNum(tv); ok {
				if tm < 0 {
					return nil, procOpts{}, einvalProc("timeout_ms no puede ser negativo")
				}
				opts.timeout = time.Duration(tm) * time.Millisecond
			}
		}
	}
	return argv, opts, nil
}

func einvalProc(msg string) error {
	return &vmwasm.StructuredError{Code: CodeEINVAL, Message: "nu.proc: " + msg}
}

// mapProcStartErrorWasm traduce el error de arrancar un proceso (cmd.Start / los
// pipes) al error estructurado de la frontera, mismo mapeo §1.4 que mapProcStartError
// del backend gopher: ejecutable inexistente → ENOENT (cubre os.ErrNotExist de una
// ruta absoluta y exec.ErrNotFound de la búsqueda en PATH), sin permiso → EACCES,
// cualquier otro fallo → EIO.
func mapProcStartErrorWasm(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		return &vmwasm.StructuredError{Code: CodeENOENT, Message: err.Error()}
	case errors.Is(err, os.ErrPermission):
		return &vmwasm.StructuredError{Code: CodeEACCES, Message: err.Error()}
	default:
		return &vmwasm.StructuredError{Code: CodeEIO, Message: err.Error()}
	}
}
