package runtime

// Catálogo de nu.search sobre el backend wasm (M13b, §11). Contraparte de
// search.go: las tres primitivas del módulo —files (⏸), grep (⏸, con handle
// iterador) y fuzzy (síncrona)— reusando ÍNTEGRO el núcleo VM-agnóstico de la
// implementación gopher: el recorrido del árbol (`walkFiles`), el scorer difuso
// (`fuzzyScore`), el pool de goroutines del grep (`newGrepIter`/`grepFile`) y sus
// structs (`filesOpts`/`grepOpts`/`grepResult`/`grepIter`). Aquí sólo cambia el
// marshaling de la frontera (mapas del wire en vez de `*lua.LTable`) y la forma de
// despachar el iterador de grep (handle + método ⏸ en vez de una clausura Lua).
//
// EL RETO DEL grep ⏸ + HANDLE ITERADOR. En gopher `nu.search.grep` devuelve una
// **clausura Lua** que suspende en cada llamada; el wire no cruza clausuras. El
// modelo wasm equivalente (DM3) es un HANDLE: `nu.search._grep` es una primitiva ⏸
// que enumera el árbol (IO, cede al scheduler) y, ya con la lista de ficheros,
// arranca el pool de goroutines y registra el iterador como handle `GrepIter`
// (mismo idioma que `ws._connect`: primitiva ⏸ que además crea handle vía
// `inst.AllocHandle` — sólo toca la `handleTable`, no la VM). El wrapper Lua
// `nu.search.grep` envuelve ese handle en una clausura que en cada iteración llama
// `GrepIter:next` por `__hcall_s`, reconstruyendo el `for r in nu.search.grep(...)`.
//
// EL MÉTODO next ⏸. `GrepIter:next` es el gemelo exacto de la clausura `next` de
// search.go: corta sin recibir si ya se alcanzó `max`, si no bloquea en el canal
// `results` (en la goroutine de fondo del scheduler, porque se despacha por
// __hcall_s), y al entregar cuenta `emitted` y cierra el pool al llegar al tope o
// al EOF (canal cerrado → nil, fin del iterador). Como una task consume el iterador
// secuencialmente (cada `next` espera al anterior), no hay dos `next` concurrentes
// sobre el mismo `grepIter`: `emitted`/`close` no necesitan candado extra.
//
// CICLO DE VIDA. El pool se ata a `grepIter.close` (idempotente, `closeOnce`), que
// lo disparan el propio `next` (al agotar/tope) y —vía scheduler— `Runtime.Close`.
// El rastreo para el apagado ordenado (`stopAllGreps`) sólo se activa si hay
// scheduler (`rt.sched != nil`); en M13b el rt de los tests es mínimo, así que el
// grep se pasa con `s == nil` y `close` sólo cancela el contexto (ver la nota de
// `grepIter.close`). El `cleanup` de la task por cancelación (registrado en Lua en
// el backend gopher) es cosa del cableado real del Runtime (M13d), no de M13b.

import (
	"path/filepath"
	"regexp"
	"sort"

	"github.com/dbareagimeno/nu/internal/vmwasm"
)

