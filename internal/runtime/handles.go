package runtime

// Registro de handles por dueño (api.md §14, sesión S13, inventario 🔒). Es la
// pieza que hace que `enu.plugin.reload` (best-effort, G2) "suelte TODOS los
// handles del plugin" sin dejar huérfanos: cada handle que el core entrega y que
// **sobrevive a un reload** —una suscripción de eventos (`enu.events.on/once`), un
// timer periódico (`enu.task.every`)— se etiqueta, al crearse, con el **dueño
// vigente** (el `currentOwner()` del `ownerStack`, S11). Recargar un plugin
// recorre su lista de handles y los libera uno a uno.
//
// POR QUÉ UN REGISTRO GENERAL, NO UN PARCHE PARA events+timers. El reload de G2
// es "suelta todos los handles del plugin": el conjunto de cosas que un plugin
// registra y que persisten entre arranques crecerá con el kernel —watchers de
// `enu.fs.watch` (S15), procesos de `enu.proc.spawn` (S16), handlers de input y
// regiones de UI (S29+)—. Si cada una inventara su propia limpieza, el reload
// sería un agregado frágil de casos especiales. En su lugar, el core lleva **un
// solo registro** indexado por dueño y cada primitiva que entrega un handle
// persistente lo **registra aquí** al crearlo y lo **desregistra** al soltarlo a
// mano (cancel/stop). `reload` no conoce los tipos concretos: itera la lista del
// dueño y llama `release()`. Añadir una primitiva nueva con handle persistente en
// una sesión futura es: implementar `ownedHandle` y llamar `track`/`untrack` —el
// reload la recoge gratis, sin tocar este fichero ni `reload`.
//
// CONCURRENCIA. En el backend wasm el registro ya NO vive bajo un token único:
// `track` lo llama `spawnProc` desde la goroutine de fondo de un hostcall
// suspendente, `untrack` la ruta de reap temprano (proc.go maybeReap, otra
// goroutine de fondo) y `releaseOwnerHandles` el reload (también suspendente).
// Por eso las tres operan bajo `s.mu`, el mismo candado que protege los demás
// mapas de recursos de fondo del scheduler; `release()` se invoca FUERA del
// candado (hace IO: matar procesos, cerrar pipes).

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
// al crearlo, que el handle ya guardó). Lo llaman las primitivas que entregan un
// handle persistente Go-side (`enu.proc.spawn`, `enu.fs.watch`).
func (s *scheduler) track(h ownedHandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
// liberación son pasos separados: aquí solo se actualiza el índice—.
func (s *scheduler) untrack(h ownedHandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// releaseOwnerHandles libera TODOS los handles Go-side de un dueño y vacía su
// lista. Lo llama `reloadWasm` (vmwasm_loader.go) tras el `__release_owner` del
// preludio: el registro Lua solo conoce subs y timers; los recursos Go (procesos
// de `enu.proc.spawn`, watchers de `enu.fs.watch`) viven aquí —"un spawn de su
// init.lua no debe sobrevivir a la recarga" (G2, inventario 🔒)—.
//
// El snapshot y el borrado ocurren bajo `s.mu`; los `release()` corren FUERA del
// candado (hacen IO: SIGKILL, cierre de pipes/watchers) y no deben re-tocar el
// registro (contrato de `ownedHandle`); un `untrack` concurrente del reap
// temprano sobre un handle ya retirado es un no-op idempotente.
func (s *scheduler) releaseOwnerHandles(owner string) {
	s.mu.Lock()
	list := s.ownerHandles[owner]
	if len(list) == 0 {
		s.mu.Unlock()
		return
	}
	snapshot := make([]ownedHandle, len(list))
	copy(snapshot, list)
	delete(s.ownerHandles, owner) // el dueño queda sin handles tras este reload
	s.mu.Unlock()
	for _, h := range snapshot {
		h.release()
	}
}
