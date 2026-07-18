package runtime

// Catálogo de enu.text sobre el backend wasm (§10). Contraparte de text.go/markdown.go/
// highlight.go/diff.go: reusa ÍNTEGRO el núcleo puro Go de cada función (uniseg,
// wrapText, renderMarkdownBlocks, highlightToBlock, computeDiff/renderDiffBlock) y sólo
// cambia el marshaling de la frontera (el wire ya lo resuelve) y la forma de devolver
// un Block.
//
// UN BLOCK CRUZA COMO HANDLE (M13c). Las que producen Blocks —wrap, markdown,
// highlight, diff— asignan el `*block` real (block.go, que implementa vmwasm.BlockObj)
// a la tabla de handles de la Instance con `inst.AllocHandle("Block", blk)` —seguro
// desde una primitiva síncrona (hilo principal), como `proc._spawn`— y devuelven
// `{id, width, height}`; un wrapper Lua fino (AddPreludio) lo envuelve como el handle
// opaco con `.width`/`.height` legibles (§9.2), la MISMA forma que `enu.ui.block`. El
// tipo "Block" es el que `Region:blit` resuelve (vmwasm/ui.go), así que un Block de
// `enu.text.*` se blittea igual que uno de `enu.ui.block`. Ninguna de estas funciones ⏸
// (CPU puro, §10): se registran con `p.Register`.
//
// El registro del handle "Block" no necesita métodos (un Block es opaco, sólo tiene
// dimensiones); su ciclo de vida es el del handleTable de la Instance (muere con ella,
// como el de gopher muere por GC).

import (
	"fmt"
	"strconv"

	"github.com/rivo/uniseg"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

func registerTextWasm(p *vmwasm.Pool, rt *Runtime) {
	// enu.text.width(s) -> integer: anchura en celdas (graphemes, east-asian, emoji).
	p.Register("text.width", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		return []any{int64(uniseg.StringWidth(argString(args, 0)))}, nil
	})
	// enu.text.truncate(s, width, opts?) -> string: recorte por grapheme con elipsis
	// opcional (opts.ellipsis). width negativo → EINVAL.
	p.Register("text.truncate", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		s := argString(args, 0)
		width := argInt(args, 1)
		if width < 0 {
			return nil, einvalText("enu.text.truncate: width no puede ser negativo")
		}
		ellipsis := ""
		if opts, ok := arg(args, 2).(map[string]any); ok {
			ellipsis, _ = opts["ellipsis"].(string)
		}
		return []any{truncateText(s, width, ellipsis)}, nil
	})

	registerTextBlocksWasm(p)
}

