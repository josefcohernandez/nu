package runtime

// Tests de G55 (providers.md §4 + agente.md §3, SEC-04): los secretos del
// provider NO se heredan por defecto en el entorno de los subprocesos que
// lanza la tool `bash`. Dos piezas, ambas de extensiones (el core queda
// intacto):
//
//   - `providers.secret_env_vars()` (providers_test.go: TestProvidersSecretEnvVars)
//     lista los NOMBRES de las `api_key_env` del registro.
//   - `agent._bash_subprocess_argv` (init.lua) antepone `env -u VAR ... --` al
//     argv real, usando esa lista, salvo opt-in nominal `[tools.bash]
//     inherit_secrets` en el `agent.toml` del USUARIO (§10/§11).
//
// Este fichero blinda, en orden: el constructor de argv de forma aislada
// (unitario, arnés `bootAgent` de agent_test.go), el opt-in leído del
// `agent.toml` del usuario (patrón manual de TestSessionThinkingFromConfig,
// agent_p21_test.go), y un turno de EXTREMO A EXTREMO con un subproceso REAL
// lanzado por la tool `bash` registrada por la propia extensión
// (tools_bash.lua) — la prueba que de verdad importa: un `printenv` hostil no
// ve la API key, el resto del entorno sobrevive, y el recorte no rompe el
// camino feliz de un turno con tool call.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// providersTomlToolStubSecret: como providersTomlToolStub (agent_test.go,
// adapter "toolstub" para ejercitar el loop del turno) pero DECLARANDO
// `api_key_env` — sin esa declaración `secret_env_vars()` está vacía y no hay
// nada que recortar (el vector real de G55 exige un provider con clave).
const providersTomlToolStubSecret = `
[providers.test]
adapter     = "toolstub"
base_url    = "http://localhost/unused"
api_key_env = "TESTCO_API_KEY"

[[providers.test.models]]
id      = "m1"
aliases = ["m"]
`

// TestG55SubprocessArgvRecortaElSecretoPorDefecto: con un provider que declara
// `api_key_env`, `agent._bash_subprocess_argv` antepone `env -u VAR --` al argv
// real. Sin overlays ni turnos: el constructor puro, aislado.
func TestG55SubprocessArgvRecortaElSecretoPorDefecto(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStubSecret, false)
	h.eval(inTask(`
		local agent = require("agent")
		local argv = agent._bash_subprocess_argv({ "/bin/sh", "-c", "echo hi" })
		out = table.concat(argv, "|")
	`))
	h.expectEval(`return tostring(err_code)`, "nil")
	h.expectEval(`return tostring(out)`, "env|-u|TESTCO_API_KEY|--|/bin/sh|-c|echo hi")
}

// TestG55SubprocessArgvSinSecretosNoTocaElArgv: un registro SIN ningún
// `api_key_env` (providersTomlToolStub, sin la variante *Secret) no tiene nada
// que recortar — el argv sale intacto, sin el prefijo `env -u`. Confirma que el
// recorte es proporcional al registro, no un envoltorio incondicional.
func TestG55SubprocessArgvSinSecretosNoTocaElArgv(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStub, false)
	h.eval(inTask(`
		local agent = require("agent")
		local argv = agent._bash_subprocess_argv({ "/bin/sh", "-c", "echo hi" })
		out = table.concat(argv, "|")
	`))
	h.expectEval(`return tostring(err_code)`, "nil")
	h.expectEval(`return tostring(out)`, "/bin/sh|-c|echo hi")
}

// TestG55InheritSecretsOptInDelAgentTomlDelUsuario: `[tools.bash]
// inherit_secrets = ["VAR"]` en el agent.toml de `config.dir()` (el ÚNICO que
// `load_config` lee — el del repo aún no se lee en absoluto, §11) reintroduce
// la variable nombrada: el argv sale SIN el `env -u` para ella. Arranque
// manual (no `bootAgent`, que no expone el cfg dir) — mismo patrón que
// TestSessionThinkingFromConfig (agent_p21_test.go).
func TestG55InheritSecretsOptInDelAgentTomlDelUsuario(t *testing.T) {
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n")
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersTomlToolStubSecret), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "agent.toml"),
		[]byte("[tools.bash]\ninherit_secrets = [\"TESTCO_API_KEY\"]\n"), 0o644); err != nil {
		t.Fatalf("write agent.toml: %v", err)
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}
	h.eval(inTask(`
		local agent = require("agent")
		local argv = agent._bash_subprocess_argv({ "/bin/sh", "-c", "echo hi" })
		out = table.concat(argv, "|")
	`))
	h.expectEval(`return tostring(err_code)`, "nil")
	// Sin recorte: el opt-in nominal concedió justo esa variable.
	h.expectEval(`return tostring(out)`, "/bin/sh|-c|echo hi")
}

