package vmwasm

// Codec de wire de la frontera VM (migracion-vm.md M05, categoría C1 del censo).
// Cruza valores entre Go y el estado Lua-en-wasm por el buffer compartido, con
// dos requisitos que descartan JSON:
//
//   - **byte-seguro (G11)**: los strings cruzan TAL CUAL, sin re-codificar. Un
//     tool_result con bytes no-UTF-8 (salida de un `grep` binario) debe viajar
//     intacto; JSON lo rompería. Aquí un string es longitud + bytes crudos.
//   - **distingue null (G11)**: el sentinel `enu.json.NULL` cruza como su propio
//     tag, sin colisionar con nil (que borraría una clave de tabla).
//
// Es un TLV compacto (tag + payload), simétrico con el codec Lua del preludio
// (host.go). Las longitudes y cuentas son u32 little-endian de ancho FIJO, para
// que el lado Lua las lea con `string.unpack("<I4")` (5.4) sin implementar
// varint. No pretende ser un formato público: es interno a la frontera.

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Tags del wire. Estables mientras exista el backend wasm.
const (
	wNil    byte = 0 // nil
	wFalse  byte = 1 // boolean false
	wTrue   byte = 2 // boolean true
	wInt    byte = 3 // integer (Lua 5.4 tiene subtipo entero): int64 LE
	wFloat  byte = 4 // number: float64 LE
	wStr    byte = 5 // string: u32 len + bytes CRUDOS (byte-seguro, G11)
	wArray  byte = 6 // secuencia 1..n: u32 count + n valores
	wMap    byte = 7 // tabla clave→valor: u32 count + n pares (clave, valor)
	wHandle byte = 8 // userdata opaco (C5, M10): u32 índice
	wNull   byte = 9 // sentinel enu.json.NULL (G11)
)

// Handle es un userdata opaco cruzando la frontera como índice (C5). Su despacho
// de métodos lo cablea M10; en M05 sólo viaja de ida y vuelta sin perder identidad.
type Handle uint32

// Null es el valor Go que representa el sentinel enu.json.NULL en el wire.
type Null struct{}

// NullValue es la instancia única de Null (comparable por igualdad).
var NullValue = Null{}

// encodeValue serializa un valor Go al wire. Tipos admitidos: nil, bool, int/
// int64, float64, string, []any (array), map[string]any (map), Handle, Null.
// Un tipo no admitido es un error de programación del kernel (no del usuario).
func encodeValue(buf []byte, v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return append(buf, wNil), nil
	case bool:
		if x {
			return append(buf, wTrue), nil
		}
		return append(buf, wFalse), nil
	case int:
		return appendInt(buf, int64(x)), nil
	case int64:
		return appendInt(buf, x), nil
	case float64:
		buf = append(buf, wFloat)
		return binary.LittleEndian.AppendUint64(buf, math.Float64bits(x)), nil
	case string:
		return appendStr(buf, x), nil
	case []byte:
		return appendStrBytes(buf, x), nil
	case Handle:
		buf = append(buf, wHandle)
		return binary.LittleEndian.AppendUint32(buf, uint32(x)), nil
	case Null:
		return append(buf, wNull), nil
	case []any:
		buf = append(buf, wArray)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(x)))
		for _, e := range x {
			var err error
			if buf, err = encodeValue(buf, e); err != nil {
				return nil, err
			}
		}
		return buf, nil
	case map[string]any:
		buf = append(buf, wMap)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(x)))
		for k, val := range x {
			buf = appendStr(buf, k)
			var err error
			if buf, err = encodeValue(buf, val); err != nil {
				return nil, err
			}
		}
		return buf, nil
	default:
		return nil, fmt.Errorf("vmwasm/wire: tipo no serializable %T", v)
	}
}

func appendInt(buf []byte, x int64) []byte {
	buf = append(buf, wInt)
	return binary.LittleEndian.AppendUint64(buf, uint64(x))
}

func appendStr(buf []byte, s string) []byte {
	buf = append(buf, wStr)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(s)))
	return append(buf, s...)
}

func appendStrBytes(buf []byte, s []byte) []byte {
	buf = append(buf, wStr)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(s)))
	return append(buf, s...)
}

