package e2e

// Tests e2e de la extensión oficial `mesh` (contrato: docs/contracts/malla.md)
// contra el BINARIO real. La cobertura in-process (internal/runtime/mesh_test.go)
// ya ejercita specs, CAS secuencial en una VM, run_job y torneo dentro del mismo
// binario de test; aquí subimos un nivel: es el `enu` COMPILADO quien lanza `git`
// como subproceso de verdad, habla HTTP real contra el provider fake, y empuja las
// ramas/refs a un remoto bare — la capa que un test in-process no toca.
//
// La restricción que condiciona todos los escenarios: el CLI no tiene un modo
// "ejecuta un job de mesh"; todo pasa por `enu -e '<lua>'`, cuyo chunk corre en el
// ESTADO PRINCIPAL (no como task), mientras que TODAS las funciones de mesh son ⏸.
// Por eso cada escenario envuelve el trabajo de mesh en `enu.task.spawn(...)` (que
// `EvalString` drena antes de devolver) y comunica el desenlace por FICHERO en disco
// (`enu.fs.write`) y por lo que `mesh.run_job` deja en la rama del remoto — el
// retorno del propio `-e` se resuelve antes de que la task corra y no sirve.
//
// git es dependencia declarada de la extensión (malla.md §3): los tests se saltan si
// no está en el PATH, mismo criterio que la cobertura in-process.
//
// Nota de arnés: el paquete e2e no traía helpers de git ni un lanzador de proceso
// apto para goroutines (Run usa t.Fatalf, ilegal fuera de la goroutine de test). Se
// añaden aquí como helpers PRIVADOS (meshGit*, runMeshDetached); no se toca el arnés.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Helpers privados de git (portados de internal/runtime/mesh_test.go) ----------

// skipMeshIfNoGit salta el test si `git` no está en el PATH: la extensión mesh lo
// declara como dependencia, no el core (malla.md §3).
func skipMeshIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está en el PATH; la extensión mesh lo declara como dependencia (malla.md §3)")
	}
}

// meshGit corre `git <args>` con cwd=dir y falla el test si sale mal; devuelve la
// salida combinada trimmeada. Para el lado del CONTROLADOR (inspeccionar el bare).
func meshGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v en %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// meshGitTry corre `git <args>` y devuelve (salida, ok) sin fallar: para aserciones
// de EXISTENCIA (p. ej. `show-ref`, que sale != 0 cuando la ref no está).
func meshGitTry(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// meshGitFixture prepara un repo local con un commit base y un remoto bare `origin`.
// Devuelve (repo, bare, sha del commit base). El repo es donde mesh crea worktrees;
// el bare es el remoto al que empuja claims y ramas-resultado.
func meshGitFixture(t *testing.T) (repo, bare, sha string) {
	t.Helper()
	repo = t.TempDir()
	bare = t.TempDir()
	meshGit(t, repo, "init", "-q")
	meshGit(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "base")
	meshGit(t, bare, "init", "-q", "--bare")
	meshGit(t, repo, "remote", "add", "origin", bare)
	sha = meshGit(t, repo, "rev-parse", "HEAD")
	return repo, bare, sha
}

// runMeshDetached lanza el binario SIN pasar por `*testing.T` (ni t.Fatalf ni
// t.Helper), apto para invocarse desde una goroutine: el escenario 4 corre DOS
// procesos `enu` en paralelo compitiendo por la misma ref, y `Workspace.Run`
// llamaría a t.Fatalf desde una goroutine ajena a la de test (prohibido). Reusa el
// entorno hermético del workspace (`baseEnv`, mismo paquete) y su Workdir.
func runMeshDetached(ws *Workspace, args []string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, enuBin, args...)
	cmd.Dir = ws.Workdir
	cmd.Env = ws.baseEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res
	}
	if err == nil {
		res.ExitCode = 0
		return res
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
	} else {
		res.ExitCode = -2 // no arrancó; el llamante lo verá como código anómalo
	}
	return res
}

// parseKV parte un `outcome.txt` de líneas `clave=valor` en un mapa. El chunk de
// mesh comunica su desenlace escribiendo estas líneas con `enu.fs.write`.
func parseKV(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if i := strings.IndexByte(line, '='); i >= 0 {
			m[line[:i]] = line[i+1:]
		}
	}
	return m
}

