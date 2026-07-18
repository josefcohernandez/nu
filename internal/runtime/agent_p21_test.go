package runtime

// Tests del control de razonamiento DESDE el agente (cierre de P21 / ADR-016): la
// opción `thinking` de la sesión viaja al request canónico (que el adaptador
// traduce por-modelo, ya cubierto en providers_p21_test.go). Aquí se verifica el
// cableado sesión → request: opts.thinking, agent.toml [thinking], set_thinking.

import (
	"os"
	"path/filepath"
	"testing"
)

// registerThinkRec registra un adaptador "thinkrec" que GRABA el `req.thinking`
// canónico que recibe (en la global REC_THINKING) y responde texto y para. Así el
// test comprueba qué `thinking` ensambló el turno, sin red.
const registerThinkRec = `
local providers = require("providers")
REC_THINKING = "unset"
providers.register_adapter("thinkrec", {
  name = "thinkrec", caps = { tools = false, system = true, usage = true, thinking = true },
  stream = function(req, provider)
    REC_THINKING = req.thinking   -- la tabla canónica { mode, budget? } o nil
    local assembled = { role = "assistant", content = { { type = "text", text = "ok" } } }
    local events = {
      { type = "text", text = "ok" },
      { type = "usage", input_tokens = 3, output_tokens = 1 },
      { type = "done", stop_reason = "end", message = assembled },
    }
    local i = 0
    return function() i = i + 1; return events[i] end
  end,
})
`

const providersTomlThinkRec = `
[providers.test]
adapter  = "thinkrec"
base_url = "http://localhost/unused"

[[providers.test.models]]
id      = "m1"
aliases = ["m"]
`

// TestSessionThinkingWiring (P21/ADR-016): opts.thinking llega al request; "off"
// no envía thinking; set_thinking lo cambia en caliente (mode y budget).
func TestSessionThinkingWiring(t *testing.T) {
	h, _ := bootAgent(t, providersTomlThinkRec, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerThinkRec + `
				-- opts.thinking = { mode = "adaptive" }
				local s = agent.session{ model = "test/m", no_store = true, thinking = { mode = "adaptive" } }
				s:send("hi")
				MODE1 = REC_THINKING and REC_THINKING.mode
				-- set_thinking("off") -> el request no lleva thinking (nil).
				s:set_thinking("off")
				s:send("hi")
				OFF_NIL = (REC_THINKING == nil)
				MODE_VIEW_OFF = s:thinking_mode()
				-- set_thinking budget con cifra.
				s:set_thinking({ mode = "budget", budget = 5000 })
				s:send("hi")
				MODE3 = REC_THINKING and REC_THINKING.mode
				BUDGET3 = REC_THINKING and REC_THINKING.budget
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return MODE1`, "adaptive")
	h.expectEval(`return tostring(OFF_NIL)`, "true")
	h.expectEval(`return MODE_VIEW_OFF`, "off")
	h.expectEval(`return MODE3`, "budget")
	h.expectEval(`return tostring(BUDGET3)`, "5000")
}

// TestSessionThinkingCompatBudget (ADR-016): `{ budget = N }` sin mode = "budget".
func TestSessionThinkingCompatBudget(t *testing.T) {
	h, _ := bootAgent(t, providersTomlThinkRec, false)
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerThinkRec + `
			local s = agent.session{ model = "test/m", no_store = true, thinking = { budget = 7000 } }
			s:send("hi")
			MODE = REC_THINKING and REC_THINKING.mode
			BUDGET = REC_THINKING and REC_THINKING.budget
			s:close()
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return MODE`, "budget")
	h.expectEval(`return tostring(BUDGET)`, "7000")
}

// TestSessionThinkingFromConfig (ADR-016, agente.md §10): el default de
// `agent.toml [thinking]` aplica si la sesión no da opts.thinking.
func TestSessionThinkingFromConfig(t *testing.T) {
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n")
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersTomlThinkRec), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "agent.toml"),
		[]byte("[thinking]\nmode = \"adaptive\"\n"), 0o644); err != nil {
		t.Fatalf("write agent.toml: %v", err)
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerThinkRec + `
			local s = agent.session{ model = "test/m", no_store = true }  -- sin opts.thinking
			s:send("hi")
			MODE = REC_THINKING and REC_THINKING.mode
			s:close()
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return MODE`, "adaptive")
}
