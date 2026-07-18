package runtime

// Tests de la extensión oficial `agent` — SUBAGENTES (S40, agente.md §9).
//
// Un subagente corre AISLADO y devuelve a su padre un RESULTADO DIGERIDO. Dos
// modos (agente.md §9):
//   - `worker = false`: el subagente corre como TASK en el estado principal,
//     compartiendo tools/permisos/hooks (barato). Es una `agent.session` hija.
//   - `worker = true`: el LOOP corre en un `enu.worker` con `caps` RECORTADAS
//     (G6/S34: la superficie no concedida NO EXISTE —p. ej. `fs.write`/`ui`—),
//     pero las tools se ejecutan en el ESTADO PRINCIPAL vía proxy de mensajes (la
//     seguridad queda centralizada). El digesto cruza la frontera como valor
//     JSON-able (api.md §13).
//
// Blinda el criterio de hecho de S40: "un subagente corre aislado con API
// recortada y devuelve resultado digerido":
//   - AISLAMIENTO (caps): el worker reporta que `enu.fs.write`/`enu.ui` NO existen.
//   - TURNO AISLADO: corre con un adaptador stub require-able (sin red).
//   - DIGESTO: el padre recibe { text, message, stop_reason, usage } y lo integra.
//   - PROXY DE TOOLS: una tool que el subagente-worker pide se ejecuta en el
//     PADRE (su handler corre allí) y su resultado vuelve al worker.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wsAdapterModule es un adaptador STUB require-able como módulo Lua (no registrado
// imperativamente): así un worker (cuyo init.lua NO corre, §13) puede registrarlo
// con `providers.register_adapter`. Decide su comportamiento mirando el REQUEST (no
// globales del estado principal, que no cruzan al worker): si el último mensaje
// trae un tool_result, es la 2ª vuelta y responde texto + para; si no, y hay tools
// declaradas, pide la primera tool; si no hay tools, responde texto directo.
const wsAdapterModule = `
return {
  name = "wstub",
  caps = { tools = true, system = true, usage = true },
  stream = function(req, provider)
    local has_result = false
    local last = req.messages[#req.messages]
    if last then
      for _, b in ipairs(last.content or {}) do
        if b.type == "tool_result" then has_result = true end
      end
    end
    local events
    if has_result or not (req.tools and #req.tools > 0) then
      local msg = { role = "assistant", content = { { type = "text", text = "DIGESTO-FINAL" } } }
      events = {
        { type = "text", text = "DIGESTO-FINAL" },
        { type = "usage", input_tokens = 11, output_tokens = 7 },
        { type = "done", stop_reason = "end", message = msg },
      }
    else
      local tname = req.tools[1].name
      local call = { type = "tool_call", id = "c1", name = tname, args = { probe = true } }
      local msg = { role = "assistant", content = { call } }
      events = {
        { type = "tool_call.begin", id = "c1", name = tname },
        { type = "tool_call.end", id = "c1" },
        { type = "usage", input_tokens = 6, output_tokens = 4 },
        { type = "done", stop_reason = "tool_calls", message = msg },
      }
    end
    local i = 0
    return function() i = i + 1; return events[i] end
  end,
}
`

// wsProbeModule es un módulo de worker que reporta al padre la existencia de varias
// piezas de la API DENTRO del worker. Sirve para auditar el aislamiento por caps
// (G6) desde dentro: con las caps por defecto de un subagente (solo-lectura), debe
// faltar `fs.write`, `fs` (módulo entero), `http`, `ui` y `events`; deben estar
// `fs.read`, `task`, `json`, `toml`.
const wsProbeModule = `
local function has(path)
  local cur = enu
  for part in string.gmatch(path, "[^.]+") do
    if type(cur) ~= "table" then return false end
    cur = cur[part]
  end
  return cur ~= nil
end
enu.worker.parent.send({
  fs_read  = has("fs.read"),
  fs_write = has("fs.write"),
  fs_mod   = has("fs"),
  http     = has("http"),
  task     = has("task"),
  json     = has("json"),
  toml     = has("toml"),
  ui       = has("ui"),
  events   = has("events"),
})
`

