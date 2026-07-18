package runtime

// Tests de G42 (reintento con backoff en el motor) y G43 (`agent:error`
// estructurado + `Session:retry`). Blindan la lógica marcada 🔒 en el plan:
//
//   G42 — el motor reintenta SOLO la APERTURA del stream (agente.md §2 paso 3)
//   ante un error tabla con `detail.retryable == true`, con backoff exponencial
//   `retry_base_ms · 2^(intento−1)` hasta `max_retries`, anunciando cada espera
//   con `agent:retry`. Un fallo A MITAD de stream NO se reintenta (los deltas ya
//   pintados duplicarían contenido). Agotados los reintentos —o un error no
//   retryable— el error propaga TAL CUAL (con su `retryable` intacto).
//
//   G43 — el payload de `agent:error` lleva `{ message, code, retryable, detail }`
//   (antes se descartaba `detail`/`retryable`); y `Session:retry()` re-ejecuta el
//   turno sobre el historial vigente SIN anexar mensaje nuevo, con EINVAL si hay
//   turno en vuelo, la sesión está cerrada o el historial está vacío.
//
// Mismo arnés que agent_test.go (bootAgent headless + un adaptador de prueba
// registrado desde Lua). El `retry_base_ms` es MINÚSCULO (5 ms) para no dormir de
// verdad: el backoff es real pero imperceptible. Las capturas van a GLOBALES (los
// handlers de eventos corren al otro lado del puente de suspensión, ADR-011).

import (
	"testing"
)

// providersTomlRetryStub: un provider cuyo adaptador "retrystub" registra el propio
// test desde Lua. Controla su comportamiento por globales (ver registerRetryStub).
const providersTomlRetryStub = `
[providers.test]
adapter  = "retrystub"
base_url = "http://localhost/unused"

[[providers.test.models]]
id      = "m1"
aliases = ["m"]
`

// registerRetryStub registra el adaptador "retrystub" y su cableado de captura de
// eventos. Comportamiento gobernado por globales (cada test las fija; en un runtime
// fresco arrancan nil):
//   - CALLS: contador de llamadas a `stream` (cuántas veces se ABRIÓ el stream).
//   - FAIL_OPEN_TIMES (int): las primeras N aperturas LANZAN antes de devolver el
//     iterador (simula 429/5xx al abrir). Su `detail.retryable` = FAIL_OPEN_RETRYABLE
//     (default true) y el detail lleva un `extra="boom"` para verificar que SOBREVIVE.
//   - FAIL_NONRETRYABLE (bool): la apertura lanza un error NO retryable (con code y
//     detail) — cero reintentos.
//   - SLEEP_MS (int): la apertura con éxito SUSPENDE antes de emitir (abre la ventana
//     de "turno en vuelo" para el EINVAL de retry).
//   - FAIL_MID (bool): la apertura tiene ÉXITO pero el iterador emite un delta y luego
//     LANZA retryable — el fallo a mitad de stream que NO debe reintentarse.
//
// En el camino de éxito emite "listo" y para (stop_reason="end").
const registerRetryStub = `
local providers = require("providers")
CALLS = 0
RETRIES = {}         -- capturas de agent:retry, en orden
ERR = nil            -- último agent:error visto
enu.events.on("agent:retry", function(p)
  RETRIES[#RETRIES + 1] = {
    attempt = p.attempt, max_retries = p.max_retries,
    delay_ms = p.delay_ms, code = p.code, session = p.session,
    message = p.message,
  }
end)
enu.events.on("agent:error", function(p)
  ERR = { message = p.message, code = p.code, retryable = p.retryable, detail = p.detail }
end)
providers.register_adapter("retrystub", {
  name = "retrystub", caps = { tools = false, system = true, usage = true },
  stream = function(req, provider)
    CALLS = CALLS + 1
    local n = CALLS
    -- Fallo en la APERTURA (antes de devolver el iterador): reintentable o no.
    if FAIL_OPEN_TIMES and n <= FAIL_OPEN_TIMES then
      error({
        code = "EPROVIDER",
        message = "boom apertura " .. tostring(n),
        detail = { retryable = (FAIL_OPEN_RETRYABLE ~= false), extra = "boom" },
      })
    end
    if FAIL_NONRETRYABLE then
      error({
        code = "EPROVIDER",
        message = "boom permanente",
        detail = { retryable = false, extra = "boom" },
      })
    end
    -- Éxito: opcionalmente suspende (ventana de turno en vuelo).
    if SLEEP_MS and SLEEP_MS > 0 then enu.task.sleep(SLEEP_MS) end
    -- Fallo A MITAD de stream: el iterador emite un delta y luego muere retryable.
    if FAIL_MID then
      local i = 0
      return function()
        i = i + 1
        if i == 1 then return { type = "text", text = "parcial" } end
        error({ code = "EPROVIDER", message = "boom a mitad",
                detail = { retryable = true } })
      end
    end
    local assembled = { role = "assistant", content = { { type = "text", text = "listo" } } }
    local events = {
      { type = "text", text = "listo" },
      { type = "usage", input_tokens = 5, output_tokens = 2 },
      { type = "done", stop_reason = "end", message = assembled },
    }
    local i = 0
    return function() i = i + 1; return events[i] end
  end,
})
`

