package runtime

// Compositor de `nu.ui` (api.md §9.1, sesión S29). Es la pieza Go que ADR-007
// pone bajo `nu.ui`: las regiones (rectángulos con z-order) y el `blit` de un
// Block son superficie Lua, pero **componer + difar + codificar a ANSI** ocurre
// aquí, en Go, y el resultado se coalesce (se pinta como mucho cada ~30 ms, sin
// "flush" manual). Promueve a producción el MODELO VALIDADO por el spike de S28
// (ADR-012): rejilla de celdas plana, `blit` como **copia** con viewport y
// recorte por ambos extremos (G28), diff por runs → ANSI, coalescing. El spike
// (interno y desechable) cumplió su función —medir que el overhead de orquestar
// desde Lua era despreciable— y queda superado por este fichero.
//
// EL MODELO DE COMPOSICIÓN (por qué una rejilla por región). El compositor
// mantiene una **rejilla de pantalla** (back/front, para el diff) y una lista de
// regiones; cada región lleva **su propia rejilla** de su tamaño lógico. `blit`/
// `fill`/`clear` escriben en la rejilla de la región (persisten entre frames: el
// contenido de una región no se borra solo, como una ventana real), y cada
// pintado compone apilando las regiones por z-order sobre la rejilla de pantalla,
// **recortando** cada una al rectángulo visible (G1). Separar "lo que la región
// contiene" (su rejilla) de "dónde y cómo se ve" (el composite) hace triviales
// las dos propiedades sagradas:
//   - **Resize (G1)**: una región fuera de pantalla no se toca; el composite la
//     recorta al pintar y, si la pantalla crece, reaparece tal cual —porque sus
//     coordenadas y su rejilla nunca cambiaron—.
//   - **Blit = copia, nunca re-render (G28)**: `blit` copia la ventana visible del
//     Block a la rejilla de la región; blittear el mismo Block con otro offset es
//     otra copia, jamás reconstruye el Block (scroll = re-blit barato).
//
// CONCURRENCIA (ADR-008). `nu.ui` es **solo estado principal**: todas las
// mutaciones (region/blit/fill/clear) corren bajo el token Lua. El timer de
// coalescing (S29) también dispara el pintado en el estado principal. Por eso el
// compositor no lleva candado propio —el token lo serializa, como el bus de
// eventos—.

import (
	"strconv"
	"strings"

	"github.com/rivo/uniseg"
)

// cell es una celda de la rejilla del compositor: un grapheme (como string, para
// no partir un emoji/ZWJ en una rune) más su estilo resuelto y su anchura. Una
// celda vacía es `{r:"", w:1}`: un espacio sin estilo (fondo). La segunda celda
// de un grapheme ancho (emoji/east-asian, w=2) queda como continuación
// (`r:"", w:0`) para no pintarse dos veces.
type cell struct {
	r  string // el grapheme de la celda ("" = espacio, fondo)
	st *style // estilo resuelto (nil = sin estilo)
	w  int    // anchura en celdas del grapheme (1, o 2 para east-asian/emoji)
}

// grid es una rejilla de celdas w×h. Plana (un slice indexado por `y*w+x`) para
// localidad de caché: la composición y el diff la recorren entera cada frame y un
// slice plano evita el doble salto de punteros de `[][]cell`. La usan tanto la
// pantalla (back/front del compositor) como cada región (su contenido lógico).
type grid struct {
	w, h  int
	cells []cell
}

// newGrid crea una rejilla w×h llena de celdas vacías (fondo). Un tamaño no
// positivo da una rejilla de 0 celdas (una región degenerada no pinta nada, pero
// existe y reaparece si se redimensiona —eso es S30—).
func newGrid(w, h int) *grid {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	g := &grid{w: w, h: h, cells: make([]cell, w*h)}
	// Una celda recién creada es fondo (`{r:"", w:1}`), no el cero de Go (`w:0`,
	// que es la marca de *continuación* de un grapheme ancho). Inicializar a `w:1`
	// deja toda la rejilla como "espacios", coherente con `clear`, para que el diff
	// y la inspección traten una celda virgen como un espacio, no como media celda.
	g.clear()
	return g
}

