package runtime

// Tests de la extensión oficial `chat` (S43, embebida en
// internal/runtime/embedded/chat). Es la **UI oficial del harness**: Lua puro
// sobre la API pública congelada (Fase 8, ADR-003 —el core NO sabe lo que es un
// chat—), construida sobre el toolkit de widgets (S42), el agente (S39), providers
// (S36/S37) y sessions (S38). La prueba arranca un Runtime con las CINCO
// extensiones (toolkit, providers, sessions, agent, chat) activadas por `nu.toml`
// y ejercita el contrato de [chat.md](../../docs/chat.md) desde Lua.
//
// Blinda:
//   - **layout** (§1): una `toolkit.app` con vbox transcript/input/statusline +
//     capa modal; el árbol compone a Blocks inspeccionables (vía el compositor);
//   - **streaming markdown** (§2, el corazón de S43): un `agent:delta` de texto se
//     acumula en el transcript y se pinta con markdown EN STREAMING — el Block del
//     transcript CRECE con el texto; `agent:message` sella;
//   - **input multilínea** (§3): enter envía, shift/alt+enter inserta línea;
//   - **diálogo de permisos** (§5): ante `agent:permission.asked` un modal responde
//     con `agent.permission.respond`;
//   - **CP-11** (dogfooding): una sesión de chat de extremo a extremo contra un
//     **SSE GRABADO** del adaptador anthropic (como CP-9): el usuario "envía" → el
//     agente corre el turno → `agent:delta` streaming se pinta con markdown en el
//     transcript del chat (el Block compuesto crece). El provider REAL (CP-11
//     original) requiere red/credenciales y NO es ejecutable headless en CI
//     (limitación del entorno, documentada en docs/decisiones-implementacion.md S43).
//
// La UI es headless en los tests (sin TTY, G20): el arnés fuerza `nu.ui` con
// `WithForceUI(true)` (como toolkit_test.go) y un tamaño conocido (`WithUISize`)
// para layout determinista. El Block es opaco a Lua (solo `.width`/`.height`): la
// inspección de CONTENIDO se hace en Go mirando la rejilla del compositor
// (`composeRow`, igual que toolkit_test.go).

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// bootChat arranca un Runtime con toolkit+providers+sessions+agent+chat activadas
// por nu.toml, `nu.ui` forzada (headless, G20) y un tamaño de pantalla conocido.
// Un `providersToml` opcional declara un provider (para los tests que corren un
// turno real contra un adaptador). Devuelve el harness ya con Boot hecho y el
// data_dir (para inspeccionar el JSONL si hiciera falta).
func bootChat(t *testing.T, providersToml string, w, h int) (*harness, string) {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg,
		"[plugins]\nenabled = [\"toolkit\", \"providers\", \"sessions\", \"agent\", \"chat\"]\n")
	if providersToml != "" {
		if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersToml), 0o644); err != nil {
			t.Fatalf("write providers.toml: %v", err)
		}
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(true), WithUISize(w, h))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}, dataDir
}

// providersTomlChatStub: un provider cuyo adaptador "chatstub" lo registra el
// propio test desde Lua. Sirve para los tests de layout/streaming que no necesitan
// el SSE real (el adaptador anthropic real lo usa CP-11).
const providersTomlChatStub = `
[providers.test]
adapter  = "chatstub"
base_url = "http://localhost/unused"

[[providers.test.models]]
id      = "m1"
context = 1000
aliases = ["m"]
`

// registerChatStreamStub registra un adaptador "chatstub" que emite un stream
// canónico con VARIOS deltas de texto markdown (para que el transcript del chat
// crezca incrementalmente). Cierra con el `done` y el Message ensamblado. No toca
// la red. El texto en deltas forma "# Hola\n\nMundo **markdown**.\n".
const registerChatStreamStub = `
local providers = require("providers")
providers.register_adapter("chatstub", {
  name = "chatstub",
  caps = { tools = true, system = true, usage = true },
  stream = function(req, provider)
    local assembled = { role = "assistant",
      content = { { type = "text", text = "# Hola\n\nMundo **markdown**.\n" } } }
    local events = {
      { type = "text", text = "# Hola\n\n" },
      { type = "text", text = "Mundo **mark" },
      { type = "text", text = "down**.\n" },
      { type = "usage", input_tokens = 11, output_tokens = 7 },
      { type = "done", stop_reason = "end", message = assembled },
    }
    local i = 0
    return function() i = i + 1; return events[i] end
  end,
})
`

