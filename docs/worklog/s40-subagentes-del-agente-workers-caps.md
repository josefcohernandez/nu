---
title: "Subagentes del agente (workers + caps recortadas + digesto al padre) (agente.md §9)"
type: "sesion"
id: "S40"
phase: 8
status: "cerrada"
---
# S40 — Subagentes del agente (workers + caps recortadas + digesto al padre) (agente.md §9)

## Qué pedía la sesión

S40 amplía la extensión `agent` (S39) con SUBAGENTES (agente.md §9): un agente que corre AISLADO
y devuelve al padre un RESULTADO DIGERIDO. El contrato: `Session:spawn(opts) -> Sub`,
`Sub:run(prompt) ⏸ -> digest`, `Sub:cancel()`, con dos modos (`worker=false`: task en el
principal compartiendo tools; `worker=true`: loop en un `enu.worker` con `caps` recortadas y los
handlers de tools ejecutados en el principal vía proxy de mensajes), más los paquetes de caps con
nombre `agent.caps.*`. Todo Lua sobre la API congelada (ADR-003): `enu.worker` §13 + `enu.task` +
el módulo `providers`. **NO amplía api.md** (APILevel sigue en 2; ni una función pública nueva).

## Arquitectura elegida (dos módulos)

- `lua/agent/subagent.lua`: el handle `Sub`, los dos modos y el PROXY de tools del lado del
  PADRE. Se cablea sobre el módulo `agent` ya construido con `subagent.attach(M)` (inyección para
  evitar require circular), exponiendo `M._subagent.spawn` (usado por `Session:spawn`).
- `lua/agent/subagent_worker.lua`: el LOOP del subagente que corre DENTRO del worker. Es el
  `module` que `enu.worker.spawn("agent.subagent_worker", {caps=...})` carga.

## Decisiones de interpretación de agente.md §9

1. **El digesto** (agente.md §9 dice "resultado digerido, no el stream crudo") se materializa como
   `{ text, message, stop_reason, usage, turns }`: `text` es el texto plano del mensaje final
   (atajo que el padre integra como tool_result/mensaje), `message` el Message canónico completo
   (JSON-able), `usage` el del proveedor del último turno. JSON-able a propósito: cruza la frontera
   del worker sin Blocks/closures (api.md §13).

2. **El proxy de tools** (modo worker). El worker NO ejecuta handlers: por cada tool_call manda
   `{kind="tool_call", id, name, args}` al padre por `enu.worker.parent.send` y espera
   `{kind="tool_result", result}`. El padre corre la tool con `M.run_tool_proxy(proxy_session,
   call)` = el mismo `run_tool` del turno (permisos → hooks → handler → tool_result). Así la
   seguridad queda centralizada (el worker no puede esquivar el pipeline porque la ejecución nunca
   ocurre en su lado) y hay UN solo registro de tools. La `proxy_session` es una `agent.session`
   hija real (aporta permisos heredados-recortados, cwd, y el transcript hijo si persiste).

3. **DOS VALLAS** (agente.md §9, literal): las *caps* limitan qué hace el código Lua del worker
   (G6, sandbox del core); los *permisos* (heredados del padre, recortados por `opts.permissions`,
   nunca ampliados) limitan qué tools usa —y como las tools corren en el padre, su pipeline de
   permisos §5 es la valla efectiva—.

4. **Caps por defecto de un subagente-worker: solo-lectura.** `FS_RO` (fs.read/stat/list/cwd) +
   `SEARCH` + los MÍNIMOS DEL LOOP (`task`/`json`/`toml`/`config.dir`/`log`/`fs.read`). Razón de
   los mínimos: el worker debe poder orquestar (task), serializar el digesto/los mensajes del
   proxy (json) y RESOLVER el modelo —`providers.resolve` lee `providers.toml` del disco con
   `enu.fs.read`+`enu.toml.decode` desde `enu.config.dir`—. Sin ellos el worker no podría ni correr
   el turno ni devolver nada. `normalize_caps` siempre los añade a una lista de usuario, sin
   ampliar la superficie de fs/net que el usuario eligió.

