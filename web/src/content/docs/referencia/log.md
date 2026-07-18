---
title: enu.log — logging
description: Logging a fichero con nivel y plugin de origen. print es un alias de enu.log.info.
---

`enu.log` es el logging del runtime. Disponible en workers **[W]**. Escribe **a un
fichero** en `data_dir`, con el plugin de origen anotado automáticamente —
**nunca a la pantalla**: la UI es de las extensiones, no del core—.

## Niveles

```
enu.log.debug(fmt, ...) [W]
enu.log.info(fmt, ...) [W]
enu.log.warn(fmt, ...) [W]
enu.log.error(fmt, ...) [W]
```

`fmt` usa el formato de `string.format`:

```lua
enu.log.info("procesados %d ficheros en %d ms", n, dur)
enu.log.warn("reintentando: %s", err.message)
```

## `print` es `enu.log.info`

En el baseline de Lua de `enu`, `print` está **redirigido a `enu.log.info`**: va al
log, no a stdout. Esto es deliberado —el IO de pantalla pasa por `enu.ui` o por
los valores de retorno de `enu -e`—.

```sh
enu -e 'print("esto va al log, no aquí"); return "esto sí sale"'
```

```
esto sí sale
```

:::caution[No uses print para salida de usuario]
Si quieres que algo aparezca en la terminal: en headless, devuélvelo con `return`
(en `enu -e`) o escríbelo a stdout vía la extensión correspondiente; en una TUI,
píntalo con [`enu.ui`](/enu/api/ui/). `print`/`enu.log.*` son para
diagnóstico, y su destino es el fichero de log en
[`enu.config.data_dir()`](/enu/api/plugin/#directorios).
:::
