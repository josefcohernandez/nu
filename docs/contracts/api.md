---
title: "API del core de enu — especificación v1 (borrador)"
description: "La API v1 del core — la superficie sagrada: firmas y semánticas."
type: "contrato"
layer: "contracts"
web: "api"
status: "vigente"
---
# API del core de enu — especificación v1 (borrador)

Estado: **borrador para discusión**. Cuando se congele, esta superficie es la
"API sagrada" (ADR-003): solo crece por adición. Todo lo que no está aquí
(toolkit de widgets, agente, MCP, providers) es extensión y se versiona aparte.

Convenciones de esta especificación:

- Las firmas usan notación `enu.mod.fn(arg: tipo, opts?: tabla) -> tipo`.
- **⏸ suspende**: la función solo puede llamarse dentro de una task
  (corrutina); cede el control hasta completarse y devuelve el resultado
  directamente. Llamarla fuera de una task es un error.
- **[W]**: disponible dentro de workers. Sin marca: solo estado principal.

---

## 1. Convenciones transversales (ADR-009)

### 1.1 Namespace

Toda la API vive bajo el global `enu`. `require` queda reservado para módulos
de plugins y librerías Lua puras. Identificadores en inglés, `snake_case`.

### 1.2 Baseline del entorno Lua

Lua 5.4 (PUC-Lua, compilada a WASM y ejecutada sobre el runtime embebido —
ver [migracion-vm.md](archive/migracion-vm.md)). Disponibles: `string`, `table`,
`math`, `coroutine`, `utf8`, `pairs/ipairs/pcall/error/load/...`.
**Deshabilitados**: `io`, `os.execute`, `os.exit`, `os.remove`, `os.rename`,
`os.getenv`, `print` (redirigido a `enu.log.info`), `dofile`/`loadfile` fuera
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
  llamar funciones ⏸; para hacer IO, lanzan una task con `enu.task.spawn`.
- **Watchdog**: cada *slice* de ejecución Lua continua (entre dos puntos de
  suspensión) tiene un presupuesto, por defecto 100 ms (configurable en
  `enu.toml`). Excederlo aborta la task y emite `core:plugin.misbehaved`.
- **Cancelación y abortos NO son capturables.** `Task:cancel()` y el
  watchdog abortan la task en su siguiente punto de suspensión (o slice)
  **desenrollando la pila sin pasar por `pcall`** — si fueran errores
  normales, cualquier `pcall` del ecosistema los capturaría y el programa
  seguiría como si nada. Para liberar recursos pase lo que pase, registra
  `enu.task.cleanup(fn)`. `ECANCELED` queda reservado para *observar* la
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
`enu.proc.run`, `enu.http.request`, `enu.http.stream`, `enu.ws.connect`—; el
resto del IO de v1 no acepta plazo (G47 — extenderlo a más firmas es una
adición futura compatible, no una promesa vigente). El valor frontera queda
definido donde el plazo existe: en `proc.run`, `0` (el default) significa
*sin límite* — un proceso local puede legítimamente no tener techo—; en
`http`/`ws` el plazo existe siempre (default 30 000 ms) y `0` es `EINVAL` —
no hay petición de red sin techo—. Los handles del core (Task, Region,
Proc...) son userdata opacos con métodos.

---

## 2. `enu` (raíz)

| Firma | Semántica |
|---|---|
| `enu.version -> {major, minor, patch, api: integer}` [W] | Versión del runtime y nivel de API. |
| `enu.has(cap: string) -> boolean` [W] | Detección de capacidades (`"ui"`, `"ui.images"`, `"net.tcp"`, ...) para extensiones portables. Cubre también módulos enteros: en headless `enu.ui` no existe (§9). |

---

## 3. `enu.task` — scheduler [W]

