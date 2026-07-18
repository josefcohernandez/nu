-- Módulo público de la extensión `repl` (S44): un **REPL de Lua** sobre la API
-- pública congelada.
--
-- Implementa el contrato de [arquitectura.md](../../../../../docs/arquitectura.md)
-- §"Distribución": «El conjunto [de extensiones embebidas] incluye, además del
-- harness (agente, chat, providers, MCP, toolkit), un **`repl`**: REPL de Lua
-- sobre la API pública, activable solo — el punto de partida del autor de
-- extensiones que no quiere el harness (G21)». Es la PRUEBA de que el runtime
-- sirve para más que el agente: `enu` con SOLO `repl` activo es un intérprete Lua
-- interactivo con acceso a `enu.*`.
--
-- ADR-003: el core NO sabe lo que es un REPL; todo esto es Lua puro sobre la API
-- pública congelada ([api.md](../../../../../docs/api.md)), SIN privilegio de
-- kernel. El repl no declara `requires` en su `plugin.toml`: depende solo de la
-- API del core (para EVALUAR) y, si hay TTY, del `toolkit` (S42) para su UI —pero
-- como dependencia BLANDA (`require` perezoso bajo `pcall`), no dura: el repl debe
-- poder activarse SOLO (G21), sin arrastrar el harness. El namespace de eventos de
-- esta extensión sería `repl:` (el del propio plugin, §4); en S44 no emite eventos
-- propios.
--
-- CÓMO EVALÚA LUA ARBITRARIO (el punto delicado de S44, corolario de completitud).
-- Un REPL NECESITA compilar y ejecutar código del usuario. El baseline del sandbox
-- (§1.2, S01) deshabilita `dofile`/`loadfile` (cargan FICHEROS de disco saltándose
-- el loader) y `os.execute`/`io`… pero **NO** `load`: esta compila
-- un string EN MEMORIA, sin IO bloqueante, así que no viola la razón del baseline
-- ("todo IO debe pasar por las primitivas async del core"). Queda disponible para
-- el Lua de usuario tal cual la define la base de PUC-Lua 5.4 (§1.2). Por eso la API
-- pública BASTA exacta para un REPL: **no hizo falta ninguna primitiva nueva**
-- (`enu.eval` o similar); APILevel sigue en 2, api.md intacto. Si `load` no
-- existiera, ESO sería un hallazgo (un REPL oficial inconstruible con la API), pero
-- no es el caso: el sandbox ya dejó la puerta justa abierta.
--
-- La superficie pública del módulo:
--
--   repl.eval(src: string) -> { ok, values?, display, error? }
--       EVALÚA una línea Lua y devuelve un resultado ESTRUCTURADO (la lógica pura,
--       probada headless). `ok=true` con `values` (los retornos) y `display` (su
--       texto); `ok=false` con `error` (la tabla estructurada o el texto) y
--       `display`. Si la entrada está INCOMPLETA, `incomplete=true` (para el modo
--       multilínea: pedir otra línea en vez de reportar un error).
--   repl.eval_in_task(src, cb) — evalúa en una TASK (para que el código de usuario
--       use funciones ⏸ del core: `enu.fs.read`, `enu.http.request`…) y entrega el
--       resultado a `cb`. La vía que usa el bucle interactivo.
--   repl.start(opts?) -> Repl   monta la UI interactiva sobre el toolkit (solo TTY).
--   repl.banner() -> string     el banner de bienvenida (versión + ayuda mínima).

local M = {}

-- ---------------------------------------------------------------------------
-- El núcleo: compilar y evaluar una línea de Lua (la lógica PROBADA, headless).
-- ---------------------------------------------------------------------------

