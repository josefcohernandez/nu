package runtime

// Tests del adaptador `anthropic` (S37, embebido en
// internal/runtime/embedded/providers/lua/providers/adapter_anthropic.lua). Es
// el PRIMER dialecto real: Lua sobre la API pública congelada (ADR-003) que
// habla la Messages API de Anthropic vía `enu.http.stream` (S20) y traduce su SSE
// al stream de Eventos CANÓNICO de [providers.md](../../docs/contracts/providers.md) §2.3.
//
// Como NO hay red, se GRABA un SSE de Anthropic realista (la secuencia de
// eventos `message_start`, `content_block_*` de tipos text/tool_use/thinking,
// `message_delta` con usage, `message_stop`) y se sirve desde un servidor
// `httptest` local (mismo patrón que los tests de S20, stream_test.go). El
// `providers.toml` apunta su `base_url` a ese servidor.
//
// Cubre:
//   - **traducción del dialecto** (criterio de hecho de S37: "contra un SSE
//     grabado, el adaptador emite el stream de mensajes canónico"): text deltas,
//     tool_use (begin/delta/end + input JSON troceado en `input_json_delta`
//     acumulado y decodificado), thinking, usage, y el `done` con el Message
//     ensamblado;
//   - **CP-9** (hito de veto de perf): el camino caliente completo HTTP stream →
//     SSE → markdown en streaming, con el Message final correcto y el markdown
//     creciendo estable token a token.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sseServer levanta un servidor httptest que sirve `body` como SSE (un único
// stream, flush por línea para imitar la llegada incremental). Reusa el patrón
// http.Flusher de stream_test.go. El handler comprueba además que el adaptador
// envió las cabeceras del dialecto (x-api-key, anthropic-version) y POST.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// El adaptador debe hablar la Messages API: POST a /v1/messages con la
		// clave en x-api-key (no Bearer) y la versión.
		if r.Method != http.MethodPost {
			t.Errorf("método: got %q, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Errorf("path: got %q, want .../v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got == "" {
			t.Errorf("falta cabecera x-api-key")
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Errorf("falta cabecera anthropic-version")
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("el ResponseWriter no implementa Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		// Empuja línea a línea para que el cliente reciba el SSE según llega
		// (camino caliente real, no buffereado al final).
		for _, line := range strings.SplitAfter(body, "\n") {
			if line == "" {
				continue
			}
			_, _ = io.WriteString(w, line)
			fl.Flush()
		}
	}))
}

// bootAnthropic arranca un Runtime con la extensión `providers` activada y un
// `providers.toml` cuyo provider `anthropic` apunta su base_url a `baseURL`
// (normalmente un servidor httptest). Fija la api_key del entorno (la lee
// `resolve`, providers.md §1).
func bootAnthropic(t *testing.T, baseURL string) *harness {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-grabado")
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\"]\n")
	toml := fmt.Sprintf(`
[providers.anthropic]
adapter     = "anthropic"
base_url    = %q
api_key_env = "ANTHROPIC_API_KEY"

[[providers.anthropic.models]]
id         = "claude-opus-4-8"
context    = 200000
max_output = 32000
aliases    = ["opus"]
`, baseURL)
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	// Forzamos la UI (G20, S32): el entorno de test es headless, así que sin
	// WithForceUI(true) `enu.ui` no existiría y el camino caliente de CP-9 (blit
	// del markdown a una región) no podría ejercitarse. El gating REAL (por TTY)
	// sigue aplicando al binario; aquí solo habilitamos la superficie para la prueba.
	return bootWithToml(t, "", cfg, WithForceUI(true))
}

// recordedSSE es un SSE de Anthropic GRABADO y realista: una vuelta de
// conversación que produce texto markdown en streaming, un bloque de thinking,
// y una tool call cuyo input JSON llega TROCEADO en `input_json_delta` (el caso
// que obliga a acumular). Cierra con message_delta (usage + stop_reason) y
// message_stop. Incluye un `ping` (keep-alive) que el adaptador debe ignorar.
//
// El texto, repartido en deltas, forma el markdown:
//
//	"# Hola\n\nEsto es **markdown** en _streaming_.\n"
const recordedSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":42,"cache_read_input_tokens":10,"output_tokens":0}}}

event: ping
data: {"type":"ping"}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Voy a "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"saludar."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"AbC123=="}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"# Hola\n\n"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Esto es **mark"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"down** en _streaming_.\n"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_99","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":" \"Madrid\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":57}}

event: message_stop
data: {"type":"message_stop"}

