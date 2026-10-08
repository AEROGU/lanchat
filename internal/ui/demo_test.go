//go:build demo

package ui

// Oficina de demostración para las capturas de pantalla del README: la
// interfaz real con datos ficticios en memoria (sin red ni base de datos).
//
// "go tool mage screenshots" la usa para regenerar docs/screenshots. A mano:
//
//	go test -c -tags demo -o demo.exe ./internal/ui
//	demo.exe -test.run ^TestDemo$ -test.timeout 0
//
// Escribe la dirección de la ventana en demo-url.txt (o LANCHAT_DEMO_URL) y
// sigue abierta hasta que se cierre el proceso.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/icon"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/version"
)

var errDemo = errors.New("no disponible en la demostración")

type demoBackend struct {
	mu        sync.Mutex
	self      app.Self
	contacts  []app.Contact
	rooms     []app.Room
	history   map[string][]store.Message // contacto o sala -> mensajes
	transfers map[string]store.Transfer
}

// today devuelve la hora h:m de hoy.
func today(h, m int) time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), h, m, 0, 0, time.Local)
}

func newDemoBackend() *demoBackend {
	fp := func(seed string) string {
		const hex = "0123456789abcdef"
		b := make([]byte, 64)
		for i := range b {
			b[i] = hex[(int(seed[i%len(seed)])*7+i*13)%16]
		}
		return string(b)
	}
	contact := func(id, name, host, ip, group, status, text string, online bool, unread int) app.Contact {
		c := app.Contact{ID: id, Name: name, Hostname: host, IP: ip, Group: group, Online: online,
			Unread: unread, Fingerprint: fp(id), AppVersion: version.App, LastSeen: time.Now()}
		if online {
			c.Status, c.StatusText = status, text
		} else {
			c.LastSeen = today(8, 5).Add(-24 * time.Hour)
		}
		return c
	}
	d := &demoBackend{
		self: app.Self{ID: "yo", Name: "Laura", Hostname: "RECEPCION", Status: protocol.StatusAvailable,
			AutoAwayEnabled: true, ReadReceipts: true, Fingerprint: fp("yo")},
		contacts: []app.Contact{
			contact("c1", "Carlos Ruiz", "PC-CONTA01", "192.168.1.21", "Contabilidad", protocol.StatusAvailable, "", true, 0),
			contact("c2", "María Fernanda Ortiz", "PC-CONTA02", "192.168.1.22", "Contabilidad", protocol.StatusAway, "En junta hasta las 12", true, 0),
			contact("c3", "Jorge Salinas", "PC-VENTAS01", "192.168.1.35", "Ventas", protocol.StatusBusy, "Llamada con cliente", true, 0),
			contact("c4", "Sofía Herrera", "PC-VENTAS02", "192.168.1.36", "Ventas", protocol.StatusAvailable, "", true, 1),
			contact("c5", "Almacén", "PC-ALMACEN", "10.0.5.20", "Operación", protocol.StatusAvailable, "", true, 0),
			contact("c6", "Roberto Díaz", "PC-DIRECCION", "192.168.1.10", "Operación", "", "", false, 0),
		},
		history:   map[string][]store.Message{},
		transfers: map[string]store.Transfer{},
	}

	in := func(id, peer string, at time.Time, body string) store.Message {
		return store.Message{ID: id, PeerID: peer, Body: body, At: at, SentAt: at, Status: store.StatusDelivered}
	}
	out := func(id, peer string, at time.Time, body string, read bool) store.Message {
		m := store.Message{ID: id, PeerID: peer, Outgoing: true, Body: body, At: at, SentAt: at, Status: store.StatusDelivered}
		if read {
			m.ReadAt = at.Add(time.Minute)
		}
		return m
	}

	// Conversación con Carlos: texto, una carpeta enviada y una oferta recibida.
	var invoices []store.TransferFile
	for i, name := range []string{"F-1021 Papelería Central.pdf", "F-1022 Servicios de limpieza.pdf",
		"F-1023 Telefonía.pdf", "F-1024 Mantenimiento.pdf", "F-1025 Paquetería Express.pdf",
		"F-1026 Cafetería.pdf", "F-1027 Software contable.pdf", "F-1028 Arrendamiento.pdf"} {
		invoices = append(invoices, store.TransferFile{Index: i, Name: name, Size: int64(380_000 + i*95_000),
			Dir: "Facturas septiembre", Done: true})
	}
	d.transfers["m3"] = store.Transfer{ID: "m3", PeerID: "c1", Outgoing: true, State: store.TransferCompleted,
		ExpiresAt: today(9, 15).Add(24 * time.Hour), Files: invoices}
	d.transfers["m5"] = store.Transfer{ID: "m5", PeerID: "c1", State: store.TransferOffered,
		ExpiresAt: today(9, 22).Add(24 * time.Hour),
		Files:     []store.TransferFile{{Name: "Conciliación bancaria septiembre.xlsx", Size: 2_458_112}}}
	files := func(m store.Message) store.Message { m.Kind = store.KindFiles; return m }
	d.history["c1"] = []store.Message{
		in("m1", "c1", today(9, 12), "Buenos días, Laura. ¿Ya llegaron las facturas de proveedores de septiembre?"),
		out("m2", "c1", today(9, 14), "Sí, las escaneé hace rato. Te paso la carpeta.", true),
		files(out("m3", "c1", today(9, 15), "📁 Facturas septiembre (8 archivos)", true)),
		in("m4", "c1", today(9, 21), "Perfecto, ya las tengo 👍\nTe mando la conciliación para que la revises."),
		files(in("m5", "c1", today(9, 22), "📎 Conciliación bancaria septiembre.xlsx")),
		out("m6", "c1", today(9, 24), "Gracias, la reviso después de la junta.", true),
	}
	d.history["c4"] = []store.Message{
		in("s1", "c4", today(10, 2), "¿Tienes a la mano el teléfono del proveedor de tóner?"),
	}
	broadcast := out("b1", "c2", today(8, 30), "Hoy a las 2 se corta la luz 15 minutos por mantenimiento. Guarden su trabajo.", true)
	broadcast.Broadcast = true
	d.history["c2"] = []store.Message{broadcast}

	// Salas.
	room := func(id, name string, unread int, members ...string) app.Room {
		return app.Room{Room: store.Room{ID: id, Name: name, Members: append([]string{"yo"}, members...), Version: 3}, Unread: unread}
	}
	d.rooms = []app.Room{
		room("r1", "Cierre de mes", 0, "c1", "c2", "c3"),
		room("r2", "Proyecto Norte", 3, "c4", "c5"),
	}
	ev := func(m store.Message) store.Message { m.Kind = store.KindRoomEvent; m.Unread = false; return m }
	inRoom := func(id, room, peer string, at time.Time, body string) store.Message {
		m := in(id, peer, at, body)
		m.RoomID = room
		return m
	}
	outRoom := func(id, room string, at time.Time, body string) store.Message {
		m := out(id, "", at, body, false)
		m.RoomID = room
		return m
	}
	d.history[roomViewPrefix+"r1"] = []store.Message{
		ev(outRoom("e1", "r1", today(8, 40), "Laura creó la sala «Cierre de mes»")),
		inRoom("r1a", "r1", "c2", today(8, 42), "Recuerden que el viernes cerramos la contabilidad de septiembre."),
		inRoom("r1b", "r1", "c1", today(8, 45), "A mí me faltan dos facturas de Ventas."),
		ev(inRoom("e2", "r1", "c2", today(8, 46), "María Fernanda Ortiz agregó a Jorge Salinas")),
		inRoom("r1c", "r1", "c3", today(8, 51), "Las mando hoy antes de las 3 🙌"),
		outRoom("r1d", "r1", today(8, 53), "Gracias a todos. Cualquier duda, aquí estoy."),
	}
	return d
}

