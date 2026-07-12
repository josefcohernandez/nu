package runtime

// Tests 🔒 de `nu.plugin.reload` (S13, api.md §14). Lógica clave a blindar
// (inventario 🔒, G2 — "reload no deja handlers huérfanos"):
//
//   - tras `reload`, las suscripciones/timers VIEJOS del plugin ya NO disparan;
//     solo corren los NUEVOS que registró el `init.lua` re-ejecutado;
//   - `core:plugin.unload` se emite ANTES de soltar, y una extensión puede
//     engancharse para limpiar lo suyo;
//   - la caché de `require` del plugin se vacía (un módulo `lua/` que cambió se
//     re-ejecuta, no se sirve cacheado);
//   - el etiquetado es por dueño: recargar A no suelta los handles de B;
//   - `Sub:cancel()`/`Timer:stop()` a mano no dejan basura en el registro (sin fuga).
//
// `reload` es ⏸: los snippets la invocan dentro de una `nu.task.spawn(...)` (como
// el resto de funciones suspendientes). `EvalString` espera (waitIdle) a que la
// task termine antes de devolver, así que el efecto del reload es observable tras
// `h.eval(...)`.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// reloadSpawn envuelve `body` en una task y devuelve un snippet que la lanza. Es
// el azúcar para ejercitar `nu.plugin.reload` (⏸) desde un `eval` síncrono.
func reloadSpawn(body string) string {
	return "nu.task.spawn(function()\n" + body + "\nend)"
}

// TestReloadNoDejaHandlersHuerfanos (🔒, G2): un plugin registra en su init.lua una
// suscripción de eventos y un `every`; tras `reload`, un `emit` NO invoca la
// suscripción vieja y el timer viejo no sigue contando —solo cuenta lo que el init
// re-ejecutado vuelva a registrar—. Es el corazón de la sesión: contamos
// invocaciones antes y después.
func TestReloadNoDejaHandlersHuerfanos(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()

	// El init incrementa un contador por evento "tic" y registra un `every`. Cada
	// carga de init crea una suscripción NUEVA; si las viejas siguieran vivas, un
	// solo `emit` invocaría a todas las acumuladas.
	initLua := `
_recibidos = _recibidos or 0
nu.events.on("p:tic", function() _recibidos = _recibidos + 1 end)
_timers = (_timers or 0) + 1
nu.task.every(1000, function() end)
`
	writePlugin(t, root, "P", "1.0", nil, initLua)
	h := newBootedHarness(t, root, cfg)

	// Tras el arranque: un emit invoca la ÚNICA suscripción del init original.
	h.eval(`nu.events.emit("p:tic")`)
	h.expectEval(`return _recibidos`, "1")

	// Recarga el plugin DOS veces. Si las suscripciones viejas quedaran huérfanas,
	// tras dos reloads habría tres suscripciones y un emit sumaría 3.
	h.eval(reloadSpawn(`nu.plugin.reload("P")`))
	h.eval(reloadSpawn(`nu.plugin.reload("P")`))

	// _recibidos sigue en 1 (el reload no re-emite "p:tic"); un emit nuevo debe
	// sumar exactamente 1, no 3: solo vive la suscripción del último init.
	h.expectEval(`return _recibidos`, "1")
	h.eval(`nu.events.emit("p:tic")`)
	h.expectEval(`return _recibidos`, "2")

	// El init corrió 3 veces (arranque + 2 reloads): cada vez creó una suscripción
	// y un `every`. Pero los viejos se soltaron en cada reload; solo deben quedar
	// los DOS handles del último init (la sub + el timer), no los acumulados.
	// (Comprobación a nivel Go: el etiquetado por dueño.)
	if got := countOwnerHandles(h, "P"); got != 2 {
		t.Fatalf("tras 2 reloads deben quedar 2 handles vivos de P (sub + every del último init); hay %d", got)
	}
}

