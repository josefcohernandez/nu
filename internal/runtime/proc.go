package runtime

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// `nu.proc` — subprocesos (api.md §6, sesión S16, inventario 🔒). Lanza procesos
// del sistema y se comunica con ellos. Dos niveles, como el contrato:
//
//   - `nu.proc.run(argv, opts?) -> {code, stdout, stderr}` ⏸ — conveniencia con
//     buffers: lanza, alimenta `stdin`, recoge `stdout`/`stderr` enteros, espera y
//     devuelve el código de salida. Un `code != 0` NO lanza (es dato, como el
//     `status` de `nu.http`); `timeout_ms` excedido mata el proceso y lanza
//     `ETIMEOUT`.
//   - `nu.proc.spawn(argv, opts?) -> Proc` — control fino con streams. NO es ⏸
//     (lanzar no bloquea: devuelve el handle al arrancar). De ahí cuelgan
//     `write`/`close_stdin`, `read_line`/`read`, `wait`/`kill` (los IO son ⏸;
//     `close_stdin`/`kill` son síncronos).
//
// SIN SHELL IMPLÍCITA (§6). `argv` es un **array**: `argv[0]` es el ejecutable y el
// resto sus argumentos, pasados tal cual al SO (`exec.Command`). Nadie invoca
// `/bin/sh` por ti —`run(["echo","$HOME"])` imprime el literal `$HOME`, no expande—:
// quien quiera una shell la pone explícitamente en `argv` (`["sh","-c","..."]`). Es
// la decisión de seguridad de toda herramienta seria: la inyección por shell no
// existe si no hay shell.
//
// VIDA DEL PROCESO (la lógica 🔒, §6). La regla es **matarlo explícitamente** vía
// `nu.task.cleanup` en quien lo crea: una task que hace `spawn` registra
// `nu.task.cleanup(function() proc:kill() end)`, de modo que al terminar la task
// —éxito, error o **cancelación** (S08)— el proceso muere con ella. Como **red de
// seguridad** (no como la vía principal), un `Proc` que se queda sin referencias en
// Lua acaba matado por el GC vía `runtime.SetFinalizer` —**no determinista**: no se
// debe confiar en ello, es solo el seguro contra una fuga si alguien olvidó el
// `cleanup`—. Y `Runtime.Close` mata todos los procesos vivos al cerrar.
//
// EL IO VA POR EL PUENTE ⏸ (S04, ADR-011). `write`/`read_line`/`read`/`wait` hacen
// su IO **bloqueante** (leer del pipe, esperar al proceso) en la goroutine de fondo
// de `suspend`, que **JAMÁS toca Lua**: los bytes cruzan a Lua solo en la
// `deliverFn`, con el token recuperado. Así una task que lee de un subproceso lento
// no congela el loop —otras progresan—. Es el mismo patrón de `nu.fs` (S14).
//
// `nu.proc.alive(pid) -> boolean` (G17) informa de **existencia, no de identidad**:
// pregunta "¿hay algún proceso vivo con este pid?", y un pid reciclado por el SO da
// `true` aunque sea otro proceso. Es la pieza para detectar locks de sesión
// huérfanos (sesiones.md §6): un lock cuyo pid ya no existe es huérfano. NO es ⏸
// (consulta inmediata, sin IO que esperar).

// procTypeName identifica la metatabla del handle `Proc` (lo que devuelve `spawn`),
// de la que cuelgan `write`/`close_stdin`/`read_line`/`read`/`wait`/`kill`.
const procTypeName = "nu.proc.Proc"

