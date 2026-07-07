package runtime

// Enchufe del compositor REAL a la frontera wasm (migracion-vm.md M13c, §9). M11
// definió la interfaz `vmwasm.UIBackend` y probó el MECANISMO con un backend de
// grabación; aquí se cablea el compositor de producción (compositor.go, que el
// censo M01 marcó VM-agnóstico) a esa interfaz, sin reimplementar nada: el
// adaptador traduce cada método de la interfaz a la operación real del `*compositor`
// / `*uiRegion` y reusa el parseo de Blocks (block.go) y de colores (ui.go,
// `isHexColor`).
//
// POR QUÉ UN ADAPTADOR Y NO IMPLEMENTAR LA INTERFAZ EN `*uiRegion` DIRECTAMENTE. El
// `*uiRegion` de gopher tiene nombres y firmas propias (`move`/`resizeRegion`/
// `release`, `content.blitBlock` toma un `*block`, `setCursor` toma un `off bool`),
// que NO coinciden con `vmwasm.RegionObj` (`Move`/`Resize`/`Destroy`, `Blit` toma un
// `vmwasm.BlockObj`, `Cursor` toma un `show bool`). En vez de contaminar el tipo de
// gopher con métodos exportados de otra forma —lo que arriesgaría su suite—, un fino
// adaptador por región traduce cada método. El único toque al gopher es que `*block`
// gane `Dims()` (block.go) para satisfacer `vmwasm.BlockObj` —una adición pura, no
// cambia el comportamiento—.
//
// EL BLOCK COMO BlockObj. `NewBlock` devuelve el `*block` real (block.go) como
// `vmwasm.BlockObj`; el adaptador de región recupera el `*block` con `b.(*block)`
// para `blitBlock`. Así el handle de un Block (asignado por `nu.ui._block` o por las
// primitivas de `nu.text`) resuelve a su `*block` en Go y el blit es la copia de
// ventana de siempre (G28), sin re-render.
//
// GATING HEADLESS (G20). `registerUIWasm` sólo instala el backend si el Runtime tiene
// UI (`rt.ui != nil`); en headless (`nu -e`, CI, salida redirigida) no se registra y
// `nu.ui` no existe —el mismo gating que el backend gopher (ui.go, S32)—.

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dbareagimeno/nu/internal/vmwasm"
)

// registerUIWasm instala el backend de UI del Pool wasm apuntando al compositor real
// del Runtime (§9). SÓLO si hay UI concedida (`rt.ui != nil`, gating G20): en headless
// no se registra y `nu.ui` no existe. Debe llamarse antes de `NewInstance` (el preludio
// se arma con el catálogo completo, y la presencia de UI decide si `nu.ui` existe).
func registerUIWasm(p *vmwasm.Pool, rt *Runtime) {
	if rt.ui == nil {
		return // headless (G20): sin compositor no hay nu.ui
	}
	p.SetUIBackend(newCompositorBackend(rt.ui.comp, rt.ui.clipWriter, rt.ui.clipReader))

	// nu.ui._check_style(style): valida un Style literal (§9.2, G22) SIN aplicarlo,
	// devolviendo EINVAL si el color no es del core (un nombre semántico, un hex mal
	// formado, un índice fuera de rango). Lo consulta el envoltorio Lua de
	// `nu.ui.region` antes de `Region:fill`, porque `RegionObj.Fill` (M11) es void y no
	// tiene canal de error: gopher (regionFill) lanza EINVAL ante un estilo inválido, y
	// esta primitiva restaura esa paridad sin cambiar la interfaz del binding. Un
	// `style` nil (fill sin estilo, equivalente a clear) es válido.
	p.Register("ui._check_style", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		if len(args) == 0 || args[0] == nil {
			return nil, nil
		}
		styleMap, ok := args[0].(map[string]any)
		if !ok {
			return nil, &vmwasm.StructuredError{Code: string(CodeEINVAL), Message: "Region:fill: `style` debe ser una tabla"}
		}
		if _, err := parseStyleWasm(styleMap); err != nil {
			return nil, &vmwasm.StructuredError{Code: string(CodeEINVAL), Message: "Region:fill: " + err.Error()}
		}
		return nil, nil
	})
}

