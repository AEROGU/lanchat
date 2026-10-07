package discovery

import (
	"cmp"
	"net/netip"
	"slices"
	"sync"
	"time"
)

// Peer es otro equipo LanChat visto en la red.
type Peer struct {
	ID string
	// Name es el nombre que eligió el usuario remoto (puede estar vacío).
	Name     string
	Hostname string
	IP       netip.Addr
	// HTTPPort es donde el equipo atiende mensajes y archivos.
	HTTPPort int
	// AppVersion es la versión del programa que usa (puede estar vacía).
	AppVersion string
	LastSeen   time.Time
	Online     bool

	// udp es la dirección UDP de origen de sus anuncios.
	udp netip.AddrPort
}

// HTTPAddr es la dirección donde el equipo atiende mensajes y archivos.
func (p Peer) HTTPAddr() netip.AddrPort {
	return netip.AddrPortFrom(p.IP, uint16(p.HTTPPort))
}

type EventType int

const (
	// PeerOnline: equipo nuevo o que volvió a conectarse.
	PeerOnline EventType = iota
	// PeerUpdated: cambió su nombre, hostname, IP, puerto o versión.
	PeerUpdated
	// PeerOffline: se despidió o dejó de anunciarse.
	PeerOffline
)

func (t EventType) String() string {
	switch t {
	case PeerOnline:
		return "online"
	case PeerUpdated:
		return "updated"
	case PeerOffline:
		return "offline"
	}
	return "unknown"
}

type Event struct {
	Type EventType
	Peer Peer
}

type registry struct {
	mu    sync.Mutex
	peers map[string]*Peer
}

func newRegistry() *registry {
	return &registry{peers: map[string]*Peer{}}
}

// seen registra un paquete hello/announce. cameOnline indica si el equipo era
// desconocido o estaba desconectado.
func (r *registry) seen(p packet, src netip.AddrPort, now time.Time) (ev *Event, cameOnline bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cur, ok := r.peers[p.ID]
	if !ok {
		cur = &Peer{ID: p.ID}
		r.peers[p.ID] = cur
	}
	updated := Peer{
		ID:         p.ID,
		Name:       p.Name,
		Hostname:   p.Hostname,
		IP:         src.Addr(),
		HTTPPort:   p.HTTPPort,
		AppVersion: p.App,
		LastSeen:   now,
		Online:     true,
		udp:        src,
	}
	changed := cur.Name != updated.Name || cur.Hostname != updated.Hostname || cur.IP != updated.IP ||
		cur.HTTPPort != updated.HTTPPort || cur.AppVersion != updated.AppVersion
	cameOnline = !cur.Online
	*cur = updated

	switch {
	case cameOnline:
		return &Event{PeerOnline, updated}, true
	case changed:
		return &Event{PeerUpdated, updated}, false
	}
	return nil, false
}

// bye marca al equipo como desconectado.
func (r *registry) bye(id string) *Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	cur, ok := r.peers[id]
	if !ok || !cur.Online {
		return nil
	}
	cur.Online = false
	return &Event{PeerOffline, *cur}
}

// expire marca como desconectados a los equipos sin anuncios desde hace más de ttl.
func (r *registry) expire(now time.Time, ttl time.Duration) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	var evs []Event
	for _, p := range r.peers {
		if p.Online && now.Sub(p.LastSeen) > ttl {
			p.Online = false
			evs = append(evs, Event{PeerOffline, *p})
		}
	}
	return evs
}

func (r *registry) get(id string) (Peer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.peers[id]; ok {
		return *p, true
	}
	return Peer{}, false
}

// snapshot devuelve una copia de todos los equipos ordenada por hostname.
func (r *registry) snapshot() []Peer {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Peer, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b Peer) int {
		return cmp.Or(cmp.Compare(a.Hostname, b.Hostname), cmp.Compare(a.ID, b.ID))
	})
	return out
}
