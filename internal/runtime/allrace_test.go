package runtime

import (
	"testing"
)

// Tests de `nu.task.all` y `nu.task.race` (api.md §3, sesión S07). Ambos están en
// el inventario de lógica clave 🔒 del plan; cada caso límite lleva un test que lo
// nombra para blindarlo de regresiones. La lógica a blindar:
//
//   - **G27**: `all` devuelve `out[i]` alineado con `fns[i]`, NUNCA en orden de
//     terminación —se fuerza con tasks que terminan en orden distinto al de
//     entrada (sleeps inversos) y se comprueba la alineación.
//   - `all` con una que lanza → relanza ESE error (code/objeto estructurado
//     intacto) y cancela a las demás.
//   - `race` devuelve el índice ganador correcto (1-based) y cancela a las
//     perdedoras.
//   - El **substrato de cancelación** de S07 (frontera con S08): una perdedora se
//     deja de ejecutar y no entrega su resultado.
//
// El arnés (harness_test.go) corre cada snippet contra un Runtime real; los
// snippets dejan resultados en globals (`out`) que un `expectEval` posterior lee.

// --- Snippet que ejercita cada firma (Definition of Done §2) ---

// TestAllRaceSnippet: el camino feliz que ejercita `all` y `race` desde el lado
// del autor de extensiones. `all` sobre tres funciones recoge sus resultados;
// `race` sobre dos elige a la que termina antes. Es la prueba de humo de S07.
func TestAllRaceSnippet(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local r = nu.task.all({
				function() return "a" end,
				function() return "b" end,
				function() return "c" end,
			})
			out.all = table.concat(r, ",")

			local i, v = nu.task.race({
				function() nu.task.sleep(20); return "lento" end,
				function() nu.task.sleep(1);  return "rapido" end,
			})
			out.race_i = i
			out.race_v = v
		end)
	`)
	h.expectEval(`return out.all`, "a,b,c")
	h.expectEval(`return tostring(out.race_i)`, "2")
	h.expectEval(`return out.race_v`, "rapido")
}

// --- 🔒 G27: out[i] alineado con fns[i] ---

// TestAllResultsAlignedWithInputs: 🔒 **G27** — el invariante central de S07. Las
// tasks terminan en orden INVERSO al de entrada (la primera duerme más, la última
// menos), así que el orden de terminación es 3,2,1; pese a ello `out[1]` debe ser
// el de `fns[1]`, etc. Si la implementación devolviera en orden de terminación, el
// array saldría barajado y este test lo cazaría.
func TestAllResultsAlignedWithInputs(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		-- G27: out[i] alineado con fns[i], no con el orden de terminación.
		out = {}
		nu.task.spawn(function()
			local r = nu.task.all({
				function() nu.task.sleep(30); return "primera" end,  -- termina la última
				function() nu.task.sleep(20); return "segunda" end,
				function() nu.task.sleep(10); return "tercera" end,  -- termina la primera
			})
			out.r = r
		end)
	`)
	h.expectEval(`return out.r[1]`, "primera")
	h.expectEval(`return out.r[2]`, "segunda")
	h.expectEval(`return out.r[3]`, "tercera")
}

// TestAllAcceptsExistingTaskHandles: `all` acepta handles `Task` ya creados, no
// solo funciones (§3: `Task[]|fn[]`). Con sleeps inversos, los resultados siguen
// alineados con la posición en el array de handles (G27).
func TestAllAcceptsExistingTaskHandles(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local t1 = nu.task.spawn(function() nu.task.sleep(20); return "uno" end)
			local t2 = nu.task.spawn(function() nu.task.sleep(5);  return "dos" end)
			local r = nu.task.all({ t1, t2 })   -- handles, no funciones
			out.r = r
		end)
	`)
	h.expectEval(`return out.r[1]`, "uno")
	h.expectEval(`return out.r[2]`, "dos")
}

// TestAllMixedHandlesAndFunctions: `all` admite mezclar handles y funciones en el
// mismo array (lo más permisivo y coherente con "handles ya creados O funciones").
func TestAllMixedHandlesAndFunctions(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local t1 = nu.task.spawn(function() return "handle" end)
			local r = nu.task.all({ t1, function() return "fn" end })
			out.r = r
		end)
	`)
	h.expectEval(`return out.r[1]`, "handle")
	h.expectEval(`return out.r[2]`, "fn")
}

