// Package storage define los puertos de almacenamiento que consumen los
// módulos de MAR (ingesta, indexación y búsqueda). Los puertos son interfaces
// Go puras: no dependen de ningún SDK de Azure ni del cliente de Qdrant. Los
// adaptadores concretos viven en los subpaquetes blob, table y qdrant.
//
// Esto permite que la lógica de negocio no sepa si por debajo hay Azurite,
// Azure real o una versión desplegada de Qdrant: solo depende de estas
// interfaces y de los tipos definidos en este paquete.
package storage

import "errors"

// Errores de dominio devueltos por los adaptadores. Los adaptadores traducen
// los errores de sus SDK a estos valores para que la lógica de negocio no
// dependa de detalles de infraestructura.
var (
	// ErrNotFound indica que un objeto, entidad o punto no existe.
	ErrNotFound = errors.New("recurso no encontrado")

	// ErrConflict indica un conflicto de concurrencia optimista (ETag) o de
	// unicidad al crear una entidad.
	ErrConflict = errors.New("conflicto")

	// ErrPreconditionFailed indica que una precondición condicional no se
	// cumplió, normalmente por evitar una sobrescritura accidental.
	ErrPreconditionFailed = errors.New("precondición no cumplida")

	// ErrIncompatibleCollection indica que ya existe una colección cuyo
	// esquema no coincide con la configuración esperada. Nunca se elimina ni
	// se modifica automáticamente: se informa del problema.
	ErrIncompatibleCollection = errors.New("colección incompatible con la configuración")

	// ErrInvalidTransition indica una transición de estado no permitida para
	// un trabajo.
	ErrInvalidTransition = errors.New("transición de estado no permitida")

	// ErrInvalidArgument indica parámetros de entrada inválidos.
	ErrInvalidArgument = errors.New("argumento inválido")
)
