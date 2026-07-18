package runtime

// Benchmarks del veto 2 de M15 (migracion-vm.md §5): el camino caliente del
// producto debía quedar DENTRO DE 2× del backend gopher. Tras la retirada de
// gopher-lua (M17) la baseline gopher YA NO ES EJECUTABLE —el binario lleva una
// sola VM—; los números del contraste wasm/gopher quedaron registrados en la
// bitácora de M15. Estos benchmarks siguen corriendo, ahora sobre wasm, como
// termómetro del camino caliente:
//
//	go test -bench BenchmarkVeto -benchtime=... ./internal/runtime/
//
// BenchmarkVetoAgentTurn cubre "un turno de agente headless contra el adaptador
// stub": dos vueltas del turno (petición → tool_call → tool_result → texto final),
// todo en Lua (el stub no toca la red), así que mide el coste VM+scheduler+eventos
// del turno real. BenchmarkVetoMarkdownRender cubre la pata cara del streaming
// (SSE → **markdown** → blit): renderizar el markdown que cada delta repinta.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bootAgentB replica bootAgent para *testing.B: runtime sandboxeado con el provider
// stub y la extensión agent cargada, cerrado al terminar. `budget` fija el
// presupuesto de slice del watchdog: 0 lo desactiva (para bucles de CPU sintéticos
// que no ceden y, si no, el watchdog abortaría).
func bootAgentB(b *testing.B, providersToml string, budget time.Duration) *Runtime {
	b.Helper()
	cfg := b.TempDir()
	dataDir := b.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "enu.toml"),
		[]byte("[plugins]\nenabled = [\"providers\", \"sessions\", \"agent\"]\n"), 0o644); err != nil {
		b.Fatalf("write enu.toml: %v", err)
	}
	if providersToml != "" {
		if err := os.WriteFile(filepath.Join(cfg, "providers.toml"), []byte(providersToml), 0o644); err != nil {
			b.Fatalf("write providers.toml: %v", err)
		}
	}
	rt := New(WithDataDir(dataDir), WithConfigDir(cfg), WithForceUI(true), WithSliceBudget(budget))
	b.Cleanup(rt.Close)
	if err := rt.Boot(); err != nil {
		b.Fatalf("Boot falló: %v", err)
	}
	return rt
}

// evalB corre un snippet exigiendo que termine limpio (drena el scheduler a
// quiescencia, como la vía del arnés).
func evalB(b *testing.B, rt *Runtime, code string) {
	b.Helper()
	if _, err := rt.EvalString(code); err != nil {
		b.Fatalf("snippet falló: %v\n%s", err, code)
	}
}

// BenchmarkVetoAgentTurn mide un TURNO DE AGENTE HEADLESS completo contra el
// adaptador stub: petición → el modelo pide una tool → se ejecuta → re-petición →
// texto final. Es el camino caliente del producto sin red (el stub sirve los
// eventos desde Lua), así que el ns/op es el coste VM+scheduler+eventos del turno.
// Veto 2: dentro de 2× del backend gopher.
func BenchmarkVetoAgentTurn(b *testing.B) {
	rt := bootAgentB(b, providersTomlToolStub, 2*time.Second)
	// Registro ÚNICO del adaptador stub y de la tool: cada iteración sólo corre el
	// turno, no el andamiaje.
	evalB(b, rt, `
		local agent = require("agent")
		`+registerToolStub+`
		TOOLNAME = "probe"
		TOOLARGS = { value = "x" }
		agent.tool{
			name = "probe",
			description = "tool de prueba",
			schema = { type = "object" },
			permissions = { default = "allow" },
			handler = function(args, ctx) return "resultado-de-probe" end,
		}
	`)
	// El bucle de b.N vive DENTRO de Lua: se compila UNA vez y un solo RunTasks lo
	// drena. Así el ns/op es el coste del turno, no el de recompilar el chunk ni
	// re-cruzar la frontera por iteración (que en wasm dominaría y falsearía el ratio).
	code := fmt.Sprintf(`
		enu.task.spawn(function()
			local agent = require("agent")
			for i = 1, %d do
				local s = agent.session{ model = "test/m", no_store = true }
				s:send("haz la cosa")
				s:close()
			end
		end)
	`, b.N)
	b.ResetTimer()
	evalB(b, rt, code)
	b.StopTimer()
}

// BenchmarkVetoMarkdownRender mide la pata cara del streaming: renderizar a un
// Block el markdown que cada delta de texto repinta (SSE → markdown → blit, la
// simulación de modelo-ejecucion.md). Es trabajo de una primitiva Go
// (enu.text.markdown) orquestada desde Lua; el ns/op contrasta el coste VM+frontera
// de un repintado. Veto 2: dentro de 2× del backend gopher.
func BenchmarkVetoMarkdownRender(b *testing.B) {
	rt := bootAgentB(b, "", 0)
	rt.SetStringGlobal("__md_src", sampleMarkdown)
	evalB(b, rt, `MD = __md_src`)
	code := fmt.Sprintf(`
		enu.task.spawn(function()
			for i = 1, %d do
				local blk = enu.text.markdown(MD, { width = 72 })
				RENDERED_H = blk.height
			end
		end)
	`, b.N)
	b.ResetTimer()
	evalB(b, rt, code)
	b.StopTimer()
}

// sampleMarkdown: un fragmento representativo de una respuesta de agente (encabezado,
// prosa, lista, bloque de código) — lo que un turno real repinta en streaming.
const sampleMarkdown = "## Resultado\n\n" +
	"He revisado el código y encontré **tres** problemas:\n\n" +
	"1. Una condición de carrera en el `scheduler`.\n" +
	"2. Un handle que no se libera (`Region:destroy`).\n" +
	"3. Un `pcall` que se traga el error estructurado.\n\n" +
	"```go\nfunc fix() error {\n    return nil // TODO\n}\n```\n\n" +
	"El primero es el más grave: dos goroutines escriben el mismo campo.\n"
