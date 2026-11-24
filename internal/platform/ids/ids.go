// Package ids genera y valida identificadores únicos.
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

// New devuelve un UUID versión 4 en formato canónico (36 caracteres).
// Usa crypto/rand: es adecuado para identificadores expuestos a clientes
// y para claves de fila, evitando dependencias externas.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand solo falla si el sistema no puede aportar entropía.
		panic("ids: no se pudo obtener entropía: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // versión 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante RFC 4122

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

// Valid indica si s tiene forma de UUID canónico (hexadecimal con guiones).
func Valid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHex(byte(r)) {
				return false
			}
		}
	}
	return true
}

func isHex(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'a' && c <= 'f':
		return true
	case c >= 'A' && c <= 'F':
		return true
	}
	return false
}
