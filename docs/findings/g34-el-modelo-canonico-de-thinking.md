---
title: "El modelo canónico de `thinking` no expresa el modo adaptativo (Opus 4.6+ 400ea con `budget_tokens`)"
type: "hallazgo"
id: "G34"
status: "resuelto"
date: "2026-06-27"
origin: "ronda 7 de pseudocódigo (validación del control de razonamiento)"
resolution: "thinking gana mode ('off'/'adaptive'/'budget') y el dialecto de razonamiento por modelo se declara como dato en providers.toml."
affected: ["providers.md §2.1/§3"]
adr: "ADR-016"
---
# G34 · El modelo canónico de `thinking` no expresa el modo adaptativo (Opus 4.6+ 400ea con `budget_tokens`) — `providers.md` §2.1/§3 — **RESUELTO**

**Resolución** (registrada en [ADR-016](../decisions/adr/adr-016-modelo-canonico-de-thinking.md), que **reabre y cierra** [P21](../postponed/pospuesto.md); aplicada en [providers.md](../contracts/providers.md) §2.1/§3 y la nota `⚠` del adaptador `anthropic`): el parámetro canónico crece **por adición** a `thinking?: { mode?: "off"|"adaptive"|"budget", budget? }` —con `{budget=N}` como **alias compatible** de `mode="budget"`, así que la forma congelada sigue válida—, y el **dialecto de razonamiento de cada modelo se declara como DATO** en el `providers.toml` (`thinking = "adaptive"|"budget"|"none"`, default `"budget"`), que viaja en el `ModelInfo` y el adaptador lee para traducir **por-modelo** (`adaptive` → `{type="adaptive"}`, `budget` → `{type="enabled", budget_tokens=N}`, degradando entre ambos según el dialecto; `none`/ausente → no se envía, degradación declarada §3 ob.5). El adaptador sigue siendo un **traductor puro** (ADR-003/ADR-005): cero tablas de versiones de modelos en el código. La superficie sagrada `enu.*` no cambia (es contrato de extensión). **Implementado** (sesión de construcción posterior al ADR, como manda el protocolo "el contrato lidera, el código sigue"): `thinking_to_wire` en `adapter_anthropic.lua` traduce por dialecto, `resolve` lleva `model.thinking` al `ModelInfo`, y `providers_p21_test.go` blinda las ocho combinaciones (dialecto × modo); el bloque legacy `budget_tokens` incondicional ya no existe.

**Problema.** El canónico congeló `thinking?: { budget?: integer }` y el adaptador `anthropic` lo emite como `{type="enabled", budget_tokens=N}` (extended thinking *legacy*). La familia Opus 4.6+ —incluido el modelo por defecto `claude-opus-4-8`— retiró `budget_tokens` y espera `{type="adaptive"}`: la petición da **400** contra la API real. No es defecto del adaptador (cumple el contrato congelado) sino del **modelo canónico**, al que le falta (1) vocabulario para pedir el modo adaptativo y (2) el dato de qué forma entiende cada modelo. Validado en [pseudocodigo.md](../validation/README.md) Ronda 7 (escenario 32): la rama "budget sobre legacy" es expresable, la rama "adaptive sobre Opus 4.6+" **no** hay código que la escriba. Estuvo pospuesta como P21; el disparador (modelo por defecto ya Opus 4.8) la reabre.

**Impacto.** **Latente** —el agente headless no rellena `req.thinking` en el ensamblado del turno, así que el 400 solo aparece por un hook `request.pre` o una futura feature de control de razonamiento— pero **bloquea la capacidad** de usar razonamiento extendido con los modelos Opus modernos, que para un harness de código es de primera. Barato de cerrar en el contrato ahora; caro después, con thinking cableado y consumidores que presupongan el canónico viejo.

**Opciones.** (a) `mode` en el canónico + dialecto por-modelo como dato del TOML (**la elegida**, ADR-016): traductor puro, crecimiento por adición; (b) heurística por id del modelo en el adaptador (`model:match("opus%-4%-[6-9]")`) — frágil, mete conocimiento de versiones de producto en un traductor, falla con ids renombrados; (c) **reemplazar** `budget` por la forma nueva — rompe la firma congelada y los tests grabados; (d) dejarlo pospuesto — descartado: el disparador (modelo por defecto Opus 4.8) ya está activo.
