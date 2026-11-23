//go:build demo

package ui

// Oficina de demostración para las capturas de pantalla del README: la
// interfaz real con datos ficticios en memoria (sin red ni base de datos).
//
// "go tool mage screenshots" la usa para regenerar docs/screenshots/<idioma>
// (LANCHAT_LANG elige el idioma de la página y de los datos). A mano:
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
	"github.com/AEROGU/lanchat/internal/i18n"
	"github.com/AEROGU/lanchat/internal/icon"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/thumb"
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
	photos    map[string][]byte // foto de perfil por equipo
}

// today devuelve la hora h:m de hoy.
func today(h, m int) time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), h, m, 0, 0, time.Local)
}

// newDemoBackend arma la oficina en el idioma en uso: en español o, en
// cualquier otro, en inglés.
func newDemoBackend() *demoBackend {
	tr := func(es, en string) string {
		if i18n.Current() == "es" {
			return es
		}
		return en
	}
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
		self: app.Self{ID: "yo", Name: "Laura", Hostname: tr("RECEPCION", "FRONT-DESK"), Status: protocol.StatusAvailable,
			AutoAwayEnabled: true, ReadReceipts: true, Fingerprint: fp("yo")},
		contacts: []app.Contact{
			contact("c1", "Carlos Ruiz", tr("PC-CONTA01", "PC-ACCT01"), "192.168.1.21", tr("Contabilidad", "Accounting"), protocol.StatusAvailable, "", true, 0),
			contact("c2", tr("María Fernanda Ortiz", "Mary Ortiz"), tr("PC-CONTA02", "PC-ACCT02"), "192.168.1.22", tr("Contabilidad", "Accounting"), protocol.StatusAway, tr("En junta hasta las 12", "In a meeting until 12"), true, 0),
			contact("c3", tr("Jorge Salinas", "George Salinas"), tr("PC-VENTAS01", "PC-SALES01"), "192.168.1.35", tr("Ventas", "Sales"), protocol.StatusBusy, tr("Llamada con cliente", "On a call with a client"), true, 0),
			contact("c4", tr("Sofía Herrera", "Sophie Herrera"), tr("PC-VENTAS02", "PC-SALES02"), "192.168.1.36", tr("Ventas", "Sales"), protocol.StatusAvailable, "", true, 1),
			contact("c5", tr("Almacén", "Warehouse"), tr("PC-ALMACEN", "PC-WAREHOUSE"), "10.0.5.20", tr("Operación", "Operations"), protocol.StatusAvailable, "", true, 0),
			contact("c6", tr("Roberto Díaz", "Robert Diaz"), tr("PC-DIRECCION", "PC-MANAGER"), "192.168.1.10", tr("Operación", "Operations"), "", "", false, 0),
		},
		history:   map[string][]store.Message{},
		transfers: map[string]store.Transfer{},
		photos:    map[string][]byte{},
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

	// react agrega al mensaje la misma reacción de cada uno de peers ("" = Laura).
	react := func(m store.Message, emoji string, peers ...string) store.Message {
		for _, p := range peers {
			m.Reactions = append(m.Reactions, store.Reaction{PeerID: p, Emoji: emoji})
		}
		return m
	}

	// Conversación con Carlos: texto, una carpeta enviada y una oferta recibida.
	var invoices []store.TransferFile
	names := []string{"F-1021 Papelería Central.pdf", "F-1022 Servicios de limpieza.pdf",
		"F-1023 Telefonía.pdf", "F-1024 Mantenimiento.pdf", "F-1025 Paquetería Express.pdf",
		"F-1026 Cafetería.pdf", "F-1027 Software contable.pdf", "F-1028 Arrendamiento.pdf"}
	if i18n.Current() != "es" {
		names = []string{"INV-1021 Central Stationery.pdf", "INV-1022 Cleaning services.pdf",
			"INV-1023 Phone.pdf", "INV-1024 Maintenance.pdf", "INV-1025 Express Courier.pdf",
			"INV-1026 Cafeteria.pdf", "INV-1027 Accounting software.pdf", "INV-1028 Lease.pdf"}
	}
	for i, name := range names {
		invoices = append(invoices, store.TransferFile{Index: i, Name: name, Size: int64(380_000 + i*95_000),
			Dir: tr("Facturas septiembre", "September invoices"), Done: true})
	}
	d.transfers["m3"] = store.Transfer{ID: "m3", PeerID: "c1", Outgoing: true, State: store.TransferCompleted,
		ExpiresAt: today(9, 15).Add(24 * time.Hour), Files: invoices}
	d.transfers["m5"] = store.Transfer{ID: "m5", PeerID: "c1", State: store.TransferOffered,
		ExpiresAt: today(9, 22).Add(24 * time.Hour),
		Files:     []store.TransferFile{{Name: tr("Conciliación bancaria septiembre.xlsx", "September bank reconciliation.xlsx"), Size: 2_458_112}}}
	files := func(m store.Message) store.Message { m.Kind = store.KindFiles; return m }
	d.history["c1"] = []store.Message{
		in("m1", "c1", today(9, 12), tr("Buenos días, Laura. ¿Ya llegaron las facturas de proveedores de septiembre?",
			"Good morning, Laura. Did the September supplier invoices arrive?")),
		react(out("m2", "c1", today(9, 14), tr("Sí, las escaneé hace rato. Te paso la carpeta.",
			"Yes, I scanned them a while ago. I'll send you the folder."), true), "👍", "c1"),
		files(out("m3", "c1", today(9, 15), tr("📁 Facturas septiembre (8 archivos)", "📁 September invoices (8 files)"), true)),
		in("m4", "c1", today(9, 21), tr("Perfecto, ya las tengo 👍\nTe mando la conciliación para que la revises.",
			"Perfect, got them 👍\nI'm sending you the reconciliation to review.")),
		files(in("m5", "c1", today(9, 22), tr("📎 Conciliación bancaria septiembre.xlsx", "📎 September bank reconciliation.xlsx"))),
		react(out("m6", "c1", today(9, 24), tr("Gracias, la reviso después de la junta.", "Thanks, I'll review it after the meeting."), true), "🙏", "c1"),
	}
	d.history["c4"] = []store.Message{
		in("s1", "c4", today(10, 2), tr("¿Tienes a la mano el teléfono del proveedor de tóner?", "Do you have the toner supplier's phone number handy?")),
	}
	broadcast := out("b1", "c2", today(8, 30), tr("Hoy a las 2 se corta la luz 15 minutos por mantenimiento. Guarden su trabajo.",
		"The power will be off for 15 minutes at 2 today for maintenance. Save your work."), true)
	broadcast.Broadcast = true
	d.history["c2"] = []store.Message{broadcast}

	// Salas.
	room := func(id, name string, unread int, members ...string) app.Room {
		return app.Room{Room: store.Room{ID: id, Name: name, Members: append([]string{"yo"}, members...), Version: 3}, Unread: unread}
	}
	d.rooms = []app.Room{
		room("r1", tr("Cierre de mes", "Month-end close"), 0, "c1", "c2", "c3"),
		room("r2", tr("Proyecto Norte", "North Project"), 3, "c4", "c5"),
	}
	// Los avisos llevan sus datos, como en LanChat real: se ven en el idioma de la página.
	ev := func(m store.Message, notice string) store.Message {
		m.Kind, m.Unread, m.Notice = store.KindRoomEvent, false, notice
		return m
	}
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
	replyTo := func(m store.Message, id, author, text string) store.Message {
		m.Reply = store.Reply{ID: id, Author: author, Text: text}
		return m
	}
	d.history[roomViewPrefix+"r1"] = []store.Message{
		ev(outRoom("e1", "r1", today(8, 40), "Laura creó la sala «Cierre de mes»"),
			`{"type":"created","name":"`+tr("Cierre de mes", "Month-end close")+`"}`),
		react(inRoom("r1a", "r1", "c2", today(8, 42), tr("Recuerden que el viernes cerramos la contabilidad de septiembre.",
			"Remember we close September's books on Friday.")), "👍", "", "c1", "c3"),
		inRoom("r1b", "r1", "c1", today(8, 45), tr("A mí me faltan dos facturas de Ventas.", "I'm still missing two invoices from Sales.")),
		ev(inRoom("e2", "r1", "c2", today(8, 46), "María Fernanda Ortiz agregó a Jorge Salinas"),
			`{"type":"added","members":[{"id":"c3","name":"Jorge Salinas"}]}`),
		react(replyTo(inRoom("r1c", "r1", "c3", today(8, 51), tr("Las mando hoy antes de las 3 🙌", "I'll send them today before 3 🙌")),
			"r1b", "c1", tr("A mí me faltan dos facturas de Ventas.", "I'm still missing two invoices from Sales.")), "❤️", "c2"),
		outRoom("r1d", "r1", today(8, 53), tr("Gracias a todos. Cualquier duda, aquí estoy.", "Thanks, everyone. Any questions, I'm here.")),
	}
	// Como en LanChat real, la lista se ordena por el último mensaje.
	for i, c := range d.contacts {
		if h := d.history[c.ID]; len(h) > 0 {
			d.contacts[i].Last = h[len(h)-1]
		}
	}
	for i, r := range d.rooms {
		if h := d.history[roomViewPrefix+r.ID]; len(h) > 0 {
			d.rooms[i].Last = h[len(h)-1]
		}
	}
	return d
}

