# S39 — Extensión oficial `agent` (motor headless: turno, tools, permisos, hooks, eventos `agent:*`); CP-10 (agente.md)

Cuarto eslabón de la Fase 8: el **motor headless** del harness, Lua puro sobre la API pública
congelada (ADR-003) y sobre las extensiones `providers` (S36/S37) y `sessions` (S38). Plugin
embebido `internal/runtime/embedded/agent/` (`plugin.toml` name="agent", `requires=["providers",
"sessions"]` —el loader §14 los ordena topológicamente antes—; `init.lua` que cablea + módulo
`lua/agent/init.lua` + `lua/agent/tools_fs.lua`). INACTIVO por defecto (ADR-010), activable por
`enu.toml` `plugins.enabled=["providers","sessions","agent"]` (las tres explícitas: `requires`
solo ordena, NO auto-descubre/activa — el loader exige que la dependencia esté en el conjunto
descubierto, que para embebidas es lo nombrado en `enabled`).

**NO amplía api.md** (corolario de completitud satisfecho): `enu.events` (§4), `enu.task.future`/
`spawn` (§3), `enu.has("ui")` (§9, G20), `enu.fs`/`enu.toml`/`enu.config.dir` y los módulos
`providers`/`sessions` bastaron exactos. APILevel sigue en 2. **Sin hallazgos `G##`.** Código de
error de la extensión: `EAGENT` (forma ADR-009).

**El TURNO (`Session:send`, agente.md §2), el corazón:** anexa el mensaje de usuario al historial
(y al transcript si persiste), `resolve` el modelo (providers), y entra en un bucle: ensambla el
request canónico (§7: system por piezas base+`opts.system`; messages = historial; tools =
ToolDefs registradas) → hooks `request.pre` (pueden mutar/vetar) → `adapter.stream(req, config)`
→ **consume el iterador de Events** (providers.md §2.3: `for ev in iter`), re-emitiendo cada delta
en el bus como `agent:delta` y guardando el `done`. El agente NO re-ensambla deltas: usa el
`Message` completo del `done` (§2.3). Persiste el mensaje del assistant con `usage`/`model`
(sesiones.md §3). Si `stop_reason == "tool_calls"`: ejecuta cada `tool_call` del mensaje EN ORDEN
(P12: paralelo pospuesto), anexa los `tool_result` como un mensaje rol `user` (providers.md §2.2)
y **vuelve a pedir**. Termina cuando el modelo para sin tools, o al agotar `max_turns` (EAGENT
accionable, protección de loops, §10).

**Registro de TOOLS (`agent.tool`, agente.md §3):** `{name, description, schema, handler, permissions?}`.
UN único registro de proceso (§9). `M.tools()` enumera las ToolDef. `run_tool` ejecuta una tool
call: permisos → `tool.pre` → handler (bajo pcall) → `tool.post` → `tool_result`. Cualquier fallo
(permiso denegado, handler que lanza, veto de hook, tool desconocida) NO rompe el loop: produce un
`tool_result` con `is_error=true` y texto accionable que el modelo VE (§3). El handler recibe
`ctx = {session, cwd, progress(text), ask(question)}`. Tools de fichero básicas (dogfooding §3):
`read_file` (default="allow", nunca pide permiso ni headless, §5 amortiguador 1) y `write_file`
(default "ask" → DENY en headless: es la que CP-10 deniega).

**PERMISOS (agente.md §5), pipeline por tool call:** (1) default="allow" concede directo; (2)
`deny` de la política corta; (3) `allow` concede; (4) hooks `permission` (deny / `{grant=true}`);
(5) nadie decidió → si tool default="deny", denegado; si `mode="auto"`, concedido (explícito y
ruidoso, amortiguador 3); si `mode="ask"` Y `enu.has("ui")` → emite `agent:permission.asked` y
ESPERA un `future` sin timeout (G3), respondible con `agent.permission.respond(id, granted)`; si
`mode="ask"` SIN UI (HEADLESS, G20) → **DEFAULT DENY** con error ACCIONABLE (amortiguador 2:
nombra la tool, el patrón `allow` a añadir, y menciona `--auto-permissions`). Patrones
`tool[:argumento]` con comodín `*` (glob → patrón Lua); `arg_text` heurístico
(command/cmd/path/file).

**HOOKS-MIDDLEWARE (`agent.hook`, agente.md §4): registro PROPIO, NO el bus.** Puntos v1:
`request.pre`/`tool.pre`/`tool.post`/`permission`/`compact`. `fn(payload, ctx)` → nil (no opina)
| payload sustituto (sigue) | `{deny="razón"}` (corta, el PRIMER deny gana). Orden: priority
ascendente, luego registro. Cada hook bajo `pcall` (frontera robusta, ADR-008): uno que lanza se
loguea y se ignora. `Hook:remove()` lo desactiva. `agent._reset_hooks()` (helper de tests, no
contractual) limpia el registro entre casos.

