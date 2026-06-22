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

local M = {}

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
  cancel  = "esc",       -- cancela el turno en curso (Session:cancel)
  quit    = "ctrl+c",    -- cierra el chat
  submit  = "enter",     -- envía el mensaje (el editor deja pasar enter "pelado")
  newline = "shift+enter", -- nueva línea (lo consume el editor; alt+enter alterno)
}

-- renderer(tool_name, fn): registra el render del resultado de una tool (chat.md
-- §2, renderers enchufables). v1: se guarda; el transcript usa el fallback de
-- texto plano (el render rico por-tool es mejora posterior sobre el mismo item,
-- claude_decisions.md S43).
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
  local md = self.transcript:markdown()
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
    pending_asks = self.pending_count or 0,
  }
  -- une los segmentos de cada lado en un solo texto por label (v1: un label por
  -- lado, separado por " · "). La forma rica (un label por segmento en el hbox) es
  -- una extensión natural; v1 concatena para simplicidad.
  local function side_text(side)
    local parts = {}
    for _, seg in ipairs(statusline.ordered(side)) do
      local ok, s = pcall(seg.render, ctx)
      if ok and s ~= nil and s ~= "" then
        parts[#parts + 1] = tostring(s)
      end
    end
    return table.concat(parts, " · ")
  end
  self.status_left:set_text(side_text("left"))
  self.status_right:set_text(side_text("right"))
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

-- Chat:open_modal(widget) / Chat:close_modal() muestran/ocultan una capa modal
-- (chat.md §1/§5: diálogo de permisos, pickers) en el `stack`. Mientras hay un
-- modal, el foco va a él (chat.md §5). Una sola capa visible a la vez (v1; la cola
-- FIFO de varios asks la lleva la lista `ask_queue`).
function Chat:open_modal(widget)
  self.modal_layer:add(widget)
  self.app:relayout()
  if widget.focusable then
    self.app:set_focus(widget)
  end
  self:_repaint()
end

function Chat:close_modal(widget)
  if widget then
    self.modal_layer:remove(widget)
  end
  self.app:relayout()
  -- el foco vuelve al editor.
  self.app:set_focus(self.input)
  self:_repaint()
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
    local ok, err = pcall(function()
      return self.session:send(text)
    end)
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
  if self.session.cancel then
    pcall(self.session.cancel, self.session)
  end
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
    self:_refresh_transcript()
    self:_repaint()
  end)
  subs[#subs + 1] = nu.events.on("agent:tool.end", function(p)
    if not mine(p) then return end
    self.transcript:tool_end(p.id, p.is_error, p.error)
    self:_refresh_transcript()
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
      -- ask de otra sesión: indicador en la statusline (G3).
      self.pending_count = (self.pending_count or 0) + 1
      self:_update_statusline()
      self:_repaint()
      return
    end
    self:_enqueue_ask(p)
  end)
end

