---
title: "Hot-reload de plugins (ciclo de desarrollo)"
type: "hallazgo"
id: "G2"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "enu.plugin.reload(name) recarga plugins best-effort limpiando registros por dueño, como herramienta de desarrollo, no garantía de producción."
affected: ["loader", "api.md §14"]
---
# G2 · Hot-reload de plugins (ciclo de desarrollo) — loader / `api.md` §14 — **RESUELTO**

**Resolución** (aplicada en [api.md](../contracts/api.md) §14 y §4):
`enu.plugin.reload(name)` best-effort — handles etiquetados por dueño,
evento `core:plugin.unload` para que las extensiones limpien sus
registros, caché de require vaciada, init.lua recargado. Herramienta de
desarrollo, no garantía de producción. El reinicio-con-`--continue` se
descartó como historia de DX (pierde estado de UI/plugins); posponer
dolía justo donde se ganan los primeros autores.

**Problema.** Iterar sobre un plugin exige reiniciar enu: `require` cachea,
re-ejecutar `init.lua` duplicaría registros, y aunque todos los registros
devuelven handles, nadie los rastrea por plugin (no existe "deshaz todo lo
de X"). Lo mismo aplica a recargar `providers.toml` / `enu.toml` en
caliente.

**Impacto.** DX de la comunidad de plugins — el público objetivo del
proyecto. No bloquea contratos.

**Opciones.** (a) El core rastrea ownership de handles por plugin (ya sabe
`plugin.current()` en cada registro) y ofrece `enu.plugin.reload(name)`;
(b) sin reload: comando de reinicio rápido de enu que repone la sesión
(`--continue` ya casi lo da); (c) posponer con disparador (P-nuevo).
