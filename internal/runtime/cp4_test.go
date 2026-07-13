package runtime

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// CP-4 · "Una herramienta de verdad, solo con primitivas" (checkpoint de
// integración tras S18, cierra la Fase 3 — IO, sistema y codecs). Es la prueba
// de humo / dogfooding temprano del plan: un script Lua que, **sin red ni UI y
// solo con primitivas del core**, recorre un repositorio, lee ficheros, lanza
// `git status` y emite un resumen JSON. Ejercita el **corolario de completitud**
// (filosofía §2): si alguna pieza no pudiera escribirse solo con la API pública,
// faltaría una primitiva y sería un hallazgo, no un atajo.
//
// ADAPTACIÓN (docs/decisiones-implementacion.md S18): el texto del plan menciona
// `nu.search.files` para el recorrido del repo, pero **esa primitiva es S27
// (Fase 5), posterior a este checkpoint de Fase 3**. Aquí se ejercita con un
// **recorrido recursivo en Lua sobre `nu.fs.list`** (disponible desde S14): la lista directa de un
// directorio + recursión por los subdirectorios. Es la sustitución más fiel —el
// mismo trabajo (enumerar el árbol) expresado con la primitiva que SÍ existe en
// la Fase 3—; `search.files` (recursión + filtrado en Go) llega en S27/CP-6.
//
// Las piezas que el checkpoint ejercita, todas del core:
//   - `nu.fs.list` (S14) — recorrido recursivo del árbol, escrito en Lua.
//   - `nu.fs.read` (S14) — lectura del contenido de cada fichero.
//   - `nu.proc.run` (S16) — `git status --porcelain` con `opts.cwd`, sin shell.
//   - `nu.json.encode` (S18) — el resumen final, con `pretty`.
//   - `nu.json.decode` (S18) — re-parseo del resumen para validarlo.

// TestCP4HerramientaSoloConPrimitivas monta un repo git temporal con un par de
// ficheros (uno trackeado y limpio, otro sin trackear), corre el script de
// dogfooding y comprueba que el resumen JSON refleja lo que el recorrido y
// `git status` ven.
func TestCP4HerramientaSoloConPrimitivas(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no disponible en el entorno; CP-4 lo necesita para `git status`")
	}

	repo := t.TempDir()
	// Un repo git mínimo y DETERMINISTA: init, un fichero comiteado (limpio) y
	// otro sin trackear (que `git status --porcelain` reportará como "??").
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		// Identidad y config mínimas para que `commit` no falle en CI.
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v falló: %v\n%s", args, err, out)
		}
	}
	writeFile := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit("init", "-q")
	writeFile(repo+"/README.md", "# repo de prueba\nlinea dos\n")
	runGit("add", "README.md")
	runGit("commit", "-q", "-m", "inicial")
	// Un fichero sin trackear: aparecerá en `git status --porcelain` como "??".
	writeFile(repo+"/notas.txt", "pendiente\n")
	// Un subdirectorio con un fichero, para ejercitar la RECURSIÓN de fs.list.
	if err := os.Mkdir(repo+"/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(repo+"/sub/dato.json", `{"clave":"valor"}`)

	h := newHarness(t)
	h.regStringFn("REPO", repo)

	// El script de dogfooding: recorrido recursivo + lectura + git status + resumen
	// JSON. Todo dentro de una task (las primitivas de fs/proc son ⏸).
	h.eval(`
		resumen_json = nil
		nu.task.spawn(function()
			local raiz = REPO()

			-- Recorrido recursivo del árbol con nu.fs.list (sustituye a search.files,
			-- S27): enumera el dir y recurre por los subdirectorios. Salta .git para
			-- no ahogarse en el interno del repo (lo mismo que haría search.files con
			-- su filtrado gitignore).
			local ficheros = {}
			local function recorrer(dir, rel)
				for _, e in ipairs(nu.fs.list(dir)) do
					if e.name ~= ".git" then
						local hijo = dir .. "/" .. e.name
						local relhijo = (rel == "" and e.name) or (rel .. "/" .. e.name)
						if e.is_dir then
							recorrer(hijo, relhijo)
						else
							ficheros[#ficheros + 1] = relhijo
						end
					end
				end
			end
			recorrer(raiz, "")
			table.sort(ficheros)

			-- Lee cada fichero y suma sus bytes (trabajo real sobre el contenido).
			local total_bytes = 0
			for _, rel in ipairs(ficheros) do
				local contenido = nu.fs.read(raiz .. "/" .. rel)
				total_bytes = total_bytes + #contenido
			end

			-- git status --porcelain con opts.cwd (sin shell, S16): parsea las
			-- líneas a {estado, ruta}.
			local r = nu.proc.run({ "git", "status", "--porcelain" }, { cwd = raiz })
			assert(r.code == 0, "git status debe salir con code 0")
			local cambios = {}
			for linea in r.stdout:gmatch("[^\n]+") do
				local estado = linea:sub(1, 2)
				local ruta = linea:sub(4)
				cambios[#cambios + 1] = { estado = estado, ruta = ruta }
			end

			-- Emite el resumen como JSON (pretty), la pieza de S18.
			resumen_json = nu.json.encode({
				raiz = raiz,
				num_ficheros = #ficheros,
				total_bytes = total_bytes,
				ficheros = ficheros,
				cambios = cambios,
			}, { pretty = true })
		end)
	`)

	out := h.eval(`return resumen_json`)
	if len(out) != 1 || strings.TrimSpace(out[0]) == "" {
		t.Fatalf("CP-4: el script no produjo resumen JSON: %q", out)
	}

	// El resumen es JSON válido y refleja lo esperado: re-parseado por el propio
	// nu.json.decode (cierra el círculo codec ida y vuelta) y validado en Go.
	h.expectEval(`
		local r = nu.json.decode(resumen_json)
		assert(type(r.raiz) == "string" and #r.raiz > 0, "raiz")
		-- README.md + notas.txt + sub/dato.json = 3 ficheros (.git excluido)
		assert(r.num_ficheros == 3, "num_ficheros (got " .. tostring(r.num_ficheros) .. ")")
		assert(r.total_bytes > 0, "total_bytes")
		-- notas.txt está sin trackear -> aparece en los cambios como "??"
		local visto_notas = false
		for _, c in ipairs(r.cambios) do
			if c.ruta == "notas.txt" and c.estado:find("?", 1, true) then visto_notas = true end
		end
		assert(visto_notas, "git status debe reportar notas.txt sin trackear")
		return "ok"
	`, "ok")
}
