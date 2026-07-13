# API del core de nu — especificación v1 (borrador)

Estado: **borrador para discusión**. Cuando se congele, esta superficie es la
"API sagrada" (ADR-003): solo crece por adición. Todo lo que no está aquí
(toolkit de widgets, agente, MCP, providers) es extensión y se versiona aparte.

Convenciones de esta especificación:

- Las firmas usan notación `nu.mod.fn(arg: tipo, opts?: tabla) -> tipo`.
- **⏸ suspende**: la función solo puede llamarse dentro de una task
  (corrutina); cede el control hasta completarse y devuelve el resultado
  directamente. Llamarla fuera de una task es un error.
- **[W]**: disponible dentro de workers. Sin marca: solo estado principal.

---

## 1. Convenciones transversales (ADR-009)

### 1.1 Namespace

Toda la API vive bajo el global `nu`. `require` queda reservado para módulos
de plugins y librerías Lua puras. Identificadores en inglés, `snake_case`.

### 1.2 Baseline del entorno Lua

Lua 5.4 (PUC-Lua, compilada a WASM y ejecutada sobre el runtime embebido —
ver [migracion-vm.md](archive/migracion-vm.md)). Disponibles: `string`, `table`,
`math`, `coroutine`, `utf8`, `pairs/ipairs/pcall/error/load/...`.
**Deshabilitados**: `io`, `os.execute`, `os.exit`, `os.remove`, `os.rename`,
`os.getenv`, `print` (redirigido a `nu.log.info`), `dofile`/`loadfile` fuera
del loader. Razón: todo IO debe pasar por las primitivas async del core; el
IO bloqueante de la stdlib congelaría el event loop.

`load(s)` sí queda disponible (compila un string EN MEMORIA, sin IO): es lo
que necesita un REPL de Lua sobre la API pública (ver la extensión `repl`).
Nota de migración 5.1→5.4: `loadstring` desaparece (`load` acepta el string
directamente), `unpack` pasa a `table.unpack`, `setfenv`/`getfenv` no existen
(el entorno es `_ENV`), y la entrada incompleta que un REPL detecta se marca
con `<eof>` en el mensaje de error (antes `at EOF`). El código de las
extensiones oficiales asume el baseline 5.4.

### 1.3 Modelo asíncrono

- El estado principal es single-threaded con event loop (ADR-004).
- Una **task** es una corrutina gestionada por el scheduler. Dentro de una
  task, las funciones ⏸ se escriben en estilo secuencial (await implícito).
- Los **handlers síncronos** (input, eventos) corren en el loop y no pueden
  llamar funciones ⏸; para hacer IO, lanzan una task con `nu.task.spawn`.
- **Watchdog**: cada *slice* de ejecución Lua continua (entre dos puntos de
  suspensión) tiene un presupuesto, por defecto 100 ms (configurable en
  `nu.toml`). Excederlo aborta la task y emite `core:plugin.misbehaved`.
- **Cancelación y abortos NO son capturables.** `Task:cancel()` y el
  watchdog abortan la task en su siguiente punto de suspensión (o slice)
  **desenrollando la pila sin pasar por `pcall`** — si fueran errores
  normales, cualquier `pcall` del ecosistema los capturaría y el programa
  seguiría como si nada. Para liberar recursos pase lo que pase, registra
  `nu.task.cleanup(fn)`. `ECANCELED` queda reservado para *observar* la
  cancelación (p. ej. en el resultado de `Task:await`), no para capturarla.

### 1.4 Errores

Las funciones del core **lanzan** (vía `error()`) tablas estructuradas:

```
{ code: string, message: string, detail?: any }
```

Códigos reservados v1: `ENOENT`, `EEXIST`, `EACCES`, `EIO`, `EHTTP`, `ENET`,
`ETIMEOUT`, `ECANCELED`, `EBUDGET`, `EINVAL`, `ECLOSED`. Se capturan con
`pcall` — con dos excepciones: `ECANCELED` y `EBUDGET` nombran los abortos
no capturables de §1.3 (cancelación y watchdog, respectivamente) y solo
sirven para *observarlos*, p. ej. en el resultado de `Task:await`. Las
extensiones acuñan sus propios códigos con la misma forma, fuera de esta
lista reservada (p. ej. `EPROVIDER`, [providers.md](providers.md) §3).
Razón frente al estilo `res, err`: los errores estructurados
componen mejor a través de capas de extensiones y nunca se ignoran en
silencio.

