// Package platform integra LanChat con Windows: inicio automático al
// encender, regla del Firewall y ejecución con permisos de administrador.
// Fuera de Windows las funciones no hacen nada.
package platform

import (
	"errors"
	"os"
	"path/filepath"
)

const (
	// appName es el nombre de la regla del firewall y de la entrada de inicio.
	appName = "LanChat"
	// HiddenFlag es la opción (-hidden) para arrancar solo en la bandeja; la
	// define cmd/lanchat y la usa el inicio automático.
	HiddenFlag = "hidden"
)

// ErrCanceled: el usuario no aceptó el aviso de Control de cuentas (UAC).
var ErrCanceled = errors.New("se canceló el permiso de administrador")

// ErrUnsupported: la función no existe en este sistema operativo.
var ErrUnsupported = errors.New("solo disponible en Windows")

// Executable es la ruta real del programa en ejecución.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
