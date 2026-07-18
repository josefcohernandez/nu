package e2e

// Tests e2e del plugin oficial `sessions` contra el BINARIO real: la persistencia
// en disco tal cual la deja el proceso, fuera de cualquier `Runtime` de Go. Cubre lo
// que ni internal/runtime/sessions_test.go (Lua sobre un `Runtime` in-process, sin
// PID real) ni main_test.go (`TestCLIContinueResumesMostRecent`/`TestCLIContinueNoSessions`,
// mismo proceso Go para "dos sesiones") pueden ejercitar: el layout real bajo
// `$XDG_DATA_HOME/enu` con slug real (sesiones.md §2), la continuidad entre DOS
// invocaciones de proceso frío distintas, el lock realmente vivo con el PID de OTRO
// binario, y la recuperación de un huérfano tras un SIGKILL de verdad (sesiones.md §6).
//
// Prefijo TestSessionsE2E* para filtrarlos con `-run TestSessions`.
//
// --- Recortes y costuras respecto al enunciado de partida --------------------------
//
//  1. NO existe una tool `bash` en el árbol hoy: `internal/runtime/embedded/agent`
//     solo registra `read_file`/`write_file` (tools_fs.lua); agente.md la documenta,
//     pero implementarla es trabajo de una sesión futura del plan. Los escenarios que
//     pedían `fp.PushToolUse("call-1","bash",{command="sleep N"})` para mantener el
//     turno "en vuelo" el tiempo que el test necesita observar se adaptan con
//     `newDelayedProvider` (más abajo): un servidor httptest PROPIO que responde al
//     POST tras un `time.Sleep`. Es equivalente para el propósito del test —lo que se
//     quiere sondear es que el LOCK sigue vivo mientras el turno no ha terminado
//     (sesiones.md §6), no específicamente un bash real— porque `agent.session(opts)`
//     adquiere el lock (`sessions.open`) ANTES de la primera llamada HTTP
//     (agent/init.lua `M.session`, línea ~1617), así que retrasar la respuesta basta
//     para mantener el lock observable sin necesitar ninguna tool.
//  2. `Workspace` solo da `Run` (bloqueante) o `Start` (con PTY); los escenarios 3-5
//     necesitan un `enu` en segundo plano SIN terminal, con acceso al PID real y a
//     `Kill()`. Se añade aquí `Workspace.RunHeadlessBackground` + `HeadlessProc` como
//     helper PRIVADO de este fichero (no se toca harness_test.go): `cmd.Start()` +
//     `cmd.Process.Pid`/`Kill()`/`Wait()`, sin pseudo-terminal.
//  3. La aserción de permisos 0600 del `.jsonl` (sesiones.md §2) se blinda **de
//     verdad**, sin amañar el umask: desde G57 la extensión `sessions` crea el
//     transcript con `enu.fs.write{ mode = 0600 }` (chmod explícito NO recortado
//     por el umask, internal/runtime/fs.go), así que el fichero sale en 0600 bajo
//     cualquier umask heredado. La versión previa fijaba `syscall.Umask(0o077)`
//     alrededor de la creación porque el binario aún no hacía chmod; ese apaño se
//     retiró al construir G57 (bajo el umask habitual 022, el código viejo dejaría
//     el transcript en 0644 y esta aserción lo cazaría).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Slug (sesiones.md §2, G38): copia de la especificación, no del código Lua —el
// mismo criterio que main_test.go (`slug`, package main) documenta: el algoritmo es
// PARTE DEL FORMATO, así que un test externo lo recalcula desde la espec, sin pasar
// por `sessions.slug` de Lua (eso sería probar la extensión contra sí misma).
// ---------------------------------------------------------------------------

// sessionSlug traduce un `cwd` a la clave de agrupación del directorio de sesiones:
// todo carácter fuera de [A-Za-z0-9.-] -> `_`, recorte de `_` en los bordes, vacío ->
// "root".
func sessionSlug(cwd string) string {
	var sb strings.Builder
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	s := strings.Trim(sb.String(), "_")
	if s == "" {
		return "root"
	}
	return s
}

// sessionsDirFor compone `dataDir/sessions/<slug(cwd)>` (sesiones.md §2).
func sessionsDirFor(dataDir, cwd string) string {
	return filepath.Join(dataDir, "sessions", sessionSlug(cwd))
}

// resolvedWorkdir devuelve `ws.Workdir` con los symlinks resueltos: `enu.fs.cwd()`
// dentro del proceso es `os.Getwd()` (vmwasm_fs.go), y en macOS `t.TempDir()` cuelga
// de `/var/...`, que el sistema resuelve a `/private/var/...` (symlink real del
// SO) — `os.Getwd()` tras el `chdir` del proceso hijo devuelve la ruta YA
// resuelta. Sin este resuelto, el slug/`meta.cwd` calculado en el test no
// coincidiría con el que escribe el binario real.
func resolvedWorkdir(t *testing.T, ws *Workspace) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(ws.Workdir)
	if err != nil {
		t.Fatalf("resolver symlinks de %s: %v", ws.Workdir, err)
	}
	return p
}

