package store

import (
	"cmp"
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
	return s.purge(ctx, deleteConversation(peerID)...)
}

func deleteConversation(peerID string) []stmt {
	return []stmt{
		{`DELETE FROM transfer_files WHERE transfer_id IN (SELECT id FROM transfers WHERE peer_id = ?)`, []any{peerID}},
		{`DELETE FROM transfers WHERE peer_id = ?`, []any{peerID}},
		{`DELETE FROM messages WHERE peer_id = ? AND room_id = ''`, []any{peerID}},
		{`DELETE FROM reactions WHERE message_id NOT IN (SELECT id FROM messages)`, nil},
	}
}

// DeleteTransfer borra de este equipo una oferta de archivos de una
// conversación 1 a 1: su mensaje, sus archivos (con las miniaturas) y sus
// reacciones. No mira el estado: quien llama decide si se puede borrar.
func (s *Store) DeleteTransfer(ctx context.Context, id string) error {
	if _, ok, err := s.Transfer(ctx, id); err != nil || !ok {
		return cmp.Or(err, ErrNotFound)
	}
	return s.purge(ctx,
		stmt{`DELETE FROM transfer_files WHERE transfer_id = ?`, []any{id}},
		stmt{`DELETE FROM transfers WHERE id = ?`, []any{id}},
		stmt{`DELETE FROM messages WHERE id = ? AND room_id = ''`, []any{id}},
		stmt{`DELETE FROM reactions WHERE message_id = ?`, []any{id}},
	)
}

// RemovePeer quita al contacto de la lista. Su conversación pasa a la de into
// (otra identidad de la misma persona, p. ej. si reinstaló LanChat), que
// también hereda su alias y grupo si no tenía; con into vacío se borra. Los
// avisos pendientes para él se descartan. Las transferencias en curso deben
// cancelarse antes.
//
// Cada mensaje combinado guarda en MergedFrom quién era el contacto quitado
// (su nombre anunciado, no el alias), para que no pase por escrito por into.
// Uno que ya venía de otra combinación conserva su origen.
func (s *Store) RemovePeer(ctx context.Context, id, into string) error {
	stmts := []stmt{{`DELETE FROM outbox WHERE peer_id = ?`, []any{id}}}
	if into == "" {
		stmts = append(stmts, deleteConversation(id)...)
	} else {
		p, ok, err := s.Peer(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return peerNotFound(id, ErrNotFound)
		}
		stmts = append(stmts,
			// Los acuses de lectura pendientes eran para la identidad anterior.
			stmt{`UPDATE messages SET peer_id = ?, receipt = 0,
				merged_from = CASE WHEN merged_from = '' THEN ? ELSE merged_from END
				WHERE peer_id = ? AND room_id = ''`, []any{into, p.Describe(), id}},
			stmt{`UPDATE transfers SET peer_id = ? WHERE peer_id = ?`, []any{into, id}},
			stmt{`UPDATE OR IGNORE reactions SET peer_id = ? WHERE peer_id = ?
				AND message_id IN (SELECT id FROM messages WHERE peer_id = ? AND room_id = '')`, []any{into, id, into}},
			stmt{`UPDATE peers SET alias = (SELECT alias FROM peers WHERE id = ?) WHERE id = ? AND alias = ''`, []any{id, into}},
			stmt{`UPDATE peers SET group_name = (SELECT group_name FROM peers WHERE id = ?) WHERE id = ? AND group_name = ''`, []any{id, into}},
		)
	}
	stmts = append(stmts, stmt{`UPDATE peers SET removed = 1 WHERE id = ?`, []any{id}})
	return s.purge(ctx, stmts...)
}

// keptRoomMessages son los avisos de sala propios que faltan por entregar
// (p. ej. "Ana salió de la sala"): se conservan al borrar para que los demás
// sepan que este equipo salió.
var keptRoomMessages = fmt.Sprintf(`SELECT message_id FROM room_deliveries WHERE delivered = 0
	AND message_id IN (SELECT id FROM messages WHERE outgoing = 1 AND kind = %d)`, KindRoomEvent)

// forgetHiddenRooms borra el nombre y los miembros de las salas ocultas que ya
// no tienen avisos por entregar; queda solo su ID para ignorar sus mensajes.
const forgetHiddenRooms = `UPDATE rooms SET name = '', members = '[]', admins = '[]'
	WHERE hidden = 1 AND id NOT IN (SELECT room_id FROM messages)`

// DeleteRoomConversation borra los mensajes de la sala (también los que
// faltaba entregar, salvo los avisos). Con hide la sala deja de listarse,
// si este equipo ya salió de ella.
func (s *Store) DeleteRoomConversation(ctx context.Context, roomID string, hide bool) error {
	stmts := []stmt{
		{`DELETE FROM room_deliveries WHERE message_id IN (SELECT id FROM messages WHERE room_id = ?)
			AND message_id NOT IN (` + keptRoomMessages + `)`, []any{roomID}},
		{`DELETE FROM messages WHERE room_id = ? AND id NOT IN (` + keptRoomMessages + `)`, []any{roomID}},
		{`DELETE FROM reactions WHERE message_id NOT IN (SELECT id FROM messages)`, nil},
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
		stmt{`DELETE FROM reactions`, nil},
		// De los avisos solo quedan los de "borrar para todos": lo que el
		// usuario ya eliminó debe eliminarse también en los demás equipos.
		stmt{`DELETE FROM outbox WHERE payload NOT LIKE '%"type":"delete"%'`, nil},
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

// ReceivedFiles devuelve las rutas donde quedaron guardados los archivos
// recibidos que la base aún recuerda (para borrarlos al desinstalar).
func (s *Store) ReceivedFiles(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.path FROM transfer_files f
		JOIN transfers t ON t.id = f.transfer_id
		WHERE t.outgoing = 0 AND f.done = 1 AND f.path != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
