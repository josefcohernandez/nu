package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// `enu.fs` — sistema de ficheros (api.md §5, sesión S14). Primitivas de IO de
// disco, **todas ⏸ salvo `cwd`** (que es síncrona y [W]): se construyen sobre el
// puente `suspend` del scheduler (S04, ADR-011) —sueltan el token, hacen el IO
// **bloqueante** en una goroutine de fondo que **jamás toca Lua**, y al volver
// recuperan el token y entregan el resultado vía `deliverFn`—. Es el primer
// submódulo de IO real; **el patrón que aquí se fija lo reusan S15/S16** (watch,
// proc) y toda la Fase 4 (red): el trabajo Go va dentro del `work func()`, los
// datos cruzan a Lua solo en la `deliverFn`, y los errores del SO se mapean a los
// códigos reservados (§1.4) antes de cruzar.
//
// "Lua decide, Go ejecuta" (ADR-004): el IO pesado (read entero, copy, walk del
// directorio) corre en Go, fuera del token, sin congelar el loop —mientras una
// task lee un fichero grande, otras progresan—. No se usa el `io`/`os` de Lua: el
// baseline del sandbox (§1.2) los dejó fuera; esto es Go puro bajo el token.
//
// Mapeo de errores del SO → códigos §1.4 (`mapFsError`, abajo): inexistente →
// `ENOENT` (salvo `stat`, que devuelve `nil` sin lanzar, §5); ya-existe →
// `EEXIST` (la pieza de `write{exclusive}`, G17, para lockfiles); permiso →
// `EACCES`; cualquier otro fallo de IO → `EIO`.

const (
	// fsDirPerm es el modo con que se crean directorios (`mkdir`): permisos
	// estándar de usuario, recortados por el umask del proceso como en cualquier
	// herramienta de terminal.
	fsDirPerm = 0o755
	// fsFilePerm es el modo con que se crean ficheros nuevos (`write`/`append`/
	// `copy`): legible/escribible por el dueño, legible por el grupo/otros, recortado
	// por el umask del proceso en la creación —el SO lo aplica al abrir con
	// `O_CREATE`—. La escritura atómica usa este mismo modo al crear el temporal de
	// un fichero nuevo; al sobrescribir uno existente, en cambio, conserva el modo
	// previo del destino (igual que las rutas `OpenFile`, que no tocan los permisos
	// de un fichero que ya existe).
	fsFilePerm = 0o644
)

// fsState es el estado de sesión del submódulo `fs`: hoy, solo el directorio
// temporal propio (`enu.fs.tmpdir`, §5). Se crea **perezosamente** la primera vez
// que `tmpdir` se invoca (no todas las sesiones lo necesitan) y se **reutiliza**
// en las siguientes; `Close` lo borra recursivamente. El candado protege la
// creación perezosa: las primitivas ⏸ corren su IO en goroutines de fondo, así
// que dos `tmpdir` concurrentes podrían carrera sobre el campo —el candado lo
// blinda sin depender del token.
type fsState struct {
	mu     sync.Mutex
	tmpdir string // directorio temporal de la sesión; "" hasta el primer tmpdir()
}

