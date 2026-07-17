package e2e

// Tests e2e de la extensión oficial `mcp` (S41) contra el BINARIO real. La capa
// que un test in-process (internal/runtime/mcp_test.go) NO cubre: la DECLARACIÓN
// del servidor por fichero (`mcp.toml` → `mcp.connect_configured`, nunca
// probado desde fuera), los EXIT CODES reales del proceso, el LOG en disco
// (`<data_dir>/enu.log`) como canal de degradación, y que el subproceso del
// servidor MUERE cuando el binario entero termina —todo observado desde FUERA
// del proceso (ficheros, códigos de salida, señales del SO), sin instrumentar
// ni el runtime Go ni el estado Lua—.
//
// Lo que NO duplicamos (ya blindado in-process): el ciclo tools/list→registro→
// tools/call→resultado, el mapeo de isError, y la introspección del pid vía
// handle. Aquí el valor es el arranque real y sus efectos de disco.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fixture: un servidor MCP de prueba propio, AMPLIADO con dos efectos de disco
// observables desde fuera del proceso `enu`. Es una copia del `mcpServerSource`
// del test in-process (no importable: vive en un `_test.go` de otro paquete)
// con dos añadidos controlados por ARGUMENTOS DE LÍNEA DE COMANDOS:
//
//   - -pidfile <ruta>: al arrancar, escribe su propio PID a ese fichero. Deja
//     ver desde el SO que el auto-connect lanzó el proceso y, tras terminar
//     `enu`, comprobar que murió (scenario 3).
//   - -invocations <ruta>: en cada `tools/call` a `echo`, añade una línea con
//     el texto recibido. Prueba que el servidor REAL, por stdio, ejecutó la
//     tool (no un stub): el efecto es un fichero, no el historial Lua (1/2).
//
// Se pasan por argv (no por env): el `env` de `mcp.toml` es un array de "K=V",
// pero `enu.proc.spawn` sólo interpreta `env` como tabla { K = V } (map, no
// array; ver internal/runtime/vmwasm_proc.go:250), así que un `env` declarado
// en `mcp.toml` no llega al hijo. argv, en cambio, es un array de strings que
// tanto `mcp.toml` como la primitiva tratan igual: es el canal que SÍ funciona
// de extremo a extremo desde el fichero. (Ver la nota de carencias al final.)
// ---------------------------------------------------------------------------

const mcpTestServerSource = `package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"os"
	"strconv"
)

type rpc struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id,omitempty\"`" + `
	Method  string          ` + "`json:\"method\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
}

func respond(w *bufio.Writer, id json.RawMessage, result interface{}) {
	out := map[string]interface{}{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result}
	b, _ := json.Marshal(out)
	w.Write(b)
	w.WriteByte('\n')
	w.Flush()
}

// appendLine añade una línea a un fichero (O_APPEND|O_CREATE); best-effort.
func appendLine(path, line string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}

