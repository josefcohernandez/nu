package runtime

// Tests de la extensión oficial `sessions` (S38, embebida en
// internal/runtime/embedded/sessions). Es Lua sobre la API pública congelada
// (Fase 8, ADR-003: el core NO sabe lo que es una sesión), así que la prueba es
// Go que arranca un Runtime con la extensión ACTIVADA por `nu.toml`
// (`plugins.enabled = ["sessions"]`, igual que el gating de S12) y ejercita el
// contrato desde Lua, requiriendo el módulo con `require("sessions")`.
//
// Blinda el contrato de [sesiones.md](../../docs/sesiones.md):
//
//   - **JSONL append-only (§1-§4)**: persistir varios mensajes y reanudar
//     (replay) recupera las entradas en orden, con el `Message` canónico intacto;
//   - **lockfile exclusivo (§6, G5/G17/G32)**: dos `open` de escritura sobre la
//     misma sesión chocan (el segundo recibe ESESSION busy); el lock graba el pid
//     de `nu.sys.pid()` (G32) y el hostname de `nu.sys.hostname()` (G17);
//   - **lock huérfano (§6)**: un lock con un pid muerto (en esta máquina) se
//     reclama en silencio; uno con pid vivo no.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// bootSessions arranca un Runtime con la extensión `sessions` activada y un
// data_dir CONOCIDO (para inspeccionar/manipular los ficheros JSONL y los locks
// desde Go). Devuelve el harness y el data_dir.
func bootSessions(t *testing.T) (*harness, string) {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"sessions\"]\n")
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}, dataDir
}

// TestSessionsCargaYActiva: la extensión carga (source="builtin") y su módulo
// expone la superficie del contrato.
func TestSessionsCargaYActiva(t *testing.T) {
	h, _ := bootSessions(t)
	if src := listSource(h, "sessions"); src != "builtin" {
		t.Fatalf(`sessions debía cargarse con source="builtin"; got %q`, src)
	}
	h.expectEval(`
		local s = require("sessions")
		assert(type(s.open) == "function", "open")
		assert(type(s.list) == "function", "list")
		return "ok"`, "ok")
}

