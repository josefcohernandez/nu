---
title: "`api.md` §1.5 promete `opts.timeout_ms` universal y no define el valor 0, que hoy diverge entre módulos"
type: "hallazgo"
id: "G47"
status: "resuelto"
date: "2026-07-12"
origin: "auditoría integral 2026-07-12"
resolution: "La promesa de opts.timeout_ms se acota a las firmas que ya lo listan, definiendo el valor 0 por módulo con su porqué."
affected: ["api.md §1.5/§5/§6/§8"]
---
# G47 · `api.md` §1.5 promete `opts.timeout_ms` universal y no define el valor 0, que hoy diverge entre módulos — `api.md` §1.5/§5/§6/§8 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §1.5; opción (a)). La promesa se **acota a las firmas que lo listan** — `enu.proc.run`, `enu.http.request`, `enu.http.stream`, `enu.ws.connect` —, que es lo que el código implementa y las propias firmas de §5-§8 siempre dijeron: la frase universal de §1.5 era la anomalía, no el código. Y el valor frontera queda definido donde existe: en `proc.run`, `0` (el default) significa *sin límite* (un proceso local puede legítimamente no tener techo); en `http`/`ws` el plazo existe siempre (default 30 000 ms) y `0` es `EINVAL` — una petición de red sin techo no es un caso soportado—. La divergencia deja de ser silenciosa: es semántica documentada con su porqué. Añadir `timeout_ms` a más firmas (p. ej. `enu.fs.*` sobre montajes de red) queda como **adición futura** compatible (la API crece solo por adición); no se promete hasta que exista.

**Problema.** §1.5 afirmaba taxativamente "Toda función con IO acepta `opts.timeout_ms` (lanza `ETIMEOUT`)", pero casi ninguna primitiva de IO lo honra ni lo lista en su firma (`enu.fs.read(path)` no tiene ni tabla de opts; `Proc:read/write/wait`, `Ws:send/recv`, `enu.search.*` tampoco). Además el valor `0` divergía sin documentar: `proc.run` lo acepta como "sin límite" mientras `http.request`/`ws.connect` lanzan `EINVAL`. Detectado en la auditoría integral (A-24/A-30 del informe), verificado contra código y firmas.

**Impacto.** Un autor de plugin que leyera §1.5 esperaría `ETIMEOUT` de un `enu.fs.read` sobre un NFS colgado (bloquea para siempre) y portabilidad del `{timeout_ms=0}` entre módulos (explota o no según el módulo).

**Opciones.** (a) Acotar §1.5 a las firmas reales + definir el 0 por módulo con su porqué (elegida). (b) Añadir `opts.timeout_ms` a todas las firmas de IO — cirugía grande de espec y kernel, sin demanda real aún. (c) Unificar el 0 (EINVAL en todas, o sin-límite en todas) — rompe `proc.run` o abre peticiones de red sin techo.
