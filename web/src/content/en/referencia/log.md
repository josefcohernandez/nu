---
title: enu.log — logging
description: File logging with level and source plugin. print is an alias for enu.log.info.
---

`enu.log` is the runtime's logging. Available in workers **[W]**. Writes **to a
file** in `data_dir`, with the source plugin annotated automatically —
**never to the screen**: the UI belongs to the extensions, not the core—.

## Levels

```
enu.log.debug(fmt, ...) [W]
enu.log.info(fmt, ...) [W]
enu.log.warn(fmt, ...) [W]
enu.log.error(fmt, ...) [W]
```

`fmt` uses `string.format`'s format:

```lua
enu.log.info("procesados %d ficheros en %d ms", n, dur)
enu.log.warn("reintentando: %s", err.message)
```

## `print` is `enu.log.info`

In `enu`'s Lua baseline, `print` is **redirected to `enu.log.info`**: it goes to the
log, not stdout. This is deliberate —screen IO goes through `enu.ui` or through
`enu -e`'s return values—.

```sh
enu -e 'print("esto va al log, no aquí"); return "esto sí sale"'
```

```
esto sí sale
```

:::caution[Don't use print for user output]
If you want something to appear on the terminal: in headless, return it with `return`
(in `enu -e`) or write it to stdout via the corresponding extension; in a TUI,
paint it with [`enu.ui`](/enu/en/api/ui/). `print`/`enu.log.*` are for
diagnostics, and their destination is the log file at
[`enu.config.data_dir()`](/enu/en/api/plugin/#directories).
:::
