package runtime

// SPIKE de ADR-007 (S28) — SHIM Lua mínimo del toolkit. El veto de ADR-007 no
// pregunta solo "¿cuánto cuesta la tubería compose+diff+encode en Go?" sino
// "¿cabe esa tubería MÁS el overhead de orquestarla DESDE LUA?". El toolkit que
// ADR-007 propone construir en Lua hará exactamente esto en el camino caliente:
// por cada token de streaming, Lua decide (re-render del markdown), llama a una
// primitiva Go (blit) y dispara el frame. Este shim expone el compositor del
// spike a un script Lua para medir ese overhead real (el cruce Go↔Lua por
// frame), que es lo que separa la opción "toolkit en Lua" de "toolkit en Go".
//
// NO ES API PÚBLICA. Se cuelga de un global privado `__spike` SOLO cuando un
// test/benchmark del spike lo pide (registerSpikeShim), nunca desde registerNu.
// La superficie `nu` de producción queda intacta (no amplía api.md §9). El shim
// reusa el `*block` de S22 (block.go) y el render markdown de S23
// (renderMarkdownBlocks) y el scorer fuzzy de S27 (fuzzyScore) por debajo.
//
// El "toolkit mínimo en Lua" que el spike tortura vive en el script Lua de los
// benchmarks (spike_bench_test.go): es un puñado de funciones Lua que orquestan
// `__spike.*`. Eso ES la opción que el veto evalúa.

import (
	"sort"

	lua "github.com/yuin/gopher-lua"
)

// spikeScored es un candidato puntuado por el fuzzy del picker (S27): su texto,
// su score y su índice de entrada (para la estabilidad de empates, §S27).
type spikeScored struct {
	text  string
	score int
	idx   int
}

// spikeComposerType / spikeRegionType son las metatablas de los handles opacos
// que el shim entrega a Lua (un compositor y sus regiones). Userdata opaco, como
// el Block (block.go): Lua los pasa de vuelta, no inspecciona su interior.
const (
	spikeComposerType = "nu.__spike.Composer"
	spikeRegionType   = "nu.__spike.Region"
)

// spikeRegionHandle empareja una región con el compositor al que pertenece, para
// que `Region:blit` sepa sobre qué rejilla estampar (en el prototipo, una región
// no vive sin su compositor).
type spikeRegionHandle struct {
	comp *composer
	reg  sregion
}

// registerSpikeShim cuelga el global `__spike` con la superficie mínima que un
// "toolkit en Lua" usaría en el camino caliente. Lo llaman SOLO los tests del
// spike. La superficie:
//
//	__spike.composer(w, h) -> Composer
//	__spike.markdown(src, width) -> Block          (reusa render de S23)
//	__spike.fuzzy_window(query, candidates, top) -> Block  (fuzzy S27 + Block de la ventana)
//	Composer:region(x, y, w, h) -> Region
//	Composer:begin()                               (limpia el back buffer)
//	Composer:frame() -> changed_cells              (diff + encode; nº de celdas cambiadas)
//	Composer:encoded_len() -> n                    (bytes ANSI del último frame)
//	Region:blit(ox, oy, block)                     (viewport copia, G28)
//	Region:fill()                                  (fondo)
func (rt *Runtime) registerSpikeShim() {
	L := rt.L

	// Metatabla del Composer.
	compMT := L.NewTypeMetatable(spikeComposerType)
	compIdx := L.NewTable()
	compIdx.RawSetString("region", L.NewFunction(rt.spikeComposerRegion))
	compIdx.RawSetString("begin", L.NewFunction(rt.spikeComposerBegin))
	compIdx.RawSetString("frame", L.NewFunction(rt.spikeComposerFrame))
	compIdx.RawSetString("encoded_len", L.NewFunction(rt.spikeComposerEncodedLen))
	L.SetField(compMT, "__index", compIdx)

	// Metatabla de la Region.
	regMT := L.NewTypeMetatable(spikeRegionType)
	regIdx := L.NewTable()
	regIdx.RawSetString("blit", L.NewFunction(rt.spikeRegionBlit))
	regIdx.RawSetString("fill", L.NewFunction(rt.spikeRegionFill))
	L.SetField(regMT, "__index", regIdx)

	spike := L.NewTable()
	spike.RawSetString("composer", L.NewFunction(rt.spikeNewComposer))
	spike.RawSetString("markdown", L.NewFunction(rt.spikeMarkdown))
	spike.RawSetString("fuzzy_window", L.NewFunction(rt.spikeFuzzyWindow))
	L.SetGlobal("__spike", spike)
}

// spikeNewComposer: `__spike.composer(w, h) -> Composer`.
func (rt *Runtime) spikeNewComposer(L *lua.LState) int {
	w := L.CheckInt(1)
	h := L.CheckInt(2)
	ud := L.NewUserData()
	ud.Value = newComposer(w, h)
	L.SetMetatable(ud, L.GetTypeMetatable(spikeComposerType))
	L.Push(ud)
	return 1
}

func checkComposer(L *lua.LState, idx int) *composer {
	ud := L.CheckUserData(idx)
	c, ok := ud.Value.(*composer)
	if !ok {
		raiseError(L, CodeEINVAL, "__spike: se esperaba un Composer", lua.LNil)
		return nil
	}
	return c
}