// luaProc es el handle Go detrás del userdata `Proc`. Envuelve el `*exec.Cmd` ya
// arrancado y sus tres pipes (stdin escribible, stdout/stderr leíbles). El IO de
// los streams corre en goroutines de fondo (sin token), así que el estado que esas
// goroutines tocan va protegido por `mu` —no por el token—.
type luaProc struct {
	s   *scheduler
	cmd *exec.Cmd

	// EL REPARTO DE CANDADOS es la decisión delicada de S16 (claude_decisions.md). El
	// IO de un `Proc` (escribir/leer/esperar) **bloquea** en goroutines de fondo
	// (sin token); `kill` corre síncrono (con token) y DEBE poder interrumpir ese IO
	// —el patrón de vida del proceso es "cleanup mata el proceso colgado para que su
	// read/wait pendiente se desbloquee"—. Si `kill` y el IO compartieran un candado,
	// matar a un proceso del que se está leyendo se DEADLOCKearía: el lock lo tendría
	// el read bloqueado, y kill esperaría a un lock que solo se suelta cuando el
	// proceso muera... que es lo que kill intenta lograr. Por eso `kill` usa un
	// candado **propio** (`killMu`), nunca tomado durante una operación bloqueante.

	// stdinMu protege `stdin`/`stdinClosed`. Se toma para LEER la referencia al
	// writer y los flags, pero el `Write` real (que puede bloquear por backpressure)
	// se hace con el candado SOLTADO.
	stdinMu     sync.Mutex
	stdin       io.WriteCloser // tubo a stdin del proceso; nil si no se pidió stdin
	stdinClosed bool           // close_stdin ya se llamó (idempotente)

	// stdoutMu/stderrMu serializan el acceso a cada lector frente a dos lecturas
	// concurrentes del MISMO stream (bufio.Reader no es seguro en paralelo). Uno por
	// stream: leer stdout y stderr a la vez (lo normal) no se serializa entre sí. El
	// `Read` real se hace bajo el candado del stream —es aceptable: dos lectores del
	// mismo stream compiten de todos modos, y `kill` (otro candado) puede romper el
	// bloqueo cerrando el proceso—.
	stdoutMu sync.Mutex
	stderrMu sync.Mutex
	stdout   *bufio.Reader // lector bufferizado de stdout; permite read_line sin perder bytes
	stderr   *bufio.Reader
	// rOut/rErr son los extremos de LECTURA de los pipes (propios, vía `os.Pipe`): los
	// cerramos al derribar el `Proc` (`reapAndClose`) para no fugar descriptores. El
	// hijo tiene los extremos de escritura; `Wait` no toca estos de lectura.
	rOut *os.File
	rErr *os.File

	// waitOnce garantiza que `cmd.Wait` se llama UNA sola vez (es ilegal llamarlo dos
	// veces: libera los recursos del proceso). El resultado se publica en `waitErr`/
	// `code` y los `wait` posteriores esperan a `waitDone` y leen ese resultado —sin
	// re-esperar y, crucialmente, **sin sostener un candado durante la espera
	// bloqueante** (de ahí el `chan`, no un `Mutex`)—.
	waitOnce sync.Once
	waitDone chan struct{} // se cierra cuando cmd.Wait retornó
	waitErr  error         // resultado de cmd.Wait (nil = salió con 0; *ExitError = código)
	code     int           // código de salida una vez esperado

	// killMu + killed: candado PROPIO de kill (ver arriba), nunca tomado durante IO.
	// `killed` evita re-señalar un proceso ya señalado —tras `Wait`, su pid podría
	// reciclarse y re-señalarlo mataría a OTRO proceso—. Idempotente.
	killMu sync.Mutex
	killed bool

	// ownerName es el dueño con que se etiquetó el proc al crearse (`currentOwner()`
	// vigente en `spawn`, S11): `nu.plugin.reload` (S13, G2) mata exactamente los
	// procesos de ESE plugin —un `spawn` de su `init.lua` no debe sobrevivir a la
	// recarga, "reload no deja handlers huérfanos"—.
	ownerName string
}

// luaProc implementa ownedHandle (S13): el registro de handles por dueño
// (handles.go) lo mata al recargar su plugin sin conocer su tipo concreto, igual
// que a un `*luaTimer` o un `*luaWatcher`. El reload de G2 ("suelta todos los
// handles del plugin") incluye sus subprocesos: un `spawn` que un plugin dejó
// corriendo no debe sobrevivir a recargarlo.

// release derriba el proceso (best-effort): lo mata (SIGKILL) y cierra los extremos
// de lectura de sus pipes. Lo llama `releaseOwnerHandles` (vía `reload`). Como es un
// derribo forzado (recarga del plugin), no importa perder salida bufferizada. El
// reaper de fondo recoge el proceso muerto. Idempotente vía `killed`/cierres. NO toca
// el registro de handles (eso lo orquesta `releaseOwnerHandles`, que ya vació la
// lista del dueño).
func (p *luaProc) release() {
	p.killSignal(syscall.SIGKILL)
	p.closeReadPipes()
}

// owner devuelve el dueño con que se etiquetó el proc al crearse.
func (p *luaProc) owner() string { return p.ownerName }

// registerProc cuelga `nu.proc` del global `nu` con sus firmas de §6, e instala la
// metatabla del tipo `Proc`. Lo llama `registerNu` (nu.go). Como el resto de IO,
// `proc` es [W] (§16: disponible en workers; los workers son S34, así que hoy se
// registra en el estado principal).
func (rt *Runtime) registerProc(nu *lua.LTable) {
	L := rt.L

	mt := L.NewTypeMetatable(procTypeName)
	index := L.NewTable()
	index.RawSetString("write", L.NewFunction(rt.procWrite))
	index.RawSetString("close_stdin", L.NewFunction(rt.procCloseStdin))
	index.RawSetString("read_line", L.NewFunction(rt.procReadLine))
	index.RawSetString("read", L.NewFunction(rt.procRead))
	index.RawSetString("wait", L.NewFunction(rt.procWait))
	index.RawSetString("kill", L.NewFunction(rt.procKill))
	L.SetField(mt, "__index", index)

	proc := L.NewTable()
	proc.RawSetString("run", L.NewFunction(rt.procRun))
	proc.RawSetString("spawn", L.NewFunction(rt.procSpawn))
	proc.RawSetString("alive", L.NewFunction(rt.procAlive))
	nu.RawSetString("proc", proc)
}

// procOpts recoge las opciones de `run`/`spawn` (§6): `cwd`, `env`, `stdin`,
// `timeout_ms`. Se extraen del segundo argumento (una tabla) en el estado principal,
// bajo el token; luego cruzan a la goroutine de fondo como datos Go puros.
type procOpts struct {
	cwd      string
	env      []string // "K=V" como exige exec.Cmd; nil = hereda el del proceso, no-nil (aun vacío) = control total (§6)
	stdin    []byte   // datos para alimentar stdin (solo `run`); nil = sin stdin
	hasStdin bool
	timeout  time.Duration // 0 = sin límite

	// envOver es la **foto del overlay de `setenv`** (`nu.sys.setenv`, S17),
	// tomada en el estado principal bajo el token al entrar en `run`/`spawn`. Son
	// las variables que afectan a los subprocesos FUTUROS (§7): se aplican al
	// construir el entorno del hijo (`mergedEnv`). Tomarla aquí —no en la
	// goroutine de fondo— fija de forma determinista qué `setenv` ve este lanzado:
	// los que ocurrieron ANTES de la llamada, no los de después. nil = sin overlay.
	envOver map[string]string
}

