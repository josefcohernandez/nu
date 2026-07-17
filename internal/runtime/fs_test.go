package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Tests de S14 (api.md §5): `enu.fs`. Sesión 🔒 —la lógica clave a blindar
// (inventario del plan): **escritura atómica** (temporal+rename, sin residuo,
// sobreescribe); **G17** `write{exclusive}` = `O_EXCL` → `EEXIST` sobre
// existente, crea sobre inexistente; **`stat` de inexistente → `nil`**, NO lanza;
// `remove` de dir no vacío sin `recursive` → error, con `recursive` → borra;
// `read`/`list` de inexistente → `ENOENT`/error.
//
// Hay dos niveles: tests Go directos sobre las funciones puras (`writeAtomic`,
// `writeExclusive`, `copyFile`) que blindan los invariantes 🔒 sin pasar por el
// scheduler, y un test de snippet Lua que ejercita la superficie completa de
// extremo a extremo (read/write/append/stat/list/mkdir/remove/rename/copy/
// tmpdir/cwd) por el puente ⏸ real. La suite corre con `-race -count=4`: las
// primitivas ⏸ hacen su IO en goroutines de fondo, así que cualquier toque a Lua
// fuera del token saltaría aquí.

// --- Lógica 🔒: escritura atómica ---

// TestWriteAtomicLeavesNoResidue blinda que la escritura atómica deja el
// contenido correcto y **no deja el fichero temporal residual** en el directorio
// destino: tras un `write`, el dir destino contiene SOLO el fichero final.
func TestWriteAtomicLeavesNoResidue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := writeAtomic(path, []byte("contenido"), nil); err != nil {
		t.Fatalf("writeAtomic falló: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no se pudo leer el destino: %v", err)
	}
	if string(got) != "contenido" {
		t.Fatalf("contenido: got %q, want %q", got, "contenido")
	}

	// No debe quedar NINGÚN temporal en el dir: solo el fichero final.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no se pudo listar el dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "out.txt" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("residuo de temporal: el dir contiene %v, want solo [out.txt]", names)
	}
}

// TestWriteAtomicOverwrites blinda que `write` sobre un fichero existente lo
// **sobreescribe** entero (el rename reemplaza el inode), dejando el contenido
// nuevo sin restos del viejo.
func TestWriteAtomicOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := os.WriteFile(path, []byte("viejo y muy largo"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := writeAtomic(path, []byte("nuevo"), nil); err != nil {
		t.Fatalf("writeAtomic falló: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "nuevo" {
		t.Fatalf("sobreescritura: got %q, want %q", got, "nuevo")
	}
}

// TestWriteAtomicRespectsUmask blinda que la escritura atómica de un fichero
// **nuevo** aplica el umask del proceso en la creación (como `append`/`copy`, que
// usan `OpenFile`), y NO deja un fichero world-readable: con umask 077, un `write`
// de `fsFilePerm` (0644) debe quedar en 0600. Es la regresión de seguridad que un
// `CreateTemp`+`Chmod` reintroducía (el `Chmod` se salta el umask). El test toca
// el umask del proceso, global: NO puede correr en paralelo y lo restaura al salir.
func TestWriteAtomicRespectsUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	dir := t.TempDir()
	path := filepath.Join(dir, "secreto.txt")
	if err := writeAtomic(path, []byte("dato sensible"), nil); err != nil {
		t.Fatalf("writeAtomic falló: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("umask 077 sobre write nuevo: modo got %o, want 0600 (no world-readable)", got)
	}
}

// TestWriteAtomicPreservesExistingMode blinda que al **sobrescribir** un fichero
// existente la escritura atómica conserva el modo previo del destino (igual que las
// rutas `OpenFile`, que no tocan los permisos de un fichero que ya existe), en vez
// de forzar `fsFilePerm`. Un fichero en 0640 debe seguir en 0640 tras el `write`.
func TestWriteAtomicPreservesExistingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("viejo"), 0o640); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Aseguramos el modo exacto pese al umask del entorno de test.
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("setup chmod: %v", err)
	}
	if err := writeAtomic(path, []byte("nuevo"), nil); err != nil {
		t.Fatalf("writeAtomic falló: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("sobrescritura debe preservar el modo previo: got %o, want 0640", got)
	}
}

// --- Lógica 🔒: G57, opts.mode (chmod explícito, no recortado por el umask) ---