func main() {
	pidfile := flag.String("pidfile", "", "fichero donde escribir el PID al arrancar")
	invocations := flag.String("invocations", "", "fichero al que añadir cada tools/call de echo")
	flag.Parse()

	// Efecto de disco 1: al arrancar, dejamos el PID en el pidfile (si se pidió).
	if *pidfile != "" {
		os.WriteFile(*pidfile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}

	r := bufio.NewReader(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return
		}
		var msg rpc
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			respond(w, msg.ID, map[string]interface{}{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]interface{}{"name": "test-mcp", "version": "0.1.0"},
			})
		case "notifications/initialized":
			// notificación: sin respuesta.
		case "tools/list":
			respond(w, msg.ID, map[string]interface{}{
				"tools": []map[string]interface{}{
					{
						"name":        "echo",
						"description": "Devuelve el texto recibido.",
						"inputSchema": map[string]interface{}{
							"type":       "object",
							"properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}},
							"required":   []string{"text"},
						},
					},
				},
			})
		case "tools/call":
			var p struct {
				Name      string                 ` + "`json:\"name\"`" + `
				Arguments map[string]interface{} ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(msg.Params, &p)
			if p.Name == "echo" {
				txt, _ := p.Arguments["text"].(string)
				// Efecto de disco 2: cada invocación real de echo deja rastro.
				appendLine(*invocations, txt)
				respond(w, msg.ID, map[string]interface{}{
					"content": []map[string]interface{}{{"type": "text", "text": "eco: " + txt}},
				})
			} else {
				respond(w, msg.ID, map[string]interface{}{
					"content": []map[string]interface{}{{"type": "text", "text": "tool desconocida"}},
					"isError": true,
				})
			}
		default:
			if len(msg.ID) > 0 {
				out := map[string]interface{}{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID),
					"error": map[string]interface{}{"code": -32601, "message": "method not found: " + msg.Method}}
				b, _ := json.Marshal(out)
				w.Write(b)
				w.WriteByte('\n')
				w.Flush()
			}
		}
	}
}
`

var (
	mcpTestServerOnce sync.Once
	mcpTestServerBin  string
	mcpTestServerOut  string // salida del build si falló (para el mensaje de error)
	mcpTestServerErr  error
)

// buildMcpTestServer compila (una vez por ejecución de la suite) el servidor MCP
// de prueba a un binario temporal y devuelve su ruta. Mismo patrón que el arnés
// usa para el binario `enu`: `go build` con CGO desactivado, sin red. Es un
// HELPER PRIVADO de este fichero: el arnés no ofrece un compilador de fixtures
// auxiliares y el `buildMCPServer` in-process vive en otro paquete (no importable).
func buildMcpTestServer(t *testing.T) string {
	t.Helper()
	mcpTestServerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "enu-e2e-mcpserver-")
		if err != nil {
			mcpTestServerErr = err
			return
		}
		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte(mcpTestServerSource), 0o644); err != nil {
			mcpTestServerErr = err
			return
		}
		bin := filepath.Join(dir, "mcpserver")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, src)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			mcpTestServerErr = err
			mcpTestServerOut = string(out)
			return
		}
		mcpTestServerBin = bin
	})
	if mcpTestServerErr != nil {
		t.Fatalf("no se pudo compilar el servidor MCP de prueba: %v\n%s", mcpTestServerErr, mcpTestServerOut)
	}
	return mcpTestServerBin
}

// writeMcpToml escribe `mcp.toml` en el ConfigDir con un único servidor `srv`
// cuyo argv se pasa tal cual. Los efectos de disco del servidor de prueba
// (pidfile, invocations) viajan como argumentos DENTRO de `command`, no por
// `env` (que la primitiva ignora si es array; ver la nota del fixture). Helper
// privado: el arnés cubre enu.toml/providers/agent, pero no la config de `mcp`.
func writeMcpToml(t *testing.T, ws *Workspace, command []string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("[servers.srv]\n")
	b.WriteString("command = [")
	for i, c := range command {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(tomlString(c))
	}
	b.WriteString("]\n")
	ws.WriteConfig(t, "mcp.toml", b.String())
}

// tomlString serializa `s` como cadena básica TOML, escapando `\` y `"`. Las
// rutas de t.TempDir() (bajo /var/folders o /tmp) no traen caracteres exóticos,
// pero escapamos por corrección.
func tomlString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// ---------------------------------------------------------------------------
// Escenario 1 (MÍNIMO IMPRESCINDIBLE): ciclo completo con un servidor MCP real
// DECLARADO EN `mcp.toml` e invocado por un turno de agente. Servidor real por
// stdio, leído del fichero (nunca `mcp.connect` a mano), su tool ejecutada por
// el agente, y el resultado en el texto final —todo observado desde fuera del
// proceso (dos ficheros de disco) a través del binario compilado—.
//
// ADAPTACIÓN respecto al enunciado original (que pedía `enu -p` + auto-connect):
// el auto-connect headless de `mcp.toml` NO deja el servidor disponible para el
// turno —la task del auto-connect (embedded/mcp/init.lua:35) no sobrevive a
// `connect_configured`, así que su `enu.task.cleanup` cierra la conexión y sus
// tools quedan como stubs de "servidor desconectado" antes de que arranque el
// turno de `-p` (ver la nota de hallazgo al final del fichero)—. Por eso aquí el
// turno se conduce con `enu -e` en UNA task que llama a `mcp.connect_configured`
// (que SÍ lee `mcp.toml`, el criterio "declarado en disco") y, con la conexión
// aún viva en esa misma task, corre un `agent.session` contra el adaptador
// anthropic real (sobre el FakeProvider). Sigue siendo e2e del binario: mcp.toml
// real, subproceso real, JSON-RPC/stdio real, HTTP/SSE real; solo cambia el
// disparador del turno, porque el `-p` + auto-connect está roto de fábrica.
// ---------------------------------------------------------------------------

