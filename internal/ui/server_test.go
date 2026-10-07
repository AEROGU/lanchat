package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

// fakeBackend simula la aplicación con un solo contacto.
type fakeBackend struct {
	mu     sync.Mutex
	sent   []string
	unread int
	name   string
	manual []string
}

var testContact = app.Contact{ID: "c1", Hostname: "PC-ANA", IP: "192.168.1.30", Online: true}

func (f *fakeBackend) Self() app.Self {
	f.mu.Lock()
	defer f.mu.Unlock()
	return app.Self{ID: "yo", Name: f.name, Hostname: "PC-YO"}
}
func (f *fakeBackend) Contacts(context.Context) ([]app.Contact, error) {
	return []app.Contact{f.contact()}, nil
}
func (f *fakeBackend) Contact(_ context.Context, id string) (app.Contact, bool, error) {
	return f.contact(), id == testContact.ID, nil
}
func (f *fakeBackend) contact() app.Contact {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := testContact
	c.Unread = f.unread
	return c
}
func (f *fakeBackend) TotalUnread(context.Context) (int, error) { return f.contact().Unread, nil }
func (f *fakeBackend) MarkRead(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := f.unread > 0
	f.unread = 0
	return changed, nil
}
func (f *fakeBackend) Send(_ context.Context, peer, body string) (store.Message, error) {
	if err := protocol.ValidateMessage(body); err != nil {
		return store.Message{}, err
	}
	f.mu.Lock()
	f.sent = append(f.sent, body)
	f.mu.Unlock()
	return store.Message{ID: "m1", PeerID: peer, Outgoing: true, Body: body, At: time.Now()}, nil
}
func (f *fakeBackend) History(context.Context, string, string, int) ([]store.Message, error) {
	return nil, nil
}
func (f *fakeBackend) SetName(name string) error {
	if err := protocol.ValidateName(name); err != nil {
		return err
	}
	f.mu.Lock()
	f.name = name
	f.mu.Unlock()
	return nil
}
func (f *fakeBackend) SetAlias(context.Context, string, string) error { return nil }
func (f *fakeBackend) ManualPeers() []string                          { return f.manual }
func (f *fakeBackend) SetManualPeers(p []string) error {
	if len(p) > 0 && p[0] == "malo" {
		return errors.New("entrada inválida")
	}
	f.manual = p
	return nil
}

func startServer(t *testing.T) (*Server, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{}
	s, err := Listen(b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	return s, b
}

// loggedClient sigue el enlace de arranque y queda con la cookie de sesión.
func loggedClient(t *testing.T, s *Server) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	resp, err := c.Get(s.LaunchURL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.RawQuery != "" {
		t.Fatalf("el arranque debía redirigir a / sin token: %d %s", resp.StatusCode, resp.Request.URL)
	}
	return c
}

func base(s *Server) string { return "http://" + s.host }

func postJSON(c *http.Client, url, body string) (*http.Response, error) {
	return c.Post(url, "application/json", strings.NewReader(body))
}

func TestAuth(t *testing.T) {
	s, _ := startServer(t)
	plain := &http.Client{Timeout: 5 * time.Second}

	// Sin token no hay acceso, ni a la página ni a la API.
	for _, path := range []string{"/", "/api/state", "/app.js"} {
		resp, err := plain.Get(base(s) + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s sin token: %d", path, resp.StatusCode)
		}
	}

	// Token equivocado.
	resp, _ := plain.Get(base(s) + "/?t=equivocado")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("token equivocado: %d", resp.StatusCode)
	}

	// Con la cookie de sesión, sí.
	c := loggedClient(t, s)
	resp, _ = c.Get(base(s) + "/api/state")
	var st stateJSON
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(st.Contacts) != 1 || st.Self.Hostname != "PC-YO" {
		t.Errorf("estado: %d %+v", resp.StatusCode, st)
	}

	// Una segunda instancia usa el encabezado Authorization.
	opened := make(chan struct{})
	s.OnOpen = func() { close(opened) }
	req, _ := http.NewRequest(http.MethodPost, base(s)+"/api/open", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token())
	resp, _ = plain.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("open: %d", resp.StatusCode)
	}
	select {
	case <-opened:
	case <-time.After(2 * time.Second):
		t.Error("no se llamó OnOpen")
	}
}