### 1.5 Unidades y tipos comunes

Tiempos en **milisegundos**. Rutas como strings UTF-8. El plazo de IO es
`opts.timeout_ms` (lanza `ETIMEOUT`) **en las firmas que lo listan** —
`nu.proc.run`, `nu.http.request`, `nu.http.stream`, `nu.ws.connect`—; el
resto del IO de v1 no acepta plazo (G47 — extenderlo a más firmas es una
adición futura compatible, no una promesa vigente). El valor frontera queda
definido donde el plazo existe: en `proc.run`, `0` (el default) significa
*sin límite* — un proceso local puede legítimamente no tener techo—; en
`http`/`ws` el plazo existe siempre (default 30 000 ms) y `0` es `EINVAL` —
no hay petición de red sin techo—. Los handles del core (Task, Region,
Proc...) son userdata opacos con métodos.

---

## 2. `nu` (raíz)

| Firma | Semántica |
|---|---|
| `nu.version -> {major, minor, patch, api: integer}` [W] | Versión del runtime y nivel de API. |
| `nu.has(cap: string) -> boolean` [W] | Detección de capacidades (`"ui"`, `"ui.images"`, `"net.tcp"`, ...) para extensiones portables. Cubre también módulos enteros: en headless `nu.ui` no existe (§9). |

---

## 3. `nu.task` — scheduler [W]

| Firma | Semántica |
|---|---|
| `nu.task.spawn(fn, ...) -> Task` | Lanza una task; los argumentos extra se pasan a `fn`. |
| `nu.task.sleep(ms)` ⏸ | Suspende la task actual. |
| `nu.task.all(fns: Task[]\|fn[]) -> any[]` ⏸ | Espera a todas; si una lanza, cancela el resto y relanza. Los resultados se devuelven **alineados con los inputs** (`out[i]` es el de `fns[i]`), nunca en orden de terminación (G27) — es lo que deja correlacionar resultado con entrada en un fan-out sin acarrear el índice a mano. |
| `nu.task.race(fns) -> (winner_index, result)` ⏸ | Primera en terminar gana; cancela el resto. |
| `nu.task.every(ms, fn) -> Timer` | Timer periódico (handler síncrono). `Timer:stop()`. |
| `nu.task.defer(fn)` | Ejecuta `fn` en el siguiente tick del loop. |
| `nu.task.future() -> Future` | Rendez-vous de un solo uso: `Future:set(v)` (síncrono, una sola vez; llamadas posteriores lanzan `EINVAL`) y `Future:await() -> v` ⏸ (varios pueden esperar; si ya está resuelto, retorna inmediato). Es la pieza para "una task espera un valor que otro código producirá" (diálogos, pickers, proxies) sin polling. |
| `Task:cancel()` | Cancelación cooperativa: aborta la task en su siguiente punto de suspensión (no capturable, §1.3); corren sus `cleanup`s. |
| `nu.task.cleanup(fn)` [W] | Registra un liberador (síncrono) en la pila LIFO de la task actual; corren todos al terminar — éxito, error o aborto. El `defer` de esta casa: procesos, regiones, handlers de input. |
| `Task:await() -> any` ⏸ | Espera el resultado de otra task. |

---

## 4. `nu.events` — bus de eventos

El core no sabe lo que es un agente: este bus genérico es donde las
extensiones definen sus propios hooks (p. ej. la extensión oficial de agente
emite `agent:tool.start`; sus hooks-middleware como `tool.pre` van por
registro propio, no por el bus — [agente.md](agente.md) §4). Convención de
nombres: `"namespace:evento"`, en **dos niveles** (G26). El core reserva
solo lo suyo — `core:` y `ui:`, las superficies que el propio kernel emite.
Cualquier otro namespace es de un plugin por convención (namespace = su
nombre); como el loader garantiza que el nombre de un plugin es único (§14),
dos extensiones no pueden colisionar. Las oficiales no tienen privilegio
aquí: `agent:` es el namespace del plugin `agent` igual que `mi-plugin:` es
el tuyo — el core no lo reserva (no sabe que `agent` existe, ADR-003).

