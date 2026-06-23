package main

// Tests de la SUPERFICIE CLI (S45, cuestión abierta nº5 de arquitectura.md): los
// flags del binario, su comportamiento HEADLESS (sin TTY, G20) y sus CÓDIGOS DE
// SALIDA. Cierra la Fase 8 y el plan.
//
// Estrategia: en vez de lanzar el proceso (os/exec) y un binario con su propio
// `~/.config`, se ejercita el núcleo testeable del CLI —`runWith(rt, opts)`— sobre
// un Runtime construido con dirs de PRUEBA (config/data temporales) y las
// extensiones `providers`/`sessions`/`agent` activadas por `nu.toml`. Es HERMÉTICO:
// sin red (el adaptador de provider es un STUB registrado desde Lua, como CP-10) y
// sin tocar el entorno real. Lo que se blinda (criterio de hecho de S45):
//
//   - `nu -e '<lua>'` evalúa sin TTY y devuelve el código de salida correcto
//     (0 en éxito; 1 en error de ejecución, con stderr coherente);
//   - un turno de agente headless con `--auto-permissions` (la tool sensible se
//     concede) vs SIN él (permiso DENEGADO → código 3, agente.md §5, G20);
//   - `--continue` (G18) reanuda la sesión MÁS RECIENTE del proyecto (cwd): se
//     montan varias sesiones y se verifica que toma la última y la reanuda.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbareagimeno/nu/internal/runtime"
)

// providersTomlToolStub declara un provider cuyo adaptador "toolstub" lo registra
// el propio test desde Lua (un adaptador de prueba que pide una tool en el primer
// turno y responde texto en el segundo). Es el mismo patrón que el CP-10 de S39.
const providersTomlToolStub = `
[providers.test]
adapter  = "toolstub"
base_url = "http://localhost/unused"

[[providers.test.models]]
id      = "m1"
aliases = ["m"]
`

// registerToolStub es el cuerpo Lua que registra el adaptador "toolstub" en la
// extensión providers. En la PRIMERA llamada (sin tool_result en el último mensaje)
// emite una tool call a la tool de las globales TOOLNAME/TOOLARGS; en las siguientes
// responde "listo" y para. Así un `Session:send` ejerce el loop completo: pide tool
// → ejecuta → re-pide → fin. (Copiado del arnés de S39; package main no ve los
// helpers de package runtime.)
const registerToolStub = `
local providers = require("providers")
providers.register_adapter("toolstub", {
  name = "toolstub",
  caps = { tools = true, system = true, usage = true },
  stream = function(req, provider)
    local has_result = false
    local last = req.messages[#req.messages]
    if last then
      for _, block in ipairs(last.content or {}) do
        if block.type == "tool_result" then has_result = true end
      end
    end
    local events
    if has_result then
      local assembled = { role = "assistant", content = { { type = "text", text = "listo" } } }
      events = {
        { type = "text", text = "listo" },
        { type = "usage", input_tokens = 10, output_tokens = 2 },
        { type = "done", stop_reason = "end", message = assembled },
      }
    else
      local call = { type = "tool_call", id = "call-1", name = TOOLNAME, args = TOOLARGS }
      local assembled = { role = "assistant", content = { call } }
      events = {
        { type = "tool_call.begin", id = "call-1", name = TOOLNAME },
        { type = "tool_call.end", id = "call-1" },
        { type = "usage", input_tokens = 5, output_tokens = 3 },
        { type = "done", stop_reason = "tool_calls", message = assembled },
      }
    end
    local i = 0
    return function() i = i + 1; return events[i] end
  end,
})
`

