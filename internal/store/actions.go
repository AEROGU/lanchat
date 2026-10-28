package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Acciones sobre mensajes ya enviados: borrarlos para todos, editarlos y
// reaccionar. Los
// avisos a los demás equipos esperan en la tabla outbox hasta entregarse.

// Reaction es la reacción de un equipo a un mensaje.
type Reaction struct {
	// PeerID es quien reaccionó; "" si fue este equipo.
	PeerID string
	Emoji  string
	At     time.Time
}

// Message busca un mensaje por ID, con sus reacciones.
func (s *Store) Message(ctx context.Context, id string) (Message, bool, error) {
	msgs, err := s.queryMessages(ctx, `SELECT `+messageSelect+` FROM messages WHERE id = ?`, id)
	if err != nil || len(msgs) == 0 {
		return Message{}, false, err
	}
	if err := s.loadReactions(ctx, msgs); err != nil {
		return Message{}, false, err
	}
	return msgs[0], true, nil
}

// DeleteMessage deja el mensaje como "eliminado": sin texto, sin reacciones y
// sin entregas pendientes (quien aún no lo recibió ya no lo recibirá). El
// renglón se conserva para mostrar que ahí hubo un mensaje. De una oferta de
// archivos se borran la transferencia y sus miniaturas (los archivos en curso
// o a medias deben descartarse antes). wasUnread indica si contaba como no
// leído.
func (s *Store) DeleteMessage(ctx context.Context, id string) (m Message, wasUnread bool, err error) {
	old, ok, err := s.Message(ctx, id)
	if err != nil {
		return Message{}, false, err
	}
	if !ok {
		return Message{}, false, ErrNotFound
	}
	err = s.purge(ctx,
		stmt{`UPDATE messages SET body = '', deleted = 1, unread = 0, receipt = 0, status = ? WHERE id = ?`,
			[]any{StatusDelivered, id}},
		stmt{`DELETE FROM reactions WHERE message_id = ?`, []any{id}},
		stmt{`DELETE FROM room_deliveries WHERE message_id = ? AND delivered = 0`, []any{id}},
		stmt{`DELETE FROM transfer_files WHERE transfer_id = ?`, []any{id}},
		stmt{`DELETE FROM transfers WHERE id = ?`, []any{id}},
	)
	if err != nil {
		return Message{}, false, err
	}
	m, _, err = s.Message(ctx, id)
	return m, old.Unread, err
}

// EditMessage cambia el texto de un mensaje no eliminado. at es la hora de la
// edición según su autor: gana la más reciente, así una edición atrasada no
// pisa a una posterior. changed es false si no cambió nada.
func (s *Store) EditMessage(ctx context.Context, id, body string, at time.Time) (changed bool, err error) {
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET body = ?, edited_at = ?
		WHERE id = ? AND deleted = 0 AND edited_at < ?`, body, at.UnixMilli(), id, at.UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RoomRecipients devuelve a quiénes se envió (o se iba a enviar) un mensaje
// de sala propio.
func (s *Store) RoomRecipients(ctx context.Context, msgID string) ([]string, error) {
	return s.queryStrings(ctx, `SELECT peer_id FROM room_deliveries WHERE message_id = ?`, msgID)
}

// SetReaction guarda la reacción de peerID ("" = este equipo) al mensaje;
// emoji vacío la quita. Gana la más reciente según at: changed es false si ya
// había una igual o más nueva.
func (s *Store) SetReaction(ctx context.Context, msgID, peerID, emoji string, at time.Time) (changed bool, err error) {
	var res sql.Result
	if emoji == "" {
		res, err = s.db.ExecContext(ctx,
			`DELETE FROM reactions WHERE message_id = ? AND peer_id = ? AND at <= ?`, msgID, peerID, at.UnixMilli())
	} else {
		res, err = s.db.ExecContext(ctx, `
			INSERT INTO reactions (message_id, peer_id, emoji, at) VALUES (?, ?, ?, ?)
			ON CONFLICT(message_id, peer_id) DO UPDATE SET emoji = excluded.emoji, at = excluded.at
			WHERE excluded.at >= reactions.at AND excluded.emoji != reactions.emoji`,
			msgID, peerID, emoji, at.UnixMilli())
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// loadReactions completa Reactions de cada mensaje (queda vacío, no nil,
// cuando no tiene: así se distingue de "no se consultó").
func (s *Store) loadReactions(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	byID := make(map[string]*Message, len(msgs))
	ids := make([]any, len(msgs))
	for i := range msgs {
		msgs[i].Reactions = []Reaction{}
		byID[msgs[i].ID] = &msgs[i]
		ids[i] = msgs[i].ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, peer_id, emoji, at FROM reactions
		WHERE message_id IN (?`+strings.Repeat(", ?", len(ids)-1)+`) ORDER BY at, peer_id`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var r Reaction
		var at int64
		if err := rows.Scan(&id, &r.PeerID, &r.Emoji, &at); err != nil {
			return err
		}
		r.At = time.UnixMilli(at)
		if m := byID[id]; m != nil {
			m.Reactions = append(m.Reactions, r)
		}
	}
	return rows.Err()
}

// ---------- Avisos por entregar ----------

// Op es un aviso que espera a entregarse a un equipo.
type Op struct {
	ID int64
	// Payload es el aviso tal como viaja (JSON); lo arma y lo interpreta chat.
	Payload string
}

// EnqueueOps deja el mismo aviso pendiente para cada destinatario.
func (s *Store) EnqueueOps(ctx context.Context, peerIDs []string, payload string) error {
	if len(peerIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, p := range peerIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO outbox (peer_id, payload, created_at) VALUES (?, ?, ?)`, p, payload, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PendingOps devuelve hasta limit avisos por entregar a peerID, en orden.
func (s *Store) PendingOps(ctx context.Context, peerID string, limit int) ([]Op, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, payload FROM outbox WHERE peer_id = ? ORDER BY id LIMIT ?`, peerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Op
	for rows.Next() {
		var op Op
		if err := rows.Scan(&op.ID, &op.Payload); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// DeleteOps quita de la cola los avisos ya entregados (o imposibles de entregar).
func (s *Store) DeleteOps(ctx context.Context, ops []Op) error {
	if len(ops) == 0 {
		return nil
	}
	ids := make([]any, len(ops))
	for i, op := range ops {
		ids[i] = op.ID
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM outbox WHERE id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, ids...)
	return err
}

func (s *Store) queryStrings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