// parseProcArgs valida y extrae `argv` (1.er arg, array no vacío de strings) y las
// `opts` (2.º arg, tabla opcional) de una llamada a `run`/`spawn`. Lanza `EINVAL`
// si `argv` no es un array de strings o está vacío: sin ejecutable no hay proceso.
// Corre en el estado principal bajo el token (toca Lua), antes de cualquier IO.
func parseProcArgs(L *lua.LState) ([]string, procOpts, bool) {
	tbl := L.CheckTable(1)
	n := tbl.Len()
	if n == 0 {
		raiseError(L, CodeEINVAL, "nu.proc: argv debe ser un array no vacío (argv[0] es el ejecutable)", lua.LNil)
		return nil, procOpts{}, false
	}
	argv := make([]string, n)
	for i := 1; i <= n; i++ {
		s, ok := tbl.RawGetInt(i).(lua.LString)
		if !ok {
			raiseError(L, CodeEINVAL, "nu.proc: cada elemento de argv debe ser un string", lua.LNil)
			return nil, procOpts{}, false
		}
		argv[i-1] = string(s)
	}

	var opts procOpts
	if o, ok := L.Get(2).(*lua.LTable); ok {
		if v, ok := o.RawGetString("cwd").(lua.LString); ok {
			opts.cwd = string(v)
		}
		// `env`: una tabla `{ K = V }` se traduce a `["K=V", ...]`. Presente (aunque
		// vacía) REEMPLAZA el entorno heredado —env explícito = control total—; ausente
		// lo hereda (env nil → exec.Cmd usa `os.Environ`). El overlay de `nu.sys.setenv`
		// (S17) queda POR DEBAJO de un `env` explícito (ver `mergedEnv`): con `env`
		// presente, el overlay no se aplica; sin él, el overlay pisa lo heredado.
		if envTbl, ok := o.RawGetString("env").(*lua.LTable); ok {
			opts.env = []string{}
			envTbl.ForEach(func(k, val lua.LValue) {
				ks, kok := k.(lua.LString)
				vs, vok := val.(lua.LString)
				if kok && vok {
					opts.env = append(opts.env, string(ks)+"="+string(vs))
				}
			})
		}
		if v, ok := o.RawGetString("stdin").(lua.LString); ok {
			opts.stdin = []byte(v)
			opts.hasStdin = true
		}
		if v, ok := o.RawGetString("timeout_ms").(lua.LNumber); ok {
			if v < 0 {
				raiseError(L, CodeEINVAL, "nu.proc: timeout_ms no puede ser negativo", lua.LNil)
				return nil, procOpts{}, false
			}
			opts.timeout = time.Duration(v) * time.Millisecond
		}
	}
	return argv, opts, true
}

// newCmd construye un `*exec.Cmd` a partir de `argv` y `opts`, **sin arrancarlo**.
// No toca Lua (solo datos Go), así que es seguro llamarlo desde la goroutine de
// fondo de `run` o desde el estado principal de `spawn`. La ausencia de shell es
// estructural: `exec.Command(argv[0], argv[1:]...)` pasa los argumentos al SO sin
// interpretación.
func newCmd(argv []string, opts procOpts) *exec.Cmd {
	cmd := exec.Command(argv[0], argv[1:]...)
	if opts.cwd != "" {
		cmd.Dir = opts.cwd
	}
	if env := mergedEnv(opts); env != nil {
		cmd.Env = env
	}
	return cmd
}

// mergedEnv construye el entorno del subproceso combinando, **por precedencia de
// menor a mayor** (la integración S16↔S17, §6/§7): entorno heredado del SO <
// overlay de `nu.sys.setenv` (`opts.envOver`) < `opts.env` explícito de la
// llamada. La regla cumple las dos semánticas a la vez:
//
//   - `setenv` afecta a los subprocesos futuros (§7): el overlay PISA lo heredado
//     —si `setenv("X","42")` y el SO no tenía `X`, el hijo ve `X=42`; si lo tenía,
//     gana el overlay—.
//   - `opts.env` explícito es **control total por llamada** (§6): lo más local
//     manda. Quien pasa `env` en ESA invocación decide esas claves por encima del
//     overlay (p. ej. para AISLAR un subproceso de un `setenv` previo).
//
// Devuelve nil (= heredar `os.Environ` tal cual) **solo** cuando no hay ni
// overlay ni `opts.env` —el caso común, sin coste—. Si hay overlay pero no
// `opts.env`, parte de `os.Environ()` y le superpone el overlay. Si hay
// `opts.env` (aunque vacío), parte de él (NO de `os.Environ`: `env` explícito
// reemplaza el heredado) y le superpone... nada por encima salvo el propio
// `opts.env`: el overlay queda DEBAJO, así que `opts.env` gana clave a clave.
func mergedEnv(opts procOpts) []string {
	envSet := opts.env != nil // no-nil (aun vacío) = `opts.env` pasado explícitamente (§6)
	if !envSet && len(opts.envOver) == 0 {
		return nil // ni overlay ni env explícito: hereda os.Environ() sin cambios
	}

	// Base por la que empezar (la capa más baja presente): `opts.env` si es
	// explícito (reemplaza lo heredado, §6), si no el entorno real del proceso.
	var base []string
	if envSet {
		base = opts.env
	} else {
		base = os.Environ()
	}

	// Índice clave→posición para superponer sin duplicar claves. exec.Cmd usa la
	// ÚLTIMA aparición de una clave repetida, pero mantenemos una sola entrada por
	// clave para un entorno limpio y determinista.
	out := make([]string, 0, len(base)+len(opts.envOver))
	idx := make(map[string]int, len(base)+len(opts.envOver))
	put := func(k, v string) {
		entry := k + "=" + v
		if i, ok := idx[k]; ok {
			out[i] = entry
			return
		}
		idx[k] = len(out)
		out = append(out, entry)
	}
	for _, kv := range base {
		k, v, _ := splitEnv(kv)
		put(k, v)
	}

	// El overlay de `setenv` se superpone SOBRE el entorno heredado del SO, pero
	// **por debajo** de un `opts.env` explícito: si la llamada pasó `env`, ese es
	// la capa más alta y el overlay no debe pisarlo. Por eso el overlay solo se
	// aplica encima del entorno heredado (`!envSet`); con `opts.env` explícito,
	// `base` ya es la capa ganadora y el overlay se ignora (lo más local manda).
	if !envSet {
		for k, v := range opts.envOver {
			put(k, v)
		}
	}
	return out
}