// bootCLI arranca un Runtime HEADLESS (WithForceUI(false): nu.has("ui")=false, el
// caso G20) con providers+sessions+agent activadas por `nu.toml` y dirs de prueba
// conocidos. Devuelve el runtime, el data_dir (para inspeccionar el JSONL) y el
// config_dir. Registra ya el adaptador "toolstub" desde Lua (como el CP-10).
func bootCLI(t *testing.T) (*runtime.Runtime, string, string) {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "nu.toml"),
		[]byte("[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n"), 0o644); err != nil {
		t.Fatalf("write nu.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersTomlToolStub), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	rt := runtime.New(runtime.WithDataDir(dataDir), runtime.WithConfigDir(cfg), runtime.WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return rt, dataDir, cfg
}

// captureOutput redirige os.Stdout y os.Stderr a sendas tuberías mientras corre
// `fn`, y devuelve lo escrito en cada uno. Es seguro aquí: el único que escribe a
// stdout/stderr en este test es `runWith` (el log del runtime va a un fichero, no a
// la pantalla, §15). Restaura los descriptores originales al terminar.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stdout, os.Stderr = wOut, wErr

	outCh := make(chan string)
	errCh := make(chan string)
	go func() { outCh <- readAll(rOut) }()
	go func() { errCh <- readAll(rErr) }()

	fn()

	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = origOut, origErr
	return <-outCh, <-errCh
}

func readAll(r *os.File) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}

// --- `nu -e` (eval headless) + códigos de salida ---------------------------------

// TestCLIEvalSuccess: `nu -e '<lua>'` evalúa sin TTY, imprime los valores de retorno
// a stdout y sale con 0.
func TestCLIEvalSuccess(t *testing.T) {
	rt, _, _ := bootCLI(t)
	var code int
	stdout, stderr := captureOutput(t, func() {
		code = runWith(rt, cliOptions{eval: "return 1 + 1, nu.version.api"})
	})
	if code != exitOK {
		t.Fatalf("código de salida: got %d, want %d (stderr=%q)", code, exitOK, stderr)
	}
	lines := strings.Fields(strings.TrimSpace(stdout))
	if len(lines) != 2 || lines[0] != "2" {
		t.Fatalf("stdout de `-e` inesperado: %q", stdout)
	}
}

// TestCLIEvalRuntimeErrorExit: `nu -e` con un chunk que LANZA un error estructurado
// del core sale con código 1 y deja el error en stderr (no en stdout). Blinda que un
// error de ejecución headless tiene código != 0 coherente.
func TestCLIEvalRuntimeErrorExit(t *testing.T) {
	rt, _, _ := bootCLI(t)
	var code int
	stdout, stderr := captureOutput(t, func() {
		// Un error estructurado del contrato (§1.4) lanzado desde el chunk: el `-e`
		// corre en el estado principal (no es task), así que no usamos una ⏸; un
		// `error{...}` directo es un error de ejecución headless. El puente NO degrada
		// el code (invariante 🔒 de S02): sale en stderr.
		code = runWith(rt, cliOptions{eval: `error({ code = "ENOENT", message = "no existe el recurso" })`})
	})
	if code != exitError {
		t.Fatalf("código de salida: got %d, want %d", code, exitError)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("un error no debe escribir a stdout; got %q", stdout)
	}
	if !strings.Contains(stderr, "ENOENT") {
		t.Fatalf("stderr no nombra el código de error: %q", stderr)
	}
}

// TestCLIEvalSyntaxErrorExit: un error de SINTAXIS (no estructurado) también sale
// con código 1 (cualquier fallo de ejecución es != 0).
func TestCLIEvalSyntaxErrorExit(t *testing.T) {
	rt, _, _ := bootCLI(t)
	var code int
	_, stderr := captureOutput(t, func() {
		code = runWith(rt, cliOptions{eval: `return 1 +`})
	})
	if code != exitError {
		t.Fatalf("código de salida: got %d, want %d (stderr=%q)", code, exitError, stderr)
	}
}

