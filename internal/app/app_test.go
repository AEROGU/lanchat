package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

type node struct {
	app    *App
	cancel context.CancelFunc
	done   chan error
	events chan any
}

// startNode arranca un App en loopback; manual son puertos UDP de otros nodos.
func startNode(t *testing.T, dir, host string, manual ...int) *node {
	t.Helper()
	return startNodeWith(t, dir, host, nil, manual...)
}

// startNodeWith permite ajustar las opciones (p. ej. IdleTime) antes de arrancar.
func startNodeWith(t *testing.T, dir, host string, tune func(*Options), manual ...int) *node {
	t.Helper()
	var peers []string
	for _, p := range manual {
		peers = append(peers, fmt.Sprintf("127.0.0.1:%d", p))
	}
	opts := Options{
		Dir:      dir,
		Hostname: host,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTPAddr: "127.0.0.1:0",
		Tune: func(c *discovery.Config) {
			c.UDPPort = 0
			c.BindIP = netip.MustParseAddr("127.0.0.1")
			c.NoBroadcast = true
			c.ManualPeers = peers
			c.Interval = 100 * time.Millisecond
			c.TTL = time.Second
		},
	}
	if tune != nil {
		tune(&opts)
	}
	a, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &node{app: a, cancel: cancel, done: make(chan error, 1), events: make(chan any, 256)}
	go func() {
		for ev := range a.Events() {
			n.events <- ev
		}
		close(n.events)
	}()
	go func() { n.done <- a.Run(ctx) }()
	t.Cleanup(n.stop)
	return n
}

func (n *node) stop() {
	n.cancel()
	<-n.done
	for range n.events {
	}
	n.done <- nil // permite llamar stop dos veces
}

func (n *node) udpPort() int { return n.app.disc.LocalPort() }

// tempDir es como t.TempDir pero reintenta el borrado: en Windows, el
// antivirus puede retener un instante los archivos que SQLite acaba de
// borrar (-wal, -shm) y la carpeta aparece "no vacía".
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lanchat-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for err := os.RemoveAll(dir); err != nil; err = os.RemoveAll(dir) {
			if time.Now().After(deadline) {
				t.Errorf("borrando %s: %v", dir, err)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return dir
}

func waitFor(t *testing.T, n *node, what string, match func(any) bool) any {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-n.events:
			if !ok {
				t.Fatalf("eventos cerrados esperando %s", what)
			}
			if match(ev) {
				return ev
			}
		case <-timeout:
			t.Fatalf("no llegó: %s", what)
		}
	}
}

func peerEvent(typ discovery.EventType, id string) func(any) bool {
	return func(ev any) bool {
		e, ok := ev.(discovery.Event)
		return ok && e.Type == typ && e.Peer.ID == id
	}
}

func chatEvent(typ chat.EventType, body string) func(any) bool {
	return func(ev any) bool {
		e, ok := ev.(chat.Event)
		return ok && e.Type == typ && e.Message.Body == body
	}
}

func TestChatDeliveryAndOfflineQueue(t *testing.T) {
	dirB := tempDir(t)
	a := startNode(t, tempDir(t), "PC-A")
	b := startNode(t, dirB, "PC-B", a.udpPort())
	idA, idB := a.app.Self().ID, b.app.Self().ID

	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, idB))
	waitFor(t, b, "B ve a A", peerEvent(discovery.PeerOnline, idA))

	// Entrega directa con acentos.
	if _, err := a.app.Send(context.Background(), idB, "¿Ya está el reporte?"); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, b, "B recibe", chatEvent(chat.MessageReceived, "¿Ya está el reporte?")).(chat.Event)
	if ev.Message.PeerID != idA || ev.Message.Outgoing {
		t.Errorf("mensaje recibido mal registrado: %+v", ev.Message)
	}
	waitFor(t, a, "A confirma entrega", chatEvent(chat.MessageDelivered, "¿Ya está el reporte?"))

	// B se cierra: el mensaje queda pendiente hasta que vuelve.
	b.stop()
	waitFor(t, a, "A ve salir a B", peerEvent(discovery.PeerOffline, idB))
	m, err := a.app.Send(context.Background(), idB, "mensaje en espera")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if h, _ := a.app.History(context.Background(), idB, "", 1); len(h) != 1 ||
		h[0].ID != m.ID || h[0].Status != store.StatusPending {
		t.Fatalf("debía seguir pendiente: %+v", h)
	}

	b2 := startNode(t, dirB, "PC-B", a.udpPort())
	waitFor(t, b2, "B recibe el pendiente", chatEvent(chat.MessageReceived, "mensaje en espera"))
	waitFor(t, a, "A confirma el pendiente", chatEvent(chat.MessageDelivered, "mensaje en espera"))

	// El historial de B sobrevivió al reinicio.
	h, err := b2.app.History(context.Background(), idA, "", 10)
	if err != nil || len(h) != 2 {
		t.Fatalf("historial de B = %+v, %v", h, err)
	}
}

