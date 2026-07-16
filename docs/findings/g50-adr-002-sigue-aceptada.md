---
title: "ADR-002 sigue \"Aceptada\" con su decisión de implementación (gopher-lua / Lua 5.1) obsoleta y sin anotación de reemplazo"
type: "hallazgo"
id: "G50"
status: "resuelto"
date: "2026-07-12"
origin: "auditoría integral 2026-07-12"
resolution: "ADR-002 recibe una nota de estado: su núcleo (Lua) sigue vigente pero su realización quedó reemplazada por ADR-019/ADR-020."
affected: ["adr.md"]
adr: "ADR-002"
---
# G50 · ADR-002 sigue "Aceptada" con su decisión de implementación (gopher-lua / Lua 5.1) obsoleta y sin anotación de reemplazo — `adr.md` — **RESUELTO**

**Resolución** (aplicada en [adr.md](adr.md) ADR-002): **nota de estado** al estilo de la de ADR-011 — el núcleo de la decisión (Lua como lenguaje de extensión, frente a Starlark/JS/WASM) **sigue vigente**; su realización (gopher-lua, Lua 5.1, y la consecuencia "no thread-safe condiciona la concurrencia") quedó **reemplazada por ADR-019/ADR-020** (PUC-Lua 5.4 compilado a WASM sobre wazero; la retirada M17 eliminó gopher-lua del binario y del `go.mod`). El cuerpo no se reescribe (disciplina del ADR); la nota evita que un lector de adr.md vea como vigente un baseline que api.md §1.2 ya contradice. (A-27 del informe.)

**Problema.** La misma migración que llevó a marcar ADR-011 "Reemplazada por ADR-020" dejó ADR-002 sin anotar, pese a que su decisión de implementación quedó igual de obsoleta: asimetría de mantenimiento del registro.
