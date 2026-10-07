// Package discovery anuncia este equipo en la LAN por UDP y mantiene la lista
// de equipos LanChat visibles.
package discovery

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/version"
)

const (
	// readBufferSize cabe cualquier datagrama UDP.
	readBufferSize = 64 << 10
	// eventBuffer da margen a quien lee Events antes de bloquear la recepción.
	eventBuffer = 64
	// dnsTimeout limita la resolución de cada equipo manual por hostname.
	dnsTimeout = 2 * time.Second
)

// limitedBroadcast (255.255.255.255) llega a toda la subred, pero Windows lo
// envía por una sola interfaz; por eso también se usa el broadcast dirigido.
var limitedBroadcast = netip.AddrFrom4([4]byte{255, 255, 255, 255})

type Config struct {
	ID       string
	Name     string
	Hostname string
	// Fingerprint es la huella de la identidad TLS de este equipo.
	Fingerprint string
	// Status y StatusText iniciales (ver SetStatus).
	Status     string
	StatusText string
	// UDPPort es el puerto de descubrimiento (0 = uno libre, útil en pruebas).
	UDPPort int
	// BindIP limita la escucha a una IP; vacío = todas las interfaces.
	BindIP netip.Addr
	// HTTPPort se anuncia a los demás para mensajes y archivos.
	HTTPPort int
	// ManualPeers: "ip", "ip:puerto" o "hostname[:puerto]" de otras subredes.
	ManualPeers []string
	// Interval y TTL: por defecto protocol.AnnounceInterval y protocol.PeerTTL.
	Interval time.Duration
	TTL      time.Duration
	// NoBroadcast desactiva el broadcast y deja solo unicast (pruebas).
	NoBroadcast bool
}

type Service struct {
	cfg    Config
	conn   *net.UDPConn
	reg    *registry
	log    *slog.Logger
	events chan Event
	// refresh pide a Run resolver de nuevo los equipos manuales.
	refresh chan struct{}

	mu         sync.Mutex
	name       string
	status     string
	statusText string
	manual     []manualPeer
	manualAddr []netip.AddrPort // última resolución de manual
	// hints son equipos que nos contaron otros equipos (p. ej. de otra
	// subred), con la hora hasta la que se les sigue saludando.
	hints     map[netip.AddrPort]time.Time
	dupWarned bool
}

// New abre el socket UDP. Falla si el puerto ya está en uso, lo que normalmente
// significa que LanChat ya está abierto en este equipo.
func New(cfg Config, log *slog.Logger) (*Service, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = protocol.AnnounceInterval
	}
	if cfg.TTL <= 0 {
		cfg.TTL = protocol.PeerTTL
	}
	manual, errs := parseManualList(cfg.ManualPeers, cfg.UDPPort)
	for _, err := range errs {
		log.Warn("equipo manual ignorado", "err", err)
	}

	laddr := &net.UDPAddr{Port: cfg.UDPPort}
	if cfg.BindIP.IsValid() {
		laddr.IP = cfg.BindIP.AsSlice()
	}
	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return nil, fmt.Errorf("no se pudo abrir el puerto UDP %d (¿LanChat ya está abierto?): %w", cfg.UDPPort, err)
	}
	cfg.UDPPort = conn.LocalAddr().(*net.UDPAddr).Port
	return &Service{
		cfg:        cfg,
		conn:       conn,
		reg:        newRegistry(),
		log:        log,
		events:     make(chan Event, eventBuffer),
		refresh:    make(chan struct{}, 1),
		hints:      map[netip.AddrPort]time.Time{},
		manual:     manual,
		name:       cfg.Name,
		status:     protocol.NormalizeStatus(cfg.Status),
		statusText: cfg.StatusText,
	}, nil
}

// LocalPort es el puerto UDP en el que escucha el servicio.
func (s *Service) LocalPort() int { return s.cfg.UDPPort }

// Events entrega los cambios en la lista de equipos. Debe leerse hasta que se
// cierre, lo cual ocurre cuando termina Run.
func (s *Service) Events() <-chan Event { return s.events }

// Peers devuelve todos los equipos vistos desde que arrancó, conectados o no.
func (s *Service) Peers() []Peer { return s.reg.snapshot() }