## Desviación: `opts.adapter_modules` (opt de la extensión, NO del core)

agente.md §9 lista `opts` = los de `agent.session` + `{ worker?, caps? }`. He añadido un opt
EXTRA de la extensión (no del core): `opts.adapter_modules` (lista de NOMBRES de módulo de
adaptador require-ables que el worker registra antes de resolver). **Por qué es necesario:** el
`init.lua` de `providers` —que registra los adaptadores oficiales imperativamente— NO corre dentro
de un worker (un worker solo ejecuta `require(module)`, sin ciclo de vida de plugins, api.md §13).
Así el registro vivo de adaptadores arranca VACÍO en el worker; el bootstrap lo rellena
requiriendo los módulos nombrados (los oficiales SON require-ables: `providers.adapter_anthropic`).
Es re-ejecutar lo que haría init.lua, sin privilegio de kernel. Tiene defecto sensato
(`{ "providers.adapter_anthropic" }`), así que el caso normal no lo necesita; los tests lo usan
para inyectar un stub require-able. Es una adición a los opts de UNA extensión, no a `api.md`.

## Por qué NO hizo falta ampliar api.md

El subagente-worker se expresa enteramente con la API pública: `enu.worker.spawn` con `caps`
(api.md §13, G6) para el aislamiento DURO; `Worker:send`/`recv` + `enu.worker.parent.send`/`recv`
para el protocolo init/tool_call/tool_result/done (mensajes JSON-ables copiados); `enu.task` para
el loop; el módulo `providers` (resolve + register_adapter) y el módulo `agent` (run_tool_proxy,
caps). El corolario de completitud se satisface: una feature oficial construida sin atajo de
kernel. APILevel sigue en 2.

## El subagente-worker es HEADLESS por construcción

Dentro del worker NO existen `enu.events` (bus principal) ni `enu.ui` (api.md §16). El loop del
subagente-worker, por tanto, DESCARTA los deltas del stream (no hay a quién emitirlos) y solo
emite el DIGESTO al padre. Coherente con agente.md §9: el padre recibe datos digeridos, no el
stream crudo. En modo task (worker=false) sí se emiten los `agent:*` (corre en el principal).

## Tests (`subagent_test.go`)

Arnés con providers+sessions+agent + un plugin de usuario que aporta dos módulos require-ables:
`wstub` (adaptador stub que decide su comportamiento mirando el REQUEST, no globales del principal
—que no cruzan al worker—) y `wprobe` (módulo de worker que reporta qué API existe dentro).
Casos: superficie de `Session:spawn`/`Sub`; modo task (digesto con texto+usage del último turno);
modo worker e2e (turno aislado con `wstub` → digesto integrado por el padre); AISLAMIENTO DESDE
DENTRO (`wprobe` con las caps por defecto: `fs.write`/`http`/`ui`/`events` NO existen,
`fs.read`/`task`/`json`/`toml` SÍ — la verificación directa del criterio "API recortada");
PROXY de tools (una tool cuyo handler marca una global del PRINCIPAL: si cambió, corrió en el
padre); caps mal formadas → EINVAL; los paquetes `agent.caps.*` sin `fs.write`.

`CGO_ENABLED=0 go build`/`go vet ./...` verdes; `gofmt -l` limpio; `CGO_ENABLED=1 go test -race
-timeout 120s -count=2 ./internal/...` verde (~53 s); no regresiona S01–S39. Sin hallazgos `G##`.

## Lo que reusará S41/S43

- **S41 (MCP):** el registro único de tools + `run_tool_proxy`/los permisos centralizados (las
  tools de un servidor MCP se registran con `agent.tool` igual que las de fichero, y el subagente
  las usa por el mismo proxy); el patrón de mensajería worker↔padre.
- **S43 (chat):** `Session:spawn`/`Sub:run`/el digesto como contrato consumido igual que un
  tercero, y (en modo task) los eventos `agent:*` del subagente.

**Nota de proceso.** Tras código + tests + docs (puntero a S41, bitácora, esta entrada) +
build/vet/gofmt/race-count=2 verdes, se commitea y pushea SIN demora (lección de S38/S39).
