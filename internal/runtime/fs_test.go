package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Tests de S14 (api.md §5): `nu.fs`. Sesión 🔒 —la lógica clave a blindar
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

	if err := writeAtomic(path, []byte("contenido")); err != nil {
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
	if err := writeAtomic(path, []byte("nuevo")); err != nil {
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
	if err := writeAtomic(path, []byte("dato sensible")); err != nil {
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
	if err := writeAtomic(path, []byte("nuevo")); err != nil {
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

// --- Lógica 🔒: G17, write{exclusive} = O_EXCL ---

// TestWriteExclusiveG17 blinda G17: `write{exclusive=true}` sobre un fichero
// **existente** falla con un error que envuelve `os.ErrExist` (→ `EEXIST`); sobre
// un **inexistente** lo crea con el contenido dado, en una operación indivisible.
// Es la pieza de los lockfiles de sesiones (sesiones.md §6).
func TestWriteExclusiveG17(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")

	// Inexistente: lo crea.
	if err := writeExclusive(path, []byte("pid:1")); err != nil {
		t.Fatalf("G17: exclusive sobre inexistente debió crear, falló: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "pid:1" {
		t.Fatalf("G17: contenido del lock: got %q, want %q", got, "pid:1")
	}

	// Existente: falla con EEXIST (os.ErrExist), sin tocar el contenido.
	err := writeExclusive(path, []byte("pid:2"))
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

// TestFsRoundTrip ejercita la superficie ⏸ completa de `nu.fs` de extremo a
// extremo por el puente real: write/read/append/stat/list/mkdir/remove/rename/
// copy/tmpdir/cwd. Todo desde una task (las ⏸ exigen task) y autovalidado con
// `assert`. Es el snippet de la Definition of Done §2 del plan.
func TestFsRoundTrip(t *testing.T) {
	h := newHarness(t)
	dir := withFsDir(h)

	h.eval(`
		ok = false
		nu.task.spawn(function()
			local base = BASE()
			local p = base .. "/f.txt"

			-- write + read
			nu.fs.write(p, "hola")
			assert(nu.fs.read(p) == "hola", "read tras write")

			-- write sobreescribe
			nu.fs.write(p, "adios")
			assert(nu.fs.read(p) == "adios", "write sobreescribe")

			-- append
			nu.fs.append(p, "!")
			assert(nu.fs.read(p) == "adios!", "append")

			-- stat de existente
			local st = nu.fs.stat(p)
			assert(st ~= nil, "stat no-nil")
			assert(st.size == 6, "stat.size")
			assert(st.is_dir == false, "stat.is_dir false")
			assert(type(st.mtime_ms) == "number", "stat.mtime_ms")

			-- stat de inexistente -> nil, NO lanza
			assert(nu.fs.stat(base .. "/no-existe") == nil, "stat inexistente nil")

			-- mkdir (con padres) + stat is_dir
			local sub = base .. "/a/b/c"
			nu.fs.mkdir(sub)
			assert(nu.fs.stat(sub).is_dir == true, "mkdir crea dir")
			-- mkdir idempotente
			nu.fs.mkdir(sub)

			-- list
			nu.fs.write(base .. "/x.txt", "1")
			local entries = nu.fs.list(base)
			local names = {}
			for _, e in ipairs(entries) do names[e.name] = e.is_dir end
			assert(names["f.txt"] == false, "list ve f.txt")
			assert(names["x.txt"] == false, "list ve x.txt")
			assert(names["a"] == true, "list ve dir a")

			-- rename
			nu.fs.rename(base .. "/x.txt", base .. "/y.txt")
			assert(nu.fs.stat(base .. "/x.txt") == nil, "rename mueve origen")
			assert(nu.fs.read(base .. "/y.txt") == "1", "rename conserva contenido")

			-- copy
			nu.fs.copy(base .. "/y.txt", base .. "/z.txt")
			assert(nu.fs.read(base .. "/z.txt") == "1", "copy reproduce contenido")
			assert(nu.fs.read(base .. "/y.txt") == "1", "copy no toca origen")

			-- remove de fichero
			nu.fs.remove(base .. "/z.txt")
			assert(nu.fs.stat(base .. "/z.txt") == nil, "remove fichero")

			-- remove de dir no vacío sin recursive -> error capturable
			local okrm = pcall(function() nu.fs.remove(base .. "/a") end)
			assert(okrm == false, "remove dir no vacío sin recursive lanza")
			assert(nu.fs.stat(base .. "/a").is_dir == true, "el dir sigue ahí")

			-- remove de dir con recursive -> borra el árbol
			nu.fs.remove(base .. "/a", { recursive = true })
			assert(nu.fs.stat(base .. "/a") == nil, "remove recursive borra el árbol")

			-- remove de inexistente -> no-op (no lanza)
			nu.fs.remove(base .. "/jamas-existio")

			-- tmpdir: propio de la sesión, reutilizado
			local td1 = nu.fs.tmpdir()
			local td2 = nu.fs.tmpdir()
			assert(td1 == td2, "tmpdir reutiliza")
			assert(nu.fs.stat(td1).is_dir == true, "tmpdir existe")

			-- cwd: string no vacío (síncrona, [W])
			assert(type(nu.fs.cwd()) == "string" and #nu.fs.cwd() > 0, "cwd")

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
// `writeAtomic` (`.nu-fs-*.tmp`) que hubiera quedado sin renombrar. Devuelve el
// primero que encuentre o "" si no hay ninguno.
func residualTmp(t *testing.T, root string) string {
	t.Helper()
	var found string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasPrefix(filepath.Base(p), ".nu-fs-") && strings.HasSuffix(p, ".tmp") {
			found = p
		}
		return nil
	})
	return found
}

// TestFsReadMissingIsENOENT blinda que `read` de un fichero inexistente lanza un
// error estructurado `ENOENT` (no nil, no EIO): leer lo que no existe es un fallo.
func TestFsReadMissingIsENOENT(t *testing.T) {
	h := newHarness(t)
	_ = withFsDir(h)
	h.eval(`
		err = nil
		nu.task.spawn(function()
			local okread, e = pcall(function() nu.fs.read(BASE() .. "/no-existe") end)
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
		nu.task.spawn(function()
			local lock = BASE() .. "/session.lock"
			nu.fs.write(lock, "owner-a", { exclusive = true })   -- crea
			local okw, e = pcall(function()
				nu.fs.write(lock, "owner-b", { exclusive = true }) -- ya existe
			end)
			assert(okw == false, "exclusive sobre existente debe lanzar (G17)")
			code = e.code
			assert(nu.fs.read(lock) == "owner-a", "el lock no se sobreescribe")
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
	se := h.evalErr(`nu.fs.read("/tmp/x")`)
	if se.Code != CodeEINVAL {
		t.Fatalf("fs.read fuera de task: got %q, want EINVAL", se.Code)
	}
	// cwd no es ⏸: corre en el chunk principal sin task y devuelve un string.
	got := h.eval(`return nu.fs.cwd()`)
	if len(got) != 1 || got[0] == "" {
		t.Fatalf("cwd fuera de task debió devolver un string, got %v", got)
	}
}
