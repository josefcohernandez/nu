---
title: "`on_message` vs `recv` simultáneos"
type: "hallazgo"
id: "G8"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "on_message y recv son mutuamente excluyentes: registrar uno con el otro pendiente lanza EINVAL de inmediato."
affected: ["api.md §13"]
---
# G8 · `on_message` vs `recv` simultáneos — `api.md` §13 — **RESUELTO**

**Resolución** (aplicada en [api.md](../contracts/api.md) §13): mutuamente excluyentes,
`EINVAL` en el acto al registrar uno con el otro pendiente. Prioridad
silenciosa descartada (esconde el bug); competencia por cola descartada
(no determinismo de serie).

**Problema.** Son "alternativas" pero nada impide usar ambas sobre el
mismo worker: ¿quién recibe el mensaje? Indefinido.

**Impacto.** Menor, pero es exactamente el tipo de indefinición que
genera bugs irreproducibles.

**Opciones.** (a) Mutuamente excluyentes: registrar `on_message` con un
`recv` pendiente (o viceversa) lanza `EINVAL`; (b) `on_message` gana
siempre y `recv` tras él lanza; (c) cola única y cualquier consumidor
compite (no determinista — probablemente descartable).
