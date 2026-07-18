---
title: "Reposicionamiento: enu es un motor para construir coding harnesses a medida; Pi es el competidor de referencia; pre-1.0 con roturas justificadas; frente público en inglés"
type: "adr"
id: "ADR-025"
status: "aceptada"
date: "2026-07-18"
---
# ADR-025 · Reposicionamiento: enu es un motor para construir coding harnesses a medida

**Estado:** Aceptada · 2026-07-18 · motivada por la [auditoría externa de
concepto](../../audits/auditoria-externa-concepto-2026-07-18.md)

**Contexto.** El proyecto se venía presentando como «runtime de Lua orientado a
terminal cuya killer app es un coding harness» y el README comparaba contra
Claude Code, Aider y Cursor. Una auditoría externa independiente (2026-07-18)
valida la arquitectura (kernel mínimo, agente sin privilegios, «Lua decide, Go
ejecuta», disciplina de testing) pero señala tres cosas: (1) el competidor
conceptual real es **Pi** — un harness extensible con ecosistema activo — y la
comparación actual lo esquiva; (2) la ventaja defendible de enu no es «ser
extensible» (Pi ya lo es) sino la **combinación** de runtime nativo
autocontenido + Lua sin toolchain + completitud de la API pública como
principio arquitectónico; (3) existe una desproporción entre profundidad de
arquitectura/proceso y validación externa (cero autores de plugins ajenos).
En paralelo, el operador ya había decidido girar el producto hacia entornos
corporativos con `mesh` y resiliencia como pilares (contexto recogido en
[G60](../../findings/g60-el-lock-de-sesion-nace-huerfano.md) §Propuesta). La
auditoría y el giro convergen: ambos apuntan al mismo posicionamiento.

**Decisión.** Cinco piezas:

1. **La tesis de producto.** enu se posiciona como **motor para construir,
   distribuir y gobernar coding harnesses a medida** — «a self-extensible
   coding harness with no host runtime» — no como «otro agente de código».
   El agente oficial es la demo de referencia del motor, no el producto. Los
   mercados objetivo: equipos de plataforma interna, autores de tooling de
   agentes, CI agentic headless y entornos locales/air-gapped. El argumento
   central de venta técnica es el corolario de completitud (idea central 2):
   *si una feature oficial no se puede construir con la API pública, es un
   bug de la API*.
2. **Pi es el competidor de referencia.** README y web comparan primero
   contra Pi, honestamente (admitiendo su madurez y ecosistema superiores y
   reclamando la ventaja propia: despliegue como infraestructura — binario
   estático, sin Node/npm, plugins Lua sin toolchain). Las comparaciones con
   Claude Code/Cursor pasan a secundarias (otra categoría: productos
   cerrados/editores).
3. **Secuencia de ejecución por demostración, no por infraestructura.** No se
   añade arquitectura de base nueva hasta convertir la existente en pruebas
   visibles, en tres fases: **Fase 1** — promesa operativa (`enu init`,
   `enu doctor`, instalador endurecido, README/hero nuevos, demo real,
   comparación con Pi, smoke tests en sistemas limpios); **Fase 2** —
   ecosistema mínimo viable (`enu plugin add/remove/update/lock` sobre git
   con lockfile y checksums — reabre y decide P4; sin registry central, que
   queda pospuesto como P40 — plantillas oficiales y el plugin `forge`:
   enu construye, prueba e instala plugins de sí mismo usando solo la API
   pública); **Fase 3** — plataforma (protocolo JSONL/RPC versionado, plugin
   `trace` de observabilidad, `worktree`/mesh como demo de paralelismo
   aislado, guía de migración desde Pi). Las capacidades entran al plan por
   `/planificar-sesion` en ese orden; las piezas con superficie de API dudosa
   (RPC, forge) pasan antes por ronda de pseudocódigo. La cola durable de
   tasks (caso «tres tareas por Slack») se aparca como P41 con disparador.
4. **Política pre-1.0 de la API (matiza la idea central 4, no la deroga).**
   La disciplina aditiva se mantiene (la API crece por adición y con
   versionado), pero mientras el proyecto sea pre-1.0 se **permiten roturas
   de firma justificadas por ADR** — nunca por la vía de hecho. El criterio
   de corte de la 1.0 deja de ser interno («las sesiones del plan están
   cerradas») y pasa a ser externo: **al menos tres autores ajenos al
   proyecto han construido extensiones no triviales que el diseño no
   anticipó**. Hasta entonces la API se declara públicamente experimental.
   Razón: la completitud se ha validado contra las extensiones propias —
   coherencia interna — pero la ergonomía externa solo la validan terceros.
5. **Idioma: frente público en inglés, fuente interna en español.** El
   README, la web de documentación, el quickstart y todo artefacto de
   adquisición se redactan **en inglés primero** (con versión española
   enlazada donde exista). La fuente documental interna — `docs/` (contratos,
   ADR, findings, plan, worklog), mensajes de commit y el flujo de trabajo —
   **sigue en español**. La web ya es bilingüe; esto fija cuál es el idioma
   canónico de cada capa.

**Alternativas descartadas.** (a) «Pi sin npm» como lema: memorable pero
dependiente de una limitación que Pi puede mitigar empaquetándose; la tesis
duradera es la combinación, no la ausencia de npm. (b) Derogar la API sagrada
(«no congelar hasta tener usuarios», propuesta literal de la auditoría): el
régimen aditivo + roturas-por-ADR conserva la disciplina que hace confiable el
motor sin fingir una estabilidad que aún no puede prometerse. (c) Traducir
toda la fuente documental al inglés: coste enorme, valor marginal — quien
evalúa el proyecto lee README/web; quien contribuye al diseño ya atraviesa el
proceso interno, que seguirá en español. (d) Perseguir el mercado generalista
(«competir con Claude Code/Cursor»): se pierde por polish, modelo y comunidad;
el nicho de plataforma es donde la arquitectura es ventaja y no sobreingeniería.

**Consecuencias.**

- README y portada de la web se reescriben (Fase 1): definición directa,
  demo visual, plugin de ~10 líneas, tabla enu-vs-Pi, quickstart de tres
  comandos; se eliminan del camino de entrada los detalles de proceso interno
  («45 sesiones», «la release va por detrás», pseudocódigo-como-validación,
  el CTA hacia la competencia). El proceso interno no se amputa: se enlaza.
- [CLAUDE.md](../../../CLAUDE.md) §«Idioma y estilo» recoge la regla de la
  pieza 5. `filosofia.md` y la tabla comparativa del README se actualizan a la
  tesis de la pieza 1 cuando se ejecute la Fase 1 (trabajo de sesiones, no de
  este ADR).
- [P4](../../postponed/pospuesto.md) (package manager) queda **decidida** por
  la pieza 3 (gestor sobre git en Fase 2); en pospuestos entran P40 (registry
  central), P41 (cola durable de tasks) y P42 (port automático Pi→Lua vía
  forge), cada una con su disparador.
- La resolución de [G60](../../findings/g60-el-lock-de-sesion-nace-huerfano.md)
  (lease + reconciliación, drenaje del apagado) pasa de «deseable» a
  **prerrequisito de la Fase 3**: tanto RPC/CI como mesh y la futura cola
  durable dependen de esa doctrina de recursos.
- La inversión en themes y fidelidad TTY de la web se congela hasta agotar
  las fases 1-2.
