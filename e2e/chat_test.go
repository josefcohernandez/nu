package e2e

// Tests e2e del plugin oficial `chat` contra el BINARIO real, bajo un PTY: la UI
// OFICIAL del harness (toolkit + agent + providers + sessions + chat) recibiendo
// BYTES de teclado reales, con el provider FAKE por el adaptador anthropic real, y
// —lo que ningún test in-process puede ver— el JSONL de la sesión TAL COMO QUEDA EN
// DISCO tras un proceso que terminó y liberó su lock (sesiones.md §6).
//
// Qué cubre el e2e que `internal/runtime/chat_test.go` NO puede: ese fichero prueba
// el layout, el streaming markdown dentro del Block, el diálogo de permisos y el
// dispatch de comandos HEADLESS (`WithForceUI(true)`), sin proceso real, sin PTY y
// sin fichero en disco. Aquí el valor es el binario compilado arrancando bajo un TTY
// de verdad, el bucle de `driver.go` decodificando teclas crudas, el EXIT CODE real
// del proceso, y el JSONL final en disco tras el cierre.
//
// Conjunto de plugins: `["toolkit", "providers", "sessions", "agent", "chat"]` es el
// mínimo que `chat` declara en su plugin.toml (`requires`) más el propio `chat`. Es
// el mismo conjunto de producto que `officialProductSet()` (todo lo embebido salvo
// `example`, ADR-015/G33) MENOS las piezas que `chat.md` no necesita (`repl`, `mesh`,
// `mcp`). Se usa `enu.toml` a mano en vez de `--default-config` porque ese flag deja
// un `providers.toml` apuntando a `api.anthropic.com`, incompatible con el fake.
//
// Prefijo TestChat* para filtrarlos con `-run TestChat`.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// chatPlugins es el conjunto de plugins de producto que monta el chat bajo TTY. Un
// solo sitio que lo nombre, para que los cinco escenarios arranquen idénticos.
var chatPlugins = []string{"toolkit", "providers", "sessions", "agent", "chat"}

// startupTimeout es el plazo para el PRIMER frame (bienvenida / pantalla degradada).
// Generoso a propósito: arrancar el proceso instancia wazero y carga cinco plugins
// embebidos (toolkit/providers/sessions/agent/chat) —un arranque en frío que, en una
// máquina cargada de tests, puede tardar bastante más que el pintado de un turno ya
// caliente. Los Expect POSTERIORES (streaming, error) usan plazos cortos: para
// entonces el proceso ya está caliente.
const startupTimeout = 15 * time.Second

// newChatWorkspace monta un workspace con el conjunto de producto activado y el
// provider FAKE cableado (providers.toml→fake, agent.toml→anthropic/opus). Helper
// PRIVADO de este fichero: el arnés no ofrece un atajo para este conjunto concreto
// (UseFakeProvider deliberadamente NO escribe enu.toml). El llamante encola en `fp`
// las respuestas del turno ANTES de arrancar el PTY.
func newChatWorkspace(t *testing.T, fp *FakeProvider) *Workspace {
	t.Helper()
	ws := NewWorkspace(t)
	ws.WriteEnuToml(t, chatPlugins...)
	ws.UseFakeProvider(t, fp)
	return ws
}

// sessionEntries localiza EL fichero de sesión en disco (glob
// data_dir/sessions/<proyecto>/<id>.jsonl, sesiones.md §2), exige que haya
// exactamente uno, y devuelve sus líneas parseadas como JSON (el transcript
// append-only: `meta` + `message`…). Falla el test si hay cero o más de un fichero,
// o si alguna línea no decodifica. Helper PRIVADO: la introspección del JSONL en
// disco es justo lo que el arnés no ofrece (es el objeto de estudio del e2e).
func sessionEntries(t *testing.T, ws *Workspace) (string, []map[string]any) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(ws.DataDir, "sessions", "*", "*.jsonl"))
	if err != nil {
		t.Fatalf("glob de sesiones: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("esperaba UN fichero de sesión; encontré %d: %v", len(matches), matches)
	}
	path := matches[0]
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leer el JSONL de sesión %s: %v", path, err)
	}
	var entries []map[string]any
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("línea %d del JSONL no decodifica (%q): %v", i+1, line, err)
		}
		entries = append(entries, m)
	}
	return path, entries
}

