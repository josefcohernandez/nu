package runtime

// Tests de la extensión oficial `agent` (S39, embebida en
// internal/runtime/embedded/agent). Es el MOTOR HEADLESS: Lua sobre la API
// pública congelada (Fase 8, ADR-003: el core NO sabe lo que es un agente),
// construido sobre las extensiones `providers` (S36/S37) y `sessions` (S38).
// La prueba arranca un Runtime con las TRES activadas por `nu.toml` y ejercita el
// contrato de [agente.md](../../docs/agente.md) desde Lua con `require("agent")`.
//
// Blinda:
//   - **el TURNO** (§2): un turno completo con una tool de prueba —el adaptador
//     pide la tool, el handler se ejecuta, su resultado se realimenta, y el
//     segundo `done` (sin tools) cierra el turno;
//   - **permisos** (§5): una tool que muta sin `allow` en headless produce un
//     error ACCIONABLE devuelto al modelo como tool_result is_error (el turno no
//     se rompe);
//   - **hooks-middleware** (§4): `tool.pre`/`tool.post` se invocan y pueden
//     reescribir/vetar;
//   - **eventos `agent:*`** (§4): turn.start/message/tool.start/tool.end/turn.end
//     se emiten con atribución `session` (G3);
//   - **CP-10**: turno headless (sin UI, G20) con una tool de FICHERO real
//     (nu.fs) y un permiso DENEGADO accionable, PERSISTIDO en JSONL (sessions) y
//     REANUDABLE.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bootAgent arranca un Runtime con providers+sessions+agent activadas y un
// data_dir/config_dir CONOCIDOS (para inspeccionar el JSONL persistido). El
// `providers.toml` declara un provider con el adaptador indicado por nombre (el
// test registra el adaptador desde Lua antes de resolver). headless: por defecto
// SIN UI (WithForceUI(false)) — es el caso G20 que el agente debe soportar; los
// tests que necesiten UI lo fuerzan.
func bootAgent(t *testing.T, providersToml string, forceUI bool) (*harness, string) {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n")
	if providersToml != "" {
		if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersToml), 0o644); err != nil {
			t.Fatalf("write providers.toml: %v", err)
		}
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(forceUI))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}, dataDir
}

// providersTomlToolStub: un provider cuyo adaptador "toolstub" lo registra el
// propio test desde Lua (un adaptador de prueba que SÍ soporta tools y devuelve
// una tool call en el primer turno; el stub oficial declara tools=false).
const providersTomlToolStub = `
[providers.test]
adapter  = "toolstub"
base_url = "http://localhost/unused"

[[providers.test.models]]
id      = "m1"
aliases = ["m"]
`

