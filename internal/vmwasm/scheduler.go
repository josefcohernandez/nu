package vmwasm

// Driver Go del scheduler por corrutinas (ADR-020, M06). Conduce el bucle de
// tasks: llama a `__sched_step` (por el export `nu_sched_step`), recoge las
// peticiones de trabajo externo que las tasks ceden, las cumple en goroutines de
// fondo (que jamás tocan la VM), y reanuda las tasks con los resultados. Es el
// event loop de ADR-004, ahora sobre corrutinas nativas (sin el token de ADR-011).
//
// M06 implementa el núcleo: spawn/sleep/await y el bucle. Las primitivas
// suspendentes de IO (fs/http/...) se enchufan aquí como nuevas `op` en M09; el
// mecanismo (ceder una petición, cumplirla, reanudar) es el mismo.

import (
	"context"
	"fmt"
	"time"
)

// schedStep llama al export nu_sched_step con los resultados inyectados (wire) y
// devuelve las peticiones pendientes (wire).
func (inst *Instance) schedStep(injected []byte) ([]byte, error) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if err := inst.writeBuf(injected); err != nil {
		return nil, err
	}
	if inst.schedStepFn == nil {
		inst.schedStepFn = inst.mod.ExportedFunction("nu_sched_step")
	}
	r, err := inst.schedStepFn.Call(inst.ctx, uint64(len(injected)))
	if err != nil {
		return nil, err
	}
	if int32(r[0]) < 0 {
		return nil, fmt.Errorf("vmwasm: nu_sched_step falló: %s", inst.readResult())
	}
	n := uint32(r[0])
	b, _ := inst.mod.Memory().Read(inst.bufPtr, n)
	out := make([]byte, n)
	copy(out, b)
	return out, nil
}

// asyncResult es el resultado de una pieza de trabajo externo, listo para
// reinyectar en la task que lo pidió.
type asyncResult struct {
	id     any // el id de la task (int64 en el wire)
	result any
	isErr  bool
}

// RunTasks conduce el bucle de scheduler hasta que no queda ninguna task viva
// (todas terminaron). Las tasks se crean desde Lua (nu.task.spawn) antes o
// durante el bucle. `ctx` permite cancelar el bucle entero (M07 lo usará para el
// apagado); su cancelación aborta la espera y retorna.
func (inst *Instance) RunTasks(ctx context.Context) error {
	ch := make(chan asyncResult, 64)
	outstanding := 0
	var inject []any // resultados a inyectar en el próximo step

	for {
		injWire, err := Encode([]any{inject})
		if err != nil {
			return err
		}
		inject = nil

		pendingWire, err := inst.schedStep(injWire)
		if err != nil {
			return err
		}
		pending, err := decodePending(pendingWire)
		if err != nil {
			return err
		}

		// Despacha cada petición de trabajo externo en una goroutine de fondo.
		for _, p := range pending {
			outstanding++
			go inst.performRequest(ctx, p, ch)
		}

		if outstanding == 0 {
			return nil // ninguna task viva ni trabajo en vuelo: todas terminaron
		}

		// Espera al menos un resultado, y drena los que ya estén listos.
		select {
		case r := <-ch:
			outstanding--
			inject = append(inject, resultMap(r))
		case <-ctx.Done():
			return ctx.Err()
		}
		for draining := true; draining; {
			select {
			case r := <-ch:
				outstanding--
				inject = append(inject, resultMap(r))
			default:
				draining = false
			}
		}
	}
}

// pendingReq es una petición de trabajo externo cedida por una task.
type pendingReq struct {
	id      any
	op      string
	request map[string]any
}

// decodePending interpreta el wire que __sched_step devolvió: un único array de
// { id, request } donde request es { op, ... }.
func decodePending(wire []byte) ([]pendingReq, error) {
	vals, err := Decode(wire)
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 || vals[0] == nil {
		return nil, nil
	}
	arr, ok := vals[0].([]any)
	if !ok {
		return nil, fmt.Errorf("vmwasm: sched pending no es array: %T", vals[0])
	}
	reqs := make([]pendingReq, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("vmwasm: sched item no es map: %T", item)
		}
		req, _ := m["request"].(map[string]any)
		op, _ := req["op"].(string)
		reqs = append(reqs, pendingReq{id: m["id"], op: op, request: req})
	}
	return reqs, nil
}

// performRequest cumple una petición de trabajo externo y manda el resultado por
// el canal. M06: "sleep"; M09: "hostcall" (una primitiva ⏸).
func (inst *Instance) performRequest(ctx context.Context, p pendingReq, ch chan<- asyncResult) {
	switch p.op {
	case "sleep":
		ms, _ := p.request["ms"].(int64)
		if msf, ok := p.request["ms"].(float64); ok {
			ms = int64(msf)
		}
		t := time.NewTimer(time.Duration(ms) * time.Millisecond)
		defer t.Stop()
		select {
		case <-t.C:
			ch <- asyncResult{id: p.id, result: nil}
		case <-ctx.Done():
			ch <- asyncResult{id: p.id, result: map[string]any{"code": "ECANCELED", "message": "cancelada"}, isErr: true}
		}
	case "hostcall":
		// Una primitiva ⏸: corre su HostFn en ESTA goroutine de fondo (no toca la
		// VM; contrato de RegisterSuspending) y reanuda con {ok, values} o {ok=false, err}.
		inst.performHostcall(p, ch)
	default:
		ch <- asyncResult{id: p.id, result: "op de scheduler desconocida: " + p.op, isErr: true}
	}
}

// performHostcall ejecuta el HostFn de una primitiva suspendente y empaqueta el
// resultado (o el error estructurado) para reanudar la task.
func (inst *Instance) performHostcall(p pendingReq, ch chan<- asyncResult) {
	idF, _ := p.request["id"].(int64)
	if idFl, ok := p.request["id"].(float64); ok {
		idF = int64(idFl)
	}
	id := int32(idF)
	var args []any
	if a, ok := p.request["args"].([]any); ok {
		args = a
	}
	reg := inst.pool.reg
	if id < 0 || int(id) >= len(reg.fns) {
		ch <- asyncResult{id: p.id, result: map[string]any{"ok": false, "err": map[string]any{"code": "EINVAL", "message": "id de primitiva fuera de rango"}}}
		return
	}
	rets, callErr := reg.fns[id](inst, args)
	if callErr != nil {
		ch <- asyncResult{id: p.id, result: map[string]any{"ok": false, "err": errToMap(callErr)}}
		return
	}
	// {ok=true, values=[...], n=len} para que el thunk desempaquete con nils.
	ch <- asyncResult{id: p.id, result: map[string]any{"ok": true, "values": rets, "n": int64(len(rets))}}
}

// errToMap traduce un error de HostFn a la tabla estructurada del contrato (§1.4).
func errToMap(callErr error) map[string]any {
	if se, ok := callErr.(*StructuredError); ok {
		m := map[string]any{"code": se.Code, "message": se.Message}
		if se.Detail != nil {
			m["detail"] = se.Detail
		}
		return m
	}
	return map[string]any{"code": "EIO", "message": callErr.Error()}
}

// resultMap empaqueta un asyncResult para el wire de inyección.
func resultMap(r asyncResult) map[string]any {
	return map[string]any{"id": r.id, "result": r.result, "iserr": r.isErr}
}