// TestG42RetryAbreYSegundoIntento: un error retryable en la APERTURA se reintenta y
// el turno acaba bien al segundo intento; se observó UN `agent:retry` con el
// attempt/delay correctos (delay = 1×base) y la atribución `session` (G3).
func TestG42RetryAbreYSegundoIntento(t *testing.T) {
	h, _ := bootAgent(t, providersTomlRetryStub, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerRetryStub + `
				FAIL_OPEN_TIMES = 1  -- la 1ª apertura falla retryable, la 2ª pasa
				local s = agent.session{ model = "test/m", no_store = true,
					max_retries = 3, retry_base_ms = 5 }
				SID = s.id
				local final = s:send("hola")
				FINAL_TEXT = final and final.content[1].text or "nil"
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(FINAL_TEXT)`, "listo") // acabó bien al reintentar
	h.expectEval(`return tostring(CALLS)`, "2")          // 1 fallo + 1 éxito
	h.expectEval(`return tostring(ERR == nil)`, "true")  // no llegó a agent:error
	h.expectEval(`return tostring(#RETRIES)`, "1")       // un solo backoff
	h.expectEval(`return tostring(RETRIES[1].attempt == 1)`, "true")
	h.expectEval(`return tostring(RETRIES[1].max_retries == 3)`, "true")
	h.expectEval(`return tostring(RETRIES[1].delay_ms == 5)`, "true") // base·2^0
	h.expectEval(`return tostring(RETRIES[1].code)`, "EPROVIDER")
	h.expectEval(`return tostring(RETRIES[1].message)`, "boom apertura 1") // §4: el payload lleva message
	h.expectEval(`return tostring(RETRIES[1].session == SID)`, "true")     // atribución G3
}

// TestG42CancelDuranteBackoff: el backoff duerme en un punto de suspensión NORMAL
// (agente.md §2): un Session:cancel durante la espera aborta el turno como siempre
// (S08) — el send devuelve nil, el turno cierra como cancelado (sin agent:error) y
// el adaptador NO vuelve a abrirse. El retry_base_ms es enorme a propósito: si el
// cancel no cortara la espera, el test no terminaría en tiempo de suite.
func TestG42CancelDuranteBackoff(t *testing.T) {
	h, _ := bootAgent(t, providersTomlRetryStub, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerRetryStub + `
				FAIL_OPEN_TIMES = 100
				CANCELED_EV = false
				enu.events.on("agent:turn.end", function(p)
					if p.canceled then CANCELED_EV = true end
				end)
				local s = agent.session{ model = "test/m", no_store = true,
					max_retries = 3, retry_base_ms = 60000 }
				RET = "sentinel"
				enu.task.spawn(function() RET = s:send("hola") end)
				enu.task.sleep(30)  -- la 1ª apertura ya falló; el turno duerme el backoff (60 s)
				CALLS_AT_CANCEL = CALLS
				s:cancel()
				enu.task.sleep(30)  -- deja aterrizar el aborto (cleanup + waiters)
				ACTIVE = s.turn_active
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(CALLS_AT_CANCEL)`, "1") // el cancel llegó DURANTE el backoff
	h.expectEval(`return tostring(CALLS)`, "1")           // y el stream no se reabrió jamás
	h.expectEval(`return tostring(#RETRIES)`, "1")        // la espera se anunció antes de dormir
	h.expectEval(`return tostring(RET == nil)`, "true")   // send devolvió nil (cancelado)
	h.expectEval(`return tostring(CANCELED_EV)`, "true")  // turn.end con canceled
	h.expectEval(`return tostring(ERR == nil)`, "true")   // cancelar NO es un agent:error
	h.expectEval(`return tostring(ACTIVE)`, "false")
}