// registerToolStub es el cuerpo Lua que registra un adaptador "toolstub" en la
// extensión providers. Comportamiento: en la PRIMERA llamada (sin tool_result en
// el historial) emite una tool call a la tool indicada por la global
// TOOLNAME/TOOLARGS; en las siguientes (ya hay un tool_result) responde texto y
// para. Así un `Session:send` ejerce el loop completo: pide → tool → re-pide → fin.
const registerToolStub = `
local providers = require("providers")
providers.register_adapter("toolstub", {
  name = "toolstub",
  caps = { tools = true, system = true, usage = true },
  stream = function(req, provider)
    -- ¿El ÚLTIMO mensaje trae un tool_result? Si sí, acabamos de ejecutar una
    -- tool: es la 2ª vuelta del turno y respondemos texto. (Mirar solo el último
    -- mensaje, no todo el historial, hace al stub correcto al REANUDAR: una
    -- sesión reanudada ya contiene tool_results de turnos previos.)
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

// TestAgentCargaYActiva: la extensión carga (source="builtin") y su módulo expone
// la superficie del contrato (§2/§3/§4/§5).
func TestAgentCargaYActiva(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false)
	if src := listSource(h, "agent"); src != "builtin" {
		t.Fatalf(`agent debía cargarse con source="builtin"; got %q`, src)
	}
	h.expectEval(`
		local a = require("agent")
		assert(type(a.session) == "function", "session")
		assert(type(a.tool) == "function", "tool")
		assert(type(a.hook) == "function", "hook")
		assert(type(a.permission.respond) == "function", "permission.respond")
		assert(type(a.caps) == "table", "caps")
		return "ok"`, "ok")
}

// TestAgentTurnoCompleto (§2, criterio de hecho de S39): un turno con una tool de
// prueba. El adaptador pide la tool en la 1ª vuelta; el handler corre; su
// resultado se realimenta; la 2ª vuelta (sin tools) cierra el turno con "listo".
// La tool de prueba se concede con `allow` para aislar el camino feliz del de
// permisos (que prueba TestAgentPermisoDenegado).
func TestAgentTurnoCompleto(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false)

	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				TOOLNAME = "probe"
				TOOLARGS = { value = "x" }
				-- Tool de prueba que registra que la llamaron.
				CALLED = false
				agent.tool{
					name = "probe",
					description = "tool de prueba",
					schema = { type = "object" },
					permissions = { default = "allow" },
					handler = function(args, ctx)
						CALLED = true
						GOT_VALUE = args.value
						return "resultado-de-probe"
					end,
				}
				local s = agent.session{ model = "test/m", no_store = true }
				local final = s:send("haz la cosa")
				FINAL_TEXT = final.content[1].text
				HIST = #s.history
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(CALLED)`, "true")
	h.expectEval(`return tostring(GOT_VALUE)`, "x")
	h.expectEval(`return tostring(FINAL_TEXT)`, "listo")
	// historial: user, assistant(tool_call), user(tool_result), assistant(listo) = 4
	h.expectEval(`return tostring(HIST)`, "4")
}

// providersTomlErrStub: un provider cuyo adaptador "errstub" LANZA en `stream`
// (simula un 401 por API key ausente/ inválida o un provider caído).
const providersTomlErrStub = `
[providers.test]
adapter  = "errstub"
base_url = "http://localhost/unused"

[[providers.test.models]]
id      = "m1"
aliases = ["m"]
`

// registerErrStub registra un adaptador que falla al primer contacto con el
// provider (como haría el anthropic/openai-compat ante un 401).
const registerErrStub = `
local providers = require("providers")
providers.register_adapter("errstub", {
  name = "errstub",
  caps = { tools = true, system = true, usage = true },
  stream = function(req, provider)
    error({ code = "EPROVIDER", message = "provider boom (HTTP 401: API key inválida)" })
  end,
})
`

// TestAgentTurnoErrorSeEmite: si el adaptador LANZA durante el turno (p. ej. 401 por
// API key ausente o inválida), el agente lo EMITE como `agent:error` —no muere en
// silencio— y `send` retorna (sin colgar). Blinda el fix del bug "si envías un
// mensaje sin API key no avisa del error": antes la task del turno moría y el future
// resolvía como cancelado, así que `send` devolvía nil y la UI no pintaba nada.
func TestAgentTurnoErrorSeEmite(t *testing.T) {
	h, _ := bootAgent(t, providersTomlErrStub, false)
	h.eval(`
		ERRMSG, SENT, RET = nil, false, "sentinel"
		nu.task.spawn(function()
			local agent = require("agent")
			` + registerErrStub + `
			local sub = nu.events.on("agent:error", function(p) ERRMSG = p.message end)
			local s = agent.session{ model = "test/m", no_store = true }
			RET = s:send("hola")   -- el turno falla; send DEBE retornar (no colgar)
			SENT = true
			sub:cancel(); s:close()
		end)`)
	// send retornó (no se quedó colgado esperando el future).
	h.expectEval(`return tostring(SENT)`, "true")
	// se emitió agent:error con el mensaje del adaptador.
	h.expectEval(`return tostring(ERRMSG ~= nil)`, "true")
	h.expectEval(`return tostring(ERRMSG:find("provider boom", 1, true) ~= nil)`, "true")
	// el turno falló: send devuelve nil (no un mensaje).
	h.expectEval(`return tostring(RET == nil)`, "true")
}

