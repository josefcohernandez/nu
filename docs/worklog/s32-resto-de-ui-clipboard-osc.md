# S32 — resto de `enu.ui`: clipboard OSC 52 + eventos `ui:*` + gating headless G20 (api.md §9.2, §9, §4, §2)

## GATING HEADLESS (G20, la decisión central)

§9/G20: sin TTY interactivo (`enu -e`, CI, salida redirigida) el módulo `enu.ui`
**directamente NO EXISTE**, y la detección es `enu.has("ui")` (nunca probar-y-capturar),
el mismo modelo que las caps de los workers ("la superficie no concedida no está").
Lo implementé así:

- **`registerUI` se llama solo si `rt.uiActive`** (en `registerNu`). En headless ni se
  cuelga `enu.ui` del global ni se construye el compositor (`rt.ui = nil` vía
  `maybeUIState`); `armPainter`/`stopPainter`/`Close` ya toleraban `rt.ui == nil`.
- **`uiActive` lo decide `New`:** `WithForceUI(active)` manda (precedencia, `forceUISet`);
  sin ella, `detectTTY()`. Así el binario `enu` (que llama `runtime.New()` sin Options)
  aplica el gating real, y los tests fuerzan la UI.
- **`detectTTY()` exige stdout Y stdin TTY** (`golang.org/x/term.IsTerminal`): una UI a
  pantalla completa escribe el render (stdout) y lee teclas (stdin); si cualquiera está
  redirigida no hay superficie viable. `x/term` es puro-Go (sin CGO, ADR-001).
- **`enu.has` pasa a per-runtime** (`rt.caps()`), no un mapa global: `"ui"` depende del
  runtime concreto (uiActive). `"ui.images"`/`"net.tcp"` siguen false (deny-by-default;
  el protocolo de imágenes lo negociará el driver de S33+).

## Activación forzada para test (NO romper S22–S31)

Los tests corren headless (sin TTY), así que con el gating real `enu.ui` no existiría y
toda la suite de UI de S22–S31 (block/region/input) fallaría. La vía: la Option
**`WithForceUI(true)`**, que el arnés base (`newHarness`) y los harness de UI
(`newHarnessUI`/`newHarnessBudget`) activan. Ajusté también las pocas pruebas que
construyen `New(...)` a mano (`ui_test.go`) para añadirla. Ningún test se borró: el
gating real (por TTY) sigue cubierto por `TestGatingHeadlessNoUI` (que construye el
runtime con `WithForceUI(false)` para observar el comportamiento headless).

## Clipboard OSC 52 (`osc52.go`): driver vs lógica probada

§9.2: `clipboard_set`/`clipboard_get` "vía OSC 52 cuando el terminal lo soporta".

- **`set` NO ⏸**, **`get` ⏸**: `set` escribe unos bytes y el terminal no responde; `get`
  envía la consulta y **espera** la respuesta (de ahí ⏸, sobre el puente `suspend` de
  S04: suelta el token, lee en la goroutine de fondo que jamás toca Lua).
- **Por qué OSC 52 y no un portapapeles nativo:** "cero dependency hell" (ADR-001)
  descarta enlazar X11/Wayland/AppKit; OSC 52 es in-band, funciona por SSH y no añade
  dependencias de sistema. Su límite (el terminal debe soportar la lectura, muchos la
  desactivan) se modela honestamente: `get` devuelve `nil`, no un portapapeles vacío.
- **DRIVER vs lógica probada (como S31):** la salida es `uiState.clipWriter`
  (`os.Stdout` en producción, un buffer en test) y la respuesta llega de
  `uiState.clipReader` (el flujo del TTY que provee el DRIVER de S33+; nil en headless
  → `get` resuelve a `nil`). La lógica propia y arriesgada —codificar `set` (base64) y
  **parsear** la respuesta (`parseOSC52Reply`: terminador BEL/ST, selector ignorado,
  ruido tolerado, base64/vacío/`?`-rebotado→nil)— se blinda por unidad con bytes
  sintéticos (`osc52_test.go`). El ida y vuelta real con un TTY vivo es del driver.
- **`set` no lanza ante un fallo de escritura al TTY:** copiar al portapapeles es
  accesorio; un error va al log best-effort, no tumba al llamante.

## Eventos `ui:*` (`ui_events.go`): emisión cableada, fuente en el driver

§4: el core emite `ui:resize`/`ui:focus`/`ui:suspend`/`ui:resume`; §9.1: cambios de
tamaño → `ui:resize`. La FUENTE real (SIGWINCH, secuencias de foco, SIGTSTP) es el
DRIVER de TTY (S33+, CP-7 manual). S32 cabla la EMISIÓN por `enu.events` y deja las vías:

- **`resizeUI(w,h)`** redimensiona el compositor (recorta regiones, G1) y emite
  `ui:resize {w,h}` —**solo si el tamaño cambió** (no un evento espurio)—.
- **`emitUIFocus(b)`** → `ui:focus {focused}`; **`emitUISuspend`/`emitUIResume`** →
  `ui:suspend`/`ui:resume` (sin payload).
- `ui:` es namespace reservado al core (§4). La emisión presupone el token (estado
  principal, ADR-008): el driver encolará el evento del SO al loop, como el painter
  toma el token para pintar. Todas son no-op si no hay UI (`rt.ui == nil`).

## Dependencia: `golang.org/x/term`

`x/term v0.13.0` (directa, puro-Go). La última (v0.44.0) exige go >= 1.25; la pineé a
v0.13.0 para no bumpear el toolchain del repo (go 1.24.7) y reusar el x/sys v0.13.0 ya
presente. `go mod tidy` coherente; `CGO_ENABLED=0 go build ./...` verde.

## Sin ampliar la API

NO se tocó `api.md` ni `enu.version.api` (APILevel sigue en 1): las firmas
`clipboard_set`/`clipboard_get`, los eventos `ui:*` y el gating ya estaban
especificados. `enu.has` per-runtime es implementación, no cambia la firma `enu.has(cap)
-> boolean`. Sin hallazgos `G##`. `CGO_ENABLED=1 go test -race -timeout 120s -count=2
./internal/...` verde, sin data races.
