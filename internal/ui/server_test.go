package ui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
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

// Archivos: una oferta "t1" recibida y completada, y una "t2" enviada.
var testTransfers = map[string]store.Transfer{
	"t1": {ID: "t1", PeerID: "c1", State: store.TransferCompleted,
		Files: []store.TransferFile{{Index: 0, Name: "a.pdf", Size: 3, Done: true, Path: `C:\no\existe\a (1).pdf`}}},
	"t2": {ID: "t2", PeerID: "c1", Outgoing: true, State: store.TransferOffered,
		Files: []store.TransferFile{{Index: 0, Name: "b.txt", Size: 5, Path: `C:\docs\b.txt`}}},
}

func (f *fakeBackend) OfferFiles(context.Context, string, []string) (store.Message, error) {
	return store.Message{}, errors.New("no usado")
}
func (f *fakeBackend) Upload(_ context.Context, peer string, next func() (string, io.Reader, error)) (store.Message, error) {
	var names []string
	for {
		name, r, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return store.Message{}, err
		}
		b, _ := io.ReadAll(r)
		names = append(names, name+"="+string(b))
	}
	f.mu.Lock()
	f.sent = append(f.sent, names...)
	f.mu.Unlock()
	return store.Message{ID: "t2", PeerID: peer, Outgoing: true, Body: "📎", Kind: store.KindFiles}, nil
}
func (f *fakeBackend) record(action, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, action+":"+id)
	return nil
}
func (f *fakeBackend) AcceptTransfer(_ context.Context, id string) error {
	return f.record("accept", id)
}
func (f *fakeBackend) RejectTransfer(_ context.Context, id string) error {
	return f.record("reject", id)
}
func (f *fakeBackend) CancelTransfer(_ context.Context, id string) error {
	return f.record("cancel", id)
}
func (f *fakeBackend) Transfer(_ context.Context, id string) (store.Transfer, bool, error) {
	t, ok := testTransfers[id]
	return t, ok, nil
}
func (f *fakeBackend) TransfersByID(_ context.Context, ids []string) (map[string]store.Transfer, error) {
	out := map[string]store.Transfer{}
	for _, id := range ids {
		if t, ok := testTransfers[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}
func (f *fakeBackend) DownloadDir() string         { return `C:\Descargas\LanChat` }
func (f *fakeBackend) SetDownloadDir(string) error { return nil }

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

func TestFileRoutes(t *testing.T) {
	s, b := startServer(t)
	c := loggedClient(t, s)

	// Acciones sobre transferencias.
	for _, action := range []string{"accept", "reject", "cancel"} {
		resp, _ := postJSON(c, base(s)+"/api/transfers/"+action, `{"id":"t1"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("%s: %d", action, resp.StatusCode)
		}
	}
	resp, _ := postJSON(c, base(s)+"/api/transfers/borrar", `{"id":"t1"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("acción desconocida: %d", resp.StatusCode)
	}

	// Solo se abren archivos recibidos y completados.
	for _, body := range []string{`{"id":"t2","index":0}`, `{"id":"t1","index":5}`, `{"id":"nada","index":0}`} {
		resp, _ := postJSON(c, base(s)+"/api/files/open", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("abrir %s: %d", body, resp.StatusCode)
		}
	}

	// Subida multipart: permitida solo en su ruta.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", "nota.txt")
	fw.Write([]byte("hola"))
	mw.Close()
	resp, _ = c.Post(base(s)+uploadPath+"?peer=c1", mw.FormDataContentType(), bytes.NewReader(buf.Bytes()))
	var m messageJSON
	json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || m.Kind != "files" || m.Transfer == nil || m.Transfer.Files[0].Name != "b.txt" {
		t.Errorf("subida: %d %+v", resp.StatusCode, m)
	}
	resp, _ = c.Post(base(s)+"/api/messages", mw.FormDataContentType(), bytes.NewReader(buf.Bytes()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("multipart fuera de la subida: %d", resp.StatusCode)
	}

	want := []string{"accept:t1", "reject:t1", "cancel:t1", "nota.txt=hola"}
	if strings.Join(b.sent, ",") != strings.Join(want, ",") {
		t.Errorf("acciones = %v", b.sent)
	}
}

func TestTransferJSON(t *testing.T) {
	tj := toTransferJSON(testTransfers["t1"])
	if tj.State != "completed" || tj.Files[0].SavedName != "a (1).pdf" || tj.Total != 3 {
		t.Errorf("recibida: %+v", tj)
	}
	if tj := toTransferJSON(testTransfers["t2"]); tj.Files[0].SavedName != "" {
		t.Errorf("la enviada no debe exponer su ruta: %+v", tj)
	}
}

func TestSplitMultiSelect(t *testing.T) {
	cases := map[string][]string{
		"C:\\docs\\a.pdf\x00\x00":              {`C:\docs\a.pdf`},
		"C:\\docs\x00a.pdf\x00b c.txt\x00\x00": {`C:\docs\a.pdf`, `C:\docs\b c.txt`},
		"C:\\\x00a.pdf\x00\x00":                {`C:\a.pdf`},
		"\x00\x00":                             nil,
	}
	for in, want := range cases {
		if got := splitMultiSelect(in); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%q: %v, quería %v", in, got, want)
		}
	}
}

func (f *fakeBackend) SetStatus(status, text string) error {
	if status != protocol.NormalizeStatus(status) {
		return errors.New("estado desconocido")
	}
	return nil
}
func (f *fakeBackend) SetAutoAway(bool) error { return nil }

func (f *fakeBackend) SendMany(ctx context.Context, peers []string, body string) ([]store.Message, error) {
	var out []store.Message
	for _, p := range peers {
		m, err := f.Send(ctx, p, body)
		if err != nil {
			return out, err
		}
		m.Broadcast = true
		out = append(out, m)
	}
	return out, nil
}

func TestSendManyRoute(t *testing.T) {
	s, b := startServer(t)
	c := loggedClient(t, s)
	resp, _ := postJSON(c, base(s)+"/api/messages/many", `{"peers":["c1","c2"],"body":"aviso"}`)
	var out struct {
		Messages []messageJSON `json:"messages"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || len(out.Messages) != 2 || !out.Messages[0].Broadcast || len(b.sent) != 2 {
		t.Errorf("mensaje a varios: %d %+v %v", resp.StatusCode, out, b.sent)
	}
}
func (f *fakeBackend) SetReadReceipts(bool) error                     { return nil }
func (f *fakeBackend) SetGroup(context.Context, string, string) error { return nil }