// sessionsJSONLFiles lista los `.jsonl` (no `.lock`) de un directorio de proyecto,
// ordenados por nombre (los ids ordenan lexicográfico = temporal, §2/§7). Un
// directorio ausente cuenta como "sin sesiones" (lista vacía), no fatal: algunos
// tests lo comprueban antes de que exista.
func sessionsJSONLFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("listar %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, e.Name())
		}
	}
	return out
}

// sessionsLockFiles lista los `.jsonl.lock` de un directorio de proyecto. Vacío si
// el directorio no existe todavía.
func sessionsLockFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("listar %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl.lock") {
			out = append(out, e.Name())
		}
	}
	return out
}

// sessionEntry es una entrada JSONL decodificada genérica (sesiones.md §3): basta
// `map[string]any` para leer `t` y navegar `message` sin comprometerse con structs
// que dupliquen el contrato de providers.md.
type sessionEntry = map[string]any

// sessionsReadEntries hace el REPLAY manual de un transcript (sesiones.md §2: "el
// estado se reconstruye por replay"), sin pasar por `Session:replay` de Lua —el
// punto es observar el fichero desde FUERA—. Misma robustez que describe §3: una
// línea que no decodifica a JSON (truncada por un crash a mitad de escritura) se
// descarta en silencio en vez de fallar la lectura entera.
func sessionsReadEntries(t *testing.T, path string) []sessionEntry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leer transcript %s: %v", path, err)
	}
	var out []sessionEntry
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // línea truncada/corrupta: se ignora, como haría un lector real (§3)
		}
		out = append(out, e)
	}
	return out
}

// sessionMessageRole devuelve `entry.message.role`, o "" si la entrada no tiene esa
// forma (no es una entrada `message` válida).
func sessionMessageRole(entry sessionEntry) string {
	msg, _ := entry["message"].(map[string]any)
	role, _ := msg["role"].(string)
	return role
}

