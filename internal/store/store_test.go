package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/testutil"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	// Ruta con espacios y acentos, como suele pasar en %APPDATA%.
	dir := filepath.Join(testutil.TempDir(t), "carpeta con espacios ñ")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(dir, "lanchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPeersKeepAlias(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	p := Peer{ID: "a", Name: "Juan", Hostname: "PC-A", IP: "192.168.1.2", LastSeen: time.Now()}
	if err := s.UpsertPeer(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlias(ctx, "a", "Contabilidad"); err != nil {
		t.Fatal(err)
	}
	p.IP = "192.168.1.3"
	if err := s.UpsertPeer(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Peer(ctx, "a")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got.Alias != "Contabilidad" || got.IP != "192.168.1.3" {
		t.Errorf("got %+v", got)
	}
	if err := s.SetAlias(ctx, "nadie", "x"); err == nil {
		t.Error("alias a contacto inexistente debía fallar")
	}
}

func TestMessagesPendingAndHistory(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.UnixMilli(1_700_000_000_000)

	for i, id := range []string{"m1", "m2", "m3"} {
		m := Message{ID: id, PeerID: "a", Outgoing: true, Body: id,
			At: base.Add(time.Duration(i) * time.Second), SentAt: base, Status: StatusPending}
		if ok, err := s.InsertMessage(ctx, m); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if ok, _ := s.InsertMessage(ctx, Message{ID: "m1", PeerID: "a"}); ok {
		t.Error("un ID repetido no debe insertarse")
	}
	if err := s.MarkDelivered(ctx, "m1"); err != nil {
		t.Fatal(err)
	}

	pend, err := s.Pending(ctx, "a")
	if err != nil || len(pend) != 2 || pend[0].ID != "m2" || pend[1].ID != "m3" {
		t.Fatalf("pending = %+v, %v", pend, err)
	}
	peers, _ := s.PeersWithPending(ctx)
	if len(peers) != 1 || peers[0] != "a" {
		t.Errorf("PeersWithPending = %v", peers)
	}

	h, err := s.History(ctx, "a", "", 2)
	if err != nil || len(h) != 2 || h[0].ID != "m2" || h[1].ID != "m3" {
		t.Fatalf("últimos 2 = %+v, %v", h, err)
	}
	h, _ = s.History(ctx, "a", h[0].ID, 10)
	if len(h) != 1 || h[0].ID != "m1" || h[0].Status != StatusDelivered {
		t.Fatalf("página anterior = %+v", h)
	}
}

func TestUnread(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	msgs := []Message{
		{ID: "1", PeerID: "a", Body: "x", At: now, SentAt: now, Status: StatusDelivered, Unread: true},
		{ID: "2", PeerID: "a", Body: "x", At: now, SentAt: now, Status: StatusDelivered, Unread: true},
		{ID: "3", PeerID: "b", Body: "x", At: now, SentAt: now, Status: StatusDelivered, Unread: true},
		{ID: "4", PeerID: "b", Body: "x", At: now, SentAt: now, Outgoing: true},
	}
	for _, m := range msgs {
		if _, err := s.InsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	counts, err := s.UnreadCounts(ctx)
	if err != nil || counts["a"] != 2 || counts["b"] != 1 {
		t.Fatalf("counts = %v, %v", counts, err)
	}
	if changed, err := s.MarkRead(ctx, "a", true); err != nil || !changed {
		t.Fatalf("MarkRead = %v, %v", changed, err)
	}
	if changed, _ := s.MarkRead(ctx, "a", true); changed {
		t.Error("la segunda vez no debía cambiar nada")
	}
	counts, _ = s.UnreadCounts(ctx)
	if _, ok := counts["a"]; ok || counts["b"] != 1 {
		t.Errorf("counts tras leer = %v", counts)
	}
	h, _ := s.History(ctx, "b", "", 10)
	if len(h) != 2 || !h[0].Unread && !h[1].Unread {
		t.Errorf("historial debía conservar unread: %+v", h)
	}
}

func TestTransfers(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	mod := time.Unix(1_700_000_000, 123456789)
	tr := Transfer{
		ID: "o1", PeerID: "a", Outgoing: true, State: TransferOffered, Token: "tok",
		ExpiresAt: now.Add(time.Hour),
		Files: []TransferFile{
			{Index: 0, Name: "informe.pdf", Size: 1000, ModTime: mod, Path: `C:\docs\informe.pdf`},
			{Index: 1, Name: "foto.jpg", Size: 2000, Path: `C:\docs\foto.jpg`},
		},
	}
	m := Message{ID: "o1", PeerID: "a", Outgoing: true, Body: "2 archivos", At: now, SentAt: now, Kind: KindFiles}
	if ok, err := s.InsertMessageWithTransfer(ctx, m, &tr); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Repetir el mensaje no debe duplicar ni fallar por la transferencia.
	if ok, err := s.InsertMessageWithTransfer(ctx, m, &tr); err != nil || ok {
		t.Fatalf("repetido: %v %v", ok, err)
	}

	got, ok, err := s.Transfer(ctx, "o1")
	if err != nil || !ok || got.TotalSize() != 3000 || len(got.Files) != 2 || !got.Files[0].ModTime.Equal(mod) {
		t.Fatalf("Transfer = %+v %v %v", got, ok, err)
	}
	if h, _ := s.History(ctx, "a", "", 1); len(h) != 1 || h[0].Kind != KindFiles {
		t.Errorf("el mensaje debía ser KindFiles: %+v", h)
	}

	if err := s.SetTransferState(ctx, "o1", TransferDownloading, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFileDone(ctx, "o1", 1, ""); err != nil {
		t.Fatal(err)
	}
	active, err := s.TransfersInState(ctx, true, TransferOffered, TransferDownloading)
	if err != nil || len(active) != 1 || !active[0].Files[1].Done || active[0].Files[1].Path != `C:\docs\foto.jpg` {
		t.Fatalf("activas = %+v, %v", active, err)
	}
	byID, err := s.TransfersByID(ctx, []string{"o1", "nada"})
	if err != nil || len(byID) != 1 || byID["o1"].State != TransferDownloading {
		t.Errorf("TransfersByID = %+v, %v", byID, err)
	}
	if err := s.SetTransferState(ctx, "nada", TransferCanceled, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("estado de una transferencia inexistente: %v", err)
	}
}

// Mensajes en el mismo milisegundo no deben perderse al paginar.
func TestHistorySameMillisecond(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := time.UnixMilli(1_700_000_000_000)
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		if _, err := s.InsertMessage(ctx, Message{ID: id, PeerID: "p", Body: id, At: at, SentAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	before := ""
	for {
		page, err := s.History(ctx, "p", before, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for i := len(page) - 1; i >= 0; i-- {
			got = append(got, page[i].ID)
		}
		before = page[0].ID
	}
	if strings.Join(got, ",") != "a4,a3,a2,a1" {
		t.Errorf("paginado = %v", got)
	}
}

func TestReadReceipts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	for _, m := range []Message{
		{ID: "in1", PeerID: "a", Body: "x", At: now, SentAt: now, Unread: true},
		{ID: "in2", PeerID: "a", Body: "x", At: now, SentAt: now, Unread: true},
		{ID: "out1", PeerID: "a", Outgoing: true, Body: "x", At: now, SentAt: now},
		{ID: "out2", PeerID: "b", Outgoing: true, Body: "x", At: now, SentAt: now},
	} {
		if _, err := s.InsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	// Leer con avisos activados deja los avisos pendientes.
	s.MarkRead(ctx, "a", true)
	ids, _ := s.PendingReceipts(ctx, "a", 10)
	peers, _ := s.PeersWithPendingReceipts(ctx)
	if strings.Join(ids, ",") != "in1,in2" || len(peers) != 1 {
		t.Fatalf("pendientes = %v, %v", ids, peers)
	}
	s.ClearReceipts(ctx, ids)
	if ids, _ := s.PendingReceipts(ctx, "a", 10); len(ids) != 0 {
		t.Errorf("tras limpiar quedan %v", ids)
	}

	// "a" avisa que leyó: solo cambia lo que le enviamos a "a".
	at := time.UnixMilli(1_700_000_000_000)
	got, err := s.MarkReadByPeer(ctx, "a", []string{"out1", "out2", "in1"}, at)
	if err != nil || len(got) != 1 || got[0].ID != "out1" || !got[0].ReadAt.Equal(at) {
		t.Fatalf("MarkReadByPeer = %+v, %v", got, err)
	}
	if h, _ := s.History(ctx, "b", "", 1); !h[0].ReadAt.IsZero() {
		t.Error("no debía marcar mensajes de otro contacto")
	}
}

func TestGroups(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	s.UpsertPeer(ctx, Peer{ID: "a", Hostname: "PC-A"})
	if err := s.SetGroup(ctx, "a", "Ventas"); err != nil {
		t.Fatal(err)
	}
	s.UpsertPeer(ctx, Peer{ID: "a", Hostname: "PC-A2"}) // un anuncio no borra el grupo
	if p, _, _ := s.Peer(ctx, "a"); p.Group != "Ventas" || p.Hostname != "PC-A2" {
		t.Errorf("peer = %+v", p)
	}
	if err := s.SetGroup(ctx, "nadie", "x"); err == nil {
		t.Error("grupo a un contacto inexistente debía fallar")
	}
}

func TestFingerprintPinning(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	s.UpsertPeer(ctx, Peer{ID: "a", Fingerprint: "huella-1"})
	s.UpsertPeer(ctx, Peer{ID: "a", Fingerprint: "huella-2"}) // un anuncio no la cambia
	if p, _, _ := s.Peer(ctx, "a"); p.Fingerprint != "huella-1" {
		t.Fatalf("huella fijada = %q", p.Fingerprint)
	}
	if err := s.SetFingerprint(ctx, "a", "huella-2"); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := s.Peer(ctx, "a"); p.Fingerprint != "huella-2" {
		t.Errorf("tras confiar = %q", p.Fingerprint)
	}
}
