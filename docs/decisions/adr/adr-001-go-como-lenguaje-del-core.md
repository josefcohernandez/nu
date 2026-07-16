---
title: "Go como lenguaje del core"
type: "adr"
id: "ADR-001"
status: "aceptada"
date: "2026-06"
---
# ADR-001 · Go como lenguaje del core

**Estado:** Aceptada · 2026-06

**Contexto.** El proyecto nace como reacción al dependency hell de JS/TS en los
harnesses actuales. Necesitamos: binario único sin runtime, cross-compile
trivial, buen soporte de concurrencia (streaming SSE, subprocesos, UI
concurrente) y velocidad de iteración alta mientras la API de extensiones está
en flujo. Candidatos evaluados: Go, Rust, Zig, C.

**Decisión.** Go, con `CGO_ENABLED=0`.

**Razonamiento.**
- Binario estático y cross-compile resuelven la distribución (la antítesis de
  npm).
- El trabajo real del harness (IO concurrente) es el punto fuerte de Go.
- Prior art directo: Crush (Charm) y la TUI original de OpenCode son Go.
- Rust (ratatui + mlua) fue el segundo candidato serio; se descarta por
  velocidad de iteración en fase de diseño, no por capacidad. Codex CLI
  (reescrito de TS a Rust) valida que ambos caminos funcionan.
- Zig/C descartados: meses de infraestructura que Go/Rust regalan.

**Consecuencias.** Renunciamos a LuaJIT embebido (requeriría cgo). El
rendimiento del scripting queda acotado por gopher-lua → refuerza ADR-004.

---
