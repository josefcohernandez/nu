---
title: "Informe de auditoría integral — 12 de julio de 2026"
type: "auditoria"
date: "2026-07-12"
status: "cerrada"
---
# Informe de auditoría integral — 12 de julio de 2026

Auditoría del repositorio completo (contratos de `docs/`, kernel Go de
`internal/runtime` + `internal/vmwasm`, extensiones Lua embebidas y CLI) en
busca de incoherencias de API, bugs ocultos, comportamiento inesperado,
limitaciones, anti-patrones y soluciones poco recomendables.

**Metodología.** 12 auditores independientes por dimensión (coherencia entre
contratos, deriva espec↔código del core y de las extensiones, bugs de
concurrencia/recursos/Lua, CLI, anti-patrones, limitaciones), con modelo
ajustado a la complejidad de cada dimensión. Cada defecto candidato pasó por un
**verificador adversarial independiente** instruido para refutarlo leyendo el
código y la espec (varios verificadores escribieron tests de reproducción
empírica). Resultado: 64 hallazgos brutos → **37 defectos confirmados**, **12
hallazgos descriptivos verificados** y **13 refutados** (apéndice A). La
dimensión de la frontera de marshaling Go↔WASM (`wire.go`, `handle.go`) se
revisó a mano tras un fallo de infraestructura; sus conclusiones están en §5
y §6.

Los ítems llevan id `A-##` para referenciarlos. Severidad: 🔴 alta, 🟡 media,
🔵 baja.

> **Estado de arreglos (mismo día).** Corregidos y blindados con tests en esta
> rama: A-02, A-04, A-05, A-06, A-08, A-09, A-10, A-11, A-13, A-14, A-15,
> A-16, A-18, A-20, A-23, A-25, A-26, A-32 (ver los commits que citan cada
> id). Lo pendiente está **registrado en el flujo canónico de diseño**
> ([problemas.md](../findings/README.md) / [pospuesto.md](../postponed/pospuesto.md)):
> el trío del scheduler (A-01/A-03/A-34) es **G44** —resuelta y **construida**
> el 2026-07-13 con la opción (b), `RunTasks` persistente (bitácora de
> implementacion.md)—, la superficie [W] de
> los workers (A-17) es **G45** —resuelta y **construida** el 2026-07-13 con
> la opción (a), marca worker-safe por snippet de preludio (bitácora de
> implementacion.md)— y el replay de `event` (A-19) es **G46** —resuelta y
> **construida** el 2026-07-13 con la opción (a) más la (c): precedencia
> `opts > transcript > agent.toml` y allow/deny reaplicados en orden—; las
> incoherencias
> documentales
> se resolvieron como **G47–G51** (la parte documental de A-33 quedó además
> recogida en modelo-ejecucion.md §limitaciones, que ya remite a G44); y las
> limitaciones A-35/A-37 son ahora **P33/P34** con disparador.
>
> **Cierre del informe (2026-07-14, rama `develop`): no queda ningún A-##
> pendiente.** El último lote se cerró así: A-07, A-12, A-21/A-22, A-36 y las
> dos mitades de A-38 **corregidos y blindados con tests** que citan su id
> (`sessions.list` compone con `nu.search.grep`, sin API nueva; la vía binaria
> de `ws.send` sí era hueco de API y siguió el flujo canónico: **G52**,
> resuelta por adición en api.md §8, nivel de API 2→3). A-28 aplicado en
> agente.md (⏸ en `agent.session`/`fork`; `close` anotada como síncrona a
> propósito). De los anti-patrones §6: A-39 → disparador **medible** para P32
> (pintado >30 ms/frame, ADR-007); A-41 → **P35** con disparador; A-42 →
> cuarentena honesta de `TestMCPToolServerError` (retry acotado que no puede
> dar falso verde + condición de salida en el propio test, y diagnóstico
> corregido: no es el handshake, apunta a la quiescencia de `runTaskLoop`);
> A-40 → el protocolo `\x01` de errores estructurados se extendió al camino de
> chunk y `wasmChunkError` (el parsing «CODE: mensaje») desapareció.

---

## 1. Bugs confirmados en el kernel Go

### 🔴 A-01 — Los timers `nu.task.every` mueren en silencio al terminar cada `RunTasks`

`internal/vmwasm/scheduler.go:57,143` — el canal de resultados `ch` y el
contador `outstanding` son **locales a cada invocación** de `RunTasks`. Al
alcanzar la quiescencia de primer plano (`liveFg == 0`) el bucle hace
`cancelAll()` y retorna: el `sleep` en vuelo de cada timer de fondo se cancela
(ECANCELED **no capturable**, así que la corrutina del `every` muere del todo,
no se pausa) y su resultado va a un canal que nadie volverá a leer. Un
`RunTasks` posterior no la reanima. **Verificado empíricamente** con dos
`RunTasks` consecutivos: el contador del `every` no avanzó en el segundo.