| Firma | Semántica |
|---|---|
| `enu.task.spawn(fn, ...) -> Task` | Lanza una task; los argumentos extra se pasan a `fn`. |
| `enu.task.sleep(ms)` ⏸ | Suspende la task actual. |
| `enu.task.all(fns: Task[]\|fn[]) -> any[]` ⏸ | Espera a todas; si una lanza, cancela el resto y relanza. Los resultados se devuelven **alineados con los inputs** (`out[i]` es el de `fns[i]`), nunca en orden de terminación (G27) — es lo que deja correlacionar resultado con entrada en un fan-out sin acarrear el índice a mano. |
| `enu.task.race(fns) -> (winner_index, result)` ⏸ | Primera en terminar gana; cancela el resto. |
| `enu.task.every(ms, fn) -> Timer` | Timer periódico (handler síncrono). `Timer:stop()`. |
| `enu.task.defer(fn)` | Ejecuta `fn` en el siguiente tick del loop. |
| `enu.task.future() -> Future` | Rendez-vous de un solo uso: `Future:set(v)` (síncrono, una sola vez; llamadas posteriores lanzan `EINVAL`) y `Future:await() -> v` ⏸ (varios pueden esperar; si ya está resuelto, retorna inmediato). Es la pieza para "una task espera un valor que otro código producirá" (diálogos, pickers, proxies) sin polling. |
| `Task:cancel()` | Cancelación cooperativa: aborta la task en su siguiente punto de suspensión (no capturable, §1.3); corren sus `cleanup`s. |
| `enu.task.cleanup(fn)` [W] | Registra un liberador (síncrono) en la pila LIFO de la task actual; corren todos al terminar — éxito, error o aborto. El `defer` de esta casa: procesos, regiones, handlers de input. |
| `Task:await() -> any` ⏸ | Espera el resultado de otra task. |

---

## 4. `enu.events` — bus de eventos

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
| `enu.events.on(name, fn) -> Sub` | Suscribe. Handlers síncronos, en orden de registro, cada uno bajo `pcall` (ADR-008). `Sub:cancel()`. |
| `enu.events.once(name, fn) -> Sub` | Una sola vez. |
| `enu.events.emit(name, payload?)` | Despacho síncrono en el estado principal. |

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

## 5. `enu.fs` — filesystem [W]

| Firma | Semántica |
|---|---|
| `enu.fs.read(path) -> string` ⏸ | Lee el fichero entero. |
| `enu.fs.write(path, data, opts?)` ⏸ / `enu.fs.append(path, data)` ⏸ | Escritura atómica (write vía fichero temporal + rename). `opts.exclusive = true` (G17): crea **solo si no existe**, en una única operación indivisible (`O_EXCL` — aquí no hay temporal+rename: rename sobreescribiría); si el fichero ya existe lanza `EEXIST`. Es la pieza para lockfiles ([sesiones.md](sesiones.md) §6). |
| `enu.fs.stat(path) -> {size, mtime_ms, is_dir, mode}?` ⏸ | `nil` si no existe (no lanza `ENOENT`). |
| `enu.fs.list(dir) -> {name, is_dir}[]` ⏸ | Sin recursión; para recursivo ver `enu.search.files`. |
| `enu.fs.mkdir(path)` ⏸ / `enu.fs.remove(path, opts?)` ⏸ / `enu.fs.rename(from, to)` ⏸ / `enu.fs.copy(from, to)` ⏸ | `remove` exige `opts.recursive=true` para directorios no vacíos. |
| `enu.fs.tmpdir() -> string` ⏸ | Directorio temporal propio de la sesión. |
| `enu.fs.cwd() -> string` [W] | Directorio de trabajo (inmutable durante la sesión; los subprocesos pueden recibir otro vía `opts.cwd`). |
| `enu.fs.watch(path, opts?, fn) -> Watcher` | `opts`: `recursive?`, `gitignore = true` (ignora lo ignorado por git: vigilar `node_modules/` es ruido), `debounce_ms = 50`. Entrega **en lotes**: `fn(events[])` con `{path, kind: "create"\|"modify"\|"remove"}` — un `git checkout` que toca miles de ficheros llega como un solo lote (G7). Handler síncrono. `Watcher:stop()`. Solo estado principal. |

