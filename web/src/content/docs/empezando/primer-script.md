---
title: Tu primer script
description: Ejecuta Lua con enu -e, entiende el estado principal frente a las tasks y maneja errores estructurados.
---

`enu` es, antes que nada, un runtime de Lua. La vía más rápida de probarlo es
`enu -e`, que **evalúa un chunk Lua sin TTY (headless) e imprime sus valores de
retorno**.

## Hola, enu

```sh
enu -e 'return "hola, " .. "enu"'
```

```
hola, enu
```

Cada valor que devuelvas (`return`) se imprime en su propia línea:

```sh
enu -e 'return 1, 2, 3'
```

```
1
2
3
```

Las tablas se imprimen como el `tostring` de Lua (`table: 0x...`), poco útil. Para
verlas, codifícalas con [`enu.json`](/enu/api/codecs/):

```sh
enu -e 'return enu.json.encode(enu.version)'
```

```
{"api":2,"major":0,"minor":1,"patch":0}
```

:::caution[`print` no va a la pantalla]
En `enu` `print` es un alias de `enu.log.info`: escribe al fichero de log, **nunca
a la pantalla** (la UI es de las extensiones, no del core). Para que algo
aparezca en stdout con `enu -e`, devuélvelo con `return`.
:::

## El estado principal y las tasks

Esta es la distinción que tienes que interiorizar desde el principio.

El chunk de `enu -e` corre en el **estado principal** (single-threaded, con event
loop). El estado principal **no es una task**, así que **no puede llamar
funciones suspendientes** (las marcadas con ⏸: casi todo el IO —`enu.fs.read`,
`enu.http.request`, `enu.proc.run`…). Si lo intentas:

```sh
enu -e 'return enu.fs.read("README.md")'
```

```
error: EINVAL: enu.fs.read solo puede llamarse dentro de una task
```

Para hacer IO, lanza una **task** con [`enu.task.spawn`](/enu/api/task/).
Dentro de una task, las funciones ⏸ se escriben en estilo secuencial (con
*await* implícito): no hay callbacks ni promesas.

```sh
enu -e '
enu.task.spawn(function()
  local texto = enu.fs.read("README.md")   -- ⏸ aquí sí vale
  enu.fs.write(enu.fs.tmpdir() .. "/copia.md", texto)
end)
return "lanzada"
'
```

`enu -e` espera a que **todas** las tasks que lanzó el chunk terminen antes de
salir, así que el efecto (el fichero copiado) ya ocurrió cuando el proceso
acaba. Lo que no puedes es *devolver* el resultado de la task como valor del
chunk: el `return` del chunk se evalúa antes de que la task corra. Para mover un
valor de una task a otra usa [`enu.task.future`](/enu/api/task/).

:::tip[¿Por qué esta separación?]
Es el modelo de concurrencia "del navegador": un hilo principal determinista
donde el IO bloqueante está prohibido (congelaría el event loop), más tasks
cooperativas para el trabajo asíncrono. Lo explicamos a fondo en [Conceptos
clave](/enu/docs/conceptos/).
:::

## Errores estructurados

Las funciones del core no devuelven `(valor, err)`: **lanzan** tablas
estructuradas con `code`, `message` y un `detail?` opcional. Se capturan con
`pcall`:

```sh
enu -e '
local ok, err = pcall(function() return enu.json.decode("{roto") end)
return ok, enu.json.encode(err)
'
```

```
false
{"code":"EINVAL","message":"...","detail":...}
```

El `code` es estable y forma parte del contrato (`ENOENT`, `EEXIST`, `EACCES`,
`ETIMEOUT`, `EINVAL`…). Es lo que ramificas en tu lógica, no el `message`. Ver
[Convenciones](/enu/api/convenciones/#errores) para la lista completa.

## Código de salida

`enu -e` sale con **0** si el chunk no lanzó, y con **1** si lanzó (un error sin
capturar). Útil en scripts y CI:

```sh
enu -e 'assert(enu.version.api >= 2)' && echo "API suficiente"
```

## Siguiente paso

Ya sabes ejecutar Lua y por qué el IO va en tasks. Si quieres ver el harness en
acción, sigue con [Tu primer agente](/enu/docs/primer-agente/). Si prefieres
el modelo mental completo, ve a [Conceptos clave](/enu/docs/conceptos/).