// TestAgentPermisoDenegado (§5): una tool que muta, SIN allow, en headless (sin
// UI) produce un error ACCIONABLE. El turno NO se rompe: el error va al modelo
// como tool_result is_error, y el loop continúa hasta el done final. El texto del
// error nombra el patrón a añadir (amortiguador 2).
func TestAgentPermisoDenegado(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false) // headless: sin UI (G20)

	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				TOOLNAME = "danger"
				TOOLARGS = { path = "/tmp/x" }
				-- Tool que muta: default "ask" → DENY en headless sin allow.
				agent.tool{
					name = "danger",
					description = "tool peligrosa",
					schema = { type = "object" },
					handler = function(args, ctx) return "no debería ejecutarse" end,
				}
				-- Capturamos el tool_result is_error del historial.
				local s = agent.session{ model = "test/m", no_store = true }
				s:send("usa danger")
				-- El historial: user, assistant(tool_call), user(tool_result is_error), assistant(listo)
				local tool_msg = s.history[3]
				local res = tool_msg.content[1]
				IS_ERROR = res.is_error
				ERR_TEXT = res.content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(IS_ERROR)`, "true")
	// El error es accionable: nombra "headless", la tool y el patrón allow.
	errText := h.eval(`return tostring(ERR_TEXT)`)[0]
	if !strings.Contains(errText, "headless") || !strings.Contains(errText, "danger") || !strings.Contains(errText, "allow") {
		t.Fatalf("el error de permiso no es accionable: %q", errText)
	}
}

// TestAgentPermisoConcedido (§5): la MISMA tool que muta, con `allow` explícito en
// opts.permissions, SÍ se ejecuta (no hay error). Confirma que el pipeline
// deny→allow→hooks→ask concede por allow.
func TestAgentPermisoConcedido(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false)

	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				TOOLNAME = "danger2"
				TOOLARGS = {}
				RAN = false
				agent.tool{
					name = "danger2", description = "muta", schema = { type = "object" },
					handler = function(args, ctx) RAN = true; return "hecho" end,
				}
				local s = agent.session{ model = "test/m", no_store = true,
					permissions = { allow = { "danger2" } } }
				s:send("usa danger2")
				local res = s.history[3].content[1]
				IS_ERROR = res.is_error == true
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(RAN)`, "true")
	h.expectEval(`return tostring(IS_ERROR)`, "false")
}

// TestAgentHooks (§4): los hooks-middleware tool.pre y tool.post se invocan y
// pueden reescribir args y resultado; un tool.pre que devuelve { deny } veta la
// tool (tool_result is_error). Registro propio de la extensión, NO el bus.
func TestAgentHooks(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false)

	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				agent._reset_hooks()
				` + registerToolStub + `
				TOOLNAME = "echo"
				TOOLARGS = { msg = "original" }
				agent.tool{
					name = "echo", description = "eco", schema = { type = "object" },
					permissions = { default = "allow" },
					handler = function(args, ctx) return "handler:" .. tostring(args.msg) end,
				}
				PRE_SEEN, POST_SEEN = false, false
				agent.hook("tool.pre", function(payload, ctx)
					PRE_SEEN = true
					-- reescribe los args: msg pasa a "reescrito"
					payload.args = { msg = "reescrito" }
					return payload
				end)
				agent.hook("tool.post", function(payload, ctx)
					POST_SEEN = true
					return { result = "post:" .. tostring(payload.result) }
				end)
				local s = agent.session{ model = "test/m", no_store = true }
				s:send("usa echo")
				local res = s.history[3].content[1]
				RESULT_TEXT = res.content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(PRE_SEEN)`, "true")
	h.expectEval(`return tostring(POST_SEEN)`, "true")
	// tool.pre reescribió args.msg a "reescrito"; el handler vio eso; tool.post lo envolvió.
	h.expectEval(`return tostring(RESULT_TEXT)`, "post:handler:reescrito")
}

// TestAgentHookVeto (§4): un hook tool.pre que devuelve { deny } veta la tool: el
// handler NO corre y el tool_result es is_error con la razón.
func TestAgentHookVeto(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false)

	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				agent._reset_hooks()
				` + registerToolStub + `
				TOOLNAME = "vetable"
				TOOLARGS = {}
				RAN = false
				agent.tool{
					name = "vetable", description = "x", schema = { type = "object" },
					permissions = { default = "allow" },
					handler = function(args, ctx) RAN = true; return "no" end,
				}
				agent.hook("tool.pre", function(payload, ctx)
					return { deny = "razón de veto" }
				end)
				local s = agent.session{ model = "test/m", no_store = true }
				s:send("usa vetable")
				local res = s.history[3].content[1]
				IS_ERROR = res.is_error == true
				ERR_TEXT = res.content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(RAN)`, "false")
	h.expectEval(`return tostring(IS_ERROR)`, "true")
	errText := h.eval(`return tostring(ERR_TEXT)`)[0]
	if !strings.Contains(errText, "razón de veto") || !strings.Contains(errText, "tool.pre") {
		t.Fatalf("el veto no se reporta accionable: %q", errText)
	}
}