Consecuencia en el flujo real del binario (`Boot` → `RunTasks`, luego
`EvalTaskString` → `RunTasks`): todo `nu.task.every` que un plugin arranque en
su `init.lua` deja de latir al terminar el arranque, sin error ni log. Las
extensiones oficiales `chat` (spinner, `chat/init.lua:602`) y `toolkit` ya usan
`every`. Agravante: con >64 resultados pendientes en el canal viejo, las
goroutinas emisoras quedan fugadas. Relacionado: la limitación A-33 (el
comportamiento está comentado en el código como deliberado pero **ningún
contrato de `docs/` lo recoge**).

### 🔴 A-02 — `nu.proc.spawn` nunca libera pipes ni sale de los mapas de rastreo

`internal/runtime/proc.go:398-409,521-529` — `spawnProc` registra el proceso en
`s.procs` y `s.ownerHandles` y lanza el reaper `go p.wait()`, pero **no existe
ningún `untrackProc` en producción**: solo el reset en bloque de
`stopAllProcs`, que corre únicamente en `Runtime.Close`. El reaper solo
memoiza el código de salida; no cierra `rOut`/`rErr`. En el ciclo normal
spawn → uso → kill, cada spawn deja **2 descriptores abiertos y el objeto
anclado en dos mapas durante toda la vida del runtime**. Los comentarios citan
un `reapAndClose` que no existe (`proc.go:93,533`). Fuga ilimitada en procesos
de larga vida (el modo interactivo, la killer app).

### 🟡 A-03 — Trabajo encolado desde `EmitEvent`/`FeedInput` no despierta a `RunTasks`

`internal/vmwasm/scheduler.go:149-154` — el bucle solo despierta por un
resultado en `ch` o por `ctx.Done()`. Los `Eval` externos serializados por
`inst.mu` (watchers de fs, señales, input del driver) pueden hacer
`nu.task.spawn`/`Future:set` en sus handlers, encolando en `__ready`, pero nada
señala al select: la task nueva no corre hasta que venza la petición en vuelo
más próxima. **Verificado empíricamente**: una task spawneada desde un
`EmitEvent` a los ~50 ms corrió ~360 ms después, exactamente al vencer el
`sleep(400)` de la task principal. Es un wakeup *retrasado* sin cota (perdido
del todo si la petición en vuelo nunca termina, p. ej. un long-poll HTTP).

### 🟡 A-04 — Ni `Runtime.Close` ni `nu.plugin.reload` detienen los `wasmWatcher` de `nu.fs.watch`

Fusión de dos hallazgos confirmados por separado.
`internal/runtime/runtime.go:452-453` afirma que los watchers «viven en la
Instance/Pool wasm y los cierra su Close», pero `Instance.Close`
(`vmwasm.go:404-409`) solo hace `cancel()` + `mod.Close()`: no itera la tabla
de handles ni invoca destructores, y no existe ningún `stopAllWatchers` (a
diferencia de procs/streams/ws/greps). Además el wrapper Lua de `watch`
(`vmwasm_fs.go:335-352`) solo registra la suscripción de eventos, **no** el
handle como owned: en un reload, `__release_owner` cancela la suscripción pero
nadie llama a `Watcher:stop()`. Resultado: goroutine `run()` + fd de
fsnotify fugados **por cada reload** de un plugin con watch, y `deliver()`
puede seguir llamando `EmitEvent` sobre un módulo ya cerrado (error tragado
con `_ =`). El comentario de `Runtime.Close` es objetivamente falso.

### 🟡 A-05 — `nu.search.grep` silencia el resto del fichero cuando una línea supera 1 MiB

`internal/runtime/search.go:424-426` — el comentario promete «más allá de este
tope se ignora el resto de la línea», pero `bufio.Scanner` no trunca-y-sigue:
ante un token que supera el buffer máximo, `Scan()` devuelve `false`
definitivo con `Err() == bufio.ErrTooLong`, y el bucle `for sc.Scan()` **ni
comprueba `sc.Err()`**: todas las líneas restantes del fichero se pierden en
silencio, indistinguible de «no hubo más matches». Reproducido con un programa
mínimo (primera línea de 2000 B con buffer de 1000 B → cero líneas emitidas).
Un fichero generado con una línea larga al principio vuelve invisible todo su
contenido para el grep del agente.

### 🔵 A-06 — Carrera del header timer en `openStream`: puede cancelar un stream válido

`internal/runtime/stream.go:333,358` — `headerTimer.Stop()` tras `client.Do`
no cancela una `AfterFunc` ya disparada; si el timer vence en la ventana entre
el retorno de `Do` y el `Stop`, el `cancel()` envenena el contexto del body y
la rama de éxito (que no consulta `headerTimedOut`) entrega el Stream con el
ctx ya cancelado: el primer `next_chunk` lanza un ENET espurio pese a que
status y cabeceras llegaron bien. Ventana estrecha (endpoints que responden
justo al límite del timeout) pero real.

### 🔵 A-07 — El registro `Pool.workers` nunca retira los workers terminados