---

## 6. `enu.proc` — subprocesos [W]

| Firma | Semántica |
|---|---|
| `enu.proc.run(argv: string[], opts?) -> {code, stdout, stderr}` ⏸ | Conveniencia con buffers. `opts`: `cwd`, `env`, `stdin`, `timeout_ms`. Sin shell implícita: `argv` es un array; quien quiera shell la invoca explícitamente. |
| `enu.proc.spawn(argv, opts?) -> Proc` | Control fino con streams. |
| `Proc:write(data)` ⏸ / `Proc:close_stdin()` | stdin en streaming. |
| `Proc:read_line(which: "stdout"\|"stderr") -> string?` ⏸ | `nil` en EOF. |
| `Proc:read(which, n?) -> string?` ⏸ | Lectura cruda. |
| `Proc:wait() -> {code}` ⏸ / `Proc:kill(signal?)` | `signal` por defecto TERM. |
| `enu.proc.alive(pid: integer) -> boolean` | ¿Hay un proceso vivo con ese `pid` en esta máquina? (G17). Informa de **existencia, no de identidad** — un pid reciclado da `true`. Para detectar locks huérfanos ([sesiones.md](sesiones.md) §6). |

Vida del proceso: la regla es matarlo explícitamente vía `enu.task.cleanup`
en quien lo crea; como red de seguridad, un `Proc` sin referencias acaba
matado por el GC (no determinista — no confíes en ello).

---

## 7. `enu.sys` — entorno y reloj [W]

| Firma | Semántica |
|---|---|
| `enu.sys.platform() -> "linux"\|"darwin"\|"windows"` | |
| `enu.sys.env(name) -> string?` / `enu.sys.setenv(name, value)` | `setenv` afecta solo a subprocesos futuros. |
| `enu.sys.now_ms() -> number` / `enu.sys.mono_ms() -> number` | Reloj de pared / monotónico. |
| `enu.sys.hostname() -> string` | Nombre de la máquina (G17; contenido de los locks de sesión, [sesiones.md](sesiones.md) §6). |
| `enu.sys.pid() -> integer` | Pid del proceso `enu` actual (consulta local, como `hostname`/`now_ms`). Junto a `hostname` forma la **identidad del escritor** de los locks de sesión (G32; [sesiones.md](sesiones.md) §6). Distinto de `enu.proc.alive(pid)`, que valida pids *ajenos*: `pid()` es el *propio*. |

---

## 8. `enu.http` y `enu.ws` — red [W]

El streaming de respuesta es de primera clase (ADR-005: los adaptadores de
providers viven en Lua y consumen SSE).

| Firma | Semántica |
|---|---|
| `enu.http.request(opts) -> {status, headers, body}` ⏸ | `opts`: `url`, `method?`, `headers?`, `body?`, `timeout_ms?`, `tls?`, `proxy?`, `max_redirects?` (TLS/proxy por petición, ver nota G12 abajo; redirects, nota G54). Respuesta buffereada. No lanza por status >= 400 (el status es dato); lanza `ENET`/`ETIMEOUT` por fallos de transporte. |
| `enu.http.stream(opts) -> Stream` ⏸ | Devuelve al recibir cabeceras: `Stream.status`, `Stream.headers`. `opts.timeout_ms` cubre hasta las cabeceras; `opts.idle_timeout_ms?` lanza `ETIMEOUT` si pasan N ms sin recibir bytes del body (un SSE puede quedarse mudo para siempre). Acepta también `opts.max_redirects?` (nota G54 abajo). |
| `Stream:chunks() -> iterator` ⏸ | Trozos crudos del body según llegan. |
| `Stream:events() -> iterator` ⏸ | Parser SSE incorporado: itera `{event?, data, id?}`. |
| `Stream:close()` | Aborta la conexión. |

