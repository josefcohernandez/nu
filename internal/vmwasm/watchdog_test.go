package vmwasm

// Tests del watchdog por slice (migracion-vm.md DM4, api.md §1.3, inventario 🔒).
// Blindan que el backend wasm aborta una task que quema CPU sin ceder (un bucle
// Lua puro) con EBUDGET, con la MISMA semántica observable que gopher (S09):
//   - el aborto es NO capturable (atraviesa el pcall del usuario);
//   - el estado sigue VIVO (otras tasks avanzan tras el corte);
//   - los cleanups de la task abortada CORREN (LIFO, §1.3);
//   - un presupuesto <=0 desactiva el watchdog (paridad con los workers, G15).
//
// IMPORTANTE (anti-cuelgue del CI): cada test usa un slice pequeño (30 ms) y un
// tope de wall-clock (`runTasksBounded`); si el watchdog NO cortara el bucle, el
// `nu_sched_step` Call giraría para siempre —así que un fallo se manifiesta como
// FALLO acotado, no como un CI colgado—.

import (
	"context"
	"testing"
	"time"
)

// newInstanceBudget crea una Instance con un presupuesto de slice concreto (el
// watchdog activo con `budget > 0`, o desactivado con `budget <= 0`).
func newInstanceBudget(t *testing.T, budget time.Duration) *Instance {
	t.Helper()
	p, err := NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	p.SetSliceBudget(budget)
	t.Cleanup(func() { _ = p.Close() })
	inst, err := p.NewInstance()
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// runTasksBounded conduce el bucle con un tope de wall-clock. Si RunTasks no
// termina en `cap` —el watchdog no cortó un bucle de CPU y el Call gira sin fin—,
// cancela el ctx de la instancia (WithCloseOnContextDone corta el Call en vuelo,
// para no dejar una goroutine quemando CPU) y FALLA el test en vez de colgar el CI.
func runTasksBounded(t *testing.T, inst *Instance, cap time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- inst.RunTasks(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTasks: %v", err)
		}
	case <-time.After(cap):
		inst.cancel() // corta el Call en vuelo para que la goroutine no siga girando
		t.Fatalf("RunTasks no terminó en %v: ¿el watchdog no cortó el bucle de CPU?", cap)
	}
}

// EL CORAZÓN (DM4 🔒): una task con un bucle de CPU puro (`while true do end`) se
// aborta por el watchdog tras el slice, y OTRA task sigue corriendo después. Sin
// watchdog esto colgaría; con él, termina y el estado sobrevive.
func TestWatchdogAbortaBucleCPU(t *testing.T) {
	inst := newInstanceBudget(t, 30*time.Millisecond)
	if _, lerr, err := inst.Eval(`
		otra = "no-corrio"
		enu.task.spawn(function()
			while true do end   -- bucle de CPU puro: jamás cede
		end)
		enu.task.spawn(function()
			enu.task.sleep(1)
			otra = "corrio"     -- el estado sigue vivo: esta task avanza tras el corte
		end)`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	runTasksBounded(t, inst, 3*time.Second)
	out, _, _ := inst.Eval(`return tostring(otra)`)
	if out != "corrio" {
		t.Fatalf("el estado no sobrevivió al aborto por budget: otra=%q", out)
	}
}

// NO CAPTURABLE (DM4 🔒): el bucle dentro de un `pcall(function() while true do end end)`
// NO lo captura ese pcall —el aborto es un yield, no un error, así que el bucle
// nunca "retorna" al pcall—. El aborto lo ve el scheduler; el awaiter lo OBSERVA
// como EBUDGET (capturable por ÉL, como en gopher: observación, no captura).
func TestWatchdogNoCapturablePorPcall(t *testing.T) {
	inst := newInstanceBudget(t, 30*time.Millisecond)
	if _, lerr, err := inst.Eval(`
		observado = "no"
		capturado = nil
		local w = enu.task.spawn(function()
			local ok = pcall(function() while true do end end)
			capturado = "SI:" .. tostring(ok)  -- NO debe ejecutarse (aborto no capturable)
		end)
		enu.task.spawn(function()
			local ok, e = pcall(function() return enu.task.await(w) end)
			observado = tostring(ok) .. ":" .. tostring(e and e.code)
		end)`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	runTasksBounded(t, inst, 3*time.Second)
	obs, _, _ := inst.Eval(`return tostring(observado)`)
	if obs != "false:EBUDGET" {
		t.Fatalf("EBUDGET no observable por el awaiter: observado=%q (want false:EBUDGET)", obs)
	}
	cap, _, _ := inst.Eval(`return tostring(capturado)`)
	if cap != "nil" {
		t.Fatalf("el pcall interno capturó el aborto (no-capturable violado): capturado=%q", cap)
	}
}

// CLEANUPS CORREN (DM4 🔒, §1.3): una task con enu.task.cleanup que entra en un
// bucle de CPU corre sus cleanups (LIFO) al abortarse por budget —pase lo que pase—.
func TestWatchdogCorreCleanups(t *testing.T) {
	inst := newInstanceBudget(t, 30*time.Millisecond)
	if _, lerr, err := inst.Eval(`
		traza = ""
		local w = enu.task.spawn(function()
			enu.task.cleanup(function() traza = traza .. "A" end)  -- registrado 1º
			enu.task.cleanup(function() traza = traza .. "B" end)  -- registrado 2º
			while true do end   -- se aborta por budget; los cleanups DEBEN correr
		end)
		enu.task.spawn(function()
			pcall(function() enu.task.await(w) end)  -- espera a que w muera
		end)`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	runTasksBounded(t, inst, 3*time.Second)
	out, _, _ := inst.Eval(`return tostring(traza)`)
	if out != "BA" { // LIFO: el segundo registrado corre primero
		t.Fatalf("los cleanups no corrieron LIFO al abortar por budget: traza=%q (want BA)", out)
	}
}

// PRESUPUESTO DESACTIVADO (DM4 🔒, G15): con sliceBudget<=0 el watchdog está OFF
// (como en el interior de un worker). Un bucle ACOTADO —largo, no infinito—
// corre hasta el final sin abortar.
func TestWatchdogDesactivado(t *testing.T) {
	inst := newInstanceBudget(t, 0) // watchdog desactivado
	if _, lerr, err := inst.Eval(`
		out = "no"
		enu.task.spawn(function()
			local x = 0
			for i = 1, 2000000 do x = x + 1 end  -- acotado; sin watchdog completa
			out = "completo:" .. tostring(x)
		end)`); err != nil || lerr != "" {
		t.Fatalf("setup: lerr=%q err=%v", lerr, err)
	}
	runTasksBounded(t, inst, 5*time.Second)
	out, _, _ := inst.Eval(`return tostring(out)`)
	if out != "completo:2000000" {
		t.Fatalf("con el watchdog OFF un bucle acotado debe completar; got %q", out)
	}
}
