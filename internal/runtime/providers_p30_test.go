package runtime

// Tests de los adaptadores `openai-compat` y `gemini` (P30) y de los breakpoints
// de caché del adaptador `anthropic` (P31), todos embebidos en
// internal/runtime/embedded/providers/lua/providers/. Mismo criterio que S37
// (providers_anthropic_test.go): contra un SSE GRABADO del dialecto, el adaptador
// debe emitir el stream de Eventos CANÓNICO de [providers.md](../../docs/providers.md)
// §2.3. Sin red real: un servidor httptest sirve el SSE y captura el cuerpo del
// request (para verificar la traducción canónico -> dialecto y el cache_control).

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// genericSSEServer levanta un httptest que sirve `body` como SSE (flush por
// línea) y CAPTURA el cuerpo del último request. Devuelve el servidor y un getter
// del cuerpo (para asserts de la traducción canónico -> dialecto, P30/P31). No
// impone cabeceras de un dialecto concreto (a diferencia de `sseServer`, atado a
// Anthropic): sirve a openai-compat, gemini y anthropic por igual.
func genericSSEServer(t *testing.T, body string) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastBody = string(raw)
		mu.Unlock()
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("el ResponseWriter no implementa Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		for _, line := range strings.SplitAfter(body, "\n") {
			if line == "" {
				continue
			}
			_, _ = io.WriteString(w, line)
			fl.Flush()
		}
	}))
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastBody
	}
}

// bootAdapter arranca un Runtime con `providers` activado y un provider de
// `adapterName` apuntando a `baseURL`. Reusa el patrón de bootAnthropic.
func bootAdapter(t *testing.T, adapterName, baseURL string) *harness {
	t.Helper()
	t.Setenv("TEST_API_KEY", "sk-test-grabado")
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\"]\n")
	toml := fmt.Sprintf(`
[providers.p]
adapter     = %q
base_url    = %q
api_key_env = "TEST_API_KEY"

[[providers.p.models]]
id         = "m1"
context    = 200000
max_output = 32000
aliases    = ["m"]
`, adapterName, baseURL)
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	return bootWithToml(t, "", cfg, WithForceUI(true))
}

// driveTurn corre un turno-de-adaptador en una task y vuelca el resultado a las
// globales que luego se asertan: kinds (secuencia de tipos de Event), text, la
// tool call (nombre, args.city del Message ensamblado), usage, stop_reason.
const driveTurn = `
	out = {}
	nu.task.spawn(function()
		local p = require("providers")
		local r = p.resolve("p/m")
		local req = {
			model = r.config.model.id,
			system = "eres útil",
			messages = {
				{ role = "user", content = { { type = "text", text = "hola" } } },
			},
			tools = { { name = "get_weather", description = "clima", schema = { type = "object" } } },
		}
		local kinds, text, args_json = {}, "", ""
		local usage_in, usage_out, done = nil, nil, nil
		for ev in r.adapter.stream(req, r.config) do
			kinds[#kinds+1] = ev.type
			if ev.type == "text" then text = text .. ev.text end
			if ev.type == "tool_call.delta" then args_json = args_json .. ev.args_json end
			if ev.type == "usage" then
				usage_in = ev.input_tokens or usage_in
				usage_out = ev.output_tokens or usage_out
			end
			if ev.type == "done" then done = ev end
		end
		out.kinds = table.concat(kinds, ",")
		out.text = text
		out.args_json = args_json
		out.usage_in, out.usage_out = usage_in, usage_out
		out.stop = done and done.stop_reason
		out.role = done and done.message.role
		out.nblocks = done and #done.message.content
		local m = done.message
		-- localiza el bloque tool_call (el orden puede variar entre dialectos).
		for _, b in ipairs(m.content) do
			if b.type == "tool_call" then
				out.tool_name = b.name
				out.tool_city = b.args.city
			elseif b.type == "text" then
				out.btext = b.text
			end
		end
	end)
`

// SSE de OpenAI GRABADO: texto en dos deltas + una tool call con `arguments`
// troceado por `index` + `finish_reason` + un chunk de `usage` + `[DONE]`.
const openaiSSE = `data: {"choices":[{"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":"Hola "},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":"mundo"},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" \"Madrid\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7}}

data: [DONE]

`

