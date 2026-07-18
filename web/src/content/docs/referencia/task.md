---
title: enu.task — concurrencia
description: El scheduler de enu — tasks, sleep, all/race, timers, defer, futures, cancelación y cleanup.
---

`enu.task` es el scheduler: corrutinas cooperativas sobre el event loop del estado
principal. Es donde vive todo el trabajo asíncrono. Repasa el modelo en
[Conceptos clave](/enu/docs/conceptos/#3-el-modelo-de-concurrencia-del-navegador)
si aún no lo tienes claro.

Todo el módulo está disponible en workers **[W]** (cada worker es un mini-runtime
con su propio scheduler).

## `enu.task.spawn` [W]

```
enu.task.spawn(fn, ...) -> Task
```

Lanza una task (una corrutina gestionada por el scheduler). Los argumentos extra
se pasan a `fn`. Devuelve un handle `Task` con el que esperar o cancelar.

Es la puerta de entrada al IO: un handler síncrono (input, eventos) que necesita
hacer IO lanza una task con `spawn`.

```lua
local t = enu.task.spawn(function(nombre)
  return "hola, " .. nombre
end, "mundo")
```

## `Task:await` ⏸ [W]

```
Task:await() -> any
```

Espera el resultado de otra task. Suspende hasta que termina.

```sh
enu -e '
enu.task.spawn(function()
  local t = enu.task.spawn(function() enu.task.sleep(10); return 42 end)
  local v = t:await()
  enu.fs.write(enu.fs.tmpdir().."/r.txt", tostring(v))  -- v == 42
end)
return "lanzada"
'
```

(Recuerda: `await` es ⏸, así que va dentro de una task; en `enu -e` el chunk no lo
es, por eso lo envolvemos en `spawn`.)

## `enu.task.sleep` ⏸ [W]

```
enu.task.sleep(ms)
```

Suspende la task actual durante `ms` milisegundos, sin bloquear el loop.

```lua
enu.task.spawn(function()
  enu.task.sleep(500)
  -- medio segundo después
end)
```

## `enu.task.all` ⏸ [W]

```
enu.task.all(fns: Task[] | fn[]) -> any[]
```

Espera a **todas** las tasks (o funciones, que se lanzan como tasks). Si una
lanza, cancela el resto y relanza. Los resultados se devuelven **alineados con
los inputs** (`out[i]` corresponde a `fns[i]`), nunca en orden de terminación:
así correlacionas resultado con entrada en un fan-out sin acarrear el índice a
mano (es el `Promise.all` de `enu`).

```sh
enu -e '
enu.task.spawn(function()
  local r = enu.task.all({
    function() return "a" end,
    function() return "b" end,
    function() return "c" end,
  })
  enu.fs.write(enu.fs.tmpdir().."/all.txt", enu.json.encode(r))  -- ["a","b","c"]
end)
return "ok"
'
```

## `enu.task.race` ⏸ [W]

```
enu.task.race(fns) -> (winner_index, result)
```

La primera task en terminar gana; el resto se cancela. Devuelve el **índice** de
la ganadora y su resultado. El patrón clásico: una operación con timeout.

```lua
enu.task.spawn(function()
  local i, res = enu.task.race({
    function() return enu.http.request{ url = "https://lento.example" } end,
    function() enu.task.sleep(2000); return "timeout" end,
  })
  if i == 2 then error({ code = "ETIMEOUT", message = "tardó demasiado" }) end
  return res
end)
```

## `enu.task.every` [W]

```
enu.task.every(ms, fn) -> Timer
  Timer:stop()
```

Timer periódico: ejecuta `fn` (handler **síncrono**) cada `ms`. Devuelve un
`Timer` con `Timer:stop()`.

```lua
local timer = enu.task.every(1000, function()
  -- cada segundo; síncrono: para IO, spawn una task aquí dentro
end)
-- ...
timer:stop()
```

## `enu.task.defer` [W]

```
enu.task.defer(fn)
```

Ejecuta `fn` en el **siguiente tick** del loop. Útil para posponer trabajo justo
después del frame actual.

```lua
enu.task.defer(function()
  -- corre tras vaciarse el trabajo del tick actual
end)
```

## `enu.task.future` [W]

```
enu.task.future() -> Future
  Future:set(v)            -- síncrono, una sola vez (otra vez lanza EINVAL)
  Future:await() -> v  ⏸   -- varios pueden esperar; si ya está, retorna ya
```

Rendez-vous de un solo uso: la pieza para "una task espera un valor que otro
código producirá" (diálogos, pickers, proxies) **sin polling**. `set` es
síncrono (lo puede llamar un handler de input o de evento); `await` suspende.

```sh
enu -e '
local f = enu.task.future()
enu.task.spawn(function()
  local v = f:await()                       -- espera el valor
  enu.fs.write(enu.fs.tmpdir().."/fut.txt", v)
end)
enu.task.spawn(function()
  enu.task.sleep(10)
  f:set("resuelto")                         -- lo produce otra task
end)
return "ok"
'
```

## `Task:cancel` [W]

```
Task:cancel()
```

Cancelación **cooperativa**: aborta la task en su siguiente punto de suspensión,
**sin pasar por `pcall`** (no es un error capturable). Corren sus `cleanup`s.
Observa el resultado como `ECANCELED` si haces `await`.

```lua
local t = enu.task.spawn(function()
  while true do enu.task.sleep(100) end   -- trabajo indefinido
end)
t:cancel()  -- lo para en el próximo sleep
```

## `enu.task.cleanup` [W]

```
enu.task.cleanup(fn)
```

Registra un liberador **síncrono** en la pila LIFO de la task actual. Corren
todos al terminar la task —éxito, error **o aborto** (cancelación/watchdog)—. Es
el `defer` de esta casa: la forma fiable de cerrar procesos, regiones o handlers
pase lo que pase.

```lua
enu.task.spawn(function()
  local proc = enu.proc.spawn({ "long-running" })
  enu.task.cleanup(function() proc:kill() end)  -- se mata siempre
  -- ... aunque esto lance o la task se cancele, el proceso muere
end)
```

:::caution[Por qué cleanup y no pcall]
Cancelación y watchdog desenrollan la pila **sin** pasar por `pcall` —de lo
contrario cualquier `pcall` del ecosistema los tragaría y el programa seguiría
como si nada—. Por eso la liberación de recursos va en `cleanup`, no en un
`pcall`/`finally`.
:::
