package runtime

import (
	lua "github.com/yuin/gopher-lua"
)

// Bus de eventos del core (api.md §4, sesión S10, inventario 🔒). Es el sustrato
// genérico donde las extensiones definen sus propios hooks: el core "no sabe lo
// que es un agente" (filosofía §1), así que `nu.events` no conoce ningún evento
// de producto —solo reserva su propio espacio (`core:`/`ui:`) y emite su ciclo
// de vida—. Tres firmas, **solo estado principal** (no [W]: en un worker no
// existe el bus; eso es S34):
//
//   - `nu.events.on(name, fn) -> Sub`   — suscribe; handler SÍNCRONO; `Sub:cancel()`.
//   - `nu.events.once(name, fn) -> Sub`  — corre una vez y se auto-cancela.
//   - `nu.events.emit(name, payload?)`   — despacho SÍNCRONO en el estado principal.
//
// LA SEMÁNTICA DE DESPACHO (G10), las tres invariantes que esta sesión blinda:
//
//  1. DESPACHO SOBRE FOTO. Cada `emit` corre sobre una **copia** de la lista de
//     suscriptores tomada al emitir. Quien se suscriba DURANTE ese despacho no ve
//     el evento en curso —solo los futuros—. Sin esto, suscribirse desde un
//     handler podría hacerle ver su propio evento (recursión encubierta) o
//     depender del orden de la iteración.
//
//  2. CANCELAR SURTE EFECTO INMEDIATO. Si un handler cancela una `Sub` que aún no
//     ha corrido en ESTE despacho, esa `Sub` ya **no** corre —aunque esté en la
//     foto—. La foto es de **identidad** (qué suscripciones había), pero antes de
//     invocar cada handler se comprueba que siga viva (`live`). Cancelar deja de
//     ser "a partir del próximo evento": es "a partir de ya".
//
//  3. EMITS ANIDADOS ENCOLADOS (anchura, NO recursión). Si un handler llama a
//     `emit`, ese emit NO se despacha en el acto (no se anida en la pila): se
//     **encola** y se despacha cuando el actual termina. El despacho es un bucle
//     plano que **drena** la cola; mientras hay un drenado en curso, `emit` solo
//     encola y vuelve. Consecuencia clave: un ping-pong infinito entre dos
//     handlers (A emite B, B emite A, ...) NO desborda la pila Go —se aplana en un
//     bucle largo—; lo corta el watchdog (S09) cuando el `emit` raíz se lanzó
//     dentro de una task (el slice de la task cubre el drenado; ver nota abajo).
//
// PCALL POR FRONTERA (ADR-008). Cada handler corre bajo `pcall` sobre un thread
// efímero de `host` (como `defer`/`every`, timers.go): uno que lance queda
// aislado —log best-effort— y NO impide correr a los demás de la foto. El orden
// es el de registro.
//
// EL WATCHDOG Y EL PING-PONG INFINITO. El drenado es plano, así que un ping-pong
// infinito es un **bucle largo de Go** en la goroutine de la task, no una
// recursión. Hay un matiz de implementación: cada handler corre sobre un thread
// efímero de `host`, que NO lleva el `context.Context` de la task —el palanca con
// que el watchdog (S09) rompe un slice de CPU puro vía `mainLoopWithContext`—. Por
// eso cancelar el ctx de la task no rompería por sí solo el bucle de drenado (la
// task no está ejecutando Lua sobre su `co`, sino orquestando handlers en Go).
// La solución, coherente con S09 y sin pieza nueva: el bucle de drenado, cuando
// corre dentro de una task, **comprueba cooperativamente entre handlers** si el
// watchdog disparó (`budgetExceeded`) y, en tal caso, reclama el aborto y
// desenrolla con el mismo centinela de S08/S09 (`abort`). Así un ping-pong
// infinito (muchos handlers pequeños re-emitiendo) se corta en el siguiente borde
// de handler al exceder el presupuesto: `EBUDGET` no capturable, `core:plugin.
// misbehaved` emitido por `runTask`, igual que cualquier slice excedido.
//
// Lo que SIGUE fuera del watchdog (idéntico límite que S09): (a) un `emit` desde
// el chunk principal o un handler síncrono (sobre `host`, sin task) —ahí un
// ping-pong infinito colgaría, como `while true do end` en el chunk—; y (b) un
// ÚNICO handler con un bucle de CPU puro en su interior, porque corre sobre un
// thread efímero sin contexto, exactamente como los handlers de `defer`/`every`
// que S09 dejó fuera. El borde cooperativo del drenado ataja el caso del bus —el
// rebote entre handlers—, que es el que api.md §4 nombra; el resto es el mismo
// territorio ya acotado en S09.