// registerTextBlocksWasm cuelga las funciones de enu.text que producen Blocks (§10):
// wrap/markdown/highlight/diff. Se registran con nombre `text._*` (primitiva cruda que
// devuelve `{id, width, height}`) y un wrapper Lua les da la forma pública `enu.text.*`
// con el Block ya como handle. Separado por volumen, no por semántica.
func registerTextBlocksWasm(p *vmwasm.Pool) {
	// enu.text.wrap(s, width, opts?) -> Block: word-wrap a `width` celdas (§10). opts.style
	// es un Style por defecto para cada span. width <= 0 → EINVAL.
	p.Register("text._wrap", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		s := argString(args, 0)
		width := argInt(args, 1)
		if width <= 0 {
			return nil, einvalText("enu.text.wrap: width debe ser un entero positivo")
		}
		var defStyle *style
		if opts, ok := arg(args, 2).(map[string]any); ok {
			if styleVal, ok := opts["style"]; ok && styleVal != nil {
				parsed, err := parseStyleWasm(styleVal)
				if err != nil {
					return nil, einvalText("enu.text.wrap: opts.style: " + err.Error())
				}
				defStyle = parsed
			}
		}
		textLines := wrapText(s, width)
		blockLines := make([][]span, len(textLines))
		for i, ln := range textLines {
			blockLines[i] = []span{{text: ln, st: defStyle}}
		}
		return blockResult(inst, newBlock(blockLines)), nil
	})

	// enu.text.markdown(s, opts) -> Block: render de markdown a un Block de ancho
	// opts.width (obligatorio), themable por opts.theme (§10). width no entero positivo
	// → EINVAL.
	p.Register("text._markdown", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		s := argString(args, 0)
		opts, ok := arg(args, 1).(map[string]any)
		if !ok {
			return nil, einvalText("enu.text.markdown: opts.width (entero positivo) es obligatorio")
		}
		width, ok := optIntWasm(opts, "width")
		if !ok || width <= 0 {
			return nil, einvalText("enu.text.markdown: opts.width debe ser un entero positivo")
		}
		theme := defaultTheme()
		if themeV, ok := opts["theme"]; ok && themeV != nil {
			if err := applyMarkdownThemeWasm(&theme, themeV); err != nil {
				return nil, einvalText("enu.text.markdown: opts.theme." + err.Error())
			}
		}
		blocks := renderMarkdownBlocks(s, width, &theme)
		var lines [][]span
		for _, bl := range blocks {
			lines = append(lines, bl...)
		}
		if len(lines) == 0 {
			lines = [][]span{{}} // un Block siempre tiene al menos una línea (height >= 1)
		}
		return blockResult(inst, newBlock(lines)), nil
	})

	// enu.text.highlight(code, lang, opts?) -> Block: syntax highlighting (§10). Lenguaje
	// desconocido/vacío → texto plano (no error); opts.theme (string) elige el theme de
	// Chroma. lang/opts mal tipados → EINVAL.
	p.Register("text._highlight", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		code := argString(args, 0)
		lang := ""
		if v := arg(args, 1); v != nil {
			s, ok := v.(string)
			if !ok {
				return nil, einvalText("enu.text.highlight: lang debe ser un string")
			}
			lang = s
		}
		themeName := defaultHighlightTheme
		if v := arg(args, 2); v != nil {
			opts, ok := v.(map[string]any)
			if !ok {
				return nil, einvalText("enu.text.highlight: opts debe ser una tabla")
			}
			if tv, ok := opts["theme"]; ok && tv != nil {
				ts, ok := tv.(string)
				if !ok {
					return nil, einvalText("enu.text.highlight: opts.theme debe ser un nombre de theme (string)")
				}
				themeName = ts
			}
		}
		return blockResult(inst, highlightToBlock(code, lang, themeName)), nil
	})

	// enu.text.diff(a, b, opts?) -> {hunks, block?}: diff estructurado línea a línea
	// (§10). opts.render añade el Block pintado; opts.theme lo colorea. opts/theme mal
	// formados → EINVAL.
	p.Register("text._diff", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		a := argString(args, 0)
		b := argString(args, 1)
		render := false
		theme := defaultDiffTheme()
		if v := arg(args, 2); v != nil {
			opts, ok := v.(map[string]any)
			if !ok {
				return nil, einvalText("enu.text.diff: opts debe ser una tabla")
			}
			render = wasmTruthy(opts["render"])
			if themeV, ok := opts["theme"]; ok && themeV != nil {
				if err := applyDiffThemeWasm(&theme, themeV); err != nil {
					return nil, einvalText("enu.text.diff: opts.theme." + err.Error())
				}
			}
		}
		hunks := computeDiff(a, b)
		result := map[string]any{"hunks": hunksToWire(hunks)}
		if render {
			blk := renderDiffBlock(hunks, &theme)
			result["block"] = map[string]any{
				"id":     int64(inst.AllocHandle("Block", blk)),
				"width":  int64(blk.width),
				"height": int64(blk.height),
			}
		}
		return []any{result}, nil
	})

	// Wrappers Lua: dan a enu.text.* su forma pública, envolviendo el `{id, width,
	// height}` que devuelven las primitivas crudas como el handle opaco de un Block
	// (§9.2), con la metatable de handles (__handle_mt, del preludio base) para que
	// `.width`/`.height` sean legibles y el Block cruce a `Region:blit` como W_HANDLE.
	p.AddPreludioW(`
enu.text = enu.text or {}
local function __wrap_block(m)
  -- Block OPACO (§10): __block_mt cruza como handle por __id pero deja .lines y demás
  -- claves de contenido en nil (no como funciones-método de __handle_mt).
  return setmetatable({ __id = m.id, width = m.width, height = m.height }, __block_mt)
end
function enu.text.wrap(s, width, opts)      return __wrap_block(enu.text._wrap(s, width, opts)) end
function enu.text.markdown(s, opts)         return __wrap_block(enu.text._markdown(s, opts)) end
function enu.text.highlight(code, lang, opts) return __wrap_block(enu.text._highlight(code, lang, opts)) end
function enu.text.diff(a, b, opts)
  local r = enu.text._diff(a, b, opts)
  if r.block then r.block = __wrap_block(r.block) end
  return r
end`, "text._wrap", "text._markdown", "text._highlight", "text._diff")
}

