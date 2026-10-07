// Package chat envía y recibe mensajes 1 a 1 entre equipos.
//
// Un mensaje saliente se guarda primero como pendiente y luego se entrega con
// POST /v1/msg al equipo destino. Solo se marca como entregado cuando el otro
// equipo responde 204 (ya lo guardó). Si el destino está desconectado, el
// mensaje espera y se reintenta cuando vuelve a aparecer o cada 30 s.
package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/ids"
	"github.com/AEROGU/lanchat/internal/peer"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/version"
)

const (
	// MaxBodyBytes es el tamaño máximo del texto de un mensaje.
	MaxBodyBytes = 64 << 10

	retryInterval = 30 * time.Second
	maxIDLen      = 64
	maxNameLen    = 64
	maxHostLen    = 255
)

var msgPath = version.APIPrefix + "/msg"

// Directory dice dónde está cada equipo; lo implementa discovery.Service.
type Directory interface {
	Peer(id string) (discovery.Peer, bool)
}

// Identity es este equipo, tal como se presenta en cada mensaje.
type Identity struct {
	ID       string
	Hostname string
	Name     func() string
}

type EventType int

const (
	// MessageReceived: llegó un mensaje nuevo.
	MessageReceived EventType = iota
	// MessageQueued: se guardó un mensaje saliente, aún sin entregar.
	MessageQueued
	// MessageDelivered: el destinatario confirmó que lo guardó.
	MessageDelivered
)

type Event struct {
	Type    EventType
	Message store.Message
}

// wireMessage es el JSON que viaja entre equipos.
type wireMessage struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	FromName string `json:"from_name,omitempty"`
	FromHost string `json:"from_host,omitempty"`
	Body     string `json:"body"`
	SentAt   int64  `json:"sent_at"` // Unix en milisegundos
}

func (m wireMessage) validate() error {
	switch {
	case m.ID == "" || len(m.ID) > maxIDLen:
		return errors.New("id inválido")
	case m.From == "" || len(m.From) > maxIDLen:
		return errors.New("remitente inválido")
	case utf8.RuneCountInString(m.FromName) > maxNameLen || len(m.FromHost) > maxHostLen:
		return errors.New("nombre demasiado largo")
	}
	return validateBody(m.Body)
}

func validateBody(body string) error {
	switch {
	case strings.TrimSpace(body) == "":
		return errors.New("mensaje vacío")
	case len(body) > MaxBodyBytes:
		return fmt.Errorf("mensaje demasiado largo (máximo %d KB)", MaxBodyBytes>>10)
	case !utf8.ValidString(body):
		return errors.New("el mensaje no es UTF-8 válido")
	}
	return nil
}

type Service struct {
	self   Identity
	store  *store.Store
	dir    Directory
	client *http.Client
	log    *slog.Logger
	events chan Event

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	closed   bool
	flushing map[string]*sync.Mutex // un envío a la vez por destinatario, para respetar el orden
}

func New(self Identity, st *store.Store, dir Directory, client *http.Client, log *slog.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		self:     self,
		store:    st,
		dir:      dir,
		client:   client,
		log:      log,
		events:   make(chan Event, 256),
		ctx:      ctx,
		cancel:   cancel,
		flushing: map[string]*sync.Mutex{},
	}
}

// Register agrega la ruta de mensajes al servidor entre equipos.
func (s *Service) Register(srv *peer.Server) {
	srv.Handle("POST "+msgPath, http.HandlerFunc(s.handleMsg))
}

// Events debe leerse hasta que se cierre (en Close).
func (s *Service) Events() <-chan Event { return s.events }

// Start inicia los reintentos periódicos de mensajes pendientes.
func (s *Service) Start() {
	s.goSafe(s.retryLoop)
}

// Close detiene los envíos en curso y cierra Events. Debe llamarse después de
// apagar el servidor HTTP, para que ningún handler emita eventos tarde.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	close(s.events)
}