func (d *demoBackend) Self() app.Self {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.self
}
func (d *demoBackend) Contacts(context.Context) ([]app.Contact, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]app.Contact(nil), d.contacts...), nil
}
func (d *demoBackend) Contact(_ context.Context, id string) (app.Contact, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.contacts {
		if c.ID == id {
			return c, true, nil
		}
	}
	return app.Contact{}, false, nil
}
func (d *demoBackend) TotalUnread(context.Context) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.contacts {
		n += c.Unread
	}
	for _, r := range d.rooms {
		n += r.Unread
	}
	return n, nil
}
func (d *demoBackend) MarkRead(_ context.Context, id string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.contacts {
		if d.contacts[i].ID == id && d.contacts[i].Unread > 0 {
			d.contacts[i].Unread = 0
			return true, nil
		}
	}
	return false, nil
}
func (d *demoBackend) Send(_ context.Context, peer, body string) (store.Message, error) {
	m := store.Message{ID: time.Now().Format("150405.000"), PeerID: peer, Outgoing: true, Body: body,
		At: time.Now(), SentAt: time.Now(), Status: store.StatusDelivered}
	d.mu.Lock()
	d.history[peer] = append(d.history[peer], m)
	d.mu.Unlock()
	return m, nil
}
func (d *demoBackend) SendMany(ctx context.Context, peers []string, body string) ([]store.Message, error) {
	var out []store.Message
	for _, p := range peers {
		m, _ := d.Send(ctx, p, body)
		out = append(out, m)
	}
	return out, nil
}
func (d *demoBackend) History(_ context.Context, key, before string, _ int) ([]store.Message, error) {
	if before != "" {
		return nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]store.Message(nil), d.history[key]...), nil
}
func (d *demoBackend) SetName(string) error                           { return errDemo }
func (d *demoBackend) SetStatus(string, string) error                 { return errDemo }
func (d *demoBackend) SetAutoAway(bool) error                         { return errDemo }
func (d *demoBackend) SetReadReceipts(bool) error                     { return errDemo }
func (d *demoBackend) SetAlias(context.Context, string, string) error { return errDemo }
func (d *demoBackend) SetGroup(context.Context, string, string) error { return errDemo }
func (d *demoBackend) TrustIdentity(context.Context, string) error    { return errDemo }
func (d *demoBackend) ManualPeers() []string                          { return []string{"10.0.5.20"} }
func (d *demoBackend) SetManualPeers([]string) error                  { return errDemo }
func (d *demoBackend) OfferFiles(context.Context, string, []string) (store.Message, error) {
	return store.Message{}, errDemo
}
func (d *demoBackend) Upload(context.Context, string, func() (string, string, io.Reader, error)) (store.Message, error) {
	return store.Message{}, errDemo
}
func (d *demoBackend) AcceptTransfer(context.Context, string) error { return errDemo }
func (d *demoBackend) RejectTransfer(context.Context, string) error { return errDemo }
func (d *demoBackend) CancelTransfer(context.Context, string) error { return errDemo }
func (d *demoBackend) Transfer(_ context.Context, id string) (store.Transfer, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.transfers[id]
	return t, ok, nil
}
func (d *demoBackend) TransfersByID(_ context.Context, ids []string) (map[string]store.Transfer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]store.Transfer{}
	for _, id := range ids {
		if t, ok := d.transfers[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}
func (d *demoBackend) DownloadDir() string         { return "" }
func (d *demoBackend) SetDownloadDir(string) error { return errDemo }
func (d *demoBackend) Rooms(context.Context) ([]app.Room, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]app.Room(nil), d.rooms...), nil
}
func (d *demoBackend) Room(_ context.Context, id string) (app.Room, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.rooms {
		if r.ID == id {
			return r, true, nil
		}
	}
	return app.Room{}, false, nil
}
func (d *demoBackend) CreateRoom(context.Context, string, []string) (store.Room, error) {
	return store.Room{}, errDemo
}
func (d *demoBackend) AddRoomMembers(context.Context, string, []string) error { return errDemo }
func (d *demoBackend) RenameRoom(context.Context, string, string) error       { return errDemo }
func (d *demoBackend) LeaveRoom(context.Context, string) error                { return errDemo }
func (d *demoBackend) SendRoom(_ context.Context, room, body string) (store.Message, error) {
	m := store.Message{ID: time.Now().Format("150405.000"), RoomID: room, Outgoing: true, Body: body,
		At: time.Now(), SentAt: time.Now(), Status: store.StatusDelivered}
	d.mu.Lock()
	d.history[roomViewPrefix+room] = append(d.history[roomViewPrefix+room], m)
	d.mu.Unlock()
	return m, nil
}
func (d *demoBackend) RoomHistory(ctx context.Context, room, before string, limit int) ([]store.Message, error) {
	return d.History(ctx, roomViewPrefix+room, before, limit)
}
func (d *demoBackend) MarkRoomRead(_ context.Context, id string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.rooms {
		if d.rooms[i].ID == id && d.rooms[i].Unread > 0 {
			d.rooms[i].Unread = 0
			return true, nil
		}
	}
	return false, nil
}
func (d *demoBackend) ExportData(context.Context, string) error             { return errDemo }
func (d *demoBackend) DeleteConversation(context.Context, string) error     { return errDemo }
func (d *demoBackend) DeleteRoomConversation(context.Context, string) error { return errDemo }
func (d *demoBackend) WipeData(context.Context) error                       { return errDemo }

