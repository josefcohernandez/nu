---
name: hallazgo
description: Ciclo completo de un hallazgo G## — registrarlo en docs/problemas.md, verificar que no sea ya expresable, decidir con opciones, aplicar la resolución a TODOS los documentos, auditar coherencia y disparadores P##, y ADR si procede. Úsala cuando la API no baste para algo, cuando un contrato presuponga API inexistente, o cuando una ronda/juicio destape una grieta real.
---

# Ciclo de un hallazgo G##

Una grieta que la v1 necesita cerrada se resuelve **una a una** y no está
hecha hasta ser coherente en todos los documentos. El código es el último
eslabón de la cadena, no el primero: si el hallazgo nació implementando, la
implementación espera a que los documentos cierren.

## Pasos

1. **Registrar.** Siguiente número G## libre en `docs/problemas.md`, con su
   formato: título corto + referencia de espec (`api.md §N`), y los bloques
   **Problema** (la grieta, con la frase textual de la espec que se queda
   corta), **Impacto** (a quién afecta y con qué urgencia) y **Opciones**
   ((a), (b), (c)... cada una con su porqué). Actualiza el contador de la
   cabecera del fichero.

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
   disparadores P##: si un disparador de `docs/pospuesto.md` suena, díselo al
   usuario (reabrir un P## es decisión suya).

6. **ADR si procede.** Si la decisión es arquitectónica (cambia una regla del
   juego, no solo una firma), añade un ADR con contexto → decisión →
   consecuencias. Las entradas **nunca se reescriben**: si reemplaza a una
   vieja, la nueva la *supersede* y la vieja se marca "Reemplazada por
   ADR-NNN".

7. **Cerrar.** La entrada pasa a `— **RESUELTO**` con el bloque **Resolución**
   al principio (aplicada en <docs afectados>, con enlaces). Commit en
   español: `Resuelve G##: <la decisión en una frase>`.

8. **Si había implementación en pausa** (el hallazgo nació en una `/sesion`):
   ahora sí, vuelve a ella e implementa la resolución. El test que blinde el
   G## debe **nombrarlo** en comentario.