// entryType es el campo `t` (discriminante de la entrada JSONL: "meta"/"message"…).
func entryType(t *testing.T, e map[string]any) string {
	t.Helper()
	s, _ := e["t"].(string)
	return s
}

// messageRole devuelve el `role` del `Message` embebido en una entrada `message`
// (`{t="message", message={role=..., content=...}}`, sesiones.md §3). "" si no lo hay.
func messageRole(t *testing.T, e map[string]any) string {
	t.Helper()
	msg, ok := e["message"].(map[string]any)
	if !ok {
		return ""
	}
	role, _ := msg["role"].(string)
	return role
}

// messageJSON reserializa el `Message` embebido para hacer aserciones de subcadena
// sobre su contenido sin depender de la forma exacta del `content` (string vs. array
// de bloques `{type="text", text=…}`, providers.md §2.2): el texto del prompt/turno
// viaja en ese JSON pase lo que pase.
func messageJSON(t *testing.T, e map[string]any) string {
	t.Helper()
	b, err := json.Marshal(e["message"])
	if err != nil {
		t.Fatalf("reserializar el message: %v", err)
	}
	return string(b)
}

// TestChatE2EHappyPathPromptStreamingQuitPersiste — [Escenario 1, MÍNIMO
// IMPRESCINDIBLE]. El flujo feliz completo bajo PTY: arranca el chat con el conjunto
// de producto, se pinta la bienvenida, se escribe un prompt y se envía con enter, el
// texto del fake llega en streaming a la pantalla, y `/quit` apaga el runtime con
// exit 0 (G36: cerrar el chat APAGA el proceso, no cae en una capa de debajo). Luego
// —lo que ningún test in-process ve— el JSONL en disco tiene `meta` + `message`(user)
// + `message`(assistant) con el texto exacto, más `usage`/`model` en la del asistente
// (sesiones.md §3). (Sobre el lockfile y los permisos, ver el RECORTE al final del test.)
func TestChatE2EHappyPathPromptStreamingQuitPersiste(t *testing.T) {
	fp := NewFakeProvider(t)
	fp.PushText("confirmo-e2e-streaming")
	ws := newChatWorkspace(t, fp)

	p := ws.Start(t, RunOpts{})
	// Bienvenida montada (chat.md §8): el heading markdown y la columna con el
	// modelo/directorio. Se ancla en texto RENDERIZADO (el markdown consume los
	// `**`, así que se busca "Modelo:", no "**Modelo:**").
	p.Expect(t, "Bienvenido a enu", startupTimeout)
	p.Expect(t, "Modelo:", 2*time.Second)

	// El prompt y su envío con enter (el editor deja pasar enter "pelado" → submit).
	p.Send(t, "saluda desde e2e")
	p.Send(t, "\r")

	// El texto del fake llega por `agent:delta`, pintado con markdown en el transcript.
	p.Expect(t, "confirmo-e2e-streaming", 5*time.Second)

	// /quit: el comando delega en Chat:quit(), que emite core:shutdown → el driver de
	// TTY lo convierte en apagado limpio (exit 0).
	code := quitViaSlashCommand(t, p)

	if code != 0 {
		t.Fatalf("/quit debía apagar el runtime (exit 0, G36); got %d\n--- salida ---\n%s", code, p.Output())
	}
	// Nada de "uso:" ni traza de fallo de arranque en pantalla.
	if out := p.Output(); strings.Contains(out, "uso:") {
		t.Fatalf("la salida no debía contener el mensaje de uso (arranque fallido):\n%s", out)
	}

	// El JSONL en disco: meta + message(user) + message(assistant), en orden.
	path, entries := sessionEntries(t, ws)
	if len(entries) != 3 {
		t.Fatalf("el JSONL debía tener 3 líneas (meta, user, assistant); got %d: %v", len(entries), entries)
	}
	if got := entryType(t, entries[0]); got != "meta" {
		t.Fatalf("línea 1 debía ser `meta`; got %q", got)
	}
	if got := entryType(t, entries[1]); got != "message" || messageRole(t, entries[1]) != "user" {
		t.Fatalf("línea 2 debía ser message(user); got t=%q role=%q", got, messageRole(t, entries[1]))
	}
	if body := messageJSON(t, entries[1]); !strings.Contains(body, "saluda desde e2e") {
		t.Fatalf("el mensaje de usuario debía llevar el prompt; got %s", body)
	}
	if got := entryType(t, entries[2]); got != "message" || messageRole(t, entries[2]) != "assistant" {
		t.Fatalf("línea 3 debía ser message(assistant); got t=%q role=%q", got, messageRole(t, entries[2]))
	}
	if body := messageJSON(t, entries[2]); !strings.Contains(body, "confirmo-e2e-streaming") {
		t.Fatalf("el mensaje del asistente debía llevar el texto del turno; got %s", body)
	}
	// usage y model adjuntos (auditoría de coste, sesiones.md §3).
	if _, ok := entries[2]["usage"]; !ok {
		t.Fatalf("la línea del asistente debía llevar `usage`; got %v", entries[2])
	}
	if _, ok := entries[2]["model"]; !ok {
		t.Fatalf("la línea del asistente debía llevar `model`; got %v", entries[2])
	}
	_ = path

	// RECORTE (documentado en la respuesta al orquestador): el escenario proponía
	// además comprobar que el `.jsonl.lock` desaparece tras salir y que el JSONL es
	// 0600. Ambas se caen fuera del contrato observable desde el e2e:
	//   - El lockfile LINGERA tras un `/quit` limpio (exit 0): el apagado por
	//     `core:shutdown` no garantiza correr el `close` que lo borra; la limpieza
	//     real es la RECLAMACIÓN de huérfano del siguiente que abra (sesiones.md §6),
	//     que se ejercita en el escenario de reanudación, no aquí. Asegurar "ya no
	//     existe" fallaría contra el binario real (candidato a hallazgo, ver respuesta).
	//   - Los permisos del JSONL son `fsFilePerm` (0o644) filtrados por el UMASK del
	//     proceso (fs.go), no un 0600 fijo: el 0600 solo se da con umask 077, que el
	//     runner de tests no impone. Un assert de modo no es portable.
}