Backpressure: los streams se bufferizan en Go mientras Lua consume a su
ritmo; el buffer tiene límite y al excederlo el stream falla con `EIO`.

TLS y proxy (G12): `request` y `stream` aceptan
`opts.tls = { ca_file?, insecure? }` (CA corporativa por petición);
`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` del entorno se respetan por defecto.
Defaults globales en la sección `[net]` de `enu.toml` (`ca_file`, proxy),
sobreescribibles por petición.

Redirects (G54): `request` y `stream` aceptan `opts.max_redirects?: number` —
el presupuesto de redirecciones que el cliente sigue automáticamente. Default
**10** (la política que el cliente aplicaba de forma implícita pasa a
contrato); `0` = no seguir ninguna. Agotado el presupuesto **no se lanza
error**: se entrega la última respuesta `3xx` **como dato** —coherente con
"el status es dato"—, con su `location` en `headers`; quien necesite observar
o validar la cadena salto a salto pone `0` y la sigue a mano (un `302` hacia
`169.254.169.254` no debe poder evadir la validación que se hizo sobre la URL
inicial). Y en cada salto **cross-host** el cliente **recorta todas las
cabeceras que el llamante puso en `opts.headers`** antes de reenviar la
petición. La regla exacta: un salto es cross-host si el host de la URL de
destino (nombre y puerto) difiere del de la URL inicial de `opts.url`, **o**
si el esquema degrada de `https` a `http` aunque el host se conserve (la
cabecera viajaría en claro por un canal interceptable); una vez recortadas,
las cabeceras **no se restauran** aunque un salto posterior regrese al host
inicial — la cadena pasó por un tercero y dejó de ser de confianza. Se
recorta *todo* lo del llamante, sin lista blanca de cabeceras "seguras",
además de lo que el cliente ya recortaba entre dominios (`Authorization`,
`Cookie`): las credenciales viven cada vez más en cabeceras custom
(`x-api-key`, `x-goog-api-key`) que ninguna lista negra conocería, y un
destino distinto es un interlocutor distinto que no hereda lo que el llamante
le dijo al primero.
| `enu.ws.connect(url, opts?) -> Ws` ⏸ | `Ws:send(data, opts?)` ⏸ — `opts.binary?: boolean` manda frame **binario**; sin él, frame de texto (el protocolo exige UTF-8 válido en texto: bytes arbitrarios van con `binary`, o un servidor conforme cierra con 1007) (G52). `Ws:recv() -> data: string?, binary: boolean` ⏸ (`nil` al cerrar; el segundo valor distingue el tipo de frame entrante) (G52). `Ws:close()`. |

Reservado para futuro (no v1): `enu.net.tcp`.

---

## 9. `enu.ui` — celdas, regiones y compositor

Solo estado principal (ADR-008). El compositor, el diffing y el pintado
viven en Go; los cambios se coalescen y se pinta como mucho cada ~30 ms
(ADR-007). No existe "flush" manual.

**Headless (G20)**: sin TTY interactivo (`enu -e`, CI, salida redirigida),
el módulo `enu.ui` directamente **no existe** — el mismo modelo que las
`caps` de workers: la superficie no concedida no está. La detección es
`enu.has("ui")`, nunca probar-y-capturar.

### 9.1 Superficie

