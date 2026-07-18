---
title: Convenciones de la API
description: Cómo leer la referencia — notación de firmas, marcadores ⏸ y [W], errores estructurados, unidades y tipos comunes.
---

La referencia documenta la **API v1 del core**: la "superficie sagrada". Todo lo
que vive bajo el global `enu` y solo crece por adición. Lo que **no** está aquí
(toolkit de widgets, agente, chat, MCP, providers) es una extensión y se versiona
aparte.

## Notación de firmas

Las firmas usan la notación `enu.mod.fn(arg: tipo, opts?: tabla) -> tipo`:

- `arg: tipo` — argumento obligatorio y su tipo.
- `opts?: tabla` — el `?` marca lo opcional.
- `-> tipo` — el valor de retorno.

## Marcadores

| Marcador | Significado |
|---|---|
| **⏸** | **Suspende**: la función solo puede llamarse **dentro de una task**; cede el control hasta completarse y devuelve el resultado directamente (await implícito). Llamarla fuera de una task lanza `EINVAL`. |
| **[W]** | Disponible dentro de **workers**. Sin la marca, la función es solo del estado principal. |

Recuerda: el chunk de `enu -e` corre en el estado principal, **no** en una task,
así que para probar funciones ⏸ las envuelves en `enu.task.spawn(function() ...
end)`. Ver [Tu primer script](/enu/docs/primer-script/).

## El namespace `enu`

Toda la API vive bajo el global `enu`, con submódulos. `require` queda reservado
para módulos de plugins y librerías Lua puras. Los identificadores son en
**inglés** y `snake_case`.

### Baseline del entorno Lua

Lua 5.1 (gopher-lua). Disponibles: `string`, `table`, `math`, `coroutine`,
`pairs`/`ipairs`/`pcall`/`error`/… **Deshabilitados**: `io`, `os.execute`,
`os.exit`, `os.remove`, `os.rename`, `os.getenv`, `dofile`/`loadfile` fuera del
loader. Y `print` está **redirigido a `enu.log.info`** (va al log, no a la
pantalla). Razón: todo IO debe pasar por las primitivas async del core; el IO
bloqueante de la stdlib congelaría el event loop.

## Errores

Las funciones del core **lanzan** (vía `error()`) tablas estructuradas, en vez
de devolver `(valor, err)`:

```lua
{ code = "ENOENT", message = "...", detail = nil }  -- detail es opcional
```

Se capturan con `pcall`. Ramifica siempre sobre `code` (estable, parte del
contrato), nunca sobre `message`.

```lua
local ok, err = pcall(function() return enu.fs.read(ruta) end)
if not ok then
  if err.code == "ENOENT" then
    -- el fichero no existe: crea uno por defecto
  else
    error(err)  -- re-lanza lo que no sabes manejar
  end
end
```

### Códigos reservados v1

`ENOENT`, `EEXIST`, `EACCES`, `EIO`, `EHTTP`, `ENET`, `ETIMEOUT`, `ECANCELED`,
`EBUDGET`, `EINVAL`, `ECLOSED`.

Dos son especiales: **`ECANCELED`** (cancelación) y **`EBUDGET`** (watchdog)
nombran abortos **no capturables** —desenrollan la pila sin pasar por `pcall`— y
solo sirven para *observarlos*, p. ej. en el resultado de `Task:await`.

Las extensiones acuñan sus propios códigos con la misma forma, fuera de esta
lista (p. ej. `EPROVIDER`, `EAGENT`).

## Unidades y tipos comunes

- **Tiempos en milisegundos.** Toda función con IO acepta `opts.timeout_ms`
  (lanza `ETIMEOUT`).
- **Rutas como strings UTF-8.**
- Los **handles** del core (Task, Region, Proc, Worker…) son userdata opacos con
  métodos (se llaman con `:`, p. ej. `task:await()`).

## Estabilidad

Congelar v1 = congelar las firmas y semánticas: solo cambian **por adición**, y
cada adición incrementa `enu.version.api`. El código escrito contra un nivel sigue
siendo válido en los siguientes. Detecta capacidades con
[`enu.has()`](/enu/api/enu/), nunca comparando versiones.
