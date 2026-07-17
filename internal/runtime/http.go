package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// `enu.http` — red (api.md §8, sesión S19). Por ahora solo `enu.http.request`: una
// petición HTTP **buffereada** (lee el body entero a string) que devuelve
// `{status, headers, body}`. Es ⏸ (sobre el puente `suspend` de S04, ADR-011):
// suelta el token, hace la petición **bloqueante** en una goroutine de fondo que
// **jamás toca Lua**, y al volver recupera el token y entrega la respuesta (o
// mapea el error) en la `deliverFn`. El streaming (`enu.http.stream`) es S20 y los
// websockets (`enu.ws`) S21; aquí no se tocan.
//
// SEMÁNTICA CLAVE (§8):
//
//   - **El status es DATO, no error.** Un 404 o un 500 devuelven
//     `{status=404, ...}` SIN lanzar —el código de estado es información que el
//     llamante decide cómo tratar (un adaptador de provider distingue 429 de 500
//     para reintentar, ADR-005)—. Solo los fallos de **transporte** lanzan:
//     conexión rechazada / DNS / reset → `ENET`; expirar `timeout_ms` → `ETIMEOUT`;
//     `url` ausente o inválida y otros usos malos → `EINVAL`.
//
//   - **TLS y proxy (G12).** `opts.tls = {ca_file?, insecure?}` añade una CA
//     corporativa por petición (`ca_file`) o desactiva la verificación
//     (`insecure`, para entornos de prueba). `opts.proxy` fija un proxy por
//     petición; sin él se respeta `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` del entorno
//     (`http.ProxyFromEnvironment`). Los defaults globales viven en `[net]` de
//     `enu.toml` (`ca_file`, `proxy`), sobreescribibles por petición.
//
// EL MODELO DEL CLIENTE (la decisión de diseño de S19, ver docs/worklog/README.md):
// **un cliente reutilizable para el caso común, uno por-petición para los casos
// con TLS/proxy a medida.** El caso común (sin `opts.tls`, sin `opts.proxy`, sin
// CA corporativa de `[net]`) reusa un único `*http.Client` cacheado en
// `httpState` —así se aprovecha el pool de conexiones keep-alive entre peticiones,
// que es lo que hace eficiente hablar repetidamente con el mismo endpoint (el
// caso del agente: muchas llamadas al mismo provider)—. Una petición que pide una
// CA distinta o `insecure` necesita su propio `tls.Config`, así que construye un
// `http.Transport`/`http.Client` efímero solo para ella. No se cachean los
// efímeros: son la excepción, y cachearlos por combinación de opciones añadiría
// complejidad sin beneficio claro en v1.

// httpDefaultTimeout es el plazo por defecto de una petición sin `timeout_ms`. No
// es "sin límite": una petición de red sin timeout puede colgar una task para
// siempre, así que se impone un techo razonable. `opts.timeout_ms` lo
// sobreescribe (incluido 0, que el contrato no contempla como "infinito": un 0
// explícito lo tratamos como inválido, ver parseo).
const httpDefaultTimeout = 30 * time.Second

// httpDefaultMaxRedirects es el presupuesto de redirects que el cliente sigue
// cuando `opts.max_redirects` no se especifica (G54, §8). Es exactamente la
// política que Go aplicaba de forma implícita (su `defaultCheckRedirect` corta a
// los 10 saltos), ahora elevada a contrato: `request`/`stream` la exponen como
// dato configurable. `0` = no seguir ninguno.
const httpDefaultMaxRedirects = 10

// httpState es el estado de sesión de `enu.http` (§8). Guarda los defaults de red
// de `[net]` de `enu.toml` (G12) y el cliente **reutilizable** del caso común,
// construido perezosamente la primera vez que una petición no necesita un cliente
// a medida. El candado protege la construcción perezosa frente a peticiones
// concurrentes (cada `request` corre su IO en una goroutine de fondo, sin token).
type httpState struct {
	caFile string // CA corporativa por defecto ([net].ca_file); "" = ninguna
	proxy  string // proxy por defecto ([net].proxy); "" = proxy del entorno

	mu        sync.Mutex
	reuseDflt *http.Client // cliente del caso común (CA del sistema + proxy default); nil hasta el primer uso
}

// newHTTPState construye el estado de `enu.http` con los defaults de `[net]` de
// `enu.toml` (G12). Lo llama `New` (runtime.go). El cliente reutilizable se crea
// perezosamente (no toda sesión hace red).
func newHTTPState(caFile, proxy string) *httpState {
	return &httpState{caFile: caFile, proxy: proxy}
}