// TestAgentEventos (§4): los eventos `agent:*` se emiten por el bus `nu.events`
// con atribución obligatoria `session` (G3). Suscribimos a varios y comprobamos
// que se disparan en un turno y llevan el id de sesión.
func TestAgentEventos(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false)

	h.eval(`
		out, errc = nil, nil
		EV = {}
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				agent._reset_hooks()
				` + registerToolStub + `
				TOOLNAME = "noop"
				TOOLARGS = {}
				agent.tool{ name = "noop", description = "x", schema = { type = "object" },
					permissions = { default = "allow" },
					handler = function(args, ctx) return "ok" end }
				local names = { "turn.start", "turn.end", "message", "tool.start", "tool.end" }
				ALL_HAVE_SESSION = true
				for _, n in ipairs(names) do
					nu.events.on("agent:" .. n, function(p)
						EV[n] = (EV[n] or 0) + 1
						if p.session == nil then ALL_HAVE_SESSION = false end
					end)
				end
				local s = agent.session{ model = "test/m", no_store = true }
				SID = s.id
				EV_SESSION_MATCHES = true
				nu.events.on("agent:turn.end", function(p)
					if p.session ~= SID then EV_SESSION_MATCHES = false end
				end)
				s:send("dispara eventos")
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(EV["turn.start"])`, "1")
	h.expectEval(`return tostring(EV["turn.end"])`, "1")
	h.expectEval(`return tostring(EV["tool.start"])`, "1")
	h.expectEval(`return tostring(EV["tool.end"])`, "1")
	// message se emite una vez por iteración del loop (tool_calls + done) = 2
	h.expectEval(`return tostring(EV["message"])`, "2")
	h.expectEval(`return tostring(ALL_HAVE_SESSION)`, "true")
	h.expectEval(`return tostring(EV_SESSION_MATCHES)`, "true")
}

