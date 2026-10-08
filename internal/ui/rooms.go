package ui

import (
	"context"
	"net/http"
	"strconv"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/chat"
)

// roomViewPrefix distingue en la presencia una sala de un contacto
// ("room:<id>"), para no notificar la conversación que se está viendo.
const roomViewPrefix = "room:"

type roomJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"` // IDs, incluido este equipo
	// Left: este equipo salió; se ve el historial pero no se puede escribir.
	Left   bool `json:"left"`
	Unread int  `json:"unread"`
}

func toRoomJSON(r app.Room) roomJSON {
	return roomJSON{ID: r.ID, Name: r.Name, Members: r.Members, Left: r.Left, Unread: r.Unread}
}

func (s *Server) roomsJSON(ctx context.Context) ([]roomJSON, error) {
	rooms, err := s.b.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]roomJSON, len(rooms))
	for i, r := range rooms {
		out[i] = toRoomJSON(r)
	}
	return out, nil
}

func (s *Server) publishRoom(ctx context.Context, id string) {
	r, ok, err := s.b.Room(ctx, id)
	if err != nil || !ok {
		s.log.Debug("sala para la interfaz", "id", id, "ok", ok, "err", err)
		return
	}
	s.hub.broadcast("room", toRoomJSON(r))
}

func (s *Server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	room, err := s.b.CreateRoom(r.Context(), req.Name, req.Members)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRoomJSON(app.Room{Room: room}))
}

func (s *Server) handleRoomHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := historyPage
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 {
		limit = min(l, maxHistoryPage)
	}
	msgs, err := s.b.RoomHistory(r.Context(), q.Get("room"), q.Get("before"), limit)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	out := make([]messageJSON, len(msgs))
	for i, m := range msgs {
		out[i] = toMessageJSON(m)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRoomSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Room string `json:"room"`
		Body string `json:"body"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	m, err := s.b.SendRoom(r.Context(), req.Room, req.Body)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, toMessageJSON(m))
}

// handleRoomAction atiende los cambios a una sala: agregar miembros,
// renombrar, salir, borrar sus mensajes y marcar como leída.
func (s *Server) handleRoomAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Room    string   `json:"room"`
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	var err error
	switch r.PathValue("action") {
	case "members":
		err = s.b.AddRoomMembers(ctx, req.Room, req.Members)
	case "rename":
		err = s.b.RenameRoom(ctx, req.Room, req.Name)
	case "leave":
		err = s.b.LeaveRoom(ctx, req.Room)
	case "delete":
		err = s.deleteRoom(r, req.Room)
	case "read":
		var changed bool
		if changed, err = s.b.MarkRoomRead(ctx, req.Room); err == nil && changed {
			s.publishRoom(ctx, req.Room)
			s.unreadChanged(ctx)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publishChat reenvía a las ventanas un evento de mensajes o salas.
func (s *Server) publishChat(ctx context.Context, e chat.Event) {
	received := e.Type == chat.MessageReceived
	switch {
	case e.Type == chat.RoomChanged:
		s.publishRoom(ctx, e.Room.ID)
	case e.Message.RoomID != "":
		s.hub.broadcast("message", toMessageJSON(e.Message))
		if received {
			s.publishRoom(ctx, e.Message.RoomID)
			s.unreadChanged(ctx)
		}
	default:
		s.hub.broadcast("message", s.messageJSON(ctx, e.Message))
		if received {
			s.publishContact(ctx, e.Message.PeerID)
			s.unreadChanged(ctx)
		}
	}
}
