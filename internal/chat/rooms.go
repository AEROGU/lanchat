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
// actual de la sala, así los cambios de nombre, de miembros o de
// administradores llegan a todos.
//
// Tampoco hay quien arbitre los permisos: cada equipo comprueba, contra la
// versión de la sala que ya tiene, que quien manda un cambio podía hacerlo
// (authorizeRoomChange). Sacar a otro miembro o cambiar los administradores
// solo lo puede hacer un administrador.

// wireRoom es la versión de una sala que viaja con cada mensaje.
type wireRoom struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
	// Admins: administradores. Las versiones que no los conocen no lo envían
	// (nil): entonces se conservan los que ya se tenían.
	Admins  []string `json:"admins,omitempty"`
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
	if len(r.Admins) > len(r.Members) {
		return errors.New("más administradores que miembros")
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
	for _, a := range r.Admins {
		if !seen[a] {
			errs = append(errs, errors.New("un administrador no es miembro de la sala"))
		}
	}
	return errors.Join(errs...)
}

func toWireRoom(r store.Room) *wireRoom {
	return &wireRoom{ID: r.ID, Name: r.Name, Members: r.Members, Admins: r.Admins, Version: r.Version}
}

// SendRoom envía body a los demás miembros de la sala y a extra (quien acaba
// de ser sacado de la sala también debe recibir el aviso). Con event es un
// aviso de la sala ("Ana agregó a Luis"); se puede enviar aunque este equipo
// ya haya salido (para avisar que salió).
func (s *Service) SendRoom(ctx context.Context, roomID, body string, event bool, extra ...string) (store.Message, error) {
	return s.sendRoom(ctx, roomID, body, event, false, store.Reply{}, extra)
}

// ForwardRoom reenvía body a la sala (llega marcado como reenviado).
func (s *Service) ForwardRoom(ctx context.Context, roomID, body string) (store.Message, error) {
	return s.sendRoom(ctx, roomID, body, false, true, store.Reply{}, nil)
}

// ReplyRoom envía a la sala body como respuesta a otro mensaje, con su cita.
func (s *Service) ReplyRoom(ctx context.Context, roomID, body string, r store.Reply) (store.Message, error) {
	return s.sendRoom(ctx, roomID, body, false, false, r, nil)
}

func (s *Service) sendRoom(ctx context.Context, roomID, body string, event, forwarded bool, reply store.Reply, extra []string) (store.Message, error) {
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
		return store.Message{}, errors.New("ya no estás en esta sala")
	}
	var recipients []string
	for _, m := range append(slices.Clone(room.Members), extra...) {
		if m != s.self.ID && !slices.Contains(recipients, m) {
			recipients = append(recipients, m)
		}
	}
	now := time.Now()
	m := store.Message{
		ID: ids.New(), Outgoing: true, Body: body, At: now, SentAt: now,
		Status: store.StatusPending, RoomID: roomID, Forwarded: forwarded, Reply: reply,
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
				Room: toWireRoom(room), Event: m.Kind == store.KindRoomEvent, Forwarded: m.Forwarded,
				Reply: toWireReply(m.Reply),
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

// authorizeRoomChange comprueba que from podía cambiar la sala de local a
// next: cualquiera puede agregar miembros, renombrar o salir, pero solo un
// administrador saca a otros o cambia quién administra.
func authorizeRoomChange(local, next store.Room, from string) error {
	if !slices.Contains(local.Members, from) {
		// Lo agregaron en una versión que aquí no llegó: no hay con qué comparar.
		return nil
	}
	if local.IsAdmin(from) {
		return nil
	}
	for _, m := range local.Members {
		if m != from && !slices.Contains(next.Members, m) {
			return errors.New("solo un administrador puede sacar a otros miembros")
		}
	}
	// Con los mismos administradores de antes, ¿quedan los que dice next?
	expected := store.Room{Members: next.Members, Admins: local.EffectiveAdmins()}.EffectiveAdmins()
	if !slices.Equal(expected, next.EffectiveAdmins()) {
		return errors.New("solo un administrador puede cambiar los administradores")
	}
	return nil
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
	local, known, err := s.store.Room(ctx, wr.ID)
	if err != nil {
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	next := store.Room{ID: wr.ID, Name: wr.Name, Members: wr.Members, Admins: wr.Admins, Version: wr.Version}
	if wr.Admins == nil { // de una versión que no conoce a los administradores
		next.Admins = local.Admins
	}
	newer := !known || wr.Version > local.Version

	// kicked: esta versión saca a este equipo de una sala en la que estaba.
	kicked := false
	if !slices.Contains(wr.Members, s.self.ID) {
		if !known || local.Left || !newer || !slices.Contains(local.Members, s.self.ID) {
			http.Error(w, "este equipo no es miembro de la sala", http.StatusForbidden)
			return
		}
		kicked, next.Left = true, true
	}
	// El remitente debe ser miembro aquí (o haberlo sido: así llega el aviso
	// de que salió) o venir agregado en una versión más nueva que la local.
	member := slices.Contains(wr.Members, wm.From)
	if known {
		member = slices.Contains(local.Members, wm.From) || (member && newer)
	}
	if !member {
		http.Error(w, "el remitente no es miembro de la sala", http.StatusForbidden)
		return
	}
	if known && newer {
		if err := authorizeRoomChange(local, next, wm.From); err != nil {
			s.log.Warn("cambio de sala rechazado", "sala", wr.ID, "from", wm.From, "err", err)
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	applied, err := s.store.ApplyRoom(ctx, next)
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
	// Fuera de la sala no se guardan más mensajes, salvo el aviso de que
	// acaban de sacar a este equipo.
	if cur.Left && !(kicked && applied) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	now := time.Now()
	m := store.Message{
		ID: wm.ID, PeerID: wm.From, Body: wm.Body, At: now, SentAt: time.UnixMilli(wm.SentAt),
		Status: store.StatusDelivered, Unread: !wm.Event, RoomID: wr.ID, Forwarded: wm.Forwarded && !wm.Event,
	}
	if wm.Event {
		m.Kind = store.KindRoomEvent
	} else {
		m.Reply = wm.reply()
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
