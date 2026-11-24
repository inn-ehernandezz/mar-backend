package storage

import (
	"crypto/sha1"
	"fmt"
)

// uuidDNSNamespace es el espacio de nombres estándar DNS de UUID (RFC 4122).
var uuidDNSNamespace = [16]byte{
	0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}

// DeterministicPointID calcula un UUID v5 (SHA-1 con espacio de nombres) a
// partir de los componentes indicados. Qdrant admite identificadores UUID, y
// al ser determinista una reentrega del mismo fragmento genera el mismo punto:
// el upsert es idempotente y no duplica datos.
//
// SHA-1 se usa aquí porque forma parte de la especificación UUID v5, no como
// primitiva de seguridad.
func DeterministicPointID(parts ...string) string {
	h := sha1.New()
	_, _ = h.Write(uuidDNSNamespace[:])
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		// Separador para que ("ab","c") y ("a","bc") no colisionen.
		_, _ = h.Write([]byte{0x1f})
	}
	sum := h.Sum(nil)

	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50 // versión 5
	u[8] = (u[8] & 0x3f) | 0x80 // variante RFC 4122

	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
