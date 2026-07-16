---
title: "Reentrada del bus de eventos"
type: "hallazgo"
id: "G10"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "El bus despacha sobre una foto de suscriptores, con cancelación inmediata y emits anidados encolados en anchura, no en profundidad."
affected: ["api.md §4"]
---
# G10 · Reentrada del bus de eventos — `api.md` §4 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §4): despacho sobre snapshot
de suscriptores; cancelación con efecto inmediato; suscritos durante el
despacho solo ven eventos futuros; emits anidados encolados (anchura, no
profundidad — el ping-pong infinito se vuelve bucle plano que corta el
watchdog). Recursión en profundidad descartada (desborde de pila + orden
sorpresa); `defer` obligatorio descartado (la UI iría un tick por detrás).

**Problema.** `emit` dentro de un handler (¿recursión o cola?), suscribir
o cancelar durante el despacho (¿el handler nuevo ve el evento en curso?
¿el cancelado a mitad se ejecuta?): todo indefinido. Produce bugs
dependientes del orden de carga de plugins.

**Impacto.** Núcleo del modelo de extensión; barato de definir, imposible
de cambiar después.

**Opciones.** (a) Despacho sobre snapshot de la lista de handlers + emits
anidados encolados al final del despacho en curso (sin recursión); (b)
despacho recursivo en profundidad con límite anti-ciclos; (c) emits
anidados via `task.defer` obligatorio (más simple en el core, más
sorpresa para el autor).
