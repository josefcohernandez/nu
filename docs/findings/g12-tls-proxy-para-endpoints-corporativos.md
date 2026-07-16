---
title: "TLS/proxy para endpoints corporativos"
type: "hallazgo"
id: "G12"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "opts.tls (ca_file/insecure) en request/stream y respeto por defecto de las variables HTTP_PROXY/HTTPS_PROXY/NO_PROXY."
affected: ["api.md §8"]
---
# G12 · TLS/proxy para endpoints corporativos — `api.md` §8 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §8): `opts.tls = { ca_file?,
insecure? }` en `request`/`stream`; las variables de entorno
`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` se respetan por defecto (el estándar
de facto corporativo); defaults globales en `[net]` de `enu.toml`
sobreescribibles por petición.

**Problema.** El "proxy corporativo" es caso anunciado en la filosofía,
pero `enu.http` no tiene opciones TLS (CA propia, insecure) ni política de
proxy (¿se respeta `HTTPS_PROXY`?). El caso no se puede configurar.

**Impacto.** Adopción en empresas — público natural de un binario sin
dependencias.

**Opciones.** (a) `opts.tls = { ca_file?, insecure? }` + respetar
`HTTP(S)_PROXY`/`NO_PROXY` por defecto (documentado); (b) además,
configuración global en `enu.toml` para no repetirlo por petición.