// TestOpenAICompatStreamCanonico: el criterio de hecho de P30 para openai-compat.
func TestOpenAICompatStreamCanonico(t *testing.T) {
	srv, getBody := genericSSEServer(t, openaiSSE)
	defer srv.Close()
	h := bootAdapter(t, "openai-compat", srv.URL)
	h.eval(driveTurn)

	h.expectEval(`return out.text`, "Hola mundo")
	h.expectEval(`return out.btext`, "Hola mundo")
	h.expectEval(`return out.args_json`, `{"city": "Madrid"}`)
	h.expectEval(`return out.tool_name`, "get_weather")
	h.expectEval(`return out.tool_city`, "Madrid")
	h.expectEval(`return tostring(out.usage_in)`, "11")
	h.expectEval(`return tostring(out.usage_out)`, "7")
	h.expectEval(`return out.stop`, "tool_calls")
	h.expectEval(`return out.role`, "assistant")
	h.expectEval(`return tostring(out.nblocks)`, "2")
	h.expectEval(`return out.kinds`,
		"text,text,tool_call.begin,tool_call.delta,tool_call.delta,usage,tool_call.end,done")

	// La traducción canónico -> dialecto: system como mensaje role=system,
	// tools de tipo function, y el mensaje de usuario.
	body := getBody()
	for _, want := range []string{`"role":"system"`, `"type":"function"`, `"get_weather"`, `"stream":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("el cuerpo enviado a openai no contiene %q\n%s", want, body)
		}
	}
}

// SSE de Gemini GRABADO (`?alt=sse`): texto en dos deltas + un `functionCall`
// entero + `finishReason` + `usageMetadata`.
const geminiSSE = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hola "}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"mundo"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Madrid"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7}}

`

// TestGeminiStreamCanonico: el criterio de hecho de P30 para gemini.
func TestGeminiStreamCanonico(t *testing.T) {
	srv, getBody := genericSSEServer(t, geminiSSE)
	defer srv.Close()
	h := bootAdapter(t, "gemini", srv.URL)
	h.eval(driveTurn)

	h.expectEval(`return out.text`, "Hola mundo")
	h.expectEval(`return out.btext`, "Hola mundo")
	h.expectEval(`return out.tool_name`, "get_weather")
	h.expectEval(`return out.tool_city`, "Madrid")
	h.expectEval(`return tostring(out.usage_in)`, "11")
	h.expectEval(`return tostring(out.usage_out)`, "7")
	// functionCall presente sin finishReason de tool -> stop derivado a tool_calls.
	h.expectEval(`return out.stop`, "tool_calls")
	h.expectEval(`return tostring(out.nblocks)`, "2")
	h.expectEval(`return out.kinds`,
		"text,text,tool_call.begin,tool_call.delta,tool_call.end,usage,done")

	// Traducción: contents con role=model, systemInstruction, functionDeclarations.
	body := getBody()
	for _, want := range []string{`"contents"`, `"systemInstruction"`, `"functionDeclarations"`, `"get_weather"`} {
		if !strings.Contains(body, want) {
			t.Errorf("el cuerpo enviado a gemini no contiene %q\n%s", want, body)
		}
	}
}

// driveOrder corre un turno-de-adaptador y vuelca a globales el ORDEN real de
// los bloques del Message ensamblado (A-12): `order` = tipos concatenados,
// `texts` = los textos de los bloques `text` separados por `|`, `nblocks`.
const driveOrder = `
	out = {}
	nu.task.spawn(function()
		local p = require("providers")
		local r = p.resolve("p/m")
		local req = {
			model = r.config.model.id,
			messages = {
				{ role = "user", content = { { type = "text", text = "hola" } } },
			},
			tools = { { name = "get_weather", description = "clima", schema = { type = "object" } } },
		}
		local done = nil
		for ev in r.adapter.stream(req, r.config) do
			if ev.type == "done" then done = ev end
		end
		local types, texts = {}, {}
		for _, b in ipairs(done.message.content) do
			types[#types+1] = b.type
			if b.type == "text" then texts[#texts+1] = b.text end
		end
		out.order = table.concat(types, ",")
		out.texts = table.concat(texts, "|")
		out.nblocks = #done.message.content
	end)
`

// SSE de Gemini con parts `[text, functionCall, text]`: el modelo intercala texto
// antes y después de la llamada. El Message canónico debe conservar ese orden
// (tres bloques), no fundir el texto ni anteponerlo a la tool_call (A-12).
const geminiSSEInterleaved = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"antes "}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Madrid"}}}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"después"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7}}

`

// TestGeminiOrdenTextoToolCall (A-12): parts `[text, functionCall, text]` producen
// un Message con bloques `text, tool_call, text` en ESE orden, con el texto
// troceado en el bloque que corresponde a cada tramo (no fundido).
func TestGeminiOrdenTextoToolCall(t *testing.T) {
	srv, _ := genericSSEServer(t, geminiSSEInterleaved)
	defer srv.Close()
	h := bootAdapter(t, "gemini", srv.URL)
	h.eval(driveOrder)

	h.expectEval(`return out.order`, "text,tool_call,text")
	h.expectEval(`return out.texts`, "antes |después")
	h.expectEval(`return tostring(out.nblocks)`, "3")
}

// SSE de Gemini con parts `[functionCall, text]`: la llamada llega ANTES del
// texto. El bug histórico insertaba el texto en posición 1 y lo anteponía a la
// tool_call; el orden canónico debe ser `tool_call, text` (A-12).
const geminiSSECallFirst = `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Madrid"}}}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"luego"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7}}

`

// TestGeminiOrdenToolCallTexto (A-12): parts `[functionCall, text]` producen un
// Message con bloques `tool_call, text` en ese orden (no `text, tool_call`).
func TestGeminiOrdenToolCallTexto(t *testing.T) {
	srv, _ := genericSSEServer(t, geminiSSECallFirst)
	defer srv.Close()
	h := bootAdapter(t, "gemini", srv.URL)
	h.eval(driveOrder)

	h.expectEval(`return out.order`, "tool_call,text")
	h.expectEval(`return out.texts`, "luego")
	h.expectEval(`return tostring(out.nblocks)`, "2")
}

// SSE mínimo de Anthropic para que el turno complete (sin tools): message_start
// + un texto + message_stop. Sirve para inspeccionar el cuerpo del request (P31).
const minimalAnthropicSSE = `event: message_start
data: {"type":"message_start","message":{"role":"assistant","usage":{"input_tokens":5}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`

// TestAnthropicCacheControl (P31): el adaptador coloca breakpoints `cache_control`
// mecánicamente en tools + system + últimos mensajes (providers.md §3 obligación
// 6). Se inspecciona el cuerpo del request enviado.
func TestAnthropicCacheControl(t *testing.T) {
	srv, getBody := genericSSEServer(t, minimalAnthropicSSE)
	defer srv.Close()
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\"]\n")
	toml := fmt.Sprintf(`
[providers.anthropic]
adapter     = "anthropic"
base_url    = %q
api_key_env = "ANTHROPIC_API_KEY"

[[providers.anthropic.models]]
id      = "claude-opus-4-8"
aliases = ["opus"]
`, srv.URL)
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	h := bootWithToml(t, "", cfg, WithForceUI(true))

	h.eval(`
		nu.task.spawn(function()
			local p = require("providers")
			local r = p.resolve("anthropic/opus")
			local req = {
				model = r.config.model.id,
				system = "eres útil",
				messages = {
					{ role = "user", content = { { type = "text", text = "hola" } } },
				},
				tools = { { name = "t1", description = "x", schema = { type = "object" } } },
			}
			for _ in r.adapter.stream(req, r.config) do end
		end)
	`)

	body := getBody()
	// cache_control presente, y system enviado como array de bloques (no string).
	if !strings.Contains(body, `"cache_control"`) {
		t.Errorf("falta cache_control en el cuerpo (P31)\n%s", body)
	}
	if strings.Count(body, `"cache_control"`) < 3 {
		t.Errorf("se esperaban >=3 breakpoints cache_control (tools+system+mensaje), got %d\n%s",
			strings.Count(body, `"cache_control"`), body)
	}
	if !strings.Contains(body, `"ephemeral"`) {
		t.Errorf("cache_control sin type ephemeral\n%s", body)
	}
}
