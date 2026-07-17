package runtime

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"unicode"

	ignore "github.com/sabhiram/go-gitignore"
)

// `enu.search` — búsqueda a escala de repo (api.md §11, sesión S27; inventario
// 🔒). Tres primitivas y el cierre de la Fase 5:
//
//   - `files(root, opts?) -> string[]` ⏸ — listado **recursivo** bajo `root`
//     respetando `.gitignore` (G7, reusa go-gitignore de S15). `opts`: `glob`
//     (filtro tipo `*.go`), `hidden` (incluir ocultos, default false), `max`.
//   - `grep(pattern, opts) -> iterator` ⏸ — busca `pattern` (RE2, S26) en los
//     ficheros bajo `opts.root`, **paralelo por dentro** (varias goroutines de
//     fondo) e itera `{path, line_no, line, ranges}` **según llegan**. `opts`:
//     `root`, `glob`, `case`, `max`.
//   - `fuzzy(query, candidates, opts?) -> {index, score}[]` — matching difuso
//     **síncrono y acotado** (la primitiva caliente del picker): NO ⏸. Ordena
//     por score descendente de forma **estable** (empates → orden de entrada).
//
// "Lua decide, Go ejecuta" (ADR-004): el recorrido del árbol, la lectura de
// ficheros y el casado del patrón son trabajo pesado que va a Go, fuera del
// token; `fuzzy` es CPU puro pero se queda en Go porque es el bucle caliente del
// picker (decenas de miles de candidatos en cada pulsación). `search` es [W]
// (§16): hoy en el estado principal, los workers son S34.
//
// EL PUENTE ⏸ (S04, ADR-011). `files` y cada `next` de `grep` son ⏸: sueltan el
// token y bloquean en goroutines de fondo que **JAMÁS tocan Lua**; los datos
// cruzan a Lua solo en la `deliverFn`, con el token recuperado. Mismo patrón que
// `enu.fs` (S14) y `enu.http.stream` (S20). `fuzzy` no suspende (es síncrono).
//
// EL MODELO DEL ITERADOR `grep` PARALELO (lo delicado, gemelo del `Stream` de
// S20). Al crear el iterador se arranca un **pool de goroutines de fondo** (una
// por núcleo, acotado) que se reparten los ficheros del árbol y casan el patrón
// línea a línea; cada match cruza por un **canal** (`results`) a la goroutine de
// la task. Cada `next` del iterador **suspende** hasta el siguiente match (o
// hasta EOF, cuando el canal se cierra tras drenarse todas las goroutines). El
// `max` corta: alcanzado el límite, el iterador deja de entregar y las goroutines
// se cancelan (`context`). Al crear el handle, el wrapper wasm registra su cierre
// en `enu.task.cleanup`, así ninguna goroutine queda colgada aunque el consumidor
// haga `break` — y el cierre es **síncrono**: `close` espera (`done`) a que el
// pool haya drenado, para que el desmontaje no dependa del planificador. Como red
// de seguridad, `Runtime.Close` cancela todos los greps vivos (`stopAllGreps`).

// --- enu.search.files ----------------------------------------------------------

// filesOpts son las opciones ya parseadas de `enu.search.files` (§11). `glob`
// vacío = sin filtro; `max <= 0` = sin límite.
type filesOpts struct {
	glob   string
	hidden bool
	max    int
}