// clear vacía la rejilla (todas las celdas a fondo) sin reasignar el slice —se
// reutiliza entre frames para no presionar al GC en el camino caliente (un frame
// por token de streaming asignaría MiB innecesarios)—.
func (g *grid) clear() {
	for i := range g.cells {
		g.cells[i] = cell{w: 1}
	}
}

// at devuelve un puntero a la celda (x,y), o nil si está fuera de la rejilla.
func (g *grid) at(x, y int) *cell {
	if x < 0 || y < 0 || x >= g.w || y >= g.h {
		return nil
	}
	return &g.cells[y*g.w+x]
}

// blitBlock estampa un `*block` (S22) en la rejilla, en coordenadas LOCALES
// (ox, oy). Es el corazón del modelo "blit = copia, nunca re-render" (§9.1): no
// recalcula el Block, copia su ventana visible celda a celda. Implementa el
// **viewport con recorte por ambos extremos (G28)**: `ox/oy` pueden ser
// **negativos** y recortan el borde *inicial* del Block (un scroll hacia abajo es
// blit con `oy` negativo: `blit(0,-3,doc)` muestra `doc` desde su cuarta fila), y
// el exceso recorta el final por el borde de la rejilla. Esto hace que el scroll
// cueste una copia de la ventana, no un re-render —la propiedad que ADR-007 quería
// barata—.
//
// La rejilla destino es la de la **región** (coordenadas locales 0..w-1, 0..h-1);
// el recorte a pantalla y el z-order ocurren después, en `composite`. Así blit no
// sabe nada de dónde vive la región: solo copia dentro de su propio lienzo.
//
// La resolución de graphemes anchos: una celda ancha (w=2) ocupa su celda y deja
// la siguiente como continuación (r="", w:0) para no pintar dos veces; el diff las
// trata como una unidad al recorrer.
func (g *grid) blitBlock(ox, oy int, b *block) {
	for ly := 0; ly < g.h; ly++ {
		// La fila del Block que cae en la fila `ly` de la rejilla es `ly - oy`
		// (offset negativo = empezar más abajo en el Block: scroll).
		by := ly - oy
		if by < 0 || by >= len(b.lines) {
			continue
		}
		// Recorre los spans de la línea acumulando la columna lógica del Block; solo
		// se copian las celdas que caen en la ventana visible [0, g.w).
		col := 0 // columna lógica dentro del Block (en celdas)
		for _, sp := range b.lines[by] {
			for _, gr := range graphemesOf(sp.text) {
				gw := grWidth(gr)
				// Columna de la rejilla: el origen del Block se ESTAMPA en `ox` (igual
				// que `oy` lo estampa en su fila, `by = ly - oy` arriba). Un `ox`
				// negativo recorta el borde inicial (scroll), un positivo desplaza el
				// Block a la derecha (padding/posición). G37: antes era `col - ox`, lo
				// que invertía el signo SOLO en X respecto a Y y al contrato de api.md
				// §9.1 ("un offset negativo recorta el borde inicial" en AMBOS ejes);
				// nunca se notó porque ningún widget se blitteaba en x>0 hasta el
				// padding del toolkit (G36).
				lx := col + ox
				col += gw
				if lx < 0 || lx >= g.w {
					continue
				}
				c := g.at(lx, ly)
				if c == nil {
					continue
				}
				c.r = gr
				c.st = sp.st
				c.w = gw
				// La segunda celda de un grapheme ancho queda como continuación.
				if gw == 2 {
					if c2 := g.at(lx+1, ly); c2 != nil {
						c2.r = ""
						c2.st = sp.st
						c2.w = 0
					}
				}
			}
		}
	}
}

// fill rellena la rejilla entera con un estilo (el lienzo de `Region:fill`,
// §9.1): pone todas sus celdas a espacio con ese estilo. `Region:clear` es
// `fill(nil)` (espacio sin estilo, fondo).
func (g *grid) fill(st *style) {
	for i := range g.cells {
		g.cells[i] = cell{st: st, w: 1}
	}
}