// TestChatCargaYActiva: la extensión carga (source="builtin") y su módulo expone
// la superficie del contrato (chat.md §4/§6/§8/§9).
func TestChatCargaYActiva(t *testing.T) {
	h, _ := bootChat(t, "", 80, 24)
	if src := listSource(h, "chat"); src != "builtin" {
		t.Fatalf(`chat debía cargarse con source="builtin"; got %q`, src)
	}
	h.expectEval(`
		local chat = require("chat")
		assert(type(chat.start) == "function", "start")
		assert(type(chat.command) == "function", "command")
		assert(type(chat.statusline) == "table" and type(chat.statusline.add) == "function", "statusline.add")
		assert(type(chat.renderer) == "function", "renderer")
		assert(type(chat.keys) == "table", "keys")
		return "ok"`, "ok")
}

// TestChatStartRequiereUI (chat.md §8, G20): en headless (sin `nu.ui`), chat.start
// es EINVAL accionable. Construimos un runtime SIN UI a mano (WithForceUI(false))
// para observar el caso headless real.
func TestChatStartRequiereUI(t *testing.T) {
	cfg := t.TempDir()
	writeNuToml(t, cfg,
		"[plugins]\nenabled = [\"toolkit\", \"providers\", \"sessions\", \"agent\", \"chat\"]\n")
	toml := providersTomlChatStub
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	rt := New(WithDataDir(t.TempDir()), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	h := &harness{t: t, rt: rt}

	// nu.ui no existe en headless (G20): chat.start es EINVAL accionable. El chequeo
	// nu.has("ui") es lo PRIMERO de chat.start (antes de cualquier suspensión), así
	// que se puede llamar directo sin task: el EINVAL se lanza síncrono.
	se := h.evalErr(`return require("chat").start({ model = "test/m", no_store = true })`)
	if se.Code != CodeEINVAL {
		t.Fatalf("chat.start headless: code=%q, want EINVAL", se.Code)
	}
	if !strings.Contains(se.Message, "headless") || !strings.Contains(se.Message, "ui") {
		t.Fatalf("el error de chat.start headless no es accionable: %q", se.Message)
	}
}

// TestChatDegradedStart (chat.md §8, ADR-017/G35): SIN modelo ni provider configurados,
// el arranque del chat NO muere al log dejando la terminal en blanco —monta una UI
// degradada ACCIONABLE (explica cómo configurar y cómo salir) y SALIBLE—. Ejercita la
// ruta REAL: el auto-arranque en `core:ready` (chat/init.lua) sin config disponible.
// bootChat NO escribe providers.toml ni agent.toml, así que agent.session lanza EINVAL.
func TestChatDegradedStart(t *testing.T) {
	h, _ := bootChat(t, "", 50, 14)
	// Pumpea el scheduler a idle para que el spawn pendiente del `core:ready`
	// (el auto-arranque del chat) corra hasta montar la UI degradada.
	h.eval(`nu.task.spawn(function() end)`)

	// El chat activo quedó en modo DEGRADADO, sin sesión (no se pudo construir).
	h.expectEval(`return tostring(require("chat")._active ~= nil)`, "true")
	h.expectEval(`return tostring(require("chat")._active.degraded == true)`, "true")
	h.expectEval(`return tostring(require("chat")._active.session == nil)`, "true")
	// Tiene atajos de salida instalados (esc/q/ctrl+c → core:shutdown).
	h.expectEval(`return tostring(#require("chat")._active.keymaps >= 1)`, "true")

	// La pantalla explica, accionable, cómo configurar y cómo salir.
	if !screenContains(h, "configuración necesaria") {
		t.Fatalf("la pantalla degradada no muestra la ayuda; pantalla:\n%s", dumpScreen(h))
	}
	if !screenContains(h, "default-config") {
		t.Fatalf("la pantalla degradada no menciona el atajo --default-config; pantalla:\n%s", dumpScreen(h))
	}

	// quit() desmonta sin error pese a la sesión nil (los guards lo toleran, G35).
	h.eval(`require("chat")._active:quit()`)
}

// TestChatLayout (chat.md §1): chat.start monta la app con el vbox
// transcript/input/statusline. Verificamos que los tres widgets existen, tienen
// área (el layout les dio geometría) y el foco arranca en el editor.
func TestChatLayout(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 80, 24)
	h.eval(`
		C = nil
		nu.task.spawn(function()
			local chat = require("chat")
			` + registerChatStreamStub + `
			C = chat.start({ model = "test/m", no_store = true })
		end)
	`)
	// los tres widgets de la columna existen.
	h.expectEval(`return tostring(C ~= nil)`, "true")
	h.expectEval(`return tostring(C.transcript_widget ~= nil)`, "true")
	h.expectEval(`return tostring(C.input ~= nil)`, "true")
	// el transcript es flexible (ocupa el alto sobrante): tiene varias filas.
	h.expectEval(`return tostring(C.transcript_widget.h > 1)`, "true")
	h.expectEval(`return tostring(C.transcript_widget.w)`, "80")
	// el editor va dentro de una CAJA (borde + prompt "› "); la caja ocupa su banda
	// (3 filas: 1 línea de entrada + 2 de borde) y el editor su interior.
	h.expectEval(`return tostring(C.input_box ~= nil)`, "true")
	h.expectEval(`return tostring(C.input_box.h)`, "3")
	h.expectEval(`return tostring(C.input.h >= 1)`, "true")
	// el foco arranca en el editor (chat.md §3).
	h.expectEval(`return tostring(C.app.focused == C.input)`, "true")
	h.eval(`C:quit()`)
}

