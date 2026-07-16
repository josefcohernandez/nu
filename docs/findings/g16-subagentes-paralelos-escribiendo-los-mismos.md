---
title: "Subagentes paralelos escribiendo los mismos ficheros"
type: "hallazgo"
id: "G16"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "Se documenta como limitación conocida con remedio de repartir territorio por prompt entre subagentes; sin lock en el core."
affected: ["agente.md §9"]
---
# G16 · Subagentes paralelos escribiendo los mismos ficheros — `agente.md` §9 — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §9): limitación conocida
documentada + remedio prescrito (repartir territorio vía prompt, como los
harnesses de referencia). Lock en tools oficiales descartado: seguridad
falsa — bash y tools de terceros escriben sin pasar por él, prometería una
garantía incumplible ("casi bien es peor que no"). Detección a posteriori
descartada por el mismo agujero de cobertura.

**Problema.** Las tools de subagentes paralelos se intercalan en el
principal, pero nada coordina dos escrituras al mismo path:
last-write-wins silencioso.

**Impacto.** Calidad de resultados con subagentes paralelos; los
harnesses de referencia tampoco lo resuelven (mitigan repartiendo
territorio vía prompt).

**Opciones.** (a) Documentar como limitación conocida + guía ("reparte
territorio entre subagentes"); (b) lock advisory por fichero dentro de la
sesión (las tools oficiales de escritura lo respetan, aviso al chocar);
(c) detección a posteriori (aviso si dos subagentes tocaron el mismo
path).
