package runtime

// Tests de G40: la denegación de permisos viaja COMO DATO (agente.md §5 "La
// denegación viaja como dato"). Blindan los dos destinos del objeto
// { id, tool, args?, source, pattern?, suggested? }:
//   - el evento `agent:permission.denied` (observadores vivos, atribución G3);
//   - el `meta.denied` del tool_result denegado, que persiste en el historial
//     (y por tanto en el JSONL): la denegación acompaña al transcript.
// Y las dos fuentes principales: "headless" (default deny sin UI, CON el
// `suggested` que alimenta el bucle de escalado de la ronda 8) y "deny" (la
// lista de política, CON el `pattern` que mordió).
//
// Mismo arnés que agent_test.go (bootAgent headless + adaptador "toolstub").

import (
	"testing"
)

// TestPermissionDeniedHeadless (G40): en headless con mode="ask", una tool que
// muta se deniega; el evento lleva source="headless" y el suggested exacto, y el
// tool_result del historial lleva el mismo objeto en meta.denied.
func TestPermissionDeniedHeadless(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				TOOLNAME, TOOLARGS = "touch", { path = "x.txt" }
				` + registerToolStub + `
				agent.tool{
					name = "touch", description = "muta", schema = { type = "object" },
					handler = function(args, ctx) return "hecho" end,
				}
				-- Captura en GLOBAL (convención de estos tests): un upvalue local no
				-- ve escrituras hechas por un handler mientras la task está suspendida
				-- al otro lado del puente pcall (consecuencia de ADR-011; anotado).
				EV = nil
				enu.events.on("agent:permission.denied", function(p) EV = p end)
				local s = agent.session{ model = "test/m1" }   -- mode "ask" por defecto
				s:send("toca el fichero")
				-- El tool_result denegado quedó en el historial como bloque:
				local META = nil
				for _, m in ipairs(s.history) do
					for _, b in ipairs(m.content or {}) do
						if b.type == "tool_result" and b.meta and b.meta.denied then META = b.meta.denied end
					end
				end
				out = {
					ev_source = EV and EV.source or "nil",
					ev_tool = EV and EV.tool or "nil",
					ev_suggested = EV and EV.suggested or "nil",
					ev_session = EV and EV.session or "nil",
					sid = s.id,
					meta_source = META and META.source or "nil",
					meta_suggested = META and META.suggested or "nil",
				}
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
		end)
	`)
	if e := h.eval(`return tostring(errc)`)[0]; e != "nil" {
		t.Fatalf("el turno falló: %s", e)
	}
	h.expectEval(`return out.ev_source`, "headless")
	h.expectEval(`return out.ev_tool`, "touch")
	h.expectEval(`return out.ev_suggested`, "touch:x.txt")             // tool:arg (arg_text)
	h.expectEval(`return tostring(out.ev_session == out.sid)`, "true") // atribución G3
	h.expectEval(`return out.meta_source`, "headless")
	h.expectEval(`return out.meta_suggested`, "touch:x.txt")
}

// TestPermissionDeniedPorLista (G40): un deny de la política lleva
// source="deny" y el `pattern` exacto que mordió.
func TestPermissionDeniedPorLista(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				TOOLNAME, TOOLARGS = "touch", { path = "x.txt" }
				` + registerToolStub + `
				agent.tool{
					name = "touch", description = "muta", schema = { type = "object" },
					handler = function(args, ctx) return "hecho" end,
				}
				EV = nil  -- global: ver la nota del test anterior (ADR-011)
				enu.events.on("agent:permission.denied", function(p) EV = p end)
				local s = agent.session{
					model = "test/m1",
					permissions = { deny = { "touch" } },
				}
				s:send("toca el fichero")
				out = {
					source = EV and EV.source or "nil",
					pattern = EV and EV.pattern or "nil",
					id = EV and EV.id or "nil",
				}
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
		end)
	`)
	if e := h.eval(`return tostring(errc)`)[0]; e != "nil" {
		t.Fatalf("el turno falló: %s", e)
	}
	h.expectEval(`return out.source`, "deny")
	h.expectEval(`return out.pattern`, "touch")
	h.expectEval(`return out.id`, "call-1") // el id de la tool call denegada
}