// walkFiles recorre el árbol bajo `root` devolviendo las rutas de los ficheros
// (no los directorios), respetando `.gitignore` (G7) y las opciones. Corre fuera
// del token (en la goroutine de fondo de `files`). Detalles del filtrado:
//
//   - `.gitignore`: se carga el de `root` (`gitignore = true` siempre en `files`,
//     §11; vigilar `node_modules/` no aporta nada a un picker de ficheros). Un
//     directorio ignorado se **poda** (`SkipDir`, no se desciende) y un fichero
//     ignorado no se incluye. El `.git/` interno se poda **siempre** (ruido
//     universal de un repo, igual que en `watch.go`).
//   - `hidden` (default false): los nombres que empiezan por `.` se saltan —el
//     directorio oculto se poda, el fichero oculto no se incluye—. Con
//     `hidden = true` se incluyen (salvo `.git/`, que sigue siendo ruido).
//   - `glob`: filtra por el **nombre base** del fichero (`*.go` casa `main.go`),
//     el convenio de un picker; no filtra directorios (un dir que no casa el
//     glob igualmente puede contener ficheros que sí).
//   - `max`: corta el recorrido en cuanto se reúnen `max` rutas (no recorre el
//     árbol entero si ya hay suficientes).
//
// Las rutas se devuelven con el mismo prefijo con que se pasó `root` (relativas
// si `root` es relativo, absolutas si absoluto): es lo que el llamante espera
// reutilizar para `fs.read`. El orden es el del recorrido (`WalkDir` lexicográfico
// por directorio), determinista.
func walkFiles(root string, opts filesOpts) ([]string, error) {
	// `WalkDir` no falla si `root` existe; comprobamos antes para dar `ENOENT`
	// accionable (el contrato de `list` es que un dir inexistente lanza).
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}

	// Carga el `.gitignore` de la raíz (G7). Ausente no es error: nada se ignora
	// por esa vía. Los patrones son relativos a `root`, así que se comprueban
	// contra rutas relativas a `root` (igual que git).
	var gi *ignore.GitIgnore
	if g, err := ignore.CompileIgnoreFile(filepath.Join(root, ".gitignore")); err == nil {
		gi = g
	}

	var out []string
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Un error sobre una entrada concreta (permiso al descender) no aborta el
			// recorrido entero: se salta esa rama. El error de `root` ya se capturó arriba.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil // la raíz misma no es un resultado
		}
		// Ruta relativa a `root` para casar `.gitignore` (sus patrones son relativos).
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			rel = p
		}
		name := d.Name()
		isDir := d.IsDir()

		// `.git/` interno: ruido universal, podado siempre (también con hidden=true).
		if isDir && name == ".git" {
			return fs.SkipDir
		}
		// Ocultos: sin `hidden`, se saltan (poda el dir, omite el fichero).
		if !opts.hidden && len(name) > 0 && name[0] == '.' {
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		// `.gitignore`: lo ignorado se poda (dir) u omite (fichero).
		if gi != nil && gi.MatchesPath(rel) {
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		if isDir {
			return nil // se desciende; los directorios no son resultados
		}
		// Fichero: aplica el glob (sobre el nombre base) y lo incluye.
		if opts.glob != "" {
			matched, _ := filepath.Match(opts.glob, name)
			if !matched {
				return nil
			}
		}
		out = append(out, p)
		if opts.max > 0 && len(out) >= opts.max {
			return errFilesMaxReached // corta el recorrido
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errFilesMaxReached) {
		return nil, walkErr
	}
	return out, nil
}

// errFilesMaxReached es el centinela con que `walkFiles` corta el `WalkDir` al
// alcanzar `max` —no es un error real, solo el modo de detener el recorrido
// antes de tiempo (`WalkDir` no tiene otra vía de parada temprana).
var errFilesMaxReached = errors.New("enu.search.files: max alcanzado")

// --- enu.search.fuzzy ----------------------------------------------------------

// fuzzyMatch es un candidato que casó: su índice 1-based en `candidates` y su
// score. Se ordena por score desc, conservando el orden de entrada en los
// empates (estabilidad, inventario 🔒).
type fuzzyMatch struct {
	index int // 1-based en candidates (lo que se devuelve a Lua)
	score int
}

// fuzzyScore calcula el score de `query` contra `cand` con un scorer de
// **subsecuencia con bonus** estilo fzf simplificado (decisión propia,
// docs/worklog/README.md S27): los caracteres de `query` deben aparecer en `cand`
// en el mismo orden (no necesariamente contiguos); el score premia las
// coincidencias **contiguas** y las que caen en un **inicio de palabra** (tras
// un separador `/`, `_`, `-`, `.`, espacio, o un cambio de minúscula→mayúscula,
// como `fooBar`). Devuelve `(score, true)` si casa, `(0, false)` si no.
//
// POR QUÉ ESTE SCORER, NO UNA LIB. Es ~50 líneas de lógica nuestra, fácil de
// blindar y de hacer determinista; añadir una dependencia (`sahilm/fuzzy`) por
// esto contradice "cero dependency hell" (filosofía §6) sin ganancia —el matching
// de pickers es un algoritmo conocido, no un parser traicionero como YAML—. El
// match es **case-insensitive** (un picker no obliga a acertar mayúsculas), pero
// el bonus de inicio-de-palabra se calcula sobre el caso original.
//
// EL ALGORITMO. Recorre `cand` buscando, en orden, cada carácter de `query`
// (comparando en minúsculas). Por cada acierto suma una base; si el acierto es
// **contiguo** al anterior, suma un bonus de contigüidad; si cae en un inicio de
// palabra, suma un bonus de frontera. Un `query` vacío casa todo con score 0
// (caso del picker recién abierto: todos los candidatos visibles). Si algún
// carácter de `query` no se encuentra, no casa.
func fuzzyScore(query, cand string) (int, bool) {
	if query == "" {
		return 0, true // query vacío: casa todo (picker sin filtro)
	}
	q := []rune(query)
	c := []rune(cand)
	ql := toLowerRunes(q)
	cl := toLowerRunes(c)

	const (
		scoreMatch      = 1 // base por cada carácter que casa
		bonusContiguous = 4 // el acierto sigue inmediatamente al anterior
		bonusWordStart  = 6 // el acierto cae en un inicio de palabra
		bonusFirstChar  = 8 // el primer acierto es el primer carácter de cand
	)

	score := 0
	ci := 0         // posición de lectura en cand
	prevMatch := -2 // índice del acierto anterior en cand (-2 = ninguno aún)
	for qi := 0; qi < len(ql); qi++ {
		found := false
		for ; ci < len(cl); ci++ {
			if cl[ci] == ql[qi] {
				score += scoreMatch
				if ci == prevMatch+1 {
					score += bonusContiguous
				}
				if ci == 0 {
					score += bonusFirstChar
				} else if isWordBoundary(c, ci) {
					score += bonusWordStart
				}
				prevMatch = ci
				ci++
				found = true
				break
			}
		}
		if !found {
			return 0, false // un carácter de query no aparece: no casa
		}
	}
	return score, true
}

// isWordBoundary indica si `c[i]` es un inicio de palabra: el carácter anterior
// es un separador (`/`, `_`, `-`, `.`, espacio) o hay un cambio
// minúscula→mayúscula (camelCase, `fooBar` → la `B` es frontera). Se calcula
// sobre el texto **original** (no en minúsculas) para detectar el cambio de caja.
func isWordBoundary(c []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := c[i-1]
	switch prev {
	case '/', '\\', '_', '-', '.', ' ':
		return true
	}
	cur := c[i]
	// camelCase: minúscula seguida de mayúscula.
	if prev >= 'a' && prev <= 'z' && cur >= 'A' && cur <= 'Z' {
		return true
	}
	return false
}

// toLowerRunes baja a minúsculas un slice de runes (ASCII y, vía unicode, el
// resto). Se usa para el casado case-insensitive del scorer.
func toLowerRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + ('a' - 'A')
		} else {
			out[i] = toLowerNonASCII(r)
		}
	}
	return out
}