func checkSpikeRegion(L *lua.LState, idx int) *spikeRegionHandle {
	ud := L.CheckUserData(idx)
	r, ok := ud.Value.(*spikeRegionHandle)
	if !ok {
		raiseError(L, CodeEINVAL, "__spike: se esperaba una Region", lua.LNil)
		return nil
	}
	return r
}

// spikeComposerRegion: `Composer:region(x, y, w, h) -> Region`.
func (rt *Runtime) spikeComposerRegion(L *lua.LState) int {
	c := checkComposer(L, 1)
	reg := sregion{x: L.CheckInt(2), y: L.CheckInt(3), w: L.CheckInt(4), h: L.CheckInt(5)}
	ud := L.NewUserData()
	ud.Value = &spikeRegionHandle{comp: c, reg: reg}
	L.SetMetatable(ud, L.GetTypeMetatable(spikeRegionType))
	L.Push(ud)
	return 1
}

// spikeComposerBegin: `Composer:begin()` limpia el back buffer.
func (rt *Runtime) spikeComposerBegin(L *lua.LState) int {
	checkComposer(L, 1).beginFrame()
	return 0
}

// spikeComposerFrame: `Composer:frame() -> changed_cells` difa+codifica.
func (rt *Runtime) spikeComposerFrame(L *lua.LState) int {
	L.Push(lua.LNumber(checkComposer(L, 1).frame()))
	return 1
}

// spikeComposerEncodedLen: `Composer:encoded_len() -> n` bytes del último frame.
func (rt *Runtime) spikeComposerEncodedLen(L *lua.LState) int {
	L.Push(lua.LNumber(len(checkComposer(L, 1).encoded())))
	return 1
}

// spikeRegionBlit: `Region:blit(ox, oy, block)` (viewport copia, G28).
func (rt *Runtime) spikeRegionBlit(L *lua.LState) int {
	r := checkSpikeRegion(L, 1)
	ox := L.CheckInt(2)
	oy := L.CheckInt(3)
	b := checkBlock(L, 4)
	r.comp.back.blitBlock(r.reg, ox, oy, b)
	return 0
}

// spikeRegionFill: `Region:fill()` pinta el fondo de la región (sin estilo).
func (rt *Runtime) spikeRegionFill(L *lua.LState) int {
	r := checkSpikeRegion(L, 1)
	r.comp.back.fill(r.reg, nil)
	return 0
}

// spikeMarkdown: `__spike.markdown(src, width) -> Block`. Reusa el render de S23
// (renderMarkdownBlocks) tal cual lo hace nu.text.markdown, aplanando a un Block
// a pantalla completa. Es el coste "Go ejecuta" del re-render por token.
func (rt *Runtime) spikeMarkdown(L *lua.LState) int {
	src := L.CheckString(1)
	width := L.CheckInt(2)
	theme := defaultTheme()
	blocks := renderMarkdownBlocks(src, width, &theme)
	var lines [][]span
	for _, b := range blocks {
		lines = append(lines, b...)
	}
	if len(lines) == 0 {
		lines = [][]span{{}}
	}
	rt.pushBlock(L, newBlock(lines))
	return 1
}

// spikeFuzzyWindow: `__spike.fuzzy_window(query, candidates, top) -> Block`.
// Corre el scorer fuzzy de S27 (fuzzyScore) sobre los candidatos, ordena por
// score y construye un Block SOLO con la ventana visible (top N) —"Lua repinta lo
// visible", §veto de ADR-007: el filtrado es primitiva Go, el Block es la lista
// ya recortada—. Devuelve el Block listo para blittear.
func (rt *Runtime) spikeFuzzyWindow(L *lua.LState) int {
	query := L.CheckString(1)
	candTbl := L.CheckTable(2)
	top := L.CheckInt(3)

	n := candTbl.Len()
	matches := make([]spikeScored, 0, n)
	for i := 1; i <= n; i++ {
		cs, ok := candTbl.RawGetInt(i).(lua.LString)
		if !ok {
			raiseError(L, CodeEINVAL, "__spike.fuzzy_window: candidates debe ser array de strings", lua.LNil)
			return 0
		}
		if score, ok := fuzzyScore(query, string(cs)); ok {
			matches = append(matches, spikeScored{text: string(cs), score: score, idx: i})
		}
	}
	// Selección de los top N por score (orden estable por índice de entrada en
	// empates, como nu.search.fuzzy S27: SliceStable, comparando solo por score).
	// Para el spike basta un sort completo: el veto mide el coste del PEOR caso
	// (una query que casa muchos candidatos).
	sort.SliceStable(matches, func(a, b int) bool { return matches[a].score > matches[b].score })
	if len(matches) > top {
		matches = matches[:top]
	}
	lines := make([][]span, 0, len(matches))
	for _, m := range matches {
		lines = append(lines, []span{{text: m.text}})
	}
	if len(lines) == 0 {
		lines = [][]span{{}}
	}
	rt.pushBlock(L, newBlock(lines))
	return 1
}
