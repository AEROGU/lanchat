package protocol

import (
	"errors"
	"time"
	"unicode"
	"unicode/utf8"
)

// Avisos sobre mensajes ya enviados: eliminarlos para todos y reaccionar.
// Viajan por RouteOps en tandas; un equipo con una versión que no conoce la
// ruta responde 404 y el aviso se descarta.

// RouteOps recibe avisos sobre mensajes ya enviados (POST).
var RouteOps = APIPrefix + "/ops"

// Tipos de aviso. Un tipo desconocido se ignora (versiones futuras).
const (
	// OpDelete: el autor eliminó su mensaje para todos.
	OpDelete = "delete"
	// OpReact: el remitente reaccionó a un mensaje (emoji vacío = quitó su reacción).
	OpReact = "react"
)

const (
	// DeleteWindow es cuánto tiempo después de enviarlo puede el autor
	// eliminar un mensaje para todos. Lo comprueban quien elimina y quien
	// recibe el aviso (con las horas del remitente, así no dependen de que
	// los relojes de los equipos coincidan).
	DeleteWindow = 10 * time.Minute
	// MaxOpsPerRequest: avisos por petición (se envían en tandas).
	MaxOpsPerRequest = 200
	// MaxReactionBytes acota una reacción: un emoji puede ser una secuencia
	// (familia, bandera, tono de piel) de varios caracteres.
	MaxReactionBytes = 40
	maxReactionRunes = 10
)

// ValidateReaction comprueba que s sea un emoji (o una secuencia de emoji) y
// no texto: solo símbolos, modificadores de tono, uniones y selectores.
func ValidateReaction(s string) error {
	if s == "" || len(s) > MaxReactionBytes || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxReactionRunes {
		return errors.New("reacción inválida")
	}
	symbols := 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.So, r):
			symbols++
		case unicode.Is(unicode.Sk, r), r == zeroWidthJoiner, r == variationSelector16:
		default:
			return errors.New("la reacción debe ser un emoji")
		}
	}
	if symbols == 0 {
		return errors.New("la reacción debe ser un emoji")
	}
	return nil
}

// Caracteres invisibles de las secuencias de emoji, por su código para no
// escribirlos en el archivo.
const (
	// zeroWidthJoiner une varios emoji en uno (una familia, una profesión).
	zeroWidthJoiner = rune(0x200D)
	// variationSelector16 pide la presentación en color (el corazón rojo).
	variationSelector16 = rune(0xFE0F)
)
