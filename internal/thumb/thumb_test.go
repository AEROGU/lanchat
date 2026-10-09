package thumb

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// halves dibuja una imagen w×h: mitad izquierda roja, derecha azul.
func halves(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{R: 255, A: 255}
			if x >= w/2 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	if !Valid(b) {
		t.Fatalf("miniatura no válida (%d bytes)", len(b))
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xC000 && g < 0x4000 && b < 0x4000
}

func TestScaleKeepsAspect(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, halves(1600, 900))
	b, err := FromReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, b).Bounds().Size(); got != image.Pt(MaxSide, 180) {
		t.Errorf("tamaño = %v, quería 320×180", got)
	}
}

func TestSmallNotEnlarged(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 40, 30))) // transparente
	b, err := FromReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, b)
	if img.Bounds().Size() != image.Pt(40, 30) {
		t.Errorf("tamaño = %v", img.Bounds().Size())
	}
	if r, g, b, _ := img.At(20, 15).RGBA(); r < 0xF000 || g < 0xF000 || b < 0xF000 {
		t.Error("la transparencia debía quedar en blanco")
	}
}

// Una foto con Orientation = 6 (girar 90° a la derecha) queda derecha.
func TestExifOrientation(t *testing.T) {
	var buf bytes.Buffer
	jpeg.Encode(&buf, halves(200, 100), &jpeg.Options{Quality: 95})
	photo := withOrientation(buf.Bytes(), 6)
	b, err := FromReader(bytes.NewReader(photo))
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, b)
	if img.Bounds().Size() != image.Pt(100, 200) {
		t.Fatalf("tamaño = %v, quería 100×200 (girada)", img.Bounds().Size())
	}
	// Girada a la derecha, la mitad izquierda (roja) queda arriba.
	if !isRed(img.At(50, 20)) || isRed(img.At(50, 180)) {
		t.Error("la imagen no quedó girada 90° a la derecha")
	}
}

func TestRejects(t *testing.T) {
	if _, err := FromReader(bytes.NewReader([]byte("no es una imagen"))); err == nil {
		t.Error("un texto no debía dar miniatura")
	}
	if Valid([]byte("no es un jpeg")) || Valid(nil) || Valid(make([]byte, MaxBytes+1)) {
		t.Error("Valid aceptó algo que no es una miniatura")
	}
	for name, want := range map[string]bool{"foto.JPG": true, "a.webp": true, "b.png": true,
		"c.gif": true, "video.mp4": false, "doc.pdf": false, "foto.heic": false, "sin": false} {
		if Supported(name) != want {
			t.Errorf("Supported(%q) = %v", name, !want)
		}
	}
}

// withOrientation inserta tras el SOI un APP1 EXIF con Orientation = o.
func withOrientation(jpg []byte, o uint16) []byte {
	tiff := []byte("MM\x00\x2A\x00\x00\x00\x08")  // big endian, IFD0 en el byte 8
	tiff = binary.BigEndian.AppendUint16(tiff, 1) // una entrada
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.BigEndian.AppendUint16(tiff, 3) // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, o)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0) // relleno del valor y siguiente IFD = 0
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(seg)+2))
	app1 = append(app1, seg...)
	return append(append(append([]byte{}, jpg[:2]...), app1...), jpg[2:]...)
}
