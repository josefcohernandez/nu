-- Módulo público de la extensión `chat` (S43): la UI oficial del harness.
--
-- Implementa el contrato de [chat.md](../../../../../docs/chat.md) sobre el
-- toolkit (S42) y el agente (S39), SIN privilegio de kernel (ADR-003):
--
--   §1 LAYOUT: una `toolkit.app` con un `vbox` de tres bandas —transcript
--      (`toolkit.text{markdown=true}`, desplazable, flex), input multilínea
--      (`chat.input`) y statusline (`hbox` de `toolkit.label`)— más una capa modal
--      (`toolkit.stack`) para el diálogo de permisos y los pickers.
--   §2 RENDER DEL TURNO: suscribe los eventos `agent:*` (filtrando por la sesión
--      activa, G3) y los pinta —`agent:delta` (texto) se acumula en el transcript y
--      se re-renderiza con markdown EN STREAMING (el corazón de S43: delta →
--      markdown → toolkit.text); `agent:message` sella; `agent:tool.*` bloques de
--      tool; `agent:permission.asked` el diálogo modal (§5); `agent:error`—.
--   §3 INPUT: editor multilínea (`chat.input`): enter envía, shift/alt+enter nueva
--      línea; `/` al inicio = comando slash (§4); esc cancela el turno.
--   §4 COMANDOS slash: `chat.command{}` (módulo commands).
--   §5 PERMISOS: diálogo modal ante `agent:permission.asked`, responde con
--      `agent.permission.respond`.
--   §6 STATUSLINE: segmentos modelo/contexto/coste/cwd/permisos (módulo statusline).
--   §7 THEME: colores semánticos resueltos por el theme del toolkit (G22).
--   §8 ARRANQUE: solo con TTY (`nu.has("ui")`); crea/reanuda la sesión del agente.
--
-- El Block del transcript CRECE con el texto en streaming porque el
-- `toolkit.text` re-renderiza el markdown acumulado al hacer `set_text` (S42 +
-- `nu.text.markdown` streaming-safe, S23): el camino caliente de CP-9, ahora
-- dentro del chat.

local toolkit = require("toolkit")
local agent = require("agent")
local providers = require("providers")
local sessions = require("sessions")

local chat_input = require("chat.input")
local transcript_mod = require("chat.transcript")
local statusline = require("chat.statusline")
local commands = require("chat.commands")
local permdialog = require("chat.permission")
local picker_mod = require("chat.picker")

local M = {}

-- Códigos de error de CONFIGURACIÓN que justifican el ARRANQUE DEGRADADO (chat.md §8,
-- ADR-017/G35): la sesión inicial no se pudo construir porque falta o está rota la
-- config de modelo/provider. `EINVAL` (no hay `model`), `EPROVIDER` (modelo/provider
-- no resoluble en `providers.toml`) o `EAGENT` (agent.toml mal formado). Cualquier otro
-- error es INESPERADO y se propaga (lo registra el init como hoy): no queremos esconder
-- un bug real tras una pantalla de "configura tu modelo".
local CONFIG_ERROR_CODES = { EINVAL = true, EPROVIDER = true, EAGENT = true }

-- Re-exporta los puntos de extensión (chat.md §9): comandos slash, segmentos de
-- statusline, renderers de tool results (v1: stub, ver transcript). La tabla de
-- atajos por defecto (chat.keys, chat.md §7) es pública y remapeable.
M.command = commands.command
M.statusline = statusline
M.commands = commands

-- chat.keys: la tabla de atajos por defecto (chat.md §7), pública y remapeable por
-- el usuario en su init.lua. Notación de `nu.ui.keymap` (api.md §9.3). En S43 la
-- mayoría del input lo enruta el editor multilínea (enter/shift+enter); estos son
-- los atajos GLOBALES del chat (no dependientes del foco del editor).
M.keys = {
  cancel   = "esc",       -- cancela el turno en curso (Session:cancel)
  quit     = "ctrl+c",    -- cierra el chat
  submit   = "enter",     -- envía el mensaje (el editor deja pasar enter "pelado")
  newline  = "shift+enter", -- nueva línea (lo consume el editor; alt+enter alterno)
  complete = "tab",       -- autocompletado de comandos `/` (P29)
}

-- renderer(tool_name, fn): registra el render del resultado de una tool (chat.md
-- §2, renderers enchufables). v1: se guarda; el transcript usa el fallback de
-- texto plano (el render rico por-tool es mejora posterior sobre el mismo item,
-- docs/decisiones-implementacion.md S43).
local tool_renderers = {}
function M.renderer(tool_name, fn)
  if type(tool_name) ~= "string" or type(fn) ~= "function" then
    error({ code = "EINVAL", message = "chat.renderer espera (tool_name, fn(result, width) -> Block)" })
  end
  tool_renderers[tool_name] = fn
end

-- ---------------------------------------------------------------------------
-- El handle Chat (una sesión visible, chat.md §1).
-- ---------------------------------------------------------------------------

local Chat = {}
Chat.__index = Chat

-- Chat:_refresh_transcript() vuelca el markdown acumulado del transcript al widget
-- `toolkit.text` (set_text). El widget re-renderiza el markdown (streaming-safe) y
-- el dirty tracking del toolkit recompone/repinta solo si cambió (S42). Tras
-- actualizar, auto-scroll al final (chat.md: ver lo último). Es el punto por el que
-- el Block del transcript CRECE con el texto en streaming (el criterio de hecho).
function Chat:_refresh_transcript()
  -- Pantalla de BIENVENIDA (chat.md §8): mientras la conversación está vacía, el
  -- transcript muestra un saludo con el modelo, el cwd y las pistas de uso —en vez de
  -- una pantalla en blanco—. Al primer mensaje, lo sustituye la conversación.
  local md
  if self.transcript:count() == 0 and not self.degraded then
    md = self:_welcome_md()
  else
    md = self.transcript:markdown()
  end
  self.transcript_widget:set_text(md)
  -- auto-scroll al final: la última línea visible. El alto del contenido a ancho
  -- del widget menos su banda da el offset de scroll (api.md §9.1: scroll = offset
  -- de viewport). Si cabe entero, scroll 0.
  local w = self.transcript_widget.w
  if w and w > 0 then
    local ch = self.transcript_widget:content_height(w)
    local band = self.transcript_widget.h or 0
    local off = math.max(0, ch - band)
    self.transcript_widget:scroll_to(off)
  end