// toLowerNonASCII baja a minúsculas un rune no-ASCII usando la tabla de Unicode.
// Separado para que el caso común (ASCII) no pague la indirección de `unicode`.
func toLowerNonASCII(r rune) rune {
	if r < 0x80 {
		return r
	}
	return unicode.ToLower(r)
}

// --- enu.search.grep -----------------------------------------------------------

// grepMaxLine es el tope de bytes de una línea que el grep examina (§11). Una
// línea más larga que esto (ficheros generados, JSON minificado) se trunca al
// escanear en vez de hacer fallar el grep del fichero. 1 MiB es holgado para
// código y configuración reales.
const grepMaxLine = 1 << 20 // 1 MiB

// grepResult es un match que cruza del pool de goroutines de fondo a la task. Es
// puramente Go (no toca Lua): los datos cruzan a Lua en la `deliverFn` del `next`.
type grepResult struct {
	path   string
	lineNo int      // 1-based
	line   string   // la línea (sin el `\n`)
	ranges [][2]int // rangos de byte 1-based inclusive del match dentro de `line`
}

// grepIter es el handle Go detrás del iterador de `grep`. Coordina el pool de
// goroutines de fondo (productores) con la task que consume (vía `next`):
//
//   - `results` es el canal por el que los matches cruzan; lo cierran las
//     goroutines al terminar (vía el `WaitGroup` + una goroutine cerradora).
//   - `ctx`/`cancel` paran las goroutines: lo dispara `close()` (alcanzado `max`,
//     `Runtime.Close` o el `cleanup` de la task al cancelarse).
//   - `emitted`/`max` cuentan los matches entregados para cortar en `max`.
//
// El orden de entrega NO es determinista entre ficheros (varias goroutines
// compiten por el canal), pero dentro de un mismo fichero las líneas salen en
// orden (cada fichero lo procesa una sola goroutine, de arriba abajo). El
// contrato (§11) solo promete "según llegan", no un orden global.
type grepIter struct {
	s *scheduler

	results chan grepResult
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{} // la cerradora lo sella cuando el pool ha drenado del todo

	max       int
	emitted   int
	closeOnce sync.Once
}