// splitEnv parte una entrada "K=V" del entorno en clave y valor por el PRIMER
// `=` (un valor puede contener `=`). Una entrada sin `=` (rara, pero posible en
// algunos SO) se trata como clave con valor vacío.
func splitEnv(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}

// mapProcStartError traduce el error de arrancar un proceso (`cmd.Start`) a un
// código §1.4 y lo lanza: ejecutable inexistente → `ENOENT`, sin permiso de
// ejecución → `EACCES`, cualquier otro fallo → `EIO`. Cubre los dos modos en que
// "no existe el ejecutable" se reporta: `os.ErrNotExist` (una ruta absoluta que no
// está) y `exec.ErrNotFound` ("executable file not found in $PATH", cuando se busca
// por nombre en el PATH) —ambos son, para el usuario, "ese binario no existe"—.
func mapProcStartError(L *lua.LState, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		raiseError(L, CodeENOENT, err.Error(), lua.LNil)
	case errors.Is(err, os.ErrPermission):
		raiseError(L, CodeEACCES, err.Error(), lua.LNil)
	default:
		raiseError(L, CodeEIO, err.Error(), lua.LNil)
	}
}

// exitCode extrae el código de salida del resultado de `cmd.Wait`. nil → 0 (salió
// limpio); un `*exec.ExitError` lleva el código del SO; otro error (fallo al
// esperar) se rinde como -1 (no debería ocurrir en la práctica). Un proceso matado
// por señal da un código negativo o >128 según el SO; lo dejamos tal cual lo
// reporta `ExitCode` —el contrato solo promete "code", su valor exacto para señales
// es del SO—.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// --- nu.proc.run --------------------------------------------------------------

// procRun implementa `nu.proc.run(argv, opts?) -> {code, stdout, stderr}` ⏸ (§6):
// la conveniencia con buffers. Lanza el proceso, le alimenta `opts.stdin` (si lo
// hay), recoge stdout/stderr **enteros** en memoria, espera a que termine y
// devuelve `{code, stdout, stderr}`. TODO el ciclo (start + IO + wait) va en la
// goroutine de fondo del puente ⏸: no toca Lua hasta la `deliverFn`.
//
// `code != 0` **no lanza** (es dato: un `grep` sin coincidencias sale con 1 y eso es
// información, no un fallo del runtime). Lo que SÍ lanza: que el ejecutable no exista
// o no se pueda arrancar (`ENOENT`/`EACCES`/`EIO`), o que `timeout_ms` se exceda
// (`ETIMEOUT`, tras matar el proceso).
func (rt *Runtime) procRun(L *lua.LState) int {
	if !rt.requireTask(L, "nu.proc.run") {
		return 0
	}
	argv, opts, ok := parseProcArgs(L)
	if !ok {
		return 0
	}
	// Foto del overlay de `nu.sys.setenv` (S17), tomada AQUÍ —estado principal, bajo
	// el token— para fijar de forma determinista qué `setenv` ve este subproceso:
	// los anteriores a la llamada (§7, "afecta solo a subprocesos futuros").
	opts.envOver = rt.sys.envOverlay()

	vals := rt.sched.suspend(L, func() deliverFn {
		code, stdout, stderr, rerr := runBuffered(argv, opts)
		return func(L *lua.LState) []lua.LValue {
			if rerr != nil {
				if errors.Is(rerr, errProcTimeout) {
					raiseError(L, CodeETIMEOUT, "nu.proc.run: el proceso excedió timeout_ms y fue terminado", lua.LNil)
				} else {
					mapProcStartError(L, rerr)
				}
				return nil
			}
			res := L.NewTable()
			res.RawSetString("code", lua.LNumber(code))
			res.RawSetString("stdout", lua.LString(stdout))
			res.RawSetString("stderr", lua.LString(stderr))
			return []lua.LValue{res}
		}
	})
	return pushAll(L, vals)
}

// errProcTimeout es el centinela interno que `runBuffered` devuelve cuando mató el
// proceso por exceder `timeout_ms`. `procRun` lo distingue para lanzar `ETIMEOUT`
// (no `EIO`).
var errProcTimeout = errors.New("nu.proc: timeout")