// TestG55InheritSecretsSoloVarNombradaElRestoSigueRecortado: con DOS providers
// (dos secretos distintos) e `inherit_secrets` nombrando solo UNO, el argv
// recorta el que no se nombró y deja pasar el que sí — el opt-in es NOMINAL,
// no "todos" (agente.md §3: "lista de nombres exactos, sin comodín").
func TestG55InheritSecretsSoloVarNombradaElRestoSigueRecortado(t *testing.T) {
	toml := `
[providers.a]
adapter     = "toolstub"
base_url    = "http://localhost/unused"
api_key_env = "SECRET_A"
[[providers.a.models]]
id = "m1"

[providers.b]
adapter     = "toolstub"
base_url    = "http://localhost/unused"
api_key_env = "SECRET_B"
[[providers.b.models]]
id = "m1"
`
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n")
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "agent.toml"),
		[]byte("[tools.bash]\ninherit_secrets = [\"SECRET_B\"]\n"), 0o644); err != nil {
		t.Fatalf("write agent.toml: %v", err)
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}
	h.eval(inTask(`
		local agent = require("agent")
		local argv = agent._bash_subprocess_argv({ "/bin/sh", "-c", "echo hi" })
		out = table.concat(argv, "|")
	`))
	h.expectEval(`return tostring(err_code)`, "nil")
	// SECRET_A (no nombrada) se recorta; SECRET_B (nombrada) no aparece con -u.
	h.expectEval(`return tostring(out)`, "env|-u|SECRET_A|--|/bin/sh|-c|echo hi")
}

// TestG55InheritSecretsTipoMalFormadoFailClosed: si `[tools.bash]
// inherit_secrets` del agent.toml del USUARIO viene con un tipo MAL FORMADO
// —no una tabla-array— el guard fail-closed de init.lua (`if type(inherit) ~=
// "table" then inherit = {}`) lo trata como lista vacía: NO concede nada y el
// secreto SIGUE recortado, sin reventar el constructor.
//
// Se ejercitan DOS formas mal formadas, y la distinción importa para que el
// test tenga dientes contra la mutación (borrar/invertir el guard):
//
//   - un STRING (`"TESTCO_API_KEY"`) es el error de config REALISTA —escribir
//     `inherit_secrets = "X"` en vez de `["X"]`—. En este Lua, `ipairs` sobre
//     un string es un no-op (itera vacío), así que documenta el comportamiento
//     observable pero, por casualidad, seguiría siendo fail-closed aun sin el
//     guard.
//   - un NÚMERO (`42`) es donde el guard es DE VERDAD portante: sin él,
//     `ipairs(42)` lanza ("attempt to index a number value") y el constructor
//     revienta. Con el guard, se recorta limpio y sin error. Es el vector que
//     mata la mutación: si se borra o invierte el guard, este sub-caso pasa de
//     "recorta, err nil" a "err no-nil" y el test cae.
func TestG55InheritSecretsTipoMalFormadoFailClosed(t *testing.T) {
	cases := []struct {
		name      string
		tomlValue string
	}{
		{"string", `"TESTCO_API_KEY"`}, // error realista del usuario
		{"numero", `42`},               // hace al guard portante (ipairs(42) lanzaría)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := t.TempDir()
			dataDir := t.TempDir()
			writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n")
			if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersTomlToolStubSecret), 0o644); err != nil {
				t.Fatalf("write providers.toml: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg, "agent.toml"),
				[]byte("[tools.bash]\ninherit_secrets = "+c.tomlValue+"\n"), 0o644); err != nil {
				t.Fatalf("write agent.toml: %v", err)
			}
			rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(false))
			t.Cleanup(rt.Close)
			if err := rt.Boot(); err != nil {
				t.Fatalf("Boot: %v", err)
			}
			h := &harness{t: t, rt: rt}
			h.eval(inTask(`
				local agent = require("agent")
				local argv = agent._bash_subprocess_argv({ "/bin/sh", "-c", "echo hi" })
				out = table.concat(argv, "|")
			`))
			// Sin error: el guard absorbe la config mal formada (no revienta).
			h.expectEval(`return tostring(err_code)`, "nil")
			// Fail-closed: config mal formada = lista vacía → el secreto SIGUE recortado.
			h.expectEval(`return tostring(out)`, "env|-u|TESTCO_API_KEY|--|/bin/sh|-c|echo hi")
		})
	}
}

