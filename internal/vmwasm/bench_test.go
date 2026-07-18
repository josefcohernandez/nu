package vmwasm

// Benchmarks del puente (migracion-vm.md M15, veto 2 de rendimiento; los del spike
// se promueven aquí). Dos métricas distintas, que NO hay que confundir:
//
//   1. BenchmarkBridgeYieldResume — el CICLO YIELD+RESUME DEL PUENTE (ADR-020), la
//      métrica que el veto 2 acota en **≤ 50 µs sostenido**. Es el coste de UNA
//      travesía del trampolín `lua_resume` (con su Snapshot de wazero): una
//      corrutina que cede en bucle, reanudada en caliente. Réplica EXACTA de la
//      metodología del spike (BenchmarkYieldResume_Wasm: `CoResume` en bucle sobre
//      `while true do coroutine.yield(...) end`), ahora contra el trampolín DE
//      PRODUCCIÓN y con el módulo oficial cargado. El spike lo midió en 26 µs en
//      frío; este audita que, en el intérprete real y bajo presión de GC, no se
//      degrada por encima de 50 µs.
//
//   2. BenchmarkSchedulerHostcallCycle — el ciclo COMPLETO del scheduler por una
//      primitiva ⏸: schedStep (con enc/dec del wire de tasks) + despacho en
//      goroutine de fondo + canal + reanudación. NO es "el puente": incluye toda la
//      maquinaria de orquestación alrededor. Se reporta como contexto, no como el
//      número del veto.

import (
	"context"
	"fmt"
	"testing"
)

// poolWithB es poolWith para benchmarks (*testing.B).
func poolWithB(b *testing.B, reg func(p *Pool)) *Instance {
	b.Helper()
	p, err := NewPool()
	if err != nil {
		b.Fatalf("NewPool: %v", err)
	}
	b.Cleanup(func() { _ = p.Close() })
	reg(p)
	inst, err := p.NewInstance()
	if err != nil {
		b.Fatalf("NewInstance: %v", err)
	}
	b.Cleanup(func() { _ = inst.Close() })
	return inst
}

// BenchmarkBridgeYieldResume mide el CICLO YIELD+RESUME PURO del puente: una
// corrutina que cede en bucle (`coroutine.yield`), reanudada con `CoResume` sobre
// el trampolín de producción. Cada iteración = UNA travesía `lua_resume` (Snapshot
// de wazero incluido, ADR-020). ns/op = coste del puente. VETO 2: ≤ 50 µs
// (50000 ns/op) sostenido. Es la réplica de BenchmarkYieldResume_Wasm del spike.
func BenchmarkBridgeYieldResume(b *testing.B) {
	inst := poolWithB(b, func(p *Pool) {})
	ref, err := inst.CoSpawn(`while true do coroutine.yield("t") end`)
	if err != nil {
		b.Fatalf("CoSpawn: %v", err)
	}
	v := "v"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st, _, err := inst.CoResume(ref, &v)
		if err != nil || st != CoYield {
			b.Fatalf("iter %d: st=%v err=%v", i, st, err)
		}
	}
	b.StopTimer()
}

// BenchmarkSchedulerHostcallCycle mide el ciclo COMPLETO del scheduler por cada
// primitiva ⏸: una primitiva no-op (`enu.bench.noop`) llamada en bucle desde una
// task. Cada llamada dispara un paso de scheduler (enc/dec del wire), un despacho
// en goroutine de fondo, un canal y una reanudación. NO es el número del veto (que
// es el puente puro, arriba): es el coste de orquestación real, reportado como
// contexto para dimensionar cuánto pesa la maquinaria por encima del puente.
func BenchmarkSchedulerHostcallCycle(b *testing.B) {
	inst := poolWithB(b, func(p *Pool) {
		p.RegisterSuspending("bench.noop", func(inst *Instance, args []any) ([]any, error) {
			return nil, nil
		})
	})
	// La task hace b.N llamadas ⏸: el bucle vive en Lua para que el timer mida sólo
	// el ciclo, no el arranque de la task ni el spawn.
	code := fmt.Sprintf("enu.task.spawn(function() for i = 1, %d do enu.bench.noop() end end)", b.N)
	if _, lerr, err := inst.Eval(code); err != nil || lerr != "" {
		b.Fatalf("Eval: lerr=%q err=%v", lerr, err)
	}
	b.ResetTimer()
	if err := inst.RunTasks(context.Background()); err != nil {
		b.Fatalf("RunTasks: %v", err)
	}
	b.StopTimer()
}

// BenchmarkTaskSpawnAwait mide un patrón realista de orquestación: una task que
// lanza otra y espera su resultado (spawn+await, el corazón de enu.task). No es el
// ciclo puro del puente pero sí el coste de coordinar dos tasks por el scheduler.
func BenchmarkTaskSpawnAwait(b *testing.B) {
	inst := poolWithB(b, func(p *Pool) {})
	code := fmt.Sprintf(`enu.task.spawn(function()
  for i = 1, %d do
    local t = enu.task.spawn(function() return i end)
    t:await()
  end
end)`, b.N)
	if _, lerr, err := inst.Eval(code); err != nil || lerr != "" {
		b.Fatalf("Eval: lerr=%q err=%v", lerr, err)
	}
	b.ResetTimer()
	if err := inst.RunTasks(context.Background()); err != nil {
		b.Fatalf("RunTasks: %v", err)
	}
	b.StopTimer()
}