`internal/vmwasm/worker.go:61-68` — `registerWorker` añade y nadie borra: ni
`shutdown()`, ni `terminate()`, ni `StopWorkers`. Cada `nu.worker.spawn` deja
para siempre la estructura, sus canales y la entrada del mapa. La degradación
semántica es correcta (send/recv → ECLOSED/nil), pero es crecimiento monótono
en procesos de larga vida que spawneen workers periódicamente. De la misma
familia que A-02, A-04 y A-35 (recursos que solo se reclaman en `Close`).

---

## 2. Bugs confirmados en las extensiones Lua y el CLI

### 🔴 A-08 — Cancelar el turno durante una tool deja un `tool_call` persistido sin `tool_result`: transcript irreanudable

`internal/runtime/embedded/agent/lua/agent/init.lua:1069-1072,1120-1124` — el
mensaje del assistant (con sus bloques `tool_call`) se anexa al historial y se
**persiste antes** de ejecutar las tools; los `tool_result` solo después.
Entre medias `run_tool` suspende (IO del handler o, la ventana más ancha, el
`fut:await()` de una petición de permiso, que puede esperar indefinidamente).
`Session:cancel` aborta la task con un aborto no capturable por `pcall` (S08)
y el cleanup (`init.lua:987`) no repara historial ni transcript. El JSONL
queda terminado en un assistant con `tool_use` sin su `tool_result`: el
siguiente `send` o el `resume` reenvía ese historial y el provider responde
400 (la API de Anthropic exige el emparejamiento). Estado persistido
corrupto con escenario común.

### 🔴 A-09 — El exit code 3 del CLI se decide por matching de subcadenas sobre texto libre

`main.go:390-395` — `nu -p` decide «permiso denegado en headless» comprobando
si `ev.error` de `agent:tool.end` contiene a la vez `"headless"` y `"allow"`.
La extensión `agent` **ya emite el evento estructurado**
`agent:permission.denied` con `source` de enum cerrado
(`"deny"|"hook"|"default"|"headless"|"user"`, `agent/init.lua:738`) que otra
extensión (`mesh`) ya consume. Falso positivo demostrable: el mensaje de un
`default="deny"` contiene `"allow"` por construcción (`init.lua:381`), y basta
una ruta/argumento con `"headless"` para colar exit 3 con el mensaje engañoso
«ejecuta con --auto-permissions». Arreglo: consumir el evento estructurado y
marcar exit 3 solo con `source == "headless"`.

### 🔴 A-10 — `-e` y `-p` combinados: `-e` gana en silencio

`main.go:116,206-211` — `headless` se calcula como OR sin exclusividad y
`runWith` resuelve con `if opts.eval != "" { return runEval }`: con ambos
flags, `-p`/`--continue`/`--auto-permissions`/`--model` se descartan sin
mensaje y el proceso sale 0. El propio doc del paquete reserva el código 2
para «flags incompatibles» (`main.go:45-52`) y el código ya trata así otras
combinaciones. Un script de CI cree haber ejecutado un turno de agente que
nunca corrió. (El verificador rebaja a media: invocación inusual, pero engaño
real a scripts.)

### 🟡 A-11 — El contador de permisos pendientes del chat se corrompe al mezclar sesiones

`embedded/chat/lua/chat/init.lua:515,529,542,581` — el mismo campo
`self.pending_count` lo escriben la rama de asks *ajenos* (incremento, G3) y
las ramas de asks *propios* (sobrescritura con `#ask_queue`/`0`): se pisan
mutuamente y la statusline miente en cuanto se solapan sesiones (2 ajenos +
1 propio → muestra 1; al responder el propio → 0, borrando los ajenos aún
pendientes). Defecto adicional confirmado: el contador ajeno tampoco se
decrementa nunca (no hay suscripción a la resolución remota).

### 🟡 A-12 — El adaptador Gemini reordena texto y `tool_calls` al ensamblar el Message

`embedded/providers/lua/providers/adapter_gemini.lua:175,186` — los
`functionCall` se appendean al recibirlos pero todo el texto acumulado se
inserta al final **siempre en posición 1** (`table.insert(content, 1, …)`).
Con parts `[functionCall, text]` el Message canónico persistido invierte el
orden real y funde todo el texto en un bloque. No viola una obligación
explícita de providers.md (el verificador lo reclasifica como incorrección
latente de reconstrucción), pero el transcript deja de ser fiel al modelo.

### 🔵 A-13 — Cancelar el turno con un ask pendiente deja el diálogo fantasma y la entrada fugada

`embedded/agent/lua/agent/init.lua:401-406,285-292` — `pending_asks[id]` solo
se limpia en `respond`; `Session:cancel` programático (API pública, G27) no la
retira ni emite nada para cerrar el modal. El id sigue válido: un `respond`
tardío escribe un future que nadie espera. Baja reachability (el camino esc
del usuario sí limpia), pero fuga + UI huérfana reales.

### 🔵 A-14 — Una tool desconocida emite `agent:tool.end` sin `tool.start` previo

