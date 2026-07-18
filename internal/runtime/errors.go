package runtime

// Errores estructurados del core (api.md §1.4). Las primitivas Go **lanzan**
// (dentro del backend wasm, vía el error estructurado que cruza la frontera) una
// tabla `{ code, message, detail? }` que el código Lua captura con `pcall`.
// Frente al estilo `res, err`, los errores estructurados componen mejor a través
// de capas de extensiones y nunca se ignoran en silencio.
//
// Este fichero declara los códigos reservados y la cara Go del error estructurado
// (`StructuredError`) que devuelven `EvalString`/`EvalTaskString` y las interfaces
// Go del binario (driver, loader, embed). El invariante que blinda S02 (inventario
// 🔒): un código reservado **nunca se traga ni se reescribe** al cruzar el puente.

// Códigos de error reservados v1 (§1.4). El core los emite y nadie más debe
// acuñarlos: las extensiones crean los suyos con la misma forma pero fuera de
// esta lista (p. ej. `EPROVIDER`). `ECANCELED` y `EBUDGET` nombran además los
// abortos *no capturables* de §1.3 (cancelación y watchdog).
const (
	CodeENOENT    = "ENOENT"    // recurso inexistente
	CodeEEXIST    = "EEXIST"    // ya existe (p. ej. write{exclusive}, G17)
	CodeEACCES    = "EACCES"    // permiso denegado
	CodeEIO       = "EIO"       // fallo de IO / backpressure desbordado
	CodeEHTTP     = "EHTTP"     // error de protocolo HTTP
	CodeENET      = "ENET"      // fallo de transporte de red
	CodeETIMEOUT  = "ETIMEOUT"  // expiró un plazo
	CodeECANCELED = "ECANCELED" // task cancelada (solo observable, §1.3)
	CodeEBUDGET   = "EBUDGET"   // presupuesto de slice excedido (watchdog, §1.3)
	CodeEINVAL    = "EINVAL"    // argumento o uso inválido
	CodeECLOSED   = "ECLOSED"   // handle cerrado
)

// reservedCodes es el conjunto de códigos que el core se reserva (§1.4, §17).
// Sirve para auditar que el puente respeta el invariante 🔒 de S02 y para que
// futuras primitivas comprueben que no acuñan uno ajeno por error.
var reservedCodes = map[string]bool{
	CodeENOENT:    true,
	CodeEEXIST:    true,
	CodeEACCES:    true,
	CodeEIO:       true,
	CodeEHTTP:     true,
	CodeENET:      true,
	CodeETIMEOUT:  true,
	CodeECANCELED: true,
	CodeEBUDGET:   true,
	CodeEINVAL:    true,
	CodeECLOSED:   true,
}

// IsReservedCode informa de si `code` es uno de los códigos reservados del core
// (§1.4). Las extensiones lo usan para no pisar el espacio del core al acuñar
// los suyos.
func IsReservedCode(code string) bool {
	return reservedCodes[code]
}

// StructuredError es la cara Go de un error estructurado (§1.4) que ha cruzado
// la frontera Lua→Go (p. ej. al evaluar un chunk con `EvalString`). Conserva el
// `code` y el `message` ya copiados a strings Go. El backend wasm reconstruye esta
// forma leyendo la tabla de error en Lua y transportándola por el protocolo de
// separadores 0x01 (tanto EvalTaskString como EvalString, A-40): un error de string
// de usuario NO se hace pasar por estructurado reinterpretando su texto. Las
// interfaces Go del binario la construyen directa.
type StructuredError struct {
	Code    string
	Message string
}

// Error implementa la interfaz `error` de Go. No inventa formato: expone código
// y mensaje, que es lo que un test o un log necesitan.
func (e *StructuredError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}