func TestMcpE2EAgentInvokesConfiguredTool(t *testing.T) {
	t.Run("dado_mcp_toml_con_servidor_cuando_el_agente_pide_la_tool_entonces_el_resultado_llega_a_disco", func(t *testing.T) {
		ws := NewWorkspace(t)
		bin := buildMcpTestServer(t)
		tmp := t.TempDir()
		invLog := filepath.Join(tmp, "invocations.log") // rastro de cada tools/call real
		replyFile := filepath.Join(tmp, "reply.txt")    // texto final del asistente

		ws.WriteEnuToml(t, "providers", "sessions", "agent", "mcp")
		fp := NewFakeProvider(t)
		ws.UseFakeProvider(t, fp)
		writeMcpToml(t, ws, []string{bin, "-invocations", invLog})

		// El modelo pide la tool MCP y, tras el tool_result, redacta el texto final.
		fp.PushToolUse("call-1", "mcp__srv__echo", map[string]any{"text": "hola MCP"})
		fp.PushText("la tool dijo: eco: hola MCP")

		// El driver Lua: connect_configured lee mcp.toml y deja las conexiones VIVAS
		// en esta task; la misma task corre la sesión, así la conexión sigue en pie
		// al invocar. El texto final del turno se vuelca a un fichero (observable
		// externo, como el `-p` lo vuelca a stdout).
		res := ws.Run(t, RunOpts{Args: []string{"-e", driveConfiguredToolLua(invLogAllow, replyFile)}})

		if res.ExitCode != 0 {
			t.Fatalf("exit: got %d, want 0 (stdout=%q, stderr=%q)", res.ExitCode, res.Stdout, res.Stderr)
		}
		if fp.RequestCount() < 2 {
			t.Fatalf("el loop de tools debía disparar >=2 requests; got %d", fp.RequestCount())
		}
		// El texto final del asistente (tras el tool_result) llegó completo.
		reply, err := os.ReadFile(replyFile)
		if err != nil {
			t.Fatalf("el turno debía haber dejado el texto final en %s: %v (stderr=%q)", replyFile, err, res.Stderr)
		}
		if !strings.Contains(string(reply), "la tool dijo: eco: hola MCP") {
			t.Fatalf("el texto final debía traer el eco de la tool; got %q", string(reply))
		}
		// Prueba de que el subproceso REAL ejecutó tools/call por stdio: el fichero de
		// invocaciones tiene exactamente una línea con el texto echoado.
		data, err := os.ReadFile(invLog)
		if err != nil {
			t.Fatalf("el servidor MCP debía haber escrito %s: %v", invLog, err)
		}
		lines := nonEmptyLines(string(data))
		if len(lines) != 1 || !strings.Contains(lines[0], "hola MCP") {
			t.Fatalf("invocations.log debía tener 1 línea con \"hola MCP\"; got %q", string(data))
		}
	})
}

// invLogAllow marca, en driveConfiguredToolLua, que la sesión concede la tool con
// `allow` (el caso del escenario 1). Es un simple booleano legible.
const invLogAllow = true

// driveConfiguredToolLua construye el script `-e` que conduce el turno: conecta
// los servidores de `mcp.toml` con `connect_configured` (leyéndolo de disco),
// abre un `agent.session` contra el modelo real del fake, envía un mensaje que
// el fake resuelve pidiendo la tool MCP, y vuelca el texto final del asistente a
// `replyFile`. Si `allow` es true, concede `mcp__srv__echo` (nombre EXACTO, sin
// glob —G53/ADR-023—); si es false, no la concede (queda a merced de la valla de
// confianza). Todo dentro de una task, con pcall para no colgar el drenaje.
func driveConfiguredToolLua(allow bool, replyFile string) string {
	perms := ""
	if allow {
		perms = `, permissions = { allow = { "mcp__srv__echo" } }`
	}
	return `enu.task.spawn(function()
	  local ok, e = pcall(function()
	    local mcp = require("mcp")
	    local agent = require("agent")
	    local conns = mcp.connect_configured()   -- lee mcp.toml y conecta (declaración en disco)
	    local s = agent.session{ model = "anthropic/opus", no_store = true` + perms + ` }
	    local reply = s:send("usa la tool echo del servidor mcp")
	    local txt = ""
	    for _, b in ipairs(reply and reply.content or {}) do
	      if b.type == "text" then txt = txt .. b.text end
	    end
	    enu.fs.write("` + replyFile + `", txt)
	    s:close()
	    for _, c in ipairs(conns) do c:close() end
	  end)
	  if not ok then
	    enu.fs.write("` + replyFile + `", "ERR: " .. tostring((type(e) == "table" and (e.message or e.code)) or e))
	  end
	end)
	return "ok"`
}

