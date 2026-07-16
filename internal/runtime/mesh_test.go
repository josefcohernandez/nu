package runtime

// Tests de la extensión `mesh` (contrato: docs/contracts/malla.md). Lógica clave 🔒:
//   - §2 specs: validación accionable de Role/Job y `to_session_opts` PURA;
//   - §3 claim por CAS de refs contra un remoto REAL (bare local): dos nodos,
//     uno gana; heartbeat con lease; release; claim_info con identidad;
//   - §4 worktrees sobre sha pineado;
//   - §5 run_job de punta a punta: worktree + sesión (adaptador de prueba) +
//     rama-resultado empujada con .enu/mesh/{transcript.jsonl,result.json} a
//     bordo (G38) y las DENEGACIONES como dato en el result.json (G40);
//   - §6 tournament: resultados alineados con variants (G27), allSettled,
//     verificador determinista.
//
// git es dependencia declarada de la extensión (malla.md §3): los tests se
// saltan si no hay git en el PATH (mismo criterio que la extensión: git no es
// del core).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bootMesh arranca providers+sessions+agent+mesh, headless.
func bootMesh(t *testing.T, providersToml string) (*harness, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está en el PATH; la extensión mesh lo declara como dependencia (malla.md §3)")
	}
	cfg := t.TempDir()
	dataDir := t.TempDir()
	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\", \"mesh\"]\n")
	if providersToml != "" {
		if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersToml), 0o644); err != nil {
			t.Fatalf("write providers.toml: %v", err)
		}
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot falló: %v", err)
	}
	return &harness{t: t, rt: rt}, dataDir
}

// gitCmd corre git y falla el test si sale mal.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v en %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitFixture prepara un repo local con un commit y un remoto bare `origin`.
// Devuelve (repo, bare, sha del commit base).
func gitFixture(t *testing.T) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	bare := t.TempDir()
	gitCmd(t, repo, "init", "-q")
	gitCmd(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "base")
	gitCmd(t, bare, "init", "-q", "--bare")
	gitCmd(t, repo, "remote", "add", "origin", bare)
	sha := gitCmd(t, repo, "rev-parse", "HEAD")
	return repo, bare, sha
}

// TestMeshCargaYSpecs: la extensión carga; role/job se validan con errores
// accionables; to_session_opts es pura y completa.
func TestMeshCargaYSpecs(t *testing.T) {
	h, _ := bootMesh(t, providersTomlCost)
	if src := listSource(h, "mesh"); src != "builtin" {
		t.Fatalf(`mesh debía cargarse con source="builtin"; got %q`, src)
	}
	dir := t.TempDir()
	role := `
model = "test/m1"
thinking = "adaptive"
[permissions]
mode = "ask"
allow = ["read"]
deny = ["bash:rm *"]
[budget]
max_turns = 7
max_cost_usd = 2.0
[[skills]]
name = "review"
hash = "abc123"
`
	job := `
id = "J-1"
base = "cafecafe"
branch = "mesh/J-1"
prompt = "haz algo"
`
	if err := os.WriteFile(filepath.Join(dir, "role.toml"), []byte(role), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job.toml"), []byte(job), 0o644); err != nil {
		t.Fatal(err)
	}
	h.eval(inTask(`
		local mesh = require("mesh")
		local role = mesh.role.load("` + filepath.ToSlash(dir) + `/role.toml")
		local job  = mesh.job.load("` + filepath.ToSlash(dir) + `/job.toml")
		local o = mesh.to_session_opts(role, job)
		assert(o.model == "test/m1", "model")
		assert(o.max_turns == 7, "max_turns del budget")
		assert(o.skills[1] == "review", "skills por nombre")
		assert(o.permissions.deny[1] == "bash:rm *", "deny")
		assert(o.thinking == "adaptive", "thinking")
		assert(o.cwd == nil, "to_session_opts es pura: el cwd lo pone el runner")
		out = "ok"`))
	h.expectEval(`return tostring(out)`, "ok")

	// Job sin prompt (y sin fork): error accionable EMESH (capturado en el
	// snippet: inTask ya envuelve en pcall y se tragaría el lanzamiento).
	h.eval(inTask(`
		local mesh = require("mesh")
		enu.fs.write("` + filepath.ToSlash(dir) + `/mal.toml", "id='x'\nbase='y'\nbranch='z'\n")
		local ok2, e = pcall(mesh.job.load, "` + filepath.ToSlash(dir) + `/mal.toml")
		out2 = {
			lanzo = not ok2,
			code = (type(e) == "table" and e.code) or "?",
			msg  = (type(e) == "table" and e.message) or tostring(e),
		}`))
	h.expectEval(`return tostring(out2.lanzo)`, "true")
	h.expectEval(`return out2.code`, "EMESH")
	if msg := h.eval(`return out2.msg`)[0]; !strings.Contains(msg, "prompt") {
		t.Fatalf("el error debe nombrar `prompt`; got %q", msg)
	}
}

