package uninstall

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/testutil"
)

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Tras usar LanChat de verdad, RemoveData no debe dejar nada: si el núcleo
// empieza a crear otro archivo, hay que agregarlo a dataEntries.
func TestRemoveDataLeavesNothing(t *testing.T) {
	dir := filepath.Join(testutil.TempDir(t), "LanChat")
	a, err := app.New(app.Options{
		Dir: dir, Hostname: "PC-PRUEBA", HTTPAddr: "127.0.0.1:0",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tune: func(c *discovery.Config) {
			c.UDPPort = 0
			c.BindIP = netip.MustParseAddr("127.0.0.1")
			c.NoBroadcast = true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	go func() {
		for range a.Events() {
		}
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Lo que agregan la ventana y el registro.
	for _, name := range []string{"ui.json", "icon.png", "lanchat.log", "lanchat.log.1", "edge/Default/Cache/data", "outbox/abc/plano.pdf"} {
		write(t, filepath.Join(dir, name))
	}

	removed, err := RemoveData(dir, 5*time.Second)
	if err != nil || !removed {
		left, _ := os.ReadDir(dir)
		t.Fatalf("debía borrar la carpeta: removed=%v err=%v quedan=%v", removed, err, left)
	}
}

func TestRemoveDataKeepsForeignFiles(t *testing.T) {
	dir := testutil.TempDir(t)
	if _, err := RemoveData(dir, 0); err == nil {
		t.Fatal("una carpeta cualquiera no se debe tocar")
	}
	write(t, filepath.Join(dir, "config.json"))
	write(t, filepath.Join(dir, "lanchat.db"))
	write(t, filepath.Join(dir, "mis notas.txt"))
	write(t, filepath.Join(dir, "fotos", "viaje.jpg"))

	removed, err := RemoveData(dir, 0)
	if err != nil || removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if exists(filepath.Join(dir, "lanchat.db")) || exists(filepath.Join(dir, "config.json")) {
		t.Error("los archivos de LanChat debían borrarse")
	}
	if !exists(filepath.Join(dir, "mis notas.txt")) || !exists(filepath.Join(dir, "fotos", "viaje.jpg")) {
		t.Error("los archivos ajenos no se tocan")
	}
}

func TestRemoveDownloads(t *testing.T) {
	// Carpeta elegida por el usuario: solo se va lo que LanChat recibió.
	custom := testutil.TempDir(t)
	received := []string{
		filepath.Join(custom, "informe.pdf"),
		filepath.Join(custom, "Proyecto", "planos", "a.dwg"),
		filepath.Join(custom, "ya-no-existe.txt"),
	}
	for _, p := range received[:2] {
		write(t, p)
	}
	write(t, filepath.Join(custom, "a medias.zip.lanchat-part"))
	write(t, filepath.Join(custom, "documento mío.docx"))
	write(t, filepath.Join(custom, "Otra", "mía.txt"))

	removed, err := RemoveDownloads(custom, false, received)
	if err != nil || removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	for _, gone := range []string{received[0], received[1], filepath.Join(custom, "Proyecto"), filepath.Join(custom, "a medias.zip.lanchat-part")} {
		if exists(gone) {
			t.Errorf("debía borrarse: %s", gone)
		}
	}
	for _, kept := range []string{filepath.Join(custom, "documento mío.docx"), filepath.Join(custom, "Otra", "mía.txt")} {
		if !exists(kept) {
			t.Errorf("no debía tocarse: %s", kept)
		}
	}

	// Sin archivos ajenos, la carpeta elegida también se va.
	empty := filepath.Join(testutil.TempDir(t), "Recibidos")
	only := filepath.Join(empty, "sub", "x.txt")
	write(t, only)
	if removed, err := RemoveDownloads(empty, false, []string{only}); err != nil || !removed {
		t.Errorf("carpeta vacía: removed=%v err=%v", removed, err)
	}

	// La carpeta propia (Descargas\LanChat) se borra entera.
	own := filepath.Join(testutil.TempDir(t), "LanChat")
	write(t, filepath.Join(own, "recibido antes de borrar la base.pdf"))
	if removed, err := RemoveDownloads(own, true, nil); err != nil || !removed || exists(own) {
		t.Errorf("carpeta propia: removed=%v err=%v", removed, err)
	}
	// Una carpeta que ya no existe no es un error.
	if _, err := RemoveDownloads(own, true, nil); err != nil {
		t.Errorf("carpeta inexistente: %v", err)
	}
}
