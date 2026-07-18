package runtime

// Test de ADR-016 (G34, antes P21): el adaptador `anthropic` traduce el `thinking`
// canónico POR-MODELO según el dialecto declarado en el registro
// (`thinking = "adaptive"|"budget"|"none"`, default "budget"). Reusa el servidor
// SSE que captura el cuerpo del request (genericSSEServer / minimalAnthropicSSE de
// providers_p30_test.go): se inspecciona qué forma de `thinking` se envió al wire.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bootThinking arranca `providers` con varios modelos `anthropic` de dialectos
// distintos apuntando a `baseURL`, para ejercitar la traducción por-modelo.
func bootThinking(t *testing.T, baseURL string) *harness {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\"]\n")
	toml := fmt.Sprintf(`
[providers.anthropic]
adapter     = "anthropic"
base_url    = %q
api_key_env = "ANTHROPIC_API_KEY"

[[providers.anthropic.models]]
id       = "adaptive-model"
thinking = "adaptive"
aliases  = ["adapt"]

[[providers.anthropic.models]]
id         = "budget-model"
thinking   = "budget"
max_output = 8000
aliases    = ["bud"]

[[providers.anthropic.models]]
id       = "none-model"
thinking = "none"
aliases  = ["non"]

[[providers.anthropic.models]]
id      = "default-model"
aliases = ["def"]
`, baseURL)
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	return bootWithToml(t, "", cfg, WithForceUI(true))
}

// TestAnthropicThinkingPorModelo (ADR-016): cada combinación (dialecto del modelo,
// `thinking` canónico pedido) produce la forma de wire correcta.
func TestAnthropicThinkingPorModelo(t *testing.T) {
	srv, getBody := genericSSEServer(t, minimalAnthropicSSE)
	defer srv.Close()
	h := bootThinking(t, srv.URL)

	// runThinking corre un turno-de-adaptador sobre `ref` con el `thinking` dado
	// (un literal Lua de tabla, o "nil") y devuelve el cuerpo enviado al wire.
	runThinking := func(ref, thinkingLua string) string {
		h.eval(fmt.Sprintf(`
			enu.task.spawn(function()
				local p = require("providers")
				local r = p.resolve(%q)
				local req = {
					model = r.config.model.id,
					messages = { { role = "user", content = { { type = "text", text = "x" } } } },
					thinking = %s,
				}
				for _ in r.adapter.stream(req, r.config) do end
			end)
		`, ref, thinkingLua))
		return getBody()
	}

	cases := []struct {
		name        string
		ref         string
		thinking    string
		wantContain []string
		wantAbsent  []string
	}{
		{
			// dialecto adaptive + mode adaptive → {type:"adaptive"}, sin budget_tokens.
			name: "adaptive_mode_adaptive", ref: "anthropic/adapt",
			thinking:    `{ mode = "adaptive" }`,
			wantContain: []string{`"thinking"`, `"type":"adaptive"`},
			wantAbsent:  []string{`budget_tokens`},
		},
		{
			// dialecto adaptive + budget → degrada a adaptive (Opus 4.6+ ignora la cifra).
			name: "adaptive_budget_degrada", ref: "anthropic/adapt",
			thinking:    `{ budget = 8000 }`,
			wantContain: []string{`"type":"adaptive"`},
			wantAbsent:  []string{`budget_tokens`},
		},
		{
			// dialecto budget + budget → {type:"enabled", budget_tokens:5000}.
			name: "budget_mode_budget", ref: "anthropic/bud",
			thinking:    `{ mode = "budget", budget = 5000 }`,
			wantContain: []string{`"type":"enabled"`, `"budget_tokens":5000`},
		},
		{
			// dialecto budget + mode adaptive → degrada a la forma legacy con default.
			name: "budget_mode_adaptive_degrada", ref: "anthropic/bud",
			thinking:    `{ mode = "adaptive" }`,
			wantContain: []string{`"type":"enabled"`, `"budget_tokens"`},
			wantAbsent:  []string{`"type":"adaptive"`},
		},
		{
			// dialecto none → no se envía thinking (degradación declarada).
			name: "none_no_envia", ref: "anthropic/non",
			thinking:   `{ mode = "adaptive" }`,
			wantAbsent: []string{`"thinking"`},
		},
		{
			// modelo sin dialecto declarado → default "budget"; compat {budget=N}.
			name: "default_compat_budget", ref: "anthropic/def",
			thinking:    `{ budget = 3000 }`,
			wantContain: []string{`"type":"enabled"`, `"budget_tokens":3000`},
		},
		{
			// off explícito → no se envía thinking.
			name: "off_no_envia", ref: "anthropic/adapt",
			thinking:   `{ mode = "off", budget = 9000 }`,
			wantAbsent: []string{`"thinking"`},
		},
		{
			// sin thinking → no se envía.
			name: "ausente_no_envia", ref: "anthropic/bud",
			thinking:   `nil`,
			wantAbsent: []string{`"thinking"`},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := runThinking(c.ref, c.thinking)
			for _, w := range c.wantContain {
				if !strings.Contains(body, w) {
					t.Errorf("falta %q en el cuerpo\n%s", w, body)
				}
			}
			for _, a := range c.wantAbsent {
				if strings.Contains(body, a) {
					t.Errorf("no debía aparecer %q en el cuerpo\n%s", a, body)
				}
			}
		})
	}
}
