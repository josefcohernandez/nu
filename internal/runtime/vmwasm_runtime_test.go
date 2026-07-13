package runtime

// Test de integración de M13d: el stack REAL del backend wasm a través del
// Runtime. A diferencia de los tests de M13b/M13c (que registran un catálogo suelto
// sobre una Instance desnuda), aquí se construye un Runtime completo con
// `New(WithVMBackend(VMWasm), ...)` —que cablea el arranque wasm y el catálogo
// entero— y se ejercita por `EvalString`/`EvalTaskString`, exactamente como el
// binario `nu -e`. Cubre el catálogo síncrono (version/sys/codecs), una primitiva ⏸
// real (nu.fs por disco), red real (nu.http contra httptest) y la concurrencia del
// scheduler wasm (nu.task).
//
// El watchdog por slice (DM4) ya está cableado: un bucle de CPU en una task wasm se
// aborta con EBUDGET (ver TestRuntimeWasmWatchdogEBUDGET) en vez de colgar. Lo que
// aún impide correr la suite dual completa (`NU_VM=wasm go test ./...`) es cargar las
// 8 extensiones oficiales sobre el estado wasm (M13d-ext); hasta entonces estos tests
// son el sondeo dedicado del stack real por el Runtime.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"path/filepath"
)

// newWasmRuntime construye un Runtime headless sobre el backend wasm (M13d) con
// dirs temporales. En tests `detectTTY` es false → `rt.ui == nil` (headless, G20),
// así que `nu.ui` no se registra; el resto del catálogo (síncrono + ⏸) sí. Verifica
// de paso que el backend se resolvió a wasm y que `buildWasmState` no falló.
func newWasmRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt := New(
		WithVMBackend(VMWasm),
		WithDataDir(t.TempDir()),
		WithConfigDir(t.TempDir()),
	)
	t.Cleanup(rt.Close)
	if rt.VMBackend() != VMWasm {
		t.Fatalf("backend = %v, esperado wasm", rt.VMBackend())
	}
	if rt.wasmErr != nil {
		t.Fatalf("buildWasmState falló: %v", rt.wasmErr)
	}
	if rt.wasm == nil || rt.wasmPool == nil {
		t.Fatal("el estado wasm no se construyó (rt.wasm / rt.wasmPool nil)")
	}
	return rt
}

// evalStringOne evalúa un chunk que devuelve UN valor y lo retorna como string.
func evalStringOne(t *testing.T, rt *Runtime, code string) string {
	t.Helper()
	res, err := rt.EvalString(code)
	if err != nil {
		t.Fatalf("EvalString(%q): %v", code, err)
	}
	if len(res) != 1 {
		t.Fatalf("EvalString(%q): %d valores, esperado 1: %v", code, len(res), res)
	}
	return res[0]
}

// evalTaskOne evalúa un chunk COMO TASK que devuelve UN valor y lo retorna.
func evalTaskOne(t *testing.T, rt *Runtime, code string) string {
	t.Helper()
	res, err := rt.EvalTaskString(code)
	if err != nil {
		t.Fatalf("EvalTaskString(%q): %v", code, err)
	}
	if len(res) != 1 {
		t.Fatalf("EvalTaskString(%q): %d valores, esperado 1: %v", code, len(res), res)
	}
	return res[0]
}

// M13d.1: nu.version.api por el Runtime → "3" (el APILevel que buildWasmState
// inyecta con SetAPIVersion). Es el smoke test que el binario `nu -e` reproduce.
func TestRuntimeWasmVersionAPI(t *testing.T) {
	rt := newWasmRuntime(t)
	if got := evalStringOne(t, rt, `return nu.version.api`); got != "3" {
		t.Fatalf("nu.version.api = %q, esperado 3", got)
	}
}

// M13d.2: nu.sys.platform() por el Runtime → un string no vacío (catálogo síncrono
// real: la primitiva corre en Go y cruza la frontera).
func TestRuntimeWasmSysPlatform(t *testing.T) {
	rt := newWasmRuntime(t)
	if got := evalStringOne(t, rt, `return nu.sys.platform()`); got == "" {
		t.Fatal("nu.sys.platform() devolvió vacío")
	}
}

// M13d.3: round-trip de un códec por EvalString (nu.json.encode∘decode). Prueba que
// las primitivas síncronas del catálogo funcionan por el estado principal wasm.
func TestRuntimeWasmJSONRoundTrip(t *testing.T) {
	rt := newWasmRuntime(t)
	got := evalStringOne(t, rt, `
		local s = nu.json.encode({ nombre = "nu", n = 3, tags = { "a", "b" } })
		local back = nu.json.decode(s)
		return back.nombre .. ":" .. tostring(back.n) .. ":" .. back.tags[2]`)
	if got != "nu:3:b" {
		t.Fatalf("json round-trip: got %q, esperado nu:3:b", got)
	}
}