// reqOpts son las opciones de una petición ya extraídas de la tabla Lua, en el
// estado principal y bajo el token (no se toca Lua en la goroutine de fondo). El
// IO (la petición) se construye a partir de esto fuera del token.
type reqOpts struct {
	method  string
	rawURL  string
	headers map[string]string
	body    string
	hasBody bool
	timeout time.Duration

	// TLS por petición (G12). `caFileSet`/`insecure` distinguen "no especificado"
	// de un valor: si `opts.tls` no aparece, ambos quedan en su cero y rige el
	// default de `[net]`.
	caFile    string
	caFileSet bool
	insecure  bool

	// Proxy por petición (G12). `proxySet` distingue "no especificado" (rige
	// `[net]` o el entorno) de un proxy explícito (incluido `""`, que un día podría
	// significar "sin proxy"; en v1 un proxy vacío explícito se trata como
	// no-especificado para no sorprender).
	proxy    string
	proxySet bool

	// Presupuesto de redirects (G54, §8). El número de redirecciones que el cliente
	// sigue automáticamente; agotado, la última respuesta `3xx` se entrega **como
	// dato** (no se lanza error). `0` = no seguir ninguno. El parseo lo fija en
	// `httpDefaultMaxRedirects` (10) cuando `opts.max_redirects` está ausente, así
	// que aquí siempre llega un valor válido (≥ 0).
	maxRedirects int
}

// needsCustomClient informa de si esta petición necesita un cliente a medida (un
// `tls.Config` o un proxy propios) en vez del cliente reutilizable del caso
// común. Es la bisagra del modelo "reutilizable vs por-petición": el caso común
// (sin TLS a medida, sin proxy a medida, sin CA corporativa de `[net]`) reusa el
// cliente cacheado; cualquier desviación construye uno efímero.
func (o *reqOpts) needsCustomClient(st *httpState) bool {
	if o.insecure || o.caFileSet {
		return true // TLS a medida por petición
	}
	if o.proxySet && o.proxy != "" {
		return true // proxy a medida por petición
	}
	// Defaults de `[net]`: una CA corporativa o un proxy globales también exigen un
	// cliente a medida (el reutilizable usa la raíz del sistema y el proxy del
	// entorno). Se construye una vez y se reusa como "el del caso común con
	// defaults" —pero por simplicidad lo tratamos como a medida y lo cacheamos
	// igual abajo solo cuando no hay overrides por petición.
	if st.caFile != "" || st.proxy != "" {
		return true
	}
	return false
}

// errHTTPTimeout / errHTTPTransport son los centinelas internos que `do`
// devuelve para que `raiseHTTPError` los mapee a `ETIMEOUT`/`ENET` sin reinspeccionar
// el error original (que ya se clasificó fuera del token). Un fallo de
// construcción de la petición o de la config TLS se devuelve como un error normal
// que se rinde como `EINVAL`.
var (
	errHTTPTimeout = errors.New("enu.http: timeout")
)

// httpError envuelve el error original con su código del core ya decidido, para
// que `raiseHTTPError` no tenga que reinspeccionar nada bajo el token.
type httpError struct {
	code string
	msg  string
}

func (e *httpError) Error() string { return e.msg }