// TestCLIUsageNoActionAgentMode: el modo agente con modificadores
// (--continue/--auto-permissions) pero SIN un prompt es uso inválido (código 2): no
// hay turno que ejecutar. (El caso "ningún flag" → pantalla desnuda/uso lo decide
// `run`, no ejecutable en CI headless; aquí se cubre el subcaso del modo agente.)
func TestCLIUsageNoActionAgentMode(t *testing.T) {
	rt, _, _ := bootCLI(t)
	// --continue/--auto-permissions sin prompt: uso inválido (código 2).
	var code int
	_, stderr := captureOutput(t, func() {
		code = runWith(rt, cliOptions{cont: true, autoPerm: true}) // promptSet=false
	})
	if code != exitUsage {
		t.Fatalf("modo agente sin prompt: got %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "prompt") {
		t.Fatalf("el uso debe mencionar el prompt: %q", stderr)
	}
}

// --- Turno de agente headless: --auto-permissions vs deny ------------------------

// registerWriteStub prepara el adaptador para pedir la tool `write_file` (que muta
// → default "ask" → en headless se deniega salvo allow / modo auto). Fija las
// globales que el stub lee al hacer stream.
func registerWriteStub(t *testing.T, rt *runtime.Runtime, repo string) {
	t.Helper()
	if _, err := rt.EvalString(registerToolStub); err != nil {
		t.Fatalf("registrar toolstub: %v", err)
	}
	setup := `
TOOLNAME = "write_file"
TOOLARGS = { path = "` + filepath.ToSlash(filepath.Join(repo, "out.txt")) + `", content = "hola" }
return "ok"`
	if _, err := rt.EvalString(setup); err != nil {
		t.Fatalf("fijar TOOLNAME/TOOLARGS: %v", err)
	}
}

// TestCLIAgentAutoPermissions: `nu -p '<...>' --auto-permissions` ejecuta el turno y
// la tool sensible (write_file) se CONCEDE (modo auto, agente.md §5), así que el
// turno termina OK (código 0) e imprime el texto final a stdout. El fichero se crea.
func TestCLIAgentAutoPermissions(t *testing.T) {
	rt, _, _ := bootCLI(t)
	repo := t.TempDir()
	registerWriteStub(t, rt, repo)

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = runWith(rt, cliOptions{
			prompt: "escribe el fichero", promptSet: true,
			model: "test/m", autoPerm: true,
		})
	})
	if code != exitOK {
		t.Fatalf("con --auto-permissions el turno debe salir 0; got %d (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stdout, "listo") {
		t.Fatalf("stdout debe traer el texto final del asistente; got %q", stdout)
	}
	// La escritura se concedió: el fichero existe con el contenido pedido.
	data, err := os.ReadFile(filepath.Join(repo, "out.txt"))
	if err != nil {
		t.Fatalf("con --auto-permissions el fichero debía crearse: %v", err)
	}
	if string(data) != "hola" {
		t.Fatalf("contenido escrito inesperado: %q", string(data))
	}
}

// TestCLIAgentPermissionDeniedExit3: `nu -p '<...>'` SIN --auto-permissions: la tool
// sensible (write_file) se DENIEGA en headless (agente.md §5, G20). El CLI sale con
// código 3 (distinto de 1) y stderr menciona --auto-permissions / allow. El fichero
// NO se crea. El turno en sí NO se rompe (el modelo recibe el error y responde).
func TestCLIAgentPermissionDeniedExit3(t *testing.T) {
	rt, _, _ := bootCLI(t)
	repo := t.TempDir()
	registerWriteStub(t, rt, repo)

	var code int
	_, stderr := captureOutput(t, func() {
		code = runWith(rt, cliOptions{
			prompt: "escribe el fichero", promptSet: true,
			model: "test/m", autoPerm: false, // sin auto-permissions
		})
	})
	if code != exitDenied {
		t.Fatalf("sin --auto-permissions un permiso denegado debe salir %d; got %d (stderr=%q)", exitDenied, code, stderr)
	}
	if !strings.Contains(stderr, "auto-permissions") && !strings.Contains(stderr, "allow") {
		t.Fatalf("el deny debe ser accionable (nombrar --auto-permissions/allow); got %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("el fichero no debía crearse tras denegar el permiso")
	}
}

// TestCLIAgentReadOnlyAllowed: una tool de SOLO LECTURA (read_file, default "allow")
// no pide permiso ni en headless (agente.md §5 amortiguador 1): el turno sale 0 sin
// --auto-permissions. Confirma que el código 3 muerde solo a las tools que mutan.
func TestCLIAgentReadOnlyAllowed(t *testing.T) {
	rt, _, _ := bootCLI(t)
	repo := t.TempDir()
	target := filepath.Join(repo, "leeme.txt")
	if err := os.WriteFile(target, []byte("contenido"), 0o600); err != nil {
		t.Fatalf("preparar fichero: %v", err)
	}
	if _, err := rt.EvalString(registerToolStub); err != nil {
		t.Fatalf("registrar toolstub: %v", err)
	}
	setup := `
TOOLNAME = "read_file"
TOOLARGS = { path = "` + filepath.ToSlash(target) + `" }
return "ok"`
	if _, err := rt.EvalString(setup); err != nil {
		t.Fatalf("fijar globales: %v", err)
	}

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = runWith(rt, cliOptions{prompt: "lee", promptSet: true, model: "test/m"})
	})
	if code != exitOK {
		t.Fatalf("una tool de solo lectura no debe denegar; got %d (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stdout, "listo") {
		t.Fatalf("stdout debe traer el texto final; got %q", stdout)
	}
}