func registerSearchWasm(p *vmwasm.Pool, rt *Runtime) {
	// nu.search.files(root, opts?) -> string[] ⏸ — listado recursivo bajo `root`
	// respetando `.gitignore` (G7). El recorrido (IO pesado) corre en la goroutine
	// de fondo; las rutas cruzan como array. `root` inexistente → ENOENT; opts mal
	// tipadas → EINVAL (reusa `walkFiles` y el mapeo de errores de fs).
	p.RegisterSuspending("search.files", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		root := argString(args, 0)
		opts, err := parseFilesOptsWasm(arg(args, 1))
		if err != nil {
			return nil, err
		}
		paths, werr := walkFiles(root, opts)
		if werr != nil {
			return nil, mapFsErrorWasm(werr)
		}
		arr := make([]any, len(paths))
		for i, pth := range paths {
			arr[i] = pth
		}
		return []any{arr}, nil
	})

	// nu.search.fuzzy(query, candidates, opts?) -> {index, score}[] — SÍNCRONA (la
	// primitiva caliente del picker): reusa `fuzzyScore` y el orden estable por score
	// descendente. candidates no-array o con no-strings → EINVAL; opts.max recorta.
	p.Register("search.fuzzy", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		query := argString(args, 0)
		cands, ok := arg(args, 1).([]any)
		if !ok {
			return nil, einvalSearch("fuzzy", "candidates debe ser un array de strings")
		}
		max := 0
		if o := arg(args, 2); o != nil {
			opts, ok := o.(map[string]any)
			if !ok {
				return nil, einvalSearch("fuzzy", "opts debe ser una tabla")
			}
			if mv, present := opts["max"]; present && mv != nil {
				mn, ok := httpNum(mv)
				if !ok {
					return nil, einvalSearch("fuzzy", "opts.max debe ser un número")
				}
				max = int(mn)
			}
		}

		matches := make([]fuzzyMatch, 0, len(cands))
		for i, cv := range cands {
			cs, ok := cv.(string)
			if !ok {
				return nil, einvalSearch("fuzzy", "candidates debe ser un array de strings")
			}
			if score, ok := fuzzyScore(query, cs); ok {
				matches = append(matches, fuzzyMatch{index: i + 1, score: score})
			}
		}
		// Orden ESTABLE por score descendente (inventario 🔒): SliceStable conserva el
		// orden de entrada en los empates; se compara sólo por score, nunca por índice.
		sort.SliceStable(matches, func(a, b int) bool {
			return matches[a].score > matches[b].score
		})
		if max > 0 && len(matches) > max {
			matches = matches[:max]
		}
		out := make([]any, len(matches))
		for i, m := range matches {
			out[i] = map[string]any{"index": int64(m.index), "score": int64(m.score)}
		}
		return []any{out}, nil
	})

	// nu.search._grep(pattern, opts) -> GrepIter ⏸ — el wrapper nu.search.grep lo
	// envuelve. Compila el patrón (RE2; EINVAL si inválido o insensible con "(?i)"),
	// enumera el árbol bajo opts.root (IO, cede al scheduler; root inexistente →
	// ENOENT), arranca el pool y registra el iterador como handle. opts sin root, mal
	// tipadas o case inválido → EINVAL, todo desde el núcleo compartido de search.go.
	p.RegisterSuspending("search._grep", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		pattern := argString(args, 0)
		opts, err := parseGrepOptsWasm(arg(args, 1))
		if err != nil {
			return nil, err
		}
		pat := pattern
		if opts.ignoreCase {
			pat = "(?i)" + pat
		}
		re, cerr := regexp.Compile(pat)
		if cerr != nil {
			return nil, einvalSearch("grep", "patrón inválido: "+cerr.Error())
		}
		// Enumera los ficheros candidatos ANTES de arrancar el pool (mismo orden que
		// search.go): así un root inexistente da ENOENT en la creación, no a mitad del
		// consumo. gitignore siempre activo, como en `files`.
		files, werr := walkFiles(opts.root, filesOpts{glob: opts.glob})
		if werr != nil {
			return nil, mapFsErrorWasm(werr)
		}
		// `rt.sched` (el scheduler gopher) es nil en el rt mínimo de M13b: se pasa igual
		// —`newGrepIter` lo guarda y `close` sólo lo usa si no es nil— y el rastreo para
		// `Runtime.Close` se gatea, igual que en vmwasm_ws.go.
		it := newGrepIter(rt.sched, re, files, opts.max)
		if rt.sched != nil {
			rt.sched.trackGrep(it)
		}
		return []any{inst.AllocHandle("GrepIter", it)}, nil
	})

	// GrepIter:next() -> {path, line_no, line, ranges}? ⏸ — el siguiente match, o nil
	// al agotarse. Gemelo de la clausura `next` de search.go: corta en `max`, bloquea
	// en el canal en la goroutine de fondo (se despacha por __hcall_s) y cierra el
	// pool al alcanzar el tope o el EOF (canal cerrado). Consumido en secuencia por
	// una task, no hay dos `next` a la vez sobre el mismo iterador.
	p.RegisterHandleMethod("GrepIter", "next", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		it := val.(*grepIter)
		// Alcanzado `max`: corta sin recibir (cierra el pool y reporta fin).
		if it.max > 0 && it.emitted >= it.max {
			it.close()
			return []any{nil}, nil
		}
		res, ok := <-it.results
		if !ok {
			it.close() // canal cerrado: pool agotado, fin del iterador
			return []any{nil}, nil
		}
		it.emitted++
		if it.max > 0 && it.emitted >= it.max {
			it.close() // este es el último: para el resto del pool
		}
		return []any{grepResultToWasm(res)}, nil
	})

	// Wrapper Lua: nu.search.grep envuelve el handle de _grep en una clausura
	// iteradora que en cada paso llama GrepIter:next por __hcall_s (⏸). Reconstruye
	// el `for r in nu.search.grep(pattern, opts) do ... end` del contrato (§11); el
	// primer valor del `for` es la función iteradora, como en el backend gopher.
	p.AddPreludio(`
nu.search = nu.search or {}
function nu.search.grep(pattern, opts)
  local it = nu.search._grep(pattern, opts)   -- handle {__id} tras enumerar el árbol
  return function()
    return __hcall_s(it.__id, "next")
  end
end`)
}