// graphemesOf parte un string en sus graphemes (clusters), para que un emoji o
// una secuencia ZWJ ocupen una sola celda lógica. Reusa uniseg (igual que
// text.width, block.go). Para un span típico ASCII es un grapheme por byte.
func graphemesOf(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, len(s))
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		out = append(out, gr.Str())
	}
	return out
}

// grWidth es la anchura en celdas de un grapheme (1, o 2 para east-asian/emoji),
// coherente con text.width (block.go usa uniseg.StringWidth). Un grapheme de
// anchura 0 (combinante suelto) se trata como 1 para no descuadrar la rejilla.
func grWidth(g string) int {
	w := uniseg.StringWidth(g)
	if w <= 0 {
		return 1
	}
	return w
}

// uiRegion es una región de composición (el tipo `Region` §9.1): un rectángulo
// con z-order que posee una porción de la pantalla y su propio lienzo (`content`).
// `x,y` son la esquina superior izquierda en coordenadas de pantalla; `z` decide
// el apilado (mayor z gana en la zona común). El contenido lo escriben
// `blit`/`fill`/`clear` y persiste entre frames (una región no se borra sola).
//
// Es un `ownedHandle` (handles.go, S13): se etiqueta con el dueño que la creó
// (`currentOwner()`) para que `reload` la destruya con el resto de los handles del
// plugin —"reload no deja regiones huérfanas" (G2)—. La destrucción explícita
// (`Region:destroy`) es S30; aquí `release` la quita del compositor.
type uiRegion struct {
	comp      *compositor
	x, y      int
	z         int
	content   *grid  // el lienzo lógico de la región (su tamaño w×h)
	ownerName string // dueño con que se creó (para reload, G2)
	seq       uint64 // orden de creación: desempata z iguales (estable)
	alive     bool   // false tras release/destroy: deja de componerse
	visible   bool   // false tras hide(): conserva contenido/coords, no se compone (S30)
}

// move recoloca la región (S30, §9.1): cambia solo sus coordenadas de pantalla; el
// siguiente `composite` la pinta en el nuevo sitio, recortada si se sale (G1). El
// lienzo (contenido) no se toca. Marca sucio.
func (r *uiRegion) move(x, y int) {
	r.x, r.y = x, y
	r.comp.markDirty()
}

// resizeRegion cambia el tamaño lógico de la región (S30, §9.1): reasigna su lienzo
// al nuevo w×h **conservando el contenido donde quepa** (la esquina superior
// izquierda se preserva; lo que cae fuera del nuevo tamaño se descarta, lo que crece
// aparece como fondo). Es coherente con el modelo "la región es una ventana": un
// resize no borra lo que sigue siendo visible, igual que agrandar una ventana real
// conserva su contenido. Marca sucio.
func (r *uiRegion) resizeRegion(w, h int) {
	old := r.content
	ng := newGrid(w, h)
	// Copia la intersección de ambas rejillas (esquina (0,0) común). Lo que excede el
	// nuevo tamaño se pierde; lo nuevo queda como fondo (newGrid ya lo dejó así).
	cw := min2(old.w, ng.w)
	ch := min2(old.h, ng.h)
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			ng.cells[y*ng.w+x] = old.cells[y*old.w+x]
		}
	}
	r.content = ng
	r.comp.markDirty()
}

// raise sube la región al frente del z-order (S30, §9.1): le asigna un z mayor que
// el de cualquier otra región viva, de modo que gana en toda zona de solape. El
// orden relativo del resto no cambia (solo esta se mueve al tope). Modelar raise/
// lower como reasignación de z (en vez de reordenar una lista) deja el criterio de
// apilado en un solo sitio —`regionLess` ordena por (z, seq)— y hace que un blit o
// un composite posteriores respeten el cambio sin estado extra. Marca sucio.
func (r *uiRegion) raise() {
	max := r.z
	for _, x := range r.comp.regions {
		if x != r && x.z > max {
			max = x.z
		}
	}
	// +1 garantiza que queda estrictamente por encima de todas; si ya era la mayor,
	// igualmente sube (idempotente en efecto visible, pero coherente).
	r.z = max + 1
	r.comp.markDirty()
}