-- compile(src) -> (fn|nil, err_msg|nil, incomplete). Compila `src` con la
-- semántica de un REPL de Lua:
--
--   1. Intenta `return <src>`. Así una EXPRESIÓN suelta (`1+1`, `enu.version.api`)
--      se evalúa y devuelve su valor sin que el usuario escriba `return` —el truco
--      clásico del REPL de Lua—. Si `return <src>` compila, esa es la chunk.
--   2. Si no (porque `src` es una SENTENCIA: `x = 5`, `for i=1,3 do ... end`, una
--      llamada sin retorno), compila `src` tal cual.
--   3. Si AMBOS fallan, el segundo error es el de verdad (el de la sentencia, no el
--      artefacto de anteponer `return`). Se inspecciona: PUC-Lua 5.4 marca la
--      entrada INCOMPLETA (función/bloque/string sin cerrar, expresión a medias)
--      con `<eof>` en el mensaje (frente a un error real, que trae `near '<token>'`).
--      Una entrada incompleta NO es un error: es la señal de "dame otra línea" del
--      modo multilínea.
--
-- `load` está disponible para el Lua de usuario (ver cabecera): el sandbox retiró
-- `dofile`/`loadfile` (disco) pero no `load` (memoria). En PUC-Lua 5.4 `load` acepta
-- un string directamente (absorbió al `loadstring` de 5.1): devuelve `(fn)` o
-- `(nil, msg)`.
local function compile(src)
  -- 1) como expresión (return ...).
  local as_expr = load("return " .. src, "=repl")
  if as_expr then
    return as_expr, nil, false
  end
  -- 2) como sentencia.
  local as_stmt, stmt_err = load(src, "=repl")
  if as_stmt then
    return as_stmt, nil, false
  end
  -- 3) ambos fallan: el error de la SENTENCIA manda. ¿Incompleta?
  local incomplete = type(stmt_err) == "string" and stmt_err:find("<eof>", 1, true) ~= nil
  return nil, stmt_err, incomplete
end

-- format_value(v) -> string. Representación legible de UN valor de retorno. Para un
-- string, se entrecomilla (así `"hola"` se distingue de un identificador y los
-- espacios/vacíos se ven); el resto va por `tostring` (números, booleanos, nil,
-- tablas/funciones/userdata con su dirección). Es el formato que un REPL imprime;
-- no pretende ser un serializador (eso es `enu.json.encode`, que el usuario llama si
-- quiere).
local function format_value(v)
  if type(v) == "string" then
    return string.format("%q", v)
  end
  return tostring(v)
end

-- format_results(values, n) -> string. Une los `n` valores de retorno con tabulador
-- (el separador de columnas, como `print`). Cero valores → "" (una sentencia que no
-- retorna nada no imprime resultado, igual que el REPL de referencia). `n` es
-- explícito (no `#values`) para preservar los `nil` intercalados de un retorno
-- múltiple (`return 1, nil, 3`).
local function format_results(values, n)
  if n == 0 then
    return ""
  end
  local parts = {}
  for i = 1, n do
    parts[i] = format_value(values[i])
  end
  return table.concat(parts, "\t")
end

-- format_error(err) -> string. Texto legible de un error capturado. Un error
-- ESTRUCTURADO del core (§1.4: `{code, message, detail?}`) se muestra como
-- `code: message` (la forma con la que el usuario lo reconoce, p. ej.
-- `ENOENT: no such file`); cualquier otro error (un `error("texto")`, un fallo de
-- runtime de Lua) por `tostring`. El puente NO degrada el error estructurado: el
-- repl lo recibe entero (invariante de S02) y decide cómo pintarlo.
local function format_error(err)
  if type(err) == "table" and err.code then
    local msg = err.message or ""
    return tostring(err.code) .. ": " .. tostring(msg)
  end
  return tostring(err)
end
M._format_error = format_error
M._format_value = format_value

