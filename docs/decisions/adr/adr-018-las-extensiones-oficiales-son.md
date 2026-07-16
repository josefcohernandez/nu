---
title: "Las extensiones oficiales son un PRODUCTO: el toolkit decora y la UI del harness se ve acabada"
type: "adr"
id: "ADR-018"
status: "aceptada"
date: "2026-06"
---
# ADR-018 · Las extensiones oficiales son un PRODUCTO: el toolkit decora y la UI del harness se ve acabada

**Estado:** Aceptada · 2026-06 (**refina** [ADR-015](adr-015-conjunto-oficial-de-producto.md) —qué significa "el conjunto oficial de producto" cuando hay TTY— y consume [ADR-012](#adr-012--el-toolkit-de-widgets-vive-en-lua-spike-de-s28); resuelve [G36](../../findings/g36-el-conjunto-oficial-de-producto.md) y [G37](../../findings/g37-blitblock-invierte-el-signo.md))

**Contexto.** Con el plan de construcción cerrado (45/45) el binario *funciona*, pero al *usarlo* la experiencia era "poco más que una terminal en blanco": el transcript del chat era prosa monocroma pegada al margen 0, el input una banda sin marco, la statusline un gris indistinguible del cuerpo, no había bienvenida ni indicador de actividad, y —peor— el conjunto oficial montaba a la vez el chat y el REPL, de modo que salir del chat dejaba el intérprete debajo. El kernel y los contratos estaban; lo que faltaba era que las extensiones oficiales **parecieran un producto acabado**, no un kernel con widgets de demostración. Dos auditorías (chat y toolkit) coincidieron en la causa raíz: el toolkit no tenía primitivas de **decoración** (borde/caja, padding, spinner, texto multi-span) y **no cableaba el theme al markdown**, así que toda la UI estaba condenada a apilar texto plano por mucho que se adornara.

**Decisión.** Elevar las extensiones oficiales a calidad de producto, **todo en Lua sobre la API ya congelada** (corolario de completitud: no hizo falta ampliar `nu.*`; `nu.version.api` no se mueve). Tres frentes:

1. **El toolkit decora.** Se añaden al catálogo (cuestión abierta nº3 de arquitectura.md, que ADR-012 dejó al toolkit fijar): `box` (marco con borde redondeado/recto, título, padding, realce de foco), `spinner` (animado vía `nu.task.every`), `richtext` (línea de varios spans con alineación), y en los contenedores `padding`/`gap`/`align`/`justify`. El `theme` pasa de una paleta-placeholder a una **paleta curada** (acento cálido, roles, superficies, selección, código/enlaces/diff) y expone `Theme:markdown_opts()` que **cablea los nombres semánticos al render de `nu.text.markdown`** (api.md §10) — el cambio de mayor impacto: el transcript deja de ser monocromo.

2. **El chat se ve acabado.** Bienvenida al arranque (banner, modelo, cwd, atajos); input **enmarcado** con prompt `› ` y placeholder visible; **spinner de actividad** mientras el turno corre ("Pensando…/Ejecutando <tool>… · esc para interrumpir"); statusline como **barra** con fondo y segmentos coloreados (aviso de contexto, cwd abreviada); **tarjetas de tool** con sus argumentos y estado; modales **enmarcados y centrados**. Sin privilegio de kernel: todo consume el toolkit y los eventos `agent:*` como podría una UI de terceros (ADR-003).

3. **Una sola UI primaria posee la pantalla.** El conjunto oficial (ADR-015) sigue incluyendo `repl`, pero el repl **cede al chat** (G36): solo auto-monta su UI si el chat no está activo. Y **cerrar el chat apaga el binario** (`core:shutdown`), en vez de devolver al usuario a una capa inferior.

Como subproducto, construir el primer widget de borde destapó [G37](../../findings/g37-blitblock-invierte-el-signo.md) (un bug latente del eje X de `blitBlock`, nunca ejercitado porque hasta ahora nada se pintaba en x>0), corregido para cumplir el contrato de `Region:blit` de api.md §9.1.

**Razonamiento.**
- **Por qué en Lua y no en el core.** Las auditorías confirmaron que las primitivas necesarias ya existían en la API congelada: Blocks con spans estilados (§9.2), `nu.text.markdown` **themable** (§10), `nu.text.highlight`/`diff`, `nu.task.every` para animar, `nu.plugin.list` para que el repl detecte al chat. El corolario de completitud se cumple: la API bastó exacta; lo que faltaba era *usarla* desde el toolkit. La única excepción fue G37, un fallo de *implementación* del compositor (no de la espec): se corrige el código para cumplir el contrato, no al revés.
- **Por qué el repl cede en vez de salir del conjunto.** El repl es valioso como punto de partida del autor de extensiones (G21); lo que sobraba no era su presencia sino su competencia por la pantalla. Cederla preserva ADR-015 (sigue instalado y activable suelto) y cierra el solape. Detalle y alternativas en G36.
- **Por qué una paleta opinada por defecto.** Un theme genérico (negro/gris) se ve a placeholder. La identidad visual del harness (acento cálido, jerarquía de superficies) es parte de "parecer producto". Sigue siendo **solo el default**: un theme alternativo es un plugin del toolkit y el usuario lo deriva con `:with{…}` (chat.md §7, G22).

**Consecuencias.**
- La primera pantalla del producto deja de ser un terminal en blanco: bienvenida coloreada, input enmarcado, barra de estado, y una sola app que al cerrarse apaga el binario.
- El catálogo del toolkit crece (box/spinner/richtext + padding/align), pero es **superficie del toolkit**, versionada aparte, no `nu.*` (la API sagrada no se mueve).
- Cambios observables cubiertos por tests: el transcript emite color (markdown themable), la statusline es una barra de spans, el editor va en una caja, el repl no monta UI con el chat activo, y `blitBlock` posiciona en x>0.
- **Disparador de reapertura:** si una UI de terceros necesitara una primitiva de decoración que el toolkit no ofrezca (tablas, scrollbar visible, mouse/hit-testing), se añade al catálogo del toolkit (no al core) siguiendo este mismo patrón; varios candidatos quedan anotados en las auditorías como P2.
