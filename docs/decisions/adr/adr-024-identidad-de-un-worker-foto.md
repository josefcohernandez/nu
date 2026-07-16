---
title: "Identidad de un worker: foto del plugin dueño en el spawn, inmutable"
type: "adr"
id: "ADR-024"
status: "aceptada"
date: "2026-07-16"
---
# ADR-024 · Identidad de un worker: foto del plugin dueño en el spawn, inmutable

**Estado:** Aceptada · 2026-07-16 · resuelve G56 (SEC-07; cierra de paso SEC-05)

**Contexto.** Un worker (ADR-004, ADR-008) es un mini-runtime sin
`enu.plugin` ni ciclo de vida: su pila de dueños es intrínsecamente vacía.
Pero varias primitivas [W] atribuyen por dueño — `enu.log` anota el plugin de
origen; `enu.proc` registra los procesos por owner para que `plugin.reload`
los suelte (G2) — y necesitan una identidad también dentro de un worker. El
contrato callaba, y la implementación rellenó el hueco leyendo
`rt.ownerStack` del runtime padre desde la goroutine del worker: atribución
no determinista (vale lo que el principal esté haciendo en ese instante) y
una carrera de datos entre hilos. La auditoría de seguridad de 2026-07-16 lo
señaló como grieta de diseño (SEC-07, raíz común del data race SEC-05), y
abrió G56.

**Decisión.** La identidad de un worker es una **foto tomada en el spawn**:
`enu.worker.spawn` captura el plugin dueño vigente en el estado principal —
donde la pila de dueños es coherente por construcción, al ser single-threaded
(ADR-004)— y el worker la porta **inmutable** durante toda su vida. Toda
primitiva [W] atribuida por dueño usa esa identidad fija; en los artefactos
de atribución se anota distinguible como `<plugin> (worker)` (p. ej.
`agent (worker)`). Los procesos lanzados por un worker quedan registrados
bajo su plugin dueño, de modo que `plugin.reload` los alcanza igual que a
los del estado principal.

Alternativas descartadas: un owner fijo `"worker"` (determinista, pero ciego
— pierde qué plugin lo lanzó y saca esos procesos del alcance de `reload`:
fuga de supervisión); el nombre del módulo como owner (el módulo no es
identidad ante el loader — la identidad es el nombre del plugin, G26); negar
la atribución en workers (`log`/`proc` [W] de segunda clase, procesos
huérfanos sin dueño); y serializar con un candado la lectura de la pila viva
del padre (taparía la carrera, pero no el no-determinismo, que es el
problema de fondo).

**Consecuencias.**

- La lectura en vivo de estado del runtime padre desde la goroutine del
  worker queda **prohibida por contrato**: la identidad viaja copiada en el
  spawn, exactamente como los mensajes (ADR-008). El data race de SEC-05
  desaparece por diseño, no por candado.
- Atribución determinista y auditable: cada línea de log y cada proceso dice
  quién *y desde dónde*, sin depender de lo que el principal hiciera en el
  instante del cruce.
- El árbol de supervisión no tiene fugas por la frontera del worker: el
  estado principal posee todos los workers (P11) y `plugin.reload` sigue
  soltando también lo que sus workers crearon.
- Es aclaración semántica de contrato, no firma nueva: `enu.version.api` no
  se mueve.
- Regla para superficie futura: cualquier primitiva [W] que en adelante
  atribuya por dueño usa esta misma identidad capturada — no se inventan
  identidades ad hoc por módulo.
