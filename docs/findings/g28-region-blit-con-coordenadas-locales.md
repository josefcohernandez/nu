---
title: "`Region:blit` con coordenadas locales negativas (viewport/scrollback)"
type: "hallazgo"
id: "G28"
status: "resuelto"
origin: "ronda 6 de pseudocódigo (harness de coding sobre enu.ui)"
resolution: "Region:blit acepta x/y negativos y recorta también el borde inicial del Block, dando un viewport sin coste en Lua."
affected: ["api.md §9.1"]
---
# G28 · `Region:blit` con coordenadas locales negativas (viewport/scrollback) — `api.md` §9.1 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §9.1 y
[guia-plugins.md](guia-plugins.md) §6): opción (a) con tres clavos. (1)
`blit` recorta por **ambos extremos**: `x/y` negativos recortan el borde
inicial del Block (`blit(0, -3, doc)` muestra `doc` desde su cuarta fila),
simétrico al recorte por exceso — un viewport sobre un Block más grande que
la región. (2) Garantía explícita: blittear el mismo Block con distinto
offset es **copia, nunca re-render** (el coste de scroll es el de copiar la
ventana visible). (3) La **virtualización** (no construir el Block entero
para historiales enormes) es del toolkit, no del core. Descartada la
primitiva de viewport dedicada (b): añade superficie para lo que el negativo
ya da; descartado recortar en Lua (c) por el coste en el estado principal.
El patrón "cachea el Block, mueve el offset" queda en la guía §6 (con su
antipatrón: reconstruir el Block en cada scroll).

**Problema.** `Region:blit(x, y, block)` "recorta a los límites", pero la
especificación solo contempla el recorte por **exceso** (la parte del Block
que se sale del borde de la región). Un transcript con scroll necesita lo
contrario: estampar un Block alto con `y` **negativo** para recortar sus
primeras filas y "asomarlo" por abajo — un viewport sobre un Block grande,
donde scroll = re-blit con otro offset (ronda 6, escenario 28). No está
escrito si las coordenadas locales negativas son legales ni qué hacen.

**Impacto.** Cualquier UI con scrollback — el transcript de `chat` el
primero; el toolkit lo necesita resuelto antes del spike. Si no fuera
legal, cada plugin tendría que recortar el Block en Lua antes de cada
`blit` (trabajo proporcional al contenido en el estado principal, contra
"Lua decide, Go ejecuta").

**Opciones.** (a) `blit` acepta `x/y` negativos y recorta el borde inicial
(filas/columnas iniciales) además del final — un viewport sobre el Block
sin coste en Lua; (b) primitiva de viewport dedicada en `Region`
(`Region:scroll(block, offset)`) que encapsule el clamp y el offset; (c)
dejarlo en el plugin: recortar el Block en Lua antes de `blit` (rechazable
por el coste en el estado principal).