-- repl.eval(src) -> result. EVALÚA una línea de Lua y devuelve un resultado
-- ESTRUCTURADO (la lógica pura del REPL, probada headless). El resultado:
--
--   { ok = true,  values = {...}, n = <nº retornos>, display = "<texto>" }
--   { ok = false, error = <err>,                     display = "<texto>", incomplete? }
--
--   * EXPRESIÓN (`1+1`)        → ok, values={2}, display="2".
--   * LLAMADA API (`enu.version.api`) → ok, values={2}, display="2".
--   * SENTENCIA (`x = 5`)      → ok, values={}, n=0, display="" (no imprime nada).
--   * ERROR (`error("boom")`, `enu.fs.read` que lanza) → ok=false, error, display.
--   * SINTAXIS mala (`return )`) → ok=false, display=el mensaje, NO incomplete.
--   * INCOMPLETA (`function f()`) → ok=false, incomplete=true (pedir otra línea).
--
-- IMPORTANTE: `eval` corre la chunk con `pcall` (captura el error del usuario sin
-- tumbar el repl) PERO no está en una task: una chunk que llame a una función ⏸
-- (`enu.fs.read`…) lanzará "fuera de una task". Para esos casos el bucle interactivo
-- usa `eval_in_task` (abajo), que corre la misma lógica DENTRO de una task. `eval`
-- es la unidad pura y síncrona: perfecta para expresiones, sentencias y llamadas a
-- la API NO suspendientes (la mayoría: `enu.version`, `enu.text.*`, `enu.json.*`…).
function M.eval(src)
  if type(src) ~= "string" then
    error({ code = "EINVAL", message = "repl.eval espera un string de código Lua" })
  end
  -- entrada en blanco: nada que evaluar (ni error ni resultado).
  if src:match("^%s*$") then
    return { ok = true, values = {}, n = 0, display = "" }
  end

  local fn, cerr, incomplete = compile(src)
  if fn == nil then
    if incomplete then
      return { ok = false, incomplete = true, error = cerr, display = "" }
    end
    -- error de sintaxis real (no incompleta): se reporta como error de compilación.
    return { ok = false, error = cerr, display = format_error(cerr) }
  end

  -- ejecuta bajo pcall: el error del usuario se captura (no tumba el repl, ADR-008
  -- en espíritu: una frontera con pcall). `pcall` devuelve (ok, ...retornos|err).
  local packed = { pcall(fn) }
  local ok = packed[1]
  if not ok then
    local err = packed[2]
    return { ok = false, error = err, display = format_error(err) }
  end
  -- éxito: los retornos están en packed[2..]. Preservamos los `nil` con un contador.
  local n = #packed - 1
  local values = {}
  for i = 1, n do
    values[i] = packed[i + 1]
  end
  return { ok = true, values = values, n = n, display = format_results(values, n) }
end

-- repl.eval_in_task(src, cb). Evalúa `src` DENTRO de una task y entrega el resultado
-- de `repl.eval` a `cb(result)`. Es la vía del bucle interactivo: una línea de
-- usuario puede llamar a funciones ⏸ del core (`enu.fs.read`, `enu.http.request`,
-- `enu.search.grep`…), que SOLO corren dentro de una task (§1.3). La task vive lo
-- justo: evalúa y llama al callback. Un error de `eval` mismo (improbable: solo
-- EINVAL por tipo) se captura para no perder el callback.
--
-- Por qué task y no el handler síncrono: el evaluar puede SUSPENDER (await
-- implícito); el handler de una tecla (`on_input`) es síncrono y no puede. El patrón
-- es el mismo que usa `chat:submit` (S43) con `Session:send`.
function M.eval_in_task(src, cb)
  enu.task.spawn(function()
    local ok, result = pcall(M.eval, src)
    if not ok then
      result = { ok = false, error = result, display = format_error(result) }
    end
    if cb then
      cb(result)
    end
  end)
end

-- repl.banner() -> string. El banner de bienvenida: identifica el runtime (versión
-- + nivel de API, §2) y recuerda lo mínimo (cómo salir). Es texto del runtime, no de
-- un producto (filosofia.md §2): habla de `enu` y su API, no de un agente.
function M.banner()
  local v = enu.version
  return string.format(
    "enu %d.%d.%d  ·  REPL de Lua (API %d)\n" ..
    "Escribe una expresión Lua y pulsa enter.  ctrl+d o /q para salir.",
    v.major, v.minor, v.patch, v.api)
end

-- ---------------------------------------------------------------------------
-- La UI interactiva (el DRIVER TTY): un transcript + un input sobre el toolkit.
-- No se prueba headless (necesita TTY/`enu.ui`, G20); la lógica que SÍ se prueba es
-- `eval`/`eval_in_task`/el banner. La UI es un cliente más del toolkit (S42), como
-- el chat (S43) pero mucho más simple: sin agente, sin streaming, sin permisos.
-- ---------------------------------------------------------------------------

local Repl = {}
Repl.__index = Repl