// --- --continue (G18): reanudar la sesión más reciente ---------------------------

// TestCLIContinueResumesMostRecent: `--continue` reanuda la sesión MÁS RECIENTE del
// proyecto (cwd). Se crean varias sesiones para ese cwd (ids crecientes = más
// reciente abajo) y se verifica que `--continue -p` reanuda la última: el JSONL de la
// sesión más reciente CRECE con el nuevo turno (mensajes añadidos), y los de las
// viejas NO. Blinda la selección de la más reciente y la reanudación (sesiones.md §7).
func TestCLIContinueResumesMostRecent(t *testing.T) {
	rt, dataDir, _ := bootCLI(t)
	repo := t.TempDir()
	registerWriteStub(t, rt, repo) // el stub pedirá write_file; con --auto-permissions se concede

	// El proyecto del `--continue` es el cwd del PROCESO (lo que `nu.fs.cwd()`
	// devuelve), no `repo`: el driver del CLI resuelve la sesión por `nu.fs.cwd()`.
	// Creamos las sesiones de prueba para ESE cwd, para que `--continue` las vea.
	cwdRes, err := rt.EvalString("return nu.fs.cwd()")
	if err != nil {
		t.Fatalf("nu.fs.cwd: %v", err)
	}
	procCwd := strings.TrimSpace(cwdRes[0])

	// Crea TRES sesiones para el cwd del proceso con ids crecientes (gen_id usa
	// now_ms + sufijo; las creamos en orden, así la última es la más reciente). Las
	// cerramos para soltar el lock (si no, reanudar la más reciente chocaría con su
	// propio lock). Va por EvalTaskString porque `sessions.open` registra un
	// `nu.task.cleanup` (que exige estar en una task, §3).
	createSessions := `
local sessions = require("sessions")
local cwd = nu.fs.cwd()
local ids = {}
for i = 1, 3 do
  local s = sessions.open({ cwd = cwd })
  s:append_message({ role = "user", content = { { type = "text", text = "sesion " .. i } } })
  ids[#ids + 1] = s.id
  s:close()
end
table.sort(ids)
LAST_ID = ids[#ids]
FIRST_ID = ids[1]
return LAST_ID`
	res, err := rt.EvalTaskString(createSessions)
	if err != nil {
		t.Fatalf("crear sesiones: %v", err)
	}
	lastID := strings.TrimSpace(res[0])
	firstRes, err := rt.EvalString("return FIRST_ID")
	if err != nil {
		t.Fatalf("leer FIRST_ID: %v", err)
	}
	firstID := strings.TrimSpace(firstRes[0])

	// Tamaño del JSONL de la sesión más reciente y de la más vieja ANTES de --continue.
	lastPath := sessionPath(t, dataDir, procCwd, lastID)
	firstPath := sessionPath(t, dataDir, procCwd, firstID)
	lastBefore := fileSize(t, lastPath)
	firstBefore := fileSize(t, firstPath)

	// --continue -p --auto-permissions: reanuda la más reciente y envía un turno.
	var code int
	_, stderr := captureOutput(t, func() {
		code = runWith(rt, cliOptions{
			prompt: "continúa", promptSet: true, model: "test/m",
			cont: true, autoPerm: true,
		})
	})
	if code != exitOK {
		t.Fatalf("--continue debe salir 0; got %d (stderr=%q)", code, stderr)
	}

	// La sesión MÁS RECIENTE creció (el turno se anexó a ELLA); la más vieja no.
	lastAfter := fileSize(t, lastPath)
	firstAfter := fileSize(t, firstPath)
	if lastAfter <= lastBefore {
		t.Fatalf("--continue no anexó a la sesión más reciente (antes=%d, después=%d)", lastBefore, lastAfter)
	}
	if firstAfter != firstBefore {
		t.Fatalf("--continue tocó una sesión que NO era la más reciente (antes=%d, después=%d)", firstBefore, firstAfter)
	}
	// El nuevo turno aparece en el JSONL de la más reciente.
	data, err := os.ReadFile(lastPath)
	if err != nil {
		t.Fatalf("leer sesión reanudada: %v", err)
	}
	if !strings.Contains(string(data), "continúa") {
		t.Fatalf("el JSONL de la sesión reanudada no contiene el nuevo prompt:\n%s", string(data))
	}
}

