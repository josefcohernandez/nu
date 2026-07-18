package runtime

// Catálogo de enu.http sobre el backend wasm (M13b, §8). Contraparte de http.go
// (enu.http.request) y stream.go (enu.http.stream): la petición de un tiro
// enu.http.request(opts) -> {status, headers, body} (⏸) y el streaming
// enu.http.stream(opts) -> Stream (⏸). Ambos reusan el estado del Runtime (rt.http)
// y su núcleo VM-agnóstico —`do` para request, `openStream`/`httpStream` para
// stream—, idéntico al backend gopher; el IO corre en la goroutine de fondo del
// scheduler.
//
// EL STREAM Y SUS ITERADORES (§8). `enu.http.stream` es a la vez BLOQUEANTE (suspende
// hasta las cabeceras) y CREADOR DE HANDLE, como `enu.ws.connect`: se implementa con
// una primitiva suspendente (`http._stream`) que abre el stream fuera de la VM y, ya
// recibidas las cabeceras, registra el handle Stream y lo devuelve por el wire junto
// a los campos `status`/`headers`. El handle lleva `next_chunk`/`next_event` (⏸, IO
// bloqueante: se despachan por `__hcall_s`, que cede al scheduler) y `close`
// (síncrono, por `__hcall`). El wrapper Lua `enu.http.stream` (AddPreludio) envuelve
// el handle, le cuelga los campos de cabecera y expone `chunks()`/`events()` como los
// ITERADORES que Lua consume con `for x in st:chunks() do` —cada `next` es una llamada
// suspendente a su método de handle—.
//
// UNA SOLA SUSPENSIÓN POR EVENTO (frente a gopher). El parser SSE es incremental: un
// evento puede llegar partido entre varios trozos de red. El backend gopher
// (`streamEvents`) suspende una vez por trozo y re-entra al bucle; aquí el método
// `next_event` corre ENTERO en la goroutine de fondo de un único `__hcall_s`, así que
// consume trozos crudos (`nextChunk`, que bloquea sin tocar Lua) en bucle hasta cerrar
// un evento o agotar el body, y sólo el evento ya parseado cruza a Lua. El parser
// (`st.sse`) lo toca sólo el consumidor —una task, secuencial— como en gopher: sin
// candado.

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/dbareagimeno/enu/internal/vmwasm"
)

func registerHTTPWasm(p *vmwasm.Pool, rt *Runtime) {
	// enu.http.request(opts) -> {status, headers, body} ⏸
	p.RegisterSuspending("http.request", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		o, err := parseReqOptsWasm(arg(args, 0))
		if err != nil {
			return nil, err
		}
		status, headers, body, derr := rt.http.do(o)
		if derr != nil {
			return nil, httpErrWasm(derr)
		}
		h := make(map[string]any, len(headers))
		for k, v := range headers {
			h[k] = v
		}
		return []any{map[string]any{
			"status":  int64(status),
			"headers": h,
			"body":    body,
		}}, nil
	})

	registerHTTPStreamWasm(p, rt)
}

