package runtime

import (
	"github.com/rivo/uniseg"
	lua "github.com/yuin/gopher-lua"
)

// El tipo `Block` y los estilos (api.md §9.2, sesión S22). Un **Block** es un
// handle **opaco** (userdata) de líneas estilizadas con `.width` y `.height`
// legibles desde Lua. Es la moneda común del render: lo producen `nu.text.wrap`
// (S22), `nu.text.markdown` (S23), `nu.text.highlight` (S24) y
// `nu.text.diff` (S25), lo construye a mano `nu.ui.block` (S22), y lo consume
// `Region:blit` como un viewport (S29). Por eso su estructura interna se fija
// aquí, fundacional para toda la Fase 5 y la 6.
//
// LA ESTRUCTURA INTERNA (lo que reusan S23–S29). Un `block` es una rebanada de
// líneas, y cada línea una rebanada de `span`s (`{text, style}`). El texto del
// span es UTF-8 crudo; el estilo es opcional (nil = sin estilo, hereda lo que
// haya debajo al pintar). `.width` es el **máximo ancho de línea en celdas de
// terminal** (calculado con `text.width`, que respeta graphemes/east-asian/ZWJ)
// y `.height` es el número de líneas. Ambos se calculan **una vez al construir**
// (los Blocks son inmutables tras crearse: wrap/markdown/etc. devuelven uno
// nuevo, no mutan) y se cachean, porque el compositor (S29) consultará `.width`/
// `.height` en cada blit y recalcularlos sería el coste cuadrático que ADR-007
// quiere evitar.
//
// POR QUÉ UNA REBANADA DE SPANS Y NO UNA REJILLA DE CELDAS. Un Block guarda el
// texto lógico por tramos de estilo, no una matriz de celdas ya resuelta. La
// rejilla es del compositor (S29), que recorta el viewport y resuelve el
// degradado de color con `nu.ui.caps().colors`. Guardar spans (a) deja a S25
// (diff) y S23 (markdown) construir líneas concatenando tramos sin pensar en
// celdas, y (b) mantiene el Block como descripción, no como pintura —blit es
// copia, "nunca re-render" (§9.1)—.

// blockTypeName identifica la metatabla del handle `Block`. De ella cuelga el
// `__index` que resuelve los campos legibles `.width`/`.height`; el contenido
// (las líneas de spans) es interno y NO se expone a Lua como tabla mutable —el
// Block es opaco (§9.2)—, solo se inspecciona en Go (tests, S29 blit).
const blockTypeName = "nu.ui.Block"

// style es el estilo de un span (api.md §9.2). Todos los campos son opcionales:
// `fg`/`bg` son colores **literales** (un "#rrggbb" o un índice 0-255), nunca
// nombres semánticos —esos son vocabulario del theme del toolkit, que los
// resuelve a literales antes de construir el Block (G22)—. Los booleanos son
// atributos. Un puntero a `style` nil en un span significa "sin estilo".
//
// El color se guarda **normalizado** como string: un literal "#rrggbb" tal cual
// (validado) o el índice decimal como string ("42"). Guardarlo como string (no
// como un tipo de color resuelto) preserva la intención literal hasta que el
// compositor (S29) la degrade a lo que el terminal soporte —el render decide,
// no el Block (§9.2)—.
type style struct {
	fg        string // "" = sin color de primer plano
	bg        string // "" = sin color de fondo
	fgSet     bool
	bgSet     bool
	bold      bool
	italic    bool
	underline bool
	reverse   bool
}

// span es un tramo de texto con un estilo común (api.md §9.2: `{text, style?}`).
// `text` es UTF-8 crudo; `st` es nil si el span no lleva estilo (hereda lo que
// haya debajo al pintar). Es la unidad mínima de una línea de un Block.
type span struct {
	text string
	st   *style // nil = sin estilo
}

