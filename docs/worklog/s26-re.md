---
title: "`enu.re` (RE2: compile/match/find_all/replace)"
type: "sesion"
id: "S26"
phase: 5
status: "cerrada"
---
# S26 — `enu.re` (RE2: compile/match/find_all/replace)

`enu.re` implementa la fila de §10 (`enu.re.compile(pattern) -> Re` + el handle
`Re` con `match`/`find_all`/`replace`) sobre el `regexp` de la stdlib de Go,
que **es RE2**. Tres decisiones de diseño propias —la forma de las capturas,
las unidades de los rangos y la sintaxis de `repl`— y una observación sobre la
elección de motor.

## Por qué RE2 (y sin dependencia nueva)

El `regexp` de la stdlib **es** una implementación de RE2: garantiza tiempo
**lineal** sobre el tamaño de la entrada (autómata, sin backtracking) a cambio
de no soportar **backreferences** ni lookaround. Para un harness es justo lo
que se quiere: un patrón venido de un agente o de la configuración NUNCA puede
colgar el runtime con un ReDoS (backtracking catastrófico). El precio se
documenta y se reporta: `compile("(a)\\1")` (backreference) → `EINVAL` con el
mensaje de `regexp.Compile` incrustado (la stdlib lo reporta como secuencia de
escape inválida), no un fallo silencioso. **Cero dependencias nuevas**
(`go.mod`/`go.sum` intactos, ADR-001). `*regexp.Regexp` es **seguro para uso
concurrente** (lo garantiza su doc), así que un mismo `Re` se casa desde varias
tasks sin candado (encaja con el modelo de concurrencia del navegador,
ADR-004). Es **[W] pero NINGUNA ⏸** (CPU puro: compila/casa un string en
memoria, sin IO; como `enu.text` y los codecs de S18): ni `suspend` ni
`requireTask`.

## Decisión: forma de `caps` en `Re:match` — array 1-based + grupos con nombre

`Re:match(s)` devuelve, ante coincidencia, una tabla con DOS vistas a la vez:

  - **Parte array, 1-based** (estilo Lua): `[1]` es la coincidencia COMPLETA
    (el grupo 0), `[2]` el primer grupo, `[3]` el segundo, etc. Así `caps[1]`
    es SIEMPRE el match entero, aunque el patrón no tenga grupos (un patrón sin
    grupos da `caps[1]` y nada más).
  - **Grupos con nombre** (`(?P<name>...)`, sintaxis de nombres de RE2/Go)
    ADEMÁS por su **clave string**: `caps.name`. Conviven con la parte array
    (un grupo con nombre aparece dos veces: por su índice posicional y por su
    nombre), lo que deja a Lua acceder como prefiera.

Alternativas descartadas: (a) solo grupos (sin el match 0 en `[1]`) —rompía el
caso "patrón sin grupos" y obligaba a un campo aparte para el match completo—;
(b) un campo `.groups` separado del `.full` —más verboso sin ganancia—. El
array 1-based con `[1]`=match completo es el convenio más natural en Lua y el
que menos sorprende. Sin coincidencia → `nil` (no lanza: no casar es un
resultado válido, no un error). Un grupo opcional que no participó (p. ej.
`(a)?` sin "a") → `""` (string vacío): `FindStringSubmatch` no distingue
"vacío" de "ausente" en su salida de strings, y un array Lua no admite `nil`
intermedio sin agujerearse.

## Decisión: unidades de `Re:find_all` — rangos de byte, 1-based, inclusivos

`Re:find_all(s)` devuelve TODAS las coincidencias (no solapadas, de izquierda a
derecha) como un array de rangos `{start, end}` con **offsets de BYTE, 1-based,
ambos inclusive** —el MISMO convenio que `string.find` de Lua—, de modo que
`s:sub(start, end)` reconstruye EXACTAMENTE cada coincidencia.