-- Repl:_print(s) añade `s` al transcript (cada \n una línea) y repinta. Auto-scroll
-- al final (ver lo último escrito), igual que el chat.
function Repl:_print(s)
  if s == nil or s == "" then
    return
  end
  self.lines[#self.lines + 1] = tostring(s)
  self:_refresh()
end

-- Repl:_refresh() vuelca el transcript acumulado al `toolkit.text` y auto-scrolla al
-- final. El widget recompone su Block solo si cambió (dirty tracking de S42).
function Repl:_refresh()
  local text = table.concat(self.lines, "\n")
  self.output:set_text(text)
  local w = self.output.w
  if w and w > 0 then
    local ch = self.output:content_height(w)
    local band = self.output.h or 0
    self.output:scroll_to(math.max(0, ch - band))
  end
  if self.app and self.app._alive then
    self.app:paint()
  end
end

-- Repl:_submit() EVALÚA la línea (o el bloque multilínea acumulado) del input. Es el
-- corazón del bucle: refleja `> <línea>` en el transcript, evalúa en una task
-- (para que el código ⏸ funcione) y pinta el resultado/error. Si la entrada está
-- INCOMPLETA (función/bloque sin cerrar), NO evalúa: acumula la línea y cambia el
-- prompt a continuación (`..`), pidiendo más. Un comando de salida (`/q`) cierra.
function Repl:_submit()
  local line = self.input:value()
  self.input:set_value("")

  -- comandos del repl (mínimos): salir. Solo en la primera línea de un bloque.
  if self.pending == "" and (line == "/q" or line == "/quit" or line == "/exit") then
    self:_print("")
    self:quit()
    return
  end

  -- ¿estamos en mitad de un bloque multilínea? acumula.
  local src
  if self.pending ~= "" then
    src = self.pending .. "\n" .. line
    self:_print(".. " .. line)
  else
    src = line
    self:_print("> " .. line)
  end

  -- ¿la entrada está completa? Compilamos en seco con `repl.eval` para detectar la
  -- incompletitud SIN ejecutar dos veces: si `incomplete`, acumulamos; si no,
  -- evaluamos de verdad en una task. (La doble compilación es barata —memoria— y
  -- evita ejecutar efectos colaterales al sondear; pero para no ejecutar el cuerpo
  -- en el sondeo, `eval` ya corre la chunk: por eso el sondeo de incompletitud lo
  -- hace una compilación ligera aparte.)
  local _fn, _cerr, incomplete = self:_compile_probe(src)
  if incomplete then
    self.pending = src
    self:_set_prompt("..")
    return
  end

  -- entrada completa: evalúa en una task (código ⏸ permitido) y pinta el resultado.
  self.pending = ""
  self:_set_prompt(">")
  M.eval_in_task(src, function(result)
    if result.ok then
      if result.display ~= "" then
        self:_print(result.display)
      end
    else
      self:_print(result.display)
    end
  end)
end

-- Repl:_compile_probe(src) sondea si `src` está completo, SIN ejecutarlo. Reusa la
-- misma regla que `compile` (return<expr> / stmt / `at EOF`), pero solo para el flag
-- `incomplete`: el bucle decide entre acumular (multilínea) o evaluar. No ejecuta
-- ninguna chunk (no hay efectos colaterales en el sondeo).
function Repl:_compile_probe(src)
  local as_expr = load("return " .. src, "=repl")
  if as_expr then
    return as_expr, nil, false
  end
  local as_stmt, stmt_err = load(src, "=repl")
  if as_stmt then
    return as_stmt, nil, false
  end
  local incomplete = type(stmt_err) == "string" and stmt_err:find("<eof>", 1, true) ~= nil
  return nil, stmt_err, incomplete
end

-- Repl:_set_prompt(p) cambia el placeholder/prompt del input (">" normal, ".." en
-- continuación multilínea). Visual; repinta.
function Repl:_set_prompt(p)
  self.prompt = p
  self.prompt_label:set_text(p .. " ")
  if self.app and self.app._alive then
    self.app:paint()
  end
end

-- Repl:quit() desmonta la UI: suelta los keymaps (sin huérfanos, G2), cierra la app
-- (su región/on_input) y marca cerrado. Idempotente.
function Repl:quit()
  if self._closed then
    return
  end
  self._closed = true
  for _, k in ipairs(self.keymaps or {}) do
    if k and k.unmap then k:unmap() end
  end
  self.keymaps = {}
  for _, s in ipairs(self.subs or {}) do
    if s and s.cancel then s:cancel() end
  end
  self.subs = {}
  if self.app then
    self.app:close()
  end
end

-- repl.start(opts?) -> Repl. Monta la UI interactiva del REPL (el DRIVER TTY).
-- Exige `enu.ui` (TTY interactivo, G20): en headless es EINVAL accionable —el repl
-- interactivo necesita pantalla; para evaluar Lua sin TTY está `repl.eval` o
-- directamente `enu -e`—. La UI: una `toolkit.app` con un `vbox` de
--   * un `toolkit.text` (el transcript: banner + entradas + resultados, flex), y
--   * una fila de entrada (`hbox`: un label-prompt `>` + un `toolkit.input`).
-- Enter evalúa (keymap global; el input deja pasar enter "pelado", como el chat).
function M.start(opts)
  opts = opts or {}
  if not enu.has("ui") then
    error({ code = "EINVAL",
      message = "repl.start: no hay UI (headless, G20). El REPL interactivo necesita "
        .. "un TTY; comprueba enu.has(\"ui\") antes (arquitectura §Distribución). "
        .. "Para evaluar Lua sin TTY usa repl.eval(src) o `enu -e`." })
  end

  -- el toolkit es dependencia BLANDA del repl (no en `requires`: el repl se activa
  -- SOLO, G21): se requiere aquí, perezosamente. Si no está, EINVAL accionable
  -- (nombra cómo activarlo) —pero el repl SOLO sí evalúa por `repl.eval`; la UI es
  -- el plus que pide el toolkit—.
  local ok_tk, toolkit = pcall(require, "toolkit")
  if not ok_tk then
    error({ code = "EINVAL",
      message = "repl.start: la UI del REPL usa el toolkit (S42), no disponible. "
        .. "Actívalo en enu.toml (plugins.enabled = [\"toolkit\", \"repl\"]) o usa "
        .. "repl.eval(src) para evaluar sin UI." })
  end

  local self = setmetatable({
    lines    = {},
    pending  = "",         -- buffer del bloque multilínea en curso
    prompt   = ">",
    keymaps  = {},
    subs     = {},
    _closed  = false,
  }, Repl)

  -- el árbol: vbox( transcript(flex) , hbox( prompt-label , input ) ).
  local column = toolkit.vbox({ id = "repl-column" })

  self.output = toolkit.text({ id = "repl-output", markdown = false })
  self.output.flex = 1

  local input_row = toolkit.hbox({ id = "repl-input-row" })
  input_row.pref_h = 1
  self.prompt_label = toolkit.label({ id = "repl-prompt", text = self.prompt .. " " })
  self.prompt_label.pref_w = 2
  self.input = toolkit.input({ id = "repl-input", placeholder = "" })
  self.input.flex = 1
  input_row:add(self.prompt_label)
  input_row:add(self.input)

  column:add(self.output)
  column:add(input_row)

  self.app = toolkit.app({ root = column, theme = opts.theme })
  self.app:set_focus(self.input)

  -- atajos GLOBALES (api.md §9.3): enter evalúa (el input deja pasar enter pelado,
  -- como el editor del chat); ctrl+d sale (la convención del REPL). Por encima del
  -- on_input de la app (el más reciente gana), así funcionan con el foco en el input.
  self.keymaps[#self.keymaps + 1] = enu.ui.keymap("enter", function()
    self:_submit()
    return true
  end)
  self.keymaps[#self.keymaps + 1] = enu.ui.keymap("ctrl+d", function()
    self:quit()
    return true
  end)

  -- ui:resize (api.md §9.1, "tu región, tu ui:resize"): rehace el layout.
  self.subs[#self.subs + 1] = enu.events.on("ui:resize", function(p)
    if not self._closed and self.app then
      self.app:resize(p and p.w, p and p.h)
      self:_refresh()
    end
  end)

  -- banner de bienvenida + primer pintado.
  self:_print(M.banner())

  M._active = self
  return self
end

return M