// Peer devuelve un equipo visto desde que arrancó (ver Peer.Online).
func (s *Service) Peer(id string) (Peer, bool) { return s.reg.get(id) }

// Name es el nombre propio que se anuncia.
func (s *Service) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// SetName cambia el nombre propio y lo anuncia de inmediato.
func (s *Service) SetName(name string) {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
	s.sendAll(typeAnnounce)
}

// SetStatus cambia el estado propio y lo anuncia de inmediato si cambió.
func (s *Service) SetStatus(status, text string) {
	status = protocol.NormalizeStatus(status)
	s.mu.Lock()
	changed := s.status != status || s.statusText != text
	s.status, s.statusText = status, text
	s.mu.Unlock()
	if changed {
		s.sendAll(typeAnnounce)
	}
}

// Run anuncia este equipo hasta que ctx se cancele; al salir envía bye.
func (s *Service) Run(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.recvLoop()
	}()

	s.resolveManual(ctx)
	s.sendAll(typeHello)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.sendAll(typeBye)
			s.conn.Close()
			<-done
			close(s.events)
			return nil
		case <-s.refresh:
			s.resolveManual(ctx)
			s.sendAll(typeHello)
		case now := <-t.C:
			// Se resuelve en cada ciclo porque las IPs por DHCP cambian.
			s.resolveManual(ctx)
			s.sendAll(typeAnnounce)
			for _, ev := range s.reg.expire(now, s.cfg.TTL) {
				s.events <- ev
			}
		}
	}
}

// resolveManual actualiza las direcciones de los equipos manuales. Se hace
// aquí y no al enviar para que SetName nunca espere al DNS.
func (s *Service) resolveManual(ctx context.Context) {
	s.mu.Lock()
	manual := s.manual
	s.mu.Unlock()

	var addrs []netip.AddrPort
	for _, m := range manual {
		rctx, cancel := context.WithTimeout(ctx, dnsTimeout)
		aps, err := m.resolve(rctx)
		cancel()
		if err != nil {
			s.log.Debug("equipo manual no resuelto", "host", m.host, "err", err)
			continue
		}
		addrs = append(addrs, aps...)
	}
	s.mu.Lock()
	s.manualAddr = addrs
	s.mu.Unlock()
}

func (s *Service) recvLoop() {
	buf := make([]byte, readBufferSize)
	incompatWarned := map[netip.Addr]bool{}
	for {
		n, src, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// En Windows un ICMP "puerto inaccesible" llega como error de lectura.
			s.log.Debug("lectura UDP", "err", err)
			continue
		}
		src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
		p, err := decodePacket(buf[:n])
		if err != nil {
			var inc incompatibleError
			if errors.As(err, &inc) && !incompatWarned[src.Addr()] {
				incompatWarned[src.Addr()] = true
				s.log.Warn("equipo con otra versión de LanChat; hay que actualizar uno de los dos",
					"ip", src.Addr(), "app", p.App, "err", err)
				continue
			}
			s.log.Debug("paquete descartado", "src", src, "err", err)
			continue
		}
		s.handle(p, src)
	}
}

func (s *Service) handle(p packet, src netip.AddrPort) {
	if p.ID == s.cfg.ID {
		if !isLocalIP(src.Addr()) {
			s.mu.Lock()
			warn := !s.dupWarned
			s.dupWarned = true
			s.mu.Unlock()
			if warn {
				s.log.Warn("otro equipo usa el mismo ID (¿se copió config.json?)", "ip", src.Addr())
			}
		}
		return
	}

	switch p.Type {
	case typeBye:
		if ev := s.reg.bye(p.ID); ev != nil {
			s.events <- *ev
		}
	case typeHello, typeAnnounce:
		ev, cameOnline := s.reg.seen(p, src, time.Now())
		if ev != nil {
			s.events <- *ev
		}
		// Responder por unicast para que el otro nos vea sin esperar al
		// siguiente anuncio (y para que funcione entre subredes).
		if p.Type == typeHello || cameOnline {
			s.send(typeAnnounce, src)
		}
	}
}