// compositorBackend adapta el `*compositor` real a `vmwasm.UIBackend`. Envuelve el
// compositor (el de `rt.ui.comp`) y, para el portapapeles OSC 52 (§9.2), los mismos
// destino/fuente que el backend gopher usa (`clipWriter`/`clipReader` de `uiState`);
// en un test desnudo son nil y el portapapeles es un no-op / `nil` (como headless).
type compositorBackend struct {
	comp       *compositor
	clipWriter io.Writer // OSC 52 destino (os.Stdout en prod; nil en tests desnudos)
	clipReader io.Reader // OSC 52 respuesta (driver TTY, S33+; nil headless)
}

// newCompositorBackend construye el adaptador sobre un compositor y los io del
// portapapeles. Lo llama `registerUIWasm` con las piezas de `rt.ui`; los tests lo
// construyen con un `newCompositor(w,h)` desnudo y nil en el portapapeles.
func newCompositorBackend(comp *compositor, clipWriter io.Writer, clipReader io.Reader) *compositorBackend {
	return &compositorBackend{comp: comp, clipWriter: clipWriter, clipReader: clipReader}
}

// Size devuelve el tamaño de la pantalla en celdas (nu.ui.size), directo del
// compositor.
func (b *compositorBackend) Size() (int, int) { return b.comp.w, b.comp.h }

// Caps devuelve las capacidades del terminal (nu.ui.caps, §9.2). Mismo criterio que
// `uiCaps`/`detectColors` del backend gopher: colores por entorno, el resto en el
// default conservador (false) hasta que la Fase 6 negocie protocolos. Los valores
// cruzan el wire, así que `colors` va como int64.
func (b *compositorBackend) Caps() map[string]any {
	return map[string]any{
		"colors":         int64(detectColors()),
		"kitty_keyboard": false,
		"mouse":          false,
		"images":         false,
	}
}

// NewBlock construye un Block (nu.ui.block) a partir de las líneas del wire: una
// línea es un string (span único sin estilo) o un array de spans `{text, style?}`.
// Reusa el parser VM-agnóstico `parseLinesWasm` (equivalente a `parseLine`/
// `parseStyle` de gopher, pero desde `[]any`) y devuelve el `*block` real como
// `vmwasm.BlockObj`. Un error es de validación (líneas mal formadas → EINVAL, que la
// primitiva `ui._block` envuelve).
func (b *compositorBackend) NewBlock(lines []any) (vmwasm.BlockObj, error) {
	parsed, err := parseLinesWasm(lines)
	if err != nil {
		return nil, err
	}
	return newBlock(parsed), nil
}

// NewRegion crea una región de composición (nu.ui.region) sobre el compositor real y
// la envuelve en un adaptador que traduce sus métodos. El dueño (para el reload de
// gopher, G2) no aplica en wasm —el ciclo de vida lo lleva la tabla de handles de la
// Instance (M10)—, así que se etiqueta con "" (vacío).
func (b *compositorBackend) NewRegion(x, y, w, h, z int) vmwasm.RegionObj {
	return &regionAdapter{r: b.comp.addRegion(x, y, w, h, z, "")}
}

// ClipboardSet copia `s` al portapapeles del sistema por OSC 52 (§9.2), escribiendo
// la secuencia al terminal. Best-effort: sin `clipWriter` (test desnudo) es un no-op.
func (b *compositorBackend) ClipboardSet(s string) {
	if b.clipWriter != nil {
		_, _ = io.WriteString(b.clipWriter, encodeOSC52Set(s))
	}
}

// ClipboardGet pide el portapapeles por OSC 52 y espera la respuesta del terminal
// (§9.2). Corre en la goroutine de fondo del ⏸ `ui.clipboard_get` (contrato de
// RegisterSuspending): la escritura de la consulta y la lectura bloqueante NO tocan la
// VM. Sin driver de TTY (`clipReader` nil, headless) devuelve `("", false)` de
// inmediato, que la primitiva traduce a `nil`.
func (b *compositorBackend) ClipboardGet() (string, bool) {
	if b.clipWriter != nil {
		_, _ = io.WriteString(b.clipWriter, encodeOSC52Query())
	}
	return readOSC52Reply(b.clipReader, clipboardReadTimeout)
}

// regionAdapter traduce cada método de `vmwasm.RegionObj` (§9.1) a la operación real
// del `*uiRegion` de gopher. No lleva estado propio: es un puntero al `*uiRegion`
// dueño de su lienzo en el compositor.
type regionAdapter struct {
	r *uiRegion
}

