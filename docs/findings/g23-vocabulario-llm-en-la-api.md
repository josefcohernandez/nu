---
title: "Vocabulario LLM en la API sagrada (`enu.text.approx_tokens`)"
type: "hallazgo"
id: "G23"
status: "resuelto"
origin: "revisión filosófico-técnica del proyecto"
resolution: "enu.text.approx_tokens sale del core y pasa a providers.approx_tokens(s) en Lua, por ser vocabulario de producto sin coste que lo sostenga."
affected: ["api.md §10", "providers.md §5"]
---
# G23 · Vocabulario LLM en la API sagrada (`enu.text.approx_tokens`) — `api.md` §10 / `providers.md` §5 — **RESUELTO**

**Resolución** (aplicada en [api.md](../contracts/api.md) §10, [providers.md](../contracts/providers.md)
§4/§5 y [agente.md](../contracts/agente.md) §8): la primitiva **sale del core**. Falla
las dos varas a la vez: "token LLM" es vocabulario de producto
([filosofia.md](../core/filosofia.md) §2), y la heurística (~4 bytes/token) es una
división en Lua puro — sin trabajo pesado no hay primitiva que justificar
("Lua decide, Go ejecuta"). A diferencia de markdown/highlighting, cuya
concesión la sostiene el rendimiento, esta no tenía sostén. El helper pasa
a la extensión de providers — dueña del vocabulario de tokens y del
`count_tokens?` exacto — como `providers.approx_tokens(s)`, en Lua.
Renombrar en el core a algo neutro se descartó (cualquier nombre seguiría
existiendo solo para estimar tokens: maquillaje, no resolución); mantenerla
como concesión documentada se descartó (sin coste de rendimiento que la
justifique, sentaría el precedente de que la vara de filosofía §2 es
negociable en la propia superficie sagrada).

**Problema.** `api.md` §10 exponía `enu.text.approx_tokens(s)` documentada
como "estimación heurística de tokens LLM", mientras `providers.md` §5
afirmaba en la misma frase que el conteo de tokens es "nunca del core
(ADR-003: el core no sabe lo que es un LLM)". La vara de filosofía §2 —
vocabulario de producto = extensión — quedaba desautorizada dentro de la
propia API sagrada.

**Impacto.** Filosófico más que funcional, pero sobre la superficie que se
congela: lo que entre con vocabulario de producto no se puede descongelar,
y debilita el argumento del kernel mínimo ante cada caso dudoso futuro.

**Opciones.** (a) Renombrar en el core a vocabulario neutro
(`bytes_estimate` o similar); (b) mantener como concesión documentada,
estilo markdown/highlighting; (c) eliminar del core y mover el helper a la
extensión de providers (una línea de Lua).