// lower baja la región al fondo del z-order (S30, §9.1): le asigna un z menor que el
// de cualquier otra región viva. Simétrico de `raise`; conserva el orden relativo
// del resto. Marca sucio.
func (r *uiRegion) lower() {
	min := r.z
	for _, x := range r.comp.regions {
		if x != r && x.z < min {
			min = x.z
		}
	}
	r.z = min - 1
	r.comp.markDirty()
}

// show vuelve a componer la región tras un `hide` (S30, §9.1). Idempotente. Marca
// sucio para que el próximo frame la repinte.
func (r *uiRegion) show() {
	if r.visible {
		return
	}
	r.visible = true
	r.comp.markDirty()
}

// hide oculta la región (S30, §9.1): deja de componerse, pero su lienzo y sus
// coordenadas se conservan —`show` la devuelve tal cual—. Si era la dueña del cursor
// real, lo suelta (oculta): una región que no se ve no puede llevar el cursor.
// Idempotente. Marca sucio.
func (r *uiRegion) hide() {
	if !r.visible {
		return
	}
	r.visible = false
	r.comp.dropCursorIf(r)
	r.comp.markDirty()
}

// release destruye la región: la descuelga del compositor y la marca muerta.
// Idempotente y silencioso (lo exige `ownedHandle`): lo llama `reload` al soltar
// los handles del plugin, y NO re-toca el registro de dueños (eso lo orquesta
// `releaseOwnerHandles`). Tras esto la región no vuelve a componerse.
func (r *uiRegion) release() {
	if !r.alive {
		return
	}
	r.alive = false
	r.comp.dropCursorIf(r) // si llevaba el cursor real, soltarlo (S30)
	r.comp.removeRegion(r)
	r.comp.markDirty()
}

// min2 devuelve el menor de dos enteros (helper local de `resizeRegion`).
func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// owner devuelve el dueño con que se etiquetó la región al crearse (lo usa
// `untrack` para encontrar su lista en el registro de handles).
func (r *uiRegion) owner() string { return r.ownerName }

// compositor es el compositor de `nu.ui`: la rejilla de pantalla (back/front,
// para el diff), la lista de regiones, y el buffer ANSI del último frame. Vive en
// el estado principal bajo el token (ADR-008); el timer de coalescing dispara
// `paint` como mucho cada ~30 ms.
type compositor struct {
	w, h    int
	back    *grid           // el frame en composición (se recompone cada pintado)
	front   *grid           // el último frame emitido (base del diff)
	regions []*uiRegion     // regiones vivas, en orden de creación (se ordenan al componer)
	enc     strings.Builder // buffer ANSI del último frame (lo recogería el TTY en S32)
	dirty   bool            // hay cambios sin pintar (coalescing): el timer pinta si está sucio
	nextSeq uint64          // secuencia de creación de regiones (desempate de z)
	frames  int             // pintados con diff no vacío (diagnóstico/tests)
	skipped int             // pintados coalescidos a vacío (diff vacío, nada que emitir)

	// Propiedad del cursor real del terminal (§9.1, S30). SOLO UNA región puede tener
	// el cursor; la ÚLTIMA llamada a `Region:cursor` gana (sustituye a la anterior).
	// `cursorOwner` es la región dueña (nil = nadie lo reclama → cursor oculto en el
	// frame); `cursorX/Y` son sus coordenadas LOCALES (relativas a la región), que el
	// frame traduce a coordenadas de pantalla al emitir. `cursorOff` lo fuerza oculto
	// aunque haya dueño (la última llamada fue `cursor(nil)`).
	cursorOwner *uiRegion
	cursorX     int
	cursorY     int
	cursorOff   bool   // la última cursor() fue nil (ocultar), aunque exista dueño
	lastCursor  string // última secuencia de cursor emitida (damage tracking: no reemitir igual)
}

