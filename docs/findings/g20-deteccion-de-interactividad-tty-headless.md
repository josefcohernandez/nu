---
title: "Detección de interactividad (TTY/headless)"
type: "hallazgo"
id: "G20"
status: "resuelto"
origin: "revisión de coherencia de la documentación completa"
resolution: "En headless el módulo enu.ui directamente no existe; el test de interactividad pasa a ser enu.has(\"ui\")."
affected: ["api.md", "agente.md §5", "chat.md §8"]
---
# G20 · Detección de interactividad (TTY/headless) — `api.md` / `agente.md` §5 / `chat.md` §8 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §2/§9, [agente.md](agente.md)
§5 y [chat.md](chat.md) §8): en headless el módulo `enu.ui` directamente
**no existe**; el test es `enu.has("ui")` — coherente con el
deny-by-default de las `caps` de workers (la superficie no concedida no
está) y sin primitiva nueva. `enu.ui.interactive()` se descartó (un módulo
de UI presente pero "apagado" invita a llamadas que no pintan nada);
exponer el modo de arranque en `enu.sys` se descartó como redundante con
lo anterior.

**Problema.** El default-deny de permisos en headless y "chat solo se
activa en TTY interactivo" dependen de saber si hay terminal; ninguna
primitiva lo dice (el pseudocódigo del turno usa un `interactive()` que
no existe).

**Impacto.** El pipeline de permisos — una decisión de seguridad — apoya
su rama principal en una función sin especificar.

**Opciones.** (a) `enu.ui.interactive() -> boolean` (o un cap:
`enu.has("ui.tty")`); (b) en headless el módulo `enu.ui` directamente no
existe y el test es `enu.has("ui")` — coherente con caps de workers
(deny-by-default de superficie); (c) exponer el modo de arranque en
`enu.sys` (`enu -e` = headless por definición).