| Firma | Semántica |
|---|---|
| `nu.events.on(name, fn) -> Sub` | Suscribe. Handlers síncronos, en orden de registro, cada uno bajo `pcall` (ADR-008). `Sub:cancel()`. |
| `nu.events.once(name, fn) -> Sub` | Una sola vez. |
| `nu.events.emit(name, payload?)` | Despacho síncrono en el estado principal. |

Semántica de despacho (G10): cada `emit` corre sobre la **foto** de
suscriptores tomada al emitir; cancelar una suscripción surte efecto
inmediato (si aún no te tocó, ya no corres); los suscritos durante un
despacho solo ven eventos futuros; los `emit` anidados **se encolan** y se
despachan al terminar el actual (anchura, no profundidad — sin recursión ni
desbordes; un ping-pong infinito entre plugins se vuelve un bucle plano que
el watchdog corta).

Eventos que emite el core: `core:ready`, `core:shutdown`,
`core:plugin.loaded`, `core:plugin.unload`, `core:plugin.error`,
`core:plugin.misbehaved`, `ui:resize`, `ui:focus`,
`ui:suspend`/`ui:resume`.

---

## 5. `nu.fs` — filesystem [W]

| Firma | Semántica |
|---|---|
| `nu.fs.read(path) -> string` ⏸ | Lee el fichero entero. |
| `nu.fs.write(path, data, opts?)` ⏸ / `nu.fs.append(path, data)` ⏸ | Escritura atómica (write vía fichero temporal + rename). `opts.exclusive = true` (G17): crea **solo si no existe**, en una única operación indivisible (`O_EXCL` — aquí no hay temporal+rename: rename sobreescribiría); si el fichero ya existe lanza `EEXIST`. Es la pieza para lockfiles ([sesiones.md](sesiones.md) §6). |
| `nu.fs.stat(path) -> {size, mtime_ms, is_dir, mode}?` ⏸ | `nil` si no existe (no lanza `ENOENT`). |
| `nu.fs.list(dir) -> {name, is_dir}[]` ⏸ | Sin recursión; para recursivo ver `nu.search.files`. |
| `nu.fs.mkdir(path)` ⏸ / `nu.fs.remove(path, opts?)` ⏸ / `nu.fs.rename(from, to)` ⏸ / `nu.fs.copy(from, to)` ⏸ | `remove` exige `opts.recursive=true` para directorios no vacíos. |
| `nu.fs.tmpdir() -> string` ⏸ | Directorio temporal propio de la sesión. |
| `nu.fs.cwd() -> string` [W] | Directorio de trabajo (inmutable durante la sesión; los subprocesos pueden recibir otro vía `opts.cwd`). |
| `nu.fs.watch(path, opts?, fn) -> Watcher` | `opts`: `recursive?`, `gitignore = true` (ignora lo ignorado por git: vigilar `node_modules/` es ruido), `debounce_ms = 50`. Entrega **en lotes**: `fn(events[])` con `{path, kind: "create"\|"modify"\|"remove"}` — un `git checkout` que toca miles de ficheros llega como un solo lote (G7). Handler síncrono. `Watcher:stop()`. Solo estado principal. |

---

## 6. `nu.proc` — subprocesos [W]

| Firma | Semántica |
|---|---|
| `nu.proc.run(argv: string[], opts?) -> {code, stdout, stderr}` ⏸ | Conveniencia con buffers. `opts`: `cwd`, `env`, `stdin`, `timeout_ms`. Sin shell implícita: `argv` es un array; quien quiera shell la invoca explícitamente. |
| `nu.proc.spawn(argv, opts?) -> Proc` | Control fino con streams. |
| `Proc:write(data)` ⏸ / `Proc:close_stdin()` | stdin en streaming. |
| `Proc:read_line(which: "stdout"\|"stderr") -> string?` ⏸ | `nil` en EOF. |
| `Proc:read(which, n?) -> string?` ⏸ | Lectura cruda. |
| `Proc:wait() -> {code}` ⏸ / `Proc:kill(signal?)` | `signal` por defecto TERM. |
| `nu.proc.alive(pid: integer) -> boolean` | ¿Hay un proceso vivo con ese `pid` en esta máquina? (G17). Informa de **existencia, no de identidad** — un pid reciclado da `true`. Para detectar locks huérfanos ([sesiones.md](sesiones.md) §6). |

