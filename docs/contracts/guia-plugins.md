---
title: "Guía de desarrollo de plugins"
description: "Sabiduría práctica para autores de plugins, con checklist."
type: "contrato"
layer: "contracts"
web: "wiki"
status: "vigente"
---
# Guía de desarrollo de plugins

Estado: viva — crece con cada lección aprendida. No es un contrato: es la
sabiduría práctica para escribir plugins que funcionen bien en el modelo de
ejecución de enu ([modelo-ejecucion.md](../core/modelo-ejecucion.md)). Las firmas
exactas están en [api.md](api.md) y los contratos de extensión en
[agente.md](agente.md) / [chat.md](chat.md) / [providers.md](providers.md).

## 1. Al cargarse, un módulo solo declara; el trabajo se hace al llamarlo

Cargar es ejecutar las líneas de nivel superior. Si tus preparativos tocan
algo que solo existe en el estado principal (`enu.ui`, `enu.events`), tu
módulo reventará en el `require` de cualquier worker — aunque el worker
quisiera usar otra función inocente del mismo módulo.

```lua
-- MAL: se ejecuta al cargar; explota en workers
local barra = enu.ui.region{ x = 0, y = 0, w = 40, h = 1 }

-- BIEN: perezoso; solo falla quien llama a avisar() donde no debe
local barra = nil
function M.avisar(texto)
  barra = barra or enu.ui.region{ x = 0, y = 0, w = 40, h = 1 }
  barra:blit(0, 0, enu.ui.block({ texto }))
end
```

## 2. Entre estados viajan datos, nunca estado vivo

Cada worker carga **su propia copia** de los módulos: las variables de
módulo no se comparten con el principal. Si un worker necesita un valor del
principal, mándaselo en el mensaje. Por la frontera solo cruzan valores
JSON-ables — nunca funciones, userdata ni Blocks. Un worker devuelve
resultados *digeridos* ("las 20 líneas con errores"), no datos crudos
masivos; el principal renderiza.

## 3. No bloquees nunca el loop

- Las funciones ⏸ (IO) solo se llaman dentro de tasks. Un handler síncrono
  (input, evento, timer) que necesite IO **lanza una task**:
  `enu.task.spawn(function() ... end)`.
- ¿CPU pesada en Lua? Tu herramienta es un worker — nunca el estado
  principal. El watchdog aborta slices que excedan su presupuesto (~100 ms)
  y marca tu plugin como sospechoso.
- ¿Trabajo proporcional a la pantalla o al repo? No lo hagas en Lua: ya hay
  primitiva Go (`enu.text.*`, `enu.search.*`). Si no la hay, probablemente es
  un hueco del core — repórtalo antes de reimplementarla lenta.
- Para esperar un valor que otro código producirá (diálogo, picker,
  respuesta), usa `enu.task.future()` — jamás polling con `task.sleep`.
- **Todo recurso que crees, regístralo en `enu.task.cleanup`** (matar el
  proceso, destruir la región, desapilar el input handler). Los cleanups
  corren siempre — éxito, error o cancelación; es la única forma de no
  dejar basura cuando el usuario pulsa `esc` a mitad de tu código.
- Si escribes listas de `caps` a mano, cuida las parejas prácticas:
  `proc.spawn` sin `proc.kill` = procesos que no puedes matar. Los paquetes
  oficiales (`agent.caps.*`) ya las curan juntas — imprímelos para ver
  exactamente qué conceden.
- Procesos longevos (un servidor MCP, un watcher): arráncalos perezosamente
  (primer uso o `core:ready`), nunca al cargar el módulo (§1), y mátalos en
  `cleanup` y en `core:shutdown`.

## 4. Errores: lanza estructurado, asume pcall en las fronteras

```lua
error({ code = "EINVAL", message = "filtro vacío", detail = { arg = "filter" } })
```

- El core envuelve cada hook en `pcall`: tu error no tumba a nadie, pero
  queda logueado contra tu plugin.
- En handlers de tools, lanzar es correcto: el loop lo convierte en
  `tool_result` con `is_error = true` y el modelo lo ve. No devuelvas
  strings de error "exitosos".

## 5. Tools: el modelo es tu usuario

- Args y resultado deben ser JSON-ables (también te da el proxy de workers
  gratis). `description` y `schema` son la UX del modelo: escríbelos como
  documentación, no como trámite.
