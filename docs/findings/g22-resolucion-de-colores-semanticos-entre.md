---
title: "Resolución de colores semánticos entre core y toolkit"
type: "hallazgo"
id: "G22"
status: "resuelto"
origin: "revisión de coherencia de la documentación completa"
resolution: "El core solo acepta colores literales; el vocabulario semántico y los themes son enteramente responsabilidad del toolkit."
affected: ["api.md §9.2"]
---
# G22 · Resolución de colores semánticos entre core y toolkit — `api.md` §9.2 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §9.2,
[arquitectura.md](arquitectura.md) y guía §6): opción (b) — el core solo
acepta colores **literales** (`#rrggbb`, índice 0-255; degradados a
`enu.ui.caps().colors` al pintar); el vocabulario semántico y los themes
son enteramente del toolkit, que resuelve nombre → literal al construir
los Blocks. Razón decisiva: no congelar un único modelo de theming en la
API sagrada — una paleta global del core restringiría a toolkits
alternativos con modelos más ricos; en espacio de extensiones el theming
puede competir e iterar. Mitigaciones de los costes conocidos: el árbol
retenido del toolkit re-renderiza solo al cambiar de theme (sus
consumidores cambian en vivo gratis); los plugins de `enu.ui` crudo que
usen colores del theme se suscriben a su evento de cambio (misma
convención que `ui:resize`: tu región, tu repintado); el cambio en vivo
para plugins que no cooperan se asume imperfecto. Descartadas: (a) tabla
`enu.ui.theme` en el core (bendice un modelo único y mete vocabulario de
theming en la API sagrada); (c) estilos por referencia (mucha superficie
para el mismo resultado).

**Problema.** Un `Style` del core acepta nombres semánticos (`"accent"`,
`"error"`), pero los themes son plugins del toolkit
([chat.md](chat.md) §7): no está definido quién traduce nombre → color
concreto, ni cuándo (¿al construir el Block o al pintar?).

**Impacto.** `Style` es API sagrada; el theming entero (y la regla "solo
colores semánticos" de la guía §6) depende de esta pieza.

**Opciones.** (a) Registro mínimo en el core — `enu.ui.theme(tabla)`
define la paleta semántica; los themes (plugins del toolkit) la llaman y
el compositor resuelve al pintar (cambiar de theme repinta todo, los
Blocks no se rehacen); (b) los nombres semánticos no son del core: el
toolkit resuelve a colores concretos antes de construir Blocks y `Style`
solo acepta colores literales (core más puro; pero cada Block queda
"horneado" con su theme y la guía §6 pasaría a ser regla del toolkit);
(c) indirection por referencia en el Block, resuelta al pintar (la más
flexible y la más cara de especificar).
