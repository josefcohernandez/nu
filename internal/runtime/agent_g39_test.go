package runtime

// Tests de G39: `Session:fork(at?, opts?)` re-aloja (agente.md §2 "Fork y
// cierre"). Blindan:
//   - los `opts` sobreescriben lo heredado (cwd → el worktree de la variante:
//     el caso del fork-como-replicación de la ronda 8);
//   - la herencia COMPLETA del estado VIGENTE del padre (thinking tras
//     set_thinking, max_tokens/temperature, permisos tras Session:allow) — la
//     lista parcial anterior perdía skills y thinking en silencio;
//   - los permisos solo RECORTAN: el deny vigente del padre acompaña SIEMPRE a
//     la hija, aunque opts.permissions lo omita;
//   - `at` indexa el historial de mensajes vigente (el prefijo copiado).
//
// Mismo arnés que agent_p22_test.go (bootAgent + adaptador "ctl").

import (
	"testing"
)

// TestForkOptsReAloja (G39): fork(nil, {cwd=...}) hereda todo pero corre en otro
// cwd; el prefijo se copia (hija autocontenida) y meta.parent enlaza al padre.
func TestForkOptsReAloja(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerCtl + `
				local s = agent.session{ model = "test/m1" }
				s:send("hola")
				local v = s:fork(nil, { cwd = "/otro/worktree" })
				out = {
					parent_cwd = s.cwd,
					child_cwd  = v.cwd,
					child_hist = #v.history,
					child_model = v.model,
				}
				v:close(); s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
		end)
	`)
	if e := h.eval(`return tostring(errc)`)[0]; e != "nil" {
		t.Fatalf("el fork con opts falló: %s", e)
	}
	h.expectEval(`return out.child_cwd`, "/otro/worktree")
	h.expectEval(`return tostring(out.parent_cwd ~= out.child_cwd)`, "true")
	h.expectEval(`return tostring(out.child_hist)`, "2") // user + assistant copiados
	h.expectEval(`return out.child_model`, "test/m1")
}

// TestForkHerenciaCompleta (G39): el estado VIGENTE viaja — thinking cambiado en
// caliente, max_tokens/temperature, skills y el allow concedido con Session:allow.
func TestForkHerenciaCompleta(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerCtl + `
				local s = agent.session{
					model = "test/m1",
					max_tokens = 512, temperature = 0.3,
					skills = { "review" },
				}
				s:set_thinking("adaptive")       -- estado vigente, no el opts original
				s:allow("bash:git *")            -- concesión en caliente
				local v = s:fork()
				local pv = v:permissions_view()
				local has = false
				for _, a in ipairs(pv.allow) do if a == "bash:git *" then has = true end end
				out = {
					thinking = v.thinking and v.thinking.mode or "nil",
					max_tokens = v.max_tokens, temperature = v.temperature,
					skill = v.opts.skills and v.opts.skills[1] or "nil",
					allow_heredado = has,
				}
				v:close(); s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
		end)
	`)
	if e := h.eval(`return tostring(errc)`)[0]; e != "nil" {
		t.Fatalf("la herencia del fork falló: %s", e)
	}
	h.expectEval(`return out.thinking`, "adaptive")
	h.expectEval(`return tostring(out.max_tokens)`, "512")
	h.expectEval(`return tostring(out.temperature)`, "0.3")
	h.expectEval(`return out.skill`, "review")
	h.expectEval(`return tostring(out.allow_heredado)`, "true")
}

// TestForkPermisosSoloRecortan (G39): opts.permissions sustituye mode/allow pero
// el deny VIGENTE del padre acompaña siempre a la hija (nunca ampliados, §9/§11).
func TestForkPermisosSoloRecortan(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerCtl + `
				local s = agent.session{
					model = "test/m1",
					permissions = { deny = { "bash:rm *" } },
				}
				local v = s:fork(nil, { permissions = { mode = "auto", allow = { "edit" } } })
				local pv = v:permissions_view()
				local has_deny = false
				for _, d in ipairs(pv.deny) do if d == "bash:rm *" then has_deny = true end end
				out = { mode = pv.mode, deny_conservado = has_deny }
				v:close(); s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
		end)
	`)
	if e := h.eval(`return tostring(errc)`)[0]; e != "nil" {
		t.Fatalf("el recorte del fork falló: %s", e)
	}
	h.expectEval(`return out.mode`, "auto")
	h.expectEval(`return tostring(out.deny_conservado)`, "true")
}

// TestForkAtIndexaMensajes (G39): `at` cuenta mensajes del historial vigente —
// fork(1) copia solo el primer mensaje (el user), no el par completo.
func TestForkAtIndexaMensajes(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerCtl + `
				local s = agent.session{ model = "test/m1" }
				s:send("hola")                       -- historial: user, assistant
				local v = s:fork(1)
				out = { hist = #v.history, role = v.history[1] and v.history[1].role or "nil" }
				v:close(); s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
		end)
	`)
	if e := h.eval(`return tostring(errc)`)[0]; e != "nil" {
		t.Fatalf("fork(at) falló: %s", e)
	}
	h.expectEval(`return tostring(out.hist)`, "1")
	h.expectEval(`return out.role`, "user")
}