// runBuffered ejecuta el ciclo completo de `run` **fuera del token** (no toca Lua):
// arranca, alimenta stdin, espera con captura de stdout/stderr y aplica el timeout.
// Devuelve `(code, stdout, stderr, err)`; `err != nil` solo para fallos de arranque
// o timeout (un `code != 0` es retorno normal). Usa los buffers de `exec.Cmd`
// (`Stdout`/`Stderr` a un `bytes.Buffer`) en vez de pipes: para `run` no hay
// streaming, así que el camino simple es el correcto.
func runBuffered(argv []string, opts procOpts) (int, string, string, error) {
	cmd := newCmd(argv, opts)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if opts.hasStdin {
		cmd.Stdin = bytes.NewReader(opts.stdin)
	}

	if err := cmd.Start(); err != nil {
		return 0, "", "", err
	}

	// Espera con timeout opcional. `cmd.Wait` en una goroutine; el `select` arbitra
	// entre su fin y el plazo. Al expirar, se mata el proceso (SIGKILL para que no lo
	// ignore) y se DRENA el `Wait` —si no, sus pipes y su goroutine quedarían
	// colgados—; entonces se devuelve el centinela de timeout.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if opts.timeout > 0 {
		timer := time.NewTimer(opts.timeout)
		defer timer.Stop()
		select {
		case werr := <-done:
			return exitCode(werr), outBuf.String(), errBuf.String(), nil
		case <-timer.C:
			_ = cmd.Process.Kill()
			<-done // drena el Wait del proceso ya muerto (libera recursos)
			return 0, outBuf.String(), errBuf.String(), errProcTimeout
		}
	}

	werr := <-done
	return exitCode(werr), outBuf.String(), errBuf.String(), nil
}

// --- nu.proc.spawn ------------------------------------------------------------

// procSpawn implementa `nu.proc.spawn(argv, opts?) -> Proc` (§6): control fino con
// streams. **NO es ⏸**: arranca el proceso y devuelve el handle en el acto, sin
// suspender —spawnear no bloquea—. El IO posterior (`write`/`read*`/`wait`) sí
// suspende, sobre los pipes de este `Proc`.
//
// Se arranca con `cmd.Start()` (síncrono y barato: hace `fork`/`exec`, no espera al
// proceso). Los pipes se montan ANTES de `Start`. Un fallo de arranque lanza
// `ENOENT`/`EACCES`/`EIO` —no se devuelve un handle a medias—.
//
// `opts.stdin` (datos prefijados) no aplica a `spawn`: el streaming es vía
// `Proc:write`; si se pasa, se ignora (el contrato de `spawn` es streams, no un
// volcado inicial).
func (rt *Runtime) procSpawn(L *lua.LState) int {
	argv, opts, ok := parseProcArgs(L)
	if !ok {
		return 0
	}
	// Foto del overlay de `nu.sys.setenv` (S17): el subproceso ve los `setenv`
	// previos a este `spawn` (§7). `spawn` corre en el estado principal bajo el
	// token, así que esta lectura no compite con un `setenv` concurrente.
	opts.envOver = rt.sys.envOverlay()

	cmd := newCmd(argv, opts)

	// Pipes MANUALES para stdout/stderr (`os.Pipe`), no `cmd.StdoutPipe`. La razón es
	// el reaper de fondo (abajo): `cmd.StdoutPipe`/`StderrPipe` cierran el extremo de
	// LECTURA en cuanto `cmd.Wait` ve salir al proceso —`os/exec` lo documenta: "es
	// incorrecto llamar a Wait antes de que todas las lecturas del pipe hayan
	// terminado"—, lo que perdería datos si reapeamos en cuanto el proceso muere. Con
	// pipes propios el extremo de lectura es NUESTRO: `Wait` no lo toca; lo cerramos al
	// derribar el `Proc`. Así reaping y streaming quedan desacoplados. Para stdin sí
	// vale `StdinPipe` (es de escritura; `close_stdin` lo cierra a mano).
	stdin, err := cmd.StdinPipe()
	if err != nil {
		mapProcStartError(L, err)
		return 0
	}
	rOut, wOut, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		mapProcStartError(L, err)
		return 0
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = rOut.Close()
		_ = wOut.Close()
		mapProcStartError(L, err)
		return 0
	}
	cmd.Stdout = wOut
	cmd.Stderr = wErr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = rOut.Close()
		_ = wOut.Close()
		_ = rErr.Close()
		_ = wErr.Close()
		mapProcStartError(L, err)
		return 0
	}
	// Tras `Start`, el hijo ya tiene su copia de los extremos de escritura; cerramos
	// los NUESTROS para que, cuando el hijo termine y cierre los suyos, los lectores
	// vean EOF (si no, el pipe nunca daría EOF porque el padre seguiría con el extremo
	// de escritura abierto).
	_ = wOut.Close()
	_ = wErr.Close()

	p := &luaProc{
		s:         rt.sched,
		cmd:       cmd,
		stdin:     stdin,
		rOut:      rOut,
		rErr:      rErr,
		stdout:    bufio.NewReader(rOut),
		stderr:    bufio.NewReader(rErr),
		waitDone:  make(chan struct{}),
		ownerName: rt.currentOwner(),
	}

	rt.sched.trackProc(p)
	rt.sched.track(p) // registro de handles por dueño (S13): que `reload` lo mate

	// Reaper de fondo: una goroutine que llama a `cmd.Wait` (vía `p.wait`, idempotente
	// con `waitOnce`) en cuanto el proceso muera. SIN ella, un proceso al que se
	// `kill`-ó pero que nadie esperó quedaría **zombi** —terminado pero no recogido—,
	// y `alive(pid)` lo reportaría vivo para siempre (un zombi responde a `kill -0`).
	// Con ella, un `spawn` sin `wait` no fuga zombis: matarlo (por `cleanup`, `kill`,
	// `reload` o `Close`) lo recoge. Un `Proc:wait()` del usuario no compite: si el
	// reaper ganó el `waitOnce`, `wait` espera a `waitDone` y lee el código publicado.
	go p.wait()

	// Red de seguridad GC (§6): si el `Proc` se queda sin referencias en Lua sin que
	// nadie lo matara (ni `cleanup`, ni `kill`, ni `Close`), el finalizer lo mata al
	// recolectarlo. **No determinista** —no se debe confiar en ello, es el seguro
	// contra una fuga, no la vía de vida del proceso (esa es `cleanup`)—.
	runtime.SetFinalizer(p, func(p *luaProc) { p.killSignal(syscall.SIGKILL) })

	ud := L.NewUserData()
	ud.Value = p
	L.SetMetatable(ud, L.GetTypeMetatable(procTypeName))
	L.Push(ud)
	return 1
}