| Firma | Semántica |
|---|---|
| `enu.ui.size() -> {w, h}` | Tamaño del terminal en celdas. Cambios → evento `ui:resize`. |
| `enu.ui.region(opts) -> Region` | `opts`: `x, y, w, h, z?`. Las regiones son la unidad de composición: rectángulos con z-order propiedad de quien los crea. **Resize (G1)**: una región total o parcialmente fuera de pantalla se recorta sin error (jamás pinta fuera de límites; si no cabe nada, no se pinta); sus coordenadas no se tocan — si la pantalla vuelve a crecer, reaparece tal cual. Recolocarse es responsabilidad del dueño (convención "tu región, tu `ui:resize`"); el relayout automático es trabajo del toolkit, no del core. |
| `Region:blit(x, y, block: Block)` | Estampa un bloque pre-renderizado (ver `enu.text`) en coordenadas locales de la región. **Recorta por ambos extremos (G28)**: `x/y` pueden ser **negativos** y recortan el borde *inicial* del bloque (`blit(0, -3, doc)` muestra `doc` desde su cuarta fila), igual que el exceso recorta el final — un **viewport** sobre un Block más grande que la región, donde *scroll = re-blit con otro offset*. Es **copia, nunca re-render**: blittear el mismo Block con distinto offset no recalcula nada (el coste de scroll es el de una copia de la ventana visible). La virtualización (no construir el Block entero para historiales enormes) es del toolkit, no del core. |
| `Region:fill(style?)` / `Region:clear()` | |
| `Region:move(x, y)` / `Region:resize(w, h)` / `Region:raise()` / `Region:lower()` | |
| `Region:show()` / `Region:hide()` / `Region:destroy()` | |
| `Region:cursor(x, y \| nil)` | Coloca el cursor real del terminal (o lo oculta con `nil`). Solo una región puede tenerlo; la última llamada gana. |

### 9.2 Bloques y estilos

Un **Block** es un handle opaco de líneas estilizadas, producido por
`enu.text.*` o construido a mano. Tiene `.width` y `.height`.

| Firma | Semántica |
|---|---|
| `enu.ui.block(lines: (string\|Span[])[]) -> Block` | Construcción manual. Un `Span` es `{text, style?}`. |
| `Style` | Tabla `{fg?, bg?, bold?, italic?, underline?, reverse?}`; colores **literales**: `"#rrggbb"` o índice 0-255 (el render los degrada a lo que el terminal soporte, `enu.ui.caps().colors`). Los nombres semánticos (`"accent"`, `"error"`, ...) **no son del core**: son vocabulario del theme del toolkit, que los resuelve a literales al construir los Blocks (G22). |
| `enu.ui.caps() -> {colors, kitty_keyboard, mouse, images}` | Capacidades del terminal. |
| `enu.ui.clipboard_set(s)` / `enu.ui.clipboard_get() -> string?` ⏸ | Vía OSC 52 cuando el terminal lo soporta. |

### 9.3 Input

Modelo de pila: el input fluye al handler superior; quien no consume, deja
pasar. El enrutado fino de focus es trabajo del toolkit (extensión), no del
core.

| Firma | Semántica |
|---|---|
| `enu.ui.on_input(fn) -> InputHandle` | Apila un handler síncrono `fn(ev) -> boolean` (true = consumido). `ev`: `{type: "key"\|"mouse"\|"paste", key?, mods?, x?, y?, text?, path?}`. `InputHandle:pop()`. |
| `enu.ui.keymap(seq: string, fn, opts?) -> Keymap` | Azúcar sobre la pila: `seq` en notación `"ctrl+k"`, `"alt+enter"`, secuencias `"g g"`. `Keymap:unmap()`. Resolución de secuencias con timeout en el core. Conflictos: la pila manda — el registro más reciente activo gana (y el `init.lua` del usuario se carga el último, §14). **Consumo:** un keymap consume la tecla por defecto (disparar el atajo es atenderlo); su `fn` puede devolver `false` EXPLÍCITO para **ceder** la tecla y que siga bajando por la pila (lo usa el chat para apartar `esc`/`enter` cuando hay un modal abierto y la tecla llegue al widget enfocado). |

Pegar una imagen (G30): cuando el portapapeles trae contenido **no-texto**
(una imagen), el core lo vuelca a un fichero temporal de la sesión
(`enu.fs.tmpdir`) y entrega el evento `paste` con `path` (la ruta volcada) en
vez de `text`. La UI inserta esa ruta igual que una mención `@` y el agente
decide leerla (no se incrusta el contenido a ciegas); así los bytes binarios
nunca cruzan las fronteras de texto/JSON (coherente con G11, §12). Pintar la
imagen en pantalla es otra cosa ([pospuesto.md](pospuesto.md) P6).

