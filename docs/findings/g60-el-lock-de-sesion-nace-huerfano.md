---
title: "El `.jsonl.lock` nace huérfano en el arranque del chat: `enu.task.cleanup` no puede ⏸, así que la promesa de liberación de `sesiones.md` §6 es inimplementable tal como está escrita"
type: "hallazgo"
id: "G60"
status: "abierto"
date: "2026-07-18"
origin: "investigación de la opción (c) de G58 (reproducido empíricamente con el binario)"
affected: ["api.md §3 (`enu.task.cleanup`)", "sesiones.md §6", "agente.md (`Session:close`)", "guia-plugins.md", "extensión sessions (init.lua)", "extensión chat (ciclo de vida de la sesión)", "malla.md (worktrees y locks de subagentes)"]
---
# G60 · El `.jsonl.lock` nace huérfano en el arranque del chat: `enu.task.cleanup` no puede ⏸ y la promesa de `sesiones.md` §6 es inimplementable — `api.md` §3 / `sesiones.md` §6 / sessions / chat

**Problema.** La investigación de la opción (c) de [G58](g58-el-chat-no-se-cierra-hasta.md)
refutó la hipótesis de que el `.jsonl.lock` sobreviviera por el camino de
apagado: el lock **no muere huérfano al salir — nace huérfano al arrancar**.
Reproducido con el binario real, la cadena tiene tres capas, de la más
superficial a la más profunda:

1. **Bug de orden en `sessions`.** `Session:close`
   (`sessions/lua/sessions/init.lua:288`) marca `closed=true` **antes** de
   intentar borrar el lock (`:292` precede a `:300`), y el borrado va dentro de
   un `pcall` que traga el error. Si el borrado falla, la sesión queda
   envenenada: cualquier cierre posterior retorna en el guard
   `if self.closed then return end` — **no-op** con el fichero intacto.
   (`Session:append`, que solo comprueba `read_only`, sigue escribiendo el
   transcript pese al `closed=true`, por eso el síntoma pasa desapercibido.)
2. **Bug de ciclo de vida en `chat`.** El chat abre la sesión dentro de una
   task **efímera** (`chat/init.lua:42-48`: el `enu.task.spawn` de `core:ready`
   arranca y retorna). Al morir esa task corre su `enu.task.cleanup`
   (`sessions/init.lua:365`, el que promete «soltar el lock pase lo que
   pase»)… en el momento equivocado: **al acabar el arranque**, no al salir.
   Ese cleanup dispara la capa 1 y deja `closed=true` + lock en disco antes de
   que el usuario escriba nada; el `Chat:quit` posterior, que sí podría borrar
   el lock (corre en task viva), es no-op. Misma familia que la task efímera
   de [G59](g59-el-auto-connect-de-mcp-toml.md).
3. **La grieta de contrato (la de verdad).** Los `enu.task.cleanup` corren en
   la frontera de `__finish`, **sin contexto de task** (`__current == nil`,
   `internal/vmwasm/host.go:515-541`): cualquier primitiva ⏸ dentro de un
   cleanup lanza `EINVAL`. El `enu.fs.remove` del lock es ⏸
   (`internal/runtime/vmwasm_fs.go`), así que el liberador registrado **no
   puede funcionar jamás**. Eso vuelve inimplementable la promesa de
   [sesiones.md](../contracts/sesiones.md) §6 de liberar el lock vía cleanup
   «pase lo que pase con la task» — y, en general, la liberación por cleanup
   de **cualquier recurso cuyo cierre necesite I/O suspendente**. Hoy la única
   recuperación real del lock es la **reclamación de huérfano** del siguiente
   proceso que abre (mismo host + pid muerto, `sessions/init.lua:163-201`).

**Impacto.** Tras cada arranque del chat, el `.jsonl.lock` de la sesión queda
huérfano en disco durante toda la vida del proceso y tras su salida (por
cualquier camino: `/quit`, `ctrl+c`, crash). En la práctica la reclamación por
pid muerto lo recupera en la siguiente apertura, así que el daño visible es
bajo — pero el contrato de `sesiones.md` §6 cuenta una garantía (el cleanup
como red de seguridad) que el kernel no permite implementar: exactamente el
corolario de completitud, o una doctrina mal contada. La suite e2e lo conoce
y **retiró** deliberadamente la aserción «el `.jsonl.lock` desaparece»
(`e2e/chat_test.go`), de modo que hoy ningún gate cubre la liberación del lock.

## Hechos verificados (investigación 2026-07-18, empírica sobre el binario)

Una segunda investigación acotó el espacio de diseño con estos hechos, todos
verificados en código y, donde se indica, ejecutando el binario:

