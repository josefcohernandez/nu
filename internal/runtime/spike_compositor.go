package runtime

// SPIKE de ADR-007 (sesión S28) — compositor MÍNIMO e INTERNO. NO es la API
// pública `nu.ui` §9 (eso es S29+): es un prototipo de validación cuyo único fin
// es **medir el coste de la tubería compose+diff+encode** que el modelo "Lua
// decide, Go ejecuta" (ADR-004) pone sobre el camino caliente, y compararlo con
// el **overhead de orquestar ese render desde Lua** frente a hacerlo todo en Go.
// De esa medición sale el **veto pre-comprometido de ADR-007**: si la tubería +
// el overhead de Lua no caben holgados en un presupuesto interactivo, el toolkit
// (S42) se construirá en Go en vez de en Lua (reordenando la Fase 8).
//
// POR QUÉ ES INTERNO Y NO TOCA api.md. El spike construye la versión mínima de
// lo que S29 expondrá como `nu.ui` (celdas, regiones, blit, diff→ANSI), pero NO
// se cuelga del global `nu`: vive solo en este fichero y lo invocan los
// benchmarks/tests del spike (spike_bench_test.go). Así valida la viabilidad de
// la primitiva sin congelar nada ni ampliar la superficie sagrada (regla del
// plan: "el spike es interno; si §9 no basta es un hallazgo G##, pero el spike
// puede seguir"). Reusa las primitivas Go ya construidas: el `*block` de S22
// (block.go) como moneda de blit, el render markdown de S23 (renderMarkdownBlocks)
// y el scorer fuzzy de S27 (fuzzyScore) como cargas calientes.
//
// LIMITACIÓN DEL ENTORNO (headless, sin TTY). Este entorno no tiene un terminal
// real, así que el diff NO se escribe a un TTY: se serializa a un **buffer en
// memoria**. La medición es por tanto el **coste de cómputo** de la tubería
// (componer la rejilla + difar contra el frame anterior + codificar a ANSI) más
// el overhead de orquestar desde Lua — NO la latencia real del terminal (ancho
// de banda del pty, vsync). Es deliberado: lo que el veto de ADR-007 pone en
// juego es justamente el coste de cómputo en el camino caliente (la limitación
// nº8 de modelo-ejecucion.md: "el rendimiento de Lua sin JIT"), no la física del
// terminal, que es idéntica se decida Lua o Go. El veto se basa en ese coste.

import (
	"strconv"
	"strings"

	"github.com/rivo/uniseg"
)

// scell es una celda de la rejilla del compositor: un grapheme (como string,
// para no partir un emoji/ZWJ en una rune) más su estilo resuelto. El compositor
// real (S29) usaría un tipo más fino; para el spike basta `{rune, style}` por
// celda (rune como string para soportar graphemes anchos), que es lo que ADR-007
// llama "superficie de celdas".
type scell struct {
	r  string // el grapheme de la celda ("" = espacio, fondo)
	st *style // estilo resuelto (nil = sin estilo)
	w  int    // anchura en celdas del grapheme (1, o 2 para east-asian/emoji)
}

// sgrid es la rejilla de celdas del compositor: una matriz w×h. Es el "back
// buffer" que se compone cada frame y se difa contra el frame anterior para
// emitir solo lo que cambió (damage tracking de ADR-007). Plana (un slice por
// índice `y*w+x`) para localidad de caché —la composición la recorre entera cada
// frame y un slice plano evita el doble salto de punteros de `[][]scell`.
type sgrid struct {
	w, h  int
	cells []scell
}

// newGrid crea una rejilla w×h llena de celdas vacías (fondo). Una celda vacía
// es `{r:"", w:1}`: un espacio sin estilo.
func newGrid(w, h int) *sgrid {
	return &sgrid{w: w, h: h, cells: make([]scell, w*h)}
}

