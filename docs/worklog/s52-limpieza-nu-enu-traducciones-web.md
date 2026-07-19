---
title: "Limpieza nu→enu en las traducciones inglesas de la web (Fase 9, residuo de ADR-022)"
type: "sesion"
id: "S52"
phase: 9
status: "cerrada"
---
# S52 — Limpieza nu→enu en las traducciones inglesas de la web (Fase 9)

**Qué es.** Residuo del renombrado total del proyecto ([ADR-022](../decisions/adr/adr-022-renombrado-total-del-proyecto.md))
que la [auditoría de renombrado 2026-07-16](../audits/auditoria-renombrado-2026-07-16.md)
no alcanzó en `web/src/content/en/` (detectado al hacer S47). Sesión
**editorial y mecánica**, exenta de la puerta SDD/juez de filosofía: su «espec»
es ADR-022 (todo es `enu`). Sin dependencias.

**Qué se entregó.** 15 sustituciones `nu → enu` verificadas una a una, en 7
ficheros de `en/wiki/`:
- **Nombres de producto en prosa/títulos:** `# nu Philosophy` → `# enu
  Philosophy`, `## What nu is not`, `# Architecture of nu`, «the visible face of
  nu», «within nu's», «we want nu's» (filosofia, arquitectura, chat,
  guia-plugins).
- **Comando `nu --default-config` → `enu --default-config`** (filosofia,
  arquitectura, providers, chat, agente).
- **Rutas repo-local `<repo>/.nu/` → `<repo>/.enu/`** (agente: `.enu/skills/` y
  dos `.enu/agent.toml`) — confirmado contra la fuente de verdad del repo
  ([agente.md](../contracts/agente.md) §skills/§permisos, que ya usa `.enu/`).
- **Ejemplo de slug** (sesiones): `/home/diego/nu` → `home_diego_nu` pasa a
  `/home/diego/enu` → `home_diego_enu`, alineado con el ejemplo canónico de
  [sesiones.md](../contracts/sesiones.md) §2 (G38).

**Falsos positivos VERIFICADOS y preservados** (la §DoD exige no pisar tokens
legítimos). Cada uno cotejado contra la fuente canónica en español antes de
dejarlo:
- `en/empezando/inicio-rapido.md`: el enlace a
  `adr-010-extensiones-oficiales-distribuidas-con-nu.md` es el **nombre de
  fichero real e inmutable** del ADR (los ADR no se reescriben); cambiarlo
  rompería el enlace. Idéntico en la versión española.
- `en/referencia/codecs.md` (×4), `red.md` (×1), `events.md` (×2): `nu` es
  **dato de ejemplo** (`nombre = "nu"`, `who = "nu"`, y las salidas `nu` que esos
  snippets decodifican), no una referencia al producto. Idéntico en el canónico
  español.

**Decisiones operativas.**
1. **La fuente canónica de las páginas `wiki/` es el repo, no `web/src/content/docs/`.**
   Al verificar los casos ambiguos (`.nu/` vs `.enu/`, el slug) descubrí que
   `web/src/content/docs/` (español) **no tiene** `wiki/` —solo `referencia`,
   `extensiones`, `empezando`—: las páginas de wiki se sirven desde los
   contratos del repo (`docs/contracts/*`). Así que la verdad para
   `.enu/`/slug salió de `docs/contracts/agente.md` y `docs/contracts/sesiones.md`,
   no de una traducción intermedia. Anotado para futuras pasadas de coherencia.
2. **Alcance real vs. previsto.** La fila del plan anticipaba tocar codecs,
   events y red; al verificar, resultaron **todos** falsos positivos (dato de
   ejemplo). El trabajo real quedó en `en/wiki/` (7 ficheros). El criterio de
   hecho (`grep -rE "\bnu\b" web/src/content/en/` sin `nu` como producto) se
   cumple: lo que queda son los 8 falsos positivos verificados.

**DoD (variante editorial de la fase, como S46/S47).** No hay código Go: `go
build`/tests intactos por no tocarse. Gates de la web que corren sin el registro
npm (bloqueado en local, E403, como en S47): `check:drift` ✓ (113/113 callables
coherentes — las ediciones son prosa/rutas, no firmas), `check:contraste` ✓,
`check:limpieza:fuente` ✓ (pares `enu:interno` balanceados). El `astro build` y
`check:limpieza` post-build se validan en la CI del PR. Sin BDD/TDD/🔒/juez
(editorial, exenta por ADR-022).

**Cierre de Fase 9.** S52 **no** cierra la fase: el cierre es **CP-12** (humo del
funnel completo README→instalador→`enu init`→`enu` + **mutación 🔒 batcheada** de
S49/S50/S51). El puntero avanza a CP-12.
