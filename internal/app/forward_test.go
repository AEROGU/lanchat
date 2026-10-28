package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/testutil"
)

// received espera a que llegue un mensaje que cumpla match.
func received(match func(store.Message) bool) func(any) bool {
	return func(ev any) bool {
		e, ok := ev.(chat.Event)
		return ok && e.Type == chat.MessageReceived && match(e.Message)
	}
}

func TestEditMessage(t *testing.T) {
	ctx := context.Background()
	dirB := testutil.TempDir(t)
	a := startNode(t, testutil.TempDir(t), "PC-A")
	b := startNode(t, dirB, "PC-B", a.udpPort())
	idA, idB := a.app.Self().ID, b.app.Self().ID
	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, idB))
	waitFor(t, b, "B ve a A", peerEvent(discovery.PeerOnline, idA))

	m, err := a.app.Send(ctx, idB, "nos vemos a las 5")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B recibe", msgEvent(chat.MessageReceived, m.ID))
	if _, err := b.app.EditMessage(ctx, m.ID, "cambiado por B"); err == nil {
		t.Error("B no debía poder editar un mensaje de A")
	}
	edited, err := a.app.EditMessage(ctx, m.ID, "nos vemos a las 6")
	if err != nil || edited.Body != "nos vemos a las 6" || edited.EditedAt.IsZero() {
		t.Fatalf("editar: %+v, %v", edited, err)
	}
	ev := waitFor(t, b, "B ve la edición", msgEvent(chat.MessageEdited, m.ID)).(chat.Event)
	if ev.Message.Body != "nos vemos a las 6" || ev.Message.EditedAt.IsZero() {
		t.Errorf("evento en B: %+v", ev.Message)
	}

	// B desconectado: la edición le llega al volver, igual que un mensaje
	// pendiente, que llega ya editado.
	b.stop()
	waitFor(t, a, "A ve salir a B", peerEvent(discovery.PeerOffline, idB))
	if _, err := a.app.EditMessage(ctx, m.ID, "nos vemos a las 7"); err != nil {
		t.Fatal(err)
	}
	pending, _ := a.app.Send(ctx, idB, "llevo el proyector")
	if _, err := a.app.EditMessage(ctx, pending.ID, "llevo el proyector y el cable"); err != nil {
		t.Fatal(err)
	}
	b2 := startNode(t, dirB, "PC-B", a.udpPort())
	waitFor(t, b2, "B recibe la edición atrasada", msgEvent(chat.MessageEdited, m.ID))
	waitFor(t, b2, "B recibe el pendiente editado", msgEvent(chat.MessageEdited, pending.ID))
	for id, want := range map[string]string{m.ID: "nos vemos a las 7", pending.ID: "llevo el proyector y el cable"} {
		got, _, _ := b2.app.store.Message(ctx, id)
		if got.Body != want || got.EditedAt.IsZero() {
			t.Errorf("en B: %+v, quería %q editado", got, want)
		}
	}
}

