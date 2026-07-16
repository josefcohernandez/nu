---
title: "El lockfile de sesiones no es implementable con la API actual"
type: "hallazgo"
id: "G17"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "Tres primitivas mínimas -fs.write{exclusive}, proc.alive(pid) y sys.hostname()- permiten implementar el lockfile de sesiones en Lua."
affected: ["api.md §5-§7", "sesiones.md §6"]
---
# G17 · El lockfile de sesiones no es implementable con la API actual — `api.md` §5-§7 / `sesiones.md` §6 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §1.4/§5/§6/§7 y
[sesiones.md](sesiones.md) §6): tres primitivas genéricas mínimas —
`opts.exclusive = true` en `enu.fs.write` (creación atómica
solo-si-no-existe vía `O_EXCL`, sin temporal+rename, lanza el nuevo código
reservado `EEXIST`), `enu.proc.alive(pid)` (existencia, no identidad: un
pid reciclado da `true`) y `enu.sys.hostname()`. El lockfile sigue siendo
lógica de la extensión del agente, en Lua. El `enu.fs.lockfile` dedicado se
descartó (metería la política de sesiones — pids, huérfanos, hostnames —
en el kernel: el core da garantías, no comodidades); el best-effort se
descartó ("casi bien es peor que no").

**Problema.** La resolución de G5 exige tres piezas que [api.md](api.md)
no tiene: (1) creación **exclusiva** de fichero — `enu.fs.write` es atómico
vía temporal+rename, pero rename *sobreescribe*: dos procesos pueden
"ganar" el lock a la vez; (2) comprobar si un `pid` ajeno está vivo
(`enu.proc` solo gestiona hijos propios) — necesario para limpiar locks
huérfanos; (3) el `hostname` (no está en `enu.sys`) — necesario para el
contenido del lock.

**Impacto.** G5 quedó resuelto en prosa pero no se puede escribir con la
API especificada; la corrupción de sesiones que cerraba sigue siendo
posible. Mismo tipo de grieta que cazaban las rondas de pseudocódigo —
esta se escapó porque G5 se resolvió sin escribir el código.

**Opciones.** (a) Tres primitivas mínimas: `opts.exclusive = true` en
`enu.fs.write` (lanza si el fichero existe), `enu.proc.alive(pid) ->
boolean`, `enu.sys.hostname() -> string`; (b) una primitiva dedicada
`enu.fs.lockfile(path, meta) -> Lock` que empaquete la semántica completa
de sesiones.md §6 (menos superficie general, más opinionada); (c) rebajar
G5 a best-effort (asumir la carrera como improbable) — probablemente
descartable: "casi bien es peor que no".