// Blit estampa un Block en coordenadas locales (§9.1, G28: copia de ventana, nunca
// re-render). Recupera el `*block` real del `BlockObj` (que `NewBlock` devolvió como
// tal) y delega en `content.blitBlock`; el recorte por ambos extremos y el z-order son
// del compositor. Un `BlockObj` que no sea un `*block` (imposible por construcción) se
// ignora defensivamente.
func (ra *regionAdapter) Blit(x, y int, b vmwasm.BlockObj) {
	blk, ok := b.(*block)
	if !ok {
		return
	}
	ra.r.content.blitBlock(x, y, blk)
	ra.r.comp.markDirty()
}

// Fill rellena el lienzo de la región con un estilo (§9.1). El estilo llega como el
// mapa crudo del wire; se parsea con `parseStyleWasm`. Un estilo mal formado se trata
// como sin estilo (nil): la interfaz `RegionObj.Fill` (M11) no tiene canal de error,
// a diferencia del `Region:fill` de gopher que lanza EINVAL —una limitación del
// binding M11, no de esta sesión; la validación fina del estilo la hace el autor—.
func (ra *regionAdapter) Fill(styleMap map[string]any) {
	var st *style
	if styleMap != nil {
		if parsed, err := parseStyleWasm(styleMap); err == nil {
			st = parsed
		}
	}
	ra.r.content.fill(st)
	ra.r.comp.markDirty()
}

// Clear limpia el lienzo (todas las celdas a fondo, sin estilo): es `fill(nil)` (§9.1).
func (ra *regionAdapter) Clear() {
	ra.r.content.fill(nil)
	ra.r.comp.markDirty()
}

// Move recoloca la región a coordenadas de pantalla (§9.1, S30). No toca el lienzo.
func (ra *regionAdapter) Move(x, y int) { ra.r.move(x, y) }

// Resize cambia el tamaño lógico de la región conservando el contenido donde quepa
// (§9.1, S30). Un tamaño negativo se degrada a 0 (newGrid lo clampa): la interfaz
// `RegionObj.Resize` no lanza (limitación del binding M11, como Fill).
func (ra *regionAdapter) Resize(w, h int) { ra.r.resizeRegion(w, h) }

// Raise sube la región al frente del z-order (§9.1, S30).
func (ra *regionAdapter) Raise() { ra.r.raise() }

// Lower baja la región al fondo del z-order (§9.1, S30).
func (ra *regionAdapter) Lower() { ra.r.lower() }

// Show vuelve a componer una región oculta por Hide (§9.1, S30). Idempotente.
func (ra *regionAdapter) Show() { ra.r.show() }

// Hide oculta la región conservando lienzo y coordenadas (§9.1, S30). Idempotente.
func (ra *regionAdapter) Hide() { ra.r.hide() }

// Destroy destruye la región en el compositor (§9.1, S30): la descuelga y suelta el
// cursor si era suyo. La liberación del HANDLE la hace la primitiva `Region:destroy`
// (vmwasm/ui.go: `inst.FreeHandle(inst.dispatchHandle)`); aquí sólo el recurso del
// compositor. Idempotente (`release`).
func (ra *regionAdapter) Destroy() { ra.r.release() }

// Cursor coloca u oculta el cursor real del terminal en coordenadas LOCALES (§9.1,
// S30). El compositor toma `off bool` (ocultar), que es el inverso de `show`.
func (ra *regionAdapter) Cursor(x, y int, show bool) {
	ra.r.comp.setCursor(ra.r, x, y, !show)
}

// ─────────────────────────────────────────────────────────────────────────────
// Parseo VM-agnóstico de líneas y estilos (equivalente a parseLine/parseStyle/
// normalizeColor de ui.go, pero desde `[]any`/`map[string]any` del wire). Lo reusan
// tanto NewBlock/Fill (aquí) como los themes de nu.text (vmwasm_text.go).
// ─────────────────────────────────────────────────────────────────────────────

// parseLinesWasm convierte las líneas de un Block (del wire) a `[][]span`. Cada línea
// es un string o un array de spans; un error compone el número de línea (1-based).
func parseLinesWasm(lines []any) ([][]span, error) {
	out := make([][]span, 0, len(lines))
	for i, v := range lines {
		spans, err := parseLineWasm(v)
		if err != nil {
			return nil, fmt.Errorf("línea %d: %v", i+1, err)
		}
		out = append(out, spans)
	}
	return out, nil
}

