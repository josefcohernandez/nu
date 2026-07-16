---
title: "El lock de sesión necesita el pid PROPIO y la API no lo expone"
type: "hallazgo"
id: "G32"
status: "resuelto"
date: "2026-06-22"
origin: "construcción de la extensión sesiones (S38)"
resolution: "enu.sys.pid() expone el pid propio del proceso, cuarta primitiva que completa el lockfile de sesiones (API 1→2)."
affected: ["api.md §7", "sesiones.md §6"]
---
# G32 · El lock de sesión necesita el pid PROPIO y la API no lo expone — `api.md` §7 / `sesiones.md` §6 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §7/§16/§17 y
[sesiones.md](sesiones.md) §6): una primitiva mínima —`enu.sys.pid() ->
integer`, el pid del proceso `enu` actual—, consulta local inmediata (no ⏸) y
[W] como el resto de `enu.sys`. Junto a `enu.sys.hostname()` forma la **identidad
del escritor** que el lock graba (`{ pid, hostname, started }`, §6). Es la
cuarta pieza que el corolario de completitud (filosofía §2) reclama: G17 añadió
`fs.write{exclusive}` + `enu.proc.alive(pid)` + `enu.sys.hostname()` para *crear*
el lock y *validar pids ajenos*, pero se le escapó la forma de conocer el pid
**propio** que va dentro del lock. Como es la **primera adición a la superficie
sagrada tras el congelado**, `enu.version.api` sube de 1 a **2** (api.md §17:
crece solo por adición, el contador se incrementa con cada una); es estricta
adición, no rompe ninguna firma. La primitiva dedicada se justifica como las
de G17: es vocabulario del **kernel** (un pid es del proceso, no del producto)
y no se compone con lo existente —`enu.proc` solo conoce los pids de los hijos
que lanza, jamás el del propio `enu`—. Descartado derivarlo de un subproceso
(`enu.proc.run(["sh","-c","echo $PPID"])` es frágil, caro y POSIX-only) y
descartado plegarlo a `enu.proc.alive` (es existencia de un pid dado, no
descubrimiento del propio).

**Problema.** El lockfile de [sesiones.md](sesiones.md) §6 graba
`{ pid, hostname, started }` con el **pid del proceso que escribe**, pero
[api.md](api.md) no lo expone: `enu.sys` da `platform`/`env`/`setenv`/`now_ms`/
`mono_ms`/`hostname` (sin pid) y `enu.proc.alive(pid)` valida pids **ajenos**
(para detectar locks huérfanos) pero no hay forma de obtener el **propio**. Sin
él la extensión sesiones (S38) no puede escribir el lock especificado: misma
clase de grieta que G17 (resolución correcta en prosa, no escribible con la API
especificada), y nacida igual al *construir* el contra-código (S38), no en una
ronda de pseudocódigo.

**Impacto.** Bloquea S38 (la extensión sesiones); reabre de hecho G5/G17 (la
corrupción de sesiones que cerraban vuelve a ser posible si el lock no se puede
escribir como está especificado). Barato de cerrar ahora, sobre la superficie
que se congela.

**Opciones.** (a) `enu.sys.pid() -> integer` (la elegida): mínima, vocabulario de
kernel, hermana de `hostname`; (b) ampliar `enu.proc` con un `enu.proc.self()` —
mete el pid propio en el módulo de *subprocesos*, donde no encaja (proc gestiona
hijos); (c) rebajar el contenido del lock a solo `{ hostname, started }` y
confiar la unicidad al `O_EXCL` — pierde la detección de huérfanos por pid de
§6 (un crash dejaría el lock para siempre), descartable.

**Problema.** Surgió **construyendo** la quilla (S04), no en una ronda de
pseudocódigo. gopher-lua (semántica Lua 5.1) no deja que una corrutina ceda a
través de una frontera de llamada Go. Verificado contra v1.1.2: (1)
`pcall(fn)` con `fn` que suspende **aborta** la corrutina en el `pcall` en vez
de ceder — pero §1.4 promete capturar los errores estructurados con `pcall`,
y el pseudocódigo lo hace alrededor de operaciones que hacen IO (⏸); (2)
`return ⏸fn()` en cola pierde la continuación (el `OP_TAILCALL` elide el frame
antes del yield). Misma raíz: el yield no cruza fronteras Go.

**Impacto.** Fundacional: sin esto el modelo de errores de §1.4 (pcall sobre
código que suspende) no se sostiene, y toda la API ⏸ tiene footguns en
posición de cola. Es la quilla — barato de cerrar aquí, carísimo después.

**Opciones.** (a) **Goroutine-por-task + token Lua** (sin yield):
pcall/tail call/errores nativos — la elegida (ADR-011); (b) seguir con el
puente de corrutinas y construir un `pcall` *yieldable* (pcall como
sub-corrutina) + trampolines Lua para las tail calls: más invasivo, defería un
`pcall` roto-por-defecto y aún así frágil; (c) cambiar de runtime Lua —
desproporcionado (ADR-002 ya está decidido). El desenrollado **no capturable**
de S08 (cancelación/watchdog) se diseñará sobre (a) con un panic centinela
propio, no con el yield aquí descartado.
