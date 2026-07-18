package runtime

import (
	"fmt"
	"path/filepath"
	"testing"
)

// Tests de G45 — la superficie [W] de api.md §16 llega COMPLETA a los workers.
// Buena parte de esa superficie no son thunks del registro sino wrappers Lua de
// extraPreludio (enu.log.*, enu.re.compile, enu.text.*, enu.proc.spawn, enu.ws.connect,
// enu.http.stream, enu.search.grep); antes del arreglo, spawnWorker copiaba módulos
// y thunks pero nunca los wrappers, y todos esos módulos eran nil dentro del
// worker. La lógica 🔒 a blindar:
//
//   - **paridad con §16**: cada wrapper que la tabla de §16 declara [W] existe
//     dentro de un worker sin caps; lo solo-principal (fs.watch, events, ui)
//     sigue SIN existir — la copia discrimina por la marca worker-safe, no en
//     bloque.
//   - **operativos, no solo presentes**: los wrappers funcionan punta a punta en
//     el worker (re con capturas nombradas, Block de text, proceso real, grep
//     iterando, log sin lanzar) — la presencia sin los locals del preludio
//     (__hcall_s, __block_mt) sería un falso verde.
//   - **el gating de caps sigue en los thunks (G6)**: los wrappers cruzan en
//     bloque, pero un wrapper cuyo thunk subyacente fue podado por caps falla al
//     llamarlo (deny-by-default de §14); el concedido funciona.

// TestWorkerG45ParidadSuperficieW recorre, DESDE DENTRO de un worker sin caps, la
// tabla de disponibilidad de api.md §16: todo wrapper [W] debe ser una función y
// la superficie solo-principal no debe existir. El worker reporta la lista de
// discrepancias; el test espera la lista vacía.
func TestWorkerG45ParidadSuperficieW(t *testing.T) {
	h := workerHarness(t, `
		local mal = {}
		local function fn(nombre, v)
			if type(v) ~= "function" then mal[#mal+1] = nombre .. " ausente" end
		end
		-- Wrappers [W] de extraPreludio (la capa que G45 repara), por módulo de §16.
		fn("enu.log.debug",     enu.log and enu.log.debug)
		fn("enu.log.info",      enu.log and enu.log.info)
		fn("enu.log.warn",      enu.log and enu.log.warn)
		fn("enu.log.error",     enu.log and enu.log.error)
		fn("print",            print)
		fn("enu.re.compile",    enu.re and enu.re.compile)
		fn("enu.text.wrap",     enu.text and enu.text.wrap)
		fn("enu.text.markdown", enu.text and enu.text.markdown)
		fn("enu.text.highlight",enu.text and enu.text.highlight)
		fn("enu.text.diff",     enu.text and enu.text.diff)
		fn("enu.proc.spawn",    enu.proc and enu.proc.spawn)
		fn("enu.ws.connect",    enu.ws and enu.ws.connect)
		fn("enu.http.stream",   enu.http and enu.http.stream)
		fn("enu.search.grep",   enu.search and enu.search.grep)
		-- Solo estado principal (§16): la copia discrimina, no cruza en bloque.
		if enu.fs and enu.fs.watch ~= nil then mal[#mal+1] = "enu.fs.watch NO debe cruzar" end
		if enu.events ~= nil then mal[#mal+1] = "enu.events NO debe cruzar" end
		if enu.ui ~= nil then mal[#mal+1] = "enu.ui NO debe cruzar" end
		enu.worker.parent.send(table.concat(mal, "; "))
	`)

	h.eval(`
		G45MAL, G45DONE = nil, false
		enu.task.spawn(function()
			local w = enu.worker.spawn("wmod")
			G45MAL = w:recv()
			w:terminate()
			G45DONE = true
		end)
	`)
	h.expectEval(`return tostring(G45DONE)`, "true")
	h.expectEval(`return G45MAL`, "")
}