// sessionMessageText concatena el texto de los bloques `type=="text"` del `content`
// de `entry.message` (providers.md §2.1: un Message es rol + bloques). Es lo mismo
// que hace `agentDriver` en main.go para componer el texto final del asistente.
func sessionMessageText(entry sessionEntry) string {
	msg, _ := entry["message"].(map[string]any)
	blocks, _ := msg["content"].([]any)
	var sb strings.Builder
	for _, b := range blocks {
		blk, _ := b.(map[string]any)
		if blk["type"] == "text" {
			if s, ok := blk["text"].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

// sessionsFirstLine devuelve la primera línea CRUDA (sin decodificar) de un
// transcript, para comparaciones byte a byte de la cabecera `meta` (sesiones.md §5:
// tras recuperar un huérfano, la `meta` debe quedar intacta, no reescrita).
func sessionsFirstLine(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leer %s: %v", path, err)
	}
	if i := strings.IndexByte(string(raw), '\n'); i >= 0 {
		return string(raw)[:i]
	}
	return string(raw)
}

// sessionsWaitForLock sondea `dir` hasta encontrar EXACTAMENTE un `.jsonl.lock`, lo
// decodifica y lo devuelve (ruta + contenido `{pid,hostname,started}`, sesiones.md
// §6). Falla la prueba si se agota `timeout` o si aparece más de uno (invariante "un
// escritor por sesión" roto).
func sessionsWaitForLock(t *testing.T, dir string, timeout time.Duration) (string, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		locks := sessionsLockFiles(t, dir)
		if len(locks) > 1 {
			t.Fatalf("más de un .jsonl.lock en %s (viola 'un escritor por sesión', §6): %v", dir, locks)
		}
		if len(locks) == 1 {
			path := filepath.Join(dir, locks[0])
			raw, err := os.ReadFile(path)
			if err == nil {
				var meta map[string]any
				if json.Unmarshal(raw, &meta) == nil {
					return path, meta
				}
			}
			// Carrera: el lock apareció pero aún no se ha volcado su contenido /
			// ya se borró entre el ReadDir y el ReadFile. Sigue sondeando.
		}
		if time.Now().After(deadline) {
			t.Fatalf("no apareció un .jsonl.lock en %s dentro de %s", dir, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sessionsWaitForUserMessage sondea el ÚNICO transcript de `dir` hasta que el mensaje
// de usuario esté ANEXADO (meta + message[user]) y devuelve sus entradas. Falla si se
// agota `timeout`.
//
// El `.jsonl.lock` aparece en `sessions.open`, ANTES de que `_drain_queue` anexe el
// mensaje de usuario (sesiones.md §4). Esperar solo al lock (sessionsWaitForLock) y
// matar acto seguido corre contra ese append: bajo carga (CI, -race, contención de
// CPU) el SIGKILL puede llegar antes de que el 'user' toque disco, dejando el
// transcript vacío/solo-meta —una carrera del TEST, no del runtime—. Esta espera lo
// vuelve determinista sin cambiar lo que el escenario prueba (el provider tarda
// segundos, así que el 'assistant' sigue sin poder aparecer: el turno queda truncado
// en el 'user', que es justo la precondición del crash a mitad de turno).
func sessionsWaitForUserMessage(t *testing.T, dir string, timeout time.Duration) []sessionEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if files := sessionsJSONLFiles(t, dir); len(files) == 1 {
			entries := sessionsReadEntries(t, filepath.Join(dir, files[0]))
			for _, e := range entries {
				if e["t"] == "message" && sessionMessageRole(e) == "user" {
					return entries
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("el mensaje de usuario no se persistió en %s dentro de %s", dir, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// HeadlessProc: costura de arnés (ver nota 2 de arriba). `Workspace` solo da `Run`
// (bloqueante, sin TTY) y `Start` (con PTY, para interactivo); aquí hace falta un
// tercer modo, "sin TTY pero EN SEGUNDO PLANO", con el PID real del proceso `enu`
// para observar el lockfile mientras sigue vivo y para poder matarlo con SIGKILL.
// ---------------------------------------------------------------------------

// HeadlessProc es un `enu` lanzado con `cmd.Start()` (sin TTY, sin bloquear): el
// llamante decide cuándo esperarlo (`Wait`) o matarlo (`Kill`/`os.Signal` vía
// `Process`). stdout/stderr se acumulan en memoria, igual que `Run`, pero solo son
// seguros de leer DESPUÉS de que `Wait` retorne (las goroutines de copia de
// `exec.Cmd` garantizan que ya han terminado de escribir en ese momento).
type HeadlessProc struct {
	cmd            *exec.Cmd
	stdout, stderr strings.Builder
}

// RunHeadlessBackground lanza el binario SIN TTY y SIN bloquear (`cmd.Start`, no
// `cmd.Run`): el análogo en segundo plano de `Workspace.Run`. El registro de limpieza
// mata el proceso (si sigue vivo) al terminar el test, igual que hacen `Run`/`Start`.
func (w *Workspace) RunHeadlessBackground(t *testing.T, opts RunOpts) *HeadlessProc {
	t.Helper()
	dir := opts.Dir
	if dir == "" {
		dir = w.Workdir
	}
	cmd := exec.Command(enuBin, opts.Args...)
	cmd.Dir = dir
	cmd.Env = append(w.baseEnv(), opts.Env...)
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}
	hp := &HeadlessProc{cmd: cmd}
	cmd.Stdout = &hp.stdout
	cmd.Stderr = &hp.stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("RunHeadlessBackground: el proceso no arrancó: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	return hp
}

// Pid devuelve el PID real del proceso `enu` ya arrancado.
func (h *HeadlessProc) Pid() int { return h.cmd.Process.Pid }

// Kill manda SIGKILL de verdad (no un cierre ordenado): es justo lo que E5 necesita
// para fabricar un lock huérfano AUTÉNTICO, sin recurrir a un lock construido a mano.
func (h *HeadlessProc) Kill() error { return h.cmd.Process.Kill() }

// Wait espera a que el proceso termine por sí mismo y devuelve un `Result` con la
// misma forma que `Workspace.Run` (stdout/stderr capturados, exit code). Mata el
// proceso si se agota `timeout`.
func (h *HeadlessProc) Wait(t *testing.T, timeout time.Duration) Result {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.cmd.Wait() }()
	select {
	case err := <-done:
		res := Result{Stdout: h.stdout.String(), Stderr: h.stderr.String()}
		if err == nil {
			res.ExitCode = 0
			return res
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
			return res
		}
		t.Fatalf("HeadlessProc.Wait: error inesperado: %v", err)
		return res
	case <-time.After(timeout):
		_ = h.cmd.Process.Kill()
		<-done
		t.Fatalf("HeadlessProc.Wait: el proceso no terminó en %s", timeout)
		return Result{}
	}
}

// ---------------------------------------------------------------------------
// DelayedProvider: sustituto de la tool `bash` inexistente (ver nota 1 de arriba).
// Mismo dialecto mínimo que `FakeProvider` (reutiliza `buildTextSSE`/`sseEvent`, de
// provider_test.go, mismo paquete) pero con un RETARDO configurable antes de
// responder al POST — lo que hace falta para mantener un turno "en vuelo" el tiempo
// suficiente para sondear el lockfile o para matar el proceso a mitad de turno.
// ---------------------------------------------------------------------------

// DelayedProvider es un servidor httptest de un solo uso: responde a CUALQUIER POST
// con el mismo turno de texto, tras dormir `delay`. No lleva cola (a diferencia de
// FakeProvider): los escenarios que lo usan solo necesitan UN turno lento.
type DelayedProvider struct {
	srv *httptest.Server
}

// newDelayedProvider levanta el servidor y lo registra para cierre al terminar el
// test. Respeta la cancelación del contexto de la request mientras "duerme" (si el
// cliente se cae —p. ej. un SIGKILL al proceso `enu`— no deja la goroutine del
// handler durmiendo hasta el final de `delay` en vano).
func newDelayedProvider(t *testing.T, delay time.Duration, text string) *DelayedProvider {
	t.Helper()
	dp := &DelayedProvider{}
	dp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "sin Flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		sse := buildTextSSE(text)
		for _, line := range strings.SplitAfter(sse, "\n") {
			if line == "" {
				continue
			}
			_, _ = io.WriteString(w, line)
			fl.Flush()
		}
	}))
	t.Cleanup(dp.srv.Close)
	return dp
}

func (dp *DelayedProvider) URL() string { return dp.srv.URL }

// useDelayedProvider cablea el workspace contra `dp`, mismo patrón que
// `Workspace.UseFakeProvider` (provider `anthropic`, modelo con alias `opus`) pero
// apuntando al `DelayedProvider`. NO escribe `enu.toml`: llama antes a
// `ws.WriteEnuToml("providers", "sessions", "agent")`.
func useDelayedProvider(t *testing.T, ws *Workspace, dp *DelayedProvider) {
	t.Helper()
	toml := "" +
		"[providers.anthropic]\n" +
		"adapter     = \"anthropic\"\n" +
		"base_url    = \"" + dp.URL() + "\"\n" +
		"api_key_env = \"" + FakeAPIKeyEnv + "\"\n\n" +
		"[[providers.anthropic.models]]\n" +
		"id         = \"claude-e2e\"\n" +
		"context    = 200000\n" +
		"max_output = 4096\n" +
		"aliases    = [\"opus\"]\n"
	ws.WriteConfig(t, "providers.toml", toml)
	ws.WriteAgentToml(t, "anthropic/opus")
}

// sessionsIDPattern casa el basename de un `.jsonl` (sin extensión): timestamp ms de
// 13 dígitos + `-` + sufijo hex de 4 dígitos (sesiones.md §2, `sessions/init.lua
// gen_id`).
var sessionsIDPattern = regexp.MustCompile(`^\d{13}-[0-9a-f]{4}$`)

// ---------------------------------------------------------------------------
// E1 (MÍNIMO IMPRESCINDIBLE) — formato en disco + continuidad real entre DOS
// procesos fríos distintos, en una sola tabla.
// ---------------------------------------------------------------------------

// TestSessionsE2EContinueAppendsToSameTranscriptFile: dos invocaciones SEPARADAS del
// binario (proceso 1 crea, proceso 2 con `--continue` reanuda) deben producir UN solo
// `.jsonl` que CRECE por append real, con la cabecera `meta` escrita una sola vez y
// las 4 entradas `message` (user1/assistant1/user2/assistant2) en orden — sesiones.md
// §2 (layout), §4 (atomicidad del append) y §6 (el lock se libera al salir).
func TestSessionsE2EContinueAppendsToSameTranscriptFile(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	ws.UseFakeProvider(t, fp)

	dir := sessionsDirFor(ws.DataDir, resolvedWorkdir(t, ws))

	// --- proceso 1: arranque en frío -------------------------------------------
	fp.PushText("primer turno")
	// Sin amañar el umask: el transcript se crea con `mode = 0600` (G57), así que sale
	// en 0600 bajo el umask heredado (ver nota 3 de la cabecera). La aserción de modo
	// de más abajo lo comprueba de verdad.
	res1 := ws.Run(t, RunOpts{Args: []string{"-p", "hola", "--auto-permissions"}})
	if res1.ExitCode != 0 {
		t.Fatalf("proceso 1: exit got %d, want 0 (stderr=%q)", res1.ExitCode, res1.Stderr)
	}

	filesAfter1 := sessionsJSONLFiles(t, dir)
	if len(filesAfter1) != 1 {
		t.Fatalf("tras el proceso 1 debía haber exactamente 1 .jsonl bajo %s; got %v", dir, filesAfter1)
	}
	path := filepath.Join(dir, filesAfter1[0])

	info1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat tras proceso 1: %v", err)
	}
	sizeAfter1 := info1.Size()
	if perm := info1.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permisos del .jsonl: got %o, want 0600 (sesiones.md §2)", perm)
	}

	// --- proceso 2: OTRO binario, `--continue` ---------------------------------
	fp.PushText("segundo turno")
	res2 := ws.Run(t, RunOpts{Args: []string{"--continue", "-p", "sigue", "--auto-permissions"}})
	if res2.ExitCode != 0 {
		t.Fatalf("proceso 2 (--continue): exit got %d, want 0 (stderr=%q)", res2.ExitCode, res2.Stderr)
	}
	if !strings.Contains(res2.Stdout, "segundo turno") {
		t.Fatalf("stdout del proceso 2 debía traer el texto del segundo turno; got %q", res2.Stdout)
	}

	filesAfter2 := sessionsJSONLFiles(t, dir)
	if len(filesAfter2) != 1 || filesAfter2[0] != filesAfter1[0] {
		t.Fatalf("--continue no debía crear/forkear un .jsonl nuevo; antes=%v después=%v", filesAfter1, filesAfter2)
	}

	info2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat tras proceso 2: %v", err)
	}
	if info2.Size() <= sizeAfter1 {
		t.Fatalf("el .jsonl no creció tras --continue (append real): antes=%d, después=%d", sizeAfter1, info2.Size())
	}

	entries := sessionsReadEntries(t, path)
	if len(entries) == 0 || entries[0]["t"] != "meta" {
		t.Fatalf("la primera línea debía ser 'meta'; got entries[0]=%v", entries[0])
	}
	metaCount := 0
	var messages []sessionEntry
	for _, e := range entries {
		switch e["t"] {
		case "meta":
			metaCount++
		case "message":
			messages = append(messages, e)
		}
	}
	if metaCount != 1 {
		t.Fatalf("debía haber exactamente 1 línea 'meta' (la continuación no reescribe la cabecera); got %d", metaCount)
	}
	if len(messages) != 4 {
		t.Fatalf("el replay debía dar 4 entradas 'message' (user1,assistant1,user2,assistant2); got %d: %v", len(messages), messages)
	}
	wantRoles := []string{"user", "assistant", "user", "assistant"}
	for i, want := range wantRoles {
		if got := sessionMessageRole(messages[i]); got != want {
			t.Fatalf("mensaje %d: role got %q, want %q", i, got, want)
		}
	}
	if got := sessionMessageText(messages[3]); got != "segundo turno" {
		t.Fatalf("texto del 2º assistant: got %q, want %q", got, "segundo turno")
	}

	if locks := sessionsLockFiles(t, dir); len(locks) != 0 {
		t.Fatalf(".jsonl.lock debía liberarse al salir (§6); quedó: %v", locks)
	}
}

// ---------------------------------------------------------------------------
// E2 — layout completo en disco de una sesión nueva.
// ---------------------------------------------------------------------------

// TestSessionsE2ESessionsWritesJSONLLayoutOnDisk: un turno mínimo deja el layout
// EXACTO que promete sesiones.md §2-§3: directorio `sessions/<slug(cwd)>/`, nombre de
// fichero `<timestamp13>-<hex4>.jsonl`, cabecera `meta` con las claves del contrato,
// y el texto del asistente persistido en la entrada `message` correspondiente.
func TestSessionsE2ESessionsWritesJSONLLayoutOnDisk(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	fp.PushText("hola persistida")

	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	ws.UseFakeProvider(t, fp)

	res := ws.Run(t, RunOpts{Args: []string{"-p", "saluda", "--auto-permissions"}})
	if res.ExitCode != 0 {
		t.Fatalf("exit got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}

	resolvedCwd := resolvedWorkdir(t, ws)
	wantDir := sessionsDirFor(ws.DataDir, resolvedCwd)
	if info, err := os.Stat(wantDir); err != nil || !info.IsDir() {
		t.Fatalf("no existe el directorio de proyecto esperado %s: %v", wantDir, err)
	}

	files := sessionsJSONLFiles(t, wantDir)
	if len(files) != 1 {
		t.Fatalf("esperaba exactamente 1 .jsonl bajo %s; got %v", wantDir, files)
	}
	base := strings.TrimSuffix(files[0], ".jsonl")
	if !sessionsIDPattern.MatchString(base) {
		t.Fatalf("el id de sesión %q no matchea %s (sesiones.md §2)", base, sessionsIDPattern.String())
	}

	path := filepath.Join(wantDir, files[0])
	entries := sessionsReadEntries(t, path)
	if len(entries) == 0 {
		t.Fatalf("transcript vacío: %s", path)
	}
	meta := entries[0]
	if meta["t"] != "meta" {
		t.Fatalf("la primera línea debía ser 'meta'; got t=%v", meta["t"])
	}
	if v, ok := meta["v"].(float64); !ok || v != 1 {
		t.Fatalf("meta.v: got %v, want 1", meta["v"])
	}
	if id, ok := meta["id"].(string); !ok || id != base {
		t.Fatalf("meta.id: got %v, want %q (el basename sin extensión)", meta["id"], base)
	}
	if cwd, ok := meta["cwd"].(string); !ok || filepath.Clean(cwd) != filepath.Clean(resolvedCwd) {
		t.Fatalf("meta.cwd: got %v, want %q", meta["cwd"], resolvedCwd)
	}
	if created, ok := meta["created"].(float64); !ok || created <= 0 {
		t.Fatalf("meta.created: got %v, quería un epoch ms > 0", meta["created"])
	}

	var assistantText string
	for _, e := range entries {
		if e["t"] == "message" && sessionMessageRole(e) == "assistant" {
			assistantText = sessionMessageText(e)
		}
	}
	if assistantText != "hola persistida" {
		t.Fatalf("texto del assistant persistido: got %q, want %q", assistantText, "hola persistida")
	}

	if locks := sessionsLockFiles(t, wantDir); len(locks) != 0 {
		t.Fatalf(".jsonl.lock debía liberarse al salir; quedó: %v", locks)
	}
}

// ---------------------------------------------------------------------------
// E3 — el lockfile existe MIENTRAS el turno está en vuelo y desaparece al salir.
// ---------------------------------------------------------------------------

// TestSessionsE2ELockFileExistsDuringTurnAndGoneAfter: con un turno artificialmente
// lento (DelayedProvider, ver nota 1 de cabecera), el `.jsonl.lock` debe existir
// mientras el proceso sigue vivo —con el PID REAL del proceso `enu`, no uno
// fabricado— y desaparecer tras una salida limpia (sesiones.md §6).
func TestSessionsE2ELockFileExistsDuringTurnAndGoneAfter(t *testing.T) {
	ws := NewWorkspace(t)
	dp := newDelayedProvider(t, 700*time.Millisecond, "listo tras dormir")
	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	useDelayedProvider(t, ws, dp)

	dir := sessionsDirFor(ws.DataDir, resolvedWorkdir(t, ws))

	proc := ws.RunHeadlessBackground(t, RunOpts{Args: []string{"-p", "espera", "--auto-permissions"}})

	lockPath, meta := sessionsWaitForLock(t, dir, 1*time.Second)
	pidF, ok := meta["pid"].(float64)
	if !ok {
		t.Fatalf("el lock no traía 'pid' numérico: %v", meta)
	}
	if int(pidF) != proc.Pid() {
		t.Fatalf("lock.pid: got %d, want %d (el PID real del proceso enu)", int(pidF), proc.Pid())
	}
	if host, _ := meta["hostname"].(string); host == "" {
		t.Fatalf("el lock no traía 'hostname' no vacío: %v", meta)
	}
	t.Logf("lock vivo confirmado en %s: %v", lockPath, meta)

	res := proc.Wait(t, 5*time.Second)
	if res.ExitCode != 0 {
		t.Fatalf("exit got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}

	if locks := sessionsLockFiles(t, dir); len(locks) != 0 {
		t.Fatalf(".jsonl.lock debía liberarse al salir (§6); quedó: %v", locks)
	}

	files := sessionsJSONLFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("esperaba exactamente 1 .jsonl; got %v", files)
	}
	entries := sessionsReadEntries(t, filepath.Join(dir, files[0]))
	var assistantText string
	for _, e := range entries {
		if e["t"] == "message" && sessionMessageRole(e) == "assistant" {
			assistantText = sessionMessageText(e)
		}
	}
	if assistantText != "listo tras dormir" {
		t.Fatalf("texto final persistido: got %q, want %q", assistantText, "listo tras dormir")
	}
}

// ---------------------------------------------------------------------------
// E4 — conflicto REAL de lock: dos binarios compitiendo por la misma sesión.
// ---------------------------------------------------------------------------

// TestSessionsE2EConcurrentContinueRealBusyConflict: mientras el proceso A mantiene
// el lock vivo (turno lento, DelayedProvider), un proceso B que intenta
// `--continue` sobre la MISMA sesión debe chocar con `ESESSION{reason="busy"}` real
// (pid vivo de OTRO proceso, no un lock fabricado a mano) — sesiones.md §6, "conflicto
// real". `--continue` en el driver del CLI (main.go `agentDriver`) NO forkea
// automático: el error se propaga tal cual y el CLI sale con el código 1 de error de
// ejecución (main.go doc de paquete, código 1).
func TestSessionsE2EConcurrentContinueRealBusyConflict(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	ws.UseFakeProvider(t, fp)

	dir := sessionsDirFor(ws.DataDir, resolvedWorkdir(t, ws))

	// Sesión ya creada por un turno normal y rápido.
	fp.PushText("primero")
	res0 := ws.Run(t, RunOpts{Args: []string{"-p", "arranca", "--auto-permissions"}})
	if res0.ExitCode != 0 {
		t.Fatalf("arranque de la sesión: exit got %d, want 0 (stderr=%q)", res0.ExitCode, res0.Stderr)
	}
	filesBefore := sessionsJSONLFiles(t, dir)
	if len(filesBefore) != 1 {
		t.Fatalf("esperaba exactamente 1 .jsonl tras el arranque; got %v", filesBefore)
	}

	// A partir de aquí, el proceso A reanuda esa misma sesión con un turno LENTO
	// (mantiene el lock vivo el tiempo que necesita el test).
	dp := newDelayedProvider(t, 2*time.Second, "A terminó")
	useDelayedProvider(t, ws, dp)

	procA := ws.RunHeadlessBackground(t, RunOpts{Args: []string{"--continue", "-p", "segundo", "--auto-permissions"}})
	_, lockMeta := sessionsWaitForLock(t, dir, 1*time.Second)
	if pidF, ok := lockMeta["pid"].(float64); !ok || int(pidF) != procA.Pid() {
		t.Fatalf("el lock vivo debía ser de A (pid %d); got %v", procA.Pid(), lockMeta)
	}

	// Proceso B: --continue mientras A sigue vivo. Bloqueante (Run): el conflicto se
	// resuelve (con error) ANTES de tocar la red, así que no necesita su propio turno
	// encolado en el fake.
	resB := ws.Run(t, RunOpts{Args: []string{"--continue", "-p", "tercero", "--auto-permissions"}})
	if resB.ExitCode != 1 {
		t.Fatalf("B contra un lock vivo: exit got %d, want 1 (stdout=%q, stderr=%q)",
			resB.ExitCode, resB.Stdout, resB.Stderr)
	}
	if !strings.Contains(resB.Stderr, "escritor vivo") {
		t.Fatalf("stderr de B debía nombrar el conflicto real ('escritor vivo'); got %q", resB.Stderr)
	}
	if resB.Stdout != "" {
		t.Fatalf("B no debía escribir nada a stdout (nunca llegó a mandar turno); got %q", resB.Stdout)
	}

	filesAfterB := sessionsJSONLFiles(t, dir)
	if len(filesAfterB) != len(filesBefore) {
		t.Fatalf("B no debía crear/forkear ningún .jsonl; antes=%v después=%v", filesBefore, filesAfterB)
	}

	resA := procA.Wait(t, 5*time.Second)
	if resA.ExitCode != 0 {
		t.Fatalf("A: exit got %d, want 0 (stderr=%q)", resA.ExitCode, resA.Stderr)
	}

	// El .jsonl reanudado por A solo contiene las entradas de A: el intento fallido
	// de B nunca llegó a escribir nada (append-only, coherente con que B nunca abrió
	// para escritura).
	entries := sessionsReadEntries(t, filepath.Join(dir, filesBefore[0]))
	var messages []sessionEntry
	for _, e := range entries {
		if e["t"] == "message" {
			messages = append(messages, e)
		}
	}
	// user("arranca") + assistant("primero") + user("segundo") + assistant("A terminó").
	if len(messages) != 4 {
		t.Fatalf("esperaba 4 entradas 'message' (solo el rastro de A); got %d: %v", len(messages), messages)
	}
	if got := sessionMessageText(messages[3]); got != "A terminó" {
		t.Fatalf("texto del último assistant: got %q, want %q", got, "A terminó")
	}
}

// ---------------------------------------------------------------------------
// E5 — recuperación de un lock huérfano tras un SIGKILL real.
// ---------------------------------------------------------------------------

// TestSessionsE2EOrphanLockReclaimedAfterRealCrash: un proceso matado con SIGKILL a
// mitad de turno deja un `.jsonl.lock` HUÉRFANO de verdad (el pid ya no existe en
// esta máquina) y un transcript truncado (solo `meta`+`message` de rol user, sin el
// `assistant`: sesiones.md §4, "el turno simplemente no existe"). Un segundo proceso
// —arranque en frío— debe reclamar el huérfano EN SILENCIO (sin error, sin pedir
// confirmación, §6) y seguir escribiendo en el MISMO fichero.
func TestSessionsE2EOrphanLockReclaimedAfterRealCrash(t *testing.T) {
	ws := NewWorkspace(t)
	dir := sessionsDirFor(ws.DataDir, resolvedWorkdir(t, ws))

	dp := newDelayedProvider(t, 5*time.Second, "no debería llegar")
	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	useDelayedProvider(t, ws, dp)

	proc := ws.RunHeadlessBackground(t, RunOpts{Args: []string{"-p", "muere a mitad", "--auto-permissions"}})
	_, lockMeta := sessionsWaitForLock(t, dir, 1*time.Second)
	if pidF, ok := lockMeta["pid"].(float64); !ok || int(pidF) != proc.Pid() {
		t.Fatalf("el lock debía ser del proceso a matar (pid %d); got %v", proc.Pid(), lockMeta)
	}
	// El lock aparece en `sessions.open`, antes de que se anexe el 'user': esperar a
	// que ese mensaje esté YA en disco antes de matar, o el SIGKILL corre contra el
	// append bajo carga y el transcript queda vacío/solo-meta (ver helper).
	sessionsWaitForUserMessage(t, dir, 2*time.Second)

	killedPid := proc.Pid()
	if err := proc.Kill(); err != nil {
		t.Fatalf("SIGKILL al proceso: %v", err)
	}
	// Cosechar el proceso (equivalente a que el SO/el padre real lo limpie tras un
	// crash): sin esto queda ZOMBIE bajo el proceso Go de este test (su padre real,
	// vía cmd.Start) — un zombie sin cosechar sigue respondiendo OK a
	// `kill(pid, 0)` para CUALQUIER llamante, así que el `enu.proc.alive` del
	// segundo proceso lo vería "vivo" y daría un falso conflicto real en vez de
	// reclamar el huérfano (sesiones.md §6).
	_ = proc.Wait(t, 2*time.Second)
	if pidAlive(killedPid) {
		t.Fatalf("el proceso matado (pid %d) seguía vivo tras cosecharlo", killedPid)
	}

	filesAfterCrash := sessionsJSONLFiles(t, dir)
	if len(filesAfterCrash) != 1 {
		t.Fatalf("esperaba exactamente 1 .jsonl tras el crash; got %v", filesAfterCrash)
	}
	crashedPath := filepath.Join(dir, filesAfterCrash[0])
	metaLineBefore := sessionsFirstLine(t, crashedPath)
	crashedEntries := sessionsReadEntries(t, crashedPath)
	metaBefore := crashedEntries[0]
	var messagesBefore []sessionEntry
	for _, e := range crashedEntries {
		if e["t"] == "message" {
			messagesBefore = append(messagesBefore, e)
		}
	}
	// Solo el user quedó anexado (§4): sessions._drain_queue anexa el mensaje de
	// usuario SÍNCRONO antes de esperar la respuesta; el assistant solo se anexa al
	// completarse el turno, y ese "completarse" nunca llegó a pasar.
	if len(messagesBefore) != 1 || sessionMessageRole(messagesBefore[0]) != "user" {
		t.Fatalf("tras el crash el transcript debía tener solo el 'user' truncado; got %v", messagesBefore)
	}

	// El lock sigue en disco (nadie lo liberó: SIGKILL no da tiempo a cleanup), pero
	// su pid ya no está vivo en esta máquina: es basura reclamable, no un conflicto.
	locksAfterCrash := sessionsLockFiles(t, dir)
	if len(locksAfterCrash) != 1 {
		t.Fatalf("esperaba el lock huérfano todavía en disco tras el SIGKILL; got %v", locksAfterCrash)
	}

	// --- segundo proceso: arranque en frío, --continue ------------------------
	fp := NewFakeProvider(t)
	fp.PushText("recuperado tras crash")
	ws.UseFakeProvider(t, fp) // vuelve a un provider RÁPIDO normal para este turno

	res := ws.Run(t, RunOpts{Args: []string{"--continue", "-p", "sigue", "--auto-permissions"}})
	if res.ExitCode != 0 {
		t.Fatalf("--continue tras crash: exit got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "recuperado tras crash") {
		t.Fatalf("stdout debía traer el texto del turno recuperado; got %q", res.Stdout)
	}

	filesAfterRecover := sessionsJSONLFiles(t, dir)
	if len(filesAfterRecover) != 1 || filesAfterRecover[0] != filesAfterCrash[0] {
		t.Fatalf("--continue debía reanudar el MISMO fichero (único/más reciente); antes=%v después=%v",
			filesAfterCrash, filesAfterRecover)
	}

	metaLineAfter := sessionsFirstLine(t, crashedPath)
	if metaLineAfter != metaLineBefore {
		t.Fatalf("la línea 'meta' cambió tras la recuperación (debía ser byte a byte idéntica):\nantes:  %s\ndespués: %s",
			metaLineBefore, metaLineAfter)
	}
	recoveredEntries := sessionsReadEntries(t, crashedPath)
	if len(recoveredEntries) == 0 || recoveredEntries[0]["t"] != "meta" {
		t.Fatalf("la primera línea seguía debiendo ser 'meta'; got %v", recoveredEntries[0])
	}
	metaAfter := recoveredEntries[0]
	if metaAfter["id"] != metaBefore["id"] || metaAfter["created"] != metaBefore["created"] {
		t.Fatalf("la cabecera 'meta' cambió tras la recuperación: antes=%v después=%v", metaBefore, metaAfter)
	}

	var messagesAfter []sessionEntry
	for _, e := range recoveredEntries {
		if e["t"] == "message" {
			messagesAfter = append(messagesAfter, e)
		}
	}
	// user(truncado) + user(nuevo) + assistant(nuevo) = 3. Ningún assistant
	// corresponde al turno matado (streaming sin `done` = nada persistido, §4).
	if len(messagesAfter) != 3 {
		t.Fatalf("esperaba 3 entradas 'message' en total (1 truncada + 2 del turno recuperado); got %d: %v",
			len(messagesAfter), messagesAfter)
	}
	wantRoles := []string{"user", "user", "assistant"}
	for i, want := range wantRoles {
		if got := sessionMessageRole(messagesAfter[i]); got != want {
			t.Fatalf("mensaje %d tras recuperar: role got %q, want %q", i, got, want)
		}
	}
	if got := sessionMessageText(messagesAfter[2]); got != "recuperado tras crash" {
		t.Fatalf("texto del assistant recuperado: got %q, want %q", got, "recuperado tras crash")
	}

	if locks := sessionsLockFiles(t, dir); len(locks) != 0 {
		t.Fatalf(".jsonl.lock debía liberarse tras el segundo proceso; quedó: %v", locks)
	}
}