// newCompositor crea un compositor de tamaño w×h con ambas rejillas en blanco y
// sin regiones. El tamaño sale de `nu.ui.size()` (del terminal con TTY, o un
// default headless inyectable para test).
func newCompositor(w, h int) *compositor {
	return &compositor{w: w, h: h, back: newGrid(w, h), front: newGrid(w, h)}
}

// markDirty marca que hay cambios pendientes de pintar. Lo llaman las mutaciones
// (region/blit/fill/clear/resize). El pintado real lo dispara el timer de
// coalescing (a lo sumo cada ~30 ms): así N cambios entre dos pintados producen
// UN frame, no N (ADR-007). No hay flush manual.
func (c *compositor) markDirty() { c.dirty = true }

// addRegion crea y registra una región nueva sobre el compositor. La rejilla de
// contenido es de su tamaño lógico (w×h), independiente del de la pantalla —vive
// aunque la región caiga fuera (G1)—. Le asigna su número de secuencia (para
// desempatar z iguales de forma estable) y marca sucio el compositor.
func (c *compositor) addRegion(x, y, w, h, z int, owner string) *uiRegion {
	r := &uiRegion{
		comp:      c,
		x:         x,
		y:         y,
		z:         z,
		content:   newGrid(w, h),
		ownerName: owner,
		seq:       c.nextSeq,
		alive:     true,
		visible:   true, // una región nace visible; `hide`/`show` la conmutan (S30)
	}
	c.nextSeq++
	c.regions = append(c.regions, r)
	c.markDirty()
	return r
}

// removeRegion descuelga una región de la lista (al destruirla o al recargar su
// plugin). Quita por intercambio-con-el-último: el orden de la lista no importa
// (la composición ordena por z cada vez), solo que el conjunto sea exacto.
func (c *compositor) removeRegion(r *uiRegion) {
	for i, x := range c.regions {
		if x == r {
			c.regions[i] = c.regions[len(c.regions)-1]
			c.regions = c.regions[:len(c.regions)-1]
			return
		}
	}
}

// setCursor coloca el cursor real del terminal (§9.1, S30). Si `off` es true se
// pide ocultarlo (`Region:cursor(nil)`); si no, `r` pasa a ser la dueña con el
// cursor en sus coordenadas LOCALES (x,y). SOLO UNA región puede tenerlo: la última
// llamada gana y desbanca a la dueña anterior (su `cursor()` previo se pierde, como
// pide §9.1). El frame emite la secuencia de posicionar/ocultar en `paint`. Marca
// sucio (el cambio de cursor debe reflejarse en el próximo frame).
func (c *compositor) setCursor(r *uiRegion, x, y int, off bool) {
	c.cursorOwner = r
	c.cursorX, c.cursorY = x, y
	c.cursorOff = off
	c.markDirty()
}

// dropCursorIf suelta el cursor si `r` era su dueña (al `hide`/`destroy`/reload de la
// dueña): una región que deja de verse no puede llevar el cursor real. Lo deja
// oculto (sin dueño). Si `r` no era la dueña, no hace nada —destruir otra región no
// toca el cursor de la actual—. Idempotente.
func (c *compositor) dropCursorIf(r *uiRegion) {
	if c.cursorOwner == r {
		c.cursorOwner = nil
		c.cursorOff = false
	}
}

// composite recompone el back buffer apilando las regiones vivas y VISIBLES por
// z-order sobre la pantalla. Recorta cada región al rectángulo visible (G1: lo
// que cae fuera no se pinta, las coordenadas no se tocan). Es el corazón del
// z-order: se ordena por (z asc, seq asc) y se pintan de menor a mayor, así la de
// mayor z queda encima en la zona común; con z iguales gana la creada después
// (orden de llegada estable). Una región oculta (`hide`, S30) se salta: su lienzo
// y coordenadas persisten, pero no aporta celdas al frame. Una celda de continuación
// (w=0) se copia con su fondo para no pintar media celda ancha al recortar.
func (c *compositor) composite() {
	c.back.clear()

	// Ordena las regiones por z (y, a igual z, por orden de creación) sin alterar
	// la lista persistente. Insertion sort sobre una copia ligera: el número de
	// regiones es pequeño (decenas), no merece `sort.Slice` ni su alloc de closure
	// en el camino caliente.
	order := make([]*uiRegion, len(c.regions))
	copy(order, c.regions)
	for i := 1; i < len(order); i++ {
		j := i
		for j > 0 && regionLess(order[j], order[j-1]) {
			order[j], order[j-1] = order[j-1], order[j]
			j--
		}
	}

	for _, r := range order {
		if !r.visible {
			continue // oculta (hide, S30): conserva su lienzo, no aporta al frame
		}
		c.blitRegion(r)
	}
}

