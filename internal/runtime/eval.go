package runtime

import (
	"context"
	"errors"
	"strconv"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

// EvalString compila y ejecuta `code` como un chunk Lua y devuelve sus valores
// de retorno convertidos a string (vía `tostring`), en orden. Es lo que respalda
// `nu -e`: el chunk `return nu.version.api` produce `["2"]` (G32 lo subió de 1).
//
// Si el chunk lanza un error estructurado del core (§1.4), se devuelve como
// `*StructuredError` con su `code`/`message` intactos: el puente no traga ni
// reescribe el error al cruzar la frontera Lua→Go (invariante 🔒 de S02). Un
// error de sintaxis o un `error("string")` cualquiera se devuelve tal cual.
//
// El chunk de `nu -e` corre en el estado principal, **no es una task**: puede
// lanzar tasks con `nu.task.spawn` pero no usar funciones ⏸ (que exigen estar en
// una task, §1.3). Corre con el token Lua tomado; al soltarlo, las tasks que
// lanzó progresan, y `waitIdle` espera a que todas terminen antes de leer los
// valores de retorno del chunk (que viven en la pila del estado principal, que
// las tasks —en sus propios threads— nunca tocan).
func (rt *Runtime) EvalString(code string) ([]string, error) {
	// Ramificación del estrangulador (migracion-vm.md M13d): con el backend wasm
	// seleccionado, el chunk corre sobre la Instance wasm (el catálogo real nu.*),
	// no sobre el estado gopher. El enrutado SÓLO aplica bajo VMWasm; en gopher (el
	// default hasta M16) todo sigue igual, por lo que su suite no cambia.
	if rt.vmBackend == VMWasm {
		return rt.evalStringWasm(code)
	}
	L := rt.L
	s := rt.sched

	s.acquire()
	fn, err := L.LoadString(code)
	if err != nil {
		s.release()
		return nil, err
	}

	base := L.GetTop()
	L.Push(fn)
	perr := L.PCall(0, lua.MultRet, nil)
	s.release()

	// Espera a que las tasks lanzadas por el chunk corran a término (sus efectos,
	// sus liberaciones en S08) antes de devolver el control.
	s.waitIdle()

	s.acquire()
	defer s.release()

	if perr != nil {
		if se, ok := structuredFromError(perr); ok {
			return nil, se
		}
		return nil, perr
	}

	n := L.GetTop() - base
	results := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		v := L.Get(base + i)
		results = append(results, L.ToStringMeta(v).String())
	}
	L.SetTop(base)
	return results, nil
}