- Si tu tool solo lee, regístrala con `permissions = { default = "allow" }`;
  si muta (escribir, ejecutar, red), deja `"ask"`. No te auto-concedas
  `allow` en tools mutantes: el diálogo de permisos es la confianza del
  usuario en todo el ecosistema.
- Los patrones `tool:argumento` casan por **glob anclado sobre el argumento
  principal** de la tool, y `bash` empareja **por subcomando** con
  fail-closed ([agente.md](agente.md) §5, G53). No vendas `allow`/`deny`
  como sandbox: no acotan lo que un binario permitido ejecuta por dentro
  (hooks de git, `postinstall` de npm) — la valla dura para código no
  confiable son los workers con `caps`.
- Salida larga o lenta: emite `ctx.progress(...)` — la UI lo pinta en vivo.
- **Sanea el binario en el origen** (G11): si tu tool puede producir bytes
  no-UTF-8 (salida de procesos, ficheros arbitrarios), sustitúyelos
  visiblemente (`[output binario: 48KB omitidos]`) antes de devolver. El
  codec JSON es estricto y lanzará `EINVAL` aguas abajo — lejos de tu
  código y de tu contexto.
- **Redirects bajo control ante URLs de terceros** (G54): si tu tool hace
  fetch de URLs que propone el modelo o que llegan de fuera (un fetcher, un
  websearch), pon `max_redirects = 0` y valida el destino de **cada** salto
  antes de seguirlo a mano — validar solo la URL inicial se evade con un
  `302` hacia dentro de la red (`169.254.169.254`). El recorte de cabeceras
  en saltos cross-host (api.md §8) protege tus credenciales por defecto,
  pero la validación del *destino* es tuya: el core no sabe qué hosts son
  legítimos para tu tool.
- **No regales secretos al hijo** (G55): si tu tool o plugin lanza
  subprocesos que ejecutan código que no controlas (comandos propuestos por
  el modelo, builds con `postinstall`), recuerda que sin `opts.env` el hijo
  hereda el entorno completo — API keys incluidas. Dos vías: componer el
  entorno **desde cero** con `opts.env` (presente, reemplaza el heredado —
  [api.md](api.md) §6, semántica fijada en S16 de la bitácora), o el idioma
  `env -u VAR ...` del SO para "heredado menos estas". En ambas, recorta
  las variables secretas conocidas: `providers.secret_env_vars()`
  te da la lista ([providers.md](providers.md) §4). Las tools oficiales ya
  lo hacen por defecto ([agente.md](agente.md) §3).

## 6. UI: bloques, no celdas; y limpia al salir

- Pide los Blocks a `enu.text.*` (markdown, wrap, highlight) y colócalos con
  `Region:blit`. Si estás escribiendo celda a celda en un camino caliente,
  estás haciendo el trabajo del compositor — y lento.
- Usa el toolkit oficial salvo que tengas una razón; si vas a `enu.ui` crudo,
  eres responsable de tu región: `input:pop()` y `Region:destroy()` también
  en los caminos de error (envuelve en `pcall` y limpia).
- Nada de colores hardcodeados: pide los colores al theme del toolkit
  (`accent`, `error`, `dim`...) al construir tus Blocks — el toolkit los
  resuelve a literales, porque el core solo acepta literales (G22). Un
  plugin que hardcodea `#ff0000` rompe todos los themes menos el del
  autor. Y si cacheas Blocks o usas colores del theme sobre `enu.ui`
  crudo, re-renderiza al evento de cambio de theme del toolkit — mismo
  trato que `ui:resize`: tu región, tu repintado.
- Input modal: tu handler devuelve `true` (consume) mientras esté activo, y
  se desapila en cuanto terminas. No dejes handlers huérfanos en la pila.
- **Tu región, tu `ui:resize`**: si creas regiones a pelo, suscríbete y
  recolócate (el core solo garantiza el recorte sin error — tu picker
  centrado para 120 columnas se verá recortado en 60 hasta que lo muevas
  tú). Con el toolkit, el relayout es automático.
- **Scroll = cachea el Block, mueve el offset** (G28): para un transcript con
  scroll, construye el Block una vez y `blit(0, -scroll, doc)` con distinto
  `scroll` por tick — `blit` con offset es copia, no re-render. El antipatrón
  es reconstruir el Block (re-render del markdown) en cada scroll: eso sí es
  caro. Acota `scroll` con `doc.height` y el alto de la región. Para
  historiales enormes, virtualiza (renderiza solo lo visible) — eso es
  trabajo tuyo/del toolkit, el core no retiene tu contenido.