func TestForward(t *testing.T) {
	ctx := context.Background()
	a, b, downloads := pair(t)
	idA, idB := a.app.Self().ID, b.app.Self().ID
	if err := a.app.SetDownloadDir(testutil.TempDir(t)); err != nil {
		t.Fatal(err)
	}

	// B reenvía a A un texto y unos archivos que recibió de A.
	text, _ := a.app.Send(ctx, idB, "el informe va adjunto")
	waitFor(t, b, "B recibe el texto", msgEvent(chat.MessageReceived, text.ID))
	src := testutil.TempDir(t)
	p, data := writeRandom(t, src, "informe.pdf", 1000)
	files := offer(t, a, b, p)
	if _, err := b.app.Forward(ctx, []string{files.ID}, []string{idA}, nil); err == nil ||
		!strings.Contains(err.Error(), "no se descargó") {
		t.Errorf("reenviar archivos sin descargar: %v", err)
	}
	// Ya existe uno con ese nombre: se guarda como "informe (1).pdf" y se
	// reenvía con su nombre original.
	os.WriteFile(filepath.Join(downloads, "informe.pdf"), []byte("otro"), 0o600)
	if err := b.app.AcceptTransfer(ctx, files.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B descarga", transferState(files.ID, store.TransferCompleted))

	sent, err := b.app.Forward(ctx, []string{files.ID, text.ID}, []string{idA}, nil)
	if err != nil || len(sent) != 2 || !sent[0].Forwarded || sent[0].Kind != store.KindText || sent[1].Kind != store.KindFiles {
		t.Fatalf("reenviar (en el orden de la conversación): %+v, %v", sent, err)
	}
	waitFor(t, a, "A recibe el texto reenviado", received(func(m store.Message) bool {
		return m.ID == sent[0].ID && m.Forwarded && m.Body == "el informe va adjunto"
	}))
	waitFor(t, a, "A recibe los archivos reenviados", received(func(m store.Message) bool {
		return m.ID == sent[1].ID && m.Forwarded && m.Kind == store.KindFiles
	}))
	tr, _, _ := a.app.Transfer(ctx, sent[1].ID)
	if len(tr.Files) != 1 || tr.Files[0].Name != "informe.pdf" {
		t.Fatalf("oferta reenviada: %+v", tr)
	}
	if err := a.app.AcceptTransfer(ctx, sent[1].ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A descarga lo reenviado", transferState(sent[1].ID, store.TransferCompleted))
	got, _, _ := a.app.Transfer(ctx, sent[1].ID)
	if content, err := os.ReadFile(got.Files[0].Path); err != nil || string(content) != string(data) {
		t.Errorf("archivo reenviado: %d bytes, %v", len(content), err)
	}

	// A una sala no se envían archivos; el texto, sí.
	room, err := b.app.CreateRoom(ctx, "Proyecto", []string{idA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.app.Forward(ctx, []string{files.ID}, nil, []string{room.ID}); err == nil {
		t.Error("reenviar archivos a una sala debía fallar")
	}
	if sent, err := b.app.Forward(ctx, []string{text.ID}, nil, []string{room.ID}); err != nil || len(sent) != 1 || sent[0].RoomID != room.ID {
		t.Errorf("reenviar texto a la sala: %+v, %v", sent, err)
	} else {
		waitFor(t, a, "A recibe el reenvío en la sala", received(func(m store.Message) bool {
			return m.ID == sent[0].ID && m.RoomID == room.ID && m.Forwarded
		}))
	}

	// Si el archivo ya no está en la carpeta de descargas, no se reenvía nada.
	recv, _, _ := b.app.Transfer(ctx, files.ID)
	os.Remove(recv.Files[0].Path)
	if _, err := b.app.Forward(ctx, []string{text.ID, files.ID}, []string{idA}, nil); err == nil ||
		!strings.Contains(err.Error(), "carpeta de descargas de LanChat") {
		t.Errorf("reenviar un archivo borrado: %v", err)
	}
	if _, err := b.app.Forward(ctx, nil, []string{idA}, nil); err == nil {
		t.Error("sin mensajes debía fallar")
	}
	if _, err := b.app.Forward(ctx, []string{text.ID}, nil, nil); err == nil {
		t.Error("sin destinos debía fallar")
	}
}

// Un archivo arrastrado a la ventana solo existe como copia temporal de su
// oferta: al reenviarlo se copia otra vez, porque cada oferta borra la suya al
// terminar.
func TestForwardStagedFiles(t *testing.T) {
	ctx := context.Background()
	a, b, downloads := pair(t)
	idB := b.app.Self().ID
	done := false
	up, err := a.app.Upload(ctx, idB, func() (string, string, io.Reader, error) {
		if done {
			return "", "", nil, io.EOF
		}
		done = true
		return "nota.txt", "", strings.NewReader("hola"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B recibe la oferta", msgEvent(chat.MessageReceived, up.ID))
	fw, err := a.app.Forward(ctx, []string{up.ID}, []string{idB}, nil)
	if err != nil || len(fw) != 1 {
		t.Fatalf("reenviar: %+v, %v", fw, err)
	}
	waitFor(t, b, "B recibe el reenvío", msgEvent(chat.MessageReceived, fw[0].ID))

	// La original termina y borra su copia; la reenviada conserva la suya.
	if err := b.app.AcceptTransfer(ctx, up.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A completa la original", transferState(up.ID, store.TransferCompleted))
	if err := b.app.AcceptTransfer(ctx, fw[0].ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B descarga el reenvío", transferState(fw[0].ID, store.TransferCompleted))
	for _, name := range []string{"nota.txt", "nota (1).txt"} {
		if got, _ := os.ReadFile(filepath.Join(downloads, name)); string(got) != "hola" {
			t.Errorf("%s = %q", name, got)
		}
	}
	// Terminadas las dos, ya no queda copia: no se puede reenviar otra vez.
	waitFor(t, a, "A completa el reenvío", transferState(fw[0].ID, store.TransferCompleted))
	if _, err := a.app.Forward(ctx, []string{up.ID}, []string{idB}, nil); err == nil ||
		!strings.Contains(err.Error(), "ya no está donde estaba") {
		t.Errorf("reenviar sin la copia temporal: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(a.app.dir, stagingDirName)); len(entries) != 0 {
		t.Errorf("quedaron copias temporales: %v", entries)
	}
}