`embedded/agent/lua/agent/init.lua:710,723-727` — la rama `tool == nil`
retorna `err_result` (que emite `tool.end`) **antes** del
`emit("tool.start")`. Cualquier UI que empareje start/end descuadra su
contador/spinner cuando el modelo alucina un nombre de tool. Arreglo trivial:
mover el `emit` del start por encima del chequeo.

### 🔵 A-15 — Un provider sin `base_url` revienta con «concatenate nil» en vez de EPROVIDER

`embedded/providers/lua/providers/init.lua:159,284` — `build_index` valida
`adapter` e `id` con EPROVIDER accionable, pero nunca `base_url` (que
providers.md:143 marca como requerido); los tres adaptadores concatenan sin
comprobar nil (`adapter_anthropic.lua:532`, etc.). Quien edite
`providers.toml` a mano recibe un error de runtime Lua en el primer turno en
vez del error de datos que la política de §1 promete.

### 🔵 A-16 — `Proc:kill("KILL")` se degrada en silencio a la señal 0 (no mata) y envenena los kills siguientes

`internal/runtime/vmwasm_proc.go:174-177` + `vmwasm_text.go:329-337` —
`argInt` devuelve 0 para tipos no numéricos, y `syscall.Signal(0)` es la sonda
de existencia. Agravante confirmado: `killSignal` (`proc.go:559-566`) fija
`killed=true` junto al envío sin condicionarlo al éxito, así que el `kill`
fallido **cortocircuita todos los kills posteriores** (cleanup, finalizer,
scheduler): proceso huérfano e inmatable. No es deriva de espec (la firma
calla sobre tipos), pero un envío de señal que no señala sin error es peor que
un EINVAL.

---

## 3. Deriva espec ↔ código (el contrato promete una cosa, el código hace otra)

### 🔴 A-17 — Los wrappers Lua de `extraPreludio` no se copian a los workers: módulos enteros marcados [W] ausentes

`internal/vmwasm/worker.go:137-179` — `spawnWorker` copia los módulos y las
primitivas del registro, pero **nunca `inst.pool.extraPreludio`**, donde viven
los wrappers públicos registrados con `AddPreludio` desde
`internal/runtime/vmwasm_*.go`: `nu.log.*`, `nu.re.compile`,
`nu.text.wrap/markdown/highlight/diff`, `nu.proc.spawn` y métodos,
`nu.ws.connect`, `nu.http.stream`, `nu.search.grep`. Todo eso está declarado
[W] en api.md §16 y **no existe** en un worker (verificado empíricamente: los
seis probados, ausentes). Los thunks host sí cruzan; falta exactamente la capa
de wrappers. Es la deriva más gorda de la superficie [W].

### 🔴 A-18 — `nu.plugin.*` se filtra a los workers y `plugin.reload` re-entra la VM principal desde la goroutine del worker

`internal/vmwasm/worker.go:186-197` — `workerGrants` excluye
`ui./worker./loader.` pero no `plugin.`, así que un worker sin caps monta
`nu.plugin.current/list/reload` (verificado empíricamente), violando api.md
§13/§16 («solo estado principal»). Lo grave: la HostFn de `reload` copiada es
el mismo closure que cierra sobre el runtime principal y hace `Eval` sobre la
Instance principal **desde la goroutine de fondo del worker** — exactamente la
reentrada multi-hilo al estado Lua principal que el modelo del navegador
(ADR-004) prohíbe.

### 🔴 A-19 — Los cambios en caliente persistidos como `event` se pierden al reanudar

`embedded/agent/lua/agent/init.lua:1337-1391,1569-1576` —
`set_model`/`set_thinking`/`allow`/`deny` escriben entradas `event` en el
transcript, pero el replay de `resume` solo reconstruye `message` y `compact`:
las `event` se descartan. sesiones.md §3 define regla de replay explícita
(«para datos repetibles… la última gana», con el cambio de modelo como ejemplo
canónico, reforzado por G19). Matiz del verificador: para `allow`/`deny`
(acumulativos) la regla no está especificada y G18 (opts efímeros) introduce
texto contrapuesto para el modelo — hay además una grieta de espec que
resolver aquí, no solo código.

### 🟡 A-20 — `nu.plugin.reload` en wasm no libera los handles Go por dueño: los procesos de un plugin sobreviven a su recarga (viola G2)

`internal/runtime/vmwasm_loader.go:193` + `handles.go:94` — el reload solo
invoca `__release_owner` (registro Lua del preludio); el registro Go-side
`scheduler.releaseOwnerHandles` **no tiene ningún caller de producción** (su
antiguo caller murió al retirar gopher). Pero `spawnProc` etiqueta cada
proceso justo en ese registro Go. Un `nu.proc.spawn` del `init.lua` de un
plugin sobrevive a `nu.plugin.reload`, contradiciendo el contrato que el
propio `proc.go:116-118` documenta («un spawn de su init.lua no debe
sobrevivir a la recarga»). Código muerto que era el mecanismo de una garantía.

### 🟡 A-21 — El transcript de un subagente-worker queda vacío (solo `meta`): la «sesión hija auditable» no existe

