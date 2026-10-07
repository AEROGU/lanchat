package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	// Ruta con espacios y acentos, como suele pasar en %APPDATA%.
	dir := filepath.Join(t.TempDir(), "carpeta con espacios ñ")
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
