package runtime

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"sync"
	"unicode"

	ignore "github.com/sabhiram/go-gitignore"
	lua "github.com/yuin/gopher-lua"
)

// `nu.search` — búsqueda a escala de repo (api.md §11, sesión S27; inventario
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
// `nu.fs` (S14) y `nu.http.stream` (S20). `fuzzy` no suspende (es síncrono).
//
// EL MODELO DEL ITERADOR `grep` PARALELO (lo delicado, gemelo del `Stream` de
// S20). Al crear el iterador se arranca un **pool de goroutines de fondo** (una
// por núcleo, acotado) que se reparten los ficheros del árbol y casan el patrón
// línea a línea; cada match cruza por un **canal** (`results`) a la goroutine de
// la task. Cada `next` del iterador **suspende** hasta el siguiente match (o
// hasta EOF, cuando el canal se cierra tras drenarse todas las goroutines). El
// `max` corta: alcanzado el límite, el iterador deja de entregar y las goroutines
// se cancelan (`context`). La cancelación de la task (S08) cancela el contexto vía
// `nu.task.cleanup`, así ninguna goroutine queda colgada. Como red de seguridad,
// `Runtime.Close` cancela todos los greps vivos (`stopAllGreps`).

// registerSearch cuelga `nu.search` del global `nu` con `files`/`grep`/`fuzzy` e
// instala la metatabla del iterador de `grep`. Lo llama `registerNu` (nu.go).
func (rt *Runtime) registerSearch(nu *lua.LTable) {
	L := rt.L
	s := L.NewTable()
	s.RawSetString("files", L.NewFunction(rt.searchFiles))
	s.RawSetString("grep", L.NewFunction(rt.searchGrep))
	s.RawSetString("fuzzy", L.NewFunction(rt.searchFuzzy))
	nu.RawSetString("search", s)
}

// --- nu.search.files ----------------------------------------------------------

// filesOpts son las opciones ya parseadas de `nu.search.files` (§11). `glob`
// vacío = sin filtro; `max <= 0` = sin límite.
type filesOpts struct {
	glob   string
	hidden bool
	max    int
}

// parseFilesOpts lee `opts` de `nu.search.files` bajo el token (toca Lua). Valida
// los tipos a `EINVAL`; `opts` ausente → defaults (sin glob, sin ocultos, sin
// tope). Un `glob` sintácticamente inválido se rechaza ya aquí (filepath.Match
// lo valida sobre una cadena trivial) para que el error sea accionable antes de
// suspender, no a mitad del recorrido.
func parseFilesOpts(L *lua.LState) (filesOpts, bool) {
	o := filesOpts{}
	v := L.Get(2)
	if v == lua.LNil {
		return o, true
	}
	tbl, ok := v.(*lua.LTable)
	if !ok {
		raiseError(L, CodeEINVAL, "nu.search.files: opts debe ser una tabla", lua.LNil)
		return o, false
	}
	if g := tbl.RawGetString("glob"); g != lua.LNil {
		gs, ok := g.(lua.LString)
		if !ok {
			raiseError(L, CodeEINVAL, "nu.search.files: opts.glob debe ser un string", lua.LNil)
			return o, false
		}
		// Valida el patrón ya: `filepath.Match` solo falla por patrón malformado.
		if _, err := filepath.Match(string(gs), ""); err != nil {
			raiseError(L, CodeEINVAL, "nu.search.files: opts.glob inválido: "+err.Error(), lua.LNil)
			return o, false
		}
		o.glob = string(gs)
	}
	if h := tbl.RawGetString("hidden"); h != lua.LNil {
		o.hidden = lua.LVAsBool(h)
	}
	if m := tbl.RawGetString("max"); m != lua.LNil {
		mn, ok := m.(lua.LNumber)
		if !ok {
			raiseError(L, CodeEINVAL, "nu.search.files: opts.max debe ser un número", lua.LNil)
			return o, false
		}
		o.max = int(mn)
	}
	return o, true
}