// ---------------------------------------------------------------------------
// Escenario 2: cleanup del subproceso al terminar el binario. El servidor deja
// su PID en un fichero; tras `enu` retornar, ese PID debe estar MUERTO. La
// garantía externa es "un `enu -p ...` que termina no deja ningún subproceso MCP
// huérfano" —lo cumplen tanto el cleanup del auto-connect (que cierra la conexión
// al terminar su task) como, red final, el `defer rt.Close()` → stopAllProcs()
// de main.go—. Sin instrumentar el runtime Go: solo señales del SO.
// ---------------------------------------------------------------------------

func TestMcpE2EServerKilledOnProcessExit(t *testing.T) {
	t.Run("dado_servidor_mcp_conectado_cuando_el_binario_enu_termina_entonces_el_subproceso_esta_muerto", func(t *testing.T) {
		ws := NewWorkspace(t)
		bin := buildMcpTestServer(t)
		pidfile := filepath.Join(t.TempDir(), "pid")

		ws.WriteEnuToml(t, "providers", "sessions", "agent", "mcp")
		fp := NewFakeProvider(t)
		ws.UseFakeProvider(t, fp)
		writeMcpToml(t, ws, []string{bin, "-pidfile", pidfile})

		// Turno sin tool: basta con que el auto-connect lance el servidor.
		fp.PushText("listo")

		res := ws.Run(t, RunOpts{Args: []string{"-p", "saluda"}})
		if res.ExitCode != 0 {
			t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
		}

		// Run es bloqueante: al retornar, `enu` ya terminó. Leemos el PID que dejó el
		// servidor y comprobamos que el proceso ya no existe.
		if !waitFile(pidfile, 2*time.Second) {
			t.Fatalf("el servidor MCP debía haber escrito su pidfile (%s); ¿no arrancó?", pidfile)
		}
		pid := readPid(t, pidfile)

		// Contraste (descarta falsos negativos del helper): un servidor idéntico
		// lanzado FUERA de `enu` sí se detecta vivo antes de matarlo.
		assertHelperDetectsLiveProcess(t, bin)

		if !waitPidDead(pid, 5*time.Second) {
			t.Fatalf("el servidor MCP (pid %d) debía estar muerto tras terminar enu "+
				"(cleanup del auto-connect y/o stopAllProcs)", pid)
		}
	})
}

// assertHelperDetectsLiveProcess lanza el servidor de prueba con os/exec (fuera
// de `enu`) y confirma que pidAlive lo ve vivo, para descartar que waitPidDead dé
// un falso "muerto" por un bug del propio helper. Lo mata al terminar.
func assertHelperDetectsLiveProcess(t *testing.T, bin string) {
	t.Helper()
	cmd := exec.Command(bin)
	// Sin stdin conectado el servidor lee EOF y sale; le damos una tubería viva.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("contraste: StdinPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("contraste: no se pudo lanzar el servidor de control: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if !pidAlive(cmd.Process.Pid) {
		t.Fatalf("contraste: el helper pidAlive no detecta vivo un proceso recién lanzado (pid %d)", cmd.Process.Pid)
	}
}

// ---------------------------------------------------------------------------
// Escenario 3: `mcp.toml` mal formado no rompe el arranque. El turno se ejecuta
// con normalidad y la degradación se registra en `<data_dir>/enu.log` (WARN),
// nunca en stderr (por diseño de enu.log). Aquí el auto-connect de `-p` SÍ es
// suficiente: solo observamos que el fallo degrada en el log, no que la tool
// llegue a un turno.
// ---------------------------------------------------------------------------