// TestMeshClaimCAS (§3): dos claims del mismo job — el primero gana (true), el
// segundo pierde la carrera (false, sin lanzar). heartbeat late con lease;
// claim_info trae la identidad; release lo borra y el heartbeat pasa a false.
func TestMeshClaimCAS(t *testing.T) {
	h, _ := bootMesh(t, providersTomlCost)
	repo, _, _ := gitFixture(t)
	h.eval(inTask(`
		local mesh = require("mesh")
		local o = { cwd = "` + filepath.ToSlash(repo) + `" }
		out = {
			primero  = mesh.claim("J-9", o),
			segundo  = mesh.claim("J-9", o),
			latido   = mesh.heartbeat("J-9", o),
			info     = mesh.claim_info("J-9", o),
			suelto   = mesh.release("J-9", o),
			postmortem = mesh.heartbeat("J-9", o),
		}`))
	h.expectEval(`return tostring(out.primero)`, "true")
	h.expectEval(`return tostring(out.segundo)`, "false")
	h.expectEval(`return tostring(out.latido)`, "true")
	h.expectEval(`return tostring(type(out.info.hostname) == "string" and out.info.ts > 0)`, "true")
	h.expectEval(`return tostring(out.suelto)`, "true")
	h.expectEval(`return tostring(out.postmortem)`, "false")
}

// TestMeshRunJob (§5): job completo de punta a punta con el adaptador toolstub
// pidiendo una tool QUE EL ROLE NO PERMITE: el turno completa, la rama-resultado
// llega al remoto con transcript.jsonl y result.json a bordo, y el result.json
// trae la denegación COMO DATO (source=headless + suggested) — el bucle de
// escalado de malla.md §7, verificable desde el lado del controlador.
func TestMeshRunJob(t *testing.T) {
	h, _ := bootMesh(t, providersTomlToolStub)
	repo, bare, sha := gitFixture(t)
	dir := t.TempDir()
	role := `
model = "test/m1"
[permissions]
mode = "ask"
allow = ["read"]
[budget]
max_turns = 5
`
	job := `
id = "J-42"
base = "` + sha + `"
branch = "mesh/J-42"
prompt = "toca el fichero"
`
	if err := os.WriteFile(filepath.Join(dir, "role.toml"), []byte(role), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job.toml"), []byte(job), 0o644); err != nil {
		t.Fatal(err)
	}
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local mesh = require("mesh")
				TOOLNAME, TOOLARGS = "touch", { path = "x.txt" }
				` + registerToolStub + `
				agent.tool{
					name = "touch", description = "muta", schema = { type = "object" },
					handler = function(args, ctx) return "hecho" end,
				}
				local role = mesh.role.load("` + filepath.ToSlash(dir) + `/role.toml")
				local job  = mesh.job.load("` + filepath.ToSlash(dir) + `/job.toml")
				local r = mesh.run_job(job, role, { cwd = "` + filepath.ToSlash(repo) + `" })
				out = {
					ok = r.ok, branch = r.branch,
					ndenials = #r.denials,
					source = r.denials[1] and r.denials[1].source or "nil",
					err = r.error and r.error.message or "nil",
					turns = r.usage and r.usage.turns or -1,
				}
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
		end)
	`)
	if e := h.eval(`return tostring(errc)`)[0]; e != "nil" {
		t.Fatalf("run_job lanzó (debe ser allSettled): %s", e)
	}
	if e := h.eval(`return tostring(out.err)`)[0]; e != "nil" {
		t.Fatalf("run_job falló: %s", e)
	}
	h.expectEval(`return tostring(out.ok)`, "true")
	h.expectEval(`return out.branch`, "mesh/J-42")
	h.expectEval(`return tostring(out.ndenials)`, "1")
	h.expectEval(`return out.source`, "headless")

	// El lado del CONTROLADOR: la rama está en el bare con la auditoría a bordo.
	resultJSON := gitCmd(t, bare, "show", "refs/heads/mesh/J-42:.enu/mesh/result.json")
	var res struct {
		JobID   string `json:"job_id"`
		Denials []struct {
			Source    string `json:"source"`
			Suggested string `json:"suggested"`
		} `json:"denials"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &res); err != nil {
		t.Fatalf("result.json no es JSON: %v\n%s", err, resultJSON)
	}
	if res.JobID != "J-42" || len(res.Denials) != 1 || res.Denials[0].Source != "headless" ||
		res.Denials[0].Suggested != "touch:x.txt" {
		t.Fatalf("result.json sin la denegación como dato: %+v", res)
	}
	transcript := gitCmd(t, bare, "show", "refs/heads/mesh/J-42:.enu/mesh/transcript.jsonl")
	if !strings.Contains(transcript, `"t":"meta"`) && !strings.Contains(transcript, `"t": "meta"`) {
		t.Fatalf("el transcript de la rama no parece un JSONL de sesión:\n%.200s", transcript)
	}
}