// regionLess ordena por z ascendente; a igual z, por secuencia de creación
// ascendente (la creada después gana, queda encima). Es el criterio que `raise`/
// `lower` (S30) afinará; aquí define el apilado base.
func regionLess(a, b *uiRegion) bool {
	if a.z != b.z {
		return a.z < b.z
	}
	return a.seq < b.seq
}

// blitRegion copia la rejilla de contenido de una región a la pantalla en su
// posición (x,y), **recortando** a los límites de la pantalla (G1). Una región
// total o parcialmente fuera de pantalla se recorta sin error y sus coordenadas no
// se tocan: al crecer la pantalla, las celdas que antes caían fuera vuelven a
// componerse. Si no cabe nada, no pinta.
func (c *compositor) blitRegion(r *uiRegion) {
	g := r.content
	for ly := 0; ly < g.h; ly++ {
		sy := r.y + ly
		if sy < 0 || sy >= c.h {
			continue // fila fuera de pantalla: recortada (G1)
		}
		for lx := 0; lx < g.w; lx++ {
			sx := r.x + lx
			if sx < 0 || sx >= c.w {
				continue // columna fuera de pantalla: recortada (G1)
			}
			dst := c.back.at(sx, sy)
			if dst == nil {
				continue
			}
			*dst = g.cells[ly*g.w+lx]
		}
	}
}

// invalidate descarta la base del diff: reinicia el `front` a una rejilla en blanco
// y olvida la última secuencia de cursor, de modo que el SIGUIENTE `paint` re-emita
// TODA la pantalla visible (no solo lo que cambió respecto al frame anterior). Lo usa
// el driver de TTY (S33) al **conectar el terminal real** (`attachOutput`): los
// pintados previos (p. ej. el de la pantalla desnuda, o los del arranque) llenaron el
// `front`, pero sus bytes no se enviaron a ningún terminal —`out` aún era nil—, así
// que el terminal recién puesto en raw mode/alt-screen está en blanco y necesita el
// frame completo, no un diff contra un `front` que él nunca vio. Marca sucio para que
// el painter repinte en el próximo tick.
func (c *compositor) invalidate() {
	c.front = newGrid(c.w, c.h)
	c.lastCursor = ""
	c.markDirty()
}

// resize cambia el tamaño de la pantalla (un `ui:resize`): reasigna las rejillas
// de back/front al nuevo tamaño y marca sucio para recomponer. Las regiones NO se
// tocan (sus coordenadas y lienzos persisten, G1): el siguiente `composite` las
// recorta al nuevo rectángulo, así una región que se salía reaparece al crecer la
// pantalla. El front se reinicia (la geometría cambió: el diff del próximo frame
// repinta todo lo visible, no hay base válida contra la que difar).
func (c *compositor) resize(w, h int) {
	if w == c.w && h == c.h {
		return
	}
	c.w, c.h = w, h
	c.back = newGrid(w, h)
	c.front = newGrid(w, h)
	c.markDirty()
}

// paint compone el frame, lo difa contra el front, codifica a ANSI las celdas que
// cambiaron y promueve el back a front. Lo dispara el timer de coalescing (a lo
// sumo cada ~30 ms) **solo si hay cambios** (`dirty`): así ningún cambio = ningún
// byte (ADR-007 "repinta por eventos"). Devuelve el número de **celdas** que
// cambiaron (0 = frame coalescido a vacío, nada que emitir). Limpia `dirty`.
//
// Esta es la pieza que el veto de ADR-012 midió: compose ya ocurrió arriba; aquí
// va diff + encode. En headless (S29) no hay TTY: el resultado vive en `enc` y los
// tests lo inspeccionan (`encoded`); S32 lo enviará al terminal real.
func (c *compositor) paint() int {
	c.composite()
	c.dirty = false
	changed := c.diffEncode()
	c.encodeCursor()
	return changed
}