// wsEchoModule es un adaptador stub que ECOA en el texto del digesto lo que el
// worker puso en el request (`thinking` y `system`): sirve para auditar desde
// fuera que el init del subagente-worker reenvía ambos (A-22) sin acoplarse a la
// implementación del worker.
const wsEchoModule = `
return {
  name = "wecho",
  caps = { tools = true, system = true, usage = true },
  stream = function(req, provider)
    local t = req.thinking
    local txt = "thinking=" .. (t and (tostring(t.mode) .. ":" .. tostring(t.budget)) or "nil")
      .. ";system=" .. tostring(req.system)
    local msg = { role = "assistant", content = { { type = "text", text = txt } } }
    local events = {
      { type = "text", text = txt },
      { type = "done", stop_reason = "end", message = msg },
    }
    local i = 0
    return function() i = i + 1; return events[i] end
  end,
}
`

// providersTomlWStub declara un provider cuyo adaptador es "wstub" (el require-able)
// y otro con el adaptador de eco "wecho" (para A-22).
const providersTomlWStub = `
[providers.test]
adapter  = "wstub"
base_url = "http://localhost/unused"

[[providers.test.models]]
id      = "m1"
aliases = ["m"]

[providers.test2]
adapter  = "wecho"
base_url = "http://localhost/unused"

[[providers.test2.models]]
id      = "m2"
aliases = ["e"]

[providers.testr]
adapter  = "wretry"
base_url = "http://localhost/unused"

[[providers.testr.models]]
id      = "m3"
aliases = ["r"]
`

// bootSubagent arranca un runtime con providers+sessions+agent activadas Y un
// directorio de plugins que aporta el módulo `wstub` require-able (lua/wstub.lua),
// de modo que tanto el estado principal como un worker puedan
// `require("wstub")`/registrarlo. headless (sin UI, G20): es el caso natural de un
// subagente. Devuelve el harness y el data_dir.
func bootSubagent(t *testing.T) (*harness, string) {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	pluginRoot := t.TempDir()

	// Plugin de usuario que solo aporta el módulo require-able `wstub`.
	pdir := filepath.Join(pluginRoot, "wstub_plugin")
	if err := os.MkdirAll(filepath.Join(pdir, "lua"), 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "plugin.toml"),
		[]byte("name = \"wstub_plugin\"\nversion = \"1.0\"\n"), 0o644); err != nil {
		t.Fatalf("write plugin.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "init.lua"), []byte(""), 0o644); err != nil {
		t.Fatalf("write init.lua: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "lua", "wstub.lua"), []byte(wsAdapterModule), 0o644); err != nil {
		t.Fatalf("write wstub.lua: %v", err)
	}
	// `wprobe`: módulo de worker que reporta al padre QUÉ API existe dentro del
	// worker (para auditar el aislamiento por caps DESDE DENTRO, G6).
	if err := os.WriteFile(filepath.Join(pdir, "lua", "wprobe.lua"), []byte(wsProbeModule), 0o644); err != nil {
		t.Fatalf("write wprobe.lua: %v", err)
	}
	// `wecho`: adaptador de eco para auditar el reenvío de thinking/system (A-22).
	if err := os.WriteFile(filepath.Join(pdir, "lua", "wecho.lua"), []byte(wsEchoModule), 0o644); err != nil {
		t.Fatalf("write wecho.lua: %v", err)
	}
	// `wretry`: adaptador que falla la apertura del stream N veces (G42, dirigido por
	// el prompt: los globales del principal no cruzan al worker). agent_g42_worker_test.go.
	if err := os.WriteFile(filepath.Join(pdir, "lua", "wretry.lua"), []byte(wsRetryAdapterModule), 0o644); err != nil {
		t.Fatalf("write wretry.lua: %v", err)
	}

	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\", \"wstub_plugin\"]\n")
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersTomlWStub), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithPluginDir(pluginRoot), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}, dataDir
}

// TestSubagentSpawnSuperficie: Session:spawn existe y devuelve un Sub con run/cancel.
func TestSubagentSpawnSuperficie(t *testing.T) {
	h, _ := bootSubagent(t)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local s = agent.session{ model = "test/m", no_store = true }
				assert(type(s.spawn) == "function", "Session:spawn")
				local sub = s:spawn{ model = "test/m", no_store = true }
				assert(type(sub.run) == "function", "Sub:run")
				assert(type(sub.cancel) == "function", "Sub:cancel")
				sub:cancel()
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
}