-- Chat:_enqueue_ask(p) encola un ask y abre el modal si no hay otro visible
-- (chat.md §5: cola FIFO, un modal visible). El diálogo (chat.permission) pinta la
-- tool y los args y responde con agent.permission.respond.
function Chat:_enqueue_ask(p)
  self.ask_queue[#self.ask_queue + 1] = p
  self.pending_count = #self.ask_queue
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
    self.pending_count = 0
    self:_update_statusline()
    self:close_modal(nil)
    return
  end
  local dialog = permdialog.new({
    tool = p.tool,
    args = p.args,
    suggested = p.suggested,
    on_respond = function(granted)
      -- responde al agente (agente.md §5) y pasa al siguiente ask de la cola.
      agent.permission.respond(p.id, granted)
      self.transcript:add_system(string.format("permiso para %q: %s",
        tostring(p.tool), granted and "concedido" or "denegado"))
      self.modal_layer:remove(dialog)
      self.current_modal = nil
      self:_refresh_transcript()
      self:_show_next_ask()
    end,
  })
  self.current_modal = dialog
  self.pending_count = #self.ask_queue + 1
  self:_update_statusline()
  self:open_modal(dialog)
end

-- ---------------------------------------------------------------------------
-- Construcción de la UI (chat.md §1) y arranque (chat.md §8).
-- ---------------------------------------------------------------------------

-- Chat:_build_ui(opts) monta el árbol de widgets (chat.md §1): un vbox con
-- transcript (flex) / input / statusline, y un stack que superpone la columna y la
-- capa modal. La raíz de la app es ese stack (la capa modal va "encima").
function Chat:_build_ui(opts)
  -- la columna principal (transcript / input / statusline).
  local column = toolkit.vbox({ id = "chat-column" })

  self.transcript_widget = toolkit.text({ id = "transcript", markdown = true })
  self.transcript_widget.flex = 1  -- ocupa el alto sobrante (chat.md §1)

  self.input = chat_input.new({
    id = "input",
    placeholder = "Escribe un mensaje (enter envía · shift+enter nueva línea · /help)",
    on_history_prev = function() self:history_prev() end,
    on_history_next = function() self:history_next() end,
  })
  self.input.pref_h = opts.input_height or 3  -- alto inicial del editor (chat.md §3)

  -- statusline: un hbox con un label a la izquierda y otro a la derecha (chat.md §6).
  local status_bar = toolkit.hbox({ id = "statusline" })
  self.status_left = toolkit.label({ id = "status-left" })
  self.status_left.flex = 1
  self.status_right = toolkit.label({ id = "status-right" })
  -- el derecho ocupa su contenido; v1 le damos un ancho fijo razonable por flex.
  self.status_right.flex = 1
  status_bar.pref_h = 1
  status_bar:add(self.status_left)
  status_bar:add(self.status_right)

  column:add(self.transcript_widget)
  column:add(self.input)
  column:add(status_bar)

  -- la raíz: un stack con la columna y (encima) la capa modal (chat.md §1).
  local root = toolkit.stack({ id = "chat-root" })
  root:add(column)
  self.modal_layer = toolkit.vbox({ id = "modal-layer" })
  self.modal_layer:set_visible(true)
  root:add(self.modal_layer)

  -- la app (chat.md §1): vincula el árbol a una región a pantalla completa,
  -- enruta el input al foco y repinta por nodos sucios (S42). manage_input por
  -- defecto: la app apila su on_input y entrega al foco (el editor). El foco
  -- arranca en el editor.
  self.app = toolkit.app({ root = root, theme = opts.theme })
  self.app:set_focus(self.input)
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
    if self.current_modal == nil then
      self:submit()
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
  for _, s in ipairs(self.subs or {}) do
    if s and s.cancel then s:cancel() end
  end
  self.subs = {}
  for _, k in ipairs(self.keymaps or {}) do
    if k and k.unmap then k:unmap() end
  end
  self.keymaps = {}
  if self.app then
    self.app:close()
  end
  if self.session and self.session.close then
    pcall(self.session.close, self.session)
  end
end

-- ---------------------------------------------------------------------------
-- chat.start (chat.md §8): el arranque.
-- ---------------------------------------------------------------------------

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
  -- pública del agente igual que un tercero (agente.md §1).
  local session = agent.session({
    model       = opts.model,
    resume      = opts.resume,
    cwd         = opts.cwd,
    system      = opts.system,
    permissions = opts.permissions,
    max_turns   = opts.max_turns,
    no_store    = opts.no_store,
  })

  local self = setmetatable({
    session       = session,
    transcript    = transcript_mod.new(),
    subs          = {},
    keymaps       = {},
    ask_queue     = {},
    current_modal = nil,
    pending_count = 0,
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
  -- rehace el layout (S42).
  self.subs[#self.subs + 1] = nu.events.on("ui:resize", function(p)
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
