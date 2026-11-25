package app

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/testutil"
	"github.com/AEROGU/lanchat/internal/thumb"
)

// pngOf es una imagen PNG de un solo color.
func pngOf(t *testing.T, c color.Color) *bytes.Reader {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := range 300 {
		for x := range 400 {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestProfilePhotos(t *testing.T) {
	ctx := context.Background()
	a := startNode(t, testutil.TempDir(t), "PC-A")
	b := startNode(t, testutil.TempDir(t), "PC-B", a.udpPort())
	idB := b.app.Self().ID
	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, idB))
	photoChanged := func(ev any) bool { e, ok := ev.(PhotoChanged); return ok && e.PeerID == idB }

	if err := b.app.SetPhoto(ctx, bytes.NewReader([]byte("no es una imagen"))); err == nil {
		t.Error("una imagen inválida debía rechazarse")
	}
	if err := b.app.SetPhoto(ctx, pngOf(t, color.RGBA{R: 200, A: 255})); err != nil {
		t.Fatal(err)
	}
	photo := b.app.Self().Photo
	if photo == "" {
		t.Fatal("Self no tiene la foto nueva")
	}
	waitFor(t, a, "A descarga la foto de B", photoChanged)
	c, _, _ := a.app.Contact(ctx, idB)
	if c.Photo != photo {
		t.Errorf("foto en A = %q, quería %q", c.Photo, photo)
	}
	if id, data, ok, err := a.app.Photo(ctx, idB); !ok || err != nil || id != photo || len(data) == 0 {
		t.Errorf("Photo = %q %d %v %v", id, len(data), ok, err)
	}

	// Cambiarla la vuelve a descargar.
	if err := b.app.SetPhoto(ctx, pngOf(t, color.RGBA{B: 200, A: 255})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A descarga la foto nueva", photoChanged)
	if c, _, _ := a.app.Contact(ctx, idB); c.Photo == photo || c.Photo != b.app.Self().Photo {
		t.Errorf("foto en A = %q tras cambiarla", c.Photo)
	}

	// Quitarla la borra en A.
	if err := b.app.RemovePhoto(ctx); err != nil {
		t.Fatal(err)
	}
	if b.app.Self().Photo != "" {
		t.Error("Self debía quedar sin foto")
	}
	waitFor(t, a, "A borra la foto", photoChanged)
	if c, _, _ := a.app.Contact(ctx, idB); c.Photo != "" {
		t.Errorf("foto en A = %q tras quitarla", c.Photo)
	}
	if _, _, ok, _ := a.app.Photo(ctx, idB); ok {
		t.Error("A debía borrar la foto guardada")
	}
}

// La foto propia se conserva al reiniciar y se anuncia desde el arranque.
func TestProfilePhotoSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dirB := testutil.TempDir(t)
	b := startNode(t, dirB, "PC-B")
	if err := b.app.SetPhoto(ctx, pngOf(t, color.RGBA{G: 200, A: 255})); err != nil {
		t.Fatal(err)
	}
	photo := b.app.Self().Photo
	b.stop()

	a := startNode(t, testutil.TempDir(t), "PC-A")
	b = startNode(t, dirB, "PC-B", a.udpPort())
	if b.app.Self().Photo != photo {
		t.Fatalf("foto tras reiniciar = %q, quería %q", b.app.Self().Photo, photo)
	}
	idB := b.app.Self().ID
	waitFor(t, a, "A descarga la foto de B", func(ev any) bool { e, ok := ev.(PhotoChanged); return ok && e.PeerID == idB })
	if c, _, _ := a.app.Contact(ctx, idB); c.Photo != photo {
		t.Errorf("foto en A = %q", c.Photo)
	}
}

// Si la descarga falla y el equipo sigue anunciando lo mismo (sin eventos de
// discovery), la foto se reintenta en el ciclo periódico y llega igual.
func TestProfilePhotoRetriedWithoutAnnounceChange(t *testing.T) {
	ctx := context.Background()
	tune := func(o *Options) { o.GossipInterval = 200 * time.Millisecond }
	a := startNodeWith(t, testutil.TempDir(t), "PC-A", tune)
	b := startNode(t, testutil.TempDir(t), "PC-B", a.udpPort())
	idB := b.app.Self().ID
	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, idB))

	// La foto que B va a poner ya "falló" en A: el anuncio que la trae no la
	// descarga. Luego vence la espera sin que B anuncie nada distinto.
	img := pngOf(t, color.RGBA{R: 90, G: 90, A: 255})
	data, err := thumb.Avatar(img)
	if err != nil {
		t.Fatal(err)
	}
	img.Seek(0, io.SeekStart)
	id := protocol.PhotoID(data)
	ph := a.app.photos
	ph.mu.Lock()
	ph.failed[idB] = failedGet{id, time.Now().Add(500 * time.Millisecond)}
	ph.mu.Unlock()
	if err := b.app.SetPhoto(ctx, img); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A reintenta y descarga la foto de B", func(ev any) bool { e, ok := ev.(PhotoChanged); return ok && e.PeerID == idB })
	if c, _, _ := a.app.Contact(ctx, idB); c.Photo != id {
		t.Errorf("foto en A = %q, quería %q", c.Photo, id)
	}
}