// searchFiles implementa `nu.search.files(root, opts?) -> string[]` ⏸ (§11):
// listado **recursivo** de ficheros bajo `root`, respetando `.gitignore` (G7).
// El recorrido del árbol (IO pesado) va en la goroutine de fondo; las rutas
// cruzan a Lua como array en la `deliverFn`. `root` inexistente → `ENOENT`.
func (rt *Runtime) searchFiles(L *lua.LState) int {
	if !rt.requireTask(L, "nu.search.files") {
		return 0
	}
	root := L.CheckString(1)
	opts, ok := parseFilesOpts(L)
	if !ok {
		return 0 // parseFilesOpts ya lanzó EINVAL
	}

	vals := rt.sched.suspend(L, func() deliverFn {
		paths, err := walkFiles(root, opts) // recorrido bloqueante, fuera del token
		return func(L *lua.LState) []lua.LValue {
			if err != nil {
				mapFsError(L, err)
				return nil
			}
			arr := L.NewTable()
			for i, p := range paths {
				arr.RawSetInt(i+1, lua.LString(p))
			}
			return []lua.LValue{arr}
		}
	})
	return pushAll(L, vals)
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
var errFilesMaxReached = errors.New("nu.search.files: max alcanzado")

// --- nu.search.fuzzy ----------------------------------------------------------

// fuzzyMatch es un candidato que casó: su índice 1-based en `candidates` y su
// score. Se ordena por score desc, conservando el orden de entrada en los
// empates (estabilidad, inventario 🔒).
type fuzzyMatch struct {
	index int // 1-based en candidates (lo que se devuelve a Lua)
	score int
}

// searchFuzzy implementa `nu.search.fuzzy(query, candidates, opts?) -> {index,
// score}[]` (§11). **SÍNCRONO y NO ⏸** (la primitiva caliente del picker, §11):
// no usa el puente `suspend` —es CPU puro sobre datos ya en memoria, como `nu.re`
// o los codecs—. Devuelve los candidatos que casan, **ordenados por score
// descendente de forma estable** (empates → orden de entrada, inventario 🔒); los
// que no casan se excluyen. `opts.max` recorta a los N mejores.
func (rt *Runtime) searchFuzzy(L *lua.LState) int {
	query := L.CheckString(1)
	candTbl := L.CheckTable(2)
	max := 0
	if v := L.Get(3); v != lua.LNil {
		tbl, ok := v.(*lua.LTable)
		if !ok {
			raiseError(L, CodeEINVAL, "nu.search.fuzzy: opts debe ser una tabla", lua.LNil)
			return 0
		}
		if m := tbl.RawGetString("max"); m != lua.LNil {
			mn, ok := m.(lua.LNumber)
			if !ok {
				raiseError(L, CodeEINVAL, "nu.search.fuzzy: opts.max debe ser un número", lua.LNil)
				return 0
			}
			max = int(mn)
		}
	}

	// Materializa los candidatos en orden (1..n). Un elemento no-string en la parte
	// array es un uso malo → `EINVAL` accionable (el picker pasa una lista de rutas).
	n := candTbl.Len()
	matches := make([]fuzzyMatch, 0, n)
	for i := 1; i <= n; i++ {
		cv := candTbl.RawGetInt(i)
		cs, ok := cv.(lua.LString)
		if !ok {
			raiseError(L, CodeEINVAL, "nu.search.fuzzy: candidates debe ser un array de strings", lua.LNil)
			return 0
		}
		if score, ok := fuzzyScore(query, string(cs)); ok {
			matches = append(matches, fuzzyMatch{index: i, score: score})
		}
	}

	// Orden ESTABLE por score descendente: `sort.SliceStable` conserva el orden de
	// entrada entre elementos de igual score (la estabilidad que el inventario 🔒
	// exige; un picker con empates debe mostrar los candidatos en su orden natural,
	// no barajados). Solo se compara por score, NUNCA por índice: si se rompieran
	// empates por índice se perdería la estabilidad frente a un orden de entrada
	// arbitrario.
	sort.SliceStable(matches, func(a, b int) bool {
		return matches[a].score > matches[b].score
	})
	if max > 0 && len(matches) > max {
		matches = matches[:max]
	}

	out := L.NewTable()
	for i, m := range matches {
		t := L.NewTable()
		t.RawSetString("index", lua.LNumber(m.index))
		t.RawSetString("score", lua.LNumber(m.score))
		out.RawSetInt(i+1, t)
	}
	L.Push(out)
	return 1
}

// fuzzyScore calcula el score de `query` contra `cand` con un scorer de
// **subsecuencia con bonus** estilo fzf simplificado (decisión propia,
// claude_decisions.md S27): los caracteres de `query` deben aparecer en `cand`
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

// --- nu.search.grep -----------------------------------------------------------

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

	max       int
	emitted   int
	closeOnce sync.Once
}

// grepOpts son las opciones ya parseadas de `nu.search.grep` (§11).
type grepOpts struct {
	root       string
	glob       string
	ignoreCase bool
	max        int
}