// block es el contenido Go de un Block: las líneas (cada una una rebanada de
// spans) y las dimensiones cacheadas en celdas. Inmutable tras construirse —cada
// primitiva que "modifica" un Block devuelve uno nuevo—. `width`/`height` se
// calculan en `newBlock` y no se recomputan (los consulta el compositor en cada
// blit, S29).
type block struct {
	lines  [][]span
	width  int // máximo ancho de línea en celdas (text.width)
	height int // número de líneas
}

// Dims devuelve las dimensiones cacheadas del Block en celdas (ancho, alto). Es lo
// que exige `vmwasm.BlockObj` (M13c): el binding wasm expone el Block como handle
// opaco cuyo objeto Go es este `*block`, y `Region:blit` lo resuelve para copiar su
// ventana. Adición pura para el backend wasm; no toca el camino de gopher.
func (b *block) Dims() (int, int) { return b.width, b.height }

// lineWidth calcula el ancho en celdas de una línea (la suma de los anchos de sus
// spans). Reusa la lógica de `text.width` (uniseg) span a span; concatenar los
// textos y medir una vez daría el mismo resultado salvo en el borde patológico de
// un grapheme partido entre dos spans, que no se da porque cada span es texto
// completo —medir por span es más barato y robusto—.
func lineWidth(spans []span) int {
	w := 0
	for _, sp := range spans {
		w += uniseg.StringWidth(sp.text)
	}
	return w
}

// newBlock construye un `block` a partir de sus líneas y precalcula `width`
// (máximo ancho de línea en celdas) y `height` (número de líneas). Es el único
// constructor: todo Block (wrap, markdown, diff, ui.block) pasa por aquí, así que
// las dimensiones siempre son coherentes con el contenido.
func newBlock(lines [][]span) *block {
	maxW := 0
	for _, ln := range lines {
		if w := lineWidth(ln); w > maxW {
			maxW = w
		}
	}
	return &block{lines: lines, width: maxW, height: len(lines)}
}

// pushBlock envuelve un `*block` en un userdata con la metatabla de `Block` y lo
// empuja a la pila. Es el puente Go→Lua del tipo: lo usan `nu.ui.block`,
// `nu.text.wrap` y (en adelante) toda primitiva que produzca un Block.
func (rt *Runtime) pushBlock(L *lua.LState, b *block) {
	ud := L.NewUserData()
	ud.Value = b
	L.SetMetatable(ud, L.GetTypeMetatable(blockTypeName))
	L.Push(ud)
}

// checkBlock recupera el `*block` del userdata del argumento `idx`. Lanza
// `EINVAL` si no es un handle de Block. Lo usarán las primitivas que consumen un
// Block (S29 `Region:blit`); aquí lo usa el `__index` y los tests.
func checkBlock(L *lua.LState, idx int) *block {
	ud := L.CheckUserData(idx)
	b, ok := ud.Value.(*block)
	if !ok {
		raiseError(L, CodeEINVAL, "Block: se esperaba un handle de Block", lua.LNil)
		return nil
	}
	return b
}

// registerBlockType instala la metatabla del tipo `Block` con un `__index` que
// resuelve los campos legibles `.width` y `.height` (§9.2). El Block es opaco: no
// se expone el contenido (las líneas de spans) a Lua, solo sus dimensiones. Un
// acceso a cualquier otra clave cae a nil (como una tabla normal). Lo llama
// `registerUI` (ui.go).
func (rt *Runtime) registerBlockType() {
	L := rt.L
	mt := L.NewTypeMetatable(blockTypeName)
	L.SetField(mt, "__index", L.NewFunction(func(L *lua.LState) int {
		b := checkBlock(L, 1)
		if b == nil {
			return 0
		}
		switch L.CheckString(2) {
		case "width":
			L.Push(lua.LNumber(b.width))
		case "height":
			L.Push(lua.LNumber(b.height))
		default:
			L.Push(lua.LNil)
		}
		return 1
	}))
}