// TestChatInputMultilinea (chat.md §3): el editor multilínea. enter "pelado" NO lo
// consume el editor (lo recoge el chat para enviar); shift+enter / alt+enter
// insertan una línea (lo consume). backspace en borde une líneas.
func TestChatInputMultilinea(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 80, 24)
	h.eval(`
		IN = require("chat.input").new({})
	`)
	// escribir texto.
	h.eval(`
		IN:on_key({ type = "key", key = "h" })
		IN:on_key({ type = "key", key = "i" })
	`)
	h.expectEval(`return IN:value()`, "hi")
	// enter SIN modificador: el editor lo DEJA PASAR (false) -> el chat enviará.
	h.expectEval(`return tostring(IN:on_key({ type = "key", key = "enter" }))`, "false")
	// shift+enter: nueva línea (lo consume).
	h.expectEval(`return tostring(IN:on_key({ type = "key", key = "enter", mods = { shift = true } }))`, "true")
	h.eval(`IN:on_key({ type = "key", key = "x" })`)
	h.expectEval(`return IN:value()`, "hi\nx")
	// el editor ocupa 2 líneas (content_height).
	h.expectEval(`return tostring(IN:content_height(80))`, "2")
	// alt+enter también inserta línea (terminal sin shift, chat.md §3).
	h.expectEval(`return tostring(IN:on_key({ type = "key", key = "enter", mods = { alt = true } }))`, "true")
	h.expectEval(`return tostring(IN:content_height(80))`, "3")
	// backspace en el inicio de la 3ª línea (vacía) la une con la 2ª.
	h.expectEval(`return tostring(IN:on_key({ type = "key", key = "backspace" }))`, "true")
	h.expectEval(`return tostring(IN:content_height(80))`, "2")
	h.expectEval(`return IN:value()`, "hi\nx")
	// el caret está al final de "x" (fila 1, 0-based; columna 1).
	h.expectEval(`return tostring(IN:caret_row())`, "1")
	h.expectEval(`return tostring(IN:caret_col())`, "1")
}

