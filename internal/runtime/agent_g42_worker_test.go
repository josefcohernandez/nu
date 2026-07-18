package runtime

// Tests de G42 en el SUBAGENTE-WORKER (agente.md §2/§9): el worker aplica la MISMA
// política de reintento de la apertura del stream que el motor del estado principal
// —es una copia del algoritmo en `subagent_worker.lua`, así que blindar el del
// motor (agent_g42_test.go) no blinda esta—, y hereda `max_retries`/`retry_base_ms`
// del padre en su `init` (`subagent.lua`), con override por el spec del spawn.
//
// Arnés: bootSubagent (subagent_test.go) + el adaptador require-able `wretry`
// (abajo). Un worker no comparte globales con el principal, así que el adaptador se
// dirige por el PROMPT ("fallar:N" = las N primeras aperturas lanzan; "permanente" =
// el fallo lleva retryable=false) y CODIFICA el número de apertura en el mensaje de
// error ("boom apertura N") y del éxito ("ok tras N"): el conteo de llamadas cruza
// la frontera como dato observable, sin tocar la API.

import (
	"strings"
	"testing"
)

// wsRetryAdapterModule: adaptador stub require-able cuya apertura de stream falla las
// primeras N veces (N del prompt). El contador vive como local del módulo: cada
// worker requiere su copia fresca (CALLS arranca en 0 por job).
const wsRetryAdapterModule = `
local CALLS = 0
local function prompt_text(req)
  local first = req.messages and req.messages[1]
  if not (first and type(first.content) == "table") then return "" end
  for _, b in ipairs(first.content) do
    if b.type == "text" and type(b.text) == "string" then return b.text end
  end
  return ""
end
return {
  name = "wretry", caps = { tools = false, system = true, usage = true },
  stream = function(req, provider)
    CALLS = CALLS + 1
    local prompt = prompt_text(req)
    local fails = tonumber(prompt:match("fallar:(%d+)")) or 0
    if CALLS <= fails then
      error({
        code = "EPROVIDER",
        message = "boom apertura " .. CALLS,
        detail = { retryable = (prompt:match("permanente") == nil) },
      })
    end
    local text = "ok tras " .. CALLS
    local msg = { role = "assistant", content = { { type = "text", text = text } } }
    local events = {
      { type = "text", text = text },
      { type = "done", stop_reason = "end", message = msg },
    }
    local i = 0
    return function() i = i + 1; return events[i] end
  end,
}
`

// firstResult devuelve el primer retorno de un eval ("" si no devolvió nada).
func firstResult(res []string) string {
	if len(res) == 0 {
		return ""
	}
	return res[0]
}

// runWorkerRetry corre un subagente-worker con el adaptador wretry y devuelve los
// globales capturados: WTEXT (digest.text del éxito) o WERR (message del EAGENT).
func runWorkerRetry(t *testing.T, h *harness, parentOpts, subOpts, prompt string) {
	t.Helper()
	h.eval(`
		out, errc = nil, nil
		WTEXT, WERR = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local parent = agent.session{ model = "testr/m3", no_store = true` + parentOpts + ` }
				local sub = parent:spawn{
					model = "testr/m3", no_store = true, worker = true, tools = {},
					adapter_modules = { "wretry" }` + subOpts + `,
				}
				local rok, r = pcall(sub.run, sub, ` + prompt + `)
				if rok then
					WTEXT = r.text
				else
					WERR = (type(r) == "table" and r.message) or tostring(r)
				end
				sub:cancel()
				parent:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
}

// TestG42WorkerRetryAbreYReintenta: dos fallos retryables en la apertura dentro del
// worker → dos backoffs (invisibles: sin bus, agente.md §2) y éxito a la 3ª apertura.
func TestG42WorkerRetryAbreYReintenta(t *testing.T) {
	h, _ := bootSubagent(t)
	runWorkerRetry(t, h, "", ", max_retries = 3, retry_base_ms = 5", `"fallar:2"`)
	h.expectEval(`return tostring(WERR)`, "nil")
	h.expectEval(`return tostring(WTEXT)`, "ok tras 3") // 2 fallos + 1 éxito, ni una apertura más
}

// TestG42WorkerReintentosAgotados: fallos sin fin con max_retries=2 → exactamente
// max_retries+1 = 3 aperturas (la frontera exacta: ni un reintento de más) y el
// error del worker llega al padre como EAGENT con el mensaje del último fallo.
func TestG42WorkerReintentosAgotados(t *testing.T) {
	h, _ := bootSubagent(t)
	runWorkerRetry(t, h, "", ", max_retries = 2, retry_base_ms = 5", `"fallar:100"`)
	h.expectEval(`return tostring(WTEXT)`, "nil")
	if got := firstResult(h.eval(`return tostring(WERR)`)); !strings.Contains(got, "boom apertura 3") {
		t.Fatalf("G42 (worker): se esperaba el fallo de la apertura 3 (max_retries=2 agotados), llegó: %q", got)
	}
}

// TestG42WorkerErrorNoRetryable: un fallo con retryable=false en la apertura no se
// reintenta NUNCA (clasificación estricta detail.retryable == true): una sola
// apertura y el error propaga al padre.
func TestG42WorkerErrorNoRetryable(t *testing.T) {
	h, _ := bootSubagent(t)
	runWorkerRetry(t, h, "", ", max_retries = 3, retry_base_ms = 5", `"permanente fallar:100"`)
	h.expectEval(`return tostring(WTEXT)`, "nil")
	if got := firstResult(h.eval(`return tostring(WERR)`)); !strings.Contains(got, "boom apertura 1") {
		t.Fatalf("G42 (worker): un error no retryable debe fallar en la apertura 1, llegó: %q", got)
	}
}

// TestG42WorkerHeredaDelPadre: el spec del spawn NO trae max_retries → el worker
// hereda el del padre (agente.md §2/§9). El padre lleva max_retries=0: un solo fallo
// retryable debe matar el job SIN reintento — si la herencia se rompiera y el worker
// cayera a su default (3), la 2ª apertura tendría éxito y el test fallaría.
func TestG42WorkerHeredaDelPadre(t *testing.T) {
	h, _ := bootSubagent(t)
	runWorkerRetry(t, h, ", max_retries = 0, retry_base_ms = 5", "", `"fallar:1"`)
	h.expectEval(`return tostring(WTEXT)`, "nil")
	if got := firstResult(h.eval(`return tostring(WERR)`)); !strings.Contains(got, "boom apertura 1") {
		t.Fatalf("G42 (worker): el max_retries=0 del padre no se heredó (¿cayó al default?): %q", got)
	}
}
