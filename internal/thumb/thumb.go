// Package thumb genera en memoria miniaturas JPEG de imágenes, para la vista
// previa de los archivos en la conversación. No usa archivos temporales: lee la
// imagen, la reduce y devuelve los bytes, que se guardan con la oferta.
package thumb

import (
	"bufio"
	"bytes"
	"errors"
	"image"
	"image/draw"
	_ "image/gif" // registra el formato para image.Decode
	"image/jpeg"
	_ "image/png" // registra el formato para image.Decode
	"io"
	"os"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registra el formato para image.Decode
)

const (
	// MaxSide es el lado mayor de la miniatura, en píxeles.
	MaxSide = 320
	// MaxBytes acota una miniatura, también al recibirla de otro equipo.
	MaxBytes = 32 << 10
	// MaxPerOffer: cuántas imágenes de una oferta llevan miniatura (las
	// primeras); acota el tamaño del mensaje y el trabajo al enviar.
	MaxPerOffer = 10
	// maxPixels: no se decodifican imágenes más grandes (la memoria del
	// teléfono; una de 50 Mpx ocupa ~200 MB al decodificarla).
	maxPixels = 50_000_000
)

var (
	errTooLarge = errors.New("imagen demasiado grande para la vista previa")
	errTooHeavy = errors.New("la miniatura no cabe en el tamaño máximo")
)

var exts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true}

// Supported indica si name es de una imagen con vista previa (JPG, PNG, GIF,
// WebP). Video, PDF o HEIC no: Go no los decodifica sin cgo.
func Supported(name string) bool { return exts[strings.ToLower(filepath.Ext(name))] }

// FromFile genera la miniatura de la imagen en path.
func FromFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return FromReader(f)
}

// FromReader genera la miniatura de una imagen: reducida a MaxSide, girada
// según la orientación EXIF de las fotos y con la transparencia sobre blanco.
func FromReader(r io.ReadSeeker) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bufio.NewReader(r))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, errTooLarge
	}
	orientation := 1
	if format == "jpeg" {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		orientation = exifOrientation(bufio.NewReader(r))
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bufio.NewReader(r))
	if err != nil {
		return nil, err
	}
	return encode(orient(scale(img), orientation))
}

// Valid indica si b es una miniatura aceptable (al recibirla de otro equipo):
// un JPEG pequeño. La página la muestra como image/jpeg.
func Valid(b []byte) bool {
	if len(b) == 0 || len(b) > MaxBytes {
		return false
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	return err == nil && cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= 2*MaxSide && cfg.Height <= 2*MaxSide
}

// scale reduce img para que su lado mayor sea MaxSide (nunca la agranda) y
// la compone sobre blanco: JPEG no tiene transparencia.
func scale(img image.Image) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > MaxSide || h > MaxSide {
		if w >= h {
			w, h = MaxSide, max(1, h*MaxSide/w)
		} else {
			w, h = max(1, w*MaxSide/h), MaxSide
		}
	}
	// Una foto grande pasa primero por una reducción rápida al doble del
	// tamaño final; CatmullRom sobre la foto entera sería lento en el teléfono.
	if b.Dx() > 4*w {
		mid := image.NewRGBA(image.Rect(0, 0, 2*w, 2*h))
		xdraw.ApproxBiLinear.Scale(mid, mid.Bounds(), img, b, draw.Src, nil)
		img, b = mid, mid.Bounds()
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// encode comprime a JPEG, bajando la calidad si no cabe en MaxBytes.
func encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	for _, q := range []int{75, 55, 40} {
		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
		if buf.Len() <= MaxBytes {
			return buf.Bytes(), nil
		}
	}
	return nil, errTooHeavy
}
