package thumb

import (
	"encoding/binary"
	"image"
	"io"
)

// maxExifSegment: un APP1 (EXIF) no puede pasar de 64 KB por formato.
const maxExifSegment = 64 << 10

// exifOrientation lee la etiqueta Orientation (0x0112) de un JPEG: los
// teléfonos guardan la foto como la ve el sensor y anotan cómo girarla. 1 si no
// la hay o no se entiende.
func exifOrientation(r io.Reader) int {
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return 1
	}
	for {
		var m [4]byte // FF xx y el largo (incluye sus 2 bytes)
		if _, err := io.ReadFull(r, m[:]); err != nil || m[0] != 0xFF {
			return 1
		}
		if m[1] == 0xDA || m[1] == 0xD9 { // empiezan los datos de la imagen: no hay EXIF
			return 1
		}
		n := int(binary.BigEndian.Uint16(m[2:])) - 2
		if n < 0 || n > maxExifSegment {
			return 1
		}
		seg := make([]byte, n)
		if _, err := io.ReadFull(r, seg); err != nil {
			return 1
		}
		if m[1] == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
	}
}

// tiffOrientation busca Orientation en el primer directorio (IFD0) del TIFF
// que va dentro del EXIF.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[off:]))
	for i := range count {
		e := off + 2 + 12*i
		if e+12 > len(t) {
			return 1
		}
		// Entrada: etiqueta (2), tipo (2; 3 = SHORT), cantidad (4), valor (4).
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient gira o refleja img según la orientación EXIF (1 a 8) para verla
// derecha. Se aplica a la miniatura, ya pequeña.
func orient(img *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return img
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 { // 5 a 8 intercambian ancho y alto
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			var sx, sy int
			switch o {
			case 2: // espejo horizontal
				sx, sy = w-1-x, y
			case 3: // 180°
				sx, sy = w-1-x, h-1-y
			case 4: // espejo vertical
				sx, sy = x, h-1-y
			case 5: // transpuesta
				sx, sy = y, x
			case 6: // 90° a la derecha
				sx, sy = y, h-1-x
			case 7: // transversa
				sx, sy = w-1-y, h-1-x
			case 8: // 90° a la izquierda
				sx, sy = w-1-y, x
			}
			dst.SetRGBA(x, y, img.RGBAAt(sx, sy))
		}
	}
	return dst
}
