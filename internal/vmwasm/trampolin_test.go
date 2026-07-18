package vmwasm

// Tests 🔒 de M03: el trampolín de desenrollado (Snapshot/Restore) endurecido a
// calidad de kernel. Estos blindan las cuatro propiedades de las que depende
// TODA la corrección del backend wasm, porque cada error de Lua y cada `pcall`
// pasan por aquí:
//   1. anidamiento profundo (el LIFO de trys aguanta);
//   2. un TRAP real del motor se propaga como fallo duro (NO se traga como throw);
//   3. la no-reentrancia de api.Function está esquivada (funciones frescas);
//   4. multi-instancia concurrente sin contaminación cruzada (ctx-routing).

import (
	"strings"
	"sync"
	"testing"
)

// M03.1: pcalls MUY anidados — el trampolín empuja y saca un frame por nivel;
// un desbalance rompería en la aserción de hostTry. 150 niveles quedan dentro
// del techo de llamadas C de Lua (LUAI_MAXCCALLS ≈ 200), que el trampolín NO
// baja de forma apreciable (hallazgo M03: el re-entry por LUAI_TRY consume
// stack, pero el límite efectivo sigue siendo el estándar de Lua).
func TestTrampolinAnidamientoProfundo(t *testing.T) {
	inst := newInstance(t)
	out, lerr, err := inst.Eval(`
		local function anida(n)
			if n == 0 then error("fondo", 0) end
			local ok, e = pcall(anida, n - 1)
			-- relanza el error subiendo: cada nivel es un try del trampolín
			if not ok then error(e, 0) end
		end
		local ok, e = pcall(anida, 150)
		return tostring(ok) .. ":" .. tostring(e)`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "false:fondo" {
		t.Fatalf("got %q", out)
	}
	// y el estado sigue sano tras 150 niveles de unwinding
	if o, _, _ := inst.Eval(`return 1+1`); o != "2" {
		t.Fatalf("estado tocado tras anidamiento: %q", o)
	}
}

// M03.1b: rebasar el techo de llamadas C DEGRADA CON GRACIA — es un error de
// Lua capturable ("C stack overflow"), NO un trap del motor, y el estado
// sobrevive. Semántica idéntica a la del Lua nativo; ningún caso de uso real de
// enu anida 200 pcalls, pero blindar la degradación evita que un plugin
// patológico tumbe el estado.
func TestTrampolinTechoLlamadasCDegrada(t *testing.T) {
	inst := newInstance(t)
	out, lerr, err := inst.Eval(`
		local function anida(n)
			if n == 0 then return end
			anida(n - 1)  -- recursión C profunda vía pcall
			pcall(function() end)
		end
		local ok, e = pcall(function()
			local function deep(n) if n > 0 then return pcall(deep, n-1) end end
			-- fuerza el techo: 500 pcalls anidados
			return deep(500)
		end)
		return tostring(ok)`)
	if err != nil || lerr != "" {
		t.Fatalf("un overflow de stack C NO debe ser trap duro: lerr=%q err=%v", lerr, err)
	}
	if out != "false" && out != "true" {
		t.Fatalf("got %q", out)
	}
	// clave: el estado sobrevive al overflow (fue capturable)
	if o, _, e := inst.Eval(`return "sigo-vivo"`); e != nil || o != "sigo-vivo" {
		t.Fatalf("el estado no sobrevivió al overflow: %q err=%v", o, e)
	}
}

// M03.2: un TRAP real del motor (nu_selftest_trap → __builtin_trap) se propaga
// como error DURO de Go, jamás se confunde con un LUAI_THROW capturable. Es la
// frontera crítica: si el trampolín tragara traps, un bug del motor quedaría
// invisible bajo un pcall.
func TestTrampolinTrapRealSePropaga(t *testing.T) {
	inst := newInstance(t)
	_, err := inst.mod.ExportedFunction("nu_selftest_trap").Call(inst.ctx)
	if err == nil {
		t.Fatal("un trap real debía devolver error de Go, no nil")
	}
	if !strings.Contains(err.Error(), "unreachable") && !strings.Contains(err.Error(), "trap") {
		t.Logf("trap propagado como: %v", err) // el mensaje exacto depende de wazero
	}
	// Y el pool sigue usable: otra instancia arranca limpia (el trap no corrompió
	// el runtime compartido, sólo esa instancia).
	other, err := inst.pool.NewInstance()
	if err != nil {
		t.Fatalf("el pool quedó inutilizable tras un trap: %v", err)
	}
	defer func() { _ = other.Close() }()
	if o, _, _ := other.Eval(`return "vivo"`); o != "vivo" {
		t.Fatalf("instancia nueva tocada: %q", o)
	}
}

// M03.3: la no-reentrancia de api.Function — el trampolín usa ExportedFunction
// FRESCO por llamada. Un anidamiento con error en cada nivel ejerce la
// reentrada de nu_call_pfunc; si se cacheara el objeto, el frame exterior se
// corrompería (síntoma: invalid table access con args basura). Que esto pase
// prueba que la mitigación sigue vigente.
func TestTrampolinNoReentrancia(t *testing.T) {
	inst := newInstance(t)
	// tres niveles de pcall, cada uno con trabajo real y un error capturado:
	// ejerce nu_call_pfunc reentrante tres veces en la misma pila.
	out, lerr, err := inst.Eval(`
		local r = {}
		local ok1 = pcall(function()
			r[#r+1] = "a"
			local ok2 = pcall(function()
				r[#r+1] = "b"
				local ok3 = pcall(function() r[#r+1] = "c"; error("z") end)
				r[#r+1] = tostring(ok3)
				error("y")
			end)
			r[#r+1] = tostring(ok2)
			error("x")
		end)
		r[#r+1] = tostring(ok1)
		return table.concat(r, ",")`)
	if err != nil || lerr != "" {
		t.Fatalf("lerr=%q err=%v", lerr, err)
	}
	if out != "a,b,c,false,false,false" {
		t.Fatalf("got %q — la no-reentrancia mordió", out)
	}
}

// M03.4: N instancias corriendo CONCURRENTEMENTE (en goroutines separadas), cada
// una con trabajo pesado en errores y yields. El ctx-routing debe mantener cada
// una en su propio inst.tries sin contaminación. Es la base de M12 (workers).
func TestTrampolinMultiInstanciaConcurrente(t *testing.T) {
	p, err := NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	const N = 8
	const iters = 50
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			inst, e := p.NewInstance()
			if e != nil {
				errs[id] = e
				return
			}
			defer func() { _ = inst.Close() }()
			marca := "inst" + string(rune('A'+id))
			if _, _, e := inst.Eval(`G = "` + marca + `"`); e != nil {
				errs[id] = e
				return
			}
			for j := 0; j < iters; j++ {
				// error capturado + verificar que G (estado propio) no se mezcla
				out, lerr, e := inst.Eval(`
					pcall(function() error("ruido") end)
					return G`)
				if e != nil || lerr != "" {
					errs[id] = errStr("iter fallo")
					return
				}
				if out != marca {
					errs[id] = errStr("contaminación: " + out + " != " + marca)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("instancia %d: %v", i, e)
		}
	}
}

// M03.5: error DESPUÉS de un yield, dentro del pcall que cruzó el yield — el
// caso que combina el puente (M06) con el trampolín. El pcall sobrevive al yield
// y captura el error posterior.
func TestTrampolinErrorTrasYield(t *testing.T) {
	inst := newInstance(t)
	ref, err := inst.CoSpawn(`
		local ok, e = pcall(function()
			nu_await("suspende")
			error("fallo tras reanudar")
		end)
		return tostring(ok) .. ":" .. tostring(e)`)
	if err != nil {
		t.Fatal(err)
	}
	if st, _, _ := inst.CoResume(ref, nil); st != CoYield {
		t.Fatal("no suspendió")
	}
	v := "reanuda"
	st, out, err := inst.CoResume(ref, &v)
	if err != nil || st != CoDone {
		t.Fatalf("st=%v err=%v", st, err)
	}
	if !strings.Contains(out, "false:") || !strings.Contains(out, "fallo tras reanudar") {
		t.Fatalf("got %q", out)
	}
}