Fusión de dos hallazgos. `embedded/agent/lua/agent/subagent.lua:214-270,295` —
en modo worker se crea `proxy_session` (que escribe `meta.parent`, **adquiere
el lock** y consume un fichero) pero `run_worker` jamás anexa nada: la
conversación vive en el `history` local del worker y solo cruza el digesto. El
comentario del código («el worker no persiste; manda el digesto y el padre lo
anexa») describe una persistencia no implementada. El modo task sí persiste
todo: asimetría directa contra sesiones.md §7 («su transcript es una sesión
propia… auditable con las mismas herramientas»).

### 🟡 A-22 — El subagente-worker ignora `thinking` y `skills`

`subagent.lua:234-244` + `subagent_worker.lua:115-122` — el mensaje `init` no
reenvía `thinking` ni `skills` y el worker arma el request sin campo
`thinking`: un subagente con `thinking={mode='budget'}` corre sin razonamiento
en silencio; el índice de skills no se inyecta en el system y `opts.skills` no
acota nada. agente.md §9 define las opts del spawn como «las de
agent.session», que incluyen ambas. El modo task las respeta.

### 🟡 A-23 — Las entradas `compact` nunca incluyen el campo `covers` que sesiones.md exige

`embedded/agent/lua/agent/init.lua:1245-1250` — sesiones.md §3 define
`covers: integer` sin `?` (obligatorio) y el agente lo calcula (`replaced`)
pero solo lo emite en el evento, no lo persiste. `grep covers internal/` → 0
resultados: nunca se escribe, lee ni testea. Arreglo de una línea.

### 🟡 A-24 — §1.5 de api.md promete `opts.timeout_ms` en «toda función con IO»; casi ninguna lo tiene

`docs/contracts/api.md:81` vs las firmas de §5/§6/§8/§11 y el código
(`vmwasm_fs.go:46` descarta args extra) — solo `http.request/stream`,
`proc.run` y `ws.connect` implementan timeout. El verificador reencuadra: es
ante todo una **incoherencia interna de la espec** (§1.5 sobrepromete frente a
sus propias firmas); el código implementa las firmas fielmente. `nu.fs.read`
sobre un NFS colgado bloquea la task para siempre con el contrato prometiendo
ETIMEOUT. Hay que decidir: o se rebaja §1.5, o es un G## que añade opts a las
firmas.

### 🔵 A-25 — El Digest de un subagente siempre reporta `stop_reason='end'`

`subagent_worker.lua:158` y `subagent.lua:313` — ambos modos descartan el
`stop_reason` canónico del `done` (`max_tokens`, `refusal`…) y fuerzan
`'end'`: el padre no puede reaccionar a truncamiento o rechazo. En modo task
el motor ni siquiera expone el dato (haría falta un `last_stop_reason` análogo
a `last_usage`).

### 🔵 A-26 — Dos comentarios que mienten sobre el código que anotan

Menores pero peligrosos para el mantenedor:

- `internal/vmwasm/vmwasm.go:339`: `mu` documentado como «sólo protege contra
  reentrada accidental en tests, no concurrencia real», cuando hoy es **el
  candado de concurrencia real de producción** que serializa
  RunTasks/EmitEvent/FeedInput/pintor (el sustituto wasm del token del
  scheduler). Fiarse del comentario y quitar el lock reintroduce un data race
  directo.
- `main.go:84` vs `main.go:109`: `promptSet` documentado como «`-p ""` es
  válido si se pasó» pero calculado como `prompt != ""` — exactamente lo
  contrario.

---

## 4. Incoherencias entre documentos de diseño

### 🔴 A-27 — ADR-002 (Lua 5.1 vía gopher-lua) sigue «Aceptada» pese a que el baseline vigente es Lua 5.4/PUC/wazero

`docs/decisions/adr/README.md:39-60` — la disciplina del ADR («nunca reescribir, marcar
Reemplazada») se aplicó a ADR-011 («Reemplazada por ADR-020») pero no a
ADR-002, cuya decisión de implementación quedó igual de obsoleta por la misma
migración M16/M17 (gopher-lua ya ni está en `go.mod`; api.md §1.2 rige 5.4).
Matiz correcto del verificador: el núcleo de ADR-002 («Lua como lenguaje de
extensión» frente a Starlark/JS) sobrevive — no toca un «Reemplazada por»
total sino una anotación de estado al estilo de ADR-011 que acote qué parte
quedó obsoleta y señale a ADR-019/020.

### 🟡 A-28 — `agent.session` y `Session:fork` sin marcador ⏸ pese a hacer IO suspendiente

`docs/contracts/agente.md:26,34` — abrir con `resume` hace replay (`nu.fs.read` ⏸) y
adquiere el lock con `nu.fs.write{exclusive}` ⏸ (sesiones.md §6); `fork` copia
el prefijo al transcript hijo. Sus hermanas `send`/`compact` sí llevan ⏸.
Ojo: el hallazgo original incluía `Session:close()` y el verificador lo
**excluye con razón** — close es deliberadamente síncrono para poder llamarse
desde `nu.task.cleanup` (patrón de `Proc:kill`/`Ws:close`).

