---
title: "Toolkit de widgets (árbol+dirty, slots, focus, themes G22) (arquitectura.md §kernel/nota ui)"
type: "sesion"
id: "S42"
phase: 8
status: "cerrada"
---
# S42 — Toolkit de widgets (árbol+dirty, slots, focus, themes G22) (arquitectura.md §kernel/nota ui)

Séptima extensión de la Fase 8. **Lua puro sobre la API congelada** (ADR-003 / ADR-012):
el core NO sabe lo que es un widget; el toolkit es una extensión oficial sin privilegio.
Plugin embebido `internal/runtime/embedded/toolkit/` (`plugin.toml` name="toolkit", sin
`requires`) con módulos `lua/toolkit/{init,theme,widget,layout,widgets,app}.lua`. Implementa
la nota de arquitectura.md §kernel sobre `ui` (el toolkit «retenida por dentro: árbol +
nodos sucios … aporta slots, focus, composición entre plugins y el sistema de themes») y,
junto a S43 que lo consume, cierra la cuestión abierta nº3 (la API pública del toolkit).

## El modelo (lo que arquitectura.md dejaba abierto, fijado aquí)

`arquitectura.md` nombra los ingredientes (árbol, nodos sucios, slots, focus, themes) pero
no el catálogo de widgets ni el modelo de layout exacto. Se implementa un **conjunto mínimo
coherente**, suficiente para el criterio de hecho de S42 y para que S43 (chat) construya su
anatomía (chat.md §1: columna transcript/input/statusline + capas modales).

- **Árbol retenido** (`toolkit.widget`): cada nodo conoce `parent`/`children`, su área local
  `(x,y,w,h)` —que le ASIGNA el layout del padre, una hoja no decide dónde va—, y
  `compose(w,h) -> Block` (lo único específico de cada tipo; el resto —árbol, dirty, focus—
  es común). `derive()` fabrica metatablas que heredan del Widget base para los tipos
  concretos sin duplicar maquinaria.

- **Dirty tracking** (decisión clave, el porqué es ADR-007: no recomponer todo cada frame).
  Cada nodo cachea su último `Block` (`_block`) y un flag `dirty`. `mark_dirty()` ensucia
  SOLO ese nodo (invalida su caché) y AVISA hacia arriba a la app (`_notify` →
  `app:_request_paint`), **sin ensuciar a los hermanos ni a los ancestros** (sus Blocks
  siguen válidos; lo que cambió es un descendiente que la app re-blittea). `render()`
  recompone únicamente si el nodo está sucio o si su TAMAÑO cambió respecto al caché. Sutileza
  importante: **mover sin redimensionar (solo `x/y`) NO recompone** —el contenido es el mismo,
  solo cambia dónde se blittea—; solo un cambio de `w/h` invalida el Block. Ese es el ahorro
  real: no RECOMPONER (medir texto, render markdown) que es lo caro; el blit es copia barata
  (api.md §9.1). Verificado instrumentando `compose` en el test (contar recomposiciones).

- **Slots/layout** (`toolkit.layout`): tres contenedores que NO pintan ellos mismos, COLOCAN
  a sus hijos repartiendo su área. `vbox`/`hbox` reparten un eje; un hijo declara cómo ocupa
  el eje principal con `flex` (>0: parte proporcional del sobrante) o tamaño fijo
  (`pref_h`/`pref_w`); un hijo sin flex ni tamaño fijo ocupa 0 (decisión explícita: quien no
  dice cuánto ocupa no acapara). El **último flexible** se queda el remanente del *slack*
  (espacio sobrante tras los fijos), no `main - pos` —el bug inicial: con `main - pos` un
  flexible intermedio robaba el hueco de los fijos posteriores; se corrigió a "slack restante
  del último flexible", que respeta a los fijos que vengan después—. `stack` superpone a todos
  los hijos en la misma área (orden de inserción = z lógico): la base de las capas modales.

- **Focus** (`toolkit.app`): la app raíz mantiene UN widget enfocado, recoge los focusables en
  PREORDEN (orden natural de tabulación), los cicla con `focus_next`/`focus_prev` (envuelve por
  los extremos) y enruta el input al ENFOCADO. `handle_key(ev)` entrega al `widget:on_key`; lo
  que el widget no consume, la app lo DEJA PASAR (devuelve false), respetando la pila del core
  (api.md §9.3: «quien no consume, deja pasar»), de modo que un keymap de capa superior puede
  recogerlo. `tab`/`shift+tab` mueven el foco por defecto. La app coloca el cursor REAL en el
  input enfocado con `Region:cursor`. Emite `toolkit:focus {app,widget}` al cambiar el foco —en
  el namespace del PLUGIN (`toolkit`), NO `ui:focus`: `ui:` es reserva del core (api.md §4), que
  ya emite su propio `ui:focus {focused}` con OTRA semántica (el foco del TERMINAL, ui_events.go);
  pisarlo rompería a sus suscriptores. El foco de WIDGET es vocabulario del toolkit (§9.3).

