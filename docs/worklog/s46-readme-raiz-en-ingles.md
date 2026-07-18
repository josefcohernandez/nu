---
title: "README raíz en inglés y filosofía a la tesis de motor de harnesses (Fase 9, ADR-025 Fase 1)"
type: "sesion"
id: "S46"
phase: 9
status: "cerrada"
---
# S46 — README raíz en inglés (Fase 9 — Producto, ADR-025 Fase 1)

**Qué es.** La primera sesión de la Fase 9 (Producto) y la primera **editorial**
del proyecto: reescribir el `README.md` raíz en inglés con el posicionamiento de
[ADR-025](../decisions/adr/adr-025-reposicionamiento-motor-de-harnesses.md)
(piezas 1-2 y 5) y las recomendaciones de la
[auditoría externa 2026-07-18](../audits/auditoria-externa-concepto-2026-07-18.md).
No toca código de runtime; se rige por el **DoD propio** de la fase (artefacto
observable + gates en verde + frente público en inglés), no por el ciclo
BDD→TDD→juicio, que presupone lógica Go.

**Qué se entregó.**
- `README.md` reescrito en inglés (~150 líneas, antes ~200): hero directo («a
  self-extensible coding harness shipped as a single static binary»), quickstart
  de tres comandos, sección «Why enu» (deploy anywhere / rewrite everything /
  automate without a UI), plugin de ejemplo completo de ~10 líneas con la nota
  «this is not a special extension API», diagrama de capas ASCII
  kernel/API/plugins, tabla comparativa **enu vs Pi** honesta (admite que Pi es
  más maduro y con ecosistema), bloque de estado breve con señales comprobables,
  y documentación **por intención del lector**.
- Eliminados del camino de entrada (recomendación de la auditoría): «las 45
  sesiones cerradas», «la release publicada va por detrás del código», el CTA a
  la competencia («Claude Code, Aider o Cursor son la elección responsable») y
  el «pseudocódigo-como-validación» — sustituido por señales verificables
  (plugins solo por API pública, e2e contra binario real, race detector,
  checksums).
- `docs/core/filosofia.md` §lema actualizado a la tesis de motor de harnesses
  (en español: es fuente documental interna, ADR-025 pieza 5).

**Decisiones operativas (bajo umbral de G##).**
1. **URL de instalación.** El quickstart usa la URL que **funciona hoy**
   (`raw.githubusercontent.com/.../install.sh`) en vez de `enu.sh/install`. El
   dominio canónico `enu.sh` y el instalador endurecido son **S51** (ADR-026
   pieza 5); publicar un `curl` roto contradiría la honestidad que el propio
   README predica. Se migra a `enu.sh` al cerrar S51.
2. **Sin `README.es.md`.** El inglés queda como fuente pública canónica; no se
   crea versión española por ahora (ADR-025 pieza 5: «versión española enlazada
   **donde exista**»). El README viejo era español pero con el posicionamiento
   antiguo; conservarlo habría propagado una tesis ya superada. Si aparece
   demanda, la traducción del contenido nuevo es trivial de añadir.
3. **La demo visual del hero es S47.** Se deja un `TODO(S47)` donde irá el
   GIF/asciinema; la portada visual y la legibilidad de la web son la sesión
   siguiente, no ésta.

**DoD (editorial).** `go build ./...` no aplica (no se tocó Go, sigue verde);
los enlaces internos del README verificados uno a uno (todos resuelven); no hay
filas 🔒 (sin lógica de runtime). El criterio de hecho de la fila S46 se cumple:
los siete elementos presentes, los cuatro eliminados fuera, `filosofia.md`
coherente con ADR-025.

**Desviación de protocolo.** No se lanzó `escenarista-bdd` ni `/juicio` de
espec: son pasos para código, y esta sesión es documentación pública. El juez
que aplica aquí es la revisión del operador sobre la prosa (la cara del
proyecto), hecha antes del cierre.