// demoScript abre la escena indicada en el fragmento de la dirección, para
// capturarla sin hacer clic: #chat=ID, #room=ID o #many.
const demoScript = `
const wait = async (sel) => {
  for (let i = 0; i < 100 && !document.querySelector(sel); i++) await new Promise((r) => setTimeout(r, 50));
  return document.querySelector(sel);
};
const scene = new URLSearchParams(location.hash.slice(1));
await wait("#contacts .contact");
if (scene.has("chat")) {
  [...document.querySelectorAll("#contacts .contact:not(.room)")]
    .find((li) => li.querySelector(".name").textContent === scene.get("chat"))?.click();
} else if (scene.has("room")) {
  [...document.querySelectorAll("#contacts .room")]
    .find((li) => li.querySelector(".name").textContent === scene.get("room"))?.click();
} else if (scene.has("many")) {
  document.getElementById("many-btn").click();
  for (const b of document.querySelectorAll("#many-groups button")) if (b.textContent === scene.get("many")) b.click();
  document.getElementById("many-text").value = "Hoy a las 2 se corta la luz 15 minutos por mantenimiento. Guarden su trabajo.";
}
await new Promise((r) => setTimeout(r, 300));
document.activeElement?.blur();
// Sin servidor de eventos real en la captura: ocultar el aviso de reconexión.
document.getElementById("banner").style.display = "none";
`

