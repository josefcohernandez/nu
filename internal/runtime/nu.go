package runtime

// Versión del runtime y nivel de la API del core (§2). `APILevel` se incrementa
// con cada adición a la superficie sagrada (api.md §17); arrancó en 1 con la
// primera sesión que inyecta `nu`. Subió a 2 en S38 al añadir `nu.sys.pid()`
// (G32): la PRIMERA adición tras el congelado inicial — adición estricta, no
// rompe ninguna firma del nivel 1.
//
// Subió a 3 al añadir los frames binarios de `nu.ws` (G52/A-38): `opts.binary` en
// `Ws:send` y el segundo retorno `binary` de `Ws:recv` — adición estricta, no rompe
// ninguna firma del nivel 2 (todo llamante existente ignora lo nuevo).
//
// El catálogo `nu.*` lo monta el backend wasm (registerWasmCatalog en runtime.go
// + los preludios de internal/vmwasm); estas constantes las inyecta el preludio
// vía `Pool.SetAPIVersion`/`Pool.SetVersion` (buildWasmState).
const (
	VersionMajor = 0
	VersionMinor = 1
	VersionPatch = 3
	APILevel     = 3
)