// TestChatStreamingMarkdown (chat.md §2, EL CORAZÓN de S43): un `agent:delta` de
// texto se acumula en el transcript del chat y se pinta con markdown EN STREAMING.
// El Block compuesto del transcript CRECE con el texto. Emitimos los eventos
// `agent:*` a mano (como haría el agente) para aislar el render del chat del turno.
func TestChatStreamingMarkdown(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 60, 20)
	h.eval(`
		C = nil
		nu.task.spawn(function()
			` + registerChatStreamStub + `
			C = require("chat").start({ model = "test/m", no_store = true })
		end)
	`)
	h.eval(`SID = C.session.id`)

	// Arranca el turno (esto retira la pantalla de bienvenida: el transcript ya tiene
	// un item de asistente en curso) y mide la altura BASE de la conversación vacía.
	h.eval(`
		-- el agente emite con atribución de sesión (G3): payload.session = SID.
		nu.events.emit("agent:turn.start", { session = SID })
		local w = C.transcript_widget.w
		H0 = C.transcript_widget:content_height(w)
	`)

	// Llegan deltas de texto markdown; cada uno debe hacer crecer el transcript.
	h.eval(`
		nu.events.emit("agent:delta", { session = SID, type = "text", text = "# Título\n\n" })
	`)
	h.eval(`
		local w = C.transcript_widget.w
		H1 = C.transcript_widget:content_height(w)
	`)
	h.eval(`
		nu.events.emit("agent:delta", { session = SID, type = "text", text = "Una línea.\n\nOtra línea más larga para forzar altura.\n" })
	`)
	h.eval(`
		local w = C.transcript_widget.w
		H2 = C.transcript_widget:content_height(w)
		-- el texto markdown acumulado del transcript contiene el encabezado.
		MD = C.transcript:markdown()
	`)

	// El Block del transcript CRECIÓ con el texto en streaming (alturas no
	// decrecientes y estrictamente mayor tras más texto): el criterio de hecho.
	h.expectEval(`return tostring(H1 >= H0)`, "true")
	h.expectEval(`return tostring(H2 > H1)`, "true")
	// el markdown acumulado contiene el contenido en streaming.
	h.expectEval(`return tostring(MD:find("Título", 1, true) ~= nil)`, "true")
	h.expectEval(`return tostring(MD:find("Otra línea", 1, true) ~= nil)`, "true")

	// agent:message SELLA el mensaje con el Message del done (chat.md §2).
	h.eval(`
		nu.events.emit("agent:message", {
			session = SID,
			message = { role = "assistant", content = { { type = "text", text = "# Final\n\nsellado.\n" } } },
			stop_reason = "end",
		})
		MD2 = C.transcript:markdown()
	`)
	// el render final sustituyó los deltas por el Message sellado.
	h.expectEval(`return tostring(MD2:find("Final", 1, true) ~= nil)`, "true")
	h.expectEval(`return tostring(MD2:find("sellado", 1, true) ~= nil)`, "true")

	// el contenido REAL pintado en la rejilla del compositor contiene el texto del
	// transcript (la primera fila del transcript empieza con el mensaje de usuario
	// o el encabezado markdown). Verificamos que la pantalla muestra texto del chat.
	h.eval(`C:_repaint()`)
	withToken(h.rt, func() {
		var found bool
		for y := 0; y < 18; y++ {
			row := composeRow(h.rt.ui.comp, y)
			if containsStr(row, "Final") || containsStr(row, "sellado") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("el transcript sellado debía verse en la pantalla compuesta")
		}
	})
	h.eval(`C:quit()`)
}

// TestChatFiltraPorSesion (chat.md §1, G3): el chat pinta solo los eventos cuyo
// `session` es la sesión activa; un `agent:delta` de OTRA sesión no toca el
// transcript (la actividad de otras va a la statusline).
func TestChatFiltraPorSesion(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 60, 20)
	h.eval(`
		C = nil
		nu.task.spawn(function()
			` + registerChatStreamStub + `
			C = require("chat").start({ model = "test/m", no_store = true })
		end)
	`)
	h.eval(`
		SID = C.session.id
		N0 = C.transcript:count()
	`)
	// un delta de la sesión ACTIVA crece el transcript.
	h.eval(`nu.events.emit("agent:delta", { session = SID, type = "text", text = "mío" })`)
	h.expectEval(`return tostring(C.transcript:count() > N0)`, "true")
	// un delta de OTRA sesión NO toca el transcript.
	h.eval(`
		N1 = C.transcript:count()
		nu.events.emit("agent:delta", { session = "otra-sesion", type = "text", text = "ajeno" })
		N2 = C.transcript:count()
	`)
	h.expectEval(`return tostring(N2 == N1)`, "true")
	h.eval(`C:quit()`)
}

