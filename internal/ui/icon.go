package ui

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

// El icono se dibuja por código: un globo de diálogo blanco sobre un cuadrado
// azul redondeado; con aviso, lleva además un punto rojo de "sin leer".

const (
	faviconSize = 64
	toastSize   = 128
	// supersample: muestras por eje y píxel para suavizar los bordes.
	supersample = 4
)

var (
	colorBrand = color.NRGBA{37, 99, 235, 255}
	colorWhite = color.NRGBA{255, 255, 255, 255}
	colorAlert = color.NRGBA{220, 38, 38, 255}
	// trayIconSizes son los tamaños que Windows elige según el DPI.
	trayIconSizes = []int{16, 20, 24, 32, 48}
)

type layer struct {
	inside func(u, v float64) bool // coordenadas normalizadas 0..1
	color  color.NRGBA
}

func iconLayers(badge bool) []layer {
	ls := []layer{
		{func(u, v float64) bool { return inRoundRect(u, v, 0, 0, 1, 1, 0.22) }, colorBrand},
		{func(u, v float64) bool {
			return inRoundRect(u, v, 0.18, 0.22, 0.82, 0.66, 0.13) ||
				inTriangle(u, v, 0.28, 0.6, 0.28, 0.82, 0.48, 0.6)
		}, colorWhite},
	}
	for _, cx := range []float64{0.34, 0.5, 0.66} {
		ls = append(ls, layer{func(u, v float64) bool { return inCircle(u, v, cx, 0.44, 0.055) }, colorBrand})
	}
	if badge {
		ls = append(ls,
			layer{func(u, v float64) bool { return inCircle(u, v, 0.8, 0.2, 0.24) }, colorWhite},
			layer{func(u, v float64) bool { return inCircle(u, v, 0.8, 0.2, 0.18) }, colorAlert},
		)
	}
	return ls
}

func drawIcon(size int, badge bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	layers := iconLayers(badge)
	n := float64(size * supersample)
	for y := range size {
		for x := range size {
			var r, g, b, a float64
			for sy := range supersample {
				for sx := range supersample {
					u := (float64(x*supersample+sx) + 0.5) / n
					v := (float64(y*supersample+sy) + 0.5) / n
					// La última capa que contiene el punto es la visible.
					for i := len(layers) - 1; i >= 0; i-- {
						if layers[i].inside(u, v) {
							c := layers[i].color
							r += float64(c.R)
							g += float64(c.G)
							b += float64(c.B)
							a++
							break
						}
					}
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / a), G: uint8(g / a), B: uint8(b / a),
				A: uint8(a * 255 / (supersample * supersample)),
			})
		}
	}
	return img
}

func inCircle(u, v, cx, cy, r float64) bool {
	return (u-cx)*(u-cx)+(v-cy)*(v-cy) <= r*r
}

func inRoundRect(u, v, x0, y0, x1, y1, r float64) bool {
	if u < x0 || u > x1 || v < y0 || v > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(u, x1-r))
	cy := math.Max(y0+r, math.Min(v, y1-r))
	return inCircle(u, v, cx, cy, r)
}

func inTriangle(u, v, x1, y1, x2, y2, x3, y3 float64) bool {
	side := func(ax, ay, bx, by float64) float64 { return (u-bx)*(ay-by) - (ax-bx)*(v-by) }
	d1, d2, d3 := side(x1, y1, x2, y2), side(x2, y2, x3, y3), side(x3, y3, x1, y1)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

type iconKey struct {
	size  int
	badge bool
}

var (
	iconCacheMu sync.Mutex
	pngCache    = map[iconKey][]byte{}
	icoCache    = map[bool][]byte{}
)

// iconPNG devuelve el icono en PNG (para la página y las notificaciones).
func iconPNG(size int, badge bool) []byte {
	iconCacheMu.Lock()
	defer iconCacheMu.Unlock()
	k := iconKey{size, badge}
	if b, ok := pngCache[k]; ok {
		return b
	}
	var buf bytes.Buffer
	png.Encode(&buf, drawIcon(size, badge))
	pngCache[k] = buf.Bytes()
	return pngCache[k]
}

// iconICO devuelve el icono en formato .ico (el que exige la bandeja de Windows).
func iconICO(badge bool) []byte {
	iconCacheMu.Lock()
	defer iconCacheMu.Unlock()
	if b, ok := icoCache[badge]; ok {
		return b
	}
	imgs := make([]*image.NRGBA, len(trayIconSizes))
	for i, s := range trayIconSizes {
		imgs[i] = drawIcon(s, badge)
	}
	icoCache[badge] = encodeICO(imgs)
	return icoCache[badge]
}

// encodeICO arma un .ico con imágenes BMP de 32 bits (BGRA con alfa), el
// formato que entiende cualquier versión de Windows.
func encodeICO(imgs []*image.NRGBA) []byte {
	const (
		dirSize    = 6
		entrySize  = 16
		headerSize = 40 // BITMAPINFOHEADER
	)
	var data bytes.Buffer
	var dir bytes.Buffer
	le := binary.LittleEndian
	binary.Write(&dir, le, [3]uint16{0, 1, uint16(len(imgs))}) // reservado, tipo icono, cantidad
	offset := dirSize + entrySize*len(imgs)

	for _, img := range imgs {
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		maskStride := (w + 31) / 32 * 4
		pixelBytes := w * h * 4
		size := headerSize + pixelBytes + maskStride*h

		binary.Write(&dir, le, struct {
			W, H, Colors, Reserved uint8
			Planes, BitCount       uint16
			Size, Offset           uint32
		}{uint8(w), uint8(h), 0, 0, 1, 32, uint32(size), uint32(offset)})
		offset += size

		binary.Write(&data, le, struct {
			Size                     uint32
			Width, Height            int32
			Planes, BitCount         uint16
			Compression, ImageSize   uint32
			XPPM, YPPM, Used, Import int32
		}{headerSize, int32(w), int32(2 * h), 1, 32, 0, uint32(pixelBytes), 0, 0, 0, 0})
		// Filas de abajo hacia arriba, en BGRA.
		for y := h - 1; y >= 0; y-- {
			for x := range w {
				c := img.NRGBAAt(x, y)
				data.Write([]byte{c.B, c.G, c.R, c.A})
			}
		}
		// Máscara AND vacía: la transparencia la da el canal alfa.
		data.Write(make([]byte, maskStride*h))
	}
	return append(dir.Bytes(), data.Bytes()...)
}