// encodeCursor anexa al buffer del frame la secuencia que coloca u oculta el cursor
// real del terminal (§9.1, S30), SOLO si cambió respecto al frame anterior (damage
// tracking, como las celdas): así un frame sin cambios de cursor no emite bytes de
// cursor —y un frame totalmente sin cambios sigue siendo vacío (coalescing de S29)—.
// Una región dueña con cursor visible y en pantalla → mover el cursor a su posición
// de PANTALLA (la local de la región + su origen) y mostrarlo (`ESC[?25h`). Sin
// dueño, o `cursor(nil)`, o el cursor cae fuera de pantalla (o de una región oculta/
// recortada) → ocultarlo (`ESC[?25l`). Nunca se posiciona fuera de límites (G1).
func (c *compositor) encodeCursor() {
	seq := c.cursorSeq()
	if seq == c.lastCursor {
		return // sin cambio de cursor: no reemitir (damage tracking)
	}
	c.lastCursor = seq
	c.enc.WriteString(seq)
}

// cursorSeq calcula la secuencia ANSI del estado actual del cursor (sin emitirla):
// ocultar (`ESC[?25l`) si no hay dueño, está apagado o cae fuera de pantalla; o
// posicionar + mostrar (`ESC[y;xH` + `ESC[?25h`) en otro caso. Coordenadas 1-based.
func (c *compositor) cursorSeq() string {
	r := c.cursorOwner
	if r == nil || c.cursorOff || !r.alive || !r.visible {
		return "\x1b[?25l"
	}
	sx := r.x + c.cursorX
	sy := r.y + c.cursorY
	if sx < 0 || sy < 0 || sx >= c.w || sy >= c.h {
		return "\x1b[?25l" // fuera de pantalla: ocultar (G1)
	}
	return "\x1b[" + strconv.Itoa(sy+1) + ";" + strconv.Itoa(sx+1) + "H\x1b[?25h"
}

// diffEncode recorre la rejilla por filas; arranca un "run" allí donde una celda
// difiere del front y lo extiende mientras siga difiriendo, emitiendo un único
// reposicionamiento de cursor (`ESC[y;xH`) por run y un SGR (`ESC[...m`) solo
// cuando el estilo cambia respecto a la celda anterior emitida (minimiza bytes,
// como un compositor real). Promueve cada celda emitida al front. NO se manda a un
// terminal (headless S29): el coste de construir la cadena —que es lo que el
// camino caliente paga— es el mismo, y el resultado es inspeccionable.
//
// CORRECCIÓN sobre el spike (revisión de S28): el SGR se fuerza al inicio de cada
// run (`stDirty`) y `lastSt` se **resetea a nil al empezar el pintado**, no se
// arrastra entre runs sin reposicionar: un run nuevo siempre reabre con su SGR, de
// modo que ningún run hereda el estilo de un run anterior que quedó en otra parte
// de la pantalla (el bug "run con encabezado en celda de continuación / SGR
// huérfano" que la revisión del spike anotó). Una celda de continuación (w=0)
// dentro de un run se promueve al front pero no emite su propio glifo.
func (c *compositor) diffEncode() int {
	c.enc.Reset()
	changed := 0
	var lastSt *style
	var stDirty bool

	for y := 0; y < c.h; y++ {
		x := 0
		for x < c.w {
			bi := y*c.w + x
			bc := &c.back.cells[bi]
			fc := &c.front.cells[bi]
			if cellEqual(bc, fc) {
				x++
				continue
			}
			// Arranca un run de celdas cambiadas en esta fila. Un solo move-cursor
			// para todo el run (coordenadas ANSI 1-based). Cada run reabre su SGR.
			c.enc.WriteString("\x1b[")
			c.enc.WriteString(strconv.Itoa(y + 1))
			c.enc.WriteByte(';')
			c.enc.WriteString(strconv.Itoa(x + 1))
			c.enc.WriteByte('H')
			stDirty = true
			for x < c.w {
				bi = y*c.w + x
				bc = &c.back.cells[bi]
				fc = &c.front.cells[bi]
				if cellEqual(bc, fc) {
					break
				}
				if bc.w == 0 {
					// Continuación de un grapheme ancho ya emitido: se promueve al
					// front pero no se pinta sola (su glifo lo emitió la celda previa).
					*fc = *bc
					x++
					continue
				}
				if !styleEqual(bc.st, lastSt) || stDirty {
					writeSGR(&c.enc, bc.st)
					lastSt = bc.st
					stDirty = false
				}
				if bc.r == "" {
					c.enc.WriteByte(' ')
				} else {
					c.enc.WriteString(bc.r)
				}
				*fc = *bc // promueve esta celda al front
				changed++
				x++
			}
		}
	}
	if changed == 0 {
		c.skipped++
		return 0
	}
	c.frames++
	return changed
}