### 🟡 A-29 — `EAGENT` se cita en chat.md, adr.md y problemas.md pero el contrato del agente nunca lo acuña

`docs/contracts/chat.md:180-181`, `adr.md:906`, `problemas.md:929` vs `agente.md` (única
mención de error: EINVAL, §10) — providers.md sí acuña formalmente EPROVIDER;
EAGENT solo existe en documentos que no son el contrato de `agent.session`.
O se declara en agente.md o se retira de los demás.

### 🟡 A-30 — `timeout_ms = 0`: «sin límite» en proc, EINVAL en http/ws

`vmwasm_proc.go:240-242` (0 permitido = sin límite) vs `vmwasm_http.go:250` y
`vmwasm_ws.go:148-150` (0 → EINVAL) — la espec §1.5 unifica la opción pero no
define el valor frontera, y ningún documento registra la divergencia como
intencional. Mismo valor, semántica contradictoria entre módulos de IO.

### 🟡 A-31 — chat.md §5 enseña `agent.permission.respond(id, "once")`, que en la API real **deniega**

`docs/contracts/chat.md:115` vs `embedded/agent/lua/agent/init.lua:291`
(`p.future:set(granted == true)`) — `"once" == true` es `false` en Lua: el
ejemplo literal del contrato produce lo contrario de lo documentado. La UI
oficial se salva porque pasa booleano; un integrador tercero que siga el
contrato deniega creyendo conceder. El defecto está en el documento.

### 🟡 A-32 — `writeAtomic` ignora el umask: `nu.fs.write` deja 0644 donde `{exclusive}` y `append` respetan el umask

`internal/runtime/fs.go:86` — el `os.Chmod(tmp, 0644)` explícito no pasa por
el umask; `writeExclusive`/`append`/`copy` usan `OpenFile` y sí. Con umask
077, `nu.fs.write(path, secreto)` queda world-readable mientras la variante
exclusiva queda 0600 — regresión de seguridad e incoherencia entre ramas de la
**misma primitiva**, contradiciendo la doc del propio fichero (fs.go:37-38).
Confirmado también en sobrescritura (chmod fuerza 0644 donde OpenFile conserva
los permisos previos).

### 🔵 A-33 — El inventario del kernel en arquitectura.md omite `nu.yaml` y `nu.search`

`docs/core/arquitectura.md:37-45` — la fila `data` enumera «Codecs JSON y TOML»
omitiendo YAML (api.md §12, necesario para skills), y `nu.search` (api.md §11)
no aparece en ninguna fila. (`nu.sys` sí está representado como «entorno» en
la fila io: esa parte del hallazgo original quedó refutada.)

---

## 5. Limitaciones actuales (verificadas; las silenciosas son las graves)

### 🔴 A-34 — El modo interactivo no bombea el scheduler: la killer app no puede correr su turno sobre el driver de TTY

`internal/runtime/driver.go:130-158` — `RunTasks` solo se invoca en Boot y en
los dos Eval headless; el bucle `drive()` solo hace FeedInput/Eval/flushFrame.
Cualquier `nu.task.spawn` desde un keymap o handler durante la sesión
interactiva encola y **nadie reanuda jamás**. El chat oficial ejecuta el turno
exactamente así (`chat/init.lua:326`). El propio código lo reconoce
(`vmwasm_loader.go:100-101`: «el bucle interactivo del driver de TTY es aparte
(pendiente…)»). Es la pieza pendiente más estructural del repo: A-01 y A-03
son en parte síntomas de que el bombeo continuo del scheduler no existe aún.

### 🔴 A-35 — `Task:cancel()` no interrumpe una primitiva ⏸ en vuelo

`internal/vmwasm/host.go:24` (`HostFn` sin `context.Context`) +
`scheduler.go:261-283` — al cancelar, la task ve ECANCELED al instante y corre
sus cleanups, pero la goroutine del hostcall (`fs.write`, `http.request`,
`proc.run`…) sigue hasta su fin natural: **sus efectos aterrizan después del
cleanup**. Peor que la «cancelación cooperativa» documentada en
modelo-ejecucion.md: aquí el punto de suspensión ya se alcanzó. Solo `sleep`
observa el ctx. Cerrar esto pide cambiar la firma de `HostFn` (o una variante
con ctx) — decisión de espec, no parche.

### 🟡 A-36 — El watchdog no cubre cleanups ni handlers del bus: un bucle infinito ahí congela el runtime con `inst.mu` retenido

`internal/vmwasm/host.go:427-435,487-517` — el count-hook se arma dentro de la
corrutina de cada task («un hilo nuevo no hereda el hook del padre en 5.4»),
pero `__finish` corre los cleanups en el hilo del scheduler sin hook, igual
que los handlers de `nu.events` vía `EmitEvent`. Un `while true do end` en un
cleanup bloquea painter, FeedInput, EmitEvent y el bucle, sin EBUDGET posible.
Contradice la promesa de robustez por watchdog + pcall en cada frontera
(CLAUDE.md/ADR-008) para esas dos fronteras concretas.

