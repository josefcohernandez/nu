package e2e

// Tests e2e del plugin oficial `agent` contra el BINARIO real: el turno headless
// (`enu -p`) completo, con el adaptador `anthropic` REAL hablando con el FakeProvider
// por HTTP, y la verificación de lo que VIAJÓ por el wire (`fp.Requests()`). Cubren el
// hueco que ni `smoke_test.go` (turno de texto y loop de read_file) ni `main_test.go`
// (permisos in-process con stub, sin HTTP) tocan: la tool SENSIBLE (`write_file`), la
// convención de exit codes de S45 (0/1/3) sobre el proceso, y la precedencia de
// `--model`/`--auto-permissions` (agente.md §2, §5, §10).
//
// Prefijo TestAgent* para filtrarlos con `-run TestAgent`.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findToolResult recorre `messages` (el array de mensajes de una request de la Messages
// API, ya decodificado a []any de map[string]any) buscando el bloque `tool_result` cuyo
// `tool_use_id` coincida. Devuelve el bloque y si lo encontró. Cada mensaje lleva
// `content` = array de bloques; el tool_result vive dentro del content de un mensaje de
// rol user (adapter_anthropic.lua §canon_block_to_wire). Es un helper PRIVADO de este
// fichero: el arnés expone los cuerpos crudos (`Requests()`) pero no navega su forma.
func findToolResult(messages any, toolUseID string) (map[string]any, bool) {
	msgs, ok := messages.([]any)
	if !ok {
		return nil, false
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, b := range blocks {
			blk, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if blk["type"] == "tool_result" && blk["tool_use_id"] == toolUseID {
				return blk, true
			}
		}
	}
	return nil, false
}

// hasToolResult indica si ALGÚN mensaje del turno trae un bloque tool_result (sin exigir
// un id concreto). Sirve para afirmar que el PRIMER turno NO lo tiene (aún no se ejecutó
// ninguna tool) y que el SEGUNDO sí.
func hasToolResult(messages any) bool {
	msgs, ok := messages.([]any)
	if !ok {
		return false
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, b := range blocks {
			if blk, ok := b.(map[string]any); ok && blk["type"] == "tool_result" {
				return true
			}
		}
	}
	return false
}