end

-- Chat:_pending_total() -> número de asks pendientes que la statusline muestra como
-- un único `pending_asks` (chat.md §6, G3). Suma DOS estados que antes compartían un
-- solo campo (`pending_count`) y se PISABAN —el flujo de asks propios lo sobrescribía
-- con `#ask_queue`/0 y borraba la cuenta de los asks ajenos aún pendientes, y a la
-- inversa—:
--   · la cola PROPIA, DERIVADA en vivo de `#self.ask_queue` (asks encolados) más el
--     que esté visible en el modal (`current_modal`). Al derivarla no hay estado que
--     otro flujo pueda machacar.
--   · `self.pending_foreign`, el contador de asks de OTRAS sesiones (G3: no abren
--     modal, solo suben este indicador). Se incrementa en la rama ajena de
--     `agent:permission.asked` y se decrementa (con guard a 0) en `agent:permission.
--     denied`.
--
-- ASIMETRÍA CONOCIDA del contador ajeno: el agente NO emite ningún evento observable
-- cuando un ask se CONCEDE (`agent.permission.respond` solo resuelve el future del
-- lado del agente; el único evento del ciclo de vida además de `permission.asked` es
-- `permission.denied`, que emite el flujo de tools al denegar). Por eso solo podemos
-- descontar los asks ajenos DENEGADOS por el usuario; los concedidos por otra sesión
-- siguen sumados hasta que el chat cambie de sesión (`switch_session` re-suscribe) o
-- se cierre. No añadimos un evento nuevo al agente para cerrar el hueco: sería
-- cambiar su contrato por una mejora cosmética de esta statusline.
function Chat:_pending_total()
  local own = #(self.ask_queue or {}) + (self.current_modal ~= nil and 1 or 0)
  return own + (self.pending_foreign or 0)
end

