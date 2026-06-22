package runtime

// Tests de la extensión oficial `mcp` (S41, embebida en
// internal/runtime/embedded/mcp). Es la **capa 2** de arquitectura.md (procesos
// externos vía JSON-RPC/stdio): Lua sobre la API pública congelada (Fase 8,
// ADR-003 — el core NO sabe lo que es MCP), construida sobre `nu.proc` (S16),
// `nu.json` (S18) y la extensión `agent` (S39).
//
// Blinda el CICLO COMPLETO (criterio de hecho de S41): un servidor MCP de prueba
// se LANZA por la extensión, ANUNCIA sus tools (tools/list), se REGISTRAN en el
// agente con su confianza, y el AGENTE las INVOCA (tools/call) obteniendo el
// resultado, con cierre limpio del proceso.
//
// El servidor MCP de prueba es un pequeño programa Go (testdata/mcpserver) que el
// test COMPILA a un binario temporal con `go build` y que habla JSON-RPC 2.0 por
// stdio (framing newline-delimited): responde a `initialize`, `notifications/
// initialized`, `tools/list` (anuncia una tool `echo` y una `add`) y `tools/call`
// (las ejecuta). Es la opción más robusta y sin dependencias de red (la sugerida
// por el enunciado de S41).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// ---------------------------------------------------------------------------
// Fuente del servidor MCP de prueba. Se escribe a un fichero temporal y se
// compila con `go build`. Habla JSON-RPC 2.0 por stdio, framing por líneas.
// ---------------------------------------------------------------------------

const mcpServerSource = `package main

import (
	"bufio"
	"encoding/json"
	"os"
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

func main() {
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
					{
						"name":        "boom",
						"description": "Devuelve un resultado de error MCP.",
						"inputSchema": map[string]interface{}{"type": "object"},
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
				respond(w, msg.ID, map[string]interface{}{
					"content": []map[string]interface{}{{"type": "text", "text": "eco: " + txt}},
				})
			} else if p.Name == "boom" {
				respond(w, msg.ID, map[string]interface{}{
					"content": []map[string]interface{}{{"type": "text", "text": "explotó"}},
					"isError": true,
				})
			} else {
				respond(w, msg.ID, map[string]interface{}{
					"content": []map[string]interface{}{{"type": "text", "text": "tool desconocida"}},
					"isError": true,
				})
			}
		default:
			// Método desconocido: respondemos error JSON-RPC si trae id.
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
	mcpServerOnce sync.Once
	mcpServerBin  string
	mcpServerErr  error
)

// buildMCPServer compila (una vez por ejecución de la suite) el servidor MCP de
// prueba a un binario temporal y devuelve su ruta. Usa `go build`, garantizado en
// el entorno (es un proyecto Go); sin red ni dependencias externas.
func buildMCPServer(t *testing.T) string {
	t.Helper()
	mcpServerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nu-mcpserver-")
		if err != nil {
			mcpServerErr = err
			return
		}
		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte(mcpServerSource), 0o644); err != nil {
			mcpServerErr = err
			return
		}
		bin := filepath.Join(dir, "mcpserver")
		cmd := exec.Command("go", "build", "-o", bin, src)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			mcpServerErr = err
			mcpServerBin = string(out)
			return
		}
		mcpServerBin = bin
	})
	if mcpServerErr != nil {
		t.Fatalf("no se pudo compilar el servidor MCP de prueba: %v\n%s", mcpServerErr, mcpServerBin)
	}
	return mcpServerBin
}

// bootMCP arranca un Runtime con providers+sessions+agent+mcp activadas, headless.
func bootMCP(t *testing.T) (*harness, string) {
	t.Helper()
	return bootMCPWith(t, nil)
}

// bootMCPWith es como bootMCP pero permite registrar helpers Go ANTES de Boot
// (`preBoot`). Registrar globales tras el Boot es una carrera con el scheduler
// (el auto-connect de mcp ya corre); quien necesite un helper Go lo instala aquí.
func bootMCPWith(t *testing.T, preBoot func(rt *Runtime)) (*harness, string) {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\", \"mcp\"]\n")
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersTomlToolStub), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if preBoot != nil {
		preBoot(rt)
	}
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}, dataDir
}

// TestMCPCargaYActiva: la extensión carga (source="builtin") y expone su API.
func TestMCPCargaYActiva(t *testing.T) {
	h, _ := bootMCP(t)
	if src := listSource(h, "mcp"); src != "builtin" {
		t.Fatalf(`mcp debía cargarse con source="builtin"; got %q`, src)
	}
	h.expectEval(`
		local m = require("mcp")
		assert(type(m.connect) == "function", "connect")
		assert(type(m.servers) == "function", "servers")
		assert(type(m.get) == "function", "get")
		return "ok"`, "ok")
}

// TestMCPConnectListAndRegister (criterio de hecho, parte 1): la extensión LANZA
// el servidor MCP de prueba, completa el handshake, ANUNCIA sus tools (tools/list)
// y las REGISTRA en el agente con su prefijo `mcp__<srv>__<tool>` y su confianza
// (default "ask", por ser externas).
func TestMCPConnectListAndRegister(t *testing.T) {
	h, _ := bootMCP(t)
	bin := buildMCPServer(t)

	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local mcp = require("mcp")
				local agent = require("agent")
				local conn = mcp.connect{ name = "test", command = { "` + bin + `" } }
				CONN_NAME = conn.name
				SERVERS = #mcp.servers()
				-- tools/list anunció echo y boom; se registraron en el agente con prefijo.
				local names = {}
				for _, td in ipairs(agent.tools()) do names[td.name] = td end
				HAS_ECHO = names["mcp__test__echo"] ~= nil
				HAS_BOOM = names["mcp__test__boom"] ~= nil
				ECHO_DESC = names["mcp__test__echo"] and names["mcp__test__echo"].description or ""
				conn:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.message or e.code)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(CONN_NAME)`, "test")
	h.expectEval(`return tostring(SERVERS)`, "1")
	h.expectEval(`return tostring(HAS_ECHO)`, "true")
	h.expectEval(`return tostring(HAS_BOOM)`, "true")
	h.expectEval(`return tostring(ECHO_DESC)`, "Devuelve el texto recibido.")
}

