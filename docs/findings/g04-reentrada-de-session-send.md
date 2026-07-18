---
title: "Reentrada de `Session:send`"
type: "hallazgo"
id: "G4"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "send con turno en vuelo encola el mensaje, que el loop inyecta al ensamblar el siguiente request, nunca a mitad de stream."
affected: ["agente.md §2"]
---
# G4 · Reentrada de `Session:send` — `agente.md` §2 — **RESUELTO**

**Resolución** (aplicada en [agente.md](../contracts/agente.md) §2): `send` con turno en
vuelo encola; el loop inyecta lo encolado al ensamblar el siguiente request
(nunca a mitad de stream). `cancel()` no vacía la cola
(`clear_queue()` aparte). Descartado `EBUSY` (cada UI reimplementaría la
cola de forma sutilmente distinta — justo lo que se quería evitar).

**Problema.** Llamar `send` con un turno en vuelo no está definido:
¿error, cola, o cancelar-y-reemplazar? Cada UI improvisaría una semántica
distinta.

**Impacto.** Contrato congelable; afecta a la UX básica (enter impaciente).

**Opciones.** (a) `EBUSY` y que la UI decida (mínimo, predecible); (b) el
motor encola mensajes y los anexa al siguiente turno (lo que hacen los
harnesses maduros); (c) configurable por sesión.