// do ejecuta la petición HTTP completa **fuera del token** (no toca Lua): elige
// el cliente (reutilizable o a medida), arma la `*http.Request`, la lanza, lee el
// body entero (respuesta buffereada, §8) y devuelve `(status, headers, body, err)`.
// `err != nil` solo para fallos de transporte/timeout/uso; un status ≥ 400 NO es
// error (el status se devuelve como dato). Los errores ya vienen envueltos con su
// código del core (`httpError`) para que la `deliverFn` los lance sin reinspección.
func (st *httpState) do(o reqOpts) (int, map[string]string, string, error) {
	base, err := st.clientFor(o)
	if err != nil {
		// Fallo al preparar el cliente (p. ej. la CA no se pudo leer): uso inválido.
		return 0, nil, "", &httpError{code: CodeEINVAL, msg: err.Error()}
	}
	// Política de redirects por petición (G54): copia del cliente base con su propio
	// `CheckRedirect` (presupuesto + recorte cross-host). No se muta el cliente
	// compartido —lo reusan peticiones concurrentes—; la copia comparte el
	// `Transport` (pool keep-alive) y solo cambia la política.
	client := withRedirectPolicy(base, o)

	// Contexto con el plazo de la petición: `ETIMEOUT` cuando expira. Se usa
	// `context.WithTimeout` en vez de `client.Timeout` para distinguir limpiamente
	// el timeout del resto de fallos de transporte vía `ctx.Err()`.
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()

	var bodyReader io.Reader
	if o.hasBody {
		bodyReader = strings.NewReader(o.body)
	}
	req, err := http.NewRequestWithContext(ctx, o.method, o.rawURL, bodyReader)
	if err != nil {
		// URL inválida, método inválido: uso inválido (§1.4 EINVAL).
		return 0, nil, "", &httpError{code: CodeEINVAL, msg: "enu.http.request: " + err.Error()}
	}
	for name, value := range o.headers {
		req.Header.Set(name, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", classifyTransportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// Fallo leyendo el body (conexión cortada a media respuesta, timeout del
		// idle): transporte/timeout, no un status.
		return 0, nil, "", classifyTransportError(ctx, err)
	}

	return resp.StatusCode, flattenHeaders(resp.Header), string(body), nil
}

// classifyTransportError decide el código del core para un fallo de transporte.
// Distingue el **timeout** (`ETIMEOUT`) —el contexto expiró o el error es de tipo
// timeout— del resto de fallos de transporte (`ENET`: conexión rechazada, DNS,
// reset). Mira primero `ctx.Err()` porque, cuando el contexto expira, el error de
// `client.Do` suele envolver `context.DeadlineExceeded` pero no siempre se detecta
// por `os.IsTimeout`.
func classifyTransportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return &httpError{code: CodeETIMEOUT, msg: "enu.http.request: la petición excedió timeout_ms"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &httpError{code: CodeETIMEOUT, msg: "enu.http.request: la petición excedió timeout_ms"}
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return &httpError{code: CodeETIMEOUT, msg: "enu.http.request: la petición excedió timeout_ms"}
	}
	if errors.Is(err, errHTTPTimeout) {
		return &httpError{code: CodeETIMEOUT, msg: "enu.http.request: la petición excedió timeout_ms"}
	}
	return &httpError{code: CodeENET, msg: "enu.http.request: fallo de transporte: " + err.Error()}
}

// flattenHeaders convierte los headers de respuesta (`http.Header`, que es
// nombre→[]valor por el modelo del protocolo) a la tabla nombre→valor que el
// contrato pide (§8). **Decisión sobre valores múltiples (docs/worklog/README.md
// S19):** se **unen por ", "** —la forma canónica de combinar headers repetidos
// según RFC 7230 §3.2.2, válida para casi todos (la excepción notable, `Set-Cookie`,
// no se parte por comas; un consumidor que necesite cookies crudas usará `stream`
// en el futuro o no le sirve la API buffereada)—. Es predecible y reversible para
// el caso común (un solo valor pasa intacto), y evita exponer arrays donde el 99 %
// del código espera un string.
func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		out[name] = strings.Join(values, ", ")
	}
	return out
}

// clientFor elige el `*http.Client` para esta petición: el **reutilizable** del
// caso común (cacheado, con pool de conexiones) o uno **a medida** efímero cuando
// la petición pide TLS/proxy propios o hay defaults de `[net]` (G12). Es el
// corazón del modelo de cliente de S19.
func (st *httpState) clientFor(o reqOpts) (*http.Client, error) {
	if !o.needsCustomClient(st) {
		return st.reusableClient(), nil
	}
	return st.customClient(o)
}