// TestChatDialogoPermisos (chat.md §5): ante `agent:permission.asked` (de la sesión
// activa) el chat abre un modal y responde con agent.permission.respond. Simulamos
// el ask y la respuesta por tecla; verificamos que el agente recibe la decisión
// (un future del lado del agente se resuelve).
func TestChatDialogoPermisos(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 60, 20)
	h.eval(`
		C = nil
		nu.task.spawn(function()
			` + registerChatStreamStub + `
			C = require("chat").start({ model = "test/m", no_store = true })
		end)
	`)
	h.eval(`
		SID = C.session.id
		-- Interceptamos agent.permission.respond para observar la decisión que el
		-- modal envía (sin un turno real). Es lo que el agente espera (agente.md §5).
		local agent = require("agent")
		GRANTED = nil
		ORIG_RESPOND = agent.permission.respond
		agent.permission.respond = function(id, granted)
			GRANTED = granted
			return ORIG_RESPOND(id, granted)
		end
	`)
	// emitimos un ask de la sesión activa: el chat abre el modal.
	h.eval(`
		nu.events.emit("agent:permission.asked", {
			session = SID, id = "ask-1", tool = "write_file",
			args = { path = "/tmp/x", content = "hola" },
			suggested = "write_file:/tmp/x",
		})
	`)
	// hay un modal visible y enfocado (la capa modal tiene el diálogo).
	h.expectEval(`return tostring(C.current_modal ~= nil)`, "true")
	h.expectEval(`return tostring(C.app.focused == C.current_modal)`, "true")
	// el usuario pulsa "a" (permitir una vez): el modal responde granted=true.
	h.eval(`C.app:handle_key({ type = "key", key = "a" })`)
	h.expectEval(`return tostring(GRANTED)`, "true")
	// el modal se cerró (no quedan asks en cola).
	h.expectEval(`return tostring(C.current_modal == nil)`, "true")
	h.eval(`C:quit()`)
}

// TestChatPendingAsksNoSePisan (G3, chat.md §6): el indicador de asks pendientes de
// la statusline suma DOS estados independientes —la cola de asks PROPIOS (los que
// abren modal) y el contador de asks de OTRAS sesiones (que solo suben el indicador,
// G3)— sin que un flujo borre al otro. Regresión: antes un ÚNICO campo `pending_count`
// lo escribían ambos flujos y se pisaban: un ask propio fijaba la cuenta a 1 (perdía
// los ajenos), y responderlo la bajaba a 0 (borraba el indicador de los ajenos aún
// pendientes). También verifica el decremento del contador ajeno al DENEGARSE un ask
// de la otra sesión (`agent:permission.denied` con source="user").
func TestChatPendingAsksNoSePisan(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 60, 20)
	h.eval(`
		C = nil
		nu.task.spawn(function()
			` + registerChatStreamStub + `
			C = require("chat").start({ model = "test/m", no_store = true })
		end)
	`)
	h.eval(`SID = C.session.id`)
	// texto renderizado del segmento de asks pendientes (derecha de la statusline).
	h.eval(`
		function pending_span_text()
			C:_update_statusline()
			local parts = {}
			for _, s in ipairs(C.status_right.spans or {}) do parts[#parts+1] = s.text end
			return table.concat(parts)
		end
	`)
	// 2 asks AJENOS (de otra sesión): solo suben el contador; NO abren modal (G3).
	h.eval(`
		for i = 1, 2 do
			nu.events.emit("agent:permission.asked", {
				session = "otra-sesion", id = "ext-"..i, tool = "bash",
				args = { command = "ls" }, suggested = "bash:ls",
			})
		end
	`)
	h.expectEval(`return tostring(C.current_modal == nil)`, "true")
	h.expectEval(`return tostring(C:_pending_total())`, "2")
	// 1 ask PROPIO (sesión activa): abre modal y SUMA a los ajenos (no los pisa).
	h.eval(`
		nu.events.emit("agent:permission.asked", {
			session = SID, id = "ask-propio", tool = "write_file",
			args = { path = "/tmp/x", content = "hola" },
			suggested = "write_file:/tmp/x",
		})
	`)
	h.expectEval(`return tostring(C.current_modal ~= nil)`, "true")
	h.expectEval(`return tostring(C:_pending_total())`, "3") // 2 ajenos + 1 propio
	h.expectEval(`return tostring(pending_span_text():find("3 perm", 1, true) ~= nil)`, "true")
	// El usuario responde el propio (permitir una vez, tecla "a"): baja SOLO el propio.
	// Los 2 ajenos SIGUEN pendientes (antes esto los borraba con pending_count = 0).
	h.eval(`C.app:handle_key({ type = "key", key = "a" })`)
	h.expectEval(`return tostring(C.current_modal == nil)`, "true")
	h.expectEval(`return tostring(C:_pending_total())`, "2")
	// Un ajeno se resuelve DENEGANDO (source="user"): decrementa el contador ajeno.
	h.eval(`
		nu.events.emit("agent:permission.denied", {
			session = "otra-sesion", id = "call-1", tool = "bash", source = "user",
		})
	`)
	h.expectEval(`return tostring(C:_pending_total())`, "1")
	// Una denegación por POLÍTICA (source≠"user") NO decrementa: nunca incrementó el
	// contador (esas denegaciones no emiten `permission.asked`). Guard a 0 implícito.
	h.eval(`
		nu.events.emit("agent:permission.denied", {
			session = "otra-sesion", id = "call-2", tool = "bash", source = "deny",
		})
	`)
	h.expectEval(`return tostring(C:_pending_total())`, "1")
	h.eval(`C:quit()`)
}