// parseLineWasm convierte una línea a una rebanada de spans (§9.2). Un string es un
// span único sin estilo; un array es una lista de spans `{text, style?}`.
func parseLineWasm(v any) ([]span, error) {
	switch line := v.(type) {
	case string:
		return []span{{text: line}}, nil
	case []any:
		spans := make([]span, 0, len(line))
		for i, sv := range line {
			st, ok := sv.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("el span %d debe ser una tabla {text, style?}", i+1)
			}
			text, ok := st["text"].(string)
			if !ok {
				return nil, fmt.Errorf("el span %d necesita un campo `text` de tipo string", i+1)
			}
			sp := span{text: text}
			if styleVal, ok := st["style"]; ok && styleVal != nil {
				parsed, err := parseStyleWasm(styleVal)
				if err != nil {
					return nil, fmt.Errorf("el span %d: %v", i+1, err)
				}
				sp.st = parsed
			}
			spans = append(spans, sp)
		}
		return spans, nil
	default:
		return nil, fmt.Errorf("cada línea debe ser un string o un array de spans")
	}
}

// parseStyleWasm convierte un `Style` del wire (`map[string]any`) a un `*style` Go,
// validando los colores literales (§9.2, G22). Espejo VM-agnóstico de `parseStyle`.
func parseStyleWasm(v any) (*style, error) {
	t, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("`style` debe ser una tabla")
	}
	s := &style{}
	if fg, ok := t["fg"]; ok && fg != nil {
		norm, err := normalizeColorWasm(fg)
		if err != nil {
			return nil, fmt.Errorf("style.fg: %v", err)
		}
		s.fg, s.fgSet = norm, true
	}
	if bg, ok := t["bg"]; ok && bg != nil {
		norm, err := normalizeColorWasm(bg)
		if err != nil {
			return nil, fmt.Errorf("style.bg: %v", err)
		}
		s.bg, s.bgSet = norm, true
	}
	s.bold = wasmTruthy(t["bold"])
	s.italic = wasmTruthy(t["italic"])
	s.underline = wasmTruthy(t["underline"])
	s.reverse = wasmTruthy(t["reverse"])
	return s, nil
}

// normalizeColorWasm valida y normaliza un color literal (§9.2, G22): un "#rrggbb"
// (a minúsculas) o un índice 0-255 (número o string numérica). Cualquier otra cosa
// —un nombre semántico, un hex mal formado, un índice fuera de rango— es error (los
// nombres los resuelve el theme del toolkit, no el core). Espejo de `normalizeColor`.
func normalizeColorWasm(v any) (string, error) {
	switch c := v.(type) {
	case int64:
		if c < 0 || c > 255 {
			return "", fmt.Errorf("índice de color debe ser 0-255, no %d", c)
		}
		return strconv.FormatInt(c, 10), nil
	case float64:
		i := int(c)
		if float64(i) != c || i < 0 || i > 255 {
			return "", fmt.Errorf("índice de color debe ser un entero 0-255, no %v", c)
		}
		return strconv.Itoa(i), nil
	case string:
		if strings.HasPrefix(c, "#") {
			if !isHexColor(c) {
				return "", fmt.Errorf("color hex debe ser \"#rrggbb\" (6 dígitos hex), no %q", c)
			}
			return strings.ToLower(c), nil
		}
		if i, err := strconv.Atoi(c); err == nil {
			if i < 0 || i > 255 {
				return "", fmt.Errorf("índice de color debe ser 0-255, no %d", i)
			}
			return strconv.Itoa(i), nil
		}
		return "", fmt.Errorf("color debe ser \"#rrggbb\" o un índice 0-255, no %q (los nombres semánticos los resuelve el theme, G22)", c)
	default:
		return "", fmt.Errorf("color debe ser un string \"#rrggbb\" o un índice 0-255")
	}
}

// wasmTruthy replica la verdad de Lua para un valor del wire: nil y false son falsos,
// todo lo demás verdadero. Lo usan los atributos booleanos de `parseStyleWasm` y el
// `render` de `nu.text.diff` (un `bold = 1` o `render = {}` cuentan como true, como en
// `lua.LVAsBool`).
func wasmTruthy(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}