// checkProc recupera el `*luaProc` del userdata `self` del primer argumento de un
// método de `Proc`. Lanza `EINVAL` si no es un handle de `Proc`.
func checkProc(L *lua.LState) *luaProc {
	ud := L.CheckUserData(1)
	p, ok := ud.Value.(*luaProc)
	if !ok {
		raiseError(L, CodeEINVAL, "Proc: se esperaba un handle de Proc", lua.LNil)
		return nil
	}
	return p
}

// procWrite implementa `Proc:write(data)` ⏸ (§6): escribe `data` al stdin del
// proceso en streaming. Es ⏸ porque escribir puede **bloquearse** (backpressure: si
// el proceso no lee su stdin, el buffer del pipe se llena y el `Write` espera). La
// escritura va en la goroutine de fondo; el token queda libre mientras tanto. Tras
// `close_stdin` (o si nunca hubo stdin), escribir lanza `ECLOSED`.
func (rt *Runtime) procWrite(L *lua.LState) int {
	if !rt.requireTask(L, "Proc:write") {
		return 0
	}
	p := checkProc(L)
	if p == nil {
		return 0
	}
	data := []byte(L.CheckString(2))

	vals := rt.sched.suspend(L, func() deliverFn {
		err := p.writeStdin(data)
		return func(L *lua.LState) []lua.LValue {
			if err != nil {
				if errors.Is(err, errStdinClosed) {
					raiseError(L, CodeECLOSED, "Proc:write: stdin cerrado", lua.LNil)
				} else {
					raiseError(L, CodeEIO, "Proc:write: "+err.Error(), lua.LNil)
				}
			}
			return nil
		}
	})
	return pushAll(L, vals)
}

// errStdinClosed lo devuelve `writeStdin` cuando stdin ya se cerró (o nunca existió):
// `procWrite` lo rinde como `ECLOSED`.
var errStdinClosed = errors.New("nu.proc: stdin cerrado")

// writeStdin escribe al pipe de stdin **fuera del token** (lo llama la goroutine de
// fondo de `procWrite`), de ahí el candado: protege `stdin`/`stdinClosed` frente a
// un `close_stdin` síncrono concurrente. El `Write` real se hace con el candado
// SOLTADO para no congelar un `close_stdin` mientras un write largo está en
// backpressure —se toma una referencia al writer bajo el candado y se escribe
// fuera—.
func (p *luaProc) writeStdin(data []byte) error {
	p.stdinMu.Lock()
	if p.stdin == nil || p.stdinClosed {
		p.stdinMu.Unlock()
		return errStdinClosed
	}
	w := p.stdin
	p.stdinMu.Unlock()

	_, err := w.Write(data)
	return err
}

// procCloseStdin implementa `Proc:close_stdin()` (§6): cierra el stdin del proceso,
// señalándole EOF en su entrada (un `cat` que lee hasta EOF termina aquí). **NO es
// ⏸** (cerrar un pipe es inmediato). Idempotente: cerrar dos veces es inocuo.
func (rt *Runtime) procCloseStdin(L *lua.LState) int {
	p := checkProc(L)
	if p == nil {
		return 0
	}
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.stdin != nil && !p.stdinClosed {
		_ = p.stdin.Close()
		p.stdinClosed = true
	}
	return 0
}

// procReadLine implementa `Proc:read_line(which) -> string?` ⏸ (§6): lee una línea
// (hasta `\n` incluido, o el resto antes de EOF) del stream `which`
// (`"stdout"`/`"stderr"`). Devuelve `nil` en **EOF** (el stream se cerró sin más
// datos): es la señal de "no hay más", con la que un bucle `while line do ... end`
// termina limpio. El IO bloqueante va en la goroutine de fondo.
func (rt *Runtime) procReadLine(L *lua.LState) int {
	if !rt.requireTask(L, "Proc:read_line") {
		return 0
	}
	p := checkProc(L)
	if p == nil {
		return 0
	}
	which := L.CheckString(2)
	r, mu, err := p.reader(which)
	if err != nil {
		raiseError(L, CodeEINVAL, err.Error(), lua.LNil)
		return 0
	}

	vals := rt.sched.suspend(L, func() deliverFn {
		line, eof, rerr := readLineFrom(mu, r)
		return func(L *lua.LState) []lua.LValue {
			if rerr != nil {
				raiseError(L, CodeEIO, "Proc:read_line: "+rerr.Error(), lua.LNil)
				return nil
			}
			if eof && line == "" {
				return []lua.LValue{lua.LNil} // EOF sin datos: nil (§6)
			}
			return []lua.LValue{lua.LString(line)}
		}
	})
	return pushAll(L, vals)
}