// Send guarda el mensaje como pendiente e intenta entregarlo de inmediato.
func (s *Service) Send(ctx context.Context, peerID, body string) (store.Message, error) {
	if err := validateBody(body); err != nil {
		return store.Message{}, err
	}
	now := time.Now()
	m := store.Message{
		ID:       ids.New(),
		PeerID:   peerID,
		Outgoing: true,
		Body:     body,
		At:       now,
		SentAt:   now,
		Status:   store.StatusPending,
	}
	if _, err := s.store.InsertMessage(ctx, m); err != nil {
		return store.Message{}, err
	}
	s.emit(Event{MessageQueued, m})
	s.Flush(peerID)
	return m, nil
}

// Flush intenta entregar en segundo plano los pendientes de peerID.
func (s *Service) Flush(peerID string) {
	s.goSafe(func() { s.flush(peerID) })
}

// goSafe lanza f salvo que el servicio ya se esté cerrando.
func (s *Service) goSafe(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f()
	}()
}

func (s *Service) peerLock(peerID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.flushing[peerID]
	if !ok {
		l = &sync.Mutex{}
		s.flushing[peerID] = l
	}
	return l
}

func (s *Service) flush(peerID string) {
	l := s.peerLock(peerID)
	l.Lock()
	defer l.Unlock()

	p, ok := s.dir.Peer(peerID)
	if !ok || !p.Online {
		return
	}
	msgs, err := s.store.Pending(s.ctx, peerID)
	if err != nil {
		s.log.Error("leyendo pendientes", "peer", peerID, "err", err)
		return
	}
	for _, m := range msgs {
		if err := s.deliver(p, m); err != nil {
			// Se reintenta más tarde; parar aquí conserva el orden.
			s.log.Debug("entrega fallida", "peer", peerID, "msg", m.ID, "err", err)
			return
		}
		if err := s.store.MarkDelivered(s.ctx, m.ID); err != nil {
			s.log.Error("marcando entregado", "msg", m.ID, "err", err)
			return
		}
		m.Status = store.StatusDelivered
		s.emit(Event{MessageDelivered, m})
	}
}

func (s *Service) deliver(p discovery.Peer, m store.Message) error {
	b, err := json.Marshal(wireMessage{
		ID:       m.ID,
		From:     s.self.ID,
		FromName: s.self.Name(),
		FromHost: s.self.Hostname,
		Body:     m.Body,
		SentAt:   m.SentAt.UnixMilli(),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost,
		"http://"+p.HTTPAddr().String()+msgPath, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("respuesta %s", resp.Status)
	}
	return nil
}

func (s *Service) handleMsg(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*MaxBodyBytes)
	var wm wireMessage
	if err := json.NewDecoder(r.Body).Decode(&wm); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}
	if err := wm.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if wm.From == s.self.ID {
		http.Error(w, "remitente inválido", http.StatusBadRequest)
		return
	}

	now := time.Now()
	ip := ""
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		ip = ap.Addr().Unmap().String()
	}
	// Registrar al remitente por si aún no lo vio el descubrimiento.
	if err := s.store.UpsertPeer(r.Context(), store.Peer{
		ID: wm.From, Name: wm.FromName, Hostname: wm.FromHost, IP: ip, LastSeen: now,
	}); err != nil {
		s.log.Error("guardando remitente", "err", err)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}

	m := store.Message{
		ID:     wm.ID,
		PeerID: wm.From,
		Body:   wm.Body,
		At:     now,
		SentAt: time.UnixMilli(wm.SentAt),
		Status: store.StatusDelivered,
	}
	inserted, err := s.store.InsertMessage(r.Context(), m)
	if err != nil {
		s.log.Error("guardando mensaje", "err", err)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	// Si no se insertó es un reenvío (el remitente no recibió nuestro 204):
	// se confirma otra vez sin duplicarlo.
	if inserted {
		s.emit(Event{MessageReceived, m})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) retryLoop() {
	t := time.NewTicker(retryInterval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			peers, err := s.store.PeersWithPending(s.ctx)
			if err != nil {
				s.log.Error("buscando pendientes", "err", err)
				continue
			}
			for _, id := range peers {
				s.Flush(id)
			}
		}
	}
}

func (s *Service) emit(ev Event) {
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}
