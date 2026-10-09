// Package chat envía y recibe mensajes 1 a 1 entre equipos.
//
// Un mensaje saliente se guarda primero como pendiente y luego se entrega con
// POST a protocol.RouteMessage en el equipo destino. Solo se marca como entregado cuando el
// otro equipo responde 204 (ya lo guardó). Si el destino está desconectado, el
// mensaje espera y se reintenta cuando vuelve a aparecer o cada retryInterval.
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
	"slices"
	"sync"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/ids"
	"github.com/AEROGU/lanchat/internal/peer"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/thumb"
)

const (
	// retryInterval: cada cuánto se reintentan los pendientes a equipos en línea.
	retryInterval = 30 * time.Second
	// maxRequestBytes acota el JSON recibido: el texto (con margen por el
	// escapado JSON) más una oferta con el máximo de archivos y de miniaturas
	// (en base64: 4/3 de su tamaño).
	maxRequestBytes = 2*protocol.MaxMessageBytes +
		protocol.MaxOfferFiles*(2*protocol.MaxFileNameLen+2*protocol.MaxRelDirLen+64) +
		thumb.MaxPerOffer*(thumb.MaxBytes*4/3+32)
	// maxResponseDrain: cuánto se lee de una respuesta que no nos interesa.
	maxResponseDrain = 4 << 10
	eventBuffer      = 256
)

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
	// MessageRead: el destinatario leyó un mensaje que le enviamos.
	MessageRead
	// RoomChanged: llegó una versión nueva de una sala (Event.Room).
	RoomChanged
)

type Event struct {
	Type    EventType
	Message store.Message
	Room    store.Room // en RoomChanged
}

// wireMessage es el JSON que viaja entre equipos.
type wireMessage struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	FromName string `json:"from_name,omitempty"`
	FromHost string `json:"from_host,omitempty"`
	// Body es el texto; en una oferta, un resumen legible ("📎 2 archivos…").
	Body   string     `json:"body"`
	SentAt int64      `json:"sent_at"` // Unix en milisegundos
	Offer  *wireOffer `json:"offer,omitempty"`
	// Broadcast: el remitente lo envió a varios contactos a la vez.
	Broadcast bool `json:"broadcast,omitempty"`
	// Room: el mensaje es de una sala; trae su versión actual.
	Room *wireRoom `json:"room,omitempty"`
	// Event: es un aviso de la sala ("Ana agregó a Luis"), no un mensaje.
	Event bool `json:"event,omitempty"`
}

// wireOffer acompaña a un mensaje que ofrece archivos.
type wireOffer struct {
	// Token autoriza al destinatario a descargar (encabezado protocol.TokenHeader).
	Token     string     `json:"token"`
	ExpiresAt int64      `json:"expires_at"` // Unix en milisegundos
	Files     []wireFile `json:"files"`
}

type wireFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Dir es la subcarpeta relativa ("Proyecto/planos"). Las versiones que no
	// lo conocen reciben los archivos sueltos.
	Dir string `json:"dir,omitempty"`
	// Thumb es la miniatura JPEG de una imagen (vista previa antes de
	// aceptar). Las versiones que no la conocen la ignoran.
	Thumb []byte `json:"thumb,omitempty"`
}

func (m wireMessage) validate() error {
	err := errors.Join(
		protocol.ValidateID(m.ID),
		protocol.ValidateID(m.From),
		protocol.ValidateName(m.FromName),
		protocol.ValidateHostname(m.FromHost),
		protocol.ValidateMessage(m.Body),
	)
	if err != nil || m.Offer == nil {
		return err
	}
	o := m.Offer
	if len(o.Files) == 0 || len(o.Files) > protocol.MaxOfferFiles {
		return fmt.Errorf("una oferta lleva de 1 a %d archivos", protocol.MaxOfferFiles)
	}
	errs := []error{protocol.ValidateToken(o.Token)}
	for _, f := range o.Files {
		errs = append(errs, protocol.ValidateFileName(f.Name), protocol.ValidateRelDir(f.Dir))
		if f.Size < 0 {
			errs = append(errs, errors.New("tamaño de archivo negativo"))
		}
	}
	return errors.Join(errs...)
}

func toWireOffer(t store.Transfer, thumbs map[int][]byte) *wireOffer {
	o := &wireOffer{Token: t.Token, ExpiresAt: t.ExpiresAt.UnixMilli(), Files: make([]wireFile, len(t.Files))}
	for i, f := range t.Files {
		o.Files[i] = wireFile{Name: f.Name, Size: f.Size, Dir: f.Dir, Thumb: thumbs[f.Index]}
	}
	return o
}

// incomingTransfer arma la transferencia que se guarda al recibir una oferta.
func (m wireMessage) incomingTransfer(now time.Time) store.Transfer {
	t := store.Transfer{
		ID:        m.ID,
		PeerID:    m.From,
		State:     store.TransferOffered,
		Token:     m.Offer.Token,
		ExpiresAt: time.UnixMilli(m.Offer.ExpiresAt),
		Files:     make([]store.TransferFile, len(m.Offer.Files)),
	}
	if !now.Before(t.ExpiresAt) {
		t.State = store.TransferExpired
	}
	thumbs := 0
	for i, f := range m.Offer.Files {
		t.Files[i] = store.TransferFile{Index: i, Name: f.Name, Size: f.Size, Dir: f.Dir}
		// Una miniatura que no sea un JPEG pequeño se descarta (no el mensaje).
		if thumbs < thumb.MaxPerOffer && thumb.Valid(f.Thumb) {
			t.Files[i].Thumb = f.Thumb
			thumbs++
		}
	}
	return t
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
		events:   make(chan Event, eventBuffer),
		ctx:      ctx,
		cancel:   cancel,
		flushing: map[string]*sync.Mutex{},
	}
}