// EvalTaskString compila `code` y lo ejecuta **como una task** (§3), no como el
// chunk principal: a diferencia de `EvalString` (que corre en el estado principal
// y por eso NO puede usar funciones ⏸), aquí el chunk corre sobre su propio thread
// con el puente de suspensión disponible, de modo que puede llamar directamente a
// `nu.fs.read`, `nu.http.stream`, `Session:send` del agente, etc. Espera a que la
// task —y cualquier otra que ella lance— termine, y devuelve sus valores de
// retorno convertidos a string (vía `tostring`), en orden.
//
// Es el **ejecutor headless** del binario: respalda los modos del CLI que orquestan
// extensiones suspendientes sin TTY (un turno de agente headless, `--continue`), la
// contraparte ⏸ de `nu -e`. NO es superficie Lua sagrada (igual que `EvalString` o
// `RenderBareScreen`): es la interfaz Go del ejecutable, fuera de api.md. El core
// sigue sin saber lo que es un agente (ADR-003): aquí solo corre un chunk Lua a
// término; la lógica de agente vive en la extensión `agent` y en el driver Lua que
// el CLI le pasa (main.go).
//
// Errores: si el chunk (o lo que orquesta) lanza un error estructurado del core o
// de una extensión (§1.4), se devuelve como `*StructuredError` con su `code`/
// `message` intactos (el puente no traga ni reescribe el code, invariante 🔒 de
// S02), exactamente como `EvalString`. Un error no estructurado (sintaxis,
// `error("texto")`) se rinde a texto. Una cancelación/abort de la task se reporta
// como `ECANCELED`/`EBUDGET` (la task no entrega valor; §1.3).
func (rt *Runtime) EvalTaskString(code string) ([]string, error) {
	// Ramificación del estrangulador (migracion-vm.md M13d): igual que EvalString,
	// con el backend wasm el chunk corre COMO TASK sobre la Instance wasm. Sólo bajo
	// VMWasm; en gopher todo sigue igual.
	if rt.vmBackend == VMWasm {
		return rt.evalTaskStringWasm(code)
	}
	L := rt.L
	s := rt.sched

	s.acquire()
	fn, err := L.LoadString(code)
	if err != nil {
		s.release()
		return nil, err
	}
	s.release()

	// Lanza el chunk como una task (su propio thread) y espera a que el primer
	// plano —la task y cuanto encole— se quiesca. `spawn` arranca la goroutine;
	// `runTask` toma el token por su cuenta, así que aquí el token NO debe estar
	// tomado (lo soltamos arriba tras compilar).
	//
	// `spawnConsumed` (no `spawn`): este ejecutor SÍ recoge el desenlace de la task
	// —incluido su error, abajo, vía `t.errValue`— y lo devuelve al llamante (que el
	// CLI mapea a un código de salida). Por eso la task NO es fire-and-forget: marcarla
	// como consumida por el host evita que `runTask` escriba la línea best-effort "una
	// task terminó con error y nadie hizo await" en una ruta de error LEGÍTIMA (p. ej.
	// `--continue` sin sesiones, un turno que lanza `EPROVIDER`). El flag se fija antes
	// de arrancar la goroutine, así que es visible sin carrera (ver `spawnConsumed`).
	t := s.spawnConsumed(fn, nil)
	s.waitIdle()

	s.acquire()
	defer s.release()

	// La task fue abortada (cancelación o watchdog): no entrega valor (§1.3). Se
	// reporta como el error estructurado correspondiente para que el CLI lo mapee a
	// un código de salida coherente.
	if t.canceled {
		if t.reason == abortBudget {
			return nil, &StructuredError{Code: CodeEBUDGET,
				Message: "la task del CLI excedió el presupuesto de slice del watchdog", Detail: lua.LNil}
		}
		return nil, &StructuredError{Code: CodeECANCELED,
			Message: "la task del CLI fue cancelada", Detail: lua.LNil}
	}

	if t.errValue != nil {
		if se, ok := structuredFromValue(t.errValue); ok {
			return nil, se
		}
		return nil, &luaRuntimeError{value: t.errValue}
	}

	results := make([]string, 0, len(t.results))
	for _, v := range t.results {
		results = append(results, L.ToStringMeta(v).String())
	}
	return results, nil
}

// luaRuntimeError envuelve un error de task que NO es la tabla estructurada del
// contrato §1.4 (un `error("texto")`, un error nativo de Lua): conserva el valor
// para rendirlo a texto. Lo usa `EvalTaskString` para que el CLI tenga siempre un
// `error` Go que mapear a un código de salida, aunque el fallo no fuera estructurado.
type luaRuntimeError struct {
	value lua.LValue
}

func (e *luaRuntimeError) Error() string { return errString(e.value) }

// evalStringWasm es la variante de EvalString sobre el backend wasm (M13d). El
// chunk corre en el ESTADO PRINCIPAL de la Instance (inst.Eval), no como task:
// puede lanzar tasks con nu.task.spawn pero no usar funciones ⏸, igual que el chunk
// de `nu -e` en gopher (§1.3). Tras evaluarlo, `RunTasks` drena las tasks que haya
// lanzado (el equivalente wasm de `waitIdle`) antes de devolver sus valores. Un
// fallo de construcción del estado wasm (`buildWasmState`) se reporta aquí, aplazado
// desde `New` (rt.wasmErr).
func (rt *Runtime) evalStringWasm(code string) ([]string, error) {
	if rt.wasmErr != nil {
		return nil, rt.wasmErr
	}
	// El chunk se envuelve en un `pcall` cuyos retornos se capturan con table.pack.
	// Así se logran DOS cosas a la vez:
	//  1) Se preserva el RECUENTO EXACTO de valores de retorno —como `L.GetTop()` en
	//     gopher—: un `return ""` da UN valor "" (no cero), y un `return a, b` da dos.
	//  2) El error se captura COMO VALOR Lua (la tabla estructurada intacta), no como
	//     texto ya rendido por el shim. Sin el pcall, un `error({code=...})` en el
	//     ESTADO PRINCIPAL se popea en nu_eval y sólo sobrevive su `luaL_tolstring`
	//     ("table: 0x..."), perdiendo el code/message. Con él leemos e.code/e.message
	//     en Lua y reconstruimos el *StructuredError fiel (mismo truco que el camino
	//     de task en evalTaskWrapper). El chunk corre en el ESTADO PRINCIPAL (no task):
	//     puede lanzar tasks pero no usar ⏸ directo (§1.3).
	_, luaErr, goErr := rt.wasm.Eval(evalStringWrapper(code))
	if goErr != nil {
		return nil, goErr // trap del motor wasm: fallo duro
	}
	if luaErr != "" {
		// El wrapper no compiló (sintaxis en `code`): sólo hay texto.
		return nil, wasmChunkError(luaErr)
	}
	// Sondea el desenlace: si el chunk lanzó, reconstruye el error ANTES de drenar
	// tasks (como gopher, que devuelve el fallo del chunk sin drenar).
	okStr, _, _ := rt.wasm.Eval("return tostring(__es_ok)")
	if okStr != "true" {
		codeStr, _, _ := rt.wasm.Eval("return tostring(__es_err_code)")
		if codeStr != "nil" {
			msgStr, _, _ := rt.wasm.Eval("return tostring(__es_err_msg or '')")
			return nil, &StructuredError{Code: codeStr, Message: msgStr, Detail: lua.LNil}
		}
		strStr, _, _ := rt.wasm.Eval("return tostring(__es_err_str)")
		return nil, wasmChunkError(strStr)
	}
	// Drena las tasks que el chunk haya lanzado (sus efectos y liberaciones deben
	// completar antes de devolver, como waitIdle en gopher).
	if err := rt.wasm.RunTasks(context.Background()); err != nil {
		return nil, err
	}
	// Lee el recuento y serializa cada valor con tostring (leído de uno en uno para
	// no depender de un delimitador que un valor podría contener).
	nStr, _, _ := rt.wasm.Eval("return tostring(__es_n)")
	n, err := strconv.Atoi(nStr)
	if err != nil || n < 0 {
		return nil, nil
	}
	results := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		v, lerr, gerr := rt.wasm.Eval("return tostring(__es[" + strconv.Itoa(i) + "])")
		if gerr != nil {
			return nil, gerr
		}
		if lerr != "" {
			return nil, wasmChunkError(lerr)
		}
		results = append(results, v)
	}
	return results, nil
}

