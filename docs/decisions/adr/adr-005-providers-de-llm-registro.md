---
title: "Providers de LLM: registro en TOML + adaptadores en Lua"
type: "adr"
id: "ADR-005"
status: "aceptada"
date: "2026-06"
---
# ADR-005 · Providers de LLM: registro en TOML + adaptadores en Lua

**Estado:** Aceptada · 2026-06

**Contexto.** Los providers difieren en protocolo (SSE, tool calls, system
prompts, thinking blocks): eso es código. Pero endpoints, claves, modelos y
límites son datos. ¿Dónde vive cada cosa?

**Decisión.** TOML declara el registro (datos); los adaptadores de protocolo
son extensiones Lua oficiales (código). El kernel solo aporta la primitiva
HTTP/SSE.

**Razonamiento.**
- Coherente con ADR-003: implementar protocolos en el core contradiría el
  kernel mínimo.
- Parsear SSE en Lua es viable: texto a velocidad de lectura humana.
- Añadir un provider raro (Ollama, vLLM, proxy corporativo) pasa a ser un
  fichero Lua, sin recompilar ni esperar release.
- La configuración del usuario común sigue siendo declarativa y simple (TOML).

**Consecuencias.** El cliente HTTP del kernel debe exponer streaming de
respuesta de primera clase desde v1.

---
