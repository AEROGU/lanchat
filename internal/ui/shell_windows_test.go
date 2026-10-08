package ui

import (
	"slices"
	"testing"
	"unicode/utf16"
)

// El filtro del selector de archivos lleva separadores \0 y termina en \0\0
// (con un filtro vacío el botón 📎 fallaba al abrir el selector).
func TestFileFilter(t *testing.T) {
	f := fileFilter()
	if len(f) < 3 || !slices.Equal(f[len(f)-2:], []uint16{0, 0}) {
		t.Fatalf("debe terminar en doble NUL: %v", f)
	}
	if got := string(utf16.Decode(f)); got != "Todos los archivos\x00*.*\x00\x00" {
		t.Errorf("filtro = %q", got)
	}
}
