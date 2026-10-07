package discovery

import (
	"net/netip"
	"sort"
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
	// Port es el puerto HTTP del equipo.
	Port int
	// AppVersion es la versión del programa que usa (puede estar vacía).
	AppVersion string
	LastSeen   time.Time
	Online     bool

	// udp es la dirección UDP de origen de sus anuncios.
	udp netip.AddrPort
}

// HTTPAddr es la dirección donde el equipo atiende mensajes y archivos.
func (p Peer) HTTPAddr() netip.AddrPort {
	return netip.AddrPortFrom(p.IP, uint16(p.Port))
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
	ip := src.Addr()
	changed := cur.Name != p.Name || cur.Hostname != p.Hostname || cur.IP != ip ||
		cur.Port != p.Port || cur.AppVersion != p.App
	cameOnline = !cur.Online

	cur.Name = p.Name
	cur.Hostname = p.Hostname
	cur.IP = ip
	cur.udp = src
	cur.Port = p.Port
	cur.AppVersion = p.App
	cur.LastSeen = now
	cur.Online = true

	switch {
	case cameOnline:
		return &Event{PeerOnline, *cur}, true
	case changed:
		return &Event{PeerUpdated, *cur}, false
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].ID < out[j].ID
	})
	return out
}