// TestCLIContinueNoSessions: `--continue` sin sesiones previas en el proyecto es un
// error de ejecución accionable (código 1), no un crash ni una sesión nueva muda.
func TestCLIContinueNoSessions(t *testing.T) {
	rt, _, _ := bootCLI(t)
	repo := t.TempDir() // proyecto sin sesiones
	registerWriteStub(t, rt, repo)

	var code int
	_, stderr := captureOutput(t, func() {
		code = runWith(rt, cliOptions{
			prompt: "x", promptSet: true, model: "test/m", cont: true, autoPerm: true,
		})
	})
	if code != exitError {
		t.Fatalf("--continue sin sesiones debe salir %d; got %d (stderr=%q)", exitError, code, stderr)
	}
	if !strings.Contains(stderr, "continue") && !strings.Contains(stderr, "sesiones") {
		t.Fatalf("el error de --continue sin sesiones debe ser accionable; got %q", stderr)
	}
}

// --- helpers de inspección del JSONL --------------------------------------------

// sessionPath compone la ruta del fichero JSONL de una sesión: el slug del cwd lo
// calcula la extensión sessions (no-alfanumérico→`_`), así que lo replicamos para
// dar con el directorio del proyecto. Falla la prueba si no existe.
func sessionPath(t *testing.T, dataDir, cwd, id string) string {
	t.Helper()
	dir := filepath.Join(dataDir, "sessions", slug(cwd))
	path := filepath.Join(dir, id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no existe el JSONL de la sesión %s: %v", id, err)
	}
	return path
}

// slug replica la codificación de cwd→directorio de la extensión sessions
// (sesiones.md §2): no-alfanumérico/`-`/`.` → `_`, recorta `_` de los bordes, vacío
// → "root". Debe coincidir con `slug` de sessions/init.lua para hallar el fichero.
func slug(cwd string) string {
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

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}

// --- `nu --default-config` (onramp sin TTY, ADR-015/G33) --------------------------

// TestCLIDefaultConfigPersistent: `nu --default-config` SOLO (sin acción headless)
// escribe el conjunto de producto en `nu.toml`, informa a stdout dónde, y sale con 0.
// Ejercita `runDefaultConfig(rt)` con un Runtime de dirs de prueba (sin tocar el HOME
// real). Blinda la pieza "onramp persistente sin TTY" de G33.
func TestCLIDefaultConfigPersistent(t *testing.T) {
	cfg := t.TempDir()
	rt := runtime.New(runtime.WithDataDir(t.TempDir()), runtime.WithConfigDir(cfg), runtime.WithForceUI(false))
	t.Cleanup(rt.Close)

	var code int
	stdout, stderr := captureOutput(t, func() { code = runDefaultConfig(rt) })
	if code != exitOK {
		t.Fatalf("código: got %d, want %d (stderr=%q)", code, exitOK, stderr)
	}
	// stdout nombra el fichero escrito y el siguiente paso (accionable).
	if !strings.Contains(stdout, "nu.toml") || !strings.Contains(stdout, "nu -p") {
		t.Fatalf("stdout debería nombrar el fichero y el siguiente paso; got %q", stdout)
	}
	// El `nu.toml` quedó con el conjunto de producto (sonda providers) y SIN example.
	data, err := os.ReadFile(filepath.Join(cfg, "nu.toml"))
	if err != nil {
		t.Fatalf("nu.toml no se escribió: %v", err)
	}
	if !strings.Contains(string(data), "providers") {
		t.Fatalf("plugins.enabled no nombra el conjunto de producto; nu.toml:\n%s", data)
	}
	if strings.Contains(string(data), "example") {
		t.Fatalf("el conjunto persistido no debe incluir example; nu.toml:\n%s", data)
	}
}

