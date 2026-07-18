package vmwasm

// Tests 🔒 de G44: el bombeo del scheduler con estado en la Instance.
// Blindan las tres manifestaciones que la auditoría verificó empíricamente
// (A-01/A-03/A-34 del informe, docs/audits/auditoria-2026-07-12.md):
//   - un `every` SOBREVIVE a la quiescencia de primer plano (pausa, no muerte)
//     y la siguiente invocación de RunTasks lo reanuda;
//   - el trabajo encolado desde fuera del bucle (EmitEvent) despierta al select
//     en tiempo acotado (el timbre kickPump), sin esperar al IO en vuelo;
//   - PumpTasks (el bombeo continuo del modo interactivo) ejecuta tasks
//     spawneadas en cualquier momento y se apaga limpio por su ctx.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// evalOK evalúa un chunk y falla el test ante cualquier error.
func evalOK(t *testing.T, inst *Instance, chunk string) {
	t.Helper()
	if _, lerr, err := inst.Eval(chunk); err != nil || lerr != "" {
		t.Fatalf("eval %q: lerr=%q err=%v", chunk, lerr, err)
	}
}

// evalInt lee una global numérica.
func evalInt(t *testing.T, inst *Instance, expr string) int {
	t.Helper()
	out, lerr, err := inst.Eval("return tostring(" + expr + ")")
	if err != nil || lerr != "" {
		t.Fatalf("eval %q: lerr=%q err=%v", expr, lerr, err)
	}
	n, cerr := strconv.Atoi(out)
	if cerr != nil {
		t.Fatalf("eval %q: no es número: %q", expr, out)
	}
	return n
}

// TestG44EverySobreviveQuiescencia (A-01): el contador de un `enu.task.every`
// avanza también en una SEGUNDA invocación de RunTasks. Antes de G44 este test
// fallaba: la quiescencia del primer RunTasks hacía cancelAll(), el sleep en
// vuelo del timer moría con ECANCELED no capturable y el segundo RunTasks no lo
// reanimaba (verificación empírica de la auditoría, ahora invertida).
func TestG44EverySobreviveQuiescencia(t *testing.T) {
	inst := newInstance(t)
	evalOK(t, inst, `
		n = 0
		enu.task.every(5, function() n = n + 1 end)
		enu.task.spawn(function() enu.task.sleep(30) end)`)
	if err := inst.RunTasks(context.Background()); err != nil {
		t.Fatalf("RunTasks 1: %v", err)
	}
	c1 := evalInt(t, inst, "n")
	if c1 == 0 {
		t.Fatal("el every no llegó a latir en la primera invocación")
	}

	// Entre invocaciones el bucle está PARADO: el timer vence y su resultado
	// espera en pumpCh (pausa). La segunda invocación lo drena y el every late.
	time.Sleep(20 * time.Millisecond)
	evalOK(t, inst, `enu.task.spawn(function() enu.task.sleep(40) end)`)
	if err := inst.RunTasks(context.Background()); err != nil {
		t.Fatalf("RunTasks 2: %v", err)
	}
	if c2 := evalInt(t, inst, "n"); c2 <= c1 {
		t.Fatalf("el every murió en la quiescencia: n pasó de %d a %d (debía avanzar)", c1, c2)
	}
}

