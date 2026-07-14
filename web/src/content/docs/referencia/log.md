---
title: nu.log — logging
description: Logging a fichero con nivel y plugin de origen. print es un alias de nu.log.info.
---

`nu.log` es el logging del runtime. Disponible en workers **[W]**. Escribe **a un
fichero** en `data_dir`, con el plugin de origen anotado automáticamente —
**nunca a la pantalla**: la UI es de las extensiones, no del core—.

## Niveles

```
nu.log.debug(fmt, ...) [W]
nu.log.info(fmt, ...) [W]
nu.log.warn(fmt, ...) [W]
nu.log.error(fmt, ...) [W]
```

`fmt` usa el formato de `string.format`:

```lua
nu.log.info("procesados %d ficheros en %d ms", n, dur)
nu.log.warn("reintentando: %s", err.message)
```

## `print` es `nu.log.info`

En el baseline de Lua de `nu`, `print` está **redirigido a `nu.log.info`**: va al
log, no a stdout. Esto es deliberado —el IO de pantalla pasa por `nu.ui` o por
los valores de retorno de `nu -e`—.

```sh
nu -e 'print("esto va al log, no aquí"); return "esto sí sale"'
```

```
esto sí sale
```

:::caution[No uses print para salida de usuario]
Si quieres que algo aparezca en la terminal: en headless, devuélvelo con `return`
(en `nu -e`) o escríbelo a stdout vía la extensión correspondiente; en una TUI,
píntalo con [`nu.ui`](/nu/referencia/ui/). `print`/`nu.log.*` son para
diagnóstico, y su destino es el fichero de log en
[`nu.config.data_dir()`](/nu/referencia/plugin/#directorios).
:::