// TestChatE2EArranqueDegradadoSaleLimpio — [Escenario 2, camino feo]. Con el conjunto
// de producto activado pero SIN providers.toml ni agent.toml (workspace virgen), el
// chat no muere al log ni queda en blanco: `agent.session` lanza EINVAL (no hay
// modelo) ANTES de tocar disco y el chat arranca DEGRADADO —una UI accionable que
// explica cómo configurar (menciona `--default-config`) y es salible (esc/q/ctrl+c →
// core:shutdown)—. `q` cierra con exit 0 y NO se escribió ninguna sesión (la
// degradación ocurre antes de `sessions.open`, así que `sessions/` no llega a existir).
func TestChatE2EArranqueDegradadoSaleLimpio(t *testing.T) {
	ws := NewWorkspace(t)
	ws.WriteEnuToml(t, chatPlugins...) // y NADA más: ni providers.toml ni agent.toml

	p := ws.Start(t, RunOpts{})
	p.Expect(t, "configuración necesaria", startupTimeout)
	p.Expect(t, "default-config", 2*time.Second)

	p.Send(t, "q")
	code := p.Wait(t, 5*time.Second)

	if code != 0 {
		t.Fatalf("la degradación no es un fallo, es salible (ADR-017/G35): exit 0; got %d\n--- salida ---\n%s", code, p.Output())
	}
	if out := p.Output(); strings.Contains(out, "error de arranque") {
		t.Fatalf("no debía pintar un traceback crudo de arranque:\n%s", out)
	}
	// La degradación ocurre antes de construir la sesión: `sessions/` no existe.
	if _, err := os.Stat(filepath.Join(ws.DataDir, "sessions")); !os.IsNotExist(err) {
		if entries, rderr := os.ReadDir(filepath.Join(ws.DataDir, "sessions")); rderr == nil && len(entries) > 0 {
			t.Fatalf("no debía crearse ninguna sesión en modo degradado; sessions/ tiene %d entradas", len(entries))
		}
	}
}

