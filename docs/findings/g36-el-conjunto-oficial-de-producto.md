---
title: "El conjunto oficial de producto auto-monta dos UIs (chat y repl): salir del chat deja el REPL debajo"
type: "hallazgo"
id: "G36"
status: "resuelto"
date: "2026-06-28"
origin: "pulido de UI/UX de las extensiones oficiales de producto"
resolution: "El repl cede la pantalla al chat cuando ambos están activos, y Chat:quit apaga el runtime en vez de dejarlo debajo."
affected: ["ADR-015", "arquitectura.md §Distribución", "chat.md §8"]
---
# G36 · El conjunto oficial de producto auto-monta dos UIs (chat y repl): salir del chat deja el REPL debajo — ADR-015 / `arquitectura.md` §Distribución / `chat.md` §8 — **RESUELTO**

**Resolución** (aplicada en el `init.lua` de la extensión `repl`, sin tocar la API sagrada; documentada en [arquitectura.md](../core/arquitectura.md) §Distribución y [chat.md](../contracts/chat.md) §8): el repl **cede la pantalla al chat**. Su auto-montaje en `core:ready` pasa a ser condicional: solo monta su UI si el `chat` **no** está entre los plugins activos (lo comprueba con `enu.plugin.list()`, sin `require`ar chat —el repl debe poder activarse SOLO, G21). Con el conjunto oficial activo, abre **solo** el chat; el repl queda como módulo accesible (`require("repl")`, `repl.eval`) pero inerte como UI. Con solo `repl` activo (G21), abre el REPL. En headless, ninguno monta UI. Además, `Chat:quit` (y `ctrl+c`) emiten `core:shutdown`: **cerrar el chat apaga el binario** en vez de devolver al usuario a una capa inferior.

**Problema.** Aflora al *usar* el producto, no en pseudocódigo. ADR-015 fijó el conjunto oficial como "las siete embebidas menos `example`", incluido `repl`, razonando **solo el caso headless** ("chat/repl se auto-gatean con `enu.has("ui")` y quedan inertes sin UI, así que activarlos juntos no estorba"). Pero **con TTY** —la experiencia real del producto— los `init.lua` de chat *y* de repl se suscriben a `core:ready` y **ambos** montan una `toolkit.app` a pantalla completa sobre el mismo compositor. Se solapan; y como el chat no apagaba el runtime al salir, cerrar el chat dejaba el REPL de Lua montado debajo: la sensación, descrita por el usuario, de "salir de la extensión de chat y luego del intérprete de lua". El razonamiento de ADR-015 tenía un hueco: *activarlos en headless* no estorba, pero *activarlos juntos en TTY* sí.

**Impacto.** Es la primera impresión del producto terminado: en vez de una TUI única y pulida, el usuario percibe capas que hay que ir cerrando. Barato de cerrar sobre la Lua de las extensiones (el repl mira el registro del loader ya existente) sin tocar la API sagrada ni el conjunto de ADR-015 (el repl sigue en él, instalado y accesible; solo no compite por la pantalla).

**Por qué el repl cede y no se saca del conjunto.** Sacar `repl` de `officialProductSet` lo desinstalaría del producto (no estaría disponible para activarse suelto desde una sesión con el conjunto oficial). El repl es valioso como herramienta del autor de extensiones (G21); lo que sobra no es su *presencia* sino su *competencia por la pantalla*. Cederla —el patrón "una sola extensión posee la UI primaria"— preserva ADR-015 y resuelve el solape. El chat, la UI del harness, es quien manda cuando está presente.