// parseGrepOpts lee `opts` de `nu.search.grep` bajo el token. `opts.root` es
// obligatorio (¿dónde buscar?); `glob`, `case`, `max` opcionales. `case`
// (string "sensitive"|"insensitive") elige sensibilidad; default sensible. Todo
// uso malo → `EINVAL` accionable antes de suspender.
func parseGrepOpts(L *lua.LState) (grepOpts, bool) {
	o := grepOpts{}
	tbl, ok := L.Get(2).(*lua.LTable)
	if !ok {
		raiseError(L, CodeEINVAL, "nu.search.grep: opts (con root) es obligatorio", lua.LNil)
		return o, false
	}
	root, ok := tbl.RawGetString("root").(lua.LString)
	if !ok || string(root) == "" {
		raiseError(L, CodeEINVAL, "nu.search.grep: opts.root es obligatorio", lua.LNil)
		return o, false
	}
	o.root = string(root)
	if g := tbl.RawGetString("glob"); g != lua.LNil {
		gs, ok := g.(lua.LString)
		if !ok {
			raiseError(L, CodeEINVAL, "nu.search.grep: opts.glob debe ser un string", lua.LNil)
			return o, false
		}
		if _, err := filepath.Match(string(gs), ""); err != nil {
			raiseError(L, CodeEINVAL, "nu.search.grep: opts.glob inválido: "+err.Error(), lua.LNil)
			return o, false
		}
		o.glob = string(gs)
	}
	if cs := tbl.RawGetString("case"); cs != lua.LNil {
		s, ok := cs.(lua.LString)
		if !ok {
			raiseError(L, CodeEINVAL, "nu.search.grep: opts.case debe ser un string", lua.LNil)
			return o, false
		}
		switch string(s) {
		case "sensitive":
			o.ignoreCase = false
		case "insensitive":
			o.ignoreCase = true
		default:
			raiseError(L, CodeEINVAL, `nu.search.grep: opts.case debe ser "sensitive" o "insensitive"`, lua.LNil)
			return o, false
		}
	}
	if m := tbl.RawGetString("max"); m != lua.LNil {
		mn, ok := m.(lua.LNumber)
		if !ok {
			raiseError(L, CodeEINVAL, "nu.search.grep: opts.max debe ser un número", lua.LNil)
			return o, false
		}
		o.max = int(mn)
	}
	return o, true
}

// searchGrep implementa `nu.search.grep(pattern, opts) -> iterator` ⏸ (§11).
// Compila el patrón (RE2, S26; `EINVAL` si inválido), arranca el pool de
// goroutines de fondo que recorren el árbol y casan línea a línea, y devuelve una
// **función iteradora** que en cada `next` suspende hasta el siguiente match
// (`nil` al agotarse). La vida del pool se ata a la task vía `nu.task.cleanup`:
// cancelar/terminar la task cancela el contexto y para las goroutines.
func (rt *Runtime) searchGrep(L *lua.LState) int {
	if !rt.requireTask(L, "nu.search.grep") {
		return 0
	}
	pattern := L.CheckString(1)
	opts, ok := parseGrepOpts(L)
	if !ok {
		return 0
	}

	// Compila el patrón bajo el token (CPU puro, no hace falta suspender). Con
	// `case = insensitive` se antepone el flag `(?i)` de RE2. Patrón inválido (o
	// backreference, que RE2 no admite) → `EINVAL` claro, igual que `nu.re.compile`.
	pat := pattern
	if opts.ignoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		raiseError(L, CodeEINVAL, "nu.search.grep: patrón inválido: "+err.Error(), lua.LNil)
		return 0
	}

	// El árbol de ficheros candidatos se enumera **antes** de arrancar el pool, en
	// la goroutine de fondo (es IO): así un `root` inexistente da `ENOENT` en la
	// creación del iterador, no a mitad del consumo. Reusa `walkFiles` con el glob
	// (gitignore siempre, como `files`).
	var (
		files   []string
		walkErr error
	)
	rt.sched.suspend(L, func() deliverFn {
		files, walkErr = walkFiles(opts.root, filesOpts{glob: opts.glob})
		return func(L *lua.LState) []lua.LValue { return nil }
	})
	if walkErr != nil {
		mapFsError(L, walkErr)
		return 0
	}

	it := newGrepIter(rt.sched, re, files, opts.max)
	rt.sched.trackGrep(it)

	// Vida del iterador (idioma de §6): se cierra al cancelar/terminar la task.
	// Registrar el `cleanup` aquí (no en el `next`) garantiza que aunque la task no
	// consuma el iterador hasta el final, las goroutines se cancelen al terminar.
	rt.registerGrepCleanup(L, it)

	// La función iteradora: cada `next` suspende hasta el siguiente match o EOF.
	L.Push(L.NewFunction(func(L *lua.LState) int {
		if !rt.requireTask(L, "nu.search.grep (next)") {
			return 0
		}
		// Alcanzado `max`: corta sin suspender (cierra el pool y reporta fin).
		if it.max > 0 && it.emitted >= it.max {
			it.close()
			L.Push(lua.LNil)
			return 1
		}
		vals := rt.sched.suspend(L, func() deliverFn {
			res, ok := <-it.results
			return func(L *lua.LState) []lua.LValue {
				if !ok {
					it.close() // canal cerrado: pool agotado, fin del iterador
					return []lua.LValue{lua.LNil}
				}
				it.emitted++
				if it.max > 0 && it.emitted >= it.max {
					it.close() // este es el último: para el resto del pool
				}
				return []lua.LValue{grepResultToLua(L, res)}
			}
		})
		return pushAll(L, vals)
	}))
	// Devolvemos SOLO la función iteradora a Lua (el `for r in nu.search.grep(...)`
	// toma el primer valor como iterador). El `cleanup` ya quedó registrado arriba.
	return 1
}

