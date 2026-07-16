# La extensión oficial de chat: contrato

Estado: **borrador para discusión**. Contrato de la extensión oficial
`chat` — la cara visible de enu, lo que el usuario ve al arrancar. Como el
resto de extensiones oficiales, **sin privilegios**: consume la API pública
del agente ([agente.md](agente.md)), el toolkit de widgets (extensión
oficial sobre [api.md](api.md) §9) y el bus de eventos. Una UI alternativa
de terceros puede hacer todo lo que hace esta.

## 1. Anatomía

```
┌──────────────────────────────────────────────┐
│ transcript (scroll)                          │
│   mensajes · bloques de tools · thinking     │
│                                              │
├──────────────────────────────────────────────┤
│ input (multilínea, historial, completado)    │
├──────────────────────────────────────────────┤
│ statusline: modelo · contexto % · coste ·    │
│             cwd · modo permisos              │
└──────────────────────────────────────────────┘
+ capas modales: diálogo de permisos, pickers
```

Una columna, una sesión visible. Splits y vista multi-sesión: pospuesto
([P14](pospuesto.md)).

> ✅ **Pulido de producto** ([ADR-018](adr.md)). La columna se ve acabada, no como
> un kernel pelado: **bienvenida** al arrancar (banner + modelo + cwd + atajos) en vez
> de una pantalla en blanco; **input enmarcado** (`toolkit.box`, borde redondeado) con
> prompt `› ` y placeholder visible; una fila de **actividad** con spinner animado
> mientras el turno corre; **statusline como barra** (fondo + segmentos coloreados);
> **tarjetas de tool** con sus argumentos; **modales enmarcados y centrados**. Todo
> sobre el toolkit (sus widgets `box`/`spinner`/`richtext` y el `Theme:markdown_opts`
> que colorea el transcript, G22) — sin privilegio de kernel.

Multi-sesión (G3): `chat` pinta solo los eventos cuyo `session` es la
sesión activa; la actividad de otras (subagentes, sesiones en background)
se refleja como indicador discreto en la statusline — incluido un contador
de permisos pendientes, porque un ask sin responder bloquea a su sesión
indefinidamente.

## 2. Render del turno (consumo de `agent:*`)

| Evento | Render |
|---|---|
| `agent:delta` | Texto en streaming al bloque del mensaje en curso (markdown vía `enu.text.markdown`, que es streaming-safe). |
| `agent:delta` (thinking) | Bloque de razonamiento **colapsado por defecto**, expandible; estilo atenuado. |
| `agent:tool.start/progress/end` | Bloque de tool **colapsable**: cabecera con nombre + args resumidos; `progress` en vivo; al terminar, resultado plegado si es largo. |
| `agent:message` | Sella el mensaje (sustituye los deltas por el render final). |
| `agent:error` | Bloque de error con el código estructurado y, si `retryable`, acción de reintento. |
| `agent:permission.asked` | Diálogo modal (§5), encolado FIFO si ya hay uno visible. |
| `agent:compact` | Marca visual de "historia compactada arriba". |

> ✅ **Implementado** ([pospuesto.md](pospuesto.md) **P27**). El chat consume
> también `agent:tool.progress` (progreso en vivo bajo la tool en curso) y
> `agent:compact` (marca "historia compactada arriba", emitida ya por el agente
> con **P25**).

**Renderers enchufables**: un plugin puede registrar el render del resultado
de su tool — `chat.renderer(tool_name, fn(result, width) -> Block)`. Así la
tool de diff pinta diffs con colores y la de tests pinta su tabla, sin que
`chat` los conozca. Fallback: texto plano plegado.

## 3. Input

- Editor multilínea **enmarcado** ([ADR-018](adr.md)): vive en un `toolkit.box`
  (borde redondeado, realce de foco) con un prompt `› `; la caja **crece y encoge**
  con el contenido (hasta un máximo). El **placeholder** (las pistas de uso) se ve
  aunque el editor tenga el foco (antes se ocultaba justo al arrancar). `enter` envía,
  `shift+enter` (o `alt+enter` según terminal, vía `enu.ui.caps`) inserta línea.
  Historial con `↑/↓` en el borde del editor. `esc` cancela el turno en curso
  (`Session:cancel()`); mientras corre, una fila de **actividad** con spinner lo
  señala ("Pensando…/Ejecutando <tool>… · esc para interrumpir").