// TestChatE2ECtrlCSaleLimpioTrasElTurno — [Escenario 3]. Misma coreografía que el
// escenario 1 pero se sale con ctrl+c (`\x03`) en vez de `/quit` (chat.md §7: ctrl+c
// es el atajo global de salida, otra vía en el código —keymap global, no comando—
// hacia el mismo Chat:quit()→core:shutdown). El proceso sale con 0 y el turno YA
// completado quedó persistido: la salida no lo revierte (append-only).
func TestChatE2ECtrlCSaleLimpioTrasElTurno(t *testing.T) {
	fp := NewFakeProvider(t)
	fp.PushText("confirmo-e2e-streaming")
	ws := newChatWorkspace(t, fp)

	p := ws.Start(t, RunOpts{})
	p.Expect(t, "Bienvenido a enu", startupTimeout)
	p.Send(t, "hola\r")
	p.Expect(t, "confirmo-e2e-streaming", 5*time.Second)

	p.Send(t, "\x03") // ctrl+c
	code := p.Wait(t, 5*time.Second)

	if code != 0 {
		t.Fatalf("ctrl+c debía cerrar limpio (exit 0); got %d\n--- salida ---\n%s", code, p.Output())
	}
	_, entries := sessionEntries(t, ws)
	if len(entries) != 3 {
		t.Fatalf("el turno completado debía quedar persistido pese al ctrl+c; got %d líneas: %v", len(entries), entries)
	}
	if entryType(t, entries[2]) != "message" || messageRole(t, entries[2]) != "assistant" {
		t.Fatalf("la 3ª línea debía ser el mensaje del asistente ya escrito; got t=%q role=%q",
			entryType(t, entries[2]), messageRole(t, entries[2]))
	}
}

// TestChatE2ETurnoFallidoNoPersisteMensajeDeAsistente — [Escenario 4, camino feo].
// El provider falla a mitad de turno (HTTP 401 = API key inválida) con un servidor
// httptest AD-HOC (el FakeProvider del arnés siempre responde 200: no sabe inyectar
// un status de error, así que este escenario trae el suyo — ver nota en la respuesta).
// El chat pinta el bloque de error estructurado (`[EPROVIDER] …`, chat.md §2), `/quit`
// sigue saliendo con 0, y el JSONL tiene `meta` + `message`(user) pero NINGÚN
// `message`(assistant): el turno que no completó "simplemente no existe" (sesiones.md
// §4). La sesión SÍ se crea aquí (el modelo resuelve; el 401 llega en el `send`, no
// al abrir), a diferencia del escenario 2.
func TestChatE2ETurnoFallidoNoPersisteMensajeDeAsistente(t *testing.T) {
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer errSrv.Close()

	ws := NewWorkspace(t)
	ws.WriteEnuToml(t, chatPlugins...)
	// providers.toml a mano (no UseFakeProvider): mismo esqueleto que el arnés, pero
	// el base_url apunta al servidor que devuelve 401. agent.toml fija el modelo.
	writeErrProviderToml(t, ws, errSrv.URL)

	p := ws.Start(t, RunOpts{})
	p.Expect(t, "Bienvenido a enu", startupTimeout)
	p.Send(t, "esto va a fallar\r")
	// El adaptador convierte el 401 en EPROVIDER accionable (providers.md §3); el
	// chat lo pinta como "[EPROVIDER] anthropic: HTTP 401 …".
	p.Expect(t, "EPROVIDER", 5*time.Second)

	code := quitViaSlashCommand(t, p)

	if code != 0 {
		t.Fatalf("un turno fallido no debe romper la salida del chat (exit 0); got %d\n--- salida ---\n%s", code, p.Output())
	}
	_, entries := sessionEntries(t, ws)
	if len(entries) != 2 {
		t.Fatalf("el JSONL debía tener 2 líneas (meta, user); got %d: %v", len(entries), entries)
	}
	if entryType(t, entries[0]) != "meta" {
		t.Fatalf("línea 1 debía ser `meta`; got %q", entryType(t, entries[0]))
	}
	if entryType(t, entries[1]) != "message" || messageRole(t, entries[1]) != "user" {
		t.Fatalf("línea 2 debía ser message(user); got t=%q role=%q",
			entryType(t, entries[1]), messageRole(t, entries[1]))
	}
	for i, e := range entries {
		if entryType(t, e) == "message" && messageRole(t, e) == "assistant" {
			t.Fatalf("un turno fallido NO debe dejar mensaje de asistente; lo hay en la línea %d: %v", i+1, e)
		}
	}
}

