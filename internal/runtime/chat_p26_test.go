package runtime

// Tests de las mejoras de UI del chat:
//   - P26: menciones `@` con picker difuso de ficheros,
//   - P27: render en vivo de agent:tool.progress + marca de agent:compact,
//   - P28: comandos /fork y /permissions,
//   - P29: "permitir siempre" (sesión/global) + autocompletado de `/`.
//
// Mismo arnés que chat_test.go (bootChat: las cinco extensiones, nu.ui forzada,
// tamaño conocido). Se conduce la UI emitiendo eventos `agent:*` y alimentando
// teclas con C.app:handle_key, e inspeccionando el estado del Chat.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startChat arranca chat.start con el chatstub registrado y devuelve tras dejar a
// la task progresar. Deja el Chat en la global C y la sesión id en SID.
func startChat(h *harness, extraOpts string) {
	h.eval(`
		C = nil
		nu.task.spawn(function()
			` + registerChatStreamStub + `
			C = require("chat").start({ model = "test/m", no_store = true` + extraOpts + ` })
		end)
	`)
	h.eval(`SID = C.session.id`)
}

// TestChatToolProgressCompact (P27): el chat pinta el progreso en vivo de una tool
// y una marca de compactación.
func TestChatToolProgressCompact(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 80, 24)
	startChat(h, "")
	h.eval(`
		nu.events.emit("agent:tool.start", { session = SID, id = "t1", name = "grep", args = {} })
		nu.events.emit("agent:tool.progress", { session = SID, id = "t1", name = "grep", text = "42 ficheros..." })
		MD_PROG = C.transcript:markdown()
		nu.events.emit("agent:compact", { session = SID, auto = true })
		MD_COMPACT = C.transcript:markdown()
	`)
	prog := h.eval(`return MD_PROG`)[0]
	if !strings.Contains(prog, "42 ficheros") {
		t.Errorf("el progreso en vivo no aparece en el transcript:\n%s", prog)
	}
	compact := h.eval(`return MD_COMPACT`)[0]
	if !strings.Contains(compact, "compactada") {
		t.Errorf("la marca de compactación no aparece:\n%s", compact)
	}
	h.eval(`C:quit()`)
}

// TestChatAllowAlways (P29): el diálogo de permisos ofrece "permitir siempre".
// La tecla `s` añade el patrón a la política de la sesión; `g` además lo persiste
// a la config global del usuario (agent.toml).
func TestChatAllowAlways(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 60, 20)
	startChat(h, "")

	// sesión: tecla `s` (permitir siempre, sesión).
	h.eval(`
		nu.events.emit("agent:permission.asked", {
			session = SID, id = "ask-1", tool = "bash",
			args = { command = "git status" }, suggested = "bash:git *",
		})
	`)
	h.expectEval(`return tostring(C.current_modal ~= nil)`, "true")
	h.eval(`C.app:handle_key({ type = "key", key = "s" })`)
	h.eval(`
		ALLOW_HAS = false
		for _, p in ipairs(C.session.permissions.allow) do
			if p == "bash:git *" then ALLOW_HAS = true end
		end
	`)
	h.expectEval(`return tostring(ALLOW_HAS)`, "true")

	// global: tecla `g` persiste a agent.toml.
	h.eval(`
		nu.events.emit("agent:permission.asked", {
			session = SID, id = "ask-2", tool = "bash",
			args = { command = "npm install" }, suggested = "bash:npm *",
		})
	`)
	h.eval(`C.app:handle_key({ type = "key", key = "g" })`)
	h.eval(`
		GLOBAL = ""
		nu.task.spawn(function()
			local ok, raw = pcall(nu.fs.read, nu.config.dir() .. "/agent.toml")
			if ok then GLOBAL = raw end
		end)
	`)
	global := h.eval(`return GLOBAL`)[0]
	if !strings.Contains(global, "bash:npm *") {
		t.Errorf("el patrón no se persistió a agent.toml global:\n%s", global)
	}
	h.eval(`C:quit()`)
}

// TestChatCommandPicker (P29): tab con un `/` en el editor abre el picker de
// comandos; al elegir, deja "/<name> " en el editor.
func TestChatCommandPicker(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 60, 20)
	startChat(h, "")
	h.eval(`
		C.input:set_value("/hel")
		C:open_command_picker()
		PICKER = (C._picker ~= nil)
		-- el prefijo "hel" filtra a "help".
		FILTERED = C._picker and table.concat(C._picker.filtered, ",") or ""
	`)
	h.expectEval(`return tostring(PICKER)`, "true")
	filtered := h.eval(`return FILTERED`)[0]
	if !strings.Contains(filtered, "help") {
		t.Errorf("el picker de comandos no filtró a help: %q", filtered)
	}
	// elegir (enter) deja "/help " en el editor.
	h.eval(`C.app:handle_key({ type = "key", key = "enter" })`)
	h.expectEval(`return C.input:value()`, "/help ")
	h.expectEval(`return tostring(C._picker == nil)`, "true")
	h.eval(`C:quit()`)
}