- **H-A · El apagado interactivo no drena: destruye.** El camino de salida
  (`core:shutdown` → `drive()` retorna → `Runtime.Close`,
  `runtime.go:470-515`) cancela el bombeo (`PumpTasks` es «pausa, no muerte»)
  y demuele la VM **sin cancelar las tasks vivas ni ejecutar ningún cleanup**.
  Solo los caminos headless (`-e`/`-p`, vía `RunTasks`) drenan. Es un agujero
  general del mecanismo de cleanup, independiente del ⏸: en modo interactivo,
  los cleanups de tasks de vida larga no corren jamás al salir.
- **H-B · `enu.task.spawn` funciona dentro de un cleanup** (verificado
  empíricamente): `spawn` no es ⏸ y no exige `__current`
  (`host.go:424-439`). Un cleanup puede spawnear una task liberadora que haga
  el I/O ⏸; en headless esa task corre antes de salir (el `RunTasks` la
  drena). El ⏸ *directo* en el cleanup sigue lanzando `EINVAL`. La utilidad
  del patrón depende de H-A: sin bombeo posterior, la task spawneada no corre.
- **H-C · El único recurso estable roto por el ⏸-en-cleanup es el lock de
  sesión.** Inventario completo de usuarios de `enu.task.cleanup` en las
  extensiones embebidas: todos los demás cierres son **síncronos a
  propósito** (`Proc:kill`, `Ws:close`, `Worker:terminate`, `Stream:close`,
  `Sub:cancel`, `Future:set`, `emit`) y funcionan hoy. G59/mcp NO está
  afectado por el ⏸ (su cleanup es `Proc:kill` síncrono; su problema es de
  task efímera, la capa 2). Pero en `mesh` (borrador v0.1) hay **dos
  afectados más**: el borrado de worktrees git (`enu.proc.run`, ⏸) y los
  cierres de sesión de subagentes (`enu.fs.remove`, ⏸).
- **H-D · `agente.md` contiene una afirmación falsa:** «`Session:close()` …
  síncrona a propósito: llamable desde enu.task.cleanup» (`agente.md:47`).
  No lo es: llama a `enu.fs.remove` (⏸). El texto literal de `sesiones.md` §6
  solo promete «se libera al salir» y la reclamación de huérfanos; la promesa
  del cleanup vive en el código y en `agente.md`.
- **H-E · `flock` se descartó en una línea hoy medio caduca.**
  `sesiones.md:156-158` lo rechazó por «semántica predecible en Windows y
  filesystems de red». G9 sacó Windows nativo de la v1 (solo WSL2, POSIX
  íntegro), y la ventaja decisiva de flock (el kernel del SO libera el lock al
  morir el proceso, incluso `kill -9`) nunca se sopesó. Sigue en pie el
  argumento de los filesystems de red — y flock es estrictamente
  **mono-host**, inservible para una malla.
- **H-F · La reclamación de huérfanos tiene un punto ciego: el pid
  reciclado.** `enu.proc.alive` informa de existencia, no de identidad
  (`api.md:188`); un pid del escritor muerto reasignado por el SO clasifica el
  lock huérfano como «busy» y lo vuelve irrecuperable por la vía automática.
  Falla siempre hacia el lado seguro (nunca roba un lock vivo), pero falla.
  El campo `started` del lock se graba y **nunca se consulta**; no hay boot id.

## Mapa de opciones (ampliado 2026-07-18)

**Familia A — hacer más potente el mecanismo (cambios de kernel):**

- **A1 · Permitir ⏸ en los cleanups** (micro-task por liberador, LIFO
  preservado). Máxima ergonomía («registro y me olvido»), máximo coste de
  especificación: qué pasa si un cleanup se cuelga (¿watchdog? entonces la
  garantía tiene asterisco; ¿inmatable? entonces rehén del apagado), orden
  entre cleanups suspendientes (serie lenta vs. paralelo sin LIFO), con qué
  caps/dueño/presupuesto corre (ADR-008/G56), qué ve durante el desmontaje, y
  errores parciales. Además, por H-A, *también* necesitaría el drenaje del
  apagado o seguiría sin correr al salir. Ni siquiera A1 cubre el crash.
- **A2 · Drenaje con plazo en el apagado.** Al apagar: cancelar las tasks
  vivas y bombear el scheduler con deadline antes de demoler (puntos
  naturales: el `defer` de `drive()` o el arranque de `Runtime.Close`). **No
  toca la API sagrada** (comportamiento interno). Combinado con H-B (cleanup
  → `spawn` de la task liberadora), suelta el lock y borra worktrees en toda
  salida limpia sin permitir ⏸ en cleanups, y cierra de paso el agujero
  general H-A. Contras: el patrón cleanup→spawn es sutil (documentarlo) y hay
  que fijar el plazo interno.