// blockResult empaqueta un `*block` recién construido como el retorno de una primitiva
// productora de Block: asigna su handle (tipo "Block") en la Instance y devuelve
// `{id, width, height}`, que el wrapper Lua envuelve. Asignar el handle desde una
// primitiva síncrona es seguro (hilo principal), como en `proc._spawn`.
func blockResult(inst *vmwasm.Instance, blk *block) []any {
	return []any{map[string]any{
		"id":     int64(inst.AllocHandle("Block", blk)),
		"width":  int64(blk.width),
		"height": int64(blk.height),
	}}
}

// hunksToWire convierte los hunks de `computeDiff` a la estructura del wire que expone
// `enu.text.diff` (§10): un array de hunks, cada uno con sus cuatro rangos (1-based) y un
// array `lines` de `{kind, text}`. Espejo VM-agnóstico de `hunksToLua`. Sin hunks → un
// array vacío (el consumidor distingue "sin cambios" por `#hunks == 0`).
func hunksToWire(hunks []diffHunk) []any {
	arr := make([]any, 0, len(hunks))
	for _, h := range hunks {
		lines := make([]any, 0, len(h.lines))
		for _, op := range h.lines {
			lines = append(lines, map[string]any{"kind": op.kind, "text": op.text})
		}
		arr = append(arr, map[string]any{
			"old_start": int64(h.oldStart),
			"old_count": int64(h.oldCount),
			"new_start": int64(h.newStart),
			"new_count": int64(h.newCount),
			"lines":     lines,
		})
	}
	return arr
}

// applyMarkdownThemeWasm rellena un `markdownTheme` con los Styles de opts.theme (del
// wire): claves por elemento ("h1".."h6", code, emphasis, strong, link, bullet,
// blockquote, rule). Espejo VM-agnóstico de `applyThemeOpts`, reusando `parseStyleWasm`.
func applyMarkdownThemeWasm(theme *markdownTheme, v any) error {
	t, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("theme debe ser una tabla de Styles por elemento")
	}
	set := func(key string, dst **style) error {
		sv, ok := t[key]
		if !ok || sv == nil {
			return nil
		}
		parsed, err := parseStyleWasm(sv)
		if err != nil {
			return fmt.Errorf("%s: %v", key, err)
		}
		*dst = parsed
		return nil
	}
	for i := 0; i < 6; i++ {
		if err := set("h"+strconv.Itoa(i+1), &theme.heading[i]); err != nil {
			return err
		}
	}
	for _, kv := range []struct {
		key string
		dst **style
	}{
		{"code", &theme.code},
		{"emphasis", &theme.emphasis},
		{"strong", &theme.strong},
		{"link", &theme.link},
		{"bullet", &theme.bullet},
		{"blockquote", &theme.blockquote},
		{"rule", &theme.rule},
	} {
		if err := set(kv.key, kv.dst); err != nil {
			return err
		}
	}
	return nil
}

// applyDiffThemeWasm rellena un `diffTheme` con los Styles de opts.theme (del wire):
// claves "add"/"del"/"context"/"header". Espejo VM-agnóstico de `applyDiffTheme`.
func applyDiffThemeWasm(theme *diffTheme, v any) error {
	t, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("theme debe ser una tabla de Styles por tipo de línea")
	}
	for _, kv := range []struct {
		key string
		dst **style
	}{
		{"add", &theme.add},
		{"del", &theme.del},
		{"context", &theme.context},
		{"header", &theme.header},
	} {
		sv, ok := t[kv.key]
		if !ok || sv == nil {
			continue
		}
		parsed, err := parseStyleWasm(sv)
		if err != nil {
			return fmt.Errorf("%s: %v", kv.key, err)
		}
		*kv.dst = parsed
	}
	return nil
}

// optIntWasm lee un entero de un mapa del wire: int64 directo o float64 con valor
// entero. Un float64 con parte fraccionaria (p. ej. 40.5) NO es entero (false), como
// exige `enu.text.markdown` para opts.width.
func optIntWasm(m map[string]any, key string) (int, bool) {
	switch v := m[key].(type) {
	case int64:
		return int(v), true
	case float64:
		i := int(v)
		if float64(i) == v {
			return i, true
		}
	}
	return 0, false
}

// einvalText construye el error estructurado EINVAL de una primitiva de enu.text.
func einvalText(msg string) error {
	return &vmwasm.StructuredError{Code: CodeEINVAL, Message: msg}
}

// argInt lee un entero de args[i] (int64 o float64 según cruce el wire).
func argInt(args []any, i int) int {
	switch v := arg(args, i).(type) {
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
