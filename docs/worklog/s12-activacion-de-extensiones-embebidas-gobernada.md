---
title: "activación de extensiones embebidas gobernada por `enu.toml` (api.md §14, ADR-010)"
type: "sesion"
id: "S12"
phase: 2
status: "cerrada"
---
# S12 — activación de extensiones embebidas gobernada por `enu.toml` (api.md §14, ADR-010)

S12 monta lo que ADR-010 exige: las extensiones oficiales se distribuyen DENTRO
del binario (`go:embed`) pero están **INACTIVAS por defecto** —enu instalado es un
runtime desnudo; el harness se activa, no se presupone—. La activación la gobierna
`config.dir()/enu.toml`.

## `enu.toml` es config del core, no la API Lua `enu.toml`

`config.dir()/enu.toml` (config_toml.go) configura al PROPIO runtime: no se confunde
con `enu.toml` el codec (API Lua de S18). Ambos reusan la misma librería TOML pura-Go
añadida en S11 (BurntSushi), pero son cosas distintas. Campos v1 leídos:
`plugins.enabled` (lista de activación), `plugins.dirs` (rutas extra), y
`watchdog.slice_budget_ms`. Claves desconocidas se ignoran (forward-compat, igual
que `plugin.toml`). Un `enu.toml` AUSENTE es lo normal del runtime desnudo: no activa
nada y no es error.

## Parseo en `New`, error de config APLAZADO a `Boot`

`enu.toml` se parsea en `New` (ahí ya se conoce `config.dir()` y sus valores deben
estar listos antes de `Boot`: el budget del watchdog va al scheduler que `New`
construye, y la lista de activación al loader). Pero `New` **no devuelve error** (su
firma es sagrada, §17). Decisión: un `enu.toml` mal formado se guarda en
`loader.configErr` y lo devuelve `Boot` (cuya firma sí lo permite), **antes** de
tocar plugin alguno. Así el error de config no deja el arranque a medias y llega a
`main`/tests con el camino que ya existía para los errores de grafo.

## `slice_budget_ms` con `*int` y precedencia de la Option

`slice_budget_ms` es `*int` (no `int`) para distinguir "no especificado" (nil → rige
el default 100 ms o `WithSliceBudget`) de "especificado como 0" (0 → desactiva el
watchdog explícitamente, semántica de S09). Precedencia: **Option explícita
`WithSliceBudget` > enu.toml > default**. Se añadió `config.sliceBudgetSet` (lo pone
`WithSliceBudget`) para que un test que fija su budget no lo pise la config de disco.
`plugins.dirs` simplemente se **suma** a las rutas de `WithPluginDir`.

## Infraestructura `go:embed` y materialización a disco

`embed.go` embebe el árbol `internal/runtime/embedded/` con `//go:embed embedded`
(el directorio DEBE existir para que `embed` compile; por eso la STUB). El loader de
S11 carga plugins de DIRECTORIOS en disco (lee `plugin.toml` con `os.ReadFile`, corre
`init.lua` con `L.LoadFile`, añade `lua/` a las rutas de require). Decisión: para que
una embebida se cargue **exactamente igual** que un plugin de usuario (§14), se
EXTRAE su árbol del `embed.FS` a `<data_dir>/embedded/<name>` (`extractEmbedded`,
idempotente: sobrescribe, así un binario nuevo gana sobre lo extraído antes) y se
reusa el loader de S11. La alternativa —enseñar al loader a leer de un `fs.FS`—
duplicaría el descubrimiento por ganancia nula (el árbol es diminuto). Sin red
(ADR-010): todo sale del binario.

## La extensión STUB `example`

Las extensiones oficiales reales (agente, chat, providers, MCP, toolkit) son la
Fase 8 y aún no existen, pero el mecanismo de embebido + gating se prueba ya en S12.
Para ello el árbol embebido contiene una sola extensión STUB, `embedded/example/`
(`plugin.toml` + `init.lua` que deja la huella `_example_embedded_cargada=true`).
Existe SOLO para los tests del gating; cuando lleguen las oficiales reales se añaden
bajo `embedded/` sin tocar el mecanismo.

## Gating ADR-010 en `loader.discover`

Tras descubrir los plugins de disco (S11, sin cambios), por cada nombre de
`plugins.enabled`:
- si ya está como plugin de disco → el **dir de usuario SUSTITUYE** a la embebida del
  mismo nombre (§14): no se materializa la embebida, gana el de usuario
  (`source="user"`), no coexisten;
- si es una embebida del catálogo → se extrae y carga con `source="builtin"`;
- si no es ni una cosa ni otra → `EINVAL` **accionable** que nombra la extensión y la
  **línea `plugins.enabled` de `enu.toml`** que lo arregla (§14).

Decisión de alcance: los plugins de disco (`WithPluginDir`/`plugins.dirs`) siguen
cargándose como en S11, sin gating. ADR-010 habla de las **extensiones oficiales
embebidas** inactivas por defecto; los plugins explícitos del usuario, por
definición ya elegidos, se cargan. Esto además evita regresionar los tests de S11
(que arrancan sin `enu.toml`). Los casos de prueba de S12 (gating de embebidas,
sustitución por nombre, errores accionables) quedan todos cubiertos.

## Sin superficie Lua nueva

S12 es config/loader interno. `enu.plugin.list()` ya reflejaba `source`/`enabled`
desde S11; una embebida activada sale `{source="builtin", enabled=true}`. No se tocó
`api.md` (§14/ADR-010 bastaron); APILevel sigue en 1. Sin hallazgos.

## Frontera explícita con S33 (G21)

La **pantalla de runtime desnudo** (render TTY del catálogo de embebidas +
activar/salir, sin red) es UI: NO se hizo en S12. Es S30/S33. S12 dejó listos el
catálogo (`embeddedNames`) y la activación por `enu.toml`, que es lo que esa pantalla
consumirá.
