package runtime

// Tests de la extensión oficial `sessions` (S38, embebida en
// internal/runtime/embedded/sessions). Es Lua sobre la API pública congelada
// (Fase 8, ADR-003: el core NO sabe lo que es una sesión), así que la prueba es
// Go que arranca un Runtime con la extensión ACTIVADA por `enu.toml`
// (`plugins.enabled = ["sessions"]`, igual que el gating de S12) y ejercita el
// contrato desde Lua, requiriendo el módulo con `require("sessions")`.
//
// Blinda el contrato de [sesiones.md](../../docs/contracts/sesiones.md):
//
//   - **JSONL append-only (§1-§4)**: persistir varios mensajes y reanudar
//     (replay) recupera las entradas en orden, con el `Message` canónico intacto;
//   - **lockfile exclusivo (§6, G5/G17/G32)**: dos `open` de escritura sobre la
//     misma sesión chocan (el segundo recibe ESESSION busy); el lock graba el pid
//     de `enu.sys.pid()` (G32) y el hostname de `enu.sys.hostname()` (G17);
//   - **lock huérfano (§6)**: un lock con un pid muerto (en esta máquina) se
//     reclama en silencio; uno con pid vivo no.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
// `enu.proc.alive` reporta muerto, cf. proc_test) y un id de sesión existente.
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
// extensión lleva el pid de ESTE proceso (`enu.sys.pid()` == os.Getpid) y su
// hostname (`enu.sys.hostname()`). Se lee el lock DESDE LA MISMA task, antes del
// `close` —el lock se suelta al terminar la task vía `enu.task.cleanup` (§6), así
// que inspeccionarlo después desde Go sería tarde—.
//
// También blinda de extremo a extremo (G57) que el `.jsonl.lock` sale en 0600:
// `write_lock` (init.lua) pasa `mode = SESSION_MODE` a `enu.fs.write`, así que
// `enu.fs.stat` sobre el lock —tomado mientras el lock sigue vivo, antes del
// `close`— debe devolver 384 (0o600), no el default recortado por el umask. Sin
// esta aserción, quitar `mode = SESSION_MODE` de `write_lock` no lo detectaría
// ningún test: el transcript sí se comprueba en el e2e (e2e/sessions_test.go),
// pero el lock no lo comprobaba nadie.
func TestSessionsLockGrababPidPropio(t *testing.T) {
	h, _ := bootSessions(t)

	h.eval(inTask(`
		local sessions = require("sessions")
		local s = sessions.open({ cwd = "/repo/lockcheck" })
		-- El lock vive junto al transcript: <path>.lock. Lo leemos en esta task.
		local raw = enu.fs.read(s.path .. ".lock")
		local meta = enu.json.decode(raw)
		LOCK_PID = meta.pid
		LOCK_HOST = meta.hostname
		LOCK_HAS_STARTED = (meta.started ~= nil)
		MY_PID = enu.sys.pid()
		MY_HOST = enu.sys.hostname()
		LOCK_MODE = enu.fs.stat(s.path .. ".lock").mode
		s:close()
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")
	// El pid del lock es el propio (enu.sys.pid) y coincide con os.Getpid del test.
	h.expectEval(`return tostring(LOCK_PID == MY_PID)`, "true")
	h.expectEval(`return tostring(LOCK_HOST == MY_HOST)`, "true")
	h.expectEval(`return tostring(LOCK_HAS_STARTED)`, "true")
	if got := h.eval(`return tostring(LOCK_PID)`)[0]; got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("el lock no graba el pid propio: got %q, want %d", got, os.Getpid())
	}
	// El modo del lock es 0600 (384 decimal), independiente del umask del ejecutor
	// (stat().mode refleja el chmod explícito, no lo que recortaría el umask).
	h.expectEval(`return tostring(LOCK_MODE == tonumber("600", 8))`, "true")
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

// TestSessionsListA38 (A-38): `sessions.list` obtiene la línea `meta` de cada
// transcript vía `enu.search.grep` —solo esa línea cruza la frontera wasm— en vez
// de leer el fichero ENTERO con `enu.fs.read`. Antes, listar costaba O(bytes
// totales del proyecto) en IO y memoria (un transcript de MB se copiaba a Lua
// solo para mirar su primera línea). Se blindan las tres invariantes del cambio:
//
//	(a) MISMO CONTRATO: con varias sesiones normales, cada entrada trae `id`,
//	    `path` y la `meta` correcta, en el orden del directorio (como antes).
//	(b) TRANSCRIPT GRANDE: un fichero de cientos de KB devuelve su `meta` de la
//	    PRIMERA línea. Se le añade a mano una segunda línea con una `meta` FALSA
//	    (`cwd="/WRONG"`): list debe quedarse con la de `line_no == 1` (la buena),
//	    lo que prueba que la disciplina "solo la primera línea" se respeta y que
//	    el coste ya no escala con el tamaño del fichero (nunca se lee entero).
//	(c) FICHERO CORRUPTO / SIN `meta`: no rompe list (no hay match de grep) y
//	    sigue apareciendo en la lista con `meta == nil` (igual que la versión
//	    previa, que dejaba `meta` a nil pero incluía el fichero).
func TestSessionsListA38(t *testing.T) {
	h, _ := bootSessions(t)
	const cwd = "/repo/a38"

	// Tres sesiones normales; capturamos el directorio del proyecto y el id de la
	// que convertiremos en GRANDE (escribiendo en su fichero desde Go).
	h.eval(inTask(`
		local sessions = require("sessions")
		local a = sessions.open({ cwd = "` + cwd + `" }); a:close()
		local b = sessions.open({ cwd = "` + cwd + `" }); b:close()
		local big = sessions.open({ cwd = "` + cwd + `" }); BIG_ID = big.id; big:close()
		DIR = sessions.dir("` + cwd + `")
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")
	dir := h.eval(`return DIR`)[0]
	bigID := h.eval(`return BIG_ID`)[0]

	// (b) Hacemos GRANDE el transcript de `big`: su línea 1 (la `meta` real) ya
	// está; añadimos una línea 2 con una `meta` FALSA (para verificar que list se
	// queda con la de line_no==1) y luego cientos de KB de entries `message`.
	bigPath := filepath.Join(dir, bigID+".jsonl")
	f, err := os.OpenFile(bigPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("no se pudo abrir el transcript grande %q: %v", bigPath, err)
	}
	if _, err := f.WriteString(`{"created":1,"cwd":"/WRONG","id":"WRONG","t":"meta","v":1}` + "\n"); err != nil {
		t.Fatalf("append meta falsa: %v", err)
	}
	var sb strings.Builder
	line := `{"model":"m","t":"message","ts":1,"message":{"role":"user","content":"` +
		strings.Repeat("x", 200) + `"}}` + "\n"
	for sb.Len() < 400*1024 { // > 400 KiB de relleno: leer esto entero es justo lo que evitamos
		sb.WriteString(line)
	}
	if _, err := f.WriteString(sb.String()); err != nil {
		t.Fatalf("append relleno: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close transcript grande: %v", err)
	}

	// (c) Fichero corrupto: un `.jsonl` sin `meta` en la primera línea (ni la
	// subcadena que casa el patrón). No debe romper list y debe salir con meta=nil.
	corruptPath := filepath.Join(dir, "0000000000001-dead.jsonl")
	if err := os.WriteFile(corruptPath,
		[]byte("esto no es json valido, ni una meta\n{\"t\":\"message\",\"ts\":1}\n"), 0o644); err != nil {
		t.Fatalf("escribir fichero corrupto: %v", err)
	}

	// Listar y auditar las tres invariantes desde Lua.
	h.eval(inTask(`
		local sessions = require("sessions")
		local l = sessions.list("` + cwd + `")
		COUNT = #l
		local withmeta = 0
		local corrupt_present, corrupt_meta_nil = false, false
		local big_meta = nil
		for _, e in ipairs(l) do
			if e.meta ~= nil then withmeta = withmeta + 1 end
			if e.id == "0000000000001-dead" then
				corrupt_present = true
				corrupt_meta_nil = (e.meta == nil)
			end
			if e.id == "` + bigID + `" then big_meta = e.meta end
		end
		WITHMETA = withmeta
		CORRUPT_PRESENT = corrupt_present
		CORRUPT_META_NIL = corrupt_meta_nil
		BIG_CWD = big_meta and big_meta.cwd or "NONE"
		BIG_METAID = big_meta and big_meta.id or "NONE"
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")

	// (a) 3 sesiones normales + 1 corrupto = 4 entradas; 3 con meta.
	h.expectEval(`return tostring(COUNT)`, "4")
	h.expectEval(`return tostring(WITHMETA)`, "3")
	// (b) la meta del fichero grande es la de la PRIMERA línea (cwd real, id real),
	// NO la falsa de la línea 2 (cwd="/WRONG"): line_no==1 manda.
	h.expectEval(`return tostring(BIG_CWD)`, cwd)
	h.expectEval(`return tostring(BIG_METAID)`, bigID)
	// (c) el corrupto está en la lista, con meta=nil, y no rompió nada.
	h.expectEval(`return tostring(CORRUPT_PRESENT)`, "true")
	h.expectEval(`return tostring(CORRUPT_META_NIL)`, "true")
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