// blockText concatena el texto de los bloques de texto del `content` de un tool_result
// (el content es a su vez Block[], adapter_anthropic.lua §85-91). El mensaje accionable
// del deny viaja ahí como texto (agente.md §5 amortiguador 2).
func blockText(toolResult map[string]any) string {
	content, ok := toolResult["content"].([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, b := range content {
		if blk, ok := b.(map[string]any); ok && blk["type"] == "text" {
			if s, ok := blk["text"].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

// writeTwoModelProvider escribe un `providers.toml` con DOS modelos del mismo provider
// fake (alias `opus` → id `claude-e2e-default`; alias `sonnet` → id
// `claude-e2e-override`) y un `agent.toml` con `model = "anthropic/opus"`. Es el arnés
// mínimo del escenario de precedencia de `--model`, que `UseFakeProvider` (un solo
// modelo) no cubre. Helper PRIVADO de este fichero.
func writeTwoModelProvider(t *testing.T, ws *Workspace, fp *FakeProvider) {
	t.Helper()
	toml := "" +
		"[providers.anthropic]\n" +
		"adapter     = \"anthropic\"\n" +
		"base_url    = \"" + fp.URL() + "\"\n" +
		"api_key_env = \"" + FakeAPIKeyEnv + "\"\n\n" +
		"[[providers.anthropic.models]]\n" +
		"id         = \"claude-e2e-default\"\n" +
		"context    = 200000\n" +
		"max_output = 4096\n" +
		"aliases    = [\"opus\"]\n\n" +
		"[[providers.anthropic.models]]\n" +
		"id         = \"claude-e2e-override\"\n" +
		"context    = 200000\n" +
		"max_output = 4096\n" +
		"aliases    = [\"sonnet\"]\n"
	ws.WriteConfig(t, "providers.toml", toml)
	ws.WriteAgentToml(t, "anthropic/opus")
}

// TestAgentE2EToolLoopAutoPermissionsAllowsWriteFile — [ESCENARIO 1, mínimo]. El camino
// feliz completo: el modelo pide `write_file` (tool SENSIBLE, default "ask" → deny en
// headless), `--auto-permissions` la concede, el binario la ejecuta, devuelve el
// tool_result al modelo y este cierra con texto. Se verifica el exit 0, el texto final,
// los DOS turnos, la forma del tool_result en el wire (id correcto, sin is_error) y que
// el fichero acabó en disco. Es el "camino feliz" pedido explícitamente.
// (agente.md §5 amortiguador 3 --auto-permissions + §3 write_file)
func TestAgentE2EToolLoopAutoPermissionsAllowsWriteFile(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	fp.PushToolUse("call-1", "write_file", map[string]any{"path": "out.txt", "content": "hola e2e"})
	fp.PushText("fichero escrito")

	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	ws.UseFakeProvider(t, fp)

	res := ws.Run(t, RunOpts{Args: []string{"-p", "escribe out.txt", "--auto-permissions"}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "fichero escrito") {
		t.Fatalf("stdout debía traer el texto del 2º turno; got %q (stderr=%q)", res.Stdout, res.Stderr)
	}
	if fp.RequestCount() != 2 {
		t.Fatalf("esperaba 2 requests (tool_use + tool_result); got %d", fp.RequestCount())
	}

	reqs := fp.Requests()
	// El PRIMER turno es el prompt inicial: aún no se ejecutó ninguna tool.
	if hasToolResult(reqs[0]["messages"]) {
		t.Fatalf("el 1er turno no debía llevar tool_result todavía; messages=%v", reqs[0]["messages"])
	}
	// El SEGUNDO turno lleva el tool_result de vuelta al modelo.
	tr, ok := findToolResult(reqs[1]["messages"], "call-1")
	if !ok {
		t.Fatalf("el 2º turno debía traer tool_result con tool_use_id=call-1; messages=%v", reqs[1]["messages"])
	}
	// Concedida y ejecutada con éxito: is_error ausente o false (nunca true).
	if v, present := tr["is_error"]; present && v == true {
		t.Fatalf("una tool concedida y OK no debía marcar is_error=true; tool_result=%v", tr)
	}

	// El efecto en disco: la tool corrió de verdad (Lua decide, Go ejecuta).
	data, err := os.ReadFile(filepath.Join(ws.Workdir, "out.txt"))
	if err != nil || string(data) != "hola e2e" {
		t.Fatalf("out.txt debía crearse con el contenido concedido: err=%v data=%q", err, data)
	}
}

// TestAgentE2EToolLoopDeniedWithoutAutoPermissionsTurnSurvives — [ESCENARIO 2]. El
// contrapunto directo del 1: SIN `--auto-permissions`, la misma `write_file` se deniega
// en headless (no hay UI que pregunte, G20) → exit 3. Pero el turno NO se rompe
// (agente.md §4): el modelo recibe el tool_result denegado (is_error=true, con el texto
// accionable) y responde igualmente, de modo que siguen siendo DOS turnos y el fichero
// nunca toca el disco.
func TestAgentE2EToolLoopDeniedWithoutAutoPermissionsTurnSurvives(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	fp.PushToolUse("call-1", "write_file", map[string]any{"path": "out.txt", "content": "hola e2e"})
	fp.PushText("no pude escribir, sin permiso")

	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	ws.UseFakeProvider(t, fp)

	res := ws.Run(t, RunOpts{Args: []string{"-p", "escribe out.txt"}})
	if res.ExitCode != 3 {
		t.Fatalf("exit: got %d, want 3 (stdout=%q, stderr=%q)", res.ExitCode, res.Stdout, res.Stderr)
	}
	// El stderr debe ser accionable: nombra --auto-permissions o el patrón allow.
	if !strings.Contains(res.Stderr, "--auto-permissions") && !strings.Contains(res.Stderr, "allow") {
		t.Fatalf("stderr debía nombrar --auto-permissions o allow; got %q", res.Stderr)
	}
	// El turno NO se rompe: el 2º POST llega igual (tool_result denegado → respuesta).
	if fp.RequestCount() != 2 {
		t.Fatalf("el turno debía sobrevivir al deny (2 requests); got %d", fp.RequestCount())
	}
	tr, ok := findToolResult(fp.Requests()[1]["messages"], "call-1")
	if !ok {
		t.Fatalf("el 2º turno debía traer el tool_result denegado; messages=%v", fp.Requests()[1]["messages"])
	}
	if tr["is_error"] != true {
		t.Fatalf("el tool_result de un deny debía marcar is_error=true; tool_result=%v", tr)
	}
	if txt := blockText(tr); txt == "" {
		t.Fatalf("el tool_result denegado debía traer texto accionable en su content; tool_result=%v", tr)
	}
	// El deny es real: nada se escribió.
	if _, err := os.Stat(filepath.Join(ws.Workdir, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("out.txt NO debía existir tras un deny; err=%v", err)
	}
}

// TestAgentE2EModelFlagOverridesAgentToml — [ESCENARIO 3]. `--model` anula el `model`
// por defecto de `agent.toml` (agente.md §2/§10: opts.model tiene precedencia). Dos
// subtests sobre el mismo layout de dos modelos: con `--model anthropic/sonnet` el wire
// lleva el id override; sin el flag, el id por defecto del agent.toml.
func TestAgentE2EModelFlagOverridesAgentToml(t *testing.T) {
	t.Run("con_model_anula_agent_toml", func(t *testing.T) {
		ws := NewWorkspace(t)
		fp := NewFakeProvider(t)
		fp.PushText("ok")
		ws.WriteEnuToml(t, "providers", "sessions", "agent")
		writeTwoModelProvider(t, ws, fp)

		res := ws.Run(t, RunOpts{Args: []string{"-p", "hola", "--model", "anthropic/sonnet"}})
		if res.ExitCode != 0 {
			t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
		}
		if fp.RequestCount() < 1 {
			t.Fatalf("el fake no recibió request")
		}
		if got := fp.Requests()[0]["model"]; got != "claude-e2e-override" {
			t.Fatalf("--model debía llevar el id override al wire; got %v, want claude-e2e-override", got)
		}
	})

	t.Run("sin_model_usa_agent_toml", func(t *testing.T) {
		ws := NewWorkspace(t)
		fp := NewFakeProvider(t)
		fp.PushText("ok")
		ws.WriteEnuToml(t, "providers", "sessions", "agent")
		writeTwoModelProvider(t, ws, fp)

		res := ws.Run(t, RunOpts{Args: []string{"-p", "hola"}})
		if res.ExitCode != 0 {
			t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
		}
		if fp.RequestCount() < 1 {
			t.Fatalf("el fake no recibió request")
		}
		if got := fp.Requests()[0]["model"]; got != "claude-e2e-default" {
			t.Fatalf("sin --model debía usar el default de agent.toml; got %v, want claude-e2e-default", got)
		}
	})
}

// TestAgentE2EPermissionsExplicitDenyOverridesAutoPermissions — [ESCENARIO 4, camino
// feo]. Un `deny = ["write_file"]` en `agent.toml` corta en el paso 1 del pipeline de §5
// (deny) ANTES de mirar el mode, así que `--auto-permissions` NO la rescata: la tool se
// deniega igual. Pero como el source es "deny" (política), no "headless" (falta de UI),
// el driver NO mapea al exit 3 (main.go §423): el turno "termina bien" con exit 0. Es la
// discrepancia sutil (flag puesto y aun así denegado, exit 0) que solo un test de caja
// negra sobre el binario detecta.
func TestAgentE2EPermissionsExplicitDenyOverridesAutoPermissions(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	fp.PushToolUse("call-1", "write_file", map[string]any{"path": "out.txt", "content": "hola e2e"})
	fp.PushText("no pude escribir, denegado por política")

	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	ws.UseFakeProvider(t, fp)
	// Sobrescribe el agent.toml de UseFakeProvider (que solo trae model) añadiendo el
	// deny de política. providers.toml queda intacto.
	ws.WriteConfig(t, "agent.toml", "model = \"anthropic/opus\"\n\n[permissions]\ndeny = [\"write_file\"]\n")

	res := ws.Run(t, RunOpts{Args: []string{"-p", "escribe out.txt", "--auto-permissions"}})
	// exit 0, NO 3: un deny de política es una denegación legítima ajena al flag.
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (un deny de política no dispara el 3) (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if fp.RequestCount() != 2 {
		t.Fatalf("el turno debía sobrevivir al deny de política (2 requests); got %d", fp.RequestCount())
	}
	tr, ok := findToolResult(fp.Requests()[1]["messages"], "call-1")
	if !ok {
		t.Fatalf("el 2º turno debía traer el tool_result denegado; messages=%v", fp.Requests()[1]["messages"])
	}
	if tr["is_error"] != true {
		t.Fatalf("el tool_result de un deny de política debía marcar is_error=true; tool_result=%v", tr)
	}
	// Aunque --auto-permissions estaba puesto, el deny gana: nada se escribió.
	if _, err := os.Stat(filepath.Join(ws.Workdir, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("out.txt NO debía existir: el deny de política gana a --auto-permissions; err=%v", err)
	}
}

// TestAgentE2EModelFlagUnknownModelFailsBeforeAnyRequest — [ESCENARIO 5, camino feo]. Un
// `--model` inexistente falla al ABRIR la sesión (`providers.resolve` lanza EPROVIDER,
// agent/init.lua) ANTES de cualquier HTTP: exit 1 (error de arranque/ejecución, no 2 ni
// 3), stderr con EPROVIDER, stdout vacío y CERO requests al fake.
func TestAgentE2EModelFlagUnknownModelFailsBeforeAnyRequest(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	fp.PushText("no debería llegar aquí")

	ws.WriteEnuToml(t, "providers", "sessions", "agent")
	ws.UseFakeProvider(t, fp)

	res := ws.Run(t, RunOpts{Args: []string{"-p", "hola", "--model", "anthropic/no-existe"}})
	if res.ExitCode != 1 {
		t.Fatalf("exit: got %d, want 1 (error de arranque de sesión) (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "EPROVIDER") {
		t.Fatalf("stderr debía nombrar EPROVIDER; got %q", res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Fatalf("un fallo antes del turno no debe escribir a stdout; got %q", res.Stdout)
	}
	// La validación ocurre antes del stream: el fake nunca ve una petición.
	if fp.RequestCount() != 0 {
		t.Fatalf("el modelo inexistente debía fallar antes de tocar el fake; got %d requests", fp.RequestCount())
	}
}
