package runtime

// Tests de la extensión oficial `providers` (S36, embebida en
// internal/runtime/embedded/providers). Es Lua sobre la API pública congelada
// (Fase 8, ADR-003: el core NO sabe lo que es un provider), así que la prueba es
// Go que arranca un Runtime con la extensión ACTIVADA por `enu.toml`
// (`plugins.enabled = ["providers"]`, igual que el gating de S12) y ejercita el
// contrato desde Lua, requiriendo el módulo con `require("providers")`.
//
// Blinda el contrato de [providers.md](../../docs/contracts/providers.md):
//
//   - **registro TOML (§1)**: un `providers.toml` de prueba se carga; `list()` y
//     `resolve("proveedor/alias")` devuelven los modelos/configs correctos; la
//     api_key sale del ENTORNO (`api_key_env`), nunca del fichero;
//   - **contrato del adaptador (§3)**: el adaptador STUB responde a una petición
//     SIMULADA emitiendo el stream canónico de Events (providers.md §2.3),
//     cerrado por un `done` con el Message ensamblado; la degradación declarada
//     (tools no soportadas → EINVAL) se respeta;
//   - **approx_tokens (§4, G23)**: heurística ~4 bytes/token sobre cadenas
//     conocidas, en Lua puro.
//
// Es el "Criterio de hecho" de S36: "El registro TOML se carga; un adaptador stub
// responde a una petición simulada".

import (
	"os"
	"path/filepath"
	"testing"
)

// bootProviders arranca un Runtime con la extensión `providers` activada y un
// `providers.toml` (puede ser "") escrito en su config.dir. Devuelve el harness
// ya booteado. Reusa el andamiaje de activación de S12 (writeNuToml, bootWithToml).
func bootProviders(t *testing.T, providersToml string) *harness {
	t.Helper()
	cfg := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\"]\n")
	if providersToml != "" {
		if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersToml), 0o644); err != nil {
			t.Fatalf("write providers.toml: %v", err)
		}
	}
	return bootWithToml(t, "", cfg)
}

// inTask envuelve un cuerpo Lua en una task que captura su resultado en la global
// `out` y su error estructurado en `err_code` (las funciones del registro —list,
// resolve— suspenden porque leen `providers.toml` con `enu.fs.read` ⏸, api.md §5,
// así que SOLO corren dentro de una task; es justamente cómo las llama el agente).
// Tras `h.eval(inTask(...))`, el siguiente `eval` lee las globales (la task ya
// progresó al soltar el token).
func inTask(body string) string {
	return `
		out, err_code = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
` + body + `
			end)
			if not ok then err_code = (type(e) == "table" and e.code) or tostring(e) end
		end)`
}

// providersToml de prueba: un provider con adaptador stub y dos modelos (uno con
// alias), más un provider local sin api_key_env (Ollama-style). Cubre los campos
// que el lector del registro consume (providers.md §1).
const sampleProvidersToml = `
[providers.testco]
adapter     = "stub"
base_url    = "https://api.testco.example"
api_key_env = "TESTCO_API_KEY"

[[providers.testco.models]]
id         = "big-1"
context    = 200000
max_output = 32000
cost       = { input = 5.0, output = 25.0 }
aliases    = ["big"]

[[providers.testco.models]]
id      = "small-1"
context = 32768

[providers.local]
adapter  = "stub"
base_url = "http://localhost:11434/v1"

[[providers.local.models]]
id = "qwen3:32b"
`

// TestProvidersCargaYActiva: la extensión `providers` carga sin error (con
// source="builtin") y su módulo se requiere. Confirma core:ready y el criterio
// "la extensión carga sin error".
func TestProvidersCargaYActiva(t *testing.T) {
	h := bootProviders(t, sampleProvidersToml)

	if src := listSource(h, "providers"); src != "builtin" {
		t.Fatalf(`providers debía cargarse con source="builtin"; got %q`, src)
	}
	// El módulo público se requiere y expone la superficie del contrato (§4).
	h.expectEval(`
		local p = require("providers")
		assert(type(p.approx_tokens) == "function", "approx_tokens")
		assert(type(p.resolve) == "function", "resolve")
		assert(type(p.list) == "function", "list")
		assert(type(p.register_adapter) == "function", "register_adapter")
		return "ok"`, "ok")
}