// clear vacía la rejilla (todas las celdas a fondo) sin reasignar el slice —se
// reutiliza el back buffer entre frames para no presionar al GC en el camino
// caliente (un frame por token de streaming asignaría MiB innecesarios).
func (g *sgrid) clear() {
	for i := range g.cells {
		g.cells[i] = scell{w: 1}
	}
}

// at devuelve un puntero a la celda (x,y), o nil si está fuera de la rejilla.
func (g *sgrid) at(x, y int) *scell {
	if x < 0 || y < 0 || x >= g.w || y >= g.h {
		return nil
	}
	return &g.cells[y*g.w+x]
}

// sregion es una región rectangular de composición (el prototipo de `Region`
// §9.1): un rectángulo con z-order, dueño de una porción de la pantalla. El
// spike soporta varias regiones con z-order para validar la composición (el
// frame final apila las regiones por z; aquí, al blittear, la última gana —el
// diff por z-order completo es S29). `x,y` son la esquina superior izquierda en
// coordenadas de pantalla.
type sregion struct {
	x, y, w, h int
	z          int
}

// blitBlock estampa un `*block` (S22) en la rejilla, dentro de la región, en
// coordenadas LOCALES (ox, oy) a la región. Es el corazón del modelo "blit = copia,
// nunca re-render" (§9.1): no recalcula el Block, copia su ventana visible celda
// a celda. Implementa el **viewport con recorte por ambos extremos (G28)**:
// `ox/oy` pueden ser **negativos** y recortan el borde inicial del Block (un
// scroll hacia abajo es blit con `oy` negativo), y el exceso recorta el final por
// el borde de la región. Esto es lo que hace que el scroll cueste una copia de la
// ventana, no un re-render (la propiedad que ADR-007 quiere validar barata).
//
// La resolución de graphemes anchos: una celda ancha (emoji/east-asian, w=2)
// ocupa su celda y deja la siguiente como continuación (r="", w:0) para no pintar
// dos veces; el diff las trata como una unidad al recorrer.
func (g *sgrid) blitBlock(reg sregion, ox, oy int, b *block) {
	for ly := 0; ly < reg.h; ly++ {
		// La fila del Block que cae en la fila `ly` de la región es `ly - oy`
		// (offset negativo = empezar más abajo en el Block: scroll).
		by := ly - oy
		if by < 0 || by >= len(b.lines) {
			continue
		}
		// Coordenada de pantalla de esta fila (recortada por la región).
		sy := reg.y + ly
		if sy < 0 || sy >= g.h {
			continue
		}
		// Recorre los spans de la línea acumulando la columna lógica del Block;
		// solo se copian las celdas que caen en la ventana visible [0, reg.w).
		col := 0 // columna lógica dentro del Block (en celdas)
		for _, sp := range b.lines[by] {
			for _, gr := range graphemesOf(sp.text) {
				gw := grWidth(gr)
				// Columna de pantalla local a la región tras aplicar el offset.
				lx := col - ox
				col += gw
				if lx < 0 || lx >= reg.w {
					continue
				}
				sx := reg.x + lx
				cell := g.at(sx, sy)
				if cell == nil {
					continue
				}
				cell.r = gr
				cell.st = sp.st
				cell.w = gw
				// La segunda celda de un grapheme ancho queda como continuación.
				if gw == 2 {
					if c2 := g.at(sx+1, sy); c2 != nil {
						c2.r = ""
						c2.st = sp.st
						c2.w = 0
					}
				}
			}
		}
	}
}

