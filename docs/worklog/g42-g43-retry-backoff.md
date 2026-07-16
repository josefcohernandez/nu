---
title: "Retry con backoff en el motor + `agent:error` estructurado con reintento manual (agente.md §2/§4/§10, chat.md §2/§4)"
type: "sesion"
id: "G42+G43"
status: "cerrada"
date: "2026-07-16"
---
# G42+G43 — Retry con backoff en el motor + `agent:error` estructurado con reintento manual (agente.md §2/§4/§10, chat.md §2/§4)

Sesión post-plan (fila `G42+G43 (extensión)` en la bitácora de
[implementacion.md](../plan/implementacion.md)). Decisiones operativas y desviaciones:

**Rescate, no desarrollo nuevo.** El trabajo nació el 2026-07-08 en la rama
`claude/architecture-analysis-report-lgmco5` (continuación de
`claude/ux-producto-pulido`, que reservaba los números G42–G43), anterior al
renombrado total nu→enu (ADR-022) y a las auditorías del 12 y del 16 de julio.
Se portó commit a commit sobre develop: barrido `nu.` → `enu.` en todo lo
añadido (código, snippets Lua de tests Go y prosa), resolución de los tres
conflictos (cabecera de `problemas.md` reescrita en develop; `chat/init.lua`;
`vmwasm.go`) y contadores de la cabecera de problemas 52→54.

**Hunk descartado deliberadamente.** El commit original de H-4 corregía además
el comentario de `Instance.mu` en `vmwasm.go`; develop ya lo había reescrito al
cerrar el data race de SEC-05 (G56), así que ese hunk se tira y solo se
conserva su test (`TestWatchConcurrentDeliveries`).

**El informe va a `docs/audits/`, anotado.** `informe-arquitectura.md` llegaba
en la raíz del repo; se archiva como
`audits/informe-arquitectura-2026-07-08.md` con nota de archivo (pre-ADR-022;
H-1/H-2 → G42/G43; H-4 → G56/SEC-05; H-12 → revisiones de CLAUDE.md; el
`govulncheck` de H-16 → pasada `/salud`). **Cabo suelto que se deja a
decisión:** H-3 (la UX de conflicto de lock de sesiones.md §6 sin consumidor)
sigue sin enrutar a G##/P## — la nota de archivo lo deja explícito.

**Juicio clean-room (panel espec+tests) y sus consecuencias.** Espec: CONFORME.
Tests: INSUFICIENTE con cuatro hallazgos, verificados uno a uno por
verificadores independientes: T1 REAL (la copia del algoritmo en el
subagente-worker no tenía ni un test) → `agent_g42_worker_test.go`, con el
adaptador `wretry` dirigido por el prompt porque los globales no cruzan al
worker, y la herencia probada con `max_retries=0` del padre (el único valor que
distingue herencia de default); T2 REAL (cancel durante el backoff sin test) →
`TestG42CancelDuranteBackoff` con `retry_base_ms=60000` (si el cancel no
cortara la espera, el test no acabaría); T3 REAL (`message` de `agent:retry`
sin asertar) → aserción añadida; T4 FALSO POSITIVO (precedencia
`opts>toml>default` sin test): el estándar de la casa es el del campo análogo
`max_turns` — misma cadena, cero tests de fontanería — y la política de tests
exime el glue de configuración.

**Inventario 🔒.** El backoff es lógica propia con frontera off-by-one y
clasificación de errores, y está DUPLICADO en el worker: entra en el inventario
como `G42 (extensión)` (la política dice "el inventario crece, nunca se
relaja"; los cierres post-plan anteriores no añadían fila — esta sí, porque el
juicio demostró en vivo el coste de no tenerla: la copia sin blindar).

**`pseudocodigo.md` NO se retro-anota (decisión explícita).** El Escenario 2 de
la ronda 2 envuelve en `with_retries` el consumo entero del stream — el
anti-patrón exacto que G42 veta—. Es un registro histórico del ejercicio SDD
(anterior a la decisión) sin precedente de retro-anotación; la semántica
canónica vive en agente.md §2 y en la entrada G42 de problemas.md.

**Web.** La wiki ES se publica sola desde `docs/`; la instantánea EN se
regeneró a mano en las páginas tocadas (`agente`, `chat`, `problemas`).
`api.md` no se toca: sin bump de `enu.version.api` y sin pasada de
`/sync-web` (el gate `check:drift` vigila solo la referencia ES ↔ api.md).