// registerHTTPStreamWasm añade enu.http.stream (el handle Stream) al catálogo wasm
// (§8). Se separa de registerHTTPWasm por volumen, no por semántica: comparte el
// estado (rt.http) y el mapeo de errores con request.
func registerHTTPStreamWasm(p *vmwasm.Pool, rt *Runtime) {
	// enu.http._stream(opts) -> (Stream, status, headers) ⏸ — el wrapper enu.http.stream
	// lo envuelve. Reusa el núcleo VM-agnóstico de stream.go: el parseo de opts
	// (parseReqOptsWasm, el mismo que request —en gopher `parseReqOpts` también lo
	// comparten, de ahí que sus errores digan "enu.http.request:") más idle_timeout_ms,
	// y `openStream`, que hace la petición SOLO hasta las cabeceras y arranca la
	// goroutine de fondo que lee el body a la cola acotada. Un status >= 400 NO es
	// error (se devuelve el Stream con su status); sólo transporte/timeout/uso malo
	// lanza. El handle se devuelve como un bare Handle (→ tabla {__id} con la metatable
	// de handles) y los campos de cabecera como valores extra, igual que ws._connect.
	p.RegisterSuspending("http._stream", func(inst *vmwasm.Instance, args []any) ([]any, error) {
		o, err := parseReqOptsWasm(arg(args, 0))
		if err != nil {
			return nil, err
		}
		idle, err := parseIdleTimeoutWasm(arg(args, 0))
		if err != nil {
			return nil, err
		}
		st, derr := rt.http.openStream(rt.sched, o, idle)
		if derr != nil {
			return nil, httpErrWasm(derr)
		}
		// Rastreo para el apagado ordenado (Runtime.Close → stopAllStreams) sólo si hay
		// scheduler; en M13b el rt de los tests es mínimo (sin scheduler), y
		// httpStream.close guarda el nil (como luaWs.close).
		if rt.sched != nil {
			rt.sched.trackStream(st)
		}
		h := make(map[string]any, len(st.headers))
		for k, v := range st.headers {
			h[k] = v
		}
		return []any{inst.AllocHandle("Stream", st), int64(st.status), h}, nil
	})

	// Stream:next_chunk() -> string? ⏸ — el siguiente trozo crudo del body, o nil al
	// terminar (fin del stream). Backpressure desbordado → EIO, body mudo →
	// ETIMEOUT, cierre → ECLOSED (todos ya clasificados por el núcleo de stream.go).
	// El wrapper Lua lo expone como el iterador que devuelve st:chunks().
	p.RegisterHandleMethod("Stream", "next_chunk", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		st := val.(*httpStream)
		chunk, eof, rerr := st.nextChunk()
		if rerr != nil {
			return nil, streamErrWasm(rerr)
		}
		if eof {
			return []any{nil}, nil // fin del body → nil (no lanza)
		}
		return []any{string(chunk)}, nil
	})

	// Stream:next_event() -> {data, event?, id?}? ⏸ — el siguiente evento SSE
	// completo, o nil al terminar. Consume trozos crudos (nextChunk) EN BUCLE hasta
	// cerrar un evento o agotar el body; todo en la goroutine de fondo de un único
	// __hcall_s (ver la nota de cabecera). El último evento sin línea en blanco final
	// se despacha en EOF (semántica SSE); si tras EOF no queda nada, fin → nil.
	p.RegisterHandleMethod("Stream", "next_event", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		st := val.(*httpStream)
		for {
			if ev, has := st.sse.next(); has {
				return []any{sseEventToWasm(ev)}, nil
			}
			chunk, eof, rerr := st.nextChunk()
			if rerr != nil {
				return nil, streamErrWasm(rerr)
			}
			if eof {
				if ev, has := st.sse.flush(); has {
					return []any{sseEventToWasm(ev)}, nil
				}
				return []any{nil}, nil
			}
			st.sse.feed(chunk)
		}
	})

	// Stream:close() — aborta la conexión y libera. Síncrono e idempotente
	// (closeOnce). NO libera el handle: Stream:close es idempotente en el contrato
	// (§8) y un segundo close resolvería el mismo handle sin ECLOSED (igual que Ws).
	p.RegisterHandleMethod("Stream", "close", func(inst *vmwasm.Instance, val any, args []any) ([]any, error) {
		val.(*httpStream).close()
		return nil, nil
	})

	// Wrapper Lua: enu.http.stream envuelve el handle de http._stream con los campos
	// status/headers (§8) y los métodos chunks()/events() —cada uno devuelve el
	// ITERADOR que Lua consume con `for x in st:chunks() do`, y cada `next` cede al
	// scheduler por __hcall_s (⏸)— y close (por __hcall, síncrono). Mismo patrón que
	// enu.ws.connect (vmwasm_ws.go): el handle ya trae la metatable de handles; aquí
	// sólo se le cuelgan los campos de cabecera y los métodos.
	p.AddPreludioW(`
enu.http = enu.http or {}
function enu.http.stream(opts)
  local st, status, headers = enu.http._stream(opts)  -- ⏸: handle {__id} tras las cabeceras
  st.status  = status
  st.headers = headers
  st.chunks  = function(self) return function() return __hcall_s(self.__id, "next_chunk") end end
  st.events  = function(self) return function() return __hcall_s(self.__id, "next_event") end end
  st.close   = function(self) return __hcall(self.__id, "close") end
  return st
end`, "http._stream")
}

// parseIdleTimeoutWasm extrae opts.idle_timeout_ms del mapa `opts` que cruzó el wire
// (§8). Mismo contrato que parseIdleTimeout del backend gopher: ausente → 0 (sin
// idle-timeout); presente debe ser un número positivo, si no EINVAL —con el prefijo
// "enu.http.stream:" (el idle-timeout es específico de stream, a diferencia del resto
// de opts, que comparte con request).
func parseIdleTimeoutWasm(v any) (time.Duration, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return 0, nil // parseReqOptsWasm ya validó que opts es un mapa; defensivo
	}
	tv, present := m["idle_timeout_ms"]
	if !present || tv == nil {
		return 0, nil
	}
	n, ok := httpNum(tv)
	if !ok {
		return 0, &vmwasm.StructuredError{Code: CodeEINVAL, Message: "enu.http.stream: opts.idle_timeout_ms debe ser un número"}
	}
	if n <= 0 {
		return 0, &vmwasm.StructuredError{Code: CodeEINVAL, Message: "enu.http.stream: opts.idle_timeout_ms debe ser positivo"}
	}
	return time.Duration(n) * time.Millisecond, nil
}