// TestG42BackoffExponencial: dos fallos retryables encadenados → dos esperas con
// delays 1×base y 2×base (el backoff CRECE exponencialmente).
func TestG42BackoffExponencial(t *testing.T) {
	h, _ := bootAgent(t, providersTomlRetryStub, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerRetryStub + `
				FAIL_OPEN_TIMES = 2  -- dos fallos, la 3ª apertura pasa
				local s = agent.session{ model = "test/m", no_store = true,
					max_retries = 3, retry_base_ms = 5 }
				local final = s:send("hola")
				FINAL_TEXT = final and final.content[1].text or "nil"
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(FINAL_TEXT)`, "listo")
	h.expectEval(`return tostring(CALLS)`, "3") // 2 fallos + 1 éxito
	h.expectEval(`return tostring(#RETRIES)`, "2")
	h.expectEval(`return tostring(RETRIES[1].attempt == 1 and RETRIES[1].delay_ms == 5)`, "true")  // 5·2^0
	h.expectEval(`return tostring(RETRIES[2].attempt == 2 and RETRIES[2].delay_ms == 10)`, "true") // 5·2^1
}

// TestG42ErrorNoRetryable: un error NO retryable en la apertura no se reintenta
// (cero `agent:retry`); se emite `agent:error` de inmediato y `send` retorna nil.
func TestG42ErrorNoRetryable(t *testing.T) {
	h, _ := bootAgent(t, providersTomlRetryStub, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerRetryStub + `
				FAIL_NONRETRYABLE = true
				local s = agent.session{ model = "test/m", no_store = true,
					max_retries = 3, retry_base_ms = 5 }
				RET = s:send("hola")
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(CALLS)`, "1") // sin reintentos
	h.expectEval(`return tostring(#RETRIES)`, "0")
	h.expectEval(`return tostring(RET == nil)`, "true") // el turno falló
	h.expectEval(`return tostring(ERR ~= nil)`, "true") // agent:error se emitió
	h.expectEval(`return tostring(ERR.code)`, "EPROVIDER")
	h.expectEval(`return tostring(ERR.retryable == nil)`, "true") // false → nil (G43)
}

// TestG42ReintentosAgotados: fallos retryables sin fin con max_retries=2 →
// max_retries+1 = 3 llamadas al adaptador, y el `agent:error` final lleva el error
// estructurado COMPLETO (G43): retryable=true, code y el detail intactos.
func TestG42ReintentosAgotados(t *testing.T) {
	h, _ := bootAgent(t, providersTomlRetryStub, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerRetryStub + `
				FAIL_OPEN_TIMES = 100  -- siempre falla (nunca alcanza el éxito)
				local s = agent.session{ model = "test/m", no_store = true,
					max_retries = 2, retry_base_ms = 5 }
				RET = s:send("hola")
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(CALLS)`, "3")    // max_retries(2) + 1 intento inicial
	h.expectEval(`return tostring(#RETRIES)`, "2") // dos esperas antes de rendirse
	h.expectEval(`return tostring(RET == nil)`, "true")
	// agent:error con el error estructurado completo (G43).
	h.expectEval(`return tostring(ERR ~= nil)`, "true")
	h.expectEval(`return tostring(ERR.retryable == true)`, "true")
	h.expectEval(`return tostring(ERR.code)`, "EPROVIDER")
	h.expectEval(`return tostring(ERR.detail ~= nil and ERR.detail.extra == "boom")`, "true")
}