// registerGrepCleanup registra en la task actual un `nu.task.cleanup` que cierra
// el iterador `it` —para que cancelar/terminar la task pare el pool de grep sin
// fuga de goroutines (S08, §6)—. Se hace en Go (no en Lua) porque el iterador es
// un handle Go opaco; la pila LIFO de cleanups vive en la task y corre bajo el
// token, así que añadirle un liberador aquí (estamos bajo el token) es seguro.
func (rt *Runtime) registerGrepCleanup(L *lua.LState, it *grepIter) {
	t, hasTask := rt.sched.taskOf(L)
	if hasTask {
		t.cleanups = append(t.cleanups, L.NewFunction(func(L *lua.LState) int {
			it.close()
			return 0
		}))
	}
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
	// señal de EOF para el consumidor (`<-results` devuelve `ok=false`).
	go func() {
		wg.Wait()
		close(it.results)
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
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Sube el tope de línea del Scanner (default 64 KiB) a un valor holgado: una
	// línea muy larga (un JSON minificado, un fichero generado) no debe abortar el
	// grep de ese fichero. Más allá de este tope se ignora el resto de la línea.
	sc.Buffer(make([]byte, 0, 64*1024), grepMaxLine)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if it.ctx.Err() != nil {
			return // cancelado entre líneas
		}
		line := sc.Text()
		idxs := re.FindAllStringIndex(line, -1)
		if idxs == nil {
			continue
		}
		ranges := make([][2]int, len(idxs))
		for i, pair := range idxs {
			// Mismo convenio que `nu.re.find_all` (S26): byte 1-based, ambos inclusive.
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

// grepResultToLua construye la tabla `{path, line_no, line, ranges}` de un match
// para entregarla a Lua. Corre bajo el token (es la `deliverFn` del `next`). Cada
// rango es `{start, end}` (byte 1-based inclusive), coherente con `nu.re.find_all`
// (S26): `line:sub(r[1], r[2])` reconstruye el tramo casado.
func grepResultToLua(L *lua.LState, res grepResult) lua.LValue {
	t := L.NewTable()
	t.RawSetString("path", lua.LString(res.path))
	t.RawSetString("line_no", lua.LNumber(res.lineNo))
	t.RawSetString("line", lua.LString(res.line))
	ranges := L.NewTable()
	for i, r := range res.ranges {
		rg := L.NewTable()
		rg.RawSetInt(1, lua.LNumber(r[0]))
		rg.RawSetInt(2, lua.LNumber(r[1]))
		ranges.RawSetInt(i+1, rg)
	}
	t.RawSetString("ranges", ranges)
	return t
}

// close cancela el pool de goroutines de fondo y deja de rastrear el iterador.
// **Idempotente** (`closeOnce`): lo llaman el `next` (al alcanzar `max` o EOF),
// el `cleanup` de la task (al cancelarse/terminar) y `Runtime.Close` (red de
// seguridad). Cancelar el contexto desbloquea el repartidor y las goroutines, que
// terminan; la cerradora cierra `results`.
//
// El rastreo (`untrackGrep`) sólo se deshace si hay scheduler: el backend gopher
// siempre lo pasa (`newGrepIter(rt.sched, ...)`), pero el backend wasm (M13b,
// vmwasm_search.go) reusa este mismo iterador con `s == nil` cuando el Runtime de
// pruebas es mínimo — igual que `luaWs.close` sólo desregistra si hay scheduler.
// Cancelar el contexto (el núcleo VM-agnóstico) siempre ocurre; sólo el rastreo
// para `Runtime.Close` es opcional. Para gopher el comportamiento es idéntico.
func (it *grepIter) close() {
	it.closeOnce.Do(func() {
		it.cancel()
		if it.s != nil {
			it.s.untrackGrep(it)
		}
	})
}
