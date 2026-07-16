---
title: "Auditoría de diseño de la web — 15 de julio de 2026"
type: "auditoria"
date: "2026-07-15"
status: "cerrada"
---
# Auditoría de diseño de la web — 15 de julio de 2026

Auditoría del rediseño completo de `web/` (commit `af10897`, "la web ES un
terminal") con una pregunta concreta de partida: **¿cuánto de "AI slop" hay en
este diseño, y qué se puede mejorar?** El documento recoge el veredicto, el
razonamiento y —a petición— una **solución accionable por cada hallazgo**.

**Metodología.** Dos pasadas de lectura del código fuente (no del `dist/`):

- Pasada 1 — la gramática visual y la portada: `styles/tokens.css`,
  `styles/global.css`, `styles/markdown.css`, `pages/index.astro`,
  `scripts/keyboard.ts`.
- Pasada 2 — el resto de superficies que la primera no cubría: `layouts/Base.astro`,
  `pages/api/[slug].astro`, `pages/docs/[slug].astro`, `pages/plugins.astro`,
  `components/Statusline.astro`, `components/HeaderInternal.astro`,
  `scripts/search.ts`, `lib/i18n.ts`.

Los contrastes de color se calcularon con la fórmula WCAG 2.1 sobre los tokens
del tema por defecto (`nu`). Los ítems llevan id `W-##`. Severidad: 🔴 alta
(rompe una función o excluye a usuarios), 🟡 media (fricción real), 🔵 baja
(pulido / criterio).

---

## Veredicto: nivel de "AI slop" **2/10**

Es prácticamente lo contrario del *slop*. El output genérico de IA tiene una
firma reconocible —gradientes morado-azul, esquinas redondeadas, glassmorphism,
tarjetas con sombra, bullets con emoji, secciones "✨ Features", copy de relleno,
un hero de plantilla Tailwind/shadcn—. Aquí **no hay nada de eso**, y no por
omisión sino por decisión declarada: `styles/global.css:1-3` prohíbe
explícitamente `border-radius`, sombras, gradientes y transiciones.

Lo que hay es **una tesis de diseño única, ejecutada con disciplina**:

- Una sola familia monoespaciada (IBM Plex Mono, self-hosted) y una paleta de
  4 temas de terminal reales (`nu`/`dracula`/`gruvbox`/`solarized`), con valores
  exactos y mapeados hasta el resaltado de Shiki (`tokens.css:59-74`).
- Detalles autorales que ninguna plantilla genera: subrayados de títulos
  dibujados con caracteres `═`/`─` recortados al ancho del texto
  (`markdown.css:57-75`), `│` en citas en lugar de `border-left`
  (`markdown.css:92-104`), navegación vim completa (`j/k`, `n/p`, `:`, `/`),
  modo comando con guiño `E492` a Vim (`keyboard.ts:383`) y un mini-REPL de Lua
  como easter egg (`keyboard.ts:136-157`).
- Comentarios que justifican el *porqué* de cada decisión — lo opuesto al output
  de plantilla.

Los ~2 puntos que le restan no son por *slop* sino por dos riesgos legítimos: el
concepto "terminal + `$ comando`" es un **tropo muy trillado** en landings de
dev-tools (roza lo derivativo, ver W-09), y hay decisiones que sacrifican
usabilidad por coherencia estética (W-01…W-04). La base, sin embargo, tiene
punto de vista y autoría reales.

---

## Hallazgos y soluciones

### 🔴 W-01 — La búsqueda de escritorio no puede teclear `n` ni `p`

**Problema.** En el overlay de búsqueda, con el foco fuera del `<input>`
(el caso de escritorio), las teclas `n` y `p` están cableadas a
"siguiente/anterior resultado" *antes* de poder escribirse como texto. El propio
código lo admite (`search.ts:407-417`, comentario en 408-410). Consecuencia: en
escritorio **es imposible buscar cualquier término que contenga `n` o `p`** —
que en una API llamada `nu.*` es una fracción enorme del vocabulario: `plugin`,
`spawn`, `proc`, `print`, `worker`... (`nu` mismo empieza por `n`). El usuario
teclea `p` para buscar "plugin" y en vez de escribirlo salta de resultado.

**Evidencia.** `web/src/scripts/search.ts:407-417`.

**Solución.** Reservar la navegación por resultados a teclas que no colisionen
con la escritura: `ArrowDown`/`ArrowUp` (ya soportadas, `search.ts:399-406`) y
`Ctrl-n`/`Ctrl-p` para quien venga de un pager; dejar que `n`/`p` **planas**
caigan al `default` y se añadan al término. Es un cambio de dos ramas del
`switch`: borrar los `case 'n'`/`case 'p'` y ampliar la guarda del `default`
para aceptar `Ctrl-n`/`Ctrl-p` como navegación. Coste bajo, arregla un fallo que
invalida la búsqueda para el grueso de los términos de la propia API.

### 🔴 W-02 — El texto secundario (`--dim`) no cumple contraste AA

**Problema.** El token `--dim` (#4e686e) sobre `--bg` (#0a1416) rinde un
contraste de **≈3.1:1**, por debajo del mínimo WCAG AA de 4.5:1 para texto
normal (y el chrome que lo usa está a 10.5–11px, *small text*, que exige 4.5:1
sin excepción). No es decorativo: `--dim` pinta la statusline y sus atajos, el
índice `§` del carril, los metadatos de commit, los footers, el enlace
"github ↗", la nota de idioma **y el cuerpo de las citas markdown** — es decir,
contenido de lectura, no solo adorno. Para referencia, `--fg` sobre `--bg` sí
cumple holgado (≈9.7:1): el problema está acotado a `--dim`.

**Evidencia.** `web/src/styles/tokens.css:16` (`--dim: #4e686e`), usado en
`markdown.css:95` (citas), `Statusline.astro:64,80`, `docs/[slug].astro:264`,
`HeaderInternal.astro:100,104`, etc.

**Solución.** Subir `--dim` en cada tema hasta pasar 4.5:1 contra su `--bg`
manteniendo el matiz. Para `nu`, un `--dim` alrededor de **#6f8f97** llega a
~4.6:1 sin desentonar con la paleta. Regla operativa: introducir en el flujo de
build un check de contraste de los pares (`--dim`/`--fg` sobre `--bg`) para los
4 temas —igual que ya hay un `check:drift`— de modo que un token que baje de AA
rompa el build. Alternativa quirúrgica si se quiere preservar el `--dim` actual
como color "de susurro": reservarlo solo para adornos no textuales (separadores
`·`, corchetes `[ ]`) y usar un `--dim-text` conforme para todo lo que sea
lectura.

### 🟡 W-03 — La prosa larga en monoespaciada cansa (el punto de más impacto en docs)

**Problema.** Toda la wiki y la referencia se renderizan en IBM Plex Mono a
14px/line-height 1.9 (`markdown.css:7-12`). El monoespaciado está diseñado para
código, no para párrafos: leer los `.md` densos en español —que es *donde el
usuario pasa el tiempo*— es fatigoso. El dogma "todo mono" tiene sentido en el
chrome (es lo que hace que la web *sea* un terminal), pero se paga caro en el
cuerpo de la documentación.

**Evidencia.** `web/src/styles/markdown.css:7-12`, `api.css` (misma familia
heredada del global).

**Solución.** Romper la regla **solo** para el cuerpo de la prosa: una humanista
o serif legible (self-hosted, para no romper el "cero dependencias externas")
aplicada a `.markdown p, .markdown li, .markdown blockquote`, dejando mono en
títulos, código inline/bloque, tablas y todo el chrome. El terminal sigue siendo
terminal —cursor, statusline, `═/─`, comandos— pero la lectura deja de doler.
Es un cambio localizado en `markdown.css` (y su gemelo en `api.css`), no toca la
gramática visual del resto. Si se quiere conservar el 100% mono como identidad,
la mitigación mínima es bajar el tamaño de línea a ~1.7 y subir el `font-size`
del cuerpo a 15px para dar aire; ayuda, pero no resuelve la fatiga de fondo del
mono.

### 🟡 W-04 — El acantilado de contenido en inglés

**Problema.** El chrome es bilingüe (`lib/i18n.ts`) pero **todo el contenido
—docs, referencia, plugins— existe solo en español**. Un visitante que pone la
web en EN ve el chrome en inglés y, al pulsar "documentation", aterriza en un
muro de prosa en español con una notita en cursiva "in spanish for now"
(`HeaderInternal.astro:50`, `i18n.ts:153`). Es honesto y está asumido (el
proyecto es *spanish-first*), pero como experiencia es un acantilado: el idioma
del chrome promete algo que el contenido no cumple.

**Evidencia.** `web/src/lib/i18n.ts:1-7` (comentario de cabecera), `:105`
(`langNote` vacío en ES), `:168`; `HeaderInternal.astro:50`.

**Solución.** No traducir todo (coste desproporcionado en fase de diseño), sino
**gestionar la expectativa antes del salto**, no después: cuando `data-lang=en`,
que las entradas de la portada y del menú que llevan a contenido español
(`documentation`, `api`, cada link del sidebar) muestren el marcador
"· es" o un `[es]` tenue *en el propio enlace*, de modo que el usuario sepa a
qué idioma va antes de hacer clic. El andamiaje ya existe (`data-lang-note`,
`langNote`): basta con moverlo del destino al origen. Coste bajo, elimina la
sensación de *bait-and-switch* sin comprometerse a traducir la wiki.

### 🔵 W-05 — Escala tipográfica ajustada a ojo (números mágicos)

**Problema.** Hay tamaños de fuente sembrados a mano por todo el CSS: 14px,
13.5px, 13px, 12.5px, 11.5px, 11px, 10.5px... No es *slop* (es lo contrario:
*demasiado* ajuste manual), pero es frágil y difícil de mantener coherente entre
páginas: dos superficies que "deberían" verse igual acaban a 12.5 y 13 sin
motivo.

**Evidencia.** `markdown.css:39-56`, `docs/[slug].astro:242-292`,
`Statusline.astro:53`, y repartido por casi todos los `<style>` de página.

**Solución.** Definir una escala como tokens en `tokens.css`
(`--fs-1`…`--fs-6`, p. ej. 10.5 / 11 / 12.5 / 13 / 14 / 20) y consumir *esos*
en lugar de literales. No cambia nada visual de golpe; convierte una constante
dispersa en una decisión central y detecta de paso los valores que solo existen
por inercia.

### 🔵 W-06 — La interactividad es invisible si no la buscas

**Problema.** Lo mejor del diseño —el prompt tecleable de la portada, `:` para
comandos, `/` para buscar, el REPL de Lua— no se anuncia. La portada tiene un
cursor parpadeante y un `>` (`index.astro:59-62`) pero nada dice que puedes
escribir; el REPL solo se descubre leyendo la segunda línea de `help`. Un
visitante que no "prueba a teclear" se pierde el 80% de la gracia.

**Evidencia.** `index.astro:58-63` (prompt sin rótulo), `keyboard.ts:189-192`
(`help` como único anuncio del easter egg).

**Solución.** Un *hint* permanente y discreto: en la portada, un placeholder
tenue en la línea del prompt tipo `escribe help ↵` que se borra al primer
carácter; en las páginas internas, la statusline ya muestra las teclas, así que
basta con asegurar que `?`/`help` aparezca listado. Cero coste conceptual, sube
mucho la tasa de descubrimiento.

### 🔵 W-07 — El overlay de búsqueda no atrapa el foco en escritorio

**Problema.** El panel de búsqueda es un `role="dialog" aria-modal="true"`
(`search.ts:106-107`) pero en escritorio el foco **no se mueve dentro del
diálogo** (el `input` solo recibe foco en táctil, `search.ts:467-470`). Para un
usuario de lector de pantalla o navegación por teclado asistida, el foco se queda
en la página de fondo mientras el modal está "abierto": el `aria-modal` miente.
No hay *focus trap* ni retorno de foco al cerrar.

**Evidencia.** `web/src/scripts/search.ts:106-107` y `:467-470`.

**Solución.** Al abrir, mover el foco a un elemento del panel (el `input`, o el
propio panel con `tabindex="-1"`) también en escritorio, y devolverlo al
elemento previo al cerrar (`cierra()` ya restaura statusline y scroll:
`search.ts:473-493`, añadir ahí `prevFocus?.focus()`). El manejo de teclado con
`capture:true` ya evita fugas al resto de la app; falta solo cerrar el círculo
del foco. Coste bajo, alinea el comportamiento con lo que el ARIA ya promete.

### 🔵 W-08 — El REPL de Lua es un simulacro que puede envejecer mal

**Problema.** El mini-REPL evalúa un subconjunto en JavaScript
(`keyboard.ts:136-157`) y devuelve, entre otras, `nu.version → "0.1.3"`
*hardcodeado* (`:139`). Es un easter egg encantador, pero es una promesa que el
JS no puede cumplir de verdad, y el número de versión quedará desactualizado sin
que nadie lo note.

**Evidencia.** `web/src/scripts/keyboard.ts:136-157`.

**Solución.** Mantener el guiño (es bueno), pero (a) alimentar `nu.version`
desde `lib/const.ts` (`VERSION`) en vez de un literal, para que siga a la versión
real; y (b) acotar la expectativa: que el prompt del REPL diga algo como
`lua (demo)` para que nadie espere un intérprete completo. Un ajuste, no un
rediseño.

### 🔵 W-09 — El concepto "terminal-website" es un tropo; la ejecución lo salva, pero conviene saberlo

**Problema.** El único vector real hacia lo genérico no es el *slop* sino lo
*derivativo*: "landing de dev-tool que imita una terminal, con `$ comando` de
hero y fuente mono" es un patrón muy visto. Este diseño está bastante por encima
de la media del tropo (temas conmutables, pager vim real, REPL, `═/─` dibujados),
pero comparte su vocabulario de partida.

**Evidencia.** `index.astro:28` (`$ nu` como línea de hero), concepto global.

**Solución.** No es un bug a corregir, es una dirección a vigilar: lo que aleja
esto del cliché son precisamente los detalles que **hacen cosas**, no los que
decoran (el prompt que ejecuta de verdad, los 4 temas que son paletas reales de
terminal, la navegación que funciona como un pager). La recomendación es
*doblar* en esa dirección —interacción con función— y evitar añadir adornos de
terminal puramente cosméticos (ASCII art de relleno, "booting..." falsos), que
son lo que empuja el tropo hacia el cliché.

---

## Resumen

| Id | Sev | Hallazgo | Esfuerzo del arreglo |
|----|-----|----------|----------------------|
| W-01 | 🔴 | La búsqueda de escritorio no puede teclear `n`/`p` | Bajo |
| W-02 | 🔴 | `--dim` no cumple contraste AA (~3.1:1) | Bajo–medio |
| W-03 | 🟡 | Prosa larga en monoespaciada (fatiga de lectura) | Medio |
| W-04 | 🟡 | Acantilado de contenido EN (chrome bilingüe, contenido no) | Bajo |
| W-05 | 🔵 | Escala tipográfica a ojo (números mágicos) | Bajo |
| W-06 | 🔵 | Interactividad invisible (sin *hint*) | Bajo |
| W-07 | 🔵 | El overlay de búsqueda no atrapa el foco en escritorio | Bajo |
| W-08 | 🔵 | REPL simulado con `nu.version` hardcodeado | Bajo |
| W-09 | 🔵 | Riesgo derivativo del tropo "terminal-website" | — (dirección) |

**Conclusión.** El diseño **no es AI slop** (2/10): tiene tesis, coherencia y
autoría. Los dos hallazgos 🔴 (W-01, W-02) son fallos concretos que conviene
cerrar porque rompen función (búsqueda) o excluyen usuarios (contraste), y son
baratos. Los 🟡 (W-03, W-04) son las decisiones donde la coherencia estética le
gana terreno a la usabilidad justo en las páginas de contenido; merecen una
decisión consciente. El resto es pulido. Ninguno cuestiona la base: el rediseño
parte de una idea con punto de vista y la ejecuta con disciplina.

---

## Addendum de resolución (2026-07-15)

W-01…W-08 quedaron resueltos el mismo día (rama `claude/auditoria-web`). W-09
no era un bug sino una dirección, y se adopta como guía de diseño: doblar en
interacción con función, evitar adorno de terminal puramente cosmético.

| Id | Resolución |
|----|------------|
| W-01 | `search.ts`: fuera los `case 'n'/'p'` planos (ya se escriben como texto); la navegación por resultados queda en `↑`/`↓` y `Ctrl-n`/`Ctrl-p`; el hint del overlay pasa de `[n/p]` a `[↑↓]`. |
| W-02 | `--dim` sube a AA en los temas que fallaban conservando el matiz (`nu` #63848b 4.63:1, `dracula` #8592b8 4.61:1, `solarized` #637275 4.64:1; `gruvbox` ya pasaba) y, destapado por el propio check, el `--fg` de `solarized` también (#5f737b, 4.61:1). Nuevo gate `check:contraste` (fórmula WCAG 2.1, 2 pares × 4 temas) en npm y en `docs.yml`, como se pedía. |
| W-03 | El cuerpo de prosa (`.markdown`/`.api-prose` p/li/blockquote) pasa a IBM Plex Sans self-hosted (`--font-prose`, 15px/1.7); títulos, código, tablas y todo el chrome siguen en mono: el terminal sigue siendo terminal. |
| W-04 | Resuelto por la vía fuerte, por decisión del propietario (sustituye a la mitigación propuesta aquí): **todo el contenido traducido al inglés** y publicado bajo rutas estáticas `/en/…` (18 docs + 16 api + plugins; 72 páginas totales), índice de búsqueda por idioma, toggle de idioma que navega a la página homóloga, gitmeta y plugins remark conscientes del idioma. Fuera la nota «in spanish for now». El español sigue siendo la fuente de verdad (nota de mantenimiento en `web/README.md`). |
| W-05 | Escala `--fs-1`…`--fs-12` en `tokens.css` con mapeo exacto de los 12 valores existentes (cero cambio visual); 65 literales sustituidos por tokens. No hubo duplicados accidentales que consolidar: los valores cercanos ya eran consistentes por rol. |
| W-06 | Placeholder tenue `escribe help ↵` / `type help ↵` en el prompt de la portada (desaparece al primer carácter, reaparece al vaciarse) y tecla `[?] ayuda` anunciada en la statusline de los pagers, con su manejador real en `keyboard.ts`. |
| W-07 | El overlay guarda `document.activeElement` al abrir, enfoca el panel (`tabindex="-1"`) también en escritorio y devuelve el foco al elemento previo al cerrar; el flujo táctil no cambia. |
| W-08 | `nu.version` del REPL sale de `VERSION` (`const.ts`), sin literal duplicado, y el banner acota la expectativa manteniendo el guiño: «lua (demo del navegador) — escribe salir para volver». |
| W-09 | Sin cambio de código: criterio de dirección adoptado. |

De propina, la pasada destapó defectos preexistentes que también se corrigieron:
enlaces del contenido a rutas inexistentes (`/nu/empezando/…` → `/nu/docs/…`,
`/nu/referencia/…` → `/nu/api/…`, en ES y en EN) y 4 anclas `#fragmento` rotas.
Cierre verificado: build (72 páginas, pagefind es+en), `check:contraste`,
`check:drift`, `check:limpieza` y `check:limpieza:fuente` en verde.
