---
title: "`blitBlock` invierte el signo del offset X respecto a Y y al contrato de `Region:blit`"
type: "hallazgo"
id: "G37"
status: "resuelto"
date: "2026-06-28"
origin: "pulido de UI/UX de las extensiones oficiales de producto"
resolution: "Se corrige blitBlock para que el eje X recorte por el borde inicial igual que el eje Y, cumpliendo el contrato ya escrito."
affected: ["api.md §9.1 / compositor.go"]
---
# G37 · `blitBlock` invierte el signo del offset X respecto a Y y al contrato de `Region:blit` — `api.md` §9.1 / `compositor.go` — **RESUELTO**

**Resolución** (aplicada en `compositor.go`; sin cambio en api.md —corrige la *implementación* para que cumpla el contrato ya documentado): `blitBlock` estampa el origen del Block en `(ox, oy)` con el **mismo** signo en ambos ejes. El eje X pasa de `lx = col - ox` a `lx = col + ox`, igual que el eje Y ya hacía con `by = ly - oy` (un `oy` negativo recorta el borde inicial). El test horizontal de viewport (G28) se corrige a la semántica coherente: `blit(-2,0)` recorta el inicio ("CDEF…"), `blit(+2,0)` desplaza a la derecha ("  AB").

**Problema.** [api.md](api.md) §9.1 documenta `Region:blit(x, y, block)` como un viewport simétrico: "`x/y` pueden ser **negativos** y recortan el borde *inicial* del bloque (`blit(0,-3,doc)` muestra `doc` desde su cuarta fila)". El eje Y lo cumplía; el X estaba **invertido** (`lx = col - ox`: era el *positivo* el que recortaba el inicio). Nunca se notó porque **ningún widget se blitteaba en x>0**: el chat, la pantalla desnuda y el repl apilaban todo contra el margen izquierdo. Al introducir `padding`/alineación en el toolkit (G36), un widget colocado en x=1 perdía su primera columna (el borde izquierdo de una caja, la viñeta de una línea), porque la app llama `region:blit(ax, ay)` esperando *posicionar* y en X obtenía un *scroll*.

**Impacto.** Latente pero real: bloquea cualquier layout con margen/padding/centrado horizontal —es decir, casi toda la UI de producto (cajas, modales centrados, statusline con padding)—. Se descubrió al construir el primer widget de borde. La corrección alinea la implementación con el contrato; no amplía ni cambia la API (`enu.version.api` no se mueve).
