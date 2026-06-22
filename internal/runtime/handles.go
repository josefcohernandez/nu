package runtime

// Registro de handles por dueño (api.md §14, sesión S13, inventario 🔒). Es la
// pieza que hace que `nu.plugin.reload` (best-effort, G2) "suelte TODOS los
// handles del plugin" sin dejar huérfanos: cada handle que el core entrega y que
// **sobrevive a un reload** —una suscripción de eventos (`nu.events.on/once`), un
// timer periódico (`nu.task.every`)— se etiqueta, al crearse, con el **dueño
// vigente** (el `currentOwner()` del `ownerStack`, S11). Recargar un plugin
// recorre su lista de handles y los libera uno a uno.
//
// POR QUÉ UN REGISTRO GENERAL, NO UN PARCHE PARA events+timers. El reload de G2
// es "suelta todos los handles del plugin": el conjunto de cosas que un plugin
// registra y que persisten entre arranques crecerá con el kernel —watchers de
// `nu.fs.watch` (S15), procesos de `nu.proc.spawn` (S16), handlers de input y
// regiones de UI (S29+)—. Si cada una inventara su propia limpieza, el reload
// sería un agregado frágil de casos especiales. En su lugar, el core lleva **un
// solo registro** indexado por dueño y cada primitiva que entrega un handle
// persistente lo **registra aquí** al crearlo y lo **desregistra** al soltarlo a
// mano (cancel/stop). `reload` no conoce los tipos concretos: itera la lista del
// dueño y llama `release()`. Añadir una primitiva nueva con handle persistente en
// una sesión futura es: implementar `ownedHandle` y llamar `track`/`untrack` —el
// reload la recoge gratis, sin tocar este fichero ni `reload`.
//
// CONCURRENCIA. Igual que el resto del estado del scheduler (bus de eventos,
// timers), el registro vive bajo el **token** Lua y se toca solo desde el estado
// principal: `on`/`once`/`every` (que registran), `cancel`/`stop` (que
// desregistran) y `reload` (que libera) son funciones Lua que corren con el
// token, y el arranque que empuja el `ownerStack` también. Por eso no necesita
// candado propio —el token lo serializa, como `eventBus`—.

// ownedHandle es un recurso entregado a un plugin que debe liberarse al recargarlo
// (G2). Lo implementan los tipos de handle persistente: `*subscriber` (eventos) y
// `*luaTimer` (every). `release` debe ser **idempotente** y **silencioso** —se
// llama tanto desde `reload` como, indirectamente, al soltarlo a mano— y NO debe
// re-tocar el registro (el desregistro lo orquesta quien llama, para no recursar).
type ownedHandle interface {
	// release suelta el recurso (marca la suscripción muerta, corta el timer...).
	// Idempotente: liberar dos veces es inocuo, porque `reload` puede liberar un
	// handle que el plugin ya estaba a punto de soltar.
	release()
	// owner devuelve el dueño con que se etiquetó al crearse. Lo usa `untrack`
	// para encontrar su lista sin que el handle tenga que recordar el scheduler.
	owner() string
}

// track registra un handle persistente bajo su dueño (el `currentOwner()` vigente
// al crearlo, que el handle ya guardó). Lo llaman `on`/`once` (events.go) y
// `every` (timers.go) al entregar el handle. Presupone el token tomado.
func (s *scheduler) track(h ownedHandle) {
	if s.ownerHandles == nil {
		s.ownerHandles = make(map[string][]ownedHandle)
	}
	o := h.owner()
	s.ownerHandles[o] = append(s.ownerHandles[o], h)
}

// untrack quita un handle de la lista de su dueño cuando se suelta **a mano**
// (`Sub:cancel`, `Timer:stop`): sin esto, el registro acumularía handles muertos
// (fuga) y un `reload` posterior intentaría liberar suscripciones/timers ya
// cancelados. Es idempotente: quitar uno que ya no está (p. ej. porque `reload` ya
// vació la lista) no hace nada. No llama a `release` —el desregistro y la
// liberación son pasos separados: aquí solo se actualiza el índice—. Presupone el
// token tomado.
func (s *scheduler) untrack(h ownedHandle) {
	if s.ownerHandles == nil {
		return
	}
	o := h.owner()
	list := s.ownerHandles[o]
	for i, x := range list {
		if x == h {
			// Quita por intercambio-con-el-último: el orden del registro no importa
			// (a diferencia del orden de despacho de eventos, que sí), solo que el
			// conjunto sea exacto. Evita re-deslizar la lista entera.
			list[i] = list[len(list)-1]
			s.ownerHandles[o] = list[:len(list)-1]
			break
		}
	}
	if len(s.ownerHandles[o]) == 0 {
		delete(s.ownerHandles, o)
	}
}

// releaseOwnerHandles libera TODOS los handles de un dueño y vacía su lista. Lo
// llama `reload` (plugin.go) tras emitir `core:plugin.unload`: suelta las
// suscripciones de eventos y para los timers del plugin que se recarga, de modo
// que su `init.lua` re-ejecutado parta de cero —"reload no deja handlers
// huérfanos" (G2, inventario 🔒)—. Presupone el token tomado.
//
// Copia la lista antes de iterar porque `release()` de algún handle podría, en
// teoría, tocar el registro (hoy no lo hace —los `release` son locales—, pero la
// copia lo blinda); y la entrada del mapa se borra al final, no a media iteración.
func (s *scheduler) releaseOwnerHandles(owner string) {
	if s.ownerHandles == nil {
		return
	}
	list := s.ownerHandles[owner]
	if len(list) == 0 {
		return
	}
	snapshot := make([]ownedHandle, len(list))
	copy(snapshot, list)
	delete(s.ownerHandles, owner) // el dueño queda sin handles tras este reload
	for _, h := range snapshot {
		h.release()
	}
}