// procRead implementa `Proc:read(which, n?) -> string?` ⏸ (§6): lectura **cruda** de
// `which`. Con `n`, lee hasta `n` bytes (puede devolver menos: lo que haya
// disponible). Sin `n`, lee **todo** hasta EOF. Devuelve `nil` en EOF (sin datos),
// igual que `read_line`.
func (rt *Runtime) procRead(L *lua.LState) int {
	if !rt.requireTask(L, "Proc:read") {
		return 0
	}
	p := checkProc(L)
	if p == nil {
		return 0
	}
	which := L.CheckString(2)
	r, mu, err := p.reader(which)
	if err != nil {
		raiseError(L, CodeEINVAL, err.Error(), lua.LNil)
		return 0
	}
	n := -1
	if v, ok := L.Get(3).(lua.LNumber); ok {
		n = int(v)
		if n < 0 {
			raiseError(L, CodeEINVAL, "Proc:read: n no puede ser negativo", lua.LNil)
			return 0
		}
	}

	vals := rt.sched.suspend(L, func() deliverFn {
		data, eof, rerr := readFrom(mu, r, n)
		return func(L *lua.LState) []lua.LValue {
			if rerr != nil {
				raiseError(L, CodeEIO, "Proc:read: "+rerr.Error(), lua.LNil)
				return nil
			}
			if eof && len(data) == 0 {
				return []lua.LValue{lua.LNil} // EOF sin datos: nil (§6)
			}
			return []lua.LValue{lua.LString(data)}
		}
	})
	return pushAll(L, vals)
}

// reader devuelve el `*bufio.Reader` del stream `which` y el candado que lo
// serializa (`stdoutMu`/`stderrMu`). Valida el nombre (`stdout`/`stderr`); cualquier
// otro es `EINVAL`. Se llama en el estado principal (bajo el token) antes de
// suspender; el reader y su candado solo los toca la goroutine de fondo.
func (p *luaProc) reader(which string) (*bufio.Reader, *sync.Mutex, error) {
	switch which {
	case "stdout":
		return p.stdout, &p.stdoutMu, nil
	case "stderr":
		return p.stderr, &p.stderrMu, nil
	default:
		return nil, nil, errors.New(`Proc:read*: "which" debe ser "stdout" o "stderr"`)
	}
}

// readLineFrom lee una línea de `r` **fuera del token** (goroutine de fondo), bajo
// `mu` (el candado del stream: serializa frente a otra lectura concurrente del mismo
// stream). `bufio.Reader.ReadString('\n')` devuelve la línea CON el `\n`, o lo leído
// hasta `io.EOF`. Distingue tres casos: línea normal, última línea sin `\n` antes de
// EOF (datos + eof), y EOF puro (sin datos). NO toma el candado de `kill`: si el
// proceso se mata para desbloquear esta lectura, el pipe se cierra y la lectura
// retorna EOF —sin deadlock—.
func readLineFrom(mu *sync.Mutex, r *bufio.Reader) (string, bool, error) {
	mu.Lock()
	defer mu.Unlock()
	line, err := r.ReadString('\n')
	if err == io.EOF {
		return line, true, nil // puede traer la última línea sin salto + EOF
	}
	if err != nil {
		return "", false, err
	}
	return line, false, nil
}

// readFrom lee bytes crudos de `r` **fuera del token**, bajo el candado del stream.
// Con `n >= 0`, lee hasta `n` bytes (lo disponible; puede ser menos). Con `n < 0`,
// lee todo hasta EOF (`io.ReadAll`). El flag `eof` indica que el stream se agotó: lo
// usa el llamante para devolver `nil` cuando además no hubo datos.
func readFrom(mu *sync.Mutex, r *bufio.Reader, n int) ([]byte, bool, error) {
	mu.Lock()
	defer mu.Unlock()
	if n < 0 {
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, false, err
		}
		return data, true, nil // ReadAll consume hasta EOF
	}
	buf := make([]byte, n)
	got, err := r.Read(buf)
	if err == io.EOF {
		return buf[:got], true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return buf[:got], false, nil
}

// procWait implementa `Proc:wait() -> {code}` ⏸ (§6): espera a que el proceso
// termine y devuelve su código de salida. Es ⏸ (esperar bloquea). `cmd.Wait` solo
// puede llamarse una vez —libera los recursos del proceso—, así que el resultado se
// **memoiza** (`waited`/`waitErr`/`code`): varios `wait` ven el mismo desenlace.
//
// Importante para `spawn` con pipes: `Proc:wait` cierra los pipes del proceso. El
// patrón normal es leer stdout/stderr hasta EOF y LUEGO `wait` (como cualquier
// `cmd.Wait` tras drenar). Si se hace `wait` con datos sin leer, el SO bufferiza
// hasta su límite; para volcados grandes, leer antes. El contrato no obliga a un
// orden; este es el comportamiento de la stdlib.
func (rt *Runtime) procWait(L *lua.LState) int {
	if !rt.requireTask(L, "Proc:wait") {
		return 0
	}
	p := checkProc(L)
	if p == nil {
		return 0
	}

	vals := rt.sched.suspend(L, func() deliverFn {
		code := p.wait()
		return func(L *lua.LState) []lua.LValue {
			res := L.NewTable()
			res.RawSetString("code", lua.LNumber(code))
			return []lua.LValue{res}
		}
	})
	return pushAll(L, vals)
}

