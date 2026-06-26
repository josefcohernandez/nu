# La extensión oficial de chat: contrato

Estado: **borrador para discusión**. Contrato de la extensión oficial
`chat` — la cara visible de nu, lo que el usuario ve al arrancar. Como el
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

Multi-sesión (G3): `chat` pinta solo los eventos cuyo `session` es la
sesión activa; la actividad de otras (subagentes, sesiones en background)
se refleja como indicador discreto en la statusline — incluido un contador
de permisos pendientes, porque un ask sin responder bloquea a su sesión
indefinidamente.

## 2. Render del turno (consumo de `agent:*`)

| Evento | Render |
|---|---|
| `agent:delta` | Texto en streaming al bloque del mensaje en curso (markdown vía `nu.text.markdown`, que es streaming-safe). |
| `agent:delta` (thinking) | Bloque de razonamiento **colapsado por defecto**, expandible; estilo atenuado. |
| `agent:tool.start/progress/end` | Bloque de tool **colapsable**: cabecera con nombre + args resumidos; `progress` en vivo; al terminar, resultado plegado si es largo. |
| `agent:message` | Sella el mensaje (sustituye los deltas por el render final). |
| `agent:error` | Bloque de error con el código estructurado y, si `retryable`, acción de reintento. |
| `agent:permission.asked` | Diálogo modal (§5), encolado FIFO si ya hay uno visible. |
| `agent:compact` | Marca visual de "historia compactada arriba". |

> **Implementación diferida** ([pospuesto.md](pospuesto.md) **P27**). La `chat`
> `0.1.0` consume `delta/message/tool.start/tool.end/error/permission.asked`,
> pero aún no `agent:tool.progress` (progreso en vivo) ni `agent:compact` (este
> último depende además de que el agente lo emita, **P25**).

**Renderers enchufables**: un plugin puede registrar el render del resultado
de su tool — `chat.renderer(tool_name, fn(result, width) -> Block)`. Así la
tool de diff pinta diffs con colores y la de tests pinta su tabla, sin que
`chat` los conozca. Fallback: texto plano plegado.

## 3. Input

- Editor multilínea: `enter` envía, `shift+enter` (o `alt+enter` según
  terminal, vía `nu.ui.caps`) inserta línea. Historial con `↑/↓` en el
  borde del editor. `esc` cancela el turno en curso (`Session:cancel()`).
- **Menciones `@`**: abre picker difuso de ficheros del repo
  (`nu.search.files` + `nu.search.fuzzy`); la mención inyecta la ruta y el
  agente decide leerla (no se incrusta el contenido a ciegas).
  *(Implementación diferida: [pospuesto.md](pospuesto.md) **P26**.)*
- **`/` al inicio**: autocompletado de comandos (§4).
  *(El autocompletado visual es implementación diferida: **P29**.)*
- Pegado multilínea correcto (evento `paste` de `nu.ui`).

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
`/permissions` (ver y editar la política de la sesión), `/help`, `/quit`.

> **Implementación diferida** ([pospuesto.md](pospuesto.md) **P28**). La `0.1.0`
> trae `/model`, `/sessions`, `/compact` (degrada mientras no haya compactación,
> P25), `/clear`, `/help`, `/quit`. Faltan `/fork` (requiere `Session:fork`,
> P22) y `/permissions`.

## 5. Diálogo de permisos

Ante `agent:permission.asked`: modal con la tool, los args completos (sin
truncar lo peligroso: el comando entero, la ruta entera) y opciones:

- **Permitir una vez** → `agent.permission.respond(id, "once")`.
- **Permitir siempre** → añade el patrón a la política de la *sesión*; con
  modificador, persiste a la config **global del usuario** (`agent.toml`) —
  nunca al `agent.toml` del proyecto: sus `allow` se ignoran por el modelo
  de confianza ([agente.md](agente.md) §11). El patrón propuesto se
  muestra y es editable antes de aceptar (generalizar `bash:npm install` a
  `bash:npm *` es decisión del humano, no de la UI).
  *(Implementación diferida: [pospuesto.md](pospuesto.md) **P29**; la `0.1.0`
  ofrece "permitir una vez" y "denegar".)*
- **Denegar** (con nota opcional, que llega al modelo como rechazo).

Mientras el modal está abierto, el turno espera (así está diseñado el
pipeline del agente); `esc` = denegar. Con varias sesiones pidiendo a la
vez: **cola FIFO, un modal visible**, etiquetado con su sesión de origen;
los demás esperan en cola (y se señalan en la statusline).

## 6. Statusline

Segmentos por defecto: modelo activo · llenado de contexto (% desde
`Session.usage`, con aviso visual cerca del umbral de compactación) ·
coste acumulado de la sesión · cwd · modo de permisos. Extensible:

```
chat.statusline.add{ id, side: "left"|"right", priority, render: fn(ctx) -> Span[] }
```

## 7. Keymaps y theming

- Atajos por defecto registrados con `nu.ui.keymap`, todos remapeables por
  el usuario en su `init.lua` (la tabla de defaults es pública:
  `chat.keys`).
- Colores únicamente **semánticos** del theme del toolkit (`accent`,
  `error`, `dim`...): `chat` no hardcodea un solo color. Los themes son
  plugins del toolkit, no de `chat`.

## 8. Arranque e interacción con el resto

- `chat` solo se activa en TTY interactivo — el test es `nu.has("ui")`
  ([api.md](api.md) §9, G20); en headless ni se carga (la
  separación motor/UI de [agente.md](agente.md) §1 es la que lo permite).
- Crea la sesión inicial (`agent.session`) con la config resuelta
  (defaults < global < proyecto), o reanuda una existente
  (`agent.session{ resume = id }`, alimentado por el picker de
  `/sessions`).
- No toca `nu.fs` ni `nu.proc` para lógica de agente: si `chat` necesita
  algo del dominio del agente que la API pública no da, la API pública del
  agente está incompleta — misma regla de siempre.

## 9. Puntos de extensión (resumen)

| Punto | Función |
|---|---|
| Comandos slash | `chat.command{}` |
| Renderers de tool results | `chat.renderer(tool, fn)` |
| Segmentos de statusline | `chat.statusline.add{}` |
| Atajos | `nu.ui.keymap` + tabla `chat.keys` |
| Apariencia | themes del toolkit (semánticos) |

## 10. Pospuesto

Splits / vista multi-sesión ([P14](pospuesto.md)), búsqueda dentro del
transcript ([P15](pospuesto.md)), modo vim del editor de input
([P16](pospuesto.md)), render de imágenes en el transcript
([P6](pospuesto.md)).
