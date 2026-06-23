---
title: Conceptos clave
description: El modelo mental de nu — kernel mínimo, extensiones, el modelo de concurrencia del navegador, workers y la API sagrada.
---

Esta página reúne los conceptos transversales que aparecen una y otra vez en la
referencia. Si entiendes estos cinco, el resto del manual encaja solo.

## 1. El core no sabe lo que es un agente

El kernel de `nu` solo conoce **sus propias capacidades**: primitivas (runtime,
IO, red, UI de terminal), el loader de plugins y sus extensiones embebidas. El
loop del agente, el chat, los comandos slash, MCP, los providers de LLM: **todo
son extensiones Lua**, incluidas las oficiales, sin privilegio arquitectónico.

La vara para cualquier caso dudoso: si algo se describe enteramente con el
vocabulario del kernel (plugins, rutas, versiones), es del kernel; si necesita
vocabulario de producto (agente, chat, tools, token), es de una extensión.

**Corolario:** si una feature oficial no se puede construir con la API pública,
la API está incompleta —y el arreglo va en la API, no en un atajo
privilegiado—. Eso es lo que mantiene la API honesta.

## 2. La API del core es sagrada

Toda la API vive bajo el global `nu`, con identificadores en inglés y
`snake_case`. Es deliberadamente **pequeña y aburrida**, y **crece solo por
adición**: una firma nunca cambia ni desaparece. Cada adición incrementa
`nu.version.api` (el nivel actual es `2`).

Por eso detectas capacidades con [`nu.has()`](/nu/referencia/nu/), nunca
comparando números de versión: `nu.has("ui")` te dice si hay terminal, sin que
tu plugin se rompa cuando la API crezca.

## 3. El modelo de concurrencia "del navegador"

`nu` toma su modelo de concurrencia del navegador y de Luau:

- **Un estado principal single-threaded** con event loop. Determinista: no hay
  data races entre tu código Lua.
- **Tasks**: corrutinas gestionadas por el scheduler. Dentro de una task, las
  funciones suspendientes (⏸) se escriben en estilo **secuencial**, con *await*
  implícito —sin callbacks, sin promesas explícitas—. El IO vive aquí.
- **Handlers síncronos** (input, eventos): corren en el loop y **no pueden**
  llamar funciones ⏸; para hacer IO, lanzan una task con `nu.task.spawn`.
- **Primitivas Go paralelas por dentro**: búsqueda, diff, markdown, highlighting
  y streaming HTTP son nativas y aprovechan varios núcleos sin que tú gestiones
  hilos.

```lua
-- Estilo secuencial dentro de una task: nada de callbacks.
nu.task.spawn(function()
  local cfg = nu.fs.read("config.json")   -- ⏸ suspende, devuelve directo
  local data = nu.json.decode(cfg)
  local res = nu.http.request{ url = data.endpoint }  -- ⏸
  return res.status
end)
```

### Cancelación y watchdog

Dos cosas que abortan una task **desenrollando la pila sin pasar por `pcall`**
(si fueran errores normales, cualquier `pcall` del ecosistema los tragaría):

- **`Task:cancel()`**: cancelación cooperativa, surte efecto en el siguiente
  punto de suspensión.
- **Watchdog**: cada *slice* de ejecución Lua continua (entre dos suspensiones)
  tiene un presupuesto (100 ms por defecto). Excederlo aborta la task.

Para liberar recursos pase lo que pase —éxito, error o aborto— registra
[`nu.task.cleanup(fn)`](/nu/referencia/task/). Los códigos `ECANCELED` y
`EBUDGET` solo sirven para *observar* esos abortos (p. ej. en `Task:await`), no
para capturarlos.

## 4. Workers: paralelismo de verdad, opt-in

Cuando necesitas quemar CPU sin congelar el loop, levantas un
[**worker**](/nu/referencia/worker/): un estado Lua nuevo en su goroutine, **sin
memoria compartida**. La comunicación es por **paso de mensajes JSON-ables**
(copiados, no referencias). Un worker no tiene `nu.ui` ni el bus de eventos
principal, y puede restringirse a un subconjunto de la API con `caps`
(sandboxing por capacidades).

La regla "Lua decide, Go ejecuta" significa que rara vez necesitarás un worker:
si estás quemando CPU en Lua, probablemente falta una primitiva Go.

## 5. Batteries included, pero no enchufadas

El binario trae las extensiones oficiales **embebidas** (`go:embed`) pero
**ninguna activa por defecto**. `nu` recién instalado es un runtime desnudo;
enchufarlas es explícito pero trivial: el primer arranque con TTY ofrece activar
el conjunto oficial (el agente, el chat…) con una tecla, y sin TTY el flag
`nu --default-config` hace lo mismo de un comando —en ambos casos sin red—.
Mismo modelo mental que Neovim: el programa no trae plugins activados.

Un [**plugin**](/nu/referencia/plugin/) es un directorio con `plugin.toml`
(`name`, `version`, `requires?`) e `init.lua`. El **nombre es la identidad** y el
loader la mantiene única, lo que deja que los namespaces de eventos y demás
registros sean libres de colisión por simple convención (namespace = nombre del
plugin). El `init.lua` del usuario se carga **el último**, así que tiene la
última palabra (keymaps, theme, overrides) por construcción.

## Marcadores que verás en la referencia

| Marcador | Significado |
|---|---|
| **⏸** | Función **suspendiente**: solo puede llamarse dentro de una task; cede el control hasta completarse y devuelve el resultado directamente. |
| **[W]** | Disponible dentro de **workers**. Sin esta marca, la función es solo del estado principal. |

Con esto, ya puedes leer cualquier página de la [referencia](/nu/referencia/convenciones/).
