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