// TestChatFilePicker (P26): `@` abre el picker difuso de ficheros del repo; al
// elegir, la ruta se inyecta en el editor.
func TestChatFilePicker(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"alpha.txt", "beta.txt", "gamma.go"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	h, _ := bootChat(t, providersTomlChatStub, 80, 24)
	startChat(h, ", cwd = "+quote(repo))
	h.eval(`
		PICKER, NCAND = false, 0
		nu.task.spawn(function()
			C:open_file_picker()
			nu.task.sleep(20)
			PICKER = (C._picker ~= nil)
			if C._picker then NCAND = #C._picker.candidates end
		end)
	`)
	h.expectEval(`return tostring(PICKER)`, "true")
	// hay candidatos (los ficheros del repo).
	h.expectEval(`return tostring(NCAND >= 3)`, "true")
	// el modal RENDERIZA: su marco recibió geometría real (no 0×0). Blinda el bug por
	// el que el picker se añadía a la capa modal pero `relayout` no lo repartía, así
	// que quedaba invisible y atrapaba el foco (chat colgado al teclear `@`).
	h.expectEval(`return tostring(C._modal_frame ~= nil and C._modal_frame.w > 0 and C._modal_frame.h > 0)`, "true")
	// teclear "alpha" filtra y enter inyecta la ruta.
	h.eval(`
		for c in ("alpha"):gmatch(".") do
			C.app:handle_key({ type = "key", key = c })
		end
		C.app:handle_key({ type = "key", key = "enter" })
	`)
	val := h.eval(`return C.input:value()`)[0]
	if !strings.Contains(val, "alpha.txt") {
		t.Errorf("la mención no inyectó la ruta del fichero: %q", val)
	}
	h.eval(`C:quit()`)
}

// TestChatForkCommand (P28): /fork bifurca la sesión y el chat continúa en la rama
// (la sesión activa cambia de id).
func TestChatForkCommand(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 80, 24)
	startChat(h, "")
	h.eval(`
		OLD_ID = C.session.id
		MSG, NEW_ID = nil, nil
		nu.task.spawn(function()
			local ctx = C:command_ctx()
			local _h, m = require("chat.commands").dispatch("/fork", ctx)
			MSG = m
			NEW_ID = C.session.id
		end)
	`)
	h.expectEval(`return tostring(NEW_ID ~= OLD_ID)`, "true")
	msg := h.eval(`return tostring(MSG)`)[0]
	if !strings.Contains(msg, "bifurcada") {
		t.Errorf("/fork no reportó la bifurcación: %q", msg)
	}
	h.eval(`C:quit()`)
}

// TestChatThinkCommand (P21/ADR-016): /think ve y cambia el razonamiento de la
// sesión, y el modo se refleja en thinking_mode() (que alimenta la statusline).
func TestChatThinkCommand(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 80, 24)
	startChat(h, "")
	h.eval(`
		nu.task.spawn(function()
			local cmds = require("chat.commands")
			local ctx = C:command_ctx()
			_, SHOW0 = cmds.dispatch("/think", ctx)            -- estado inicial: off
			cmds.dispatch("/think adaptive", ctx)
			MODE_A = C.session:thinking_mode()
			_, MSG_BUD = cmds.dispatch("/think budget 8000", ctx)
			MODE_B = C.session:thinking_mode()
			BUDGET_B = C.session.thinking and C.session.thinking.budget
			cmds.dispatch("/think off", ctx)
			MODE_OFF = C.session:thinking_mode()
		end)
	`)
	h.expectEval(`return SHOW0`, "razonamiento: off")
	h.expectEval(`return MODE_A`, "adaptive")
	h.expectEval(`return MODE_B`, "budget")
	h.expectEval(`return tostring(BUDGET_B)`, "8000")
	h.expectEval(`return MODE_OFF`, "off")
	h.eval(`C:quit()`)
}

// TestChatPermissionsCommand (P28): /permissions edita la política de la sesión.
func TestChatPermissionsCommand(t *testing.T) {
	h, _ := bootChat(t, providersTomlChatStub, 80, 24)
	startChat(h, "")
	h.eval(`
		nu.task.spawn(function()
			local ctx = C:command_ctx()
			local cmds = require("chat.commands")
			cmds.dispatch("/permissions allow bash:git *", ctx)
			cmds.dispatch("/permissions mode auto", ctx)
			local _h, VIEW = cmds.dispatch("/permissions", ctx)
			SHOW = VIEW
		end)
	`)
	h.eval(`
		ALLOW_HAS, MODE = false, C.session.permissions.mode
		for _, p in ipairs(C.session.permissions.allow) do
			if p == "bash:git *" then ALLOW_HAS = true end
		end
	`)
	h.expectEval(`return tostring(ALLOW_HAS)`, "true")
	h.expectEval(`return C.session.permissions.mode`, "auto")
	show := h.eval(`return SHOW`)[0]
	if !strings.Contains(show, "bash:git *") || !strings.Contains(show, "auto") {
		t.Errorf("/permissions no muestra la política:\n%s", show)
	}
	h.eval(`C:quit()`)
}
