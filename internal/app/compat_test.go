package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/identity"
	"github.com/AEROGU/lanchat/internal/peer"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/testutil"
	"github.com/AEROGU/lanchat/internal/version"
)

// legacyPeer simula un LanChat con el protocolo anterior: se anuncia por UDP
// y guarda los mensajes que le llegan a su ruta de mensajes.
type legacyPeer struct {
	id  string
	idt *identity.Identity
	srv *peer.Server

	mu     sync.Mutex
	bodies []string
}

func startLegacyPeer(t *testing.T) *legacyPeer {
	t.Helper()
	const id = "equipo-viejo"
	idt, err := identity.Load(testutil.TempDir(t), id)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := peer.Listen("127.0.0.1:0", idt)
	if err != nil {
		t.Fatal(err)
	}
	lp := &legacyPeer{id: id, idt: idt, srv: srv}
	srv.Handle("POST "+protocol.LegacyRouteMessage, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			From string `json:"from"`
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		lp.mu.Lock()
		lp.bodies = append(lp.bodies, m.Body)
		lp.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return lp
}

// announce envía un anuncio con el protocolo anterior al puerto UDP udpPort.
func (lp *legacyPeer) announce(t *testing.T, udpPort int) {
	t.Helper()
	conn, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", udpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, `{"m":%q,"v":%d,"app":"0.17.0","t":"announce","id":%q,"host":"PC-VIEJA","port":%d,"fp":%q}`,
		protocol.Magic, protocol.LegacyVersion, lp.id, lp.srv.Port(), lp.idt.Fingerprint)
}

func (lp *legacyPeer) received() []string {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	return append([]string(nil), lp.bodies...)
}

// Un equipo con el protocolo anterior aparece como incompatible, no se le
// puede enviar nada y recibe una sola vez el aviso de actualizar, aunque ya
// fuera un contacto guardado y LanChat se reinicie.
func TestIncompatiblePeerNotifiedOnce(t *testing.T) {
	ctx := context.Background()
	dir := testutil.TempDir(t)
	old := startLegacyPeer(t)
	a := startNode(t, dir, "PC-A")
	// Ya era un contacto (de cuando ambos usaban el protocolo anterior).
	if err := a.app.store.UpsertPeer(ctx, store.Peer{ID: old.id, Hostname: "PC-VIEJA", Fingerprint: old.idt.Fingerprint}); err != nil {
		t.Fatal(err)
	}

	old.announce(t, a.udpPort())
	waitFor(t, a, "A ve al equipo viejo", peerEvent(discovery.PeerOnline, old.id))
	c, ok, err := a.app.Contact(ctx, old.id)
	if err != nil || !ok || !c.Incompatible() || c.AppVersion != "0.17.0" {
		t.Fatalf("contacto: %+v, %v, %v", c, ok, err)
	}
	if _, err := a.app.Send(ctx, old.id, "hola"); err == nil || !strings.Contains(err.Error(), version.Releases) {
		t.Errorf("enviar a un incompatible debía fallar pidiendo actualizar: %v", err)
	}

	waitUntil(t, "el aviso llega", func() bool { return len(old.received()) == 1 })
	if got := old.received()[0]; !strings.Contains(got, version.Releases) || !strings.Contains(got, "0.17.0") {
		t.Errorf("aviso: %q", got)
	}
	if v, _ := a.app.store.NotifiedProtocol(ctx, old.id); v != protocol.Version {
		t.Errorf("aviso guardado = %d, quería %d", v, protocol.Version)
	}

	// Más anuncios, y LanChat reiniciado: no se vuelve a avisar.
	for range 3 {
		old.announce(t, a.udpPort())
	}
	a.stop()
	a2 := startNode(t, dir, "PC-A")
	old.announce(t, a2.udpPort())
	waitFor(t, a2, "A reiniciado ve al equipo viejo", peerEvent(discovery.PeerOnline, old.id))
	time.Sleep(300 * time.Millisecond)
	if n := len(old.received()); n != 1 {
		t.Errorf("el aviso llegó %d veces, quería 1", n)
	}
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("no ocurrió: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