// TestWriteAtomicModeIndependentOfUmask blinda G57 sobre el camino atómico: con
// `mode` explícito el fichero resultante tiene EXACTAMENTE ese modo,
// **independientemente del umask del proceso** —ni recortado hacia abajo ni dejado
// world-readable—. Se comprueba en las dos direcciones que un `umask` mal razonado
// rompería: (a) umask laxo (022, que dejaría un default 0644 legible por otros) +
// `mode = 0600` → 0600; (b) umask estricto (077, que recortaría 0644→0600) +
// `mode = 0644` → 0644. Ambas exigen que el modo se fije con `Chmod` (bits tal
// cual), no con el `perm` de `OpenFile` que el SO tamiza por el umask. Toca el
// umask del proceso (global): NO corre en paralelo y lo restaura al salir.
func TestWriteAtomicModeIndependentOfUmask(t *testing.T) {
	dir := t.TempDir()

	// (a) umask laxo + mode restrictivo: el chmod debe bajar a 0600 pese al umask 022.
	old := syscall.Umask(0o022)
	p1 := filepath.Join(dir, "a.txt")
	mode600 := os.FileMode(0o600)
	if err := writeAtomic(p1, []byte("secreto"), &mode600); err != nil {
		syscall.Umask(old)
		t.Fatalf("writeAtomic con mode falló: %v", err)
	}
	syscall.Umask(old)
	if got := statPerm(t, p1); got != 0o600 {
		t.Fatalf("umask 022 + mode 0600: got %o, want 0600 (mode explícito, no world-readable)", got)
	}

	// (b) umask estricto + mode permisivo: el chmod debe SUBIR a 0644 pese al umask 077.
	old = syscall.Umask(0o077)
	p2 := filepath.Join(dir, "b.txt")
	mode644 := os.FileMode(0o644)
	if err := writeAtomic(p2, []byte("publico"), &mode644); err != nil {
		syscall.Umask(old)
		t.Fatalf("writeAtomic con mode falló: %v", err)
	}
	syscall.Umask(old)
	if got := statPerm(t, p2); got != 0o644 {
		t.Fatalf("umask 077 + mode 0644: got %o, want 0644 (mode NO recortado por el umask)", got)
	}
}

// TestWriteAtomicModeOverridesExistingMode blinda que, en la sobrescritura, un
// `mode` explícito GANA sobre la preservación del modo previo del destino (G57): un
// 0640 preexistente que se reescribe con `mode = 0600` queda en 0600, no en 0640.
func TestWriteAtomicModeOverridesExistingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("viejo"), 0o640); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("setup chmod: %v", err)
	}
	mode600 := os.FileMode(0o600)
	if err := writeAtomic(path, []byte("nuevo"), &mode600); err != nil {
		t.Fatalf("writeAtomic con mode falló: %v", err)
	}
	if got := statPerm(t, path); got != 0o600 {
		t.Fatalf("mode explícito debe ganar a la preservación: got %o, want 0600", got)
	}
}

// TestWriteExclusiveModeIndependentOfUmask blinda G57 sobre el camino exclusivo (la
// pieza del lockfile de sesiones, §6): `write{exclusive=true, mode=0600}` bajo umask
// laxo (022) crea el lock en 0600, no world-readable. Toca el umask del proceso.
func TestWriteExclusiveModeIndependentOfUmask(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl.lock")
	mode600 := os.FileMode(0o600)
	if err := writeExclusive(path, []byte(`{"pid":1}`), &mode600); err != nil {
		t.Fatalf("writeExclusive con mode falló: %v", err)
	}
	if got := statPerm(t, path); got != 0o600 {
		t.Fatalf("umask 022 + exclusive mode 0600: got %o, want 0600", got)
	}
}

// statPerm es un helper de test: los bits de permiso del fichero en `path`.
func statPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return info.Mode().Perm()
}

// --- Lógica 🔒: G17, write{exclusive} = O_EXCL ---

