package e2e

// El provider FAKE: un servidor `httptest` que habla el dialecto de la Messages API
// de Anthropic (POST /v1/messages + SSE del dialecto), de forma que el adaptador
// `anthropic` REAL del plugin `providers` (embedded/providers/lua/.../adapter_anthropic.lua)
// funcione contra él sin tocar la red de verdad —el `base_url` del providers.toml
// apunta aquí (ver Workspace.UseFakeProvider)—.
//
// Las respuestas son PROGRAMABLES: cada POST consume la siguiente de la cola (Push*),
// así que un turno con tool_use se modela encolando la respuesta de la tool y luego la
// del texto. Sin nada encolado, sirve un texto trivial ("listo") para que un turno
// mínimo funcione sin ceremonia. `RecordedSSE` es el SSE grabado y realista copiado de
// providers_anthropic_test.go (e2e no puede importar los `_test.go` de otro paquete).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	// FakeAPIKeyEnv es la variable de entorno donde vive la API key que el providers.toml
	// del fake nombra (`api_key_env`). El arnés la fija en el entorno del proceso.
	FakeAPIKeyEnv = "ANTHROPIC_API_KEY"
	// FakeAPIKey es un valor cualquiera: el fake no lo valida, solo comprueba que la
	// cabecera x-api-key viaja (como el sseServer de S37).
	FakeAPIKey = "sk-e2e-fake"
)

// FakeProvider es un servidor httptest programable que imita la Messages API de
// Anthropic. Créalo con NewFakeProvider, encola respuestas con Push/PushText/PushToolUse,
// y cablea el workspace con Workspace.UseFakeProvider(t, fp).
type FakeProvider struct {
	srv *httptest.Server

	mu       sync.Mutex
	queue    []string         // cuerpos SSE pendientes, consumidos en orden (FIFO)
	requests []map[string]any // cuerpos de request decodificados, en orden de llegada
	headers  []http.Header    // cabeceras de cada request (para aserciones de auth)
}

// NewFakeProvider levanta el servidor y lo registra para cierre al terminar el test.
func NewFakeProvider(t *testing.T) *FakeProvider {
	t.Helper()
	f := &FakeProvider{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// URL es el base_url que el providers.toml debe usar (sin la ruta /v1/messages, que la
// añade el adaptador).
func (f *FakeProvider) URL() string { return f.srv.URL }

// handle atiende cada POST: registra el request, consume la siguiente respuesta de la
// cola (o un texto trivial si está vacía) y la escribe como SSE con flush por línea
// (llegada incremental, camino caliente real).
func (f *FakeProvider) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)

	f.mu.Lock()
	f.requests = append(f.requests, decoded)
	f.headers = append(f.headers, r.Header.Clone())
	var sse string
	if len(f.queue) > 0 {
		sse = f.queue[0]
		f.queue = f.queue[1:]
	} else {
		sse = buildTextSSE("listo")
	}
	f.mu.Unlock()

	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "sin Flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	for _, line := range strings.SplitAfter(sse, "\n") {
		if line == "" {
			continue
		}
		_, _ = io.WriteString(w, line)
		fl.Flush()
	}
}

// Push encola un cuerpo SSE CRUDO (dialecto Anthropic). Úsalo con RecordedSSE o con un
// SSE que construyas a mano.
func (f *FakeProvider) Push(sse string) {
	f.mu.Lock()
	f.queue = append(f.queue, sse)
	f.mu.Unlock()
}

// PushText encola un turno que emite `text` y para con stop_reason end_turn (una
// respuesta de texto normal del asistente).
func (f *FakeProvider) PushText(text string) { f.Push(buildTextSSE(text)) }

// PushToolUse encola un turno que pide UNA tool call (`name` con `input`) y para con
// stop_reason tool_use. El loop del agente ejecutará la tool y volverá a pedir: encola
// después la respuesta del siguiente turno (p. ej. PushText). `input` se serializa a
// JSON (usa map[string]any o una struct).
func (f *FakeProvider) PushToolUse(id, name string, input any) {
	f.Push(buildToolUseSSE(id, name, input))
}

// Requests devuelve una copia de los cuerpos de request decodificados (JSON de la
// Messages API: `model`, `messages`, `system`, `tools`, `max_tokens`…), en orden. Sirve
// para afirmar qué envió el adaptador (p. ej. que el tool_result volvió en el 2º turno).
func (f *FakeProvider) Requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.requests))
	copy(out, f.requests)
	return out
}

// RequestCount es el número de POST recibidos (turnos que el adaptador disparó).
func (f *FakeProvider) RequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// Header devuelve las cabeceras del request i-ésimo (0-based), o nil si no existe.
func (f *FakeProvider) Header(i int) http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.headers) {
		return nil
	}
	return f.headers[i]
}

// --- Constructores de SSE del dialecto Anthropic ---------------------------------

// sseEvent formatea un evento SSE (`event: <tipo>\ndata: <json>\n\n`) serializando
// `data` con json.Marshal (escapa comillas/saltos correctamente).
func sseEvent(event string, data any) string {
	b, err := json.Marshal(data)
	if err != nil {
		// datos de test siempre serializables; si no, es un bug del test.
		panic("e2e: sseEvent no serializa: " + err.Error())
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(b))
}

// buildTextSSE arma el SSE de un turno de TEXTO: message_start (usage de entrada),
// un bloque de texto con el `text` en un solo delta, message_delta con end_turn, y
// message_stop. Es el turno "normal" del asistente.
func buildTextSSE(text string) string {
	var b strings.Builder
	b.WriteString(sseEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_e2e", "role": "assistant", "model": "claude-e2e",
			"usage": map[string]any{"input_tokens": 5, "output_tokens": 0},
		},
	}))
	b.WriteString(sseEvent("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": text},
	}))
	b.WriteString(sseEvent("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": 0,
	}))
	b.WriteString(sseEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": map[string]any{"output_tokens": 3},
	}))
	b.WriteString(sseEvent("message_stop", map[string]any{"type": "message_stop"}))
	return b.String()
}

// buildToolUseSSE arma el SSE de un turno con UNA tool call: un bloque tool_use cuyo
// input JSON llega en un único `input_json_delta`, y message_delta con stop_reason
// tool_use. El agente ejecutará la tool y disparará el siguiente turno.
func buildToolUseSSE(id, name string, input any) string {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		panic("e2e: buildToolUseSSE no serializa input: " + err.Error())
	}
	var b strings.Builder
	b.WriteString(sseEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_e2e", "role": "assistant", "model": "claude-e2e",
			"usage": map[string]any{"input_tokens": 5, "output_tokens": 0},
		},
	}))
	b.WriteString(sseEvent("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}},
	}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(inputJSON)},
	}))
	b.WriteString(sseEvent("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": 0,
	}))
	b.WriteString(sseEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "tool_use"},
		"usage": map[string]any{"output_tokens": 3},
	}))
	b.WriteString(sseEvent("message_stop", map[string]any{"type": "message_stop"}))
	return b.String()
}

// RecordedSSE es un SSE de Anthropic GRABADO y realista (thinking + texto markdown en
// streaming + una tool call con input troceado), COPIADO literal de
// internal/runtime/providers_anthropic_test.go (`recordedSSE`). e2e no puede importar
// los `_test.go` de otro paquete, así que se duplica aquí como fixture base: encólalo
// con `fp.Push(RecordedSSE)` para ejercitar el camino caliente completo del adaptador.
const RecordedSSE = `event: message_start
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