Vida del proceso: la regla es matarlo explícitamente vía `nu.task.cleanup`
en quien lo crea; como red de seguridad, un `Proc` sin referencias acaba
matado por el GC (no determinista — no confíes en ello).

---

## 7. `nu.sys` — entorno y reloj [W]

| Firma | Semántica |
|---|---|
| `nu.sys.platform() -> "linux"\|"darwin"\|"windows"` | |
| `nu.sys.env(name) -> string?` / `nu.sys.setenv(name, value)` | `setenv` afecta solo a subprocesos futuros. |
| `nu.sys.now_ms() -> number` / `nu.sys.mono_ms() -> number` | Reloj de pared / monotónico. |
| `nu.sys.hostname() -> string` | Nombre de la máquina (G17; contenido de los locks de sesión, [sesiones.md](sesiones.md) §6). |
| `nu.sys.pid() -> integer` | Pid del proceso `nu` actual (consulta local, como `hostname`/`now_ms`). Junto a `hostname` forma la **identidad del escritor** de los locks de sesión (G32; [sesiones.md](sesiones.md) §6). Distinto de `nu.proc.alive(pid)`, que valida pids *ajenos*: `pid()` es el *propio*. |

---

## 8. `nu.http` y `nu.ws` — red [W]

El streaming de respuesta es de primera clase (ADR-005: los adaptadores de
providers viven en Lua y consumen SSE).

| Firma | Semántica |
|---|---|
| `nu.http.request(opts) -> {status, headers, body}` ⏸ | `opts`: `url`, `method?`, `headers?`, `body?`, `timeout_ms?`, `tls?`, `proxy?` (TLS/proxy por petición, ver nota G12 abajo). Respuesta buffereada. No lanza por status >= 400 (el status es dato); lanza `ENET`/`ETIMEOUT` por fallos de transporte. |
| `nu.http.stream(opts) -> Stream` ⏸ | Devuelve al recibir cabeceras: `Stream.status`, `Stream.headers`. `opts.timeout_ms` cubre hasta las cabeceras; `opts.idle_timeout_ms?` lanza `ETIMEOUT` si pasan N ms sin recibir bytes del body (un SSE puede quedarse mudo para siempre). |
| `Stream:chunks() -> iterator` ⏸ | Trozos crudos del body según llegan. |
| `Stream:events() -> iterator` ⏸ | Parser SSE incorporado: itera `{event?, data, id?}`. |
| `Stream:close()` | Aborta la conexión. |

Backpressure: los streams se bufferizan en Go mientras Lua consume a su
ritmo; el buffer tiene límite y al excederlo el stream falla con `EIO`.

TLS y proxy (G12): `request` y `stream` aceptan
`opts.tls = { ca_file?, insecure? }` (CA corporativa por petición);
`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` del entorno se respetan por defecto.
Defaults globales en la sección `[net]` de `nu.toml` (`ca_file`, proxy),
sobreescribibles por petición.
| `nu.ws.connect(url, opts?) -> Ws` ⏸ | `Ws:send(data, opts?)` ⏸ — `opts.binary?: boolean` manda frame **binario**; sin él, frame de texto (el protocolo exige UTF-8 válido en texto: bytes arbitrarios van con `binary`, o un servidor conforme cierra con 1007) (G52). `Ws:recv() -> data: string?, binary: boolean` ⏸ (`nil` al cerrar; el segundo valor distingue el tipo de frame entrante) (G52). `Ws:close()`. |

Reservado para futuro (no v1): `nu.net.tcp`.

---

## 9. `nu.ui` — celdas, regiones y compositor

