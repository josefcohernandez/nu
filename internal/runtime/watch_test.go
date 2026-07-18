package runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests 🔒 de S15 (`enu.fs.watch`, api.md §5, §16). Lógica clave a blindar
// (inventario 🔒, G7):
//
//   - ENTREGA EN LOTES (G7): una ráfaga de N cambios (un `git checkout` simulado)
//     llega como UN solo `fn(events[])`, no como N llamadas —el batching/debounce
//     es lógica nuestra—;
//   - FILTRADO GITIGNORE (G7): un fichero que `.gitignore` ignora NO genera evento;
//     uno no ignorado SÍ;
//   - DEBOUNCE_MS: cambios separados por más de `debounce_ms` llegan en lotes
//     distintos;
//   - `Watcher:stop()`: tras parar, no llegan más lotes; sin fuga de goroutines; un
//     `reload` del plugin dueño también lo suelta (etiquetado por dueño, G2);
//   - kinds correctos (create/modify/remove) en casos simples.
//
// MODELO DE LOS TESTS. El handler de `watch` es síncrono: corre en el estado
// principal bajo el token, entregado por la goroutine de fondo del watcher (como un
// disparo de `every`). Para observarlo desde un `eval` síncrono se usa una task
// "ancla" que mantiene el runtime vivo (con `enu.task.sleep`) mientras los cambios
// del FS ocurren y se entregan; el handler acumula en globals Lua que el test lee
// al final. Los tiempos son holgados (debounce corto, esperas largas) para no ser
// flaky bajo `-race -count=4`; la sincronización fuerte (no solo timing) se logra
// dejando que la task ancla espere a que el contador llegue al objetivo antes de
// parar.

// touch crea o reescribe un fichero con un contenido, fallando el test si no puede.
func touch(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("no se pudo escribir %s: %v", path, err)
	}
}

// TestWatchBatchesBurst es el criterio central de S15 (G7): una ráfaga de muchas
// escrituras —un `git checkout` simulado que toca N ficheros— llega como UN solo
// lote, no como N llamadas al handler. El handler cuenta cuántas VECES se le llamó
// (`batches`) y cuántos eventos vio en total (`total`): con el debounce, `batches`
// debe ser muy pequeño (idealmente 1) y `total` cubrir los ficheros tocados.
func TestWatchBatchesBurst(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()

	const n = 40
	h.eval(`
		batches = 0
		total = 0
		seen = {}
		_w = enu.fs.watch(` + luaStr(dir) + `, { debounce_ms = 60 }, function(events)
			batches = batches + 1
			for _, e in ipairs(events) do
				total = total + 1
				seen[e.path] = e.kind
			end
		end)
	`)

	// Ráfaga: crea N ficheros lo más rápido posible (sin esperas entre ellos), como
	// un checkout que materializa muchos ficheros de golpe. Todos deben caer dentro
	// de la misma ventana de debounce y salir en uno (o muy pocos) lotes.
	for i := 0; i < n; i++ {
		touch(t, filepath.Join(dir, "f"+itoa(i)+".txt"), "x")
	}

	// Da tiempo a que el debounce cierre y el lote se entregue, manteniendo el
	// runtime vivo con una task ancla.
	waitFor(h, `return batches > 0 and total >= 1`)

	got := h.eval(`return tostring(batches)`)
	batches := atoiStr(t, got[0])
	if batches < 1 {
		t.Fatalf("el handler no se llamó; batches=%d", batches)
	}
	// G7: la ráfaga NO debe producir N llamadas. Con debounce de 60 ms y N
	// escrituras inmediatas, debe agruparse en muy pocos lotes (holgura para algún
	// reparto del SO, pero MUCHO menor que N).
	if batches > 3 {
		t.Fatalf("G7: una ráfaga de %d cambios debe llegar en pocos lotes, llegó en %d", n, batches)
	}
	total := atoiStr(t, h.eval(`return tostring(total)`)[0])
	if total < 1 {
		t.Fatalf("G7: el lote no llevó eventos (total=%d)", total)
	}
	h.eval(`_w:stop()`)
}

