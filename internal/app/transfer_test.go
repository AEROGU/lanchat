package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/testutil"
	"github.com/AEROGU/lanchat/internal/transfer"
)

// pair arranca dos equipos que se ven entre sí; B guarda en su propia carpeta.
func pair(t *testing.T) (a, b *node, downloads string) {
	t.Helper()
	a = startNode(t, testutil.TempDir(t), "PC-A")
	b = startNode(t, testutil.TempDir(t), "PC-B", a.udpPort())
	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, b.app.Self().ID))
	waitFor(t, b, "B ve a A", peerEvent(discovery.PeerOnline, a.app.Self().ID))
	downloads = testutil.TempDir(t)
	if err := b.app.SetDownloadDir(downloads); err != nil {
		t.Fatal(err)
	}
	return a, b, downloads
}

func writeRandom(t *testing.T, dir, name string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, data
}

func transferState(id string, st store.TransferState) func(any) bool {
	return func(ev any) bool {
		e, ok := ev.(transfer.Event)
		return ok && e.Type == transfer.TransferChanged && e.Transfer.ID == id && e.Transfer.State == st
	}
}

// offer hace que A ofrezca los archivos a B y espera a que B reciba la oferta.
func offer(t *testing.T, a, b *node, paths ...string) store.Message {
	t.Helper()
	m, err := a.app.OfferFiles(context.Background(), b.app.Self().ID, paths)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B recibe la oferta", func(ev any) bool {
		e, ok := ev.(chat.Event)
		return ok && e.Type == chat.MessageReceived && e.Message.ID == m.ID && e.Message.Kind == store.KindFiles
	})
	return m
}