func (s *Service) packet(t packetType) packet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return packet{
		Magic:       protocol.Magic,
		Version:     protocol.Version,
		App:         version.App,
		Type:        t,
		ID:          s.cfg.ID,
		Name:        s.name,
		Hostname:    s.cfg.Hostname,
		HTTPPort:    s.cfg.HTTPPort,
		Status:      s.status,
		StatusText:  s.statusText,
		Fingerprint: s.cfg.Fingerprint,
	}
}

func (s *Service) send(t packetType, dst netip.AddrPort) {
	b, err := json.Marshal(s.packet(t))
	if err != nil {
		s.log.Error("serializando paquete", "err", err)
		return
	}
	if _, err := s.conn.WriteToUDPAddrPort(b, dst); err != nil {
		s.log.Debug("envío UDP", "dst", dst, "err", err)
	}
}

func (s *Service) sendAll(t packetType) {
	for _, dst := range s.targets() {
		s.send(t, dst)
	}
}

// targets: broadcast por cada interfaz, equipos manuales y equipos conocidos
// de otras subredes (a esos no les llega el broadcast).
func (s *Service) targets() []netip.AddrPort {
	seen := map[netip.AddrPort]bool{}
	var out []netip.AddrPort
	add := func(ap netip.AddrPort) {
		if ap.Addr().Is4() && !seen[ap] {
			seen[ap] = true
			out = append(out, ap)
		}
	}

	nets := localNets()
	port := uint16(s.cfg.UDPPort)
	if !s.cfg.NoBroadcast {
		add(netip.AddrPortFrom(limitedBroadcast, port))
		for _, n := range nets {
			if b, ok := broadcastAddr(n); ok {
				add(netip.AddrPortFrom(b, port))
			}
		}
	}

	now := time.Now()
	s.mu.Lock()
	manual := s.manualAddr
	for ap, until := range s.hints {
		if now.After(until) {
			delete(s.hints, ap)
			continue
		}
		manual = append(manual, ap)
	}
	s.mu.Unlock()
	for _, ap := range manual {
		add(ap)
	}

	for _, p := range s.reg.snapshot() {
		if p.udp.IsValid() && !inAnyNet(p.IP, nets) {
			add(p.udp)
		}
	}
	return out
}

// parseManualList interpreta las entradas válidas y devuelve un error por cada
// inválida. Sin puerto explícito se usa el de descubrimiento de este equipo.
func parseManualList(list []string, udpPort int) ([]manualPeer, []error) {
	defPort := cmp.Or(udpPort, protocol.DefaultUDPPort)
	var out []manualPeer
	var errs []error
	for _, s := range list {
		m, err := parseManual(s, defPort)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, m)
	}
	return out, errs
}

// SetManualPeers reemplaza los equipos manuales y los saluda de inmediato. Si
// alguna entrada es inválida no cambia nada.
func (s *Service) SetManualPeers(list []string) error {
	manual, errs := parseManualList(list, s.cfg.UDPPort)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	s.mu.Lock()
	s.manual = manual
	s.mu.Unlock()
	select {
	case s.refresh <- struct{}{}:
	default: // ya hay una actualización pendiente
	}
	return nil
}

// hintTTL: cuánto se saluda a un equipo que nos contó otro equipo; si
// responde, desde entonces se le saluda como a cualquier conocido.
const hintTTL = time.Hour

// AddHints agrega equipos que conoce otro equipo (su dirección UDP) y los
// saluda de inmediato. Así basta con configurar a mano una PC de otra subred
// en un solo equipo.
func (s *Service) AddHints(addrs []netip.AddrPort) {
	until := time.Now().Add(hintTTL)
	added := false
	s.mu.Lock()
	for _, ap := range addrs {
		if !ap.Addr().Is4() {
			continue
		}
		if _, ok := s.hints[ap]; !ok {
			added = true
		}
		s.hints[ap] = until
	}
	s.mu.Unlock()
	if added {
		select {
		case s.refresh <- struct{}{}:
		default:
		}
	}
}

// UDPAddr es la dirección UDP desde la que se anuncia el equipo.
func (p Peer) UDPAddr() netip.AddrPort { return p.udp }
