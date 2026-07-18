---
name: hallazgo
description: Ciclo completo de un hallazgo G## — registrarlo como fichero nuevo en docs/findings/, verificar que no sea ya expresable, decidir con opciones, aplicar la resolución a TODOS los documentos, auditar coherencia y disparadores P##, y ADR si procede. Úsala cuando la API no baste para algo, cuando un contrato presuponga API inexistente, o cuando una ronda/juicio destape una grieta real.
---

# Ciclo de un hallazgo G##

Una grieta que la v1 necesita cerrada se resuelve **una a una** y no está
hecha hasta ser coherente en todos los documentos. El código es el último
eslabón de la cadena, no el primero: si el hallazgo nació implementando, la
implementación espera a que los documentos cierren.

## Pasos

1. **Registrar.** Crea `docs/findings/gNN-<slug>.md` con el siguiente número
   G## libre (el índice de `docs/findings/README.md` lo dice; ojo a los huecos
   reservados que anota). El fichero lleva frontmatter de tipo hallazgo
   (`type: hallazgo`, `id: GNN`, `status: abierto`, `origin: <de dónde salió>`)
   y el formato de siempre: H1 `# GNN · <título corto> — <docs afectados>`,
   y los bloques **Problema** (la grieta, con la frase textual de la espec que
   se queda corta), **Impacto** (a quién afecta y con qué urgencia) y
   **Opciones** ((a), (b), (c)... cada una con su porqué). Añade su fila a la
   tabla índice de `docs/findings/README.md` y actualiza el contador de la
   cabecera (N registradas, N resueltas, N abiertas).

2. **¿Ya es expresable?** Antes de inventar API, lanza el agente `verificador`
   con el hallazgo: su mandato es demostrar que el escenario ya se compone con
   la API existente (el precedente: el semáforo con `nu.task.future`). Si
   vuelve FALSO POSITIVO con la composición como evidencia, el G## se cierra
   como *demostrado-expresable*: se documenta la composición en la resolución
   y no se toca la API. Este paso mata la mayoría de hallazgos baratos.

3. **Decidir.** Presenta las opciones al usuario con tu recomendación
   motivada (qué rompe menos, qué compone mejor, qué precedente sienta). Si la
   resolución **añade o toca `api.md`**, lanza antes `juez-filosofia` con la
   propuesta: una OBJECIÓN bloqueante vuelve al usuario. Toda adición a la API
   exige bump de `nu.version.api` y crece solo por adición.

4. **Aplicar a TODOS los documentos.** Busca con Grep cada contrato y
   pseudocódigo que presuponía la semántica vieja (`agente.md`,
   `providers.md`, `sesiones.md`, `chat.md`, `guia-plugins.md`,
   `pseudocodigo.md`, `arquitectura.md`, `modelo-ejecucion.md`... el corpus
   completo) y actualízalos. Deja el rastro: cada cambio cita el G## que lo
   motiva.

5. **Auditar.** Lanza `auditor-docs` con la descripción del cambio. Corrige
   cada incoherencia que reporte y presta atención a su barrido de
   disparadores P##: si un disparador de `docs/postponed/pospuesto.md` suena, díselo al
   usuario (reabrir un P## es decisión suya).

6. **ADR si procede.** Si la decisión es arquitectónica (cambia una regla del
   juego, no solo una firma), crea `docs/decisions/adr/adr-NNN-<slug>.md` con
   frontmatter de tipo adr (`id: ADR-NNN`, `status: aceptada`, `date`) y el
   formato contexto → decisión → consecuencias, y añade su fila al índice de
   `docs/decisions/adr/README.md`. Las entradas **nunca se reescriben**: si
   reemplaza a una vieja, la nueva declara `supersedes: [ADR-VVV]` y en la
   vieja se edita SOLO el frontmatter (`status: reemplazada`,
   `superseded_by: [ADR-NNN]`) y su línea `**Estado:**` — el cuerpo queda como
   registro histórico.

7. **Cerrar.** El fichero del hallazgo pasa a resuelto: el título gana
   `— **RESUELTO**`, el bloque **Resolución** va al principio (aplicada en
   <docs afectados>, con enlaces) y el frontmatter se rellena
   (`status: resuelto`, `resolution: <una frase>`, `affected: [...]`,
   `adr: ADR-NNN` si lo hubo, `date`). Actualiza su fila y el contador en
   `docs/findings/README.md`. Commit en español: `Resuelve G##: <la decisión
   en una frase>`.

8. **Si había implementación en pausa** (el hallazgo nació en una `/sesion`):
   ahora sí, vuelve a ella e implementa la resolución. El test que blinde el
   G## debe **nombrarlo** en comentario.