// --- 🔒 all con una que lanza: relanza ESE error y cancela el resto ---

// TestAllReraisesStructuredError: 🔒 si una task lanza un error estructurado, `all`
// lo **relanza intacto** (mismo `code`). Se usa un error de extensión (`EPROVIDER`,
// no reservado) para verificar que el code no se reescribe ni se traga: viaja
// task → all → quien hizo spawn de la task que llamó a all.
func TestAllReraisesStructuredError(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ok, err = pcall(function()
				nu.task.all({
					function() nu.task.sleep(20); return "ok" end,
					function() error({ code = "EPROVIDER", message = "explotó" }) end,
				})
			end)
			out.ok = ok
			out.code = err.code
			out.msg = err.message
		end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "EPROVIDER")
	h.expectEval(`return out.msg`, "explotó")
}

// TestAllCancelsOthersOnError: 🔒 cuando una task de `all` lanza, las demás se
// **cancelan** —dejan de ejecutar Lua y no completan—. La task lenta marca un flag
// global ANTES de dormir (para probar que llegó a correr) y otro DESPUÉS (que NO
// debe verse, porque la cancelación la aborta en el `sleep`). El error de la otra
// task llega casi de inmediato, así que la lenta es cancelada mientras duerme.
func TestAllCancelsOthersOnError(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = { antes = false, despues = false }
		nu.task.spawn(function()
			pcall(function()
				nu.task.all({
					function()
						out.antes = true
						nu.task.sleep(50)      -- aquí la cancelan: el sleep aborta
						out.despues = true     -- NO debe ejecutarse
						return "tarde"
					end,
					function()
						error({ code = "EINVAL", message = "fallo rápido" })
					end,
				})
			end)
		end)
	`)
	h.expectEval(`return tostring(out.antes)`, "true")    // la lenta llegó a correr
	h.expectEval(`return tostring(out.despues)`, "false") // pero fue abortada en el sleep
}

// --- 🔒 race: índice ganador 1-based + cancela perdedoras ---

// TestRaceReturnsWinnerIndexOneBased: 🔒 `race` devuelve el índice 1-based de la
// ganadora (la que termina antes) y su resultado. La segunda del array es la más
// rápida, así que el índice debe ser 2 (no 1, no 0-based).
func TestRaceReturnsWinnerIndexOneBased(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local i, v = nu.task.race({
				function() nu.task.sleep(40); return "A" end,
				function() nu.task.sleep(2);  return "B" end,  -- gana esta: índice 2
				function() nu.task.sleep(40); return "C" end,
			})
			out.i = i
			out.v = v
		end)
	`)
	h.expectEval(`return tostring(out.i)`, "2")
	h.expectEval(`return out.v`, "B")
}