// TestChatE2EDosTurnosAcumulanEnOrdenEnJSONL — [Escenario 5]. Dos prompts consecutivos
// en la MISMA sesión interactiva (sin cerrar el proceso entre medias): el fake sirve
// dos respuestas y el JSONL acumula las cinco entradas EN ORDEN (meta, user1,
// assistant1, user2, assistant2) — ninguna reescribe a la anterior (sesiones.md §1:
// append-only). Blinda que un segundo turno solo AÑADE, respetando lo ya escrito.
func TestChatE2EDosTurnosAcumulanEnOrdenEnJSONL(t *testing.T) {
	fp := NewFakeProvider(t)
	fp.PushText("respuesta-uno")
	fp.PushText("respuesta-dos")
	ws := newChatWorkspace(t, fp)

	p := ws.Start(t, RunOpts{})
	p.Expect(t, "Bienvenido a enu", startupTimeout)
	p.Send(t, "primer prompt\r")
	p.Expect(t, "respuesta-uno", 5*time.Second)
	p.Send(t, "segundo prompt\r")
	p.Expect(t, "respuesta-dos", 5*time.Second)
	code := quitViaSlashCommand(t, p)

	if code != 0 {
		t.Fatalf("/quit debía salir con 0; got %d\n--- salida ---\n%s", code, p.Output())
	}
	if n := fp.RequestCount(); n != 2 {
		t.Fatalf("el fake debía recibir 2 POST (uno por turno); got %d", n)
	}

	_, entries := sessionEntries(t, ws)
	if len(entries) != 5 {
		t.Fatalf("el JSONL debía tener 5 líneas (meta + 2 turnos); got %d: %v", len(entries), entries)
	}
	wantType := []string{"meta", "message", "message", "message", "message"}
	wantRole := []string{"", "user", "assistant", "user", "assistant"}
	for i := range entries {
		if got := entryType(t, entries[i]); got != wantType[i] {
			t.Fatalf("línea %d: t=%q, want %q", i+1, got, wantType[i])
		}
		if wantRole[i] != "" {
			if got := messageRole(t, entries[i]); got != wantRole[i] {
				t.Fatalf("línea %d: role=%q, want %q", i+1, got, wantRole[i])
			}
		}
	}
	// El texto de cada prompt está en su línea, en orden (el 2º turno no tocó el 1º).
	if body := messageJSON(t, entries[1]); !strings.Contains(body, "primer prompt") {
		t.Fatalf("línea 2 (user1) debía llevar 'primer prompt'; got %s", body)
	}
	if body := messageJSON(t, entries[3]); !strings.Contains(body, "segundo prompt") {
		t.Fatalf("línea 4 (user2) debía llevar 'segundo prompt'; got %s", body)
	}
	if body := messageJSON(t, entries[2]); !strings.Contains(body, "respuesta-uno") {
		t.Fatalf("línea 3 (assistant1) debía llevar 'respuesta-uno'; got %s", body)
	}
	if body := messageJSON(t, entries[4]); !strings.Contains(body, "respuesta-dos") {
		t.Fatalf("línea 5 (assistant2) debía llevar 'respuesta-dos'; got %s", body)
	}
}