// TestWatchGitignoreFilters es la pieza de filtrado G7: con `.gitignore` que ignora
// `ignored.txt`, escribir ese fichero NO genera evento; escribir uno no ignorado
// (`kept.txt`) SÍ. El handler registra los paths vistos; al final el ignorado no
// debe aparecer y el conservado sí.
func TestWatchGitignoreFilters(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	touch(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n*.log\n")

	h.eval(`
		seen = {}
		hits = 0
		_w = enu.fs.watch(` + luaStr(dir) + `, { debounce_ms = 40 }, function(events)
			for _, e in ipairs(events) do
				hits = hits + 1
				seen[e.path] = true
			end
		end)
	`)

	// Escribe ambos: el ignorado y el conservado.
	touch(t, filepath.Join(dir, "ignored.txt"), "secreto")
	touch(t, filepath.Join(dir, "debug.log"), "ruido")
	touch(t, filepath.Join(dir, "kept.txt"), "importante")

	// Espera a que el conservado llegue.
	keptPath := filepath.Join(dir, "kept.txt")
	waitFor(h, `return seen[`+luaStr(keptPath)+`] == true`)

	if v := h.eval(`return tostring(seen[` + luaStr(filepath.Join(dir, "ignored.txt")) + `])`); v[0] != "nil" {
		t.Fatalf("G7: un fichero ignorado por .gitignore NO debe generar evento; seen=%q", v[0])
	}
	if v := h.eval(`return tostring(seen[` + luaStr(filepath.Join(dir, "debug.log")) + `])`); v[0] != "nil" {
		t.Fatalf("G7: un *.log ignorado NO debe generar evento; seen=%q", v[0])
	}
	if v := h.eval(`return tostring(seen[` + luaStr(keptPath) + `])`); v[0] != "true" {
		t.Fatalf("G7: un fichero NO ignorado SÍ debe generar evento; seen=%q", v[0])
	}
	h.eval(`_w:stop()`)
}

// TestWatchDebounceSeparatesBatches: dos cambios separados por MÁS de `debounce_ms`
// llegan en lotes DISTINTOS (el debounce cierra el primero antes de que llegue el
// segundo). Con un debounce corto (30 ms) y una espera entre cambios bastante mayor
// (que la task ancla impone), `batches` debe ser >= 2.
func TestWatchDebounceSeparatesBatches(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()

	h.eval(`
		batches = 0
		_w = enu.fs.watch(` + luaStr(dir) + `, { debounce_ms = 25 }, function(events)
			batches = batches + 1
		end)
	`)

	touch(t, filepath.Join(dir, "a.txt"), "1")
	// Espera al primer lote.
	waitFor(h, `return batches >= 1`)
	// Pausa mayor que el debounce, luego un segundo cambio: nuevo lote.
	time.Sleep(120 * time.Millisecond)
	touch(t, filepath.Join(dir, "b.txt"), "2")
	waitFor(h, `return batches >= 2`)

	batches := atoiStr(t, h.eval(`return tostring(batches)`)[0])
	if batches < 2 {
		t.Fatalf("debounce_ms: dos cambios separados deben dar >=2 lotes, dieron %d", batches)
	}
	h.eval(`_w:stop()`)
}

// TestWatchStopNoMoreBatches: tras `Watcher:stop()`, ningún lote más, ni siquiera
// por cambios posteriores. Se toma una foto del contador tras parar, se provoca un
// cambio y se espera holgadamente: el contador no se mueve.
func TestWatchStopNoMoreBatches(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()

	h.eval(`
		batches = 0
		_w = enu.fs.watch(` + luaStr(dir) + `, { debounce_ms = 25 }, function(events)
			batches = batches + 1
		end)
	`)
	touch(t, filepath.Join(dir, "a.txt"), "1")
	waitFor(h, `return batches >= 1`)

	h.eval(`
		_w:stop()
		snapshot = batches
	`)
	// Cambio tras parar: no debe entregarse ningún lote.
	touch(t, filepath.Join(dir, "b.txt"), "2")
	touch(t, filepath.Join(dir, "c.txt"), "3")
	time.Sleep(120 * time.Millisecond)

	snap := atoiStr(t, h.eval(`return tostring(snapshot)`)[0])
	now := atoiStr(t, h.eval(`return tostring(batches)`)[0])
	if now != snap {
		t.Fatalf("tras stop no debe haber más lotes: snapshot=%d, ahora=%d", snap, now)
	}
}

// TestWatchStopNoGoroutineLeak: parar el watcher no deja goroutines colgadas. Se
// mide el número de goroutines antes de arrancar y después de parar (con margen
// para que el runtime estabilice): debe volver al baseline.
func TestWatchStopNoGoroutineLeak(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()

	settle()
	before := runtime.NumGoroutine()

	h.eval(`
		_w = enu.fs.watch(` + luaStr(dir) + `, { debounce_ms = 20 }, function(events) end)
	`)
	touch(t, filepath.Join(dir, "a.txt"), "1")
	time.Sleep(60 * time.Millisecond)
	h.eval(`_w:stop()`)

	// Tras parar, la goroutine de `run` debe retornar y el watcher del SO cerrarse.
	settle()
	after := runtime.NumGoroutine()
	// Holgura de 1 por ruido del scheduler de Go; lo que se descarta es una fuga
	// real (la goroutine de `run` viva para siempre subiría el conteo de forma
	// estable).
	if after > before+1 {
		t.Fatalf("posible fuga de goroutines tras stop: antes=%d, después=%d", before, after)
	}
}

// TestWatchKinds comprueba los `kind` en casos simples: crear un fichero da
// `create`, modificarlo da `modify`, borrarlo da `remove`. Se hace en pasos
// separados por debounce para que cada operación caiga en su propio lote y el kind
// sea inequívoco.
func TestWatchKinds(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")

	// Registra el CONJUNTO de kinds vistos por path: el SO puede agrupar create+write
	// en la misma ventana de debounce (un fichero nuevo se crea y se escribe), así
	// que "el último kind" no es estable —lo robusto es "¿se vio este kind alguna
	// vez para este path?"—.
	h.eval(`
		kinds = {}  -- kinds[path] = { create=true, modify=true, ... }
		_w = enu.fs.watch(` + luaStr(dir) + `, { debounce_ms = 25 }, function(events)
			for _, e in ipairs(events) do
				kinds[e.path] = kinds[e.path] or {}
				kinds[e.path][e.kind] = true
			end
		end)
	`)
	saw := `function(p, k) local s = kinds[p]; return s ~= nil and s[k] == true end`

	// create: un fichero nuevo debe verse como create (acompañado o no de modify).
	touch(t, target, "v1")
	waitFor(h, `local saw = `+saw+`; return saw(`+luaStr(target)+`, "create")`)

	// modify: reescribir un fichero existente da modify (separado del create por
	// > debounce, en su propio lote).
	time.Sleep(120 * time.Millisecond)
	touch(t, target, "v2-mas-largo")
	waitFor(h, `local saw = `+saw+`; return saw(`+luaStr(target)+`, "modify")`)

	// remove: borrar da remove.
	time.Sleep(120 * time.Millisecond)
	if err := os.Remove(target); err != nil {
		t.Fatalf("no se pudo borrar %s: %v", target, err)
	}
	waitFor(h, `local saw = `+saw+`; return saw(`+luaStr(target)+`, "remove")`)

	h.eval(`_w:stop()`)
}

// TestWatchRecursive: con `recursive = true`, un cambio en un SUBdirectorio (creado
// al arrancar) genera evento; y un subdirectorio creado AL VUELO también empieza a
// vigilarse (alcance documentado de recursive).
func TestWatchRecursive(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	h.eval(`
		seen = {}
		_w = enu.fs.watch(` + luaStr(dir) + `, { recursive = true, debounce_ms = 30 }, function(events)
			for _, e in ipairs(events) do seen[e.path] = true end
		end)
	`)

	// Cambio en el subdir existente.
	deep := filepath.Join(sub, "x.txt")
	touch(t, deep, "1")
	waitFor(h, `return seen[`+luaStr(deep)+`] == true`)

	// Subdir creado al vuelo + cambio dentro de él.
	time.Sleep(80 * time.Millisecond)
	sub2 := filepath.Join(dir, "nuevo")
	if err := os.Mkdir(sub2, 0o755); err != nil {
		t.Fatalf("mkdir nuevo: %v", err)
	}
	// Da un respiro a que el watcher añada el dir nuevo antes de escribir dentro.
	time.Sleep(80 * time.Millisecond)
	deep2 := filepath.Join(sub2, "y.txt")
	touch(t, deep2, "2")
	waitFor(h, `return seen[`+luaStr(deep2)+`] == true`)

	h.eval(`_w:stop()`)
}

// TestWatchFromTaskWorks: `watch` es "solo estado principal" (§16) en el sentido de
// "no en workers" (donde ni se registra, S34), NO "no en tasks". Las tasks corren
// en el event loop del estado principal y comparten el `enu` global, así que `watch`
// —como `every`/`on`, que tampoco distinguen host de task— es invocable DESDE una
// task: registra el `Watcher` síncronamente (no suspende) y los cambios llegan
// luego como lotes. Se arranca el watch dentro de una task ancla que mantiene el
// runtime vivo; un cambio de fichero debe producir al menos un lote.
func TestWatchFromTaskWorks(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	// La task arranca el watch (debe funcionar, no lanzar EINVAL) y luego espera, sin
	// suspender en el `watch` mismo, a que el handler reciba al menos un lote.
	h.eval(`
		batches = 0
		enu.task.spawn(function()
			_w = enu.fs.watch(` + luaStr(dir) + `, { debounce_ms = 30 }, function(events)
				batches = batches + 1
			end)
			local intentos = 0
			while batches < 1 and intentos < 200 do
				enu.task.sleep(10)
				intentos = intentos + 1
			end
		end)
	`)
	// Provoca un cambio; la task ancla sigue viva haciendo polling hasta que el lote
	// llega (sincronización por condición, no solo timing → no flaky).
	touch(t, filepath.Join(dir, "a.txt"), "1")
	waitFor(h, `return batches >= 1`)

	got := atoiStr(t, h.eval(`return tostring(batches)`)[0])
	if got < 1 {
		t.Fatalf("watch desde una task debe funcionar y entregar al menos un lote; batches=%d", got)
	}
	h.eval(`_w:stop()`)
}

// TestWatchNonexistentPath: vigilar un path inexistente → ENOENT (el path debe
// existir para observarlo).
func TestWatchNonexistentPath(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`enu.fs.watch("/no/existe/jamas/nunca", function() end)`)
	if se.Code != CodeENOENT {
		t.Fatalf("watch de inexistente: code=%q, want %q", se.Code, CodeENOENT)
	}
}