// parseFilesOptsWasm extrae filesOpts del mapa `opts` que cruzó el wire. Mismo
// contrato que parseFilesOpts (§11) del backend gopher: glob (string, validado ya
// con filepath.Match sobre trivial), hidden (bool), max (número); opts ausente →
// defaults; un valor mal tipado → EINVAL accionable antes de suspender.
func parseFilesOptsWasm(v any) (filesOpts, error) {
	o := filesOpts{}
	if v == nil {
		return o, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return o, einvalSearch("files", "opts debe ser una tabla")
	}
	if gv, present := m["glob"]; present && gv != nil {
		gs, ok := gv.(string)
		if !ok {
			return o, einvalSearch("files", "opts.glob debe ser un string")
		}
		if err := validGlob(gs); err != nil {
			return o, einvalSearch("files", "opts.glob inválido: "+err.Error())
		}
		o.glob = gs
	}
	if hv, present := m["hidden"]; present && hv != nil {
		o.hidden, _ = hv.(bool)
	}
	if mv, present := m["max"]; present && mv != nil {
		mn, ok := httpNum(mv)
		if !ok {
			return o, einvalSearch("files", "opts.max debe ser un número")
		}
		o.max = int(mn)
	}
	return o, nil
}

// parseGrepOptsWasm extrae grepOpts del mapa `opts` que cruzó el wire. Mismo
// contrato que parseGrepOpts (§11): root obligatorio (¿dónde buscar?); glob, case
// ("sensitive"|"insensitive", default sensible) y max opcionales; uso malo →
// EINVAL. El equivalente de parseGrepOpts sin `*lua.LState`.
func parseGrepOptsWasm(v any) (grepOpts, error) {
	o := grepOpts{}
	m, ok := v.(map[string]any)
	if !ok {
		return o, einvalSearch("grep", "opts (con root) es obligatorio")
	}
	root, ok := m["root"].(string)
	if !ok || root == "" {
		return o, einvalSearch("grep", "opts.root es obligatorio")
	}
	o.root = root
	if gv, present := m["glob"]; present && gv != nil {
		gs, ok := gv.(string)
		if !ok {
			return o, einvalSearch("grep", "opts.glob debe ser un string")
		}
		if err := validGlob(gs); err != nil {
			return o, einvalSearch("grep", "opts.glob inválido: "+err.Error())
		}
		o.glob = gs
	}
	if cv, present := m["case"]; present && cv != nil {
		cs, ok := cv.(string)
		if !ok {
			return o, einvalSearch("grep", "opts.case debe ser un string")
		}
		switch cs {
		case "sensitive":
			o.ignoreCase = false
		case "insensitive":
			o.ignoreCase = true
		default:
			return o, einvalSearch("grep", `opts.case debe ser "sensitive" o "insensitive"`)
		}
	}
	if mv, present := m["max"]; present && mv != nil {
		mn, ok := httpNum(mv)
		if !ok {
			return o, einvalSearch("grep", "opts.max debe ser un número")
		}
		o.max = int(mn)
	}
	return o, nil
}

// grepResultToWasm construye el mapa {path, line_no, line, ranges} de un match para
// cruzar el wire. Gemelo de grepResultToLua (search.go): cada rango es {start, end}
// en BYTES 1-based inclusive (convenio de nu.re.find_all, S26), como array de dos
// enteros; `line:sub(r[1], r[2])` reconstruye el tramo casado.
func grepResultToWasm(res grepResult) map[string]any {
	ranges := make([]any, len(res.ranges))
	for i, r := range res.ranges {
		ranges[i] = []any{int64(r[0]), int64(r[1])}
	}
	return map[string]any{
		"path":    res.path,
		"line_no": int64(res.lineNo),
		"line":    res.line,
		"ranges":  ranges,
	}
}

// validGlob valida un patrón de glob igual que el backend gopher: filepath.Match
// sólo falla por patrón malformado, así que se prueba contra una cadena trivial
// para rechazar el error accionable antes de suspender.
func validGlob(glob string) error {
	_, err := filepath.Match(glob, "")
	return err
}

// einvalSearch acuña el error EINVAL del módulo con el mismo prefijo que el backend
// gopher ("nu.search.<fn>: ...").
func einvalSearch(fn, msg string) error {
	return &vmwasm.StructuredError{Code: CodeEINVAL, Message: "nu.search." + fn + ": " + msg}
}