Se eligen **bytes** (no runes/caracteres) por dos razones que se refuerzan: (a)
`string.sub` de Lua indexa por byte, así que el rango es directamente
utilizable (componer con `s:sub` es el caso de uso obvio: localizar/resaltar);
(b) `FindAllStringIndex` de Go ya devuelve offsets de byte —convertir a runes
obligaría a recontar y rompería esa composición—. La conversión de convenios:
Go da `[inicio, fin)` 0-based con fin **exclusivo**; a Lua, `start = inicio+1`
(1-based) y `end = fin` (un fin exclusivo 0-based coincide numéricamente con el
último byte 1-based inclusive). Una coincidencia **vacía** (p. ej. `x*` sobre
"ab" casa el vacío en cada posición) da `end = start-1` (longitud cero),
coherente con que `s:sub(start, start-1)` es `""` en Lua.

Se devuelven **solo los rangos de la coincidencia completa**, no los de cada
grupo: es el caso común (resaltar/localizar dónde casa el patrón) y mantiene la
firma simple. Quien necesite las capturas de cada coincidencia las saca con
`match` sobre el tramo; si el patrón "rangos por grupo" se repitiera, sería una
adición futura (no se especula API; la superficie de §10 es sagrada).

## Decisión: sintaxis de `repl` en `Re:replace` — la de Go

`Re:replace(s, repl)` sustituye TODAS las coincidencias no solapadas y delega
en `Regexp.ReplaceAllString`, así que `repl` usa la **sintaxis de Go**: `$1`,
`$2`, ... refieren grupos por número; `${name}` por nombre; `$0` (o `${0}`) la
coincidencia completa; `$$` es un `$` literal. Un nombre no delimitado por
llaves se extiende hasta el último carácter alfanumérico (`$1x` busca el grupo
"1x", no el grupo 1 seguido de "x"): se recomienda `${1}x` —el mismo matiz que
documenta la stdlib—. Una referencia a un grupo inexistente se reemplaza por
vacío. No se inventa una sintaxis propia (`\1` u otra): reusar la de la
librería es menos superficie, menos sorpresas y documentación gratis. Sin
coincidencias → `s` intacto.

## Tests

Vía el arnés Lua (`re_test.go`), porque la forma de la tabla de capturas y de
los rangos solo es observable desde Lua: `TestReMatchPositional` (`(\d+)-(\d+)`
sobre "12-34" → `[1]`/`[2]`/`[3]`), `TestReMatchNamed` (por índice Y por
nombre), `TestReMatchNoGroups`, `TestReMatchNoMatch` (→nil), `TestReMatchEmpty
String` (`\d+` no casa el vacío, `\d*` sí con match vacío), `TestReMatchOptional
Group` (grupo ausente→""), `TestReFindAllRanges` (`s:sub` reconstruye cada match
+ offsets concretos), `TestReFindAllNone`, `TestReFindAllUTF8` (offsets de BYTE
coherentes con `string.sub` sobre texto multibyte), `TestReFindAllEmptyMatch`
(`end=start-1`), `TestReReplaceNumbered`/`Named`/`NoMatch`/`All`,
`TestReCompileBackreference` (`(a)\1`→`EINVAL` con mensaje, criterio de hecho),
`TestReCompileInvalidSyntax` (`(abc`→`EINVAL`), `TestReFromTask` (uso desde una
task; resultado expuesto por global tras `waitIdle`), `TestReTypeMismatch`
(self no-`Re`→`EINVAL`).

`CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios; `CGO_ENABLED=1 go test
-race -timeout 120s -count=2 ./internal/...` verde, sin flaky (no regresiona
S01–S25). Binario `enu -e` confirma e2e: match con grupos (12-34/12/34), grupo
nombrado, no-match→nil, find_all que reconstruye por `s:sub`, replace
`${b}-${a}`, backreference→`EINVAL`.

**Sin hallazgos:** §10 bastó (`compile`/`match`/`find_all`/`replace` se
implementaron tal cual). Puntero ▶ avanza a **S27** (`enu.search.files`).
