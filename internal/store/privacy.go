package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// Privacidad: copia de los datos y borrado. La base se abre con
// secure_delete, así que SQLite sobrescribe con ceros lo que se borra; luego
// se vacía el WAL para que no queden copias de las páginas viejas.

// Backup escribe en path una copia consistente de la base (que no debe existir).
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return errors.New("el archivo de destino ya existe")
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

// DeleteConversation borra la conversación 1 a 1 con peerID: mensajes y
// ofertas de archivos. Conserva el contacto (alias, grupo, huella). Las
// transferencias en curso deben cancelarse antes.
func (s *Store) DeleteConversation(ctx context.Context, peerID string) error {
	return s.purge(ctx,
		stmt{`DELETE FROM transfer_files WHERE transfer_id IN (SELECT id FROM transfers WHERE peer_id = ?)`, []any{peerID}},
		stmt{`DELETE FROM transfers WHERE peer_id = ?`, []any{peerID}},
		stmt{`DELETE FROM messages WHERE peer_id = ? AND room_id = ''`, []any{peerID}},
	)
}

// keptRoomMessages son los avisos de sala propios que faltan por entregar
// (p. ej. "Ana salió de la sala"): se conservan al borrar para que los demás
// sepan que este equipo salió.
var keptRoomMessages = fmt.Sprintf(`SELECT message_id FROM room_deliveries WHERE delivered = 0
	AND message_id IN (SELECT id FROM messages WHERE outgoing = 1 AND kind = %d)`, KindRoomEvent)

// forgetHiddenRooms borra el nombre y los miembros de las salas ocultas que ya
// no tienen avisos por entregar; queda solo su ID para ignorar sus mensajes.
const forgetHiddenRooms = `UPDATE rooms SET name = '', members = '[]'
	WHERE hidden = 1 AND id NOT IN (SELECT room_id FROM messages)`

// DeleteRoomConversation borra los mensajes de la sala (también los que
// faltaba entregar, salvo los avisos). Con hide la sala deja de listarse,
// si este equipo ya salió de ella.
func (s *Store) DeleteRoomConversation(ctx context.Context, roomID string, hide bool) error {
	stmts := []stmt{
		{`DELETE FROM room_deliveries WHERE message_id IN (SELECT id FROM messages WHERE room_id = ?)
			AND message_id NOT IN (` + keptRoomMessages + `)`, []any{roomID}},
		{`DELETE FROM messages WHERE room_id = ? AND id NOT IN (` + keptRoomMessages + `)`, []any{roomID}},
	}
	if hide {
		stmts = append(stmts,
			stmt{`UPDATE rooms SET hidden = 1 WHERE id = ? AND left_room = 1`, []any{roomID}},
			stmt{forgetHiddenRooms, nil})
	}
	return s.purge(ctx, stmts...)
}

// Wipe borra todos los datos del usuario: mensajes, salas, transferencias y
// los alias y grupos que puso a sus contactos. Conserva el directorio de
// equipos con sus huellas (no son datos del usuario y sin ellas se perdería
// la protección contra suplantaciones). Antes hay que salir de las salas y
// cancelar las transferencias en curso: de las salas quedan solo los avisos
// de salida por entregar y lo mínimo para ignorar sus mensajes.
func (s *Store) Wipe(ctx context.Context) error {
	err := s.purge(ctx,
		stmt{`DELETE FROM room_deliveries WHERE message_id NOT IN (` + keptRoomMessages + `)`, nil},
		stmt{`DELETE FROM messages WHERE id NOT IN (` + keptRoomMessages + `)`, nil},
		stmt{`UPDATE rooms SET hidden = 1 WHERE left_room = 1`, nil},
		stmt{`DELETE FROM rooms WHERE left_room = 0`, nil},
		stmt{forgetHiddenRooms, nil},
		stmt{`DELETE FROM transfer_files`, nil},
		stmt{`DELETE FROM transfers`, nil},
		stmt{`UPDATE peers SET alias = '', group_name = ''`, nil},
	)
	if err != nil {
		return err
	}
	// VACUUM reescribe el archivo: no quedan páginas libres con datos viejos.
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return err
	}
	return s.truncateWAL(ctx)
}

type stmt struct {
	query string
	args  []any
}

// purge ejecuta los borrados en una transacción y vacía el WAL.
func (s *Store) purge(ctx context.Context, stmts ...stmt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.query, st.args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.truncateWAL(ctx)
}

// truncateWAL pasa el WAL a la base y lo deja vacío.
func (s *Store) truncateWAL(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed)
	if err == nil && busy != 0 {
		err = errors.New("no se pudo vaciar el registro de la base (está ocupada)")
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return err
}