// TestWatchStopIdempotent: parar dos veces el mismo watcher no entra en pánico
// (cerrar `stopCh` dos veces lo haría; `stopOnce`/`stopWatcher` lo evitan).
func TestWatchStopIdempotent(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.eval(`
		_w = enu.fs.watch(` + luaStr(dir) + `, function(events) end)
		_w:stop()
		_w:stop()
		ok = true
	`)
	h.expectEval(`return tostring(ok)`, "true")
}

// TestWatchReloadReleasesHandle (G2 + S15): un `Watcher` se etiqueta por dueño y
// `enu.plugin.reload` lo suelta —"reload no deja handlers huérfanos"—. Se comprueba
// a nivel Go: un plugin que arranca un `watch` en su `init.lua` registra un handle
// bajo su nombre; tras `reload`, el handle viejo se soltó (su goroutine paró) y el
// init re-creó otro: el conteo del registro queda en 1, no en 2 (sin fuga).
func TestWatchReloadReleasesHandle(t *testing.T) {
	dir := t.TempDir()
	pdir := filepath.Join(dir, "P")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	touch(t, filepath.Join(pdir, "plugin.toml"), "name = \"P\"\n")
	// El init del plugin arranca un watcher sobre su propio directorio.
	touch(t, filepath.Join(pdir, "init.lua"),
		`_pw = enu.fs.watch(`+luaStr(pdir)+`, function(events) end)`)

	rt := New(WithDataDir(t.TempDir()), WithConfigDir(t.TempDir()), WithPluginDir(dir))
	t.Cleanup(rt.Close)
	h := &harness{t: t, rt: rt}
	// El etiquetado de handles por dueño (G2) se sondea con `countOwnerHandles`, que
	// consulta el registro del preludio de reload en wasm (`__count_owner`) —la misma
	// vía que usan los TestReload*—.
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	// Tras el arranque, P tiene exactamente 1 handle (el watcher).
	if got := countOwnerHandles(h, "P"); got != 1 {
		t.Fatalf("tras boot, P debe tener 1 handle (el watcher); hay %d", got)
	}

	// Recarga P dentro de una task (reload es ⏸): el watcher viejo se suelta y el
	// init re-creado arranca otro. Sin etiquetado/untrack correcto, quedarían 2.
	h.eval(reloadSpawn(`enu.plugin.reload("P")`))
	if got := countOwnerHandles(h, "P"); got != 1 {
		t.Fatalf("G2: tras reload, P debe tener 1 handle (sin huérfanos); hay %d", got)
	}

	// El watcher vigente (re-creado por el init) puede pararse a mano: `untrack` baja
	// el registro de P a 0 (sin fuga). El init dejó `_pw` global con el handle vivo.
	h.eval(`_pw:stop()`)
	if got := countOwnerHandles(h, "P"); got != 0 {
		t.Fatalf("tras stop del watcher, P debe quedar en 0 handles (untrack); hay %d", got)
	}
}