// withDemoScript agrega demoScript a la página.
func withDemoScript(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Edge sin ventana espera a que terminen las peticiones antes de
		// capturar; el flujo de eventos nunca termina. 204 = no reconectar.
		if r.URL.Path == "/api/events" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/demo.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			io.WriteString(w, demoScript)
			return
		}
		if r.URL.Path != "/" || r.URL.Query().Has("t") {
			next.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		body := strings.Replace(rec.Body.String(), "</body>", `<script type="module" src="/demo.js"></script></body>`, 1)
		for k, v := range rec.Header() {
			if k != "Content-Length" {
				w.Header()[k] = v
			}
		}
		w.WriteHeader(rec.Code)
		io.WriteString(w, body)
	})
}

// TestDemoLogo escribe el ícono de LanChat en PNG en LANCHAT_DEMO_LOGO.
func TestDemoLogo(t *testing.T) {
	path := os.Getenv("LANCHAT_DEMO_LOGO")
	if path == "" {
		t.Skip("sin LANCHAT_DEMO_LOGO")
	}
	if err := os.WriteFile(path, icon.PNG(256, false), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDemo(t *testing.T) {
	s, err := Listen(newDemoBackend(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.srv.Handler = withDemoScript(s.srv.Handler)
	urlFile := os.Getenv("LANCHAT_DEMO_URL")
	if urlFile == "" {
		urlFile = "demo-url.txt"
	}
	if err := os.WriteFile(urlFile, []byte(s.LaunchURL()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log(s.LaunchURL())
	if err := s.Serve(); err != nil {
		t.Fatal(err)
	}
}