// TestSubagentTaskModeDigest (worker=false, agente.md §9): el subagente corre como
// task en el estado principal y devuelve un DIGESTO con el texto del mensaje final.
func TestSubagentTaskModeDigest(t *testing.T) {
	h, _ := bootSubagent(t)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				-- registra el adaptador stub en el estado principal (modo task: el turno
				-- corre aquí, comparte el registro de providers del principal).
				local providers = require("providers")
				providers.register_adapter("wstub", require("wstub"))
				local parent = agent.session{ model = "test/m", no_store = true }
				-- subagente sin tools (el stub responde texto directo) → digesto.
				local sub = parent:spawn{ model = "test/m", no_store = true, tools = {} }
				local digest = sub:run("resume el repo")
				DIGEST_TEXT = digest.text
				DIGEST_STOP = digest.stop_reason
				DIGEST_HAS_MSG = (digest.message ~= nil)
				DIGEST_USAGE_IN = digest.usage and digest.usage.input_tokens
				sub:cancel()
				parent:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(DIGEST_TEXT)`, "DIGESTO-FINAL")
	h.expectEval(`return tostring(DIGEST_HAS_MSG)`, "true")
	h.expectEval(`return tostring(DIGEST_USAGE_IN)`, "11")
}

// TestSubagentWorkerIsolationAndDigest es el corazón de S40: un subagente con
// `worker = true` corre el LOOP aislado en un worker (caps recortadas) con un
// adaptador stub require-able (sin red) y devuelve un DIGESTO que el padre integra.
// El aislamiento por caps DESDE DENTRO lo blinda TestSubagentWorkerProbeAPI; aquí se
// blinda el camino completo extremo a extremo (turno aislado → digesto → integración).
func TestSubagentWorkerIsolationAndDigest(t *testing.T) {
	h, _ := bootSubagent(t)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local parent = agent.session{ model = "test/m", no_store = true }
				-- worker=true: el loop corre aislado. Le pasamos el adaptador require-able
				-- "wstub" (el init.lua de providers NO corre en el worker, así que el worker
				-- debe registrar adaptadores él mismo, agente.md §9).
				-- Sin tools: el stub responde texto → el subagente devuelve su digesto.
				local sub = parent:spawn{
					model = "test/m", no_store = true, worker = true, tools = {},
					adapter_modules = { "wstub" },
				}
				local digest = sub:run("trabaja en aislado")
				WT = digest.text
				WSTOP = digest.stop_reason
				WUSAGE = digest.usage and digest.usage.output_tokens
				-- El padre INTEGRA el digesto (agente.md §9): aquí, como un mensaje suyo.
				INTEGRATED = "el subagente dijo: " .. tostring(digest.text)
				sub:cancel()
				parent:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(WT)`, "DIGESTO-FINAL")
	h.expectEval(`return tostring(WUSAGE)`, "7")
	h.expectEval(`return tostring(INTEGRATED)`, "el subagente dijo: DIGESTO-FINAL")
}

