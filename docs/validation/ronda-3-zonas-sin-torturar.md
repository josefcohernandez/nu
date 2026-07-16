---
title: "Ronda 3: las zonas sin torturar"
type: "ronda"
id: "ronda-3"
zone: "las zonas sin torturar"
status: "cerrada"
scenarios: [12, 13, 14, 15, 16, 17]
---
# Ronda 3: las zonas sin torturar

Cambio de método: esta ronda **no aplica resoluciones** — cada grieta va a
la lista de problemas abiertos ([problemas.md](../findings/README.md)) para
resolverse una a una.

## Escenario 12: resize del terminal con un modal abierto

```lua
-- El picker del escenario 5, con el terminal a 120 columnas:
local reg = enu.ui.region{ x = 4, y = 2, w = enu.ui.size().w - 8, h = 20, z = 100 }
-- El usuario encoge el terminal a 60 columnas. ¿Y ahora qué?
--   · La región tiene w = 112 sobre una pantalla de 60: ¿se recorta? ¿error?
--     La spec define el clipping de blit DENTRO de la región, pero no qué
--     hace una región que se sale de la pantalla.                    [G1]
--   · Nadie recoloca el picker: no se suscribió a "ui:resize". ¿Convención,
--     anclajes declarativos (x = "center"), o cada plugin a su suerte? [G1]
```

## Escenario 13: el ciclo de desarrollo del autor de plugins

```lua
-- Edito mi plugin y quiero probarlo SIN reiniciar enu:
enu.plugin.reload("mi-plugin")   -- ← no existe
-- Y aunque existiera: require cachea módulos; re-ejecutar init.lua
-- duplicaría tools, comandos, keymaps y hooks (no hay des-registro masivo).
-- Todos los registros devuelven handle (Sub, Keymap, Hook...), pero nadie
-- los rastrea por plugin → no se puede deshacer "todo lo de mi-plugin".
-- Hoy la única vía es reiniciar enu en cada iteración.               [G2]
-- (Mismo agujero menor: editar providers.toml o enu.toml en caliente.)
```

## Escenario 14: dos sesiones de agente en la misma UI

```lua
-- Un subagente en marcha + la sesión principal, ambos emitiendo:
enu.events.emit("agent:delta", { text = ev.text })        -- ¿de QUIÉN es?
-- Los contratos no OBLIGAN a llevar session_id en cada payload agent:*;
-- chat.md tampoco dice que filtre. Dos turnos concurrentes mezclarían
-- deltas en el mismo bloque.                                        [G3]

-- Y si ambas sesiones piden permiso a la vez: dos modales simultáneos
-- sobre la misma pila de input — ¿cola de modales? Sin definir.     [G3]

-- Reentrada: el usuario pulsa enter con un turno en vuelo:
session:send("otra cosa")   -- ¿EBUSY? ¿se encola? ¿cancela y reemplaza?
-- Sin definir; cada UI improvisaría una semántica distinta.         [G4]
```

## Escenario 15: la misma sesión reanudada en dos terminales

```lua
-- Terminal A: enu --continue  → abre sessions/proy/2026-...jsonl
-- Terminal B: enu --continue  → ¡abre EL MISMO fichero!
-- Dos procesos haciendo fs.append intercalado sobre un JSONL: corrupción
-- silenciosa (líneas entrelazadas). sesiones.md no contempla lock alguno.
--                                                                   [G5]
```

## Escenario 16: el subagente de solo lectura no se puede expresar

```lua
-- Quiero un subagente auditor: que lea TODO, que no escriba NADA.
local w = enu.worker.spawn("auditor", { caps = { "fs", "text", "search" } })
-- caps concede MÓDULOS ENTEROS: "fs" incluye write, remove, rename...
-- No existe "fs de solo lectura" ni caps por función o por ruta. La
-- granularidad módulo-entero se queda corta justo en el caso estrella
-- del sandboxing.                                                   [G6]
```

## Escenario 17: flecos detectados sin escenario propio

```lua
-- a) enu.fs.watch(path, fn): ¿recursivo o un solo path? ¿respeta
--    .gitignore? (vigilar node_modules/ = ráfaga infinita) ¿coalesce
--    ráfagas (git checkout toca 5000 ficheros)?                     [G7]

-- b) Worker:on_message(fn) y Worker:recv() son "alternativas", pero nada
--    prohíbe usar ambos: ¿quién recibe el mensaje? Indefinido.      [G8]

-- c) Windows: la tool bash hace { "sh", "-c", ... } (no existe sh),
--    Proc:kill habla de señales POSIX, y el input de terminal (IME,
--    teclas) difiere. ¿Cuál es el alcance v1 en Windows?            [G9]
```

Hallazgos G1-G9 consolidados con impacto y opciones en
[problemas.md](../findings/README.md).

---