// grepOpts son las opciones ya parseadas de `enu.search.grep` (§11).
type grepOpts struct {
	root       string
	glob       string
	ignoreCase bool
	max        int
}

// newGrepIter arranca el pool de goroutines de fondo que casan el patrón en los
// ficheros y devuelve el iterador. El paralelismo: un número acotado de
// goroutines (no una por fichero) toman ficheros de un canal de trabajo y casan
// línea a línea, empujando cada match al canal `results`. Una goroutine cerradora
// espera (`WaitGroup`) a que todas terminen y cierra `results` —así el consumidor
// distingue "fin del stream" (canal cerrado) de "aún quedan matches".
func newGrepIter(s *scheduler, re *regexp.Regexp, files []string, max int) *grepIter {
	ctx, cancel := context.WithCancel(context.Background())
	it := &grepIter{
		s:       s,
		results: make(chan grepResult), // sin buffer: backpressure natural (cada next saca uno)
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		max:     max,
	}

	work := make(chan string)
	var wg sync.WaitGroup
	workers := grepWorkers(len(files))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range work {
				if it.ctx.Err() != nil {
					return // cancelado: para de procesar (cierre/max/cancelación de task)
				}
				grepFile(it, re, path)
			}
		}()
	}
	// Repartidor: alimenta el canal de trabajo y lo cierra al acabar (o al
	// cancelarse). No toca Lua.
	go func() {
		defer close(work)
		for _, f := range files {
			select {
			case work <- f:
			case <-it.ctx.Done():
				return
			}
		}
	}()
	// Cerradora: cuando todas las goroutines terminan, cierra `results` —es la
	// señal de EOF para el consumidor (`<-results` devuelve `ok=false`)— y sella
	// `done` —la señal de "pool drenado" que `close` espera para ser síncrono—.
	// El orden importa: un `next` bloqueado en `<-results` se desbloquea por el
	// cierre de `results` ANTES de que `close` deje de esperar.
	go func() {
		wg.Wait()
		close(it.results)
		close(it.done)
	}()
	return it
}

// grepWorkers decide el tamaño del pool: tantas goroutines como núcleos, acotado
// por el número de ficheros (no tiene sentido lanzar 8 goroutines para 3
// ficheros) y con un suelo de 1.
func grepWorkers(nFiles int) int {
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	if nFiles > 0 && n > nFiles {
		n = nFiles
	}
	if n < 1 {
		n = 1
	}
	return n
}

