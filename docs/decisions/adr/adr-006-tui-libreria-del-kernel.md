---
title: "TUI: librería del kernel"
type: "adr"
id: "ADR-006"
status: "propuesta"
date: "2026-06"
---
# ADR-006 · TUI: librería del kernel

**Estado:** Propuesta · 2026-06

**Contexto.** Candidatos en Go: Bubble Tea + Lipgloss (+ glamour para
markdown) o tview. La elección está acoplada a ADR-007 (qué API de UI se
expone a Lua): el kernel podría incluso usar primitivas de terminal propias.

**Decisión (provisional).** Bubble Tea + Lipgloss como punto de partida, a
revisar cuando se cierre ADR-007.

**Consecuencias.** Ninguna irreversible mientras la API Lua de UI no exponga
conceptos de Bubble Tea directamente (no debería: la API pública es nuestra,
la librería es detalle de implementación).

---