// TestG42FalloMitadStreamNoReintenta: un fallo A MITAD de stream (el stub emite un
// delta y luego lanza retryable) NO se reintenta —los deltas ya están pintados—:
// una sola apertura y cero `agent:retry`; propaga como error del turno.
func TestG42FalloMitadStreamNoReintenta(t *testing.T) {
	h, _ := bootAgent(t, providersTomlRetryStub, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerRetryStub + `
				FAIL_MID = true
				local s = agent.session{ model = "test/m", no_store = true,
					max_retries = 3, retry_base_ms = 5 }
				RET = s:send("hola")
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(CALLS)`, "1")         // la apertura NO se reabrió
	h.expectEval(`return tostring(#RETRIES)`, "0")      // sin reintento
	h.expectEval(`return tostring(RET == nil)`, "true") // el turno falló
	h.expectEval(`return tostring(ERR ~= nil)`, "true") // agent:error del fallo
}

// TestG43SessionRetryReejecuta (G43): tras un turno fallido, `Session:retry()`
// re-ejecuta el turno SIN duplicar el mensaje de usuario en el historial y devuelve
// el mensaje final del turno re-ejecutado.
func TestG43SessionRetryReejecuta(t *testing.T) {
	h, _ := bootAgent(t, providersTomlRetryStub, false)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerRetryStub + `
				FAIL_NONRETRYABLE = true  -- el primer turno falla
				local s = agent.session{ model = "test/m", no_store = true,
					max_retries = 0, retry_base_ms = 5 }
				local r1 = s:send("hola")
				R1_NIL = (r1 == nil)
				-- Ahora el adaptador tendrá éxito: el reintento debe re-ejecutar.
				FAIL_NONRETRYABLE = false
				local r2 = s:retry()
				R2_TEXT = r2 and r2.content[1].text or "nil"
				-- El historial NO duplica el mensaje de usuario.
				USERS = 0
				for _, m in ipairs(s.history) do
					if m.role == "user" then USERS = USERS + 1 end
				end
				HIST = #s.history
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(R1_NIL)`, "true")   // el primer turno falló
	h.expectEval(`return tostring(R2_TEXT)`, "listo") // retry devolvió el mensaje final
	h.expectEval(`return tostring(USERS)`, "1")       // "hola" NO se duplicó
	h.expectEval(`return tostring(HIST)`, "2")        // [user "hola", assistant "listo"]
}

// TestG43SessionRetryEINVAL (G43): `Session:retry()` lanza EINVAL accionable si el
// historial está vacío, la sesión está cerrada, o hay un turno en vuelo.
func TestG43SessionRetryEINVAL(t *testing.T) {
	h, _ := bootAgent(t, providersTomlRetryStub, false)

	// (a) historial vacío: no hay turno que re-ejecutar.
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerRetryStub + `
			local s = agent.session{ model = "test/m", no_store = true }
			local ok, e = pcall(s.retry, s)
			VACIO_OK = ok
			VACIO_CODE = (type(e) == "table" and e.code) or "nil"
			s:close()
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(VACIO_OK)`, "false")
	h.expectEval(`return tostring(VACIO_CODE)`, "EINVAL")

	// (b) sesión cerrada.
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerRetryStub + `
			local s = agent.session{ model = "test/m", no_store = true }
			s:send("hola")          -- deja algo en el historial
			s:close()
			local ok, e = pcall(s.retry, s)
			CERR_OK = ok
			CERR_CODE = (type(e) == "table" and e.code) or "nil"
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(CERR_OK)`, "false")
	h.expectEval(`return tostring(CERR_CODE)`, "EINVAL")

	// (c) turno en vuelo: un send con el adaptador SUSPENDIDO mantiene el turno
	// activo; retry mientras tanto debe rechazar con EINVAL.
	h.eval(`
		out = nil
		enu.task.spawn(function()
			local agent = require("agent")
			` + registerRetryStub + `
			SLEEP_MS = 50
			local s = agent.session{ model = "test/m", no_store = true }
			enu.task.spawn(function() s:send("hola") end)  -- abre el turno (adaptador durmiendo)
			enu.task.sleep(10)  -- deja arrancar el turno (queda suspendido en la apertura)
			local ok, e = pcall(s.retry, s)
			VUELO_OK = ok
			VUELO_CODE = (type(e) == "table" and e.code) or "nil"
			enu.task.sleep(80)  -- deja terminar el turno
			s:close()
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(VUELO_OK)`, "false")
	h.expectEval(`return tostring(VUELO_CODE)`, "EINVAL")
}