func TestGuardRejectsCrossSite(t *testing.T) {
	s, b := startServer(t)
	c := loggedClient(t, s)

	// DNS rebinding: otro nombre de host que apunte a 127.0.0.1.
	req, _ := http.NewRequest(http.MethodGet, base(s)+"/api/state", nil)
	req.Host = "malicioso.example:1234"
	resp, _ := c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("host ajeno: %d", resp.StatusCode)
	}

	// Formulario de otra página (no JSON).
	resp, _ = c.Post(base(s)+"/api/messages", "application/x-www-form-urlencoded", strings.NewReader("peer=c1&body=x"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("formulario: %d", resp.StatusCode)
	}

	// JSON desde otro origen.
	req, _ = http.NewRequest(http.MethodPost, base(s)+"/api/messages", strings.NewReader(`{"peer":"c1","body":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://malicioso.example")
	resp, _ = c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("otro origen: %d", resp.StatusCode)
	}
	if len(b.sent) != 0 {
		t.Errorf("no debía enviarse nada: %v", b.sent)
	}
}

func TestSendAndErrors(t *testing.T) {
	s, b := startServer(t)
	c := loggedClient(t, s)

	resp, _ := postJSON(c, base(s)+"/api/messages", `{"peer":"c1","body":"hola"}`)
	var m messageJSON
	json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || m.Body != "hola" || m.Status != "pending" || len(b.sent) != 1 {
		t.Errorf("envío: %d %+v %v", resp.StatusCode, m, b.sent)
	}

	resp, _ = postJSON(c, base(s)+"/api/name", `{"name":"`+strings.Repeat("a", protocol.MaxNameLen+1)+`"}`)
	var e map[string]string
	json.NewDecoder(resp.Body).Decode(&e)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || e["error"] == "" {
		t.Errorf("nombre largo: %d %v", resp.StatusCode, e)
	}

	resp, _ = postJSON(c, base(s)+"/api/manual-peers", `{"peers":["malo"]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("equipo manual inválido: %d", resp.StatusCode)
	}
}

func TestEventsAndUnread(t *testing.T) {
	s, b := startServer(t)
	c := loggedClient(t, s)
	b.unread = 2
	totals := make(chan int, 4)
	s.OnUnreadChanged = func(n int) { totals <- n }

	resp, err := c.Get(base(s) + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	waitFor := func(cond func() bool) {
		deadline := time.Now().Add(2 * time.Second)
		for !cond() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(s.HasWindow)
	if !s.HasWindow() {
		t.Fatal("la ventana no quedó registrada")
	}

	s.Publish(context.Background(), chat.Event{Type: chat.MessageReceived,
		Message: store.Message{ID: "m9", PeerID: "c1", Body: "¿comemos?", At: time.Now()}})

	events := readEvents(t, resp.Body, 2)
	if events[0].name != "message" || !strings.Contains(events[0].data, "¿comemos?") {
		t.Errorf("primer evento: %+v", events[0])
	}
	if events[1].name != "contact" || !strings.Contains(events[1].data, `"unread":2`) {
		t.Errorf("segundo evento: %+v", events[1])
	}
	if n := <-totals; n != 2 {
		t.Errorf("total no leídos = %d", n)
	}

	// Notificar solo si no está viendo esa conversación con la ventana enfocada.
	postJSON(c, base(s)+"/api/presence", `{"focused":true,"viewing":"c1"}`)
	if s.ShouldNotify("c1") || !s.ShouldNotify("otro") {
		t.Error("ShouldNotify con la conversación abierta y enfocada")
	}

	resp2, _ := postJSON(c, base(s)+"/api/read", `{"peer":"c1"}`)
	resp2.Body.Close()
	if n := <-totals; n != 0 {
		t.Errorf("tras leer, total = %d", n)
	}
}

type rawEvent struct{ name, data string }

func readEvents(t *testing.T, r io.Reader, n int) []rawEvent {
	t.Helper()
	var out []rawEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(r)
		var cur rawEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case line == "" && cur.name != "":
				out = append(out, cur)
				cur = rawEvent{}
				if len(out) == n {
					return
				}
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("solo llegaron %d de %d eventos", len(out), n)
	}
	return out
}

func TestEncodeICO(t *testing.T) {
	ico := iconICO(true)
	if len(ico) < 6 || ico[2] != 1 || int(ico[4]) != len(trayIconSizes) {
		t.Fatalf("cabecera ICO inválida: % x", ico[:6])
	}
	if png := iconPNG(faviconSize, false); string(png[1:4]) != "PNG" {
		t.Error("iconPNG no es PNG")
	}
}