// TestChatComandoSlash (chat.md §4): un comando slash se registra y despacha.
// /help lista los comandos; un comando desconocido devuelve un mensaje (no se
// envía al modelo).
func TestChatComandoSlash(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 60, 20)
	h.eval(`
		C = nil
		nu.task.spawn(function()
			` + registerChatStreamStub + `
			C = require("chat").start({ model = "test/m", no_store = true })
		end)
	`)
	h.expectEval(`return tostring(C ~= nil)`, "true")
	// /help es un builtin (chat.md §4): dispatch devuelve handled=true y un texto.
	h.eval(`
		HANDLED, MSG = nil, nil
		nu.task.spawn(function()
			local commands = require("chat.commands")
			HANDLED, MSG = commands.dispatch("/help", C:command_ctx())
		end)
	`)
	h.expectEval(`return tostring(HANDLED)`, "true")
	h.expectEval(`return tostring(MSG ~= nil and MSG:find("help", 1, true) ~= nil)`, "true")
	// un comando desconocido: handled=true (se maneja mostrando el error), con mensaje.
	h.eval(`
		HANDLED2, MSG2 = nil, nil
		nu.task.spawn(function()
			local commands = require("chat.commands")
			HANDLED2, MSG2 = commands.dispatch("/noexiste", C:command_ctx())
		end)
	`)
	h.expectEval(`return tostring(HANDLED2)`, "true")
	h.expectEval(`return tostring(MSG2:find("desconocido", 1, true) ~= nil)`, "true")
	h.eval(`C:quit()`)
}

// TestChatStatusline (chat.md §6): los segmentos por defecto (modelo/contexto/
// coste/cwd/permisos) se registran y producen texto; un segmento de tercero se
// añade con chat.statusline.add y aparece.
func TestChatStatusline(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 80, 24)
	h.eval(`
		C = nil
		nu.task.spawn(function()
			` + registerChatStreamStub + `
			C = require("chat").start({ model = "test/m", no_store = true })
		end)
	`)
	h.eval(`C:_update_statusline()`)
	// la statusline es ahora una BARRA de spans coloreados (richtext): el texto de
	// cada lado se reconstruye uniendo los `text` de sus spans.
	h.eval(`
		function span_text(w)
			local parts = {}
			for _, s in ipairs(w.spans or {}) do parts[#parts+1] = s.text end
			return table.concat(parts)
		end
	`)
	// el segmento de modelo muestra el modelo activo en la izquierda.
	h.expectEval(`return tostring(span_text(C.status_left) ~= "")`, "true")
	h.expectEval(`return tostring(span_text(C.status_left):find("test/m", 1, true) ~= nil)`, "true")
	// la derecha contiene el modo de permisos ("ask" por defecto).
	h.expectEval(`return tostring(span_text(C.status_right):find("ask", 1, true) ~= nil)`, "true")
	h.eval(`C:quit()`)
}

// ---------------------------------------------------------------------------
// CP-11 (dogfooding): la sesión de chat de extremo a extremo contra un SSE
// GRABADO del adaptador anthropic. ADAPTACIÓN: el CP-11 original pide un provider
// REAL; en este entorno NO hay red ni credenciales, así que se ejercita contra el
// SSE grabado (como CP-9). Documentado en docs/decisiones-implementacion.md S43.
// ---------------------------------------------------------------------------

