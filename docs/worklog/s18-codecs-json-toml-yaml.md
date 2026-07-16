---
title: "codecs `enu.json` / `enu.toml` / `enu.yaml` (api.md §12)"
type: "sesion"
id: "S18"
phase: 3
status: "cerrada"
---
# S18 — codecs `enu.json` / `enu.toml` / `enu.yaml` (api.md §12)

Tres pares `encode`/`decode` (`codecs.go`), **ninguno ⏸** (CPU puro: parsean o
serializan un string ya en memoria, no hay IO que esperar) y todos **[W]** (§16;
hoy en el estado principal, los workers son S34). "Lua decide, Go ejecuta"
(ADR-004): el parseo/serialización es Go (stdlib `encoding/json`, BurntSushi/toml
—la misma de S11—, `gopkg.in/yaml.v3`), en particular YAML, "demasiado
traicionero para Lua puro" (§12). **APILevel sigue en 1** (§12 ya estaba en
api.md; se implementa, no se amplía la superficie sagrada).

## El mapeo Lua↔Go (compartido por los tres formatos)

El puente es un valor intermedio Go (`interface{}` con
`map[string]interface{}`/`[]interface{}`/`float64`/`string`/`bool`/`nil`) que las
tres librerías saben serializar (`luaToGo`/`goToLua`):

- **`nil` (Lua) → null.** En `decode`, un null → `nil` PERDERÍA la clave en una
  tabla Lua (`t.k = nil` la borra), así que JSON usa el **sentinel `NULL`** (ver
  abajo). TOML/YAML mapean nil a `nil` Lua (`useNull=false`): null no se da en su
  forma típica de config, y quien necesite el round-trip de null usa JSON.
- **boolean → bool; number → float64.** Lua no distingue int de float; el lado Go
  emite entero si no hay parte fraccionaria. JSON `decode` usa `UseNumber` para no
  degradar enteros grandes a notación científica en el round-trip.
- **string → string, con UTF-8 ESTRICTO (G11):** ver abajo.
- **table → array vs objeto:** una tabla cuyas claves son **exactamente 1..n
  contiguas** (la convención de secuencia de Lua) → **array**; cualquier otra →
  **objeto** (claves a string, vía `luaKeyToString`: número 1.0 → "1"). La
  detección cuenta la longitud de la secuencia y el total de claves; solo es
  array si coinciden y hay al menos una. Claves no escalares (tabla, función) →
  `EINVAL`.

**Tabla vacía → objeto (`{}`)** (la decisión ambigua de §12). Una tabla vacía
podría ser `[]` o `{}`; se elige `{}` porque la inmensa mayoría de las
tablas-config de este proyecto son mapas y una lista vacía es el caso raro.
Documentado aquí y en la cabecera de `codecs.go`. Quien necesite `[]` exacto lo
trata como dato (un array no vacío sí se detecta sin ambigüedad).

## UTF-8 estricto (G11) — la mitad 🔒 de S18

`encoding/json` **reemplaza** los bytes UTF-8 inválidos por U+FFFD en silencio.
El contrato (§12) exige lo contrario: `encode` **lanza `EINVAL`** ante bytes
inválidos —sanear es una decisión visible de quien tiene el contexto (la tool),
nunca del codec—. Se detecta con `utf8.ValidString` en `luaToGo` (valores) **y en
las claves de objeto** (un string-clave inválido rompe el documento igual). Vale
para los tres formatos al codificar. También se rechaza un número no finito
(NaN/Inf), sin representación en JSON/TOML/YAML.

## Sentinel `enu.json.NULL` — la otra mitad 🔒

Un **userdata único** por Runtime (`rt.jsonNull`, creado una vez en
`registerCodecs`), reconocido por **identidad**. `decode` entrega el sentinel en
lugar de `null` (NO `nil`, que al asignarse a una tabla borra la clave: una ida y
vuelta perdería claves con valor null); `encode` lo reconoce y emite `null`. Es
el patrón canónico de "null que sobrevive el round-trip". El test contrasta
explícitamente con `{ a = nil, b = 1 }`, que SÍ pierde la clave `a` — justo lo que
el sentinel evita.

