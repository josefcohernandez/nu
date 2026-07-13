package runtime

// Catálogo de nu.ws sobre el backend wasm (M13b, §11). Contraparte de ws.go: una
// sola primitiva de entrada, nu.ws.connect(url, opts?) -> Ws, y el handle Ws con
// send/recv/close. Reusa TODA la lógica de red/protocolo VM-agnóstica de ws.go
// —el dial (`rt.dialWs`), la escritura (`luaWs.send`), la lectura (`luaWs.recv`)
// y el cierre idempotente (`luaWs.close`)—; sólo cambia el marshaling de la
// frontera (el wire ya lo resuelve) y la forma de despachar los métodos ⏸.
//
// EL RETO DEL connect ⏸ + HANDLE. `nu.ws.connect` es a la vez BLOQUEANTE (el
// handshake) y CREADOR DE HANDLE. Lo bloqueante exige una primitiva suspendente
// (`RegisterSuspending`), que corre en la goroutine de fondo del scheduler; crear
// el handle exige `inst.AllocHandle`. El contrato de RegisterSuspending desaconseja
// tocar el *Instance desde la goroutine de fondo, pero AllocHandle sólo toca la
// `handleTable` (protegida por su propio mutex — el diseño de handle.go lo bendice
// explícitamente: "un HostFn suspendente podría liberar/asignar un handle"), NO la
// VM de Lua. Así `ws._connect` dialea fuera de la VM y, ya conectado, registra el
// handle y lo devuelve por el wire (W_HANDLE, ida y vuelta). Es la única vía sin un
// mecanismo nuevo, y respeta que `connect` ceda al scheduler durante el handshake.
//
// LOS MÉTODOS ⏸. La metatable genérica de handles despacha todo por `__hcall`
// (síncrono). send/recv son ⏸ (IO bloqueante), así que se enrutan por `__hcall_s`
// (que cede al scheduler y corre en la goroutine de fondo); close es síncrono
// (cerrar es inmediato, como en gopher). El wrapper Lua (AddPreludio) envuelve el
// handle que devuelve `ws._connect` y le añade send/recv/close apuntando al
// despacho correcto —mismo estilo que vmwasm_re.go, aquí enrutando por __hcall_s—.
//
// CICLO DE VIDA. `close` NO libera el handle (a diferencia de Region:destroy, M11):
// `Ws:close()` es idempotente en el contrato (§11), y liberar el handle haría que
// un segundo close diera ECLOSED al resolverlo. `luaWs.close` ya es idempotente
// (closeOnce) y desregistra del rastreo del scheduler; el handle sobrevive como en
// gopher (donde el userdata muere por GC). El rastreo para `Runtime.Close` sólo se
// activa si hay scheduler (M13d lo cablea); en M13b `rt.sched` puede ser nil.

import (
	"errors"
	"time"

	"github.com/dbareagimeno/nu/internal/vmwasm"
)