Solo estado principal (ADR-008). El compositor, el diffing y el pintado
viven en Go; los cambios se coalescen y se pinta como mucho cada ~30 ms
(ADR-007). No existe "flush" manual.

**Headless (G20)**: sin TTY interactivo (`nu -e`, CI, salida redirigida),
el módulo `nu.ui` directamente **no existe** — el mismo modelo que las
`caps` de workers: la superficie no concedida no está. La detección es
`nu.has("ui")`, nunca probar-y-capturar.

### 9.1 Superficie

| Firma | Semántica |
|---|---|
| `nu.ui.size() -> {w, h}` | Tamaño del terminal en celdas. Cambios → evento `ui:resize`. |
| `nu.ui.region(opts) -> Region` | `opts`: `x, y, w, h, z?`. Las regiones son la unidad de composición: rectángulos con z-order propiedad de quien los crea. **Resize (G1)**: una región total o parcialmente fuera de pantalla se recorta sin error (jamás pinta fuera de límites; si no cabe nada, no se pinta); sus coordenadas no se tocan — si la pantalla vuelve a crecer, reaparece tal cual. Recolocarse es responsabilidad del dueño (convención "tu región, tu `ui:resize`"); el relayout automático es trabajo del toolkit, no del core. |
| `Region:blit(x, y, block: Block)` | Estampa un bloque pre-renderizado (ver `nu.text`) en coordenadas locales de la región. **Recorta por ambos extremos (G28)**: `x/y` pueden ser **negativos** y recortan el borde *inicial* del bloque (`blit(0, -3, doc)` muestra `doc` desde su cuarta fila), igual que el exceso recorta el final — un **viewport** sobre un Block más grande que la región, donde *scroll = re-blit con otro offset*. Es **copia, nunca re-render**: blittear el mismo Block con distinto offset no recalcula nada (el coste de scroll es el de una copia de la ventana visible). La virtualización (no construir el Block entero para historiales enormes) es del toolkit, no del core. |
| `Region:fill(style?)` / `Region:clear()` | |
| `Region:move(x, y)` / `Region:resize(w, h)` / `Region:raise()` / `Region:lower()` | |
| `Region:show()` / `Region:hide()` / `Region:destroy()` | |
| `Region:cursor(x, y \| nil)` | Coloca el cursor real del terminal (o lo oculta con `nil`). Solo una región puede tenerlo; la última llamada gana. |

### 9.2 Bloques y estilos

Un **Block** es un handle opaco de líneas estilizadas, producido por
`nu.text.*` o construido a mano. Tiene `.width` y `.height`.

| Firma | Semántica |
|---|---|
| `nu.ui.block(lines: (string\|Span[])[]) -> Block` | Construcción manual. Un `Span` es `{text, style?}`. |
| `Style` | Tabla `{fg?, bg?, bold?, italic?, underline?, reverse?}`; colores **literales**: `"#rrggbb"` o índice 0-255 (el render los degrada a lo que el terminal soporte, `nu.ui.caps().colors`). Los nombres semánticos (`"accent"`, `"error"`, ...) **no son del core**: son vocabulario del theme del toolkit, que los resuelve a literales al construir los Blocks (G22). |
| `nu.ui.caps() -> {colors, kitty_keyboard, mouse, images}` | Capacidades del terminal. |
| `nu.ui.clipboard_set(s)` / `nu.ui.clipboard_get() -> string?` ⏸ | Vía OSC 52 cuando el terminal lo soporta. |

### 9.3 Input

Modelo de pila: el input fluye al handler superior; quien no consume, deja
pasar. El enrutado fino de focus es trabajo del toolkit (extensión), no del
core.

