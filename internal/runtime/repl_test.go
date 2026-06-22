package runtime

// Tests de la extensión oficial `repl` (S44, embebida en
// internal/runtime/embedded/repl). Es un **REPL de Lua** sobre la API pública
// congelada (Fase 8, ADR-003: el core NO sabe lo que es un REPL), así que la
// prueba es Go que arranca un Runtime con la extensión ACTIVADA por `nu.toml`
// (`plugins.enabled = ["repl"]`, igual que el gating de S12) y ejercita el
// contrato desde Lua, requiriendo el módulo con `require("repl")`.
//
// Blinda el contrato de [arquitectura.md](../../docs/arquitectura.md)
// §"Distribución" (G21):
//
//   - **ACTIVABLE SOLO** (G21, el criterio de hecho de S44): `nu` con SOLO `repl`
//     en `plugins.enabled` (sin el harness: ni agent, ni chat, ni toolkit) carga el
//     repl (source="builtin") y evalúa Lua. Es la prueba de que el runtime sirve
//     sin el agente.
//   - **EVALÚA Lua arbitrario con la API pública**: `repl.eval` compila con
//     `load`/`loadstring` (que el sandbox de S01 NO retiró —memoria, no disco—) y
//     ejecuta: expresiones (`1+1`→2), sentencias (`x=5`), llamadas a la API
//     (`nu.version.api`), errores (capturados, no tumban el repl) e incompletitud
//     (multilínea). NO hizo falta una primitiva nueva (corolario de completitud
//     satisfecho; APILevel sigue en 2).
//   - **código ⏸ vía task** (`eval_in_task`): una línea que llama a una función
//     suspendiente del core (`nu.fs.read`) se evalúa dentro de una task.
//
// La parte INTERACTIVA (leer teclas del TTY) necesita `nu.ui` (G20): un test la
// fuerza con `WithForceUI(true)`+`WithUISize` (como toolkit/chat) y comprueba que
// la UI se monta, pinta el banner y, al enviar una línea, evalúa y muestra el
// resultado en la rejilla del compositor. Lo PROBADO de verdad es la lógica de
// EVAL; el driver de teclado real (elegir, ver el efecto) es manual con TTY.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bootRepl arranca un Runtime con SOLO la extensión `repl` activada por nu.toml
// (sin el harness: la prueba de "activable solo", G21). Headless por defecto (sin
// UI): ejercita la lógica de eval. Devuelve el harness.
func bootRepl(t *testing.T) *harness {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"repl\"]\n")
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}
}

// bootReplUI arranca un Runtime con `repl` + `toolkit` activados (el repl usa el
// toolkit para su UI), `nu.ui` forzada (headless, G20) y un tamaño conocido, ya
// con Boot hecho. Para el test del driver interactivo.
func bootReplUI(t *testing.T, w, h int) *harness {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"toolkit\", \"repl\"]\n")
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(true), WithUISize(w, h))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}
}

// gridHas escanea las `h` filas de la pantalla compuesta buscando `needle`. Toma el
// token (gopher-lua no es thread-safe: leer el compositor se hace bajo el token,
// como en chat_test/toolkit_test). Devuelve true si alguna fila lo contiene.
func gridHas(h *harness, rows int, needle string) bool {
	var found bool
	withToken(h.rt, func() {
		for y := 0; y < rows; y++ {
			if containsStr(composeRow(h.rt.ui.comp, y), needle) {
				found = true
				return
			}
		}
	})
	return found
}

// gridText vuelca las `rows` filas de la pantalla compuesta a un string multilínea,
// para los mensajes de fallo (ver qué se pintó). Bajo el token.
func gridTextDump(h *harness, rows int) string {
	var b strings.Builder
	withToken(h.rt, func() {
		for y := 0; y < rows; y++ {
			b.WriteString(composeRow(h.rt.ui.comp, y))
			b.WriteByte('\n')
		}
	})
	return b.String()
}