// TestProvidersApproxTokens blinda la heurística ~4 bytes/token (providers.md §4,
// G23) sobre cadenas conocidas. La cadena vacía es 0; el resto es ceil(bytes/4).
func TestProvidersApproxTokens(t *testing.T) {
	h := bootProviders(t, "")

	cases := []struct {
		in   string
		want string
	}{
		{"", "0"},
		{"a", "1"},        // ceil(1/4) = 1
		{"abcd", "1"},     // ceil(4/4) = 1
		{"abcde", "2"},    // ceil(5/4) = 2
		{"abcdefgh", "2"}, // ceil(8/4) = 2
		{"123456789", "3"},
	}
	for _, c := range cases {
		got := h.eval(`return tostring(require("providers").approx_tokens("` + c.in + `"))`)[0]
		if got != c.want {
			t.Errorf("approx_tokens(%q): got %q, want %q", c.in, got, c.want)
		}
	}

	// Bytes, no caracteres: un carácter multibyte (é = 2 bytes en UTF-8) cuenta
	// por sus bytes. "éé" = 4 bytes -> 1 token.
	h.expectEval(`return tostring(require("providers").approx_tokens("éé"))`, "1")

	// No-cadena -> EINVAL (estructurado).
	se := h.evalErr(`require("providers").approx_tokens(123)`)
	if se.Code != CodeEINVAL {
		t.Fatalf("approx_tokens(no-cadena): code %q, want EINVAL", se.Code)
	}
}