| Firma | Semántica |
|---|---|
| `nu.ui.on_input(fn) -> InputHandle` | Apila un handler síncrono `fn(ev) -> boolean` (true = consumido). `ev`: `{type: "key"\|"mouse"\|"paste", key?, mods?, x?, y?, text?, path?}`. `InputHandle:pop()`. |
| `nu.ui.keymap(seq: string, fn, opts?) -> Keymap` | Azúcar sobre la pila: `seq` en notación `"ctrl+k"`, `"alt+enter"`, secuencias `"g g"`. `Keymap:unmap()`. Resolución de secuencias con timeout en el core. Conflictos: la pila manda — el registro más reciente activo gana (y el `init.lua` del usuario se carga el último, §14). **Consumo:** un keymap consume la tecla por defecto (disparar el atajo es atenderlo); su `fn` puede devolver `false` EXPLÍCITO para **ceder** la tecla y que siga bajando por la pila (lo usa el chat para apartar `esc`/`enter` cuando hay un modal abierto y la tecla llegue al widget enfocado). |

Pegar una imagen (G30): cuando el portapapeles trae contenido **no-texto**
(una imagen), el core lo vuelca a un fichero temporal de la sesión
(`nu.fs.tmpdir`) y entrega el evento `paste` con `path` (la ruta volcada) en
vez de `text`. La UI inserta esa ruta igual que una mención `@` y el agente
decide leerla (no se incrusta el contenido a ciegas); así los bytes binarios
nunca cruzan las fronteras de texto/JSON (coherente con G11, §12). Pintar la
imagen en pantalla es otra cosa ([pospuesto.md](pospuesto.md) P6).

---

## 10. `nu.text` — render y procesado [W]

Las operaciones cuadráticas-en-pantalla viven aquí, en Go (ADR-004/007).

| Firma | Semántica |
|---|---|
| `nu.text.width(s) -> integer` | Anchura en celdas (graphemes, east-asian, emoji). |
| `nu.text.wrap(s, width, opts?) -> Block` | Word-wrap; `opts.style?` (un Style §9.2) aplica a cada línea. |
| `nu.text.truncate(s, width, opts?) -> string` | Con elipsis opcional. |
| `nu.text.markdown(s, opts) -> Block` | Render completo de markdown a `opts.width`, themable. Acepta entrada incompleta (streaming-safe). |
| `nu.text.highlight(code, lang, opts?) -> Block` | Syntax highlighting. |
| `nu.text.diff(a, b, opts?) -> {hunks, block?}` | Diff estructurado; `opts.render=true` devuelve además el Block pintado. |
| `nu.re.compile(pattern) -> Re` | Regex RE2. `Re:match(s) -> caps?`, `Re:find_all(s) -> ranges`, `Re:replace(s, repl) -> string`. |

Nota (G23): aquí no hay estimación de tokens LLM — "token" es vocabulario
de producto, y la heurística (~4 bytes/token) es una división en Lua puro
que no justifica primitiva ("Lua decide, Go ejecuta"). Vive en la extensión
de providers: `providers.approx_tokens` ([providers.md](providers.md) §4).
Las concesiones de este módulo (markdown, highlighting) se quedan porque
las justifica el rendimiento; esa no lo hacía.

---

## 11. `nu.search` — búsqueda a escala de repo [W]

| Firma | Semántica |
|---|---|
| `nu.search.files(root, opts?) -> string[]` ⏸ | Listado recursivo respetando `.gitignore`. `opts`: `glob`, `hidden`, `max`. |
| `nu.search.grep(pattern, opts) -> iterator` ⏸ | Paralelo por dentro; itera `{path, line_no, line, ranges}` según llegan. `opts`: `root`, `glob`, `case`, `max`. |
| `nu.search.fuzzy(query, candidates: string[], opts?) -> {index, score}[]` | Matching difuso ordenado, para pickers. Síncrono y acotado (es la primitiva caliente del picker). |

---

## 12. `nu.json` / `nu.toml` / `nu.yaml` — codecs [W]

| Firma | Semántica |
|---|---|
| `nu.json.encode(v, opts?) -> string` / `nu.json.decode(s) -> v` | `opts.pretty`. `null` ↔ `nu.json.NULL` (sentinel) para no perder claves. **Estricto con UTF-8** (G11): `encode` lanza `EINVAL` ante bytes inválidos — sanear es decisión visible de quien tiene el contexto (la tool), nunca del codec. |
| `nu.toml.encode(v) -> string` / `nu.toml.decode(s) -> v` | |
| `nu.yaml.encode(v) -> string` / `nu.yaml.decode(s) -> v` | Necesario para metadatos del ecosistema existente (frontmatter de skills); YAML es demasiado traicionero para parsearlo en Lua puro. |

