---
title: nu.task — concurrencia
description: El scheduler de nu — tasks, sleep, all/race, timers, defer, futures, cancelación y cleanup.
---

`nu.task` es el scheduler: corrutinas cooperativas sobre el event loop del estado
principal. Es donde vive todo el trabajo asíncrono. Repasa el modelo en
[Conceptos clave](/nu/empezando/conceptos/#3-el-modelo-de-concurrencia-del-navegador)
si aún no lo tienes claro.

Todo el módulo está disponible en workers **[W]** (cada worker es un mini-runtime
con su propio scheduler).

## `nu.task.spawn` [W]

```
nu.task.spawn(fn, ...) -> Task
```

Lanza una task (una corrutina gestionada por el scheduler). Los argumentos extra
se pasan a `fn`. Devuelve un handle `Task` con el que esperar o cancelar.

Es la puerta de entrada al IO: un handler síncrono (input, eventos) que necesita
hacer IO lanza una task con `spawn`.

```lua
local t = nu.task.spawn(function(nombre)
  return "hola, " .. nombre
end, "mundo")
```

## `Task:await` ⏸ [W]

```
Task:await() -> any
```

Espera el resultado de otra task. Suspende hasta que termina.

```sh
nu -e '
nu.task.spawn(function()
  local t = nu.task.spawn(function() nu.task.sleep(10); return 42 end)
  local v = t:await()
  nu.fs.write(nu.fs.tmpdir().."/r.txt", tostring(v))  -- v == 42
end)
return "lanzada"
'
```

(Recuerda: `await` es ⏸, así que va dentro de una task; en `nu -e` el chunk no lo
es, por eso lo envolvemos en `spawn`.)

## `nu.task.sleep` ⏸ [W]

```
nu.task.sleep(ms)
```

Suspende la task actual durante `ms` milisegundos, sin bloquear el loop.

```lua
nu.task.spawn(function()
  nu.task.sleep(500)
  -- medio segundo después
end)
```

## `nu.task.all` ⏸ [W]

```
nu.task.all(fns: Task[] | fn[]) -> any[]
```

Espera a **todas** las tasks (o funciones, que se lanzan como tasks). Si una
lanza, cancela el resto y relanza. Los resultados se devuelven **alineados con
los inputs** (`out[i]` corresponde a `fns[i]`), nunca en orden de terminación:
así correlacionas resultado con entrada en un fan-out sin acarrear el índice a
mano (es el `Promise.all` de `nu`).

```sh
nu -e '
nu.task.spawn(function()
  local r = nu.task.all({
    function() return "a" end,
    function() return "b" end,
    function() return "c" end,
  })
  nu.fs.write(nu.fs.tmpdir().."/all.txt", nu.json.encode(r))  -- ["a","b","c"]
end)
return "ok"
'
```

## `nu.task.race` ⏸ [W]

```
nu.task.race(fns) -> (winner_index, result)
```

La primera task en terminar gana; el resto se cancela. Devuelve el **índice** de
la ganadora y su resultado. El patrón clásico: una operación con timeout.

```lua
nu.task.spawn(function()
  local i, res = nu.task.race({
    function() return nu.http.request{ url = "https://lento.example" } end,
    function() nu.task.sleep(2000); return "timeout" end,
  })
  if i == 2 then error({ code = "ETIMEOUT", message = "tardó demasiado" }) end
  return res
end)
```

## `nu.task.every` [W]

```
nu.task.every(ms, fn) -> Timer
  Timer:stop()
```

Timer periódico: ejecuta `fn` (handler **síncrono**) cada `ms`. Devuelve un
`Timer` con `Timer:stop()`.

```lua
local timer = nu.task.every(1000, function()
  -- cada segundo; síncrono: para IO, spawn una task aquí dentro
end)
-- ...
timer:stop()
```

## `nu.task.defer` [W]

```
nu.task.defer(fn)
```

Ejecuta `fn` en el **siguiente tick** del loop. Útil para posponer trabajo justo
después del frame actual.

```lua
nu.task.defer(function()
  -- corre tras vaciarse el trabajo del tick actual
end)
```

## `nu.task.future` [W]

```
nu.task.future() -> Future
  Future:set(v)            -- síncrono, una sola vez (otra vez lanza EINVAL)
  Future:await() -> v  ⏸   -- varios pueden esperar; si ya está, retorna ya
```

Rendez-vous de un solo uso: la pieza para "una task espera un valor que otro
código producirá" (diálogos, pickers, proxies) **sin polling**. `set` es
síncrono (lo puede llamar un handler de input o de evento); `await` suspende.

```sh
nu -e '
local f = nu.task.future()
nu.task.spawn(function()
  local v = f:await()                       -- espera el valor
  nu.fs.write(nu.fs.tmpdir().."/fut.txt", v)
end)
nu.task.spawn(function()
  nu.task.sleep(10)
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
local t = nu.task.spawn(function()
  while true do nu.task.sleep(100) end   -- trabajo indefinido
end)
t:cancel()  -- lo para en el próximo sleep
```

## `nu.task.cleanup` [W]

```
nu.task.cleanup(fn)
```

Registra un liberador **síncrono** en la pila LIFO de la task actual. Corren
todos al terminar la task —éxito, error **o aborto** (cancelación/watchdog)—. Es
el `defer` de esta casa: la forma fiable de cerrar procesos, regiones o handlers
pase lo que pase.

```lua
nu.task.spawn(function()
  local proc = nu.proc.spawn({ "long-running" })
  nu.task.cleanup(function() proc:kill() end)  -- se mata siempre
  -- ... aunque esto lance o la task se cancele, el proceso muere
end)
```

:::caution[Por qué cleanup y no pcall]
Cancelación y watchdog desenrollan la pila **sin** pasar por `pcall` —de lo
contrario cualquier `pcall` del ecosistema los tragaría y el programa seguiría
como si nada—. Por eso la liberación de recursos va en `cleanup`, no en un
`pcall`/`finally`.
:::
