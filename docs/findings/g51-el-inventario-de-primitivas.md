---
title: "El inventario de primitivas de `arquitectura.md` omite `enu.search` y el codec YAML"
type: "hallazgo"
id: "G51"
status: "resuelto"
date: "2026-07-12"
origin: "auditoría integral 2026-07-12"
resolution: "La tabla de arquitectura.md se completa nombrando enu.search y el codec YAML en el inventario de primitivas del kernel."
affected: ["arquitectura.md", "api.md §11-§12"]
---
# G51 · El inventario de primitivas de `arquitectura.md` omite `enu.search` y el codec YAML — `arquitectura.md` / `api.md` §11-§12 — **RESUELTO**

**Resolución** (aplicada en [arquitectura.md](arquitectura.md), tabla del kernel): la fila **io** nombra la búsqueda paralela del árbol (`files`/`grep`, api.md §11) y la fila **data** enumera YAML junto a JSON y TOML (api.md §12, necesario para las skills de agente.md §6). Quien lea solo la tabla como "el inventario" ya no pierde dos módulos de la superficie sagrada. (A-33 del informe; la supuesta omisión de `enu.sys` se refutó en la verificación — está representada como "entorno" en la fila io.)
