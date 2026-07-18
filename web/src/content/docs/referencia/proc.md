---
title: enu.proc — subprocesos
description: Ejecutar y controlar subprocesos — run con buffers, spawn con streams, y detección de procesos vivos.
---

`enu.proc` lanza subprocesos. Disponible en workers **[W]**. **Sin shell
implícita**: `argv` es un array de strings; quien quiera shell la invoca
explícitamente (`{"sh", "-c", "..."}`).

## `enu.proc.run` ⏸ [W]

```
enu.proc.run(argv: string[], opts?) -> { code, stdout, stderr }
```

Conveniencia con buffers: ejecuta, espera y devuelve la salida completa. `opts`:
`cwd`, `env`, `stdin`, `timeout_ms`.

```sh
enu -e '
enu.task.spawn(function()
  local r = enu.proc.run({ "echo", "hola" })
  enu.fs.write(enu.fs.tmpdir().."/o.txt", enu.json.encode(r))
  -- r == { code = 0, stdout = "hola\n", stderr = "" }
end)
return "ok"
'
```

Con entrada estándar y directorio:

```lua
enu.task.spawn(function()
  local r = enu.proc.run({ "grep", "TODO" }, {
    cwd = "/proyecto",
    stdin = "linea1\nTODO: algo\nlinea3\n",
    timeout_ms = 5000,
  })
  return r.stdout
end)
```

## `enu.proc.spawn` [W]

```
enu.proc.spawn(argv, opts?) -> Proc
```

Control fino con streams (para procesos largos o interactivos). Devuelve un
`Proc`:

```
Proc:write(data) ⏸ [W]                                  -- escribe en stdin
Proc:close_stdin() [W]
Proc:read_line(which: "stdout"|"stderr") -> string? ⏸ [W]  -- nil en EOF
Proc:read(which, n?) -> string? ⏸ [W]                   -- lectura cruda
Proc:wait() -> { code } ⏸ [W]
Proc:kill(signal?) [W]                                  -- por defecto TERM
```

```lua
enu.task.spawn(function()
  local p = enu.proc.spawn({ "cat" })
  enu.task.cleanup(function() p:kill() end)   -- red de seguridad

  p:write("una línea\n")
  p:close_stdin()

  local linea = p:read_line("stdout")        -- "una línea"
  local res = p:wait()                        -- { code = 0 }
end)
```

:::caution[Vida del proceso]
La regla es matar el proceso explícitamente vía
[`enu.task.cleanup`](/enu/api/task/#enutaskcleanup-w) en quien lo crea. Como
red de seguridad, un `Proc` sin referencias acaba matado por el GC, pero es **no
determinista**: no confíes en ello.
:::

## `enu.proc.alive` [W]

```
enu.proc.alive(pid: integer) -> boolean
```

¿Hay un proceso vivo con ese `pid` en esta máquina? Informa de **existencia, no
de identidad**: un pid reciclado da `true`. Sirve para detectar locks huérfanos
(combínalo con [`enu.sys.pid`](/enu/api/sys/) y `enu.sys.hostname`).

```lua
-- ¿El dueño del lock sigue vivo?
if not enu.proc.alive(pid_del_lock) then
  -- lock huérfano: se puede reclamar
end
```
