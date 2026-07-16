---
title: "docs/ — el mapa de la documentación"
type: "indice"
status: "vigente"
---
# docs/ — el mapa de la documentación

Este directorio **es** el proyecto: la API se diseñó y validó aquí antes de
escribir el kernel, y sigue siendo la fuente de verdad cuando el código y un
documento discrepan. Para que esa autoridad no se diluya, los ficheros se
organizan por **capas** según su naturaleza, no por orden de llegada: un
contrato vivo no pesa lo mismo que un informe fechado ya cerrado.

## Capa 1 — Fuente de verdad (contratos vivos)

Lo normativo: si el código contradice uno de estos documentos, el que está mal
es el código. Orden de lectura sugerido:

| Documento | Rol |
|---|---|
| [filosofia.md](core/filosofia.md) | Principios fundacionales y "lo que enu no es". El *porqué* del proyecto. |
| [arquitectura.md](core/arquitectura.md) | Vista estática: las capas, el inventario de primitivas del kernel. |
| [modelo-ejecucion.md](core/modelo-ejecucion.md) | Vista dinámica: concurrencia, comunicación, limitaciones. |
| [api.md](contracts/api.md) | **La API v1 del core — la "superficie sagrada".** Firmas y semánticas. |
| [providers.md](contracts/providers.md) | Contrato de la extensión oficial de providers. |
| [agente.md](contracts/agente.md) | Contrato de la extensión oficial `agent` (motor headless). |
| [sesiones.md](contracts/sesiones.md) | Contrato de persistencia: JSONL append-only. |
| [chat.md](contracts/chat.md) | Contrato de la extensión oficial `chat` (la UI). |
| [guia-plugins.md](contracts/guia-plugins.md) | Sabiduría práctica para autores de plugins + checklist. |
| [malla.md](contracts/malla.md) | Contrato de la extensión oficial `mesh` (borrador v0.1; su §11 sigue abierta). |

## Capa 2 — Flujo de diseño y construcción

El registro de cómo se decide y se construye. Append-only por convención: las
entradas no se reescriben, se suceden.

| Documento | Rol |
|---|---|
| [adr.md](decisions/adr/README.md) | Decisiones técnicas (ADR-NNN); las reemplazadas se marcan, nunca se borran. |
| [problemas.md](findings/README.md) | Grietas que la v1 necesita cerradas (G##, con estado vivo en su cabecera). |
| [pospuesto.md](postponed/pospuesto.md) | Lo que se decidió no decidir todavía (P##), cada uno con su disparador. |
| [pseudocodigo.md](validation/README.md) | El ejercicio de validación: rondas de pseudocódigo que torturan la API. |
| [implementacion.md](plan/implementacion.md) | Plan de construcción por sesiones (S##), con puntero ▶ y bitácora. |
| [decisiones-implementacion.md](worklog/README.md) | Bitácora operativa: decisiones y desviaciones por sesión, por debajo del umbral de `G##`. |
| [release.md](ops/release.md) | Runbook operativo para cortar una release estable (los *steps* que ADR-013 deja fuera). |

## Capa 3 — [audits/](audits/) (informes fechados)

Auditorías puntuales del repo: cada informe lleva su fecha en el nombre, se
cierra enrutando sus hallazgos al flujo canónico (G##/P##) y no vuelve a
editarse salvo para anotar ese cierre. Aquí aterrizan las auditorías futuras.

- [audits/auditoria-2026-07-12.md](audits/auditoria-2026-07-12.md) — auditoría
  integral (A-01…A-42); cerrada el 2026-07-14 sin A-## pendientes.

## Capa 4 — [archive/](archive/) (ejecutado, no vigente)

Planes y artefactos que ya cumplieron su función. Se conservan porque el
histórico importa (los ADR y las bitácoras los citan), pero no gobiernan nada.

- [archive/migracion-vm.md](archive/migracion-vm.md) — plan de migración de la
  VM a PUC-Lua/wasm (ADR-019); completado con M17.
- [archive/migracion-vm-censo.md](archive/migracion-vm-censo.md) — censo de la
  frontera VM (anexo de M01).

## Publicación web

La web de documentación (`web/`, GitHub Pages) publica **solo la Capa 1** —los
contratos vivos— más las seis páginas locales de «empezar» y la referencia
navegable derivada de `api.md`. Las **Capas 2-4 no se publican**: el flujo de
diseño (`adr.md`, `problemas.md`, `pospuesto.md`, `pseudocodigo.md`,
`implementacion.md`, `decisiones-implementacion.md`), `audits/` y `archive/` son
el registro interno del proyecto y no sirven a un visitante.

De la Capa 1 se publican **todos los contratos salvo `malla.md`**: la extensión
`mesh` sigue con su §11 abierta, así que aparece solo como línea «experimental»
del índice, con enlace al blob de GitHub. `api.md` **tampoco** se publica como
página wiki; su presentación web es `/api`, la referencia navegable derivada con
su propio gate `check-drift` (ver
[.claude/skills/sync-web](../.claude/skills/sync-web/SKILL.md)).

La transformación fuente→web es **mecánica y en build**, sin duplicar contenido
—`docs/` sigue siendo la única fuente de verdad—:

- Una sección de un contrato que no debe publicarse se envuelve entre los
  marcadores `<!-- enu:interno -->` y `<!-- /enu:interno -->`. El plugin
  `web/src/lib/markdown/remark-limpieza-interno.mjs` la elimina al renderizar.
- Los **marcadores de proceso** —referencias parentéticas `(G##)`, `(P##)`,
  `(S##)`, `(ADR-NNN)`, incluso en títulos, y los blockquotes de estado
  `> ✅ …`— los barre el mismo plugin.
- **`docs/` conserva su trazabilidad intacta**: el flujo interno (jueces,
  `auditor-docs`, el propio diseño) necesita esos marcadores, y la limpieza vive
  solo en el pipeline de la web.

Dos gates lo blindan en CI: `check:limpieza:fuente` verifica en `docs/` que los
pares `enu:interno` estén balanceados **antes** del build; el gate
`check-limpieza-html` (`check:limpieza`) recorre el HTML de `dist/` **después** y
falla si se filtró cualquier marcador. Para dar de alta o retirar una página de
la wiki, la skill [/alta-wiki](../.claude/skills/alta-wiki/SKILL.md).

## Nota sobre nombres e idioma

Las carpetas nuevas (`audits/`, `archive/`) ya usan nombres en inglés; los
ficheros conservan sus nombres en español para no romper dos veces las
referencias cruzadas (enlaces markdown, rutas en `.claude/` y citas en
comentarios de código). El renombrado de ficheros y la documentación
multi-idioma (inglés/español) quedan para la futura migración a inglés del
repo, en una sola pasada.