// subTypeName identifica la metatabla del handle `Sub` (lo que devuelven `on` y
// `once`), de la que cuelga `cancel`.
const subTypeName = "nu.events.Sub"

// subscriber es una suscripción a un nombre de evento. `live` es lo que hace que
// cancelar surta efecto inmediato (G10, invariante 2): el despacho lo consulta
// justo antes de invocar el handler, así que cancelar una `Sub` que aún no ha
// corrido en el despacho en curso la salta. `once` la auto-cancela tras su
// primera ejecución.
//
// Toda la vida de un `subscriber` (registro, cancelación, despacho) ocurre bajo
// el token Lua en el estado principal: `on`/`once`/`emit`/`cancel` son funciones
// Lua que corren con el token, y el drenado de `emit` también. Por eso `live` y
// las listas del `eventBus` no necesitan candado —el token los serializa—.
type subscriber struct {
	fn   *lua.LFunction
	once bool
	live bool
}

// pendingEmit es un trabajo de emisión encolado: el nombre del evento y su
// payload (un `LValue` ya capturado en el estado principal, no copiado —no cruza
// estados; el bus es solo del principal). Se drena por anchura (FIFO).
type pendingEmit struct {
	name    string
	payload lua.LValue
}

// eventBus es el estado del bus: las suscripciones por nombre de evento y la
// cola de emisiones anidadas. Vive en el scheduler porque comparte su token y su
// `host` (el estado principal donde corre todo el bus). No es [W]: un worker
// (S34) no lo tiene.
type eventBus struct {
	// subs mapea nombre de evento -> lista de suscriptores en **orden de
	// registro** (G10: el despacho respeta ese orden). Una `Sub` cancelada se
	// marca `live = false` y se purga perezosamente (al final de cada drenado),
	// no se borra a media iteración —eso movería índices de una foto en curso.
	subs map[string][]*subscriber

	// queue es la cola de emisiones anidadas (G10, invariante 3): un `emit`
	// llamado mientras se drena solo encola aquí. Se drena por anchura (FIFO).
	queue []pendingEmit

	// draining es true mientras un drenado está en curso. El primer `emit` (el
	// raíz) lo pone y drena hasta vaciar la cola; los `emit` anidados lo ven true
	// y solo encolan. Es lo que aplana la recursión en un bucle plano.
	draining bool
}

// registerEvents construye la tabla `nu.events` con `on`/`once`/`emit` e instala
// la metatabla del tipo `Sub`. Lo llama `registerNu`. El bus se inicializa vacío;
// vive en el scheduler (comparte token y `host`).
func (s *scheduler) registerEvents(nu *lua.LTable) {
	L := s.host

	s.events = &eventBus{subs: make(map[string][]*subscriber)}

	mt := L.NewTypeMetatable(subTypeName)
	index := L.NewTable()
	index.RawSetString("cancel", L.NewFunction(s.subCancel))
	L.SetField(mt, "__index", index)

	ev := L.NewTable()
	ev.RawSetString("on", L.NewFunction(s.eventsOn))
	ev.RawSetString("once", L.NewFunction(s.eventsOnce))
	ev.RawSetString("emit", L.NewFunction(s.eventsEmit))
	nu.RawSetString("events", ev)
}

// eventsOn implementa `nu.events.on(name, fn) -> Sub` (§4): suscribe `fn` al
// evento `name`. El handler es **síncrono** (corre en el loop, no puede llamar
// ⏸; para IO lanza una task con `nu.task.spawn`, §1.3). Devuelve un `Sub` con
// `cancel`.
func (s *scheduler) eventsOn(L *lua.LState) int {
	return s.subscribe(L, false)
}

