package runtime

// Versión del runtime y nivel de la API del core (§2). `APILevel` se incrementa
// con cada adición a la superficie sagrada (api.md §17); arrancó en 1 con la
// primera sesión que inyecta `enu`. Subió a 2 en S38 al añadir `enu.sys.pid()`
// (G32): la PRIMERA adición tras el congelado inicial — adición estricta, no
// rompe ninguna firma del nivel 1.
//
// Subió a 3 al añadir los frames binarios de `enu.ws` (G52/A-38): `opts.binary` en
// `Ws:send` y el segundo retorno `binary` de `Ws:recv` — adición estricta, no rompe
// ninguna firma del nivel 2 (todo llamante existente ignora lo nuevo).
//
// Subió a 4 al añadir el control de redirects de `enu.http` (G54): `opts.max_redirects`
// en `request`/`stream` (default 10, `0` = no seguir) y el recorte de las cabeceras del
// llamante en cada salto cross-host — adición estricta, no rompe ninguna firma del
// nivel 3 (quien no pase `max_redirects` conserva la política implícita de default 10).
//
// Subió a 5 con el modo de creación de `enu.fs.write` (G57): `opts.mode` (chmod
// explícito no recortado por el umask) — adición estricta, no rompe ninguna firma del
// nivel 4 (quien no pase `mode` conserva el default `fsFilePerm` recortado por umask).
//
// El catálogo `enu.*` lo monta el backend wasm (registerWasmCatalog en runtime.go
// + los preludios de internal/vmwasm); estas constantes las inyecta el preludio
// vía `Pool.SetAPIVersion`/`Pool.SetVersion` (buildWasmState).
const (
	VersionMajor = 0
	VersionMinor = 2
	VersionPatch = 0
	APILevel     = 5
)