// TestWriteExclusiveG17 blinda G17: `write{exclusive=true}` sobre un fichero
// **existente** falla con un error que envuelve `os.ErrExist` (→ `EEXIST`); sobre
// un **inexistente** lo crea con el contenido dado, en una operación indivisible.
// Es la pieza de los lockfiles de sesiones (sesiones.md §6).
func TestWriteExclusiveG17(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")

	// Inexistente: lo crea.
	if err := writeExclusive(path, []byte("pid:1"), nil); err != nil {
		t.Fatalf("G17: exclusive sobre inexistente debió crear, falló: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "pid:1" {
		t.Fatalf("G17: contenido del lock: got %q, want %q", got, "pid:1")
	}

	// Existente: falla con EEXIST (os.ErrExist), sin tocar el contenido.
	err := writeExclusive(path, []byte("pid:2"), nil)
	if err == nil {
		t.Fatal("G17: exclusive sobre existente debió fallar, no falló")
	}
	if !os.IsExist(err) {
		t.Fatalf("G17: exclusive sobre existente debió dar os.ErrExist (→ EEXIST), dio: %v", err)
	}
	got2, _ := os.ReadFile(path)
	if string(got2) != "pid:1" {
		t.Fatalf("G17: el lock existente no debe modificarse: got %q, want %q", got2, "pid:1")
	}
}

// --- Lógica 🔒: copy ---

// TestCopyFile blinda que `copy` reproduce el contenido del origen en el destino
// (creándolo) y que **sobreescribe** un destino existente (truncándolo).
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "from")
	to := filepath.Join(dir, "to")
	if err := os.WriteFile(from, []byte("origen"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Destino preexistente más largo: debe quedar truncado al contenido del origen.
	if err := os.WriteFile(to, []byte("destino preexistente largo"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := copyFile(from, to); err != nil {
		t.Fatalf("copyFile falló: %v", err)
	}
	got, _ := os.ReadFile(to)
	if string(got) != "origen" {
		t.Fatalf("copy: got %q, want %q", got, "origen")
	}
}

// TestCopyFileMissingSource blinda que copiar un origen inexistente da un error
// que envuelve `os.ErrNotExist` (→ `ENOENT`).
func TestCopyFileMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := copyFile(filepath.Join(dir, "nope"), filepath.Join(dir, "to"))
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("copy de origen inexistente debió dar os.ErrNotExist, dio: %v", err)
	}
}

// --- Snippet Lua de extremo a extremo ---

// withFsDir registra un global `BASE` con un directorio temporal de la prueba,
// para que los snippets compongan rutas dentro de él sin tocar el disco real.
func withFsDir(h *harness) string {
	dir := h.t.TempDir()
	// El path cruza sin interpolar (SetStringGlobal); BASE() lo devuelve.
	h.rt.SetStringGlobal("__base_val", dir)
	h.defWasmGlobal("function BASE() return __base_val end")
	return dir
}

// TestFsRoundTrip ejercita la superficie ⏸ completa de `enu.fs` de extremo a
// extremo por el puente real: write/read/append/stat/list/mkdir/remove/rename/
// copy/tmpdir/cwd. Todo desde una task (las ⏸ exigen task) y autovalidado con
// `assert`. Es el snippet de la Definition of Done §2 del plan.
func TestFsRoundTrip(t *testing.T) {
	h := newHarness(t)
	dir := withFsDir(h)

	h.eval(`
		ok = false
		enu.task.spawn(function()
			local base = BASE()
			local p = base .. "/f.txt"

			-- write + read
			enu.fs.write(p, "hola")
			assert(enu.fs.read(p) == "hola", "read tras write")

			-- write sobreescribe
			enu.fs.write(p, "adios")
			assert(enu.fs.read(p) == "adios", "write sobreescribe")

			-- append
			enu.fs.append(p, "!")
			assert(enu.fs.read(p) == "adios!", "append")

			-- stat de existente
			local st = enu.fs.stat(p)
			assert(st ~= nil, "stat no-nil")
			assert(st.size == 6, "stat.size")
			assert(st.is_dir == false, "stat.is_dir false")
			assert(type(st.mtime_ms) == "number", "stat.mtime_ms")

			-- stat de inexistente -> nil, NO lanza
			assert(enu.fs.stat(base .. "/no-existe") == nil, "stat inexistente nil")

			-- mkdir (con padres) + stat is_dir
			local sub = base .. "/a/b/c"
			enu.fs.mkdir(sub)
			assert(enu.fs.stat(sub).is_dir == true, "mkdir crea dir")
			-- mkdir idempotente
			enu.fs.mkdir(sub)

			-- list
			enu.fs.write(base .. "/x.txt", "1")
			local entries = enu.fs.list(base)
			local names = {}
			for _, e in ipairs(entries) do names[e.name] = e.is_dir end
			assert(names["f.txt"] == false, "list ve f.txt")
			assert(names["x.txt"] == false, "list ve x.txt")
			assert(names["a"] == true, "list ve dir a")

			-- rename
			enu.fs.rename(base .. "/x.txt", base .. "/y.txt")
			assert(enu.fs.stat(base .. "/x.txt") == nil, "rename mueve origen")
			assert(enu.fs.read(base .. "/y.txt") == "1", "rename conserva contenido")

			-- copy
			enu.fs.copy(base .. "/y.txt", base .. "/z.txt")
			assert(enu.fs.read(base .. "/z.txt") == "1", "copy reproduce contenido")
			assert(enu.fs.read(base .. "/y.txt") == "1", "copy no toca origen")

			-- remove de fichero
			enu.fs.remove(base .. "/z.txt")
			assert(enu.fs.stat(base .. "/z.txt") == nil, "remove fichero")

			-- remove de dir no vacío sin recursive -> error capturable
			local okrm = pcall(function() enu.fs.remove(base .. "/a") end)
			assert(okrm == false, "remove dir no vacío sin recursive lanza")
			assert(enu.fs.stat(base .. "/a").is_dir == true, "el dir sigue ahí")

			-- remove de dir con recursive -> borra el árbol
			enu.fs.remove(base .. "/a", { recursive = true })
			assert(enu.fs.stat(base .. "/a") == nil, "remove recursive borra el árbol")

			-- remove de inexistente -> no-op (no lanza)
			enu.fs.remove(base .. "/jamas-existio")

			-- tmpdir: propio de la sesión, reutilizado
			local td1 = enu.fs.tmpdir()
			local td2 = enu.fs.tmpdir()
			assert(td1 == td2, "tmpdir reutiliza")
			assert(enu.fs.stat(td1).is_dir == true, "tmpdir existe")

			-- cwd: string no vacío (síncrona, [W])
			assert(type(enu.fs.cwd()) == "string" and #enu.fs.cwd() > 0, "cwd")

			ok = true
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")

	// El temporal de la sesión debe borrarse en Close: lo comprueba el Cleanup del
	// harness implícitamente; aquí solo verificamos que existió y que el dir base
	// no quedó con residuos de temporales de writeAtomic.
	if leftover := residualTmp(t, dir); leftover != "" {
		t.Fatalf("residuo de temporal de write atómico: %s", leftover)
	}
}

// residualTmp busca, recursivamente, algún fichero con el patrón del temporal de
// `writeAtomic` (`.enu-fs-*.tmp`) que hubiera quedado sin renombrar. Devuelve el
// primero que encuentre o "" si no hay ninguno.
func residualTmp(t *testing.T, root string) string {
	t.Helper()
	var found string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasPrefix(filepath.Base(p), ".enu-fs-") && strings.HasSuffix(p, ".tmp") {
			found = p
		}
		return nil
	})
	return found
}

// TestFsWriteModeAppliesAndValidates blinda G57 de extremo a extremo por el puente
// ⏸ real: (1) `enu.fs.write{ mode }` fija el modo del fichero (comprobado con el
// `mode` que devuelve `enu.fs.stat`, que sería el default del umask si no se
// aplicara); componible con `exclusive`; (2) un `opts.mode` inválido (no entero,
// negativo o fuera de 0..0o777) lanza `EINVAL` en el acto. El modo se afirma con
// `stat().mode`, que no depende del umask del ejecutor (la independencia del umask
// la blindan los tests Go directos de arriba). 0o600 = 384, 0o755 = 493. Además de
// los valores "cómodos" de en medio, se prueban los EXTREMOS del rango válido —
// 0 (sin permisos) y 0o777 (todos)— como aceptados, y 0o1000 = 512 (el primer
// entero inválido por exceso, justo en el límite) como rechazado: sin estos casos,
// una mutación off-by-one en `n > 0o777`/`n < 0` (p. ej. `>=`/`<=`) sobreviviría.
func TestFsWriteModeAppliesAndValidates(t *testing.T) {
	h := newHarness(t)
	_ = withFsDir(h)
	h.eval(`
		ok = false
		badcodes = {}
		enu.task.spawn(function()
			local base = BASE()

			-- write con mode explícito: stat().mode refleja el modo fijado.
			local p = base .. "/m.txt"
			enu.fs.write(p, "x", { mode = tonumber("600", 8) })
			assert(enu.fs.stat(p).mode == tonumber("600", 8), "write{mode} fija 0600")

			-- componible con exclusive: crea el lock con 0600.
			local lk = base .. "/l.lock"
			enu.fs.write(lk, "pid", { exclusive = true, mode = tonumber("600", 8) })
			assert(enu.fs.stat(lk).mode == tonumber("600", 8), "write{exclusive,mode} fija 0600")

			-- mode distinto (0755) para descartar que case por el default del umask.
			local p2 = base .. "/s.sh"
			enu.fs.write(p2, "#!/bin/sh", { mode = tonumber("755", 8) })
			assert(enu.fs.stat(p2).mode == tonumber("755", 8), "write{mode=0755} fija 0755")

			-- Límites válidos: 0 (sin permisos) y 0o777 (todos) también deben
			-- aceptarse y fijarse tal cual, no solo los valores "cómodos" de
			-- en medio (0600/0755) — una mutación de n > 0o777 a n >= 0o777 solo
			-- la pillaría un caso que toque el propio 0o777.
			local p3 = base .. "/full.txt"
			enu.fs.write(p3, "x", { mode = tonumber("777", 8) })
			assert(enu.fs.stat(p3).mode == tonumber("777", 8), "write{mode=0o777} fija 0777 (límite superior válido)")

			-- mode = 0: sin permisos para nadie, ni siquiera el dueño. No podemos
			-- leer el contenido después (el propio proceso, dueño, no tiene bit de
			-- lectura), así que solo comprobamos vía os.Stat/enu.fs.stat (metadatos,
			-- no requieren permiso de lectura del fichero).
			local p4 = base .. "/none.txt"
			enu.fs.write(p4, "x", { mode = 0 })
			assert(enu.fs.stat(p4).mode == 0, "write{mode=0} fija 0 (límite inferior válido)")

			-- opts.mode inválido → EINVAL en el acto.
			local function code_of(m)
				local _, e = pcall(function() enu.fs.write(base .. "/bad", "x", { mode = m }) end)
				return type(e) == "table" and e.code or "NO-ERROR"
			end
			badcodes.frac = code_of(0.5)     -- no entero
			badcodes.neg  = code_of(-1)      -- negativo
			badcodes.big  = code_of(4096)    -- muy por encima de 0o777
			badcodes.edge = code_of(512)     -- 0o1000: primer entero inválido por exceso (0o777 + 1)
			badcodes.str  = code_of("600")   -- no numérico

			ok = true
		end)
	`)
	h.expectEval(`return tostring(ok)`, "true")
	for _, k := range []string{"frac", "neg", "big", "edge", "str"} {
		h.expectEval(`return badcodes.`+k, "EINVAL")
	}
}

// TestFsReadMissingIsENOENT blinda que `read` de un fichero inexistente lanza un
// error estructurado `ENOENT` (no nil, no EIO): leer lo que no existe es un fallo.
func TestFsReadMissingIsENOENT(t *testing.T) {
	h := newHarness(t)
	_ = withFsDir(h)
	h.eval(`
		err = nil
		enu.task.spawn(function()
			local okread, e = pcall(function() enu.fs.read(BASE() .. "/no-existe") end)
			assert(okread == false, "read inexistente debe lanzar")
			err = e
		end)
	`)
	h.expectEval(`return err.code`, "ENOENT")
}

// TestFsWriteExclusiveLua blinda G17 desde Lua de extremo a extremo: el primer
// `write{exclusive}` crea; el segundo, sobre el mismo path, lanza `EEXIST`
// capturable —el patrón exacto del lockfile de sesiones.
func TestFsWriteExclusiveLua(t *testing.T) {
	h := newHarness(t)
	_ = withFsDir(h)
	h.eval(`
		code = nil
		enu.task.spawn(function()
			local lock = BASE() .. "/session.lock"
			enu.fs.write(lock, "owner-a", { exclusive = true })   -- crea
			local okw, e = pcall(function()
				enu.fs.write(lock, "owner-b", { exclusive = true }) -- ya existe
			end)
			assert(okw == false, "exclusive sobre existente debe lanzar (G17)")
			code = e.code
			assert(enu.fs.read(lock) == "owner-a", "el lock no se sobreescribe")
		end)
	`)
	h.expectEval(`return code`, "EEXIST")
}

// TestFsOutsideTaskIsEINVAL blinda que una primitiva ⏸ de `fs` llamada fuera de
// una task (en el chunk de `-e`, sobre `host`) lanza `EINVAL` accionable —no
// puede suspender sin una task (§1.3)—. `cwd`, que NO es ⏸, sí funciona fuera de
// una task (se prueba en el round-trip, pero aquí confirmamos que no exige task).
func TestFsOutsideTaskIsEINVAL(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`enu.fs.read("/tmp/x")`)
	if se.Code != CodeEINVAL {
		t.Fatalf("fs.read fuera de task: got %q, want EINVAL", se.Code)
	}
	// cwd no es ⏸: corre en el chunk principal sin task y devuelve un string.
	got := h.eval(`return enu.fs.cwd()`)
	if len(got) != 1 || got[0] == "" {
		t.Fatalf("cwd fuera de task debió devolver un string, got %v", got)
	}
}