// TestWatchConcurrentDeliveries (auditoría post-M17): varios watchers vivos
// entregando lotes desde GOROUTINES DE FONDO DISTINTAS mientras el estado principal
// también trabaja (la task ancla de waitFor entra a la VM desde el driver). El mutex
// de la Instance (`mu`, con `slotMu` para el par ranura+Eval de EmitEvent) es la
// única barrera que serializa esas entradas concurrentes a la VM: este test, corrido
// bajo `-race` como el resto de la suite, delata cualquier "optimización" que lo
// debilite. No afirma orden ni conteo exacto de lotes (eso ya lo cubren los otros
// tests de S15): solo que TODOS los watchers entregan y ninguna entrada corre una
// carrera.
func TestWatchConcurrentDeliveries(t *testing.T) {
	h := newHarness(t)
	const nw = 4
	dirs := make([]string, nw)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}

	// nw watchers con debounce corto, cada uno con su contador propio: lotes
	// pequeños y frecuentes maximizan el solape de las entregas.
	for i, d := range dirs {
		h.eval(`
			c` + itoa(i) + ` = 0
			_w` + itoa(i) + ` = enu.fs.watch(` + luaStr(d) + `, { debounce_ms = 15 }, function(events)
				c` + itoa(i) + ` = c` + itoa(i) + ` + 1
			end)
		`)
	}

	// Ráfagas concurrentes: una goroutine Go por directorio, varias rondas separadas
	// por más que el debounce para que cada watcher cierre y entregue VARIOS lotes,
	// con las goroutines de deliver de los nw watchers compitiendo por mu/slotMu.
	// Las escrituras son best-effort (no se puede t.Fatalf fuera de la goroutine del
	// test); un fallo real lo delataría el waitFor de abajo.
	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			for round := 0; round < 5; round++ {
				for f := 0; f < 3; f++ {
					_ = os.WriteFile(filepath.Join(d, "r"+itoa(round)+"f"+itoa(f)+".txt"), []byte("x"), 0o644)
				}
				time.Sleep(40 * time.Millisecond)
			}
		}(d)
	}
	wg.Wait()

	// Todos los watchers deben haber entregado al menos un lote (la condición corre
	// dentro del estado principal, intercalada con las entregas pendientes).
	waitFor(h, `return c0 >= 1 and c1 >= 1 and c2 >= 1 and c3 >= 1`)

	for i := 0; i < nw; i++ {
		h.eval(`_w` + itoa(i) + `:stop()`)
	}
}

