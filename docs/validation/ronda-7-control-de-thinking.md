---
title: "Ronda 7: control de razonamiento (`thinking`) por-modelo"
type: "ronda"
id: "ronda-7"
zone: "control de razonamiento (`thinking`) por-modelo"
status: "cerrada"
scenarios: [32]
findings: ["G34"]
---
# Ronda 7: control de razonamiento (`thinking`) por-modelo

Una zona que las rondas previas no torturaron: **pedir** razonamiento
extendido al modelo (no recibir sus bloques `thinking`, que ya se validaron —
viajan con su firma en `meta`, §2.2 —, sino el parámetro de *solicitud* del
request canónico). El disparador es real: el modelo por defecto del proyecto es
`claude-opus-4-8`, de la familia que cambió la forma de pedir razonamiento.

## Escenario 32: activar razonamiento en dos modelos con el contrato canónico

```lua
-- Un plugin (o una futura feature del agente) quiere activar razonamiento por
-- turno. Con SOLO el contrato canónico de hoy (providers.md §2.1) la única
-- forma es `thinking = { budget }`.

agent.hook("request.pre", function(req, ctx)
  req.thinking = { budget = 8000 }   -- lo único que el canónico sabe expresar
  return req
end)

-- (a) Modelo "legacy" (extended thinking con presupuesto): EXPRESABLE.
--     El adaptador anthropic traduce { budget = 8000 } -> el wire
--     { type = "enabled", budget_tokens = 8000 }, que esos modelos aceptan.

-- (b) Opus 4.6+ (claude-opus-4-8, el modelo POR DEFECTO): el MISMO código
--     produce el MISMO wire { type = "enabled", budget_tokens = 8000 } -> la API
--     real responde 400: esa familia RETIRÓ budget_tokens y espera
--     { type = "adaptive" }. El contrato canónico NO TIENE forma de pedir
--     "adaptive": no hay nada que `req.thinking` pueda llevar para expresarlo, y
--     el adaptador —traductor fiel— no puede inventar lo que el canónico calla.
--                                                                        [G34]
```

Veredicto: la rama (a) es expresable; la (b) **no**. El modelo canónico solo
sabe pedir razonamiento por *presupuesto* (`budget`), una forma que los modelos
modernos rechazan, y no ofrece un *modo* "adaptive". Es una grieta del **modelo
canónico** (no del adaptador, que cumple el contrato congelado al pie): falta
vocabulario para expresar el modo de razonamiento, y falta el **dato** de qué
forma entiende cada modelo. **[G34]**

> Nota: la grieta está **latente** hoy —el agente headless no rellena
> `req.thinking` en el ensamblado del turno (§2 paso 2), así que el 400 solo
> aparece por un hook `request.pre` como el de arriba o por una futura feature
> de control de razonamiento—. Se torturó y resolvió **antes** de cablear
> thinking para que esa feature nazca sobre un canónico ya correcto.

---

## Hallazgos (ronda 7)

**G34 — el modelo canónico de `thinking` no expresa el modo adaptativo.**
Resuelta en [ADR-016](../decisions/adr/adr-016-modelo-canonico-de-thinking.md)
(que **reabre y cierra [P21](../postponed/pospuesto.md)**, hasta hoy pospuesta): el parámetro
canónico crece por adición a `thinking = { mode?: "off"|"adaptive"|"budget",
budget? }` (con `{budget=N}` como alias compatible de `mode="budget"`), y el
**dialecto de razonamiento de cada modelo se declara como dato** en el
`providers.toml` (`thinking = "adaptive"|"budget"|"none"`), que el adaptador lee
para traducir por-modelo. El adaptador sigue siendo un traductor puro
(ADR-003/ADR-005): cero tablas de versiones de modelos en el código. Registrada
en [problemas.md](../findings/g34-el-modelo-canonico-de-thinking.md) (G34). Es la primera grieta nacida de
*usar* el binario contra la realidad de la API de un proveedor (el 400 de Opus
4.6+), no de una incompletitud interna.

---
