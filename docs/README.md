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
| [filosofia.md](filosofia.md) | Principios fundacionales y "lo que nu no es". El *porqué* del proyecto. |
| [arquitectura.md](arquitectura.md) | Vista estática: las capas, el inventario de primitivas del kernel. |
| [modelo-ejecucion.md](modelo-ejecucion.md) | Vista dinámica: concurrencia, comunicación, limitaciones. |
| [api.md](api.md) | **La API v1 del core — la "superficie sagrada".** Firmas y semánticas. |
| [providers.md](providers.md) | Contrato de la extensión oficial de providers. |
| [agente.md](agente.md) | Contrato de la extensión oficial `agent` (motor headless). |
| [sesiones.md](sesiones.md) | Contrato de persistencia: JSONL append-only. |
| [chat.md](chat.md) | Contrato de la extensión oficial `chat` (la UI). |
| [guia-plugins.md](guia-plugins.md) | Sabiduría práctica para autores de plugins + checklist. |
| [malla.md](malla.md) | Contrato de la extensión oficial `mesh` (borrador v0.1; su §11 sigue abierta). |

## Capa 2 — Flujo de diseño y construcción

El registro de cómo se decide y se construye. Append-only por convención: las
entradas no se reescriben, se suceden.

| Documento | Rol |
|---|---|
| [adr.md](adr.md) | Decisiones técnicas (ADR-NNN); las reemplazadas se marcan, nunca se borran. |
| [problemas.md](problemas.md) | Grietas que la v1 necesita cerradas (G##, con estado vivo en su cabecera). |
| [pospuesto.md](pospuesto.md) | Lo que se decidió no decidir todavía (P##), cada uno con su disparador. |
| [pseudocodigo.md](pseudocodigo.md) | El ejercicio de validación: rondas de pseudocódigo que torturan la API. |
| [implementacion.md](implementacion.md) | Plan de construcción por sesiones (S##), con puntero ▶ y bitácora. |
| [decisiones-implementacion.md](decisiones-implementacion.md) | Bitácora operativa: decisiones y desviaciones por sesión, por debajo del umbral de `G##`. |

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

## Nota sobre nombres e idioma

Las carpetas nuevas (`audits/`, `archive/`) ya usan nombres en inglés; los
ficheros conservan sus nombres en español para no romper dos veces las
referencias cruzadas (enlaces markdown, rutas en `.claude/` y citas en
comentarios de código). El renombrado de ficheros y la documentación
multi-idioma (inglés/español) quedan para la futura migración a inglés del
repo, en una sola pasada.
