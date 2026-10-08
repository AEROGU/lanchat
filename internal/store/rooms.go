package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Room es una sala de chat grupal. No hay servidor: cada miembro guarda su
// copia y los cambios viajan con los mensajes; gana la de mayor Version.
type Room struct {
	ID      string
	Name    string
	Members []string // IDs de los equipos, incluido este
	Version int64
	// Left: este equipo salió de la sala (se conserva el historial).
	Left      bool
	UpdatedAt time.Time
}

const roomColumns = `id, name, members, version, left_room, updated_at`

func scanRoom(sc scanner) (Room, error) {
	var r Room
	var members string
	var updated int64
	if err := sc.Scan(&r.ID, &r.Name, &members, &r.Version, &r.Left, &updated); err != nil {
		return r, err
	}
	r.UpdatedAt = time.UnixMilli(updated)
	return r, json.Unmarshal([]byte(members), &r.Members)
}

func (s *Store) Room(ctx context.Context, id string) (Room, bool, error) {
	r, err := scanRoom(s.db.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM rooms WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, false, nil
	}
	return r, err == nil, err
}

func (s *Store) Rooms(ctx context.Context) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+roomColumns+` FROM rooms ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		r, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveRoom guarda la sala tal cual (cambios hechos en este equipo).
func (s *Store) SaveRoom(ctx context.Context, r Room) error {
	members, err := json.Marshal(r.Members)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO rooms (`+roomColumns+`) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, members = excluded.members,
			version = excluded.version, left_room = excluded.left_room, updated_at = excluded.updated_at`,
		r.ID, r.Name, string(members), r.Version, r.Left, time.Now().UnixMilli())
	return err
}

// ApplyRoom guarda una versión de la sala recibida de otro equipo solo si es
// nueva o más reciente que la local; applied indica si se guardó.
func (s *Store) ApplyRoom(ctx context.Context, r Room) (applied bool, err error) {
	members, err := json.Marshal(r.Members)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO rooms (`+roomColumns+`) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, members = excluded.members,
			version = excluded.version, left_room = excluded.left_room, updated_at = excluded.updated_at
		WHERE excluded.version > rooms.version`,
		r.ID, r.Name, string(members), r.Version, r.Left, time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// InsertRoomMessage guarda un mensaje saliente de sala y deja pendiente su
// entrega a cada destinatario.
func (s *Store) InsertRoomMessage(ctx context.Context, m Message, recipients []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (`+messageColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.PeerID, m.Outgoing, m.Body, m.At.UnixMilli(), m.SentAt.UnixMilli(), m.Status, m.Unread, m.Kind,
		m.Broadcast, m.RoomID); err != nil {
		return err
	}
	for _, p := range recipients {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO room_deliveries (message_id, peer_id) VALUES (?, ?)`, m.ID, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PendingRoomDeliveries devuelve los mensajes de sala que faltan por entregar
// a peerID, del más antiguo al más nuevo.
func (s *Store) PendingRoomDeliveries(ctx context.Context, peerID string) ([]Message, error) {
	return s.queryMessages(ctx, `SELECT `+messageSelect+` FROM messages
		WHERE id IN (SELECT message_id FROM room_deliveries WHERE peer_id = ? AND delivered = 0)
		ORDER BY at, rowid`, peerID)
}

// MarkRoomDelivered anota que peerID recibió el mensaje; si ya lo recibieron
// todos, el mensaje queda entregado (allDone).
func (s *Store) MarkRoomDelivered(ctx context.Context, msgID, peerID string) (allDone bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE room_deliveries SET delivered = 1 WHERE message_id = ? AND peer_id = ?`, msgID, peerID); err != nil {
		return false, err
	}
	var left int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM room_deliveries WHERE message_id = ? AND delivered = 0`, msgID).Scan(&left); err != nil {
		return false, err
	}
	if left == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET status = ? WHERE id = ?`, StatusDelivered, msgID); err != nil {
			return false, err
		}
	}
	return left == 0, tx.Commit()
}

// RoomUnreadCounts devuelve cuántos mensajes sin leer hay por sala.
func (s *Store) RoomUnreadCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT room_id, COUNT(*) FROM messages WHERE unread = 1 AND room_id != '' GROUP BY room_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// MarkRoomRead marca como leídos los mensajes de la sala.
func (s *Store) MarkRoomRead(ctx context.Context, roomID string) (changed bool, err error) {
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET unread = 0 WHERE room_id = ? AND unread = 1`, roomID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
