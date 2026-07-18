---
title: enu.sys — entorno y reloj
description: Plataforma, variables de entorno, relojes de pared y monotónico, hostname y pid.
---

`enu.sys` expone el entorno del proceso y los relojes. Todo disponible en workers
**[W]** y nada suspende: son consultas locales.

## `enu.sys.platform` [W]

```
enu.sys.platform() -> "linux" | "darwin" | "windows"
```

```sh
enu -e 'return enu.sys.platform()'
```

```
linux
```

## `enu.sys.env` / `enu.sys.setenv` [W]

```
enu.sys.env(name) -> string?
enu.sys.setenv(name, value)
```

Lee y fija variables de entorno. `setenv` afecta **solo a subprocesos futuros**
(no reescribe el entorno del proceso `enu` ya en marcha).

```lua
local home = enu.sys.env("HOME")
enu.sys.setenv("MI_FLAG", "1")   -- lo verán los enu.proc.run posteriores
```

## `enu.sys.now_ms` / `enu.sys.mono_ms` [W]

```
enu.sys.now_ms() -> number   -- reloj de pared (epoch ms)
enu.sys.mono_ms() -> number  -- reloj monotónico
```

Usa `now_ms` para timestamps; usa `mono_ms` para **medir duraciones** (no salta
con ajustes de hora).

```sh
enu -e '
local t0 = enu.sys.mono_ms()
local s = 0; for i=1,1000 do s = s + i end
return (enu.sys.mono_ms() - t0) >= 0
'
```

```
true
```

## `enu.sys.hostname` [W]

```
enu.sys.hostname() -> string
```

Nombre de la máquina. Junto a `pid` forma la **identidad del escritor** de los
locks de sesión.

## `enu.sys.pid` [W]

```
enu.sys.pid() -> integer
```

Pid del **propio** proceso `enu`. No lo confundas con
[`enu.proc.alive(pid)`](/enu/api/proc/#enuprocalive-w), que valida pids
*ajenos*: `pid()` es el tuyo.

```sh
enu -e 'return enu.sys.pid() > 0'
```

```
true
```

```lua
-- Identidad del escritor para un lockfile.
local quien = enu.sys.hostname() .. ":" .. enu.sys.pid()
```

:::note[Nivel de API]
`enu.sys.pid()` fue la primera adición a la API congelada (subió `enu.version.api`
de 1 a 2). Buen recordatorio de que la superficie **crece solo por adición**.
:::
