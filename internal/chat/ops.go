package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

// Avisos sobre mensajes ya enviados: eliminarlos para todos y reaccionar.
// Cada aviso espera en la cola (store.EnqueueOps) hasta que su destinatario
// lo confirma, igual que los mensajes: si está desconectado, le llega al
// volver. Se envían después de los mensajes pendientes, así un aviso nunca
// llega antes que el mensaje al que se refiere.

// wireOp es un aviso tal como viaja y como se guarda en la cola.
type wireOp struct {
	Type string `json:"type"` // protocol.OpDelete u OpReact
	Msg  string `json:"msg"`  // ID del mensaje
	// Emoji de OpReact; vacío quita la reacción.
	Emoji string `json:"emoji,omitempty"`
	// At es la hora del remitente (Unix en milisegundos).
	At int64 `json:"at"`
}

// wireOps es una tanda de avisos de From.
type wireOps struct {
	From string            `json:"from"`
	Ops  []json.RawMessage `json:"ops"`
}

// maxOpsRequestBytes acota el JSON de una tanda de avisos.
const maxOpsRequestBytes = protocol.MaxOpsPerRequest*(protocol.MaxIDLen+protocol.MaxReactionBytes+96) + 256

// DeleteForEveryone elimina para todos un mensaje propio (de texto, o una
// oferta de archivos que ya terminó), si no pasó protocol.DeleteWindow desde
// que se envió. Quien aún no lo recibió ya no lo recibirá; a los demás se les
// avisa para que lo eliminen. Los archivos ya descargados se quedan en la
// carpeta de quien los recibió.
func (s *Service) DeleteForEveryone(ctx context.Context, msgID string) (store.Message, error) {
	m, ok, err := s.store.Message(ctx, msgID)
	switch {
	case err != nil:
		return store.Message{}, err
	case !ok:
		return store.Message{}, errors.New("mensaje inexistente")
	case !m.Outgoing:
		return store.Message{}, errors.New("solo puedes eliminar para todos tus propios mensajes")
	case m.Deleted:
		return m, nil
	case m.Kind != store.KindText && m.Kind != store.KindFiles:
		return store.Message{}, errors.New("este mensaje no se puede eliminar")
	case time.Since(m.SentAt) > protocol.DeleteWindow:
		return store.Message{}, fmt.Errorf("ya pasaron más de %d minutos desde que lo enviaste",
			int(protocol.DeleteWindow.Minutes()))
	}
	if err := s.checkFilesDeletable(ctx, m); err != nil {
		return store.Message{}, err
	}
	// Antes de eliminar: después ya no se sabe a quién se le iba a entregar.
	recipients := []string{m.PeerID}
	if m.RoomID != "" {
		if recipients, err = s.store.RoomRecipients(ctx, msgID); err != nil {
			return store.Message{}, err
		}
	}
	deleted, err := s.deleteMessage(ctx, m)
	if err != nil {
		return store.Message{}, err
	}
	return deleted, s.enqueue(ctx, recipients, wireOp{Type: protocol.OpDelete, Msg: msgID})
}

// checkFilesDeletable dice si una oferta de archivos puede eliminarse para
// todos (los demás mensajes, sí). Quien la envió, cuando ya terminó:
// descargada, cancelada, rechazada o caducada. Quien la recibió acepta el
// aviso en cualquier estado; lo ya descargado se queda en su carpeta.
func (s *Service) checkFilesDeletable(ctx context.Context, m store.Message) error {
	if m.Kind != store.KindFiles || !m.Outgoing {
		return nil
	}
	t, ok, err := s.store.Transfer(ctx, m.ID)
	if err != nil || !ok || t.State.Final() {
		return err
	}
	return errors.New("cancela el envío antes de eliminarlo para todos")
}

// deleteMessage deja el mensaje como eliminado y avisa a las ventanas. De una
// oferta de archivos, antes detiene la transferencia y limpia lo que dejó en
// disco.
func (s *Service) deleteMessage(ctx context.Context, m store.Message) (store.Message, error) {
	if m.Kind == store.KindFiles && s.discardFiles != nil {
		s.discardFiles(ctx, m.ID)
	}
	deleted, _, err := s.store.DeleteMessage(ctx, m.ID)
	if err != nil {
		return store.Message{}, err
	}
	s.emit(Event{Type: MessageDeleted, Message: deleted})
	return deleted, nil
}

// React pone la reacción de este equipo a un mensaje (una por mensaje; emoji
// vacío la quita) y avisa a los demás participantes de la conversación.
func (s *Service) React(ctx context.Context, msgID, emoji string) (store.Message, error) {
	if emoji != "" {
		if err := protocol.ValidateReaction(emoji); err != nil {
			return store.Message{}, err
		}
	}
	m, ok, err := s.store.Message(ctx, msgID)
	switch {
	case err != nil:
		return store.Message{}, err
	case !ok:
		return store.Message{}, errors.New("mensaje inexistente")
	case m.Deleted || m.Kind == store.KindRoomEvent:
		return store.Message{}, errors.New("a este mensaje no se puede reaccionar")
	}
	recipients := []string{m.PeerID}
	if m.RoomID != "" {
		room, ok, err := s.store.Room(ctx, m.RoomID)
		switch {
		case err != nil:
			return store.Message{}, err
		case !ok || room.Left:
			return store.Message{}, errors.New("ya no estás en esta sala")
		}
		recipients = slices.DeleteFunc(slices.Clone(room.Members), func(id string) bool { return id == s.self.ID })
	}
	now := time.Now()
	changed, err := s.store.SetReaction(ctx, msgID, "", emoji, now)
	if err != nil || !changed {
		return m, err
	}
	if m, _, err = s.store.Message(ctx, msgID); err != nil {
		return store.Message{}, err
	}
	s.emit(Event{Type: MessageReaction, Message: m})
	return m, s.enqueue(ctx, recipients, wireOp{Type: protocol.OpReact, Msg: msgID, Emoji: emoji, At: now.UnixMilli()})
}

