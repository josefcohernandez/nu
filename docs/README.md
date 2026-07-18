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
contrato vivo no pesa lo mismo que un informe fechado ya cerrado. La capa se
materializa en la estructura de carpetas (nombres de carpeta en inglés,
ficheros en español) y cada documento declara sus metadatos en un
**frontmatter YAML** (claves en inglés, valores en español): `type`, `status`,
`id` y los campos propios de su tipo. Los registros (decisiones, hallazgos,
rondas, sesiones) van a **un fichero por entrada**, con un `README.md` índice
por carpeta que concentra el estado vivo (contadores, tablas).

```
docs/
├── core/          Capa 1 · fundacionales (filosofía, arquitectura, modelo de ejecución)
├── contracts/     Capa 1 · contratos de API y extensiones
├── decisions/adr/ Capa 2 · un fichero por ADR + índice
├── findings/      Capa 2 · un fichero por hallazgo G## + índice con el estado vivo
├── postponed/     Capa 2 · pospuesto.md (la tabla P##, con disparadores)
├── validation/    Capa 2 · una ronda de pseudocódigo por fichero + índice
├── plan/          Capa 2 · implementacion.md (el plan) + estado.md (puntero ▶ y bitácora)
├── worklog/       Capa 2 · una sesión de construcción por fichero + índice
├── ops/           Capa 2 · runbooks operativos (release)
├── audits/        Capa 3 · informes de auditoría fechados y cerrados
└── archive/       Capa 4 · planes ya ejecutados, solo valor histórico
```

## Capa 1 — Fuente de verdad (contratos vivos)

Lo normativo: si el código contradice uno de estos documentos, el que está mal
es el código. Orden de lectura sugerido:

| Documento | Rol |
|---|---|
| [core/filosofia.md](core/filosofia.md) | Principios fundacionales y "lo que enu no es". El *porqué* del proyecto. |
| [core/arquitectura.md](core/arquitectura.md) | Vista estática: las capas, el inventario de primitivas del kernel. |
| [core/modelo-ejecucion.md](core/modelo-ejecucion.md) | Vista dinámica: concurrencia, comunicación, limitaciones. |
| [contracts/api.md](contracts/api.md) | **La API v1 del core — la "superficie sagrada".** Firmas y semánticas. |
| [contracts/providers.md](contracts/providers.md) | Contrato de la extensión oficial de providers. |
| [contracts/agente.md](contracts/agente.md) | Contrato de la extensión oficial `agent` (motor headless). |
| [contracts/sesiones.md](contracts/sesiones.md) | Contrato de persistencia: JSONL append-only. |
| [contracts/chat.md](contracts/chat.md) | Contrato de la extensión oficial `chat` (la UI). |
| [contracts/guia-plugins.md](contracts/guia-plugins.md) | Sabiduría práctica para autores de plugins + checklist. |
| [contracts/malla.md](contracts/malla.md) | Contrato de la extensión oficial `mesh` (borrador v0.1; su §11 sigue abierta). |

## Capa 2 — Flujo de diseño y construcción

El registro de cómo se decide y se construye. Append-only por convención: las
entradas no se reescriben, se suceden — y desde la reestructuración, cada
entrada es un fichero.

