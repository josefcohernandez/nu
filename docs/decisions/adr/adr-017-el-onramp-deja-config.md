---
title: "El onramp deja config de agente usable y el chat degrada con gracia"
type: "adr"
id: "ADR-017"
status: "aceptada"
date: "2026-06"
---
# ADR-017 · El onramp deja config de agente usable y el chat degrada con gracia

**Estado:** Aceptada · 2026-06 (**refina** [ADR-015](adr-015-conjunto-oficial-de-producto.md); resuelve [G35](../../findings/g35-el-onramp-de-adr-015.md)) · **Refinada por [ADR-026](adr-026-subcomandos-de-gestion-del-binario.md)** (ejecuta su disparador «flujo de configuración guiado»: `enu init` reutiliza estas plantillas y primitivas; plantillas y degradación del chat siguen siendo de este ADR)

**Contexto.** ADR-015 dio el onramp no interactivo (`nu --default-config`) que
activa el **conjunto oficial de producto** en `nu.toml`. Pero "activar los
plugins" no es "tener un harness usable": el agente y el chat necesitan un
**modelo**, un **provider** y una **API key** que el onramp no provee. Al usar el
binario terminado tras `nu --default-config`, ejecutar `nu` deja la terminal en
blanco; el log lo dice: `chat: no se pudo arrancar: agent.session requiere model
("proveedor/modelo") en opts o en agent.toml`. Son **dos defectos** ([G35](../../findings/g35-el-onramp-de-adr-015.md)):
(1) el onramp no escribe `agent.toml`/`providers.toml`, así que `core:ready` →
`chat.start` → `agent.session({model=nil})` lanza `EINVAL`; (2) el chat captura
ese fallo con `pcall` y lo manda solo al log (`nu.log.error`, nunca a pantalla,
§15) sin montar nada, y como la pantalla desnuda —la única ruta que instala un
handler de salida de emergencia— no se toma con plugins activos, el usuario queda
**atrapado** (en raw mode `ctrl+c` no genera `SIGINT`). El comando que prometía
"batteries-included" deja el producto roto e inservible en su primer arranque.

**Decisión.** Dos piezas, **ninguna en la API sagrada** `nu.*` (es superficie CLI
+ loader + Lua de las extensiones; `nu.version.api` no cambia):

1. **El onramp deja config de agente USABLE.** `nu --default-config` (modo
   persistente) escribe, además de `nu.toml`, **plantillas activas** de:
   - `agent.toml`: `model = "anthropic/opus"`, `max_turns = 32`.
   - `providers.toml`: provider `anthropic` (`base_url`, `api_key_env =
     "ANTHROPIC_API_KEY"`) con el modelo `claude-opus-4-8` (alias `opus`,
     `context`, `thinking = "adaptive"` por ADR-016).

   Se escriben **solo si no existen** (nunca sobrescriben config del usuario;
   atómico, idempotente — reusan `writeAtomic` y el patrón de no pisar TOML
   existente de `writeEnabledPlugins`, G33/ADR-015). La **clave nunca va al
   fichero** (providers.md §1): vive en el entorno. El mensaje de éxito pasa a ser
   **honesto**: lista los ficheros creados y recuerda exportar `ANTHROPIC_API_KEY`
   (o editar `providers.toml`) antes de arrancar — ya no la promesa engañosa "ya
   puedes ejecutar el agente: nu -p".

   El modo **efímero** (`--default-config -p/-e`, Docker inmutable) sigue sin
   tocar disco: ahí la config la aporta el entorno o ficheros montados, y la
   degradación (pieza 2) y el render de `agent:error` cubren su ausencia.

2. **El chat degrada con gracia.** Si `chat.start` no puede construir la sesión
   inicial por un fallo **de configuración** (`agent.session` lanza `EINVAL` por
   modelo ausente, `EPROVIDER` por modelo/provider no resoluble, o
   `EAGENT`/`EPROVIDER` por TOML roto), el chat monta una **UI mínima accionable**
   en vez de morir al log: un texto que explica cómo configurar (`agent.toml`,
   `providers.toml`, la API key) y un keymap de salida (`esc`/`q`/`ctrl+c` →
   `core:shutdown`). Los errores **inesperados** (no de config) se propagan como
   hoy. Como **red de seguridad** del kernel, el modo interactivo instala además
   un handler de salida de emergencia al **fondo** de la pila de input (cualquier
   app montada lo tapa), garantizando que ninguna ruta deje la terminal sin salida
   por teclado.

**Razonamiento.**
- **Por qué plantillas activas y no comentadas.** Con la key en el entorno, `nu`
  *just works* tras un solo comando — la promesa de ADR-015, ahora real. Sin la
  key, `providers.resolve` **no falla** (deja `api_key=nil`): el chat monta igual,
  la statusline muestra el modelo y el error por clave ausente sale **in-transcript**
  al primer turno (`agent:error` → `transcript:add_error`, que el chat ya pinta),
  mucho mejor que una pantalla muerta. Comentadas obligarían a editar TOML antes
  del primer arranque, la fricción que el onramp borra.
- **Por qué un default Anthropic.** `nu` es un harness de coding estilo
  claude-code; el default opinado es coherente con su identidad y con el modelo
  por defecto del proyecto (`claude-opus-4-8`, ADR-016). El usuario lo cambia
  editando dos líneas; las plantillas muestran el formato.
- **Por qué no un modelo por defecto cableado en el agente.** Meter qué modelo,
  qué endpoint y qué env en el motor es vocabulario de producto en el kernel/motor,
  contra ADR-003/ADR-005. La config vive en TOML, declarada; el motor solo la lee.
- **Por qué la degradación además del onramp.** El onramp cubre el camino feliz,
  pero el chat no debe morir en silencio si el usuario borra o rompe la config: la
  robustez por `pcall` en las fronteras y la salida siempre disponible son el
  principio 5 de la filosofía. Las dos piezas son complementarias, no alternativas.
- **Por qué no toca la API sagrada.** Igual que ADR-015: el onramp es del binario
  (`main.go`/loader) y la degradación es Lua de la extensión `chat`. `nu.*` y
  `nu.version.api` quedan intactos.

**Consecuencias.**
- `nu --default-config` deja el harness **realmente** listo (con la key exportada,
  un comando basta); sin ella, el primer `nu` abre el chat con un error accionable
  en vez de una pantalla muerta.
- `chat.start` deja de ser un punto de fallo silencioso: cualquier config ausente
  o rota produce una pantalla que **explica y se cierra**.
- Ninguna ruta interactiva puede atrapar la terminal (red de salida del kernel).
- Cambio observable cubierto por tests: `WriteDefaultConfig` escribe tres ficheros
  (no uno), el mensaje del flag cambia, y `chat.start` ya no lanza ante falta de
  config.
- **Disparador de reapertura:** si el onramp tuviera que sembrar config de más de
  un provider, o secretos que no caben en variables de entorno, reconsiderar un
  flujo de configuración guiado (ligado al disparador `nu config` de ADR-015).
