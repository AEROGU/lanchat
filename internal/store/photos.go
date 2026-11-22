package store

import (
	"context"
	"database/sql"
	"errors"
)

// Fotos de perfil: la propia (con el ID de este equipo) y las descargadas de
// los contactos. Se guardan en la base para que se borren con ella.

// Photo devuelve la foto de perfil guardada de peerID y su identificador
// (protocol.PhotoID); ok es false si no tiene.
func (s *Store) Photo(ctx context.Context, peerID string) (id string, data []byte, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT photo_id, data FROM photos WHERE peer_id = ?`, peerID).Scan(&id, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	return id, data, err == nil, err
}

// PhotoIDs devuelve el identificador de la foto guardada de cada equipo que tiene una.
func (s *Store) PhotoIDs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT peer_id, photo_id FROM photos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var peer, id string
		if err := rows.Scan(&peer, &id); err != nil {
			return nil, err
		}
		out[peer] = id
	}
	return out, rows.Err()
}

// SetPhoto guarda (o reemplaza) la foto de perfil de peerID.
func (s *Store) SetPhoto(ctx context.Context, peerID, id string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO photos (peer_id, photo_id, data) VALUES (?, ?, ?)
		ON CONFLICT(peer_id) DO UPDATE SET photo_id = excluded.photo_id, data = excluded.data`, peerID, id, data)
	return err
}

// DeletePhoto borra la foto de perfil de peerID (sin error si no tenía).
func (s *Store) DeletePhoto(ctx context.Context, peerID string) error {
	return s.purge(ctx, stmt{`DELETE FROM photos WHERE peer_id = ?`, []any{peerID}})
}