// eventsOnce implementa `nu.events.once(name, fn) -> Sub` (§4): como `on`, pero
// el handler corre **una sola vez** y se auto-cancela tras esa ejecución (incluso
// si lanza: no vuelve a correr).
func (s *scheduler) eventsOnce(L *lua.LState) int {
	return s.subscribe(L, true)
}

// subscribe es el cuerpo común de `on`/`once`: valida, registra el suscriptor al
// final de la lista del evento (orden de registro, G10) y devuelve su `Sub`.
func (s *scheduler) subscribe(L *lua.LState, once bool) int {
	name := L.CheckString(1)
	fn := L.CheckFunction(2)

	sub := &subscriber{fn: fn, once: once, live: true}
	s.events.subs[name] = append(s.events.subs[name], sub)

	ud := L.NewUserData()
	ud.Value = sub
	L.SetMetatable(ud, L.GetTypeMetatable(subTypeName))
	L.Push(ud)
	return 1
}

// subCancel implementa `Sub:cancel()` (§4): cancela una suscripción. Marca
// `live = false` —no la borra de la lista en el acto— para que cancelar surta
// efecto **inmediato** durante un despacho en curso (G10, invariante 2: si la
// `Sub` aún no corrió en este emit, ya no corre) sin reindexar una foto a media
// iteración. La purga real ocurre al final del drenado (`purge`). Idempotente.
func (s *scheduler) subCancel(L *lua.LState) int {
	ud := L.CheckUserData(1)
	sub, ok := ud.Value.(*subscriber)
	if !ok {
		raiseError(L, CodeEINVAL, "Sub:cancel espera un handle de Sub", lua.LNil)
		return 0
	}
	sub.live = false
	return 0
}

// eventsEmit implementa `nu.events.emit(name, payload?)` (§4): despacho
// **síncrono** en el estado principal. No suspende. Si ya hay un drenado en
// curso (este `emit` viene de dentro de un handler), solo **encola** y vuelve
// —así los emits anidados se aplanan en anchura, sin recursión (G10, invariante
// 3)—. Si no, arranca el drenado y lo lleva hasta vaciar la cola.
func (s *scheduler) eventsEmit(L *lua.LState) int {
	name := L.CheckString(1)
	payload := L.Get(2) // LNil si no se pasó; se entrega tal cual al handler
	s.emit(L, name, payload)
	return 0
}

// emit es el motor de despacho, reutilizable desde Go (lo usa también
// `emitMisbehaved`, runtime.go, pasando `host` como `L`). **Presupone el token
// tomado** en el estado principal —todo el bus corre allí—. Encola el evento y, si
// nadie está drenando, drena hasta vaciar la cola (bucle plano, anchura). `L` es
// el thread que llamó a `emit`: si es el `co` de una task, el bucle vigila el
// watchdog entre handlers para cortar un ping-pong infinito (ver cabecera).
func (s *scheduler) emit(L *lua.LState, name string, payload lua.LValue) {
	b := s.events
	b.queue = append(b.queue, pendingEmit{name: name, payload: payload})

	if b.draining {
		// Ya hay un drenado en curso (emit anidado): solo encolar. El bucle de
		// abajo, en el frame que arrancó el drenado, lo recogerá. Esto es lo que
		// convierte la recursión en un bucle plano (G10): el ping-pong no crece la
		// pila Go.
		return
	}

	// Si el `emit` raíz se lanzó dentro de una task, el drenado entero corre en el
	// slice de esa task: hay que vigilar el watchdog (S09) entre handlers para
	// cortar un ping-pong infinito (los handlers corren en threads sin contexto, así
	// que la cancelación de ctx por sí sola no los rompe; ver cabecera del fichero).
	t, hasTask := s.taskOf(L)

	b.draining = true
	// Si el drenado se abandona por un **aborto** (el `abort` de abajo hace panic
	// para desenrollar la task de forma no capturable, S08/S09), `draining` se
	// quedaría en true y la cola a medias: el bus quedaría permanentemente atascado
	// (todo `emit` futuro solo encolaría). Este `defer` deja el bus en estado limpio
	// pase lo que pase —el panic sigue subiendo hacia `runTask`, que lo recupera—.
	// En el camino normal (sin panic) `draining` ya es false aquí y esto es no-op.
	defer func() {
		b.draining = false
		b.queue = nil
	}()
	for len(b.queue) > 0 {
		// Borde cooperativo del watchdog (G10 + S09): antes de despachar el siguiente
		// evento, si el watchdog disparó para esta task, reclama el aborto y
		// desenrolla con el centinela de S08/S09. `abort` no retorna (hace panic); el
		// `defer` de arriba restaura el bus y el panic sube hasta `runTask`.
		if hasTask && s.claimBudgetAbort(t) {
			s.abort(t) // EBUDGET no capturable; runTask emite core:plugin.misbehaved
		}
		job := b.queue[0]
		b.queue = b.queue[1:]
		s.dispatch(job.name, job.payload)
	}
}