- **A3 · Última voluntad declarativa.** Registrar *datos* de deshacer
  (`{op="remove", path=...}`) que Go ejecuta al morir la task o el proceso.
  Incolgable por construcción. Contras: superficie de API nueva y
  expresividad mínima (cubre el lock; no cubre el worktree, que necesita git).

**Familia B — doctrina y espacio de usuario (sin tocar kernel):**

- **B1 · Conserje + cierre explícito antes del shutdown.** Los recursos
  persistentes viven bajo una task de vida larga y se cierran explícitamente
  *antes* de emitir `core:shutdown` (es lo que `/quit` ya hace bien; el
  keymap `ctrl+c` solo necesita spawnear como hace `submit`). Cero cambios de
  kernel; no cubre crash/`kill -9`.
- **B2 · Lease con renovación + reconciliación externa.** El dueño renueva el
  lock periódicamente (mtime/heartbeat); lock rancio = reclamable, sin
  depender de `proc.alive` (cierra H-F). Generaliza a cualquier recurso
  persistente vía *reaper* (reconciliación al siguiente arranque o desde un
  supervisor). Es el único mecanismo que sobrevive a `kill -9`, pid
  reciclado, NFS y **multi-host** (malla).

**Familia C — mover el problema al SO:**

- **C1 · `flock` híbrido** (lock advisory del SO + fichero con metadatos
  legibles). Liberación por el kernel del SO pase lo que pase; cero
  huérfanos locales. Contras: adición a la API sagrada (handle que debe vivir
  abierto), poco fiable en filesystems de red (frecuentes en corporativo) y
  **mono-host**: no generaliza a la malla. Reabriría la decisión de
  `sesiones.md:156-158` con la evidencia nueva de H-E.

**Familia D — mínimo honesto:**

- **D1 · Solo capas 1+2 + reescritura documental.** Arreglar `Session:close`
  y el ciclo de vida del chat, corregir H-D, documentar que la garantía es la
  reclamación. Con H-A y H-F sobre la mesa, insuficiente por sí sola.

## Propuesta de resolución (2026-07-18 — PENDIENTE de confirmación)

> Contexto que pesa en la elección: el reposicionamiento del proyecto como
> **motor para construir coding harnesses a medida** en entornos
> corporativos, con `mesh` como columna vertebral y la resiliencia a fallos
> como pilar. Eso (a) convierte a los autores de harnesses en clientes de la
> API pública — la garantía de liberación pasa a ser propuesta de valor —,
> (b) asciende los recursos de `mesh` (worktrees, locks de subagentes) de
> «borrador» a caso primario, disparando ya el criterio del «segundo recurso
> real», y (c) degrada C1/flock (mono-host, NFS) frente a B2 (multi-host).

Paquete de cuatro piezas, en capas de garantía:

1. **Doctrina: recursos persistentes = lease reclamable + reconciliación
   (B2, ascendida).** Todo recurso persistente debe poder reclamarse desde
   fuera por identidad verificable: lease renovado por el dueño; lo rancio lo
   reconcilia el siguiente proceso o un reaper (worktrees huérfanos incluidos).
   Es la única corrección total (`kill -9`, pid reciclado, NFS, multi-host).
   Se escribe en `sesiones.md` §6 y nace como principio en `malla.md`.
2. **A2: el apagado cancela y drena con plazo.** Cambio interno del kernel,
   sin API nueva. Con el patrón cleanup→spawn (H-B), da prontitud en toda
   salida limpia y cierra el agujero general H-A.
3. **Capas 1+2 + honestidad documental (siempre).** Orden correcto en
   `Session:close`; la sesión del chat bajo task de vida larga con cierre
   explícito (B1); corregir `agente.md:47` (H-D); documentar en `api.md` §3 y
   `guia-plugins.md` la restricción real de los cleanups (síncronos,
   solo-memoria) y el patrón cleanup→spawn.
4. **A1 se pospone como P## con disparador:** «evidencia de fricción real de
   autores de harnesses con el patrón cleanup→spawn». C1 (flock) se descarta
   como columna vertebral por mono-host + NFS; reconsiderable solo como
   optimización local si algún día un lease local resultara insuficiente.

Jerarquía de garantías resultante, contable a un cliente: *cleanup* =
prontitud en memoria; *drenaje* = prontitud de I/O en salida limpia; *lease +
reaper* = corrección pase lo que pase, incluso quitando el enchufe.

La entrada queda **ABIERTA** hasta confirmar la propuesta; al resolverse,
aplicar a todos los documentos afectados (y valorar si H-A merece hallazgo
propio o queda absorbido aquí).

**Disparador de reapertura.** — (abierto). Afecta a: cualquier sesión que
toque `enu.task.cleanup`, el ciclo de vida de la sesión del chat, el texto de
`sesiones.md` §6, o la estabilización de `malla.md`.
