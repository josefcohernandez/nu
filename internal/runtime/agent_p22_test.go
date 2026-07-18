package runtime

// Tests de los métodos de control de sesión de la extensión `agent`:
//   - P22: Session:cancel / fork / compact / clear_queue,
//   - P23: cola de reentrada de Session:send (G4),
//   - P25: compactación automática por umbral + evento agent:compact.
//
// Mismo arnés que agent_test.go (bootAgent: providers+sessions+agent activadas).
// Un adaptador de prueba "ctl" —registrado desde Lua— da control sobre el stream
// (texto, usage configurable, y una pausa opcional para abrir la ventana de
// "turno en vuelo" que cancel/reentrada necesitan).

import (
	"testing"
)

// providersTomlCtl: provider con adaptador "ctl" y una ventana de contexto
// pequeña (100) para que el umbral de autocompactación (80%) sea fácil de cruzar.
const providersTomlCtl = `
[providers.test]
adapter  = "ctl"
base_url = "http://localhost/unused"

[[providers.test.models]]
id      = "m1"
context = 100
aliases = ["m"]
`

// registerCtl registra el adaptador "ctl". Cada llamada incrementa CALLS; emite
// un texto "r<CALLS>" y un `usage` con USAGE_IN (global, default 5) input_tokens,
// y para (stop_reason="end"). Si SLEEP_MS>0, SUSPENDE antes de emitir (abre la
// ventana de turno en vuelo para cancel/reentrada).
const registerCtl = `
local providers = require("providers")
CALLS = 0
providers.register_adapter("ctl", {
  name = "ctl", caps = { tools = true, system = true, usage = true },
  stream = function(req, provider)
    CALLS = CALLS + 1
    local mycall = CALLS
    if SLEEP_MS and SLEEP_MS > 0 then enu.task.sleep(SLEEP_MS) end
    local text = "r" .. mycall
    local assembled = { role = "assistant", content = { { type = "text", text = text } } }
    local events = {
      { type = "text", text = text },
      { type = "usage", input_tokens = (USAGE_IN or 5), output_tokens = 2 },
      { type = "done", stop_reason = "end", message = assembled },
    }
    local i = 0
    return function() i = i + 1; return events[i] end
  end,
})
`

// TestSessionCompactManual (P22/P25): Session:compact con un hook `compact` que
// da un resumen FIJO (evita depender del LLM). Tras compactar: el historial se
// reduce a [resumen], se escribió una entrada `compact` (vía store) y se emitió
// `agent:compact`.
func TestSessionCompactManual(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerCtl + `
				COMPACTED = false
				enu.events.on("agent:compact", function(p) COMPACTED = true end)
				agent.hook("compact", function(payload, ctx)
					return { summary = { role = "user", content = { { type = "text", text = "RESUMEN" } } } }
				end)
				local s = agent.session{ model = "test/m", no_store = true }
				s:send("hola")            -- un turno: historial = user + assistant
				HIST_ANTES = #s.history
				local ok2 = s:compact()
				OKC = ok2
				HIST_DESPUES = #s.history
				FIRST_TEXT = s.history[1].content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(OKC)`, "true")
	h.expectEval(`return tostring(HIST_DESPUES)`, "1")
	h.expectEval(`return FIRST_TEXT`, "RESUMEN")
	h.expectEval(`return tostring(COMPACTED)`, "true")
}

// TestSessionCompactHookDeny (P22): un hook `compact` que deniega impide la
// compactación; el historial queda intacto y compact() devuelve false.
func TestSessionCompactHookDeny(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerCtl + `
			agent.hook("compact", function() return { deny = "no" } end)
			local s = agent.session{ model = "test/m", no_store = true }
			s:send("hola")
			local ok2 = s:compact()
			OKC = ok2
			HIST = #s.history
			s:close()
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(OKC)`, "false")
	// historial intacto: user + assistant (2).
	h.expectEval(`return tostring(HIST)`, "2")
}

// TestSessionAutoCompact (P25): con context=100 y USAGE_IN=90 (>80% del
// contexto), el SEGUNDO turno autocompacta en su límite antes de pedir. Un hook
// `compact` da el resumen fijo. Se observa el evento agent:compact con auto=true.
func TestSessionAutoCompact(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerCtl + `
			USAGE_IN = 90    -- > 0.8 * 100
			AUTO = nil
			enu.events.on("agent:compact", function(p) AUTO = p.auto end)
			agent.hook("compact", function()
				return { summary = { role = "user", content = { { type = "text", text = "S" } } } }
			end)
			local s = agent.session{ model = "test/m", no_store = true }
			s:send("uno")    -- deja _last_input_tokens = 90
			s:send("dos")    -- en su límite: autocompacta
			-- tras autocompactar + inyectar "dos" + responder: [S, dos(user), assistant]
			HIST = #s.history
			FIRST = s.history[1].content[1].text
			s:close()
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(AUTO)`, "true")
	h.expectEval(`return FIRST`, "S")
}