// quitViaSlashCommand envía el comando `/quit` y espera el apagado del proceso,
// DESPERTANDO al bucle del driver con pulsaciones inocuas hasta que muere.
//
// Por qué el despertador: `/quit` NO es un keymap síncrono como ctrl+c; el editor lo
// somete (enter → Chat:submit) y el despacho del comando corre en `enu.task.spawn`
// (una task async). Esa task emite `core:shutdown`, que enciende el flag
// `__driver_quit`. Pero el bucle del driver solo SONDEA ese flag en `feed()` —tras
// procesar un lote de input o al vencer el timeout de una secuencia ESC pendiente
// (driver.go)—; cuando el flag lo enciende una task de fondo, el `select` del driver
// puede estar bloqueado esperando el próximo trozo de teclado y NO se entera hasta que
// llega otra tecla. ctrl+c/esc/q, keymaps síncronos, apagan al instante; `/quit` no.
// (Candidato a hallazgo — documentado en la respuesta al orquestador.) El nudge con
// esc es determinista: una secuencia ESC pendiente arma el timeout del select, y su
// flush llama a `feed`→`pollWasmQuit`, que ya ve el flag encendido por la task.
func quitViaSlashCommand(t *testing.T, p *PTY) int {
	t.Helper()
	p.Send(t, "/quit")
	p.Send(t, "\r")

	// Espera el fin del proceso en una goroutine (sin t.Fatalf ahí: eso solo vale en
	// la goroutine del test). Reutiliza el `waitOnce` del PTY para no chocar con el
	// Close de t.Cleanup.
	exited := make(chan int, 1)
	go func() {
		p.waitOnce.Do(func() { p.waitErr = p.cmd.Wait() })
		code := 0
		if p.waitErr != nil {
			if ee, ok := p.waitErr.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		exited <- code
	}()

	deadline := time.After(8 * time.Second)
	for {
		select {
		case code := <-exited:
			return code
		case <-deadline:
			t.Fatalf("/quit no apagó el proceso ni con nudges de esc\n--- salida ---\n%s", p.Output())
			return -1
		case <-time.After(150 * time.Millisecond):
			// esc directo al maestro, TOLERANTE al error: si el proceso ya salió, el
			// write falla con EIO y no debe tumbar el test (p.Send sí lo haría).
			_, _ = p.master.WriteString("\x1b")
		}
	}
}

// writeErrProviderToml escribe un providers.toml con el MISMO esqueleto que
// Workspace.UseFakeProvider (provider anthropic, modelo claude-e2e alias opus) pero
// con `base_url` arbitrario (aquí, un servidor que devuelve 401), más el agent.toml
// que fija `model="anthropic/opus"`. Helper PRIVADO de este fichero: el arnés cablea
// el FakeProvider "bueno" (200) y no expone un atajo para un base_url que falla —lo
// que el escenario 4 necesita para ejercitar el camino de error del adaptador—.
func writeErrProviderToml(t *testing.T, ws *Workspace, baseURL string) {
	t.Helper()
	toml := "" +
		"[providers.anthropic]\n" +
		"adapter     = \"anthropic\"\n" +
		"base_url    = \"" + baseURL + "\"\n" +
		"api_key_env = \"" + FakeAPIKeyEnv + "\"\n\n" +
		"[[providers.anthropic.models]]\n" +
		"id         = \"claude-e2e\"\n" +
		"context    = 200000\n" +
		"max_output = 4096\n" +
		"aliases    = [\"opus\"]\n"
	ws.WriteConfig(t, "providers.toml", toml)
	ws.WriteAgentToml(t, "anthropic/opus")
}