// --- helpers de los tests de watch ---

// luaStr formatea una string Go como literal Lua con comillas dobles. Los paths de
// `t.TempDir()` no llevan comillas ni barras invertidas ni saltos de línea, así que
// no hace falta escapar —y evita la ambigüedad `t[[[...]]]` de los corchetes largos
// pegados a un índice—.
func luaStr(s string) string {
	return `"` + s + `"`
}

// itoa/atoiStr: enteros sin importar strconv en cada sitio (helpers locales del
// test, mínimos).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func atoiStr(t *testing.T, s string) int {
	t.Helper()
	s = strings.TrimSpace(s)
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("atoiStr: %q no es un entero", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// waitFor mantiene el runtime vivo con una task ancla que hace polling de una
// condición Lua (`cond` debe ser un `return <bool>`), durmiendo entre intentos.
// Como la task ancla mantiene `live > 0`, `EvalString` no vuelve hasta que la
// condición se cumple (o se agota el presupuesto de intentos) —y mientras, las
// entregas de lote del watcher (handlers síncronos) se intercalan en el loop—.
// Falla el test si no se cumple en el plazo (no flaky: el plazo es holgado).
func waitFor(h *harness, cond string) {
	h.t.Helper()
	h.eval(`
		enu.task.spawn(function()
			local intentos = 0
			while not (function() ` + cond + ` end)() and intentos < 200 do
				enu.task.sleep(10)
				intentos = intentos + 1
			end
			_waitfor_ok = (function() ` + cond + ` end)()
		end)
	`)
	if got := h.eval(`return tostring(_waitfor_ok)`); got[0] != "true" {
		h.t.Fatalf("waitFor: la condición no se cumplió a tiempo: %s", cond)
	}
}

// settle da un respiro al scheduler de Go para que las goroutines transitorias
// terminen antes de contar (para el test de fugas).
func settle() {
	for i := 0; i < 5; i++ {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
}
