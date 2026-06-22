package runtime

// Tests del ciclo de vida de `Region` (§9.1, S30). Blindan la lógica propia que el
// "Criterio de hecho" de la sesión nombra —"z-order respeta raise/lower; solo la
// última cursor() gana"— y los casos límite de move/resize/show/hide/destroy:
//
//   - **z-order raise/lower**: tres regiones solapadas; `raise()` de la del fondo la
//     lleva al frente (gana en el solape); `lower()` la manda atrás.
//   - **move/resize**: tras move la región se pinta en el nuevo sitio (recortada si
//     se sale, G1); tras resize el nuevo tamaño se respeta y el contenido que cabe se
//     conserva.
//   - **show/hide**: hide → no aparece en el composite; show → reaparece tal cual.
//   - **destroy**: desaparece del composite; idempotente; suelta el cursor si era
//     suyo.
//   - **cursor — la última gana**: A reclama el cursor, luego B; el frame lo coloca
//     según B; `cursor(nil)` lo oculta; `hide`/`destroy` de la dueña lo sueltan.
//
// Caja blanca (mismo paquete): inspeccionan la rejilla compuesta y el buffer ANSI,
// como `compositor_test.go` (S29). El lado Lua lo cubre el snippet de `ui_test.go`.

import (
	"strings"
	"testing"
)

// z-order raise/lower: tres regiones solapadas en la misma columna. De arranque, la
// creada después gana (z iguales, seq); `raise()` de la del fondo la lleva al frente,
// `lower()` la devuelve atrás. Se verifica con la celda compuesta de la zona común.
func TestRegionRaiseLowerZOrder(t *testing.T) {
	c := newCompositor(3, 1)
	a := c.addRegion(0, 0, 3, 1, 0, "user")
	b := c.addRegion(0, 0, 3, 1, 0, "user")
	d := c.addRegion(0, 0, 3, 1, 0, "user")
	a.content.blitBlock(0, 0, blockOf("AAA"))
	b.content.blitBlock(0, 0, blockOf("BBB"))
	d.content.blitBlock(0, 0, blockOf("DDD"))

	// z iguales: la última creada (d) gana en toda la zona común.
	if got := composeRow(c, 0); got != "DDD" {
		t.Fatalf("arranque: fila 0 = %q, want %q (la creada después gana)", got, "DDD")
	}

	// raise() de la del fondo (a) la lleva al frente: ahora gana a.
	a.raise()
	if got := composeRow(c, 0); got != "AAA" {
		t.Fatalf("raise(a): fila 0 = %q, want %q (a al frente)", got, "AAA")
	}

	// lower() de a la manda al fondo: vuelve a ganar d (que sigue por encima de b).
	a.lower()
	if got := composeRow(c, 0); got != "DDD" {
		t.Fatalf("lower(a): fila 0 = %q, want %q (a al fondo, gana d)", got, "DDD")
	}

	// raise() de b la pone por encima de d: ahora gana b. Comprueba que raise respeta
	// el orden relativo del resto (a sigue al fondo, d sigue por debajo de b).
	b.raise()
	if got := composeRow(c, 0); got != "BBB" {
		t.Fatalf("raise(b): fila 0 = %q, want %q (b al frente)", got, "BBB")
	}
}

// move: tras mover una región, el siguiente composite la pinta en el nuevo sitio; lo
// que cae fuera se recorta (G1) y sus coordenadas son las nuevas.
func TestRegionMove(t *testing.T) {
	c := newCompositor(6, 1)
	r := c.addRegion(0, 0, 2, 1, 0, "user")
	r.content.blitBlock(0, 0, blockOf("XY"))
	if got := composeRow(c, 0); got != "XY" {
		t.Fatalf("antes de move: fila 0 = %q, want %q", got, "XY")
	}

	r.move(3, 0)
	if r.x != 3 || r.y != 0 {
		t.Fatalf("move: coords = (%d,%d), want (3,0)", r.x, r.y)
	}
	if got := composeRow(c, 0); got != "   XY" {
		t.Fatalf("tras move: fila 0 = %q, want %q", got, "   XY")
	}

	// Mover fuera de pantalla por la derecha: se recorta (G1), nada se pinta, sin
	// error, y las coordenadas quedan en el nuevo sitio.
	r.move(10, 0)
	if got := composeRow(c, 0); got != "" {
		t.Fatalf("move fuera: fila 0 = %q, want \"\" (recortada G1)", got)
	}
	// Mover parcialmente fuera por la izquierda (x=-1): solo la columna x=0 (la 'Y')
	// entra.
	r.move(-1, 0)
	if got := composeRow(c, 0); got != "Y" {
		t.Fatalf("move parcial izq: fila 0 = %q, want %q (G1)", got, "Y")
	}
}

