package runtime

// Tests del compositor de `enu.ui` (§9.1, S29, lógica 🔒). Blindan los casos límite
// que el inventario de [implementacion.md] nombra para S29:
//
//   - **G28 viewport**: `blit` con offsets negativos muestra la ventana correcta
//     del Block; un offset positivo que excede recorta el final.
//   - **G28 copia, nunca re-render**: blittear el MISMO Block con distinto offset no
//     reconstruye el Block (es copia de la ventana).
//   - **G1 resize**: una región fuera de pantalla se recorta sin escribir fuera de
//     la rejilla; sus coordenadas NO cambian; al crecer la pantalla reaparece.
//   - **z-order**: dos regiones solapadas → la de mayor z gana en la zona común.
//   - **coalescing**: varios cambios entre dos pintados producen UN frame; el diff
//     solo emite lo cambiado.
//
// Son tests Go de caja blanca (mismo paquete): inspeccionan la rejilla compuesta y
// los frames directamente, que es como se valida un compositor headless (no hay
// TTY hasta S32; el snippet Lua de `ui_test.go` cubre el lado del autor).

import (
	"strings"
	"testing"
)

// blockOf construye un Block de prueba a partir de líneas-string (un span sin
// estilo por línea), reusando el constructor de producción `newBlock`.
func blockOf(lines ...string) *block {
	ls := make([][]span, len(lines))
	for i, l := range lines {
		ls[i] = []span{{text: l}}
	}
	return newBlock(ls)
}

