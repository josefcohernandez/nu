package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests de G56 (ADR-024) — la IDENTIDAD/DUEÑO de un worker para las primitivas [W]
// atribuidas por dueño es la FOTO tomada en el spawn, inmutable durante toda la vida
// del worker. Lo que blindan:
//
//   - **atribución de log determinista**: una línea logueada DESDE un worker se anota
//     con el plugin dueño vigente en el momento del `enu.worker.spawn` —distinguido
//     como `<plugin> (worker)`—, aunque el estado del padre (su ownerStack) haya
//     cambiado para cuando el worker realmente loguea. La goroutine del worker NO lee
//     el ownerStack del padre: la foto viaja copiada, como los mensajes (ADR-008), lo
//     que elimina por diseño el data race de SEC-05 (raíz común de SEC-07).
//   - **supervisión de proc sin fugas**: un proceso lanzado por un worker queda
//     registrado bajo el plugin dueño (el nombre CRUDO, sin sufijo), de modo que el
//     árbol de supervisión —y con él `plugin.reload`— lo alcanza igual que a los del
//     estado principal.
//
// El caso adverso (que rompía la implementación anterior de lectura viva) se fuerza
// haciendo que el worker se spawnee DURANTE el `init.lua` del plugin `p` (ownerStack
// = [p]) pero loguee/spawnee MÁS TARDE, cuando el padre corre una task de usuario y su
// ownerStack está vacío ("user"): la atribución correcta es la foto `p`, no lo que el
// padre estuviera haciendo en ese instante.

// g56Harness construye un harness cuyo runtime tiene un plugin `p` cuyo `init.lua`
// es `initLua` (típicamente un `enu.worker.spawn(...)` que deja el handle en un
// global para que un eval posterior lo maneje). El worker corre su cuerpo INLINE
// (la fuente pasada a `spawn`), sin necesidad de un módulo `require`-able. Devuelve
// el harness ya con `Boot()` hecho, de modo que el spawn del init ya ocurrió con el
// ownerStack = [p].
func g56Harness(t *testing.T, initLua string) *harness {
	t.Helper()
	root := t.TempDir()
	cfg := t.TempDir()
	dir := filepath.Join(root, "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"),
		[]byte("name = \"p\"\nversion = \"1.0\"\n"), 0o644); err != nil {
		t.Fatalf("write plugin.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.lua"), []byte(initLua), 0o644); err != nil {
		t.Fatalf("write init.lua: %v", err)
	}
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithPluginDir(root), WithForceUI(true))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}
}

// TestWorkerLogOwnerEsFotoDelSpawn (G56, ADR-024): una línea logueada desde un worker
// se atribuye al plugin dueño del SPAWN (`p (worker)`), y ese valor es DETERMINISTA
// aunque el ownerStack del padre haya cambiado —a "user"— para cuando el worker
// loguea. Con la implementación vieja (lectura viva de rt.ownerStack del padre desde
// la goroutine del worker) la atribución habría sido "user"; con la foto es
// "p (worker)". El worker loguea SÓLO tras recibir un mensaje que el padre le manda
// desde una task de usuario, garantizando que el ownerStack del padre está vacío en
// ese instante.
func TestWorkerLogOwnerEsFotoDelSpawn(t *testing.T) {
	h := g56Harness(t, `
		-- Spawn DURANTE el init de p: la foto del dueño del worker es "p".
		__WK = enu.worker.spawn([[
			local m = enu.worker.parent.recv()      -- bloquea hasta el mensaje del padre
			enu.log.info("marca-g56-worker:" .. m)   -- se atribuye a "p (worker)"
			enu.worker.parent.send("ok")
		]])
	`)

	// Boot ya terminó: el ownerStack del padre está vacío ("user"). El worker sigue
	// bloqueado en parent.recv. Ahora una task de USUARIO le manda "go": el worker
	// loguea en ese momento, con el padre en contexto "user".
	h.eval(`
		DONE = false
		enu.task.spawn(function()
			__WK:send("go")
			local r = __WK:recv()
			__WK:terminate()
			DONE = true
		end)
	`)
	h.expectEval(`return tostring(DONE)`, "true")

	// La línea del worker debe llevar el dueño de la FOTO del spawn, distinguido:
	// "[p (worker)]", nunca "[user]" (lo que daría la lectura viva) ni "[p]" (sin marca).
	var linea string
	for _, l := range h.logLines() {
		if strings.Contains(l, "marca-g56-worker:go") {
			linea = l
			break
		}
	}
	if linea == "" {
		t.Fatalf("G56: no se encontró la línea logueada por el worker; log:\n%s", strings.Join(h.logLines(), "\n"))
	}
	if !strings.Contains(linea, "[p (worker)]") {
		t.Fatalf("G56: la atribución del log del worker debe ser la foto del spawn \"p (worker)\", no lo que el padre hiciera después; línea: %q", linea)
	}
	if strings.Contains(linea, "[user]") {
		t.Fatalf("G56: la atribución cayó a \"user\" (lectura viva del ownerStack del padre, SEC-05/SEC-07); línea: %q", linea)
	}
}