// TestReloadEmiteUnload (🔒, G2): `core:plugin.unload` se emite (antes de soltar
// los handles) con el nombre del plugin, y una extensión enganchada puede limpiar
// lo suyo. Aquí el "registro externo" es una variable global que el handler de
// unload limpia.
func TestReloadEmiteUnload(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "P", "1.0", nil, `_marca = "viva"`)
	h := newBootedHarness(t, root, cfg)

	// Una extensión (aquí, el chunk del usuario) se engancha a unload para limpiar
	// SU registro al descargarse P.
	h.eval(`
_unload_visto = nil
nu.events.on("core:plugin.unload", function(ev) _unload_visto = ev.name; _marca = nil end)
`)
	h.eval(reloadSpawn(`nu.plugin.reload("P")`))

	// El handler corrió con el nombre correcto, y limpió su registro.
	h.expectEval(`return _unload_visto`, "P")
	// El init re-ejecutado volvió a poner _marca: el unload ocurrió ANTES del init.
	h.expectEval(`return _marca`, "viva")
}

// TestReloadVaciaCacheRequire (🔒): el init del plugin `require`a un módulo suyo;
// si ese módulo cambia en disco, tras `reload` el init ve la versión nueva (el
// módulo se re-ejecuta, no queda cacheado en package.loaded).
func TestReloadVaciaCacheRequire(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	dir := writePlugin(t, root, "P", "1.0", nil, `_valor = require("datos").v`)

	// Módulo `lua/datos.lua` con la versión 1.
	luaDir := filepath.Join(dir, "lua")
	if err := os.MkdirAll(luaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modPath := filepath.Join(luaDir, "datos.lua")
	if err := os.WriteFile(modPath, []byte(`return { v = "v1" }`), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newBootedHarness(t, root, cfg)
	h.expectEval(`return _valor`, "v1")

	// Cambia el módulo en disco a la versión 2 y recarga: el init debe ver "v2".
	if err := os.WriteFile(modPath, []byte(`return { v = "v2" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.eval(reloadSpawn(`nu.plugin.reload("P")`))
	h.expectEval(`return _valor`, "v2")
}

// TestReloadEtiquetadoPorDueno (🔒, G2): un handle creado por el plugin A NO se
// suelta al recargar el plugin B (aislamiento por dueño). Recargar B no debe tocar
// la suscripción de A.
func TestReloadEtiquetadoPorDueno(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "A", "1.0", nil, `nu.events.on("ev", function() _a = (_a or 0) + 1 end)`)
	writePlugin(t, root, "B", "1.0", nil, `nu.events.on("ev", function() _b = (_b or 0) + 1 end)`)
	h := newBootedHarness(t, root, cfg)

	// Recarga B: su suscripción se suelta y se vuelve a crear; la de A queda intacta.
	h.eval(reloadSpawn(`nu.plugin.reload("B")`))

	// Un emit debe invocar A (intacta) y B (re-creada): cada una una vez. Si reload
	// hubiera soltado la de A, _a no se incrementaría; si hubiera dejado huérfana la
	// vieja de B, _b sería 2.
	h.eval(`_a = 0; _b = 0; nu.events.emit("ev")`)
	h.expectEval(`return _a`, "1")
	h.expectEval(`return _b`, "1")

	// A no se recargó: sigue con su único handle. B también, tras el reload.
	if got := countOwnerHandles(h, "A"); got != 1 {
		t.Fatalf("A debe conservar su handle tras recargar B; hay %d", got)
	}
	if got := countOwnerHandles(h, "B"); got != 1 {
		t.Fatalf("B debe tener 1 handle tras su reload; hay %d", got)
	}
}

// TestReloadCancelStopSinFuga (🔒): `Sub:cancel()` y `Timer:stop()` a mano
// desregistran del registro de handles por dueño (sin fuga). Si dejaran basura, el
// recuento del dueño no bajaría.
func TestReloadCancelStopSinFuga(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	// El init crea una Sub y un Timer y los guarda en globales para cancelarlos
	// luego desde el chunk del usuario.
	writePlugin(t, root, "P", "1.0", nil, `
_sub = nu.events.on("x", function() end)
_timer = nu.task.every(1000, function() end)
`)
	h := newBootedHarness(t, root, cfg)

	if got := countOwnerHandles(h, "P"); got != 2 {
		t.Fatalf("P debe tener 2 handles tras el init (sub + timer); hay %d", got)
	}

	// Cancela/para a mano: el registro debe quedar a cero, sin fuga.
	h.eval(`_sub:cancel(); _timer:stop()`)
	if got := countOwnerHandles(h, "P"); got != 0 {
		t.Fatalf("tras cancel+stop el registro de P debe quedar vacío (sin fuga); hay %d", got)
	}

	// Cancelar/parar dos veces es inocuo (idempotente, sin pánico).
	h.eval(`_sub:cancel(); _timer:stop()`)
	if got := countOwnerHandles(h, "P"); got != 0 {
		t.Fatalf("cancel/stop repetidos no deben corromper el registro; hay %d", got)
	}
}

// TestReloadOnceAutoCancelSinFuga (🔒, G2): un `nu.events.once` que se DISPARA se
// auto-cancela, y esa auto-cancelación debe sacarlo del registro de handles por
// dueño igual que lo hace el `cancel` a mano —si no, un dueño de vida larga (aquí
// "user") que use `once` repetidamente acumularía handles muertos en
// `ownerHandles` (fuga que viola el invariante 🔒 "sin fuga en el registro")—.
//
// El chunk del usuario corre con dueño "user" (S11). Registramos un `once`, lo
// disparamos con un `emit` y comprobamos que el recuento de "user" vuelve a 0, no
// a 1. También cubrimos: (a) un `once` NO disparado y cancelado a mano tampoco
// fuga; (b) un `once` ya disparado antes de un reload no provoca doble-libre —su
// `release()` es no-op idempotente—.
func TestReloadOnceAutoCancelSinFuga(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "P", "1.0", nil, "")
	h := newBootedHarness(t, root, cfg)

	// Caso 1: un `once` que se dispara se desregistra del registro de su dueño.
	h.eval(`nu.events.once("u:tic", function() _visto = (_visto or 0) + 1 end)`)
	if got := countOwnerHandles(h, "user"); got != 1 {
		t.Fatalf("tras registrar el once debe haber 1 handle de user; hay %d", got)
	}
	h.eval(`nu.events.emit("u:tic")`)
	h.expectEval(`return _visto`, "1")
	if got := countOwnerHandles(h, "user"); got != 0 {
		t.Fatalf("tras dispararse el once, el registro de user debe quedar a 0 (sin fuga); hay %d", got)
	}

	// Un segundo emit no lo re-ejecuta (once consumido) y no reintroduce fuga.
	h.eval(`nu.events.emit("u:tic")`)
	h.expectEval(`return _visto`, "1")
	if got := countOwnerHandles(h, "user"); got != 0 {
		t.Fatalf("el registro de user debe seguir a 0 tras un segundo emit; hay %d", got)
	}

	// Caso 2: un `once` NO disparado y cancelado a mano tampoco fuga.
	h.eval(`_s = nu.events.once("u:noviene", function() end)`)
	if got := countOwnerHandles(h, "user"); got != 1 {
		t.Fatalf("el once sin disparar debe estar registrado; hay %d", got)
	}
	h.eval(`_s:cancel()`)
	if got := countOwnerHandles(h, "user"); got != 0 {
		t.Fatalf("tras cancelar a mano un once sin disparar, user debe quedar a 0; hay %d", got)
	}
}

// TestReloadOnceDisparadoAntesDeReload (🔒, G2): un `once` registrado por un plugin
// que se dispara ANTES de recargarlo no provoca doble-libre ni corrompe el reload.
// Tras dispararse, ya se auto-desregistró; el `reload` posterior no encuentra ese
// handle, y su `release()` (no-op idempotente) no rompería nada aunque lo
// encontrara. El plugin queda con el handle que su init re-ejecutado registra.
func TestReloadOnceDisparadoAntesDeReload(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	// El init registra un `once` y un `on` persistente. El `once` se consume con un
	// emit antes del reload; el `on` permanece.
	writePlugin(t, root, "P", "1.0", nil, `
nu.events.once("p:una", function() _una = (_una or 0) + 1 end)
nu.events.on("p:siempre", function() end)
`)
	h := newBootedHarness(t, root, cfg)

	// Tras el init: 2 handles (once + on).
	if got := countOwnerHandles(h, "P"); got != 2 {
		t.Fatalf("tras el init debe haber 2 handles de P (once + on); hay %d", got)
	}

	// Dispara el `once`: se consume y se auto-desregistra; queda solo el `on`.
	h.eval(`nu.events.emit("p:una")`)
	h.expectEval(`return _una`, "1")
	if got := countOwnerHandles(h, "P"); got != 1 {
		t.Fatalf("tras dispararse el once debe quedar 1 handle de P (el on); hay %d", got)
	}

	// Recarga: no debe haber doble-libre del once ya consumido. El init re-ejecutado
	// vuelve a crear 2 handles (once + on), no acumula los viejos.
	h.eval(reloadSpawn(`nu.plugin.reload("P")`))
	if got := countOwnerHandles(h, "P"); got != 2 {
		t.Fatalf("tras el reload, P debe tener 2 handles del init re-ejecutado; hay %d", got)
	}

	// El `once` re-creado sigue funcionando: un emit lo dispara una vez más.
	h.eval(`nu.events.emit("p:una")`)
	h.expectEval(`return _una`, "2")
}

// TestReloadDesconocido (🔒): recargar un plugin que no está cargado es `EINVAL`
// accionable que nombra el plugin.
func TestReloadDesconocido(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "P", "1.0", nil, "")
	h := newBootedHarness(t, root, cfg)

	// La task que llama a reload con un nombre inexistente lanza EINVAL; lo
	// capturamos con pcall dentro de la propia task y lo exponemos en un global.
	// `eval` espera (waitIdle) a que la task termine, así que `_err` ya está puesto.
	h.eval(`
_err = nil
nu.task.spawn(function()
  local ok, e = pcall(function() nu.plugin.reload("fantasma") end)
  _err = e
end)
`)
	code := h.eval(`return _err.code`)[0]
	msg := h.eval(`return _err.message`)[0]
	if code != string(CodeEINVAL) {
		t.Fatalf("código: got %q, want EINVAL", code)
	}
	if !strings.Contains(msg, "fantasma") {
		t.Fatalf("el mensaje no nombra el plugin desconocido: %q", msg)
	}
}

// TestReloadFueraDeTask (🔒): `nu.plugin.reload` es ⏸; llamarla fuera de una task
// (en el chunk principal) es `EINVAL`, como el resto de suspendientes (§1.3).
func TestReloadFueraDeTask(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "P", "1.0", nil, "")
	h := newBootedHarness(t, root, cfg)

	se := h.evalErr(`nu.plugin.reload("P")`)
	if se.Code != CodeEINVAL {
		t.Fatalf("código: got %q, want EINVAL", se.Code)
	}
	if !strings.Contains(se.Message, "task") {
		t.Fatalf("el mensaje debe explicar que es ⏸ (solo en task): %q", se.Message)
	}
}

// TestReloadMataProcesosDelPlugin (G2, 🔒): un `nu.proc.spawn` del `init.lua` de
// un plugin NO sobrevive a recargarlo. El registro Lua del preludio solo conoce
// subs y timers; los procesos viven en el registro Go por dueño, que el reload
// también libera —sin esto, el proceso quedaba huérfano (con sus pipes) hasta
// Runtime.Close, contra el contrato explícito de proc.go—.
func TestReloadMataProcesosDelPlugin(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	writePlugin(t, root, "P", "1.0", nil, `nu.proc.spawn({ "sleep", "100" })`)
	h := newBootedHarness(t, root, cfg)

	snapshot := func() (int, int) {
		h.rt.sched.mu.Lock()
		defer h.rt.sched.mu.Unlock()
		pid := 0
		for p := range h.rt.sched.procs {
			pid = p.cmd.Process.Pid
		}
		return len(h.rt.sched.procs), pid
	}
	n, oldPid := snapshot()
	if n != 1 || oldPid == 0 {
		t.Fatalf("tras el boot debe haber 1 proc del init de P (hay %d)", n)
	}

	h.eval(reloadSpawn(`nu.plugin.reload("P")`))

	// El init re-ejecutado lanza OTRO sleep; el viejo debe morir (SIGKILL del
	// release por dueño) y salir del mapa de vivos. El reaper de fondo recoge el
	// zombi, así que pidAlive(oldPid) acaba en false.
	deadline := time.Now().Add(3 * time.Second)
	for {
		n, newPid := snapshot()
		if n == 1 && newPid != oldPid && !pidAlive(oldPid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("el proc del plugin sobrevivió al reload: procs=%d, viejo vivo=%v", n, pidAlive(oldPid))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestReloadParaWatchersDelPlugin (G2, 🔒): un `nu.fs.watch` del `init.lua` de un
// plugin se corta al recargarlo — goroutine y fd de fsnotify incluidos —, y el
// watcher viejo sale del mapa de vivos del scheduler (antes sobrevivía a reload
// Y a Runtime.Close, emitiendo a un evento ya sin suscriptores).
func TestReloadParaWatchersDelPlugin(t *testing.T) {
	root := t.TempDir()
	cfg := t.TempDir()
	watched := t.TempDir()
	writePlugin(t, root, "P", "1.0", nil, `nu.fs.watch("`+watched+`", function() end)`)
	h := newBootedHarness(t, root, cfg)

	oldW := func() *wasmWatcher {
		h.rt.sched.mu.Lock()
		defer h.rt.sched.mu.Unlock()
		for w := range h.rt.sched.watchers {
			return w
		}
		return nil
	}()
	if oldW == nil {
		t.Fatal("tras el boot debe haber 1 watcher del init de P")
	}

	h.eval(reloadSpawn(`nu.plugin.reload("P")`))

	// El viejo quedó parado (stopCh cerrado) y fuera del mapa; el init
	// re-ejecutado registró uno nuevo.
	select {
	case <-oldW.stopCh:
	default:
		t.Fatal("el watcher viejo sigue corriendo tras el reload")
	}
	h.rt.sched.mu.Lock()
	_, oldTracked := h.rt.sched.watchers[oldW]
	n := len(h.rt.sched.watchers)
	h.rt.sched.mu.Unlock()
	if oldTracked || n != 1 {
		t.Fatalf("mapa de watchers tras reload: viejo dentro=%v, total=%d (want fuera y 1)", oldTracked, n)
	}
}

// countOwnerHandles devuelve cuántos handles vivos hay registrados para `owner` en
// el registro por dueño —la lógica 🔒 del etiquetado (S13, G2)—. El registro vive en
// el preludio de reload (preludioReload) del estado wasm, así que se consulta con
// `__count_owner(owner)` vía un eval.
func countOwnerHandles(h *harness, owner string) int {
	got := h.eval(`return __count_owner("` + owner + `")`)
	if len(got) != 1 {
		h.t.Fatalf("__count_owner(%q) devolvió %d valores, want 1", owner, len(got))
	}
	s := strings.TrimSpace(got[0])
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	// Por si el número cruza como float ("2.0").
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		h.t.Fatalf("__count_owner(%q) no devolvió un número: %q", owner, s)
	}
	return int(f)
}