// fill rellena la región con un estilo (el prototipo de `Region:fill`, §9.1):
// pone todas sus celdas a espacio con ese estilo. Lo usan los workloads para
// pintar un fondo antes de blittear (mide también el coste de un fill por frame).
func (g *sgrid) fill(reg sregion, st *style) {
	for ly := 0; ly < reg.h; ly++ {
		sy := reg.y + ly
		for lx := 0; lx < reg.w; lx++ {
			if cell := g.at(reg.x+lx, sy); cell != nil {
				cell.r = ""
				cell.st = st
				cell.w = 1
			}
		}
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

// composer es el compositor del spike: mantiene el back buffer (el frame que se
// está componiendo) y el front buffer (el último frame ya "pintado", contra el
// que se difa). Coalescing: `frame()` no emite nada si el back buffer es idéntico
// al front (ningún cambio = ningún byte, como el ADR-007 promete "repinta por
// eventos"). El front se actualiza solo cuando hay diff que emitir.
type composer struct {
	w, h    int
	back    *sgrid          // el frame en composición (se limpia y se blittea cada vez)
	front   *sgrid          // el último frame emitido (base del diff)
	enc     strings.Builder // buffer ANSI en memoria (NO un TTY: entorno headless)
	frames  int             // frames con diff no vacío (para diagnóstico)
	skipped int             // frames coalescidos (diff vacío, no se emitió nada)
}

// newComposer crea un compositor de tamaño w×h con ambos buffers en blanco.
func newComposer(w, h int) *composer {
	return &composer{w: w, h: h, back: newGrid(w, h), front: newGrid(w, h)}
}

// beginFrame limpia el back buffer para empezar a componer un frame nuevo. El
// patrón de uso es: beginFrame → fill/blit* → frame (diff+encode).
func (c *composer) beginFrame() {
	c.back.clear()
}

// frame difa el back buffer contra el front, codifica las celdas que cambiaron a
// ANSI en el buffer en memoria, y promueve el back a front. Devuelve el número de
// **celdas** que cambiaron (0 = frame coalescido, nada que emitir). Es la pieza
// que el veto mide: compose ya ocurrió (fill/blit), aquí va diff + encode.
//
// LA CODIFICACIÓN ANSI (mínima, suficiente para medir el coste real). Recorre la
// rejilla por filas; arranca un "run" allí donde una celda difiere del front y lo
// extiende mientras siga difiriendo, emitiendo un único reposicionamiento de
// cursor (`ESC[y;xH`) por run y un SGR (`ESC[...m`) solo cuando el estilo cambia
// respecto a la celda anterior emitida (minimiza bytes, como un compositor real).
// Esto NO se manda a un terminal (headless), pero el coste de construir la cadena
// —que es lo que el camino caliente paga— es el mismo.
func (c *composer) frame() int {
	c.enc.Reset()
	changed := 0
	var lastSt *style
	stDirty := true // forzar SGR al inicio de cada run

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
			// para todo el run (coordenadas ANSI 1-based).
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
					// Continuación de un grapheme ancho ya emitido: no se pinta sola.
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

// encoded devuelve los bytes ANSI del último `frame()` (lo que se enviaría al
// terminal). En el entorno headless es la salida observable de la tubería; los
// tests comprueban su forma y los benchmarks su tamaño.
func (c *composer) encoded() string { return c.enc.String() }

// cellEqual compara dos celdas para el diff: iguales si coinciden grapheme,
// anchura y estilo. Una celda que no cambió respecto al front no se reemite
// (damage tracking).
func cellEqual(a, b *scell) bool {
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

// writeSGR emite la secuencia SGR (`ESC[...m`) para un estilo. Un estilo nil
// emite el reset (`ESC[0m`): vuelve al fondo. Los colores son literales (§9.2):
// un "#rrggbb" se emite como truecolor (`38;2;r;g;b`), un índice 0-255 como
// `38;5;n`. Es la degradación mínima del spike; el compositor real (S29) la afina
// con `caps().colors`, pero el coste de construir la cadena es del mismo orden.
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

// writeColor emite el tramo SGR de un color literal (S29 lo degradará con caps).
// "#rrggbb" → truecolor (`;38;2;r;g;b` o `;48;...` para fondo); un índice → 256
// colores (`;38;5;n`). Un color mal formado se ignora (no debería llegar: el
// Block ya guarda colores normalizados, ui.go normalizeColor).
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
