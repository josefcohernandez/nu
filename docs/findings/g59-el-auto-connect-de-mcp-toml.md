---
title: "El auto-connect de `mcp.toml` es inservible en headless `-p`: la task efímera desconecta las tools antes del turno, y `env` (array) no llega al subproceso"
type: "hallazgo"
id: "G59"
status: "abierto"
date: "2026-07-18"
origin: "suite e2e de plugins oficiales (e2e/mcp_test.go, cabecera de hallazgos)"
affected: ["extensión mcp (embedded/mcp/lua/mcp/init.lua)", "enu.proc.spawn"]
---
# G59 · El auto-connect de `mcp.toml` es inservible en headless `-p`: la task efímera desconecta las tools antes del turno, y `env` (array) no llega al subproceso — extensión `mcp` / `enu.proc`

**Problema.** Dos grietas contiguas, ambas caracterizadas desde fuera del
binario en `e2e/mcp_test.go`:

1. **La task del auto-connect es efímera y se desconecta antes del turno.**
   El auto-connect de servidores declarados en `mcp.toml`
   (`embedded/mcp/lua/mcp/init.lua:35`) hace
   `pcall(mcp.connect_configured)` y retorna. Al terminar esa task, su
   `enu.task.cleanup` (registrado dentro de `M.connect`) cierra cada conexión,
   mata el subproceso del servidor y re-registra sus tools como stubs de
   "servidor desconectado" (`permissions.default = "deny"`, handler que lanza
   `EMCP`). Todo esto ocurre **durante `Boot`**, porque `RunTasks` drena la
   task hasta quiescencia antes de que arranque el turno de `-p` — así que
   cuando el agente por fin puede pedir la tool, el servidor real ya está
   muerto. Comprobado desde fuera: tras el boot, `mcp.servers()` queda vacío
   pero `mcp__srv__echo` sigue en `agent.tools()` (el stub); un `-p` que pide
   esa tool no invoca el servidor real y no da `exit 3` (el stub de deny
   devuelve `tool_result` con `is_error`, así que el proceso sale con 0). El
   propio módulo se contradice: `connect_configured` se documenta como
   pensado para correr en una **task de larga vida**
   (`mcp/lua/mcp/init.lua:463`), pero la task que la invoca en el auto-connect
   es efímera por construcción.
2. **`env` de `mcp.toml` (array) no llega al subproceso.** `mcp.toml`
   documenta `env = ["K=V", ...]` (array de strings;
   `embedded/mcp/lua/mcp/init.lua:428`), y ese array se pasa tal cual a
   `enu.proc.spawn`. Pero la primitiva **solo interpreta `env` como tabla**
   `{ K = V }` (mapa string→string; `internal/runtime/vmwasm_proc.go:250`):
   un array Lua es `[]any`, no `map[string]any`, así que se ignora en
   silencio y el subproceso hereda el entorno del padre sin las claves
   declaradas. Verificado e2e: un servidor MCP configurado con `env` no
   recibe la variable.

**Impacto.** Un servidor MCP declarado en `mcp.toml` es, hoy, **inservible
desde `enu -p`** (el modo headless de un solo turno): el auto-connect lo
lanza y lo mata antes de que el agente pueda usarlo, y aunque sobreviviera,
cualquier configuración que dependa de `env` para autenticarse o parametrizar
el servidor tampoco llegaría. Afecta a cualquier integración MCP pensada para
correr desatendida (CI, automatización) — el caso de uso que `-p` existe para
servir. `e2e/mcp_test.go` documenta ambas grietas en su cabecera de
"HALLAZGOS que esta suite destapó" y las **rodea sin trampa**: el escenario 1
(mínimo imprescindible) se conduce con `enu -e` + `connect_configured` +
`agent.session` en una única task —que sigue leyendo `mcp.toml` real y
ejerciendo servidor/stdio/HTTP reales—, y el escenario 2 original (deny →
`exit 3` vía tool MCP en `-p`) se recortó por inalcanzable, ya que la tool MCP
real nunca llega viva a un turno de `-p`. Los ajustes del servidor de prueba
se pasan por **argv** en vez de por `env` precisamente porque `env` está
verificado como roto.

**Opciones a explorar** (no se decide en esta entrada; el arreglo queda
pospuesto):
- **(a) Para la task efímera: fusionar `connect_configured` + `agent.session`
  en una sola task de vida más larga**, en vez de dos pasos donde el primero
  se autolimpia antes del segundo — es la vía que ya usa el escenario 1 de la
  suite e2e como rodeo, y candidato natural a convertirse en el camino oficial
  del auto-connect headless.
- **(b) Para la task efímera: no registrar el `cleanup` de desconexión al
  auto-connect**, dejando la conexión viva hasta el apagado real del binario
  — requiere decidir quién es entonces el dueño del cleanup final (¿el propio
  `core:shutdown`, con el mismo mecanismo que G58 investiga?).
- **(c) Para `env`: traducir el array de `mcp.toml` a mapa antes del
  `spawn`** dentro de `connect_configured`, sin tocar la primitiva — mínimo
  cambio, pero dos formatos de `env` conviviendo en el ecosistema (el que
  documenta `mcp.toml` y el que espera `enu.proc.spawn`) es una superficie
  confusa para cualquier otro plugin que dispare `mcp.toml`-alike.
- **(d) Para `env`: `enu.proc.spawn` acepta también array `["K=V", ...]`**
  además de la tabla — corolario de completitud si el patrón `env` como array
  resulta ser el vocabulario natural de más de un llamador; toca `api.md` y
  exige el mismo escrutinio que cualquier adición a la superficie sagrada.

**Disparador de reapertura.** Cuando se toque el ciclo de vida del
auto-connect de `mcp.toml` (`embedded/mcp/lua/mcp/init.lua`, `M.connect` /
`connect_configured`) o el parseo de `env` de `enu.proc.spawn`
(`internal/runtime/vmwasm_proc.go`).