// Encode serializa una lista de valores (los args o los retornos de una
// primitiva) precedida de su número: u32 count + valores.
func Encode(vals []any) ([]byte, error) {
	buf := binary.LittleEndian.AppendUint32(make([]byte, 0, 64), uint32(len(vals)))
	for _, v := range vals {
		var err error
		if buf, err = encodeValue(buf, v); err != nil {
			return nil, err
		}
	}
	return buf, nil
}

// decoder consume el wire byte a byte.
type decoder struct {
	b   []byte
	pos int
}

func (d *decoder) u8() (byte, error) {
	if d.pos >= len(d.b) {
		return 0, fmt.Errorf("vmwasm/wire: fin de datos inesperado")
	}
	c := d.b[d.pos]
	d.pos++
	return c, nil
}

func (d *decoder) u32() (uint32, error) {
	if d.pos+4 > len(d.b) {
		return 0, fmt.Errorf("vmwasm/wire: u32 truncado")
	}
	x := binary.LittleEndian.Uint32(d.b[d.pos:])
	d.pos += 4
	return x, nil
}

// count lee un u32 destinado a dimensionar un `make` (nº de elementos de un
// array/map o de la lista raíz) y lo VALIDA contra el buffer restante antes de
// devolverlo. Cada elemento cuesta al menos un byte en el wire, así que un
// recuento mayor que los bytes que quedan es imposible: son bytes corruptos
// (p. ej. un dispatcher que pasó basura, o un frame truncado). Rechazarlo aquí
// evita `make([]any, nºgigante)` → OOM. Sin esta guardia, un u32 arbitrario se
// convierte en una petición de memoria de hasta 4 GiB.
func (d *decoder) count() (uint32, error) {
	n, err := d.u32()
	if err != nil {
		return 0, err
	}
	if int64(n) > int64(len(d.b)-d.pos) {
		return 0, fmt.Errorf("vmwasm/wire: recuento %d excede los %d bytes restantes (datos corruptos)", n, len(d.b)-d.pos)
	}
	return n, nil
}

func (d *decoder) u64() (uint64, error) {
	if d.pos+8 > len(d.b) {
		return 0, fmt.Errorf("vmwasm/wire: u64 truncado")
	}
	x := binary.LittleEndian.Uint64(d.b[d.pos:])
	d.pos += 8
	return x, nil
}

func (d *decoder) value() (any, error) {
	tag, err := d.u8()
	if err != nil {
		return nil, err
	}
	switch tag {
	case wNil:
		return nil, nil
	case wFalse:
		return false, nil
	case wTrue:
		return true, nil
	case wNull:
		return NullValue, nil
	case wInt:
		x, err := d.u64()
		return int64(x), err
	case wFloat:
		x, err := d.u64()
		return math.Float64frombits(x), err
	case wStr:
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		if d.pos+int(n) > len(d.b) {
			return nil, fmt.Errorf("vmwasm/wire: string truncado")
		}
		s := string(d.b[d.pos : d.pos+int(n)])
		d.pos += int(n)
		return s, nil
	case wHandle:
		x, err := d.u32()
		return Handle(x), err
	case wArray:
		n, err := d.count()
		if err != nil {
			return nil, err
		}
		arr := make([]any, n)
		for i := range arr {
			if arr[i], err = d.value(); err != nil {
				return nil, err
			}
		}
		return arr, nil
	case wMap:
		n, err := d.count()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, n)
		for i := uint32(0); i < n; i++ {
			kv, err := d.value()
			if err != nil {
				return nil, err
			}
			k, ok := kv.(string)
			if !ok {
				return nil, fmt.Errorf("vmwasm/wire: clave de map no-string %T", kv)
			}
			if m[k], err = d.value(); err != nil {
				return nil, err
			}
		}
		return m, nil
	default:
		return nil, fmt.Errorf("vmwasm/wire: tag desconocido %d", tag)
	}
}

// Decode deserializa una lista de valores (u32 count + valores).
func Decode(b []byte) ([]any, error) {
	d := &decoder{b: b}
	n, err := d.count()
	if err != nil {
		return nil, err
	}
	vals := make([]any, n)
	for i := range vals {
		if vals[i], err = d.value(); err != nil {
			return nil, err
		}
	}
	return vals, nil
}