func registerWsWasm(p *vmwasm.Pool, rt *Runtime) {
	// nu.ws._connect(url, opts?) -> Ws ⏸ — el wrapper nu.ws.connect lo envuelve. El
	// handshake corre en la goroutine de fondo (cede al scheduler); ya conectado se
	// registra el handle Ws. url/opts malos → EINVAL; puerto cerrado/DNS → ENET;
	// handshake que expira timeout_ms → ETIMEOUT (los tres, del núcleo de ws.go).
	p.RegisterSuspending("ws._connect", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		url, opts, err := parseWsOptsWasm(arg(args, 0), arg(args, 1))
		if err != nil {
			return nil, err
		}
		w, derr := rt.dialWs(url, opts)
		if derr != nil {
			// dialWs envuelve el fallo del handshake como *httpError (ENET/ETIMEOUT) o
			// EINVAL; httpErrWasm lo traduce al error estructurado de la frontera.
			return nil, httpErrWasm(derr)
		}
		// El rastreo para el apagado ordenado (Runtime.Close → stopAllWs) sólo si hay
		// scheduler; en M13b el rt de los tests es mínimo (sin scheduler).
		if rt.sched != nil {
			rt.sched.trackWs(w)
		}
		return []any{inst.AllocHandle("Ws", w)}, nil
	})

	// Ws:send(data, opts?) ⏸ — envía data; con `opts.binary` true sale como frame
	// binario, sin él como frame de texto (G52/A-38). Tras close → ECLOSED; un fallo
	// de transporte → ENET. El Write bloqueante corre en la goroutine de fondo.
	p.RegisterHandleMethod("Ws", "send", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		w := val.(*luaWs)
		binary := false
		if opts, ok := arg(args, 1).(map[string]any); ok {
			binary, _ = opts["binary"].(bool)
		}
		if err := w.send([]byte(argString(args, 0)), binary); err != nil {
			return nil, wsErrWasm(err, "Ws:send")
		}
		return nil, nil
	})

	// Ws:recv() -> data: string?, binary: boolean ⏸ — el siguiente mensaje y el tipo
	// de su frame (binary true si era binario, false si texto — G52/A-38), o nil
	// cuando la conexión se cierra (ordenadamente o por Ws:close; el segundo valor
	// queda nil). Un fallo de transporte real → ENET.
	p.RegisterHandleMethod("Ws", "recv", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		w := val.(*luaWs)
		data, binary, closed, err := w.recv()
		if err != nil {
			return nil, wsErrWasm(err, "Ws:recv")
		}
		if closed {
			return []any{nil}, nil // conexión cerrada: fin de stream → nil (no lanza)
		}
		return []any{string(data), binary}, nil
	})

	// Ws:close() — cierra la conexión. Síncrono e idempotente (closeOnce). No libera
	// el handle (ver la nota de arriba: la idempotencia lo exige).
	p.RegisterHandleMethod("Ws", "close", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		val.(*luaWs).close()
		return nil, nil
	})

	// Wrapper Lua: nu.ws.connect envuelve el handle de ws._connect y le añade
	// send/recv (por __hcall_s, ⏸) y close (por __hcall, síncrono). Mismo patrón que
	// nu.re.compile (vmwasm_re.go), aquí enrutando los métodos bloqueantes al
	// despacho suspendente.
	p.AddPreludioW(`
nu.ws = nu.ws or {}
function nu.ws.connect(url, opts)
  local ws = nu.ws._connect(url, opts)   -- ⏸: handle {__id} tras el handshake
  ws.send  = function(self, data, opts) return __hcall_s(self.__id, "send", data, opts) end
  ws.recv  = function(self)             return __hcall_s(self.__id, "recv") end
  ws.close = function(self)       return __hcall(self.__id, "close") end
  return ws
end`, "ws._connect")
}

// parseWsOptsWasm extrae (url, wsOpts) del wire de nu.ws.connect. Mismo contrato
// que parseWsOpts (§11) del backend gopher: url obligatoria (string no vacío),
// opts?.headers (string→string) y opts?.timeout_ms (positivo); un uso malo →
// EINVAL. El equivalente de parseReqOptsWasm (vmwasm_http.go) para ws.
func parseWsOptsWasm(urlArg, optsArg any) (string, wsOpts, error) {
	o := wsOpts{timeout: httpDefaultTimeout}

	url, ok := urlArg.(string)
	if !ok || url == "" {
		return "", o, einvalWs("url es obligatoria (string no vacío)")
	}

	if optsArg != nil {
		opts, ok := optsArg.(map[string]any)
		if !ok {
			return "", o, einvalWs("opts debe ser una tabla")
		}
		if hv, present := opts["headers"]; present && hv != nil {
			h, ok := hv.(map[string]any)
			if !ok {
				return "", o, einvalWs("opts.headers debe ser una tabla")
			}
			o.headers = make(map[string]string, len(h))
			for k, val := range h {
				s, ok := val.(string)
				if !ok {
					return "", o, einvalWs("opts.headers debe ser una tabla de string→string")
				}
				o.headers[k] = s
			}
		}
		if tv, present := opts["timeout_ms"]; present && tv != nil {
			tm, ok := httpNum(tv)
			if !ok {
				return "", o, einvalWs("opts.timeout_ms debe ser un número")
			}
			if tm <= 0 {
				return "", o, einvalWs("opts.timeout_ms debe ser positivo")
			}
			o.timeout = time.Duration(tm) * time.Millisecond
		}
	}

	return url, o, nil
}

func einvalWs(msg string) error {
	return &vmwasm.StructuredError{Code: CodeEINVAL, Message: "nu.ws.connect: " + msg}
}

// wsErrWasm traduce el error de send/recv (del núcleo de ws.go) al error
// estructurado de la frontera: un cierre (errWsClosed → ECLOSED) o un *httpError
// ya clasificado (ENET/ETIMEOUT). Mismo mapeo que raiseWsError del backend gopher.
func wsErrWasm(err error, fn string) error {
	if errors.Is(err, errWsClosed) {
		return &vmwasm.StructuredError{Code: CodeECLOSED, Message: fn + ": la conexión fue cerrada"}
	}
	return httpErrWasm(err)
}