// TestCP10AgenteHeadless es el CHECKPOINT CP-10 (tras S39): un turno HEADLESS (sin
// UI, G20) que (a) ejecuta una tool de FICHERO real (read_file/write_file con
// nu.fs), (b) DENIEGA el permiso de escritura (error accionable, sin que el turno
// se rompa), (c) PERSISTE la sesión en JSONL (sessions S38) y (d) es REANUDABLE.
// Verifica que TODO corre sin una sola línea de UI.
func TestCP10AgenteHeadless(t *testing.T) {
	// headless de verdad: WithForceUI(false). nu.has("ui") será false.
	h, dataDir := bootCP10(t)

	// Fichero a leer por la tool read_file (camino de solo lectura, se permite).
	repo := t.TempDir()
	target := filepath.Join(repo, "saludo.txt")
	if err := os.WriteFile(target, []byte("hola desde disco"), 0o600); err != nil {
		t.Fatalf("preparar fichero: %v", err)
	}

	// Turno 1: el adaptador pide read_file(target). El handler lee el fichero
	// (nu.fs), se concede (solo lectura, default allow), su contenido se realimenta,
	// y el done final cierra. La sesión se PERSISTE (no no_store).
	h.eval(`
		out, errc = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				HAS_UI = nu.has("ui")
				TOOLNAME = "read_file"
				TOOLARGS = { path = "` + target + `" }
				local s = agent.session{ model = "test/m", cwd = "` + repo + `" }
				SID = s.id
				local final = s:send("lee el saludo")
				FINAL = final.content[1].text
				-- El tool_result del read_file debe traer el contenido del fichero.
				READ_BACK = s.history[3].content[1].content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(HAS_UI)`, "false") // CP-10: HEADLESS, sin UI
	h.expectEval(`return tostring(FINAL)`, "listo")
	h.expectEval(`return tostring(READ_BACK)`, "hola desde disco")
	sid := h.eval(`return tostring(SID)`)[0]

	// El JSONL se persistió: existe el fichero de sesión bajo data_dir/sessions/.
	// Verificamos que contiene los mensajes del turno (append-only, sesiones.md).
	sessFile := findSessionFile(t, dataDir, sid)
	data, err := os.ReadFile(sessFile)
	if err != nil {
		t.Fatalf("leer sesión persistida: %v", err)
	}
	content := string(data)
	for _, want := range []string{`"t":"meta"`, `"t":"message"`, `read_file`, `hola desde disco`, `listo`} {
		if !strings.Contains(content, want) {
			t.Fatalf("el JSONL persistido no contiene %q\n--- jsonl ---\n%s", want, content)
		}
	}

	// Turno 2 (permiso DENEGADO headless): reanudar la sesión y pedir write_file.
	// La tool muta y no hay allow → DENY accionable (el turno no se rompe). El
	// reanudar repobló el historial (replay) — comprobamos que la sesión continúa.
	h.eval(`
		out2, errc2 = nil, nil
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				TOOLNAME = "write_file"
				TOOLARGS = { path = "` + filepath.Join(repo, "no.txt") + `", content = "intento" }
				local s = agent.session{ model = "test/m", cwd = "` + repo + `", resume = "` + sid + `" }
				RESUMED_HIST = #s.history -- el replay repobló mensajes del turno 1
				s:send("escribe un fichero")
				-- Busca el tool_result is_error en el historial (el de la tool denegada).
				DENIED = false
				DENY_TEXT = ""
				for _, msg in ipairs(s.history) do
					for _, block in ipairs(msg.content or {}) do
						if block.type == "tool_result" and block.is_error == true then
							DENIED = true
							DENY_TEXT = block.content[1].text
						end
					end
				end
				s:close()
			end)
			if not ok then errc2 = (type(e) == "table" and e.code) or tostring(e) end
			out2 = "done"
		end)`)
	h.expectEval(`return tostring(out2)`, "done")
	h.expectEval(`return tostring(errc2)`, "nil")
	// El replay repobló el historial del turno 1 (al menos los 4 mensajes).
	resumed := h.eval(`return tostring(RESUMED_HIST)`)[0]
	if resumed == "0" || resumed == "nil" {
		t.Fatalf("la reanudación no repobló el historial; got %q", resumed)
	}
	h.expectEval(`return tostring(DENIED)`, "true")
	denyText := h.eval(`return tostring(DENY_TEXT)`)[0]
	if !strings.Contains(denyText, "headless") || !strings.Contains(denyText, "write_file") || !strings.Contains(denyText, "allow") {
		t.Fatalf("CP-10: el permiso denegado no es accionable: %q", denyText)
	}

	// El fichero NO debe existir (la escritura fue denegada).
	if _, err := os.Stat(filepath.Join(repo, "no.txt")); !os.IsNotExist(err) {
		t.Fatalf("CP-10: el fichero se creó pese a denegarse el permiso")
	}
}

// bootCP10 arranca el runtime headless (sin UI) con las tres extensiones y el
// providers.toml del toolstub, devolviendo el data_dir para inspeccionar el JSONL.
func bootCP10(t *testing.T) (*harness, string) {
	t.Helper()
	return bootAgent(t, providersTomlToolStub, false)
}

// findSessionFile localiza el .jsonl de la sesión `sid` bajo data_dir/sessions/.
func findSessionFile(t *testing.T, dataDir, sid string) string {
	t.Helper()
	var found string
	root := filepath.Join(dataDir, "sessions")
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, sid+".jsonl") {
			found = path
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no se encontró el .jsonl de la sesión %q bajo %s", sid, root)
	}
	return found
}