- **Hit-testing del ratón es tuyo** (G29): el evento de ratón llega en
  coordenadas de pantalla; tú fijaste `x,y,w,h` de tu región y aplicaste tu
  `scroll`, así que el mapeo pantalla→contenido (qué bloque/línea se clicó) lo
  resuelves tú restando origen y offset — mismo reparto que el relayout: lo
  que depende de tu layout es tuyo, no del core. Con el toolkit, el ruteo de
  clics a widgets es automático.
- Contenido en streaming: re-renderiza el mensaje en curso **una vez por
  tick de pintado** (el repintado ya va coalescido a ~30 ms), no por cada
  delta — el render en Go es barato; lo que mata es hacerlo mil veces por
  segundo.

## 7. Convivencia en el ecosistema

- **Almacenamiento**: solo bajo `enu.config.data_dir()/plugins/<tu-nombre>/`.
  Las sesiones (`sessions/`) se leen, no se escriben — son del agente.
  Credenciales y tokens: en tu directorio, `0600`, y jamás en el repo del
  usuario ni en resultados de tools (acabarían en el transcript).
- **Eventos propios**: namespace = tu nombre de plugin
  (`"mi-plugin:cosa.paso"`). Como el loader garantiza que tu nombre es
  único, nadie puede pisarlo. Solo `core:` y `ui:` los reserva el core (son
  superficies suyas); `agent:` **no** es reserva del core, es el namespace
  del plugin oficial `agent` igual que el tuyo es tuyo (G26) — sin
  privilegio: no puedes llamarte `agent`, ni el agente apropiarse de tu
  nombre.
- **Sé librería**: lo reutilizable, en `lua/` de tu plugin — otros podrán
  hacer `require("tu-plugin.modulo")`. Así se construyó el ecosistema de
  Neovim y así queremos el de enu.
- **Hooks**: registra con la mínima `priority` necesaria y devuelve `nil`
  cuando no opinas. Un hook que modifica payloads que no entiende rompe a
  los plugins que vienen detrás en la cadena.
- No monopolices: keymaps configurables (expón tu tabla de defaults, como
  hace `chat.keys`), regiones con el `z` justo, y nada de capturar input
  global "por si acaso".

## 8. Compatibilidad

- **El intérprete es Lua 5.4** (PUC-Lua oficial compilado a WebAssembly sobre
  wazero; ver [api.md](api.md) §1.2). Si vienes de Lua 5.1 —o de scripts hechos
  para el antiguo backend gopher-lua—, la biblioteca estándar cambió: `loadstring(s)`
  se absorbió en `load(s)` (acepta el string directamente), `unpack` es ahora
  `table.unpack`, `setfenv`/`getfenv` desaparecen (el entorno es el upvalue léxico
  `_ENV`), y se fueron `table.getn`/`string.gfind`/`math.mod`/`math.log10`. A cambio
  ganas enteros de verdad (división entera `//`, `%` entero), operadores de bits
  nativos (`&`, `|`, `~`, `<<`, `>>`) y `goto`. No detectes la versión a mano:
  escribe Lua 5.4 y ya.
- Detecta capacidades con `enu.has()` y `enu.ui.caps()`, nunca mirando
  versiones.
- Declara dependencias de otros plugins en `plugin.toml` (`requires`) — el
  orden de carga topológico depende de ello.
- Si tu módulo puede acabar en un worker (librerías de lógica), no
  referencies módulos solo-principal ni al cargar ni en las funciones que
  un worker llamaría. Truco: separa `tu-plugin/logica.lua` (worker-safe) de
  `tu-plugin/ui.lua`.

## 9. Checklist antes de publicar

- [ ] `require` de todos mis módulos funciona en un estado limpio (sin
      efectos al cargar).
- [ ] Ningún handler síncrono hace IO ni CPU pesada.
- [ ] Errores estructurados; nada de strings "exitosos" con errores dentro.
- [ ] Tools mutantes con `default = "ask"`; schemas descriptivos.
- [ ] Regiones e input handlers limpiados también en errores.
- [ ] Solo colores semánticos; keymaps remapeables.
- [ ] Escribo solo en mi directorio; mis eventos llevan mi namespace.
- [ ] API de Lua 5.4 (nada de `loadstring`/`unpack`/`setfenv`/`getfenv` de 5.1).