---

## 10. `enu.text` — render y procesado [W]

Las operaciones cuadráticas-en-pantalla viven aquí, en Go (ADR-004/007).

| Firma | Semántica |
|---|---|
| `enu.text.width(s) -> integer` | Anchura en celdas (graphemes, east-asian, emoji). |
| `enu.text.wrap(s, width, opts?) -> Block` | Word-wrap; `opts.style?` (un Style §9.2) aplica a cada línea. |
| `enu.text.truncate(s, width, opts?) -> string` | Con elipsis opcional. |
| `enu.text.markdown(s, opts) -> Block` | Render completo de markdown a `opts.width`, themable. Acepta entrada incompleta (streaming-safe). |
| `enu.text.highlight(code, lang, opts?) -> Block` | Syntax highlighting. |
| `enu.text.diff(a, b, opts?) -> {hunks, block?}` | Diff estructurado; `opts.render=true` devuelve además el Block pintado. |
| `enu.re.compile(pattern) -> Re` | Regex RE2. `Re:match(s) -> caps?`, `Re:find_all(s) -> ranges`, `Re:replace(s, repl) -> string`. |

Nota (G23): aquí no hay estimación de tokens LLM — "token" es vocabulario
de producto, y la heurística (~4 bytes/token) es una división en Lua puro
que no justifica primitiva ("Lua decide, Go ejecuta"). Vive en la extensión
de providers: `providers.approx_tokens` ([providers.md](providers.md) §4).
Las concesiones de este módulo (markdown, highlighting) se quedan porque
las justifica el rendimiento; esa no lo hacía.

---

## 11. `enu.search` — búsqueda a escala de repo [W]

| Firma | Semántica |
|---|---|
| `enu.search.files(root, opts?) -> string[]` ⏸ | Listado recursivo respetando `.gitignore`. `opts`: `glob`, `hidden`, `max`. |
| `enu.search.grep(pattern, opts) -> iterator` ⏸ | Paralelo por dentro; itera `{path, line_no, line, ranges}` según llegan. `opts`: `root`, `glob`, `case`, `max`. |
| `enu.search.fuzzy(query, candidates: string[], opts?) -> {index, score}[]` | Matching difuso ordenado, para pickers. Síncrono y acotado (es la primitiva caliente del picker). |

---

## 12. `enu.json` / `enu.toml` / `enu.yaml` — codecs [W]

| Firma | Semántica |
|---|---|
| `enu.json.encode(v, opts?) -> string` / `enu.json.decode(s) -> v` | `opts.pretty`. `null` ↔ `enu.json.NULL` (sentinel) para no perder claves. **Estricto con UTF-8** (G11): `encode` lanza `EINVAL` ante bytes inválidos — sanear es decisión visible de quien tiene el contexto (la tool), nunca del codec. |
| `enu.toml.encode(v) -> string` / `enu.toml.decode(s) -> v` | |
| `enu.yaml.encode(v) -> string` / `enu.yaml.decode(s) -> v` | Necesario para metadatos del ecosistema existente (frontmatter de skills); YAML es demasiado traicionero para parsearlo en Lua puro. |

---

## 13. `enu.worker` — paralelismo opt-in (ADR-008)

