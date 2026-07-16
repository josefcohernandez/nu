---
title: "`enu.http`/`enu.http.stream` siguen redirects sin control: no es expresable no-seguirlos ni observar la cadena"
type: "hallazgo"
id: "G54"
status: "resuelto"
date: "2026-07-16"
origin: "auditoría de seguridad 2026-07-16 (SEC-03)"
resolution: "request/stream ganan opts.max_redirects (default 10) y recortan cabeceras en cada salto cross-host, cerrando la amplificación de SSRF."
affected: ["api.md §8"]
---
# G54 · `enu.http`/`enu.http.stream` siguen redirects sin control: no es expresable no-seguirlos ni observar la cadena — `api.md` §8 — **RESUELTO**

**Resolución** (2026-07-16; adición a [api.md](api.md) §8, nivel de API 3→4).
`request` y `stream` ganan `opts.max_redirects?: number`: default **10** (la
política que el cliente aplicaba de forma implícita pasa a contrato), `0` =
no seguir ninguna. Agotado el presupuesto **no se lanza error nuevo**: se
entrega la última respuesta `3xx` **como dato** — coherente con el principio
ya escrito en §8 de que "el status es dato" —, de modo que observar o validar
la cadena es expresable poniendo `0` y siguiendo los saltos a mano (cierra la
amplificación de SSRF: el `302` hacia `169.254.169.254` deja de resolverse
por debajo de la validación que se hizo sobre la URL inicial). Y como default
seguro, en cada salto **cross-host** — cambio de host (nombre y puerto)
respecto de la URL inicial, o degradación de esquema `https`→`http` — el
cliente recorta **todas** las cabeceras que el llamante puso en
`opts.headers` antes de reenviar, sin restaurarlas aunque la cadena regrese
al host inicial: la regla total (sin lista blanca) cubre las credenciales en
cabeceras custom (`x-api-key`, `x-goog-api-key`) que el recorte estándar
entre dominios (`Authorization`/`Cookie`) no conoce. Con el modelo de amenaza
acotado por la verificación adversarial de SEC-03: el eje robusto es la
**amplificación de SSRF** más el open-redirect hacia un tercero honesto — el
robo directo de credencial vía redirect se refutó (quien inyecta el `302` ya
recibió la clave en la petición inicial). Recomendación de uso
(`max_redirects = 0` ante URLs de terceros) añadida a
[guia-plugins.md](guia-plugins.md) §5 y [providers.md](providers.md) §3.
**Implementación pendiente** (sesión de construcción, no este commit, por el
protocolo "el contrato lidera, el código sigue": el kernel aún sigue la
política implícita de Go y `APILevel` sigue en 3 hasta que se construya).
(Origen: SEC-03.)

**Problema.** El cliente HTTP sigue las redirecciones automáticamente y la API v1
no ofrece forma de desactivarlo, limitarlas ni inspeccionar la cadena. Un `302`
hacia `169.254.169.254` (u otro destino interno) evade cualquier validación que
un adaptador hiciera sobre la URL **inicial** —amplificación de SSRF— y un
open-redirect cross-host puede arrastrar cabeceras sensibles al nuevo destino.
Corolario de completitud: hoy la mitigación **no es expresable** componiendo la
API existente. Detectado en SEC-03 (2026-07-16).

**Impacto.** Cualquier adaptador de provider o plugin que acepte URLs de terceros
(o que un LLM las proponga) queda expuesto a SSRF por redirect, sin herramienta
en la API para defenderse.