// TestSessionsPersistirYReanudar (§1-§4): se crea una sesión, se anexan varios
// mensajes (Message canónico de providers.md §2), se cierra, y al REANUDAR el
// replay recupera la entrada `meta` + los mensajes en orden, con el contenido
// intacto. Es el criterio de hecho central de S38.
func TestSessionsPersistirYReanudar(t *testing.T) {
	h, _ := bootSessions(t)

	// Crear + anexar dos mensajes (user, assistant) + cerrar. La id creada se
	// deja en la global SID para reanudar después.
	h.eval(inTask(`
		local sessions = require("sessions")
		local s = sessions.open({ cwd = "/repo/proyecto" })
		SID = s.id
		s:append_message({ role = "user", content = { { type = "text", text = "hola" } } })
		s:append_message(
			{ role = "assistant", content = { { type = "text", text = "qué tal" } } },
			{ model = "anthropic/claude", usage = { input_tokens = 5, output_tokens = 7 } })
		s:close()
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")
	sid := h.eval(`return tostring(SID)`)[0]
	if sid == "" || sid == "nil" {
		t.Fatalf("no se generó id de sesión; got %q", sid)
	}

	// Reanudar y verificar el replay: meta + 2 mensajes, contenido y roles
	// correctos, el `usage`/`model` del assistant adjuntos (§3).
	h.eval(inTask(`
		local sessions = require("sessions")
		local s = sessions.open({ cwd = "/repo/proyecto", resume = "` + sid + `" })
		local e = s:replay()
		assert(#e == 3, "esperaba 3 entradas (meta + 2 mensajes), hay " .. #e)
		assert(e[1].t == "meta" and e[1].id == "` + sid + `", "meta")
		assert(e[1].cwd == "/repo/proyecto", "meta.cwd")
		assert(e[2].t == "message" and e[2].message.role == "user", "msg1 user")
		assert(e[2].message.content[1].text == "hola", "msg1 texto")
		assert(e[3].t == "message" and e[3].message.role == "assistant", "msg2 assistant")
		assert(e[3].message.content[1].text == "qué tal", "msg2 texto")
		assert(e[3].model == "anthropic/claude", "msg2 model adjunto")
		assert(e[3].usage.output_tokens == 7, "msg2 usage adjunto")
		assert(e[2].ts ~= nil, "msg lleva ts")
		s:close()
		out = "ok2"`))
	h.expectEval(`return tostring(out)`, "ok2")
}

// TestSessionsLockExclusivo (§6, G5): mientras una sesión está abierta para
// escritura, un segundo `open` de escritura sobre la MISMA sesión choca con el
// lock (pid vivo = el propio proceso) y recibe ESESSION{reason="busy"}. El primero
// sí pudo abrir. Un `open{read_only=true}` no necesita lock y SÍ puede abrir.
func TestSessionsLockExclusivo(t *testing.T) {
	h, _ := bootSessions(t)

	h.eval(inTask(`
		local sessions = require("sessions")
		local s1 = sessions.open({ cwd = "/repo/x" })
		SID = s1.id
		-- Segundo open de escritura sobre la misma sesión: debe lanzar ESESSION busy
		-- (el lock vivo es de este mismo pid).
		local ok, err = pcall(function()
			return sessions.open({ cwd = "/repo/x", resume = s1.id })
		end)
		assert(not ok, "el segundo open de escritura debió fallar (lock)")
		ERRCODE = (type(err) == "table") and err.code or tostring(err)
		ERRREASON = (type(err) == "table" and type(err.detail) == "table") and err.detail.reason or ""
		-- Pero un lector (read_only) no necesita lock: abre sin problema.
		local r = sessions.open({ cwd = "/repo/x", resume = s1.id, read_only = true })
		RO_OK = (r ~= nil)
		s1:close()
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")
	h.expectEval(`return tostring(ERRCODE)`, "ESESSION")
	h.expectEval(`return tostring(ERRREASON)`, "busy")
	h.expectEval(`return tostring(RO_OK)`, "true")
}

// TestSessionsLockHuerfano (§6): un lockfile dejado por un crash (mismo hostname,
// pid MUERTO) es huérfano: el siguiente `open` lo reclama EN SILENCIO y adquiere
// el lock. Se simula escribiendo a mano un lock con un pid imposible (1<<30, que
// `nu.proc.alive` reporta muerto, cf. proc_test) y un id de sesión existente.
func TestSessionsLockHuerfano(t *testing.T) {
	h, dataDir := bootSessions(t)

	// Crear una sesión y cerrarla (deja el .jsonl, libera su lock).
	h.eval(inTask(`
		local sessions = require("sessions")
		local s = sessions.open({ cwd = "/repo/orphan" })
		SID = s.id
		s:close()
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")
	sid := h.eval(`return tostring(SID)`)[0]

	// La ubicación (sesiones.md §2): data_dir/sessions/<slug>/<id>.jsonl(.lock).
	// El slug de "/repo/orphan" lo calcula el módulo; aquí lo reconstruimos igual
	// (no-alfanum→'_', recorta bordes).
	// El slug de "/repo/orphan": no-alnum→'_' da "_repo_orphan", y el recorte de
	// los '_' de los bordes deja "repo_orphan" (sesiones.md §2; ver slug() del módulo).
	projDir := filepath.Join(dataDir, "sessions", "repo_orphan")
	lockPath := filepath.Join(projDir, sid+".jsonl.lock")
	deadPID := 1 << 30
	host, _ := os.Hostname()
	orphan := `{"pid":` + strconv.Itoa(deadPID) + `,"hostname":` + strconv.Quote(host) + `,"started":1}`
	if err := os.WriteFile(lockPath, []byte(orphan), 0o600); err != nil {
		t.Fatalf("escribir lock huérfano: %v", err)
	}

	// open de escritura: el huérfano (pid muerto, mismo host) se reclama en
	// silencio y se adquiere el lock; ahora el lock graba NUESTRO pid (G32).
	h.eval(inTask(`
		local sessions = require("sessions")
		local s = sessions.open({ cwd = "/repo/orphan", resume = "` + sid + `" })
		RECLAIMED = (s ~= nil)
		s:close()
		out = "ok2"`))
	h.expectEval(`return tostring(out)`, "ok2")
	h.expectEval(`return tostring(RECLAIMED)`, "true")
}

// TestSessionsLockGrababPidPropio (G32): el contenido del lock que escribe la
// extensión lleva el pid de ESTE proceso (`nu.sys.pid()` == os.Getpid) y su
// hostname (`nu.sys.hostname()`). Se lee el lock DESDE LA MISMA task, antes del
// `close` —el lock se suelta al terminar la task vía `nu.task.cleanup` (§6), así
// que inspeccionarlo después desde Go sería tarde—.
func TestSessionsLockGrababPidPropio(t *testing.T) {
	h, _ := bootSessions(t)

	h.eval(inTask(`
		local sessions = require("sessions")
		local s = sessions.open({ cwd = "/repo/lockcheck" })
		-- El lock vive junto al transcript: <path>.lock. Lo leemos en esta task.
		local raw = nu.fs.read(s.path .. ".lock")
		local meta = nu.json.decode(raw)
		LOCK_PID = meta.pid
		LOCK_HOST = meta.hostname
		LOCK_HAS_STARTED = (meta.started ~= nil)
		MY_PID = nu.sys.pid()
		MY_HOST = nu.sys.hostname()
		s:close()
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")
	// El pid del lock es el propio (nu.sys.pid) y coincide con os.Getpid del test.
	h.expectEval(`return tostring(LOCK_PID == MY_PID)`, "true")
	h.expectEval(`return tostring(LOCK_HOST == MY_HOST)`, "true")
	h.expectEval(`return tostring(LOCK_HAS_STARTED)`, "true")
	if got := h.eval(`return tostring(LOCK_PID)`)[0]; got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("el lock no graba el pid propio: got %q, want %d", got, os.Getpid())
	}
}

// TestSessionsList (§7): listar las sesiones de un proyecto enumera los `.jsonl`
// con su `meta`, e ignora los `.jsonl.lock`. Un proyecto sin sesiones da lista
// vacía.
func TestSessionsList(t *testing.T) {
	h, _ := bootSessions(t)

	// Proyecto vacío: lista vacía.
	h.eval(inTask(`
		local sessions = require("sessions")
		out = tostring(#sessions.list("/repo/vacio"))`))
	h.expectEval(`return tostring(out)`, "0")

	// Crear dos sesiones en el mismo proyecto.
	h.eval(inTask(`
		local sessions = require("sessions")
		local a = sessions.open({ cwd = "/repo/listame" }); a:close()
		local b = sessions.open({ cwd = "/repo/listame" }); b:close()
		local l = sessions.list("/repo/listame")
		COUNT = #l
		-- Cada entrada lleva id, path y meta con el cwd correcto.
		local all_meta = true
		for _, e in ipairs(l) do
			if not (e.meta and e.meta.cwd == "/repo/listame" and e.id) then all_meta = false end
		end
		ALLMETA = all_meta
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")
	h.expectEval(`return tostring(COUNT)`, "2")
	h.expectEval(`return tostring(ALLMETA)`, "true")
}

// TestSessionsReanudarInexistente (§6/G18): reanudar una sesión que no existe es
// un error claro (ESESSION reason="missing"), no la creación silenciosa de una.
func TestSessionsReanudarInexistente(t *testing.T) {
	h, _ := bootSessions(t)
	h.eval(inTask(`
		local sessions = require("sessions")
		local ok, err = pcall(function()
			return sessions.open({ cwd = "/repo/y", resume = "0000000000000-dead" })
		end)
		ERRCODE = (not ok and type(err) == "table") and err.code or "NO-ERROR"
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")
	h.expectEval(`return tostring(ERRCODE)`, "ESESSION")
}