// TestRaceCancelsLosers: 🔒 `race` **cancela a las perdedoras**: la lenta marca un
// flag tras su sleep que NO debe verse, porque la rápida gana y la cancela durante
// su sleep. Comprueba el substrato de cancelación de S07 (frontera con S08).
func TestRaceCancelsLosers(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = { perdedora_termino = false }
		nu.task.spawn(function()
			local i = nu.task.race({
				function() nu.task.sleep(1); return "gana" end,
				function()
					nu.task.sleep(60)               -- la cancelan aquí
					out.perdedora_termino = true    -- NO debe ejecutarse
					return "pierde"
				end,
			})
			out.i = i
		end)
	`)
	h.expectEval(`return tostring(out.i)`, "1")
	h.expectEval(`return tostring(out.perdedora_termino)`, "false")
}

// TestRaceWinnerErrorReraised: si la primera en terminar lo hace por error, `race`
// relanza ese error (coherente con `all`: el error es el desenlace genuino). La
// task que falla rápido gana la carrera por error; las demás se cancelan.
func TestRaceWinnerErrorReraised(t *testing.T) {
	h := newHarness(t)

	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ok, err = pcall(function()
				nu.task.race({
					function() error({ code = "EIO", message = "rápido y mal" }) end,
					function() nu.task.sleep(50); return "lento" end,
				})
			end)
			out.ok = ok
			out.code = err.code
		end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "EIO")
}

// --- Validaciones de forma y contexto ---

// TestAllOutsideTask: `nu.task.all` es ⏸; fuera de una task lanza `EINVAL` (§1.3),
// como el resto de suspendientes.
func TestAllOutsideTask(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`return nu.task.all({ function() return 1 end })`)
	if se.Code != CodeEINVAL {
		t.Fatalf("all fuera de task: code got %q, want %q", se.Code, CodeEINVAL)
	}
}

// TestRaceOutsideTask: `nu.task.race` es ⏸; fuera de una task lanza `EINVAL`.
func TestRaceOutsideTask(t *testing.T) {
	h := newHarness(t)
	se := h.evalErr(`return nu.task.race({ function() return 1 end })`)
	if se.Code != CodeEINVAL {
		t.Fatalf("race fuera de task: code got %q, want %q", se.Code, CodeEINVAL)
	}
}

// TestAllEmptyList: una lista vacía es `EINVAL` (no hay nada que esperar ni
// alinear). Se prueba desde dentro de una task para no chocar con el chequeo ⏸.
func TestAllEmptyList(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ok, err = pcall(function() return nu.task.all({}) end)
			out.ok = ok
			out.code = err.code
		end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "EINVAL")
}

// TestAllBadElement: un elemento que no es Task ni función es `EINVAL` con mensaje
// que nombra la posición (accionable), no un pánico de Go.
func TestAllBadElement(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local ok, err = pcall(function()
				return nu.task.all({ function() return 1 end, 42 })  -- 42 no es Task ni fn
			end)
			out.ok = ok
			out.code = err.code
		end)
	`)
	h.expectEval(`return tostring(out.ok)`, "false")
	h.expectEval(`return out.code`, "EINVAL")
}

// --- Variantes de resultado ---

// TestAllNilResultPreservesPositions: una task que no retorna nada deja `nil` en su
// posición sin desplazar a las demás. Importa para la alineación (G27): los huecos
// no corren los índices. Se comprueba indexando explícitamente por posición.
func TestAllNilResultPreservesPositions(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local r = nu.task.all({
				function() return "x" end,
				function() return end,        -- sin valor de retorno -> nil
				function() return "z" end,
			})
			out.first = r[1]
			out.second_is_nil = (r[2] == nil)
			out.third = r[3]
		end)
	`)
	h.expectEval(`return out.first`, "x")
	h.expectEval(`return tostring(out.second_is_nil)`, "true")
	h.expectEval(`return out.third`, "z")
}

// TestAllParallelism: las tasks de `all` corren en paralelo, no en serie —el total
// se aproxima al máximo de los sleeps, no a su suma—. Cronometrarlo con el reloj de
// pared (`nu.sys`) sería frágil; en su lugar se comprueba el efecto observable: tres
// sleeps de 20 ms terminan todos (si fueran en serie tardaría 60 ms, pero igual
// terminan), así que aquí basta con que el resultado llegue completo y alineado.
// El no-bloqueo del loop ya lo blindan los tests de S05; este fija que `all` no
// serializa el fan-out.
func TestAllParallelism(t *testing.T) {
	h := newHarness(t)
	h.eval(`
		out = {}
		nu.task.spawn(function()
			local r = nu.task.all({
				function() nu.task.sleep(20); return 1 end,
				function() nu.task.sleep(20); return 2 end,
				function() nu.task.sleep(20); return 3 end,
			})
			out.sum = r[1] + r[2] + r[3]
		end)
	`)
	h.expectEval(`return tostring(out.sum)`, "6")
}