// withRedirectPolicy devuelve una **copia** del cliente base con el `CheckRedirect`
// que impone la política de redirects de G54 (§8). La copia es por petición: el
// cliente base puede ser el reutilizable compartido entre peticiones concurrentes, y
// `CheckRedirect` depende de `opts` (presupuesto, cabeceras del llamante), así que no
// puede vivir en el cliente compartido —lo mutaría bajo otras peticiones—. Copiar el
// struct `http.Client` comparte el `Transport` (y con él el pool keep-alive), solo
// cambia el campo de política; es barato y sin carrera (cada `do`/`openStream` tiene
// su copia).
//
// El `CheckRedirect` de Go se llama ANTES de seguir cada redirect, con `via` = las
// peticiones ya hechas (la inicial es `via[0]`) y `req` = la petición de destino que
// Go ya armó (URL resuelta, método/cuerpo ajustados según el status). Dos reglas:
//
//   - **Presupuesto (G54).** Antes de seguir el k-ésimo redirect, `len(via) == k`
//     (la inicial más los k-1 ya seguidos). Se sigue mientras `k <= max_redirects`;
//     agotado, se devuelve `http.ErrUseLastResponse`, que hace que `client.Do`
//     retorne la última respuesta `3xx` **con nil de error** —justo la semántica
//     "el status es dato" que pide §8: quien puso `0` observa el `302` a mano—. Con
//     `max_redirects = 0` el primer redirect ya tiene `len(via) == 1 > 0`: no se
//     sigue ninguno.
//
//   - **Recorte cross-host (G54).** En cada salto cross-host se borran de la petición
//     de destino TODAS las cabeceras que el llamante puso en `opts.headers` (sin
//     lista blanca): un host distinto es un interlocutor distinto que no hereda la
//     credencial custom (`x-api-key`…) que el llamante dio al primero. El recorte no
//     se restaura aunque un salto posterior regrese al host inicial —la cadena ya
//     pasó por un tercero—: por eso se mira TODA la cadena (`crossHostChain`), no solo
//     el salto actual, y una vez cruzada la frontera se recorta en todos los saltos
//     que queden.
func withRedirectPolicy(base *http.Client, o reqOpts) *http.Client {
	// Claves del llamante, canonicalizadas (Go guarda las cabeceras con la clave
	// canónica; `opts.headers` puede traerlas en cualquier caja).
	var callerKeys map[string]struct{}
	if len(o.headers) > 0 {
		callerKeys = make(map[string]struct{}, len(o.headers))
		for k := range o.headers {
			callerKeys[http.CanonicalHeaderKey(k)] = struct{}{}
		}
	}

	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > o.maxRedirects {
			// Presupuesto agotado: entrega la última 3xx como dato (no error). Incluye
			// el caso `max_redirects == 0` (el primer redirect ya excede).
			return http.ErrUseLastResponse
		}
		if callerKeys != nil && crossHostChain(via[0].URL, req, via) {
			for k := range callerKeys {
				req.Header.Del(k)
			}
		}
		return nil
	}
	return &c
}

// crossHostChain informa de si la cadena de redirects ha cruzado —en el salto que se
// va a seguir (`req`) o en cualquier salto ya seguido (`via[1:]`)— la frontera del
// host inicial (`initial` == `via[0].URL`). Mirar toda la cadena, y no solo el salto
// actual, implementa la regla de G54 de que el recorte de cabeceras **no se restaura**
// aunque un salto posterior regrese al host inicial: basta un tercero en el camino
// para envenenar la confianza del resto de la cadena.
func crossHostChain(initial *url.URL, req *http.Request, via []*http.Request) bool {
	if isCrossHost(initial, req.URL) {
		return true
	}
	for _, v := range via[1:] { // via[0] es la URL inicial: same-host por definición
		if isCrossHost(initial, v.URL) {
			return true
		}
	}
	return false
}

// isCrossHost decide si `next` es cross-host respecto de la URL inicial según la regla
// exacta de G54 (§8), en dos cláusulas independientes:
//
//   - el host —nombre **y** puerto— difiere, **o**
//   - el esquema degrada de `https` a `http` **aunque el host se conserve** (la
//     cabecera viajaría en claro por un canal interceptable).
//
// La segunda cláusula solo tiene sentido si la identidad de host de la primera IGNORA
// el esquema: por eso `sameHostPort` normaliza el puerto default (`http`/`ws`→80,
// `https`/`wss`→443) a "" antes de comparar. Así `http://a` y `http://a:80` son el
// mismo host (no hay recorte espurio), y —clave— `http://a` y `https://a` también lo
// son: un **upgrade** `http`→`https` al mismo host es un redirect benigno y frecuente
// (un sitio que fuerza TLS) que NO debe perder las cabeceras del llamante; solo la
// **degradación** explícita `https`→`http` (cláusula 2) las recorta.
func isCrossHost(initial, next *url.URL) bool {
	if !sameHostPort(initial, next) {
		return true
	}
	if initial.Scheme == "https" && next.Scheme == "http" {
		return true
	}
	return false
}

// sameHostPort compara dos URLs por nombre de host y puerto, con el puerto default del
// esquema normalizado a "" (ver `normalizedPort`). Es la identidad de host, sin
// esquema, sobre la que se apoya la regla cross-host de G54.
func sameHostPort(a, b *url.URL) bool {
	return a.Hostname() == b.Hostname() && normalizedPort(a) == normalizedPort(b)
}