// encoded devuelve los bytes ANSI del último `paint()` (lo que se enviaría al
// terminal en S32). En headless es la salida observable de la tubería; los tests
// comprueban su forma y su tamaño.
func (c *compositor) encoded() string { return c.enc.String() }

// cellEqual compara dos celdas para el diff: iguales si coinciden grapheme,
// anchura y estilo. Una celda que no cambió respecto al front no se reemite
// (damage tracking de ADR-007).
func cellEqual(a, b *cell) bool {
	return a.r == b.r && a.w == b.w && styleEqual(a.st, b.st)
}

// styleEqual compara dos `*style` (incluido el caso nil = sin estilo). Compara por
// valor todos los campos para que un cambio de color o atributo dispare un SGR.
func styleEqual(a, b *style) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// writeSGR emite la secuencia SGR (`ESC[...m`) para un estilo. Un estilo nil emite
// el reset (`ESC[0m`): vuelve al fondo. Los colores son literales (§9.2): un
// "#rrggbb" se emite como truecolor (`38;2;r;g;b`), un índice 0-255 como `38;5;n`.
// El degradado fino con `caps().colors` lo refinará S32; el coste de construir la
// cadena es del mismo orden (lo que el veto de ADR-012 midió).
func writeSGR(b *strings.Builder, st *style) {
	b.WriteString("\x1b[0") // reset + acumula atributos
	if st != nil {
		if st.bold {
			b.WriteString(";1")
		}
		if st.italic {
			b.WriteString(";3")
		}
		if st.underline {
			b.WriteString(";4")
		}
		if st.reverse {
			b.WriteString(";7")
		}
		if st.fgSet {
			writeColor(b, st.fg, true)
		}
		if st.bgSet {
			writeColor(b, st.bg, false)
		}
	}
	b.WriteByte('m')
}

// writeColor emite el tramo SGR de un color literal. "#rrggbb" → truecolor
// (`;38;2;r;g;b` o `;48;...` para fondo); un índice → 256 colores (`;38;5;n`). Un
// color mal formado se ignora (no debería llegar: el Block ya guarda colores
// normalizados, ui.go normalizeColor).
func writeColor(b *strings.Builder, c string, fg bool) {
	base := ";48"
	if fg {
		base = ";38"
	}
	if strings.HasPrefix(c, "#") && len(c) == 7 {
		r, _ := strconv.ParseInt(c[1:3], 16, 0)
		gg, _ := strconv.ParseInt(c[3:5], 16, 0)
		bb, _ := strconv.ParseInt(c[5:7], 16, 0)
		b.WriteString(base)
		b.WriteString(";2;")
		b.WriteString(strconv.FormatInt(r, 10))
		b.WriteByte(';')
		b.WriteString(strconv.FormatInt(gg, 10))
		b.WriteByte(';')
		b.WriteString(strconv.FormatInt(bb, 10))
		return
	}
	// Índice 0-255.
	b.WriteString(base)
	b.WriteString(";5;")
	b.WriteString(c)
}
