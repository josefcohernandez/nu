package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests de G46 — el replay de `resume` reaplica las entradas `event` del agente:
// la sesión reanudada continúa DONDE ESTABA, no donde arrancó. Antes del arreglo
// el replay solo reconstruía `message` y `compact`: un `set_model`/`set_thinking`
// en caliente se perdía al reanudar (la sesión volvía al modelo de
// opts/agent.toml sin aviso) y los `allow`/`deny` — una palanca de SEGURIDAD —
// había que reconcederlos. La lógica a blindar (agente.md §2 / sesiones.md §3):
//
//   - **precedencia**: opts explícitos del resume > event del transcript >
//     agent.toml. Los opts siguen siendo efímeros *cuando se dan* (G18); cuando
//     callan, el transcript manda.
//   - **last-wins para los repetibles**: varios `set_model`/`set_thinking`
//     grabados → al reanudar rige el último (sesiones.md §3).
//   - **acumulativos en orden**: los `allow`/`deny` se reaplican sobre la
//     política base con la semántica de caliente (idempotentes, sin duplicar) y
//     SIEMPRE (no los pisa un opts.permissions: solo acumulan encima).

// bootAgentG46 arranca providers+sessions+agent con un providers.toml de DOS
// modelos (para que un set_model tenga adónde ir) y un agent.toml con modelo por
// defecto (para poder abrir la sesión SIN opts.model y ver quién gana al reanudar).
func bootAgentG46(t *testing.T) *harness {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n")
	providersToml := `
[providers.test]
adapter  = "toolstub"
base_url = "http://localhost/unused"

[[providers.test.models]]
id      = "m1"
aliases = ["m"]

[[providers.test.models]]
id      = "m2"
`
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersToml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "agent.toml"), []byte("model = \"test/m\"\n"), 0o644); err != nil {
		t.Fatalf("write agent.toml: %v", err)
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}
}

// TestAgentG46ResumeReaplicaEventos: una sesión cambia modelo, razonamiento y
// permisos en caliente; al reanudarla SIN opts, todo eso rige de nuevo (el
// transcript gana a agent.toml); al reanudarla CON opts explícitos, los opts
// ganan al transcript — y los allow/deny se reaplican en ambos casos.
func TestAgentG46ResumeReaplicaEventos(t *testing.T) {
	h := bootAgentG46(t)
	repo := t.TempDir()

	// Sesión 1: nace con el modelo de agent.toml y cambia TODO en caliente.
	// Cambios repetidos a propósito: el replay debe quedarse con el último.
	h.eval(`
		out1, errc1, SID, MODEL1 = nil, nil, nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				local s = agent.session{ cwd = "` + repo + `" }
				SID = s.id
				MODEL1 = s.model                     -- "test/m", de agent.toml
				s:set_model("test/m2")
				s:set_model("test/m")
				s:set_model("test/m2")               -- la última gana
				s:set_thinking({ mode = "budget", budget = 2048 })
				s:set_thinking("adaptive")           -- la última gana
				s:allow("write_file")
				s:allow("write_file")                -- idempotente en caliente
				s:deny("run_shell")
				s:close()
			end)
			if not ok then errc1 = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out1 = "done"
		end)`)
	h.expectEval(`return tostring(out1)`, "done")
	h.expectEval(`return tostring(errc1)`, "nil")
	h.expectEval(`return tostring(MODEL1)`, "test/m")

	// Reanudación con los opts CALLADOS: el transcript manda sobre agent.toml.
	h.eval(`
		out2, errc2, MODEL2, THINK2, ALLOW2, DENY2 = nil, nil, nil, nil, nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local s = agent.session{ cwd = "` + repo + `", resume = SID }
				MODEL2 = s.model
				THINK2 = s:thinking_mode()
				local pv = s:permissions_view()
				ALLOW2 = table.concat(pv.allow, "|")
				DENY2 = table.concat(pv.deny, "|")
				s:close()
			end)
			if not ok then errc2 = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out2 = "done"
		end)`)
	h.expectEval(`return tostring(out2)`, "done")
	h.expectEval(`return tostring(errc2)`, "nil")
	h.expectEval(`return tostring(MODEL2)`, "test/m2")    // último set_model, no agent.toml
	h.expectEval(`return tostring(THINK2)`, "adaptive")   // último set_thinking
	h.expectEval(`return tostring(ALLOW2)`, "write_file") // reaplicado, sin duplicar
	h.expectEval(`return tostring(DENY2)`, "run_shell")

	// Reanudación con opts EXPLÍCITOS: los opts ganan al transcript (siguen
	// siendo efímeros cuando se dan, G18) — pero los allow/deny acumulativos se
	// reaplican igual (no son "dato repetible" que un opts pise).
	h.eval(`
		out3, errc3, MODEL3, THINK3, ALLOW3, DENY3 = nil, nil, nil, nil, nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local s = agent.session{ cwd = "` + repo + `", resume = SID,
				                         model = "test/m", thinking = "off" }
				MODEL3 = s.model
				THINK3 = s:thinking_mode()
				local pv = s:permissions_view()
				ALLOW3 = table.concat(pv.allow, "|")
				DENY3 = table.concat(pv.deny, "|")
				s:close()
			end)
			if not ok then errc3 = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out3 = "done"
		end)`)
	h.expectEval(`return tostring(out3)`, "done")
	h.expectEval(`return tostring(errc3)`, "nil")
	h.expectEval(`return tostring(MODEL3)`, "test/m") // el opts explícito gana
	h.expectEval(`return tostring(THINK3)`, "off")    // ídem
	h.expectEval(`return tostring(ALLOW3)`, "write_file")
	h.expectEval(`return tostring(DENY3)`, "run_shell")
}
