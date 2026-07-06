package vmwasm

// Watchdog por slice del backend wasm (migracion-vm.md DM4, api.md §1.3,
// inventario 🔒). Réplica de la semántica del watchdog de gopher
// (internal/runtime/watchdog.go): abortar la task que quema CPU sin ceder —un
// bucle Lua puro (`while true do end`)— con EBUDGET **NO capturable**, dejando el
// estado vivo y corriendo sus cleanups.
//
// EL PROBLEMA (por qué wasm lo hace distinto a gopher). En gopher cada task es una
// goroutine con un `context.Context`; el intérprete comprueba `ctx.Done()` en cada
// instrucción y el watchdog (otra goroutine) cancela el ctx. En wazero eso NO sirve:
// cancelar el ctx de un Call cierra el MÓDULO ENTERO (mata el estado Lua), no una
// task —y todas las tasks viven en un único estado que un solo `nu_sched_step` Call
// hace girar—. Un `while true do end` dentro de una task haría que ese Call girase
// para siempre sin volver a Go.
//
// LA PALANCA: un count-hook de PUC-Lua. `__wd_arm()` (nu_shim.c) instala en cada
// corrutina de task un hook con `LUA_MASKCOUNT` que, cada WD_COUNT instrucciones,
// llama al import `nu_over_budget` (hostOverBudget, vmwasm.go). Si el slice rebasó
// su deadline, el hook CEDE (`lua_yield`). En Lua 5.4 un count-hook puede ceder, y
// ese yield es un `luaD_throw(LUA_YIELD)` idéntico al de un `coroutine.yield`
// normal: atraviesa el `pcall` (base yieldable, sin frame de try intermedio) y el
// trampolín Snapshot/Restore (verificado en la Fase 0 de esta sesión). Por eso el
// aborto es NO capturable: el `pcall` del usuario nunca lo ve; sólo el scheduler.
//
// EL DEADLINE ES POR SLICE (paridad con gopher, que arma el watchdog por slice de
// task). El preludio del scheduler (`preludioSched`, `__resume`) llama a
// `__reset_budget()` ANTES de cada `coroutine.resume`, fijando
// `inst.taskDeadline = now + sliceBudget`. Así cada tramo continuo de Lua tiene su
// propio presupuesto; una task que suspende a menudo nunca lo rebasa.
//
// EL ABORTO lo consuma el scheduler Lua: como el count-hook no puede ceder valores
// (luaD_hook restaura el `top` de la pila tras el hook), el yield del watchdog es
// de 0 valores y `coroutine.resume` devuelve `yielded == nil` —firma inequívoca,
// pues todos los ⏸ normales ceden una tabla `{op=...}`—. `__resume` lo detecta,
// marca la task con `{code="EBUDGET"}`, corre sus cleanups LIFO (`__finish`) y NO
// la reencola. Es el gemelo del aborto por cancelación (`t.cancelled`), con EBUDGET.
//
// DESACTIVADO: un `sliceBudget <= 0` hace que `nu_over_budget` devuelva siempre 0
// (el hook nunca cede). Es lo que usan los workers (G15: un worker es un
// mini-runtime cuyo trabajo puede ser quemar CPU) y los tests que no lo quieren.

import "time"

// registerWatchdog registra la primitiva `__reset_budget` del watchdog (DM4).
// Presente en TODO Pool (principal y workers): el scheduler Lua (`__resume`, en
// `preludioSched`, que está en todos los preludios) la llama antes de cada
// `coroutine.resume`. Se registra en `newBarePool`; en los workers,
// `workerGrants` la excluye de la copia del padre para no duplicarla.
func (p *Pool) registerWatchdog() {
	// __reset_budget(): reinicia el deadline del slice de la task que va a
	// reanudarse. Fija `inst.taskDeadline = now + sliceBudget`, que el count-hook
	// (vía nu_over_budget) comparará en cada chequeo. Con `sliceBudget <= 0` el
	// deadline es irrelevante (nu_over_budget devuelve 0 sin mirarlo), así que ni
	// siquiera se calcula. Síncrona: corre en el goroutine del Call, sin ceder.
	p.Register("__reset_budget", func(inst *Instance, _ []any) ([]any, error) {
		if inst.sliceBudget > 0 {
			inst.taskDeadline = time.Now().Add(inst.sliceBudget)
		}
		return nil, nil
	})
}