// normalizedPort devuelve el puerto de una URL con el default del esquema plegado a ""
// (un puerto ausente, o el `80` de `http`/`ws`, o el `443` de `https`/`wss`). De ese
// modo un puerto explícito que coincide con el default no cuenta como "puerto distinto"
// y el plegado a "" hace que `http` (80) y `https` (443) por defecto compartan
// identidad de host —dejando la distinción de esquema a la cláusula de degradación—.
func normalizedPort(u *url.URL) string {
	p := u.Port()
	if p == "" {
		return ""
	}
	switch u.Scheme {
	case "http", "ws":
		if p == "80" {
			return ""
		}
	case "https", "wss":
		if p == "443" {
			return ""
		}
	}
	return p
}

// reusableClient devuelve el cliente del caso común, creándolo perezosamente la
// primera vez. Usa la raíz de confianza del sistema y el proxy del entorno
// (`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`, vía `http.ProxyFromEnvironment`). Se
// reusa entre peticiones para aprovechar keep-alive; el candado serializa su
// construcción frente a peticiones concurrentes. Sin `client.Timeout`: el plazo
// va por el `context` de cada petición (`do`).
func (st *httpState) reusableClient() *http.Client {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.reuseDflt == nil {
		tr := newBaseTransport()
		tr.Proxy = http.ProxyFromEnvironment
		st.reuseDflt = &http.Client{Transport: tr}
	}
	return st.reuseDflt
}

// customClient construye un cliente efímero para una petición con TLS/proxy a
// medida (G12). No se cachea: es la excepción, no el camino caliente. Resuelve la
// CA (la de la petición si la dio, si no la de `[net]`), el flag `insecure` y el
// proxy (el de la petición, el de `[net]`, o el del entorno).
func (st *httpState) customClient(o reqOpts) (*http.Client, error) {
	tlsCfg := &tls.Config{}

	// `insecure`: desactiva la verificación del certificado del servidor. Solo para
	// entornos de prueba; el contrato lo expone a sabiendas (G12).
	if o.insecure {
		tlsCfg.InsecureSkipVerify = true
	}

	// CA corporativa: la de la petición tiene precedencia sobre la de `[net]`.
	caFile := st.caFile
	if o.caFileSet {
		caFile = o.caFile
	}
	if caFile != "" && !o.insecure {
		pool, err := loadCAPool(caFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.RootCAs = pool
	}

	tr := newBaseTransport()
	tr.TLSClientConfig = tlsCfg

	// Proxy: el de la petición tiene precedencia sobre el de `[net]`; sin ninguno,
	// el del entorno (comportamiento por defecto).
	proxyURL := st.proxy
	if o.proxySet && o.proxy != "" {
		proxyURL = o.proxy
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, errors.New("enu.http.request: proxy inválido: " + err.Error())
		}
		tr.Proxy = http.ProxyURL(u)
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}

	return &http.Client{Transport: tr}, nil
}

// loadCAPool lee un fichero PEM con una o más CAs y devuelve un pool que las
// confía **además** de las del sistema (parte de la raíz del sistema y le añade la
// corporativa, en vez de sustituirla: lo que pide G12 es "añadir una CA", no
// reemplazar la confianza). Un fichero ilegible o sin certificados válidos es un
// error de uso (`EINVAL` arriba).
func loadCAPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, errors.New("enu.http.request: no se pudo leer ca_file: " + err.Error())
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("enu.http.request: ca_file no contiene certificados PEM válidos: " + caFile)
	}
	return pool, nil
}

// newBaseTransport crea un `http.Transport` con los timeouts de marcado y de
// conexión inactiva razonables, sin proxy ni TLS fijados (cada llamante los pone).
// Es la base común del cliente reutilizable y de los a medida, para que todos
// tengan el mismo comportamiento de bajo nivel (keep-alive, límites de conexiones).
func newBaseTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// close libera los recursos del cliente reutilizable (cierra las conexiones
// inactivas del pool). Lo llama `Runtime.Close`. Best-effort: un cliente que no
// llegó a crearse no tiene nada que cerrar.
func (st *httpState) close() {
	st.mu.Lock()
	c := st.reuseDflt
	st.mu.Unlock()
	if c != nil {
		c.CloseIdleConnections()
	}
}