| Firma | Semántica |
|---|---|
| `enu.worker.spawn(module: string, opts?) -> Worker` | Levanta un estado Lua nuevo en su goroutine, cargando `module` (resoluble por el loader). Las rutas de `require` del loader (módulos Lua de plugins) están disponibles dentro del worker; lo que no existe es la API `enu.plugin` (ciclo de vida). Sin `enu.ui`, `enu.events` (bus principal) ni workers anidados. `opts.caps?: string[]` restringe la API del worker a lo enumerado, con **dos granularidades** (G6): `"fs"` concede el módulo entero; `"fs.read"` concede una función concreta. Lo no concedido **no existe** dentro del estado — sandboxing por capacidades; las funciones añadidas a la API en el futuro nunca quedan concedidas por listas antiguas (deny-by-default para superficie nueva). Sin `caps`, el worker recibe toda la API [W]. Paquetes con nombre (p. ej. solo-lectura): tablas de la extensión del agente (`agent.caps.*`), no del core. |
| `Worker:send(msg)` ⏸ / `Worker:recv() -> msg` ⏸ | Mensajes = valores JSON-ables, **copiados** (las tablas no cruzan estados). Tampoco cruzan closures, userdata ni Blocks: un worker manda datos digeridos y el estado principal renderiza. Las colas son **acotadas**: `send` suspende si está llena (backpressure, coherente con §8) — desde un handler síncrono, `task.spawn` como siempre. |
| `Worker:on_message(fn) -> Sub` | Alternativa por callback en el estado principal. **Excluyente con `recv`** (G8): registrar uno con el otro pendiente (o viceversa) lanza `EINVAL` en el acto — nunca prioridad silenciosa. |
| `Worker:terminate()` | Inmediato y seguro (estados aislados). |
| *(dentro del worker)* `enu.worker.parent.send(msg)` ⏸ / `...recv() -> msg` ⏸ | Canal con el estado principal; mismas colas acotadas. |

Interior de un worker (G15): cada worker es un **mini-runtime completo** —
scheduler propio, múltiples tasks, timers y futures (todo `enu.task` [W]).
**Sin watchdog**: los workers existen precisamente para quemar CPU a gusto;
el control es `terminate()` desde el principal más las `caps`.

**Identidad de un worker (G56, ADR-024)**: un worker porta como identidad el
plugin dueño vigente en el momento de `enu.worker.spawn`, capturada en el
estado principal —donde la pila de dueños es coherente por construcción
(single-threaded, ADR-004)— e **inmutable** durante toda la vida del worker.
La razón: dentro del worker no existe `enu.plugin` ni ciclo de vida alguno,
así que no hay pila propia que consultar, y consultar la del padre desde otra
goroutine daría una atribución no determinista (valdría lo que el principal
estuviera haciendo en ese instante) además de una carrera de datos — la
identidad viaja **copiada** en el spawn, como los mensajes, nunca leída en
vivo del runtime padre. Toda primitiva [W] atribuida por dueño usa esa
identidad fija: `enu.log` (§15) la anota como plugin de origen y los procesos
de `enu.proc` (§6) lanzados desde el worker se registran bajo ese plugin. En
los artefactos de atribución se anota distinguible como `<plugin> (worker)` —
p. ej. `agent (worker)`— para que la traza diga quién *y desde dónde*.
Consecuencia de supervisión: como el estado principal posee todos los workers
([P11](pospuesto.md)), un `enu.plugin.reload` (§14) del plugin dueño sigue
soltando también los procesos lanzados por sus workers — el árbol de
supervisión no tiene fugas por la frontera del worker.

---

## 14. `enu.plugin` y loader

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

**Configuración del runtime**: `config.dir()/enu.toml` gobierna al propio
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
lógica. Es lo que se ve siempre que enu arranca sin nada activo, no un
diálogo de primera vez. Sin TTY no hay pantalla: arranca desnudo, y los
errores por extensión inactiva son accionables (nombran la línea de
`enu.toml` que lo arregla, como los de permisos en
[agente.md](agente.md) §5). El onramp sin TTY (CI, Docker, scripts) es el
flag de CLI `enu --default-config` (ADR-015, G33): escribe ese mismo conjunto
de producto en `enu.toml` —y plantillas activas de `agent.toml`/`providers.toml`
si no existen, para que el harness quede usable, ADR-017/G35— y sale, o
—combinado con `-p`/`-e`— lo activa solo para ese proceso sin tocar disco. Es
superficie CLI del binario, no API sagrada: no añade nada a `enu.*` ni mueve
`enu.version.api`.

