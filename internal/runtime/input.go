package runtime

// Tipos de evento de input de `nu.ui` (api.md §9.3). La pila de input, el despacho
// y la resolución de secuencias/keymaps viven en el preludio Lua del backend wasm
// (internal/vmwasm/ui.go); del lado Go sólo sobreviven estos tipos, que el parser de
// bytes del terminal (`decodeInput`, tty.go) produce y el driver de TTY (driver.go)
// traduce al mapa crudo que la Instance espera (`inputEventToWasm`).

// modSet es el conjunto de modificadores de una tecla, como flags. Independiente del
// orden de escritura, comparable por valor.
type modSet struct {
	ctrl, alt, shift, meta bool
}

// inputEvent es un evento de entrada ya normalizado que el driver de TTY (o un test)
// construye desde los bytes del terminal (`decodeInput`, tty.go): `{type, key?, mods?,
// x?, y?, text?, path?}`. Para un paste de imagen (G30) el driver materializa los
// bytes a `nu.fs.tmpdir` y entrega el evento con `path` (y `pasteIsText=false`) en vez
// de `text` —los bytes binarios nunca cruzan a Lua—.
type inputEvent struct {
	typ  string // "key" | "mouse" | "paste" | "focus"
	key  string // tecla canónica (type=="key")
	mods modSet // modificadores (type=="key")
	x, y int    // coordenadas (type=="mouse")
	hasX bool   // el evento trae coordenadas (mouse)

	// Paste (§9.3, G30). `text` para un pegado de texto. Para una imagen, el driver
	// la materializa a `nu.fs.tmpdir` y entrega `path` con `pasteIsText=false`.
	text        string
	path        string
	pasteIsText bool
}
