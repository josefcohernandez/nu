package runtime

// Tests de P24: inyección de skills (índice en el system prompt + tool `skill`
// bajo demanda), inyección de `nu.md` del repo, y la puerta TOFU (§11.2) que
// gobierna el contenido del REPO (el del usuario es siempre de confianza).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill escribe un SKILL.md (frontmatter YAML + cuerpo) en <dir>/<name>/.
func writeSkill(t *testing.T, dir, name, desc, body string) {
	t.Helper()
	sd := filepath.Join(dir, name)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// bootAgentP24 arranca el agente con cfg/repo CONOCIDOS, una skill de usuario en
// cfg/skills y una skill de repo + nu.md en un dir de repo aparte. Devuelve el
// harness y la ruta del repo (para pasarla como cwd y como clave de trust).
func bootAgentP24(t *testing.T) (*harness, string) {
	t.Helper()
	cfg := t.TempDir()
	dataDir := t.TempDir()
	repo := t.TempDir()

	writeNuToml(t, cfg, "[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n")
	if err := os.WriteFile(filepath.Join(cfg, "providers.toml"),
		[]byte(providersTomlToolStub), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	// skill del USUARIO (siempre de confianza).
	writeSkill(t, filepath.Join(cfg, "skills"), "userskill", "skill del usuario", "CUERPO-USER")
	// skill del REPO + nu.md (tras TOFU).
	writeSkill(t, filepath.Join(repo, ".nu", "skills"), "repoanalyzer", "analiza el repo", "CUERPO-REPO")
	if err := os.WriteFile(filepath.Join(repo, "nu.md"), []byte("Regla del proyecto: usar pnpm."), 0o644); err != nil {
		t.Fatalf("write nu.md: %v", err)
	}

	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(false))
	t.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	return &harness{t: t, rt: rt}, repo
}

// TestSkillsIndexTOFU (P24/§6/§11.2): el índice de skills incluye SIEMPRE las del
// usuario; las del repo y el nu.md SOLO tras confiar en el repo (TOFU).
func TestSkillsIndexTOFU(t *testing.T) {
	h, repo := bootAgentP24(t)
	h.eval(`
		out, errc = nil, nil
		REPO = ` + quote(repo) + `
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				local s = agent.session{ model = "test/m", cwd = REPO, no_store = true }
				-- sin TOFU: solo skill de usuario, sin repo ni nu.md.
				SYS1 = s:_assemble_system()
				LIST1 = #agent.skills.list(REPO)
				-- confiamos en el repo (lo que haría el TOFU de la UI).
				agent.trust.set(REPO, true)
				SYS2 = s:_assemble_system()
				LIST2 = #agent.skills.list(REPO)
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")

	// sin confianza: userskill sí, repoanalyzer/nu.md no.
	sys1 := h.eval(`return SYS1`)[0]
	if !strings.Contains(sys1, "userskill") {
		t.Errorf("SYS1 debía incluir la skill de usuario:\n%s", sys1)
	}
	if strings.Contains(sys1, "repoanalyzer") || strings.Contains(sys1, "pnpm") {
		t.Errorf("SYS1 NO debía incluir contenido del repo sin TOFU:\n%s", sys1)
	}
	h.expectEval(`return tostring(LIST1)`, "1") // solo la de usuario

	// con confianza: aparecen las del repo y el nu.md.
	sys2 := h.eval(`return SYS2`)[0]
	for _, want := range []string{"userskill", "repoanalyzer", "pnpm"} {
		if !strings.Contains(sys2, want) {
			t.Errorf("SYS2 debía incluir %q tras el TOFU:\n%s", want, sys2)
		}
	}
	h.expectEval(`return tostring(LIST2)`, "2") // usuario + repo
}

// TestSkillToolLoad (P24/§6 fase 2): la tool interna `skill` carga el cuerpo
// completo bajo demanda. Se conduce un turno (toolstub) que invoca skill{name}.
func TestSkillToolLoad(t *testing.T) {
	h, repo := bootAgentP24(t)
	h.eval(`
		out, errc = nil, nil
		REPO = ` + quote(repo) + `
		nu.task.spawn(function()
			local ok, e = pcall(function()
				local agent = require("agent")
				` + registerToolStub + `
				TOOLNAME = "skill"
				TOOLARGS = { name = "repoanalyzer" }
				agent.trust.set(REPO, true)   -- confiar para poder cargar la skill del repo
				local s = agent.session{ model = "test/m", cwd = REPO, no_store = true }
				-- la tool skill debe estar ofrecida (la sesión tiene skills).
				local has_skill_tool = false
				for _, td in ipairs(s.tools_for_request or {}) do
					if td.name == "skill" then has_skill_tool = true end
				end
				HAS_SKILL_TOOL = has_skill_tool
				s:send("usa la skill")
				-- el tool_result con el cuerpo de la skill está en el historial.
				for _, m in ipairs(s.history) do
					if m.role == "user" then
						for _, b in ipairs(m.content) do
							if b.type == "tool_result" then RESULT = b.content[1].text end
						end
					end
				end
				s:close()
			end)
			if not ok then errc = (type(e) == "table" and e.code) or tostring(e) end
			out = "done"
		end)`)
	h.expectEval(`return tostring(out)`, "done")
	h.expectEval(`return tostring(errc)`, "nil")
	h.expectEval(`return tostring(HAS_SKILL_TOOL)`, "true")
	result := h.eval(`return RESULT`)[0]
	if !strings.Contains(result, "CUERPO-REPO") {
		t.Errorf("la tool skill debía devolver el cuerpo de la skill, got:\n%s", result)
	}
}

// quote devuelve un literal Lua de la cadena (entre corchetes largos para no
// pelearse con barras de rutas).
func quote(s string) string {
	return "[==[" + s + "]==]"
}