// evalStringWrapper envuelve `code` en un pcall que captura su desenlace (recuento de
// retornos y error estructurado) en globales `__es_*`, de forma análoga a
// evalTaskWrapper pero SIN spawnear una task: el chunk corre en el estado principal.
// Los globales se reinician al principio para que una llamada previa no filtre estado.
func evalStringWrapper(code string) string {
	return `__es_ok = nil; __es_n = 0; __es = nil
__es_err_code = nil; __es_err_msg = nil; __es_err_str = nil
local __packed = table.pack(pcall(function()
` + code + `
end))
__es_ok = __packed[1]
if __es_ok then
  __es_n = __packed.n - 1
  __es = {}
  for i = 2, __packed.n do __es[i - 1] = __packed[i] end
else
  local e = __packed[2]
  if type(e) == "table" and type(e.code) == "string" then
    __es_err_code = e.code
    __es_err_msg = e.message
  else
    __es_err_str = tostring(e)
  end
end`
}

// evalTaskStringWasm es la variante de EvalTaskString sobre el backend wasm (M13d).
// A diferencia de evalStringWasm, el chunk corre COMO TASK: sobre su propio thread,
// con el puente ⏸ disponible, de modo que puede llamar directamente a nu.fs.read,
// nu.http.request, etc. El chunk se envuelve en una task cuyo `pcall` captura su
// desenlace (primer valor de retorno o error) en globales, para que la task nunca
// lance —el scheduler Lua captura los errores por task, no escapan de RunTasks— y
// podamos leer el resultado tras drenar el bucle. Un sondeo posterior recupera el
// desenlace codificado.
//
// El soporte multi-valor y el cruce 100% fiel de errores estructurados se afinan si
// algún test lo exige (hoy: primer valor de retorno + {code,message} de un error
// estructurado). El error estructurado SÍ cruza fiel aquí (se lee la tabla en Lua),
// a diferencia de EvalString, donde el puente sólo expone el texto del error.
func (rt *Runtime) evalTaskStringWasm(code string) ([]string, error) {
	if rt.wasmErr != nil {
		return nil, rt.wasmErr
	}
	if _, luaErr, goErr := rt.wasm.Eval(evalTaskWrapper(code)); goErr != nil {
		return nil, goErr
	} else if luaErr != "" {
		// El wrapper no compiló (sintaxis en `code`) o el propio spawn falló.
		return nil, wasmChunkError(luaErr)
	}
	if err := rt.wasm.RunTasks(context.Background()); err != nil {
		return nil, err
	}
	outcome, luaErr, goErr := rt.wasm.Eval(evalTaskProbe)
	if goErr != nil {
		return nil, goErr
	}
	if luaErr != "" {
		return nil, errors.New(luaErr)
	}
	return parseEvalTaskOutcome(outcome)
}