- **Themes (G22)** (`toolkit.theme`): EL punto de G22. El core solo entiende colores literales
  (`#rrggbb`/0-255); los nombres semánticos (`accent`/`error`/`dim`…) son vocabulario del
  theme, que los RESUELVE a literales antes de construir el Block/Style. `theme:color(name)`
  (literal→intacto; nombre→literal; desconocido→EINVAL accionable: un theme incompleto se nota,
  no degrada en silencio); `theme:style(spec)` convierte `fg`/`bg` semánticos a literales,
  copiando los atributos. `theme.new{colors}` VALIDA que la paleta sean literales (un theme
  que mapeara "accent" a otro nombre fallaría más tarde dentro de `enu.ui.block`; validarlo al
  construir lo ancla al theme). Se replica `is_literal_color` en Lua (misma forma que
  `normalizeColor` del core) para distinguir "ya es literal" de "es un nombre a resolver" SIN
  intentar construir un Block y capturar el error. `theme.default` trae una paleta mínima con
  los nombres que chat.md §7 exige.

- **Sin colisión entre plugins** (criterio de hecho): cada `toolkit.app` es INDEPENDIENTE —su
  propia `Region` (z-order propio, api.md §9.1), su propio árbol, su propio foco, su propio
  `on_input` en la pila—. Dos plugins que montan cada uno su app componen en regiones distintas
  y el input fluye por la pila (quien consume gana; quien no, deja pasar al de abajo, que puede
  ser otra app). No hay estado global compartido entre apps: toda la retención vive en la
  instancia.

## Widgets base implementados

- **label**: una línea de texto estilizado (statusline, cabeceras). No focusable. `pref_h=1`
  por defecto (un label ocupa su renglón, no 0). Compone con `enu.ui.block` + `theme:style`.
- **text**: bloque multilínea de markdown (`enu.text.markdown`, streaming-safe) o word-wrap
  (`enu.text.wrap`), con SCROLL por viewport. Compone el Block COMPLETO; el scroll es un offset
  (`scroll_to` solo pide repintado, no ensucia: "scroll = re-blit con otro offset", api.md §9.1).
- **input**: editor de UNA línea, FOCUSABLE. `on_key` consume caracteres imprimibles, backspace,
  flechas, home/end y mantiene un caret (en bytes; el editor rico/multilínea es la extensión
  natural posterior, chat.md §3). enter/tab los DEJA PASAR (los gestiona la app: enviar/cambiar
  foco). `caret_col()` da la columna del cursor real.

## Decisión de implementación: recorte a la banda por región-viewport (scroll Y desborde)

El recorte del core es por REGIÓN, no por banda de widget (api.md §9.1: la región es el viewport,
`blit(0,-3,doc)` recorta el borde inicial pero clipa al borde de la REGIÓN). Como la región de la
app abarca el árbol ENTERO, blittear ahí el Block de un `text` lo recorta a la región, no a su
banda — y el `text` compone su Block COMPLETO (puede exceder su banda `h`, widgets.lua). Eso
SANGRA en DOS casos:
  * **scroll** (`scroll>0`, offset negativo): el `text` empezaría por una fila posterior,
    derramando sobre el widget de ARRIBA;
  * **desborde** (Block más alto que la banda, incluso con `scroll==0`): el `text` escribiría
    filas de más sobre el widget de ABAJO.
El modelo correcto del core es **una región por viewport**: por eso un `text` que está desplazado
**o** que desborda su banda obtiene su PROPIA región hija (creada al vuelo, `z = app.z + 1`,
propiedad de la app, destruida en `App:close()`), recortada a su banda; ahí el offset recorta
limpio por AMBOS extremos (G28) y nada sale de la banda. Los widgets que CABEN en su banda y no
están desplazados se blittean directos en la región de la app (vía rápida: ni región hija ni z
extra para un label/input/text corto). Si un `text` que desbordaba vuelve a caber, se OCULTA su
región-viewport (su contenido viejo, a `z+1`, no debe seguir tapando lo que pinta la app; se
re-muestra si vuelve a hacer falta). El gate es `oy ~= 0 or blk.height > node.h`. Es uso correcto
de la primitiva, no una ampliación del core. (La revisión de S42 detectó el sangrado del desborde
sin scroll: el gate original solo cubría `scroll~=0`.)

## Render síncrono en `_request_paint` (simplicidad + tests deterministas)