### 🟡 A-37 — La tabla de handles de la Instance es monotónica: solo `Region:destroy` libera

*(Revisión manual de la frontera Go↔WASM.)* `FreeHandle` tiene **un único
caller en todo el kernel** (`vmwasm/ui.go:195`). `Ws`, `Proc`, `Stream`,
`Watcher`, `GrepIter`, `Re` y todos los `Block` de text/markdown entran por
`AllocHandle` y no salen jamás. Es una decisión documentada
(`vmwasm_ws.go:28-32`: liberar rompería la idempotencia de `close`, que daría
ECLOSED al segundo intento), pero el coste es retención sin cota del
`handleEntry` **y del objeto Go** apuntado durante toda la vida de la
Instance. Los `Block` son el caso caliente: cada render de markdown/text en
una sesión interactiva larga añade entradas. Nota positiva: los ids no se
reutilizan (`next` monótono), así que no hay ABA; y el codec de `wire.go` está
bien defendido (guardia anti-OOM en `count()`, strings byte-seguros G11,
sentinel NULL). Limitación de diseño a registrar (¿un P##?) más que bug.

Derivada de la misma revisión: el mecanismo `dispatchHandle` que permite a un
método liberar su propio handle **solo existe en el despacho síncrono**
(`handle.go:97-126`, por la carrera real que hubo en M15 con el camino
suspendente). Si algún día un método suspendente necesita liberar (un
`close` ⏸), no hay vía: restricción latente sin documentar fuera del
comentario.

### 🟡 A-38 — Otras limitaciones confirmadas, en corto

- **`nu.ws.send` siempre manda frame de texto** (`ws.go:148`): bytes no-UTF-8
  → un servidor conforme cierra con 1007; no hay vía binaria y api.md no
  restringe `data` a texto.
- **`sessions.list` lee cada JSONL completo** para quedarse con la línea
  `meta` (`sessions/init.lua:391-393`): listar sesiones cuesta O(bytes
  totales del proyecto) en IO y memoria; con transcripts de MB el picker lo
  paga entero.
- **A-01/A-33 como limitación documental**: la muerte de los `every` por
  quiescencia está comentada en el código (`scheduler.go:79-80`) pero no
  existe ni en modelo-ejecucion.md §limitaciones ni como P## con disparador —
  limitación silenciosa por partida doble.

---

## 6. Anti-patrones y soluciones asumidas poco recomendables

### 🔴 A-39 — El veto de rendimiento de la migración a wasm falló (2,45×–4,5×) y se aceptó como excepción permanente sin backend de vuelta

`docs/archive/migracion-vm.md:276` (bitácora M15) — el hito de veto exigía camino
caliente dentro de 2× de gopher; midió 4,5× (turno headless 574 µs → 2601 µs)
y 2,45× (markdown streaming). La causa es arquitectónica (~50% intérprete
PUC-en-wasm + ~33% maquinaria de cruce), la decisión humana fue proceder con
excepción razonada, y **dos sesiones después (M17) se retiró gopher del
binario**: el peaje ya no tiene alternativa y la mejora vive en P32 sin
disparador operativo («que algún día moleste»). Es la deuda estructural más
cara del repo y merece un disparador medible (p. ej. latencia percibida en el
chat interactivo cuando A-34 se cierre).

### 🟡 A-40 — `wasmChunkError` reconstruye errores estructurados parseando el texto «CODE: mensaje»

`internal/runtime/eval.go:247-259` — en el camino de `EvalString` la tabla de
error no sobrevive y se recupera el `StructuredError` con `strings.Cut(msg,
": ")` + `IsReservedCode`. Best-effort confeso, con falso positivo posible: un
`error("ENOENT: no encontré X")` de código de usuario se reclasifica como
error del core y los llamantes deciden códigos de salida por ese `code`.
`EvalTaskString` demuestra que el protocolo estructurado (separadores `\x01`)
es viable; el camino de chunk quedó con el apaño.

### 🟡 A-41 — `nu.plugin.reload` es best-effort ante colisión de nombres de módulo (G2), documentado pero sin resolver

`internal/vmwasm/loader.go:8` + `decisiones-implementacion.md` (S13) — la purga de
`package.loaded` enumera los `.lua` del plugin recargado, pero el espacio de
nombres es global: dos plugins con un módulo `utils` y el reload de uno puede
dejar cargado el del otro. La resolución registrada fue explícitamente «no
resolver». Garantía debilitada de una herramienta que guia-plugins.md presenta
como fiable para desarrollo.

### 🟡 A-42 — Tests flaky conocidos y tolerados sin skip/retry ni issue

`decisiones-implementacion.md:3529` — `TestMCPToolServerError` es flake documentado
bajo la suite completa con `-race -count=2` («pasa aislado») y se decidió solo
anotarlo; `TestSearchGrepEarlyStopNoLeak` aparece señalado como «preexistente
y ajeno» en la bitácora post-M17 (ventana de 2 s contando goroutines). El
riesgo clásico: la próxima regresión intermitente real de concurrencia se
descartará por reflejo como «el flake de siempre». Mínimo recomendable:
marcarlos (retry explícito o quarantine) y dejar issue con condición de
salida.

> **Actualización (mismo día, `origin/main` `dee8be2`, PR #71):** la mitad
> `TestSearchGrepEarlyStopNoLeak` de este hallazgo queda **resuelta en main**
> — y de la forma que este anti-patrón advertía: el «flake preexistente y
> ajeno» era en realidad **una fuga real** (el port wasm de M13b nunca
> registró el cleanup del grep que el backend gopher sí registraba, y M17
> retiró el único camino que lo hacía; el runner pequeño la ocultaba porque
> el margen de goroutines escalaba con `NumCPU`). El arreglo añade
> `GrepIter:close` síncrono e idempotente, lo registra automáticamente en
> `nu.task.cleanup` desde el wrapper, y endurece el test para consultar el
> registro del scheduler directamente (ya no puede dar falso verde). Queda
> vigente la mitad `TestMCPToolServerError`.

---

## 7. Recomendaciones priorizadas

**Romper ya (bugs con corrupción o fuga en el flujo shipping):**
1. A-08 (transcript irreanudable al cancelar) — persistir assistant y
   tool_results de forma atómica, o reparar en `_finish_turn`/resume.
2. A-01 + A-34 + A-03 en conjunto: es el mismo problema de fondo — el
   scheduler no tiene bucle de vida continuo. Diseñar el bombeo del modo
   interactivo (la pieza «pendiente» reconocida) resuelve o reencuadra los
   tres; parchearlos por separado sería pelear síntomas.
3. A-02 y A-04 (fugas de fds/goroutines por spawn y por watch/reload) +
   A-20 (reload no mata procesos del plugin): el registro Go por dueño ya
   existe y está huérfano; recablearlo cierra dos de los tres.
4. A-32 (umask) — una línea (`OpenFile` en vez de `Chmod`), impacto de
   seguridad.
5. A-09 y A-10 (CLI): consumir `agent:permission.denied` y validar
   exclusividad `-e`/`-p` (exit 2).

**Decidir en los documentos antes de tocar código (flujo G##):**
- A-17/A-18 (superficie [W] y aislamiento de workers) — probablemente el G##
  más urgente: hay promesa de espec incumplida y una violación del modelo de
  concurrencia.
- A-24 (§1.5 timeout_ms), A-30 (semántica de 0), A-19 (replay de `event`,
  con la tensión G18/G19), A-29 (EAGENT), A-28 (⏸), A-31 (chat.md), A-27
  (ADR-002), A-33 (inventario).
- A-35 (HostFn con ctx) y A-37 (ciclo de vida de handles) — candidatos a P##
  con disparador si no se abordan ahora.

**Higiene:**
- A-23, A-25, A-14, A-26 son arreglos de minutos cada uno.
- A-42: quarantine con condición de salida para el flake que queda
  (`TestMCPToolServerError`); el del grep ya se resolvió en main (era una
  fuga real, ver la actualización en A-42).

---

## Apéndice A — Hallazgos refutados por la verificación adversarial

Trece candidatos cayeron al verificarlos; se listan porque delimitan lo que
**sí** está bien y ahorran re-auditarlo:

| Candidato | Por qué se refutó |
|---|---|
| El agente escribe en `meta` violando providers.md | La «Regla meta» aplica a artefactos provider→agente; no hay contradicción |
| `parent?` en opts de agent.session incoherente | Es la palanca pública sobre la que fork/spawn construyen `meta.parent` |
| `fs.append` prometido como atómico | api.md scoped el temp+rename a `write`; sesiones.md §6 ya regula el append concurrente |
| `nu.has` solo reconoce "ui" | `caps().images` es `false` cableado: no hay divergencia observable, y es interrogación válida |
| watchdog emite `misbehaved` con plugin="user" fijo | La espec no promete identificar al culpable; mismo comportamiento que la VM de referencia |
| default='allow' cortocircuita el deny | agente.md §5 lo ordena explícitamente así (amortiguador 1) |
| `Session:send` devuelve nil «en silencio» | Diseño normativo: la «garantía de error visible» va por `agent:error` |
| El deny se evade con rutas no canónicas | Limitación deliberadamente pospuesta con disparador (P17) |
| Cancelación no propagada a subagentes-worker | Regla de la casa: quien abre cierra vía `nu.task.cleanup` (agente.md:103) |
| openai-compat rompe tool call troceada sin `index` | El formato de OpenAI mandata `index` en cada chunk; input no producible |
| El thinking se pinta después de la respuesta | Orden documentado como decisión de UI en el propio código |
| `--default-config` no transaccional | Escrituras idempotentes; el reintento converge |
| `--model`/`--auto-permissions` ignorados sin `-p` | `--continue` sí actúa; los modificadores sin acción son convención CLI estándar |

*(Un hallazgo duplicado de A-35 quedó sin verificador por un fallo de red; lo
cubre el veredicto de su gemelo. Un descriptivo fue descartado por
imprecisión factual.)*