// TestG44KickDespiertaElBucle (A-03): una task spawneada por el handler de un
// EmitEvent externo corre EN TIEMPO ACOTADO aunque la única petición en vuelo
// sea un sleep largo. Antes de G44, la task esperaba al vencimiento del sleep
// (~400 ms medidos en la auditoría); con el timbre corre en milisegundos.
func TestG44KickDespiertaElBucle(t *testing.T) {
	inst := newInstance(t)
	evalOK(t, inst, `
		hecho = 0
		enu.events.on("t:go", function()
			enu.task.spawn(function() hecho = 1 end)
		end)
		enu.task.spawn(function() enu.task.sleep(400) end)`)

	done := make(chan error, 1)
	go func() { done <- inst.RunTasks(context.Background()) }()

	time.Sleep(30 * time.Millisecond) // el bucle ya espera al sleep(400)
	if err := inst.EmitEvent("t:go", nil); err != nil {
		t.Fatalf("EmitEvent: %v", err)
	}
	// La task debe haber corrido MUCHO antes de que venza el sleep(400): se
	// sondea con Eval (serializado por el mutex; el bucle está esperando).
	deadline := time.Now().Add(300 * time.Millisecond)
	for evalInt(t, inst, "hecho") != 1 {
		if time.Now().After(deadline) {
			t.Fatal("la task del handler no corrió en 300 ms: el kick no despertó al bucle (el wakeup dependía del sleep(400) en vuelo)")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
}

// TestG44PumpTasksBombeoContinuo (A-34, la mecánica): el bombeo persistente
// ejecuta trabajo spawneado en cualquier momento (sin task de primer plano que
// lo "arrastre"), mantiene latiendo un every a través de quiescencias y se
// apaga por su ctx devolviendo el control sin colgarse.
func TestG44PumpTasksBombeoContinuo(t *testing.T) {
	inst := newInstance(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- inst.PumpTasks(ctx) }()

	// (1) Una task spawneada con el bombeo ya en marcha (y ocioso) corre: es el
	// gesto del keymap/handler del modo interactivo.
	evalOK(t, inst, `x = 0; enu.task.spawn(function() enu.task.sleep(1); x = 1 end)`)
	deadline := time.Now().Add(2 * time.Second)
	for evalInt(t, inst, "x") != 1 {
		if time.Now().After(deadline) {
			t.Fatal("la task spawneada no corrió bajo PumpTasks")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// (2) Un every late de forma continua (ninguna task de primer plano viva).
	evalOK(t, inst, `n = 0; enu.task.every(5, function() n = n + 1 end)`)
	c1 := -1
	deadline = time.Now().Add(2 * time.Second)
	for {
		c := evalInt(t, inst, "n")
		if c1 >= 0 && c > c1 {
			break // dos lecturas crecientes: late sin primer plano
		}
		if c > 0 {
			c1 = c
		}
		if time.Now().After(deadline) {
			t.Fatalf("el every no late bajo PumpTasks (n=%d)", c)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// (3) Reentrada detectada: un RunTasks concurrente no corrompe el estado.
	if err := inst.RunTasks(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "reentrante") {
		t.Fatalf("RunTasks con el bombeo activo debía fallar como reentrante; got %v", err)
	}

	// (4) Apagado: cancelar el ctx detiene el bucle con prontitud.
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("PumpTasks debía retornar context.Canceled; got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PumpTasks no retornó tras cancelar su ctx")
	}
}

// TestG44CloseReclamaElFondo: tras retornar RunTasks con un every pausado, el
// Close de la Instance cancela su petición en vuelo (reqCtx cuelga de inst.ctx)
// y la goroutine emisora no queda fugada esperando un lector que no volverá
// (el agravante de A-01: emisores bloqueados en un canal abandonado).
func TestG44CloseReclamaElFondo(t *testing.T) {
	inst := newInstance(t)
	evalOK(t, inst, `
		enu.task.every(3600000, function() end) -- una hora: solo Close puede reclamarlo
		enu.task.spawn(function() enu.task.sleep(1) end)`)
	if err := inst.RunTasks(context.Background()); err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	// El every quedó pausado con su sleep de una hora en vuelo. Close debe
	// soltarlo: si la goroutine no observara inst.ctx, quedaría una hora viva
	// (y con el buffer lleno, para siempre). Se comprueba de forma indirecta y
	// robusta: Close retorna y el proceso de test no acumula el timer (el
	// leak-check global de la suite con -race caza goroutines colgadas).
	if err := inst.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