func TestMcpE2ETomlMalformedDoesNotBlockBoot(t *testing.T) {
	t.Run("dado_mcp_toml_invalido_cuando_arranca_headless_entonces_boot_ok_y_warning_en_el_log", func(t *testing.T) {
		ws := NewWorkspace(t)

		ws.WriteEnuToml(t, "providers", "sessions", "agent", "mcp")
		fp := NewFakeProvider(t)
		ws.UseFakeProvider(t, fp)
		// TOML roto: cabecera de tabla sin cerrar. `enu.toml.decode` lo rechaza.
		ws.WriteConfig(t, "mcp.toml", "[servers.srv\ncommand = [\"x\"]\n")

		fp.PushText("hola sin mcp")

		res := ws.Run(t, RunOpts{Args: []string{"-p", "saluda"}})

		if res.ExitCode != 0 {
			t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
		}
		if !strings.Contains(res.Stdout, "hola sin mcp") {
			t.Fatalf("el turno debía ejecutarse pese al mcp.toml roto; stdout=%q", res.Stdout)
		}
		// El fallo del auto-connect degrada en el LOG, no en pantalla.
		logText := readLog(t, ws)
		if !strings.Contains(logText, "WARN") || !strings.Contains(logText, "mal formado") {
			t.Fatalf("enu.log debía traer un WARN de mcp.toml mal formado; got:\n%s", logText)
		}
		if strings.Contains(res.Stderr, "mcp") || strings.Contains(res.Stderr, "mal formado") {
			t.Fatalf("el fallo de MCP no debía asomar a stderr; got %q", res.Stderr)
		}
	})
}

// ---------------------------------------------------------------------------
// Escenario 4: comando de servidor inexistente (ENOENT) degrada solo ese
// servidor. TOML válido pero `command` a una ruta que no existe: el fallo lo
// atrapa el pcall POR SERVIDOR de connect_configured (mensaje distinto al del
// escenario 3: nombra el servidor, no "mal formado").
// ---------------------------------------------------------------------------

func TestMcpE2EServerCommandNotFoundDegradesGracefully(t *testing.T) {
	t.Run("dado_command_inexistente_cuando_arranca_headless_entonces_boot_ok_y_solo_ese_servidor_degrada", func(t *testing.T) {
		ws := NewWorkspace(t)

		ws.WriteEnuToml(t, "providers", "sessions", "agent", "mcp")
		fp := NewFakeProvider(t)
		ws.UseFakeProvider(t, fp)
		// TOML VÁLIDO, pero el binario del servidor no existe: falla al hacer spawn.
		missing := filepath.Join(t.TempDir(), "no", "existe", "mcpserver-fake")
		writeMcpToml(t, ws, []string{missing})

		fp.PushText("hola sin mcp")

		res := ws.Run(t, RunOpts{Args: []string{"-p", "saluda"}})

		if res.ExitCode != 0 {
			t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
		}
		if !strings.Contains(res.Stdout, "hola sin mcp") {
			t.Fatalf("el turno debía ejecutarse pese al servidor caído; stdout=%q", res.Stdout)
		}
		logText := readLog(t, ws)
		// El pcall por servidor loguea "no se pudo conectar el servidor \"srv\"".
		if !strings.Contains(logText, "WARN") || !strings.Contains(logText, "no se pudo conectar") || !strings.Contains(logText, "srv") {
			t.Fatalf("enu.log debía traer un WARN nombrando el servidor caído; got:\n%s", logText)
		}
		// A diferencia del escenario 3, aquí el TOML NO estaba mal formado.
		if strings.Contains(logText, "mal formado") {
			t.Fatalf("un command inexistente no es un mcp.toml mal formado; log:\n%s", logText)
		}
	})
}

// ---------------------------------------------------------------------------
// Utilidades locales.
// ---------------------------------------------------------------------------

// nonEmptyLines parte un texto en líneas descartando las vacías (para contar
// líneas reales de un fichero append-only).
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// readPid lee y parsea el PID que el servidor de prueba dejó en su pidfile.
func readPid(t *testing.T, pidfile string) int {
	t.Helper()
	data, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("no se pudo leer el pidfile %s: %v", pidfile, err)
	}
	pid := 0
	for _, c := range strings.TrimSpace(string(data)) {
		if c < '0' || c > '9' {
			t.Fatalf("pidfile con contenido no numérico: %q", string(data))
		}
		pid = pid*10 + int(c-'0')
	}
	if pid <= 0 {
		t.Fatalf("pidfile con pid inválido: %q", string(data))
	}
	return pid
}

