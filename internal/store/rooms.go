package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Room es una sala de chat grupal. No hay servidor: cada miembro guarda su
// copia y los cambios viajan con los mensajes; gana la de mayor Version.
type Room struct {
	ID      string
	Name    string
	Members []string // IDs de los equipos, incluido este
	// Admins son los administradores elegidos; ver EffectiveAdmins.
	Admins  []string
	Version int64
	// Left: este equipo salió de la sala (se conserva el historial).
	Left bool
	// Hidden: el usuario borró la sala tras salir; no se lista.
	Hidden    bool
	UpdatedAt time.Time
}

const roomColumns = `id, name, members, version, left_room, hidden, updated_at, admins`

func scanRoom(sc scanner) (Room, error) {
	var r Room
	var members, admins string
	var updated int64
	if err := sc.Scan(&r.ID, &r.Name, &members, &r.Version, &r.Left, &r.Hidden, &updated, &admins); err != nil {
		return r, err
	}
	r.UpdatedAt = time.UnixMilli(updated)
	return r, errors.Join(json.Unmarshal([]byte(members), &r.Members), json.Unmarshal([]byte(admins), &r.Admins))
}

// EffectiveAdmins devuelve quiénes administran la sala: los de Admins que
// siguen siendo miembros o, si no queda ninguno (salas de versiones
// anteriores, o salieron todos), el miembro más antiguo. Todos los equipos
// llegan al mismo resultado porque comparten la lista de miembros en orden.
func (r Room) EffectiveAdmins() []string {
	var out []string
	for _, m := range r.Members {
		if slices.Contains(r.Admins, m) {
			out = append(out, m)
		}
	}
	if len(out) == 0 && len(r.Members) > 0 {
		out = []string{r.Members[0]}
	}
	return out
}

// IsAdmin indica si id administra la sala.
func (r Room) IsAdmin(id string) bool { return slices.Contains(r.EffectiveAdmins(), id) }

// roomJSON codifica los miembros y los administradores para guardarlos.
func roomJSON(r Room) (members, admins string, err error) {
	m, err := json.Marshal(r.Members)
	if err != nil {
		return "", "", err
	}
	a, err := json.Marshal(append([]string{}, r.Admins...)) // nunca "null"
	return string(m), string(a), err
}

func (s *Store) Room(ctx context.Context, id string) (Room, bool, error) {
	r, err := scanRoom(s.db.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM rooms WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, false, nil
	}
	return r, err == nil, err
}

// Rooms devuelve las salas visibles (no las borradas).
func (s *Store) Rooms(ctx context.Context) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+roomColumns+` FROM rooms WHERE hidden = 0 ORDER BY name, id`)
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
	members, admins, err := roomJSON(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO rooms (`+roomColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, members = excluded.members,
			version = excluded.version, left_room = excluded.left_room, hidden = excluded.hidden,
			updated_at = excluded.updated_at, admins = excluded.admins`,
		r.ID, r.Name, members, r.Version, r.Left, r.Hidden, time.Now().UnixMilli(), admins)
	return err
}

// ApplyRoom guarda una versión de la sala recibida de otro equipo solo si es
// nueva o más reciente que la local; applied indica si se guardó. Como la
// versión recibida incluye a este equipo, la sala vuelve a ser visible.
func (s *Store) ApplyRoom(ctx context.Context, r Room) (applied bool, err error) {
	members, admins, err := roomJSON(r)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO rooms (`+roomColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, members = excluded.members,
			version = excluded.version, left_room = excluded.left_room, hidden = excluded.hidden,
			updated_at = excluded.updated_at, admins = excluded.admins
		WHERE excluded.version > rooms.version`,
		r.ID, r.Name, members, r.Version, r.Left, r.Hidden, time.Now().UnixMilli(), admins)
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
		`INSERT INTO messages (`+messageColumns+`) VALUES (`+messagePlaceholders+`)`,
		messageArgs(m)...); err != nil {
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
		// El aviso de salida de una sala borrada (oculta o ya inexistente)
		// solo esperaba a entregarse.
		hidden := `SELECT id FROM messages WHERE room_id != '' AND room_id NOT IN (SELECT id FROM rooms WHERE hidden = 0)`
		for _, q := range []string{
			`DELETE FROM room_deliveries WHERE message_id = ? AND message_id IN (` + hidden + `)`,
			`DELETE FROM messages WHERE id = ? AND id IN (` + hidden + `)`,
		} {
			if _, err := tx.ExecContext(ctx, q, msgID); err != nil {
				return false, err
			}
		}
		if _, err := tx.ExecContext(ctx, forgetHiddenRooms); err != nil {
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