## Detalles de serialización

- **JSON `SetEscapeHTML(false):`** por defecto `encoding/json` escapa `<`/`>`/`&`
  (defensa para incrustar en HTML); en un codec de propósito general eso
  sorprende (un round-trip cambiaría el texto), así que se desactiva —quien
  incruste en HTML escapa él, coherente con que sanear es del consumidor (§12)—.
  Se recorta el `\n` final que `json.Encoder.Encode` añade.
- **`opts.pretty`** → `SetIndent("", "  ")` (dos espacios).
- **TOML raíz:** un documento TOML es un mapa; `encode` exige que la raíz sea un
  objeto (array/escalar → `EINVAL` accionable).
- **Errores de parseo** (`decode` de JSON/TOML/YAML inválido) → `EINVAL` con el
  texto de la librería (BurntSushi y yaml.v3 incluyen línea/columna).

## Deps añadidas

`gopkg.in/yaml.v3` v3.0.1 (puro-Go; `go get` + `go mod tidy`, hubo red). No toca
`CGO_ENABLED=0`. BurntSushi/toml ya estaba (S11); `encoding/json` es stdlib.

## CP-4 — adaptación `search.files` → `fs.list` (cierra Fase 3)

El texto de CP-4 ("una herramienta de verdad, solo con primitivas") menciona
`enu.search.files` para recorrer el repo, pero **esa primitiva es S27 (Fase 5) y
aún no existe**. Se sustituye por un **recorrido recursivo en Lua sobre
`enu.fs.list`** (disponible desde S14): enumerar el directorio + recurrir por los
subdirectorios (saltando `.git`, como haría el filtrado gitignore de
`search.files`). Es la sustitución más fiel —el mismo trabajo (enumerar el árbol)
con la primitiva que SÍ existe en la Fase 3—; `search.files` (recursión +
filtrado en Go) llega en S27/CP-6. El test (`cp4_test.go`) monta un repo git
temporal (un fichero comiteado + uno sin trackear + un subdir), recorre con
`fs.list` recursivo, lee con `fs.read`, lanza `git status --porcelain` con
`proc.run` (`opts.cwd`, sin shell), y emite un resumen con `json.encode` que luego
re-parsea con `json.decode` para validarlo (cierra el círculo del codec). **Sin
red ni UI, solo primitivas del core** → ejercita el corolario de completitud
(filosofía §2): no hizo falta ninguna primitiva nueva, así que **sin hallazgo
G##**. Si `git` no está, el test se salta (lo necesita para `git status`).

## Tests 🔒 (`codecs_test.go`, nombran G11)

UTF-8 estricto G11 (byte 0xff suelto, anidado, y como clave de objeto → `EINVAL`;
ASCII+multibyte+emoji round-trip exacto); sentinel NULL ida y vuelta (clave `a`
presente como `enu.json.NULL` distinta de nil, iterada con `pairs`; `encode` →
`"a":null`; round-trip; contraste con `nil` que pierde la clave); array vs objeto
y tabla vacía → `{}`; `pretty` indenta y es JSON válido; `decode` inválido →
`EINVAL`; `toml.decode` de un `plugin.toml` real (name/version/requires) +
round-trip + raíz no-objeto → `EINVAL`; YAML frontmatter de skill (claves, listas,
strings) + round-trip; codecs desde una task ([W]); no-serializable (función,
NaN, Inf) → `EINVAL`. `CGO_ENABLED=0 go build`/`go vet`/`gofmt -l` limpios;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde. Binario
`enu -e` confirma de extremo a extremo encode/decode de los tres formatos, el
round-trip del sentinel NULL, `pretty` y el UTF-8 estricto (G11 → `EINVAL`).

**Sin hallazgos:** §12 bastó tal cual. **CP-4 verde → Fase 3 cerrada.**