// sseEventToWasm traduce un evento SSE ya parseado (el mismo sseEvent que produce el
// parser 🔒 de stream.go) al mapa que cruza el wire hacia Lua (§8): `data` siempre;
// `event`/`id` sólo si el evento los traía (los flags distinguen "ausente" de
// "presente pero vacío"). Espejo de pushEvent del backend gopher.
func sseEventToWasm(ev sseEvent) map[string]any {
	m := map[string]any{"data": ev.data}
	if ev.hasEvent {
		m["event"] = ev.event
	}
	if ev.hasID {
		m["id"] = ev.id
	}
	return m
}

// streamErrWasm traduce el error de un `next` de stream (del núcleo de stream.go) al
// error estructurado de la frontera: un cierre (errStreamClosed → ECLOSED) o un
// *httpError ya clasificado (EIO/ETIMEOUT/ENET). Mismo mapeo que raiseStreamError del
// backend gopher.
func streamErrWasm(err error) error {
	if errors.Is(err, errStreamClosed) {
		return &vmwasm.StructuredError{Code: CodeECLOSED, Message: "enu.http.stream: el stream fue cerrado"}
	}
	return httpErrWasm(err)
}

// parseReqOptsWasm construye un reqOpts desde el mapa `opts` que cruzó el wire.
// Mismo contrato que parseReqOpts (§8): url obligatoria, method/body/headers/
// timeout_ms/tls/proxy opcionales; un valor inválido → EINVAL.
func parseReqOptsWasm(v any) (reqOpts, error) {
	o := reqOpts{method: http.MethodGet, timeout: httpDefaultTimeout, maxRedirects: httpDefaultMaxRedirects}
	m, ok := v.(map[string]any)
	if !ok {
		return o, einvalHTTP("opts debe ser una tabla")
	}
	url, _ := m["url"].(string)
	if url == "" {
		return o, einvalHTTP("opts.url es obligatoria (string no vacío)")
	}
	o.rawURL = url
	if mth, ok := m["method"].(string); ok && mth != "" {
		o.method = strings.ToUpper(mth)
	}
	if b, ok := m["body"].(string); ok {
		o.body = b
		o.hasBody = true
	}
	// timeout_ms: número positivo.
	if tv, present := m["timeout_ms"]; present && tv != nil {
		tm, ok := httpNum(tv)
		if !ok {
			return o, einvalHTTP("opts.timeout_ms debe ser un número")
		}
		if tm <= 0 {
			return o, einvalHTTP("opts.timeout_ms debe ser positivo")
		}
		o.timeout = time.Duration(tm) * time.Millisecond
	}
	// headers: tabla string→string.
	if hv, present := m["headers"]; present && hv != nil {
		h, ok := hv.(map[string]any)
		if !ok {
			return o, einvalHTTP("opts.headers debe ser una tabla")
		}
		o.headers = make(map[string]string, len(h))
		for k, val := range h {
			s, ok := val.(string)
			if !ok {
				return o, einvalHTTP("opts.headers debe ser una tabla de string→string")
			}
			o.headers[k] = s
		}
	}
	// tls por petición (G12): {ca_file?, insecure?}.
	if tv, present := m["tls"]; present && tv != nil {
		tls, ok := tv.(map[string]any)
		if !ok {
			return o, einvalHTTP("opts.tls debe ser una tabla")
		}
		if ca, ok := tls["ca_file"].(string); ok {
			o.caFile = ca
			o.caFileSet = true
		}
		o.insecure, _ = tls["insecure"].(bool)
	}
	// proxy por petición (G12).
	if px, ok := m["proxy"].(string); ok {
		o.proxy = px
		o.proxySet = true
	}
	// max_redirects (G54): entero no negativo. Ausente → default 10 (ya fijado en el
	// inicializador); `0` = no seguir ninguno. Un fraccionario o un negativo es un uso
	// inválido —el presupuesto es un número de saltos, no cabe media redirección—.
	if rv, present := m["max_redirects"]; present && rv != nil {
		n, ok := httpNum(rv)
		if !ok {
			return o, einvalHTTP("opts.max_redirects debe ser un número")
		}
		if n < 0 || n != math.Trunc(n) {
			return o, einvalHTTP("opts.max_redirects debe ser un entero no negativo")
		}
		o.maxRedirects = int(n)
	}
	return o, nil
}

func httpNum(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func einvalHTTP(msg string) error {
	return &vmwasm.StructuredError{Code: CodeEINVAL, Message: "enu.http.request: " + msg}
}

// httpErrWasm traduce el error de rt.http.do (un *httpError con code/msg) al error
// estructurado de la frontera. Mismo mapeo que raiseHTTPError.
func httpErrWasm(err error) error {
	var he *httpError
	if errors.As(err, &he) {
		return &vmwasm.StructuredError{Code: he.code, Message: he.msg}
	}
	return &vmwasm.StructuredError{Code: CodeENET, Message: err.Error()}
}
