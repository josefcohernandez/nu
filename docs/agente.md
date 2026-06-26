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

- Modo scripting/CI gratis: `nu -e "script.lua"` puede usar el agente sin
  terminal interactivo.
- Los subagentes pueden correr en workers (sin `ui`) sin caso especial.
- La UI oficial no tiene acceso privilegiado: la API pública es suficiente o
  está incompleta.

## 2. Sesiones y turno

```
agent.session(opts) -> Session
  opts: { model: "proveedor/modelo", system?, cwd?, tools?: string[],
          skills?: string[], permissions?: Permissions, parent?,
          resume?: string }                          -- id: reabre en vez de crear

Session:send(content: string|Block[]) ⏸ -> Message  -- ejecuta el turno completo
Session:cancel()                                     -- cancela el turno en curso  [⏸ pospuesto P22]
Session:fork(at?: integer) -> Session                -- sesiones.md §5             [⏸ pospuesto P22]
Session:compact() ⏸                                  -- compactación manual        [⏸ pospuesto P22]
Session:set_model(model: string)                     -- cambio en caliente (G19)
Session.id / Session.usage -> { context_tokens, cost_usd, turns }
```

> **Estado de implementación.** La extensión `agent` `0.1.0` implementa
> `send/spawn/set_model/close`; `cancel`, `fork`, `compact` y `clear_queue`
> (abajo) son **implementación diferida** ([pospuesto.md](pospuesto.md) **P22**).

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
`Session:clear_queue()`). *(Implementación diferida:
[pospuesto.md](pospuesto.md) **P23**; depende de `cancel`, P22.)*

**Reanudación (G18)**: `opts.resume = <id>` reabre una sesión existente en
vez de crearla: replay del transcript ([sesiones.md](sesiones.md) §3) y
adquisición del lock de escritor (§6, con su flujo de conflicto — fork,
solo lectura o forzar). El resto de `opts` aplica igual que en una sesión
nueva: son estado efímero del proceso, no se persisten ni reescriben
historia. El id sale del listado de sesiones (sesiones.md §7).

**Cambio de modelo (G19)**: `Session:set_model("proveedor/modelo")` valida
contra el registro de providers, escribe una entrada `event` en el
transcript ([sesiones.md](sesiones.md) §3) y aplica desde el siguiente
request; con un turno en vuelo, al ensamblar la siguiente iteración (como
la cola de G4), nunca a mitad de un stream.

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
  remota con `agent.tool{...}` y su handler habla JSON-RPC por `nu.proc`.

## 4. Hooks

Dos mecanismos, deliberadamente separados:

**Notificaciones** (fire-and-forget, bus del core `nu.events`, namespace
`agent:`): `session.start`, `session.end`, `turn.start`, `turn.end`,
`delta`, `message`, `tool.start`, `tool.progress`, `tool.end`, `compact`,
`error`, `permission.asked`. Para pintar, loggear, observar. *(El evento
`compact` solo se emitirá cuando exista la compactación automática:
[pospuesto.md](pospuesto.md) **P25**.)* El namespace
`agent:` no es una reserva del core (el core no sabe de agentes, ADR-003):
es el namespace del plugin `agent`, protegido por la unicidad del nombre de
plugin como cualquier otro (G26, [api.md](api.md) §4).

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
el diálogo). **En headless — no existe `nu.ui`; el test es `nu.has("ui")`
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

Concurrencia de asks (G3): varias sesiones pueden tener asks pendientes a
la vez; cada una espera su `future` **sin timeout** (un timeout→deny
metería denegaciones sorpresa no deterministas). La UI es responsable de
hacer visibles los pendientes.

Esto es la capa *blanda* (frente al modelo). La capa *dura* para código no
confiable son los workers con `caps` ([api.md](api.md) §13): un subagente en
worker sin `proc` no ejecuta procesos, opine quien opine.

## 6. Skills

> **Implementación diferida** ([pospuesto.md](pospuesto.md) **P24**). El
> ensamblado del system prompt de la `0.1.0` aún no descubre skills ni inyecta
> su índice; `agent.skills.list()` no está expuesto. Esta sección describe el
> diseño; su construcción espera el disparador de P24.

Compatibles con el formato del ecosistema existente: directorio con
`SKILL.md` (frontmatter YAML: `name`, `description` — vía `nu.yaml`).

- Descubrimiento: `config.dir()/skills/` (usuario) + `<repo>/.nu/skills/`
  (proyecto). `agent.skills.list() -> SkillInfo[]`. El contenido del repo
  está sujeto al modelo de confianza de §11.
- Inyección en dos fases (economía de contexto): el system prompt lleva solo
  el **índice** (nombre + descripción); el contenido completo se carga bajo
  demanda mediante la tool interna `skill` que el modelo invoca. 
- Por sesión/subagente: `opts.skills = { "review", "deploy" }` limita el
  índice visible.

## 7. System prompt

Ensamblado por piezas ordenadas: base de la extensión → índice de skills →
fichero de contexto del proyecto (`nu.md` en la raíz del repo, si existe) →
`opts.system`. Los hooks `request.pre` pueden retocar el resultado. Cada
pieza es sustituible por configuración — no hay prompt mágico inaccesible.

> **Implementación diferida** ([pospuesto.md](pospuesto.md) **P24**). La `0.1.0`
> ensambla solo `base → opts.system`: las piezas de **índice de skills** y de
> **`nu.md`** (con su puerta TOFU de §11.2) aún no se inyectan.

## 8. Compactación

> **Implementación diferida** ([pospuesto.md](pospuesto.md) **P25**). El hook
> `compact`, el replay desde el resumen y el soporte de entradas `compact` en el
> store existen; lo que la `0.1.0` aún no hace es **disparar** la compactación al
> superar el umbral ni emitir el evento `agent:compact`.

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
de compactación, política de retención de sesiones ([P10](pospuesto.md)),
permisos globales. La precedencia es la estándar: defaults < global <
proyecto (`<repo>/.nu/agent.toml`) < sesión (`opts`) — con la excepción de
seguridad de §11: los permisos del proyecto solo recortan.

## 11. Modelo de confianza del contenido del repo (G14)

El repo no es el usuario: su config la escribió un tercero. Dos reglas, sin
sandbox ni diálogos constantes:

1. **El repo solo recorta permisos, jamás amplía.** Los `deny` de
   `<repo>/.nu/agent.toml` se honran siempre; sus `allow` y su `mode` se
   **ignoran** — si el usuario los quiere, los copia a su config global o
   los concede en sesión. Cero fricción, cierra el vector "clonar y abrir
   ejecuta la voluntad del repo".
2. **TOFU de una tecla para el contenido que llega al modelo.** La primera
   vez que nu se abre en un repo con `.nu/skills/` o `nu.md`, una sola
   pregunta ("este repo trae skills/contexto, ¿usarlas? — se recuerda por
   repo", persistido en `data_dir`). Sin respuesta afirmativa (incluido
   headless), ese contenido no se inyecta. Es el mismo patrón `:trust` /
   `vim.secure.read()` de Neovim ante el clásico ataque del `exrc`.

Las descripciones de tools de servidores MCP no entran aquí: instalar un
servidor MCP es un acto consciente del usuario — su responsabilidad, como
instalar un plugin.

## 12. Relación con lo pospuesto

Tool calls paralelas ([P12](pospuesto.md)), workers anidados para subagentes
([P11](pospuesto.md)) y retención de sesiones ([P10](pospuesto.md)) tienen
entrada en el registro de pospuestos con su disparador.
