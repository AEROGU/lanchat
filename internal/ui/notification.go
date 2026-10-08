package ui

import (
	"context"
	"unicode/utf8"

	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

// notifyPreview es cuántos caracteres del mensaje muestra la notificación.
const notifyPreview = 200

// Notification decide si un evento de app.Events merece una notificación del
// sistema y con qué texto. La usan la bandeja de Windows y la app de Android.
// No se notifica la conversación que el usuario está viendo, los avisos de
// sala ni nada en estado Ocupado ("no molestar").
func (s *Server) Notification(ctx context.Context, ev any) (title, body string, ok bool) {
	e, isChat := ev.(chat.Event)
	if !isChat || e.Type != chat.MessageReceived || e.Message.Kind == store.KindRoomEvent {
		return "", "", false
	}
	m := e.Message
	view := m.PeerID
	if m.RoomID != "" {
		view = roomViewPrefix + m.RoomID
	}
	if !s.ShouldNotify(view) || s.b.Self().Status == protocol.StatusBusy {
		return "", "", false
	}
	c, _, err := s.b.Contact(ctx, m.PeerID)
	if err != nil {
		return "", "", false
	}
	title, body = c.DisplayName(), preview(m.Body)
	if m.RoomID != "" {
		r, found, err := s.b.Room(ctx, m.RoomID)
		if err != nil || !found {
			return "", "", false
		}
		title, body = r.Name, c.DisplayName()+": "+body
	}
	return title, body, true
}

func preview(s string) string {
	if utf8.RuneCountInString(s) <= notifyPreview {
		return s
	}
	return string([]rune(s)[:notifyPreview]) + "…"
}