// writeAtomic realiza la escritura atómica del camino normal de `write`: temporal
// en el MISMO directorio destino + `rename`. El temporal va al mismo dir (no a
// `/tmp`) para garantizar que el rename es **same-filesystem** y por tanto
// atómico —un rename entre sistemas de ficheros distintos no es atómico (y en Go
// ni siquiera funciona con `os.Rename`)—. Si algo falla tras crear el temporal,
// se borra para no dejar residuo (la prueba 🔒 verifica "no queda temporal").
//
// Permisos (§5, G57): `mode` es el modo explícito de `opts.mode` (nil = ausente).
// Prioridad del modo del fichero resultante:
//  1. `mode != nil` → `Chmod` al valor EXACTO, **no recortado por el umask**
//     (chmod fija los bits tal cual). Gana sobre el default y sobre la
//     preservación del previo: es la vía para forzar `0600` en un transcript o
//     lockfile independientemente del umask ambiental (G57).
//  2. `mode == nil` y destino existente → se preserva su modo previo (`Stat`
//     antes + `Chmod` al modo actual), igual que las rutas `OpenFile` que no
//     tocan los permisos de un fichero que ya existe.
//  3. `mode == nil` y destino inexistente → sin `Chmod`: el temporal se crea con
//     `O_CREATE|O_EXCL` y `fsFilePerm`, así que el SO le aplica el umask del
//     proceso —a diferencia de un `CreateTemp`+`Chmod`, que forzaría 0644
//     saltándose el umask y sería world-readable con umask 077—.
func writeAtomic(path string, data []byte, mode *os.FileMode) error {
	dir := filepath.Dir(path)

	// Modo a aplicar al fichero resultante (ver prioridad arriba). Con `mode`
	// explícito, se aplica ese valor exacto; sin él, se preserva el modo del
	// destino existente; sobre un destino nuevo sin `mode`, no se toca (queda
	// `fsFilePerm` recortado por el umask).
	applyMode := false
	var wantMode os.FileMode
	if mode != nil {
		applyMode = true
		wantMode = *mode
	} else if info, err := os.Stat(path); err == nil {
		applyMode = true
		wantMode = info.Mode().Perm()
	}

	tmpName, tmp, err := createExclTemp(dir, fsFilePerm)
	if err != nil {
		return err
	}
	// Limpieza best-effort: si retornamos por error antes del rename, el temporal
	// no debe sobrevivir. Tras un rename con éxito, `tmpName` ya no existe con ese
	// nombre, así que el `os.Remove` diferido es un no-op inocuo.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Aplica el modo al temporal ANTES del rename (para que el fichero final nunca
	// exista con permisos distintos ni un instante): modo explícito (`opts.mode`,
	// no recortado por el umask), o el previo del destino en la sobrescritura. Sin
	// nada que aplicar (destino nuevo sin `mode`), queda `fsFilePerm` bajo el umask.
	if applyMode {
		if err := os.Chmod(tmpName, wantMode); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// createExclTemp crea un fichero temporal con nombre aleatorio en `dir`, abierto
// para escritura con `O_CREATE|O_EXCL` y el modo `perm` —así el SO aplica el umask
// del proceso en la creación, cosa que un `CreateTemp`+`Chmod` no haría—. El
// nombre lleva un sufijo aleatorio de `crypto/rand`; ante la (improbable) colisión
// de nombre se reintenta unas pocas veces, como hace `os.CreateTemp` en el stdlib,
// antes de rendirse con `os.ErrExist`. Devuelve la ruta del temporal y el fichero
// abierto (que el llamante debe cerrar).
func createExclTemp(dir string, perm os.FileMode) (string, *os.File, error) {
	const maxAttempts = 10000
	for i := 0; i < maxAttempts; i++ {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", nil, err
		}
		name := filepath.Join(dir, ".enu-fs-"+hex.EncodeToString(suffix[:])+".tmp")
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", nil, err
		}
		return name, f, nil
	}
	return "", nil, os.ErrExist
}

// writeExclusive realiza `write{exclusive=true}` (G17): `O_EXCL` crea el fichero
// **solo si no existe**, en una única llamada al SO. Si ya existe, `OpenFile`
// falla con un error que envuelve `os.ErrExist` → `mapFsError` lo rinde como
// `EEXIST`. No hay temporal+rename: la exclusión exige que la creación misma sea
// la operación indivisible.
//
// Permisos (§5, G57): la creación usa `fsFilePerm` recortado por el umask; si
// `mode != nil` (`opts.mode`), un `Chmod` posterior fija el modo EXACTO no
// recortado por el umask —la vía para un lockfile `0600` bajo cualquier umask—.
// El `Chmod` va sobre un fichero que este proceso acaba de crear en exclusiva, así
// que la ventana entre creación y chmod no la ve otro escritor de esta sesión.
func writeExclusive(path string, data []byte, mode *os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fsFilePerm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if mode != nil {
		if err := os.Chmod(path, *mode); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copia el contenido de `from` a `to` en streaming. Abre el origen
// primero (su inexistencia/permiso es el error que el usuario espera ver) y solo
// entonces crea el destino. `io.Copy` mueve los bytes en bloques, sin materializar
// el fichero entero en RAM.
func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fsFilePerm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

// ensureTmpdir crea el directorio temporal de la sesión la primera vez y lo
// devuelve cacheado después. Corre en la goroutine de fondo de `tmpdir` (fuera del
// token), de ahí el candado: dos `tmpdir` concurrentes no deben crear dos
// directorios ni correr una carrera sobre el campo.
func (s *fsState) ensureTmpdir() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tmpdir != "" {
		return s.tmpdir, nil
	}
	dir, err := os.MkdirTemp("", "enu-session-*")
	if err != nil {
		return "", err
	}
	s.tmpdir = dir
	return dir, nil
}

// closeTmpdir borra el directorio temporal de la sesión si llegó a crearse. Lo
// llama `Runtime.Close`: el scratch de la sesión no debe sobrevivir al proceso.
// Best-effort (un fallo al borrar no es accionable al cerrar).
func (s *fsState) closeTmpdir() {
	s.mu.Lock()
	dir := s.tmpdir
	s.tmpdir = ""
	s.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}
