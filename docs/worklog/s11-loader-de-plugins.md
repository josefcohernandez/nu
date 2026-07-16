---
title: "loader de plugins (api.md §14)"
type: "sesion"
id: "S11"
phase: 2
status: "cerrada"
---
# S11 — loader de plugins (api.md §14)

Superficie nueva exacta: `enu.plugin.current()`, `enu.plugin.list()`,
`enu.config.dir()` [W] y `enu.config.data_dir()` [W] (§14). El arranque canónico
(carga de plugins, `init.lua` del usuario, `core:ready`) lo dispara `Runtime.Boot`,
método Go interno, no superficie Lua. APILevel sigue en 1 (api.md ya describía §14;
no es una adición post-congelado). Sin hallazgo: el modelo de S04–S10 (token +
estado principal + bus de eventos) bastó.

## Dependencia TOML añadida

`github.com/BurntSushi/toml` (resuelto a v1.6.0 por `go get`; TOML **puro-Go**,
coherente con `CGO_ENABLED=0`/ADR-001). Se usa para parsear `plugin.toml` (campos
`name`, `version`, `requires?`) **internamente en el loader**: NO es la API Lua
`enu.toml` (eso es S18, que reusará esta misma librería para `enu.toml.encode/decode`).
`go mod tidy` deja go.mod/go.sum coherentes.

## Modelo del loader

- **Descubrimiento**: por cada directorio pasado con `WithPluginDir`, cada
  subdirectorio con `plugin.toml` es un plugin. La unicidad de nombre se valida en
  el descubrimiento (un `map[name]`); colisión = `EINVAL` accionable que nombra el
  plugin y **ambas rutas**. El nombre es la identidad (§14), lo que deja libres de
  colisión los namespaces de eventos por convención (G26).
- **Orden topológico**: DFS post-orden sobre el grafo de `requires` (el dependido
  antes que el dependiente). Visita determinista (nodos y `requires` ordenados por
  nombre) para que el arranque sea reproducible. Dos errores accionables: ciclo
  (coloreado blanco/gris/negro; un re-encuentro de gris reconstruye el tramo del
  ciclo `a -> b -> a`) y dependencia ausente (`requires` que no corresponde a
  ningún plugin descubierto). La validación del grafo es **total antes** de correr
  un solo `init.lua`: un grafo roto no deja el sistema medio-cargado.
- **Arranque canónico** (`Boot`, bajo el token, en el estado principal — como un
  chunk de `-e`, no como una task): rutas de require → por cada plugin en orden
  topológico {empuja owner, corre `init.lua`, emite `core:plugin.loaded`} →
  `init.lua` del usuario (`config.dir()/init.lua`) **el último** → `core:ready`
  **una vez**. Un `init.lua` que lanza queda **aislado** (ADR-008): se loguea, se
  emite `core:plugin.error`, y los demás plugins + el usuario siguen cargando;
  `Boot` solo devuelve error por un **grafo** inválido (colisión/ciclo/ausente), no
  por un fallo de runtime de un init.

## Rutas de `require`

El baseline (S01, sandbox.go §1.2) dejó `package`/`require` **cerrados**. El loader
abre `OpenPackage` una sola vez en `setupRequirePaths` y fija `package.path` a
**solo** los `lua/` de los plugins (`<dir>/lua/?.lua` y `<dir>/lua/?/init.lua`).
Deliberadamente NO incluye el `./?.lua` que gopher-lua trae por defecto: `require`
es para módulos de plugins, no un agujero para cargar ficheros arbitrarios del cwd
(respeta el sandbox). `cpath` vacío (sin librerías C, CGO_ENABLED=0). El loader usa
`L.LoadFile` para ejecutar los `init.lua` (es el único autorizado a tocar el disco
así, §1.2); `dofile`/`loadfile` siguen deshabilitados como globales.

## Pila de owner para `enu.plugin.current` y el log

`rt.owner` (string, S03) se sustituyó por `rt.ownerStack []*pluginInfo` +
`rt.currentOwner()` (tope de la pila, o `"user"` si vacía). El loader empuja el
contexto del plugin antes de su `init.lua` y lo saca al terminar (defer). Así,
DURANTE el `init.lua` de un plugin, `enu.plugin.current()` y el owner del log son ese
plugin; fuera (chunk de `-e`, `init.lua` del usuario, handlers) son `"user"`. La
pila se muta **solo bajo el token** (el arranque es síncrono) y se lee solo desde
código Lua (que también exige el token): sin candado ni carrera (`-race` verde).
`current()` nunca es `nil`: fuera de plugin devuelve `{name="user", version="",
dir=config.dir}`. Limitación conocida (no del alcance de S11): una task spawneada
por el init de un plugin corre **después** de que el owner se haya sacado, así que
verá owner "user" — el etiquetado fiable de handles por dueño es trabajo de S13
(reload), que se construye sobre esta pila.

## Frontera con S12/S13

- **S12** (activación por `enu.toml` + embebidas `go:embed`): el campo
  `pluginInfo.Source` (`"user"`/`"builtin"`) y `pluginInfo.Enabled` están previstos
  pero en S11 son siempre `"user"`/`true`. `WithSliceBudget`/`WithDataDir`/
  `WithConfigDir`/`WithPluginDir` son los ganchos que S12 cableará a `enu.toml`. El
  gancho de activación (qué plugins se cargan) vive en `loader.discover`/`Boot` sin
  adelantar su lógica.
- **S13** (`enu.plugin.reload`): se apoya en `ownerStack`/`currentOwner` para
  etiquetar handles por dueño (G2); la recarga en sí (vaciar caché de require,
  `core:plugin.unload`, re-correr init) NO se implementa en S11.