// TestG55BashToolPresentaFalloConStderrYExitCode blinda la PRESENTACIÓN de la
// tool `bash` real (tools_bash.lua) cuando el comando FALLA: un comando que
// escribe en stderr y sale con código != 0 compone las tres ramas del formato
// —stdout (aquí vacío), `[stderr]\n...` y `[exit code N]`—. Los tests previos de
// G55 solo ejercen el camino feliz (printf, exit 0, sin stderr), así que las
// ramas de stderr y exit code quedaban sin cubrir: borrar cualquiera de las dos
// sobreviviría. Aquí no.
func TestG55BashToolPresentaFalloConStderrYExitCode(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStubSecret, false) // headless (sin UI, G20)

	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				TOOLNAME = "bash"
				-- Escribe en stderr y sale con código 3 (fallo del COMANDO, no de
				-- la tool): las tres ramas del formato deben componer la salida.
				TOOLARGS = { command = "echo err >&2; exit 3" }
				local s = agent.session{
					model = "test/m", no_store = true,
					permissions = { allow = { "bash" } },
				}
				s:send("corre el comando")
				TOOL_OUT = s.history[3].content[1].content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")

	toolOut := h.eval(`return tostring(TOOL_OUT)`)[0]
	// Rama stderr: el `[stderr]` precede a la salida de error del comando.
	if !strings.Contains(toolOut, "[stderr]\nerr") {
		t.Fatalf("se esperaba la rama [stderr] con la salida de error; got %q", toolOut)
	}
	// Rama exit code: el código != 0 se reporta como texto (no como is_error).
	if !strings.Contains(toolOut, "[exit code 3]") {
		t.Fatalf("se esperaba la rama [exit code 3]; got %q", toolOut)
	}
}

// TestG55BashSubprocesoRealNoVeElSecretoDelProvider es la prueba que de verdad
// importa: un turno normal pide la tool `bash` REAL (registrada por la propia
// extensión en tools_bash.lua, no un doble de test) con un comando que vuelca
// las dos variables al tool_result. Blinda LAS TRES cosas que pide G55:
//
//  1. el subproceso NO ve la API key del provider activo (vacía);
//  2. el RESTO del entorno sobrevive (otra variable exportada sí aparece);
//  3. el recorte no rompe un turno normal con tool call: el loop completa y
//     cierra con el `done` final del stub ("listo").
func TestG55BashSubprocesoRealNoVeElSecretoDelProvider(t *testing.T) {
	h, _ := bootAgent(t, providersTomlToolStubSecret, false) // headless (sin UI, G20)

	t.Setenv("TESTCO_API_KEY", "clave-secreta-no-debe-verse")
	t.Setenv("ENU_G55_OTRA_VAR", "sobrevive-al-recorte")

	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				TOOLNAME = "bash"
				TOOLARGS = { command = "printf 'SECRET=[%s] OTRA=[%s]' \"$TESTCO_API_KEY\" \"$ENU_G55_OTRA_VAR\"" }
				-- allow = {"bash"} (SIN ":") concede la tool ENTERA por nombre exacto
				-- (G53/ADR-023): aísla el recorte de entorno del pipeline de permisos,
				-- que ya tiene su propia batería de tests (agent_g53_test.go).
				local s = agent.session{
					model = "test/m", no_store = true,
					permissions = { allow = { "bash" } },
				}
				local final = s:send("corre el comando")
				FINAL_TEXT = final.content[1].text
				-- El tool_result trae la salida REAL del subproceso (bash real, no un stub).
				TOOL_OUT = s.history[3].content[1].content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	// 3. el recorte no rompe el turno normal: llega al `done` final del stub.
	h.expectEval(`return tostring(FINAL_TEXT)`, "listo")

	toolOut := h.eval(`return tostring(TOOL_OUT)`)[0]
	// 1. la API key NUNCA llega al subproceso (ni como valor, ni queda su hueco
	// relleno: SECRET=[] confirma la variable AUSENTE, no vacía-mismo-nombre).
	if strings.Contains(toolOut, "clave-secreta-no-debe-verse") {
		t.Fatalf("el subproceso de bash VIO la API key del provider: %q", toolOut)
	}
	if !strings.Contains(toolOut, "SECRET=[]") {
		t.Fatalf("se esperaba SECRET=[] (variable recortada), got %q", toolOut)
	}
	// 2. el resto del entorno sobrevive.
	if !strings.Contains(toolOut, "OTRA=[sobrevive-al-recorte]") {
		t.Fatalf("el resto del entorno debía sobrevivir al recorte; got %q", toolOut)
	}
}

// TestG55BashSubprocesoRealConInheritSecretsSiVeLaVariable: el mismo turno,
// pero con el opt-in del USUARIO concedido — la tool `bash` real SÍ ve la API
// key. Confirma extremo a extremo (no solo en el constructor de argv) que
// `inherit_secrets` hace lo que promete.
func TestG55BashSubprocesoRealConInheritSecretsSiVeLaVariable(t *testing.T) {
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n")
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersTomlToolStubSecret), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "agent.toml"),
		[]byte("[tools.bash]\ninherit_secrets = [\"TESTCO_API_KEY\"]\n"), 0o644); err != nil {
		t.Fatalf("write agent.toml: %v", err)
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	h := &harness{t: t, rt: rt}

	t.Setenv("TESTCO_API_KEY", "clave-con-opt-in-si-visible")

	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				TOOLNAME = "bash"
				TOOLARGS = { command = "printf 'SECRET=[%s]' \"$TESTCO_API_KEY\"" }
				local s = agent.session{
					model = "test/m", no_store = true,
					permissions = { allow = { "bash" } },
				}
				s:send("corre el comando")
				TOOL_OUT = s.history[3].content[1].content[1].text
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(TOOL_OUT)`, "SECRET=[clave-con-opt-in-si-visible]")
}
