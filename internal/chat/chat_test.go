package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

type noPeers struct{}

func (noPeers) Peer(string) (discovery.Peer, bool) { return discovery.Peer{}, false }

func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Identity{ID: "yo", Hostname: "PC-YO", Name: func() string { return "" }},
		st, noPeers{}, http.DefaultClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		s.Close()
		st.Close()
	})
	return s
}

func post(t *testing.T, s *Service, m wireMessage) int {
	t.Helper()
	b, _ := json.Marshal(m)
	req := httptest.NewRequest(http.MethodPost, protocol.RouteMessage, strings.NewReader(string(b)))
	req.RemoteAddr = "192.168.1.30:51234"
	rec := httptest.NewRecorder()
	s.handleMsg(rec, req)
	return rec.Code
}

func TestHandleMsgDeduplicates(t *testing.T) {
	s := newTestService(t)
	m := wireMessage{ID: "m1", From: "otro", FromName: "Ana", FromHost: "PC-ANA", Body: "hola", SentAt: time.Now().UnixMilli()}

	for range 2 {
		if code := post(t, s, m); code != http.StatusNoContent {
			t.Fatalf("código %d, quería 204", code)
		}
	}
	select {
	case ev := <-s.Events():
		if ev.Type != MessageReceived || ev.Message.Body != "hola" || ev.Message.PeerID != "otro" {
			t.Errorf("evento inesperado: %+v", ev)
		}
	default:
		t.Fatal("no llegó el evento del primer mensaje")
	}
	select {
	case ev := <-s.Events():
		t.Errorf("el reenvío no debía generar evento: %+v", ev)
	default:
	}

	p, ok, err := s.store.Peer(context.Background(), "otro")
	if err != nil || !ok || p.IP != "192.168.1.30" || p.Name != "Ana" {
		t.Errorf("remitente guardado: %+v %v %v", p, ok, err)
	}
}

func TestHandleMsgRejectsInvalid(t *testing.T) {
	s := newTestService(t)
	valid := wireMessage{ID: "m1", From: "otro", Body: "hola"}
	cases := map[string]func(*wireMessage){
		"sin id":            func(m *wireMessage) { m.ID = "" },
		"de mí mismo":       func(m *wireMessage) { m.From = "yo" },
		"vacío":             func(m *wireMessage) { m.Body = "  " },
		"escape de consola": func(m *wireMessage) { m.Body = "\x1b[2Jborrado" },
		"nombre con salto":  func(m *wireMessage) { m.FromName = "Ana\nFalsa" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid
			mutate(&m)
			if code := post(t, s, m); code != http.StatusBadRequest {
				t.Errorf("código %d, quería 400", code)
			}
		})
	}
}