func TestNamesAndAliases(t *testing.T) {
	a := startNode(t, tempDir(t), "PC-A")
	b := startNode(t, tempDir(t), "PC-B", a.udpPort())
	idB := b.app.Self().ID
	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, idB))

	if err := b.app.SetName("Juan"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A ve el nombre nuevo", func(ev any) bool {
		e, ok := ev.(discovery.Event)
		return ok && e.Type == discovery.PeerUpdated && e.Peer.Name == "Juan"
	})

	ctx := context.Background()
	c, ok, _ := a.app.Contact(ctx, idB)
	if !ok || c.DisplayName() != "Juan" || c.Detail() != "PC-B · 127.0.0.1" {
		t.Fatalf("contacto = %+v (%q, %q)", c, c.DisplayName(), c.Detail())
	}
	if err := a.app.SetAlias(ctx, idB, "  Juan de Ventas "); err != nil {
		t.Fatal(err)
	}
	c, _, _ = a.app.Contact(ctx, idB)
	if c.DisplayName() != "Juan de Ventas" {
		t.Errorf("alias = %q", c.DisplayName())
	}
	if _, err := a.app.Send(ctx, "desconocido", "hola"); err == nil {
		t.Error("enviar a un contacto desconocido debía fallar")
	}
	if _, err := a.app.Send(ctx, idB, "   "); err == nil {
		t.Error("un mensaje vacío debía fallar")
	}
}

func TestStatusAndAutoAway(t *testing.T) {
	var idle atomic.Int64 // nanosegundos sin usar la PC (simulado)
	a := startNode(t, tempDir(t), "PC-A")
	b := startNodeWith(t, tempDir(t), "PC-B", func(o *Options) {
		o.IdleTime = func() (time.Duration, error) { return time.Duration(idle.Load()), nil }
		o.IdleCheckInterval = 50 * time.Millisecond
	}, a.udpPort())
	idB := b.app.Self().ID
	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, idB))
	status := func(st, text string) func(any) bool {
		return func(ev any) bool {
			e, ok := ev.(discovery.Event)
			return ok && e.Peer.ID == idB && e.Peer.Status == st && e.Peer.StatusText == text
		}
	}

	if err := b.app.SetStatus("vacaciones", ""); err == nil {
		t.Error("un estado desconocido debía rechazarse")
	}
	if err := b.app.SetStatus(protocol.StatusBusy, "  En junta "); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A ve Ocupado", status(protocol.StatusBusy, "En junta"))

	// Ocupado no cambia por inactividad; Disponible sí pasa a Ausente.
	b.app.SetStatus(protocol.StatusAvailable, "")
	waitFor(t, a, "A ve Disponible", status(protocol.StatusAvailable, ""))
	idle.Store(int64(11 * time.Minute))
	waitFor(t, a, "A ve Ausente por inactividad", status(protocol.StatusAway, ""))
	if s := b.app.Self(); !s.Idle || s.Status != protocol.StatusAvailable {
		t.Errorf("Self = %+v", s)
	}
	idle.Store(0)
	waitFor(t, a, "A ve que B volvió", status(protocol.StatusAvailable, ""))

	c, _, _ := a.app.Contact(context.Background(), idB)
	if c.Status != protocol.StatusAvailable {
		t.Errorf("contacto: %+v", c)
	}
}
