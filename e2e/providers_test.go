package e2e

// Tests e2e del plugin oficial `providers` contra el BINARIO real (`enu -e`): la
// resolución del registro TOML + entorno REALES (no simulables in-process sin
// repetir todo el arnés de os/exec), y el adaptador `anthropic` REAL hablando por
// TCP loopback con el FakeProvider. Cubren exclusivamente lo que solo se ve
// cruzando el límite de proceso — carga y parseo de providers.toml, resolve() por
// id/alias y la traducción exhaustiva de eventos del adaptador ya están cubiertos
// in-process (internal/runtime/providers_test.go, providers_anthropic_test.go) y
// deliberadamente NO se repiten aquí.
//
// Nota técnica (eval.go): `enu -e` respalda EvalString, que corre el chunk en el
// ESTADO PRINCIPAL, no como task — no puede usar funciones ⏸ directamente. En la
// práctica esto alcanza a TODO el módulo `providers`, no solo a `stream`:
// `resolve`/`list` cuelgan de `load_registry`, que llama a `enu.fs.read` (⏸), así
// que cualquier llamada al plugin desde `-e` exige `enu.task.spawn` (comprobado
// corriendo el binario a mano: sin task, revienta con `EINVAL: esta primitiva sólo
// puede llamarse dentro de una task`). Además RunTasks drena las tasks lanzadas por
// el chunk DESPUÉS de que su `return` ya capturó los valores, así que una task no
// puede devolver su resultado por el `return` del chunk de nivel superior — y un
// error NO capturado dentro de una task tampoco aborta el proceso ni se asoma a
// stderr/exit code (se aísla por task, ADR-008, y solo queda logueado en
// `enu.log`). Por eso todos los escenarios de este fichero lanzan la task con
// `enu.task.spawn` y dejan su resultado (éxito o error, vía `pcall` cuando aplica)
// en un fichero (`enu.fs.write`, ⏸ y por tanto lícito dentro de la task) en vez de
// en el valor de retorno del chunk o en stderr: no es una grieta de la API —
// eval.go ya avisa de la restricción de ⏸—, es el patrón real para observar el
// resultado de un plugin headless desde fuera del proceso.
//
// Prefijo TestProviders* para filtrarlos con `-run TestProviders`.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProvidersE2EResolveViaEval: `enu -e` resuelve una ref contra el
// providers.toml y el entorno REALES del proceso hijo (ConfigDir en disco,
// ANTHROPIC_API_KEY exportada de verdad) sin disparar ningún tráfico de red: resolve
// es puramente local (providers.md §4).
//
// Recorte respecto al guion original: `resolve`/`list` llaman a `load_registry`,
// que hace `enu.fs.read` — ⏸ — así que NINGUNA llamada al módulo `providers` es
// invocable en el estado principal del chunk de `-e` (comprobado: revienta con
// `EINVAL: esta primitiva sólo puede llamarse dentro de una task`, no es un fallo
// del arnés). Por eso, igual que el escenario de streaming, la resolución corre
// dentro de una task y dejamos el resultado en disco.
func TestProvidersE2EResolveViaEval(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	ws.WriteEnuToml(t, "providers")
	ws.UseFakeProvider(t, fp) // providers.toml (anthropic -> fp.URL(), alias opus) + agent.toml

	script := `
enu.task.spawn(function()
  local r = require("providers").resolve("anthropic/opus")
  local out = table.concat({
    r.adapter.name, r.config.base_url, r.config.model.id, tostring(r.config.api_key ~= nil),
  }, "\n")
  enu.fs.write("resolve-out.txt", out)
end)`
	res := ws.Run(t, RunOpts{Args: []string{"-e", script}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}

	out, err := os.ReadFile(filepath.Join(ws.Workdir, "resolve-out.txt"))
	if err != nil {
		t.Fatalf("leer resolve-out.txt: %v", err)
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("resolve-out.txt debía traer 4 líneas; got %q", string(out))
	}
	if lines[0] != "anthropic" {
		t.Errorf("adapter.name: got %q, want %q", lines[0], "anthropic")
	}
	if lines[1] != fp.URL() {
		t.Errorf("config.base_url: got %q, want %q", lines[1], fp.URL())
	}
	if lines[2] != "claude-e2e" {
		t.Errorf("config.model.id: got %q, want %q", lines[2], "claude-e2e")
	}
	if lines[3] != "true" {
		t.Errorf("config.api_key ~= nil: got %q, want %q", lines[3], "true")
	}
	if fp.RequestCount() != 0 {
		t.Fatalf("resolve no debía disparar HTTP; got %d requests", fp.RequestCount())
	}
}

// TestProvidersE2EStreamCortoViaEval: el foco central del ticket. Una task lanzada
// desde `-e` resuelve el modelo, abre el stream REAL del adaptador `anthropic`
// contra el FakeProvider (TCP loopback, SSE real) y vuelca el texto acumulado a un
// fichero (el side-channel que exige la restricción ⏸ de eval.go, ver nota arriba).
// Verifica que el binario real hizo el POST, que la cabecera de auth viajó desde el
// entorno real del proceso, y que el modelo canónico se tradujo al wire real.
func TestProvidersE2EStreamCortoViaEval(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	fp.PushText("hola stream corto e2e")
	ws.WriteEnuToml(t, "providers")
	ws.UseFakeProvider(t, fp)

	script := `
enu.task.spawn(function()
  local p = require("providers")
  local r = p.resolve("anthropic/opus")
  local req = { model = r.config.model.id,
    messages = { { role = "user", content = { { type = "text", text = "hola" } } } } }
  local ok, res = pcall(function()
    local text = ""
    for ev in r.adapter.stream(req, r.config) do
      if ev.type == "text" then text = text .. ev.text end
    end
    return text
  end)
  enu.fs.write("stream-out.txt", ok and res or ("ERR:" .. tostring(res and res.code)))
end)`
	res := ws.Run(t, RunOpts{Args: []string{"-e", script}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}

	out, err := os.ReadFile(filepath.Join(ws.Workdir, "stream-out.txt"))
	if err != nil {
		t.Fatalf("leer stream-out.txt: %v", err)
	}
	if got := string(out); got != "hola stream corto e2e" {
		t.Fatalf("stream-out.txt: got %q, want %q", got, "hola stream corto e2e")
	}

	if fp.RequestCount() != 1 {
		t.Fatalf("el stream debía disparar exactamente 1 POST; got %d", fp.RequestCount())
	}
	if h := fp.Header(0); h == nil || h.Get("x-api-key") != FakeAPIKey {
		t.Fatalf("la cabecera x-api-key debía viajar con el valor real del entorno; got %q", fp.Header(0).Get("x-api-key"))
	}
	reqs := fp.Requests()
	if len(reqs) != 1 || reqs[0]["model"] != "claude-e2e" {
		t.Fatalf("el request debía traducir el modelo canónico al wire; got %#v", reqs)
	}
}

// TestProvidersE2EResolveInexistenteEPROVIDER: resolver una ref que no existe en
// el registro lanza un EPROVIDER accionable que cita la ref (providers.md §1: "el
// error debe decirle al usuario qué línea arreglar").
//
// Recorte respecto al guion original: el guion asumía que un `resolve()` fallido
// SIN pcall, llamado directo en el chunk de `-e`, propaga como StructuredError
// hasta el proceso (exit 1, EPROVIDER en stderr). Dos hechos reales del binario lo
// impiden: (1) `resolve` exige una task (ver nota de TestProvidersE2EResolveViaEval),
// y (2) un error NO capturado dentro de una task NO aborta el proceso ni se asoma a
// stderr — el runtime lo aísla por task (ADR-008) y lo registra en el log
// (`enu.log`: "una task terminó con error y nadie hizo await: EPROVIDER: ..."),
// comprobado corriendo el binario a mano. Así que, para observar el EPROVIDER desde
// fuera del proceso, lo capturamos con `pcall` DENTRO de la task (lícito: seguimos
// en contexto de task) y volcamos `code`/`message` al side-channel de disco.
func TestProvidersE2EResolveInexistenteEPROVIDER(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	ws.WriteEnuToml(t, "providers")
	ws.UseFakeProvider(t, fp)

	script := `
enu.task.spawn(function()
  local ok, err = pcall(function() return require("providers").resolve("noexiste/modelo-fantasma") end)
  if ok then
    enu.fs.write("resolve-err.txt", "OK")
  else
    local code = (type(err) == "table" and err.code) or "?"
    local message = (type(err) == "table" and err.message) or tostring(err)
    enu.fs.write("resolve-err.txt", code .. "\n" .. message)
  end
end)`
	res := ws.Run(t, RunOpts{Args: []string{"-e", script}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}

	out, err := os.ReadFile(filepath.Join(ws.Workdir, "resolve-err.txt"))
	if err != nil {
		t.Fatalf("leer resolve-err.txt: %v", err)
	}
	lines := strings.SplitN(string(out), "\n", 2)
	if lines[0] != "EPROVIDER" {
		t.Fatalf("code: got %q, want %q (contenido completo=%q)", lines[0], "EPROVIDER", string(out))
	}
	if len(lines) < 2 || !strings.Contains(lines[1], "noexiste") {
		t.Fatalf("message debía citar la ref inexistente; got %q", string(out))
	}
	if strings.Contains(string(out), "stack traceback") {
		t.Fatalf("el error no debía traer un traceback interno de Lua; got %q", string(out))
	}
	if fp.RequestCount() != 0 {
		t.Fatalf("un resolve fallido no debía disparar HTTP; got %d requests", fp.RequestCount())
	}
}

// TestProvidersE2ESinApiKeyEnvCabeceraAusente: cuando `api_key_env` nombra una
// variable que el proceso hijo NUNCA tiene exportada, resolve NO falla (providers.md
// §1: la ausencia de api_key_env es válida, "el adaptador decide") y el request real
// que sale por el wire no lleva `x-api-key`. Solo observable end-to-end: lo que se
// verifica es la ausencia de la variable en el ENTORNO REAL del proceso hijo, no en
// el test de Go.
func TestProvidersE2ESinApiKeyEnvCabeceraAusente(t *testing.T) {
	ws := NewWorkspace(t)
	fp := NewFakeProvider(t)
	fp.PushText("sin auth e2e")
	ws.WriteEnuToml(t, "providers")

	toml := "" +
		"[providers.anthropic]\n" +
		"adapter     = \"anthropic\"\n" +
		"base_url    = \"" + fp.URL() + "\"\n" +
		"api_key_env = \"ANTHROPIC_API_KEY_NO_EXISTE\"\n\n" +
		"[[providers.anthropic.models]]\n" +
		"id         = \"claude-e2e\"\n" +
		"context    = 200000\n" +
		"max_output = 4096\n" +
		"aliases    = [\"opus\"]\n"
	ws.WriteConfig(t, "providers.toml", toml)
	ws.WriteAgentToml(t, "anthropic/opus")

	script := `
enu.task.spawn(function()
  local p = require("providers")
  local r = p.resolve("anthropic/opus")
  local req = { model = r.config.model.id,
    messages = { { role = "user", content = { { type = "text", text = "hola" } } } } }
  local text = ""
  for ev in r.adapter.stream(req, r.config) do
    if ev.type == "text" then text = text .. ev.text end
  end
  enu.fs.write("stream-out.txt", text)
end)`
	res := ws.Run(t, RunOpts{Args: []string{"-e", script}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	out, err := os.ReadFile(filepath.Join(ws.Workdir, "stream-out.txt"))
	if err != nil {
		t.Fatalf("leer stream-out.txt: %v", err)
	}
	if got := string(out); got != "sin auth e2e" {
		t.Fatalf("stream-out.txt: got %q, want %q", got, "sin auth e2e")
	}
	if h := fp.Header(0); h == nil || h.Get("x-api-key") != "" {
		t.Fatalf("sin api_key_env exportada, x-api-key NO debía viajar; got %q", fp.Header(0).Get("x-api-key"))
	}
}

// TestProvidersE2ETomlAusenteArranqueLimpio: sin `providers.toml` en disco, `list()`
// devuelve un registro vacío (arranque limpio, providers.md §1: "un providers.toml
// ausente es válido") y el binario real NO lo materializa por leerlo: solo
// observable tocando el disco real del proceso tras `Run`. Igual que los escenarios
// de arriba, `list()` cuelga de `enu.fs.read` (⏸) y corre dentro de una task con el
// resultado volcado al side-channel de disco.
func TestProvidersE2ETomlAusenteArranqueLimpio(t *testing.T) {
	ws := NewWorkspace(t)
	ws.WriteEnuToml(t, "providers") // activa el plugin, pero SIN escribir providers.toml

	script := `
enu.task.spawn(function()
  local n = #require("providers").list()
  enu.fs.write("list-out.txt", tostring(n))
end)`
	res := ws.Run(t, RunOpts{Args: []string{"-e", script}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}

	out, err := os.ReadFile(filepath.Join(ws.Workdir, "list-out.txt"))
	if err != nil {
		t.Fatalf("leer list-out.txt: %v", err)
	}
	if got := string(out); got != "0" {
		t.Fatalf("list-out.txt: got %q, want %q", got, "0")
	}
	if _, err := os.Stat(filepath.Join(ws.ConfigDir, "providers.toml")); !os.IsNotExist(err) {
		t.Fatalf("providers.toml no debía materializarse en disco tras un list() vacío; err=%v", err)
	}
}