// resize: tras redimensionar, el nuevo tamaño del lienzo se respeta y el contenido
// que cabe se conserva (esquina superior izquierda); al crecer, lo nuevo es fondo.
func TestRegionResize(t *testing.T) {
	c := newCompositor(8, 2)
	r := c.addRegion(0, 0, 4, 2, 0, "user")
	r.content.blitBlock(0, 0, blockOf("ABCD", "EFGH"))

	// Encoger a 2×1: solo se conserva "AB" (esquina sup-izq); el resto se descarta.
	r.resizeRegion(2, 1)
	if r.content.w != 2 || r.content.h != 1 {
		t.Fatalf("resize: lienzo = %d×%d, want 2×1", r.content.w, r.content.h)
	}
	if got := composeRow(c, 0); got != "AB" {
		t.Fatalf("resize encoger: fila 0 = %q, want %q (contenido conservado)", got, "AB")
	}
	if got := composeRow(c, 1); got != "" {
		t.Fatalf("resize encoger: fila 1 = %q, want \"\" (la fila ya no existe)", got)
	}

	// Crecer a 4×2: lo conservado ("AB") sigue ahí; lo nuevo es fondo (espacios).
	r.resizeRegion(4, 2)
	if got := composeRow(c, 0); got != "AB" {
		t.Fatalf("resize crecer: fila 0 = %q, want %q (lo nuevo es fondo)", got, "AB")
	}
	if got := composeRow(c, 1); got != "" {
		t.Fatalf("resize crecer: fila 1 = %q, want \"\" (fondo)", got)
	}
}

// show/hide: hide saca la región del composite conservando su lienzo y coordenadas;
// show la devuelve tal cual. Idempotente.
func TestRegionShowHide(t *testing.T) {
	c := newCompositor(6, 1)
	bg := c.addRegion(0, 0, 6, 1, 0, "user")
	fg := c.addRegion(2, 0, 2, 1, 5, "user")
	bg.content.blitBlock(0, 0, blockOf("aaaaaa"))
	fg.content.blitBlock(0, 0, blockOf("FF"))
	if got := composeRow(c, 0); got != "aaFFaa" {
		t.Fatalf("antes de hide: fila 0 = %q, want %q", got, "aaFFaa")
	}

	fg.hide()
	if got := composeRow(c, 0); got != "aaaaaa" {
		t.Fatalf("tras hide: fila 0 = %q, want %q (fg oculta)", got, "aaaaaa")
	}
	// El lienzo y las coordenadas de fg se conservaron (no se borraron).
	if fg.x != 2 || fg.content.w != 2 {
		t.Fatalf("hide alteró lienzo/coords de fg: x=%d w=%d", fg.x, fg.content.w)
	}
	fg.hide() // idempotente
	if got := composeRow(c, 0); got != "aaaaaa" {
		t.Fatalf("hide x2: fila 0 = %q, want %q", got, "aaaaaa")
	}

	fg.show()
	if got := composeRow(c, 0); got != "aaFFaa" {
		t.Fatalf("tras show: fila 0 = %q, want %q (fg reaparece tal cual)", got, "aaFFaa")
	}
}

// destroy: la región desaparece del composite; es idempotente (destroy x2 inocuo);
// queda muerta (sus métodos posteriores fallarían vía checkRegion).
func TestRegionDestroy(t *testing.T) {
	c := newCompositor(6, 1)
	bg := c.addRegion(0, 0, 6, 1, 0, "user")
	fg := c.addRegion(2, 0, 2, 1, 5, "user")
	bg.content.blitBlock(0, 0, blockOf("aaaaaa"))
	fg.content.blitBlock(0, 0, blockOf("FF"))
	if got := composeRow(c, 0); got != "aaFFaa" {
		t.Fatalf("antes de destroy: fila 0 = %q, want %q", got, "aaFFaa")
	}

	fg.release() // lo que regionDestroy invoca tras untrack
	if fg.alive {
		t.Fatal("destroy: la región debería quedar muerta")
	}
	if len(c.regions) != 1 {
		t.Fatalf("destroy: el compositor debería tener 1 región, tiene %d", len(c.regions))
	}
	if got := composeRow(c, 0); got != "aaaaaa" {
		t.Fatalf("tras destroy: fila 0 = %q, want %q (fg fuera)", got, "aaaaaa")
	}
	fg.release() // idempotente
	if len(c.regions) != 1 {
		t.Fatalf("destroy x2 alteró el conjunto de regiones: %d", len(c.regions))
	}
}