// dispatch despacha UN evento: corre sus handlers sobre la **foto** de
// suscriptores (G10, invariante 1) y en orden de registro, cada uno bajo `pcall`
// por frontera (ADR-008). Antes de invocar cada handler comprueba que siga vivo
// (G10, invariante 2: una cancelación durante este despacho lo salta). Tras
// terminar, purga los suscriptores muertos del evento (los `once` que corrieron y
// los cancelados).
func (s *scheduler) dispatch(name string, payload lua.LValue) {
	b := s.events

	// FOTO: copia de la lista en el momento de emitir (G10, invariante 1). Iterar
	// sobre la copia hace que un `on` lanzado por un handler de ESTE despacho NO
	// se vea ahora —se añadió a `b.subs[name]`, no a esta copia— y solo cuente
	// para emisiones futuras. Es copia de punteros: `live` se sigue leyendo del
	// `*subscriber` vivo, así que una cancelación posterior SÍ se observa.
	snapshot := make([]*subscriber, len(b.subs[name]))
	copy(snapshot, b.subs[name])

	for _, sub := range snapshot {
		if !sub.live {
			// Cancelado (G10, invariante 2): puede haberlo cancelado un handler
			// anterior de este mismo despacho, o haber nacido ya muerto. No corre.
			continue
		}
		// `once` se auto-cancela ANTES de correr: si el handler lanza, igualmente no
		// vuelve a dispararse (contrato de `once`); y si re-emite el mismo evento,
		// no se re-ejecuta a sí mismo.
		if sub.once {
			sub.live = false
		}
		s.callEventHandler(sub.fn, payload)
	}

	s.purge(name)
}

// callEventHandler corre un handler de evento sobre un thread efímero de `host`,
// bajo `pcall` por frontera (ADR-008). **Presupone el token tomado.** Es la misma
// estrategia que `callSyncLocked` (timers.go): un thread por disparo para no tocar
// la pila del estado principal (que mientras `EvalString` espera en `waitIdle`
// custodia los valores de retorno del chunk). Un handler que lance queda aislado
// en el log (best-effort, ADR-008) y no impide correr a los demás de la foto.
//
// El handler es síncrono: NO puede suspender (no es ⏸). Corre sobre un thread de
// `host`, que no está registrado en `coToTask`, así que un ⏸ dentro de él daría
// `EINVAL` (la misma detección "estoy en una task" de §1.3).
func (s *scheduler) callEventHandler(fn *lua.LFunction, payload lua.LValue) {
	co, _ := s.host.NewThread()
	err := co.CallByParam(lua.P{Fn: fn, NRet: 0, Protect: true}, payload)
	if err != nil {
		_ = s.rt.log.write(levelError, s.rt.owner,
			"un handler de nu.events lanzó: "+errString(raisedValue(err)))
	}
}

// purge borra de la lista de un evento los suscriptores ya muertos (los `once`
// que corrieron y los cancelados). Se hace al FINAL de cada despacho, no a media
// iteración, para no reindexar una foto en curso. Si la lista queda vacía, se
// elimina la entrada del mapa para no acumular nombres muertos.
func (s *scheduler) purge(name string) {
	b := s.events
	list := b.subs[name]
	kept := list[:0]
	for _, sub := range list {
		if sub.live {
			kept = append(kept, sub)
		}
	}
	if len(kept) == 0 {
		delete(b.subs, name)
		return
	}
	b.subs[name] = kept
}
