package discovery

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"strconv"
	"time"
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

func inAnyNet(ip netip.Addr, nets []netip.Prefix) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
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

// resolveManual convierte "ip", "ip:puerto" o "hostname[:puerto]" en direcciones UDP.
func resolveManual(ctx context.Context, s string, defPort int) ([]netip.AddrPort, error) {
	host, port := s, defPort
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, &net.AddrError{Err: "puerto inválido", Addr: s}
		}
		host, port = h, n
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.AddrPort{netip.AddrPortFrom(a.Unmap(), uint16(port))}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.AddrPort, 0, len(ips))
	for _, ip := range ips {
		out = append(out, netip.AddrPortFrom(ip.Unmap(), uint16(port)))
	}
	return out, nil
}