`

// TestAnthropicStreamCanonico es el "criterio de hecho" de S37: contra el SSE
// grabado, el adaptador emite el stream de mensajes CANÓNICO (providers.md
// §2.3). Verifica la secuencia de tipos de Event, el texto/thinking ensamblados,
// la tool call (input JSON troceado acumulado y decodificado), el usage y el
// Message final del `done`.
func TestAnthropicStreamCanonico(t *testing.T) {
	srv := sseServer(t, recordedSSE)
	defer srv.Close()
	h := bootAnthropic(t, srv.URL)

	h.eval(`
		out = {}
		enu.task.spawn(function()
			local p = require("providers")
			local r = p.resolve("anthropic/opus")
			local req = {
				model = r.config.model.id,
				system = "eres útil",
				messages = {
					{ role = "user", content = { { type = "text", text = "hola" } } },
				},
				tools = { { name = "get_weather", description = "clima", schema = { type = "object" } } },
			}
			local kinds = {}
			local text, thinking = "", ""
			local tool_begin_name, tool_args_json, tool_end_id = nil, "", nil
			local usage_in, usage_out = nil, nil
			local last_done = nil
			for ev in r.adapter.stream(req, r.config) do
				kinds[#kinds+1] = ev.type
				if ev.type == "text" then text = text .. ev.text end
				if ev.type == "thinking" then thinking = thinking .. ev.text end
				if ev.type == "tool_call.begin" then tool_begin_name = ev.name end
				if ev.type == "tool_call.delta" then tool_args_json = tool_args_json .. ev.args_json end
				if ev.type == "tool_call.end" then tool_end_id = ev.id end
				if ev.type == "usage" then
					usage_in = ev.input_tokens or usage_in
					usage_out = ev.output_tokens or usage_out
				end
				if ev.type == "done" then last_done = ev end
			end
			out.kinds = table.concat(kinds, ",")
			out.text = text
			out.thinking = thinking
			out.tool_begin_name = tool_begin_name
			out.tool_args_json = tool_args_json
			out.tool_end_id = tool_end_id
			out.usage_in = usage_in
			out.usage_out = usage_out
			-- Message ensamblado en el done (providers.md §2.1/§2.3).
			out.stop = last_done and last_done.stop_reason
			out.role = last_done and last_done.message.role
			out.nblocks = last_done and #last_done.message.content
			-- Bloques por orden: thinking, text, tool_call.
			local m = last_done.message
			out.b1type = m.content[1].type
			out.b1sig  = m.content[1].meta and m.content[1].meta.signature
			out.b2type = m.content[2].type
			out.b2text = m.content[2].text
			out.b3type = m.content[3].type
			out.b3id   = m.content[3].id
			out.b3name = m.content[3].name
			out.b3city = m.content[3].args.city
		end)
	`)

	// El texto ensamblado es el markdown completo.
	h.expectEval(`return out.text`, "# Hola\n\nEsto es **markdown** en _streaming_.\n")
	h.expectEval(`return out.thinking`, "Voy a saludar.")
	// Tool call: nombre, JSON de args troceado acumulado, id de cierre.
	h.expectEval(`return out.tool_begin_name`, "get_weather")
	h.expectEval(`return out.tool_args_json`, `{"city": "Madrid"}`)
	h.expectEval(`return out.tool_end_id`, "toolu_99")
	// Usage del dialecto -> canónico.
	h.expectEval(`return tostring(out.usage_in)`, "42")
	h.expectEval(`return tostring(out.usage_out)`, "57")
	// done: stop_reason mapeado (tool_use -> tool_calls) y Message ensamblado.
	h.expectEval(`return out.stop`, "tool_calls")
	h.expectEval(`return out.role`, "assistant")
	h.expectEval(`return tostring(out.nblocks)`, "3")
	h.expectEval(`return out.b1type`, "thinking")
	h.expectEval(`return out.b1sig`, "AbC123==")
	h.expectEval(`return out.b2type`, "text")
	h.expectEval(`return out.b2text`, "# Hola\n\nEsto es **markdown** en _streaming_.\n")
	h.expectEval(`return out.b3type`, "tool_call")
	h.expectEval(`return out.b3id`, "toolu_99")
	h.expectEval(`return out.b3name`, "get_weather")
	h.expectEval(`return out.b3city`, "Madrid")

	// La secuencia de tipos de Event en orden (providers.md §2.3): el `done`
	// cierra; el `ping` no aparece; el `usage` temprano (message_start) y el
	// final (message_delta) ambos presentes.
	h.expectEval(`return out.kinds`,
		"usage,thinking,thinking,text,text,text,tool_call.begin,tool_call.delta,tool_call.delta,tool_call.end,usage,done")
}

// TestAnthropicErrorSSE blinda la traducción de un evento `error` del dialecto a
// EPROVIDER (providers.md §3 obligación 2), con `retryable` marcado para un
// overloaded_error (5xx-equivalente).
func TestAnthropicErrorSSE(t *testing.T) {
	const errSSE = `event: message_start
data: {"type":"message_start","message":{"role":"assistant","usage":{"input_tokens":5}}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"saturado"}}

