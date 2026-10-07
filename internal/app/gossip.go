package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/peer"
	"github.com/AEROGU/lanchat/internal/protocol"
)

// Listas compartidas: cada equipo publica los equipos en línea que conoce y
// pregunta a los demás por los suyos. Si una PC de otra subred se configura
// a mano en un solo equipo, los demás la conocen a través de él.

const (
	// gossipInterval: cada cuánto se vuelve a preguntar a los equipos en línea.
	gossipInterval = 2 * time.Minute
	// maxPeersResponse acota el JSON de una lista compartida.
	maxPeersResponse = protocol.MaxSharedPeers * (protocol.MaxIDLen + 64)
)

type wirePeer struct {
	ID      string `json:"id"`
	IP      string `json:"ip"`
	UDPPort int    `json:"udp_port"`
}

// handlePeers entrega los equipos en línea que conoce este equipo.
func (a *App) handlePeers(w http.ResponseWriter, r *http.Request) {
	out := []wirePeer{}
	for _, p := range a.disc.Peers() {
		if p.Online && p.UDPAddr().IsValid() && len(out) < protocol.MaxSharedPeers {
			out = append(out, wirePeer{ID: p.ID, IP: p.IP.String(), UDPPort: int(p.UDPAddr().Port())})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// shareableAddr: solo se aceptan direcciones de red local, para que una lista
// manipulada no haga enviar paquetes a Internet.
func shareableAddr(ip netip.Addr) bool {
	return ip.Is4() && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

// askPeers pide a p su lista y saluda a los equipos que no conocemos.
func (a *App) askPeers(ctx context.Context, p discovery.Peer) {
	list, err := a.fetchPeers(ctx, p)
	if err != nil {
		a.log.Debug("pidiendo lista de equipos", "peer", p.ID, "err", err)
		return
	}
	self := a.Self().ID
	var hints []netip.AddrPort
	for _, wp := range list {
		ip, err := netip.ParseAddr(wp.IP)
		if err != nil || protocol.ValidateID(wp.ID) != nil || wp.ID == self ||
			wp.UDPPort < 1 || wp.UDPPort > 65535 || !shareableAddr(ip.Unmap()) {
			continue
		}
		if known, ok := a.disc.Peer(wp.ID); ok && known.Online {
			continue
		}
		hints = append(hints, netip.AddrPortFrom(ip.Unmap(), uint16(wp.UDPPort)))
	}
	if len(hints) > 0 {
		a.log.Debug("equipos recibidos de otro equipo", "peer", p.ID, "nuevos", len(hints))
		a.disc.AddHints(hints)
	}
}

func (a *App) fetchPeers(ctx context.Context, p discovery.Peer) ([]wirePeer, error) {
	pctx, err := peer.ContextFor(ctx, a.store, p.ID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(pctx, http.MethodGet,
		"https://"+p.HTTPAddr().String()+protocol.RoutePeers, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { // p. ej. una versión que no comparte listas
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxPeersResponse))
		return nil, errors.New(resp.Status)
	}
	var list []wirePeer
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPeersResponse)).Decode(&list); err != nil {
		return nil, err
	}
	if len(list) > protocol.MaxSharedPeers {
		list = list[:protocol.MaxSharedPeers]
	}
	return list, nil
}

// gossip pregunta a un equipo en segundo plano; wg permite esperarlo al cerrar.
func (a *App) gossip(ctx context.Context, wg *sync.WaitGroup, p discovery.Peer) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.askPeers(ctx, p)
	}()
}

// gossipLoop vuelve a preguntar periódicamente a los equipos en línea, por si
// conocieron a alguien nuevo.
func (a *App) gossipLoop(ctx context.Context, wg *sync.WaitGroup) {
	t := time.NewTicker(a.gossipEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, p := range a.disc.Peers() {
				if p.Online {
					a.gossip(ctx, wg, p)
				}
			}
		}
	}
}