// wait espera al proceso **fuera del token** (goroutine de fondo) y memoiza el
// código. NO sostiene ningún candado durante la espera bloqueante —esa es la razón
// de `sync.Once` + `chan` en vez de un `Mutex`—: `cmd.Wait` se llama una sola vez
// (`waitOnce`), y los `wait` concurrentes o posteriores esperan a que `waitDone` se
// cierre y leen el resultado ya publicado. Sostener un candado aquí causaría el
// deadlock clásico con `kill` (que mata el proceso para que `Wait` retorne).
func (p *luaProc) wait() int {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		p.code = exitCode(p.waitErr)
		close(p.waitDone)
	})
	<-p.waitDone // un segundo `wait` (que no ganó el Once) espera al desenlace publicado
	return p.code
}

// closeReadPipes cierra los extremos de lectura de stdout/stderr (propios, vía
// `os.Pipe`): sin esto, sus descriptores se fugarían al derribar el `Proc`. Lo llama
// `reapAndClose` tras `Wait`. Tomar los candados de cada stream evita cerrar el pipe
// mientras una lectura de fondo lo usa (la lectura verá el cierre como error/EOF).
func (p *luaProc) closeReadPipes() {
	p.stdoutMu.Lock()
	if p.rOut != nil {
		_ = p.rOut.Close()
	}
	p.stdoutMu.Unlock()
	p.stderrMu.Lock()
	if p.rErr != nil {
		_ = p.rErr.Close()
	}
	p.stderrMu.Unlock()
}

// procKill implementa `Proc:kill(signal?)` (§6): envía una señal al proceso.
// **Señal por defecto TERM** (`SIGTERM`: terminación cortés que el proceso puede
// atender). **NO es ⏸**: señalar es inmediato (no espera a que el proceso muera —eso
// es `wait`—). `signal` puede ser un número (el de la señal del SO). Idempotente y
// best-effort: matar un proceso ya muerto no es error (no se relanza).
func (rt *Runtime) procKill(L *lua.LState) int {
	p := checkProc(L)
	if p == nil {
		return 0
	}
	sig := syscall.SIGTERM // por defecto TERM (§6)
	if v, ok := L.Get(2).(lua.LNumber); ok {
		sig = syscall.Signal(int(v))
	}
	p.killSignal(sig)
	return 0
}

// killSignal envía `sig` al proceso, una sola vez (idempotente vía `killed`).
// Best-effort: un fallo (el proceso ya murió, pid reciclado) no se propaga —matar es
// "asegúrate de que no siga vivo", y un proceso ya muerto cumple el objetivo—. El
// flag `killed` evita re-señalar: tras `Wait`, el pid podría reciclarse y señalarlo
// de nuevo mataría a OTRO proceso. Lo llaman `Proc:kill`, el `cleanup` de quien lo
// creó, el finalizer del GC, `reload` (vía `release`) y `Close` (vía `stopAllProcs`).
//
// Usa `killMu` —su candado PROPIO, nunca tomado durante una operación bloqueante—,
// de modo que matar a un proceso del que se está leyendo o al que se está esperando
// NO se deadlockea contra el candado del stream o el `waitDone`: cerrar/señalar el
// proceso es lo que **desbloquea** ese IO.
func (p *luaProc) killSignal(sig syscall.Signal) {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	if p.killed || p.cmd.Process == nil {
		return
	}
	p.killed = true
	_ = p.cmd.Process.Signal(sig)
}

// --- nu.proc.alive ------------------------------------------------------------

// procAlive implementa `nu.proc.alive(pid) -> boolean` (§6, G17). **NO es ⏸**: es
// una consulta inmediata al SO, sin IO que esperar. Informa de **existencia, no de
// identidad**: devuelve true si hay ALGÚN proceso vivo con ese pid en esta máquina,
// aunque sea uno distinto que reusó un pid reciclado. Es deliberado —para detectar
// locks de sesión huérfanos (sesiones.md §6) basta saber si "alguien" tiene ese pid;
// la identidad la da el contenido del lock (hostname, §7), no esta llamada—.
func (rt *Runtime) procAlive(L *lua.LState) int {
	pid := int(L.CheckNumber(1))
	L.Push(lua.LBool(pidAlive(pid)))
	return 1
}

// pidAlive comprueba si existe un proceso con `pid` enviándole la "señal 0": en
// Unix, `kill(pid, 0)` no envía señal alguna pero falla si el proceso no existe
// (`ESRCH`) o no es señalable por nosotros por permisos (`EPERM` —existe, pero de
// otro usuario—). Así:
//   - sin error → existe y es nuestro: vivo.
//   - `EPERM` → existe (de otro usuario): vivo (G17: existencia, no identidad ni
//     propiedad).
//   - cualquier otro (`ESRCH`) → no existe: muerto.
//
// Un pid <= 0 nunca es un proceso concreto vivo (0 = grupo, negativos = grupos): no
// vivo.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false // en Unix FindProcess no falla, pero por robustez
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