// TestReplActivableSolo (G21, el criterio de hecho de S44): el repl se carga con
// SOLO él en plugins.enabled (sin el harness), source="builtin", y su módulo expone
// la superficie de eval. Es la prueba de que el runtime sirve sin el agente.
func TestReplActivableSolo(t *testing.T) {
	h := bootRepl(t)
	if src := listSource(h, "repl"); src != "builtin" {
		t.Fatalf(`repl debía cargarse con source="builtin"; got %q`, src)
	}
	// NINGÚN otro plugin del harness está cargado (activable solo).
	for _, other := range []string{"agent", "chat", "toolkit", "providers", "sessions", "mcp"} {
		if src := listSource(h, other); src != "" {
			t.Fatalf("con solo repl activo, %q no debía cargarse; got source=%q", other, src)
		}
	}
	h.expectEval(`
		local repl = require("repl")
		assert(type(repl.eval) == "function", "eval")
		assert(type(repl.eval_in_task) == "function", "eval_in_task")
		assert(type(repl.start) == "function", "start")
		assert(type(repl.banner) == "function", "banner")
		return "ok"`, "ok")
}

// TestReplEvalExpresion: una EXPRESIÓN suelta se evalúa y devuelve su valor sin que
// el usuario escriba `return` (el truco return<expr> del REPL de Lua). 1+1 → "2".
func TestReplEvalExpresion(t *testing.T) {
	h := bootRepl(t)
	h.expectEval(`
		local repl = require("repl")
		local r = repl.eval("1 + 1")
		assert(r.ok, "ok")
		assert(r.display == "2", "display='2' got "..tostring(r.display))
		assert(r.values[1] == 2, "values[1]==2")
		return "ok"`, "ok")

	// expresión con varios valores de retorno → unidos por tab, nils preservados.
	h.expectEval(`
		local repl = require("repl")
		local r = repl.eval("1, nil, 3")
		assert(r.ok, "ok")
		assert(r.n == 3, "n==3 got "..tostring(r.n))
		assert(r.display == "1\tnil\t3", "display got "..tostring(r.display))
		return "ok"`, "ok")

	// un string se entrecomilla (se distingue de un identificador).
	h.expectEval(`
		local repl = require("repl")
		local r = repl.eval('"hola"')
		assert(r.ok and r.display == '"hola"', "string entrecomillado got "..tostring(r.display))
		return "ok"`, "ok")
}

// TestReplEvalSentencia: una SENTENCIA (asignación) se ejecuta —efecto visible— y
// NO imprime resultado (display vacío, n=0), como el REPL de referencia.
func TestReplEvalSentencia(t *testing.T) {
	h := bootRepl(t)
	h.expectEval(`
		local repl = require("repl")
		REPL_X = nil
		local r = repl.eval("REPL_X = 5")
		assert(r.ok, "ok")
		assert(r.n == 0, "una sentencia no retorna nada; n="..tostring(r.n))
		assert(r.display == "", "display vacío got "..tostring(r.display))
		assert(REPL_X == 5, "el efecto colateral se aplicó; REPL_X="..tostring(REPL_X))
		return "ok"`, "ok")

	// un bucle (sentencia compuesta) corre completo.
	h.expectEval(`
		local repl = require("repl")
		REPL_SUM = 0
		local r = repl.eval("for i=1,3 do REPL_SUM = REPL_SUM + i end")
		assert(r.ok and r.display == "", "bucle sin retorno")
		assert(REPL_SUM == 6, "el bucle corrió; REPL_SUM="..tostring(REPL_SUM))
		return "ok"`, "ok")
}