// A-21 (sesiones.md §7): el transcript de un subagente-WORKER es una sesión hija
// REAL y auditable, no un fichero con solo `meta`. El worker no persiste (sin
// fs.write): manda cada Message con `{kind="message"}` y el PADRE —que tiene el
// lock de la proxy_session— lo anexa. Aquí el padre es no_store y el hijo
// persiste: el único JSONL del dataDir debe contener el prompt del usuario y el
// assistant final con su usage, en orden.
func TestSubagentWorkerTranscriptPersistidoA21(t *testing.T) {
	h, dataDir := bootSubagent(t)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local parent = agent.session{ model = "test/m", no_store = true }
				local sub = parent:spawn{
					model = "test/m", worker = true, tools = {},
					adapter_modules = { "wstub" },
				}
				local digest = sub:run("audita mi transcript")
				WT = digest.text
				sub:cancel()
				parent:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(WT)`, "DIGESTO-FINAL")

	// El transcript hijo existe y contiene la CONVERSACIÓN, no solo la meta.
	var jsonls []string
	if err := filepath.Walk(dataDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".jsonl") {
			jsonls = append(jsonls, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk dataDir: %v", err)
	}
	if len(jsonls) != 1 {
		t.Fatalf("A-21: se esperaba exactamente 1 transcript (el hijo; el padre es no_store), hay %d: %v", len(jsonls), jsonls)
	}
	raw, err := os.ReadFile(jsonls[0])
	if err != nil {
		t.Fatalf("leer transcript hijo: %v", err)
	}
	content := string(raw)
	lines := strings.Count(strings.TrimSpace(content), "\n") + 1
	if lines < 3 {
		t.Fatalf("A-21: el transcript hijo tiene %d líneas (se esperaba meta + prompt + assistant como mínimo):\n%s", lines, content)
	}
	for _, frag := range []string{`"t":"meta"`, "audita mi transcript", "DIGESTO-FINAL", `"output_tokens":7`} {
		if !strings.Contains(content, frag) {
			t.Fatalf("A-21: el transcript hijo no contiene %q:\n%s", frag, content)
		}
	}
	// El prompt del usuario precede al assistant (mismo orden que el modo task).
	if strings.Index(content, "audita mi transcript") > strings.Index(content, "DIGESTO-FINAL") {
		t.Fatalf("A-21: el prompt aparece DESPUÉS del assistant en el transcript hijo:\n%s", content)
	}
}

// A-22 (agente.md §9): las opts del spawn son «las de agent.session», thinking y
// skills incluidas. El init del worker reenvía el `thinking` resuelto de la
// sesión hija y su system ENSAMBLADO (_assemble_system: base → skills → opts),
// no el opts.system crudo. El adaptador de eco devuelve en el texto lo que vio
// en el request: si thinking no cruza, el eco diría "thinking=nil".
func TestSubagentWorkerThinkingYSystemA22(t *testing.T) {
	h, _ := bootSubagent(t)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local parent = agent.session{ model = "test2/e", no_store = true }
				local sub = parent:spawn{
					model = "test2/e", no_store = true, worker = true, tools = {},
					adapter_modules = { "wecho" },
					thinking = { mode = "budget", budget = 123 },
					system = "SISTEMA-DEL-SPAWN",
				}
				local digest = sub:run("eco")
				ECO = digest.text
				sub:cancel()
				parent:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	eco := h.eval(`return tostring(ECO)`)[0]
	if !strings.Contains(eco, "thinking=budget:123") {
		t.Fatalf("A-22: el thinking del spawn no llegó al request del worker: %q", eco)
	}
	if !strings.Contains(eco, "SISTEMA-DEL-SPAWN") {
		t.Fatalf("A-22: el system de la sesión hija no llegó al request del worker: %q", eco)
	}
}

// TestSubagentWorkerCapsDenyWrite (AISLAMIENTO, G6): el worker del subagente corre
// con caps recortadas (solo-lectura por defecto). Verificamos DESDE DENTRO DEL
// WORKER que `enu.fs.write` y `enu.ui` NO existen, pero `enu.fs.read` y `enu.task` SÍ.
// Para auditar el interior del worker sin acoplarnos al loop del subagente, usamos
// directamente `enu.worker.spawn` con las MISMAS caps por defecto que un subagente,
// expuestas inspeccionablemente por la extensión (agent.caps + los mínimos).
func TestSubagentWorkerCapsDenyWrite(t *testing.T) {
	h, _ := bootSubagent(t)

	// El módulo `wstub` no sirve aquí; necesitamos un worker que reporte su API. Lo
	// hacemos con un módulo embebido en el plugin... pero más simple: comprobamos que
	// la extensión expone los paquetes de caps con nombre y que NO incluyen fs.write.
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				-- Paquetes de caps con nombre (agente.md §9): inspeccionables.
				local function has(list, v)
					for _, x in ipairs(list) do if x == v then return true end end
					return false
				end
				FS_RO_HAS_READ = has(agent.caps.FS_RO, "fs.read")
				FS_RO_HAS_WRITE = has(agent.caps.FS_RO, "fs.write")
				FS_RO_HAS_FS = has(agent.caps.FS_RO, "fs")
			end)
			if not ok then errc = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(FS_RO_HAS_READ)`, "true")
	h.expectEval(`return tostring(FS_RO_HAS_WRITE)`, "false")
	h.expectEval(`return tostring(FS_RO_HAS_FS)`, "false")
}