// TestSessionFork (P22): Session:fork copia el prefijo al hijo y escribe el
// puntero parent en el meta. El hijo es una sesión nueva e independiente.
func TestSessionFork(t *testing.T) {
	h, dataDir := bootAgent(t, providersTomlCtl, false)
	_ = dataDir
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerCtl + `
				local s = agent.session{ model = "test/m" }   -- con store (persiste)
				s:send("hola")                                 -- historial: user + assistant
				PARENT_HIST = #s.history
				PARENT_ID = s.id
				local child = s:fork()
				CHILD_HIST = #child.history
				CHILD_ID = child.id
				DISTINCT = (child.id ~= s.id)
				-- el meta del hijo apunta al padre (sesiones.md §5).
				local meta = child.store:meta()
				PARENT_PTR = meta and meta.parent and meta.parent.id
				child:close()
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(DISTINCT)`, "true")
	// el hijo copió el prefijo del padre.
	h.expectEval(`return tostring(CHILD_HIST == PARENT_HIST)`, "true")
	h.expectEval(`return tostring(PARENT_PTR == PARENT_ID)`, "true")
}

// TestSessionReentryQueue (P23/G4): un `send` con un turno EN VUELO encola; el
// loop lo inyecta y ambos `send` resuelven con el mensaje final del turno. Se
// abre la ventana de turno en vuelo con SLEEP_MS (el adaptador suspende), y el
// segundo send se emite desde otra task durante esa pausa.
func TestSessionReentryQueue(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerCtl + `
			SLEEP_MS = 15
			local s = agent.session{ model = "test/m", no_store = true }
			R1, R2 = nil, nil
			enu.task.spawn(function() R1 = s:send("uno") end)
			enu.task.sleep(5)   -- deja arrancar el turno (adaptador suspendido)
			enu.task.spawn(function() R2 = s:send("dos") end)
			enu.task.sleep(80)  -- deja terminar todo
			-- ambos sends resuelven con el MISMO mensaje final (G4).
			SAME = (R1 ~= nil and R2 ~= nil and R1.content[1].text == R2.content[1].text)
			-- el historial contiene los DOS mensajes de usuario inyectados.
			local users = 0
			for _, m in ipairs(s.history) do if m.role == "user" then users = users + 1 end end
			USERS = users
			CALLS_TOTAL = CALLS
			s:close()
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(SAME)`, "true")
	// dos mensajes de usuario ("uno" y "dos" inyectado por la cola).
	h.expectEval(`return tostring(USERS)`, "2")
	// dos llamadas al modelo (una por mensaje de usuario).
	h.expectEval(`return tostring(CALLS_TOTAL)`, "2")
}

// TestSessionCancel (P22): Session:cancel cancela el turno en vuelo; el `send`
// resuelve como cancelado (devuelve nil) y se emite turn.end con canceled=true.
func TestSessionCancel(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerCtl + `
			SLEEP_MS = 50
			CANCELED_EV = false
			enu.events.on("agent:turn.end", function(p)
				if p.canceled then CANCELED_EV = true end
			end)
			local s = agent.session{ model = "test/m", no_store = true }
			R = "sentinel"
			enu.task.spawn(function() R = s:send("largo") end)
			enu.task.sleep(10)   -- el turno está en vuelo (adaptador durmiendo)
			s:cancel()
			enu.task.sleep(30)   -- deja correr el cleanup
			RES_NIL = (R == nil)
			ACTIVE = s.turn_active
			s:close()
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(RES_NIL)`, "true")
	h.expectEval(`return tostring(CANCELED_EV)`, "true")
	h.expectEval(`return tostring(ACTIVE)`, "false")
}

// TestSessionClearQueue (P22): clear_queue descarta los sends encolados (no el
// turno en vuelo); el send descartado resuelve como cancelado (nil).
func TestSessionClearQueue(t *testing.T) {
	h, _ := bootAgent(t, providersTomlCtl, false)
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerCtl + `
			SLEEP_MS = 40
			local s = agent.session{ model = "test/m", no_store = true }
			R2 = "sentinel"
			enu.task.spawn(function() s:send("uno") end)
			enu.task.sleep(5)
			enu.task.spawn(function() R2 = s:send("dos") end)  -- encolado
			enu.task.sleep(5)
			s:clear_queue()      -- descarta "dos"
			enu.task.sleep(80)
			R2_NIL = (R2 == nil)
			-- solo un mensaje de usuario en el historial ("uno"); "dos" se descartó.
			local users = 0
			for _, m in ipairs(s.history) do if m.role == "user" then users = users + 1 end end
			USERS = users
			s:close()
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(R2_NIL)`, "true")
	h.expectEval(`return tostring(USERS)`, "1")
}