---

## 13. `nu.worker` — paralelismo opt-in (ADR-008)

| Firma | Semántica |
|---|---|
| `nu.worker.spawn(module: string, opts?) -> Worker` | Levanta un estado Lua nuevo en su goroutine, cargando `module` (resoluble por el loader). Las rutas de `require` del loader (módulos Lua de plugins) están disponibles dentro del worker; lo que no existe es la API `nu.plugin` (ciclo de vida). Sin `nu.ui`, `nu.events` (bus principal) ni workers anidados. `opts.caps?: string[]` restringe la API del worker a lo enumerado, con **dos granularidades** (G6): `"fs"` concede el módulo entero; `"fs.read"` concede una función concreta. Lo no concedido **no existe** dentro del estado — sandboxing por capacidades; las funciones añadidas a la API en el futuro nunca quedan concedidas por listas antiguas (deny-by-default para superficie nueva). Sin `caps`, el worker recibe toda la API [W]. Paquetes con nombre (p. ej. solo-lectura): tablas de la extensión del agente (`agent.caps.*`), no del core. |
| `Worker:send(msg)` ⏸ / `Worker:recv() -> msg` ⏸ | Mensajes = valores JSON-ables, **copiados** (las tablas no cruzan estados). Tampoco cruzan closures, userdata ni Blocks: un worker manda datos digeridos y el estado principal renderiza. Las colas son **acotadas**: `send` suspende si está llena (backpressure, coherente con §8) — desde un handler síncrono, `task.spawn` como siempre. |
| `Worker:on_message(fn) -> Sub` | Alternativa por callback en el estado principal. **Excluyente con `recv`** (G8): registrar uno con el otro pendiente (o viceversa) lanza `EINVAL` en el acto — nunca prioridad silenciosa. |
| `Worker:terminate()` | Inmediato y seguro (estados aislados). |
| *(dentro del worker)* `nu.worker.parent.send(msg)` ⏸ / `...recv() -> msg` ⏸ | Canal con el estado principal; mismas colas acotadas. |

Interior de un worker (G15): cada worker es un **mini-runtime completo** —
scheduler propio, múltiples tasks, timers y futures (todo `nu.task` [W]).
**Sin watchdog**: los workers existen precisamente para quemar CPU a gusto;
el control es `terminate()` desde el principal más las `caps`.

---

## 14. `nu.plugin` y loader

Un plugin es un directorio con `plugin.toml` (`name`, `version`,
`requires?: string[]`) e `init.lua`, que se ejecuta al cargar. El directorio
`lua/` del plugin se añade a las rutas de `require` (así los plugins se
requieren entre sí: composabilidad de ADR-008). Las extensiones oficiales
embebidas (`go:embed`) se cargan primero y son sustituibles por nombre
desde el directorio de usuario. El **nombre es la identidad** del plugin y
el loader la mantiene única: el directorio de usuario *sustituye* a la
embebida del mismo nombre (no coexisten), y dos plugins con el mismo nombre
son un error de carga accionable. Esa unicidad es lo que deja que los
namespaces de eventos (§4) y demás registros sean libres de colisión por
simple convención (namespace = nombre del plugin), sin que el core reserve
nombre alguno de extensión (G26).

**Configuración del runtime**: `config.dir()/nu.toml` gobierna al propio
core — la activación de plugins (las extensiones oficiales embebidas están
**inactivas por defecto**, ADR-010; el primer arranque ofrece activar el
**conjunto oficial de producto** —las embebidas menos el plugin-andamiaje
`example`, ADR-015), rutas extra de plugins, presupuesto del watchdog.

