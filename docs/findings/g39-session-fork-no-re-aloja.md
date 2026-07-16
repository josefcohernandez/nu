---
title: "`Session:fork` no re-aloja: sin `opts` (cwd/permisos/modelo) y con `at` sin unidad definida"
type: "hallazgo"
id: "G39"
status: "resuelto"
date: "2026-07-02"
origin: "ronda 8 de pseudocódigo (malla distribuida de agentes sobre git)"
resolution: "Session:fork gana opts (cwd/permisos/modelo) con herencia efímera y solo-recorte, más Session:close() en la firma del contrato."
affected: ["agente.md §2", "sesiones.md §5"]
---
# G39 · `Session:fork` no re-aloja: sin `opts` (cwd/permisos/modelo) y con `at` sin unidad definida — `agente.md` §2 / `sesiones.md` §5 — **RESUELTO**

**Resolución** (aplicada en [agente.md](agente.md) §2 —firma, párrafo "Fork y cierre" y nota de estado— y [sesiones.md](sesiones.md) §5): la opción (c) con las tres sub-decisiones. Crecimiento por adición: `fork(at?)` sigue válido.

1. **`Session:fork(at?, opts?)`** — el camino directo del re-alojamiento: los `opts` sobreescriben lo heredado con la misma semántica efímera que `resume` (G18: no se persisten, no reescriben historia), y los permisos **solo recortan** (la regla de `spawn`, §9/§11). La variante nace ya en su worktree, sin la ventana intermedia del rodeo (una sesión viva apuntando al cwd equivocado).
2. **`Session:close()` entra en la firma del contrato.** Existía de facto (implementado, idempotente, suelta el lock de escritor de sesiones.md §6) y lo necesitan otros flujos: el conflicto de locks de §6 y cualquier orquestador que abra N sesiones y deba soltarlas determinísticamente. Regla de la casa: cerrar explícitamente vía `enu.task.cleanup`; el GC como red de seguridad no determinista (mismo patrón que los `Proc` de api.md §6).
3. **Semánticas clavadas.** `at` indexa el **historial de mensajes vigente** (post-compactación; lo que la implementación ya hacía) — y `meta.parent.entry` queda documentado como enlace **navegacional**, no puntero de replay. La **herencia se especifica completa** ("todos los opts efímeros del padre salvo sobreescritura"), lo que convierte la deriva actual —el fork de la v1 copia una lista parcial que pierde `skills` y `thinking`— en un bug nombrable con contrato que lo respalda. Y se **bendice la desviación de la v1**: el fork **copia el prefijo** al transcript de la hija (sesiones.md §5 pasa de "el replay lee del padre" a la copia) — la hija autocontenida es justo lo que hace viajar los transcripts entre máquinas (ronda 8, escenario 35; P9).

Se descartó (b) a secas (bendecir solo el rodeo fork→close→resume): dos pasos y doble ciclo de lock para lo que conceptualmente es una operación, con el arma cargada de la sesión intermedia mal alojada. `close` se añade igualmente porque es higiene de ciclo de vida que faltaba con independencia del fork.

Nota para la sesión de construcción: implementar el `opts?` de `fork` y la herencia completa (hoy: lista parcial en `agent/init.lua:1139` que omite `skills` y `thinking`); la copia del prefijo y `close` ya cumplen.

**Problema.** Fork-como-replicación —K variantes que comparten el prefijo exacto del transcript (y su caché de prompt) y compiten en un torneo— exige que cada variante corra en su propio worktree (`cwd` distinto: el remedio de G16 para escrituras paralelas) y a veces con permisos recortados o modelo alternativo. Pero `Session:fork(at?) -> Session` no acepta `opts`, y qué hereda la sesión hija del padre no está escrito. El rodeo natural (cerrar el fork y reabrirlo con `agent.session{ resume = id, cwd = ... }`, opts efímeros de G18) *casi* funciona, pero se apoya en `Session:close()`, que la nota de estado de §2 da por implementado y la **firma del contrato omite**. Además `at` no define qué indexa (¿entrada del JSONL, mensaje, turno?) — `meta.parent = {id, entry}` de sesiones.md §5 sugiere entradas, pero la correspondencia es implícita. Aflorada en la ronda 8 ([pseudocodigo.md](pseudocodigo.md), escenarios 34-35).

**Impacto.** El torneo de forks (local y distribuido) se queda a un paso de ser expresable con fidelidad; el plan B (subagentes frescos con el plan re-inyectado por prompt) pierde justo el valor del fork — el prefijo compartido y su caché. También afecta al flujo de conflicto de locks de sesiones.md §6, cuya salida por defecto es «fork»: si el fork no puede re-alojarse, hereda el mismo cwd que causó el conflicto.

**Opciones.** (a) `fork(at?, opts?)` con la misma semántica efímera que `resume` (los opts son estado del proceso: no se persisten ni reescriben historia; los permisos solo recortan, como en `spawn`); (b) bendecir el rodeo: añadir `Session:close()` a la firma del contrato y documentar el patrón fork→close→resume-con-opts; (c) ambas — `fork(at?, opts?)` como camino directo y `close` en el contrato porque ya existe de facto y otros flujos lo necesitan. En cualquier caso: especificar que `at` indexa **entradas del transcript** (la unidad de `meta.parent.entry`) y qué hereda el fork en ausencia de opts.