func TestFileTransferEndToEnd(t *testing.T) {
	a, b, downloads := pair(t)
	src := testutil.TempDir(t)
	big, bigData := writeRandom(t, src, "informe anual.pdf", 3<<20)
	small, smallData := writeRandom(t, src, "año–ñ.txt", 10)

	m := offer(t, a, b, big, small)
	ctx := context.Background()
	tr, ok, _ := b.app.Transfer(ctx, m.ID)
	if !ok || tr.State != store.TransferOffered || tr.Outgoing || len(tr.Files) != 2 || tr.TotalSize() != 3<<20+10 {
		t.Fatalf("oferta recibida: %+v", tr)
	}
	if !strings.HasPrefix(m.Body, "📎 2 archivos") {
		t.Errorf("resumen: %q", m.Body)
	}

	if err := b.app.AcceptTransfer(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B completa", transferState(m.ID, store.TransferCompleted))
	waitFor(t, a, "A ve completada", transferState(m.ID, store.TransferCompleted))

	for name, want := range map[string][]byte{"informe anual.pdf": bigData, "año–ñ.txt": smallData} {
		got, err := os.ReadFile(filepath.Join(downloads, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %d bytes, %v", name, len(got), err)
		}
	}
	tr, _, _ = b.app.Transfer(ctx, m.ID)
	if !tr.Files[0].Done || tr.Files[0].Path != filepath.Join(downloads, "informe anual.pdf") {
		t.Errorf("archivo guardado: %+v", tr.Files[0])
	}

	// Descarga única: con el mismo token ya no se puede volver a bajar.
	if code := getFile(t, a, m.ID, 0, tr.Token); code != http.StatusGone {
		t.Errorf("segunda descarga: %d, quería 410", code)
	}
	if code := getFile(t, a, m.ID, 0, "token-falso"); code != http.StatusForbidden {
		t.Errorf("token falso: %d, quería 403", code)
	}
	if err := b.app.AcceptTransfer(ctx, m.ID); err == nil {
		t.Error("aceptar otra vez una oferta completada debía fallar")
	}
}

func getFile(t *testing.T, sender *node, id string, idx int, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", sender.app.srv.Port(), protocol.PathFile(id, idx)), nil)
	req.Header.Set(protocol.TokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestRejectAndCancel(t *testing.T) {
	a, b, _ := pair(t)
	p, _ := writeRandom(t, testutil.TempDir(t), "x.bin", 100)
	ctx := context.Background()

	m1 := offer(t, a, b, p)
	if err := b.app.RejectTransfer(ctx, m1.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A ve el rechazo", transferState(m1.ID, store.TransferRejected))

	m2 := offer(t, a, b, p)
	if err := a.app.CancelTransfer(ctx, m2.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B ve la cancelación", transferState(m2.ID, store.TransferCanceled))
	if err := b.app.AcceptTransfer(ctx, m2.ID); err == nil {
		t.Error("aceptar una oferta cancelada debía fallar")
	}
}

// Un .part existente se reanuda; si su contenido no coincide con el original,
// el SHA-256 lo detecta, se descarta y el reintento baja todo de nuevo.
func TestResumeAndIntegrity(t *testing.T) {
	a, b, downloads := pair(t)
	p, data := writeRandom(t, testutil.TempDir(t), "video.mp4", 1<<20)
	ctx := context.Background()

	// Mismo formato que transfer.partPath.
	part := func(id string) string {
		return filepath.Join(downloads, fmt.Sprintf("video.mp4.%s-0.lanchat-part", id[:8]))
	}

	m := offer(t, a, b, p)
	os.WriteFile(part(m.ID), data[:400_000], 0o600) // mitad buena: se reanuda
	if err := b.app.AcceptTransfer(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B completa reanudando", transferState(m.ID, store.TransferCompleted))
	if got, _ := os.ReadFile(filepath.Join(downloads, "video.mp4")); !bytes.Equal(got, data) {
		t.Fatal("el archivo reanudado no es igual al original")
	}

	m2 := offer(t, a, b, p)
	bad := bytes.Clone(data[:400_000])
	bad[123] ^= 0xff
	os.WriteFile(part(m2.ID), bad, 0o600) // mitad dañada
	if err := b.app.AcceptTransfer(ctx, m2.ID); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, b, "B detecta el daño", transferState(m2.ID, store.TransferFailed)).(transfer.Event)
	if !strings.Contains(ev.Transfer.Error, "SHA-256") {
		t.Errorf("error: %q", ev.Transfer.Error)
	}
	if err := b.app.AcceptTransfer(ctx, m2.ID); err != nil { // reintento
		t.Fatal(err)
	}
	waitFor(t, b, "B completa al reintentar", transferState(m2.ID, store.TransferCompleted))
	if got, _ := os.ReadFile(filepath.Join(downloads, "video (1).mp4")); !bytes.Equal(got, data) {
		t.Fatal("el reintento no bajó el archivo correcto")
	}
}

func TestSourceChangedAfterOffer(t *testing.T) {
	a, b, _ := pair(t)
	p, _ := writeRandom(t, testutil.TempDir(t), "doc.txt", 50)
	m := offer(t, a, b, p)
	os.WriteFile(p, []byte("otro contenido"), 0o600)
	if err := b.app.AcceptTransfer(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, b, "B ve el error", transferState(m.ID, store.TransferFailed)).(transfer.Event)
	if !strings.Contains(ev.Transfer.Error, "cambió") {
		t.Errorf("error: %q", ev.Transfer.Error)
	}
}

func TestUploadStagesAndCleansUp(t *testing.T) {
	a, b, downloads := pair(t)
	ctx := context.Background()
	files := []struct{ name, body string }{{"nota.txt", "hola"}, {"nota.txt", "otra"}}
	i := 0
	m, err := a.app.Upload(ctx, b.app.Self().ID, func() (string, io.Reader, error) {
		if i == len(files) {
			return "", nil, io.EOF
		}
		f := files[i]
		i++
		return f.name, strings.NewReader(f.body), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B recibe la oferta", func(ev any) bool {
		e, ok := ev.(chat.Event)
		return ok && e.Type == chat.MessageReceived && e.Message.ID == m.ID
	})
	if err := b.app.AcceptTransfer(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A completa", transferState(m.ID, store.TransferCompleted))
	for name, want := range map[string]string{"nota.txt": "hola", "nota (1).txt": "otra"} {
		if got, _ := os.ReadFile(filepath.Join(downloads, name)); string(got) != want {
			t.Errorf("%s = %q", name, got)
		}
	}
	staging := filepath.Join(a.app.dir, stagingDirName)
	if entries, _ := os.ReadDir(staging); len(entries) != 0 {
		t.Errorf("quedaron copias temporales: %v", entries)
	}
}
