package runtime

// Tests de dos derivas encontradas en la auditoría de extensiones (2026-07-02):
//   - `Session.usage.cost_usd` estaba documentado (agente.md §2) y el dato
//     `cost` del providers.toml existía, pero nunca se multiplicaban: el chat
//     mostraba siempre $0. Ahora se acumula por iteración con el usage del
//     PROVEEDOR (USD por Mtok, providers.md §1).
//   - `providers.list()` omitía `thinking` en el ModelInfo mientras `resolve`
//     sí lo entregaba (providers.md §3: las dos vías dan la misma forma).

import (
	"testing"
)

// providersTomlCost: el adaptador "ctl" de agent_p22_test.go + un modelo CON
// tarifa declarada (10 USD/Mtok input, 20 USD/Mtok output) y dialecto thinking.
const providersTomlCost = `
[providers.test]
adapter  = "ctl"
base_url = "http://localhost/unused"

[[providers.test.models]]
id       = "m1"
context  = 100000
cost     = { input = 10.0, output = 20.0 }
thinking = "adaptive"
aliases  = ["m"]
`

// TestUsageCostAcumulado: el ctl emite usage {input=5, output=2} por turno →
// (5*10 + 2*20)/1e6 = 0.00009 USD. Dos sends: el doble. La fuente es el usage
// del proveedor, y sin `cost` en el TOML no se acumula nada.
func TestUsageCostAcumulado(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCost, false)
	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerCtl + `
				local s = agent.session{ model = "test/m1" }
				s:send("uno")
				local tras_uno = s.usage.cost_usd
				s:send("dos")
				out = { uno = tras_uno, dos = s.usage.cost_usd }
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
		end)
	`)
	if e := h.eval(`return tostring(errc)`)[0]; e != "nil" {
		t.Fatalf("el turno falló: %s", e)
	}
	// 5 input * 10 + 2 output * 20 = 90 → 90/1e6 = 9e-05
	h.expectEval(`return tostring(out.uno)`, "9e-05")
	h.expectEval(`return tostring(out.dos)`, "0.00018")
}

// TestProvidersListLlevaThinking: list() entrega el dialecto de razonamiento en
// el ModelInfo, igual que resolve (providers.md §3, ADR-016).
func TestProvidersListLlevaThinking(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCost, false)
	h.eval(inTask(`
		local providers = require("providers")
		local models = providers.list()
		out = "no-encontrado"
		for _, m in ipairs(models) do
			if m.ref == "test/m1" then out = tostring(m.thinking) end
		end`))
	h.expectEval(`return tostring(out)`, "adaptive")
}