- **Menciones `@`**: abre picker difuso de ficheros del repo
  (`enu.search.files` + `enu.search.fuzzy`); la mención inyecta la ruta y el
  agente decide leerla (no se incrusta el contenido a ciegas).
  *(✅ Implementado: [pospuesto.md](pospuesto.md) **P26**, vía `chat.picker`.)*
- **`/` al inicio**: autocompletado de comandos (§4) — `tab` abre el picker
  de comandos. *(✅ Implementado: **P29**.)*
- Pegado multilínea correcto (evento `paste` de `enu.ui`).

## 4. Comandos slash

Punto de extensión de primera clase:

```
chat.command{
  name, description,
  args?: string,                 -- ayuda de uso, p. ej. "<modelo>"
  complete?: fn(prefix) -> string[],
  handler: fn(args, ctx) ⏸,
}
```

Builtins (registrados con esta misma función — dogfooding):
`/model` (picker desde `providers.list()`, aplica `Session:set_model`),
`/sessions` (picker desde el listado de [sesiones.md](sesiones.md) §7,
reanuda vía `agent.session{ resume = id }`), `/fork`, `/compact`,
`/permissions` (ver y editar la política de la sesión), `/think` (ver y
cambiar el razonamiento, ADR-016), `/help`, `/quit`.

> ✅ **Implementado** ([pospuesto.md](pospuesto.md) **P28**). Además de
> `/model`, `/sessions`, `/compact`, `/clear`, `/help`, `/quit`, el chat trae
> `/fork` (bifurca con `Session:fork` y sigue en la rama vía `Chat:switch_session`),
> `/permissions` (ve y edita la política: `allow|deny <patrón>`, `mode ask|auto`)
> y `/think` (`off|adaptive|budget <N>`, vía `Session:set_thinking`, ADR-016).

## 5. Diálogo de permisos

Ante `agent:permission.asked`: modal con la tool, los args completos (sin
truncar lo peligroso: el comando entero, la ruta entera) y opciones:

- **Permitir una vez** → `agent.permission.respond(id, true)`. El segundo
  argumento es un **booleano** (`true` concede, `false`/`nil` deniega, G49):
  "una vez" y "siempre" conceden igual; difieren solo en si además se
  persiste el patrón (abajo).
- **Permitir siempre** → añade el patrón a la política de la *sesión*; con
  modificador, persiste a la config **global del usuario** (`agent.toml`) —
  nunca al `agent.toml` del proyecto: sus `allow` se ignoran por el modelo
  de confianza ([agente.md](agente.md) §11). El patrón propuesto se
  muestra y es editable antes de aceptar (generalizar `bash:npm install` a
  `bash:npm *` es decisión del humano, no de la UI). Para un comando `bash`
  **compuesto**, la propuesta es **un patrón por subcomando** — la calcula la
  extensión `agent` y llega como lista en `suggested`; no el string
  encadenado, que bajo la semántica de emparejamiento por subcomando
  ([agente.md](agente.md) §5, G53) solo volvería a casar esa combinación
  exacta; cada patrón propuesto es además auditable por separado.
  *(✅ Implementado: [pospuesto.md](pospuesto.md) **P29**. Tecla `s` = siempre
  (sesión), `g` = siempre (global, persiste a `agent.toml`). La edición inline del
  patrón antes de aceptar queda como pulido menor; v1 usa el patrón sugerido.)*
- **Denegar** (con nota opcional, que llega al modelo como rechazo).

Mientras el modal está abierto, el turno espera (así está diseñado el
pipeline del agente); `esc` = denegar. Con varias sesiones pidiendo a la
vez: **cola FIFO, un modal visible**, etiquetado con su sesión de origen;
los demás esperan en cola (y se señalan en la statusline).

## 6. Statusline

Se pinta como una **barra** ([ADR-018](adr.md)): un fondo continuo (`bg_surface`)
y, sobre él, los segmentos como **spans coloreados** por el theme (G22) —no un texto
gris concatenado—. Cada segmento devuelve `{ text, style }` (un nombre semántico de
color) o `""` para ocultarse; el chat los separa con un `·` atenuado y alinea el lado
derecho. Segmentos por defecto: modelo activo · llenado de contexto (% desde
`Session.usage`, que pasa a color `warn` cerca del umbral de compactación) ·
coste acumulado de la sesión · razonamiento (🧠, solo si está activo;
ADR-016) · cwd (**abreviada**, `~`/dos últimos segmentos) · modo de permisos
(`auto` resaltado). Extensible:

```
chat.statusline.add{ id, side: "left"|"right", priority, render: fn(ctx) -> Span[] }
```

## 7. Keymaps y theming

- Atajos por defecto registrados con `enu.ui.keymap`, todos remapeables por
  el usuario en su `init.lua` (la tabla de defaults es pública:
  `chat.keys`).
- Colores únicamente **semánticos** del theme del toolkit (`accent`,
  `error`, `dim`...): `chat` no hardcodea un solo color. Los themes son
  plugins del toolkit, no de `chat`.

## 8. Arranque e interacción con el resto

- `chat` solo se activa en TTY interactivo — el test es `enu.has("ui")`
  ([api.md](api.md) §9, G20); en headless ni se carga (la
  separación motor/UI de [agente.md](agente.md) §1 es la que lo permite).
- **Bienvenida** ([ADR-018](adr.md)). Mientras la conversación está vacía, el
  transcript muestra un saludo (identifica el harness, el **modelo** y el **cwd**
  activos, y recuerda los atajos) en vez de una pantalla en blanco; al primer mensaje
  lo sustituye la conversación. La calidad de la pantalla de arranque degradado
  (abajo) deja de ser la excepción.
- **El chat posee la pantalla y al cerrarse apaga el binario** ([G36](problemas.md#g36)).
  El conjunto oficial (ADR-015) activa también `repl`, pero el repl **cede**: solo
  auto-monta su UI si el chat no está activo (lo comprueba con `enu.plugin.list`, sin
  depender de chat). Y `Chat:quit` (y `ctrl+c`) emiten `core:shutdown`: salir del chat
  **apaga el runtime** en vez de dejar al usuario en una capa inferior (el REPL/intérprete).
  Así `enu` con el conjunto oficial abre **una** TUI única; con solo `repl` activo
  (G21), abre el REPL.
- Crea la sesión inicial (`agent.session`) con la config resuelta
  (defaults < global < proyecto), o reanuda una existente
  (`agent.session{ resume = id }`, alimentado por el picker de
  `/sessions`).
- **Arranque degradado ([ADR-017](adr.md), [G35](problemas.md)).** Si la sesión
  inicial **no se puede construir por falta o rotura de config** —`agent.session`
  lanza `EINVAL` (no hay modelo), `EPROVIDER` (modelo/provider no resoluble en
  `providers.toml`) o `EAGENT`/`EPROVIDER` (TOML mal formado)—, `chat.start`
  **no muere al log**: monta una **UI mínima accionable y salible** que explica
  cómo configurar (`agent.toml`, `providers.toml`, la API key del entorno) y deja
  salir (`esc`/`q`/`ctrl+c` → `core:shutdown`). Un fallo **inesperado** (no de
  config) se propaga como siempre. El onramp `enu --default-config` deja plantillas
  activas que evitan este camino en el primer arranque (ADR-017); la falta de
  **API key** no llega aquí (`providers.resolve` no falla sin ella): el error sale
  in-transcript como `agent:error` al primer turno.
- No toca `enu.fs` ni `enu.proc` para lógica de agente: si `chat` necesita
  algo del dominio del agente que la API pública no da, la API pública del
  agente está incompleta — misma regla de siempre.

## 9. Puntos de extensión (resumen)

| Punto | Función |
|---|---|
| Comandos slash | `chat.command{}` |
| Renderers de tool results | `chat.renderer(tool, fn)` |
| Segmentos de statusline | `chat.statusline.add{}` |
| Atajos | `enu.ui.keymap` + tabla `chat.keys` |
| Apariencia | themes del toolkit (semánticos) |

<!-- enu:interno -->

## 10. Pospuesto

Splits / vista multi-sesión ([P14](pospuesto.md)), búsqueda dentro del
transcript ([P15](pospuesto.md)), modo vim del editor de input
([P16](pospuesto.md)), render de imágenes en el transcript
([P6](pospuesto.md)).

<!-- /enu:interno -->