// gridRow devuelve el texto visible de la fila `y` de una rejilla (graphemes
// concatenados; una celda vacía es un espacio, una continuación de grapheme ancho
// no añade nada). Es la lente de inspección de los tests.
func gridRow(g *grid, y int) string {
	var b strings.Builder
	for x := 0; x < g.w; x++ {
		c := g.cells[y*g.w+x]
		if c.w == 0 {
			continue // continuación de grapheme ancho: ya lo emitió la celda previa
		}
		if c.r == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.r)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// composeRow compone el compositor (apila regiones por z, recorta) y devuelve la
// fila `y` de la pantalla compuesta.
func composeRow(c *compositor, y int) string {
	c.composite()
	return gridRow(c.back, y)
}

// G28 viewport: blit con offset vertical NEGATIVO muestra el Block desde una fila
// posterior (scroll hacia abajo), y un offset positivo que excede recorta el
// final.
func TestBlitViewportVerticalG28(t *testing.T) {
	doc := blockOf("L0", "L1", "L2", "L3", "L4")

	// blit(0, -3, doc): la primera fila visible es la 4ª del Block (índice 3).
	// G28: oy negativo recorta el borde inicial.
	g := newGrid(10, 3)
	g.blitBlock(0, -3, doc)
	for y, want := range []string{"L3", "L4", ""} {
		if got := gridRow(g, y); got != want {
			t.Fatalf("G28 blit(0,-3): fila %d = %q, want %q", y, got, want)
		}
	}

	// blit(0, +1, doc): el Block empieza una fila más abajo; la última fila visible
	// recorta el final del Block (solo caben L0,L1 en una rejilla de 3 con hueco).
	g2 := newGrid(10, 3)
	g2.blitBlock(0, 1, doc)
	for y, want := range []string{"", "L0", "L1"} {
		if got := gridRow(g2, y); got != want {
			t.Fatalf("G28 blit(0,+1): fila %d = %q, want %q", y, got, want)
		}
	}
}

// G28/G37 viewport: blit con offset HORIZONTAL recorta por ambos extremos con el
// MISMO signo que el eje vertical (G37): un offset NEGATIVO recorta el borde inicial
// (scroll), uno POSITIVO desplaza el Block a la derecha (padding/posición). Antes de
// G37 el eje X tenía el signo invertido respecto a Y y al contrato de api.md §9.1.
func TestBlitViewportHorizontalG28(t *testing.T) {
	doc := blockOf("ABCDEFGH")

	// blit(-2, 0): el Block se estampa 2 columnas a la izquierda → se ven "CDEFGH..."
	// (recorta el borde inicial, como el `blit(0,-3)` vertical).
	g := newGrid(4, 1)
	g.blitBlock(-2, 0, doc)
	if got := gridRow(g, 0); got != "CDEF" {
		t.Fatalf("G37 blit(-2,0): fila 0 = %q, want %q", got, "CDEF")
	}

	// blit(+2, 0): el Block se estampa 2 columnas a la derecha; las 2 primeras celdas
	// quedan en blanco y luego "AB" (padding/posición, coherente con `blit(0,+2)`).
	g2 := newGrid(4, 1)
	g2.blitBlock(2, 0, doc)
	if got := gridRow(g2, 0); got != "  AB" {
		t.Fatalf("G37 blit(+2,0): fila 0 = %q, want %q", got, "  AB")
	}
}

// G28 copia, nunca re-render: blittear el MISMO Block con distinto offset no
// reconstruye el Block. Se comprueba por **identidad**: el puntero del Block y su
// `.lines` no cambian tras dos blits con offsets distintos (blit solo lee el
// Block; no llama a `newBlock` ni muta nada del Block).
func TestBlitIsCopyNotRerenderG28(t *testing.T) {
	doc := blockOf("uno", "dos", "tres")
	linesPtrBefore := &doc.lines[0]
	wBefore, hBefore := doc.width, doc.height

	g := newGrid(10, 3)
	g.blitBlock(0, 0, doc)
	g.clear()
	g.blitBlock(0, -1, doc) // mismo Block, otro offset: scroll = re-blit
	g.clear()
	g.blitBlock(0, 1, doc)

	if &doc.lines[0] != linesPtrBefore {
		t.Fatal("G28: blit reasignó las líneas del Block (debería ser copia, no re-render)")
	}
	if doc.width != wBefore || doc.height != hBefore {
		t.Fatalf("G28: blit alteró las dimensiones del Block (%d×%d → %d×%d)",
			wBefore, hBefore, doc.width, doc.height)
	}
	// Y el contenido del Block sigue intacto tras los re-blits.
	if got := strings.Join([]string{doc.lines[0][0].text, doc.lines[1][0].text, doc.lines[2][0].text}, ","); got != "uno,dos,tres" {
		t.Fatalf("G28: blit mutó el contenido del Block: %q", got)
	}
}

// G1 resize: una región parcial o totalmente fuera de pantalla se recorta al
// componer (nunca escribe fuera de la rejilla); sus coordenadas NO cambian; al
// crecer la pantalla reaparece tal cual.
func TestRegionResizeClipG1(t *testing.T) {
	// Pantalla pequeña 3×2. Una región 4×2 en (2,0): sus dos primeras columnas caen
	// en pantalla (x=2), el resto (x=3,4,5) cae fuera y se recorta.
	c := newCompositor(3, 2)
	r := c.addRegion(2, 0, 4, 2, 0, "user")
	r.content.blitBlock(0, 0, blockOf("WXYZ", "abcd"))

	// Compuesto: solo cabe la columna x=2 ('W' / 'a'). Nada se escribió fuera (si se
	// hubiera escrito fuera, `composite`/`at` habría entrado en pánico por índice).
	if got := composeRow(c, 0); got != "  W" {
		t.Fatalf("G1 recorte parcial: fila 0 = %q, want %q", got, "  W")
	}
	if got := composeRow(c, 1); got != "  a" {
		t.Fatalf("G1 recorte parcial: fila 1 = %q, want %q", got, "  a")
	}

	// La región totalmente fuera (x muy a la derecha): no pinta nada, sin error.
	r2 := c.addRegion(10, 0, 2, 2, 5, "user")
	r2.content.blitBlock(0, 0, blockOf("ZZ", "ZZ"))
	if got := composeRow(c, 0); got != "  W" {
		t.Fatalf("G1 región totalmente fuera: contaminó la pantalla: fila 0 = %q", got)
	}

	// Las coordenadas de la región NO se tocaron por estar fuera de pantalla.
	if r.x != 2 || r.y != 0 || r2.x != 10 || r2.y != 0 {
		t.Fatalf("G1: el recorte alteró las coordenadas de las regiones (r=%d,%d r2=%d,%d)", r.x, r.y, r2.x, r2.y)
	}

	// Al CRECER la pantalla, la región reaparece tal cual (sus coords/lienzo nunca
	// cambiaron): ahora caben las 4 columnas de r en (2..5) y r2 en (10,11).
	c.resize(20, 2)
	if got := composeRow(c, 0); got != "  WXYZ    ZZ" {
		t.Fatalf("G1 reaparición al crecer: fila 0 = %q, want %q", got, "  WXYZ    ZZ")
	}
	if got := composeRow(c, 1); got != "  abcd    ZZ" {
		t.Fatalf("G1 reaparición al crecer: fila 1 = %q, want %q", got, "  abcd    ZZ")
	}
}

// G1: una región con y negativa (empieza por encima de la pantalla) se recorta por
// arriba sin escribir en índices negativos.
func TestRegionNegativeOriginClipG1(t *testing.T) {
	c := newCompositor(4, 2)
	r := c.addRegion(-1, -1, 3, 3, 0, "user")
	r.content.blitBlock(0, 0, blockOf("123", "456", "789"))
	// Solo la porción (x>=0, y>=0) cae en pantalla: el bloque empieza en (-1,-1), así
	// que la pantalla ve a partir de la fila 1 / columna 1 del lienzo.
	if got := composeRow(c, 0); got != "56" {
		t.Fatalf("G1 origen negativo: fila 0 = %q, want %q", got, "56")
	}
	if got := composeRow(c, 1); got != "89" {
		t.Fatalf("G1 origen negativo: fila 1 = %q, want %q", got, "89")
	}
	if r.x != -1 || r.y != -1 {
		t.Fatalf("G1 origen negativo: coordenadas alteradas (%d,%d)", r.x, r.y)
	}
}

// z-order: dos regiones solapadas → la de mayor z gana en la zona común; con z
// iguales gana la creada después (orden de llegada estable).
func TestRegionZOrder(t *testing.T) {
	c := newCompositor(6, 1)
	lo := c.addRegion(0, 0, 6, 1, 1, "user") // z=1, fondo
	hi := c.addRegion(2, 0, 2, 1, 5, "user") // z=5, encima en cols 2..3
	lo.content.blitBlock(0, 0, blockOf("aaaaaa"))
	hi.content.blitBlock(0, 0, blockOf("BB"))
	if got := composeRow(c, 0); got != "aaBBaa" {
		t.Fatalf("z-order: fila 0 = %q, want %q", got, "aaBBaa")
	}

	// z iguales: la creada después gana en la zona común.
	c2 := newCompositor(4, 1)
	first := c2.addRegion(0, 0, 4, 1, 0, "user")
	second := c2.addRegion(1, 0, 2, 1, 0, "user")
	first.content.blitBlock(0, 0, blockOf("...."))
	second.content.blitBlock(0, 0, blockOf("##"))
	if got := composeRow(c2, 0); got != ".##." {
		t.Fatalf("z-order empate: fila 0 = %q, want %q (la creada después gana)", got, ".##.")
	}
}

// coalescing: varios cambios entre dos pintados producen UN frame (no N); el diff
// solo emite lo cambiado. Se modela con el flag `dirty` (que las mutaciones
// marcan) y un único `paint()` que lo consume.
func TestCoalescingSingleFrame(t *testing.T) {
	c := newCompositor(10, 1)
	r := c.addRegion(0, 0, 10, 1, 0, "user")

	// Tres mutaciones seguidas (lo que el timer de ~30 ms vería entre dos ticks).
	r.content.blitBlock(0, 0, blockOf("aaa"))
	c.markDirty()
	r.content.blitBlock(0, 0, blockOf("bbb"))
	c.markDirty()
	r.content.blitBlock(0, 0, blockOf("ccc"))
	c.markDirty()

	if !c.dirty {
		t.Fatal("coalescing: el compositor debería estar sucio tras las mutaciones")
	}
	// UN solo pintado: las tres mutaciones colapsan en un frame con el estado final.
	changed := c.paint()
	if c.frames != 1 {
		t.Fatalf("coalescing: %d frames pintados, want 1 (N cambios → 1 frame)", c.frames)
	}
	if c.dirty {
		t.Fatal("coalescing: paint debería limpiar el flag dirty")
	}
	if changed != 3 { // solo "ccc" (3 celdas) difiere del front en blanco
		t.Fatalf("coalescing: %d celdas cambiadas, want 3 (solo el estado final)", changed)
	}
	if got := gridRow(c.back, 0); got != "ccc" {
		t.Fatalf("coalescing: el frame muestra %q, want %q (último estado)", got, "ccc")
	}

	// Sin cambios entre dos pintados: el segundo paint no emite nada (diff vacío,
	// frame coalescido a vacío).
	changed2 := c.paint()
	if changed2 != 0 || c.encoded() != "" {
		t.Fatalf("coalescing: paint sin cambios emitió %d celdas / %q bytes, want 0/\"\"", changed2, c.encoded())
	}
	if c.skipped != 1 {
		t.Fatalf("coalescing: %d frames saltados, want 1", c.skipped)
	}
}

// diff: tras pintar un estado, un cambio puntual solo reemite las celdas que
// difieren (damage tracking), no la pantalla entera.
func TestDiffEmitsOnlyChanged(t *testing.T) {
	c := newCompositor(10, 1)
	r := c.addRegion(0, 0, 10, 1, 0, "user")
	r.content.blitBlock(0, 0, blockOf("hola"))
	c.markDirty()
	c.paint() // primer frame: pinta "hola"

	// Cambia solo una letra: "hola" → "hala". El diff debe tocar 1 celda.
	r.content.fill(nil)
	r.content.blitBlock(0, 0, blockOf("hala"))
	c.markDirty()
	changed := c.paint()
	if changed != 1 {
		t.Fatalf("diff: %d celdas cambiadas, want 1 (solo la letra distinta)", changed)
	}
	enc := c.encoded()
	if !strings.Contains(enc, "a") || strings.Contains(enc, "hola") {
		t.Fatalf("diff: el frame reemite de más: %q", enc)
	}
}