// evalTaskWrapper envuelve `code` en una task que captura su desenlace en globales.
// `code` se inserta como CUERPO de una función (no como string literal: sin
// escapado ni riesgo de inyección), con saltos de línea alrededor para que un
// comentario de línea al final de `code` no se trague el `end`. `table.pack`
// preserva el recuento de retornos (multi-valor futuro). Los globales se reinician
// al principio para que una llamada previa no filtre estado.
func evalTaskWrapper(code string) string {
	return `__eval_ok = nil; __eval_n = 0; __eval_result = nil
__eval_err_code = nil; __eval_err_msg = nil; __eval_err_str = nil
nu.task.spawn(function()
  local packed = table.pack(pcall(function()
` + code + `
  end))
  __eval_ok = packed[1]
  if __eval_ok then
    __eval_n = packed.n - 1
    __eval_result = packed[2]
  else
    local e = packed[2]
    if type(e) == "table" and type(e.code) == "string" then
      __eval_err_code = e.code
      __eval_err_msg = e.message
    else
      __eval_err_str = tostring(e)
    end
  end
end)`
}

// evalTaskProbe es el chunk que lee el desenlace que dejó evalTaskWrapper y lo
// codifica en un string delimitado por 0x01 (SOH, que no aparece en códigos ni
// mensajes normales): "N" sin valores, "V\1<valor>" un valor, "E\1<code>\1<msg>"
// error estructurado, "X\1<texto>" error simple.
const evalTaskProbe = `
if __eval_ok ~= true then
  if __eval_err_code ~= nil then
    return "E\1" .. __eval_err_code .. "\1" .. tostring(__eval_err_msg or "")
  end
  return "X\1" .. tostring(__eval_err_str or "la task del CLI no produjo resultado")
end
if __eval_n == 0 then return "N" end
return "V\1" .. tostring(__eval_result)`

// parseEvalTaskOutcome traduce el string que emitió evalTaskProbe al par
// ([]string, error) que EvalTaskString devuelve.
func parseEvalTaskOutcome(outcome string) ([]string, error) {
	switch {
	case outcome == "N":
		return nil, nil // sin valores de retorno (como gopher: slice vacío)
	case strings.HasPrefix(outcome, "V\x01"):
		return []string{outcome[len("V\x01"):]}, nil
	case strings.HasPrefix(outcome, "E\x01"):
		parts := strings.SplitN(outcome[len("E\x01"):], "\x01", 2)
		se := &StructuredError{Code: parts[0], Detail: lua.LNil}
		if len(parts) == 2 {
			se.Message = parts[1]
		}
		return nil, se
	case strings.HasPrefix(outcome, "X\x01"):
		return nil, errors.New(outcome[len("X\x01"):])
	default:
		return nil, errors.New("evalTaskStringWasm: desenlace de task no reconocido: " + outcome)
	}
}

// wasmChunkError traduce el mensaje de error (string) que el backend wasm entrega
// al evaluar un chunk en el ESTADO PRINCIPAL (Instance.Eval) a un error Go. El
// puente sólo expone el error ya rendido a texto por el shim (luaL_tolstring): la
// tabla estructurada original se popea en nu_eval y no sobrevive (a diferencia de
// EvalTaskString, que la lee en Lua). Se recupera como *StructuredError si el texto
// tiene la forma "CODE: mensaje" con un code reservado (best-effort); si no, un
// error simple. El cruce 100% fiel por EvalString se afina si un test lo exige.
func wasmChunkError(msg string) error {
	if code, rest, ok := strings.Cut(msg, ": "); ok && IsReservedCode(code) {
		return &StructuredError{Code: code, Message: rest, Detail: lua.LNil}
	}
	return errors.New(msg)
}

// SetStringGlobal fija un global Lua de tipo string desde Go. Es la vía por la que
// el BINARIO (main.go) pasa sus argumentos de línea de comandos —el prompt del
// agente, el modelo, los flags— al **driver Lua** del CLI SIN interpolarlos en el
// código (lo que abriría una inyección a través de un prompt con comillas o saltos
// de línea). Igual que `EvalTaskString`/`RenderBareScreen`, es interfaz Go del
// ejecutable, NO superficie Lua sagrada (fuera de api.md): el core no acuña aquí
// ningún nombre de producto; el contrato del nombre del global lo fija el CLI con
// su driver. Toma el token para tocar el estado Lua de forma segura.
func (rt *Runtime) SetStringGlobal(name, value string) {
	rt.sched.acquire()
	defer rt.sched.release()
	rt.L.SetGlobal(name, lua.LString(value))
}