// TestReplEvalLlamadaAPI: una llamada a la API pública NO suspendiente se evalúa
// directamente y devuelve su valor. nu.version.api → "2" (el nivel actual).
func TestReplEvalLlamadaAPI(t *testing.T) {
	h := bootRepl(t)
	h.expectEval(`
		local repl = require("repl")
		local r = repl.eval("nu.version.api")
		assert(r.ok, "ok")
		assert(r.values[1] == nu.version.api, "el valor es el de la API")
		assert(r.display == tostring(nu.version.api), "display got "..tostring(r.display))
		return "ok"`, "ok")

	// otra primitiva no-⏸: nu.text.width (CPU puro, §10).
	h.expectEval(`
		local repl = require("repl")
		local r = repl.eval('nu.text.width("hola")')
		assert(r.ok and r.values[1] == 4, "width hola==4 got "..tostring(r.display))
		return "ok"`, "ok")
}

// TestReplEvalError: un error LANZADO por el código del usuario se CAPTURA (ok=false)
// y se formatea; NO tumba el repl. Cubre un error estructurado del core (§1.4: la
// forma code:message se preserva, invariante de S02) y un error("texto") plano.
func TestReplEvalError(t *testing.T) {
	h := bootRepl(t)

	// error plano error("boom").
	h.expectEval(`
		local repl = require("repl")
		local r = repl.eval('error("boom")')
		assert(r.ok == false, "ok==false")
		assert(tostring(r.display):find("boom", 1, true), "display menciona boom got "..tostring(r.display))
		return "ok"`, "ok")

	// error estructurado del core: la forma code:message se preserva (S02). Forzamos
	// uno con un código reservado lanzado desde el propio código de usuario.
	h.expectEval(`
		local repl = require("repl")
		local r = repl.eval('error({ code = "ENOENT", message = "no existe" })')
		assert(r.ok == false, "ok==false")
		assert(r.display == "ENOENT: no existe", "code:message got "..tostring(r.display))
		assert(type(r.error) == "table" and r.error.code == "ENOENT", "error estructurado intacto")
		return "ok"`, "ok")

	// error de RUNTIME de Lua (indexar nil): se captura igual.
	h.expectEval(`
		local repl = require("repl")
		local r = repl.eval("local t = nil; return t.x")
		assert(r.ok == false, "ok==false")
		assert(r.display ~= "", "hay mensaje de error")
		return "ok"`, "ok")

	// tras un error, el repl SIGUE evaluando (no se rompió).
	h.expectEval(`
		local repl = require("repl")
		repl.eval('error("primero")')
		local r = repl.eval("2 + 2")
		assert(r.ok and r.display == "4", "el repl sigue vivo tras un error")
		return "ok"`, "ok")
}

// TestReplEvalSintaxis: un error de SINTAXIS real (token inesperado, no fin
// prematuro) se reporta como error, NO como incompleto.
func TestReplEvalSintaxis(t *testing.T) {
	h := bootRepl(t)
	h.expectEval(`
		local repl = require("repl")
		local r = repl.eval("return )")
		assert(r.ok == false, "ok==false")
		assert(r.incomplete ~= true, "NO es incompleto (es un error real)")
		assert(r.display ~= "", "hay mensaje de error de sintaxis")
		return "ok"`, "ok")
}

// TestReplEvalIncompleta (multilínea): una entrada con un bloque/función/string sin
// cerrar se marca INCOMPLETA (incomplete=true), no como error: es la señal de "dame
// otra línea". gopher-lua la distingue por "at EOF" en el mensaje.
func TestReplEvalIncompleta(t *testing.T) {
	h := bootRepl(t)
	for _, src := range []string{
		"function f()",
		"if true then",
		"for i=1,3 do",
		"local t = {",
	} {
		h.expectEval(`
			local repl = require("repl")
			local r = repl.eval([[`+src+`]])
			assert(r.ok == false, "ok==false para incompleta: `+src+`")
			assert(r.incomplete == true, "incomplete==true para: `+src+`")
			return "ok"`, "ok")
	}

	// al COMPLETAR el bloque (concatenando líneas), ya evalúa.
	h.expectEval(`
		local repl = require("repl")
		local src = "for i=1,3 do REPL_ACC = (REPL_ACC or 0) + i end"
		local r = repl.eval(src)
		assert(r.ok, "el bloque completo evalúa")
		assert(REPL_ACC == 6, "corrió; REPL_ACC="..tostring(REPL_ACC))
		return "ok"`, "ok")
}

