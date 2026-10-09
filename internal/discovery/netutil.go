package discovery

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

// localNets devuelve las redes IPv4 de las interfaces activas (sin loopback).
func localNets() []netip.Prefix {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			ones, bits := ipnet.Mask.Size()
			if bits == 128 { // máscara IPv4 en formato de 16 bytes
				ones -= 96
			} else if bits != 32 {
				continue
			}
			addr, _ := netip.AddrFromSlice(ip4)
			out = append(out, netip.PrefixFrom(addr, ones))
		}
	}
	return out
}

// broadcastAddr calcula la dirección de broadcast dirigido de la red.
func broadcastAddr(p netip.Prefix) (netip.Addr, bool) {
	if !p.Addr().Is4() || p.Bits() < 0 || p.Bits() >= 31 {
		return netip.Addr{}, false
	}
	a := p.Addr().As4()
	v := binary.BigEndian.Uint32(a[:]) | ^(^uint32(0) << (32 - p.Bits()))
	binary.BigEndian.PutUint32(a[:], v)
	return netip.AddrFrom4(a), true
}

// isLocalIP indica si ip pertenece a este equipo (incluye loopback).
func isLocalIP(ip netip.Addr) bool {
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if addr, ok := netip.AddrFromSlice(ipnet.IP); ok && addr.Unmap() == ip {
				return true
			}
		}
	}
	return false
}

// manualPeer es una entrada de Config.ManualPeers ya interpretada.
type manualPeer struct {
	host string     // hostname a resolver por DNS (vacío si es una IP)
	addr netip.Addr // válida si la entrada es una IP
	port uint16
}

// parseManual interpreta "ip", "ip:puerto", "hostname" o "hostname:puerto".
func parseManual(s string, defPort int) (manualPeer, error) {
	host, port := s, defPort
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return manualPeer{}, fmt.Errorf("puerto inválido en %q", s)
		}
		host, port = h, n
	}
	if host == "" {
		return manualPeer{}, fmt.Errorf("equipo manual vacío: %q", s)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a = a.Unmap(); !a.Is4() {
			return manualPeer{}, fmt.Errorf("solo se admite IPv4: %q", s)
		}
		return manualPeer{addr: a, port: uint16(port)}, nil
	}
	return manualPeer{host: host, port: uint16(port)}, nil
}

// resolve devuelve las direcciones UDP del equipo (consulta DNS si es un hostname).
func (m manualPeer) resolve(ctx context.Context) ([]netip.AddrPort, error) {
	if m.addr.IsValid() {
		return []netip.AddrPort{netip.AddrPortFrom(m.addr, m.port)}, nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", m.host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.AddrPort, 0, len(ips))
	for _, ip := range ips {
		out = append(out, netip.AddrPortFrom(ip.Unmap(), m.port))
	}
	return out, nil
}
