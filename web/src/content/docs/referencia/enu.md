---
title: enu — raíz
description: Versión del runtime, nivel de API y detección de capacidades con enu.has.
---

El namespace raíz expone la versión del runtime y la detección de capacidades.
Es lo primero que toca cualquier plugin que quiera ser portable.

## `enu.version` [W]

```
enu.version -> { major, minor, patch, api: integer }
```

Versión del runtime y **nivel de API** del core. `api` es el número que crece con
cada adición a la superficie sagrada; úsalo para exigir un mínimo, pero prefiere
[`enu.has`](#enuhas) para detectar capacidades concretas.

```sh
enu -e 'return enu.json.encode(enu.version)'
```

```
{"api":2,"major":0,"minor":1,"patch":0}
```

```lua
-- Exigir un nivel mínimo de API.
assert(enu.version.api >= 2, "este plugin necesita api >= 2")
```

## `enu.has` [W]

```
enu.has(cap: string) -> boolean
```

Detección de capacidades para extensiones portables. Devuelve si una capacidad
está disponible en este runtime/entorno. Cubre tanto rasgos finos
(`"ui.images"`, `"net.tcp"`) como **módulos enteros**: en headless `enu.ui` no
existe, y `enu.has("ui")` es la forma correcta de saberlo —nunca probar y
capturar el error—.

```sh
enu -e 'return enu.has("ui")'
```

```
false
```

(En `enu -e` no hay TTY, así que `enu.ui` no existe y `enu.has("ui")` es `false`.)

```lua
-- Degradar con elegancia según el entorno.
if enu.has("ui") then
  -- pintar una región
else
  -- modo headless: solo texto a stdout/log
end
```

:::tip[Capacidades, no versiones]
`enu.has` es el mecanismo de detección recomendado por encima de comparar
`enu.version.api`. Una capacidad puede estar ausente por el *entorno* (headless,
terminal sin soporte de imágenes), no solo por el nivel de API.
:::
