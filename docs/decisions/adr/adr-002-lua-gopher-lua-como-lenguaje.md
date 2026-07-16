---
title: "Lua (gopher-lua) como lenguaje de extensión"
type: "adr"
id: "ADR-002"
status: "aceptada"
date: "2026-06"
---
# ADR-002 · Lua (gopher-lua) como lenguaje de extensión

**Estado:** Aceptada · 2026-06 · *Nota (2026-07-12, G50): el núcleo de la
decisión —Lua como lenguaje de extensión, frente a Starlark/JS/WASM— sigue
vigente. Su realización (gopher-lua, Lua 5.1, y la consecuencia "no
thread-safe condiciona la concurrencia") quedó reemplazada por
ADR-019/ADR-020: PUC-Lua 5.4 compilado a WASM sobre wazero; la retirada M17
eliminó gopher-lua del binario y del `go.mod`.*

**Contexto.** La extensibilidad es el producto. Candidatos: Lua (gopher-lua o
LuaJIT/cgo), Starlark, Risor/Tengo, JS vía goja, WASM.

**Decisión.** Lua 5.1 embebido vía gopher-lua (Go puro).

**Razonamiento.**
- Lua está culturalmente probado como lenguaje de extensión (Neovim, wezterm,
  mpv, hammerspoon): la familiaridad del usuario es una feature.
- gopher-lua mantiene el binario estático sin cgo (coherente con ADR-001).
  LuaJIT daría rendimiento real pero rompe el cross-compile y el binario único.
- Starlark: paralelizable pero deliberadamente limitado (sin while ni
  recursión); incompatible con "Lua puede hacer cualquier cosa".
- goja (JS): mismo modelo monohilo, y reintroduce la cultura que evitamos.
- WASM: sandboxing y multi-lenguaje, pero DX de autoría muy inferior a 30
  líneas de Lua. Se reconsiderará solo si el sandboxing de terceros se vuelve
  requisito duro.

**Consecuencias.** Lua 5.1 (no 5.4). Rendimiento de intérprete: el trabajo
pesado debe vivir en primitivas Go (ADR-004). gopher-lua no es thread-safe →
condiciona todo el modelo de concurrencia.

---