// readOutcome lee el `outcome.txt` que el chunk dejó en el Workdir del workspace.
func readOutcome(t *testing.T, ws *Workspace) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(ws.Workdir, "outcome.txt"))
	if err != nil {
		t.Fatalf("no pude leer outcome.txt (el chunk de mesh no lo escribió): %v", err)
	}
	return parseKV(string(raw))
}

// runJobChunk arma el chunk `-e` del patrón "job declarativo": carga Role+Job,
// reclama el job, corre run_job y vuelca el desenlace a outcome.txt. `extra` se
// inserta ANTES de run_job (para registrar tools, escenario 5). Todas las rutas en
// forward-slash (rutas UTF-8 de la API; en macOS/Linux coinciden con las del SO).
func runJobChunk(ws *Workspace, repo, rolePath, jobPath, extra string) string {
	out := filepath.ToSlash(filepath.Join(ws.Workdir, "outcome.txt"))
	return `
enu.task.spawn(function()
  local ok, err = pcall(function()
    local agent = require("agent")
    local mesh  = require("mesh")
    ` + extra + `
    local role = mesh.role.load("` + filepath.ToSlash(rolePath) + `")
    local job  = mesh.job.load("` + filepath.ToSlash(jobPath) + `")
    assert(mesh.claim(job.id, { cwd = "` + filepath.ToSlash(repo) + `" }), "el claim inicial debía ganar")
    local r = mesh.run_job(job, role, { cwd = "` + filepath.ToSlash(repo) + `" })
    enu.fs.write("` + out + `",
      "ok=" .. tostring(r.ok) ..
      "\nbranch=" .. tostring(r.branch) ..
      "\ndenials=" .. tostring(#r.denials) ..
      "\nerr=" .. tostring(r.error and r.error.code or "nil"))
  end)
  if not ok then
    enu.fs.write("` + out + `", "crash=" .. tostring(type(err) == "table" and err.message or err))
  end
end)`
}

// bootMeshWorkspace monta un workspace con el conjunto de producto + mesh activados y
// cableado al provider fake (el adaptador anthropic REAL apunta a su base_url).
func bootMeshWorkspace(t *testing.T, fp *FakeProvider) *Workspace {
	t.Helper()
	ws := NewWorkspace(t)
	ws.WriteEnuToml(t, "providers", "sessions", "agent", "mesh")
	ws.UseFakeProvider(t, fp)
	return ws
}

// --- Escenario 1 (MÍNIMO IMPRESCINDIBLE) -----------------------------------------

// TestMeshE2ERunJobRealBinary: un job declarativo sin skills pineadas, de punta a
// punta con el binario real. El proceso sale 0; la rama-resultado aterriza en el
// bare con result.json/transcript.jsonl a bordo; el claim queda vivo (run_job NO lo
// libera: eso es del nodo-patrón §10); y el fake confirma que hubo HTTP real con
// x-api-key. Es la prueba de que la orquestación de mesh sobrevive al cruce
// binario→git-subproceso→remoto que el in-process no puede reproducir.
func TestMeshE2ERunJobRealBinary(t *testing.T) {
	skipMeshIfNoGit(t)
	fp := NewFakeProvider(t)
	ws := bootMeshWorkspace(t, fp)
	repo, bare, sha := meshGitFixture(t)

	rolePath := ws.WriteFile(t, "role.toml", `
model = "anthropic/opus"
[permissions]
mode = "ask"
allow = ["read"]
[budget]
max_turns = 3
`)
	jobPath := ws.WriteFile(t, "job.toml", `
id = "J-E2E-1"
base = "`+sha+`"
branch = "mesh/J-E2E-1"
prompt = "haz algo simple"
`)

	res := ws.Run(t, RunOpts{Args: []string{"-e", runJobChunk(ws, repo, rolePath, jobPath, "")}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}

	out := readOutcome(t, ws)
	if out["ok"] != "true" || out["branch"] != "mesh/J-E2E-1" || out["err"] != "nil" {
		t.Fatalf("outcome del job inesperado: %+v (stderr=%q)", out, res.Stderr)
	}

	// El lado del CONTROLADOR: la rama-resultado está en el bare con la auditoría.
	resultJSON := meshGit(t, bare, "show", "refs/heads/mesh/J-E2E-1:.enu/mesh/result.json")
	var parsed struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &parsed); err != nil {
		t.Fatalf("result.json no es JSON parseable: %v\n%s", err, resultJSON)
	}
	if parsed.JobID != "J-E2E-1" {
		t.Fatalf("result.json.job_id: got %q, want J-E2E-1", parsed.JobID)
	}
	transcript := meshGit(t, bare, "show", "refs/heads/mesh/J-E2E-1:.enu/mesh/transcript.jsonl")
	if strings.TrimSpace(transcript) == "" {
		t.Fatalf("el transcript.jsonl de la rama está vacío")
	}

	// El claim sigue vivo tras run_job (el runner no lo libera; §10).
	if _, ok := meshGitTry(t, bare, "show-ref", "--verify", "refs/enu/mesh/claims/J-E2E-1"); !ok {
		t.Fatalf("la claim-ref refs/enu/mesh/claims/J-E2E-1 debía existir tras run_job")
	}

	// El fake confirma HTTP real con la cabecera de auth.
	if fp.RequestCount() < 1 {
		t.Fatalf("el binario no habló HTTP con el provider (RequestCount=%d)", fp.RequestCount())
	}
	if got := fp.Header(0).Get("x-api-key"); got != FakeAPIKey {
		t.Fatalf("x-api-key: got %q, want %q", got, FakeAPIKey)
	}
}

