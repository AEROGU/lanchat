package protocol

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Rutas HTTP entre equipos. Los patrones (Route*) son para http.ServeMux y las
// funciones Path* arman la URL concreta.
var (
	// RouteMessage recibe un mensaje de chat (POST).
	RouteMessage = APIPrefix + "/msg"
	// RoutePeers entrega los equipos en línea que conoce este equipo (GET), para
	// que los de otras subredes se descubran sin configurarlos en cada PC.
	RoutePeers = APIPrefix + "/peers"
	// RouteRead recibe avisos de lectura: el otro equipo leyó estos mensajes (POST).
	RouteRead = APIPrefix + "/read"
	// RouteFile entrega un archivo de una oferta (GET, admite Range).
	RouteFile = APIPrefix + "/transfers/{id}/files/{idx}"
	// RouteFileDone confirma que el archivo llegó íntegro (POST).
	RouteFileDone = APIPrefix + "/transfers/{id}/files/{idx}/done"
	// RouteTransferState avisa al otro equipo de un rechazo o cancelación (POST).
	RouteTransferState = APIPrefix + "/transfers/{id}/state"
)

func PathFile(id string, idx int) string {
	return APIPrefix + "/transfers/" + url.PathEscape(id) + "/files/" + strconv.Itoa(idx)
}

func PathFileDone(id string, idx int) string { return PathFile(id, idx) + "/done" }

func PathTransferState(id string) string {
	return APIPrefix + "/transfers/" + url.PathEscape(id) + "/state"
}

const (
	// TokenHeader lleva el token de la oferta en las peticiones de transferencia.
	TokenHeader = "X-Lanchat-Token"
	// HashTrailer lleva, al final de la descarga, el SHA-256 (hex) del archivo completo.
	HashTrailer = "X-Lanchat-Sha256"
)

const (
	MaxOfferFiles  = 1000
	MaxFileNameLen = 255 // en bytes
	MaxTokenLen    = 128
	// MaxReceiptIDs: mensajes por aviso de lectura (se envían en tandas).
	MaxReceiptIDs = 500
	// MaxSharedPeers: equipos por lista compartida.
	MaxSharedPeers = 256
)

// Estados que un equipo puede comunicar al otro en RouteTransferState.
const (
	StateRejected = "rejected"
	StateCanceled = "canceled"
)

// ValidateFileName valida el nombre de un archivo ofrecido. El receptor
// además lo adapta a las reglas de Windows antes de guardarlo.
func ValidateFileName(s string) error {
	switch {
	case s == "" || s == "." || s == "..":
		return errors.New("nombre de archivo vacío")
	case len(s) > MaxFileNameLen:
		return fmt.Errorf("nombre de archivo de más de %d bytes", MaxFileNameLen)
	case !utf8.ValidString(s) || hasControl(s, false):
		return errors.New("nombre de archivo con caracteres no permitidos")
	case strings.ContainsAny(s, `/\`):
		return errors.New("el nombre de archivo no puede incluir carpetas")
	}
	return nil
}

func ValidateToken(s string) error {
	if s == "" || len(s) > MaxTokenLen || hasControl(s, false) {
		return errors.New("token inválido")
	}
	return nil
}

const (
	// MaxRelDirLen y MaxDirDepth limitan la subcarpeta de un archivo ofrecido.
	MaxRelDirLen = 1024 // en bytes
	MaxDirDepth  = 32
)

// ValidateRelDir valida la subcarpeta relativa de un archivo ofrecido
// ("Proyecto/planos"; vacío = sin carpeta): segmentos separados por "/",
// cada uno un nombre de archivo válido, sin "." ni "..".
func ValidateRelDir(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > MaxRelDirLen {
		return fmt.Errorf("ruta de carpeta de más de %d bytes", MaxRelDirLen)
	}
	parts := strings.Split(s, "/")
	if len(parts) > MaxDirDepth {
		return fmt.Errorf("más de %d niveles de carpetas", MaxDirDepth)
	}
	for _, p := range parts {
		if err := ValidateFileName(p); err != nil {
			return fmt.Errorf("carpeta %q: %w", s, err)
		}
	}
	return nil
}
