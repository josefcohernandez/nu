---
title: "Namespaces de extensión reservados al core"
type: "hallazgo"
id: "G26"
status: "resuelto"
origin: "revisión filosófico-técnica del proyecto"
resolution: "El core solo reserva core: y ui:; la unicidad del nombre de plugin, garantía del loader, protege a las extensiones entre sí."
affected: ["api.md §4 / guía §7", "agente.md §4"]
---
# G26 · Namespaces de extensión reservados al core — `api.md` §4 / guía §7 / `agente.md` §4 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §4 y §14, [guia-plugins.md](guia-plugins.md)
§7 y [agente.md](agente.md) §4): esquema de **dos niveles**, sin reservar
nombres de extensión en el core. (1) El core reserva solo lo suyo — `core:`
y `ui:`, las superficies que el propio kernel emite. (2) Todo otro namespace
pertenece a un plugin por convención (namespace = nombre del plugin), y la
colisión entre extensiones la cierra el loader, que garantiza que el nombre
de un plugin es único — es su identidad (storage `plugins/<nombre>/`,
resolución de `requires`, sustitución por nombre de las embebidas; dos
nombres iguales = error de carga). Así `agent:` deja de ser una reserva del
core y pasa a ser el namespace del plugin `agent`, protegido igual que
`mi-plugin:` — sin privilegio: nadie más puede llamarse `agent`, y el agente
no puede apropiarse de `mi-plugin`. Descartado reservar `agent:` (y los
namespaces de las demás oficiales) en el core: el kernel reservando un nombre
por cuenta de una extensión es justo lo que prohíbe «el kernel solo conoce
sus propias capacidades» ([filosofia.md](filosofia.md) §2, ADR-003) — la
misma vara que cerró G21 y G23. Descartado también un registro central de
namespaces en el core (otra vez vocabulario de extensiones en la superficie
sagrada).

**Problema.** La guía (§7) listaba `core:`, `ui:` **y `agent:`** como
namespaces de eventos reservados, mientras [api.md](api.md) (§4, §17) reserva
solo `core:`/`ui:`. La incoherencia escondía una de fondo: `agent` es una
extensión oficial, no el core; que el core reserve su namespace lo obliga a
conocer una extensión por su nombre, contra ADR-003. Y sin esa reserva
quedaba sin responder qué impide que dos extensiones declaren el mismo
namespace.

**Impacto.** Coherencia del modelo de extensión sobre la superficie que se
congela; toca el principio del kernel mínimo que sostiene G21/G23. Barato
ahora, caro tras congelar.

**Opciones.** (a) Reservar `agent:` (y las demás oficiales) en el core —
cómodo, pero mete nombres de extensión en la API sagrada; (b) un registro de
namespaces en el core que las extensiones reclaman al cargarse — resuelve
colisiones pero a costa de superficie y de que el core sepa de namespaces de
producto; (c) dos niveles por convención: el core reserva solo `core:`/`ui:`,
y la unicidad del nombre de plugin (garantía del loader) protege a las
extensiones entre sí — `agent:` es un namespace de plugin más.