// readLog lee `<data_dir>/enu.log` del workspace. El log se abre perezosamente
// en la primera escritura, así que ausente = nadie logueó (fallo del test que lo
// espera).
func readLog(t *testing.T, ws *Workspace) string {
	t.Helper()
	path := filepath.Join(ws.DataDir, "enu.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no se pudo leer el log %s (¿nadie logueó?): %v", path, err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// HALLAZGOS que esta suite destapó (candidatos a G##; NO es tarea de este
// fichero resolverlos, solo dejarlos registrados donde el siguiente los vea).
//
//   1. AUTO-CONNECT DE `mcp.toml` INUTILIZABLE EN HEADLESS `-p`. La task del
//      auto-connect (internal/runtime/embedded/mcp/init.lua:35) hace
//      `pcall(mcp.connect_configured)` y RETORNA: al terminar esa task, su
//      `enu.task.cleanup` (registrado en M.connect) cierra cada conexión, mata
//      el subproceso y re-registra las tools como stubs de "servidor
//      desconectado" (permissions.default="deny", handler que lanza EMCP). Todo
//      esto ocurre DURANTE `Boot` (RunTasks drena la task a quiescencia), así que
//      cuando arranca el turno de `-p` los servidores de `mcp.toml` ya están
//      caídos y sus tools son stubs. Comprobado desde fuera: tras el boot,
//      `mcp.servers()` está vacío pero `mcp__srv__echo` sigue en `agent.tools()`
//      (el stub). Un `-p` que pide esa tool NO invoca el servidor real y NO da
//      exit 3 (el stub de deny devuelve tool_result is_error → exit 0). El propio
//      módulo se contradice: connect_configured se documenta como "corre en una
//      task de LARGA VIDA" (mcp/lua/mcp/init.lua:463) pero la task que la llama es
//      efímera. Mantener un servidor vivo entre boot y turno tampoco es posible
//      desde config: cualquier task viva al cerrar el boot (p. ej. un
//      `enu.task.sleep` largo en el init.lua del usuario) BLOQUEA el boot (lo
//      espera RunTasks) hasta el timeout. CONSECUENCIA para esta suite: el
//      escenario 1 (mínimo imprescindible) se conduce con `enu -e` +
//      `connect_configured` + `agent.session` en UNA task —sigue leyendo mcp.toml
//      y ejerciendo servidor/stdio/HTTP reales—, y el escenario 2 original
//      (deny → exit 3 vía tool MCP en `-p`) se RECORTÓ por inalcanzable: la tool
//      MCP nunca llega viva a un turno de `-p`.
//
//   2. `env` DE `mcp.toml` (ARRAY) NO LLEGA AL SUBPROCESO. `mcp.toml` documenta
//      `env = ["K=V", ...]` (array; embedded/mcp/lua/mcp/init.lua:428), y ese
//      array se pasa tal cual a `enu.proc.spawn`. Pero la primitiva SOLO
//      interpreta `env` como tabla { K = V } (map string→string;
//      internal/runtime/vmwasm_proc.go:250): un array es `[]any`, no
//      `map[string]any`, así que se IGNORA en silencio y el hijo hereda el
//      entorno sin las claves declaradas. Verificado e2e: un servidor MCP
//      declarado con `env` no recibe la variable. Por eso esta suite pasa los
//      ajustes del servidor de prueba por ARGV (que sí viaja íntegro), no por
//      `env`. O el doc de mcp.toml debe pasar a `env = { K = "V" }` (map), o
//      `connect_configured` debe traducir el array a map antes del spawn.
//
//   3. (Menor, ya anotado por el escenarista) El comentario de
//      embedded/mcp/lua/mcp/init.lua:24 sugiere `allow = {"mcp__<srv>__*"}` (glob),
//      pero agente.md §5 (G53/ADR-023) prohíbe el glob sobre nombres: el patrón
//      casa por nombre EXACTO. Un autor de config que copie ese comentario
//      escribiría un `allow` que no concede nada. Los escenarios usan el nombre
//      exacto (`mcp__srv__echo`).
// ---------------------------------------------------------------------------
