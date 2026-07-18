---
title: "Core mínimo: el agente y MCP son extensiones oficiales"
type: "adr"
id: "ADR-003"
status: "aceptada"
date: "2026-06"
---
# ADR-003 · Core mínimo: el agente y MCP son extensiones oficiales

**Estado:** Aceptada · 2026-06

**Contexto.** Dos modelos posibles: core-con-hooks (Neovim: el programa
principal en nativo, extensiones decoran) o kernel-runtime (Emacs/Textadept:
el programa entero escrito en el lenguaje de extensión sobre un kernel de
primitivas).

**Decisión.** Kernel-runtime. El core Go no contiene lógica de agente, MCP,
chat ni comandos: todo eso son extensiones Lua oficiales, embebidas en el
binario con `go:embed` pero sin ningún privilegio arquitectónico.

**Razonamiento.**
- "Lua puede hacer cualquier cosa" exige que las features oficiales sean
  construibles con la API pública; si no, la API está incompleta. Dogfooding
  estructural (como pi con sus propias features).
- El usuario radical no hace fork: sustituye extensiones.
- `go:embed` preserva la experiencia batteries-included.

**Consecuencias.** La superficie de primitivas del kernel crece (HTTP/SSE,
spawn con streams, UI completa): el core conceptualmente mínimo necesita una
stdlib grande. La estabilidad de la API core se vuelve crítica desde v1: los
breaking changes nos rompen a nosotros primero y al ecosistema después.

---