// Register agrega la ruta de mensajes al servidor entre equipos.
func (s *Service) Register(srv *peer.Server) {
	srv.Handle("POST "+protocol.RouteMessage, http.HandlerFunc(s.handleMsg))
	srv.Handle("POST "+protocol.RouteRead, http.HandlerFunc(s.handleRead))
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
	return s.send(ctx, store.Message{ID: ids.New(), PeerID: peerID, Body: body}, nil)
}

// SendBroadcast es como Send pero marca el mensaje como enviado a varios
// contactos (el destinatario lo ve como "Mensaje a varios").
func (s *Service) SendBroadcast(ctx context.Context, peerID, body string) (store.Message, error) {
	return s.send(ctx, store.Message{ID: ids.New(), PeerID: peerID, Body: body, Broadcast: true}, nil)
}

// SendOffer envía una oferta de archivos como un mensaje con resumen body. El
// ID de la transferencia es el del mensaje; t.ID y t.PeerID se completan aquí.
func (s *Service) SendOffer(ctx context.Context, peerID, body string, t store.Transfer) (store.Message, error) {
	t.ID, t.PeerID, t.Outgoing = ids.New(), peerID, true
	return s.send(ctx, store.Message{ID: t.ID, PeerID: peerID, Body: body, Kind: store.KindFiles}, &t)
}

func (s *Service) send(ctx context.Context, m store.Message, t *store.Transfer) (store.Message, error) {
	if err := protocol.ValidateMessage(m.Body); err != nil {
		return store.Message{}, err
	}
	now := time.Now()
	m.Outgoing, m.At, m.SentAt, m.Status = true, now, now, store.StatusPending
	if _, err := s.store.InsertMessageWithTransfer(ctx, m, t); err != nil {
		return store.Message{}, err
	}
	s.emit(Event{Type: MessageQueued, Message: m})
	s.Flush(m.PeerID)
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
		s.emit(Event{Type: MessageDelivered, Message: m})
	}
	if !s.flushRoom(p) {
		return
	}
	s.sendReceipts(p)
}

func (s *Service) deliver(p discovery.Peer, m store.Message) error {
	wm := wireMessage{
		ID:        m.ID,
		From:      s.self.ID,
		FromName:  s.self.Name(),
		FromHost:  s.self.Hostname,
		Body:      m.Body,
		SentAt:    m.SentAt.UnixMilli(),
		Broadcast: m.Broadcast,
	}
	if m.Kind == store.KindFiles {
		t, ok, err := s.store.Transfer(s.ctx, m.ID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("oferta %s sin transferencia", m.ID)
		}
		thumbs, err := s.store.Thumbs(s.ctx, m.ID)
		if err != nil {
			return err
		}
		wm.Offer = toWireOffer(t, thumbs)
	}
	status, err := s.post(p, protocol.RouteMessage, wm)
	if err == nil && status != http.StatusNoContent {
		err = fmt.Errorf("respuesta %d", status)
	}
	return err
}

// post envía v como JSON al equipo p y devuelve el código de respuesta.
func (s *Service) post(p discovery.Peer, path string, v any) (int, error) {
	ctx, err := peer.ContextFor(s.ctx, s.store, p.ID)
	if err != nil {
		return 0, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+p.HTTPAddr().String()+path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain)) // permite reutilizar la conexión
	return resp.StatusCode, nil
}

func (s *Service) handleMsg(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
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
	fp, ok := s.checkSender(w, r, wm.From)
	if !ok {
		return
	}

	now := time.Now()
	ip := ""
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		ip = ap.Addr().Unmap().String()
	}
	// Registrar al remitente por si aún no lo vio el descubrimiento.
	if err := s.store.UpsertPeer(r.Context(), store.Peer{
		ID: wm.From, Name: wm.FromName, Hostname: wm.FromHost, IP: ip, LastSeen: now, Fingerprint: fp,
	}); err != nil {
		s.log.Error("guardando remitente", "err", err)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	if wm.Room != nil {
		s.handleRoomMessage(w, r, wm)
		return
	}

	m := store.Message{
		ID:        wm.ID,
		PeerID:    wm.From,
		Body:      wm.Body,
		At:        now,
		SentAt:    time.UnixMilli(wm.SentAt),
		Status:    store.StatusDelivered,
		Unread:    true,
		Broadcast: wm.Broadcast,
	}
	var t *store.Transfer
	if wm.Offer != nil {
		m.Kind = store.KindFiles
		tr := wm.incomingTransfer(now)
		t = &tr
	}
	inserted, err := s.store.InsertMessageWithTransfer(r.Context(), m, t)
	if err != nil {
		s.log.Error("guardando mensaje", "err", err)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	// Si no se insertó es un reenvío (el remitente no recibió nuestro 204):
	// se confirma otra vez sin duplicarlo.
	if inserted {
		s.emit(Event{Type: MessageReceived, Message: m})
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
			withReceipts, rerr := s.store.PeersWithPendingReceipts(s.ctx)
			if err = errors.Join(err, rerr); err != nil {
				s.log.Error("buscando pendientes", "err", err)
				continue
			}
			for _, id := range slices.Compact(slices.Sorted(slices.Values(append(peers, withReceipts...)))) {
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
