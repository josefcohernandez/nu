# S36 — extensión oficial `providers` (registro TOML + contrato del adaptador + `approx_tokens`) (providers.md)

Primera extensión de la **Fase 8**: ya no se toca el kernel, se escribe **Lua sobre la
API pública congelada** (ADR-003, sin privilegio de kernel; el core no sabe lo que es un
provider). El contrato es [providers.md](providers.md), no api.md.

## Estructura de la extensión (plugin embebido)

Plugin embebido bajo `internal/runtime/embedded/providers/`, materializado y cargado por
el loader como cualquier plugin (mecanismo de S12, ADR-010: INACTIVO por defecto):

- `plugin.toml` — `name = "providers"`, `version = "0.1.0"`. Sin `requires` (no depende de
  otro plugin; depende de primitivas del core, no de extensiones).
- `init.lua` — solo CABLEA: `require("providers")` y registra los adaptadores oficiales.
  En S36 solo el `stub`; el `anthropic` real (S37) se añadirá aquí igual:
  `providers.register_adapter("anthropic", ...)`.
- `lua/providers/init.lua` — el módulo público (`require("providers")`): lector del
  registro, `resolve`/`list`/`register_adapter`/`approx_tokens`/`reload`.
- `lua/providers/adapter_stub.lua` — adaptador STUB que materializa y prueba el contrato §3
  contra una petición simulada (sin red).

**Por qué módulo `require`-able y no namespace `enu.providers`:** el core reserva el
namespace `enu`; las extensiones exponen su API por `require(<nombre-plugin>)` (api.md §14,
convención namespace = nombre del plugin). El agente y la UI consumirán
`require("providers")`. El namespace de eventos de la extensión sería `providers:` (no se
emiten eventos en S36, pero queda reservado por convención).

## Decisiones de interpretación de providers.md

1. **`providers.toml` se lee perezosamente y por eso `resolve`/`list` SUSPENDEN (⏸).**
   providers.md §1 dice "vive en `enu.config.dir()`" pero no fija *cuándo* se lee. Se lee con
   `enu.fs.read` (⏸, api.md §5) la primera vez que alguien resuelve o lista, y se cachea;
   `reload()` invalida la caché. Consecuencia heredada de la API: como `enu.fs.read`
   suspende, `resolve`/`list` solo corren dentro de una task — que es exactamente el
   contexto del loop del agente. Es coherente con el resto de la API (IO = ⏸) y no requiere
   primitiva nueva.

2. **`providers.toml` ausente = registro vacío, no error.** Un enu recién instalado sin
   modelos configurados debe arrancar limpio; `list()` devuelve `[]`. Se distingue `ENOENT`
   (ausente → vacío) de un fallo de IO real (`EACCES`/`EIO`, se propaga) por el `code` del
   error estructurado de `enu.fs.read`.

3. **Errores del registro = `EPROVIDER` accionable** (providers.md §3 acuña `EPROVIDER`;
   CLAUDE.md/api.md §1.4: las extensiones acuñan los suyos con la misma forma). TOML mal
   formado, provider sin `adapter`, modelo sin `id`, modelo/ref inexistente, adaptador no
   registrado → `EPROVIDER` con `detail` y mensaje que nombra el provider/ref. La validación
   de *argumentos* del propio API (`approx_tokens(123)`, `resolve("")`) usa `EINVAL` (es un
   error del llamante, no del provider).

4. **`api_key` SIEMPRE del entorno** (providers.md §1: "nunca la clave en el fichero"). Se
   lee con `enu.sys.env(prov.api_key_env)`. Si el provider no declara `api_key_env` (p. ej.
   Ollama local), la `config` va sin `api_key` y no es error: el adaptador decide si la
   necesita.

5. **Resolución de adaptador por nombre con `require`** (providers.md §1/§4: el TOML puede
   declarar `adapter = "mi-plugin/corp-gateway"`). `get_adapter` mira primero el registro
   vivo (oficiales + `register_adapter`) y, si no está, intenta `require(name)` (resoluble
   contra las rutas `lua/` de los plugins, api.md §14) y valida su forma. Cachea el
   resultado. `register_adapter` con un nombre ya registrado SUSTITUYE (un plugin puede
   pisar un adaptador oficial a propósito); no es error.

6. **`approx_tokens` cuenta BYTES, `ceil(#s/4)`** (providers.md §4, G23). `#s` en Lua es
   longitud en bytes, que es lo que mejor aproxima la tokenización BPE sobre texto mixto y
   lo que hacía el core. `ceil` (aritmética entera `floor((n+3)/4)`) para no infraestimar;
   cadena vacía = 0. Es heurística, no exactitud — para eso está el `count_tokens?` del
   adaptador.

7. **El STUB declara `caps.tools = false` a propósito** para poder ejercitar la
   "degradación declarada" (§3 obligación 5: request con tools + adaptador sin soporte →
   `EINVAL`, no simulación silenciosa). Su `stream` devuelve un iterador Lua (función que da
   un `Event` por llamada y `nil` al agotarse) — el mismo protocolo que `Stream:events()`
   (api.md §8) que el adaptador real de S37 envolverá. Emite `text`,`text`,`usage`,`done`,
   con el `done` cargando el `Message` canónico ensamblado (§2.3): el agente no re-ensambla
   deltas.

## Hallazgo (corolario de completitud) — RESUELTO sin tocar api.md

`enu.toml.decode` **estringaba los array-de-tablas** (`[[providers.x.models]]`), el formato
CENTRAL de `providers.toml`. Causa: BurntSushi/toml, al decodificar un array-de-tablas en
`map[string]interface{}`, entrega el tipo concreto `[]map[string]interface{}` (no el
`[]interface{}` "abierto"); el puente `goToLua` (codecs.go, S18) solo contemplaba
`[]interface{}` y `map[string]interface{}` y caía al `default` de stringificación
(`models` salía como el string `"[map[id:big-1]]"`). Sin esto la extensión era
**inconstruible** sobre la API pública (filosofía §2).

**No es un hueco de api.md**: la firma documentada `enu.toml.decode -> v` ya prometía
convertir el documento a una tabla Lua —incluidos sus array-de-tablas—; era un bug de la
*implementación* del codec, no de la espec. El arreglo es por tanto MÍNIMO y en el codec,
no en api.md (que queda INTACTO): un fallback por reflexión en `goToLua` para slices y maps
de cualquier tipo concreto (cubre también `[]string`, `map[string]string`, etc., robusto
ante cualquier librería), con claves ordenadas para determinismo. Blindado por
`TestTOMLDecodeArrayDeTablas` (codecs_test.go) nombrando el caso. Es la única línea de Go
del kernel tocada, justificada por "lo mínimo imprescindible para que la extensión
funcione".

## Resultado

`CGO_ENABLED=0 go build ./...` y `go vet ./...` verdes; `gofmt -l` limpio;
`CGO_ENABLED=1 go test -race -timeout 120s -count=2 ./internal/...` verde. Tests en
`providers_test.go` (registro TOML, stub contra petición simulada, approx_tokens) +
`TestTOMLDecodeArrayDeTablas`. No regresiona S01–S35. Puntero ▶ avanza a **S37** (adaptador
`anthropic` real, depende de S20 y S36).

---