-- Chat:_update_statusline() recompone los segmentos (chat.md §6) y los vuelca a los
-- labels. El ctx lo arma con el estado vivo de la sesión (modelo, usage, permisos).
function Chat:_update_statusline()
  local ctx = {
    model = self.session.model,
    usage = self.session.usage,
    context_window = self.context_window,
    cost_usd = (self.session.usage or {}).cost_usd,
    cwd = self.session.cwd,
    perms_mode = self.perms_mode,
    pending_asks = self:_pending_total(),
    thinking = (self.session.thinking_mode and self.session:thinking_mode()) or "off",
  }
  -- Construye los SPANS coloreados de un lado de la barra (chat.md §6): cada segmento
  -- aporta su `{text, style}` y se separan con un `·` atenuado. Un segmento puede
  -- devolver un string suelto (compat) o "" para ocultarse. Todos los spans llevan
  -- `bg = "bg_surface"` para que la barra sea un fondo continuo (el `box` ya pinta el
  -- relleno; el bg en los spans evita cortes bajo el texto).
  local function side_spans(side)
    local spans = {}
    local function sep()
      if #spans > 0 then
        spans[#spans + 1] = { text = "  ·  ", style = { fg = "border", bg = "bg_surface" } }
      end
    end
    for _, seg in ipairs(statusline.ordered(side)) do
      local ok, s = pcall(seg.render, ctx)
      if ok and s ~= nil and s ~= "" then
        local text, style
        if type(s) == "table" then
          text, style = tostring(s.text or ""), s.style or {}
        else
          text, style = tostring(s), {}
        end
        if text ~= "" then
          style = { fg = style.fg, bold = style.bold, italic = style.italic,
            bg = "bg_surface" }
          sep()
          spans[#spans + 1] = { text = text, style = style }
        end
      end
    end
    return spans
  end
  self.status_left:set_spans(side_spans("left"))
  self.status_right:set_spans(side_spans("right"))
end

-- Chat:_place_cursor() coloca el cursor REAL del terminal en el caret del editor
-- (api.md §9.1: Region:cursor). El `toolkit.app` solo coloca el cursor en la
-- columna del foco (una fila); como el editor es MULTILÍNEA, el chat lo recoloca
-- tras pintar sumando `caret_row` (la fila del caret dentro del Block del editor).
function Chat:_place_cursor()
  if not self.app._alive then
    return
  end
  if self.app.focused == self.input and self.input.caret_col then
    local ax, ay = self.app:_abs(self.input)
    self.app.region:cursor(ax + self.input:caret_col(), ay + self.input:caret_row())
  end
end

-- Chat:_repaint() repinta y recoloca el cursor multilínea. El toolkit ya repinta
-- por nodos sucios; tras eso ajustamos el cursor (su colocación multilínea es del
-- chat, no del toolkit S42).
function Chat:_repaint()
  self.app:paint()
  self:_place_cursor()
end

-- Chat:open_modal(widget, opts?) / Chat:close_modal() muestran/ocultan una capa
-- modal (chat.md §1/§5: diálogo de permisos, pickers). El widget interno se ENMARCA
-- en un `toolkit.box` (borde + título + fondo de overlay) CENTRADO sobre la columna,
-- en vez de plano contra el margen: la firma de un modal de producto. El foco va al
-- widget INTERNO (focusable) aunque el contenedor sea la caja. `opts`: `title`
-- (cabecera del marco), `height` (alto en filas del contenido; el marco añade 2).
function Chat:open_modal(widget, opts)
  opts = opts or {}
  local box = toolkit.box({
    id = "modal-frame", child = widget, border = "rounded",
    title = opts.title, pad = { 0, 1 }, bg = "overlay",
  })
  -- tamaño del panel: ancho acotado y centrado; alto = contenido + borde, sin pasar
  -- de la pantalla. El `modal_layer` (vbox con justify/align center) lo centra.
  local aw = self.app.w or 80
  local ah = self.app.h or 24
  box.pref_w = math.max(24, math.min(76, aw - 8))
  box.pref_h = math.max(3, math.min(ah - 4, (opts.height or 6) + 2))
  self._modal_frame = box
  self.modal_layer:add(box)
  self.app:relayout()
  if widget.focusable then
    self.app:set_focus(widget)
  end
  self:_repaint()
end

function Chat:close_modal(_widget)
  -- siempre quitamos el marco (envuelve al widget interno, que se va con él).
  if self._modal_frame then
    if self._modal_frame.dispose then self._modal_frame:dispose() end
    self.modal_layer:remove(self._modal_frame)
    self._modal_frame = nil
  end
  self.app:relayout()
  -- el foco vuelve al editor.
  self.app:set_focus(self.input)
  self:_repaint()
end

-- Chat:open_file_picker() abre el picker difuso de ficheros (menciones `@`,
-- chat.md §3 / P26). Lista el repo con `nu.search.files` (⏸, en una task) y, al
-- elegir, INYECTA la ruta en el editor —el agente decide leerla, no se incrusta
-- el contenido—. No abre si ya hay un modal (un picker o un diálogo de permiso).
function Chat:open_file_picker()
  if self.current_modal ~= nil or self._picker ~= nil then
    return
  end
  nu.task.spawn(function()
    local ok, files = pcall(nu.search.files, self.session.cwd or nu.fs.cwd(), { max = 2000 })
    if not ok or type(files) ~= "table" then files = {} end
    local pick
    pick = picker_mod.new({
      title = "Mencionar fichero (@)",
      candidates = files,
      on_select = function(path)
        self.input:insert(path)
        self._picker = nil
        self:close_modal(pick)
      end,
      on_cancel = function()
        self._picker = nil
        self:close_modal(pick)
      end,
    })
    self._picker = pick
    self:open_modal(pick, { title = "Mencionar fichero (@)", height = 12 })
  end)
end

-- Chat:open_command_picker() abre el autocompletado visual de comandos `/`
-- (chat.md §3 / P29). Candidatos = nombres de comando (commands.list); el prefijo
-- ya tecleado tras la `/` pre-filtra. Al elegir, deja `"/<name> "` en el editor.
function Chat:open_command_picker()
  if self.current_modal ~= nil or self._picker ~= nil then
    return
  end
  local val = self.input:value()
  local prefix = val:match("^/(%S*)") or ""
  local names = {}
  for _, c in ipairs(commands.list()) do
    names[#names + 1] = c.name
  end
  local pick
  pick = picker_mod.new({
    title = "Comando (/)",
    candidates = names,
    query = prefix,
    on_select = function(name)
      self.input:set_value("/" .. name .. " ")
      self._picker = nil
      self:close_modal(pick)
    end,
    on_cancel = function()
      self._picker = nil
      self:close_modal(pick)
    end,
  })
  self._picker = pick
  self:open_modal(pick, { title = "Comando (/)", height = 12 })
end

-- Chat:submit() ENVÍA el contenido del editor (chat.md §3: enter envía). Si el
-- texto es un comando slash (§4), lo despacha; si no, lo manda al agente
-- (Session:send) en una task —el turno SUSPENDE y su streaming llega por eventos—.
-- Vacía el editor y empuja al historial de entrada. No envía si está vacío o si ya
-- hay un turno en vuelo del lado del editor (la reentrada del agente la maneja
-- Session:send, G4; aquí evitamos disparar dos envíos de UI a la vez).
function Chat:submit()
  if self.input:is_empty() then
    return
  end
  local text = self.input:value()
  -- historial de entrada (chat.md §3, ↑/↓): empuja y resetea el cursor de historia.
  self.input_history[#self.input_history + 1] = text
  self.history_cursor = #self.input_history + 1
  self.input:clear()
  self:_repaint()

  -- ¿comando slash? (chat.md §3/§4). Se ejecuta en una task (el handler ⏸).
  if commands.parse(text) then
    nu.task.spawn(function()
      local ctx = self:command_ctx()
      local _handled, message = commands.dispatch(text, ctx)
      if message ~= nil and message ~= "" then
        self.transcript:add_system(message)
        self:_refresh_transcript()
        self:_repaint()
      end
    end)
    return
  end

  -- Mensaje normal: al transcript como "usuario" y al agente (chat.md §2/§3).
  self.transcript:add_user(text)
  self.transcript:begin_assistant()
  self:_refresh_transcript()
  self:_repaint()

  nu.task.spawn(function()
    self:_set_busy(true, "Pensando…")
    local ok, err = pcall(function()
      return self.session:send(text)
    end)
    self:_set_busy(false)
    if not ok then
      -- error del turno (el adaptador, max_turns…): al transcript como error
      -- (los eventos agent:error ya lo pintan; esto cubre un fallo de send mismo).
      local msg = (type(err) == "table" and err.message) or tostring(err)
      self.transcript:add_error(msg)
      self:_refresh_transcript()
      self:_repaint()
    end
  end)
end

-- Chat:command_ctx() arma el contexto que reciben los handlers de comandos slash
-- (chat.md §4): la sesión, el chat y los módulos consumidos.
function Chat:command_ctx()
  return {
    chat = self,
    session = self.session,
    agent = agent,
    providers = providers,
    sessions = sessions,
  }
end

-- Chat:cancel_turn() cancela el turno en curso (chat.md §3: esc cancela). Delega en
-- Session:cancel (agente.md §2). No vacía la cola de reentrada (eso es aparte, G4).
function Chat:cancel_turn()
  -- En modo degradado (arranque sin sesión, chat.md §8/G35) no hay turno que
  -- cancelar: `self.session` es nil. El guard lo tolera (lo invoca `quit`).
  if self.session and self.session.cancel then
    pcall(self.session.cancel, self.session)
  end
  self:_set_busy(false)
end

-- Chat:switch_session(new_session) cambia la sesión ACTIVA del chat (lo usa
-- `/fork`, P28: bifurca y continúa en la rama). Suelta las suscripciones `agent:`
-- de la sesión vieja y re-suscribe a la nueva (su filtro por id, G3), y refresca
-- el contexto/statusline. NO toca `global_subs` (ui:resize) ni los keymaps.
function Chat:switch_session(new_session)
  for _, s in ipairs(self.subs or {}) do
    if s and s.cancel then s:cancel() end
  end
  self.subs = {}
  self.session = new_session
  self.perms_mode = (new_session.permissions and new_session.permissions.mode) or self.perms_mode
  self:_subscribe_agent()
  local okr, resolved = pcall(providers.resolve, new_session.model)
  if okr and type(resolved) == "table" and type(resolved.config) == "table"
      and type(resolved.config.model) == "table" then
    self.context_window = resolved.config.model.context or self.context_window
  end
  self:_update_statusline()
  self:_repaint()
end

-- Chat:history_prev()/history_next() recorren el historial de ENTRADA (chat.md §3,
-- ↑/↓ en el borde del editor). Cargan el mensaje previo/siguiente en el editor.
function Chat:history_prev()
  if #self.input_history == 0 then return end
  self.history_cursor = math.max(1, (self.history_cursor or (#self.input_history + 1)) - 1)
  self.input:set_value(self.input_history[self.history_cursor] or "")
  self:_repaint()
end

function Chat:history_next()
  if #self.input_history == 0 then return end
  self.history_cursor = (self.history_cursor or #self.input_history) + 1
  if self.history_cursor > #self.input_history then
    self.history_cursor = #self.input_history + 1
    self.input:clear()
  else
    self.input:set_value(self.input_history[self.history_cursor] or "")
  end
  self:_repaint()
end

-- ---------------------------------------------------------------------------
-- Eventos del agente (chat.md §2): el render del turno.
-- ---------------------------------------------------------------------------

-- Chat:_subscribe_agent() suscribe los eventos `agent:*` (api.md §4, bus del
-- core), filtrando por la sesión ACTIVA (G3: chat pinta solo los eventos cuyo
-- `session` es la sesión visible; la actividad de otras va a la statusline). Cada
-- suscripción es un `Sub` (api.md §4) que se cancela al cerrar el chat (sin
-- handlers huérfanos, G2). Los handlers son SÍNCRONOS (api.md §4): solo tocan el
-- modelo del transcript y piden repintado (barato); nada que suspenda.
function Chat:_subscribe_agent()
  local sid = self.session.id
  local subs = self.subs

  local function mine(p)
    return type(p) == "table" and p.session == sid
  end

  subs[#subs + 1] = nu.events.on("agent:turn.start", function(p)
    if not mine(p) then return end
    self.transcript:begin_assistant()
    self:_refresh_transcript()
    self:_repaint()
  end)

  -- agent:delta — el STREAMING (chat.md §2). El payload es el Event canónico
  -- (providers.md §2.3) re-emitido por el agente: `ev.type` "text"/"thinking"/
  -- "tool_call.*"/"usage" (más `session`). El texto se acumula en el item en curso
  -- y el transcript re-renderiza su markdown: el widget CRECE incrementalmente.
  subs[#subs + 1] = nu.events.on("agent:delta", function(p)
    if not mine(p) then return end
    if p.type == "text" and p.text then
      self.transcript:append_delta(p.text)
      self:_refresh_transcript()
      self:_repaint()
    elseif p.type == "thinking" and p.text then
      self.transcript:append_thinking(p.text)
      self:_refresh_transcript()
      self:_repaint()
    elseif p.kind == "usage" or p.type == "usage" then
      -- usage en vivo: refresca la statusline (% de contexto, coste).
      self:_update_statusline()
      self:_repaint()
    end
  end)

  -- agent:message — SELLA el mensaje con el Message del done (chat.md §2).
  subs[#subs + 1] = nu.events.on("agent:message", function(p)
    if not mine(p) then return end
    self.transcript:seal_assistant(p.message)
    self:_refresh_transcript()
    self:_update_statusline()
    self:_repaint()
  end)

  -- agent:tool.start / tool.end — bloques de tool (chat.md §2).
  subs[#subs + 1] = nu.events.on("agent:tool.start", function(p)
    if not mine(p) then return end
    self.transcript:add_tool(p.id, p.name, p.args)
    -- el spinner refleja la fase: "Ejecutando <tool>…".
    if self.activity and self.activity.visible then
      self:_set_busy(true, "Ejecutando " .. tostring(p.name or "tool") .. "…")
    end
    self:_refresh_transcript()
    self:_repaint()
  end)
  subs[#subs + 1] = nu.events.on("agent:tool.end", function(p)
    if not mine(p) then return end
    self.transcript:tool_end(p.id, p.is_error, p.error)
    if self.activity and self.activity.visible then
      self:_set_busy(true, "Pensando…")
    end
    self:_refresh_transcript()
    self:_repaint()
  end)

  -- agent:tool.progress — progreso EN VIVO de una tool larga (chat.md §2 / P27).
  subs[#subs + 1] = nu.events.on("agent:tool.progress", function(p)
    if not mine(p) then return end
    self.transcript:tool_progress(p.id, p.text)
    self:_refresh_transcript()
    self:_repaint()
  end)

  -- agent:compact — marca de "historia compactada arriba" (chat.md §2 / P27).
  subs[#subs + 1] = nu.events.on("agent:compact", function(p)
    if not mine(p) then return end
    self.transcript:add_compact_marker()
    self:_refresh_transcript()
    self:_update_statusline()
    self:_repaint()
  end)

  -- agent:error — bloque de error (chat.md §2).
  subs[#subs + 1] = nu.events.on("agent:error", function(p)
    if not mine(p) then return end
    self.transcript:add_error(p.message or "error del agente")
    self:_refresh_transcript()
    self:_repaint()
  end)

  -- agent:permission.asked — diálogo modal (chat.md §5). Se encola FIFO; un solo
  -- modal visible (chat.md §1/§5). Los asks de OTRAS sesiones suben el contador de
  -- la statusline (G3) pero no abren modal aquí.
  subs[#subs + 1] = nu.events.on("agent:permission.asked", function(p)
    if not mine(p) then
      -- ask de otra sesión: indicador en la statusline (G3). Campo SEPARADO de la
      -- cola propia (ver Chat:_pending_total) para que los dos flujos no se pisen.
      self.pending_foreign = (self.pending_foreign or 0) + 1
      self:_update_statusline()
      self:_repaint()
      return
    end
    self:_enqueue_ask(p)
  end)

  -- agent:permission.denied — un ask AJENO que la otra sesión resolvió DENEGANDO
  -- (G3): descuenta el indicador. Es el único evento del ciclo de vida de un ask que
  -- podemos observar al resolverse (el CONCEDIDO no emite evento: ver la asimetría
  -- documentada en Chat:_pending_total). `source == "user"` distingue la denegación
  -- de un diálogo —la que sí incrementó el contador— de las denegaciones por política
  -- (deny/default/hook/headless), que nunca emitieron `permission.asked`. Guard a 0.
  subs[#subs + 1] = nu.events.on("agent:permission.denied", function(p)
    if mine(p) then return end
    if type(p) ~= "table" or p.source ~= "user" then return end
    self.pending_foreign = math.max(0, (self.pending_foreign or 0) - 1)
    self:_update_statusline()
    self:_repaint()
  end)
end

-- Chat:_enqueue_ask(p) encola un ask y abre el modal si no hay otro visible
-- (chat.md §5: cola FIFO, un modal visible). El diálogo (chat.permission) pinta la
-- tool y los args y responde con agent.permission.respond.
function Chat:_enqueue_ask(p)
  self.ask_queue[#self.ask_queue + 1] = p
  -- el total propio se deriva de #ask_queue + el modal (Chat:_pending_total).
  self:_update_statusline()
  if self.current_modal == nil then
    self:_show_next_ask()
  else
    self:_repaint()
  end
end

function Chat:_show_next_ask()
  local p = table.remove(self.ask_queue, 1)
  if p == nil then
    self.current_modal = nil
    self:_update_statusline()
    self:close_modal(nil)
    return
  end
  local dialog = permdialog.new({
    tool = p.tool,
    args = p.args,
    suggested = p.suggested,
    on_respond = function(action)
      -- "permitir siempre" (P29): añade el patrón a la política de la sesión, y
      -- con la variante global lo persiste a agent.toml (chat.md §5). Ambas tocan
      -- disco (⏸: store de la sesión, fichero global), así que van en una task; la
      -- respuesta al agente y la UI siguen síncronas (agente.md §5).
      local granted = (action ~= "deny")
      if (action == "always" or action == "always_global") and p.suggested then
        nu.task.spawn(function()
          pcall(function() self.session:allow(p.suggested) end)
          if action == "always_global" then
            pcall(function() agent.permission.persist_allow(p.suggested) end)
          end
        end)
      end
      agent.permission.respond(p.id, granted)
      local label = ({ once = "concedido (una vez)", always = "concedido (siempre)",
        always_global = "concedido (siempre, global)", deny = "denegado" })[action] or "denegado"
      self.transcript:add_system(string.format("permiso para %q: %s", tostring(p.tool), label))
      -- quita el MARCO del modal (envuelve al diálogo), no el diálogo suelto.
      if self._modal_frame then
        if self._modal_frame.dispose then self._modal_frame:dispose() end
        self.modal_layer:remove(self._modal_frame)
        self._modal_frame = nil
      end
      self.current_modal = nil
      self:_refresh_transcript()
      self:_show_next_ask()
    end,
  })
  self.current_modal = dialog
  -- el total propio (cola + este modal) lo deriva Chat:_pending_total.
  self:_update_statusline()
  self:open_modal(dialog, { title = "Permiso requerido", height = 8 })
end

-- ---------------------------------------------------------------------------
-- Construcción de la UI (chat.md §1) y arranque (chat.md §8).
-- ---------------------------------------------------------------------------

-- Chat:_build_ui(opts) monta el árbol de widgets (chat.md §1): un vbox con
-- transcript (flex) / input / statusline, y un stack que superpone la columna y la
-- capa modal. La raíz de la app es ese stack (la capa modal va "encima").
function Chat:_build_ui(opts)
  -- la columna principal (transcript / actividad / input enmarcado / statusline).
  local column = toolkit.vbox({ id = "chat-column" })

  self.transcript_widget = toolkit.text({ id = "transcript", markdown = true })
  self.transcript_widget.flex = 1  -- ocupa el alto sobrante (chat.md §1)

  -- Fila de ACTIVIDAD (chat.md §2): un spinner animado mientras el turno corre,
  -- oculto en reposo. Es lo que evita la sensación de "terminal muerta" entre el
  -- envío y el primer delta. La anima `nu.task.every` (toolkit.spinner).
  self.activity = toolkit.spinner({ id = "activity", label = "", color = "accent" })
  self.activity.pref_h = 1
  self.activity:set_visible(false)

  -- Editor ENMARCADO con un prompt "› " (la firma visual del harness, al estilo de
  -- la caja de entrada de Claude Code). El editor multilínea vive dentro de un `box`
  -- (borde redondeado, realce de foco) junto a un label-prompt.
  self.input = chat_input.new({
    id = "input",
    placeholder = "Escribe un mensaje · enter envía · shift+enter nueva línea · @ ficheros · /help",
    on_history_prev = function() self:history_prev() end,
    on_history_next = function() self:history_next() end,
    on_mention = function() self:open_file_picker() end,
    on_change = function() self:_sync_input_height() end,
  })
  self.input.flex = 1
  self._input_max_lines = opts.input_height or 6
  self._prompt = toolkit.label({ id = "prompt", text = "› ",
    style = { fg = "accent", bold = true } })
  self._prompt.pref_w = 2
  local input_row = toolkit.hbox({ id = "input-row" })
  input_row:add(self._prompt)
  input_row:add(self.input)
  self.input_box = toolkit.box({ id = "input-box", child = input_row,
    border = "rounded", pad = { 0, 1 } })
  self.input_box.pref_h = 3  -- 1 línea visible + borde; crece con _sync_input_height

  -- STATUSLINE como BARRA (chat.md §6): un fondo `bg_surface` a todo lo ancho y dos
  -- richtext (izquierda/derecha) con segmentos coloreados por el theme. El `box`
  -- sin borde pero con `bg` pinta el fondo continuo; los richtext se blittean encima.
  self.status_left = toolkit.richtext({ id = "status-left" })
  self.status_left.flex = 1
  self.status_right = toolkit.richtext({ id = "status-right",
    align = "right", fill_bg = "bg_surface" })
  self.status_right.flex = 1
  local status_row = toolkit.hbox({ id = "status-row" })
  status_row:add(self.status_left)
  status_row:add(self.status_right)
  self.status_box = toolkit.box({ id = "status-box", child = status_row,
    border = "none", bg = "bg_surface", pad = { 0, 1 } })
  self.status_box.pref_h = 1

  column:add(self.transcript_widget)
  column:add(self.activity)
  column:add(self.input_box)
  column:add(self.status_box)

  -- la raíz: un stack con la columna y (encima) la capa modal (chat.md §1).
  local root = toolkit.stack({ id = "chat-root" })
  root:add(column)
  -- la capa modal CENTRA su contenido (un panel enmarcado) sobre la columna
  -- (chat.md §1/§5): justify/align center colocan la caja del modal en el medio.
  self.modal_layer = toolkit.vbox({ id = "modal-layer", justify = "center", align = "center" })
  self.modal_layer:set_visible(true)
  root:add(self.modal_layer)

  -- la app (chat.md §1): vincula el árbol a una región a pantalla completa,
  -- enruta el input al foco y repinta por nodos sucios (S42). manage_input por
  -- defecto: la app apila su on_input y entrega al foco (el editor). El foco
  -- arranca en el editor.
  self.app = toolkit.app({ root = root, theme = opts.theme })
  self.app:set_focus(self.input)
  self:_sync_input_height()
end

-- Chat:_welcome_md() -> string. El markdown de la pantalla de bienvenida (chat.md
-- §8): identifica el harness, el modelo y el cwd activos, y recuerda las pistas
-- mínimas. El render lo colorea el theme del markdown (G22) — encabezado en acento,
-- etc.—, así que la primera pantalla ya se ve como producto, no en blanco.
function Chat:_welcome_md()
  local v = nu.version
  local model = (self.session and self.session.model) or "?"
  local cwd = (self.session and self.session.cwd) or nu.fs.cwd()
  return table.concat({
    "# ✻ Bienvenido a nu",
    "",
    string.format("Harness de código sobre el runtime `nu` %d.%d.%d (API %d).",
      v.major, v.minor, v.patch, v.api),
    "",
    "- **Modelo:** `" .. model .. "`",
    "- **Directorio:** `" .. cwd .. "`",
    "",
    "Escribe tu mensaje abajo y pulsa `enter`. Atajos útiles:",
    "",
    "- `/help` — lista de comandos · `/model` — cambiar modelo · `/sessions` — reanudar",
    "- `@` — mencionar un fichero · `shift+enter` — nueva línea · `esc` — interrumpir el turno",
    "",
    "> *Empieza pidiendo algo: \"explica este repo\", \"añade un test a X\"…*",
  }, "\n")
end

-- Chat:_set_busy(on, label) muestra/oculta la fila de actividad (el spinner) y la
-- anima mientras un turno está en vuelo (chat.md §2). Cambiar la visibilidad mueve el
-- layout, así que rehace el layout y repinta. `label` describe la fase ("Pensando…",
-- "Ejecutando bash…"). Idempotente en lo esencial (start/stop del spinner lo son).
function Chat:_set_busy(on, label)
  if not (self.activity and self.app and self.app._alive) then
    return
  end
  if on then
    self.activity:set_label((label or "Pensando…") .. "  ·  esc para interrumpir")
    if not self.activity.visible then
      self.activity:set_visible(true)
      self.app:relayout()
    end
    self.activity:start()
  else
    self.activity:stop()
    if self.activity.visible then
      self.activity:set_visible(false)
      self.app:relayout()
    end
  end
  self:_repaint()
end

-- Chat:_sync_input_height() ajusta el alto de la caja del editor al contenido: crece
-- al añadir líneas (hasta `_input_max_lines`) y encoge al borrarlas. La caja añade 2
-- filas de borde. Solo rehace el layout si el alto cambió (evita trabajo por tecla).
function Chat:_sync_input_height()
  if not (self.input_box and self.app and self.app._alive) then
    return
  end
  local lines = self.input:content_height() or 1
  local want = math.max(1, math.min(self._input_max_lines or 6, lines)) + 2
  if want ~= self.input_box.pref_h then
    self.input_box.pref_h = want
    self.app:relayout()
  end
  self:_repaint()
end

-- Chat:_install_keymaps() registra los atajos GLOBALES del chat (chat.md §7):
-- esc cancela el turno, ctrl+c cierra. Usan `nu.ui.keymap` (api.md §9.3), por
-- encima del on_input de la app (el más reciente gana): así esc/ctrl+c funcionan
-- aunque el editor tenga el foco. El editor maneja enter/shift+enter por su on_key
-- (no como keymap global: dependen del foco del editor). Remapeables vía chat.keys.
function Chat:_install_keymaps()
  self.keymaps = self.keymaps or {}
  local km = self.keymaps

  km[#km + 1] = nu.ui.keymap(M.keys.cancel, function()
    -- con un modal/picker abierto, esc lo maneja el propio modal (deny/cancelar):
    -- el keymap global se aparta (devuelve false → la app lo enruta al foco).
    if self.current_modal ~= nil or self._picker ~= nil then
      return false
    end
    self:cancel_turn()
    return true
  end)
  km[#km + 1] = nu.ui.keymap(M.keys.quit, function()
    self:quit()
    return true
  end)
  -- enviar: el editor deja pasar enter "pelado" (chat.md §3); un keymap global lo
  -- recoge y envía. (El editor consume shift/alt+enter como nueva línea, así que
  -- este keymap solo dispara con enter sin modificadores.)
  km[#km + 1] = nu.ui.keymap(M.keys.submit, function()
    if self.current_modal == nil and self._picker == nil then
      self:submit()
      return true
    end
    return false
  end)
  -- autocompletado de comandos `/` (P29): tab con un `/...` en el editor abre el
  -- picker de comandos. Sin `/` (o con un modal abierto), el keymap se aparta.
  km[#km + 1] = nu.ui.keymap(M.keys.complete, function()
    if self.current_modal == nil and self._picker == nil
        and self.input:value():sub(1, 1) == "/" then
      self:open_command_picker()
      return true
    end
    return false
  end)
end

-- Chat:quit() cierra el chat (chat.md §4 /quit): cancela el turno, suelta las
-- suscripciones a `agent:*` (sin huérfanos, G2), desmonta los keymaps, cierra la
-- app (su región/on_input) y cierra la sesión (suelta el lock, sesiones.md §6).
-- Idempotente.
function Chat:quit()
  if self._closed then
    return
  end
  self._closed = true
  self:cancel_turn()
  if self.activity then self.activity:stop() end
  for _, s in ipairs(self.subs or {}) do
    if s and s.cancel then s:cancel() end
  end
  self.subs = {}
  for _, s in ipairs(self.global_subs or {}) do
    if s and s.cancel then s:cancel() end
  end
  self.global_subs = {}
  for _, k in ipairs(self.keymaps or {}) do
    if k and k.unmap then k:unmap() end
  end
  self.keymaps = {}
  -- suelta las suscripciones al foco de las cajas (sin handlers huérfanos, G2).
  for _, b in ipairs({ self.input_box, self.status_box }) do
    if b and b.dispose then b:dispose() end
  end
  if self.app then
    self.app:close()
  end
  if self.session and self.session.close then
    pcall(self.session.close, self.session)
  end
  -- El chat ES el producto: cerrarlo APAGA el runtime (chat.md §8). Sin esto, salir
  -- del chat dejaba el binario vivo —y, con el conjunto oficial, el REPL montado
  -- debajo (G36)—, esa sensación de "salir de una capa para caer en otra". El driver
  -- de TTY convierte `core:shutdown` en apagado limpio (driver.go §4); es el mismo
  -- canal que usa el arranque degradado. Idempotente: emitir dos veces no daña.
  nu.events.emit("core:shutdown")
end

-- ---------------------------------------------------------------------------
-- chat.start (chat.md §8): el arranque.
-- ---------------------------------------------------------------------------

-- M._start_degraded(err, opts) monta el ARRANQUE DEGRADADO del chat (chat.md §8,
-- ADR-017/G35): cuando la sesión inicial no es construible por falta o rotura de
-- config, en vez de morir al log (donde el usuario no lo ve) se monta una UI MÍNIMA
-- ACCIONABLE —explica cómo configurar el modelo/provider y la API key— y SALIBLE
-- —esc/q/ctrl+c emiten `core:shutdown`, que el driver convierte en apagado (driver.go
-- §4 endosa que una UI Lua mapee su tecla de salida a ese evento)—. Devuelve un handle
-- de Chat en MODO DEGRADADO: sin sesión ni suscripciones a `agent:*`; `quit` lo
-- desmonta igual (los guards toleran la sesión nil). No suspende.
function M._start_degraded(err, opts)
  opts = opts or {}
  local msg = (type(err) == "table" and err.message) or tostring(err)
  local dir = nu.config.dir()

  local body = table.concat({
    "# nu — configuración necesaria",
    "",
    "No se pudo abrir la sesión del agente:",
    "",
    "    " .. msg,
    "",
    "Para chatear necesitas un **modelo** y un **provider** configurados en `"
      .. dir .. "`:",
    "",
    "1. `agent.toml` → `model = \"anthropic/opus\"`",
    "2. `providers.toml` → el provider y su `api_key_env`",
    "3. Exporta tu API key, p. ej. `export ANTHROPIC_API_KEY=...`",
    "",
    "Atajo: **`nu --default-config`** deja ambas plantillas listas.",
    "",
    "Pulsa `esc`, `q` o `ctrl+c` para salir.",
  }, "\n")

  local text = toolkit.text({ id = "config-help", markdown = true })
  text.flex = 1
  text:set_text(body)

  local column = toolkit.vbox({ id = "config-column" })
  column:add(text)

  -- `toolkit.app` hace el primer layout y pide pintura sola (app.lua): no hay que
  -- repintar a mano. Sin widget enfocable, su `on_input` deja pasar las teclas a los
  -- keymaps de abajo.
  local app = toolkit.app({ root = column, theme = opts.theme })

  local self = setmetatable({
    degraded    = true,
    app         = app,
    subs        = {},
    global_subs = {},
    keymaps     = {},
    _closed     = false,
  }, Chat)

  -- Salida: esc/q/ctrl+c → core:shutdown. Apilados DESPUÉS de la app (más arriba en
  -- la pila), así reciben la tecla que la app dejó pasar.
  local function bye()
    nu.events.emit("core:shutdown")
    return true
  end
  for _, seq in ipairs({ "esc", "q", "ctrl+c" }) do
    self.keymaps[#self.keymaps + 1] = nu.ui.keymap(seq, bye)
  end

  M._active = self
  return self
end

-- chat.start(opts?) ⏸ -> Chat. Arranca el chat (chat.md §8). SUSPENDE: crea o
-- reanuda la sesión del agente (lee disco). Exige `nu.ui` (TTY interactivo, G20):
-- en headless es EINVAL accionable (chat.md §8). opts (todos opcionales):
--   - model: "proveedor/modelo" (default: agent.toml). resume: id (reanuda, G18).
--   - cwd, permissions, system, max_turns: pasan a agent.session.
--   - theme: el Theme del toolkit (default toolkit.theme.default, G22).
--   - no_store: NO persistir (tests in-memory).
function M.start(opts)
  opts = opts or {}
  if not nu.has("ui") then
    error({ code = "EINVAL",
      message = "chat.start: no hay UI (headless, G20). El chat necesita un TTY "
        .. "interactivo; comprueba nu.has(\"ui\") antes (chat.md §8). Para uso "
        .. "headless usa el agente directamente (agent.session/Session:send)." })
  end

  -- segmentos y comandos por defecto (dogfooding, chat.md §4/§6). Idempotente.
  statusline._reset()
  statusline.install_defaults()
  commands._reset()
  commands.install_builtins({ agent = agent, providers = providers, sessions = sessions })

  -- la sesión del agente (chat.md §8): crea o reanuda. El chat consume la API
  -- pública del agente igual que un tercero (agente.md §1). Si NO se puede construir
  -- por falta o rotura de config (no hay modelo, provider/modelo no resoluble, TOML
  -- roto), arranca DEGRADADO: una UI accionable y salible en vez de morir al log
  -- (chat.md §8, ADR-017/G35). Un error inesperado (no de config) se propaga.
  local ok, session_or_err = pcall(agent.session, {
    model       = opts.model,
    resume      = opts.resume,
    cwd         = opts.cwd,
    system      = opts.system,
    permissions = opts.permissions,
    max_turns   = opts.max_turns,
    no_store    = opts.no_store,
  })
  if not ok then
    local err = session_or_err
    local code = type(err) == "table" and err.code or nil
    if CONFIG_ERROR_CODES[code] then
      return M._start_degraded(err, opts)
    end
    error(err) -- inesperado: que lo registre el init como hoy
  end
  local session = session_or_err

  local self = setmetatable({
    session       = session,
    transcript    = transcript_mod.new(),
    subs          = {},
    global_subs   = {},
    keymaps       = {},
    ask_queue     = {},
    current_modal = nil,
    pending_foreign = 0, -- asks de OTRAS sesiones (G3); la cola propia se deriva
    input_history = {},
    history_cursor = 1,
    perms_mode    = (opts.permissions and opts.permissions.mode) or "ask",
    context_window = opts.context_window, -- para el % de contexto (chat.md §6)
    _closed       = false,
    _renderers    = tool_renderers,
  }, Chat)

  -- intenta conocer la ventana de contexto del modelo (para el % de la statusline,
  -- chat.md §6). providers.resolve trae el ModelInfo; tolera fallos (no es crítico).
  local okr, resolved = pcall(providers.resolve, session.model)
  if okr and type(resolved) == "table" and type(resolved.config) == "table"
      and type(resolved.config.model) == "table" then
    self.context_window = self.context_window or resolved.config.model.context
  end

  self:_build_ui(opts)
  self:_subscribe_agent()
  self:_install_keymaps()

  -- si reanudamos, el historial del agente ya está repoblado (G18): lo reflejamos
  -- en el transcript para que la conversación previa se vea (chat.md §8).
  if opts.resume then
    for _, msg in ipairs(session.history or {}) do
      if type(msg) == "table" and type(msg.content) == "table" then
        if msg.role == "user" then
          local txt = ""
          for _, b in ipairs(msg.content) do
            if b.type == "text" then txt = txt .. b.text end
          end
          if txt ~= "" then self.transcript:add_user(txt) end
        elseif msg.role == "assistant" then
          self.transcript:seal_assistant(msg)
        end
      end
    end
  end

  -- ui:resize (api.md §9.1, "tu región, tu ui:resize"): el chat resuelve su layout
  -- de nuevo al cambiar el tamaño del terminal. La app redimensiona su región y
  -- rehace el layout (S42). Va en `global_subs` (NO en `subs`, que son las del
  -- agente y se re-suscriben al bifurcar, P28): el resize es del chat, no de la sesión.
  self.global_subs = self.global_subs or {}
  self.global_subs[#self.global_subs + 1] = nu.events.on("ui:resize", function(p)
    if not self._closed and self.app then
      self.app:resize(p and p.w, p and p.h)
      self:_refresh_transcript()
      self:_repaint()
    end
  end)

  self:_refresh_transcript()
  self:_update_statusline()
  self:_repaint()

  -- la sesión queda accesible para tests/scripts y para el arranque automático.
  M._active = self
  return self
end

-- M._reset_registries() limpia statusline/commands (tests deterministas). No es
-- contrato público.
function M._reset_registries()
  statusline._reset()
  commands._reset()
end

return M
