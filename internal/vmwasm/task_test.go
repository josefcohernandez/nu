package vmwasm

// Tests de M07: la superficie completa de enu.task sobre el bucle de M06 —
// future, all (G27), race, cleanup (LIFO) y cancelación cooperativa (§1.3).

import "testing"

// M07.1: future — rendez-vous entre dos tasks.
func TestTaskFuture(t *testing.T) {
	out := runScript(t, `
		out = "no"
		local f = enu.task.future()
		enu.task.spawn(function()
			out = f:await()
		end)
		enu.task.spawn(function()
			enu.task.sleep(5)
			f:set("valor")
		end)`)
	if out != "valor" {
		t.Fatalf("got %q", out)
	}
}

// M07.2: all — resultados ALINEADOS con los inputs (G27), no en orden de
// terminación. La task 1 duerme más que la 2, pero out[1] sigue siendo suyo.
func TestTaskAllAlineado(t *testing.T) {
	out := runScript(t, `
		out = ""
		enu.task.spawn(function()
			local r = enu.task.all({
				function() enu.task.sleep(20); return "lento" end,
				function() enu.task.sleep(2);  return "rapido" end,
			})
			out = r[1] .. "," .. r[2]
		end)`)
	if out != "lento,rapido" {
		t.Fatalf("G27: got %q (esperado lento,rapido — alineado con inputs)", out)
	}
}

// M07.3: race — la primera en terminar gana, con su índice.
func TestTaskRace(t *testing.T) {
	out := runScript(t, `
		out = ""
		enu.task.spawn(function()
			local idx, r = enu.task.race({
				function() enu.task.sleep(20); return "A" end,
				function() enu.task.sleep(2);  return "B" end,
			})
			out = tostring(idx) .. ":" .. r
		end)`)
	if out != "2:B" {
		t.Fatalf("race: got %q (esperado 2:B)", out)
	}
}

// M07.4: cleanup — corre en orden LIFO al terminar la task (éxito).
func TestTaskCleanupLIFO(t *testing.T) {
	out := runScript(t, `
		local traza = {}
		out = ""
		local w = enu.task.spawn(function()
			enu.task.cleanup(function() traza[#traza+1] = "primero-registrado" end)
			enu.task.cleanup(function() traza[#traza+1] = "segundo-registrado" end)
			enu.task.sleep(1)
		end)
		enu.task.spawn(function()
			enu.task.await(w)
			out = table.concat(traza, ",")
		end)`)
	// LIFO: el segundo registrado corre primero.
	if out != "segundo-registrado,primero-registrado" {
		t.Fatalf("cleanup LIFO: got %q", out)
	}
}

// M07.5: cleanup corre también cuando la task se cancela (§1.3: pase lo que pase).
func TestTaskCancelCorreCleanup(t *testing.T) {
	out := runScript(t, `
		out = "no"
		local limpio = false
		local w = enu.task.spawn(function()
			enu.task.cleanup(function() limpio = true end)
			enu.task.sleep(1000)  -- se cancelará antes
		end)
		enu.task.spawn(function()
			enu.task.sleep(5)
			enu.task.cancel(w)
			enu.task.sleep(5)
			out = tostring(limpio)
		end)`)
	if out != "true" {
		t.Fatalf("cleanup tras cancel: got %q", out)
	}
}

// M07.6: una task cancelada mientras espera un await termina; el que la esperaba
// recibe su resultado ECANCELED (observable, no capturable por el pcall interno).
func TestTaskCancelObservable(t *testing.T) {
	out := runScript(t, `
		out = "no"
		local w = enu.task.spawn(function()
			enu.task.sleep(1000)
			return "no-deberia"
		end)
		enu.task.spawn(function()
			enu.task.sleep(5)
			enu.task.cancel(w)
			local ok, e = pcall(function() return enu.task.await(w) end)
			out = tostring(ok) .. ":" .. tostring(e.code)
		end)`)
	if out != "false:ECANCELED" {
		t.Fatalf("cancel observable: got %q", out)
	}
}