// TestWorkerG45WrappersOperativos blinda que los wrappers no solo existen sino que
// FUNCIONAN dentro del worker: dependen de locals del preludio (__hcall, __hcall_s,
// __block_mt) que deben estar en alcance también en el chunk del worker. Ejercita
// cada mecánica distinta: fusión de capturas (re), Block opaco (text), handle con
// métodos suspendientes (proc), iterador + cleanup (search sobre un árbol de
// fixtures), formateo a fichero (log), y el cableado wrapper→thunk de http.stream
// (EINVAL estructurado del thunk, sin tocar la red).
func TestWorkerG45WrappersOperativos(t *testing.T) {
	fixtures := t.TempDir()
	escribirFicheroTest(t, filepath.Join(fixtures, "a.txt"), "una aguja en el pajar\notra aguja\n")
	escribirFicheroTest(t, filepath.Join(fixtures, "b.txt"), "solo paja\n")

	h := workerHarness(t, `
		local root = enu.worker.parent.recv()
		local mal = {}

		-- re.compile: la fusión array+nombradas de la tabla de capturas es la razón
		-- de ser del wrapper (el wire no la cruza de una pieza). [1]=completo,
		-- [2..]=grupos, más los nombrados por clave.
		local re = enu.re.compile([[(?P<clave>\w+)=(\w+)]])
		local caps = re:match("color=azul")
		if not (caps and caps[1] == "color=azul" and caps[2] == "color"
			and caps[3] == "azul" and caps.clave == "color") then
			mal[#mal+1] = "re.match no fusiona capturas"
		end

		-- text.wrap: el wrapper envuelve {id,width,height} como Block OPACO (§10).
		local b = enu.text.wrap("hola mundo cruel", 6)
		if type(b.width) ~= "number" or type(b.height) ~= "number" then
			mal[#mal+1] = "text.wrap no devuelve un Block con dimensiones"
		end
		if b.lines ~= nil then mal[#mal+1] = "el Block debe ser opaco (.lines nil)" end

		-- log: formatea y escribe a fichero; no debe lanzar.
		local okl = pcall(function() enu.log.info("g45 en worker: %d", 42) end)
		if not okl then mal[#mal+1] = "enu.log.info lanza" end

		-- proc.spawn: handle con métodos suspendientes (__hcall_s) sobre un proceso real.
		local p = enu.proc.spawn({"echo", "g45"})
		local linea = p:read_line("stdout")   -- "g45\n": read_line conserva el \n
		local st = p:wait()
		if linea ~= "g45\n" then mal[#mal+1] = "proc read_line: " .. tostring(linea) end
		if st.code ~= 0 then mal[#mal+1] = "proc wait code: " .. tostring(st.code) end

		-- search.grep: iterador suspendiente + enu.task.cleanup (el cuerpo del worker
		-- ES una task, así que el cleanup tiene dónde registrarse).
		local vistos = 0
		for m in enu.search.grep("aguja", { root = root }) do vistos = vistos + 1 end
		if vistos ~= 2 then mal[#mal+1] = "grep vio " .. vistos .. " matches, esperaba 2" end

		-- http.stream: el cableado wrapper→thunk sin tocar la red — sin url el thunk
		-- responde EINVAL estructurado, prueba de que la llamada LLEGA al host.
		local okh, eh = pcall(function() return enu.http.stream({}) end)
		if okh or type(eh) ~= "table" or eh.code ~= "EINVAL" then
			mal[#mal+1] = "http.stream sin url no dio EINVAL estructurado"
		end

		enu.worker.parent.send(table.concat(mal, "; "))
	`)

	h.eval(fmt.Sprintf(`
		G45OPMAL, G45OPDONE = nil, false
		enu.task.spawn(function()
			local w = enu.worker.spawn("wmod")
			w:send(%q)
			G45OPMAL = w:recv()
			w:terminate()
			G45OPDONE = true
		end)
	`, fixtures))
	h.expectEval(`return tostring(G45OPDONE)`, "true")
	h.expectEval(`return G45OPMAL`, "")
}

// TestWorkerG45CapsPodanLosWrappers blinda que el arreglo respeta el sandboxing
// por capacidades (G6/§14) también en la capa de wrappers: la misma autoridad
// (workerGrants, sobre los thunks `needs` de cada snippet) decide qué wrappers
// cruzan. Con caps={"re"}: el wrapper concedido funciona punta a punta, y los
// módulos no concedidos NO EXISTEN dentro del worker — ni siquiera como tabla
// (lo que un `if enu.http then` de detección de superficie debe poder fiarse).
func TestWorkerG45CapsPodanLosWrappers(t *testing.T) {
	h := workerHarness(t, `
		local mal = {}
		local re = enu.re.compile([[(\w+)]])
		local caps = re:match("hola")
		if not (caps and caps[1] == "hola") then
			mal[#mal+1] = "re.compile no funciona con cap 're' concedida"
		end
		-- Lo no concedido no existe (§14), tampoco su capa de wrappers.
		if enu.log ~= nil then mal[#mal+1] = "enu.log existe sin la cap 'log'" end
		if enu.http ~= nil then mal[#mal+1] = "enu.http existe sin la cap 'http'" end
		if enu.proc ~= nil then mal[#mal+1] = "enu.proc existe sin la cap 'proc'" end
		enu.worker.parent.send(table.concat(mal, "; "))
	`)

	h.eval(`
		G45CAPMAL, G45CAPDONE = nil, false
		enu.task.spawn(function()
			local w = enu.worker.spawn("wmod", { caps = {"re"} })
			G45CAPMAL = w:recv()
			w:terminate()
			G45CAPDONE = true
		end)
	`)
	h.expectEval(`return tostring(G45CAPDONE)`, "true")
	h.expectEval(`return G45CAPMAL`, "")
}