// chatAnthropicProvidersToml: un providers.toml cuyo provider `anthropic` apunta su
// base_url al servidor httptest del SSE grabado (igual que bootAnthropic en
// providers_anthropic_test.go).
func chatAnthropicProvidersToml(baseURL string) string {
	return fmt.Sprintf(`
[providers.anthropic]
adapter     = "anthropic"
base_url    = %q
api_key_env = "ANTHROPIC_API_KEY"

[[providers.anthropic.models]]
id         = "claude-opus-4-8"
context    = 200000
max_output = 32000
aliases    = ["opus"]
`, baseURL)
}

// recordedFinalSSE: un segundo SSE GRABADO de Anthropic, la respuesta del modelo
// TRAS recibir el tool_result de get_weather (la 2ª vuelta del turno): texto
// markdown y `stop_reason=end_turn` (sin más tools). Cierra el turno limpiamente.
// El texto: "El tiempo en **Madrid** es soleado.\n".
const recordedFinalSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_02","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":80,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"El tiempo en **Mad"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"rid** es soleado.\n"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}

`

// sseSequenceServer sirve los `bodies` EN ORDEN: la i-ésima petición recibe
// `bodies[i]` (y la última se repite si llegan más peticiones, defensivo). Imita
// una conversación grabada de varias vueltas (turno con tool: 1ª vuelta tool_use,
// 2ª vuelta texto final). Mismo flush-por-línea que sseServer (llegada incremental).
func sseSequenceServer(t *testing.T, bodies []string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	n := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		idx := n
		if idx >= len(bodies) {
			idx = len(bodies) - 1
		}
		n++
		mu.Unlock()
		body := bodies[idx]

		fl, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("el ResponseWriter no implementa Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		for _, line := range strings.SplitAfter(body, "\n") {
			if line == "" {
				continue
			}
			_, _ = io.WriteString(w, line)
			fl.Flush()
		}
	}))
}

// TestCP11ChatStreamingE2E es el checkpoint CP-11 ADAPTADO: una sesión de chat de
// EXTREMO A EXTREMO contra un SSE grabado del adaptador anthropic. El usuario
// "envía" un mensaje → el agente corre el turno (Session:send) → el `agent:delta`
// streaming se pinta con markdown en el transcript del chat → el Block compuesto
// del transcript CRECE con el texto en streaming. El turno tiene DOS vueltas
// (grabadas): la 1ª pide la tool `get_weather` (registrada, default allow), la 2ª
// responde texto final tras el tool_result — el camino completo con tools.
//
// Es el camino COMPLETO chat → agente → stream → markdown → toolkit, la primera
// vez que corre junto dentro del chat (CP-9 lo corrió fuera del chat). Cubre el
// criterio de hecho de S43 (conversación con streaming markdown).
//
// Lo que NO cubre (limitación del entorno): el provider REAL (red + credenciales),
// no ejecutable en CI headless. Eso queda para un humano con credenciales.
func TestCP11ChatStreamingE2E(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-grabado")
	// dos vueltas grabadas: tool_use (recordedSSE de CP-9) y texto final.
	srv := sseSequenceServer(t, []string{recordedSSE, recordedFinalSSE})
	defer srv.Close()

	h, dataDir := bootChat(t, chatAnthropicProvidersToml(srv.URL), 60, 24)
	_ = dataDir

	// Arrancamos el chat contra el provider anthropic (SSE grabado). no_store=false
	// para que el turno se PERSISTA (dogfooding: una sesión real de chat). La tool
	// `get_weather` (que el SSE grabado pide) se registra con default="allow" para
	// que el turno corra sus DOS vueltas sin pedir permiso (el flujo de permisos lo
	// cubre TestChatDialogoPermisos). El handler es trivial (devuelve un texto).
	h.eval(`
		C = nil
		nu.task.spawn(function()
			local agent = require("agent")
			agent.tool{
				name = "get_weather",
				description = "consulta el tiempo",
				schema = { type = "object" },
				permissions = { default = "allow" },
				handler = function(args, ctx) return "soleado en " .. tostring(args.city or "?") end,
			}
			C = require("chat").start({ model = "anthropic/opus" })
		end)
	`)
	h.eval(`
		SID = C.session.id
		-- altura inicial del transcript (vacío).
		H0 = C.transcript_widget:content_height(C.transcript_widget.w)
		-- instrumentamos el render del transcript para contar repintados con
		-- contenido creciente (el streaming). Capturamos las alturas a lo largo del
		-- turno suscribiéndonos a agent:delta DESPUÉS del chat (el chat ya está
		-- suscrito; este observador solo MIDE, no pinta).
		HEIGHTS = {}
		OBS = nu.events.on("agent:delta", function(p)
			if p.session == SID and p.type == "text" then
				local w = C.transcript_widget.w
				HEIGHTS[#HEIGHTS+1] = C.transcript_widget:content_height(w)
			end
		end)
	`)

	// El usuario ESCRIBE y ENVÍA (chat.md §3: enter envía). Simulamos el envío
	// llamando a submit con el editor poblado, dentro de una task (submit lanza el
	// turno como task que SUSPENDE en el stream). Esperamos a que el turno acabe
	// observando el done (agent:message sella).
	h.eval(`
		DONE = false
		-- el turno termina con agent:turn.end (una vez, tras las dos vueltas).
		nu.events.on("agent:turn.end", function(p)
			if p.session == SID then DONE = true end
		end)
		nu.task.spawn(function()
			C.input:set_value("¿qué tiempo hace en Madrid?")
			C:submit()
			-- esperamos a que el turno (dos vueltas del SSE grabado) termine; el loop
			-- avanza el turno suspendido mientras sondeamos.
			for _ = 1, 400 do
				if DONE then break end
				nu.task.sleep(5)
			end
		end)
	`)

	// El turno completó las dos vueltas: el transcript se selló con el Message final
	// (texto markdown del segundo SSE grabado).
	h.expectEval(`return tostring(DONE)`, "true")

	// El Block del transcript CRECIÓ con el texto en streaming: hubo varios deltas
	// de texto y las alturas observadas son no-decrecientes y la final > inicial.
	h.expectEval(`return tostring(#HEIGHTS >= 1)`, "true")
	h.eval(`
		NONDECR = true
		for i = 2, #HEIGHTS do
			if HEIGHTS[i] < HEIGHTS[i-1] then NONDECR = false end
		end
		HFINAL = C.transcript_widget:content_height(C.transcript_widget.w)
	`)
	h.expectEval(`return tostring(NONDECR)`, "true")
	// el transcript final es al menos tan alto como el primer frame de streaming (la
	// conversación creció de forma monótona hasta el final). Comparar contra el primer
	// frame de streaming —no contra H0— es independiente de la pantalla de bienvenida,
	// que ocupa el transcript hasta el primer mensaje.
	h.expectEval(`return tostring(HFINAL >= HEIGHTS[1])`, "true")

	// El transcript contiene el markdown de AMBAS vueltas grabadas: el texto de la
	// 1ª (sellado), el bloque de la tool `get_weather` (el agente la ejecutó porque
	// el done de la 1ª trae stop_reason tool_calls), y el texto final de la 2ª.
	h.eval(`MD = C.transcript:markdown()`)
	// 1ª vuelta (texto markdown sellado):
	h.expectEval(`return tostring(MD:find("markdown", 1, true) ~= nil)`, "true")
	h.expectEval(`return tostring(MD:find("Hola", 1, true) ~= nil)`, "true")
	// bloque de la tool ejecutada:
	h.expectEval(`return tostring(MD:find("get_weather", 1, true) ~= nil)`, "true")
	// 2ª vuelta (texto final tras el tool_result):
	h.expectEval(`return tostring(MD:find("Madrid", 1, true) ~= nil)`, "true")
	h.expectEval(`return tostring(MD:find("soleado", 1, true) ~= nil)`, "true")

	// El contenido REAL pintado en la pantalla compuesta muestra el markdown del
	// transcript (el camino llegó hasta el toolkit/compositor — chat → agente →
	// stream → markdown → toolkit). Auto-scroll: lo último (el texto final) se ve.
	h.eval(`C:_repaint()`)
	withToken(h.rt, func() {
		var found bool
		for y := 0; y < 24; y++ {
			row := composeRow(h.rt.ui.comp, y)
			if containsStr(row, "soleado") || containsStr(row, "Madrid") || containsStr(row, "markdown") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("CP-11: el markdown del turno debía verse en la pantalla compuesta del chat")
		}
	})
	h.eval(`C:quit()`)
}
