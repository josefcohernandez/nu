---
title: "Providers por suscripción (OAuth)"
type: "hallazgo"
id: "G13"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "El device flow OAuth es el camino v1 sin listener, con tokens guardados en 0600 bajo data_dir()/plugins/<nombre>/."
affected: ["providers.md", "api.md"]
---
# G13 · Providers por suscripción (OAuth) — `providers.md` / `api.md` — **RESUELTO**

**Resolución** (aplicada en [providers.md](providers.md) §4 y guía §7):
camino v1 sin listener — device flow o pegado manual de código (patrón
`gh`/`gcloud`), escribible con `http.request` + `enu.proc`; tokens en
`data_dir()/plugins/<nombre>/` con `0600`, en claro (coherente con P7). El
listener localhost (`listen_once`) va a [P19](pospuesto.md) con disparador
"provider real sin device flow ni pegado de código".

**Problema.** El device flow es escribible con lo que hay (polling +
abrir URL), pero el flujo con callback localhost no: no existe primitiva
de listener HTTP. Y no hay convención de dónde/cómo guarda un adaptador
sus refresh tokens.

**Impacto.** Los planes de suscripción (no API key) son cada vez más
comunes; decide si enu los soporta de primera.

**Opciones.** (a) Bendecir device flow como el camino v1 + convención de
almacenamiento de tokens (`plugins/<nombre>/`, `0600`) y nada de
listener; (b) añadir un listener HTTP mínimo (`enu.http.listen_once` para
callbacks de OAuth, efímero, solo loopback) — superficie pequeña y
acotada; (c) posponer OAuth entero con disparador.
