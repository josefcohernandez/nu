---
name: ronda
description: Ejecuta una ronda nueva de pseudocódigo contra la API (docs/pseudocodigo.md) — el ejercicio SDD de validación — con fan-out paralelo de escenaristas y verificación adversarial de cada candidato a hallazgo. Úsala para torturar una zona de la API o de los contratos antes de congelar un diseño, o proactivamente cuando una zona lleve tiempo sin torturar.
---

# Ronda de pseudocódigo (torturar la API)

La API se valida escribiendo pseudocódigo contra ella: escenarios reales
usando **solo** lo especificado en los contratos. Cada punto donde el código
no se puede escribir es un hallazgo. Esta skill paraleliza el ejercicio y le
añade el filtro que evita su modo de fallo clásico: inventar API para tapar
lo que ya se componía.

## Pasos

1. **Elegir la zona.** Lee las rondas previas de `docs/pseudocodigo.md` (y el
   estado de `problemas.md`) para ver qué zonas ya fueron torturadas (caminos
   feos, orquestación por un tercero...). Propón al usuario la zona y el
   ángulo de esta ronda: una extensión concreta, una combinación de
   primitivas, un tipo de autor de plugin (novato, hostil, con prisa), un
   régimen (cancelación masiva, reload en caliente, worker saturado).

2. **Fan-out.** Lanza **en paralelo** N agentes `escenarista-bdd` en modo
   ronda (N = 3-5 según amplitud de la zona), cada uno con una **semilla
   distinta**: un escenario-objetivo concreto y diferente que intentar
   escribir. Las semillas deben forzar caminos distintos (no tres variantes
   del mismo happy path). Cada escenarista devuelve su pseudocódigo con los
   §N citados y sus candidatos a hallazgo.

3. **Consolidar.** Reúne los candidatos y deduplica (dos escenaristas suelen
   chocar con la misma grieta por caminos distintos — eso es señal de que es
   real, consérvala como evidencia).

4. **Verificación adversarial.** Por cada candidato, lanza un `verificador`
   fresco (solo el candidato + la espec; nunca el razonamiento del
   escenarista). Su mandato: demostrar que el escenario **ya es expresable**
   componiendo la API existente.

5. **Triaje** con el usuario, candidato a candidato:
   - `REAL` y la v1 lo necesita → **G##** vía `/hallazgo`.
   - `REAL` pero puede esperar → **P##** en `docs/pospuesto.md`, siempre con
     su **disparador** (la señal concreta que indica reabrirlo).
   - `FALSO POSITIVO` → *demostrado-expresable*: la composición del
     verificador se documenta en la ronda (es tan valiosa como un hallazgo:
     enseña el patrón).

6. **Documentar la ronda** en `docs/pseudocodigo.md` con su formato: título
   de ronda numerada, escenarios numerados y titulados con su pseudocódigo
   citando los §N que ejercitan, y la sección final `## Hallazgos` con cada
   uno en negrita (`**G## — <título>.**`), dónde apareció y su estado.
   Actualiza los contadores/menciones en `problemas.md` y `CLAUDE.md` si
   citan el número de rondas.

7. **Commit** en español: `Ronda N de pseudocódigo: <zona torturada>`.
