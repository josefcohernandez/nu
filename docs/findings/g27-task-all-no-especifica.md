---
title: "`enu.task.all` no especifica el orden de los resultados"
type: "hallazgo"
id: "G27"
status: "resuelto"
origin: "ronda 5 de pseudocódigo (orquestación de agentes por un tercero)"
resolution: "enu.task.all devuelve resultados alineados con los inputs (semántica Promise.all), independientemente del orden de terminación."
affected: ["api.md §3"]
---
# G27 · `enu.task.all` no especifica el orden de los resultados — `api.md` §3 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §3): `enu.task.all` devuelve los
resultados **alineados con los inputs** (`out[i]` es el de `fns[i]`),
independiente del orden de terminación — semántica `Promise.all`. No es API
nueva: fija la semántica de orden de un primitivo que ya existía. Pasa la
vara de filosofía §4 que descarta las alternativas: *allSettled* (envolver
cada rama en `pcall`) y el límite de concurrencia (semáforo de
`enu.task.future`) un plugin los compone en Lua, así que se quedan en
userland; el orden de un primitivo del core **no** se puede fijar desde
fuera, luego es su contrato. Orden-de-terminación descartado: rompe la
correlación resultado↔entrada y obliga a cada llamante a re-etiquetar, justo
la fricción que «compone mejor a través de capas» (§1.4) quiere evitar;
alinear es además gratis (escribir en el slot indexado al resolver, sin
quitar paralelismo). Una nueva función `enu.task.all_settled`/`map_limit` se
descartó: sería superficie sagrada ad hoc para lo que Lua ya hace
(filosofía §3/§6).

**Problema.** La firma `(fns) -> any[]` dice "espera a todas" pero no que
`out[i]` corresponda a `fns[i]` — las tasks terminan en cualquier orden.
Para una orquestación paralela determinista (un fan-out de subagentes sobre
territorios) es justo lo que hace falta garantizado: sin alineación
posicional no se puede correlacionar resultado con territorio salvo metiendo
el índice dentro de cada payload a mano. Misma clase de indefinición que
cazaban las rondas 3-4 (cf. G8, G10): comportamiento que variaría según el
scheduler dentro de la API sagrada.

**Impacto.** Cualquier consumidor de `task.all` con más de un resultado;
bloquea la orquestación paralela determinista de la ronda 5. Barato ahora,
imposible de cambiar tras congelar.

**Opciones.** (a) Especificar semántica `Promise.all` (orden de inputs,
no de terminación); (b) dejarlo en orden de terminación y que el llamante
acarree el índice (fricción en cada uso, contra §1.4); (c) añadir variantes
nuevas (`all_settled`, `map_limit`) — superficie ad hoc para lo que Lua ya
compone.
