---
title: nu.sys — entorno y reloj
description: Plataforma, variables de entorno, relojes de pared y monotónico, hostname y pid.
---

`nu.sys` expone el entorno del proceso y los relojes. Todo disponible en workers
**[W]** y nada suspende: son consultas locales.

## `nu.sys.platform` [W]

```
nu.sys.platform() -> "linux" | "darwin" | "windows"
```

```sh
nu -e 'return nu.sys.platform()'
```

```
linux
```

## `nu.sys.env` / `nu.sys.setenv` [W]

```
nu.sys.env(name) -> string?
nu.sys.setenv(name, value)
```

Lee y fija variables de entorno. `setenv` afecta **solo a subprocesos futuros**
(no reescribe el entorno del proceso `nu` ya en marcha).

```lua
local home = nu.sys.env("HOME")
nu.sys.setenv("MI_FLAG", "1")   -- lo verán los nu.proc.run posteriores
```

## `nu.sys.now_ms` / `nu.sys.mono_ms` [W]

```
nu.sys.now_ms() -> number   -- reloj de pared (epoch ms)
nu.sys.mono_ms() -> number  -- reloj monotónico
```

Usa `now_ms` para timestamps; usa `mono_ms` para **medir duraciones** (no salta
con ajustes de hora).

```sh
nu -e '
local t0 = nu.sys.mono_ms()
local s = 0; for i=1,1000 do s = s + i end
return (nu.sys.mono_ms() - t0) >= 0
'
```

```
true
```

## `nu.sys.hostname` [W]

```
nu.sys.hostname() -> string
```

Nombre de la máquina. Junto a `pid` forma la **identidad del escritor** de los
locks de sesión.

## `nu.sys.pid` [W]

```
nu.sys.pid() -> integer
```

Pid del **propio** proceso `nu`. No lo confundas con
[`nu.proc.alive(pid)`](/nu/referencia/proc/#nuprocalive-w), que valida pids
*ajenos*: `pid()` es el tuyo.

```sh
nu -e 'return nu.sys.pid() > 0'
```

```
true
```

```lua
-- Identidad del escritor para un lockfile.
local quien = nu.sys.hostname() .. ":" .. nu.sys.pid()
```

:::note[Nivel de API]
`nu.sys.pid()` fue la primera adición a la API congelada (subió `nu.version.api`
de 1 a 2). Buen recordatorio de que la superficie **crece solo por adición**.
:::
