---
title: "Censo de la frontera VM (M01)"
type: "archivo"
status: "ejecutado"
---
# Censo de la frontera VM (M01)

Producido por [M01](migracion-vm.md#fase-a--cimientos-la-interfaz-de-vm-de-adr-019-fase-a).
La parte mecánica la regenera `tools/censo-vm.sh`; este documento es la
**clasificación** y la **estrategia wasm por categoría** — el mapa del que
cuelgan M05-M13. Cifras de referencia: 2026-07-03, backend gopher-lua sobre
`main`.

## Cómo leer esto

`tools/censo-vm.sh` tiene tres modos: resumen (símbolo × frecuencia),
`--files` (densidad por fichero) y `--check` (guardia de CI: falla si el kernel
gana un símbolo de gopher-lua **fuera de la allowlist** — así, durante la
migración, ningún fichero introduce acoplamiento nuevo sin que se note). La
allowlist del script ES la lista de símbolos censados aquí.

## Las 6 categorías de la frontera

Todo símbolo de gopher-lua que el kernel toca cae en una de seis categorías.
Cada una tiene una **estrategia de traducción** al backend wasm y la **sesión**
donde se paga.

### C1 · Valores Lua (el marshaling) — M05

`LValue`, `LNil`/`LNilType`, `LString`, `LNumber`, `LBool`/`LTrue`/`LFalse`,
`LTable`, `LVAsBool`. Son el ~55% del censo (LNil 200, LString 114, LValue 112,
LTable 71, LNumber 64...). Hoy el kernel construye y lee estos valores
**directamente** porque comparte proceso con la VM.

- **Estrategia**: en wasm los valores viven al otro lado de la frontera. El
  kernel deja de manipular `LValue` y pasa a un **marshaling por copia**: los
  argumentos de una host function llegan como una región de memoria wasm que se
  decodifica a un `any` de Go (o a un tipo específico), y los retornos se
  codifican de vuelta. El formato de intercambio es el que api.md ya bendice
  para los workers (§13) y las sesiones (JSONL): **valores JSON-ables**, con el
  añadido de `nu.json.NULL` (el sentinel de G11) y strings **sin re-codificar**
  (UTF-8 estricto, G11: los bytes cruzan tal cual, sin pasar por `utf8.Valid`
  que rompería binarios legítimos en tránsito controlado).
- **Riesgo**: el coste de copia en el camino caliente (INFORME §4). Mitigado
  por el diseño de nu (primitivas gruesas) y medido en el veto 2 (M15).
- **Lo que NO cruza como copia**: userdata (→ C5) y funciones Lua (→ C6).

### C2 · Registro de primitivas (host functions) — M05

`NewFunction`, `LGFunction`, `NewTable`, `SetGlobal`, `RawSetString`,
`SetField`, `RawSet`. El patrón `rt.registerXxx(nu)` de nu.go: cada primitiva
es un `L.NewFunction(handler)` colgado de una tabla.

- **Estrategia**: el equivalente wasm de `registerNu`. Cada primitiva se
  registra como una **host function de wazero** (`NewFunctionBuilder`), y el
  lado Lua (dentro del wasm) recibe una tabla `nu` cuyas entradas son thunks
  Lua que llaman a la host function correspondiente por índice. La tabla `nu` y
  su forma (submódulos) se ensamblan con un pequeño preludio Lua ejecutado al
  arrancar el estado — no con `NewTable` de Go. `registerXxx` pasa de "construir
  una LTable" a "declarar un conjunto de host functions + su preludio".
- **Firma canónica de un handler**: hoy `func(L *lua.LState) int`; en wasm,
  `func(ctx, args []any) ([]any, error)` sobre la infra de C1. La conversión es
  mecánica y es el grueso de M09.

### C3 · Threads y corrutinas (el puente ⏸) — M06 (ADR-020)

`NewThread`, `Resume`, `Status`, y todo el mecanismo de `scheduler.go`:
`CallByParam`, el token (`gil`), `coToTask`, `suspend`/`deliverFn`. **La
categoría más profunda** — es ADR-011 entero.

- **Estrategia**: **el cambio arquitectónico de la migración.** Hoy una task es
  una goroutine + un `LState` hijo que NUNCA cede (el token se libera, el
  trabajo va a una goroutine de fondo, se re-adquiere el token — "sin yields",
  porque gopher-lua no deja ceder a través de `pcall`, G31). En wasm, con el
  Lua de referencia, una task **es una corrutina Lua nativa**: ⏸ = `lua_yield`
  con una petición de trabajo; el loop Go la resume con el resultado. Esto se
  diseña en **ADR-020** (que reemplaza a ADR-011) ANTES de codificar.
- **Lo que se conserva**: la semántica observable de api.md §1.3 — await
  implícito, `nu.task.all` alineado (G27), race, future, cleanup LIFO. Los
  tests `scheduler_test`/`allrace`/`future`/`timers` son el contrato.
- **La incógnita medida**: el coste de `Snapshot`/`Restore` (INFORME §4.1). El
  ADR-020 elige la técnica y el veto 2 la audita.

### C4 · Errores y desenrollado — M03 (mecanismo) + M07 (semántica)

`ApiError`, `Error`, `RaiseError`, `PCall`, `Upvalue`, `P` (Protect), y el
`installCancelPcall`/trampolín de cancel.go. Hoy el kernel lucha con dos
peculiaridades de gopher-lua: el aborto no-capturable a través de `pcall`
(cancel.go, el wrapper de `pcall`/`xpcall`) y el bug de upvalues de G41 (el
blindaje con `unsafe`).

- **Estrategia**: en wasm el desenrollado es el **trampolín Snapshot/Restore**
  (M03, ya prototipado en el spike): `LUAI_THROW`/`LUAI_TRY` redefinidos, sin
  setjmp. Y la gran limpieza: **G41 no existe en PUC** (los tests `TestG41*`
  pasan sin blindaje) y el aborto no-capturable se realiza con el throw nativo
  + un marcador propio, **sin envolver `pcall`** (diseño en ADR-020). Es decir:
  esta categoría no se "traduce", se **simplifica** — M17 borra cancel.go casi
  entero.
- `Upvalue`/`NewClosure` sólo aparecían por el blindaje de G41: mueren con él.

### C5 · Userdata (los handles opacos) — M10

`LUserData`, `NewUserData`, `NewTypeMetatable`, `GetTypeMetatable`,
`CheckUserData`, `SetMetatable`. Task, Proc, Stream, Ws, Watcher, Timer,
Future, Region, Block: los handles que api.md §1.5 declara "userdata opacos con
métodos".

- **Estrategia**: como los valores no cruzan por referencia (C1), un handle es
  un **entero opaco** en el lado Lua (índice en una tabla Go de objetos vivos,
  como el `handles.go` que ya existe para el registro por dueño). Los métodos
  (`Proc:wait`, `Region:blit`...) son host functions que reciben el índice +
  args y despachan sobre el objeto Go. El metatable con `__index` se arma en el
  preludio Lua. Un índice inválido/liberado → error estructurado accionable
  (nuevo 🔒). El ciclo de vida (cleanup/GC, "quien crea mata") se conserva:
  `handles.go` ya modela el registro por dueño.
- Los **Blocks** ya son opacos y viven en Go: no cambian de naturaleza, sólo su
  representación en el lado Lua (de userdata a índice).

### C6 · Libs estándar y baseline — M04 (sandbox) + M14 (5.4)

`OpenBase`/`OpenTable`/`OpenString`/`OpenMath`/`OpenCoroutine`/`OpenOs`,
`*LibName`, `OpenPackage` (que NO se abre), `DoString`/`LoadString`/`LoadFile`,
`ToStringMeta`, `GetMetaField`, `Options`, `NewState`, `Close`.

- **Estrategia**: el sandbox (sandbox.go) se reimplementa sobre el estado wasm
  — pero **las libs vienen compiladas dentro de `lua.wasm`** (M02 ya las
  selecciona en el shim: base/table/string/math/coroutine/utf8, sin io/os/
  package/debug). El recorte de `os` (bannedOsFuncs) y de `dofile`/`loadfile`/
  `io` se hace con un preludio Lua. `DoString`/`LoadString` pasan por los
  exports del shim (`spike_eval` y familia). El baseline sube a **Lua 5.4** en
  M14 (única modificación de api.md §1.2, decidida en ADR-019).
- `require`/loader (C6-loader): el cargador curado (loader.go, plugin.go) se
  reimplementa sobre wasm en M13; `package` de PUC sigue sin abrirse (DM5).

## Mapa fichero → categoría → sesión

Ordenado por densidad de acoplamiento (`--files`). "Toca" = categorías presentes.

| Fichero | refs | Categorías | Sesión que lo migra |
|---|---:|---|---|
| `ui.go` | 62 | C2, C5 | M11 |
| `search.go` | 61 | C1, C2, C3(⏸) | M09 |
| `proc.go` | 55 | C2, C3(⏸), C5 | M09/M10 |
| `codecs.go` | 54 | C1, C2 | M09 |
| `worker.go` | 52 | C2, C3, C6 | M12 |
| `scheduler.go` | 51 | **C3** | M06 |
| `fs.go` | 46 | C2, C3(⏸) | M09 |
| `cancel.go` | 35 | **C4** | M07 (y muere en M17) |
| `http.go` | 34 | C2, C3(⏸) | M09 |
| `ws.go` | 32 | C2, C3(⏸), C5 | M09/M10 |
| `stream.go` | 31 | C2, C3(⏸), C5 | M09/M10 |
| `input.go` | 30 | C2, C5 | M11 |
| `plugin.go` | 21 | C2, C6-loader | M13 |
| `loader.go` | 19 | C6-loader | M13 |
| `diff.go` | 18 | C1, C2 | M09 |
| `text.go` | 17 | C1, C2 | M09 |
| `watch.go` | 16 | C2, C5 | M10 |
| `sys.go` | 16 | C2 | M09 |
| `allrace.go` | 16 | C3 | M06 |
| `timers.go` | 15 | C2, C3, C5 | M06/M10 |
| `sandbox.go` | 14 | **C6** | M04 |
| `re.go` | 14 | C1, C2, C5 | M09/M10 |
| `markdown.go` | 14 | C1, C2 | M09 |
| `errors.go` | 14 | **C4** | M05 (cruce de errores) |
| `events.go` | 13 | C2, C6 | M08 |
| `driver.go` | 13 | C2, C5 | M11 |
| `worker_registry.go` | 11 | C3, C6 | M12 |
| `highlight.go` | 11 | C1, C2 | M09 |
| `future.go` | 9 | C3 | M06 |
| `ui_events.go` | 7 | C2, C5 | M11 |
| `block.go` | 7 | C1, C5 | M09/M11 |
| `nu.go` | 6 | C2 | M05 (registerNu) |
| `log.go` | 6 | C2 | M08 |
| `runtime.go` | 5 | C6 | M04 |
| `eval.go` | 5 | C6 | M04 |

## Conclusiones que guían el orden de las sesiones

1. **El grueso es mecánico (C1+C2)**: marshaling + host functions. Una vez M05
   fija la infra, M09 convierte ~15 ficheros de IO/datos casi en bloque.
2. **El riesgo está concentrado en C3 (M06)**: el puente. Por eso lleva su
   propio ADR y es dependencia de casi todo. Se ataca temprano tras los
   cimientos.
3. **C4 es la buena noticia**: no se traduce, se borra. cancel.go y el blindaje
   de G41 existen SOLO por defectos de gopher-lua; en PUC sobran. La migración
   *reduce* el kernel aquí.
4. **La allowlist de `--check` es el trinquete**: mientras la migración avanza,
   ningún fichero puede acoplar un símbolo gopher-lua nuevo sin que CI lo cante.
   Cuando M17 retire gopher-lua, el script se retira con él.
