package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/ids"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

// Salas: no hay servidor. Cada mensaje de sala se entrega por separado a
// cada miembro (con la misma cola que los mensajes 1 a 1) y lleva la versión
// actual de la sala, así los cambios de nombre o de miembros llegan a todos.

// wireRoom es la versión de una sala que viaja con cada mensaje.
type wireRoom struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
	Version int64    `json:"version"`
}

func (r wireRoom) validate() error {
	if r.Version < 1 {
		return errors.New("versión de sala inválida")
	}
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("la sala no tiene nombre")
	}
	if len(r.Members) < 1 || len(r.Members) > protocol.MaxRoomMembers {
		return fmt.Errorf("una sala tiene de 1 a %d miembros", protocol.MaxRoomMembers)
	}
	errs := []error{protocol.ValidateID(r.ID), protocol.ValidateName(r.Name)}
	seen := map[string]bool{}
	for _, m := range r.Members {
		if seen[m] {
			errs = append(errs, errors.New("miembro repetido"))
		}
		seen[m] = true
		errs = append(errs, protocol.ValidateID(m))
	}
	return errors.Join(errs...)
}

func toWireRoom(r store.Room) *wireRoom {
	return &wireRoom{ID: r.ID, Name: r.Name, Members: r.Members, Version: r.Version}
}

// SendRoom envía body a los demás miembros de la sala. Con event es un aviso
// de la sala ("Ana agregó a Luis"); se puede enviar aunque este equipo ya
// haya salido (para avisar que salió).
func (s *Service) SendRoom(ctx context.Context, roomID, body string, event bool) (store.Message, error) {
	if err := protocol.ValidateMessage(body); err != nil {
		return store.Message{}, err
	}
	room, ok, err := s.store.Room(ctx, roomID)
	if err != nil {
		return store.Message{}, err
	}
	if !ok {
		return store.Message{}, errors.New("sala inexistente")
	}
	if room.Left && !event {
		return store.Message{}, errors.New("saliste de esta sala")
	}
	var recipients []string
	for _, m := range room.Members {
		if m != s.self.ID {
			recipients = append(recipients, m)
		}
	}
	now := time.Now()
	m := store.Message{
		ID: ids.New(), Outgoing: true, Body: body, At: now, SentAt: now,
		Status: store.StatusPending, RoomID: roomID,
	}
	if event {
		m.Kind = store.KindRoomEvent
	}
	if len(recipients) == 0 {
		m.Status = store.StatusDelivered
	}
	if err := s.store.InsertRoomMessage(ctx, m, recipients); err != nil {
		return store.Message{}, err
	}
	s.emit(Event{Type: MessageQueued, Message: m})
	for _, p := range recipients {
		s.Flush(p)
	}
	return m, nil
}

// flushRoom entrega a p los mensajes de sala pendientes; false si hay que
// reintentar más tarde.
func (s *Service) flushRoom(p discovery.Peer) bool {
	msgs, err := s.store.PendingRoomDeliveries(s.ctx, p.ID)
	if err != nil {
		s.log.Error("leyendo pendientes de salas", "peer", p.ID, "err", err)
		return false
	}
	for _, m := range msgs {
		room, ok, err := s.store.Room(s.ctx, m.RoomID)
		if err != nil {
			return false
		}
		if ok {
			status, err := s.post(p, protocol.RouteMessage, wireMessage{
				ID: m.ID, From: s.self.ID, FromName: s.self.Name(), FromHost: s.self.Hostname,
				Body: m.Body, SentAt: m.SentAt.UnixMilli(),
				Room: toWireRoom(room), Event: m.Kind == store.KindRoomEvent,
			})
			switch {
			case err != nil || status >= 500:
				s.log.Debug("entrega de sala fallida", "peer", p.ID, "msg", m.ID, "status", status, "err", err)
				return false
			case status != http.StatusNoContent:
				// Lo rechazó (p. ej. ya no es miembro): reintentar no sirve.
				s.log.Warn("mensaje de sala rechazado", "peer", p.ID, "msg", m.ID, "status", status)
			}
		}
		done, err := s.store.MarkRoomDelivered(s.ctx, m.ID, p.ID)
		if err != nil {
			s.log.Error("marcando entrega de sala", "msg", m.ID, "err", err)
			return false
		}
		if done {
			m.Status = store.StatusDelivered
			s.emit(Event{Type: MessageDelivered, Message: m})
		}
	}
	return true
}

// handleRoomMessage guarda un mensaje de sala ya validado y con el remitente
// verificado por TLS (en handleMsg).
func (s *Service) handleRoomMessage(w http.ResponseWriter, r *http.Request, wm wireMessage) {
	wr := *wm.Room
	if err := wr.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if !slices.Contains(wr.Members, s.self.ID) {
		http.Error(w, "este equipo no es miembro de la sala", http.StatusForbidden)
		return
	}
	local, known, err := s.store.Room(ctx, wr.ID)
	if err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	// El remitente debe ser miembro aquí (o haberlo sido: así llega el aviso
	// de que salió) o venir agregado en una versión más nueva que la local.
	member := slices.Contains(wr.Members, wm.From)
	if known {
		member = slices.Contains(local.Members, wm.From) || (member && wr.Version > local.Version)
	}
	if !member {
		http.Error(w, "el remitente no es miembro de la sala", http.StatusForbidden)
		return
	}

	applied, err := s.store.ApplyRoom(ctx, store.Room{ID: wr.ID, Name: wr.Name, Members: wr.Members, Version: wr.Version})
	if err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	cur, _, err := s.store.Room(ctx, wr.ID)
	if err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	if applied {
		s.emit(Event{Type: RoomChanged, Room: cur})
	}
	if cur.Left { // salimos de la sala: no se guardan más mensajes
		w.WriteHeader(http.StatusNoContent)
		return
	}

	now := time.Now()
	m := store.Message{
		ID: wm.ID, PeerID: wm.From, Body: wm.Body, At: now, SentAt: time.UnixMilli(wm.SentAt),
		Status: store.StatusDelivered, Unread: !wm.Event, RoomID: wr.ID,
	}
	if wm.Event {
		m.Kind = store.KindRoomEvent
	}
	inserted, err := s.store.InsertMessage(ctx, m)
	if err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	if inserted {
		s.emit(Event{Type: MessageReceived, Message: m})
	}
	w.WriteHeader(http.StatusNoContent)
}

// NotifyRoomChanged avisa a la interfaz de un cambio hecho en este equipo.
func (s *Service) NotifyRoomChanged(r store.Room) {
	s.emit(Event{Type: RoomChanged, Room: r})
}