// TestReplEvalEnTask (código ⏸): una línea que llama a una función SUSPENDIENTE del
// core (nu.fs.read) se evalúa dentro de una task vía eval_in_task. Es lo que permite
// al REPL usar TODA la API pública, no solo la no-⏸.
func TestReplEvalEnTask(t *testing.T) {
	h := bootRepl(t)
	// un fichero temporal con contenido conocido, leído por el código del usuario.
	dir := t.TempDir()
	p := filepath.Join(dir, "saludo.txt")
	if err := os.WriteFile(p, []byte("hola repl"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	h.expectEval(`
		REPL_OUT = nil
		local repl = require("repl")
		repl.eval_in_task('nu.fs.read([[` + p + `]])', function(result)
			REPL_OUT = result
		end)`)
	// la task ya progresó (soltó el token); leemos el resultado.
	h.expectEval(`
		assert(REPL_OUT ~= nil, "el callback corrió")
		assert(REPL_OUT.ok, "ok")
		assert(REPL_OUT.display == [["hola repl"]], "leyó el fichero; display="..tostring(REPL_OUT.display))
		return "ok"`, "ok")

	// y un ⏸ que LANZA (leer un fichero inexistente) se captura como error.
	h.expectEval(`
		REPL_ERR = nil
		local repl = require("repl")
		repl.eval_in_task('nu.fs.read([[/no/existe/jamas]])', function(result)
			REPL_ERR = result
		end)`)
	h.expectEval(`
		assert(REPL_ERR ~= nil and REPL_ERR.ok == false, "el ⏸ que lanza se captura")
		return "ok"`, "ok")
}

// TestReplEvalTipoInvalido: repl.eval con algo que no es string → EINVAL (la API de
// la extensión rechaza el mal uso, ADR-009).
func TestReplEvalTipoInvalido(t *testing.T) {
	h := bootRepl(t)
	se := h.evalErr(`local repl = require("repl"); return repl.eval(123)`)
	if se.Code != "EINVAL" {
		t.Fatalf("repl.eval(123) debía lanzar EINVAL; got %q", se.Code)
	}
}

// TestReplStartHeadlessEINVAL (G20): repl.start sin UI (headless) lanza EINVAL
// accionable —el REPL interactivo necesita TTY; para evaluar sin TTY está
// repl.eval—. La acción (qué hacer) se nombra en el mensaje.
func TestReplStartHeadlessEINVAL(t *testing.T) {
	h := bootRepl(t) // headless (sin WithForceUI)
	se := h.evalErr(`local repl = require("repl"); return repl.start()`)
	if se.Code != "EINVAL" {
		t.Fatalf("repl.start headless debía lanzar EINVAL; got %q", se.Code)
	}
	if !strings.Contains(se.Message, "repl.eval") || !strings.Contains(strings.ToLower(se.Message), "tty") {
		t.Fatalf("el EINVAL debe ser accionable (nombrar TTY y repl.eval); got %q", se.Message)
	}
}

// TestReplBanner: el banner identifica el runtime (versión + nivel de API, §2): es
// texto del RUNTIME, no de un producto (filosofia.md §2).
func TestReplBanner(t *testing.T) {
	h := bootRepl(t)
	out := h.eval(`local repl = require("repl"); return repl.banner()`)
	banner := out[0]
	if !strings.Contains(banner, "nu ") || !strings.Contains(banner, "REPL") {
		t.Fatalf("el banner debe nombrar nu y REPL; got %q", banner)
	}
	if !strings.Contains(banner, "API") {
		t.Fatalf("el banner debe mostrar el nivel de API; got %q", banner)
	}
}

// TestReplInteractivo (el DRIVER TTY, con UI forzada G20): repl.start monta la UI
// (banner en el transcript), enviar una línea la EVALÚA y su resultado aparece en la
// rejilla del compositor. Es el camino completo input→eval→pintar, lo automatizable
// del driver (la elección con el teclado real es manual con TTY).
func TestReplInteractivo(t *testing.T) {
	h := bootReplUI(t, 60, 12)

	// monta la UI y deja el handle en una global; banner pintado.
	h.expectEval(`
		REPL = require("repl").start()
		assert(REPL ~= nil, "start devolvió la UI")
		return "ok"`, "ok")

	// el banner está en la rejilla (texto del runtime).
	if !gridHas(h, 12, "nu ") {
		t.Fatalf("el banner del repl no se pintó; rejilla:\n%s", gridTextDump(h, 12))
	}

	// "envía" una expresión: escribe en el input y dispara _submit (lo que hace el
	// keymap de enter). Evalúa en una task; tras soltar el token, el resultado está.
	h.expectEval(`
		REPL.input:set_value("21 * 2")
		REPL:_submit()
		return "ok"`, "ok")

	// el eco de la entrada ("> 21 * 2") y el resultado ("42") están en la rejilla.
	if !gridHas(h, 12, "> 21 * 2") {
		t.Fatalf("el eco de la entrada no se pintó; rejilla:\n%s", gridTextDump(h, 12))
	}
	if !gridHas(h, 12, "42") {
		t.Fatalf("el resultado de la evaluación no se pintó; rejilla:\n%s", gridTextDump(h, 12))
	}

	// un error se pinta sin tumbar el repl, y se sigue evaluando.
	h.expectEval(`
		REPL.input:set_value('error("ay")')
		REPL:_submit()
		return "ok"`, "ok")
	if !gridHas(h, 12, "ay") {
		t.Fatalf("el error no se pintó; rejilla:\n%s", gridTextDump(h, 12))
	}
	h.expectEval(`
		REPL.input:set_value("100 + 1")
		REPL:_submit()
		return "ok"`, "ok")
	if !gridHas(h, 12, "101") {
		t.Fatalf("el repl no siguió evaluando tras un error; rejilla:\n%s", gridTextDump(h, 12))
	}

	h.expectEval(`REPL:quit(); return "ok"`, "ok")
}

// TestReplInteractivoMultilinea (driver TTY): enviar una línea INCOMPLETA no evalúa
// (cambia el prompt a "..", acumula); al completar el bloque, evalúa y muestra el
// resultado. Es el modo multilínea por el lado del bucle.
func TestReplInteractivoMultilinea(t *testing.T) {
	h := bootReplUI(t, 70, 14)
	h.expectEval(`REPL = require("repl").start(); return "ok"`, "ok")

	// línea incompleta: un bloque do/end sin cerrar. No evalúa: acumula.
	h.expectEval(`
		REPL.input:set_value("do")
		REPL:_submit()
		assert(REPL.pending ~= "", "quedó pendiente (incompleta)")
		assert(REPL.prompt == "..", "prompt de continuación got "..tostring(REPL.prompt))
		return "ok"`, "ok")

	// completa el bloque: ahora evalúa.
	h.expectEval(`
		REPL.input:set_value("REPL_ML = 7 end")
		REPL:_submit()
		assert(REPL.pending == "", "el bloque se cerró")
		assert(REPL.prompt == ">", "prompt vuelve a normal")
		return "ok"`, "ok")
	// el efecto del bloque se aplicó (corrió en una task; ya progresó).
	h.expectEval(`assert(REPL_ML == 7, "el bloque multilínea corrió; REPL_ML="..tostring(REPL_ML)); return "ok"`, "ok")

	h.expectEval(`REPL:quit(); return "ok"`, "ok")
}