func (d *demoBackend) Self() app.Self {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.self
	s.Lang = i18n.Current()
	return s
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
func (d *demoBackend) SetName(string) error           { return errDemo }
func (d *demoBackend) SetStatus(string, string) error { return errDemo }
func (d *demoBackend) SetAutoAway(bool) error         { return errDemo }
func (d *demoBackend) SetupDone() bool                { return true }
func (d *demoBackend) MarkSetupDone() error           { return nil }
func (d *demoBackend) SetReadReceipts(bool) error     { return errDemo }
func (d *demoBackend) SetPreviews(bool) error         { return errDemo }
func (d *demoBackend) SetHideOffline(bool) error      { return nil }
func (d *demoBackend) SetAutoUpdate(bool) error       { return nil }
func (d *demoBackend) SetLanguage(code string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.self.Language = code
	lang := i18n.Resolve(code, i18n.System())
	i18n.SetCurrent(lang)
	return lang, nil
}
func (d *demoBackend) SetCollapsedGroups([]string) error { return nil }
func (d *demoBackend) SetRecentEmoji([]string) error     { return nil }
func (d *demoBackend) ThumbsUsage(context.Context) (int, int64, error) {
	return 0, 0, nil
}
func (d *demoBackend) ClearThumbs(context.Context) error              { return errDemo }
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
func (d *demoBackend) AcceptTransferTo(context.Context, string, string) error {
	return errDemo
}
func (d *demoBackend) RejectTransfer(context.Context, string) error { return errDemo }
func (d *demoBackend) CancelTransfer(context.Context, string) error { return errDemo }
func (d *demoBackend) DeleteTransfer(context.Context, string) error { return errDemo }
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
func (d *demoBackend) Thumb(context.Context, string, int) ([]byte, bool, error) {
	return nil, false, nil
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
func (d *demoBackend) RemoveRoomMember(context.Context, string, string) error { return errDemo }
func (d *demoBackend) SetRoomAdmin(context.Context, string, string, bool) error {
	return errDemo
}
func (d *demoBackend) DeleteForEveryone(context.Context, string) (store.Message, error) {
	return store.Message{}, errDemo
}
func (d *demoBackend) EditMessage(context.Context, string, string) (store.Message, error) {
	return store.Message{}, errDemo
}

// Reply responde en la conversación del mensaje id, como app.Reply.
func (d *demoBackend) Reply(_ context.Context, id, body string) (store.Message, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, list := range d.history {
		for _, o := range list {
			if o.ID != id {
				continue
			}
			author, peer := o.PeerID, o.PeerID
			if o.Outgoing {
				author = d.self.ID
			}
			if o.RoomID != "" {
				peer = ""
			}
			m := store.Message{ID: time.Now().Format("150405.000"), PeerID: peer, RoomID: o.RoomID, Outgoing: true,
				Body: body, At: time.Now(), SentAt: time.Now(), Status: store.StatusDelivered,
				Reply: store.Reply{ID: o.ID, Author: author, Text: protocol.ReplySnippet(o.Body)}}
			d.history[key] = append(list, m)
			return m, nil
		}
	}
	return store.Message{}, errors.New("el mensaje al que respondes ya no existe")
}
func (d *demoBackend) Forward(context.Context, []string, []string, []string) ([]store.Message, error) {
	return nil, errDemo
}
func (d *demoBackend) React(context.Context, string, string) (store.Message, error) {
	return store.Message{}, errDemo
}
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
func (d *demoBackend) RemoveContact(context.Context, string, string) error  { return errDemo }
func (d *demoBackend) WipeData(context.Context) error                       { return errDemo }

// demoScript abre la escena indicada en el fragmento de la dirección, para
// capturarla sin hacer clic: #chat=NOMBRE, #room=NOMBRE o #many=GRUPO (y
// &emoji para abrir además el selector de emojis).
const demoScript = `
const wait = async (sel) => {
  for (let i = 0; i < 100 && !document.querySelector(sel); i++) await new Promise((r) => setTimeout(r, 50));
  return document.querySelector(sel);
};
const scene = new URLSearchParams(location.hash.slice(1));
await wait("#contacts .contact");
// chat=c1 y room=r1 abren esa conversación; many, el mensaje a varios con el
// primer grupo elegido. Los ids no cambian con el idioma.
if (scene.has("chat")) {
  window.lanchatOpen(scene.get("chat"));
} else if (scene.has("room")) {
  window.lanchatOpen("room:" + scene.get("room"));
} else if (scene.has("many")) {
  document.getElementById("many-btn").click();
  document.querySelector("#many-groups button")?.click();
  document.getElementById("many-text").value = document.documentElement.lang === "es"
    ? "Hoy a las 2 se corta la luz 15 minutos por mantenimiento. Guarden su trabajo."
    : "The power will be off for 15 minutes at 2 today for maintenance. Save your work.";
}
await new Promise((r) => setTimeout(r, 300));
// &emoji: además, el selector de emojis abierto.
if (scene.has("emoji")) document.getElementById("emoji-btn").click();
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
	// El idioma del sistema, o LANCHAT_LANG (las capturas del README en cada idioma).
	i18n.SetCurrent(i18n.Resolve("", i18n.System()))
	s, err := Listen(newDemoBackend(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.srv.Handler = withDemoScript(s.srv.Handler)
	// LANCHAT_DEMO_MOBILE=1: la página se comporta como en el teléfono.
	if os.Getenv("LANCHAT_DEMO_MOBILE") == "1" {
		s.Shell = &Shell{
			OpenFile: func(string) error { return errDemo },
			OpenURL:  func(string) error { return errDemo },
		}
	}
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

// La foto de perfil propia sí funciona en la demostración (en memoria).
func (d *demoBackend) SetPhoto(_ context.Context, r io.ReadSeeker) error {
	data, err := thumb.Avatar(r)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.photos[d.self.ID] = data
	d.self.Photo = protocol.PhotoID(data)
	return nil
}
func (d *demoBackend) RemovePhoto(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.photos, d.self.ID)
	d.self.Photo = ""
	return nil
}
func (d *demoBackend) Photo(_ context.Context, id string) (string, []byte, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, ok := d.photos[id]
	if !ok {
		return "", nil, false, nil
	}
	return protocol.PhotoID(data), data, true, nil
}
