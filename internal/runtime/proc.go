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

// spawnProc arranca el subproceso descrito por `argv`/`opts` y monta su handle
// `luaProc` —pipes, tracking, reaper y finalizer—, **sin tocar la VM**: es el
// núcleo VM-agnóstico que comparten el backend gopher (`procSpawn`) y el wasm
// (`registerProcWasm`). Toma aquí la foto del overlay de `nu.sys.setenv` (§7), como
// el resto de `spawn`, en el estado principal. Devuelve o el handle ya arrancado o
// el error **crudo** de arranque (`StdinPipe`/`os.Pipe`/`Start`), que cada backend
// traduce a `ENOENT`/`EACCES`/`EIO` con su propio mapeador (`mapProcStartError` en
// gopher, `mapProcStartErrorWasm` en wasm). No cambia el comportamiento observable
// del backend gopher: allí `rt.sys` y `rt.sched` siempre existen, así que las
// guardas de nil nunca se toman —solo permiten un `rt` mínimo en los tests de wasm—.
func (rt *Runtime) spawnProc(argv []string, opts procOpts) (*luaProc, error) {
	// Foto del overlay de `nu.sys.setenv` (S17): el subproceso ve los `setenv`
	// previos a este `spawn` (§7). Corre en el estado principal, así que esta lectura
	// no compite con un `setenv` concurrente.
	if rt.sys != nil {
		opts.envOver = rt.sys.envOverlay()
	}

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
		return nil, err
	}
	rOut, wOut, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = rOut.Close()
		_ = wOut.Close()
		return nil, err
	}
	cmd.Stdout = wOut
	cmd.Stderr = wErr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = rOut.Close()
		_ = wOut.Close()
		_ = rErr.Close()
		_ = wErr.Close()
		return nil, err
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

	if rt.sched != nil {
		rt.sched.trackProc(p)
		rt.sched.track(p) // registro de handles por dueño (S13): que `reload` lo mate
	}

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

	return p, nil
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

// closeStdin cierra el stdin del proceso (idempotente), señalándole EOF. Es la
// parte VM-agnóstica de `close_stdin` (§6): la comparten el backend gopher
// (`procCloseStdin`) y el wasm (`registerProcWasm`). Cerrar dos veces es inocuo.
func (p *luaProc) closeStdin() {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.stdin != nil && !p.stdinClosed {
		_ = p.stdin.Close()
		p.stdinClosed = true
	}
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
		return nil, nil, errors.New(`el argumento "which" debe ser "stdout" o "stderr"`)
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