`
	srv := sseServer(t, errSSE)
	defer srv.Close()
	h := bootAnthropic(t, srv.URL)

	h.eval(`
		err_code, err_retryable = nil, nil
		enu.task.spawn(function()
			local p = require("providers")
			local r = p.resolve("anthropic/opus")
			local req = { model = r.config.model.id,
				messages = { { role = "user", content = { { type = "text", text = "x" } } } } }
			local ok, e = pcall(function()
				for _ in r.adapter.stream(req, r.config) do end
			end)
			if not ok then
				err_code = e.code
				err_retryable = e.detail and e.detail.retryable
			end
		end)
	`)
	h.expectEval(`return err_code`, "EPROVIDER")
	h.expectEval(`return tostring(err_retryable)`, "true")
}

// TestAnthropicHTTPError blinda el status >= 400 (api.md §8: el status es dato,
// `stream` no lanza) convertido a EPROVIDER accionable con el provider_code y
// `retryable` del cuerpo JSON de error de Anthropic. 429 -> retryable.
func TestAnthropicHTTPError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer srv.Close()

	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\"]\n")
	toml := fmt.Sprintf("[providers.anthropic]\nadapter=\"anthropic\"\nbase_url=%q\napi_key_env=\"ANTHROPIC_API_KEY\"\n\n[[providers.anthropic.models]]\nid=\"claude-opus-4-8\"\naliases=[\"opus\"]\n", srv.URL)
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	h := bootWithToml(t, "", cfg)

	h.eval(`
		err_code, err_status, err_provider_code, err_retryable = nil, nil, nil, nil
		enu.task.spawn(function()
			local p = require("providers")
			local r = p.resolve("anthropic/opus")
			local req = { model = r.config.model.id,
				messages = { { role = "user", content = { { type = "text", text = "x" } } } } }
			local ok, e = pcall(function()
				for _ in r.adapter.stream(req, r.config) do end
			end)
			if not ok then
				err_code = e.code
				err_status = e.detail and e.detail.status
				err_provider_code = e.detail and e.detail.provider_code
				err_retryable = e.detail and e.detail.retryable
			end
		end)
	`)
	h.expectEval(`return err_code`, "EPROVIDER")
	h.expectEval(`return tostring(err_status)`, "429")
	h.expectEval(`return err_provider_code`, "rate_limit_error")
	h.expectEval(`return tostring(err_retryable)`, "true")
}

// TestAnthropicCountTokens blinda el `count_tokens?` opcional del adaptador
// (providers.md §3): suma la heurística approx_tokens sobre system + bloques de
// texto, en Lua puro (sin red).
func TestAnthropicCountTokens(t *testing.T) {
	srv := sseServer(t, recordedSSE) // no se consume; solo para resolver el provider
	defer srv.Close()
	h := bootAnthropic(t, srv.URL)

	h.eval(`
		count = nil
		enu.task.spawn(function()
			local p = require("providers")
			local r = p.resolve("anthropic/opus")
			local req = {
				model = r.config.model.id,
				system = "abcd",  -- 4 bytes -> 1 token
				messages = { { role = "user", content = { { type = "text", text = "abcd" } } } }, -- 1 token
			}
			count = r.adapter.count_tokens(req, r.config)
		end)
	`)
	h.expectEval(`return tostring(count)`, "2")
}

// TestCP9CaminoCaliente es el checkpoint CP-9 (hito de veto de perf): el camino
// caliente completo de extremo a extremo. Una vuelta de conversación → el
// adaptador `anthropic` consume el SSE grabado vía `enu.http.stream` → emite el
// stream canónico → se renderiza con `enu.text.markdown` en STREAMING (token a
// token, recomponiendo el markdown acumulado en cada delta) → se blittea a una
// región. Verifica:
//   - el Message canónico final es correcto (texto markdown ensamblado, tool
//     call con args, usage);
//   - el markdown CRECE estable: cada render parcial es un Block válido
//     (height >= 1) y el render final contiene el encabezado "Hola".
//
// Es la PRIMERA vez que HTTP stream → SSE → markdown → blit corre junto (plan
// S37 / 🔎 CP-9): valida que el camino caliente en Lua es aceptable. Reusa el
// aprendizaje de S28/ADR-012: lo pesado (parseo SSE en Go, markdown en Go, blit
// en Go) es primitiva; Lua solo orquesta el bucle de deltas.
func TestCP9CaminoCaliente(t *testing.T) {
	srv := sseServer(t, recordedSSE)
	defer srv.Close()
	h := bootAnthropic(t, srv.URL)

	h.eval(`
		cp9 = {}
		enu.task.spawn(function()
			local p = require("providers")
			local r = p.resolve("anthropic/opus")
			local req = {
				model = r.config.model.id,
				messages = { { role = "user", content = { { type = "text", text = "saluda en markdown" } } } },
				tools = { { name = "get_weather", description = "clima", schema = { type = "object" } } },
			}

			-- Región donde se blittea el markdown en vivo (camino caliente UI).
			local region = enu.ui.region({ x = 0, y = 0, w = 40, h = 20 })

			-- Bucle del camino caliente: por cada delta de texto, recomponemos el
			-- markdown acumulado, lo renderizamos con la primitiva Go (streaming-safe,
			-- api.md §10) y lo blitteamos. Lua solo orquesta; el trabajo pesado es Go.
			local acc = ""
			local renders = 0
			local all_valid = true   -- todo render parcial es un Block válido
			local heights_nondecreasing = true
			local last_h = 0
			local final_block = nil
			local done = nil

			for ev in r.adapter.stream(req, r.config) do
				if ev.type == "text" then
					acc = acc .. ev.text
					local blk = enu.text.markdown(acc, { width = 40 })
					region:blit(0, 0, blk)
					renders = renders + 1
					if blk.height < 1 then all_valid = false end
					if blk.height < last_h then heights_nondecreasing = false end
					last_h = blk.height
					final_block = blk
				elseif ev.type == "done" then
					done = ev
				end
			end

			cp9.renders = renders
			cp9.all_valid = all_valid
			cp9.heights_nondecreasing = heights_nondecreasing
			cp9.final_height = final_block and final_block.height or 0
			-- El Block es OPACO (api.md §9.2: solo .width/.height, no su contenido).
			-- Para confirmar que el render final corresponde al markdown COMPLETO
			-- acumulado, comparamos su altura con un render fresco del texto entero:
			-- si el streaming acumuló bien, ambas alturas coinciden (mismo doc).
			local fresh = enu.text.markdown(acc, { width = 40 })
			cp9.final_matches_fresh = final_block ~= nil and final_block.height == fresh.height
			-- Un encabezado markdown "# Hola" produce un Block de varias líneas
			-- (heading + cuerpo): altura > 1 confirma que el markdown se renderizó
			-- como tal y no como una sola línea de texto plano.
			cp9.final_multiline = final_block ~= nil and final_block.height > 1

			-- El Message canónico final (providers.md §2.1): texto, tool call, usage.
			cp9.text_assembled = ""
			cp9.has_tool = false
			for _, b in ipairs(done.message.content) do
				if b.type == "text" then cp9.text_assembled = cp9.text_assembled .. b.text end
				if b.type == "tool_call" then
					cp9.has_tool = true
					cp9.tool_city = b.args.city
				end
			end
			cp9.stop = done.stop_reason

			region:destroy()
		end)
	`)

	// El camino caliente produjo varios renders (uno por delta de texto: 3).
	h.expectEval(`return tostring(cp9.renders)`, "3")
	// Cada render parcial fue un Block válido y el markdown creció estable.
	h.expectEval(`return tostring(cp9.all_valid)`, "true")
	h.expectEval(`return tostring(cp9.heights_nondecreasing)`, "true")
	// El render final corresponde al markdown completo (misma altura que un
	// render fresco del texto entero) y es multilínea (encabezado + cuerpo).
	h.expectEval(`return tostring(cp9.final_matches_fresh)`, "true")
	h.expectEval(`return tostring(cp9.final_multiline)`, "true")
	// El Message canónico final es correcto.
	h.expectEval(`return cp9.text_assembled`, "# Hola\n\nEsto es **markdown** en _streaming_.\n")
	h.expectEval(`return tostring(cp9.has_tool)`, "true")
	h.expectEval(`return cp9.tool_city`, "Madrid")
	h.expectEval(`return cp9.stop`, "tool_calls")
}