`_request_paint` pinta de forma SÍNCRONA (`paint()`). En una app viva el compositor del core ya
coalesce los blits y pinta como mucho cada ~30 ms (api.md §9), así que blittear de más es barato
(es copia, no re-render); la ganancia del dirty tracking es no RECOMPONER los Blocks (lo caro),
no evitar el blit. Pintar síncrono mantiene el código simple y deja a los tests ver el resultado
al instante (inspeccionando la rejilla del compositor tras `APP:paint()`).

## NO amplía api.md (corolario de completitud satisfecho)

El toolkit se construyó EXACTAMENTE sobre la API §9 (`enu.ui.region`/`blit`/`fill`/`clear`/
`cursor`/`size`, `enu.ui.block`/`Style`, `enu.ui.on_input`) + §10 (`enu.text.markdown`/`wrap`/
`truncate`/`width`) + §4 (`enu.events.emit`, con su evento propio `toolkit:focus` en el namespace
del plugin) + §2 (`enu.has`). Ni una función pública de más; APILevel sigue en 2. Sin hallazgos `G##`. Confirma que la API de UI de bajo nivel
(ADR-007) basta para un toolkit de alto nivel en Lua (ADR-012: el veto de ADR-007 no se ejecutó).

## Tests y resultado

`toolkit_test.go` (arnés de S12 con `WithForceUI(true)`+`WithUISize` —el toolkit es UI, en
headless `enu.ui` no existe, G20—; el Block es opaco a Lua, así que el CONTENIDO se inspecciona en
Go mirando la rejilla del compositor, igual que `compositor_test.go`, y la lógica del toolkit se
inspecciona desde Lua sobre sus propias tablas): carga+activa (builtin); theme G22; dirty
tracking; layout+focus entre dos widgets (criterio de hecho); sin colisión entre dos árboles
(criterio de hecho); input no consumido se deja pasar; reparto del vbox; scroll-viewport;
**desborde sin scroll** (un `text` más alto que su banda, con `scroll==0`, encima de un label: el
recorte a banda evita que derrame sobre el de abajo); `app`
sin `enu.ui`→EINVAL. `CGO_ENABLED=0 go build`/`go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde (~55 s; el toolkit
estable bajo `-race -count=4`). Nota: `TestMCPToolServerError` (S41) es un flake conocido bajo la
suite completa con `-race -count=2` (compila y lanza un proceso externo; bajo contención de
CPU/IO del conjunto su handshake JSON-RPC ocasionalmente excede el timing); pasa aislado y en
re-ejecuciones de la suite completa; es ORTOGONAL a S42 (el toolkit es Lua sobre `enu.ui`/`enu.text`,
no toca proc/red). No regresiona S01–S41.

## Nota de revisión de S42 (dos arreglos antes de aprobar)

La revisión encontró dos defectos, ambos arreglados (el commit de S42 se enmendó):

1. **[Bloqueante] Colisión de evento con el core.** `App:set_focus` emitía `ui:focus` con payload
   `{app,widget}`, pisando el `ui:focus {focused}` que el core emite para el foco del TERMINAL
   (ui_events.go, blindado en `gating_test.go`): cualquier suscriptor del `ui:focus` del core se
   rompía (su `ev.focused` desaparecía). `ui:` es reserva del core (api.md §4) y el foco de
   WIDGET es vocabulario del toolkit (§9.3), así que el evento se RENOMBRÓ a **`toolkit:focus`**
   (namespace = nombre del plugin). Se ajustó la prosa en `app.lua`, `init.lua`, la bitácora
   (implementacion.md) y esta entrada. El `ui:focus` del core sigue intacto (su test sigue verde,
   no depende del toolkit).
2. **[Menor] Sangrado del `text` sin scroll.** El `paint()` solo usaba la región-viewport
   recortada cuando `scroll~=0`; con `scroll==0` un `text` más alto que su banda se blitteaba
   directo sobre la región compartida y derramaba filas sobre el widget de ABAJO (el recorte del
   core es por REGIÓN, no por banda). Se amplió el gate a `oy ~= 0 or blk.height > node.h`: todo
   `text` que desborde su banda o esté desplazado pinta por su región-viewport recortada a la
   banda (un `text` que vuelve a caber oculta su viewport para no dejar restos). Test nuevo:
   `TestToolkitTextDesbordeSinScroll` (un `text` de 6 líneas en una banda de 3 sobre un label →
   el label NO se sobrescribe). Detalle en "recorte a la banda por región-viewport".

**Nota de proceso.** Tras código + tests + docs (puntero a S43, bitácora, esta entrada) +
build/vet/gofmt/race-count=2 verdes, se commitea y pushea SIN demora.