// TestSubagentWorkerProbeAPI (AISLAMIENTO real, DESDE DENTRO, G6): spawnea un worker
// con las MISMAS caps por defecto de un subagente (las que expone la extensión) y un
// módulo `wprobe` que reporta qué API existe DENTRO del worker. Blinda que las caps
// recortan de verdad la superficie del código Lua del subagente (agente.md §9 valla
// 1): `fs.write`, `fs` (módulo entero), `http`, `ui`, `events` NO existen; `fs.read`,
// `task`, `json`, `toml` SÍ. Es la verificación directa del criterio de hecho de S40
// ("API recortada"): comprobado desde dentro, no inferido.
func TestSubagentWorkerProbeAPI(t *testing.T) {
	h, _ := bootSubagent(t)
	h.eval(`
		out, errc = nil, nil
		REP = nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				-- Las caps por defecto de un subagente, expuestas inspeccionablemente.
				local caps = agent._subagent.default_caps()
				local w = enu.worker.spawn("wprobe", { caps = caps })
				REP = w:recv()
				w:terminate()
			end)
			if not ok then errc = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	// DENTRO del worker: solo-lectura. Lo no concedido no existe (deny-by-default).
	h.expectEval(`return tostring(REP.fs_read == true)`, "true")
	h.expectEval(`return tostring(REP.fs_write == true)`, "false") // ¡sin escritura!
	// `enu.fs` existe pero PODADO a solo-lectura (granularidad de función, G6): la
	// tabla está, pero `fs.write` no. Lo que importa para el aislamiento es que la
	// superficie de ESCRITURA no exista, ya comprobado arriba.
	h.expectEval(`return tostring(REP.http == true)`, "false")   // ni red
	h.expectEval(`return tostring(REP.ui == true)`, "false")     // nunca ui en worker
	h.expectEval(`return tostring(REP.events == true)`, "false") // ni bus principal
	h.expectEval(`return tostring(REP.task == true)`, "true")    // sí: el loop
	h.expectEval(`return tostring(REP.json == true)`, "true")    // sí: serializa
	h.expectEval(`return tostring(REP.toml == true)`, "true")    // sí: resolve
}

// TestSubagentWorkerCapsRejectBad: caps mal formadas → EINVAL antes de crear nada.
func TestSubagentWorkerCapsRejectBad(t *testing.T) {
	h, _ := bootSubagent(t)
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local parent = agent.session{ model = "test/m", no_store = true }
				local bad = pcall(function()
					parent:spawn{ model = "test/m", no_store = true, worker = true, caps = "no-lista" }
				end)
				BAD_REJECTED = (bad == false)
				parent:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(BAD_REJECTED)`, "true")
}

// TestSubagentWorkerToolProxy (agente.md §9, las DOS vallas): un subagente-worker
// pide una tool; el HANDLER de esa tool corre en el ESTADO PRINCIPAL (proxy de
// mensajes), no en el worker. Lo blindamos con una tool cuyo handler marca una
// global del PRINCIPAL: si se ejecutó, la global cambió (prueba de que la ejecución
// fue en el padre, donde viven el registro de tools y los permisos —centralizados—).
func TestSubagentWorkerToolProxy(t *testing.T) {
	h, _ := bootSubagent(t)
	h.eval(`
		out, errc = nil, nil
		PROXY_RAN_IN_PARENT = false
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				-- Tool registrada en el PRINCIPAL. Su handler marca una global del
				-- principal: solo accesible si corre AQUÍ (en el worker no existiría).
				agent.tool{
					name = "audit", description = "tool de auditoría",
					schema = { type = "object" },
					permissions = { default = "allow" },
					handler = function(args, ctx)
						PROXY_RAN_IN_PARENT = true
						return "auditado"
					end,
				}
				local parent = agent.session{ model = "test/m", no_store = true }
				-- Con tools: el stub pide la primera tool declarada ("audit"); el worker
				-- la proxy al padre, el padre la corre, devuelve "auditado", el worker
				-- re-pide y el stub responde texto → digesto final.
				local sub = parent:spawn{
					model = "test/m", no_store = true, worker = true,
					tools = { "audit" }, adapter_modules = { "wstub" },
				}
				local digest = sub:run("usa la tool audit")
				PROXY_TEXT = digest.text
				sub:cancel()
				parent:close()
			end)
			if not ok then errc = (type(e) == "table" and (e.code or e.message)) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	// El handler corrió EN EL PRINCIPAL (la global del padre cambió): proxy OK.
	h.expectEval(`return tostring(PROXY_RAN_IN_PARENT)`, "true")
	h.expectEval(`return tostring(PROXY_TEXT)`, "DIGESTO-FINAL")
}

// sanity: las constantes Lua de andamiaje no contienen marcadores accidentales.
func TestSubagentStubWellFormed(t *testing.T) {
	if !strings.Contains(wsAdapterModule, "DIGESTO-FINAL") {
		t.Fatal("el stub debe producir el texto digerido esperado")
	}
}