// TestMCPAgentInvokesTool (criterio de hecho, parte 2 — EL CICLO COMPLETO): el
// AGENTE invoca una tool MCP. El adaptador de prueba pide la tool `mcp__test__echo`;
// el handler registrado hace `tools/call` al servidor por JSON-RPC y devuelve su
// resultado, que se realimenta al modelo. La tool se concede con `allow` (es
// externa → default "ask"; allow explícito demuestra la valla de confianza).
func TestMCPAgentInvokesTool(t *testing.T) {
	h, _ := bootMCP(t)
	bin := buildMCPServer(t)

	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local mcp = require("mcp")
				local agent = require("agent")
				` + registerToolStub + `
				local conn = mcp.connect{ name = "srv", command = { "` + bin + `" } }
				CONN = conn
				TOOLNAME = "mcp__srv__echo"
				TOOLARGS = { text = "hola MCP" }
				-- allow explícito: la tool externa requiere permiso (confianza).
				local s = agent.session{ model = "test/m", no_store = true,
					permissions = { allow = { "mcp__srv__echo" } } }
				s:send("usa la tool MCP")
				-- El tool_result (history[3]) trae lo que devolvió el servidor MCP.
				local res = s.history[3].content[1]
				IS_ERROR = res.is_error == true
				RESULT_TEXT = res.content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.message or e.code)) or tostring(e) end
			out = "done"
		end)
		-- cerramos la conexión tras el turno (vida del proceso).
		nu.task.spawn(function() if CONN then CONN:close() end end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(IS_ERROR)`, "false")
	// El servidor MCP devolvió "eco: hola MCP" vía tools/call.
	h.expectEval(`return tostring(RESULT_TEXT)`, "eco: hola MCP")
}

// TestMCPToolTrustHeadlessDeny (CONFIANZA, agente.md §5): una tool MCP SIN allow
// en headless es DENEGADA (es externa, default "ask"). El error es accionable y el
// turno no se rompe (tool_result is_error). Demuestra la valla de confianza.
func TestMCPToolTrustHeadlessDeny(t *testing.T) {
	h, _ := bootMCP(t)
	bin := buildMCPServer(t)

	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local mcp = require("mcp")
				local agent = require("agent")
				` + registerToolStub + `
				local conn = mcp.connect{ name = "srv2", command = { "` + bin + `" } }
				CONN2 = conn
				TOOLNAME = "mcp__srv2__echo"
				TOOLARGS = { text = "no debería ejecutarse" }
				-- SIN allow: en headless, default "ask" → DENY.
				local s = agent.session{ model = "test/m", no_store = true }
				s:send("intenta la tool MCP")
				local res = s.history[3].content[1]
				IS_ERROR = res.is_error == true
				DENY_TEXT = res.content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.message or e.code)) or tostring(e) end
			out = "done"
		end)
		nu.task.spawn(function() if CONN2 then CONN2:close() end end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(IS_ERROR)`, "true")
	denyText := h.eval(`return tostring(DENY_TEXT)`)[0]
	if !strings.Contains(denyText, "headless") || !strings.Contains(denyText, "mcp__srv2__echo") || !strings.Contains(denyText, "allow") {
		t.Fatalf("confianza MCP: el permiso denegado no es accionable: %q", denyText)
	}
}

