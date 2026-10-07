//go:build !windows

package ui

import (
	"fmt"
	"os"
)

// ShowError escribe el error en la salida de errores.
func ShowError(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}

// ShowInfo escribe el aviso en la salida estándar.
func ShowInfo(msg string) {
	fmt.Println(msg)
}