// TestWorkerProcOwnerEsFotoDelSpawn (G56, ADR-024): un proceso lanzado desde un worker
// queda registrado en el árbol de supervisión del PADRE bajo el plugin dueño del
// spawn —el nombre CRUDO "p", sin el sufijo "(worker)" de los logs—, de modo que
// `plugin.reload` de ese plugin lo alcanza igual que a los del estado principal. Se
// verifica inspeccionando `rt.sched.ownerHandles`: la entrada vive bajo "p", no bajo
// "p (worker)".
func TestWorkerProcOwnerEsFotoDelSpawn(t *testing.T) {
	h := g56Harness(t, `
		-- Spawn DURANTE el init de p: la foto del dueño del worker es "p".
		__WK = enu.worker.spawn([[
			enu.worker.parent.recv()                 -- espera "spawn"
			P = enu.proc.spawn({"sleep", "30"})       -- se registra bajo "p" en el padre
			enu.worker.parent.send("spawned")
			enu.worker.parent.recv()                 -- espera "kill"
			P:kill()
			P:wait()
			enu.worker.parent.send("killed")
		]])
	`)

	// Fase 1: disparar el spawn del proceso dentro del worker y esperar el ack. Tras
	// esto el proceso está vivo y registrado en rt.sched.ownerHandles.
	h.eval(`
		S1 = false
		enu.task.spawn(function()
			__WK:send("spawn")
			__WK:recv()          -- "spawned"
			S1 = true
		end)
	`)
	h.expectEval(`return tostring(S1)`, "true")

	// El proceso del worker está bajo el plugin dueño CRUDO ("p"), no bajo "p (worker)".
	h.rt.sched.mu.Lock()
	bajoP := len(h.rt.sched.ownerHandles["p"])
	bajoPWorker := len(h.rt.sched.ownerHandles["p (worker)"])
	h.rt.sched.mu.Unlock()
	if bajoP < 1 {
		t.Fatalf("G56: el proc lanzado por el worker debe registrarse bajo el plugin dueño \"p\" (reload debe alcanzarlo); handles bajo \"p\"=%d", bajoP)
	}
	if bajoPWorker != 0 {
		t.Fatalf("G56: el proc NO debe quedar bajo \"p (worker)\" (fuga de supervisión: reload no lo alcanzaría); handles bajo \"p (worker)\"=%d", bajoPWorker)
	}

	// Fase 2: matar el proceso y terminar el worker, para no fugar el `sleep 30` más
	// allá de la prueba (el kill/wait ocurre dentro del cuerpo del worker).
	h.eval(`
		S2 = false
		enu.task.spawn(function()
			__WK:send("kill")
			__WK:recv()          -- "killed"
			__WK:terminate()
			S2 = true
		end)
	`)
	h.expectEval(`return tostring(S2)`, "true")
}
