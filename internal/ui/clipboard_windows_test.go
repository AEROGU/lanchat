package ui

import (
	"encoding/binary"
	"slices"
	"testing"
	"unicode/utf16"
)

// CF_HDROP: DROPFILES (pFiles=20, fWide=1) y las rutas en UTF-16, cada una
// con su NUL y un NUL final.
func TestHdropData(t *testing.T) {
	paths := []string{`C:\Descargas\año.jpg`, `C:\x\Obra (1).zip`}
	b := hdropData(paths)
	if got := binary.LittleEndian.Uint32(b[0:]); got != dropFilesSize {
		t.Errorf("pFiles = %d", got)
	}
	if got := binary.LittleEndian.Uint32(b[16:]); got != 1 {
		t.Errorf("fWide = %d", got)
	}
	rest := b[dropFilesSize:]
	chars := make([]uint16, len(rest)/2)
	for i := range chars {
		chars[i] = binary.LittleEndian.Uint16(rest[2*i:])
	}
	want := utf16.Encode([]rune(paths[0] + "\x00" + paths[1] + "\x00\x00"))
	if !slices.Equal(chars, want) {
		t.Errorf("rutas = %q", string(utf16.Decode(chars)))
	}
}
