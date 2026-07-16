---
title: "Doble reanudación de la misma sesión"
type: "hallazgo"
id: "G5"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "Un escritor por sesión vía lockfile con pid/hostname/started; huérfanos se limpian solos y el conflicto real ofrece fork/solo-lectura/forzar."
affected: ["sesiones.md"]
---
# G5 · Doble reanudación de la misma sesión — `sesiones.md` — **RESUELTO**

**Resolución** (aplicada en [sesiones.md](sesiones.md) §6): un escritor por
sesión vía lockfile `<sesión>.jsonl.lock` con `{pid, hostname, started}`;
lectores sin lock; locks huérfanos (pid muerto local) se limpian en
silencio; conflicto real → aviso con fork por defecto / solo lectura /
forzar con confirmación. `flock` descartado (semántica impredecible en
Windows/red); auto-fork silencioso descartado (bifurca sin conocimiento
del usuario).

**Problema.** Dos procesos enu pueden abrir el mismo JSONL y hacer appends
intercalados: corrupción silenciosa. No hay lock.

**Impacto.** Pérdida de datos del usuario; barato de cerrar ahora, caro
después.

**Opciones.** (a) Lockfile junto al JSONL (`.lock` con pid; el segundo
proceso recibe error claro y ofrece fork); (b) lock advisory del SO
(flock) — ¿portabilidad Windows?; (c) detectar-y-fork automático: el
segundo `--continue` crea fork silenciosamente.
