package ui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/version"
)

// fakeBackend simula la aplicación con un solo contacto.
type fakeBackend struct {
	mu     sync.Mutex
	sent   []string
	unread int
	name   string
	manual []string
	status string
	room   *app.Room
	// downloadDir vacío = una ruta de Windows que no se usa.
	downloadDir string
}

var testContact = app.Contact{ID: "c1", Hostname: "PC-ANA", IP: "192.168.1.30", Online: true}

func (f *fakeBackend) Self() app.Self {
	f.mu.Lock()
	defer f.mu.Unlock()
	return app.Self{ID: "yo", Name: f.name, Hostname: "PC-YO", Status: f.status}
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
func (f *fakeBackend) Upload(_ context.Context, peer string, next func() (string, string, io.Reader, error)) (store.Message, error) {
	var names []string
	for {
		name, dir, r, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return store.Message{}, err
		}
		b, _ := io.ReadAll(r)
		if dir != "" {
			name = dir + "/" + name
		}
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

// testThumb es la miniatura del archivo 0 de la transferencia "t1".
var testThumb = []byte("\xff\xd8 miniatura")

func (f *fakeBackend) Thumb(_ context.Context, id string, idx int) ([]byte, bool, error) {
	if id == "t1" && idx == 0 {
		return testThumb, true, nil
	}
	return nil, false, nil
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
func (f *fakeBackend) DownloadDir() string {
	if f.downloadDir != "" {
		return f.downloadDir
	}
	return `C:\Descargas\LanChat`
}
func (f *fakeBackend) SetDownloadDir(string) error { return nil }

// startServer arranca el servidor; setup lo ajusta antes de Serve.
func startServer(t *testing.T, setup ...func(*Server)) (*Server, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{}
	s, err := Listen(b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range setup {
		f(s)
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
	fw, _ = mw.CreateFormFile("dir:Obra/planos", "p.txt")
	fw.Write([]byte("plano"))
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

	want := []string{"accept:t1", "reject:t1", "cancel:t1", "nota.txt=hola", "Obra/planos/p.txt=plano"}
	if strings.Join(b.sent, ",") != strings.Join(want, ",") {
		t.Errorf("acciones = %v", b.sent)
	}
}

// Sin escritorio (Android) los archivos y enlaces los abre Shell, y no hay
// carpetas ni selectores de Windows.
func TestShell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foto.jpg")
	if err := os.WriteFile(path, []byte("jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	testTransfers["t3"] = store.Transfer{ID: "t3", PeerID: "c1", State: store.TransferCompleted,
		Files: []store.TransferFile{{Index: 0, Name: "foto.jpg", Size: 4, Done: true, Path: path}}}
	t.Cleanup(func() { delete(testTransfers, "t3") })

	var mu sync.Mutex
	var opened []string
	record := func(p string) error {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, p)
		return nil
	}
	s, _ := startServer(t, func(s *Server) { s.Shell = &Shell{OpenFile: record, OpenURL: record} })
	c := loggedClient(t, s)

	resp, err := c.Get(base(s) + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var st stateJSON
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if !st.Mobile {
		t.Error("state.mobile debía ser true")
	}

	cases := []struct {
		route, body string
		want        int
	}{
		{"/api/files/open", `{"id":"t3","index":0}`, http.StatusNoContent},
		{"/api/files/open", `{"id":"t3","index":0,"reveal":true}`, http.StatusBadRequest},
		{"/api/download-dir/open", `{}`, http.StatusBadRequest},
		{"/api/download-dir", `{"dir":"C:\\otra"}`, http.StatusBadRequest},
		{"/api/files/pick", `{"peer":"c1"}`, http.StatusBadRequest},
		{"/api/files/pick-folder", `{"peer":"c1"}`, http.StatusBadRequest},
		{"/api/open-repository", `{}`, http.StatusNoContent},
	}
	for _, tc := range cases {
		resp, err := postJSON(c, base(s)+tc.route, tc.body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s: %d, quería %d", tc.route, tc.body, resp.StatusCode, tc.want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{path, version.Repository}; !slices.Equal(opened, want) {
		t.Errorf("abiertos = %v, quería %v", opened, want)
	}
}

// Vista previa: la miniatura sale de la base y la imagen completa del archivo,
// solo si de verdad es una imagen y la transferencia lo permite.
func TestPreviewRoutes(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var img bytes.Buffer
	png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	received := store.TransferFile{Index: 0, Name: "foto.png", Size: int64(img.Len()), Done: true,
		Path: write("foto.png", img.Bytes())}
	testTransfers["p1"] = store.Transfer{ID: "p1", PeerID: "c1", State: store.TransferCompleted, Files: []store.TransferFile{
		received,
		{Index: 1, Name: "pendiente.png", Path: received.Path},                                // sin terminar
		{Index: 2, Name: "falsa.png", Done: true, Path: write("falsa.png", []byte("<html>"))}, // no es imagen
		{Index: 3, Name: "notas.txt", Done: true, Path: write("notas.txt", []byte("hola"))},
	}}
	t.Cleanup(func() { delete(testTransfers, "p1") })

	s, _ := startServer(t)
	c := loggedClient(t, s)
	get := func(path string) (*http.Response, []byte) {
		resp, err := c.Get(base(s) + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}

	resp, b := get("/api/files/thumb?id=t1&index=0")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(b, testThumb) {
		t.Errorf("miniatura: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := get("/api/files/thumb?id=t1&index=1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("sin miniatura: %d", resp.StatusCode)
	}

	resp, b = get("/api/files/view?id=p1&index=0")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") || !bytes.Equal(b, img.Bytes()) {
		t.Errorf("imagen: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Security-Policy"))
	}
	for path, want := range map[string]int{
		"/api/files/view?id=p1&index=1":   http.StatusNotFound,             // sin terminar
		"/api/files/view?id=p1&index=2":   http.StatusUnsupportedMediaType, // HTML con nombre de imagen
		"/api/files/view?id=p1&index=3":   http.StatusNotFound,             // no es imagen
		"/api/files/view?id=nada&index=0": http.StatusNotFound,
		"/api/files/view?id=p1":           http.StatusBadRequest,
	} {
		if resp, _ := get(path); resp.StatusCode != want {
			t.Errorf("%s: %d, quería %d", path, resp.StatusCode, want)
		}
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
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
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
func (f *fakeBackend) TrustIdentity(context.Context, string) error    { return nil }

// Salas: el fake guarda una sola, sin miembros reales.

func (f *fakeBackend) Rooms(ctx context.Context) ([]app.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.room == nil {
		return nil, nil
	}
	return []app.Room{*f.room}, nil
}
func (f *fakeBackend) Room(_ context.Context, id string) (app.Room, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.room == nil || f.room.ID != id {
		return app.Room{}, false, nil
	}
	return *f.room, true, nil
}
func (f *fakeBackend) CreateRoom(_ context.Context, name string, members []string) (store.Room, error) {
	if name == "" {
		return store.Room{}, errors.New("ponle un nombre a la sala")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.room = &app.Room{Room: store.Room{ID: "r1", Name: name, Members: append([]string{"yo"}, members...), Version: 1}}
	return f.room.Room, nil
}
func (f *fakeBackend) AddRoomMembers(context.Context, string, []string) error { return nil }
func (f *fakeBackend) RenameRoom(_ context.Context, _, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.room.Name = name
	return nil
}
func (f *fakeBackend) LeaveRoom(context.Context, string) error { return nil }
func (f *fakeBackend) SendRoom(_ context.Context, room, body string) (store.Message, error) {
	return store.Message{ID: "rm1", RoomID: room, Outgoing: true, Body: body, At: time.Now()}, nil
}
func (f *fakeBackend) RoomHistory(_ context.Context, room, _ string, _ int) ([]store.Message, error) {
	return []store.Message{
		{ID: "e1", RoomID: room, Outgoing: true, Body: "PC-YO creó la sala", Kind: store.KindRoomEvent, At: time.Now()},
		{ID: "m1", RoomID: room, PeerID: "c1", Body: "hola sala", At: time.Now()},
	}, nil
}
func (f *fakeBackend) MarkRoomRead(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := f.room.Unread > 0
	f.room.Unread = 0
	return changed, nil
}

func TestRooms(t *testing.T) {
	s, b := startServer(t)
	c := loggedClient(t, s)

	resp, _ := postJSON(c, base(s)+"/api/rooms", `{"name":"","members":["c1"]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("sala sin nombre: %d", resp.StatusCode)
	}
	resp, _ = postJSON(c, base(s)+"/api/rooms", `{"name":"Contabilidad","members":["c1"]}`)
	var room roomJSON
	json.NewDecoder(resp.Body).Decode(&room)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || room.ID != "r1" || len(room.Members) != 2 {
		t.Fatalf("crear sala: %d %+v", resp.StatusCode, room)
	}

	resp, _ = c.Get(base(s) + "/api/state")
	var st stateJSON
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if len(st.Rooms) != 1 || st.Rooms[0].Name != "Contabilidad" {
		t.Errorf("salas en el estado: %+v", st.Rooms)
	}

	resp, _ = c.Get(base(s) + "/api/rooms/messages?room=r1")
	var msgs []messageJSON
	json.NewDecoder(resp.Body).Decode(&msgs)
	resp.Body.Close()
	if len(msgs) != 2 || msgs[0].Kind != "event" || msgs[1].RoomID != "r1" || msgs[1].PeerID != "c1" {
		t.Errorf("historial de sala: %+v", msgs)
	}

	resp, _ = postJSON(c, base(s)+"/api/rooms/messages", `{"room":"r1","body":"hola"}`)
	var m messageJSON
	json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || m.RoomID != "r1" {
		t.Errorf("enviar a la sala: %d %+v", resp.StatusCode, m)
	}

	resp, _ = postJSON(c, base(s)+"/api/rooms/rename", `{"room":"r1","name":"Conta"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || b.room.Name != "Conta" {
		t.Errorf("renombrar: %d %q", resp.StatusCode, b.room.Name)
	}
	resp, _ = postJSON(c, base(s)+"/api/rooms/otra-cosa", `{"room":"r1"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("acción desconocida: %d", resp.StatusCode)
	}

	// Un mensaje recibido en la sala actualiza su contador; ver la sala evita notificar.
	events, err := c.Get(base(s) + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !s.HasWindow() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	b.mu.Lock()
	b.room.Unread = 1
	b.mu.Unlock()
	s.Publish(context.Background(), chat.Event{Type: chat.MessageReceived,
		Message: store.Message{ID: "m2", RoomID: "r1", PeerID: "c1", Body: "¿junta?", At: time.Now()}})
	got := readEvents(t, events.Body, 2)
	if got[0].name != "message" || !strings.Contains(got[0].data, `"roomId":"r1"`) ||
		got[1].name != "room" || !strings.Contains(got[1].data, `"unread":1`) {
		t.Errorf("eventos de sala: %+v", got)
	}
	postJSON(c, base(s)+"/api/presence", `{"focused":true,"viewing":"room:r1"}`)
	if s.ShouldNotify(roomViewPrefix+"r1") || !s.ShouldNotify("c1") {
		t.Error("ShouldNotify con la sala abierta")
	}
}

// Privacidad: el fake anota qué se pidió.

func (f *fakeBackend) ExportData(_ context.Context, path string) error {
	return os.WriteFile(path, []byte("SQLite format 3\x00copia"), 0o600)
}
func (f *fakeBackend) DeleteConversation(_ context.Context, peer string) error {
	return f.record("delete-conversation", peer)
}
func (f *fakeBackend) DeleteRoomConversation(_ context.Context, room string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.room != nil && f.room.ID == room && f.room.Left {
		f.room = nil
	}
	return nil
}
func (f *fakeBackend) WipeData(context.Context) error { return f.record("wipe", "") }

// Sin escritorio (Android) la copia se guarda directo en la carpeta de
// recibidos, sin pisar otra con el mismo nombre; en el escritorio se descarga.
func TestSaveExport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LanChat")
	s, _ := startServer(t, func(s *Server) {
		s.b.(*fakeBackend).downloadDir = dir
		s.Shell = &Shell{OpenFile: func(string) error { return nil }, OpenURL: func(string) error { return nil }}
	})
	c := loggedClient(t, s)
	var names []string
	for range 2 {
		resp, err := postJSON(c, base(s)+"/api/data/export", `{}`)
		if err != nil {
			t.Fatal(err)
		}
		var out struct{ Name, Dir string }
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || out.Dir != dir {
			t.Fatalf("guardar copia: %d %+v", resp.StatusCode, out)
		}
		b, err := os.ReadFile(filepath.Join(dir, out.Name))
		if err != nil || !strings.HasPrefix(string(b), "SQLite format 3") {
			t.Errorf("%s: %v %q", out.Name, err, b)
		}
		names = append(names, out.Name)
	}
	date := time.Now().Format("2006-01-02")
	if want := []string{"LanChat-PC-YO-" + date + ".db", "LanChat-PC-YO-" + date + " (1).db"}; !slices.Equal(names, want) {
		t.Errorf("nombres = %v, quería %v", names, want)
	}

	desk, _ := startServer(t)
	resp, err := postJSON(loggedClient(t, desk), base(desk)+"/api/data/export", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("en el escritorio: %d, quería 404", resp.StatusCode)
	}
}

func TestPrivacy(t *testing.T) {
	s, b := startServer(t)
	c := loggedClient(t, s)

	resp, err := c.Get(base(s) + "/api/data/export")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "SQLite format 3") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), `attachment; filename=LanChat-PC-YO-`) {
		t.Errorf("exportar: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Disposition"), body)
	}

	events, err := c.Get(base(s) + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !s.HasWindow() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	resp, _ = postJSON(c, base(s)+"/api/conversation/delete", `{"peer":"c1"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("borrar conversación: %d", resp.StatusCode)
	}
	got := readEvents(t, events.Body, 1)
	if got[0].name != "cleared" || !strings.Contains(got[0].data, `"peer":"c1"`) {
		t.Errorf("evento al borrar: %+v", got)
	}

	// Sin la palabra de confirmación no se borra nada.
	resp, _ = postJSON(c, base(s)+"/api/data/wipe", `{"confirm":"borrar"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("borrar todo sin confirmar: %d", resp.StatusCode)
	}
	resp, _ = postJSON(c, base(s)+"/api/data/wipe", `{"confirm":"BORRAR"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("borrar todo: %d", resp.StatusCode)
	}
	b.mu.Lock()
	actions := strings.Join(b.sent, ",")
	b.mu.Unlock()
	if actions != "delete-conversation:c1,wipe:" {
		t.Errorf("acciones = %q", actions)
	}
}

// Un error de programación en una acción responde 500 y no corta la conexión
// (la página mostraría "LanChat no responde").
func TestRecoverPanic(t *testing.T) {
	s, _ := startServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	func() {
		defer s.recoverPanic(rec, req)
		var empty []uint16
		_ = empty[0]
	}()
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "error interno") {
		t.Errorf("respuesta: %d %s", rec.Code, rec.Body.String())
	}
}

// Notification: texto de la notificación y cuándo no se notifica.
func TestNotification(t *testing.T) {
	s, b := startServer(t)
	ctx := context.Background()
	received := func(m store.Message) chat.Event { return chat.Event{Type: chat.MessageReceived, Message: m} }
	msg := store.Message{ID: "m1", PeerID: "c1", Body: strings.Repeat("a", notifyPreview+5)}

	n, ok := s.Notification(ctx, received(msg))
	if !ok || n.Title != testContact.DisplayName() || n.Body != strings.Repeat("a", notifyPreview)+"…" || n.Chat != "c1" {
		t.Errorf("mensaje: %+v %v", n, ok)
	}
	if _, ok := s.Notification(ctx, chat.Event{Type: chat.MessageDelivered, Message: msg}); ok {
		t.Error("una entrega no se notifica")
	}

	// En una sala: título = sala, cuerpo = "Autor: texto"; los avisos no.
	b.CreateRoom(ctx, "Proyecto", []string{"c1"})
	inRoom := store.Message{ID: "m2", PeerID: "c1", RoomID: "r1", Body: "hola"}
	if n, ok := s.Notification(ctx, received(inRoom)); !ok || n.Title != "Proyecto" ||
		n.Body != testContact.DisplayName()+": hola" || n.Chat != "room:r1" {
		t.Errorf("sala: %+v %v", n, ok)
	}
	inRoom.Kind = store.KindRoomEvent
	if _, ok := s.Notification(ctx, received(inRoom)); ok {
		t.Error("un aviso de sala no se notifica")
	}

	// Viendo esa conversación con la ventana abierta y enfocada, u Ocupado: no.
	events, err := loggedClient(t, s).Get(base(s) + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	for deadline := time.Now().Add(2 * time.Second); !s.HasWindow() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	s.setPresence(true, "c1")
	if _, ok := s.Notification(ctx, received(msg)); ok {
		t.Error("conversación abierta: no se notifica")
	}
	s.setPresence(false, "")
	b.SetStatus(protocol.StatusBusy, "")
	if _, ok := s.Notification(ctx, received(msg)); ok {
		t.Error("Ocupado: no se notifica")
	}
}