// TestMeshSkillHashVeto (§9): un Role con skill pineada cuyo hash NO coincide
// con el worktree muere ANTES de abrir sesión, con error accionable.
func TestMeshSkillHashVeto(t *testing.T) {
	h, _ := bootMesh(t, providersTomlCost)
	repo, _, _ := gitFixture(t)
	// El repo trae una skill cuyo contenido no casa con el pin del Role.
	skillDir := filepath.Join(repo, ".enu", "skills", "review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\n---\nx"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "-A")
	gitCmd(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "skill")
	sha := gitCmd(t, repo, "rev-parse", "HEAD")

	dir := t.TempDir()
	role := "model = \"test/m1\"\n[[skills]]\nname = \"review\"\nhash = \"0000000000000000000000000000000000000000\"\n"
	job := "id = \"J-7\"\nbase = \"" + sha + "\"\nbranch = \"mesh/J-7\"\nprompt = \"da igual\"\n"
	if err := os.WriteFile(filepath.Join(dir, "role.toml"), []byte(role), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job.toml"), []byte(job), 0o644); err != nil {
		t.Fatal(err)
	}
	h.eval(inTask(`
		local mesh = require("mesh")
		local role = mesh.role.load("` + filepath.ToSlash(dir) + `/role.toml")
		local job  = mesh.job.load("` + filepath.ToSlash(dir) + `/job.toml")
		local r = mesh.run_job(job, role, { cwd = "` + filepath.ToSlash(repo) + `" })
		out = { ok = r.ok, code = r.error and r.error.code or "nil", msg = r.error and r.error.message or "" }`))
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "EMESH")
	if msg := h.eval(`return out.msg`)[0]; !strings.Contains(msg, "review") || !strings.Contains(msg, "pin") {
		t.Fatalf("el veto por hash debe nombrar la skill y el pin; got %q", msg)
	}
}

// TestMeshTournament (§6): dos variantes con nudges distintos, resultados
// ALINEADOS con variants (G27), cada una en su cwd (G39), y el verificador
// determinista filtrando.
func TestMeshTournament(t *testing.T) {
	h, _ := bootMesh(t, providersTomlCost)
	wt1, wt2 := t.TempDir(), t.TempDir()
	h.eval(`
		out, errc = nil, nil
		enu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				local mesh = require("mesh")
				` + registerCtl + `
				local root = agent.session{ model = "test/m1" }
				root:send("plan común")
				local res = mesh.tournament{
					session = root,
					variants = {
						{ nudge = "variante A", cwd = "` + filepath.ToSlash(wt1) + `" },
						{ nudge = "variante B", cwd = "` + filepath.ToSlash(wt2) + `" },
					},
					verify = function(dir, o) return dir == "` + filepath.ToSlash(wt2) + `" end,
					limit = 1,
				}
				out = {
					n = #res,
					ok1 = res[1].ok, ok2 = res[2].ok,
					dir1 = res[1].dir, dir2 = res[2].dir,
					v1 = tostring(res[1].verified), v2 = tostring(res[2].verified),
					distintos = res[1].session_id ~= res[2].session_id,
				}
				root:close()
			end)
			if not ok then errc = (type(e) == "table" and e.message) or tostring(e) end
		end)
	`)
	if e := h.eval(`return tostring(errc)`)[0]; e != "nil" {
		t.Fatalf("el torneo falló: %s", e)
	}
	h.expectEval(`return tostring(out.n)`, "2")
	h.expectEval(`return tostring(out.ok1)`, "true")
	h.expectEval(`return tostring(out.ok2)`, "true")
	h.expectEval(`return out.dir1`, filepath.ToSlash(wt1)) // alineado con variants (G27)
	h.expectEval(`return out.dir2`, filepath.ToSlash(wt2))
	h.expectEval(`return out.v1`, "false")
	h.expectEval(`return out.v2`, "true")
	h.expectEval(`return tostring(out.distintos)`, "true")
}
