package icon

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestICO(t *testing.T) {
	ico := ICO(true)
	var hdr struct{ Reserved, Type, Count uint16 }
	if err := binary.Read(bytes.NewReader(ico), binary.LittleEndian, &hdr); err != nil ||
		hdr.Type != 1 || int(hdr.Count) != len(TraySizes) {
		t.Fatalf("cabecera ICO inválida: %+v %v", hdr, err)
	}
}

func TestPNG(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(PNG(64, false)))
	if err != nil || img.Bounds().Dx() != 64 {
		t.Fatalf("PNG: %v %v", img, err)
	}
	// Las esquinas son transparentes y el centro, opaco.
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Error("la esquina debía ser transparente")
	}
	if _, _, _, a := img.At(32, 32).RGBA(); a == 0 {
		t.Error("el centro debía ser opaco")
	}
}