// TestCLIDefaultConfigPersistentMalformedExit1: `nu --default-config` ante un `nu.toml`
// mal formado NO lo sobrescribe y sale con código 1 (error de ejecución), con stderr
// accionable. Blinda que el modo persistente no destruye config del usuario.
func TestCLIDefaultConfigPersistentMalformedExit1(t *testing.T) {
	cfg := t.TempDir()
	bad := "esto no es toml = = [[[\n"
	if err := os.WriteFile(filepath.Join(cfg, "nu.toml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := runtime.New(runtime.WithDataDir(t.TempDir()), runtime.WithConfigDir(cfg), runtime.WithForceUI(false))
	t.Cleanup(rt.Close)

	var code int
	stdout, stderr := captureOutput(t, func() { code = runDefaultConfig(rt) })
	if code != exitError {
		t.Fatalf("código: got %d, want %d", code, exitError)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("un fallo no debe escribir a stdout; got %q", stdout)
	}
	if !strings.Contains(stderr, "nu.toml") {
		t.Fatalf("stderr debería ser accionable y nombrar nu.toml; got %q", stderr)
	}
	// El fichero roto sigue intacto.
	data, _ := os.ReadFile(filepath.Join(cfg, "nu.toml"))
	if string(data) != bad {
		t.Fatalf("el nu.toml mal formado NO debe sobrescribirse; quedó:\n%s", data)
	}
}

// TestCLIDefaultConfigEphemeral: `nu --default-config -e '<lua>'` activa el conjunto de
// producto SOLO para ese proceso (sin escribir `nu.toml`) y ejecuta el `-e`. Como `run`
// (no `runWith`) es quien construye el runtime efímero, este test ejercita el camino
// equivalente: un Runtime con `WithEnabledPlugins(OfficialProductSet)` + dirs de prueba,
// y comprueba que tras `runWith(-e)` el `nu.toml` NO existe y el conjunto está activo.
func TestCLIDefaultConfigEphemeral(t *testing.T) {
	cfg := t.TempDir()
	names, err := runtime.OfficialProductSet()
	if err != nil {
		t.Fatalf("OfficialProductSet: %v", err)
	}
	rt := runtime.New(
		runtime.WithDataDir(t.TempDir()), runtime.WithConfigDir(cfg),
		runtime.WithForceUI(false), runtime.WithEnabledPlugins(names))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot efímero: %v", err)
	}

	var code int
	stdout, stderr := captureOutput(t, func() {
		// Cuenta las extensiones activas vía la API pública.
		code = runWith(rt, cliOptions{eval: `local n=0 for _,p in ipairs(nu.plugin.list()) do if p.enabled then n=n+1 end end return n`})
	})
	if code != exitOK {
		t.Fatalf("código: got %d, want %d (stderr=%q)", code, exitOK, stderr)
	}
	if got, want := strings.TrimSpace(stdout), fmt.Sprint(len(names)); got != want {
		t.Fatalf("esperaba %s extensiones de producto activas; got %q", want, got)
	}
	// NO se escribió `nu.toml` (efímero = solo memoria).
	if _, err := os.Stat(filepath.Join(cfg, "nu.toml")); !os.IsNotExist(err) {
		t.Fatalf("el modo efímero NO debe escribir nu.toml; err=%v", err)
	}
}