// enqueue deja el aviso pendiente para cada destinatario e intenta entregarlo.
func (s *Service) enqueue(ctx context.Context, recipients []string, op wireOp) error {
	if op.At == 0 {
		op.At = time.Now().UnixMilli()
	}
	payload, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if err := s.store.EnqueueOps(ctx, recipients, string(payload)); err != nil {
		return err
	}
	for _, p := range recipients {
		s.Flush(p)
	}
	return nil
}

// sendOps entrega a p los avisos pendientes, en tandas; false si hay que
// reintentar más tarde. Si p tiene una versión que no conoce los avisos
// (404) o los rechaza, se descartan: reintentar no serviría.
func (s *Service) sendOps(p discovery.Peer) bool {
	for {
		ops, err := s.store.PendingOps(s.ctx, p.ID, protocol.MaxOpsPerRequest)
		if err != nil {
			s.log.Error("leyendo avisos pendientes", "peer", p.ID, "err", err)
			return false
		}
		if len(ops) == 0 {
			return true
		}
		req := wireOps{From: s.self.ID, Ops: make([]json.RawMessage, len(ops))}
		for i, op := range ops {
			req.Ops[i] = json.RawMessage(op.Payload)
		}
		status, err := s.post(p, protocol.RouteOps, req)
		if err != nil || status >= http.StatusInternalServerError {
			s.log.Debug("entrega de avisos fallida", "peer", p.ID, "status", status, "err", err)
			return false
		}
		if status != http.StatusNoContent {
			s.log.Warn("avisos descartados: el otro equipo no los acepta (¿versión anterior?)",
				"peer", p.ID, "status", status, "avisos", len(ops))
		}
		if err := s.store.DeleteOps(s.ctx, ops); err != nil {
			s.log.Error("quitando avisos entregados", "err", err)
			return false
		}
	}
}

func (s *Service) handleOps(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxOpsRequestBytes)
	var req struct {
		From string   `json:"from"`
		Ops  []wireOp `json:"ops"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}
	if err := protocol.ValidateID(req.From); err != nil || len(req.Ops) == 0 || len(req.Ops) > protocol.MaxOpsPerRequest {
		http.Error(w, "avisos inválidos", http.StatusBadRequest)
		return
	}
	if _, ok := s.checkSender(w, r, req.From); !ok {
		return
	}
	// Un aviso que no aplica (mensaje desconocido, sin permiso, fuera de
	// plazo) se ignora: responder un error haría que el remitente reintente
	// algo que nunca va a servir.
	for _, op := range req.Ops {
		if err := s.applyOp(r.Context(), req.From, op); err != nil {
			s.log.Debug("aviso ignorado", "from", req.From, "tipo", op.Type, "msg", op.Msg, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// applyOp aplica un aviso de from, cuya identidad ya se verificó por TLS.
func (s *Service) applyOp(ctx context.Context, from string, op wireOp) error {
	if op.Type != protocol.OpDelete && op.Type != protocol.OpReact {
		return nil // de una versión futura
	}
	if err := protocol.ValidateID(op.Msg); err != nil {
		return err
	}
	m, ok, err := s.store.Message(ctx, op.Msg)
	switch {
	case err != nil:
		return err
	case !ok:
		return errors.New("mensaje desconocido")
	case m.Deleted:
		return nil
	}

	if op.Type == protocol.OpDelete {
		switch {
		case m.Outgoing || m.PeerID != from:
			return errors.New("solo el autor puede eliminar su mensaje")
		case m.Kind != store.KindText && m.Kind != store.KindFiles:
			return errors.New("solo se eliminan mensajes de texto y ofertas de archivos")
		case op.At-m.SentAt.UnixMilli() > protocol.DeleteWindow.Milliseconds():
			return errors.New("fuera de plazo")
		}
		if err := s.checkFilesDeletable(ctx, m); err != nil {
			return err
		}
		_, err := s.deleteMessage(ctx, m)
		return err
	}

	if op.Emoji != "" {
		if err := protocol.ValidateReaction(op.Emoji); err != nil {
			return err
		}
	}
	if m.Kind == store.KindRoomEvent {
		return errors.New("a un aviso de sala no se reacciona")
	}
	// Solo reacciona quien participa en la conversación.
	if m.RoomID == "" {
		if m.PeerID != from {
			return errors.New("el mensaje no es de una conversación con el remitente")
		}
	} else {
		room, ok, err := s.store.Room(ctx, m.RoomID)
		if err != nil {
			return err
		}
		if !ok || !slices.Contains(room.Members, from) {
			return errors.New("el remitente no es miembro de la sala")
		}
	}
	changed, err := s.store.SetReaction(ctx, m.ID, from, op.Emoji, time.UnixMilli(op.At))
	if err != nil || !changed {
		return err
	}
	if m, _, err = s.store.Message(ctx, m.ID); err != nil {
		return err
	}
	s.emit(Event{Type: MessageReaction, Message: m})
	return nil
}
