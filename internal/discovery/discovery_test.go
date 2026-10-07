package discovery

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"
)

var loopback = netip.MustParseAddr("127.0.0.1")

func newTestService(t *testing.T, id string, manual ...string) *Service {
	t.Helper()
	s, err := New(Config{
		ID:          id,
		Name:        "Usuario " + id,
		Hostname:    "PC-" + id,
		BindIP:      loopback,
		HTTPPort:    50001,
		ManualPeers: manual,
		Interval:    100 * time.Millisecond,
		TTL:         500 * time.Millisecond,
		NoBroadcast: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitEvent(t *testing.T, s *Service, typ EventType, id string) Event {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatalf("canal cerrado esperando %v de %s", typ, id)
			}
			if ev.Type == typ && ev.Peer.ID == id {
				return ev
			}
		case <-timeout:
			t.Fatalf("no llegó %v de %s", typ, id)
		}
	}
}

// Solo B conoce a A (como un equipo de otra subred configurado a mano);
// ambos deben terminar viéndose, y A debe enterarse cuando B se cierra.
func TestDiscoveryUnicastBothWays(t *testing.T) {
	a := newTestService(t, "A")
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	go a.Run(ctxA)

	b := newTestService(t, "B", fmt.Sprintf("127.0.0.1:%d", a.LocalPort()))
	ctxB, cancelB := context.WithCancel(context.Background())
	go b.Run(ctxB)

	ev := waitEvent(t, a, PeerOnline, "B")
	if ev.Peer.Hostname != "PC-B" || ev.Peer.Name != "Usuario B" || ev.Peer.IP != loopback {
		t.Errorf("datos de B incorrectos: %+v", ev.Peer)
	}
	waitEvent(t, b, PeerOnline, "A")

	b.SetName("Ventas")
	if ev := waitEvent(t, a, PeerUpdated, "B"); ev.Peer.Name != "Ventas" {
		t.Errorf("nombre = %q, quería Ventas", ev.Peer.Name)
	}

	cancelB()
	for range b.Events() {
	}
	waitEvent(t, a, PeerOffline, "B")
}

func TestRegistryExpire(t *testing.T) {
	r := newRegistry()
	now := time.Now()
	src := netip.AddrPortFrom(netip.MustParseAddr("192.168.1.20"), 50000)
	p := packet{ID: "x", Hostname: "PC-X", Port: 50001}

	if ev, online := r.seen(p, src, now); ev == nil || ev.Type != PeerOnline || !online {
		t.Fatalf("primer anuncio: %+v %v", ev, online)
	}
	if ev, _ := r.seen(p, src, now.Add(time.Second)); ev != nil {
		t.Fatalf("anuncio repetido no debe generar evento: %+v", ev)
	}
	if evs := r.expire(now.Add(10*time.Second), 35*time.Second); len(evs) != 0 {
		t.Fatalf("expiró antes de tiempo: %+v", evs)
	}
	evs := r.expire(now.Add(40*time.Second), 35*time.Second)
	if len(evs) != 1 || evs[0].Type != PeerOffline {
		t.Fatalf("debía expirar: %+v", evs)
	}
	if ev, online := r.seen(p, src, now.Add(50*time.Second)); ev == nil || ev.Type != PeerOnline || !online {
		t.Fatalf("al volver debe estar online: %+v", ev)
	}
}

func TestBroadcastAddr(t *testing.T) {
	cases := map[string]string{
		"192.168.1.20/24": "192.168.1.255",
		"10.0.5.3/16":     "10.0.255.255",
		"172.16.4.9/22":   "172.16.7.255",
	}
	for in, want := range cases {
		got, ok := broadcastAddr(netip.MustParsePrefix(in))
		if !ok || got.String() != want {
			t.Errorf("%s: got %v, want %s", in, got, want)
		}
	}
	if _, ok := broadcastAddr(netip.MustParsePrefix("10.0.0.1/32")); ok {
		t.Error("/32 no tiene broadcast")
	}
}

func TestDecodePacketRejectsForeign(t *testing.T) {
	bad := []string{
		`{"m":"otro","v":1,"t":"hello","id":"x","host":"h","port":1}`,
		`{"m":"lanchat","v":1,"t":"raro","id":"x","host":"h","port":1}`,
		`{"m":"lanchat","v":1,"t":"hello","id":"","host":"h","port":1}`,
		`{"m":"lanchat","v":1,"t":"hello","id":"x","host":"h","port":0}`,
		`no es json`,
	}
	for _, b := range bad {
		if _, err := decodePacket([]byte(b)); err == nil {
			t.Errorf("debía rechazar %s", b)
		}
	}
	if _, err := decodePacket([]byte(`{"m":"lanchat","v":1,"t":"hello","id":"x","host":"h","port":50001}`)); err != nil {
		t.Errorf("paquete válido rechazado: %v", err)
	}
}