// TestProvidersRegistroTOML blinda el lector del registro (providers.md §1):
// list() enumera los modelos declarados y resolve() empareja "proveedor/id" y
// "proveedor/alias" con su adaptador y una ProviderConfig cocinada (base_url,
// api_key del entorno, model). Es la mitad "el registro TOML se carga" del
// criterio de hecho.
func TestProvidersRegistroTOML(t *testing.T) {
	t.Setenv("TESTCO_API_KEY", "secreto-del-entorno")
	h := bootProviders(t, sampleProvidersToml)

	// list(): los tres modelos declarados, con sus refs canónicas.
	h.eval(inTask(`
		local refs = {}
		for _, m in ipairs(require("providers").list()) do refs[m.ref] = true end
		assert(refs["testco/big-1"], "falta testco/big-1")
		assert(refs["testco/small-1"], "falta testco/small-1")
		assert(refs["local/qwen3:32b"], "falta local/qwen3:32b")
		local n = 0; for _ in pairs(refs) do n = n + 1 end
		out = tostring(n)`))
	h.expectEval(`return tostring(out)`, "3")

	// resolve por id canónico: adaptador correcto + config cocinada con la
	// api_key del ENTORNO (providers.md §1: nunca del fichero).
	h.eval(inTask(`
		local r = require("providers").resolve("testco/big-1")
		assert(r.adapter.name == "stub", "adaptador stub")
		assert(r.config.base_url == "https://api.testco.example", "base_url")
		assert(r.config.api_key == "secreto-del-entorno", "api_key del entorno")
		assert(r.config.model.id == "big-1", "model.id")
		assert(r.config.model.context == 200000, "model.context")
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")

	// resolve por ALIAS: "testco/big" apunta al mismo modelo que "testco/big-1".
	h.eval(inTask(`out = require("providers").resolve("testco/big").config.model.id`))
	h.expectEval(`return tostring(out)`, "big-1")

	// Provider sin api_key_env (Ollama local): config sin api_key, sin error.
	h.eval(inTask(`
		local r = require("providers").resolve("local/qwen3:32b")
		assert(r.config.api_key == nil, "sin api_key")
		assert(r.config.base_url == "http://localhost:11434/v1", "base_url local")
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")

	// Modelo inexistente -> EPROVIDER accionable que nombra la ref.
	h.eval(inTask(`require("providers").resolve("testco/fantasma")`))
	h.expectEval(`return tostring(err_code)`, "EPROVIDER")
}

// TestProvidersTOMLAusente: sin `providers.toml`, el registro está vacío pero no
// es error (un enu sin modelos configurados arranca limpio). list() devuelve [].
func TestProvidersTOMLAusente(t *testing.T) {
	h := bootProviders(t, "")
	h.eval(inTask(`out = tostring(#require("providers").list())`))
	h.expectEval(`return tostring(out)`, "0")
}

// TestProvidersTOMLMalFormado: un `providers.toml` ilegible es error accionable
// (EPROVIDER) que nombra el fichero, lanzado al primer list()/resolve().
func TestProvidersTOMLMalFormado(t *testing.T) {
	h := bootProviders(t, "esto no es [[[ toml válido")
	h.eval(inTask(`require("providers").list()`))
	h.expectEval(`return tostring(err_code)`, "EPROVIDER")
}

// TestProvidersTOMLSinAdapter: un provider sin `adapter` es registro inválido
// accionable (EPROVIDER) que nombra el provider (providers.md §1).
func TestProvidersTOMLSinAdapter(t *testing.T) {
	toml := `
[providers.roto]
base_url = "https://x"
[[providers.roto.models]]
id = "m1"
`
	h := bootProviders(t, toml)
	h.eval(inTask(`require("providers").list()`))
	h.expectEval(`return tostring(err_code)`, "EPROVIDER")
}

// TestProvidersTOMLSinBaseURL: un provider con `adapter` válido pero sin
// `base_url` es registro inválido accionable (EPROVIDER) que nombra el provider
// y el campo del TOML a rellenar (providers.md §1). Sin esta validación, el
// `base_url` nil se propagaría a la ProviderConfig y reventaría con "attempt to
// concatenate a nil value" en el primer turno del adaptador (A-15).
func TestProvidersTOMLSinBaseURL(t *testing.T) {
	toml := `
[providers.roto]
adapter = "stub"
[[providers.roto.models]]
id = "m1"
`
	h := bootProviders(t, toml)
	h.eval(inTask(`
		local ok, err = pcall(function() require("providers").list() end)
		err_ok = ok
		err_code = err and err.code
		err_msg = err and err.message
	`))
	h.expectEval(`return tostring(err_ok)`, "false")
	h.expectEval(`return tostring(err_code)`, "EPROVIDER")
	h.expectEval(`return tostring(string.find(err_msg, "base_url", 1, true) ~= nil)`, "true")
}

// TestProvidersAdaptadorStub blinda el contrato del adaptador (providers.md §3)
// contra una petición SIMULADA: el stub emite el stream canónico de Events
// (§2.3) terminado por un `done` con el Message ensamblado. Es la mitad "un
// adaptador stub responde a una petición simulada" del criterio de hecho.
func TestProvidersAdaptadorStub(t *testing.T) {
	t.Setenv("TESTCO_API_KEY", "k")
	h := bootProviders(t, sampleProvidersToml)

	// Petición canónica (providers.md §2.1) con un mensaje de usuario. Se resuelve
	// el modelo, se corre el adaptador y se recoge el stream de Events. Corre en
	// una task porque `stream` es ⏸ (aunque el stub no suspende, respetamos la
	// firma del contrato).
	if err := h.rt.Boot(); err != nil {
		// Ya booteado por bootProviders; Boot es idempotente.
		t.Fatalf("Boot: %v", err)
	}
	h.eval(`
		out = {}
		enu.task.spawn(function()
			local p = require("providers")
			local r = p.resolve("testco/big")
			local req = {
				model = r.config.model.id,
				system = "eres útil",
				messages = {
					{ role = "user", content = { { type = "text", text = "hola" } } },
				},
			}
			local kinds, last_done = {}, nil
			for ev in r.adapter.stream(req, r.config) do
				kinds[#kinds+1] = ev.type
				if ev.type == "done" then last_done = ev end
			end
			out.kinds = table.concat(kinds, ",")
			out.text = last_done and last_done.message.content[1].text
			out.role = last_done and last_done.message.role
			out.stop = last_done and last_done.stop_reason
		end)
	`)

	// El stream emitió text, text, usage, done en orden y cerró con el Message
	// canónico ensamblado (providers.md §2.3).
	h.expectEval(`return out.kinds`, "text,text,usage,done")
	h.expectEval(`return out.text`, "eco: hola")
	h.expectEval(`return out.role`, "assistant")
	h.expectEval(`return out.stop`, "end")
}

// TestProvidersStubDegradacionDeclarada blinda la obligación 5 de §3: si el
// adaptador declara caps.tools=false y el request trae tools, lanza EINVAL —no
// simula en silencio—.
func TestProvidersStubDegradacionDeclarada(t *testing.T) {
	t.Setenv("TESTCO_API_KEY", "k")
	h := bootProviders(t, sampleProvidersToml)
	if err := h.rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h.eval(`
		err_code = nil
		enu.task.spawn(function()
			local p = require("providers")
			local r = p.resolve("testco/big")
			local req = {
				model = r.config.model.id,
				messages = { { role = "user", content = { { type = "text", text = "x" } } } },
				tools = { { name = "t", description = "d", schema = {} } },
			}
			local ok, err = pcall(function()
				for _ in r.adapter.stream(req, r.config) do end
			end)
			err_code = (not ok) and err.code or "NO-ERROR"
		end)
	`)
	h.expectEval(`return tostring(err_code)`, "EINVAL")
}

// TestProvidersStubCountTokens blinda el `count_tokens?` opcional del adaptador
// (providers.md §3): suma la heurística sobre system + bloques de texto.
func TestProvidersStubCountTokens(t *testing.T) {
	t.Setenv("TESTCO_API_KEY", "k")
	h := bootProviders(t, sampleProvidersToml)
	if err := h.rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h.eval(`
		count = nil
		enu.task.spawn(function()
			local p = require("providers")
			local r = p.resolve("testco/big")
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

// TestProvidersRegisterAdapterValida blinda la validación de forma del contrato
// (providers.md §3) al registrar: un adaptador sin `stream` es EINVAL accionable.
func TestProvidersRegisterAdapterValida(t *testing.T) {
	h := bootProviders(t, "")
	se := h.evalErr(`
		require("providers").register_adapter("malo", { name = "malo", caps = {} })`)
	if se.Code != CodeEINVAL {
		t.Fatalf("register_adapter(sin stream): code %q, want EINVAL", se.Code)
	}

	// Un adaptador bien formado se registra y queda resoluble (sustituye al stub
	// para un provider que lo nombre). Aquí solo comprobamos que registrar no falla.
	h.expectEval(`
		require("providers").register_adapter("bueno", {
			name = "bueno", caps = { tools = true },
			stream = function(req, provider) return function() return nil end end,
		})
		return "ok"`, "ok")
}

// TestProvidersSecretEnvVars blinda `secret_env_vars()` (providers.md §4, G55):
// los NOMBRES —nunca los valores— de las `api_key_env` del registro,
// deduplicados y en orden alfabético. Es la pieza de la que depende TODO el
// recorte de secretos de agente.md §3: si esto lista mal (falta un nombre, o
// devuelve un valor en vez de un nombre), el recorte de la tool `bash` no
// protege nada.
func TestProvidersSecretEnvVars(t *testing.T) {
	// sampleProvidersToml declara `api_key_env = "TESTCO_API_KEY"` para
	// `testco` y ningún `api_key_env` para `local` (Ollama-style): la lista
	// debe traer solo el primero, una vez.
	h := bootProviders(t, sampleProvidersToml)
	h.eval(inTask(`
		local vars = require("providers").secret_env_vars()
		out = table.concat(vars, ",")
	`))
	h.expectEval(`return tostring(err_code)`, "nil")
	h.expectEval(`return tostring(out)`, "TESTCO_API_KEY")

	// Nunca el VALOR, solo el nombre: aunque la variable esté exportada en el
	// entorno del proceso `enu`, secret_env_vars() no la lee ni la expone.
	t.Setenv("TESTCO_API_KEY", "secreto-no-debe-aparecer")
	h.eval(inTask(`
		local vars = require("providers").secret_env_vars()
		out = table.concat(vars, ",")
	`))
	h.expectEval(`return tostring(out)`, "TESTCO_API_KEY")

	// providers.toml ausente -> lista vacía (no error): un enu recién arrancado
	// sin providers configurados no tiene nada que recortar.
	h2 := bootProviders(t, "")
	h2.eval(inTask(`out = tostring(#require("providers").secret_env_vars())`))
	h2.expectEval(`return tostring(out)`, "0")

	// Deduplicación: dos providers que comparten el MISMO api_key_env (caso
	// real: dos endpoints del mismo proveedor, p. ej. staging/prod) aparecen
	// una sola vez.
	tomlDup := `
[providers.a]
adapter     = "stub"
base_url    = "https://a.example"
api_key_env = "SHARED_KEY"
[[providers.a.models]]
id = "m1"

[providers.b]
adapter     = "stub"
base_url    = "https://b.example"
api_key_env = "SHARED_KEY"
[[providers.b.models]]
id = "m1"
`
	h3 := bootProviders(t, tomlDup)
	h3.eval(inTask(`
		local vars = require("providers").secret_env_vars()
		out = table.concat(vars, ",")
	`))
	h3.expectEval(`return tostring(out)`, "SHARED_KEY")

	// Orden alfabético DETERMINISTA con DOS claves distintas: el registro se lee
	// con `pairs` (sin orden estable), así que `secret_env_vars()` ordena con
	// `table.sort` (providers/init.lua). Se declaran en orden NO alfabético
	// (`ZKEY` antes que `AKEY`) para que solo el `table.sort` pueda producir la
	// salida esperada: sin él, el orden dependería del hash de `pairs` y este
	// aserto caería (o sería flaky). Blinda el sort, no solo la deduplicación.
	tomlOrden := `
[providers.z]
adapter     = "stub"
base_url    = "https://z.example"
api_key_env = "ZKEY"
[[providers.z.models]]
id = "m1"

[providers.a]
adapter     = "stub"
base_url    = "https://a.example"
api_key_env = "AKEY"
[[providers.a.models]]
id = "m1"
`
	h4 := bootProviders(t, tomlOrden)
	h4.eval(inTask(`
		local vars = require("providers").secret_env_vars()
		out = table.concat(vars, ",")
	`))
	h4.expectEval(`return tostring(err_code)`, "nil")
	h4.expectEval(`return tostring(out)`, "AKEY,ZKEY")
}