// --- Escenario 2 -----------------------------------------------------------------

// TestMeshE2EDefaultConfigExcludesMesh: `enu --default-config` activa el conjunto
// oficial de producto SIN mesh (ADR-015/§1.4: mesh se enchufa explícitamente). Se
// verifica desde fuera (el fichero en disco) y desde dentro (una segunda invocación
// confirma que require("mesh") sobre ese enu.toml falla).
func TestMeshE2EDefaultConfigExcludesMesh(t *testing.T) {
	ws := NewWorkspace(t) // sin enu.toml previo: onramp limpio.

	res := ws.Run(t, RunOpts{Args: []string{"--default-config"}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "conjunto oficial de producto activado") {
		t.Fatalf("stdout no anuncia el onramp: %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "mesh") {
		t.Fatalf("el onramp no debía nombrar mesh en la lista activada: %q", res.Stdout)
	}

	// El enu.toml en disco (leído directamente, sin el binario) no menciona mesh.
	raw, err := os.ReadFile(filepath.Join(ws.ConfigDir, "enu.toml"))
	if err != nil {
		t.Fatalf("--default-config no escribió enu.toml: %v", err)
	}
	if strings.Contains(string(raw), "mesh") {
		t.Fatalf("plugins.enabled no debía contener mesh:\n%s", raw)
	}

	// Desde dentro: sobre ese mismo enu.toml, require("mesh") no resuelve.
	res2 := ws.Run(t, RunOpts{Args: []string{"-e",
		`local ok,e = pcall(require,"mesh"); return tostring(ok) .. "|" .. tostring(type(e)=="table" and e.code or e)`}})
	if res2.ExitCode != 0 {
		t.Fatalf("exit del probe: got %d, want 0 (stderr=%q)", res2.ExitCode, res2.Stderr)
	}
	got := strings.TrimSpace(res2.Stdout)
	if !strings.HasPrefix(got, "false|") {
		t.Fatalf("require(\"mesh\") debía fallar sobre el onramp por defecto; got %q", got)
	}
}

// --- Escenario 3 -----------------------------------------------------------------

// TestMeshE2EUnpinnedRoleSkipsRepoInjection: un Role SIN skills pineadas no marca el
// worktree como de confianza, así que el default headless (agente.md §11.2, malla.md
// §9) NO inyecta el enu.md del repo. El marcador único del enu.md no debe aparecer en
// el `system` del PRIMER request — inspeccionado directamente contra el fake, un
// proceso ajeno al `enu` bajo prueba.
func TestMeshE2EUnpinnedRoleSkipsRepoInjection(t *testing.T) {
	skipMeshIfNoGit(t)
	fp := NewFakeProvider(t)
	ws := bootMeshWorkspace(t, fp)
	repo, _, _ := meshGitFixture(t)

	// enu.md con un marcador único, commiteado en el base sha (así viaja al worktree
	// desanclado que run_job crea sobre ese sha).
	const marcador = "MARCADOR-9f3c-no-debe-viajar"
	if err := os.WriteFile(filepath.Join(repo, "enu.md"), []byte("# Repo\n"+marcador+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	meshGit(t, repo, "add", "-A")
	meshGit(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "enu.md")
	sha := meshGit(t, repo, "rev-parse", "HEAD")

	rolePath := ws.WriteFile(t, "role.toml", `
model = "anthropic/opus"
[permissions]
mode = "ask"
allow = ["read"]
`)
	jobPath := ws.WriteFile(t, "job.toml", `
id = "J-E2E-3"
base = "`+sha+`"
branch = "mesh/J-E2E-3"
prompt = "no importa el prompt"
`)

	res := ws.Run(t, RunOpts{Args: []string{"-e", runJobChunk(ws, repo, rolePath, jobPath, "")}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if out := readOutcome(t, ws); out["ok"] != "true" {
		t.Fatalf("el job debía completar ok=true: %+v (stderr=%q)", out, res.Stderr)
	}

	reqs := fp.Requests()
	if len(reqs) < 1 {
		t.Fatalf("no hubo request al provider (RequestCount=%d)", fp.RequestCount())
	}
	// El cuerpo entero del primer request (system incluido, sea string o bloques) no
	// debe portar el marcador del enu.md del repo.
	body, err := json.Marshal(reqs[0])
	if err != nil {
		t.Fatalf("no pude serializar el request: %v", err)
	}
	if strings.Contains(string(body), marcador) {
		t.Fatalf("el enu.md del repo VIAJÓ al request de un Role sin skills pineadas:\n%s", body)
	}
}

// --- Escenario 4 -----------------------------------------------------------------

// TestMeshE2EClaimRaceAcrossRealProcesses: la carrera que el in-process no puede
// reproducir — DOS procesos `enu` del SO compitiendo de verdad por la misma claim-ref
// en el mismo remoto bare. Exactamente uno gana; la ref queda una sola vez, sin
// corrupción por la escritura concurrente, con la identidad del ganador a bordo.
func TestMeshE2EClaimRaceAcrossRealProcesses(t *testing.T) {
	skipMeshIfNoGit(t)
	repo, bare, _ := meshGitFixture(t)

	// Dos workspaces independientes (HOME/XDG propios), ambos con mesh y ambos
	// apuntando al MISMO repo por cwd — sus `git push` van al mismo bare.
	mkWS := func() *Workspace {
		w := NewWorkspace(t)
		w.WriteEnuToml(t, "providers", "sessions", "agent", "mesh")
		return w
	}
	wsA, wsB := mkWS(), mkWS()

	chunk := func(w *Workspace) string {
		out := filepath.ToSlash(filepath.Join(w.Workdir, "outcome.txt"))
		return `
enu.task.spawn(function()
  local mesh = require("mesh")
  local won = mesh.claim("J-RACE", { cwd = "` + filepath.ToSlash(repo) + `" })
  enu.fs.write("` + out + `", "gane=" .. tostring(won))
end)`
	}

	// Lanzamiento en paralelo: dos goroutines, cada una con su exec.Command real.
	var wg sync.WaitGroup
	resA, resB := Result{}, Result{}
	wg.Add(2)
	go func() { defer wg.Done(); resA = runMeshDetached(wsA, []string{"-e", chunk(wsA)}) }()
	go func() { defer wg.Done(); resB = runMeshDetached(wsB, []string{"-e", chunk(wsB)}) }()
	wg.Wait()

	if resA.ExitCode != 0 || resB.ExitCode != 0 {
		t.Fatalf("ambos procesos debían salir 0 (mesh.claim no lanza): A=%d B=%d\nstderrA=%q\nstderrB=%q",
			resA.ExitCode, resB.ExitCode, resA.Stderr, resB.Stderr)
	}

	outA := readOutcome(t, wsA)["gane"]
	outB := readOutcome(t, wsB)["gane"]
	ganaA := outA == "true" && outB == "false"
	ganaB := outA == "false" && outB == "true"
	if !ganaA && !ganaB {
		t.Fatalf("exactamente uno debía ganar el claim; got A=%q B=%q", outA, outB)
	}

	// La ref existe UNA sola vez en el bare, sin duplicados ni corrupción.
	refs := meshGit(t, bare, "show-ref")
	n := strings.Count(refs, "refs/enu/mesh/claims/J-RACE")
	if n != 1 {
		t.Fatalf("la claim-ref debía figurar exactamente una vez en el bare; got %d\n%s", n, refs)
	}

	// El commit-baliza trae { hostname, ts } coherente (identidad del ganador).
	sha := strings.Fields(meshGit(t, bare, "show-ref", "refs/enu/mesh/claims/J-RACE"))[0]
	msg := meshGit(t, bare, "cat-file", "-p", sha)
	idx := strings.Index(msg, "\n\n")
	if idx < 0 {
		t.Fatalf("el commit del claim no tiene cuerpo con la identidad:\n%s", msg)
	}
	var info struct {
		Hostname string  `json:"hostname"`
		TS       float64 `json:"ts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(msg[idx+2:])), &info); err != nil {
		t.Fatalf("la identidad del claim no es JSON: %v\n%s", err, msg[idx+2:])
	}
	if info.Hostname == "" || info.TS <= 0 {
		t.Fatalf("identidad del claim incoherente: %+v", info)
	}
}

// --- Escenario 5 (opcional, solape declarado con TestMeshRunJob in-process) -------

// TestMeshE2EDenialSurvivesRealGitRoundtrip solapa con TestMeshRunJob
// (internal/runtime/mesh_test.go), que ya verifica la denegación como dato en el
// result.json in-process. Se justifica SOLO por el cruce que aquel no cubre: el
// binario compilado hace `enu.proc.run(["git", ...])` como subproceso REAL y el turno
// headless con una tool denegada no cuelga el proceso — la denegación sobrevive el
// roundtrip binario→git-real→remoto. Un Role con allow=["read"] deniega la tool
// `touch`; el turno completa igual (2 requests) y la rama trae la denegación a bordo.
func TestMeshE2EDenialSurvivesRealGitRoundtrip(t *testing.T) {
	skipMeshIfNoGit(t)
	fp := NewFakeProvider(t)
	// El fake pide `touch` en el 1er turno y cierra con texto en el 2º.
	fp.PushToolUse("call-1", "touch", map[string]any{"path": "x.txt"})
	fp.PushText("listo")

	ws := bootMeshWorkspace(t, fp)
	repo, bare, sha := meshGitFixture(t)

	rolePath := ws.WriteFile(t, "role.toml", `
model = "anthropic/opus"
[permissions]
mode = "ask"
allow = ["read"]
[budget]
max_turns = 5
`)
	jobPath := ws.WriteFile(t, "job.toml", `
id = "J-E2E-5"
base = "`+sha+`"
branch = "mesh/J-E2E-5"
prompt = "toca el fichero"
`)

	// Registra la tool `touch` en el chunk (el Role NO la permite → denegada).
	extra := `
    agent.tool{
      name = "touch", description = "muta", schema = { type = "object" },
      handler = function(args, ctx) return "hecho" end,
    }`

	res := ws.Run(t, RunOpts{Args: []string{"-e", runJobChunk(ws, repo, rolePath, jobPath, extra)}})
	if res.ExitCode != 0 {
		t.Fatalf("exit: got %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	out := readOutcome(t, ws)
	if out["ok"] != "true" || out["denials"] != "1" {
		t.Fatalf("el job debía completar con 1 denegación como dato: %+v (stderr=%q)", out, res.Stderr)
	}

	// La rama en el bare trae la denegación con source=headless y un suggested.
	resultJSON := meshGit(t, bare, "show", "refs/heads/mesh/J-E2E-5:.enu/mesh/result.json")
	var parsed struct {
		Denials []struct {
			Source    string `json:"source"`
			Suggested string `json:"suggested"`
		} `json:"denials"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &parsed); err != nil {
		t.Fatalf("result.json no es JSON: %v\n%s", err, resultJSON)
	}
	if len(parsed.Denials) != 1 || parsed.Denials[0].Source != "headless" ||
		!strings.Contains(parsed.Denials[0].Suggested, "touch") {
		t.Fatalf("result.json sin la denegación como dato: %+v", parsed.Denials)
	}

	// El turno completó pese a la denegación: dos requests vistos por el fake.
	if fp.RequestCount() != 2 {
		t.Fatalf("RequestCount: got %d, want 2 (el turno no completó tras la denegación)", fp.RequestCount())
	}
}
