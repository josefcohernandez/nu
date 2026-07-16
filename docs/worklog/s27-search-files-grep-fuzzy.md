# S27 — `enu.search` (files/grep/fuzzy) (api.md §11, 🔒; cierra Fase 5 — CP-6)

Búsqueda a escala de repo: las tres primitivas de §11 en `search.go`. **[W]**
(§16; hoy estado principal, workers S34). **Ni una función pública de más**
(§11 ya estaba en api.md). **Sin hallazgos:** el puente ⏸ de S04, el patrón del
iterador del `Stream` de S20 y las librerías ya presentes (go-gitignore de S15,
`regexp`/RE2 de S26) bastaron.

## Sin dependencia nueva (`go.mod`/`go.sum` intactos)

`files`/`grep` reusan **go-gitignore** (S15) y **`regexp`/RE2** (S26). Para
`fuzzy` se evaluó añadir `github.com/sahilm/fuzzy`; **descartado**: el scorer de
un picker es un algoritmo conocido de ~50 líneas (subsecuencia con bonus, estilo
fzf simplificado), fácil de blindar y de hacer determinista; añadir una
dependencia por eso contradice "cero dependency hell" (filosofía §6) sin ganancia
(no es un parser traicionero como YAML, donde la lib sí se justificó en S18).

## `files` — recorrido y filtrado (`walkFiles`)

`filepath.WalkDir` desde `root`, en la goroutine de fondo del puente ⏸. Filtros,
en este orden por entrada: (1) `.git/` podado SIEMPRE (`SkipDir`); (2) ocultos
(nombre con `.` inicial) — sin `hidden` se podan los dirs y se omiten los ficheros,
con `hidden=true` se incluyen (salvo `.git/`); (3) `.gitignore` (cargado de
`root`, comprobado sobre la ruta RELATIVA a `root` como hace git) — lo ignorado se
poda (dir) u omite (fichero); (4) `glob` por nombre BASE del fichero. `max` corta
el `WalkDir` con un centinela (`errFilesMaxReached`) — no hay otra vía de parada
temprana en `WalkDir`. `root` inexistente → `ENOENT` (se comprueba con `os.Stat`
antes del walk, porque `WalkDir` no falla limpio si la raíz no existe).
**Decisión:** `gitignore` es SIEMPRE activo en `files` (no es opt-out como en
`watch`): un picker de ficheros sobre `node_modules/` es ruido puro; §11 no expone
una perilla para desactivarlo, así que no se inventa. El `.gitignore` mismo es un
fichero más (con `hidden=true` aparece; sus patrones no lo nombran, no se
autoexcluye).

## `grep` — el iterador paralelo (lo delicado, gemelo del `Stream` de S20)

El patrón es el del iterador de stream (S20) generalizado a N productores:

- **Enumeración primero, bajo el puente ⏸:** `walkFiles` (gitignore+glob) lista
  los ficheros candidatos en la goroutine de fondo, de modo que `root`
  inexistente → `ENOENT` al CREAR el iterador, no a mitad del consumo.
- **Pool acotado:** `grepWorkers` = `runtime.NumCPU()` acotado por el nº de
  ficheros (no lanzar 8 goroutines para 3 ficheros), suelo 1. Cada worker toma
  ficheros de un canal de trabajo y casa línea a línea (`bufio.Scanner`, buffer
  subido a 1 MiB para no abortar en líneas largas).
- **Canal SIN buffer (`results`):** backpressure natural — un worker bloquea al
  empujar un match hasta que el `next` lo saca. El `next` lee `<-results` dentro
  del `work` del puente ⏸ (fuera del token, JAMÁS toca Lua); la cuenta
  `emitted`/`max` y `it.close()` se tocan SOLO en la `deliverFn` (bajo el token).
- **EOF:** una goroutine cerradora hace `wg.Wait()` (todos los workers
  terminaron) y `close(results)`; el `next` distingue "fin" (`ok=false`) de
  "siguiente match".
- **Cancelación (S08):** al crear el iterador se registra un `enu.task.cleanup`
  (`registerGrepCleanup`, manipula la pila LIFO de la task bajo el token) que
  cierra el `context`. Cancelar/terminar la task → repartidor y workers paran
  (`ctx.Done`) y la cerradora cierra `results`, desbloqueando un `next` colgado.
  Sin esto, un `<-results` colgado tras un abort dejaría una goroutine de la task
  esperando para siempre. Red de seguridad `Runtime.Close`→`stopAllGreps`
  (rastreo `scheduler.greps`, gemelo de `streams`). `grepIter.close` idempotente
  (`closeOnce`).
- **`ranges` coherentes con S26:** byte 1-based inclusive (`FindAllStringIndex`
  +1 en el inicio, fin tal cual), de modo que `line:sub(start,end)` reconstruye
  el match — el mismo convenio que `enu.re.find_all`.
- **`opts.root` OBLIGATORIO** (¿dónde buscar?); `case` por string
  `"sensitive"|"insensitive"` (insensitive antepone `(?i)` a la regex). Ficheros
  ilegibles/binarios se saltan en silencio (como `grep -r`).

**Orden de entrega:** NO determinista entre ficheros (varios workers compiten por
el canal), pero dentro de un fichero las líneas salen en orden (un fichero lo
procesa un solo worker, de arriba abajo). §11 solo promete "según llegan", no un
orden global — el test de paralelismo verifica el TOTAL y la cuenta por fichero,
no el orden.

## `fuzzy` — síncrono, scorer propio, orden estable

**NO ⏸** (la primitiva caliente del picker, §11): CPU puro sobre datos en memoria,
como `enu.re`/codecs — no usa el puente `suspend`. `fuzzyScore`: subsecuencia
case-insensitive con base por carácter + bonus de contigüidad + bonus de inicio de
palabra (tras separador `/\_-. ` o cambio minúscula→mayúscula camelCase) + bonus
de primer carácter. `query` vacío casa todo (score 0: picker recién abierto).

**Estabilidad (inventario 🔒):** `sort.SliceStable` por score DESC comparando
**SOLO por score** (NUNCA por índice). Comparar por índice rompería la
estabilidad frente a un orden de entrada arbitrario; `SliceStable` ya conserva el
orden de entrada en los empates, que es justo lo que el contrato pide (un picker
con empates muestra los candidatos en su orden natural, no barajados). El test
estrella pasa 4 candidatos idénticos y exige el orden 1,2,3,4.

## Tests y verificación

`search_test.go` (herméticos en `t.TempDir()`): files (gitignore/hidden/glob/max/
errores), fuzzy (orden/estabilidad/scorer unitario/vacío/max/EINVAL), grep
(forma+ranges, glob/case/max, paralelo completo 50×3=150 sin pérdida/duplicado,
early-stop sin fuga de goroutines). `cp6_test.go` (CP-6, cierra Fase 5): markdown
+highlight+diff+grep+fuzzy+files juntos sobre un repo en disco, todo inspeccionado
sin pintar pantalla.

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S26). Binario `enu -e` confirma e2e. **APILevel sigue en 1.**

**CP-6 verde → `[x] Fase 5`.** Puntero ▶ avanza a **S28** (SPIKE ADR-007, abre
Fase 6).
