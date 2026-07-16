---
title: "Modelo de concurrencia híbrido (\"modelo del navegador\")"
type: "adr"
id: "ADR-004"
status: "aceptada"
date: "2026-06"
---
# ADR-004 · Modelo de concurrencia híbrido ("modelo del navegador")

**Estado:** Aceptada · 2026-06

**Contexto.** Un agente es inherentemente concurrente (stream de tokens, tool
calls paralelas, input de UI simultáneos). gopher-lua no es thread-safe. El
modelo Neovim (todo en un hilo) produce los cuelgues con trabajo pesado que
queremos evitar. Alternativas evaluadas: (1) estado único + event loop, (2)
actores puros con paso de mensajes por extensión, (3) extensiones como
subprocesos, (4) cambiar de runtime (Starlark/WASM).

**Decisión.** Híbrido de tres patas:
1. Estado Lua principal single-threaded con event loop y async por coroutines
   (patrón Node/libuv/`vim.uv`) para UI, hooks y orquestación.
2. Workers explícitos (`worker.spawn()`): estados Lua adicionales en
   goroutines propias, sin memoria compartida, paso de mensajes.
3. Primitivas Go paralelas por dentro para todo lo universalmente pesado
   (búsqueda, diff, parsing, highlighting, markdown).

Regla de oro: **Lua decide, Go ejecuta**.

**Razonamiento.**
- Un harness no es un editor: no mantiene buffers gigantes resaltados a cada
  tecla. Sus tareas pesadas son delegables a primitivas paralelas.
- El monohilo en el estado principal es una feature (determinismo, cero data
  races) para el 95% de los plugins; el 5% restante tiene workers opt-in.
- Subprocesos como modelo principal: latencia inaceptable para hooks de UI y
  reintroduce fricción de distribución (queda como Capa 2).
- Es el modelo ya validado por la plataforma web y por Luau (actores de
  Roblox).

**Consecuencias.** Hay que construir el equivalente a "luv para Go" (event
loop + puente de coroutines): el mayor coste de ingeniería inicial del core.
Markdown/highlighting entran al kernel como builtins por rendimiento, violando
conscientemente la pureza del kernel mínimo. Queda abierta la granularidad de
aislamiento (ADR-008).

---