// M13d.4: una primitiva ⏸ REAL por EvalTaskString — escribe y lee un fichero de
// disco con nu.fs.write/nu.fs.read. Prueba de punta a punta el scheduler wasm + una
// primitiva suspendente (que cede al bucle y se cumple en una goroutine de fondo) +
// el Runtime: es la ruta que EvalString NO puede tomar (⏸ exige task).
func TestRuntimeWasmTaskFsRoundTrip(t *testing.T) {
	rt := newWasmRuntime(t)
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "hola.txt"))
	got := evalTaskOne(t, rt, `
		nu.fs.write("`+path+`", "contenido real de disco")
		return nu.fs.read("`+path+`")`)
	if got != "contenido real de disco" {
		t.Fatalf("fs round-trip: got %q", got)
	}
}

// M13d.5: red REAL por el Runtime — nu.http.request contra un httptest.NewServer.
// Prueba que una primitiva ⏸ de red (petición completa hasta el body) cruza el
// scheduler wasm y el Runtime y vuelve con status + body.
func TestRuntimeWasmTaskHTTPRequest(t *testing.T) {
	rt := newWasmRuntime(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hola-desde-el-servidor"))
	}))
	t.Cleanup(srv.Close)
	got := evalTaskOne(t, rt, `
		local resp = nu.http.request({ url = "`+srv.URL+`" })
		return tostring(resp.status) .. ":" .. resp.body`)
	if got != "200:hola-desde-el-servidor" {
		t.Fatalf("http request: got %q", got)
	}
}

// M13d.6: concurrencia por el Runtime — dos tasks con nu.task.sleep distinto se
// intercalan por el scheduler wasm (la de 5 ms termina antes que la de 20 ms). Prueba
// que EvalTaskString corre como task, que puede lanzar sub-tasks, y que nu.task.all
// las espera a todas (G27) por el bucle real.
func TestRuntimeWasmTaskConcurrencia(t *testing.T) {
	rt := newWasmRuntime(t)
	got := evalTaskOne(t, rt, `
		local traza = {}
		nu.task.all({
			function() nu.task.sleep(20); traza[#traza+1] = "a" end,
			function() nu.task.sleep(5);  traza[#traza+1] = "b" end,
		})
		return table.concat(traza, ",")`)
	if got != "b,a" {
		t.Fatalf("concurrencia: got %q, esperado b,a", got)
	}
}

// M13d.7: un error estructurado de una primitiva ⏸ cruza fiel por EvalTaskString
// (se lee la tabla del error en Lua). nu.fs.read de un fichero inexistente → ENOENT.
func TestRuntimeWasmTaskStructuredError(t *testing.T) {
	rt := newWasmRuntime(t)
	noexiste := filepath.ToSlash(filepath.Join(t.TempDir(), "noexiste"))
	_, err := rt.EvalTaskString(`return nu.fs.read("` + noexiste + `")`)
	if err == nil {
		t.Fatal("EvalTaskString: esperaba un error, got nil")
	}
	se, ok := err.(*StructuredError)
	if !ok {
		t.Fatalf("error no estructurado: %T %v", err, err)
	}
	if se.Code != CodeENOENT {
		t.Fatalf("code = %q, esperado ENOENT", se.Code)
	}
}

// M13d/DM4: el watchdog por slice corta un bucle de CPU en una task wasm por el
// Runtime REAL. Una sub-task con `while true do end` se aborta con EBUDGET tras el
// slice; la task del CLI la ESPERA y observa el EBUDGET (capturable por el awaiter,
// como en gopher), que EvalTaskString devuelve como *StructuredError. Cierra el
// hallazgo de M13a (un bucle de CPU por el Runtime wasm ya no cuelga). Se acota con
// un tope de wall-clock: un watchdog roto sería un FALLO, no un cuelgue del CI.
func TestRuntimeWasmWatchdogEBUDGET(t *testing.T) {
	rt := New(
		WithVMBackend(VMWasm),
		WithDataDir(t.TempDir()),
		WithConfigDir(t.TempDir()),
		WithSliceBudget(30*time.Millisecond),
	)
	t.Cleanup(rt.Close)
	if rt.wasmErr != nil {
		t.Fatalf("buildWasmState: %v", rt.wasmErr)
	}
	type res struct {
		out []string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := rt.EvalTaskString(`
			local w = nu.task.spawn(function() while true do end end)
			return nu.task.await(w)`)
		ch <- res{out, err}
	}()
	select {
	case r := <-ch:
		se, ok := r.err.(*StructuredError)
		if !ok {
			t.Fatalf("esperaba EBUDGET estructurado; got out=%v err=%T %v", r.out, r.err, r.err)
		}
		if se.Code != CodeEBUDGET {
			t.Fatalf("code = %q, esperado EBUDGET", se.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EvalTaskString colgó: el watchdog wasm no cortó el bucle de CPU")
	}
}