// cursor — la última gana: A reclama el cursor en (1,1), luego B en (0,0); el frame
// lo posiciona según B (la última llamada). Las coordenadas locales se traducen a
// pantalla sumando el origen de la región. `cursor(nil)` lo oculta.
func TestRegionCursorLastWins(t *testing.T) {
	c := newCompositor(10, 5)
	a := c.addRegion(2, 2, 4, 2, 0, "user") // origen (2,2)
	b := c.addRegion(5, 0, 4, 2, 0, "user") // origen (5,0)

	// A reclama el cursor en local (1,1) → pantalla (3,3).
	c.setCursor(a, 1, 1, false)
	c.paint()
	// ANSI 1-based: fila 4, col 4 → "\x1b[4;4H" y mostrar.
	if enc := c.encoded(); !strings.Contains(enc, "\x1b[4;4H") || !strings.Contains(enc, "\x1b[?25h") {
		t.Fatalf("cursor A: frame = %q, want posicionar en (4,4) y mostrar", enc)
	}

	// B reclama el cursor (la última gana) en local (0,0) → pantalla (5,0).
	c.setCursor(b, 0, 0, false)
	c.paint()
	if enc := c.encoded(); !strings.Contains(enc, "\x1b[1;6H") {
		t.Fatalf("cursor B (última gana): frame = %q, want posicionar en (1,6)", enc)
	}
	if c.cursorOwner != b {
		t.Fatal("cursor: la dueña debería ser B (la última en reclamar)")
	}

	// cursor(nil) sobre B → ocultar.
	c.setCursor(b, 0, 0, true)
	c.paint()
	if enc := c.encoded(); !strings.Contains(enc, "\x1b[?25l") {
		t.Fatalf("cursor(nil): frame = %q, want ocultar (\\x1b[?25l)", enc)
	}
}

// cursor — soltar en hide/destroy de la dueña: si la región que lleva el cursor se
// oculta o se destruye, el cursor se suelta (se oculta en el frame). Destruir/ocultar
// OTRA región no toca el cursor de la actual.
func TestRegionCursorReleasedOnHideDestroy(t *testing.T) {
	// hide de la dueña suelta el cursor.
	c := newCompositor(10, 5)
	a := c.addRegion(0, 0, 4, 2, 0, "user")
	c.setCursor(a, 1, 1, false)
	c.paint()
	a.hide()
	if c.cursorOwner != nil {
		t.Fatal("hide de la dueña debería soltar el cursor")
	}
	c.paint()
	if enc := c.encoded(); !strings.Contains(enc, "\x1b[?25l") {
		t.Fatalf("hide dueña: frame = %q, want ocultar el cursor", enc)
	}

	// destroy (release) de la dueña suelta el cursor; destruir otra no.
	c2 := newCompositor(10, 5)
	x := c2.addRegion(0, 0, 4, 2, 0, "user")
	y := c2.addRegion(5, 0, 4, 2, 0, "user")
	c2.setCursor(x, 0, 0, false)
	y.release() // otra región: no toca el cursor de x
	if c2.cursorOwner != x {
		t.Fatal("destruir OTRA región no debería soltar el cursor de x")
	}
	x.release() // la dueña: suelta el cursor
	if c2.cursorOwner != nil {
		t.Fatal("destroy de la dueña debería soltar el cursor")
	}
}

// cursor fuera de pantalla: si la posición de pantalla del cursor cae fuera de
// límites (la región se movió fuera, o la coord local excede), el frame lo oculta en
// vez de posicionarlo fuera (G1: nunca fuera de límites).
func TestRegionCursorOffscreen(t *testing.T) {
	c := newCompositor(4, 2)
	r := c.addRegion(0, 0, 4, 2, 0, "user")
	c.setCursor(r, 0, 0, false)
	c.paint()
	if enc := c.encoded(); !strings.Contains(enc, "\x1b[1;1H") {
		t.Fatalf("cursor en pantalla: frame = %q, want posicionar en (1,1)", enc)
	}

	// Mover la región fuera: el cursor caería fuera de pantalla → se oculta.
	r.move(10, 0)
	c.paint()
	if enc := c.encoded(); !strings.Contains(enc, "\x1b[?25l") {
		t.Fatalf("cursor fuera de pantalla: frame = %q, want ocultar (G1)", enc)
	}
}