// grepFile lee `path` línea a línea y empuja al canal `results` cada línea que
// casa el patrón, con sus rangos de match. Corre en una goroutine de fondo (jamás
// toca Lua). Atiende a la cancelación entre líneas (`ctx.Done`) para no seguir
// trabajando tras un `close`/`max`/cancelación de task. Un fichero ilegible
// (permiso, binario que falla al leer) se **salta en silencio** —un grep sobre un
// árbol no debe abortar porque un fichero no se pueda abrir; es el comportamiento
// de `grep -r`—.
func grepFile(it *grepIter, re *regexp.Regexp, path string) {
	f, err := os.Open(path)
	if err != nil {
		return // ilegible: se salta (como grep -r)
	}
	defer func() { _ = f.Close() }()

	// Lectura por líneas con TRUNCADO real más allá de `grepMaxLine`: una línea
	// muy larga (un JSON minificado, un fichero generado) no debe abortar el grep
	// de ese fichero. Aquí no sirve `bufio.Scanner`: ante un token que supera su
	// buffer devuelve false DEFINITIVO (ErrTooLong) y las líneas restantes del
	// fichero se perderían en silencio; `readGrepLine` recorta la cola de la
	// línea larga y sigue con la siguiente.
	r := bufio.NewReaderSize(f, 64*1024)
	lineNo := 0
	for {
		line, eof, rerr := readGrepLine(r, grepMaxLine)
		if rerr != nil {
			return // fallo de lectura a mitad: se salta el resto (como el open)
		}
		if eof && line == "" {
			return
		}
		lineNo++
		if it.ctx.Err() != nil {
			return // cancelado entre líneas
		}
		idxs := re.FindAllStringIndex(line, -1)
		if idxs == nil {
			continue
		}
		ranges := make([][2]int, len(idxs))
		for i, pair := range idxs {
			// Mismo convenio que `enu.re.find_all` (S26): byte 1-based, ambos inclusive.
			// `s:sub(start, end)` reconstruye el match; match vacío → end = start-1.
			ranges[i] = [2]int{pair[0] + 1, pair[1]}
		}
		res := grepResult{path: path, lineNo: lineNo, line: line, ranges: ranges}
		select {
		case it.results <- res:
		case <-it.ctx.Done():
			return
		}
	}
}

// readGrepLine lee la siguiente línea de `r` (sin el salto final, y sin el `\r`
// de un CRLF, como hacía ScanLines) truncándola a `max` bytes: la cola de una
// línea que supere el tope se DESCARTA consumiéndola hasta el `\n`, y la lectura
// continúa en la línea siguiente. eof=true al agotarse el fichero (la última
// línea sin salto llega junto con eof=true; después, línea vacía + eof).
func readGrepLine(r *bufio.Reader, max int) (string, bool, error) {
	var buf []byte
	truncated := false
	for {
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 && !truncated {
			keep := chunk
			if keep[len(keep)-1] == '\n' {
				keep = keep[:len(keep)-1]
			}
			if room := max - len(buf); len(keep) > room {
				keep = keep[:room]
				truncated = true
			}
			buf = append(buf, keep...)
		}
		switch err {
		case nil:
			// Línea completa (el '\n' quedó consumido).
			if n := len(buf); n > 0 && buf[n-1] == '\r' {
				buf = buf[:n-1]
			}
			return string(buf), false, nil
		case bufio.ErrBufferFull:
			continue // aún sin '\n': sigue consumiendo (y descartando, si ya truncó)
		case io.EOF:
			return string(buf), true, nil
		default:
			return "", false, err
		}
	}
}

// close cancela el pool de goroutines de fondo, deja de rastrear el iterador y
// **espera a que el pool haya drenado** antes de volver. **Idempotente**
// (`closeOnce`): lo llaman el `next` (al alcanzar `max` o EOF), el `cleanup` de
// la task (al cancelarse/terminar) y `Runtime.Close` (red de seguridad).
// Cancelar el contexto desbloquea el repartidor y los workers (todos tienen una
// salida por `ctx.Done` y ninguno toca Lua); cuando el último worker sale, la
// cerradora cierra `results` y sella `done`. La espera hace el desmontaje
// **determinista**: al retornar `close`, los workers han salido — sin ella, en
// runners con pocos núcleos efectivos (cgroups de CI) las goroutines canceladas
// podían tardar segundos en ser planificadas para morir, y el conteo del DoD
// "sin fugas" las veía como fuga (flake registrada en la bitácora de salud
// 2026-07-11). La espera es corta por construcción: como mucho, lo que tarde un
// worker en terminar la línea que tiene entre manos.
//
// El rastreo (`untrackGrep`) sólo se deshace si hay scheduler: algunos tests
// aislados del núcleo vmwasm crean el iterador sin un Runtime completo. Cancelar
// el contexto siempre ocurre; sólo el rastreo para `Runtime.Close` es opcional.
func (it *grepIter) close() {
	it.closeOnce.Do(func() {
		it.cancel()
		if it.s != nil {
			it.s.untrackGrep(it)
		}
		<-it.done
	})
}