| Carpeta / documento | Rol |
|---|---|
| [decisions/adr/](decisions/adr/README.md) | Decisiones técnicas, un fichero por ADR (`adr-NNN-<slug>.md`); las reemplazadas se marcan (`superseded_by`), nunca se borran. |
| [findings/](findings/README.md) | Grietas que la v1 necesita cerradas, un fichero por hallazgo (`gNN-<slug>.md`); el índice concentra el contador y la procedencia por lotes. |
| [postponed/pospuesto.md](postponed/pospuesto.md) | Lo que se decidió no decidir todavía (P##), cada uno con su disparador. |
| [validation/](validation/README.md) | El ejercicio de validación: una ronda de pseudocódigo por fichero (`ronda-N-<slug>.md`), escenarios con numeración global. |
| [plan/implementacion.md](plan/implementacion.md) | Plan de construcción por sesiones (S##): protocolo, fases, grafo, política de tests. |
| [plan/estado.md](plan/estado.md) | **El estado vivo del plan**: puntero ▶, tablero de fases y bitácora append-only. Lo que editan las skills al avanzar. |
| [worklog/](worklog/README.md) | Bitácora operativa, un fichero por sesión (`sNN-<slug>.md`): decisiones y desviaciones por debajo del umbral de `G##`. |
| [ops/release.md](ops/release.md) | Runbook operativo para cortar una release estable (los *steps* que ADR-013 deja fuera). |

## Capa 3 — [audits/](audits/) (informes fechados)

Auditorías puntuales del repo: cada informe lleva su fecha en el nombre, se
cierra enrutando sus hallazgos al flujo canónico (G##/P##) y no vuelve a
editarse salvo para anotar ese cierre. Aquí aterrizan las auditorías futuras.

- [informe-arquitectura-2026-07-08.md](audits/informe-arquitectura-2026-07-08.md) —
  análisis de arquitectura post-M17 (H-1…H-16); H-1/H-2 enrutados como G42/G43,
  cierre anotado en su nota de archivo.
- [auditoria-2026-07-12.md](audits/auditoria-2026-07-12.md) — auditoría
  integral (A-01…A-42); cerrada el 2026-07-14 sin A-## pendientes.
- [analisis-nombres-2026-07-15.md](audits/analisis-nombres-2026-07-15.md) —
  análisis del renombrado `nu` → `enu`.
- [auditoria-web-diseno-2026-07-15.md](audits/auditoria-web-diseno-2026-07-15.md) —
  auditoría de diseño de la web de docs.
- [auditoria-promocion-reddit-2026-07-15.md](audits/auditoria-promocion-reddit-2026-07-15.md) —
  auditoría de presentación pública (R-01…R-16).
- [auditoria-renombrado-2026-07-16.md](audits/auditoria-renombrado-2026-07-16.md) —
  verificación del renombrado total (N-##).
- [auditoria-camino-desconocido-2026-07-16.md](audits/auditoria-camino-desconocido-2026-07-16.md) —
  auditoría del camino no recorrido.
- [auditoria-seguridad-2026-07-16.md](audits/auditoria-seguridad-2026-07-16.md) —
  auditoría de seguridad (SEC-01…SEC-08); enrutada a G53–G56.
- [auditoria-externa-concepto-2026-07-18.md](audits/auditoria-externa-concepto-2026-07-18.md) —
  primera auditoría externa de concepto, producto y comunicación; enrutada a
  ADR-025 (reposicionamiento), P4→ADR-025 y P40–P42.

## Capa 4 — [archive/](archive/) (ejecutado, no vigente)

Planes y artefactos que ya cumplieron su función. Se conservan porque el
histórico importa (los ADR y las bitácoras los citan), pero no gobiernan nada.

- [archive/migracion-vm.md](archive/migracion-vm.md) — plan de migración de la
  VM a PUC-Lua/wasm (ADR-019); completado con M17.
- [archive/migracion-vm-censo.md](archive/migracion-vm-censo.md) — censo de la
  frontera VM (anexo de M01).

## Frontmatter

Todo `.md` de `docs/` abre con un bloque YAML. Campos comunes: `title`,
`description` (opcional en entradas-registro), `type` (`contrato · adr ·
hallazgo · ronda · sesion · plan · estado · pospuesto · runbook · auditoria ·
archivo · indice`), `status` y `date` (cuando consta). Por tipo: los contratos
llevan `layer` (`core`/`contracts`) y `web` (`wiki`/`api`/`none`); los ADR,
`id` y `supersedes`/`superseded_by`; los hallazgos, `id`, `origin`,
`resolution`, `affected` y `adr`; las rondas, `zone`, `scenarios` y
`findings`; las sesiones, `id` y `phase`. El frontmatter **duplica el estado
para las máquinas** (grep, la web, los agentes del flujo): el texto del
documento sigue siendo el registro canónico, y ambos deben cuadrar.

## Publicación web

La web de documentación (`web/`, GitHub Pages) publica **solo la Capa 1** —los
contratos vivos— más las seis páginas locales de «empezar» y la referencia
navegable derivada de `contracts/api.md`. Las **Capas 2-4 no se publican**: el
flujo de diseño (`decisions/`, `findings/`, `postponed/`, `validation/`,
`plan/`, `worklog/`), `audits/` y `archive/` son
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
  `> ✅ …`— los barre el mismo plugin. El frontmatter no llega al HTML: Astro
  lo separa del cuerpo al cargar la colección.
- **`docs/` conserva su trazabilidad intacta**: el flujo interno (jueces,
  `auditor-docs`, el propio diseño) necesita esos marcadores, y la limpieza vive
  solo en el pipeline de la web.

Dos gates lo blindan en CI: `check:limpieza:fuente` verifica en `docs/` que los
pares `enu:interno` estén balanceados **antes** del build; el gate
`check-limpieza-html` (`check:limpieza`) recorre el HTML de `dist/` **después** y
falla si se filtró cualquier marcador. Para dar de alta o retirar una página de
la wiki, la skill [/alta-wiki](../.claude/skills/alta-wiki/SKILL.md).

## Nota sobre nombres e idioma

Desde la reestructuración de 2026-07-17, **todas las carpetas** de `docs/`
usan nombres en inglés y los ficheros conservan sus nombres en español, igual
que la prosa. El reparto de idiomas quedó decidido en
[ADR-025](decisions/adr/adr-025-reposicionamiento-motor-de-harnesses.md)
(pieza 5): el **frente público** (README raíz, web de docs, quickstart) se
redacta en inglés primero; la **fuente documental interna** — este `docs/`,
los commits y el flujo de trabajo — sigue en español. No habrá migración del
repo al inglés (alternativa descartada en ese ADR).