**Pantalla de runtime desnudo (G21)**: con TTY interactivo y ningún plugin
activo, el kernel pinta una pantalla fija hecha solo de sus capacidades —
versión y nivel de API, rutas de config y plugins, extensiones embebidas
disponibles — y sus acciones: activar el conjunto oficial (escribe
`plugins.enabled` y continúa el arranque canónico, sin red), activar
extensiones sueltas (p. ej. solo `repl`), o salir. No es la UI de un
producto sino la del runtime: las extensiones embebidas y su activación
son capacidad del loader, así que el kernel habla de lo suyo
([filosofia.md](filosofia.md) §2) — render fijo, pre-Lua, sin widgets ni
lógica. Es lo que se ve siempre que nu arranca sin nada activo, no un
diálogo de primera vez. Sin TTY no hay pantalla: arranca desnudo, y los
errores por extensión inactiva son accionables (nombran la línea de
`nu.toml` que lo arregla, como los de permisos en
[agente.md](agente.md) §5). El onramp sin TTY (CI, Docker, scripts) es el
flag de CLI `nu --default-config` (ADR-015, G33): escribe ese mismo conjunto
de producto en `nu.toml` —y plantillas activas de `agent.toml`/`providers.toml`
si no existen, para que el harness quede usable, ADR-017/G35— y sale, o
—combinado con `-p`/`-e`— lo activa solo para ese proceso sin tocar disco. Es
superficie CLI del binario, no API sagrada: no añade nada a `nu.*` ni mueve
`nu.version.api`.

**Orden de arranque canónico**: core → plugins activados (topológico por
`requires`) → `init.lua` del usuario → evento `core:ready`. El
init del usuario va **último** a propósito: como en la pila de input el
registro más reciente gana, el usuario tiene la última palabra (keymaps,
theme, overrides) por construcción, sin sistema de prioridades.

| Firma | Semántica |
|---|---|
| `nu.plugin.current() -> {name, version, dir}` | Plugin en cuyo contexto corre el código. |
| `nu.plugin.list() -> {name, version, source: "builtin"\|"user", enabled}[]` | |
| `nu.plugin.reload(name)` ⏸ | Herramienta de desarrollo, **best-effort** (G2): suelta todos los handles del plugin (el core los etiqueta por dueño vía `plugin.current()`), emite `core:plugin.unload` (las extensiones limpian sus registros: tools, comandos...), vacía la caché de `require` del plugin y recarga su `init.lua`. Un plugin con efectos globales exóticos puede no descargarse limpio — para iterar, no para producción. |
| `nu.config.dir() -> string` [W] / `nu.config.data_dir() -> string` [W] | `~/.config/nu` y `~/.local/share/nu` (o equivalentes por plataforma). |

---

## 15. `nu.log` [W]

| Firma | Semántica |
|---|---|
| `nu.log.debug/info/warn/error(fmt, ...)` | A fichero en `data_dir`, con plugin de origen anotado. `print` es alias de `info`. Nunca a la pantalla: la UI es de las extensiones. |

---

## 16. Resumen de disponibilidad en workers

| Disponible [W] | Solo estado principal |
|---|---|
| `task`, `fs` (salvo `watch`), `proc`, `sys`, `http`, `ws`, `text`, `re`, `search`, `json`, `toml`, `yaml`, `log`, `config.dir`, `config.data_dir` | `ui`, `events`, `fs.watch`, `worker.spawn`, `plugin` |

---

## 17. Estabilidad y evolución

- Congelar v1 = congelar **este documento**: firmas y semánticas solo cambian
  por adición; `nu.version.api` se incrementa con cada adición. **Nivel actual:
  `api = 3`** — el nivel 1 fue el congelado inicial; `nu.sys.pid()` (G32) lo
  subió a 2; los frames binarios de `nu.ws` (G52: `opts.binary` en `Ws:send`,
  segundo retorno de `Ws:recv`) lo subieron a 3. Una adición nunca rompe
  firmas existentes: el código escrito contra el nivel 1 sigue siendo válido
  en los niveles siguientes.
- Detección de capacidades con `nu.has()`, nunca sniffing de versión.
- Namespaces de eventos `core:`/`ui:` y códigos de error de §1.4 reservados.
- Fuera de esta especificación (deliberadamente): toolkit de widgets, hooks
  del agente (`agent:*`), MCP, formato de `providers.toml`. Son contratos de
  sus extensiones, versionados aparte. El de providers ya tiene borrador:
  [providers.md](providers.md).