// TestMCPToolServerError (mapeo de resultados): una tool cuyo servidor devuelve
// `isError=true` se propaga como tool_result is_error (el modelo lo ve). El turno
// no se rompe.
func TestMCPToolServerError(t *testing.T) {
	h, _ := bootMCP(t)
	bin := buildMCPServer(t)

	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local mcp = require("mcp")
				local agent = require("agent")
				` + registerToolStub + `
				local conn = mcp.connect{ name = "srv3", command = { "` + bin + `" } }
				CONN3 = conn
				TOOLNAME = "mcp__srv3__boom"
				TOOLARGS = {}
				local s = agent.session{ model = "test/m", no_store = true,
					permissions = { allow = { "mcp__srv3__boom" } } }
				s:send("usa boom")
				local res = s.history[3].content[1]
				IS_ERROR = res.is_error == true
				ERR_TEXT = res.content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.message or e.code)) or tostring(e) end
			out = "done"
		end)
		nu.task.spawn(function() if CONN3 then CONN3:close() end end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(IS_ERROR)`, "true")
	errText := h.eval(`return tostring(ERR_TEXT)`)[0]
	if !strings.Contains(errText, "explotó") {
		t.Fatalf("el isError del servidor MCP no se propagó: %q", errText)
	}
}

// TestMCPProcessLifecycle (ciclo de vida): el proceso del servidor se LANZA, vive
// mientras la conexión existe, y se MATA limpiamente al cerrar. El pid del
// subproceso se publica al lado Go con un helper (`__mcp_pid`, reusa `procPidFromUD`
// de proc_test) y se comprueba con `pidAlive` que muere tras `close()`.
func TestMCPProcessLifecycle(t *testing.T) {
	bin := buildMCPServer(t)
	pidCh := make(chan int, 1)
	// Helpers Go registrados ANTES de Boot (evita la carrera con el scheduler).
	h, _ := bootMCPWith(t, func(rt *Runtime) {
		rt.L.SetGlobal("__publish_pid", rt.L.NewFunction(func(L *lua.LState) int {
			pidCh <- int(L.CheckNumber(1))
			return 0
		}))
		rt.L.SetGlobal("__mcp_pid", rt.L.NewFunction(procPidFromUD))
	})

	h.eval(`
		out, errc, ALIVE_BEFORE = nil, nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local mcp = require("mcp")
				local conn = mcp.connect{ name = "life", command = { "` + bin + `" } }
				__publish_pid(__mcp_pid(conn.proc))
				ALIVE_BEFORE = nu.proc.alive(__mcp_pid(conn.proc))
				conn:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.message or e.code)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(ALIVE_BEFORE)`, "true")

	pid := <-pidCh
	// Tras close, el proceso debe morir (vida por cleanup/kill, api.md §6).
	if !waitDead(pid, 5*time.Second) {
		t.Fatalf("ciclo de vida: el servidor MCP (pid %d) debería estar muerto tras close()", pid)
	}
}
