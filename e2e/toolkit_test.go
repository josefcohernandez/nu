package e2e

// Tests e2e del plugin oficial `toolkit` contra el BINARIO real, bajo un PTY: el
// widget MÍNIMO (un `vbox` con un `label`) se pinta de verdad en un terminal y las
// teclas de salida cierran limpio el proceso. Cubren lo que un test in-process del
// toolkit (rejilla del compositor en memoria) no puede ver: el flujo de bytes ANSI
// sobre un TTY real, el exit code del binario, una señal SIGWINCH real, y la red de
// salida de emergencia (ADR-017/G35) cuando el `init.lua` de usuario falla antes de
// montar la app.
//
// Prefijo TestToolkit* para filtrarlos con `-run TestToolkit`.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// toolkitLabelInitLua es el `init.lua` de usuario común a S1-S3: monta un `vbox` con
// un único `label` de texto conocido y lo cuelga de `tk.app`. SIN widgets focusables
// (el label no lo es): la app nunca consume `q`/`esc`/`ctrl+c`, así que esas teclas
// caen siempre a la red de salida de emergencia del kernel (installKernelExitWasm,
// driver.go) sin que el init.lua tenga que cablear su propio atajo de salida — el
// escenario queda determinista.
const toolkitLabelInitLua = `
local tk = require("toolkit")
local root = tk.vbox({})
root:add(tk.label({ text = "HOLA E2E TOOLKIT" }))
tk.app({ root = root })
`

// newToolkitWorkspace monta un workspace con `enu.toml` (`plugins.enabled =
// ["toolkit"]`) y el `init.lua` de usuario dado en `ConfigDir/init.lua`. Ningún
// providers/sessions/agent: aísla el toolkit puro, como pide el escenario. Helper
// PRIVADO de este fichero (el arnés no ofrece un atajo para plugin único + init.lua).
func newToolkitWorkspace(t *testing.T, initLua string) *Workspace {
	t.Helper()
	ws := NewWorkspace(t)
	ws.WriteEnuToml(t, "toolkit")
	ws.WriteConfig(t, "init.lua", initLua)
	return ws
}

// TestToolkitE2ELabelPintaYQSaleLimpio — [S1, MÍNIMO IMPRESCINDIBLE]. El widget se
// pinta de verdad bajo un TTY real y `q` cierra limpio: el único par de observables
// (ANSI crudo del PTY + exit code del proceso) que un test in-process no puede ver.
func TestToolkitE2ELabelPintaYQSaleLimpio(t *testing.T) {
	ws := newToolkitWorkspace(t, toolkitLabelInitLua)
	p := ws.Start(t, RunOpts{})

	p.Expect(t, "HOLA E2E TOOLKIT", 5*time.Second)
	p.Send(t, "q")
	if code := p.Wait(t, 5*time.Second); code != 0 {
		t.Fatalf("q debía cerrar limpio (exit 0); got %d\n--- salida ---\n%s", code, p.Output())
	}
}

// TestToolkitE2ETeclasDeSalida — [S2]. Las otras dos teclas que la red de emergencia
// nombra explícitamente (installKernelExitWasm, driver.go: q/esc/ctrl+c →
// core:shutdown) también cierran limpio. Una instancia de PTY nueva por caso: no se
// arrastra estado entre subtests.
func TestToolkitE2ETeclasDeSalida(t *testing.T) {
	casos := []struct {
		nombre string
		tecla  string
	}{
		{"esc_cierra_limpio", "\x1b"},
		{"ctrl_c_cierra_limpio", "\x03"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ws := newToolkitWorkspace(t, toolkitLabelInitLua)
			p := ws.Start(t, RunOpts{})

			p.Expect(t, "HOLA E2E TOOLKIT", 5*time.Second)
			p.Send(t, c.tecla)
			if code := p.Wait(t, 5*time.Second); code != 0 {
				t.Fatalf("%s debía cerrar limpio (exit 0); got %d\n--- salida ---\n%s", c.nombre, code, p.Output())
			}
		})
	}
}

// TestToolkitE2EResizeMantieneElTexto — [S3]. Un SIGWINCH real (vía `p.Resize`,
// ioctl TIOCSWINSZ) es una señal de terminal que solo un PTY puede disparar: tras
// encogerlo a 40x10 el label sigue cabiendo y pintado, y el proceso no queda colgado
// (`q` lo cierra con 0).
func TestToolkitE2EResizeMantieneElTexto(t *testing.T) {
	ws := newToolkitWorkspace(t, toolkitLabelInitLua)
	p := ws.Start(t, RunOpts{})

	p.Expect(t, "HOLA E2E TOOLKIT", 5*time.Second)
	p.Resize(40, 10)
	// El label ("HOLA E2E TOOLKIT", 16 columnas) cabe de sobra en 40: tras el
	// repintado disparado por el resize, el texto sigue en la pantalla.
	p.Expect(t, "HOLA E2E TOOLKIT", 5*time.Second)
	p.Send(t, "q")
	if code := p.Wait(t, 5*time.Second); code != 0 {
		t.Fatalf("tras el resize, q debía cerrar limpio (exit 0); got %d\n--- salida ---\n%s", code, p.Output())
	}
}

// TestToolkitE2EInitRotoRedDeEmergenciaCierra — [S4, camino feo G35]. Un `init.lua`
// de usuario que LANZA antes de montar la app (nunca llega a `tk.app`, así que ningún
// `on_input` de producto queda apilado) no tumba el arranque (ADR-008): `Boot` sigue
// adelante, la red de emergencia (instalada ANTES de Boot, driver.go
// InstallEmergencyExit) sigue viva, y `q` cierra con 0. El fallo del init.lua queda
// registrado en `enu.log`, no perdido en silencio.
func TestToolkitE2EInitRotoRedDeEmergenciaCierra(t *testing.T) {
	ws := newToolkitWorkspace(t, `
local tk = require("toolkit")
error("boom-e2e-init")
`)
	p := ws.Start(t, RunOpts{})

	// Nada de producto llega a montarse (la app nunca se construye): no hay texto
	// que esperar. Un margen breve deja que el arranque (init.lua roto incluido) y
	// la instalación de la red de emergencia terminen antes de mandar la tecla.
	time.Sleep(200 * time.Millisecond)
	p.Send(t, "q")
	if code := p.Wait(t, 5*time.Second); code != 0 {
		t.Fatalf("la red de emergencia debía cerrar limpio (exit 0) pese al init.lua roto; got %d\n--- salida ---\n%s", code, p.Output())
	}

	logPath := filepath.Join(ws.DataDir, "enu.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("enu.log debía existir tras el fallo del init.lua: %v", err)
	}
	if !strings.Contains(string(data), "boom-e2e-init") {
		t.Fatalf("enu.log debía registrar el fallo del init.lua; got %q", string(data))
	}
}
