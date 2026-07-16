---
title: "Convenciones de la API: namespace global, async por corrutinas, errores estructurados"
type: "adr"
id: "ADR-009"
status: "propuesta"
date: "2026-06"
---
# ADR-009 · Convenciones de la API: namespace global, async por corrutinas, errores estructurados

**Estado:** Propuesta · 2026-06 (se acepta al congelar [api.md](../../contracts/api.md)) ·
**El namespace global `nu` de la decisión 1 queda reemplazado por
[ADR-022](adr-022-renombrado-total-del-proyecto.md)**
(16-jul-2026); el resto de esta entrada (async por corrutinas, errores
estructurados) sigue vigente sin cambios.

**Contexto.** Antes de escribir código se define formalmente la API v1
([api.md](../../contracts/api.md)). Tres decisiones transversales necesitan registro propio.

**Decisión.**

1. **Namespace global `nu`** con submódulos (`nu.fs`, `nu.ui`, ...), como el
   global `vim` de Neovim; `require` queda para módulos de plugins. La stdlib
   bloqueante de Lua (`io`, `os.execute`, ...) se deshabilita: todo IO pasa
   por las primitivas async del core o congelaría el event loop.
2. **Async por funciones suspendientes**: dentro de una task (corrutina del
   scheduler), las primitivas de IO se llaman en estilo secuencial y
   suspenden hasta completarse (await implícito, patrón cosockets de
   OpenResty). Los handlers síncronos (input, eventos) no pueden suspender:
   lanzan tasks. Sin callbacks anidados ni promesas explícitas en la API.
3. **Errores estructurados lanzados** (`error({code, message, detail})`,
   capturables con `pcall`) en lugar del estilo `res, err`. Códigos
   reservados (`ENOENT`, `ETIMEOUT`, `ECANCELED`, `EBUDGET`, ...). Razón:
   los errores que se lanzan componen a través de capas de extensiones y no
   se ignoran en silencio; `res, err` se pierde al primer descuido.

**Consecuencias.** La DX de plugin trivial es código secuencial sin
conceptos async visibles. Deshabilitar `io`/`os` rompe compatibilidad con
librerías Lua puras que los usen (asumido: el ecosistema objetivo escribe
contra `nu.*`). El puente corrutinas↔goroutines del scheduler es la pieza
central del kernel (coherente con ADR-004).

---