**Eventos `agent:*` (agente.md §4, notificaciones por `enu.events`):** session.start/end,
turn.start/end, delta, message, tool.start/progress/end, permission.asked, error. **Atribución
obligatoria (G3):** un único helper `emit(session_id, name, payload)` pone `payload.session`
SIEMPRE — imposible olvidarlo. `agent:` es el namespace del plugin (no reserva del core, ADR-003).

**Persistencia (sesiones.md):** `agent.session{...}` crea/reanuda vía `sessions.open` (hereda lock
de escritor, §6) salvo `no_store=true` (sesiones in-memory de test). Cada mensaje (user, assistant
con usage/model, tool_results) se persiste con `Session:append_message`. **Reanudación (G18):**
`opts.resume=<id>` hace replay del transcript y repuebla el historial en memoria (la política de
replay para el LLM —desde el último `compact`— vive aquí). `Session:set_model` (G19) valida contra
providers y escribe una entrada `event` (sesiones.md §3).

**Decisiones / desviaciones.**
- **`requires` no auto-activa**: el `enu.toml` de test enumera las tres extensiones; `requires`
  solo da el orden de carga (verificado leyendo `loader.go`: `topoSort` opera sobre lo descubierto,
  y una embebida solo se descubre si `enabled` la nombra). Documentado para S43/S45.
- **System prompt (§7) parcial en S39**: solo base + `opts.system`. El índice de skills (§6),
  `enu.md` del repo y el TOFU/confianza (§11) son trabajo posterior (no en el alcance de S39).
- **Compactación (§8)** no implementada en S39 (el hook `compact` existe en el registro de puntos,
  pero el disparo automático y la estrategia por defecto son trabajo posterior). No bloquea CP-10.
- **`ask` del handler (ctx.ask)**: en headless sin UI devuelve `false` (coherente con §5 default
  deny); con UI usa el mismo flujo de `future` que los permisos.
- **Resultado del handler** normalizado a `content: Block[]`: string→bloque texto; tabla con
  `type`→un bloque; tabla sin `type`→se asume Block[].

**Adaptador de prueba (`toolstub`)**: el stub oficial declara `tools=false` (degradación
declarada, §3), así que los tests registran desde Lua un adaptador `toolstub` con `tools=true`
que en la 1ª vuelta emite una tool call y en la 2ª (cuando el ÚLTIMO mensaje trae un tool_result)
responde texto y para. Mirar solo el último mensaje (no todo el historial) lo hace correcto al
REANUDAR (una sesión reanudada ya contiene tool_results de turnos previos) — sutileza que costó
un ciclo de depuración en CP-10.

**🔎 CP-10 verde (agente headless mínimo, usable):** `TestCP10AgenteHeadless` arranca el runtime
HEADLESS (`WithForceUI(false)`, `enu.has("ui")`=false), ejecuta un turno con la tool de fichero
real `read_file` (lee un fichero de disco con `enu.fs`, se concede por ser solo lectura, su
contenido se realimenta y el done final cierra), PERSISTE la sesión en JSONL (se verifica que el
fichero bajo `data_dir/sessions/` contiene `meta`, los `message`, el nombre de la tool, el
contenido leído y la respuesta final), y luego REANUDA la sesión (replay repuebla el historial) y
pide `write_file` → permiso DENEGADO accionable (nombra "headless"/"write_file"/"allow"), el turno
NO se rompe, y el fichero NO se crea. Todo SIN una sola línea de UI (G20).

**Tests** (`agent_test.go`, arnés de S12 con las tres extensiones por `enu.toml`): carga+activa;
turno completo (tool llamada, resultado realimentado, done final, historial de 4 mensajes);
permiso denegado headless → tool_result is_error accionable; permiso concedido por `allow`; hooks
tool.pre/post (reescriben args/resultado) y veto por `{deny}`; eventos `agent:*` emitidos con
`session` (G3); CP-10 (persistencia + reanudación headless con tool de fichero + permiso denegado).
`CGO_ENABLED=0 go build`/`go vet ./...` verdes; `gofmt -l` limpio; `CGO_ENABLED=1 go test -race
-timeout 120s -count=2 ./internal/...` verde (~54 s); no regresiona S01–S38.

**Lo que reusará S40 (subagentes):** `agent.caps.*` (paquetes de caps con nombre, §9, ya
definidos como tablas inspeccionables), el registro único de tools (los handlers corren en el
principal vía proxy, §9), los permisos/hooks centralizados (el worker no los esquiva), y
`opts.parent` (sesión hija con `meta.parent`). **Lo que reusará S43 (chat):** los eventos
`agent:delta`/`agent:message`/`agent:permission.asked` (para pintar streaming y diálogos),
`agent.permission.respond` (responder el ask del usuario), y `agent.session`/`Session:send` como
contrato consumido igual que un tercero.

**Nota de proceso.** Tras dejar el código, los tests, los docs (puntero, bitácora, esta entrada)
y verificar build/vet/gofmt/race-count=2 verdes, se commiteó y pusheó SIN demora (lección de S38).
