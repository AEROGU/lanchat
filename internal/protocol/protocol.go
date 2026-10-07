// Package protocol es el contrato entre equipos LanChat: versión, puertos,
// tiempos, límites y validación de lo que llega por la red.
//
// Regla: un valor va aquí solo si dos equipos tienen que coincidir en él. Los
// valores que solo afectan a un equipo (tiempos de espera, tamaños de búfer)
// son constantes con nombre en el paquete que los usa.
package protocol

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Version es la versión del protocolo (paquetes UDP y API HTTP). Solo se
// incrementa con cambios incompatibles: agregar un campo nuevo al JSON no lo
// es, porque los equipos viejos ignoran los campos que no conocen.
const Version = 2 // 2: comunicación cifrada con TLS (ver internal/identity)

// APIPrefix es el prefijo de las rutas HTTP entre equipos, p. ej. "/v1".
var APIPrefix = "/v" + strconv.Itoa(Version)

// Magic identifica los paquetes UDP de LanChat.
const Magic = "lanchat"

const (
	DefaultUDPPort  = 50000
	DefaultHTTPPort = 50001
)

const (
	// AnnounceInterval es cada cuánto se anuncia un equipo.
	AnnounceInterval = 10 * time.Second
	// PeerTTL es cuánto tiempo sin anuncios se espera antes de considerar
	// desconectado a un equipo; tolera perder 3 anuncios.
	PeerTTL = 3*AnnounceInterval + AnnounceInterval/2
)

const (
	MaxIDLen         = 64
	MaxNameLen       = 64 // en caracteres
	MaxHostnameLen   = 255
	MaxAppVersionLen = 32
	// MaxMessageBytes es el tamaño máximo del texto de un mensaje.
	MaxMessageBytes = 64 << 10
)

func ValidateID(s string) error {
	if s == "" || len(s) > MaxIDLen || hasControl(s, false) {
		return errors.New("id inválido")
	}
	return nil
}

// ValidateName valida un nombre propio o alias (vacío es válido).
func ValidateName(s string) error {
	if utf8.RuneCountInString(s) > MaxNameLen {
		return fmt.Errorf("el nombre admite máximo %d caracteres", MaxNameLen)
	}
	if !utf8.ValidString(s) || hasControl(s, false) {
		return errors.New("el nombre contiene caracteres no permitidos")
	}
	return nil
}

func ValidateHostname(s string) error {
	if len(s) > MaxHostnameLen || !utf8.ValidString(s) || hasControl(s, false) {
		return errors.New("hostname inválido")
	}
	return nil
}

func ValidateAppVersion(s string) error {
	if len(s) > MaxAppVersionLen || hasControl(s, false) {
		return errors.New("versión inválida")
	}
	return nil
}

// ValidateMessage valida el texto de un mensaje: admite saltos de línea y
// tabuladores, pero no otros caracteres de control (p. ej. secuencias de
// escape que alterarían una consola).
func ValidateMessage(s string) error {
	switch {
	case strings.TrimSpace(s) == "":
		return errors.New("mensaje vacío")
	case len(s) > MaxMessageBytes:
		return fmt.Errorf("mensaje demasiado largo (máximo %d KB)", MaxMessageBytes>>10)
	case !utf8.ValidString(s):
		return errors.New("el mensaje no es UTF-8 válido")
	case hasControl(s, true):
		return errors.New("el mensaje contiene caracteres de control")
	}
	return nil
}

func hasControl(s string, allowNewlines bool) bool {
	for _, r := range s {
		if allowNewlines && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// Estados de presencia que cada equipo anuncia.
const (
	StatusAvailable = "available"
	StatusAway      = "away"
	// StatusBusy es "no molestar": el equipo no muestra notificaciones.
	StatusBusy = "busy"
	// MaxStatusTextLen es el largo máximo del texto de estado ("En junta…").
	MaxStatusTextLen = 80 // en caracteres
)

// NormalizeStatus devuelve el estado si es conocido, o Disponible: así un
// estado nuevo de una versión futura no rompe a las anteriores.
func NormalizeStatus(s string) string {
	switch s {
	case StatusAway, StatusBusy:
		return s
	}
	return StatusAvailable
}

// ValidateStatusText valida el texto de estado (vacío es válido).
func ValidateStatusText(s string) error {
	if utf8.RuneCountInString(s) > MaxStatusTextLen {
		return fmt.Errorf("el estado admite máximo %d caracteres", MaxStatusTextLen)
	}
	if !utf8.ValidString(s) || hasControl(s, false) {
		return errors.New("el estado contiene caracteres no permitidos")
	}
	return nil
}

// FingerprintLen es el largo de una huella (SHA-256 en hex).
const FingerprintLen = 64

// ValidateFingerprint valida una huella: 64 caracteres hex en minúsculas.
func ValidateFingerprint(s string) error {
	if len(s) != FingerprintLen || strings.Trim(s, "0123456789abcdef") != "" {
		return errors.New("huella inválida")
	}
	return nil
}
