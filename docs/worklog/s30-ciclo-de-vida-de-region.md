# S30 — ciclo de vida de `Region` (move/resize/raise/lower/show/hide/destroy/cursor) (api.md §9.1)

Sesión sin desviaciones de la espec: §9.1 bastó para las ocho firmas, que se
implementaron exactas sobre el `uiRegion`/`compositor` de S29 (no se amplió api.md,
`enu.version.api` sigue en 1; **sin hallazgos `G##`**). Decisiones de modelado
(donde §9.1 deja libertad) y su porqué:

- **raise/lower por reasignación de `z`, no por reordenar una lista.** `raise()`
  pone `z = max(z de las demás vivas)+1`; `lower()`, `min−1`. Alternativa
  descartada: mantener una lista ordenada y mover el elemento al final/principio.
  Se eligió la reasignación porque el criterio de apilado ya vive en un solo sitio
  —`regionLess` ordena por `(z, seq)` (S29)— y así un `composite` o un `blit`
  posteriores lo respetan sin estado adicional ni un segundo invariante que
  mantener. Conserva el orden relativo del resto: solo la región afectada salta al
  tope o al fondo. El `seq` de creación sigue desempatando z iguales (estabilidad).

- **resize conserva el contenido en la esquina superior izquierda.** §9.1 deja
  abierto qué pasa con el contenido al redimensionar; se decidió **conservar la
  intersección** (copiar la esquina (0,0) común al nuevo lienzo; lo que excede se
  descarta, lo nuevo es fondo) en vez de reiniciar el lienzo. Razón: coherencia con
  el modelo "la región es una ventana" de S29 —agrandar una ventana real no borra
  lo que ya mostraba—. `w/h<0` → `EINVAL`, igual que `enu.ui.region`.

- **hide conserva lienzo y coordenadas; show la devuelve tal cual.** `hide` no
  destruye nada: conmuta un flag `visible` que `composite` consulta para saltarse la
  región. Es el simétrico barato de `show`; ambos idempotentes. Una región oculta
  que llevaba el cursor lo **suelta** (no puede tener el cursor algo que no se ve).

- **destroy: `untrack` + `release`, idempotente, métodos posteriores fallan limpio.**
  `destroy` desregistra del registro de handles por dueño (S13, no fuga) y luego
  `release` (descuelga del compositor, suelta el cursor si era suyo, marca `alive=
  false`). Es **idempotente** (segunda llamada no-op). Tras destruir, los demás
  métodos lanzan `EINVAL` "ya destruida" vía `checkRegion` —error de uso accionable,
  no no-op silencioso—. Matiz: `destroy` valida el tipo a mano (no `checkRegion`)
  para que su propia idempotencia no lance sobre la región ya muerta; la asimetría
  es deliberada (una Region muerta es el caso esperado de la idempotencia; pasar algo
  que no es Region es un error de tipo que sí debe lanzar).

- **cursor: propiedad única, "la última gana", soltar en hide/destroy.** El
  compositor lleva la dueña del cursor (`cursorOwner` + coords LOCALES + flag
  `cursorOff`). `Region:cursor(x,y)` reclama el cursor y **desbanca a la dueña
  anterior** (su `cursor()` previo se pierde, como pide §9.1: "solo una región puede
  tenerlo; la última llamada gana"). `cursor(nil)`/`cursor()` lo oculta (la región
  sigue siendo dueña, apagada). `hide`/`destroy`/reload de la dueña sueltan el cursor
  (`dropCursorIf`); destruir/ocultar OTRA región no lo toca. El frame lo emite en
  `paint` (`encodeCursor`): posiciona+muestra (`ESC[y;xH`+`ESC[?25h`, coords de
  pantalla = local + origen, 1-based) o oculta (`ESC[?25l`) si no hay dueña, está
  apagado o **cae fuera de pantalla** (G1: el cursor nunca se posiciona fuera de
  límites). Es **damage-tracked** (`lastCursor`): un frame que no cambia el cursor no
  reemite su secuencia, de modo que un frame totalmente sin cambios sigue emitiendo
  0 bytes y NO rompe el coalescing de S29 (esto se validó re-ejecutando los tests de
  S29 `TestCoalescingSingleFrame`/`TestDiffEmitsOnlyChanged`).

- **Firma de `cursor`.** `cursor(nil)` o `cursor()` ocultan; `cursor(x, y)` exige
  los dos enteros (`L.CheckInt(2)`/`(3)`): la firma de §9.1 es `(x, y | nil)`, no
  `(x)`, así que un solo entero suelto es un error de uso.

- **Solo estado principal (ADR-008), síncrono (no ⏸, no [W]).** Como `blit`/`fill`/
  `clear` de S29: mutan el compositor bajo el token, sin candado propio. Verificado
  con `CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` (verde, sin
  data races, incluida la goroutine viva del painter `TestUIPainterLive`).
