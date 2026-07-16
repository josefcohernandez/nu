---
title: "El contrato [W] no define la identidad/dueño de un worker para las primitivas atribuidas por owner"
type: "hallazgo"
id: "G56"
status: "resuelto"
date: "2026-07-16"
origin: "auditoría de seguridad 2026-07-16 (SEC-07)"
resolution: "Un worker porta como identidad la foto del plugin dueño capturada en el spawn, inmutable, cerrando también el data race de SEC-05."
affected: ["api.md §13/§16", "agente.md"]
adr: "ADR-024"
---
# G56 · El contrato [W] no define la identidad/dueño de un worker para las primitivas atribuidas por owner — `api.md` §13/§16 / `agente.md` — **RESUELTO**

**Resolución** (2026-07-16; ADR-024; aclaración semántica en
[api.md](api.md) §13/§14/§15/§16 y [agente.md](agente.md) §9 — sin firma
nueva: `enu.version.api` no se mueve). **Foto del dueño en el spawn**: un
worker porta como identidad el plugin dueño vigente en el momento de
`enu.worker.spawn`, capturada en el estado principal —donde la pila de dueños
es coherente por construcción— e **inmutable** durante toda su vida. Toda
primitiva [W] atribuida por dueño usa esa identidad fija: `enu.log` la anota
como plugin de origen y los procesos de `enu.proc` lanzados desde el worker
se registran bajo ese plugin; en los artefactos de atribución se distingue
como `<plugin> (worker)` (p. ej. `agent (worker)`), para que la traza diga
quién *y desde dónde*. Consecuencia de supervisión: `enu.plugin.reload` del
plugin dueño sigue soltando también los procesos lanzados por sus workers —
coherente con el árbol de supervisión en el que el estado principal posee
todos los workers (P11, que no se reabre: nada aquí necesita workers
anidados). Alternativas descartadas: el owner fijo `"worker"` (pierde la
traza de *qué plugin* lo lanzó y saca esos procesos del alcance de `reload`:
fuga de supervisión), el nombre del módulo como owner (el módulo no es
identidad ante el loader — la identidad es el nombre del plugin, G26), y
negar la atribución en workers (dejaría `log`/`proc` [W] de segunda clase y
procesos huérfanos sin dueño). Cierra de paso **SEC-05**: al viajar la
identidad **copiada** en el spawn —como los mensajes— y quedar prohibida la
lectura en vivo de `rt.ownerStack` del padre desde la goroutine del worker,
el data race deja de existir **por diseño**, no por candado. (Origen: SEC-07
de [auditoria-seguridad-2026-07-16.md](audits/auditoria-seguridad-2026-07-16.md).)

**Problema.** Las primitivas marcadas [W] que se atribuyen a un "dueño" (p. ej.
`enu.log`, `enu.proc`) no tienen definido bajo qué identidad corren cuando se
invocan **desde un worker**, donde no hay una task-padre viva de la que heredar el
owner. El contrato calla, y la implementación resuelve el hueco leyendo
`rt.ownerStack` del padre —lo que además introduce el data race de SEC-05 (dos
hilos tocando esa pila)—. Detectado en SEC-07 (2026-07-16); su resolución elimina
de paso la causa raíz de SEC-05.

**Impacto.** Comportamiento indefinido (y no determinista, por la carrera) en la
atribución de logs y procesos lanzados desde workers; ambigüedad de auditoría
sobre "quién hizo qué".
