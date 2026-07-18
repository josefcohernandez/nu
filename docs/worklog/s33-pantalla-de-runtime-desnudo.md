---
title: "pantalla de runtime desnudo (api.md §14, G21; cierra Fase 6, CP-7 manual)"
type: "sesion"
id: "S33"
phase: 6
status: "cerrada"
---
# S33 — pantalla de runtime desnudo (api.md §14, G21; cierra Fase 6, CP-7 manual)

Cuando enu arranca con un TTY interactivo y NINGÚN plugin activo, el kernel pinta —ANTES
de correr Lua de producto— una pantalla FIJA hecha solo de sus capacidades: versión +
nivel de API (`enu.version`), rutas de config y de plugins (`config.dir`, `pluginDirs`),
catálogo de extensiones embebidas DISPONIBLES (`embeddedNames`, embed.go) y las acciones
(activar el conjunto oficial / activar sueltas / salir). Render FIJO (Block sobre el
compositor de S29), pre-Lua, sin widgets ni lógica de producto (filosofía §2: el kernel
habla de lo suyo). NO amplía `api.md` (G21 ya estaba en §14; APILevel sigue en 1); ni
una función de superficie Lua nueva. Sin hallazgos `G##`. Todo en `bare_screen.go` +
`bare_screen_test.go`; cableado mínimo en `main.go`.

## Condición y dónde se cablea

La pantalla se muestra SSI `rt.uiActive` (TTY interactivo, o `WithForceUI` en test) Y no
hay plugins activos (`loader.hasActivePlugins`: `len(enabled) > 0` o algún subdir con
`plugin.toml` en los dirs de plugins —comprobación LIGERA, sin materializar embebidas ni
validar el grafo, que es trabajo del `Boot` real—). Decidí cablearlo en **`main`**, no
dentro de `Boot`: `main` (sin `-e`) consulta `rt.BareScreenActive()` y, si procede,
pinta y vuelca las líneas; si no, sigue el arranque canónico de siempre. Así NINGÚN test
de S01–S32 que llama `rt.Boot()` directamente cambia de comportamiento (Boot sigue
cargando plugins + init del usuario + `core:ready`): la pantalla es una decisión del
binario, que es donde vive el TTY. Sin TTY (`enu` sin `-e` en CI) → `BareScreenActive`
es false → se imprime el uso (arranca desnudo), confirmado con el binario.

## Acciones: activar → escribir enu.toml → continuar Boot (sin red)

`activateAndBoot(names)` escribe `names` en `plugins.enabled` de `config.dir()/enu.toml`,
relee la config en el loader, resetea `booted` y llama a **`rt.Boot()`** (no `ldr.Boot()`,
para armar también el painter del compositor: tras activar, la UI de las extensiones debe
repintarse). `ActivateOfficial()` = `activateAndBoot(embeddedNames())`; activar suelta =
`activateAndBoot([]string{"repl"})`. Sin red: las embebidas salen del binario (ADR-010,
reusa `extractEmbedded`/`discover` de S12). La elección con el TECLADO la cablea el driver
de TTY (S33+/CP-7 manual); la lógica queda invocable por una vía interna testeable.

## Escritura de enu.toml: preservar el resto del fichero, atómica

`writeEnabledPlugins` lee el `enu.toml` existente a un **`map[string]any`** genérico (NO a
`runtimeConfig`, que perdería las claves que el core ignora por forward-compat), fija
`plugins.enabled` conservando el resto de `[plugins]` y de las demás secciones/claves
desconocidas, y reescribe TODO con BurntSushi de forma **atómica** reusando `writeAtomic`
de S14 (temporal en el mismo dir + `rename`: no deja un `enu.toml` a medias). Un `enu.toml`
MAL FORMADO **no se sobrescribe a ciegas** (perdería config del usuario): devuelve `EINVAL`
accionable y deja el fichero intacto. Un fichero ausente se crea (primer arranque); el
`config.dir` se crea si falta.

## CP-7 (cierra Fase 6) — MANUAL con TTY, NO ejecutable en CI headless

CP-7 es una prueba de humo **manual con TTY** (arrancar sin plugins → ver la pantalla;
activar el conjunto oficial; un plugin pinta markdown en streaming y responde a un keymap;
redimensionar; pegar imagen → path). En este entorno **HEADLESS no hay TTY**, así que NO
se pudo ejecutar la parte interactiva (limitación del entorno). Lo que SÍ queda cubierto
por tests automáticos (`bare_screen_test.go`):

- **Condición** TTY+sin-plugins (con UI y sin plugins → activa; sin UI → no; con embebida
  activada → no; con plugin de disco → no).
- **Contenido / render FIJO a buffer**: el modelo y la **rejilla del compositor** (`back`)
  contienen versión+API, rutas (config y dir de plugins), el catálogo de embebidas
  (`example`) y las acciones; el frame ANSI emitido no es vacío.
- **Activar conjunto oficial → enu.toml → Boot**: escribe `plugins.enabled` con el catálogo,
  y el Boot que continúa carga la embebida con `source="builtin"` y corre su init (sin red).
- **Activar suelta** (`example`): escribe solo esa.
- **Preservar config**: escribir `enabled` conserva `dirs`, `watchdog` y claves/secciones
  ajenas; el resultado es un `enu.toml` válido.
- **enu.toml mal formado** no se sobrescribe (EINVAL, fichero intacto).
- **No regresión**: `enu -e` headless sigue funcionando; `enu` sin `-e` sin TTY imprime el uso.

QUEDA PENDIENTE de un humano con TTY (CP-7 manual): la interacción de TECLADO para elegir
una acción, el streaming visible token a token, y el resize/paste VISIBLES. El render de la
pantalla, la condición y la cadena activar→enu.toml→Boot están automatizadas; el tablero
marca `[x] Fase 6` con esta nota.

`CGO_ENABLED=0 go build ./...` y `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde, sin data races
(no regresiona S01–S32).