**Orden de arranque canónico**: core → plugins activados (topológico por
`requires`) → `init.lua` del usuario → evento `core:ready`. El
init del usuario va **último** a propósito: como en la pila de input el
registro más reciente gana, el usuario tiene la última palabra (keymaps,
theme, overrides) por construcción, sin sistema de prioridades.

| Firma | Semántica |
|---|---|
| `enu.plugin.current() -> {name, version, dir}` | Plugin en cuyo contexto corre el código. |
| `enu.plugin.list() -> {name, version, source: "builtin"\|"user", enabled}[]` | |
| `enu.plugin.reload(name)` ⏸ | Herramienta de desarrollo, **best-effort** (G2): suelta todos los handles del plugin (el core los etiqueta por dueño vía `plugin.current()`; los creados desde sus workers portan la identidad capturada en el spawn —§13, G56— y caen bajo el mismo dueño), emite `core:plugin.unload` (las extensiones limpian sus registros: tools, comandos...), vacía la caché de `require` del plugin y recarga su `init.lua`. Un plugin con efectos globales exóticos puede no descargarse limpio — para iterar, no para producción. |
| `enu.config.dir() -> string` [W] / `enu.config.data_dir() -> string` [W] | `~/.config/enu` y `~/.local/share/enu` (o equivalentes por plataforma). |

---

## 15. `enu.log` [W]

| Firma | Semántica |
|---|---|
| `enu.log.debug/info/warn/error(fmt, ...)` | A fichero en `data_dir`, con plugin de origen anotado. Desde un worker, el plugin anotado es la identidad capturada en el spawn, distinguida como `<plugin> (worker)` (§13, G56). `print` es alias de `info`. Nunca a la pantalla: la UI es de las extensiones. |

---

## 16. Resumen de disponibilidad en workers

| Disponible [W] | Solo estado principal |
|---|---|
| `task`, `fs` (salvo `watch`), `proc`, `sys`, `http`, `ws`, `text`, `re`, `search`, `json`, `toml`, `yaml`, `log`, `config.dir`, `config.data_dir` | `ui`, `events`, `fs.watch`, `worker.spawn`, `plugin` |

Las primitivas [W] que atribuyen por dueño (`log`, el registro de procesos
de `proc`) usan dentro de un worker la **identidad capturada en el spawn**
(§13, G56): fija durante toda la vida del worker, anotada como
`<plugin> (worker)`, jamás una consulta en vivo al estado del principal.

---

## 17. Estabilidad y evolución

- Congelar v1 = congelar **este documento**: firmas y semánticas solo cambian
  por adición; `enu.version.api` se incrementa con cada adición. **Nivel actual:
  `api = 4`** — el nivel 1 fue el congelado inicial; `enu.sys.pid()` (G32) lo
  subió a 2; los frames binarios de `enu.ws` (G52: `opts.binary` en `Ws:send`,
  segundo retorno de `Ws:recv`) lo subieron a 3; el control de redirects de
  `enu.http` (G54: `opts.max_redirects` en `request`/`stream` y recorte de
  cabeceras en saltos cross-host) lo subió a 4. Una adición nunca rompe
  firmas existentes: el código escrito contra el nivel 1 sigue siendo válido
  en los niveles siguientes.
- Detección de capacidades con `enu.has()`, nunca sniffing de versión.
- Namespaces de eventos `core:`/`ui:` y códigos de error de §1.4 reservados.
- Fuera de esta especificación (deliberadamente): toolkit de widgets, hooks
  del agente (`agent:*`), MCP, formato de `providers.toml`. Son contratos de
  sus extensiones, versionados aparte. El de providers ya tiene borrador:
  [providers.md](providers.md).
